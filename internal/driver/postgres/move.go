package postgres

// move.go — one store, from its SQLite file to its schema on hanzo-sql, while the
// process that owns it keeps serving every other store.
//
// WHAT HOLDS THE STORE STILL. Three things, none of them new:
//
//   - In the process: the caller's own first open. [Move] is called from the open of a
//     store nothing in this process has open yet (OrgStore.For is that open, and every
//     other For for the same namespace waits on it), so there is no handle on the file
//     to write to it. A deployment that sets CLOUD_SQL_MOVE has just restarted, and every
//     store is opened for the first time by the process that will move it.
//   - On the file: SQLite's own write lock. The mover takes it (BEGIN IMMEDIATE) before
//     it reads a row and holds it until the store is marked moved, so any other writer
//     of the file, in any process, waits at the same lock a writer always takes.
//   - On the server: a session advisory lock named for the store. Two movers of one
//     store cannot both hold it, and the server drops it with the connection, so a mover
//     that dies cannot leave the store locked.
//
// THE ORDER. The file is first opened by the store's own open, which migrates it to the
// shape the current code writes (the in-place upgrades — cents to decimals, a column
// added later — exist only for SQLite, and a file never opened by this code is in the
// old shape). The schema is made empty and the store's own open runs on it, which is
// what makes its tables the store's own DDL. Every table is copied, an identity column's
// sequence moved past the largest id (Dialect.Resync), the tables analysed, every table
// read back and its row count and checksum held to the file's. Only then does the
// registry say moved, and that one UPDATE is the whole switch: before it the store is
// its file and the schema is scratch; after it the store is the schema and the file is
// left where it is, untouched.
//
// A FAILURE IS A STORE THAT STAYS A FILE. The schema is dropped, the registry says
// failed and why, and the store is not tried again for [moveBackoff]. A crash leaves the
// row at moving with no lock held, which the next mover takes as a failure that did not
// get to say so. The move is atomic per store and not resumable within one: it starts
// over, and the sweep that finds stores to move skips the ones already moved.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/sqlite"

	"github.com/hanzoai/namespace"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
)

const (
	stateMoving = "moving"
	stateMoved  = "moved"
	stateFailed = "failed"
)

// moveBackoff is how long a store whose move failed stays a file before it is tried
// again. The reason is in the registry; a store that cannot move for its data is not
// helped by trying at every open. A variable so this package's own tests can shorten it.
var moveBackoff = 10 * time.Minute

// session is one dedicated server connection, outside any store's pool, drawing on the
// process's connection budget like every other.
type session struct {
	*sql.Conn
	db *sql.DB
}

func (s *session) Close() error { return errors.Join(s.Conn.Close(), s.db.Close()) }

// pgx hands f the connection as pgx has it, which COPY needs.
func (s *session) pgx(f func(*pgx.Conn) error) error {
	return s.Conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*slotConn)
		if !ok {
			return fmt.Errorf("postgres: a connection of type %T is not one this package dialled", driverConn)
		}
		return f(c.Conn.Conn())
	})
}

func (s *server) dedicated(ctx context.Context) (*session, error) {
	db := sql.OpenDB(connector{inner: stdlib.GetConnector(*s.cfg), lim: limit()})
	db.SetMaxOpenConns(1)
	c, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &session{Conn: c, db: db}, nil
}

// lockMove takes the store's move lock for as long as the session lives.
func (s *server) lockMove(ctx context.Context, schema string) (*session, error) {
	c, err := s.dedicated(ctx)
	if err != nil {
		return nil, err
	}
	var got bool
	if err := c.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, schema+":move").Scan(&got); err != nil {
		_ = c.Close()
		return nil, err
	}
	if !got {
		_ = c.Close()
		return nil, ErrMoving
	}
	return c, nil
}

