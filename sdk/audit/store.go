package audit

// The append-only sink + the serialized Recorder that owns the hash-chain head.
//
// WHY THE CHAIN LIVES IN THE DATABASE (not datastore). The chain is only
// tamper-EVIDENT if records are appended in a strict, gapless total order and
// each record's PrevHash is the immediately-preceding record's Hash. That demands
// one serializing writer at a time with a synchronous, read-your-write head. The
// chain is a table the application can only INSERT into (hanzo-sql in production,
// embedded SQLite for a developer's machine), and the head of the chain IS the
// table's last row. An append is one transaction that takes the store's write lock
// (Dialect.BeginWrite: one writing transaction per chain across every replica),
// reads that last row, seals the batch onto it and inserts it, so replicas of one
// deployment share ONE chain and cannot fork it. It gives (a) a real total order,
// (b) synchronous durability so NO record is ever lost on the request path (unlike a
// fire-and-forget mirror), and (c) an append-only surface — the app issues no
// UPDATE/DELETE, and the hash-chain detects any out-of-band edit to the store.
// That is the compliance-grade primary control.
//
// THE DATASTORE MIRROR IS A PROJECTION, NOT THE SOURCE OF TRUTH. The datastore
// (datastore MergeTree — insert-only, mutation-rejected at parse time) is the
// fleet-wide OLAP mirror for long-retention, cross-deployment query. It is
// best-effort and asynchronous: a mirror outage must never block or fail an
// audited request, and the local chain remains the authority the verifier walks.
// Losing a mirror row is a query-completeness issue, not an integrity one.
//
// FAIL MODE. Append is INLINE and its error is RETURNED to the middleware, which
// fails the request CLOSED (a security-relevant action that cannot be recorded is
// not permitted to silently succeed). This is the AU-5 "response to audit logging
// process failure": deny rather than act-unlogged.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	// cek opens the chain encrypted at rest, keyed from this process's master
	// and the name below. github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver
	// it opens through; importing modernc directly instead would double-register
	// "sqlite" under CGO and panic at init. Blank import registers the driver.
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
)

// Mirror is the optional OLAP projection sink (the datastore/datastore). It is
// deliberately a tiny interface, not a concrete client, so the Recorder has no
// compile-time dependency on datastore and tests can supply a fake. Append is
// called best-effort, asynchronously, off the request path.
type Mirror interface {
	// Append writes one sealed record to the projection. A returned error is
	// logged and dropped by the Recorder — the mirror never gates a request.
	Append(ctx context.Context, r Record) error
}

// Checkpoint is a periodic, tamper-EVIDENCE digest of the chain head: the record
// count and the head hash at a moment in time. It is the AU-9 anchor for
// TAIL-TRUNCATION detection — an internal chain walk cannot notice that the last
// K records were deleted (the surviving prefix still verifies), but a durable,
// INDEPENDENT series of head checkpoints can: Count is monotonic, so any decrease
// between two consecutive checkpoints is deletion, and an attacker cannot forge a
// higher count without appending records whose hashes the chain walk would reject.
type Checkpoint struct {
	Time  time.Time `json:"time"`
	Count uint64    `json:"count"`
	Head  string    `json:"head"`
}

// CheckpointSink is an optional capability a Mirror may implement to persist the
// head digest series to an INDEPENDENT store (so truncating the local SQLite
// cannot also rewrite the anchor history). A Mirror that does not implement it
// still gets its records; checkpoints then flow only to the structured log.
type CheckpointSink interface {
	Checkpoint(ctx context.Context, cp Checkpoint) error
}

