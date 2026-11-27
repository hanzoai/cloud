package train

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
)

// store is one org's train database: jobs, their tasks, events and metrics, the
// org's clients, its artifacts and the objects holding their bytes. On SQLite it is
// the file train.db; on hanzo-sql, a schema of that name. Its SQL is the dialect both
// engines share (sdk/dialect) and its tests run on both (internal/sqltest).
//
// A column SQL looks inside (an artifact's meta, an event's body) is the dialect's
// JSON type; the rest of a document stays TEXT and is returned as it was written.
// Integers are BIGINT, and a flag is 0 or 1 set from Go (bit).
//
// A correction's note is a sealed column (internal/org, field.go): what a person wrote to
// explain a correction rests as ciphertext, bound to the org, the correction and the column.
type store struct {
	db     *sql.DB
	fields *tenure.Fields
}

// subsystem names the store, and keys its sealed column.
const subsystem = "train"

var correctionKey, correctionSealed = []string{"id"}, []string{"note"}

// schema is the tables, as d spells them.
func schema(d dialect.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  org TEXT NOT NULL DEFAULT '',
  project TEXT NOT NULL,
  base_model TEXT NOT NULL,
  spec ` + d.JSON() + ` NOT NULL,
  status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  published TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL,
  payer TEXT NOT NULL DEFAULT '',
  validated BIGINT NOT NULL DEFAULT 0,
  created BIGINT NOT NULL,
  started BIGINT NOT NULL DEFAULT 0,
  ended BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS jobs_created ON jobs(created);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  job TEXT NOT NULL,
  role TEXT NOT NULL,
  machine TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  lease TEXT NOT NULL DEFAULT '',
  holder TEXT NOT NULL DEFAULT '',
  host TEXT NOT NULL DEFAULT '',
  coordinator TEXT NOT NULL DEFAULT '',
  class TEXT NOT NULL DEFAULT '',
  devices BIGINT NOT NULL DEFAULT 0,
  rate BIGINT NOT NULL DEFAULT 0,
  held BIGINT NOT NULL DEFAULT 0,
  used BIGINT NOT NULL DEFAULT 0,
  claimed BIGINT NOT NULL DEFAULT 0,
  seen BIGINT NOT NULL DEFAULT 0,
  created BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS tasks_job ON tasks(job);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status);
