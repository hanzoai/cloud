// Package tasks is the dev edition's work queue: a durable local queue over one
// embedded SQLite file. It is a queue and nothing more — it does not orchestrate
// steps, it does not hand work to anyone else, it hands work to whoever asks.
//
// The semantics are lease and acknowledge, which is what makes a queue survive
// its workers. A task is queued until a worker LEASES it for a stated duration;
// the worker then COMPLETES it with a result or FAILS it with a reason. A worker
// that dies mid-task acknowledges nothing, its lease runs out, and the task is
// queued again — so the only way work is lost is if a worker both takes it and
// lies about finishing it.
//
// The claim is one guarded UPDATE inside a transaction: it names the state the
// task must be in and returns the row it changed. Two workers reaching for the
// same task therefore resolve to one winner and one empty answer, not to two
// workers running the same job. That property is the point of this package;
// everything else here is bookkeeping around it.
//
// The store is one table keyed (org, id). The org is the tenant boundary and it
// comes from the validated caller, never from the body, so one tenant's worker
// can never lease another tenant's work: every statement here filters on it.
package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// The five states a task can be in. It is queued until a worker takes it, leased
// while that worker holds it, and then done, failed or cancelled — the three ways
// work stops. An expired lease is NOT a fourth ending: it returns the task to the
// queue, which is why it is not a state of its own.
const (
	Queued    = "queued"
	Leased    = "leased"
	Done      = "done"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// MaxPayload bounds a task's payload and its result. A queue is a place to put
// a description of work, not the work's data; anything this large is a file and
// belongs somewhere a file belongs.
const MaxPayload = 256 << 10

const (
	defaultAttempts = 3    // tries before a failure is final, when the caller names none
	defaultLease    = 30   // seconds a lease lasts, when the caller names none
	maxLease        = 3600 // ceiling, so a typo cannot park a task for a week
	defaultLimit    = 50
	maxLimit        = 500
)

// label is what may be a task kind: a short lowercase slug. A kind is how a
// worker says which work it can do, so it is checked against a pattern rather
// than trusted — a kind that is not plainly a name matches no worker anyway.
var label = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// schema is the whole store. One table, org first, so the primary key is also
// the index the reads want. lease is the millisecond deadline of the current
// lease and is 0 when there is none.
const schema = `
CREATE TABLE IF NOT EXISTS tasks (
  org          TEXT    NOT NULL,
  id           TEXT    NOT NULL,
  kind         TEXT    NOT NULL,
  payload      TEXT    NOT NULL,
  status       TEXT    NOT NULL,
  attempts     INTEGER NOT NULL,
  max_attempts INTEGER NOT NULL,
  lease        INTEGER NOT NULL,
  result       TEXT    NOT NULL DEFAULT '',
  err          TEXT    NOT NULL DEFAULT '',
  created      INTEGER NOT NULL,
  updated      INTEGER NOT NULL,
  PRIMARY KEY (org, id)
)`

// live is the ONE definition of a task's status: the stored column, except that
// a lease past its deadline has already returned the task to the queue. The
// claim guards on it, every read projects through it, and the acknowledgements
// guard on it — so what a worker can lease and what a caller is told are the
// same fact computed once, and there is no window in which they disagree. Its
// single parameter is the current time in milliseconds.
const live = `CASE WHEN status = 'leased' AND lease <= ? THEN 'queued' ELSE status END`

// cols is the read projection, in the order scan reads it. Every statement that
// yields a task selects exactly this, so one scan serves them all.
const cols = `id, kind, payload, ` + live + ` AS status, attempts, max_attempts, lease, result, err, created, updated`

// state is tasks' own data; shared deps live in the embedded cloud.Base.
type state struct{ db *sql.DB }

// mounted is the open store, kept for Shutdown. build is its only writer.
var mounted *sql.DB

// Mount wires the queue onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "tasks", build, routes)
}

