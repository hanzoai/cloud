package git

// statuses.go is the COMMIT STATUS noun: what an outside system (CI, a linter, a
// deploy gate) says about one commit of one repository.
//
// The forge this app replaces kept one row per job per attempt of every run: 2.1M
// rows over 141 repositories, 135k written in the last seven days. A reader only
// ever wants the newest word per context, so a status here is keyed by
// (repository, commit, context) and re-stating a context replaces what it said.
// That bounds the table by the number of distinct contexts instead of the number
// of runs, and loses nothing a combined or a per-context read could show.
//
// Two writers, one row shape. An outside caller states a status through POST
// /v1/git/repos/:name/commits/:sha/statuses. The run engine states one for every job it
// opens and every job a runner reports on, inside the same transaction that moves
// the task (run.go), so the status a PR shows can never disagree with the run
// beside it.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

// The words a status may carry. They are the forge's own, so a client written for
// it keeps working; "skipped" is the one a run engine adds for a job that did not
// run.
const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
	StateError   = "error"
	StateWarning = "warning"
	StateSkipped = "skipped"
)

const (
	maxContext     = 255
	maxDescription = 1024
	maxTargetURL   = 2048
)

// shaRE is a full object name: SHA-1 (40) or SHA-256 (64), lower case. A short or
// abbreviated name is refused rather than guessed, because a status filed under a
// prefix would match whichever commit was resolved first.
var shaRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// statusDDL is this file's half of the git store's schema. The unique index is the
// key; the second index serves the one read a combined state makes.
const statusDDL = `
CREATE TABLE IF NOT EXISTS commit_status (
  id          TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  project     TEXT NOT NULL DEFAULT '',
  repo        TEXT NOT NULL,
  sha         TEXT NOT NULL,
  context     TEXT NOT NULL,
  state       TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  target_url  TEXT NOT NULL DEFAULT '',
  creator     TEXT NOT NULL DEFAULT '',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_commit_status ON commit_status(org, project, repo, sha, context);
`

// Status is one context's latest word about one commit.
type Status struct {
	ID          string
	Org         string
	Project     string
	Repo        string
	SHA         string
	Context     string
	State       string
	Description string
	TargetURL   string
	Creator     string
	CreatedAt   int64
	UpdatedAt   int64
}

// validState reports whether s is a word a status may carry.
func validState(s string) bool {
	switch s {
	case StatePending, StateSuccess, StateFailure, StateError, StateWarning, StateSkipped:
		return true
	}
	return false
}

// execer is what a statement needs: a *sql.DB or a *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// putStatus states a status, replacing the word the same context said before. It
// runs on whatever executes it, so the run engine can state one inside the
// transaction that moves a task.
func putStatus(ctx context.Context, q execer, v Status) error {
	if err := dialect.Text(v.Org, v.Project, v.Repo, v.SHA, v.Context, v.State, v.Description, v.TargetURL, v.Creator); err != nil {
		return fmt.Errorf("put status: %w", err)
	}
	now := time.Now().Unix()
	if v.ID == "" {
		v.ID = mint.ID("cs")
	}
	_, err := q.ExecContext(ctx,
		`INSERT INTO commit_status (id,org,project,repo,sha,context,state,description,target_url,creator,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
		 ON CONFLICT (org,project,repo,sha,context) DO UPDATE SET
		   state=excluded.state, description=excluded.description, target_url=excluded.target_url,
		   creator=excluded.creator, updated_at=excluded.updated_at`,
		v.ID, v.Org, v.Project, v.Repo, v.SHA, v.Context, v.State, v.Description, v.TargetURL, v.Creator, now)
	if err != nil {
		return fmt.Errorf("put status: %w", err)
	}
	return nil
}

// PutStatus is putStatus on the store's own connection.
func (s *Store) PutStatus(ctx context.Context, v Status) error { return putStatus(ctx, s.db, v) }

