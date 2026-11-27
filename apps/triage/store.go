package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	_ "github.com/hanzoai/sqlite"
)

const subsystem = "triage"

// A fault's state in the ledger, and a run's.
const (
	seen   = "seen"   // counted, no issue yet
	filing = "filing" // one replica holds the right to file it
	open   = "open"   // an issue stands for it
	fixed  = "fixed"  // its fix merged

	queued   = "queued"   // an issue waits for a run
	running  = "running"  // a run is working it
	proposed = "proposed" // a pull request is open
	merged   = "merged"
	failed   = "failed"
)

// Store is the ledger: one row per (org, fingerprint) and one per issue handed to
// a run. It holds the state the loop needs between events and nothing a GitHub issue
// or the event plane already holds.
type Store struct{ db *sql.DB }

func openStore(dir string) (*Store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db}
		return s, s.migrate()
	})
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS triage_config (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS triage_faults (
  org TEXT NOT NULL, fp TEXT NOT NULL, repo TEXT NOT NULL, class TEXT NOT NULL, code TEXT NOT NULL, op TEXT NOT NULL,
  first_seen BIGINT NOT NULL, last_seen BIGINT NOT NULL, window_start BIGINT NOT NULL, hits BIGINT NOT NULL, total BIGINT NOT NULL,
  state TEXT NOT NULL, issue BIGINT NOT NULL DEFAULT 0, reported BIGINT NOT NULL DEFAULT 0,
  fixed_sha TEXT NOT NULL DEFAULT '', fixed_at BIGINT NOT NULL DEFAULT 0, release TEXT NOT NULL DEFAULT '',
  sample TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (org, fp));
CREATE TABLE IF NOT EXISTS triage_work (
  org TEXT NOT NULL, repo TEXT NOT NULL, issue BIGINT NOT NULL, fp TEXT NOT NULL DEFAULT '', attempt BIGINT NOT NULL,
  state TEXT NOT NULL, session TEXT NOT NULL DEFAULT '', branch TEXT NOT NULL DEFAULT '', pr BIGINT NOT NULL DEFAULT 0,
  head TEXT NOT NULL DEFAULT '', queued_at BIGINT NOT NULL, started_at BIGINT NOT NULL DEFAULT 0, deadline BIGINT NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org, repo, issue));
CREATE INDEX IF NOT EXISTS ix_triage_work_state ON triage_work(org, repo, state);`
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return dialect.Of(s.db).Migrate(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, ddl)
		return err
	})
}

func (s *Store) Close() error { return s.db.Close() }

// Config is the saved configuration; none saved is the zero Config.
func (s *Store) Config(ctx context.Context) (Config, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM triage_config WHERE k = 'config'`).Scan(&raw)
	var c Config
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal([]byte(raw), &c)
}