CREATE TABLE IF NOT EXISTS events (
  job TEXT NOT NULL,
  seq BIGINT NOT NULL,
  at BIGINT NOT NULL,
  task TEXT NOT NULL DEFAULT '',
  body ` + d.JSON() + ` NOT NULL,
  PRIMARY KEY (job, seq)
);
CREATE TABLE IF NOT EXISTS metrics (
  job TEXT NOT NULL,
  name TEXT NOT NULL,
  step BIGINT NOT NULL,
  value DOUBLE PRECISION NOT NULL,
  at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS metrics_job ON metrics(job, name, step);
CREATE TABLE IF NOT EXISTS artifacts (
  job TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  size BIGINT NOT NULL,
  state TEXT NOT NULL,
  meta ` + d.JSON() + ` NOT NULL DEFAULT '{}',
  payer TEXT NOT NULL DEFAULT '',
  created BIGINT NOT NULL,
  PRIMARY KEY (job, sha256)
);
CREATE INDEX IF NOT EXISTS artifacts_sha ON artifacts(sha256);
CREATE TABLE IF NOT EXISTS uploads (
  job TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  id TEXT NOT NULL,
  PRIMARY KEY (job, sha256)
);
CREATE TABLE IF NOT EXISTS objects (
  sha256 TEXT PRIMARY KEY,
  key TEXT NOT NULL,
  size BIGINT NOT NULL,
  payer TEXT NOT NULL DEFAULT '',
  seal TEXT NOT NULL DEFAULT '',
  stored BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS charges (
  ref TEXT PRIMARY KEY,
  payer TEXT NOT NULL,
  amount_micros BIGINT NOT NULL,
  model TEXT NOT NULL,
  project TEXT NOT NULL,
  request_id TEXT NOT NULL,
  created BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS storage (
  payer TEXT PRIMARY KEY,
  metered_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS clients (
  id TEXT PRIMARY KEY,
  base_model TEXT NOT NULL,
  spec TEXT NOT NULL,
  created_by TEXT NOT NULL,
  created BIGINT NOT NULL,
  gone BIGINT NOT NULL DEFAULT 0
);` + capSchema + polSchema(d)
}

// columns are what a file made before them lacks; open adds each one missing.
var columns = []struct{ table, name, ddl string }{
	{"jobs", "org", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "payer", "TEXT NOT NULL DEFAULT ''"},
	{"jobs", "validated", "BIGINT NOT NULL DEFAULT 0"},
	{"tasks", "class", "TEXT NOT NULL DEFAULT ''"},
	{"tasks", "devices", "BIGINT NOT NULL DEFAULT 0"},
	{"tasks", "rate", "BIGINT NOT NULL DEFAULT 0"},
	{"tasks", "held", "BIGINT NOT NULL DEFAULT 0"},
	{"tasks", "used", "BIGINT NOT NULL DEFAULT 0"},
	{"tasks", "claimed", "BIGINT NOT NULL DEFAULT 0"},
	{"artifacts", "payer", "TEXT NOT NULL DEFAULT ''"},
	{"objects", "seal", "TEXT NOT NULL DEFAULT ''"},
}

// bit is a flag as it is stored: 1 or 0.
func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// openStore opens the org's train database for ns and keys its sealed column. master is
// cloud.Deps.MasterKey; with none the store does not open, so no note is ever written in
// the clear because a key was missing.
func openStore(master []byte) func(namespace.Namespace, *sql.DB) (*store, error) {
	return func(ns namespace.Namespace, db *sql.DB) (*store, error) {
		c, err := tenure.NewCipher(master)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("train: %w", err)
		}
		fields, err := c.Fields(ns, subsystem)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("train: %w", err)
		}
		return migrated(db, fields)
	}
}

func migrated(db *sql.DB, fields *tenure.Fields) (*store, error) {
	d := dialect.Of(db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schema(d)); err != nil {
			return err
		}
		for _, c := range columns {
			has, err := d.HasColumn(ctx, tx, c.table, c.name)
			if err != nil {
				return err
			}
			if has {
				continue
			}
			if _, err := tx.ExecContext(ctx, `ALTER TABLE `+c.table+` ADD COLUMN `+c.name+` `+c.ddl); err != nil {
				return fmt.Errorf("%s.%s: %w", c.table, c.name, err)
			}
		}
		return reshape(ctx, tx)
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("train schema: %w", err)
	}
	// A SQLite file from before the note was sealed holds it in plaintext. Seal it in
	// place, before any read, so the store never reads a value it did not write and
	// cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed, and Upgrade leaves
	// them alone. It is a walk in small transactions of its own, after the schema has
	// committed.
	if _, err := fields.Upgrade(context.Background(), db, "corrections", correctionKey, correctionSealed); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("train schema: %w", err)
	}
	return &store{db: db, fields: fields}, nil
}

// minting holds the lock an org's key is minted under, and answers how to let it go.
// On hanzo-sql it is an advisory lock of this org's schema, taken in a transaction of
// its own that outlives the request (dialect.Held), so the replicas that ask for
// the org's first key at once take it in turn, and one that dies holding it frees it
// with its connection. A SQLite file has one process, and the minting there is
// already one at a time (keys.seal's single flight), so nothing is held: the file's
// one connection must stay free for the statements that are not the mint.
func (s *store) minting(ctx context.Context) (release func(), err error) {
	d := dialect.Of(s.db)
	if !d.Postgres() {
		return func() {}, nil
	}
	_, held, err := dialect.Held(ctx, dialect.LockWait, func(ctx context.Context) (*sql.Tx, error) {
		return d.Serialize(ctx, s.db, "key")
	})
	if err != nil {
		return nil, fmt.Errorf("train: lock the org's key: %w", err)
	}
	return func() { _ = held() }, nil
}

// reshape carries stored specs to the current shape: a boolean
// protect.projection becomes its object form, and resources.seconds becomes
// resources.max_seconds. Stored artifacts a file kept before objects existed
// become objects, keyed where they were written and sealed under no key — they
// were stored unencrypted, so a registration of the same bytes is answered a
// sealed upload, not stored — their storage clocked from now: time before the
// meter could see them is never billed.
//
// The specs are rewritten here and not by the engine's JSON functions, which the
// two engines spell differently; the query only finds the few that need it, so a
// store with none reads no spec.
func reshape(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, spec FROM jobs
WHERE CAST(spec -> 'protect' -> 'projection' AS TEXT) IN ('true', 'false') OR spec -> 'resources' -> 'seconds' IS NOT NULL`)
	if err != nil {
		return err
	}
	type old struct{ id, spec string }
	var found []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.id, &o.spec); err != nil {
			_ = rows.Close()
			return err
		}
		found = append(found, o)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, o := range found {
		spec, changed, err := reshaped(o.spec)
		if err != nil {
			return fmt.Errorf("job %s: %w", o.id, err)
		}
		if !changed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET spec = $1 WHERE id = $2`, spec, o.id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO objects (sha256, key, size, payer, seal, stored)
  SELECT sha256, '', MAX(size), '', '', MIN(created) FROM artifacts WHERE state = 'stored' GROUP BY sha256
ON CONFLICT (sha256) DO NOTHING`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO storage (payer, metered_at)
  SELECT DISTINCT payer, CAST($1 AS BIGINT) FROM objects WHERE true
ON CONFLICT (payer) DO NOTHING`, unix())
	return err
}

// reshaped is spec in the current shape, and whether it was not already.
func reshaped(spec string) (string, bool, error) {
	dec := json.NewDecoder(strings.NewReader(spec))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return "", false, err
	}
	changed := false
	if protect, ok := doc["protect"].(map[string]any); ok {
		switch protect["projection"] {
		case true:
			protect["projection"], changed = map[string]any{"strength": json.Number("1")}, true
		case false:
			delete(protect, "projection")
			changed = true
		}
	}
	if resources, ok := doc["resources"].(map[string]any); ok {
		if secs, ok := resources["seconds"].(json.Number); ok && whole.MatchString(secs.String()) {
			delete(resources, "seconds")
			resources["max_seconds"], changed = secs, true
		}
	}
	if !changed {
		return spec, false, nil
	}
	out, err := json.Marshal(doc)
	return string(out), true, err
}

// whole is a JSON number with no fraction or exponent: what SQLite's json_type calls an integer.
var whole = regexp.MustCompile(`^-?[0-9]+$`)

func (s *store) Close() error { return s.db.Close() }

// Job statuses (HIP-1333 §5).
const (
	queued     = "queued"
	running    = "running"
	evaluating = "evaluating"
	succeeded  = "succeeded"
	rejected   = "rejected"
	failed     = "failed"
	cancelled  = "cancelled"
	stopped    = "stopped"
)

// terminal reports whether a job status is final.
func terminal(status string) bool {
	switch status {
	case succeeded, rejected, failed, cancelled, stopped:
		return true
	}
	return false
}

// ended is terminal() as SQL: the statuses a job does not leave.
const ended = `('succeeded','rejected','failed','cancelled','stopped')`

// Task statuses: open until claimed, claimed while an executor holds its lease,
// then done, lost (the lease lapsed) or cancelled.
const (
	open    = "open"
	claimed = "claimed"
	done    = "done"
	lost    = "lost"
)

var errNotFound = errors.New("train: not found")

// token is a fresh identifier: prefix and n random bytes in hex.
func token(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// row is a job as stored.
// Validated says Project is bound to a verified claim, so a project's spend cap
// stays hard for every window the job holds.
type row struct {
	ID, Org, Project, Base, Spec, Status, Error, Result, Published, By, Payer string
	Validated                                                                 bool
	Created, Started, Ended                                                   int64
}

const jobCols = `id, org, project, base_model, spec, status, error, result, published, created_by, payer, validated, created, started, ended`

func scanJob(sc interface{ Scan(...any) error }) (row, error) {
	var r row
	err := sc.Scan(&r.ID, &r.Org, &r.Project, &r.Base, &r.Spec, &r.Status, &r.Error, &r.Result, &r.Published, &r.By, &r.Payer, &r.Validated, &r.Created, &r.Started, &r.Ended)
	return r, err
}

// create stores a queued job with its lead task and a join task per further
// machine. org is the org it was created in, as the caller's credential named it;
// payer the wallet its compute and storage are charged to, and validated whether
// project is claim-bound.
func (s *store) create(ctx context.Context, org, project, by, payer string, validated bool, spec *Spec, now int64) (row, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return row{}, err
	}
	if err := dialect.Text(org, project, by, payer, spec.BaseModel); err != nil {
		return row{}, err
	}
	r := row{ID: token("job_", 8), Org: org, Project: project, Base: spec.BaseModel, Spec: string(raw), Status: queued, By: by, Payer: payer, Validated: validated, Created: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return row{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs (`+jobCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		r.ID, r.Org, r.Project, r.Base, r.Spec, r.Status, "", "", "", r.By, r.Payer, bit(r.Validated), r.Created, 0, 0); err != nil {
		return row{}, err
	}
	machines := spec.Resources.Machines
	lead := ""
	if len(machines) > 0 {
		lead = machines[0]
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, job, role, machine, status, created) VALUES ($1,$2,$3,$4,$5,$6)`,
		token("tsk_", 8), r.ID, "lead", lead, open, now); err != nil {
		return row{}, err
	}
	for _, m := range machines[min(1, len(machines)):] {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, job, role, machine, status, created) VALUES ($1,$2,$3,$4,$5,$6)`,
			token("tsk_", 8), r.ID, "join", m, open, now); err != nil {
			return row{}, err
		}
	}
	return r, tx.Commit()
}

