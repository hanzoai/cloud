package audit

// The append-only sink + the serialized Recorder that owns the hash-chain head.
//
// WHY SQLITE IS THE PRIMARY, DURABLE STORE (not ClickHouse). The chain is only
// tamper-EVIDENT if records are appended in a strict, gapless total order and
// each record's PrevHash is the immediately-preceding record's Hash. That demands
// a single serializing writer with a synchronous, read-your-write head. cloud's
// canonical store is embedded SQLite (one store per the storagelock lockdown;
// pricing/provisioning already persist to {DataDir}/*.db). A local SQLite table
// the application can only INSERT into gives us: (a) a real total order under one
// connection, (b) synchronous durability so NO record is ever lost on the request
// path (unlike a fire-and-forget mirror), and (c) an append-only surface — the
// app issues no UPDATE/DELETE, and the hash-chain detects any out-of-band edit to
// the file. That is the compliance-grade primary control.
//
// THE CLICKHOUSE MIRROR IS A PROJECTION, NOT THE SOURCE OF TRUTH. The datastore
// (ClickHouse MergeTree — insert-only, mutation-rejected at parse time) is the
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
	"sync"
	"time"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

// Mirror is the optional OLAP projection sink (the datastore/ClickHouse). It is
// deliberately a tiny interface, not a concrete client, so the Recorder has no
// compile-time dependency on ClickHouse and tests can supply a fake. Append is
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

// Recorder is the single serialized writer that owns the audit chain head and
// the append-only store. Every Record flows through Append, which under one lock
// assigns the next Seq, links PrevHash to the current head, seals (hashes), and
// synchronously persists to SQLite before returning. Concurrency is serialized
// by mu AND by the single-connection SQLite pool, so the on-disk order equals
// the chain order with no gaps.
type Recorder struct {
	db     *sql.DB
	mirror Mirror // nil when no OLAP mirror is configured.

	mu       sync.Mutex // guards nextSeq/headHash and serializes appends.
	nextSeq  uint64     // Seq to assign to the next record.
	headHash string     // Hash of the last-appended record (PrevHash for the next).

	// Checkpoint emission (AU-9 tail-truncation anchor). logCheckpoint, when set,
	// receives each head digest so it lands in the append-only observability log;
	// stopCh/wg manage the periodic emitter goroutine's lifecycle; started guards
	// against a second StartCheckpoints call (the field write + WaitGroup use are
	// not safe to race). All three are set once, before any concurrent Append.
	logCheckpoint func(cp Checkpoint)
	stopCh        chan struct{}
	wg            sync.WaitGroup
	started       bool
}

// CheckpointFunc receives a head digest for the structured (o11y) log. It is a
// plain func so the pure audit package stays free of any concrete logger type;
// the cloud wiring adapts luxlog to it.
type CheckpointFunc func(cp Checkpoint)

// Open opens (creating if needed) the append-only audit DB at path and recovers
// the chain head from it, so a restart continues the SAME chain rather than
// forking a new one. path may be ":memory:" for tests. mirror may be nil.
//
// the hanzoai/sqlite "sqlite" driver; MaxOpenConns(1) serializes every statement against
// the file lock — the same single-writer discipline pricing/provisioning use,
// here doubling as the chain's serialization guarantee.
func Open(path string, mirror Mirror) (*Recorder, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("audit: open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("audit: pragma %q: %w", pragma, err)
		}
	}
	r := &Recorder{db: db, mirror: mirror}
	if err := r.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := r.recoverHead(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return r, nil
}

