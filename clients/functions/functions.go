// Package functions is the local function plane behind /v1/functions: a registry
// of named source snippets plus a runner that actually executes them, over one
// embedded SQLite file at {DataDir}/functions.db. Two tables — the functions
// themselves and an invocation ledger — both put `org` first in their key, and
// every query binds `WHERE org = ?`, so a read in one namespace can never reach
// another's rows.
//
// # Staged
//
// This package is linked into the binary but INERT until an operator names it.
// Every other primitive in the dev edition stores or serves data; this one RUNS
// CODE the caller supplied, with the server process's own privileges. That is a
// choice an operator makes deliberately for a machine they control, never a
// default that falls out of `go build`. The wiring lives with the lead; nothing
// in here turns itself on.
//
// # What bounds the runner
//
// run.go holds the mechanism. The properties it exists to make true:
//
//   - The interpreter comes from a fixed allow-list — node, python3, bash —
//     resolved with exec.LookPath. A request names a RUNTIME; it never names a
//     path and never contributes an argument, so no request can choose the
//     binary that runs or how it is called.
//   - The code is written into a fresh os.MkdirTemp directory, passed as argv,
//     and the directory is removed when the run ends. Nothing is ever handed to a
//     shell as a string, so there is no interpolation to break out of.
//   - The child gets a deadline from the function's own timeout, a fixed working
//     directory, and a two-variable environment. The server's environment carries
//     the KMS master key and everything else it was started with; a function has
//     no business reading any of it.
//   - The child leads its own process group and the deadline kills the GROUP, so
//     what a function backgrounds dies with it. Killing only the process we
//     started would leave a caller free to accumulate work the host never agreed
//     to, one timed-out invoke at a time.
//   - stdout and stderr are captured separately and capped, so a function that
//     prints in a loop costs a fixed amount of memory and a fixed amount of disk.
//
// A host missing an interpreter answers 503 for that runtime and keeps serving
// the registry: the ability to store a function does not depend on the ability to
// run it. Every attempt lands in the ledger — ok, error, timeout, or unavailable
// — because a record with holes in it answers no question worth asking.
package functions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher;
	// !cgo → pure-Go modernc). Blank import registers it.
	_ "github.com/hanzoai/sqlite"
)

// Bounds. A function is a snippet somebody typed, and every one of these numbers
// is the point past which a caller is no longer describing a function but
// spending the host's memory, disk, or clock.
const (
	maxNameLen     = 64
	maxCodeBytes   = 1 << 20 // source
	maxInputBytes  = 1 << 20 // stdin handed to one run
	maxOutputBytes = 64 << 10

	defaultTimeout = 10 // seconds
	minTimeout     = 1
	maxTimeout     = 60

	defaultLedger = 20
	maxLedger     = 200
)

// namePattern is the whole naming rule: lowercase alphanumerics with interior
// hyphens. A name is both a URL segment and the stem of a file this package
// writes, so anything that could be a path, a flag, or a second segment is not a
// name — the check is an allow-list of shapes rather than a hunt for bad ones.
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// reserved holds the sub-route verbs that live under /v1/functions/:name. A
// function wearing one of them would address itself at /v1/functions/invoke/invoke,
// which reads as a route rather than as a resource. Two words is a cheap price for
// URLs that mean one thing.
var reserved = map[string]bool{"invoke": true, "invocations": true}

// mounted is the process's one service value, captured at route registration so
// Shutdown can close the database it opened. Nil before Mount and after Shutdown.
var mounted *cloud.Service[state]

// Mount wires the functions surface onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "functions", build, routes)
}

// Shutdown closes the registry. Idempotent, so a double shutdown on a failed boot
// is not an error.
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	db := mounted.State.db
	mounted = nil
	return db.Close()
}

// state is functions' own data; shared deps live in the embedded cloud.Base.
type state struct{ db *sql.DB }