func (s *store) job(ctx context.Context, id string) (row, error) {
	r, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, errNotFound
	}
	return r, err
}

func (s *store) jobs(ctx context.Context, status string, limit int) ([]row, error) {
	if status != "" {
		return s.rows(ctx, `SELECT `+jobCols+` FROM jobs WHERE status = $1 ORDER BY created DESC, id DESC LIMIT $2`, status, limit)
	}
	return s.rows(ctx, `SELECT `+jobCols+` FROM jobs ORDER BY created DESC, id DESC LIMIT $1`, limit)
}

func (s *store) rows(ctx context.Context, q string, args ...any) ([]row, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// task is a job's unit of execution on one machine, and what it is held and has
// used: Class (gpu or cpu) and Devices are what its executor claimed it on, Rate
// what one device-hour of it costs in micro-USD (0 on the org's own machines),
// Held the device-seconds held for it, Used those metered by its reports, Claimed
// when it was claimed and Seen when its executor last proved its lease.
type task struct {
	ID, Job, Role, Machine, Status, Lease, Holder, Host, Coordinator, Class string
	Devices, Rate, Held, Used, Claimed, Seen, Created                       int64
}

const taskCols = `id, job, role, machine, status, lease, holder, host, coordinator, class, devices, rate, held, used, claimed, seen, created`

func scanTask(sc interface{ Scan(...any) error }) (task, error) {
	var t task
	err := sc.Scan(&t.ID, &t.Job, &t.Role, &t.Machine, &t.Status, &t.Lease, &t.Holder, &t.Host, &t.Coordinator, &t.Class, &t.Devices, &t.Rate, &t.Held, &t.Used, &t.Claimed, &t.Seen, &t.Created)
	return t, err
}

func (s *store) taskRows(ctx context.Context, q string, args ...any) ([]task, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *store) tasks(ctx context.Context, job string) ([]task, error) {
	return s.taskRows(ctx, `SELECT `+taskCols+` FROM tasks WHERE job = $1 ORDER BY role = 'join', created, id`, job)
}

// candidates are the open tasks placed on machine, oldest job first: a lead, or a
// join whose lead has said where it coordinates. A task placed on no machine is
// Hanzo's (unplaced) and never a machine's.
func (s *store) candidates(ctx context.Context, machine string) ([]task, error) {
	ts, err := s.taskRows(ctx, `
SELECT t.id, t.job, t.role, t.machine, t.status, t.lease, t.holder, t.host,
       COALESCE((SELECT l.coordinator FROM tasks l WHERE l.job = t.job AND l.role = 'lead' ORDER BY l.created, l.id LIMIT 1), ''),
       t.class, t.devices, t.rate, t.held, t.used, t.claimed, t.seen, t.created
FROM tasks t JOIN jobs j ON j.id = t.job
WHERE t.status = 'open' AND j.status IN ('queued', 'running')
  AND t.machine != '' AND t.machine = $1
ORDER BY j.created, t.role = 'join', t.id`, machine)
	if err != nil {
		return nil, err
	}
	out := ts[:0]
	for _, t := range ts {
		if t.Role == "join" && t.Coordinator == "" {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// unplaced are the open lead tasks of the queued jobs that name no machine: the
// work Hanzo's executor claims.
func (s *store) unplaced(ctx context.Context) ([]task, error) {
	return s.taskRows(ctx, `
SELECT `+prefixed("t", taskCols)+` FROM tasks t JOIN jobs j ON j.id = t.job
WHERE t.status = 'open' AND t.role = 'lead' AND t.machine = '' AND j.status = 'queued'
ORDER BY j.created, t.id`)
}

// prefixed qualifies each of a column list's names with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ", ")
	for i, p := range parts {
		parts[i] = alias + "." + p
	}
	return strings.Join(parts, ", ")
}

// claim is what a claim settles about the task it takes: the device class and
// count it runs on, what a device-hour costs, and the first window held.
type claim struct {
	Class         string
	Devices, Rate int64
	Held          int64
}

// take claims an open task for holder on host under a fresh lease, holding its
// first window, and moves a queued job to running when its lead is taken. False
// when another claim won.
func (s *store) take(ctx context.Context, t task, holder, host string, c claim, now int64) (string, bool, error) {
	lease := token("trl_", 32)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = $1, lease = $2, holder = $3, host = $4, claimed = $5, seen = $6, class = $7, devices = $8, rate = $9, held = $10, used = 0 WHERE id = $11 AND status = $12`,
		claimed, digest(lease), holder, host, now, now, c.Class, c.Devices, c.Rate, c.Held, t.ID, open)
	if err != nil {
		return "", false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", false, nil
	}
	if t.Role == "lead" {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = $1, started = $2 WHERE id = $3 AND status = $4`, running, now, t.Job, queued); err != nil {
			return "", false, err
		}
	}
	return lease, true, tx.Commit()
}

// untake gives a task back as it was before take, for a claim that could not be
// recorded on the audit trail. Nothing ran under the lease, so nothing is owed.
func (s *store) untake(ctx context.Context, t task, lease string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = $1, lease = '', holder = '', host = '', claimed = 0, seen = 0, class = '', devices = 0, rate = 0, held = 0, used = 0 WHERE id = $2 AND lease = $3 AND status = $4`,
		open, t.ID, digest(lease), claimed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 && t.Role == "lead" {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = $1, started = 0 WHERE id = $2 AND status = $3`, queued, t.Job, running); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// held resolves a report's or a registration's task: it must be the job's,
// claimed, and under this lease. The seen stamp, which keeps the lease, moves
// with every call.
func (s *store) held(ctx context.Context, job, id, lease string, now int64) (task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = $1 AND job = $2`, id, job))
	if errors.Is(err, sql.ErrNoRows) {
		return task{}, errNotFound
	}
	if err != nil {
		return task{}, err
	}
	if t.Status != claimed || t.Lease != digest(lease) {
		return task{}, errLease
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET seen = $1 WHERE id = $2`, now, id); err != nil {
		return task{}, err
	}
	return t, nil
}