// moveHeld reports whether some session holds the store's move lock.
func (s *server) moveHeld(ctx context.Context, schema string) (bool, error) {
	if _, ok := s.inMove.Load(schema); ok {
		return true, nil
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	c, err := s.lockMove(ctx, schema)
	if errors.Is(err, ErrMoving) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, c.Close()
}

// Slot takes the one place in the deployment that a sweep moves a store from: at most
// one background move runs at a time, across every process, so the walk is as gentle on
// hanzo-sql as it is long. A move a request asks for does not wait for it. The release
// func gives it back.
func Slot(ctx context.Context) (release func(), err error) {
	s, err := serverOf(DSN())
	if err != nil {
		return nil, err
	}
	c, err := s.dedicated(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended('move-slot', 0))`); err != nil {
		_ = c.Close()
		return nil, err
	}
	return func() { _ = c.Close() }, nil
}

// Move moves the store (ns, subsystem) from its file under dir to hanzo-sql, or says
// why not and leaves it where it is. open is the store's own open over a database
// handle, migration included, as the store opens itself: it is run on the file, to bring
// it to the current shape, and on the schema, to make its tables. The handle open is
// given is the store's to close.
//
// It answers ErrMoving when another process is moving the store and ErrBackoff when a
// move failed less than moveBackoff ago. Any other error is this attempt's failure,
// already rolled back: the store is still its file, and the registry says why.
func Move(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir string, open func(*sql.DB) (io.Closer, error)) (Report, error) {
	return move(ctx, log, ns, subsystem, dir, "", open, nil, nil)
}

// move moves the store. legacy, when set, is the plain SQLite file a module opened
// itself, which is the source in place of the data plane's encrypted file under dir.
// how, when set, opens each side itself (see [shape]) in place of open.
func move(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir, legacy string, open func(*sql.DB) (io.Closer, error), on *Online, how *shape) (Report, error) {
	var rep Report
	dsn := DSN()
	if dsn == "" {
		return rep, fmt.Errorf("postgres: %s is not set, so there is nowhere to move %s/%s to", Env, ns, subsystem)
	}
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return rep, err
	}
	path, err := namespace.Path(dir, ns, subsystem)
	if err != nil {
		return rep, err
	}
	if legacy != "" {
		path = legacy
	}
	// A live store is open, and a build with no codec keeps what it has open in memory
	// until it closes, so the file's being on disk is not the proof it is for a store
	// nothing has open.
	if on == nil && !fileExists(path) {
		return rep, fmt.Errorf("postgres: %s/%s has no file under %s to move", ns, subsystem, dir)
	}
	s, err := serverOf(dsn)
	if err != nil {
		return rep, err
	}
	if _, ok := s.inMove.Load(schema); ok {
		return rep, ErrMoving
	}
	select {
	case s.mover <- struct{}{}:
		defer func() { <-s.mover }()
	case <-ctx.Done():
		return rep, ctx.Err()
	}
	if _, busy := s.inMove.LoadOrStore(schema, struct{}{}); busy {
		return rep, ErrMoving
	}
	defer s.inMove.Delete(schema)

	lock, err := s.lockMove(ctx, schema)
	if err != nil {
		return rep, err
	}
	defer func() { _ = lock.Close() }()

	state, failedAt, err := s.state(ctx, schema)
	if err != nil {
		return rep, err
	}
	switch {
	case state == stateMoved:
		s.moved.Store(schema, struct{}{})
		rep.Already = true
		return rep, nil
	case state == stateFailed && time.Since(time.Unix(failedAt, 0)) < moveBackoff:
		return rep, ErrBackoff
	}

	began := time.Now()
	m := &mover{s: s, lock: lock, log: log, ns: ns, subsystem: subsystem, schema: schema, dir: dir, legacy: legacy, open: open, how: how}
	run := func() error { return m.run(ctx, &rep) }
	if on != nil {
		run = func() error { return m.runOnline(ctx, &rep, *on) }
	}
	if err := run(); err != nil {
		// The cleanup must outlive a cancelled ctx: a move stopped by a signal still has
		// to put the schema back.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*opTimeout)
		m.undo(cctx, err)
		cancel()
		return rep, fmt.Errorf("postgres: move %s/%s: %w", ns, subsystem, err)
	}
	rep.Took = time.Since(began)
	return rep, nil
}

// mover is one attempt.
type mover struct {
	s         *server
	lock      *session
	log       luxlog.Logger
	ns        namespace.Namespace
	subsystem string
	schema    string
	dir       string
	legacy    string
	open      func(*sql.DB) (io.Closer, error)
	how       *shape

	made bool // the schema exists and is the mover's
}

func (m *mover) say(msg string, kv ...any) {
	if m.log != nil {
		m.log.Info(msg, append([]any{"subsystem", m.subsystem, "namespace", m.ns}, kv...)...)
	}
}

// begin makes the schema again, empty, so what a crashed attempt left cannot be
// copied over, and says in the registry that the store is moving.
func (m *mover) begin(ctx context.Context) error {
	m.s.made.Delete(m.schema)
	now := time.Now().Unix()
	for _, q := range []string{
		`DROP SCHEMA IF EXISTS ` + quote(m.schema) + ` CASCADE`,
		`CREATE SCHEMA ` + quote(m.schema),
	} {
		if _, err := m.lock.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	m.made = true
	_, err := m.lock.ExecContext(ctx, `INSERT INTO `+registry+` (schema, namespace, subsystem, created, state, moving)
		VALUES ($1, $2, $3, $4, 'moving', $4)
		ON CONFLICT (schema) DO UPDATE SET state = 'moving', moving = excluded.moving, failed = 0, error = ''`,
		m.schema, m.ns.String(), m.subsystem, now)
	return err
}

// flip is the switch: the registry says moved. After it the store is the schema, and
// the file is left as it is. It is not given up halfway by a signal that arrives now.
func (m *mover) flip(ctx context.Context) error {
	flip, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()
	res, err := m.lock.ExecContext(flip, `UPDATE `+registry+` SET state = 'moved', moved = $1 WHERE schema = $2 AND state = 'moving'`, time.Now().Unix(), m.schema)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("the registry no longer has the store as moving")
	}
	m.s.made.Store(m.schema, struct{}{})
	m.s.moved.Store(m.schema, struct{}{})
	return nil
}

func (m *mover) run(ctx context.Context, rep *Report) error {
	if err := m.begin(ctx); err != nil {
		return err
	}

	// The file, brought to the shape the current code writes, and held.
	file, fileStore, err := m.openFile()
	if err != nil {
		return err
	}
	defer func() {
		if fileStore != nil {
			_ = fileStore.Close()
		}
	}()
	fc, err := file.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = fc.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`) // nothing was written; this lets go of the lock
		_ = fc.Close()
	}()
	if _, err := fc.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("take the file's write lock: %w", err)
	}

	// The schema, with the store's own tables in it.
	pool, pooled, err := m.openSchema()
	if err != nil {
		return err
	}
	defer func() {
		if pooled != nil {
			_ = pooled.Close()
		}
		_ = pool.Close()
	}()

	files, err := fileTables(ctx, fc)
	if err != nil {
		return err
	}
	target, needs, err := targetColumns(ctx, pool, m.schema)
	if err != nil {
		return err
	}
	p, err := makePlan(ctx, fc, target, needs, files)
	if err != nil {
		return err
	}
	rep.Dropped = p.dropped

	// Seeds the migration put in the schema are the file's own rows by now, so the
	// schema starts empty whatever the migration did.
	if len(p.tables) > 0 {
		names := make([]string, len(p.tables))
		for i, t := range p.tables {
			names[i] = quote(m.schema) + "." + quote(t.name)
		}
		if _, err := m.lock.ExecContext(ctx, `TRUNCATE `+strings.Join(names, ", ")); err != nil {
			return err
		}
	}

	sums := make(map[string]total, len(p.tables))
	for _, t := range p.tables {
		began := time.Now()
		var sum total
		var bytes int64
		err := m.lock.pgx(func(c *pgx.Conn) error {
			var err error
			sum, bytes, err = copyTable(ctx, fc, c, m.schema, t)
			return err
		})
		if err != nil {
			return err
		}
		sums[t.name] = sum
		rep.Rows += sum.rows
		rep.Bytes += bytes
		rep.Tables = append(rep.Tables, TableReport{Name: t.name, Rows: sum.rows, Bytes: bytes, Took: time.Since(began)})
		if took := time.Since(began); took > 5*time.Second {
			m.say("sql move: table copied", "table", t.name, "rows", sum.rows, "bytes", bytes, "took", took.Round(time.Millisecond))
		}
	}

	d := dialect.Of(pool)
	tables := make([]string, 0, len(p.tables))
	for _, t := range p.tables {
		tables = append(tables, t.name)
		for _, col := range t.identity {
			if err := d.Resync(ctx, pool, t.name, col); err != nil {
				return fmt.Errorf("resync %s.%s: %w", t.name, col, err)
			}
		}
	}
	if err := d.Analyze(ctx, pool, tables...); err != nil {
		return err
	}

	for _, t := range p.tables {
		got, err := readBack(ctx, pool, t)
		if err != nil {
			return err
		}
		if want := sums[t.name]; got != want {
			return fmt.Errorf("%s did not arrive as it left: the file has %d rows with checksum %x, hanzo-sql has %d with %x", t.name, want.rows, want.sum[:8], got.rows, got.sum[:8])
		}
	}

	return m.flip(ctx)
}

