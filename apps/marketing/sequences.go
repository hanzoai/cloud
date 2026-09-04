// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/internal/shorten"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
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

// Sequence is a drip campaign definition. It is also the INPUT of create, with
// ID/CreatedAt/UpdatedAt assigned by the server.
type Sequence struct {
	// ID is the server-assigned sequence id ("seq_" + 128 random bits).
	ID  string `json:"id"`
	Org string `json:"-"`
	// Name is the sequence's label. Required, trimmed, capped at 1024 bytes.
	Name string `json:"name"`
	// Status is the lifecycle: draft, active or archived. Empty means draft, and
	// ONLY an active sequence accepts enrollments.
	Status string `json:"status"`
	// CreatedAt is unix seconds when the sequence was registered, server-assigned
	// and never rewritten.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is unix seconds of the last status flip, server-assigned, and the
	// key the sequence list is ordered by (newest first). Adding a step or
	// enrolling a contact does NOT touch it — only draft/active/archived does —
	// so it tracks activation rather than activity.
	UpdatedAt int64 `json:"updatedAt"`
}

// Step is one message in a sequence. DelaySeconds is measured from the previous
// step's send (from enrollment for the first step).
type Step struct {
	// ID is the server-assigned step id ("step_" + 128 random bits).
	ID  string `json:"id"`
	Org string `json:"-"`
	// SequenceID is the sequence this step belongs to.
	SequenceID string `json:"sequenceId"`
	// Idx is the step's 0-based position, assigned by appending: a new step
	// always lands after the last one.
	Idx int `json:"idx"`
	// DelaySeconds is how long after the previous step this one sends (after
	// enrollment, for step 0).
	DelaySeconds int64 `json:"delaySeconds"`
	// Subject is the email subject line, capped at 1024 bytes.
	Subject string `json:"subject"`
	// Body is the message text. Required. The signed one-click unsubscribe link
	// is appended to it at send time.
	Body string `json:"body"`
	// CreatedAt is unix seconds, server-assigned.
	CreatedAt int64 `json:"createdAt"`
}