var errLease = errors.New("train: lease lost")

// use moves a task's used device-seconds from was to now, and reports whether
// this call moved them: a compare-and-set, so a span is counted once. The charge
// the span owes, when it owes one, is written to the outbox in the same
// transaction, so the seconds are never counted without what they cost.
func (s *store) use(ctx context.Context, id string, was, now int64, c *charge, at int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET used = $1 WHERE id = $2 AND used = $3 AND status = $4`, now, id, was, claimed)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if c != nil {
		u := c.usage
		if _, err := tx.ExecContext(ctx, `INSERT INTO charges (ref, payer, amount_micros, model, project, request_id, created) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (ref) DO NOTHING`,
			u.Ref, c.payer.String(), u.AmountMicros, u.Model, u.Project, u.RequestID, at); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// owed is the outbox: every charge counted and not yet handed to the meter.
func (s *store) owed(ctx context.Context) ([]*charge, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ref, payer, amount_micros, model, project, request_id FROM charges ORDER BY created, ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*charge
	for rows.Next() {
		var c charge
		var payer string
		if err := rows.Scan(&c.usage.Ref, &payer, &c.usage.AmountMicros, &c.usage.Model, &c.usage.Project, &c.usage.RequestID); err != nil {
			return nil, err
		}
		c.payer = account.Parse(payer)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// paid drops charges handed to the meter. The ledger knows each by its ref, so a
// charge handed over twice — by a process that died before this — moves money once.
func (s *store) paid(ctx context.Context, refs []string) error {
	for _, ref := range refs {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM charges WHERE ref = $1`, ref); err != nil {
			return err
		}
	}
	return nil
}