// Recorder is the writer of the audit chain: every Record flows through Append,
// which in one transaction under the chain's write lock reads the head (the last
// row), assigns the next Seq, links PrevHash to it, seals (hashes), and
// synchronously persists before returning. The lock is the database's, so any
// number of replicas append to one chain and the order on disk is the chain order,
// with no gaps; within one process mu batches the appends that wait together.
type Recorder struct {
	db *sql.DB
	d  dialect.Dialect
	// read is where the chain is read: its own handles with the C codec on SQLite, db
	// without it and on hanzo-sql (Open). No read ever holds db's one connection when
	// it can avoid it.
	read *sql.DB
	// name is the chain this recorder writes — the subsystem its store is keyed
	// under. It rides on every verdict, so a verification can never be read as
	// being about a chain other than the one it walked.
	//
	// dir is the data root it was opened under, which is where the REST of the
	// family lives on SQLite. Keeping it means Trail needs no argument: the one
	// object that holds a chain open is the one that knows where its siblings are.
	name   string
	dir    string
	mirror Mirror // nil when no OLAP mirror is configured.
	// owner is set on a Remote recorder, and then db is nil: every append and read
	// is asked of the process that holds the chain.
	owner Owner
	// acting names the operator behind the call a record is appended for, for a
	// record whose emit point did not (Acting). Nil names nobody.
	acting func(context.Context) Act

	mu sync.Mutex // serializes this process's commits, so one holder writes every record queued.

	// sm guards db, read and d for a reader that does not hold mu: they change once, when
	// the chain moves from its file to hanzo-sql (see move.go), under mu and sm together.
	sm      sync.RWMutex
	retired []*sql.DB // the file's handles, closed once the move that retired them is over

	moveStop context.CancelFunc // ends the background move (StartMove)
	moveDone chan struct{}

	// seen is the newest head this recorder has read or written. The chain's head is
	// the last row of audit_log, which every replica appends to, so this is not
	// where a commit takes it from: it is what Head answers while the database does
	// not.
	hmu      sync.Mutex
	nextSeq  uint64 // Seq the next record takes, as last seen.
	headHash string // Hash of the last record, as last seen.

	// queue is the appends waiting for the next holder of mu to commit them (Append).
	qmu   sync.Mutex
	queue []*waiter

	// Checkpoint emission (AU-9 tail-truncation anchor). logCheckpoint, when set,
	// receives each head digest so it lands in the append-only observability log;
	// stopCh/wg manage the periodic emitter goroutine's lifecycle; started guards
	// against a second StartCheckpoints call (the field write + WaitGroup use are
	// not safe to race). All three are set once, before any concurrent Append.
	logCheckpoint func(cp Checkpoint)
	stopCh        chan struct{}
	wg            sync.WaitGroup
	started       bool

	// last is the newest full walk, served by Latest.
	last latest
}

// Owner is the trail as a process that does not hold it reaches it. One process
// keeps the deployment's chain; every other one hands it records and asks it for
// them, so there is one store and one writer whatever the fleet's shape.
//
// Append must not answer until the record is durable in the chain: a returned
// error is a record that did not land, and the caller fails closed on it exactly
// as it would on a local write that failed.
type Owner interface {
	Append(ctx context.Context, rec Record) (Record, error)
	Query(ctx context.Context, f Filter) (rows []Record, total int, err error)
}

// Remote is the Recorder of a process that does not hold the trail. Append and
// Query are asked of owner, which seals each record into the one chain; name is
// the process that holds it, for the errors that say where to look.
//
// It keeps no chain, so it has nothing to verify, no head and no checkpoint of its
// own: Verify and Trail refuse and name the holder, Head is zero, and
// StartCheckpoints arms nothing — a zero count on the AU-9 stream would read as a
// truncated chain.
func Remote(name string, owner Owner) *Recorder {
	return &Recorder{name: name, owner: owner}
}

// Acting sets how an append names the operator behind the call it is made for,
// for a record that does not name one itself: every row a support session causes
// carries who was at the keyboard, whichever emit point wrote it. Set once, before
// the first Append.
func (r *Recorder) Acting(f func(context.Context) Act) { r.acting = f }

// held is the refusal a Remote recorder answers anything that needs the chain
// itself with.
func (r *Recorder) held() error {
	return fmt.Errorf("audit: the trail is held by %s; ask it there", r.name)
}

// CheckpointFunc receives a head digest for the structured (o11y) log. It is a
// plain func so the pure audit package stays free of any concrete logger type;
// the cloud wiring adapts luxlog to it.
type CheckpointFunc func(cp Checkpoint)