// Statuses lists the latest status of every context of one commit, by context.
func (s *Store) Statuses(ctx context.Context, org, project, repo, sha string) ([]Status, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,project,repo,sha,context,state,description,target_url,creator,created_at,updated_at
		 FROM commit_status WHERE org=$1 AND project=$2 AND repo=$3 AND sha=$4 ORDER BY context ASC`,
		org, project, repo, sha)
	if err != nil {
		return nil, fmt.Errorf("list statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Status
	for rows.Next() {
		var v Status
		if err := rows.Scan(&v.ID, &v.Org, &v.Project, &v.Repo, &v.SHA, &v.Context, &v.State,
			&v.Description, &v.TargetURL, &v.Creator, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan status: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// combined is the one word for a commit: the worst of its contexts. A commit no
// context has spoken about is pending, because "nothing checked it yet" must not
// read as green.
func combined(statuses []Status) string {
	rank := func(s string) int {
		switch s {
		case StateFailure:
			return 5
		case StateError:
			return 4
		case StateWarning:
			return 3
		case StatePending:
			return 2
		}
		return 1 // success and skipped
	}
	if len(statuses) == 0 {
		return StatePending
	}
	worst := StateSuccess
	for _, v := range statuses {
		if rank(v.State) > rank(worst) {
			worst = v.State
		}
	}
	if worst == StateSkipped {
		return StateSuccess
	}
	return worst
}

// taskState is a run task's status in the status vocabulary. A job that never ran
// states nothing worse than "skipped"; a cancelled one is an error, not a failure,
// because the code did not fail — the run was stopped.
func taskState(status string) string {
	switch status {
	case Success:
		return StateSuccess
	case Failure:
		return StateFailure
	case Cancelled:
		return StateError
	case Skipped:
		return StateSkipped
	}
	return StatePending
}

// jobContext names a job's status the way the forge did: "<workflow> / <job> (<event>)".
func jobContext(workflow, job, event string) string {
	return workflow + " / " + job + " (" + event + ")"
}

// ── wire shapes ──────────────────────────────────────────────────────────────

// statusIn states a status for one commit. The repository and commit come from the
// URL, the rest from the body.
type statusIn struct {
	// Name is the repository, from the :name path segment.
	Name string `json:"name"`
	// SHA is the full commit hash the status is about, from the :sha path segment.
	SHA string `json:"sha"`
	// State is pending, success, failure, error, warning or skipped. Required.
	State string `json:"state" url:"-"`
	// Context names what is speaking ("ci/lint"). Stating the same context again
	// replaces its earlier word. Defaults to "default".
	Context string `json:"context" url:"-"`
	// Description is a short human sentence about the state.
	Description string `json:"description" url:"-"`
	// TargetURL is where a person reads more: a run page, a build log.
	TargetURL string `json:"targetUrl" url:"-"`
}

// statusRef addresses one commit of one repository.
type statusRef struct {
	// Name is the repository, from the :name path segment.
	Name string `json:"name"`
	// SHA is the full commit hash, from the :sha path segment.
	SHA string `json:"sha"`
}

// statusView is one context's latest word about a commit.
type statusView struct {
	// ID is the status row's identifier.
	ID string `json:"id"`
	// Context names what spoke.
	Context string `json:"context"`
	// State is pending, success, failure, error, warning or skipped.
	State string `json:"state"`
	// Description is the short sentence the context gave.
	Description string `json:"description"`
	// TargetURL is where a person reads more; empty when none was given.
	TargetURL string `json:"targetUrl"`
	// Creator is who stated it: the principal's subject, or "run" for the engine.
	Creator string `json:"creator"`
	// CreatedAt is when the context first spoke, RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when it last spoke, RFC 3339 UTC.
	UpdatedAt string `json:"updatedAt"`
}

// combinedStatus is what a commit's contexts say together.
type combinedStatus struct {
	// SHA is the commit.
	SHA string `json:"sha"`
	// State is the worst of the contexts: failure, error, warning, pending, else
	// success. A commit with no status is pending.
	State string `json:"state"`
	// Total is the number of contexts that have spoken.
	Total int `json:"total"`
	// Statuses holds each context's latest word, ordered by context.
	Statuses []statusView `json:"statuses"`
}

func toStatusView(v Status) statusView {
	return statusView{
		ID: v.ID, Context: v.Context, State: v.State, Description: v.Description,
		TargetURL: v.TargetURL, Creator: v.Creator,
		CreatedAt: text.Unix(v.CreatedAt), UpdatedAt: text.Unix(v.UpdatedAt),
	}
}

func cleanSHA(raw string) (string, error) {
	sha := strings.ToLower(strings.TrimSpace(raw))
	if !shaRE.MatchString(sha) {
		return "", zip.ErrBadRequest("sha must be a full 40 or 64 character commit hash")
	}
	return sha, nil
}

// postStatus states a commit's status. Answers 201. Stating a context that already
// spoke replaces its word. A repository outside the caller's scope is not found.
//
// Example: {"name": "widgets", "sha": "7dca5a5c406040cceafbaa147e0e27b3734ad576", "state": "success", "context": "ci/lint", "description": "lint passed"}
func (o ops) postStatus(ctx context.Context, in *statusIn) (*statusView, error) {
	t, name, err := o.scoped(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	sha, err := cleanSHA(in.SHA)
	if err != nil {
		return nil, err
	}
	state := strings.ToLower(strings.TrimSpace(in.State))
	if !validState(state) {
		return nil, zip.ErrBadRequest("state must be pending, success, failure, error, warning or skipped")
	}
	cx := strings.TrimSpace(in.Context)
	if cx == "" {
		cx = "default"
	}
	if len(cx) > maxContext || len(in.Description) > maxDescription || len(in.TargetURL) > maxTargetURL {
		return nil, zip.ErrBadRequest("context, description or targetUrl is too long")
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	v := Status{
		Org: t.org, Project: t.project, Repo: name, SHA: sha, Context: cx, State: state,
		Description: strings.TrimSpace(in.Description), TargetURL: strings.TrimSpace(in.TargetURL),
		Creator: t.user,
	}
	if err := store.PutStatus(ctx, v); err != nil {
		return nil, internalErr(err)
	}
	rows, err := store.Statuses(ctx, t.org, t.project, name, sha)
	if err != nil {
		return nil, internalErr(err)
	}
	for _, r := range rows {
		if r.Context == cx {
			view := toStatusView(r)
			return &view, nil
		}
	}
	return nil, internalErr(fmt.Errorf("status vanished after write"))
}

// listStatuses reads what a commit's contexts say, and the one word they say
// together.
//
// Example: {"name": "widgets", "sha": "7dca5a5c406040cceafbaa147e0e27b3734ad576"}
func (o ops) listStatuses(ctx context.Context, in *statusRef) (*combinedStatus, error) {
	t, name, err := o.scoped(ctx, in.Name)
	if err != nil {
		return nil, err
	}
	sha, err := cleanSHA(in.SHA)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.Statuses(ctx, t.org, t.project, name, sha)
	if err != nil {
		return nil, internalErr(err)
	}
	out := &combinedStatus{SHA: sha, State: combined(rows), Total: len(rows), Statuses: make([]statusView, 0, len(rows))}
	for _, r := range rows {
		out.Statuses = append(out.Statuses, toStatusView(r))
	}
	return out, nil
}