// build opens the store and creates the table. It fails closed: a queue that
// cannot reach its data must not accept work it will drop.
func build(b cloud.Base) (state, error) {
	if b.DataDir == "" {
		return state{}, errors.New("empty DataDir")
	}
	db, err := cek.Open(filepath.Join(b.DataDir, "tasks.db"))
	if err != nil {
		return state{}, err
	}
	// SQLite admits one writer at a time, and the dev edition may open this file
	// with no busy timeout at all (cek's unencrypted path is a bare sql.Open).
	// One connection turns that contention into a queue instead of an error —
	// which matters here, where several workers reach for the same row on purpose.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		_ = db.Close()
		return state{}, fmt.Errorf("create schema: %w", err)
	}
	mounted = db
	return state{db: db}, nil
}

// Shutdown closes the store. Idempotent.
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.Close()
	mounted = nil
	return err
}

// routes is the ONE registration point. Static paths precede their :param
// siblings so the first-match scan resolves them first.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/tasks")

	// The two collection-root ops name their path in full, on the app rather
	// than the group. An empty leaf on a Group renders as "/v1/tasks/" — with
	// the trailing slash — and that string is the CONTRACT: it is what the
	// OpenAPI document publishes and what a generated SDK calls. fiber matches
	// both spellings, so this never showed up as a broken request; it showed up
	// as a document describing a path nobody writes by hand.
	zip.Post[SubmitIn, Task](app, "/v1/tasks", submit(s),
		zip.WithOperationID("tasks_submit"),
		zip.WithSummary("Enqueue a task"),
		zip.WithTags("tasks"),
		zip.WithStatus(201))
	zip.Get[ListIn, ListOut](app, "/v1/tasks", list(s),
		zip.WithOperationID("tasks_list"),
		zip.WithSummary("List tasks, newest first"),
		zip.WithTags("tasks"))
	zip.Post[LeaseIn, Task](g, "/lease", lease(s),
		zip.WithOperationID("tasks_lease"),
		zip.WithSummary("Lease the next queued task"),
		zip.WithTags("tasks"))
	zip.Get[GetIn, Task](g, "/:id", get(s),
		zip.WithOperationID("tasks_get"),
		zip.WithSummary("Read one task"),
		zip.WithTags("tasks"))
	zip.Post[CompleteIn, Task](g, "/:id/complete", complete(s),
		zip.WithOperationID("tasks_complete"),
		zip.WithSummary("Complete a leased task"),
		zip.WithTags("tasks"))
	zip.Post[FailIn, Task](g, "/:id/fail", fail(s),
		zip.WithOperationID("tasks_fail"),
		zip.WithSummary("Fail a leased task, requeueing it while attempts remain"),
		zip.WithTags("tasks"))
	zip.Delete[CancelIn, Task](g, "/:id", cancel(s),
		zip.WithOperationID("tasks_cancel"),
		zip.WithSummary("Cancel a task"),
		zip.WithTags("tasks"))
}

// Task is one unit of work and everything known about it. A worker reads Status
// to know where it stands and Attempts to know how many tries are left; the rest
// is what the submitter said and what the worker answered.
type Task struct {
	// ID is the server-generated identity of the task, stable for its life.
	ID string `json:"id"`
	// Kind is the label a worker filters on when it leases, so one queue can
	// carry unrelated work without workers stepping on each other.
	Kind string `json:"kind"`
	// Payload is the description of the work: any JSON value, returned exactly
	// as submitted.
	Payload json.RawMessage `json:"payload"`
	// Status is queued, leased, done, failed or cancelled.
	Status string `json:"status"`
	// Attempts counts the leases taken so far, including the current one.
	Attempts int `json:"attempts"`
	// MaxAttempts is how many a failure may burn before it becomes final.
	MaxAttempts int `json:"maxAttempts"`
	// Lease is when the current lease runs out; absent when nothing holds it.
	Lease *time.Time `json:"lease,omitempty"`
	// Result is what the worker returned on completion.
	Result json.RawMessage `json:"result,omitempty"`
	// Error is why the last attempt failed. It survives a requeue, so the next
	// worker can see what went wrong last time.
	Error string `json:"error,omitempty"`
	// Created is when the task was enqueued.
	Created time.Time `json:"created"`
	// Updated is when it last changed state.
	Updated time.Time `json:"updated"`
}