// Open opens (creating if needed) the append-only audit chain named by subsystem
// under dir, so a restart, or another replica, continues the SAME chain rather than
// forking a new one: the head is read from the chain at every commit. mirror may be
// nil.
//
// The trail belongs to the DEPLOYMENT and not to any tenant, so it is keyed under
// the system namespace. Which chain — one process's — is the subsystem. With
// CLOUD_SQL_DSN set it is a schema on hanzo-sql, shared by every replica, and dir
// is not read; otherwise it is the encrypted SQLite file under dir, pinned to one
// connection (MaxOpenConns(1) serializes every statement against the file lock,
// which doubles as the chain's serialization there).
//
// READS GET HANDLES OF THEIR OWN. The writer's one connection is the chain's, and a
// read that held it held every append in the deployment behind it: GET
// /v1/admin/audit counted a 16-million-row chain on that connection for 126-190 s,
// every append waited past the plane's 5 s bound, and cloud answered 503 to every
// audited request while KMS refused every secret (2026-10-30 03:13, 13:22, 14:17 UTC;
// thousands a day as the chain grew). The SQLite chain is WAL, where readers and the
// one writer do not wait on each other, so Query and the walks read through readers.
// Only with the C codec: the pure-Go envelope gives each handle a private copy of
// the file and writes it back on Close, so there a second handle is a second writer
// (verifyChain says the same of a sibling chain) and reads share the writer's. On
// hanzo-sql the pool is several connections already and reads share it.
func Open(dir, subsystem string, mirror Mirror) (*Recorder, error) {
	db, err := locus.Open(namespace.System(), subsystem, dir)
	if err != nil {
		return nil, err
	}
	r := &Recorder{db: db, d: dialect.Of(db), read: db, name: subsystem, dir: dir, mirror: mirror, headHash: genesisPrevHash}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), commitWithin)
	_, _, err = r.head(ctx, db)
	cancel()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !r.d.Postgres() && sqlitedrv.CodecLinked() {
		read, err := locus.Open(namespace.System(), subsystem, dir)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		read.SetMaxOpenConns(readers)
		r.read = read
	}
	return r, nil
}

// readers bounds the chain's read connections: a slow read takes one, and the
// others keep answering.
const readers = 4