func (s *Store) SetConfig(ctx context.Context, c Config) error {
	b, _ := json.Marshal(c)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO triage_config (k, v) VALUES ('config', $1) ON CONFLICT (k) DO UPDATE SET v = $1`, string(b))
	return err
}

// Fault is one fingerprint's row.
type Fault struct {
	Org      string `json:"org"`
	FP       string `json:"fingerprint"`
	Repo     string `json:"repo"`
	Class    string `json:"class"`
	Code     string `json:"code"`
	Op       string `json:"op"`
	First    int64  `json:"firstSeen"`
	Last     int64  `json:"lastSeen"`
	Window   int64  `json:"windowStart"`
	Hits     int64  `json:"hits"`
	Total    int64  `json:"total"`
	State    string `json:"state"`
	Issue    int    `json:"issue,omitempty"`
	Reported int64  `json:"reported"`
	FixedSHA string `json:"fixedSha,omitempty"`
	FixedAt  int64  `json:"fixedAt,omitempty"`
	Release  string `json:"release,omitempty"`
	Sample   Sample `json:"sample"`
}

// Sample is what an issue shows of the occurrences: the latest reason, the build, and
// the last few request ids. It is already scrubbed: it came off the event plane.
type Sample struct {
	Reason   string   `json:"reason"`
	Service  string   `json:"service,omitempty"`
	Requests []string `json:"requests,omitempty"`
	Site     Site     `json:"site"`
}

const faultCols = `org, fp, repo, class, code, op, first_seen, last_seen, window_start, hits, total, state, issue, reported, fixed_sha, fixed_at, release, sample`

type scanner interface{ Scan(...any) error }

func scanFault(r scanner) (Fault, error) {
	var f Fault
	var sample string
	if err := r.Scan(&f.Org, &f.FP, &f.Repo, &f.Class, &f.Code, &f.Op, &f.First, &f.Last, &f.Window, &f.Hits, &f.Total,
		&f.State, &f.Issue, &f.Reported, &f.FixedSHA, &f.FixedAt, &f.Release, &sample); err != nil {
		return f, err
	}
	return f, json.Unmarshal([]byte(sample), &f.Sample)
}

// Hit is one occurrence.
type Hit struct {
	Org, FP, Repo, Class, Code, Op string
	At                             time.Time
	Release, Reason, Service       string
	Request                        string
	Site                           Site
}

// Count adds one occurrence to its fingerprint's row and answers the row. A
// window that has run out starts again at this occurrence; a row that was fixed
// keeps its state, so the caller can tell a regression from a first sight.
func (s *Store) Count(ctx context.Context, h Hit, window time.Duration) (Fault, error) {
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Fault{}, err
	}
	defer func() { _ = tx.Rollback() }()
	at := h.At.Unix()
	f, err := scanFault(tx.QueryRowContext(ctx, `SELECT `+faultCols+` FROM triage_faults WHERE org = $1 AND fp = $2`, h.Org, h.FP))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		f = Fault{Org: h.Org, FP: h.FP, State: seen, First: at, Window: at}
	case err != nil:
		return Fault{}, err
	}
	if at-f.Window >= int64(window.Seconds()) {
		f.Window, f.Hits = at, 0
	}
	f.Repo, f.Class, f.Code, f.Op = h.Repo, h.Class, h.Code, h.Op
	f.Last, f.Hits, f.Total = max(f.Last, at), f.Hits+1, f.Total+1
	if h.Release != "" {
		f.Release = h.Release
	}
	f.Sample.Reason, f.Sample.Service = h.Reason, h.Service
	if h.Site.File != "" || h.Site.Stack != "" {
		f.Sample.Site = h.Site
	}
	if h.Request != "" && !slices.Contains(f.Sample.Requests, h.Request) {
		f.Sample.Requests = append(f.Sample.Requests, h.Request)
		if n := len(f.Sample.Requests); n > maxRequests {
			f.Sample.Requests = f.Sample.Requests[n-maxRequests:]
		}
	}
	sample, _ := json.Marshal(f.Sample)
	if _, err := tx.ExecContext(ctx, `INSERT INTO triage_faults (`+faultCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
ON CONFLICT (org, fp) DO UPDATE SET repo=$3, class=$4, code=$5, op=$6, last_seen=$8, window_start=$9, hits=$10, total=$11, release=$17, sample=$18`,
		f.Org, f.FP, f.Repo, f.Class, f.Code, f.Op, f.First, f.Last, f.Window, f.Hits, f.Total, f.State, f.Issue, f.Reported,
		f.FixedSHA, f.FixedAt, f.Release, string(sample)); err != nil {
		return Fault{}, err
	}
	return f, tx.Commit()
}

// maxRequests is how many request ids an issue shows.
const maxRequests = 5

// Move changes a fault's state from one to another and says whether this call made
// the change: the one replica that does holds the right to what follows.
func (s *Store) Move(ctx context.Context, org, fp, from, to string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE triage_faults SET state = $1 WHERE org = $2 AND fp = $3 AND state = $4`, to, org, fp, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Filed records the issue that stands for a fault and how many occurrences it said.
func (s *Store) Filed(ctx context.Context, org, fp string, issue int, reported int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_faults SET state = $1, issue = $2, reported = $3 WHERE org = $4 AND fp = $5`,
		open, issue, reported, org, fp)
	return err
}