// schema is the whole store. Both tables lead their key with org, which is what
// makes `WHERE org = ?` an index seek rather than a filter over somebody else's
// rows — the tenant boundary is the shape of the index, not a habit of the
// queries.
const schema = `
CREATE TABLE IF NOT EXISTS functions (
  org        TEXT    NOT NULL,
  name       TEXT    NOT NULL,
  runtime    TEXT    NOT NULL,
  code       TEXT    NOT NULL,
  timeout_s  INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, name)
);

CREATE TABLE IF NOT EXISTS invocations (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  org         TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  status      TEXT    NOT NULL,
  exit_code   INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL,
  stdout      TEXT    NOT NULL,
  stderr      TEXT    NOT NULL,
  created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_invocations_org_name ON invocations(org, name, id DESC);
`

// build opens the store and fails the mount closed if it cannot. The boot line
// names the runtimes this host can actually run, which is the fact an operator
// needs when python3 works and node does not.
func build(b cloud.Base) (state, error) {
	db, err := cek.Open(filepath.Join(b.DataDir, "functions.db"))
	if err != nil {
		return state{}, fmt.Errorf("open functions.db: %w", err)
	}
	db.SetMaxOpenConns(1) // one writer against the file lock
	for _, pragma := range []string{"PRAGMA busy_timeout=5000", "PRAGMA journal_mode=WAL"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return state{}, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return state{}, fmt.Errorf("migrate functions.db: %w", err)
	}
	b.Log.Info("functions registry open", "runtimes", strings.Join(installed(), ","))
	return state{db: db}, nil
}

// routes is the ONE registration point. The collection ops come first, then the
// :name family, so a static path is matched as itself.
func routes(app *zip.App, s *cloud.Service[state]) {
	mounted = s
	g := app.Group("/v1/functions")

	zip.Get[ListIn, ListOut](g, "", list(s),
		zip.WithOperationID("functions_list"),
		zip.WithSummary("List the functions in this namespace"),
		zip.WithTags("functions"))

	zip.Post[CreateIn, Function](g, "", create(s),
		zip.WithOperationID("functions_create"),
		zip.WithSummary("Register a function, replacing any of the same name"),
		zip.WithTags("functions"),
		zip.WithStatus(201))

	zip.Get[GetIn, GetOut](g, "/:name", get(s),
		zip.WithOperationID("functions_get"),
		zip.WithSummary("Read one function and its recent invocations"),
		zip.WithTags("functions"))

	zip.Delete[DeleteIn, DeleteOut](g, "/:name", remove(s),
		zip.WithOperationID("functions_delete"),
		zip.WithSummary("Remove one function"),
		zip.WithTags("functions"))

	zip.Post[InvokeIn, InvokeOut](g, "/:name/invoke", invoke(s),
		zip.WithOperationID("functions_invoke"),
		zip.WithSummary("Run one function and record the invocation"),
		zip.WithTags("functions"))

	zip.Get[InvocationsIn, InvocationsOut](g, "/:name/invocations", history(s),
		zip.WithOperationID("functions_invocations"),
		zip.WithSummary("Read one function's invocation ledger"),
		zip.WithTags("functions"))
}

// ── the public contract ──────────────────────────────────────────────────────