// Enrollment is one contact walking one sequence.
type Enrollment struct {
	// ID is the server-assigned enrollment id ("enr_" + 128 random bits).
	ID  string `json:"id"`
	Org string `json:"-"`
	// SequenceID is the sequence being walked.
	SequenceID string `json:"sequenceId"`
	// Address is the normalized (lower-cased, trimmed) recipient.
	Address string `json:"address"`
	// Channel is the delivery surface the steps go out on.
	Channel string `json:"channel"`
	// CurrentStep is the index of the step that sends next.
	CurrentStep int `json:"currentStep"`
	// Status is active, completed or canceled.
	Status string `json:"status"`
	// NextRunAt is the unix time the current step comes due; 0 once the walk has
	// ended. It IS the schedule — durable in SQLite, so it survives restarts.
	NextRunAt int64 `json:"nextRunAt"`
	// EnrolledAt is unix seconds when the contact joined the walk, and orders the
	// enrollment list (newest first).
	EnrolledAt int64 `json:"enrolledAt"`
	// UpdatedAt is unix seconds of the last move: the drip engine writes it each
	// time it advances the walk a step, completes it or cancels it. Together with
	// Status it says when the walk last did anything, which is how a stalled
	// enrollment is told from a finished one.
	UpdatedAt int64 `json:"updatedAt"`
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

// SequenceRef addresses one sequence.
type SequenceRef struct {
	// ID is the sequence id from the path, as returned by create.
	ID string `json:"id"`
}

// SequenceList is a page of sequences, most recently updated first.
type SequenceList struct {
	// Data is the page; an empty array when the org has no sequence.
	Data []Sequence `json:"data"`
}

// SequenceView is one sequence together with its ordered steps.
type SequenceView struct {
	// Sequence is the definition itself — the same record create and the list
	// return. Its status is the one that decides whether enroll is accepted.
	Sequence Sequence `json:"sequence"`
	// Steps are in send order (idx ascending); empty for a sequence with no
	// messages yet, which enrolls fine and completes immediately.
	Steps []Step `json:"steps"`
}

// SequenceStatus is a sequence's lifecycle state — the input AND the result of
// setting it, because the wire shape is the same fact either way.
type SequenceStatus struct {
	// ID is the sequence id from the path.
	ID string `json:"id"`
	// Status is draft, active or archived. Required; there is no default here,
	// unlike on create. Only an active sequence accepts enrollments.
	Status string `json:"status"`
}

// StepInput appends one message to a sequence.
type StepInput struct {
	// SequenceID is the sequence id from the path (the route's :id).
	SequenceID string `json:"id"`
	// DelaySeconds is how long after the previous step this one sends (after
	// enrollment, for the first step). Must be >= 0.
	DelaySeconds int64 `json:"delaySeconds"`
	// Subject is the email subject line, capped at 1024 bytes.
	Subject string `json:"subject"`
	// Body is the message text. Required.
	Body string `json:"body"`
}

// StepList is a sequence's steps in send order.
type StepList struct {
	// Data is every step of the sequence, idx ascending — the order they send
	// in. It is not paged: a sequence's steps are a handful, and a partial list
	// would misstate the drip. An empty array for a sequence with no messages
	// yet, which enrolls fine and completes immediately.
	Data []Step `json:"data"`
}

// EnrollmentQuery pages one sequence's enrollments.
type EnrollmentQuery struct {
	// ID is the sequence id from the path.
	ID string `json:"id"`
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// EnrollmentList is a page of enrollments, most recently enrolled first.
type EnrollmentList struct {
	// Data is the page: every contact walking this ONE sequence, in any state —
	// active, completed and canceled walks all appear, since the history of who
	// was reached is the point. An empty array when nobody has been enrolled.
	Data []Enrollment `json:"data"`
}

// EnrollmentRef addresses one enrollment within its sequence.
type EnrollmentRef struct {
	// ID is the sequence id from the path.
	ID string `json:"id"`
	// EID is the enrollment id from the path, as returned by a single-address
	// enroll.
	EID string `json:"eid"`
}

// createSequence registers a drip sequence in the caller's org. Name is
// required; status defaults to draft, and a sequence must be ACTIVE before it
// will accept enrollments. The id, createdAt and updatedAt of the input are
// ignored — the server assigns them.
//
// Example: {"name": "Trial onboarding", "status": "draft"}
func (o ops) createSequence(ctx context.Context, in *Sequence) (*Sequence, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name := shorten.Trim(in.Name, maxField)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	status := "draft"
	if in.Status != "" {
		if !seqStatuses[in.Status] {
			return nil, zip.ErrBadRequest("status must be one of draft, active, archived")
		}
		status = in.Status
	}
	id := mint.ID("seq")
	now := time.Now().Unix()
	seq, err := o.s.State.store.CreateSequence(ctx, Sequence{ID: id, Org: org, Name: name, Status: status, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return nil, mapErr(err, "")
	}
	cloud.Created(ctx)
	return &seq, nil
}

// listSequences returns the org's drip sequences, most recently updated first.
//
// Example: {"limit": 50}
func (o ops) listSequences(ctx context.Context, in *Page) (*SequenceList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListSequences(ctx, org, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &SequenceList{Data: rows}, nil
}

// getSequence returns one of the caller org's sequences together with its steps
// in send order. A sequence belonging to another org reads as not found.
//
// Example: {"id": "seq_7b3e5a1c9d024f68b0a3e7c5d9f1a248"}
func (o ops) getSequence(ctx context.Context, in *SequenceRef) (*SequenceView, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	seq, err := o.s.State.store.GetSequence(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "sequence not found")
	}
	steps, err := o.s.State.store.ListSteps(ctx, org, seq.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "steps: %v", err)
	}
	return &SequenceView{Sequence: seq, Steps: steps}, nil
}

// setSequenceStatus flips draft/active/archived — the activation gate for
// sending, since only an active sequence accepts enrollments. It does not touch
// enrollments already walking: archiving stops new ones, not in-flight ones.
//
// Example: {"id": "seq_7b3e5a1c9d024f68b0a3e7c5d9f1a248", "status": "active"}
func (o ops) setSequenceStatus(ctx context.Context, in *SequenceStatus) (*SequenceStatus, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !seqStatuses[in.Status] {
		return nil, zip.ErrBadRequest("status must be one of draft, active, archived")
	}
	id := strings.TrimSpace(in.ID)
	updated, err := o.s.State.store.SetSequenceStatus(ctx, org, id, in.Status, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "status: %v", err)
	}
	if !updated {
		return nil, zip.ErrNotFound("sequence not found")
	}
	return &SequenceStatus{ID: id, Status: in.Status}, nil
}

// addStep appends a message to the END of a sequence: the new step's idx is one
// past the last, so steps arrive in the order they are added. Body is required
// and delaySeconds must be >= 0. Adding a step does not disturb enrollments
// already walking — one that has passed this index simply never sees it.
//
// Example: {"delaySeconds": 86400, "subject": "Day 1: your first model call", "body": "Here is how to make your first request…"}
func (o ops) addStep(ctx context.Context, in *StepInput) (*Step, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	seqID := strings.TrimSpace(in.SequenceID)
	if _, err := o.s.State.store.GetSequence(ctx, org, seqID); err != nil {
		return nil, mapErr(err, "sequence not found")
	}
	if shorten.Trim(in.Body, maxField) == "" {
		return nil, zip.ErrBadRequest("body is required")
	}
	if in.DelaySeconds < 0 {
		return nil, zip.ErrBadRequest("delaySeconds must be >= 0")
	}
	id := mint.ID("step")
	step, err := o.s.State.store.AddStep(ctx, Step{
		ID: id, Org: org, SequenceID: seqID, DelaySeconds: in.DelaySeconds,
		Subject: shorten.Trim(in.Subject, maxField), Body: in.Body, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "add step: %v", err)
	}
	cloud.Created(ctx)
	return &step, nil
}

// listSteps returns one sequence's steps in send order.
//
// Example: {"id": "seq_7b3e5a1c9d024f68b0a3e7c5d9f1a248"}
func (o ops) listSteps(ctx context.Context, in *SequenceRef) (*StepList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListSteps(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "steps: %v", err)
	}
	return &StepList{Data: rows}, nil
}

