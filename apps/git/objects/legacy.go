package objects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// A release before packs kept every object as a row of a table called objects, its body
// the zlib stream a pack entry holds. Opening a store moves what is left of them into
// packs, one repository at a time under that repository's lock, and then drops the table.
// A row is exactly a pack entry's stream, so nothing is inflated or deflated on the way.
//
// A repository's move is one transaction: its packs are uploaded, indexed, and checked
// against the rows (every row has an index row, and there are as many of each), and only
// then are the rows deleted. A move that fails leaves the rows where they were and the
// store does not open; the next open starts that repository again, and the objects that
// already have an index row are not written twice.

func (s *Store) moveLegacy(ctx context.Context) error {
	ok, err := s.d.HasTable(ctx, s.db, "objects")
	if err != nil || !ok {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT repo FROM objects`)
	if err != nil {
		return s.gone(ctx, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return s.gone(ctx, err)
	}
	for _, id := range ids {
		if err := s.moveRepo(ctx, id); err != nil {
			return fmt.Errorf("repository %s: %w", id, err)
		}
	}
	var left int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects`).Scan(&left); err != nil {
		return s.gone(ctx, err)
	}
	if left != 0 {
		return fmt.Errorf("%d rows are still in the objects table", left)
	}
	return s.d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS objects`)
		return err
	})
}

// gone is nil when err is the table having been dropped by another replica that finished
// the move first.
func (s *Store) gone(ctx context.Context, err error) error {
	if ok, e := s.d.HasTable(ctx, s.db, "objects"); e == nil && !ok {
		return nil
	}
	return err
}

func (s *Store) moveRepo(ctx context.Context, id string) error {
	t, err := s.begin(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = t.Rollback() }()
	after := []byte{}
	var total int64
	for {
		q, err := t.tx.QueryContext(ctx,
			`SELECT oid, type, size, data FROM objects WHERE repo = $1 AND oid > $2 ORDER BY oid LIMIT 500`, id, after)
		if err != nil {
			return s.gone(ctx, err)
		}
		n := 0
		for q.Next() {
			var oid, z []byte
			var typ, size int64
			if err := q.Scan(&oid, &typ, &size, &z); err != nil {
				_ = q.Close()
				return err
			}
			after = oid
			n++
			total++
			t.b.add(row{oid: hashOf(oid), typ: plumbing.ObjectType(typ), size: size, z: z})
		}
		err = q.Err()
		_ = q.Close()
		if err != nil {
			return err
		}
		if t.b.buf.Len() >= packMax {
			if err := t.flush(); err != nil {
				return err
			}
		}
		if n < 500 {
			break
		}
	}
	if total == 0 {
		return nil
	}
	if err := t.flush(); err != nil {
		return err
	}
	var unindexed, indexed int64
	if err := t.tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM objects o WHERE o.repo = $1 AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.repo = o.repo AND e.oid = o.oid)`,
		id).Scan(&unindexed); err != nil {
		return err
	}
	if err := t.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE repo = $1`, id).Scan(&indexed); err != nil {
		return err
	}
	if unindexed != 0 || indexed != total {
		return fmt.Errorf("%d objects were in rows, %d are in packs and %d of the rows have no index row", total, indexed, unindexed)
	}
	// Spot-check that the first and last object resolve through a pack, which is the path
	// every read takes, before the rows that could answer instead are deleted.
	for _, oid := range [][]byte{firstOID(ctx, t, id, "ASC"), firstOID(ctx, t, id, "DESC")} {
		if oid == nil {
			continue
		}
		if _, err := t.Repo.EncodedObject(plumbing.AnyObject, hashOf(oid)); err != nil {
			return fmt.Errorf("object %s does not resolve from its pack: %w", hashOf(oid), err)
		}
	}
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM objects WHERE repo = $1`, id); err != nil {
		return err
	}
	// The counter was the rows' bytes and is now the packs'.
	if _, err := t.tx.ExecContext(ctx, `UPDATE repos SET bytes = 0 WHERE repo = $1`, id); err != nil {
		return err
	}
	return t.Commit()
}

func firstOID(ctx context.Context, t *Tx, id, dir string) []byte {
	var oid []byte
	err := t.tx.QueryRowContext(ctx, `SELECT oid FROM entries WHERE repo = $1 ORDER BY oid `+dir+` LIMIT 1`, id).Scan(&oid)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return nil
	}
	return oid
}