// undo puts the store back to a file: the schema dropped and the registry saying why.
func (m *mover) undo(ctx context.Context, cause error) {
	if m.made {
		if _, err := m.lock.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+quote(m.schema)+` CASCADE`); err != nil && m.log != nil {
			m.log.Warn("sql move: the schema was not dropped after a failed move", "subsystem", m.subsystem, "namespace", m.ns, "err", err)
		}
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		// A move that was stopped is not a defect, and is not held against the store:
		// it is as it was before, and the next open moves it.
		if _, err := m.lock.ExecContext(ctx, `DELETE FROM `+registry+` WHERE schema = $1 AND state <> 'moved'`, m.schema); err != nil && m.log != nil {
			m.log.Warn("sql move: the registry row of a stopped move was not removed", "subsystem", m.subsystem, "namespace", m.ns, "err", err)
		}
		m.s.made.Delete(m.schema)
		return
	}
	msg := cause.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	if _, err := m.lock.ExecContext(ctx, `UPDATE `+registry+` SET state = 'failed', failed = $1, error = $2 WHERE schema = $3 AND state <> 'moved'`,
		time.Now().Unix(), msg, m.schema); err != nil && m.log != nil {
		m.log.Warn("sql move: the failure was not recorded", "subsystem", m.subsystem, "namespace", m.ns, "err", err)
	}
	m.s.made.Delete(m.schema)
}