// EnrollInput names WHO to enroll: exactly one of a single address or an
// AUDIENCE — the org's own IAM customers, optionally narrowed to an event cohort.
// One endpoint, one enrollment path: "announce to every model user" is an
// audience fanned into a one-step sequence, NOT a second blast engine, so every
// message it produces still walks the drip engine and the ONE send gate.
type EnrollInput struct {
	// ID is the sequence id from the path.
	ID string `json:"id"`
	// Address is a single recipient, normalized (lower-cased, trimmed) before
	// use. Give this OR audienceId, never both and never neither.
	Address string `json:"address"`
	// AudienceID fans the sequence out over a saved audience, resolved live to
	// the org's mailable customers. Email only.
	AudienceID string `json:"audienceId"`
	// Channel is the delivery surface; empty means email. An audience resolves
	// mailboxes, so an audience enroll must be email.
	Channel string `json:"channel"`
}

// EnrollResult reports the fan-out. EnrollmentID is set only when a single
// address was named, so the one-recipient caller can still address its walk
// (cancel). AlreadyEnrolled counts addresses this sequence had already taken —
// the idempotence that makes re-POSTing a partially-applied announcement safe:
// it resumes rather than double-drips anyone.
type EnrollResult struct {
	// Resolved is how many addresses the request named — 1 for an address, the
	// audience's deliverable count for an audience.
	Resolved int `json:"resolved"`
	// Enrolled is how many started a walk on this call.
	Enrolled int `json:"enrolled"`
	// AlreadyEnrolled is how many this sequence had already taken and were left
	// alone.
	AlreadyEnrolled int `json:"alreadyEnrolled"`
	// EnrollmentID names the walk, and is present ONLY for a single-address
	// enroll — a fan-out has many, and reporting one of them would be a lie.
	EnrollmentID string `json:"enrollmentId,omitempty"`
}

// recipients resolves an enroll request's WHO into addresses: the one address it
// named, or every customer its audience resolves to. The audience is loaded
// org-scoped, so a caller can only ever fan out over its OWN org's audience, and
// resolution reads its OWN org's roster.
func recipients(ctx context.Context, s *cloud.Service[state], org, channel string, in EnrollInput) ([]string, error) {
	addr := normAddr(in.Address)
	audienceID := strings.TrimSpace(in.AudienceID)
	if (addr == "") == (audienceID == "") {
		return nil, zip.ErrBadRequest("exactly one of address or audienceId is required")
	}
	if addr != "" {
		return []string{addr}, nil
	}
	// An audience resolves mailboxes; sending them down a non-email channel would
	// deliver an email address as if it were a phone number.
	if channel != "email" {
		return nil, zip.ErrBadRequest("an audience resolves email addresses; channel must be email")
	}
	a, err := s.State.store.GetAudience(ctx, org, audienceID)
	if err != nil {
		return nil, mapErr(err, "audience not found")
	}
	r, err := resolveAudience(ctx, org, a)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return r.addresses, nil
}