// SubmitIn enqueues work.
type SubmitIn struct {
	// Kind labels the work so a worker can lease only what it can do.
	Kind string `json:"kind"`
	// Payload describes the work: any JSON value, up to 256 KiB.
	Payload json.RawMessage `json:"payload"`
	// MaxAttempts is how many failures the task absorbs before it is final;
	// 0 means the default of 3.
	MaxAttempts int `json:"maxAttempts"`
}

// submit enqueues a task.
func submit(s *cloud.Service[state]) zip.TypedHandler[SubmitIn, Task] {
	return func(ctx context.Context, in *SubmitIn) (*Task, error) {
		if !label.MatchString(in.Kind) {
			return nil, zip.ErrBadRequest("kind must match " + label.String())
		}
		if err := payload("payload", in.Payload); err != nil {
			return nil, err
		}
		max := in.MaxAttempts
		if max <= 0 {
			max = defaultAttempts
		}
		now := time.Now().UTC()
		t := Task{
			ID:          newID(),
			Kind:        in.Kind,
			Payload:     in.Payload,
			Status:      Queued,
			MaxAttempts: max,
			Created:     at(now.UnixMilli()),
			Updated:     at(now.UnixMilli()),
		}
		if _, err := s.State.db.ExecContext(ctx,
			`INSERT INTO tasks (org, id, kind, payload, status, attempts, max_attempts, lease, created, updated)
			 VALUES (?, ?, ?, ?, ?, 0, ?, 0, ?, ?)`,
			principal.Tenant(ctx), t.ID, t.Kind, string(in.Payload), Queued, max, now.UnixMilli(), now.UnixMilli()); err != nil {
			return nil, zip.ErrInternal("write failed")
		}
		return &t, nil
	}
}

// ListIn filters the caller's tasks.
type ListIn struct {
	// Status narrows to queued, leased, done, failed or cancelled; empty means
	// all of them.
	Status string `json:"status"`
	// Limit caps the page; 0 means the default of 50, and 500 is the ceiling.
	Limit int `json:"limit"`
}

// ListOut is one page of tasks, newest first.
type ListOut struct {
	// Tasks in this page.
	Tasks []Task `json:"tasks"`
}

// list returns the caller's tasks, newest first, because the recent ones are
// the ones anybody is looking for.
func list(s *cloud.Service[state]) zip.TypedHandler[ListIn, ListOut] {
	return func(ctx context.Context, in *ListIn) (*ListOut, error) {
		switch in.Status {
		case "", Queued, Leased, Done, Failed, Cancelled:
		default:
			return nil, zip.ErrBadRequest("status must be queued, leased, done, failed or cancelled")
		}
		limit := in.Limit
		switch {
		case limit <= 0:
			limit = defaultLimit
		case limit > maxLimit:
			limit = maxLimit
		}
		// The filter reads the PROJECTED status, so a task whose lease has run
		// out lists as queued — the same answer a worker would get by leasing it.
		// That needs the projection to exist before the filter applies, which is
		// what the subquery is for.
		rows, err := s.State.db.QueryContext(ctx,
			`SELECT * FROM (SELECT `+cols+` FROM tasks WHERE org = ?)
			  WHERE (? = '' OR status = ?) ORDER BY created DESC, id LIMIT ?`,
			now(), principal.Tenant(ctx), in.Status, in.Status, limit)
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		defer func() { _ = rows.Close() }()
		out := ListOut{Tasks: []Task{}}
		for rows.Next() {
			t, err := scan(rows)
			if err != nil {
				return nil, zip.ErrInternal("read failed")
			}
			out.Tasks = append(out.Tasks, *t)
		}
		if rows.Err() != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return &out, nil
	}
}

// GetIn addresses one task.
type GetIn struct {
	// ID of the task.
	ID string `json:"id"`
}