// hold moves a task's held device-seconds from was to now: a compare-and-set.
func (s *store) hold(ctx context.Context, id string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET held = $1 WHERE id = $2 AND held = $3 AND status = $4`, now, id, was, claimed)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// committed is what a job's tasks have taken from its caps: a live task its held
// device-seconds, a closed one those it used, and what each costs at its rate.
func (s *store) committed(ctx context.Context, job string) (secs, micros int64, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, rate, held, used FROM tasks WHERE job = $1`, job)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var rate, held, used int64
		if err := rows.Scan(&status, &rate, &held, &used); err != nil {
			return 0, 0, err
		}
		n := used
		if status == claimed {
			n = held
		}
		secs += n
		micros += cost(rate, n)
	}
	return secs, micros, rows.Err()
}

// outstanding is what a payer has committed in this org that the ledger does not
// know yet, in micro-USD: what its live priced tasks hold beyond what they used,
// and its charges not yet sent. A gate weighs it, summed over orgs, with the next
// window so two of the payer's jobs cannot each spend one balance.
func (s *store) outstanding(ctx context.Context, payer string) (int64, error) {
	var out int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_micros), 0) FROM charges WHERE payer = $1`, payer).Scan(&out); err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.rate, t.held, t.used FROM tasks t JOIN jobs j ON j.id = t.job
WHERE t.status = $1 AND t.rate > 0 AND j.payer = $2`, claimed, payer)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var rate, held, used int64
		if err := rows.Scan(&rate, &held, &used); err != nil {
			return 0, err
		}
		out += cost(rate, held) - cost(rate, used)
	}
	return out, rows.Err()
}

// expire ends every claimed task silent past ttl. A lost lead fails its job
// executor_lost. It is run before any read of the org's jobs and by every sweep,
// so a lapsed lease is seen by whoever looks next.
func (s *store) expire(ctx context.Context, now, ttl int64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, job, role FROM tasks WHERE status = $1 AND seen < $2`, claimed, now-ttl)
	if err != nil {
		return err
	}
	type gone struct{ id, job, role string }
	var list []gone
	for rows.Next() {
		var g gone
		if err := rows.Scan(&g.id, &g.job, &g.role); err != nil {
			rows.Close()
			return err
		}
		list = append(list, g)
	}
	rows.Close()
	for _, g := range list {
		if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = $1 WHERE id = $2 AND status = $3`, lost, g.id, claimed); err != nil {
			return err
		}
		if g.role != "lead" {
			continue
		}
		if err := s.end(ctx, g.job, failed, "executor_lost: no report for the lead task within the lease", now); err != nil && !errors.Is(err, errTerminal) {
			return err
		}
	}
	return nil
}

var errTerminal = errors.New("train: job already ended")

// end moves a job to a terminal status once; its open or claimed tasks close
// with it, which releases what they held.
func (s *store) end(ctx context.Context, job, status, msg string, now int64) error {
	if err := dialect.Text(msg); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = $1, error = $2, ended = $3 WHERE id = $4 AND status NOT IN `+ended,
		status, msg, now, job)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errTerminal
	}
	_, err = s.db.ExecContext(ctx, `UPDATE tasks SET status = $1 WHERE job = $2 AND status IN ('open','claimed')`, done, job)
	return err
}

// finish records a lead's result and ends the job succeeded or rejected.
func (s *store) finish(ctx context.Context, job, status, result string, now int64) error {
	if err := dialect.Text(result); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = $1, result = $2, ended = $3 WHERE id = $4 AND status IN ('running','evaluating')`,
		status, result, now, job)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errTerminal
	}
	_, err = s.db.ExecContext(ctx, `UPDATE tasks SET status = $1 WHERE job = $2 AND status IN ('open','claimed')`, done, job)
	return err
}

func (s *store) status(ctx context.Context, job, from, to string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = $1 WHERE id = $2 AND status = $3`, to, job, from)
	return err
}

func (s *store) close(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = $1 WHERE id = $2 AND status = $3`, status, id, claimed)
	return err
}

