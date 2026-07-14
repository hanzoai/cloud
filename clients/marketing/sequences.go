// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// sequences.go is the email drip engine. A Sequence is an ordered list of Steps,
// each with a delay (seconds after the previous step). A contact is Enrolled;
// the engine walks the steps, sending each through the ONE suppression gate
// (state.deliver → notify rail) when it comes due.
//
// DURABILITY & TIME. The schedule is a VALUE, not a place: an enrollment's
// next_run_at lives in SQLite, so it survives restarts with no in-flight job to
// lose. The embedded tasks engine (drip.go) is the durable CLOCK that wakes the
// engine on a fixed cadence to process whatever is due — the same "engine owns
// time, state owns the schedule" split clients/cron uses, no Redis, no ticker.
//
// IDEMPOTENCE. Each (enrollment, step) is CLAIMED in marketing_sends before it
// is sent; the claim is a unique-key insert. A redelivered tick, a crash between
// send and advance, or two overlapping sweeps all find the claim already taken
// and DO NOT re-send — every step is delivered at most once, then the enrollment
// advances regardless, so it can never wedge.

// Sequence lifecycle. Only an active sequence enrolls and sends.
var seqStatuses = map[string]bool{"draft": true, "active": true, "archived": true}

// Enrollment lifecycle.
const (
	enrollActive    = "active"
	enrollCompleted = "completed"
	enrollCanceled  = "canceled"
)

// Sequence is a drip campaign definition.
type Sequence struct {
	ID        string `json:"id"`
	Org       string `json:"-"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Step is one message in a sequence. DelaySeconds is measured from the previous
// step's send (from enrollment for the first step).
type Step struct {
	ID           string `json:"id"`
	Org          string `json:"-"`
	SequenceID   string `json:"sequenceId"`
	Idx          int    `json:"idx"`
	DelaySeconds int64  `json:"delaySeconds"`
	Subject      string `json:"subject"`
	Body         string `json:"body"`
	CreatedAt    int64  `json:"createdAt"`
}

// Enrollment is one contact walking one sequence.
type Enrollment struct {
	ID          string `json:"id"`
	Org         string `json:"-"`
	SequenceID  string `json:"sequenceId"`
	Address     string `json:"address"`
	Channel     string `json:"channel"`
	CurrentStep int    `json:"currentStep"`
	Status      string `json:"status"`
	NextRunAt   int64  `json:"nextRunAt"`
	EnrolledAt  int64  `json:"enrolledAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func (s *Store) migrateSequences() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS marketing_sequences (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'draft',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_marketing_sequences_org ON marketing_sequences(org, updated_at);

CREATE TABLE IF NOT EXISTS marketing_sequence_steps (
  id            TEXT PRIMARY KEY,
  org           TEXT NOT NULL,
  sequence_id   TEXT NOT NULL,
  idx           INTEGER NOT NULL,
  delay_seconds INTEGER NOT NULL DEFAULT 0,
  subject       TEXT NOT NULL DEFAULT '',
  body          TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  UNIQUE (org, sequence_id, idx)
);

CREATE TABLE IF NOT EXISTS marketing_enrollments (
  id           TEXT PRIMARY KEY,
  org          TEXT NOT NULL,
  sequence_id  TEXT NOT NULL,
  address      TEXT NOT NULL,
  channel      TEXT NOT NULL DEFAULT 'email',
  current_step INTEGER NOT NULL DEFAULT 0,
  status       TEXT NOT NULL DEFAULT 'active',
  next_run_at  INTEGER NOT NULL DEFAULT 0,
  enrolled_at  INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE (org, sequence_id, address)
);
CREATE INDEX IF NOT EXISTS ix_marketing_enroll_due ON marketing_enrollments(status, next_run_at);
CREATE INDEX IF NOT EXISTS ix_marketing_enroll_org ON marketing_enrollments(org, sequence_id);

CREATE TABLE IF NOT EXISTS marketing_sends (
  enrollment_id TEXT NOT NULL,
  step_idx      INTEGER NOT NULL,
  address       TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending',
  error         TEXT NOT NULL DEFAULT '',
  sent_at       INTEGER NOT NULL,
  PRIMARY KEY (enrollment_id, step_idx)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("marketing migrate sequences: %w", err)
	}
	return nil
}