// enroll adds one contact or a whole audience to a sequence and schedules the
// first step for each. The sequence must be ACTIVE (a draft sends nothing), and
// the request must name exactly one of address or audienceId.
//
// Enrolling is ALL this does: the message itself is sent later by the drip
// engine, through the suppression gate, so an opted-out customer can be enrolled
// here and still never be mailed. Re-posting is safe — an address this sequence
// already took is counted in alreadyEnrolled and never double-dripped — which is
// what makes retrying a partially-applied announcement a resume rather than a
// second send.
//
// Example: {"audienceId": "aud_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "channel": "email"}
// Response: {"resolved": 412, "enrolled": 409, "alreadyEnrolled": 3}
func (o ops) enroll(ctx context.Context, in *EnrollInput) (*EnrollResult, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	seqID := strings.TrimSpace(in.ID)
	seq, err := o.s.State.store.GetSequence(ctx, org, seqID)
	if err != nil {
		return nil, mapErr(err, "sequence not found")
	}
	if seq.Status != "active" {
		return nil, zip.ErrBadRequest("sequence must be active to enroll")
	}
	channel, okCh := normChannel(in.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("unknown channel")
	}
	addrs, err := recipients(ctx, o.s, org, channel, *in)
	if err != nil {
		return nil, err
	}
	first, hasFirst, err := o.s.State.store.GetStep(ctx, org, seqID, 0)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "first step: %v", err)
	}
	now := time.Now().Unix()
	status, nextRun := enrollActive, int64(0)
	if hasFirst {
		nextRun = now + first.DelaySeconds
	} else {
		status = enrollCompleted // nothing to send
	}

	out := EnrollResult{Resolved: len(addrs)}
	for _, addr := range addrs {
		id := mint.ID("enr")
		e, err := o.s.State.store.Enroll(ctx, Enrollment{
			ID: id, Org: org, SequenceID: seqID, Address: addr, Channel: channel,
			CurrentStep: 0, Status: status, NextRunAt: nextRun, EnrolledAt: now, UpdatedAt: now,
		})
		switch {
		case errors.Is(err, errConflict):
			out.AlreadyEnrolled++
		case err != nil:
			return nil, zip.Errorf(http.StatusInternalServerError, "enroll: %v", err)
		default:
			out.Enrolled++
			out.EnrollmentID = e.ID
		}
	}
	if len(addrs) != 1 {
		out.EnrollmentID = ""
	}
	cloud.Created(ctx)
	return &out, nil
}

// listEnrollments returns who is walking one sequence, most recently enrolled
// first, with each walk's current step and next due time.
//
// Example: {"id": "seq_7b3e5a1c9d024f68b0a3e7c5d9f1a248", "limit": 100}
func (o ops) listEnrollments(ctx context.Context, in *EnrollmentQuery) (*EnrollmentList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListEnrollments(ctx, org, strings.TrimSpace(in.ID), limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "enrollments: %v", err)
	}
	return &EnrollmentList{Data: rows}, nil
}

// cancelEnrollment stops one walk mid-sequence and answers 204: no further step
// is sent, and steps already delivered are not recalled. Only an ACTIVE
// enrollment can be canceled — one already completed or canceled reads as not
// found.
//
// Example: {"id": "seq_7b3e5a1c9d024f68b0a3e7c5d9f1a248", "eid": "enr_2a8d6f0b4c1e9375a0d2f6b8c4e19f73"}
func (o ops) cancelEnrollment(ctx context.Context, in *EnrollmentRef) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	canceled, err := o.s.State.store.CancelEnrollment(ctx, org, in.EID, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "cancel: %v", err)
	}
	if !canceled {
		return nil, zip.ErrNotFound("active enrollment not found")
	}
	return nil, nil
}