// moveIfFile moves a store that is a file, when the policy says to. A move that failed is
// logged and is not an error: the store opens on its file next. One in progress
// elsewhere, or a cancelled context, is an error.
func moveIfFile(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir string, open func(*sql.DB) (io.Closer, error)) error {
	mode, err := MoveMode()
	if err != nil {
		return err
	}
	if mode == Never {
		return nil
	}
	place, err := Where(ns, subsystem, dir)
	if err != nil {
		return err
	}
	if place == OnServer {
		return nil
	}
	rep, err := Move(ctx, log, ns, subsystem, dir, open)
	switch {
	case err == nil && rep.Already:
		return nil
	case err == nil:
		if log != nil {
			log.Info("sql move: store moved to hanzo-sql", "subsystem", subsystem, "namespace", ns,
				"tables", len(rep.Tables), "rows", rep.Rows, "bytes", rep.Bytes, "took", rep.Took.Round(time.Millisecond),
				"mb_per_s", rate(rep.Bytes, rep.Took), "left_on_the_file", rep.Dropped)
		}
		return nil
	case errors.Is(err, ErrMoving):
		return err
	case ctx.Err() != nil:
		return ctx.Err()
	}
	if log != nil {
		log.Warn("sql move: the store stays on its file", "subsystem", subsystem, "namespace", ns, "err", err)
	}
	return nil
}

func rate(bytes int64, took time.Duration) float64 {
	if took <= 0 {
		return 0
	}
	return float64(bytes) / 1e6 / took.Seconds()
}

// source opens the file the move reads: the store's encrypted file, or the plain SQLite
// file of a module that opened its own.
func (m *mover) source() (*sql.DB, error) {
	if m.legacy == "" {
		return locus.Local(m.ns, m.subsystem, m.dir)
	}
	db, err := sql.Open("sqlite", sqlite.PragmaDSN(m.legacy, sqlite.DefaultPragmas))
	if err != nil {
		return nil, err
	}
	locus.Single(db)
	return db, nil
}