// ---- sequence + step store ----

func (s *Store) CreateSequence(ctx context.Context, seq Sequence) (Sequence, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_sequences (id,org,name,status,created_at,updated_at) VALUES (?,?,?,?,?,?)`,
		seq.ID, seq.Org, seq.Name, seq.Status, seq.CreatedAt, seq.UpdatedAt); err != nil {
		return Sequence{}, fmt.Errorf("insert sequence: %w", err)
	}
	return seq, nil
}

func (s *Store) GetSequence(ctx context.Context, org, id string) (Sequence, error) {
	var seq Sequence
	err := s.db.QueryRowContext(ctx,
		`SELECT id,org,name,status,created_at,updated_at FROM marketing_sequences WHERE org=? AND id=?`, org, id).
		Scan(&seq.ID, &seq.Org, &seq.Name, &seq.Status, &seq.CreatedAt, &seq.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Sequence{}, errNotFound
	}
	if err != nil {
		return Sequence{}, fmt.Errorf("get sequence: %w", err)
	}
	return seq, nil
}

func (s *Store) ListSequences(ctx context.Context, org string, limit int) ([]Sequence, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,name,status,created_at,updated_at FROM marketing_sequences WHERE org=? ORDER BY updated_at DESC LIMIT ?`, org, limit)
	if err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Sequence, 0, 16)
	for rows.Next() {
		var seq Sequence
		if err := rows.Scan(&seq.ID, &seq.Org, &seq.Name, &seq.Status, &seq.CreatedAt, &seq.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, seq)
	}
	return out, rows.Err()
}

// SetSequenceStatus flips a sequence's lifecycle (e.g. draft → active).
func (s *Store) SetSequenceStatus(ctx context.Context, org, id, status string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE marketing_sequences SET status=?, updated_at=? WHERE org=? AND id=?`, status, now, org, id)
	if err != nil {
		return false, fmt.Errorf("set sequence status: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AddStep appends a step at the next index for the sequence.
func (s *Store) AddStep(ctx context.Context, st Step) (Step, error) {
	var idx int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(idx)+1,0) FROM marketing_sequence_steps WHERE org=? AND sequence_id=?`,
		st.Org, st.SequenceID).Scan(&idx)
	if err != nil {
		return Step{}, fmt.Errorf("next step idx: %w", err)
	}
	st.Idx = idx
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_sequence_steps (id,org,sequence_id,idx,delay_seconds,subject,body,created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		st.ID, st.Org, st.SequenceID, st.Idx, st.DelaySeconds, st.Subject, st.Body, st.CreatedAt); err != nil {
		return Step{}, fmt.Errorf("insert step: %w", err)
	}
	return st, nil
}

// GetStep loads the step at idx, ok=false when there is none (walked past the end).
func (s *Store) GetStep(ctx context.Context, org, seqID string, idx int) (Step, bool, error) {
	var st Step
	err := s.db.QueryRowContext(ctx,
		`SELECT id,org,sequence_id,idx,delay_seconds,subject,body,created_at FROM marketing_sequence_steps
		 WHERE org=? AND sequence_id=? AND idx=?`, org, seqID, idx).
		Scan(&st.ID, &st.Org, &st.SequenceID, &st.Idx, &st.DelaySeconds, &st.Subject, &st.Body, &st.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Step{}, false, nil
	}
	if err != nil {
		return Step{}, false, fmt.Errorf("get step: %w", err)
	}
	return st, true, nil
}

