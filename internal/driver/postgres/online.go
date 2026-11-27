package postgres

// online.go — a store that is written to while it moves.
//
// [Move] holds the file's write lock from the first row it reads to the flip, which for
// a store of tens of gigabytes is long enough that every write to it fails. A store
// whose one table is APPEND-ONLY, rows only ever added, in increasing order of an
// integer key, can move without that: what was already written never changes, so it can
// be copied while the store keeps serving, and only what was appended meanwhile needs
// the store held still.
//
//	phase 1   no lock on the file. Batches of rows are read in key order through the
//	          store's own read handle and COPYed into the schema, and each batch is read
//	          back and held to the file's count and checksum before the next. It ends
//	          when a batch comes up short: the copy has caught up with the writer.
//	phase 2   [Online.Freeze] holds the store still (its writer waits, the file's write
//	          lock is taken). The rows appended since are copied and checked the same
//	          way, the last row of each side is held equal whole (for a hash chain, its
//	          head), the registry says moved, and [Online.Switch] hands the store the
//	          pool it writes to from now on. The writer waits for the tail and no more.
//
// A FAILURE IS A STORE THAT STAYS A FILE, as in [Move]: nothing here changes the file,
// the schema is dropped and the registry says why, and a process killed at any point
// leaves the registry at moving with no lock held, which the next mover takes as a
// failure that did not get to say so.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/namespace"
	"github.com/jackc/pgx/v5"
	luxlog "github.com/luxfi/log"
)

// freezeBound is how long phase 2 may hold the store still. It copies what was
// appended while phase 1 ran, which is seconds; a freeze that runs past this is a
// failure, and the store keeps its file.
var freezeBound = 2 * time.Minute

// MoveOnline moves the live store (ns, subsystem) from its file to hanzo-sql while its
// writer keeps appending. open is the store's own open over a database handle,
// migration included, run on the schema to make its tables; the handle is the store's
// to close. The errors are [Move]'s.
func MoveOnline(ctx context.Context, log luxlog.Logger, ns namespace.Namespace, subsystem, dir string, open func(*sql.DB) (io.Closer, error), on Online) (Report, error) {
	return move(ctx, log, ns, subsystem, dir, "", open, &on, nil)
}

func (m *mover) runOnline(ctx context.Context, rep *Report, on Online) error {
	if err := m.begin(ctx); err != nil {
		return err
	}
	pool := m.s.pool(m.schema)
	pooled, err := m.open(pool)
	if err != nil {
		_ = pool.Close()
		return fmt.Errorf("migrate the schema: %w", err)
	}
	defer func() { _ = pooled.Close() }()

	files, err := fileTables(ctx, on.Read)
	if err != nil {
		return err
	}
	target, needs, err := targetColumns(ctx, pool, m.schema)
	if err != nil {
		return err
	}
	p, err := makePlan(ctx, on.Read, target, needs, files)
	if err != nil {
		return err
	}
	rep.Dropped = p.dropped
	if len(p.tables) != 1 || p.tables[0].name != on.Table {
		return fmt.Errorf("an online move carries one append-only table, %s, and the store has %d tables to move", on.Table, len(p.tables))
	}
	t := p.tables[0]
	t.key = on.Key
	if !hasKey(t) {
		return fmt.Errorf("%s has no integer column %s to walk by", on.Table, on.Key)
	}
	if _, err := m.lock.ExecContext(ctx, `TRUNCATE `+quote(m.schema)+"."+quote(t.name)); err != nil {
		return err
	}

	batch := on.Batch
	if batch <= 0 {
		batch = 10000
	}
	key := quote(on.Key)
	var sum total
	var bytes int64
	next := int64(math.MinInt64)
	began := time.Now()

	// copy takes the rows from next on that tail selects, checks them on the server and
	// answers the key after the last, so one call is one verified range.
	copied := func(src Querier, tail string, args ...any) (total, error) {
		var got total
		var n, last int64
		err := m.lock.pgx(func(c *pgx.Conn) error {
			var err error
			got, n, last, err = copyRows(ctx, src, c, m.schema, t, tail, args...)
			return err
		})
		if err != nil {
			return total{}, err
		}
		bytes += n
		if got.rows == 0 {
			return got, nil
		}
		back, err := readBackWhere(ctx, pool, t, `WHERE `+key+` >= $1 AND `+key+` <= $2`, next, last)
		if err != nil {
			return total{}, err
		}
		if back != got {
			return total{}, fmt.Errorf("rows %d to %d of %s did not arrive as they left: the file has %d rows with checksum %x, hanzo-sql has %d with %x", next, last, t.name, got.rows, got.sum[:8], back.rows, back.sum[:8])
		}
		next = last + 1
		sum = sum.plus(got)
		return got, nil
	}

	// Phase 1: the file keeps serving.
	for {
		got, err := copied(on.Read, `WHERE `+key+` >= $1 ORDER BY `+key+fmt.Sprintf(` LIMIT %d`, batch), next)
		if err != nil {
			return err
		}
		if got.rows < int64(batch) {
			break
		}
	}
	m.say("sql move: copied ahead, now holding the store for the tail", "table", t.name, "rows", sum.rows, "bytes", bytes, "took", time.Since(began).Round(time.Millisecond))

	// Phase 2: the store is held still, and only what was appended since is copied.
	frozen, cancel := context.WithTimeout(ctx, freezeBound)
	defer cancel()
	held := time.Now()
	err = on.Freeze(frozen, func(q Querier) error {
		if _, err := copied(q, `WHERE `+key+` >= $1 ORDER BY `+key, next); err != nil {
			return err
		}
		// The last row of each side, whole: for a hash chain, the head.
		want, err := fileHead(frozen, q, t)
		if err != nil {
			return err
		}
		got, err := readBackWhere(frozen, pool, t, `ORDER BY `+key+` DESC LIMIT 1`)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("the last row of %s is not the same on both sides", t.name)
		}
		if err := dialect.Of(pool).Analyze(frozen, pool, t.name); err != nil {
			return err
		}
		db, err := onServer(DSN(), m.ns, m.subsystem)
		if err != nil {
			return err
		}
		if err := m.flip(frozen); err != nil {
			_ = db.Close()
			return err
		}
		on.Switch(db)
		return nil
	})
	if err != nil {
		return err
	}
	rep.Rows, rep.Bytes = sum.rows, bytes
	rep.Tables = append(rep.Tables, TableReport{Name: t.name, Rows: sum.rows, Bytes: bytes, Took: time.Since(began)})
	m.say("sql move: store moved to hanzo-sql", "table", t.name, "rows", sum.rows, "held_for", time.Since(held).Round(time.Millisecond))
	return nil
}

func hasKey(t *table) bool {
	for _, c := range t.cols {
		if c.name == t.key && c.kind == kInt {
			return true
		}
	}
	return false
}

// fileHead is the checksum of the file's last row, taken the way the server's is.
func fileHead(ctx context.Context, q Querier, t *table) (total, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+strings.Join(t.read, ", ")+` FROM `+quote(t.from)+` ORDER BY `+quote(t.key)+` DESC LIMIT 1`)
	if err != nil {
		return total{}, fmt.Errorf("read the last row of %s: %w", t.from, err)
	}
	defer func() { _ = rows.Close() }()
	src := newSource(rows, t)
	for src.Next() {
	}
	return src.sum, src.err
}