func (s *store) coordinate(ctx context.Context, id, addr string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET coordinator = $1 WHERE id = $2`, addr, id)
	return err
}

// publish writes a job's publish record, and takes its name from any other job of
// the org that held it: one name is one capability. beside is handed the org's
// other records inside the same transaction and refuses one this record cannot
// stand beside.
func (s *store) publish(ctx context.Context, job, name, record string, beside func(others []string) error) error {
	if err := dialect.Text(record); err != nil {
		return err
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// every other published route: the org's jobs' and its installed capabilities'
	rows, err := tx.QueryContext(ctx, `SELECT id, published FROM jobs WHERE id != $1 AND published != '' ORDER BY id`, job)
	if err != nil {
		return err
	}
	var others, held []string
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			_ = rows.Close()
			return err
		}
		others = append(others, p)
		if named(p) == name {
			held = append(held, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	installed, err := s.installedRecords(ctx, tx, "")
	if err != nil {
		return err
	}
	if err := beside(append(others, installed...)); err != nil {
		return err
	}
	for _, id := range held {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET published = '' WHERE id = $1`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET published = $1 WHERE id = $2`, record, job); err != nil {
		return err
	}
	return tx.Commit()
}

// named is the name a publish record carries, "" for one that is not a record.
func named(record string) string {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(record), &p)
	return p.Name
}

// published is every publish record of the org, by job, in the order of their names.
func (s *store) published(ctx context.Context) ([]row, error) {
	rs, err := s.rows(ctx, `SELECT `+jobCols+` FROM jobs WHERE published != '' ORDER BY id`)
	sort.SliceStable(rs, func(i, j int) bool { return named(rs[i].Published) < named(rs[j].Published) })
	return rs, err
}

// event is one entry of a job's log.
type event struct {
	// Seq orders the job's events from 1.
	Seq int64 `json:"seq"`
	// At is when it was recorded, unix seconds.
	At int64 `json:"at"`
	// Task is the task that reported it.
	Task string `json:"task,omitempty"`
	// Body is the event as its executor reported it.
	Body json.RawMessage `json:"event"`
}

// append adds events to a job's log in order and returns the last seq.
func (s *store) append(ctx context.Context, job, taskID string, bodies []json.RawMessage, now int64) (int64, error) {
	for _, b := range bodies {
		if err := dialect.Document(b); err != nil {
			return 0, err
		}
	}
	if err := dialect.Text(taskID); err != nil {
		return 0, err
	}
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events WHERE job = $1`, job).Scan(&seq); err != nil {
		return 0, err
	}
	for _, b := range bodies {
		seq++
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (job, seq, at, task, body) VALUES ($1,$2,$3,$4,$5)`, job, seq, now, taskID, string(b)); err != nil {
			return 0, err
		}
	}
	return seq, tx.Commit()
}

func (s *store) events(ctx context.Context, job string, after int64, limit int) ([]event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, at, task, body FROM events WHERE job = $1 AND seq > $2 ORDER BY seq LIMIT $3`, job, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		var body string
		if err := rows.Scan(&e.Seq, &e.At, &e.Task, &body); err != nil {
			return nil, err
		}
		e.Body = json.RawMessage(body)
		out = append(out, e)
	}
	return out, rows.Err()
}

// point is one metric value.
type point struct {
	Name  string
	Step  int64
	Value float64
}

// live counts the org's jobs that have not ended.
func (s *store) live(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ($1, $2, $3)`, queued, running, evaluating).Scan(&n)
	return n, err
}

// notes counts the log and metric events a job keeps.
func (s *store) notes(ctx context.Context, job string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE job = $1 AND body ->> 'type' IN ('log', 'metric')`, job).Scan(&n)
	return n, err
}

func (s *store) measure(ctx context.Context, job string, ps []point, now int64) error {
	for _, p := range ps {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO metrics (job, name, step, value, at) VALUES ($1,$2,$3,$4,$5)`, job, p.Name, p.Step, p.Value, now); err != nil {
			return err
		}
	}
	return nil
}

// series is a job's metrics by name, each in step order.
func (s *store) series(ctx context.Context, job string) (map[string][][3]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, step, value, at FROM metrics WHERE job = $1 ORDER BY name, step, at`, job)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][][3]float64{}
	for rows.Next() {
		var n string
		var step, at int64
		var v float64
		if err := rows.Scan(&n, &step, &v, &at); err != nil {
			return nil, err
		}
		out[n] = append(out[n], [3]float64{float64(step), v, float64(at)})
	}
	return out, rows.Err()
}

// artifact is one output of a job, or one upload of the org's, by content.
type artifact struct {
	// Job is the job that produced it; empty for an upload.
	Job string `json:"job"`
	// SHA256 is the hex digest of its bytes, and its name in the object store.
	SHA256 string `json:"sha256"`
	// Kind is checkpoint, lora, capability, basis or merged, or dataset for an upload.
	Kind string `json:"kind"`
	// Name labels it.
	Name string `json:"name"`
	// Size is its byte count.
	Size int64 `json:"size"`
	// State is pending until its bytes are read back, then stored; deleted once
	// the org deleted it or it outlived its retention unpublished.
	State string `json:"state"`
	// Meta describes it: its base and revision, its modules, a basis's address.
	Meta json.RawMessage `json:"meta,omitempty"`
	// Created is when it was registered, unix seconds.
	Created int64 `json:"created"`
	// payer is the wallet its storage is charged to.
	payer string
}

const artCols = `job, sha256, kind, name, size, state, meta, payer, created`

func scanArt(sc interface{ Scan(...any) error }) (artifact, error) {
	var a artifact
	var meta string
	err := sc.Scan(&a.Job, &a.SHA256, &a.Kind, &a.Name, &a.Size, &a.State, &meta, &a.payer, &a.Created)
	a.Meta = json.RawMessage(meta)
	return a, err
}