// get returns one task, or 404.
func get(s *cloud.Service[state]) zip.TypedHandler[GetIn, Task] {
	return func(ctx context.Context, in *GetIn) (*Task, error) {
		t, err := scan(s.State.db.QueryRowContext(ctx,
			`SELECT `+cols+` FROM tasks WHERE org = ? AND id = ?`, now(), principal.Tenant(ctx), in.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, zip.ErrNotFound("no such task")
		}
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return t, nil
	}
}

// LeaseIn asks for the next piece of work.
type LeaseIn struct {
	// Kind narrows the claim to one label; empty takes whatever is next.
	Kind string `json:"kind"`
	// Seconds the lease lasts. 0 means the default of 30, and an hour is the
	// ceiling. Set it longer than the work takes: when it runs out the task is
	// queued again and another worker may pick it up.
	Seconds int `json:"seconds"`
}

// lease claims the oldest task the caller may run and hands it over. It answers
// 204 when there is nothing to do, which is the ordinary case for a polling
// worker and therefore not an error.
//
// The claim is the single UPDATE below. Its subquery picks a candidate and its
// guard requires that candidate to still be queued at the moment the row is
// written, so a worker that loses the race changes nothing and is told the queue
// is empty — never handed a task another worker is already running.
func lease(s *cloud.Service[state]) zip.TypedHandler[LeaseIn, Task] {
	return func(ctx context.Context, in *LeaseIn) (*Task, error) {
		if in.Kind != "" && !label.MatchString(in.Kind) {
			return nil, zip.ErrBadRequest("kind must match " + label.String())
		}
		secs := in.Seconds
		switch {
		case secs <= 0:
			secs = defaultLease
		case secs > maxLease:
			secs = maxLease
		}
		ms, org := now(), principal.Tenant(ctx)
		t, found, err := transition(ctx, s.State.db,
			`UPDATE tasks SET status = ?, attempts = attempts + 1, lease = ?, updated = ?
			  WHERE org = ? AND id = (SELECT id FROM tasks
			                           WHERE org = ? AND (? = '' OR kind = ?) AND `+live+` = ?
			                           ORDER BY created, id LIMIT 1)
			  RETURNING `+cols,
			Leased, ms+int64(secs)*1000, ms, org, org, in.Kind, in.Kind, ms, Queued, ms)
		if err != nil || !found {
			return nil, err // no work is a nil Out, which answers 204
		}
		return t, nil
	}
}

// CompleteIn finishes a leased task.
type CompleteIn struct {
	// ID of the task.
	ID string `json:"id"`
	// Result is what the work produced: any JSON value, up to 256 KiB.
	Result json.RawMessage `json:"result"`
}

// complete marks a leased task done. The guard requires the lease to still be
// held, so a worker that overran its lease cannot report a result for a task
// another worker has since taken.
func complete(s *cloud.Service[state]) zip.TypedHandler[CompleteIn, Task] {
	return func(ctx context.Context, in *CompleteIn) (*Task, error) {
		if err := payload("result", in.Result); err != nil {
			return nil, err
		}
		ms, org := now(), principal.Tenant(ctx)
		return settle(ctx, s.State.db, org, in.ID,
			`UPDATE tasks SET status = ?, result = ?, err = '', lease = 0, updated = ?
			  WHERE org = ? AND id = ? AND `+live+` = ?
			  RETURNING `+cols,
			Done, string(in.Result), ms, org, in.ID, ms, Leased, ms)
	}
}

// FailIn reports that a leased task did not work.
type FailIn struct {
	// ID of the task.
	ID string `json:"id"`
	// Error says what went wrong. It is kept on the task through a requeue, so
	// the next worker can see why the last one gave up.
	Error string `json:"error"`
}

// fail records a failure. The task returns to the queue while attempts remain
// and is final once they are spent — the retry decision is in the statement,
// so it is made from the row's own count rather than from anything a caller
// could have got wrong.
func fail(s *cloud.Service[state]) zip.TypedHandler[FailIn, Task] {
	return func(ctx context.Context, in *FailIn) (*Task, error) {
		if in.Error == "" {
			return nil, zip.ErrBadRequest("error is required: a failure with no reason cannot be acted on")
		}
		if len(in.Error) > MaxPayload {
			return nil, zip.ErrBadRequest("error is too long")
		}
		ms, org := now(), principal.Tenant(ctx)
		return settle(ctx, s.State.db, org, in.ID,
			`UPDATE tasks SET status = CASE WHEN attempts < max_attempts THEN ? ELSE ? END,
			                  err = ?, lease = 0, updated = ?
			  WHERE org = ? AND id = ? AND `+live+` = ?
			  RETURNING `+cols,
			Queued, Failed, in.Error, ms, org, in.ID, ms, Leased, ms)
	}
}

// CancelIn addresses the task to cancel. A DELETE carries no body: the URL says
// what stops.
type CancelIn struct {
	// ID of the task.
	ID string `json:"id"`
}

// cancel stops a task that has not finished. A task that already ended keeps the
// ending it had — a cancellation cannot rewrite history, so it is refused.
func cancel(s *cloud.Service[state]) zip.TypedHandler[CancelIn, Task] {
	return func(ctx context.Context, in *CancelIn) (*Task, error) {
		ms, org := now(), principal.Tenant(ctx)
		return settle(ctx, s.State.db, org, in.ID,
			`UPDATE tasks SET status = ?, lease = 0, updated = ?
			  WHERE org = ? AND id = ? AND `+live+` IN (?, ?)
			  RETURNING `+cols,
			Cancelled, ms, org, in.ID, ms, Queued, Leased, ms)
	}
}

// transition runs one guarded UPDATE inside a transaction and returns the row it
// changed. It is the ONE way a task moves: the statement that decides a task's
// fate is the statement that reads it back, so no other worker can slip between
// the decision and the answer. found is false when the guard matched nothing —
// which means "the queue is empty" to a lease and "wrong state" to everything
// else, so the caller says which of those it asked for.
func transition(ctx context.Context, db *sql.DB, q string, args ...any) (*Task, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, zip.ErrInternal("write failed")
	}
	defer func() { _ = tx.Rollback() }()
	t, err := scan(tx.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, zip.ErrInternal("write failed")
	}
	if err := tx.Commit(); err != nil {
		return nil, false, zip.ErrInternal("write failed")
	}
	return t, true, nil
}