// migrate creates the append-only audit table. It is INSERT-only by application
// discipline: this package issues no UPDATE or DELETE against it, and seq is the
// PRIMARY KEY so a replayed/duplicated seq is rejected by the engine, which is also
// what stops a writer that bypassed the chain lock from forking it. The hash
// columns make any out-of-band row edit detectable by Verify regardless of the
// storage layer's own guarantees. The whole migration is one Dialect.Migrate
// transaction, so replicas opening the chain together do not run its DDL at once.
func migrate(db *sql.DB) error {
	d := dialect.Of(db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ddl := `
CREATE TABLE audit_log (
  seq        BIGINT PRIMARY KEY NOT NULL, -- chain position; gapless, assigned under the chain lock
  ts         TEXT    ` + d.Bytewise() + ` NOT NULL, -- RFC3339Nano UTC event time, read by range
  actor_org  TEXT    NOT NULL DEFAULT '',
  actor_sub  TEXT    NOT NULL DEFAULT '',
  actor_email TEXT   NOT NULL DEFAULT '',
  actor_home TEXT    NOT NULL DEFAULT '',  -- set ONLY on a cross-org (impersonated) action
  action     TEXT    NOT NULL,
  res_type   TEXT    NOT NULL DEFAULT '',
  res_id     TEXT    NOT NULL DEFAULT '',
  auth_method TEXT   NOT NULL DEFAULT '',
  is_admin   BIGINT  NOT NULL DEFAULT 0,
  result     TEXT    NOT NULL,          -- success|deny|error
  status     BIGINT  NOT NULL DEFAULT 0,
  reason     TEXT    NOT NULL DEFAULT '',
  source_ip  TEXT    NOT NULL DEFAULT '',
  user_agent TEXT    NOT NULL DEFAULT '',
  request_id TEXT    NOT NULL DEFAULT '',
  method     TEXT    NOT NULL DEFAULT '',
  path       TEXT    NOT NULL DEFAULT '',
  before     TEXT    NOT NULL DEFAULT '',  -- redacted JSON (explicit emit only)
  after      TEXT    NOT NULL DEFAULT '',  -- redacted JSON (explicit emit only)
  prev_hash  TEXT    NOT NULL,
  hash       TEXT    NOT NULL
);
-- Query indexes for the /v1/admin/audit filters (actor/action/resource/time).
CREATE INDEX ix_audit_org_seq    ON audit_log(actor_org, seq);
CREATE INDEX ix_audit_action_seq ON audit_log(action, seq);
CREATE INDEX ix_audit_result_seq ON audit_log(result, seq);
CREATE INDEX ix_audit_ts         ON audit_log(ts);
CREATE INDEX ix_audit_res_seq    ON audit_log(res_type, res_id, seq);
`
	err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
		// CREATE INDEX locks out the table's writers even to find the index already
		// there, and the chain is opened by every replica as it starts, beside
		// replicas that are appending. The table and its indexes are made together,
		// so a table that is there has them.
		has, err := d.HasTable(ctx, tx, "audit_log")
		if err != nil {
			return err
		}
		if !has {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		// CREATE TABLE IF NOT EXISTS does nothing to a table that already exists, so a
		// trail written before these columns existed needs them added explicitly.
		// actor_home MUST exist: Verify rehydrates each record from its columns and
		// recomputes the hash, so a field that is written but not persisted would come
		// back empty and every impersonation record would be reported as TAMPERED — a
		// false alarm on the one control that must never cry wolf.
		//
		// Additive, defaulted, and idempotent: existing rows read back actor_home='',
		// which (omitempty) canonicalizes exactly as before, so their stored hashes
		// still verify. See TestVerify_HomeFieldIsHashCompatible. The operator behind an
		// impersonated principal (Actor.Act) and the agent behind a delegated one
		// (Actor.Agent) are on the same terms: both are omitted from the canonical JSON
		// when empty.
		for _, col := range []string{"actor_home", "act_sub", "act_owner", "act_name", "actor_agent"} {
			if err := addColumn(ctx, d, tx, col); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("audit: migrate: %w", err)
	}
	return nil
}

// addColumn adds a TEXT NOT NULL DEFAULT ” column to audit_log if it is not
// already present. Idempotent by inspection (Dialect.HasColumn) rather than by
// swallowing the duplicate-column error, so a REAL migration failure still
// surfaces instead of being mistaken for "already applied" — and on hanzo-sql a
// swallowed error would abort the migration's whole transaction anyway.
func addColumn(ctx context.Context, d dialect.Dialect, tx *sql.Tx, name string) error {
	has, err := d.HasColumn(ctx, tx, "audit_log", name)
	if err != nil {
		return fmt.Errorf("column %s: %w", name, err)
	}
	if has {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE audit_log ADD COLUMN `+name+` TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add column %s: %w", name, err)
	}
	return nil
}

// head reads the chain's head from q: the Seq the next record takes and the hash it
// links to. The head is the last row of audit_log; an empty chain starts at the
// genesis anchor. It is read inside the commit's transaction, under the chain lock,
// so it is the head no other replica can move until that transaction ends. What it
// read is kept as the head last seen.
func (r *Recorder) head(ctx context.Context, q dialect.Querier) (next uint64, hash string, err error) {
	var seq int64
	switch err := q.QueryRowContext(ctx, `SELECT seq, hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&seq, &hash); {
	case errors.Is(err, sql.ErrNoRows):
		next, hash = 0, genesisPrevHash
	case err != nil:
		return 0, "", fmt.Errorf("audit: read head: %w", err)
	default:
		next = uint64(seq) + 1
	}
	r.see(next, hash)
	return next, hash, nil
}

// see records the newest head this recorder has read or written. Commits and reads
// of one chain may finish out of order, so the head only moves forward.
func (r *Recorder) see(next uint64, hash string) {
	r.hmu.Lock()
	defer r.hmu.Unlock()
	if next >= r.nextSeq {
		r.nextSeq, r.headHash = next, hash
	}
}

// Append seals r into the next chain position and persists it. It fills Seq,
// PrevHash, and Hash (the caller sets everything else), and mirrors best-effort. A
// persistence error is returned so the caller can fail the request CLOSED — the
// transaction rolled back, so the head did not move and the chain never gaps.
//
// GROUP COMMIT. The critical section (lock the chain → read the head → seal →
// INSERT) is one transaction, and mu orders the holders inside this process, so two
// concurrent requests here never wait on the chain lock separately. Every append
// queues its record and then takes mu, and whoever holds it seals and inserts EVERY
// record queued by then, in queue order, in one transaction. An append whose record
// an earlier holder already wrote finds its answer waiting and returns. Each record
// is still durable before its Append returns, still in a gapless total order, and a
// failed transaction still fails every record in it with the head unmoved. Another
// REPLICA's holder is ordered by the database, not by mu: it waits at BeginWrite for
// this transaction to end, then reads the head this one left.
//
// One INSERT per transaction made the chain a queue as long as its burst: a fleet
// fold on the admin board audits each of its thousands of per-org reads, appends
// waited past the plane's 5 s bound, and every audited request in the deployment
// answered 503 for minutes (2026-10-30 03:13 and 13:22 UTC; /v1/event and webhooks
// among them). A batch costs one commit, so a burst costs a few.
func (r *Recorder) Append(ctx context.Context, rec Record) (Record, error) {
	if rec.Time.IsZero() {
		rec.Time = time.Now().UTC()
	} else {
		rec.Time = rec.Time.UTC()
	}
	// So is the operator: an emit point that built its Actor by hand knows the
	// person and not who was at the keyboard, and the call it is made for does.
	if rec.Actor.Act.Sub == "" && r.acting != nil {
		rec.Actor.Act = r.acting(ctx)
	}
	// The time is stamped HERE, where the action happened, and the holder keeps it.
	if r.owner != nil {
		return r.owner.Append(ctx, rec)
	}

	w := &waiter{rec: rec, at: time.Now(), done: make(chan written, 1)}
	r.qmu.Lock()
	r.queue = append(r.queue, w)
	r.qmu.Unlock()

	// Each turn at mu commits the oldest records queued, at most maxBatch; the queue
	// is first in, first out, so a record past one batch is in the next turn's.
	for {
		r.mu.Lock()
		select {
		case out := <-w.done: // a holder wrote it
			r.mu.Unlock()
			return out.rec, out.err
		default:
		}
		r.qmu.Lock()
		n := min(len(r.queue), maxBatch)
		batch := r.queue[:n:n]
		r.queue = r.queue[n:]
		r.qmu.Unlock()
		r.commit(ctx, batch)
		r.mu.Unlock()
	}
}

// maxBatch bounds one transaction.
const maxBatch = 512

// waiter is one queued append, when it was queued, and where its answer goes.
type waiter struct {
	rec  Record
	at   time.Time
	done chan written
}

// Behind is how long the oldest append still waiting to be written has waited: zero
// when nothing waits. A trail that keeps up answers in milliseconds; one a read or a
// stuck disk holds shows it here before any caller's bound runs out, which is what
// the trail's health route reads (apps/admin/audit). Zero on a Remote recorder.
func (r *Recorder) Behind() time.Duration {
	r.qmu.Lock()
	defer r.qmu.Unlock()
	if len(r.queue) == 0 {
		return 0
	}
	return time.Since(r.queue[0].at)
}

// written is an append's answer: the sealed record, or why it did not land.
type written struct {
	rec Record
	err error
}

// commitWithin bounds one commit, the wait for the chain lock included: a replica
// that holds it and is gone drops its connection and releases it, so a wait this
// long is a fault to fail closed on, not a queue to stand in.
const commitWithin = 30 * time.Second

// commit seals batch onto the chain's head in order and inserts it in one
// transaction, then answers every waiter. The caller holds mu. A record that will
// not seal is refused alone and takes no seq; a transaction that fails refuses the
// rest and moves nothing. The write is detached from the caller that happens to lead
// it: it carries other callers' records.
//
// The head is read after the chain lock is held. Another replica may have appended
// since this one last looked, and its records are the ones this batch links to.
func (r *Recorder) commit(ctx context.Context, batch []*waiter) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitWithin)
	defer cancel()
	sealed, kept, err := r.write(ctx, batch)
	if err != nil {
		// Nothing landed and the head is where it was; the next commit reads it again.
		for _, w := range kept {
			w.done <- written{err: fmt.Errorf("audit: persist: %w", err)}
		}
		return
	}
	for i, w := range kept {
		w.done <- written{rec: sealed[i]}
	}

	// Best-effort OLAP mirror, detached so a slow/failed mirror never blocks the
	// request or corrupts the reply. The local chain is already durable and is the
	// authority; a lost mirror row is a query-completeness gap, not an integrity
	// one. Copy by value: the record is immutable and safe to hand to a goroutine.
	if r.mirror != nil && len(sealed) > 0 {
		m := r.mirror
		go func() {
			for _, out := range sealed {
				_ = m.Append(context.Background(), out)
			}
		}()
	}
}

// write is the chain's critical section: one transaction under the chain lock that
// reads the head, seals the batch onto it and inserts it. It answers the records it
// refused (they will not seal, or hold text the database would refuse) and returns
// the sealed ones with the waiters they belong to. On an error nothing was written,
// and kept names the waiters to fail: every one that was not refused alone.
func (r *Recorder) write(ctx context.Context, batch []*waiter) (sealed []Record, kept []*waiter, err error) {
	tx, err := r.d.BeginWrite(ctx, r.db)
	if err != nil {
		return nil, batch, err
	}
	defer func() { _ = tx.Rollback() }()
	seq, head, err := r.head(ctx, tx)
	if err != nil {
		return nil, batch, err
	}
	sealed = make([]Record, 0, len(batch))
	kept = make([]*waiter, 0, len(batch))
	for _, w := range batch {
		s, err := seal(w.rec, seq, head)
		if err == nil {
			err = texts(w.rec)
		}
		if err != nil {
			w.done <- written{err: fmt.Errorf("audit: seal: %w", err)}
			continue
		}
		sealed, kept = append(sealed, s), append(kept, w)
		seq, head = s.Seq+1, s.Hash
	}
	if len(sealed) == 0 {
		return nil, nil, nil
	}
	if err := insert(ctx, tx, sealed); err != nil {
		return nil, kept, err
	}
	if err := tx.Commit(); err != nil {
		return nil, kept, err
	}
	// Durable — this is the head now.
	r.see(seq, head)
	return sealed, kept, nil
}

// texts refuses a record whose text the database would refuse: a NUL byte, or bytes
// that are not UTF-8. SQLite stores them, PostgreSQL does not, and a chain must hold
// the same records on both. A record refused here is refused alone, at the boundary,
// and the caller fails the request closed like any record that did not land.
func texts(rec Record) error {
	if err := dialect.Text(
		rec.Actor.Org, rec.Actor.Sub, rec.Actor.Email, rec.Actor.Home,
		rec.Actor.Act.Sub, rec.Actor.Act.Owner, rec.Actor.Act.Name, rec.Actor.Agent,
		rec.Action, rec.Resource.Type, rec.Resource.ID, rec.Auth.Method,
		rec.Outcome.Result, rec.Outcome.Reason, rec.SourceIP, rec.UserAgent,
		rec.RequestID, rec.Method, rec.Path, string(rec.Before), string(rec.After),
	); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

// rowChunk is how many records one INSERT statement carries: a round trip per
// record would hold the chain lock for as many round trips as the batch is long, and
// every replica's appends wait behind it.
const rowChunk = 100

// insert writes sealed records in tx. INSERT-only — the sole write statement in this
// package. A duplicate seq (PRIMARY KEY) fails here, which is the desired invariant:
// the chain never overwrites a position.
func insert(ctx context.Context, tx *sql.Tx, recs []Record) error {
	for len(recs) > 0 {
		n := min(len(recs), rowChunk)
		var a dialect.Args
		var b strings.Builder
		b.WriteString(`INSERT INTO audit_log (` + selectCols + `) VALUES `)
		for i, rec := range recs[:n] {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("(")
			for j, v := range []any{
				int64(rec.Seq), rec.Time.Format(time.RFC3339Nano),
				rec.Actor.Org, rec.Actor.Sub, rec.Actor.Email, rec.Actor.Home,
				rec.Action, rec.Resource.Type, rec.Resource.ID,
				rec.Auth.Method, boolToInt(rec.Auth.IsAdmin),
				rec.Outcome.Result, rec.Outcome.Status, rec.Outcome.Reason,
				rec.SourceIP, rec.UserAgent, rec.RequestID, rec.Method, rec.Path,
				string(rec.Before), string(rec.After), rec.PrevHash, rec.Hash,
				rec.Actor.Act.Sub, rec.Actor.Act.Owner, rec.Actor.Act.Name, rec.Actor.Agent,
			} {
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(a.Mark(v))
			}
			b.WriteString(")")
		}
		if _, err := tx.ExecContext(ctx, b.String(), a.Values()...); err != nil {
			return err
		}
		recs = recs[n:]
	}
	return nil
}

// checkpointCloseTimeout bounds the final (synchronous) checkpoint write to the
// independent sink at shutdown, so Close cannot hang on an unreachable datastore.
const checkpointCloseTimeout = 5 * time.Second

// Close stops the periodic checkpoint emitter, emits a FINAL checkpoint
// SYNCHRONOUSLY (so the head at shutdown reaches both the o11y log and the
// independent digest store before the process exits — the AU-9 anchor must be
// current exactly when an attacker might trigger shutdown then truncate), and
// closes the underlying database.
func (r *Recorder) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	if r.stopCh != nil {
		close(r.stopCh)
		r.wg.Wait()
		r.stopCh = nil
	}
	// Anchor the final head before the DB closes — independent of whether the
	// periodic ticker was running (every<=0 still gets a shutdown checkpoint).
	// Synchronous to the sink (bounded), so the independent store's last count is
	// as fresh as the local chain at the moment of shutdown.
	r.emitCheckpoint(true)
	if r.read != nil && r.read != r.db {
		_ = r.read.Close()
	}
	return r.db.Close()
}

// StartCheckpoints begins periodic head-digest emission every `every` (no ticker
// if every<=0; the on-Close checkpoint still fires). logFn, when non-nil,
// receives each digest for the append-only observability log (o11y), and a mirror
// implementing CheckpointSink also gets it persisted to an INDEPENDENT store —
// together the AU-9 anchor an external monitor compares to detect tail-truncation
// (count regression). MUST be called at most once, before any concurrent Append
// (a second call is ignored); the emitter stops on Close.
func (r *Recorder) StartCheckpoints(every time.Duration, logFn CheckpointFunc) {
	if r.owner != nil {
		return // no chain here; the holder anchors its own head.
	}
	// Guard the check-and-set under mu so a (mis)use that calls this concurrently
	// is race-free, not just the single-call production path.
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return // already started — do not re-arm (avoids a field/WaitGroup race).
	}
	r.started = true
	r.logCheckpoint = logFn
	r.mu.Unlock()
	if every <= 0 {
		return
	}
	r.stopCh = make(chan struct{})
	stop := r.stopCh
	r.wg.Go(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r.emitCheckpoint(false)
			}
		}
	})
}

// emitCheckpoint snapshots the head and emits it to the log and (if the mirror is
// a CheckpointSink) the independent digest store. sync controls the sink write:
// on the periodic path (sync=false) it is detached so a slow sink never delays
// the ticker; on the Close path (sync=true) it BLOCKS on a bounded context so the
// final anchor is durable before shutdown. The log emission is always synchronous
// (it is the primary anchor and o11y ingests it append-only).
func (r *Recorder) emitCheckpoint(sync bool) {
	ctx, cancel := context.WithTimeout(context.Background(), checkpointCloseTimeout)
	count, head, err := r.chainHead(ctx)
	cancel()
	if err != nil {
		// A head this replica could not read is not the head. A stale count on the
		// anchor stream would read as truncation to the monitor that compares them,
		// so no checkpoint is emitted; the next tick reads again.
		return
	}
	cp := Checkpoint{Time: time.Now().UTC(), Count: count, Head: head}
	if r.logCheckpoint != nil {
		r.logCheckpoint(cp)
	}
	cs, ok := r.mirror.(CheckpointSink)
	if !ok || cs == nil {
		return
	}
	if sync {
		ctx, cancel := context.WithTimeout(context.Background(), checkpointCloseTimeout)
		defer cancel()
		_ = cs.Checkpoint(ctx, cp)
		return
	}
	go func() { _ = cs.Checkpoint(context.Background(), cp) }()
}

// Head returns the current chain head (count of records, and the head hash). A
// count of 0 means the genesis (empty) chain, headHash == genesisPrevHash. An
// external monitor can pin (count, headHash) over time to detect tail-truncation
// — which an internal chain walk alone cannot catch (a truncated prefix still
// verifies). This is the anchor point for AU-9 protection against deletion of the
// most-recent records.
//
// The head is the chain's, read from the database: another replica may have
// appended since this one last did. When the database does not answer it is the
// newest head this recorder saw, which can be behind and is never made up.
func (r *Recorder) Head() (count uint64, headHash string) {
	ctx, cancel := context.WithTimeout(context.Background(), checkpointCloseTimeout)
	defer cancel()
	if count, headHash, err := r.chainHead(ctx); err == nil {
		return count, headHash
	}
	r.hmu.Lock()
	defer r.hmu.Unlock()
	return r.nextSeq, r.headHash
}

// chainHead reads the head from the chain, through a read handle: asking it never
// waits behind a commit.
func (r *Recorder) chainHead(ctx context.Context) (uint64, string, error) {
	if r.db == nil { // a Remote recorder keeps no chain
		return 0, "", nil
	}
	read, _ := r.view()
	return r.head(ctx, read)
}

// view is the handle reads go through and the dialect it speaks, as they are now: the
// chain's file or, once it has moved, its schema on hanzo-sql.
func (r *Recorder) view() (*sql.DB, dialect.Dialect) {
	r.sm.RLock()
	defer r.sm.RUnlock()
	return r.read, r.d
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