func (s *store) arts(ctx context.Context, q string, args ...any) ([]artifact, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []artifact
	for rows.Next() {
		a, err := scanArt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *store) artifacts(ctx context.Context, job string) ([]artifact, error) {
	return s.arts(ctx, `SELECT `+artCols+` FROM artifacts WHERE job = $1 ORDER BY created, sha256`, job)
}

func (s *store) artifact(ctx context.Context, job, sha string) (artifact, error) {
	a, err := scanArt(s.db.QueryRowContext(ctx, `SELECT `+artCols+` FROM artifacts WHERE job = $1 AND sha256 = $2`, job, sha))
	if errors.Is(err, sql.ErrNoRows) {
		return artifact{}, errNotFound
	}
	return a, err
}

// open records the multipart upload an artifact's bytes arrive through.
func (s *store) open(ctx context.Context, job, sha, id string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO uploads (job, sha256, id) VALUES ($1,$2,$3)
ON CONFLICT(job, sha256) DO UPDATE SET id = excluded.id`, job, sha, id)
	return err
}

// upload is the multipart upload an artifact's bytes arrive through, or "".
func (s *store) upload(ctx context.Context, job, sha string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM uploads WHERE job = $1 AND sha256 = $2`, job, sha).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// holds reports whether the org has these bytes stored, sealed under its key,
// under any job or upload. Bytes stored before objects were sealed are not held:
// they are uploaded again, sealed.
func (s *store) holds(ctx context.Context, sha string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects WHERE sha256 = $1 AND seal != ''`, sha).Scan(&n)
	return n > 0, err
}

// record writes an artifact as registered. A pending one is dated by its latest
// grant, which is what a lapse is measured from.
func (s *store) record(ctx context.Context, a artifact) error {
	if len(a.Meta) == 0 {
		a.Meta = json.RawMessage(`{}`)
	}
	if err := errors.Join(dialect.Text(a.Job, a.SHA256, a.Kind, a.Name, a.State, a.payer), dialect.Document(a.Meta)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO artifacts (`+artCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT(job, sha256) DO UPDATE SET kind = excluded.kind, name = excluded.name, size = excluded.size, meta = excluded.meta, state = excluded.state,
  created = CASE WHEN excluded.state = 'pending' THEN excluded.created ELSE artifacts.created END`,
		a.Job, a.SHA256, a.Kind, a.Name, a.Size, a.State, string(a.Meta), a.payer, a.Created)
	return err
}

// kept is the bytes the org stores billed to payer.
func (s *store) kept(ctx context.Context, payer string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM objects WHERE payer = $1`, payer).Scan(&n)
	return n, err
}

// pending is the bytes the org has been granted and not confirmed, each sha256
// once, but for except: what the next grant is weighed against.
func (s *store) pending(ctx context.Context, except string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM (SELECT MAX(size) AS size FROM artifacts
WHERE state = 'pending' AND sha256 != $1 GROUP BY sha256) pending`, except).Scan(&n)
	return n, err
}

// lapsed are the sha256s whose every pending registration was granted before
// cutoff: grants no longer accepted, for bytes never confirmed.
func (s *store) lapsed(ctx context.Context, cutoff int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sha256 FROM artifacts WHERE state = 'pending' GROUP BY sha256 HAVING MAX(created) < $1 ORDER BY sha256`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out = append(out, sha)
	}
	return out, rows.Err()
}

// lapse closes the pending registrations of bytes whose grants lapsed: stored
// where the org holds those bytes under another registration, deleted where it
// does not, and their open uploads gone either way.
func (s *store) lapse(ctx context.Context, sha string, held bool) error {
	state := "deleted"
	if held {
		state = "stored"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET state = $1 WHERE sha256 = $2 AND state = 'pending'`, state, sha); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM uploads WHERE sha256 = $1`, sha); err != nil {
		return err
	}
	return tx.Commit()
}

// stored marks an artifact's bytes read back, and keeps the object they are:
// its key, the key it is sealed under, its size and the payer its storage is
// charged to, its storage clock started now. Bytes the org held unsealed take
// the new key and seal.
func (s *store) stored(ctx context.Context, a artifact, key, seal string, now int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET state = 'stored' WHERE job = $1 AND sha256 = $2`, a.Job, a.SHA256); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO objects (sha256, key, size, payer, seal, stored) VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT(sha256) DO UPDATE SET key = excluded.key, seal = excluded.seal`, a.SHA256, key, a.Size, a.payer, seal, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO storage (payer, metered_at) VALUES ($1,$2) ON CONFLICT (payer) DO NOTHING`, a.payer, now); err != nil {
		return err
	}
	return tx.Commit()
}

// lookup finds a stored artifact of kind by sha256, or a basis by its address,
// for validation.
func (s *store) lookup(ctx context.Context, kind, key string) (map[string]any, bool, error) {
	q := `SELECT meta FROM artifacts WHERE kind = $1 AND state = 'stored' AND sha256 = $2 LIMIT 1`
	if strings.HasPrefix(key, "basis://") {
		q = `SELECT meta FROM artifacts WHERE kind = $1 AND state = 'stored' AND meta ->> 'address' = $2 ORDER BY created DESC LIMIT 1`
	}
	var meta string
	err := s.db.QueryRowContext(ctx, q, kind, key).Scan(&meta)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	m := map[string]any{}
	_ = json.Unmarshal([]byte(meta), &m)
	return m, true, nil
}

// stored is one stored object of the org: the bytes every artifact row with its
// sha256 names.
type stored struct {
	SHA256, Key, Payer string
	Size, Stored       int64
}

func (s *store) object(ctx context.Context, sha string) (stored, error) {
	var o stored
	err := s.db.QueryRowContext(ctx, `SELECT sha256, key, size, payer, stored FROM objects WHERE sha256 = $1`, sha).Scan(&o.SHA256, &o.Key, &o.Size, &o.Payer, &o.Stored)
	if errors.Is(err, sql.ErrNoRows) {
		return stored{}, errNotFound
	}
	return o, err
}

// pinned is the sha256s the org keeps whatever their age: a job of the org has
// them published, or a capability has them as a version. A capability's version
// counts as published: versions are immutable and a rollback installs any of them
// again.
func (s *store) pinned(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT published FROM jobs WHERE published != ''`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var rec struct {
			SHA256 string `json:"sha256"`
		}
		if json.Unmarshal([]byte(p), &rec) == nil && rec.SHA256 != "" {
			out[rec.SHA256] = true
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT sha256 FROM versions`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out[sha] = true
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// reading is the sha256s a job that has not ended reads, by one of the fields that
// name an artifact to read — dataset.uri, protect.capabilities, adaptation.sources,
// adaptation.basis, from — each compared whole.
func (s *store) reading(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec FROM jobs WHERE status NOT IN `+ended)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var spec struct {
			Dataset *struct {
				URI string `json:"uri"`
			} `json:"dataset"`
			Adaptation struct {
				Basis   string   `json:"basis"`
				Sources []string `json:"sources"`
			} `json:"adaptation"`
			Protect *struct {
				Capabilities []string `json:"capabilities"`
			} `json:"protect"`
			From string `json:"from"`
		}
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if spec.Dataset != nil {
			if sha, ok := strings.CutPrefix(spec.Dataset.URI, "artifact:"); ok {
				out[sha] = true
			}
		}
		out[spec.Adaptation.Basis], out[spec.From] = true, true
		for _, sha := range spec.Adaptation.Sources {
			out[sha] = true
		}
		if spec.Protect != nil {
			for _, sha := range spec.Protect.Capabilities {
				out[sha] = true
			}
		}
	}
	delete(out, "")
	return out, errors.Join(rows.Err(), rows.Close())
}