// settle is transition for the acknowledgements, where matching nothing is a
// refusal rather than an empty queue: 404 when the task is not there at all,
// 409 when it is but was in no state to move. A caller acts differently on each,
// so they are different answers.
func settle(ctx context.Context, db *sql.DB, org, id, q string, args ...any) (*Task, error) {
	t, found, err := transition(ctx, db, q, args...)
	if err != nil {
		return nil, err
	}
	if !found {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tasks WHERE org = ? AND id = ?`, org, id).Scan(&n); err != nil || n == 0 {
			return nil, zip.ErrNotFound("no such task")
		}
		return nil, zip.ErrConflict("the task is not in a state that allows this")
	}
	return t, nil
}

// row is the one thing a *sql.Row and a *sql.Rows both are: somewhere a task can
// be read from. One scan then serves the single-row reads, the list and the
// RETURNING clause of every transition.
type row interface{ Scan(dest ...any) error }

// scan reads one task in the order cols selects it.
func scan(r row) (*Task, error) {
	var t Task
	var payload, result []byte
	var lease, created, updated int64
	if err := r.Scan(&t.ID, &t.Kind, &payload, &t.Status, &t.Attempts, &t.MaxAttempts,
		&lease, &result, &t.Error, &created, &updated); err != nil {
		return nil, err
	}
	t.Payload, t.Created, t.Updated = json.RawMessage(payload), at(created), at(updated)
	if len(result) > 0 {
		t.Result = json.RawMessage(result)
	}
	if t.Status == Leased && lease > 0 {
		d := at(lease)
		t.Lease = &d
	}
	return &t, nil
}

// payload refuses a body this queue will not carry: not JSON, or too big. Both
// are the caller's mistake, so both are 400.
func payload(field string, doc json.RawMessage) error {
	if len(doc) == 0 || !json.Valid(doc) {
		return zip.ErrBadRequest(field + " must be a JSON value")
	}
	if len(doc) > MaxPayload {
		return zip.ErrBadRequest(fmt.Sprintf("%s is %d bytes, over the %d-byte limit", field, len(doc), MaxPayload))
	}
	return nil
}

// now is the current time in the milliseconds the store keeps. Time is stored as
// an integer because that is what leases are compared against.
func now() int64 { return time.Now().UTC().UnixMilli() }

// at renders a stored millisecond stamp for a reader.
func at(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// newID mints a task id: 128 random bits, hex. Ids are server-generated so a
// caller can neither choose one nor guess another tenant's task.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read is documented never to fail
	return hex.EncodeToString(b[:])
}