// migrate creates the append-only audit table. It is INSERT-only by application
// discipline: this package issues no UPDATE or DELETE against it, and seq is the
// PRIMARY KEY so a replayed/duplicated seq is rejected by the engine. The hash
// columns make any out-of-band row edit detectable by Verify regardless of the
// storage layer's own guarantees.
func (r *Recorder) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS audit_log (
  seq        INTEGER PRIMARY KEY,       -- chain position; gapless, assigned under lock
  ts         TEXT    NOT NULL,          -- RFC3339Nano UTC event time
  actor_org  TEXT    NOT NULL DEFAULT '',
  actor_sub  TEXT    NOT NULL DEFAULT '',
  actor_email TEXT   NOT NULL DEFAULT '',
  action     TEXT    NOT NULL,
  res_type   TEXT    NOT NULL DEFAULT '',
  res_id     TEXT    NOT NULL DEFAULT '',
  auth_method TEXT   NOT NULL DEFAULT '',
  is_admin   INTEGER NOT NULL DEFAULT 0,
  result     TEXT    NOT NULL,          -- success|deny|error
  status     INTEGER NOT NULL DEFAULT 0,
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
CREATE INDEX IF NOT EXISTS ix_audit_org_seq    ON audit_log(actor_org, seq);
CREATE INDEX IF NOT EXISTS ix_audit_action_seq ON audit_log(action, seq);
CREATE INDEX IF NOT EXISTS ix_audit_result_seq ON audit_log(result, seq);
CREATE INDEX IF NOT EXISTS ix_audit_ts         ON audit_log(ts);
`
	if _, err := r.db.Exec(ddl); err != nil {
		return fmt.Errorf("audit: migrate: %w", err)
	}
	return nil
}

// recoverHead loads the highest-seq record so a restarted process continues the
// existing chain (nextSeq = maxSeq+1, headHash = its hash). An empty table starts
// the genesis chain (nextSeq 0, headHash = genesisPrevHash).
func (r *Recorder) recoverHead() error {
	var (
		maxSeq sql.NullInt64
		hash   sql.NullString
	)
	row := r.db.QueryRow(`SELECT seq, hash FROM audit_log WHERE seq = (SELECT MAX(seq) FROM audit_log)`)
	if err := row.Scan(&maxSeq, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			r.nextSeq = 0
			r.headHash = genesisPrevHash
			return nil
		}
		return fmt.Errorf("audit: recover head: %w", err)
	}
	if !maxSeq.Valid { // empty table (MAX over zero rows is NULL)
		r.nextSeq = 0
		r.headHash = genesisPrevHash
		return nil
	}
	r.nextSeq = uint64(maxSeq.Int64) + 1
	r.headHash = hash.String
	return nil
}

// Append seals r into the next chain position and persists it. It fills Seq,
// PrevHash, and Hash (the caller sets everything else), advances the in-memory
// head only AFTER the durable INSERT succeeds, and mirrors best-effort. A
// persistence error is returned so the caller can fail the request CLOSED — the
// head is NOT advanced on failure, so the chain never gaps.
//
// The whole critical section (assign seq → seal → INSERT → advance head) holds
// mu, so two concurrent requests can never claim the same seq or race the head.
func (r *Recorder) Append(ctx context.Context, rec Record) (Record, error) {
	if rec.Time.IsZero() {
		rec.Time = time.Now().UTC()
	} else {
		rec.Time = rec.Time.UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	sealed, err := seal(rec, r.nextSeq, r.headHash)
	if err != nil {
		return Record{}, fmt.Errorf("audit: seal: %w", err)
	}
	if err := r.insert(ctx, sealed); err != nil {
		// Head not advanced; the next append reuses this seq. Fail closed upstream.
		return Record{}, fmt.Errorf("audit: persist: %w", err)
	}
	// Durable — advance the chain head.
	r.nextSeq = sealed.Seq + 1
	r.headHash = sealed.Hash

	// Best-effort OLAP mirror, detached so a slow/failed mirror never blocks the
	// request or corrupts the reply. The local chain is already durable and is the
	// authority; a lost mirror row is a query-completeness gap, not an integrity
	// one. Copy by value: the record is immutable and safe to hand to a goroutine.
	if r.mirror != nil {
		m, out := r.mirror, sealed
		go func() { _ = m.Append(context.Background(), out) }()
	}
	return sealed, nil
}

// insert writes one sealed record. INSERT-only — the sole write statement in this
// package. A duplicate seq (PRIMARY KEY) fails here, which is the desired
// invariant: the chain never overwrites a position.
func (r *Recorder) insert(ctx context.Context, rec Record) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO audit_log (
  seq, ts, actor_org, actor_sub, actor_email, action, res_type, res_id,
  auth_method, is_admin, result, status, reason, source_ip, user_agent,
  request_id, method, path, before, after, prev_hash, hash
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.Seq, rec.Time.Format(time.RFC3339Nano),
		rec.Actor.Org, rec.Actor.Sub, rec.Actor.Email,
		rec.Action, rec.Resource.Type, rec.Resource.ID,
		rec.Auth.Method, boolToInt(rec.Auth.IsAdmin),
		rec.Outcome.Result, rec.Outcome.Status, rec.Outcome.Reason,
		rec.SourceIP, rec.UserAgent, rec.RequestID, rec.Method, rec.Path,
		string(rec.Before), string(rec.After), rec.PrevHash, rec.Hash,
	)
	return err
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
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
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
	}()
}

// emitCheckpoint snapshots the head and emits it to the log and (if the mirror is
// a CheckpointSink) the independent digest store. sync controls the sink write:
// on the periodic path (sync=false) it is detached so a slow sink never delays
// the ticker; on the Close path (sync=true) it BLOCKS on a bounded context so the
// final anchor is durable before shutdown. The log emission is always synchronous
// (it is the primary anchor and o11y ingests it append-only).
func (r *Recorder) emitCheckpoint(sync bool) {
	count, head := r.Head()
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
func (r *Recorder) Head() (count uint64, headHash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nextSeq, r.headHash
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