// Function is one registered function. The code is returned as stored, because
// an operator inspecting a surface that executes code needs to read exactly what
// will execute.
type Function struct {
	// Name identifies the function within the namespace and addresses it in the
	// URL: lowercase alphanumerics with interior hyphens.
	Name string `json:"name"`
	// Runtime names the interpreter from the fixed allow-list: node, python3, or bash.
	Runtime string `json:"runtime"`
	// Code is the source, written to a temporary file and handed to the interpreter.
	Code string `json:"code"`
	// TimeoutSeconds is the wall clock a run gets before it is killed.
	TimeoutSeconds int `json:"timeoutSeconds"`
	// CreatedAt is when the name was first registered, in Unix seconds. It survives
	// a replacing create: a function's identity is its name, and re-registering is
	// an edit of the same thing.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the source or its settings last changed, in Unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// Invocation is one entry of the ledger: what happened when the function ran.
// Entries outlive the function — deleting a registration does not erase the
// record of what already executed on this host.
type Invocation struct {
	// ID orders the ledger; it rises with time and is unique within the store.
	ID int64 `json:"id"`
	// Name is the function that ran.
	Name string `json:"name"`
	// Status is one of ok, error, timeout, or unavailable (nothing ran, because
	// the runtime is not installed here).
	Status string `json:"status"`
	// ExitCode is the process exit status, or -1 when there was none — killed at
	// the deadline, or never started.
	ExitCode int `json:"exitCode"`
	// DurationMS is the wall clock the attempt took, in milliseconds.
	DurationMS int64 `json:"durationMs"`
	// Stdout is what the run printed, up to the capture cap.
	Stdout string `json:"stdout"`
	// Stderr is what the run printed on the error stream, up to the capture cap;
	// for an unavailable attempt it carries the reason nothing ran.
	Stderr string `json:"stderr"`
	// CreatedAt is when the attempt was recorded, in Unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// ListIn takes no input: the namespace is the caller's identity, never a field.
type ListIn struct{}

// ListOut is every function in the caller's namespace, ordered by name.
type ListOut struct {
	Functions []Function `json:"functions"`
}

// CreateIn registers a function, replacing any already holding the name.
type CreateIn struct {
	// Name is the function's identity: lowercase alphanumerics with interior
	// hyphens, at most 64 characters. `invoke` and `invocations` are reserved.
	Name string `json:"name"`
	// Runtime must be one of node, python3, or bash. Nothing else is accepted,
	// and it can never be a path.
	Runtime string `json:"runtime"`
	// Code is the source to run. It is passed to the interpreter as a file, never
	// through a shell.
	Code string `json:"code"`
	// TimeoutSeconds bounds one run, clamped to 1..60. Absent means 10.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// GetIn addresses one function by name.
type GetIn struct {
	Name string `json:"name"`
}

// GetOut is one function together with its most recent invocations, so a caller
// reading a function sees what it has been doing without a second round trip.
type GetOut struct {
	Function    Function     `json:"function"`
	Invocations []Invocation `json:"invocations"`
}

// DeleteIn addresses the function to remove.
type DeleteIn struct {
	Name string `json:"name"`
}

// DeleteOut names what was removed. The function's ledger entries stay: the
// record of what ran on this host outlives the registration that caused it.
type DeleteOut struct {
	Name string `json:"name"`
}

// InvokeIn runs one function. The name comes from the URL, which is the
// addressing authority — a body naming a different function does not reach it.
type InvokeIn struct {
	Name string `json:"name"`
	// Input is written to the run's stdin. Absent means an empty stdin.
	Input string `json:"input,omitempty"`
}

// InvokeOut is the outcome of one run, and is exactly what the ledger keeps.
type InvokeOut struct {
	Name string `json:"name"`
	// Status is ok, error, or timeout. An unavailable runtime answers 503 instead
	// of returning here, because nothing ran.
	Status string `json:"status"`
	// ExitCode is the process exit status, or -1 when it was killed at the deadline.
	ExitCode int `json:"exitCode"`
	// DurationMS is the wall clock the run took, in milliseconds.
	DurationMS int64 `json:"durationMs"`
	// Stdout is what the run printed.
	Stdout string `json:"stdout"`
	// Stderr is what the run printed on the error stream.
	Stderr string `json:"stderr"`
	// Truncated reports that a stream hit the capture cap and the rest was dropped.
	Truncated bool `json:"truncated"`
}

// InvocationsIn reads one function's ledger.
type InvocationsIn struct {
	Name string `json:"name"`
	// Limit caps how many entries come back, clamped to 1..200. Absent means 20.
	Limit int `json:"limit,omitempty"`
}

// InvocationsOut is the ledger, newest first.
type InvocationsOut struct {
	Name        string       `json:"name"`
	Invocations []Invocation `json:"invocations"`
}

// ── handlers ─────────────────────────────────────────────────────────────────

// list returns the caller's functions.
func list(s *cloud.Service[state]) zip.TypedHandler[ListIn, ListOut] {
	return func(ctx context.Context, _ *ListIn) (*ListOut, error) {
		rows, err := s.State.db.QueryContext(ctx,
			`SELECT name, runtime, code, timeout_s, created_at, updated_at
			   FROM functions WHERE org = ? ORDER BY name`, principal.Tenant(ctx))
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		defer func() { _ = rows.Close() }()
		out := ListOut{Functions: []Function{}}
		for rows.Next() {
			var f Function
			if err := rows.Scan(&f.Name, &f.Runtime, &f.Code, &f.TimeoutSeconds, &f.CreatedAt, &f.UpdatedAt); err != nil {
				return nil, zip.ErrInternal("read failed")
			}
			out.Functions = append(out.Functions, f)
		}
		if rows.Err() != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return &out, nil
	}
}

// create registers a function, or replaces the one already holding the name.
// Replacing keeps the original created_at, so the row records when the name came
// into being rather than when it was last edited.
func create(s *cloud.Service[state]) zip.TypedHandler[CreateIn, Function] {
	return func(ctx context.Context, in *CreateIn) (*Function, error) {
		name, err := checkName(in.Name)
		if err != nil {
			return nil, err
		}
		rt := strings.TrimSpace(in.Runtime)
		if _, ok := runtimes[rt]; !ok {
			return nil, zip.ErrBadRequest(fmt.Sprintf("unknown runtime %q; known: %s", rt, strings.Join(names(), ", ")))
		}
		if strings.TrimSpace(in.Code) == "" {
			return nil, zip.ErrBadRequest("code is required")
		}
		if len(in.Code) > maxCodeBytes {
			return nil, zip.ErrBadRequest("code is too large")
		}
		org, now := principal.Tenant(ctx), time.Now().Unix()
		if _, err := s.State.db.ExecContext(ctx,
			`INSERT INTO functions (org, name, runtime, code, timeout_s, created_at, updated_at)
			      VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(org, name) DO UPDATE SET
			      runtime = excluded.runtime, code = excluded.code,
			      timeout_s = excluded.timeout_s, updated_at = excluded.updated_at`,
			org, name, rt, in.Code, clampTimeout(in.TimeoutSeconds), now, now); err != nil {
			return nil, zip.ErrInternal("write failed")
		}
		// Read back rather than echo: on a replace the stored created_at is the
		// earlier one, and the caller should see the row that exists.
		f, err := loadFunction(ctx, s.State.db, org, name)
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return &f, nil
	}
}

// get returns one function and its recent invocations.
func get(s *cloud.Service[state]) zip.TypedHandler[GetIn, GetOut] {
	return func(ctx context.Context, in *GetIn) (*GetOut, error) {
		name, err := checkName(in.Name)
		if err != nil {
			return nil, err
		}
		org := principal.Tenant(ctx)
		f, err := loadFunction(ctx, s.State.db, org, name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, zip.ErrNotFound("no such function")
		}
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		ledger, err := loadLedger(ctx, s.State.db, org, name, defaultLedger)
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return &GetOut{Function: f, Invocations: ledger}, nil
	}
}

// remove deletes one function. The ledger is left alone on purpose: what already
// executed on this host is a fact, and unregistering a name does not unmake it.
func remove(s *cloud.Service[state]) zip.TypedHandler[DeleteIn, DeleteOut] {
	return func(ctx context.Context, in *DeleteIn) (*DeleteOut, error) {
		name, err := checkName(in.Name)
		if err != nil {
			return nil, err
		}
		res, err := s.State.db.ExecContext(ctx,
			`DELETE FROM functions WHERE org = ? AND name = ?`, principal.Tenant(ctx), name)
		if err != nil {
			return nil, zip.ErrInternal("delete failed")
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return nil, zip.ErrNotFound("no such function")
		}
		return &DeleteOut{Name: name}, nil
	}
}

// invoke runs one function and records the attempt. The ledger write happens
// before the answer is decided, so a 503 for a missing runtime is recorded the
// same as a run that finished.
func invoke(s *cloud.Service[state]) zip.TypedHandler[InvokeIn, InvokeOut] {
	return func(ctx context.Context, in *InvokeIn) (*InvokeOut, error) {
		name, err := checkName(in.Name)
		if err != nil {
			return nil, err
		}
		if len(in.Input) > maxInputBytes {
			return nil, zip.ErrBadRequest("input is too large")
		}
		org := principal.Tenant(ctx)
		f, err := loadFunction(ctx, s.State.db, org, name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, zip.ErrNotFound("no such function")
		}
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}

		r := run(ctx, f.Runtime, f.Code, in.Input, time.Duration(f.TimeoutSeconds)*time.Second)
		record(ctx, s, org, name, r)
		if r.status == statusUnavailable {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", r.stderr)
		}
		return &InvokeOut{
			Name:       name,
			Status:     r.status,
			ExitCode:   r.exitCode,
			DurationMS: r.duration.Milliseconds(),
			Stdout:     r.stdout,
			Stderr:     r.stderr,
			Truncated:  r.truncated,
		}, nil
	}
}

// history reads one function's ledger. An unregistered name is a 404, so "no such
// function" stays distinguishable from "never ran".
func history(s *cloud.Service[state]) zip.TypedHandler[InvocationsIn, InvocationsOut] {
	return func(ctx context.Context, in *InvocationsIn) (*InvocationsOut, error) {
		name, err := checkName(in.Name)
		if err != nil {
			return nil, err
		}
		org := principal.Tenant(ctx)
		if _, err := loadFunction(ctx, s.State.db, org, name); errors.Is(err, sql.ErrNoRows) {
			return nil, zip.ErrNotFound("no such function")
		} else if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		ledger, err := loadLedger(ctx, s.State.db, org, name, clampLimit(in.Limit))
		if err != nil {
			return nil, zip.ErrInternal("read failed")
		}
		return &InvocationsOut{Name: name, Invocations: ledger}, nil
	}
}

// ── store ────────────────────────────────────────────────────────────────────

// loadFunction reads one row, or sql.ErrNoRows. It is the ONE loader — create
// reads back through it, get and invoke and history address through it — so the
// org predicate is written once and cannot go missing from one path.
func loadFunction(ctx context.Context, db *sql.DB, org, name string) (Function, error) {
	f := Function{Name: name}
	err := db.QueryRowContext(ctx,
		`SELECT runtime, code, timeout_s, created_at, updated_at
		   FROM functions WHERE org = ? AND name = ?`, org, name).
		Scan(&f.Runtime, &f.Code, &f.TimeoutSeconds, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

// loadLedger reads the newest entries for one function.
func loadLedger(ctx context.Context, db *sql.DB, org, name string, limit int) ([]Invocation, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, name, status, exit_code, duration_ms, stdout, stderr, created_at
		   FROM invocations WHERE org = ? AND name = ? ORDER BY id DESC LIMIT ?`,
		org, name, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Invocation{}
	for rows.Next() {
		var v Invocation
		if err := rows.Scan(&v.ID, &v.Name, &v.Status, &v.ExitCode, &v.DurationMS,
			&v.Stdout, &v.Stderr, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// record appends one row to the ledger, for every outcome including the refusal
// to start at all. It writes under a context stripped of cancellation: the run
// already happened, and a caller hanging up must not be able to erase the receipt
// for it. A failed write is logged, never returned — losing the receipt is bad,
// turning a completed run into a 500 is worse.
func record(ctx context.Context, s *cloud.Service[state], org, name string, r result) {
	if _, err := s.State.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO invocations (org, name, status, exit_code, duration_ms, stdout, stderr, created_at)
		      VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		org, name, r.status, r.exitCode, r.duration.Milliseconds(), r.stdout, r.stderr, time.Now().Unix()); err != nil {
		s.Log.Warn("functions: ledger write failed", "org", org, "name", name, "err", err)
	}
}

// ── input bounds ─────────────────────────────────────────────────────────────

// checkName is the ONE name check, run by every op that takes a name — including
// the reads, so a malformed name is a 400 about the name rather than a 404 about
// a function nobody could have created.
func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", zip.ErrBadRequest("name is required")
	case len(name) > maxNameLen:
		return "", zip.ErrBadRequest(fmt.Sprintf("name is longer than %d characters", maxNameLen))
	case !namePattern.MatchString(name):
		return "", zip.ErrBadRequest("name must be lowercase letters, digits, and interior hyphens")
	case reserved[name]:
		return "", zip.ErrBadRequest(fmt.Sprintf("name %q is reserved by the route", name))
	}
	return name, nil
}

// clampTimeout keeps a run's deadline inside what this host will lend it.
func clampTimeout(seconds int) int {
	switch {
	case seconds <= 0:
		return defaultTimeout
	case seconds < minTimeout:
		return minTimeout
	case seconds > maxTimeout:
		return maxTimeout
	}
	return seconds
}

// clampLimit bounds a ledger page.
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultLedger
	case limit > maxLedger:
		return maxLedger
	}
	return limit
}