// openFile opens the file the move reads, brought to the shape the current code
// writes, and answers what is held open beside the handle.
func (m *mover) openFile() (*sql.DB, io.Closer, error) {
	if m.how != nil {
		return m.how.file(m)
	}
	file, err := m.source()
	if err != nil {
		return nil, nil, err
	}
	store, err := m.open(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("open the file: %w", err)
	}
	return file, store, nil
}

// openSchema makes the store's tables in the schema and answers a pool on it.
func (m *mover) openSchema() (*sql.DB, io.Closer, error) {
	if m.how != nil {
		return m.how.schema(m)
	}
	pool := m.s.pool(m.schema)
	store, err := m.open(pool)
	if err != nil {
		_ = pool.Close()
		return nil, nil, fmt.Errorf("migrate the schema: %w", err)
	}
	return pool, store, nil
}

// shape is how a store is opened on each side of a move. A store the seam opens itself
// (an OrgStore) is opened over a handle, the same function on the file and on the
// schema; an engine that opens its own databases from a path and a DSN (a Base) gives
// each side its own way.
type shape struct {
	// file opens the file, brought to the shape the current code writes, and answers
	// the handle the mover reads it through. Whatever it opened that is not the handle
	// is the closer's, held until the move is over.
	file func(*mover) (*sql.DB, io.Closer, error)
	// schema makes the store's tables in the schema and answers a pool on it.
	schema func(*mover) (*sql.DB, io.Closer, error)
}

// MoveFile is [Move] for a store whose engine opens its own file and its own pool: the
// same lock, the same registry, the same copy and the same proof. The schema is made
// empty, f.Make makes its tables, every table the file holds is copied across, and
// every table is read back and held to the file's row count and checksum before the
// registry says moved.
func MoveFile(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem string, f Files) (Report, error) {
	var rep Report
	if DSN() == "" {
		return rep, fmt.Errorf("postgres: %s is not set, so there is nowhere to move %s/%s to", Env, ns, subsystem)
	}
	if !fileExists(f.Path) {
		return rep, fmt.Errorf("postgres: %s/%s has no file at %s to move", ns, subsystem, f.Path)
	}
	return move(ctx, log, ns, subsystem, filepath.Dir(f.Path), f.Path, nil, nil, &shape{
		file: func(*mover) (*sql.DB, io.Closer, error) {
			if err := f.Settle(); err != nil {
				return nil, nil, fmt.Errorf("open the file: %w", err)
			}
			db, err := sql.Open("sqlite", sqlite.PragmaDSN(f.Path, sqlite.DefaultPragmas))
			if err != nil {
				return nil, nil, err
			}
			locus.Single(db)
			return db, db, nil
		},
		schema: func(m *mover) (*sql.DB, io.Closer, error) {
			if err := f.Make(func() (*sql.DB, error) { return m.s.described(m.schema), nil }); err != nil {
				return nil, nil, fmt.Errorf("make the schema: %w", err)
			}
			return m.s.described(m.schema), nil, nil
		},
	})
}

// Revert puts a store that was moved back to being its file: the registry row is
// deleted and the schema dropped, which is the rollback LLM.md names, as one call. It
// is for a caller that moved two stores that must live together (a Base's data and
// auxiliary databases) and could move only one: nothing may have been written to the
// schema since it was moved, because what was is not carried back to the file. It
// takes the store's move lock, so it cannot run beside a move.
func Revert(ctx context.Context, ns namespace.Namespace, subsystem string) error {
	if DSN() == "" {
		return nil
	}
	schema, err := Schema(ns, subsystem)
	if err != nil {
		return err
	}
	s, err := serverOf(DSN())
	if err != nil {
		return err
	}
	lock, err := s.lockMove(ctx, schema)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if _, err := lock.ExecContext(ctx, `DELETE FROM `+registry+` WHERE schema = $1`, schema); err != nil {
		return err
	}
	if _, err := lock.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+quote(schema)+` CASCADE`); err != nil {
		return err
	}
	s.moved.Delete(schema)
	s.made.Delete(schema)
	return nil
}