// uses reports whether an object is published, and whether a job that has not
// ended reads it.
func (s *store) uses(ctx context.Context, sha string) (pub, live bool, err error) {
	pinned, err := s.pinned(ctx)
	if err != nil {
		return false, false, err
	}
	reading, err := s.reading(ctx)
	if err != nil {
		return false, false, err
	}
	return pinned[sha], reading[sha], nil
}

// forget drops an object the store has removed: every artifact row naming it
// reads deleted, and the object and its pending uploads are gone.
func (s *store) forget(ctx context.Context, sha string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`UPDATE artifacts SET state = 'deleted' WHERE sha256 = $1`,
		`DELETE FROM objects WHERE sha256 = $1`,
		`DELETE FROM uploads WHERE sha256 = $1`,
	} {
		if _, err := tx.ExecContext(ctx, q, sha); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// uploadIDs are the multipart uploads still open for these bytes.
func (s *store) uploadIDs(ctx context.Context, sha string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM uploads WHERE sha256 = $1 AND id != ''`, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// stale are the objects stored before cutoff that nothing keeps: unpublished,
// and read by no job that has not ended.
func (s *store) stale(ctx context.Context, cutoff int64) ([]string, error) {
	pinned, err := s.pinned(ctx)
	if err != nil {
		return nil, err
	}
	reading, err := s.reading(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sha256 FROM objects WHERE stored < $1 ORDER BY stored, sha256`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		if !pinned[sha] && !reading[sha] {
			out = append(out, sha)
		}
	}
	return out, rows.Err()
}

// holding is one payer's stored bytes: what it is charged for, and the second
// through which it has been.
type holding struct {
	Payer     string
	Bytes     int64
	MeteredAt int64
}

// holdings is every payer's stored bytes: every object is charged from when it
// is stored until it is deleted. A payer whose last object went is forgotten
// first, so the meter stops visiting it.
func (s *store) holdings(ctx context.Context) ([]holding, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM storage WHERE NOT EXISTS (SELECT 1 FROM objects o WHERE o.payer = storage.payer)`); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.payer, s.metered_at,
  COALESCE((SELECT SUM(o.size) FROM objects o WHERE o.payer = s.payer), 0)
FROM storage s ORDER BY s.payer`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []holding
	for rows.Next() {
		var h holding
		if err := rows.Scan(&h.Payer, &h.MeteredAt, &h.Bytes); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// meter moves a payer's storage watermark from was to now: a compare-and-set.
func (s *store) meter(ctx context.Context, payer string, was, now int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE storage SET metered_at = $1 WHERE payer = $2 AND metered_at = $3`, now, payer, was)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// idle reports whether the org has nothing a sweep or a cross-org read needs:
// no job that has not ended, no object, no pending upload and no charge owed.
func (s *store) idle(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM jobs WHERE status NOT IN `+ended+`) +
  (SELECT COUNT(*) FROM objects) +
  (SELECT COUNT(*) FROM artifacts WHERE state = 'pending') +
  (SELECT COUNT(*) FROM charges)`).Scan(&n)
	return n == 0, err
}

// client is an engine training client the org created.
type client struct {
	ID, Base, Spec, By string
	Created            int64
}

func (s *store) addClient(ctx context.Context, c client) error {
	if err := dialect.Text(c.ID, c.Base, c.Spec, c.By); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO clients (id, base_model, spec, created_by, created) VALUES ($1,$2,$3,$4,$5)`, c.ID, c.Base, c.Spec, c.By, c.Created)
	return err
}

func (s *store) owns(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients WHERE id = $1 AND gone = 0`, id).Scan(&n)
	return n > 0, err
}

func (s *store) clients(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM clients WHERE gone = 0 ORDER BY created, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *store) drop(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	q := `UPDATE clients SET gone = 1 WHERE id IN (` + dialect.Marks(1, len(ids)) + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// unix is the clock, in unix seconds: leases, holds and the seconds a report may
// add are all read off it. A var so a test can move it.
var unix = func() int64 { return time.Now().Unix() }

// cost is what secs device-seconds cost at rate micro-USD a device-hour,
// rounded half up: the one arithmetic every charge and hold of a job uses.
func cost(rate, secs int64) int64 { return cloud.RuntimeCost(rate, secs) }
