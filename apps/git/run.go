package git

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// run.go holds what a workflow run IS in this plugin: the pools declared to
// execute one, the runs a commit creates, and the tasks a runner leases.
//
// A POOL IS DECLARED, never inferred. A runner joins a pool by presenting that
// pool's secret, and the secret only exists because somebody declared the pool —
// so "a daemon is up, therefore capacity exists" is not a state this schema can
// represent. That is the whole reason the join secret lives on the pool row
// rather than in a table of its own: one row, one fact, nothing to keep in step.
//
// Everything here lives in the org's existing git.db beside the repos. A run
// belongs to a repository, a repository belongs to an org, and the org file is
// already the isolation boundary every other git row is stored under.

// runDDL is this file's half of the git store's schema.
//
// Task ids are per-org and autoincrement, which is what the wire needs: a runner
// authenticates into exactly one org, so it never sees two orgs' ids and the
// protocol's "never reused" holds for everything that can observe it.
func runDDL(d dialect.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS pool (
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  labels      TEXT NOT NULL DEFAULT '',
  secret_hash TEXT NOT NULL,
  secret_salt TEXT NOT NULL,
  created_at  BIGINT NOT NULL,
  PRIMARY KEY (org, name)
);

CREATE TABLE IF NOT EXISTS runner (
  uuid        TEXT PRIMARY KEY,
  org         TEXT NOT NULL,
  pool        TEXT NOT NULL,
  name        TEXT NOT NULL,
  token_hash  TEXT NOT NULL,
  token_salt  TEXT NOT NULL,
  version     TEXT NOT NULL DEFAULT '',
  labels      TEXT NOT NULL DEFAULT '',
  ephemeral   BIGINT NOT NULL DEFAULT 0,
  cancelling  BIGINT NOT NULL DEFAULT 0,
  last_online BIGINT NOT NULL DEFAULT 0,
  last_active BIGINT NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_runner_pool ON runner(org, pool);

CREATE TABLE IF NOT EXISTS run (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  event      TEXT NOT NULL DEFAULT 'push',
  workflow   TEXT NOT NULL,
  doc        ` + d.Blob() + ` NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'waiting',
  number     BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_run_repo ON run(org, project, repo, created_at DESC);
-- One run per (repo, commit, workflow). A redelivered trigger finds this row
-- instead of making a second run, which is what makes delivery idempotent
-- without the dispatcher having to remember anything.
CREATE UNIQUE INDEX IF NOT EXISTS ux_run_commit ON run(org, project, repo, commit_sha, workflow);

CREATE TABLE IF NOT EXISTS task (
  id         ` + d.Identity() + `,
  run        TEXT NOT NULL,
  org        TEXT NOT NULL,
  pool       TEXT NOT NULL,
  job        TEXT NOT NULL,
  runner     TEXT NOT NULL DEFAULT '',
  status     TEXT NOT NULL DEFAULT 'waiting',
  steps      TEXT NOT NULL DEFAULT '',
  started    BIGINT NOT NULL DEFAULT 0,
  stopped    BIGINT NOT NULL DEFAULT 0,
  log_length BIGINT NOT NULL DEFAULT 0,
  log_size   BIGINT NOT NULL DEFAULT 0,
  log_sealed BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_task_waiting ON task(org, pool, status, id);
CREATE INDEX IF NOT EXISTS ix_task_run ON task(run);

CREATE TABLE IF NOT EXISTS task_output (
  task  BIGINT NOT NULL,
  name  TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (task, name)
);

-- version is the org's queue counter. A runner sends the one it last saw; equal
-- means nothing has been queued since and the lease transaction is never opened.
CREATE TABLE IF NOT EXISTS queue (
  org     TEXT PRIMARY KEY,
  version BIGINT NOT NULL DEFAULT 0
);
`
}

// Status names a run's or a task's progress. They are the same four words the
// wire's Result uses plus the two that precede a result, so nothing has to be
// translated between the database and the protocol.
const (
	Waiting   = "waiting"
	Running   = "running"
	Success   = "success"
	Failure   = "failure"
	Cancelled = "cancelled"
	Skipped   = "skipped"
)

// Pool is capacity somebody declared: a name, the labels a workflow selects it
// by, and the secret a daemon presents to join it.
type Pool struct {
	Org       string
	Name      string
	Labels    []string
	CreatedAt int64

	hash string
	salt string
}

// Runner is one registered daemon.
type Runner struct {
	UUID       string
	Org        string
	Pool       string
	Name       string
	Version    string
	Labels     []string
	Ephemeral  bool
	Cancelling bool
	LastOnline int64
	LastActive int64
	CreatedAt  int64

	hash string
	salt string
}

// Run is one execution of one workflow for one commit.
type Run struct {
	ID        string
	Org       string
	Project   string
	Repo      string
	Ref       string
	Commit    string
	Event     string
	Workflow  string
	Doc       []byte
	Actor     string
	Status    string
	Number    int64
	CreatedAt int64
	UpdatedAt int64
}

// Task is one job of a run, and the thing a runner leases. The workflow
// document travels on the run; the task names which job in it to execute. This
// plugin decides THAT a run happens and WHICH pool executes it; act — on the
// runner — decides what the document means.
type Task struct {
	ID        int64
	Run       string
	Org       string
	Pool      string
	Job       string
	Runner    string
	Status    string
	Steps     string
	Started   int64
	Stopped   int64
	LogLength int64
	LogSize   int64
	LogSealed bool
	CreatedAt int64
}

var (
	errNoPool   = errors.New("git: pool not declared")
	errNoRunner = errors.New("git: runner not registered")
	errNoTask   = errors.New("git: task not found")
)

// credential returns a machine secret and the stored form of it.
//
// SHA-256 over a per-row salt, not a password hash: these are 32 bytes of
// crypto/rand presented every few seconds by a polling daemon, so there is no
// guessing to slow down and a work factor would only buy the fleet a CPU bill.
func credential() (clear, hash, salt string) {
	var b, s [16]byte
	_, _ = rand.Read(b[:])
	_, _ = rand.Read(s[:])
	clear, salt = hex.EncodeToString(b[:]), hex.EncodeToString(s[:])
	return clear, digest(clear, salt), salt
}

func digest(clear, salt string) string {
	sum := sha256.Sum256([]byte(salt + clear))
	return hex.EncodeToString(sum[:])
}

func matches(clear, hash, salt string) bool {
	return subtle.ConstantTimeCompare([]byte(digest(clear, salt)), []byte(hash)) == 1
}

// join is the secret a daemon presents to enter a pool, and it names the pool it
// opens: "<org>/<pool>.<secret>". The name is not the secret — it is the lookup,
// so register can open one org's store and read one row instead of hunting for a
// hash it cannot index.
func join(org, pool, clear string) string { return org + "/" + pool + "." + clear }

// split reads a join secret back. A malformed one resolves to nothing, which
// reaches the caller as the same refusal a wrong secret does.
func split(s string) (org, pool, clear string, ok bool) {
	dot := strings.LastIndex(s, ".")
	slash := strings.Index(s, "/")
	if dot < 0 || slash < 0 || slash > dot {
		return "", "", "", false
	}
	org, pool, clear = s[:slash], s[slash+1:dot], s[dot+1:]
	if org == "" || pool == "" || clear == "" {
		return "", "", "", false
	}
	return org, pool, clear, true
}

// handle is the runner credential the forge mints: an org-qualified opaque id.
// Every op but register arrives with nothing but this and a token, and the org
// is what says which store holds the runner — so the org travels in the handle
// rather than being hunted for across every org file on the host.
func handle(org string) string { return org + "/" + mint.ID("runner") }

// orgOf reads the org back out of a handle.
func orgOf(h string) string {
	if i := strings.Index(h, "/"); i > 0 {
		return h[:i]
	}
	return ""
}

func joinLabels(l []string) string { return strings.Join(l, ",") }
func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// DeclarePool records capacity under a name, and answers with the secret a
// daemon joins it by. The secret is shown once: only its digest is stored.
func (s *Store) DeclarePool(ctx context.Context, org, name string, labels []string) (Pool, string, error) {
	if err := dialect.Text(org, name, joinLabels(labels)); err != nil {
		return Pool{}, "", fmt.Errorf("declare pool: %w", err)
	}
	clear, hash, salt := credential()
	p := Pool{Org: org, Name: name, Labels: labels, CreatedAt: time.Now().Unix(), hash: hash, salt: salt}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pool (org,name,labels,secret_hash,secret_salt,created_at) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT(org,name) DO UPDATE SET labels=excluded.labels,
		   secret_hash=excluded.secret_hash, secret_salt=excluded.secret_salt`,
		org, name, joinLabels(labels), hash, salt, p.CreatedAt)
	if err != nil {
		return Pool{}, "", fmt.Errorf("declare pool: %w", err)
	}
	return p, join(org, name, clear), nil
}

// Pool reads one declared pool.
func (s *Store) Pool(ctx context.Context, org, name string) (Pool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT org,name,labels,secret_hash,secret_salt,created_at FROM pool WHERE org=$1 AND name=$2`, org, name)
	var p Pool
	var labels string
	err := row.Scan(&p.Org, &p.Name, &labels, &p.hash, &p.salt, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Pool{}, errNoPool
	}
	if err != nil {
		return Pool{}, fmt.Errorf("get pool: %w", err)
	}
	p.Labels = splitLabels(labels)
	return p, nil
}

// Pools lists an org's declared capacity.
func (s *Store) Pools(ctx context.Context, org string) ([]Pool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org,name,labels,secret_hash,secret_salt,created_at FROM pool WHERE org=$1 ORDER BY name`, org)
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}
	defer rows.Close()
	var out []Pool
	for rows.Next() {
		var p Pool
		var labels string
		if err := rows.Scan(&p.Org, &p.Name, &labels, &p.hash, &p.salt, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Labels = splitLabels(labels)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Enter registers a daemon into the pool the join secret names, and answers with
// the runner and the token it authenticates with ever after. A secret that names
// a pool nobody declared has no row to match and is refused — which is the whole
// of the rule that capacity is declared and never inferred.
func (s *Store) Enter(ctx context.Context, org, pool, clear string, r Runner) (Runner, string, error) {
	p, err := s.Pool(ctx, org, pool)
	if err != nil {
		return Runner{}, "", err
	}
	if !matches(clear, p.hash, p.salt) {
		return Runner{}, "", errNoPool
	}
	if err := dialect.Text(r.Name, r.Version, joinLabels(r.Labels)); err != nil {
		return Runner{}, "", fmt.Errorf("register runner: %w", err)
	}
	token, hash, salt := credential()
	r.UUID, r.Org, r.Pool = handle(org), org, pool
	r.hash, r.salt = hash, salt
	r.CreatedAt = time.Now().Unix()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO runner (uuid,org,pool,name,token_hash,token_salt,version,labels,ephemeral,cancelling,last_online,last_active,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		r.UUID, r.Org, r.Pool, r.Name, hash, salt, r.Version, joinLabels(r.Labels),
		bit(r.Ephemeral), bit(r.Cancelling), r.CreatedAt, r.CreatedAt, r.CreatedAt)
	if err != nil {
		return Runner{}, "", fmt.Errorf("register runner: %w", err)
	}
	return r, token, nil
}

// Runner resolves a daemon from the credential it presents. A uuid that is
// unknown and a token that does not match give the SAME answer, so the call
// cannot be used to learn which handles exist.
func (s *Store) Runner(ctx context.Context, uuid, token string) (Runner, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT uuid,org,pool,name,token_hash,token_salt,version,labels,ephemeral,cancelling,last_online,last_active,created_at
		 FROM runner WHERE uuid=$1`, uuid)
	r, err := scanRunner(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Runner{}, errNoRunner
	}
	if err != nil {
		return Runner{}, fmt.Errorf("get runner: %w", err)
	}
	if !matches(token, r.hash, r.salt) {
		return Runner{}, errNoRunner
	}
	return r, nil
}

func scanRunner(sc interface{ Scan(...any) error }) (Runner, error) {
	var r Runner
	var labels string
	err := sc.Scan(&r.UUID, &r.Org, &r.Pool, &r.Name, &r.hash, &r.salt, &r.Version,
		&labels, &r.Ephemeral, &r.Cancelling, &r.LastOnline, &r.LastActive, &r.CreatedAt)
	r.Labels = splitLabels(labels)
	return r, err
}

// Runners lists an org's registered daemons.
func (s *Store) Runners(ctx context.Context, org string) ([]Runner, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT uuid,org,pool,name,token_hash,token_salt,version,labels,ephemeral,cancelling,last_online,last_active,created_at
		 FROM runner WHERE org=$1 ORDER BY created_at DESC`, org)
	if err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}
	defer rows.Close()
	var out []Runner
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Seen records that a runner called, and whether it was executing when it did.
// Both instants are written on the same row visit because with a fleet polling
// every few seconds this write is most of what the runner table costs.
func (s *Store) Seen(ctx context.Context, uuid string, working bool, now time.Time) error {
	q := `UPDATE runner SET last_online=$1 WHERE uuid=$2`
	if working {
		q = `UPDATE runner SET last_online=$1, last_active=$1 WHERE uuid=$2`
	}
	_, err := s.db.ExecContext(ctx, q, now.Unix(), uuid)
	return err
}

// Redeclare stores what a runner says about itself.
func (s *Store) Redeclare(ctx context.Context, uuid, version string, labels []string, cancelling bool) error {
	if err := dialect.Text(version, joinLabels(labels)); err != nil {
		return fmt.Errorf("redeclare runner: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE runner SET version=$1, labels=$2, cancelling=$3 WHERE uuid=$4`,
		version, joinLabels(labels), bit(cancelling), uuid)
	return err
}

// Queue reports the org's queue version.
func (s *Store) Queue(ctx context.Context, org string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM queue WHERE org=$1`, org).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}

// bump advances the org's queue version so a polling runner stops short-circuiting.
func (s *Store) bump(ctx context.Context, org string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO queue (org,version) VALUES ($1,1) ON CONFLICT(org) DO UPDATE SET version=queue.version+1`, org)
	return err
}

// Job is one job of a workflow and the declared pool that will execute it.
type Job struct {
	Name string
	Pool string
}

// Open records a run and its tasks in ONE transaction, and answers with the run.
//
// It is idempotent on (repo, commit, workflow): a redelivered trigger finds the
// existing row and creates no second run, so the dispatcher may retry as often
// as it likes and a runner can never be handed the same commit twice.
func (s *Store) Open(ctx context.Context, r Run, jobs []Job) (Run, bool, error) {
	texts := []string{r.Org, r.Project, r.Repo, r.Ref, r.Commit, r.Event, r.Workflow, r.Actor}
	for _, j := range jobs {
		texts = append(texts, j.Name, j.Pool)
	}
	if err := dialect.Text(texts...); err != nil {
		return Run{}, false, fmt.Errorf("open run: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Run{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var have Run
	err = tx.QueryRowContext(ctx,
		`SELECT id,org,project,repo,ref,commit_sha,event,workflow,doc,actor,status,number,created_at,updated_at
		 FROM run WHERE org=$1 AND project=$2 AND repo=$3 AND commit_sha=$4 AND workflow=$5`,
		r.Org, r.Project, r.Repo, r.Commit, r.Workflow).Scan(
		&have.ID, &have.Org, &have.Project, &have.Repo, &have.Ref, &have.Commit,
		&have.Event, &have.Workflow, &have.Doc, &have.Actor, &have.Status, &have.Number,
		&have.CreatedAt, &have.UpdatedAt)
	if err == nil {
		return have, false, nil // already open: the redelivery is a no-op
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, fmt.Errorf("find run: %w", err)
	}

	var last int64
	_ = tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(number),0) FROM run WHERE org=$1 AND project=$2 AND repo=$3`,
		r.Org, r.Project, r.Repo).Scan(&last)

	now := time.Now().Unix()
	r.ID, r.Number, r.Status = mint.ID("run"), last+1, Waiting
	r.CreatedAt, r.UpdatedAt = now, now
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run (id,org,project,repo,ref,commit_sha,event,workflow,doc,actor,status,number,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		r.ID, r.Org, r.Project, r.Repo, r.Ref, r.Commit, r.Event, r.Workflow, r.Doc,
		r.Actor, r.Status, r.Number, r.CreatedAt, r.UpdatedAt); err != nil {
		return Run{}, false, fmt.Errorf("insert run: %w", err)
	}
	for _, j := range jobs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task (run,org,pool,job,status,created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			r.ID, r.Org, j.Pool, j.Name, Waiting, now); err != nil {
			return Run{}, false, fmt.Errorf("insert task: %w", err)
		}
		if err := stateJob(ctx, tx, r, j.Name, Waiting); err != nil {
			return Run{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO queue (org,version) VALUES ($1,1) ON CONFLICT(org) DO UPDATE SET version=queue.version+1`,
		r.Org); err != nil {
		return Run{}, false, fmt.Errorf("bump queue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	return r, true, nil
}

// Lease hands the oldest waiting task in the runner's pool to that runner, or
// reports none. The claim is a conditional UPDATE, so two runners polling at the
// same instant cannot both win it.
func (s *Store) Lease(ctx context.Context, r Runner) (Task, Run, bool, error) {
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Task{}, Run{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM task WHERE org=$1 AND pool=$2 AND status=$3 ORDER BY id LIMIT 1`,
		r.Org, r.Pool, Waiting).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, Run{}, false, nil
	}
	if err != nil {
		return Task{}, Run{}, false, fmt.Errorf("find task: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE task SET status=$1, runner=$2, started=$3 WHERE id=$4 AND status=$5`,
		Running, r.UUID, time.Now().UnixNano(), id, Waiting)
	if err != nil {
		return Task{}, Run{}, false, fmt.Errorf("claim task: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Task{}, Run{}, false, nil // another runner won it
	}
	t, err := taskAt(ctx, tx, id)
	if err != nil {
		return Task{}, Run{}, false, err
	}
	run, err := runAt(ctx, tx, t.Run)
	if err != nil {
		return Task{}, Run{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run SET status=$1, updated_at=$2 WHERE id=$3`,
		Running, time.Now().Unix(), run.ID); err != nil {
		return Task{}, Run{}, false, err
	}
	run.Status = Running
	if err := tx.Commit(); err != nil {
		return Task{}, Run{}, false, err
	}
	return t, run, true, nil
}

type rowSource interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func taskAt(ctx context.Context, q rowSource, id int64) (Task, error) {
	var t Task
	err := q.QueryRowContext(ctx,
		`SELECT id,run,org,pool,job,runner,status,steps,started,stopped,log_length,log_size,log_sealed,created_at
		 FROM task WHERE id=$1`, id).Scan(
		&t.ID, &t.Run, &t.Org, &t.Pool, &t.Job, &t.Runner, &t.Status, &t.Steps,
		&t.Started, &t.Stopped, &t.LogLength, &t.LogSize, &t.LogSealed, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, errNoTask
	}
	return t, err
}

func runAt(ctx context.Context, q rowSource, id string) (Run, error) {
	var r Run
	err := q.QueryRowContext(ctx,
		`SELECT id,org,project,repo,ref,commit_sha,event,workflow,doc,actor,status,number,created_at,updated_at
		 FROM run WHERE id=$1`, id).Scan(
		&r.ID, &r.Org, &r.Project, &r.Repo, &r.Ref, &r.Commit, &r.Event, &r.Workflow, &r.Doc,
		&r.Actor, &r.Status, &r.Number, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, errNotFound
	}
	return r, err
}

// Task reads one task.
func (s *Store) Task(ctx context.Context, id int64) (Task, error) { return taskAt(ctx, s.db, id) }

// Run reads one run.
func (s *Store) Run(ctx context.Context, id string) (Run, error) { return runAt(ctx, s.db, id) }

// Tasks reads one run's tasks, in the order they were created.
func (s *Store) Tasks(ctx context.Context, run string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,run,org,pool,job,runner,status,steps,started,stopped,log_length,log_size,log_sealed,created_at
		 FROM task WHERE run=$1 ORDER BY id`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Run, &t.Org, &t.Pool, &t.Job, &t.Runner, &t.Status, &t.Steps,
			&t.Started, &t.Stopped, &t.LogLength, &t.LogSize, &t.LogSealed, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Runs lists an org's runs, newest first.
func (s *Store) Runs(ctx context.Context, org, project, repo string, limit int) ([]Run, error) {
	q := `SELECT id,org,project,repo,ref,commit_sha,event,workflow,doc,actor,status,number,created_at,updated_at
	      FROM run WHERE org=$1`
	args := []any{org}
	if repo != "" {
		q += ` AND project=$2 AND repo=$3`
		args = append(args, project, repo)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Org, &r.Project, &r.Repo, &r.Ref, &r.Commit,
			&r.Event, &r.Workflow, &r.Doc, &r.Actor, &r.Status, &r.Number, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Report records a task's progress and its run's, and answers with the result
// the store now holds — which is how a runner learns its task was stopped from
// somewhere else.
func (s *Store) Report(ctx context.Context, id int64, status string, stopped int64, steps string) (Task, error) {
	if err := dialect.Text(status, steps); err != nil {
		return Task{}, fmt.Errorf("report task: %w", err)
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback() }()

	t, err := taskAt(ctx, tx, id)
	if err != nil {
		return Task{}, err
	}
	// A task already finished keeps the result it finished with: a late report
	// from a runner that has been cancelled must not resurrect it.
	if done(t.Status) {
		return t, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET status=$1, stopped=$2, steps=$3 WHERE id=$4`,
		status, stopped, steps, id); err != nil {
		return Task{}, err
	}
	runStatus := status
	if !done(status) {
		runStatus = Running
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run SET status=$1, updated_at=$2 WHERE id=$3`,
		runStatus, time.Now().Unix(), t.Run); err != nil {
		return Task{}, err
	}
	if r, err := runAt(ctx, tx, t.Run); err == nil {
		if err := stateJob(ctx, tx, r, t.Job, status); err != nil {
			return Task{}, err
		}
	}
	t.Status, t.Stopped, t.Steps = status, stopped, steps
	return t, tx.Commit()
}

// done reports whether a status is final.
func done(s string) bool {
	switch s {
	case Success, Failure, Cancelled, Skipped:
		return true
	}
	return false
}

// Record stores one of a task's outputs, keeping the first value written under a
// name: a runner resends outputs it has had no acknowledgement for.
func (s *Store) Record(ctx context.Context, task int64, name, value string) error {
	if err := dialect.Text(name, value); err != nil {
		return fmt.Errorf("record output: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO task_output (task,name,value) VALUES ($1,$2,$3) ON CONFLICT(task,name) DO NOTHING`,
		task, name, value)
	return err
}

// Recorded names the outputs a task has stored, so a runner stops resending them.
func (s *Store) Recorded(ctx context.Context, task int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM task_output WHERE task=$1 ORDER BY name`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Wrote advances a task's durable log position.
func (s *Store) Wrote(ctx context.Context, id, lines, bytes int64, seal bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE task SET log_length=log_length+$1, log_size=log_size+$2, log_sealed=CASE WHEN $3 > 0 THEN 1 ELSE log_sealed END WHERE id=$4`,
		lines, bytes, bit(seal), id)
	return err
}

// ── The public control surface. ───────────────────────────────────────────────
//
// These are /v1/git operations like every other: a person or an agent asking
// this plugin about the work a repository does. "Show me the runners", "start a
// run", "which pool can execute this workflow" are Git questions, so they are
// answered at the Git address, by the same principal the rest of /v1/git uses.
// The five machine operations a daemon calls are not here; they are runner.go.

// poolDeclare declares execution capacity under a name.
type poolDeclare struct {
	// Name is the pool's handle in this org, e.g. "evo" or "spark".
	Name string `json:"name"`
	// Labels are what a workflow's `runs-on:` selects this pool by. A job is
	// assigned to a pool that carries EVERY label it asked for.
	Labels []string `json:"labels"`
}

// poolView is a declared pool as a caller sees it. The join secret is answered
// once, by declarePool, and is never readable back.
type poolView struct {
	// Name is the pool's handle in this org.
	Name string `json:"name"`
	// Labels are what a workflow's `runs-on:` selects this pool by.
	Labels []string `json:"labels"`
	// Runners is how many daemons have entered the pool. Zero means the
	// capacity is declared and nothing has turned up to provide it.
	Runners int `json:"runners"`
	// CreatedAt is when the pool was declared, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// poolDeclared is the answer to declaring a pool: the pool, and the secret a
// daemon joins it with. The secret is shown HERE AND NOWHERE ELSE — only its
// digest is stored — so a lost one is replaced by declaring the pool again.
type poolDeclared struct {
	// Pool is the capacity that now exists.
	Pool poolView `json:"pool"`
	// Secret is what a runner daemon presents to enter the pool. It is answered
	// HERE AND NOWHERE ELSE — only a digest is stored — so a lost one is
	// replaced by declaring the pool again.
	Secret string `json:"secret"`
}

type poolList struct {
	// Data is the org's declared pools, by name.
	Data []poolView `json:"data"`
}

// declarePool records the capacity an org has, and answers with the secret a
// runner daemon presents to enter it.
//
// Declaring is the ONLY way capacity comes to exist: a daemon cannot register
// against a pool nobody declared, because the secret it would have to present
// does not exist until this runs. Re-declaring an existing pool replaces its
// labels and mints a fresh secret; runners already inside it keep working.
//
// Example: {"name": "evo", "labels": ["ubuntu-latest", "linux/amd64"]}
func (o ops) declarePool(ctx context.Context, in *poolDeclare) (*poolDeclared, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || strings.ContainsAny(name, "/.") {
		return nil, zip.ErrBadRequest("pool name is required and may not contain '/' or '.'")
	}
	if len(in.Labels) == 0 {
		return nil, zip.ErrBadRequest("a pool with no labels can execute nothing")
	}
	st, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	p, secret, err := st.DeclarePool(ctx, t.org, name, in.Labels)
	if err != nil {
		return nil, internalErr(err)
	}
	return &poolDeclared{Pool: poolView{Name: p.Name, Labels: p.Labels, CreatedAt: p.CreatedAt}, Secret: secret}, nil
}

// listPools returns the capacity this org has declared and how many daemons have
// entered each pool.
func (o ops) listPools(ctx context.Context, _ *cloud.Unit) (*poolList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	st, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	pools, err := st.Pools(ctx, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	runners, err := st.Runners(ctx, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	out := make([]poolView, 0, len(pools))
	for _, p := range pools {
		n := 0
		for _, r := range runners {
			if r.Pool == p.Name {
				n++
			}
		}
		out = append(out, poolView{Name: p.Name, Labels: p.Labels, Runners: n, CreatedAt: p.CreatedAt})
	}
	return &poolList{Data: out}, nil
}

// runnerView is one registered daemon as a caller sees it. No credential of any
// kind appears here: the token is stored as a digest and the handle is the org's
// to know, not a reader's.
type runnerView struct {
	// Name is what the daemon called itself when it registered.
	Name string `json:"name"`
	// Pool is the declared capacity it entered; for a runner on the forge, the
	// label set it answers to, sorted and joined by "+".
	Pool string `json:"pool"`
	// Forge is true for a runner registered on the forge (git.hanzo.ai), which
	// runs the repositories whose home is there, and false for a daemon in one of
	// this org's declared pools.
	Forge bool `json:"forge"`
	// Online is whether it was heard from lately: within the forge's offline
	// window for a forge runner, within a minute for a daemon here.
	Online bool `json:"online"`
	// Busy is whether it is executing a job now. Known for a forge runner only.
	Busy bool `json:"busy,omitempty"`
	// Labels are what the daemon last said it can do.
	Labels []string `json:"labels"`
	// Version is the runner build the daemon last reported.
	Version string `json:"version"`
	// Ephemeral means the daemon takes ONE job and exits, so it is not expected
	// back and its absence is not a fault.
	Ephemeral bool `json:"ephemeral"`
	// LastOnline is when it last called at all, in unix seconds.
	LastOnline int64 `json:"lastOnline"`
	// LastActive is when it was last EXECUTING a job, in unix seconds. It lags
	// LastOnline on an idle daemon, which is how the two differ.
	LastActive int64 `json:"lastActive"`
}

type runnerList struct {
	// Data is the runners that can run the caller's work: the forge's, then the
	// daemons registered into this org's pools, newest first.
	Data []runnerView `json:"data"`
	// Pools is each label set a forge runner answers to or a queued job asks for,
	// with how many runners carry it, how many of those are online, and how many
	// of the caller's jobs wait on it. A pool with jobs queued and no runner
	// online is a dead pool.
	Pools []poolHealth `json:"pools"`
	// Fault says why the forge's runners are missing from this answer, when the
	// forge could not be read; the daemons here are listed either way.
	Fault string `json:"fault,omitempty"`
}

// listRunners returns the runners that can run the caller's work, each with
// when it was last heard from, and the health of every pool.
//
// First the forge's: the runners registered on git.hanzo.ai, which run every
// repository whose home is there — the platform starts one for each job the
// forge queues, labelled as the job asked. Then the daemons registered into this
// org's own declared pools. Pools groups the forge's by label set, with the
// caller's jobs queued on each, so a label set with work waiting and no runner
// online — a dead pool — is visible.
//
// The forge's runners serve every tenant and are named for the repository whose
// job they took, so they are listed by name to a SuperAdmin; anyone else sees
// the pools their own queued jobs wait on — the jobs of repositories the forge
// lets them read — with how many runners carry each and when one was last heard.
func (o ops) listRunners(ctx context.Context, _ *cloud.Unit) (*runnerList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	fleet, pools, fault := forgeRunners(ctx, t)
	st, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	rs, err := st.Runners(ctx, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	out := append(make([]runnerView, 0, len(fleet)+len(rs)), fleet...)
	for _, r := range rs {
		out = append(out, runnerView{Name: r.Name, Pool: r.Pool, Labels: r.Labels,
			Version: r.Version, Ephemeral: r.Ephemeral,
			Online:     time.Since(time.Unix(r.LastOnline, 0)) < heard,
			LastOnline: r.LastOnline, LastActive: r.LastActive})
	}
	return &runnerList{Data: out, Pools: pools, Fault: fault}, nil
}

// heard is how long after a daemon here last called it still reads as online:
// the forge's own window for its runners (RunnerOfflineTime).
const heard = time.Minute

// workflowRun is one run of one workflow, as a caller sees it. Named for what it
// is a run OF: an agent run and a container run are different things with the
// same short word, and one document carries all three.
type workflowRun struct {
	// ID addresses this run.
	ID string `json:"id"`
	// Number is the run's position in its repository, counting from 1 — the
	// handle a person uses ("#4").
	Number int64 `json:"number"`
	// Repo is the repository the run is for.
	Repo string `json:"repo"`
	// Ref is the full ref, e.g. "refs/heads/main".
	Ref string `json:"ref"`
	// Commit is the commit being run, in full.
	Commit string `json:"commit"`
	// Event is what caused the run, e.g. "push".
	Event string `json:"event"`
	// Workflow is the path of the document being run, e.g.
	// ".hanzo/workflows/ci.yml".
	Workflow string `json:"workflow"`
	// Actor is who caused it.
	Actor string `json:"actor"`
	// Status is waiting, running, success, failure, cancelled or skipped: waiting
	// until a runner takes it (or, on the forge, while a job it needs is still
	// going), running while it executes, and then how it ended.
	Status string `json:"status"`
	// Title is the run's display title on the forge, usually its commit's subject.
	Title string `json:"title,omitempty"`
	// Attempt counts the forge's re-runs of this run, from 1.
	Attempt int `json:"attempt,omitempty"`
	// URL is the run's page on the forge.
	URL string `json:"url,omitempty"`
	// CreatedAt is when the run was opened, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when its status last changed, in unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
	// StartedAt is when it started executing, RFC 3339; absent until it has.
	StartedAt time.Time `json:"startedAt,omitzero"`
	// EndedAt is when it finished, RFC 3339; absent until it has.
	EndedAt time.Time `json:"endedAt,omitzero"`
	// Jobs are the run's jobs in the order they were created, with their steps:
	// answered by `runs get`, and by `runs list` when a ref names one commit.
	Jobs []runJob `json:"jobs,omitempty"`
}

// runJob is one job of a run.
type runJob struct {
	// ID addresses the job, as `runs logs --job` takes it.
	ID int64 `json:"id"`
	// Name is the job's name in the workflow document.
	Name string `json:"name"`
	// Status is waiting, running, or how the job ended, as a run's is.
	Status string `json:"status"`
	// Runner is the runner that took the job; empty until one has.
	Runner string `json:"runner,omitempty"`
	// Labels are what the job's runs-on asked for.
	Labels []string `json:"labels,omitempty"`
	// URL is the job's page on the forge.
	URL string `json:"url,omitempty"`
	// StartedAt is when it started, RFC 3339; absent until it has.
	StartedAt time.Time `json:"startedAt,omitzero"`
	// EndedAt is when it finished, RFC 3339; absent until it has.
	EndedAt time.Time `json:"endedAt,omitzero"`
	// Steps are the job's steps in order: where in it a failure was.
	Steps []runStep `json:"steps,omitempty"`
}

// runStep is one step of a job.
type runStep struct {
	// Number is the step's position in its job, from 1.
	Number int `json:"number"`
	// Name is the step's name in the workflow document.
	Name string `json:"name"`
	// Status is waiting, running, or how the step ended.
	Status string `json:"status"`
}

func view(r Run) workflowRun {
	return workflowRun{ID: r.ID, Number: r.Number, Repo: r.Repo, Ref: r.Ref, Commit: r.Commit,
		Event: r.Event, Workflow: r.Workflow, Actor: r.Actor, Status: r.Status,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type workflowRuns struct {
	// Data is the runs in scope, newest first.
	Data []workflowRun `json:"data"`
	// Commit is the commit a ref named, when the listing asked for one: the runs
	// are the ones on it, and a branch that moves does not move them.
	Commit string `json:"commit,omitempty"`
	// State answers "is this ref green?" when the listing named a forge ref:
	// passed, failed, running, queued or none. It is read over the latest attempt
	// of each workflow on the commit: any failed or cancelled run or job is
	// failed; otherwise any still going is running, any waiting for a runner is
	// queued, and when everything finished as success or skipped it is passed.
	// none means nothing ran.
	State string `json:"state,omitempty"`
}

// runQuery narrows a run listing.
type runQuery struct {
	// Repo restricts the listing to one repository. owner/name, such as
	// hanzo-inc/cloud, is a repository on the forge, and its runs are the forge's;
	// a bare name is one this app holds. Empty lists the whole org's runs here.
	Repo string `json:"repo,omitempty"`
	// Ref keeps one branch, tag or commit. On the forge it is resolved to its
	// commit and every run on that commit is answered with its jobs, and State
	// says whether the ref is green; here it keeps the runs of that branch.
	Ref string `json:"ref,omitempty"`
	// Limit caps the answer; 0 means the default of 50, and 200 is the ceiling.
	Limit int `json:"limit,omitempty"`
}

// listRuns returns this org's runs, newest first.
//
// A repository named owner/name is on the forge, and its runs are the forge's:
// the newest, or, when a ref is named, every run on the commit it resolves to,
// each with its jobs and steps and the ref's verdict in State.
//
// Example: {"repo": "hanzo-inc/cloud", "ref": "v8.5.1131"}
func (o ops) listRuns(ctx context.Context, in *runQuery) (*workflowRuns, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if owner, name, ok := place(in.Repo); ok {
		return forgeRuns(ctx, t, owner, name, strings.TrimSpace(in.Ref), in.Limit)
	}
	st, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rs, err := st.Runs(ctx, t.org, t.project, strings.TrimSuffix(in.Repo, ".git"), limit)
	if err != nil {
		return nil, internalErr(err)
	}
	ref := strings.TrimSpace(in.Ref)
	out := make([]workflowRun, 0, len(rs))
	for _, r := range rs {
		if ref == "" || r.Ref == ref || r.Ref == "refs/heads/"+ref || r.Commit == ref {
			out = append(out, view(r))
		}
	}
	return &workflowRuns{Data: out}, nil
}

// runRef addresses one run by id.
type runRef struct {
	// ID is the run to read, from the :id path segment.
	ID string `json:"id"`
	// Repo is the forge repository the run belongs to, owner/name; empty for a
	// run this app scheduled.
	Repo string `json:"repo,omitempty"`
}

// getRun returns one run with its jobs and their steps.
//
// A run of a forge repository is addressed by its id on the forge and the
// repository, owner/name; one this app scheduled by its id alone.
//
// Example: {"id": "17677", "repo": "hanzo-inc/cloud"}
func (o ops) getRun(ctx context.Context, in *runRef) (*workflowRun, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if owner, name, ok := place(in.Repo); ok {
		return forgeRun(ctx, t, owner, name, in.ID)
	}
	st, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	r, err := st.Run(ctx, in.ID)
	if errors.Is(err, errNotFound) || err == nil && r.Org != t.org {
		return nil, zip.ErrNotFound("run not found")
	}
	if err != nil {
		return nil, internalErr(err)
	}
	v := view(r)
	tasks, err := st.Tasks(ctx, r.ID)
	if err != nil {
		return nil, internalErr(err)
	}
	for _, k := range tasks {
		v.Jobs = append(v.Jobs, taskView(k))
	}
	return &v, nil
}

// runStart asks for a run of a repository's workflows at a ref.
type runStart struct {
	// Repo is the repository to run.
	Repo string `json:"repo"`
	// Ref is the branch to run; empty means the repository's default.
	Ref string `json:"ref,omitempty"`
}

// startRun runs a repository's workflows at a ref, on demand.
//
// It takes the SAME path a push takes: the request is recorded in the journal
// and delivered from there, so an explicit run and a pushed one are one
// mechanism with one idempotency rule and not two that can disagree. Asking
// twice for the same commit yields the same run.
//
// Example: {"repo": "widgets", "ref": "main"}
func (o ops) startRun(ctx context.Context, in *runStart) (*workflowRuns, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(strings.TrimSpace(in.Repo), ".git")
	if name == "" {
		return nil, zip.ErrBadRequest("repo is required")
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	r, err := store.Get(ctx, t.org, t.project, name)
	if err != nil {
		return nil, zip.ErrNotFound("repo not found")
	}
	repo, done, err := open(ctx, o.s, r)
	if err != nil {
		return nil, unheld(err)
	}
	defer done()
	rev, branch, err := repo.Resolve(ctx, strings.TrimPrefix(in.Ref, "refs/heads/"))
	if err != nil {
		return nil, zip.ErrNotFound("ref not found")
	}
	st, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	commit := rev.String()
	ref := "refs/heads/" + branch
	e := Note{ID: fact(t.org, t.project, name, ref, commit), Org: t.org, Project: t.project,
		Repo: name, Ref: ref, Commit: commit, Pusher: t.user}
	if err := st.Owe(ctx, e); err != nil {
		return nil, internalErr(err)
	}
	if err := deliver(o.s, ctx, st, e); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if err := st.Took(ctx, e.ID); err != nil {
		o.s.Log.Warn("git: acknowledge started run", "org", t.org, "entry", e.ID, "err", err)
	}
	rs, err := st.Runs(ctx, t.org, t.project, name, 50)
	if err != nil {
		return nil, internalErr(err)
	}
	out := make([]workflowRun, 0, len(rs))
	for _, r := range rs {
		if r.Commit == commit {
			out = append(out, view(r))
		}
	}
	return &workflowRuns{Data: out}, nil
}

// jobView is one job of a workflow and the pool that would execute it. Pool is
// empty when no declared pool carries the labels the job asked for — which is
// the answer to "why is nothing running": nobody declared the capacity.
type jobView struct {
	// Name is the job's id in the workflow document.
	Name string `json:"name"`
	// RunsOn is the labels the job asked for.
	RunsOn []string `json:"runsOn"`
	// Uses is the reusable workflow the job calls instead of running steps of its
	// own, such as hanzoai/ci/.github/workflows/build.yml@v2.1.21; its jobs run
	// where that workflow says.
	Uses string `json:"uses,omitempty"`
	// Needs are the jobs this one waits for.
	Needs []string `json:"needs,omitempty"`
	// Pool is where the job would run. Here it is the declared pool carrying
	// every one of those labels, or empty when no pool does — which is the answer
	// to "why is nothing running". On the forge it is the label set, sorted and
	// joined by "+", that the platform starts a runner for when the job queues,
	// and empty when no node in the cluster can run it (macOS, Windows) or the job
	// asks for nothing.
	Pool string `json:"pool"`
}

// workflowView is one workflow document at a ref, and where each of its jobs
// would run.
type workflowView struct {
	// Name is the document's path in the repository.
	Name string `json:"name"`
	// Jobs is what it declares, and where each would run.
	Jobs []jobView `json:"jobs"`
	// Fault is why this document cannot run at all, when it cannot be read.
	Fault string `json:"fault,omitempty"`
}

type workflowList struct {
	// Data is the workflows the repository declares at the ref.
	Data []workflowView `json:"data"`
	// Commit is the commit the ref resolved to, which the documents were read at.
	Commit string `json:"commit,omitempty"`
}

// workflowQuery names the repository and ref to read workflows at.
type workflowQuery struct {
	// Repo is the repository whose workflows to read: owner/name on the forge,
	// such as hanzo-inc/cloud, or a bare name this app holds.
	Repo string `json:"repo"`
	// Ref is the branch to read them at; empty means the default.
	Ref string `json:"ref,omitempty"`
}

// listWorkflows reports the workflows a repository declares at a ref and which
// declared pool would execute each job — the answer to "would a push here run,
// and where".
//
// A repository named owner/name is read from the forge, at the commit the ref
// resolves to, in the same two directories the forge reads (.hanzo/workflows,
// then .github/workflows).
//
// Example: {"repo": "hanzo-inc/cloud", "ref": "main"}
func (o ops) listWorkflows(ctx context.Context, in *workflowQuery) (*workflowList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if owner, name, ok := place(in.Repo); ok {
		return forgeWorkflows(ctx, t, owner, name, strings.TrimSpace(in.Ref))
	}
	name := strings.TrimSuffix(strings.TrimSpace(in.Repo), ".git")
	if name == "" {
		return nil, zip.ErrBadRequest("repo is required")
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	r, err := store.Get(ctx, t.org, t.project, name)
	if err != nil {
		return nil, zip.ErrNotFound("repo not found")
	}
	repo, done, err := open(ctx, o.s, r)
	if err != nil {
		return nil, unheld(err)
	}
	defer done()
	rev, _, err := repo.Resolve(ctx, strings.TrimPrefix(in.Ref, "refs/heads/"))
	if err != nil {
		return nil, zip.ErrNotFound("ref not found")
	}
	docs, err := workflows(ctx, repo, rev.String())
	if err != nil {
		return nil, internalErr(err)
	}
	st, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	pools, err := st.Pools(ctx, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	out := make([]workflowView, 0, len(docs))
	for _, d := range docs {
		out = append(out, inspect(d, pools))
	}
	return &workflowList{Data: out, Commit: rev.String()}, nil
}

// stateJob states a job's status on its run's commit, in the transaction that
// moves the task, so the status a reader sees and the run beside it cannot differ.
func stateJob(ctx context.Context, q execer, r Run, job, status string) error {
	if !shaRE.MatchString(r.Commit) {
		return nil // a run on something that is not a commit names no commit to state on
	}
	return putStatus(ctx, q, Status{
		Org: r.Org, Project: r.Project, Repo: r.Repo, SHA: r.Commit,
		Context: jobContext(r.Workflow, job, r.Event), State: taskState(status),
		Description: status, Creator: "run",
	})
}