func (s *Store) ListSteps(ctx context.Context, org, seqID string) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,sequence_id,idx,delay_seconds,subject,body,created_at FROM marketing_sequence_steps
		 WHERE org=? AND sequence_id=? ORDER BY idx`, org, seqID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Step, 0, 8)
	for rows.Next() {
		var st Step
		if err := rows.Scan(&st.ID, &st.Org, &st.SequenceID, &st.Idx, &st.DelaySeconds, &st.Subject, &st.Body, &st.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ---- enrollment store ----

// Enroll inserts an enrollment. Idempotent on (org, sequence_id, address): a
// duplicate enroll returns errConflict rather than starting a second walk, so a
// contact can never be double-dripped by the same sequence.
func (s *Store) Enroll(ctx context.Context, e Enrollment) (Enrollment, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_enrollments (id,org,sequence_id,address,channel,current_step,status,next_run_at,enrolled_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT(org,sequence_id,address) DO NOTHING`,
		e.ID, e.Org, e.SequenceID, e.Address, e.Channel, e.CurrentStep, e.Status, e.NextRunAt, e.EnrolledAt, e.UpdatedAt)
	if err != nil {
		return Enrollment{}, fmt.Errorf("enroll: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Enrollment{}, errConflict
	}
	return e, nil
}

// GetEnrollment loads one org-scoped enrollment.
func (s *Store) GetEnrollment(ctx context.Context, org, id string) (Enrollment, error) {
	var e Enrollment
	err := s.db.QueryRowContext(ctx,
		`SELECT id,org,sequence_id,address,channel,current_step,status,next_run_at,enrolled_at,updated_at
		 FROM marketing_enrollments WHERE org=? AND id=?`, org, id).
		Scan(&e.ID, &e.Org, &e.SequenceID, &e.Address, &e.Channel, &e.CurrentStep, &e.Status, &e.NextRunAt, &e.EnrolledAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Enrollment{}, errNotFound
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("get enrollment: %w", err)
	}
	return e, nil
}