// OpenFor is the fault, other than fp, whose issue stands open for the same class
// and op in repo: the issue a new fingerprint at that op folds into.
func (s *Store) OpenFor(ctx context.Context, org, repo, class, op, fp string) (Fault, bool, error) {
	f, err := scanFault(s.db.QueryRowContext(ctx, `SELECT `+faultCols+` FROM triage_faults
WHERE org = $1 AND repo = $2 AND class = $3 AND op = $4 AND fp <> $5 AND state = $6 AND issue > 0 ORDER BY first_seen, fp LIMIT 1`,
		org, repo, class, op, fp, open))
	if errors.Is(err, sql.ErrNoRows) {
		return Fault{}, false, nil
	}
	return f, err == nil, err
}

// Fold gives back the right to file a fault and records the open issue it was folded
// into instead. The fault is seen again, so it files its own once that issue no
// longer stands open.
func (s *Store) Fold(ctx context.Context, org, fp string, issue int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_faults SET state = $1, issue = $2 WHERE org = $3 AND fp = $4 AND state = $5`,
		seen, issue, org, fp, filing)
	return err
}

// Reported records how many occurrences the issue now says.
func (s *Store) Reported(ctx context.Context, org, fp string, total int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_faults SET reported = $1 WHERE org = $2 AND fp = $3`, total, org, fp)
	return err
}

// Fix records the merge that fixed a fault, or clears it when the fault came back.
func (s *Store) Fix(ctx context.Context, org, fp, sha string, at int64) error {
	state := fixed
	if sha == "" {
		state = open
	}
	_, err := s.db.ExecContext(ctx, `UPDATE triage_faults SET state = $1, fixed_sha = $2, fixed_at = $3 WHERE org = $4 AND fp = $5`,
		state, sha, at, org, fp)
	return err
}

// Faults lists the ledger, newest first.
func (s *Store) Faults(ctx context.Context) ([]Fault, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+faultCols+` FROM triage_faults ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Fault
	for rows.Next() {
		f, err := scanFault(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Work is one issue handed to a run.
type Work struct {
	Org      string `json:"org"`
	Repo     string `json:"repo"`
	Issue    int    `json:"issue"`
	FP       string `json:"fingerprint,omitempty"`
	Attempt  int    `json:"attempt"`
	State    string `json:"state"`
	Session  string `json:"session,omitempty"`
	Branch   string `json:"branch,omitempty"`
	PR       int    `json:"pr,omitempty"`
	Head     string `json:"head,omitempty"`
	Queued   int64  `json:"queuedAt"`
	Started  int64  `json:"startedAt,omitempty"`
	Deadline int64  `json:"deadline,omitempty"`
	Note     string `json:"note,omitempty"`
}

const workCols = `org, repo, issue, fp, attempt, state, session, branch, pr, head, queued_at, started_at, deadline, note`

func scanWork(r scanner) (Work, error) {
	var w Work
	err := r.Scan(&w.Org, &w.Repo, &w.Issue, &w.FP, &w.Attempt, &w.State, &w.Session, &w.Branch, &w.PR, &w.Head, &w.Queued, &w.Started, &w.Deadline, &w.Note)
	return w, err
}

// Enqueue hands an issue to the runs. An issue already handed over is left as it
// is, and false says so: one issue is one piece of work.
func (s *Store) Enqueue(ctx context.Context, w Work) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO triage_work (org, repo, issue, fp, attempt, state, queued_at) VALUES ($1,$2,$3,$4,1,$5,$6)
ON CONFLICT (org, repo, issue) DO NOTHING`, w.Org, w.Repo, w.Issue, w.FP, queued, w.Queued)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Requeue puts a finished piece of work back in line for another attempt.
func (s *Store) Requeue(ctx context.Context, org, repo string, issue int, at int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_work SET state = $1, attempt = attempt + 1, session = '', pr = 0, head = '', queued_at = $2, started_at = 0, deadline = 0
WHERE org = $3 AND repo = $4 AND issue = $5`, queued, at, org, repo, issue)
	return err
}

func (s *Store) one(ctx context.Context, where string, args ...any) (Work, bool, error) {
	w, err := scanWork(s.db.QueryRowContext(ctx, `SELECT `+workCols+` FROM triage_work WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Work{}, false, nil
	}
	return w, err == nil, err
}