func (s *Store) ListEnrollments(ctx context.Context, org, seqID string, limit int) ([]Enrollment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,sequence_id,address,channel,current_step,status,next_run_at,enrolled_at,updated_at
		 FROM marketing_enrollments WHERE org=? AND sequence_id=? ORDER BY enrolled_at DESC LIMIT ?`, org, seqID, limit)
	if err != nil {
		return nil, fmt.Errorf("list enrollments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanEnrollments(rows)
}

// DueEnrollments returns active enrollments whose next step is due at or before
// now, across ALL orgs (the sweep is a platform durable task; each row still
// carries its own org, and every downstream send is org-scoped through deliver).
func (s *Store) DueEnrollments(ctx context.Context, now int64, limit int) ([]Enrollment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,sequence_id,address,channel,current_step,status,next_run_at,enrolled_at,updated_at
		 FROM marketing_enrollments WHERE status=? AND next_run_at>0 AND next_run_at<=?
		 ORDER BY next_run_at LIMIT ?`, enrollActive, now, limit)
	if err != nil {
		return nil, fmt.Errorf("due enrollments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanEnrollments(rows)
}

func scanEnrollments(rows *sql.Rows) ([]Enrollment, error) {
	out := make([]Enrollment, 0, 16)
	for rows.Next() {
		var e Enrollment
		if err := rows.Scan(&e.ID, &e.Org, &e.SequenceID, &e.Address, &e.Channel, &e.CurrentStep,
			&e.Status, &e.NextRunAt, &e.EnrolledAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AdvanceEnrollment moves an enrollment to its next step + due time.
func (s *Store) AdvanceEnrollment(ctx context.Context, id string, nextStep int, nextRunAt, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE marketing_enrollments SET current_step=?, next_run_at=?, updated_at=? WHERE id=?`,
		nextStep, nextRunAt, now, id)
	if err != nil {
		return fmt.Errorf("advance enrollment: %w", err)
	}
	return nil
}

// FinishEnrollment terminates an enrollment (completed or canceled).
func (s *Store) FinishEnrollment(ctx context.Context, id, status string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE marketing_enrollments SET status=?, next_run_at=0, updated_at=? WHERE id=?`, status, now, id)
	if err != nil {
		return fmt.Errorf("finish enrollment: %w", err)
	}
	return nil
}

// CancelEnrollment stops an org's enrollment mid-walk.
func (s *Store) CancelEnrollment(ctx context.Context, org, id string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE marketing_enrollments SET status=?, next_run_at=0, updated_at=? WHERE org=? AND id=? AND status=?`,
		enrollCanceled, now, org, id, enrollActive)
	if err != nil {
		return false, fmt.Errorf("cancel enrollment: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- idempotent send ledger ----

// ClaimStep atomically reserves (enrollment, step) for exactly one sender. Only
// the first caller gets claimed=true; every redelivery/re-sweep gets false and
// MUST NOT send. This is the idempotence key of the whole engine.
func (s *Store) ClaimStep(ctx context.Context, enrollID string, stepIdx int, address string, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_sends (enrollment_id,step_idx,address,status,sent_at)
		 VALUES (?,?,?, 'pending', ?) ON CONFLICT(enrollment_id,step_idx) DO NOTHING`,
		enrollID, stepIdx, normAddr(address), now)
	if err != nil {
		return false, fmt.Errorf("claim step: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FinishStep records a claimed step's delivery outcome.
func (s *Store) FinishStep(ctx context.Context, enrollID string, stepIdx int, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE marketing_sends SET status=?, error=? WHERE enrollment_id=? AND step_idx=?`,
		status, errMsg, enrollID, stepIdx)
	if err != nil {
		return fmt.Errorf("finish step: %w", err)
	}
	return nil
}

// ---- the drip engine ----

// processDue advances every due enrollment by ONE step: send the current step
// (claimed once, gated by suppression, delivered via the notify rail), then
// schedule the next step or complete. Returns how many enrollments it advanced.
// It is a pure function of (store, clock) — the tasks engine only supplies the
// clock — so it is unit-tested directly without any queue.
func processDue(ctx context.Context, s *cloud.Service[state], now int64, limit int) (int, error) {
	due, err := s.State.store.DueEnrollments(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	advanced := 0
	for _, e := range due {
		step, ok, err := s.State.store.GetStep(ctx, e.Org, e.SequenceID, e.CurrentStep)
		if err != nil {
			s.Log.Warn("drip: get step", "enrollment", e.ID, "err", err)
			continue
		}
		if !ok {
			// Walked past the last step — complete.
			if err := s.State.store.FinishEnrollment(ctx, e.ID, enrollCompleted, now); err != nil {
				s.Log.Warn("drip: complete (no step)", "enrollment", e.ID, "err", err)
			}
			continue
		}
		// CLAIM before send: only the first handler of this (enrollment, step)
		// delivers; a redelivery advances without re-sending.
		claimed, err := s.State.store.ClaimStep(ctx, e.ID, e.CurrentStep, e.Address, now)
		if err != nil {
			s.Log.Warn("drip: claim", "enrollment", e.ID, "step", e.CurrentStep, "err", err)
			continue
		}
		if claimed {
			body := step.Body
			if link := unsubURL(ctx, s, e.Org, e.Channel, e.Address); link != "" {
				body += "\n\n—\nUnsubscribe: " + link
			}
			sent, derr := s.State.deliver(ctx, s.KMS, e.Org, e.Channel, e.Address, step.Subject, body)
			switch {
			case derr != nil:
				_ = s.State.store.FinishStep(ctx, e.ID, e.CurrentStep, "failed", derr.Error())
				s.Log.Warn("drip: send failed", "enrollment", e.ID, "step", e.CurrentStep, "err", derr)
			case !sent:
				_ = s.State.store.FinishStep(ctx, e.ID, e.CurrentStep, "skipped", "suppressed")
			default:
				_ = s.State.store.FinishStep(ctx, e.ID, e.CurrentStep, "sent", "")
			}
		}
		// Advance regardless of send outcome so a failed/suppressed step never
		// wedges the walk (at-most-once, always-forward).
		nextIdx := e.CurrentStep + 1
		next, hasNext, err := s.State.store.GetStep(ctx, e.Org, e.SequenceID, nextIdx)
		if err != nil {
			s.Log.Warn("drip: peek next", "enrollment", e.ID, "err", err)
			continue
		}
		if hasNext {
			if err := s.State.store.AdvanceEnrollment(ctx, e.ID, nextIdx, now+next.DelaySeconds, now); err != nil {
				s.Log.Warn("drip: advance", "enrollment", e.ID, "err", err)
				continue
			}
		} else if err := s.State.store.FinishEnrollment(ctx, e.ID, enrollCompleted, now); err != nil {
			s.Log.Warn("drip: complete", "enrollment", e.ID, "err", err)
			continue
		}
		advanced++
	}
	return advanced, nil
}

// ---- handlers ----

func createSequence(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	var body Sequence
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	status := "draft"
	if body.Status != "" {
		if !seqStatuses[body.Status] {
			return zip.ErrBadRequest("status must be one of draft, active, archived")
		}
		status = body.Status
	}
	id, err := genID("seq")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	seq, err := s.State.store.CreateSequence(c.Context(), Sequence{ID: id, Org: org, Name: name, Status: status, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, seq)
}

func listSequences(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	rows, err := s.State.store.ListSequences(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getSequence(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	seq, err := s.State.store.GetSequence(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "sequence not found")
	}
	steps, err := s.State.store.ListSteps(c.Context(), org, seq.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "steps: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"sequence": seq, "steps": steps})
}

// setSequenceStatus flips draft/active/archived (activation gate for sending).
func setSequenceStatus(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if !seqStatuses[body.Status] {
		return zip.ErrBadRequest("status must be one of draft, active, archived")
	}
	updated, err := s.State.store.SetSequenceStatus(c.Context(), org, idParam(c), body.Status, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "status: %v", err)
	}
	if !updated {
		return zip.ErrNotFound("sequence not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"id": idParam(c), "status": body.Status})
}

func addStep(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	seqID := idParam(c)
	if _, err := s.State.store.GetSequence(c.Context(), org, seqID); err != nil {
		return mapErr(err, "sequence not found")
	}
	var body Step
	if err := c.Bind(&body); err != nil {
		return err
	}
	if clip(body.Body) == "" {
		return zip.ErrBadRequest("body is required")
	}
	if body.DelaySeconds < 0 {
		return zip.ErrBadRequest("delaySeconds must be >= 0")
	}
	id, err := genID("step")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	step, err := s.State.store.AddStep(c.Context(), Step{
		ID: id, Org: org, SequenceID: seqID, DelaySeconds: body.DelaySeconds,
		Subject: clip(body.Subject), Body: body.Body, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "add step: %v", err)
	}
	return c.JSON(http.StatusCreated, step)
}

func listStepsHandler(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	rows, err := s.State.store.ListSteps(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "steps: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

// enroll adds a contact to a sequence and schedules its first step. An enrollment
// is only accepted for an ACTIVE sequence (a draft sends nothing).
func enroll(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	seqID := idParam(c)
	seq, err := s.State.store.GetSequence(c.Context(), org, seqID)
	if err != nil {
		return mapErr(err, "sequence not found")
	}
	if seq.Status != "active" {
		return zip.ErrBadRequest("sequence must be active to enroll")
	}
	var body Enrollment
	if err := c.Bind(&body); err != nil {
		return err
	}
	addr := normAddr(body.Address)
	if addr == "" {
		return zip.ErrBadRequest("address is required")
	}
	channel, okCh := normChannel(body.Channel)
	if !okCh {
		return zip.ErrBadRequest("unknown channel")
	}
	first, hasFirst, err := s.State.store.GetStep(c.Context(), org, seqID, 0)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "first step: %v", err)
	}
	now := time.Now().Unix()
	status, nextRun := enrollActive, int64(0)
	if hasFirst {
		nextRun = now + first.DelaySeconds
	} else {
		status = enrollCompleted // nothing to send
	}
	id, err := genID("enr")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	e, err := s.State.store.Enroll(c.Context(), Enrollment{
		ID: id, Org: org, SequenceID: seqID, Address: addr, Channel: channel,
		CurrentStep: 0, Status: status, NextRunAt: nextRun, EnrolledAt: now, UpdatedAt: now,
	})
	if err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("address already enrolled in this sequence")
		}
		return zip.Errorf(http.StatusInternalServerError, "enroll: %v", err)
	}
	return c.JSON(http.StatusCreated, e)
}

func listEnrollments(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	rows, err := s.State.store.ListEnrollments(c.Context(), org, idParam(c), limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "enrollments: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func cancelEnrollment(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	canceled, err := s.State.store.CancelEnrollment(c.Context(), org, c.Param("eid"), time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "cancel: %v", err)
	}
	if !canceled {
		return zip.ErrNotFound("active enrollment not found")
	}
	return c.NoContent(http.StatusNoContent)
}