// Issue is the work for one issue.
func (s *Store) Issue(ctx context.Context, org, repo string, issue int) (Work, bool, error) {
	return s.one(ctx, `org = $1 AND repo = $2 AND issue = $3`, org, repo, issue)
}

// Branch is the work whose run writes branch.
func (s *Store) Branch(ctx context.Context, org, repo, branch string) (Work, bool, error) {
	return s.one(ctx, `org = $1 AND repo = $2 AND branch = $3`, org, repo, branch)
}

// Head is the work whose pull request is at head.
func (s *Store) Head(ctx context.Context, org, repo, head string) (Work, bool, error) {
	return s.one(ctx, `org = $1 AND repo = $2 AND head = $3`, org, repo, head)
}

// Session is the work a run belongs to.
func (s *Store) Session(ctx context.Context, session string) (Work, bool, error) {
	return s.one(ctx, `session = $1`, session)
}

// Next is the oldest issue waiting for a run on repo.
func (s *Store) Next(ctx context.Context, org, repo string) (Work, bool, error) {
	return s.one(ctx, `org = $1 AND repo = $2 AND state = 'queued' ORDER BY queued_at, issue LIMIT 1`, org, repo)
}

// Running is how many runs are working on repo and still inside their budget.
func (s *Store) Running(ctx context.Context, org, repo string, now int64) (n int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM triage_work WHERE org = $1 AND repo = $2 AND state = 'running' AND deadline > $3`,
		org, repo, now).Scan(&n)
	return
}

// Started is how many runs the org has started since the instant given.
func (s *Store) Started(ctx context.Context, org string, since int64) (n int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM triage_work WHERE org = $1 AND started_at >= $2`, org, since).Scan(&n)
	return
}

// Claim takes a queued issue for a run, atomically: the replica that does holds the
// right to start it, and the run counts against its repository from this instant.
func (s *Store) Claim(ctx context.Context, w Work, now, deadline int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE triage_work SET state = $1, started_at = $2, deadline = $3, branch = $4
WHERE org = $5 AND repo = $6 AND issue = $7 AND state = $8`, running, now, deadline, w.Branch, w.Org, w.Repo, w.Issue, queued)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Unclaim returns a claimed issue to the line: no run was started for it.
func (s *Store) Unclaim(ctx context.Context, w Work) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_work SET state = $1, started_at = 0, deadline = 0, branch = '' WHERE org = $2 AND repo = $3 AND issue = $4`,
		queued, w.Org, w.Repo, w.Issue)
	return err
}

// Begun records the run a claimed issue started.
func (s *Store) Begun(ctx context.Context, w Work, session, branch string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_work SET session = $1, branch = $2 WHERE org = $3 AND repo = $4 AND issue = $5`,
		session, branch, w.Org, w.Repo, w.Issue)
	return err
}

// Settle moves a piece of work to a state, with a note saying why.
func (s *Store) Settle(ctx context.Context, w Work, state, note string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_work SET state = $1, note = $2 WHERE org = $3 AND repo = $4 AND issue = $5`,
		state, note, w.Org, w.Repo, w.Issue)
	return err
}

// Propose records the pull request a run opened and the head it is at.
func (s *Store) Propose(ctx context.Context, w Work, pr int, head string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE triage_work SET state = $1, pr = $2, head = $3 WHERE org = $4 AND repo = $5 AND issue = $6`,
		proposed, pr, head, w.Org, w.Repo, w.Issue)
	return err
}

// Live lists the work that holds a session: what the kill switch stops.
func (s *Store) Live(ctx context.Context) ([]Work, error) {
	return s.list(ctx, `state = 'running' ORDER BY started_at`)
}

// All lists the work, newest first.
func (s *Store) All(ctx context.Context) ([]Work, error) {
	return s.list(ctx, `1 = 1 ORDER BY queued_at DESC`)
}

func (s *Store) list(ctx context.Context, where string, args ...any) ([]Work, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workCols+` FROM triage_work WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Work
	for rows.Next() {
		w, err := scanWork(rows)
		if err != nil {
			return nil, fmt.Errorf("triage: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
