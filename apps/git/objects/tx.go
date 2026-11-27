package objects

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// Tx is a repository being written. Everything written through it, objects, refs, config,
// lands at Commit, or none of it does; reads through it see what it has written.
//
// The repository's lock is held from Begin to the end, so another writer of the same
// repository waits, and a writer of another does not.
type Tx struct {
	*Repo
	tx     *sql.Tx
	b      *builder
	held   map[plumbing.Hash]row // written through SetEncodedObject, not yet in a pack
	added  int64                 // bytes of pack this transaction attached
	got    map[plumbing.Hash]struct{}
	refers map[plumbing.Hash]struct{} // oids the objects it was given name
	done   bool
}

// packMax is the size at which objects written one at a time are made a pack and
// uploaded: a copy of a large directory is a series of packs and not one in memory.
const packMax = 64 << 20

// analyzeAfter is how many new index rows in one pack make the planner's statistics worth
// refreshing in the transaction that added them.
const analyzeAfter = 5000

// Begin starts writing repository id, which must be there. Its first statement is a
// write, so the transaction holds the repository from the start: on SQLite that is the
// file's write lock, taken before anything is read and not upgraded to after (a
// transaction that read first and then wrote would be refused if another had written in
// between), and on PostgreSQL the row lock beside the advisory one.
func (s *Store) Begin(ctx context.Context, id string) (*Tx, error) {
	t, err := s.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	res, err := t.tx.ExecContext(ctx, `UPDATE repos SET bytes = bytes WHERE repo = $1`, id)
	if err != nil {
		_ = t.Rollback()
		return nil, fmt.Errorf("objects: begin %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = t.Rollback()
		return nil, ErrNotFound
	}
	return t, nil
}

// Make starts writing a repository that is not there yet: HEAD names branch, the config
// is a bare repository's. It answers ErrExists when id is there.
func (s *Store) Make(ctx context.Context, id, branch string) (*Tx, error) {
	if err := dialect.Text(id, branch); err != nil {
		return nil, fmt.Errorf("objects: make %s: %w", id, err)
	}
	t, err := s.begin(ctx, id)
	if err != nil {
		return nil, err
	}
	res, err := t.tx.ExecContext(ctx,
		`INSERT INTO repos (repo, bytes, created) VALUES ($1, 0, $2) ON CONFLICT (repo) DO NOTHING`, id, now())
	if err != nil {
		_ = t.Rollback()
		return nil, fmt.Errorf("objects: make %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = t.Rollback()
		return nil, ErrExists
	}
	cfg := config.NewConfig()
	cfg.Core.IsBare = true
	if err := t.SetConfig(cfg); err != nil {
		_ = t.Rollback()
		return nil, err
	}
	head := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))
	if err := t.SetReference(head); err != nil {
		_ = t.Rollback()
		return nil, err
	}
	return t, nil
}

func (s *Store) begin(ctx context.Context, id string) (*Tx, error) {
	tx, err := s.d.Serialize(ctx, s.db, id)
	if err != nil {
		return nil, fmt.Errorf("objects: begin %s: %w", id, err)
	}
	t := &Tx{
		tx:     tx,
		b:      newBuilder(),
		held:   map[plumbing.Hash]row{},
		got:    map[plumbing.Hash]struct{}{},
		refers: map[plumbing.Hash]struct{}{},
	}
	t.Repo = newRepo(ctx, tx, s, id)
	t.Repo.tx = t
	return t, nil
}

// add holds an object back to be made a pack with the others written this way, and
// notes what it names so Connected can tell whether the push is whole.
func (t *Tx) add(r row, content []byte) error {
	if err := names(r.typ, content, func(h plumbing.Hash) { t.refers[h] = struct{}{} }); err != nil {
		return fmt.Errorf("objects: %s %s: %w", r.typ, r.oid, err)
	}
	t.got[r.oid] = struct{}{}
	if _, ok := t.held[r.oid]; ok {
		return nil
	}
	t.held[r.oid] = r
	t.b.add(r)
	if t.b.buf.Len() >= packMax {
		return t.flush()
	}
	return nil
}

// flush makes the objects written one at a time a pack: those the repository does not
// hold already, uploaded and indexed in this transaction.
func (t *Tx) flush() error {
	if len(t.b.ents) == 0 {
		return nil
	}
	oids := make([]plumbing.Hash, len(t.b.ents))
	for i, e := range t.b.ents {
		oids[i] = e.oid
	}
	missing, err := t.Repo.absent(oids)
	if err != nil {
		return err
	}
	keep := make(map[plumbing.Hash]struct{}, len(missing))
	for _, h := range missing {
		keep[h] = struct{}{}
	}
	pack, id, ents := t.b.pack(keep)
	t.b = newBuilder()
	clear(t.held)
	if len(ents) == 0 {
		return nil
	}
	if err := t.s.blobs.Put(t.ctx, t.s.packKey(t.id, id), bytes.NewReader(pack), int64(len(pack))); err != nil {
		return err
	}
	return t.stage(id, int64(len(pack)), ents)
}

// Attach makes a pack Ingest uploaded part of the repository, in this transaction.
func (t *Tx) Attach(in *Incoming) error {
	if in.repo != t.id {
		return fmt.Errorf("objects: a pack for %s attached to %s", in.repo, t.id)
	}
	for _, e := range in.ents {
		t.got[e.oid] = struct{}{}
	}
	for h := range in.refers {
		t.refers[h] = struct{}{}
	}
	if len(in.ents) == 0 {
		return nil
	}
	return t.stage(in.pack, in.size, in.ents)
}

// stage indexes a pack that is in the blob store: a row for the pack and a row for each
// object in it that the repository does not have. A pack that adds nothing is not
// recorded; the bytes in the blob store are garbage and Collect removes them.
func (t *Tx) stage(pack string, size int64, ents []ent) error {
	var added int64
	const chunk = 200
	for len(ents) > 0 {
		n := min(chunk, len(ents))
		var a dialect.Args
		var b strings.Builder
		b.WriteString(`INSERT INTO entries (repo, oid, pack, at, n, type, size, base) VALUES `)
		for i, e := range ents[:n] {
			if i > 0 {
				b.WriteByte(',')
			}
			var base any
			if e.base != nil {
				base = e.base
			}
			b.WriteString("(" + a.Mark(t.id) + "," + a.Mark(e.oid[:]) + "," + a.Mark(pack) + "," + a.Mark(e.off) + "," +
				a.Mark(e.n) + "," + a.Mark(int64(e.typ)) + "," + a.Mark(e.size) + "," + a.Mark(base) + ")")
		}
		b.WriteString(` ON CONFLICT (repo, oid) DO NOTHING RETURNING oid`)
		rows, err := t.tx.QueryContext(t.ctx, b.String(), a.Values()...)
		if err != nil {
			return fmt.Errorf("objects: index: %w", err)
		}
		for rows.Next() {
			added++
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("objects: index: %w", err)
		}
		ents = ents[n:]
	}
	if added == 0 {
		return nil
	}
	if added >= analyzeAfter {
		// A table filled in one statement has no statistics until autovacuum looks, and
		// PostgreSQL plans the lookups of the first clone of it as scans.
		if err := t.d.Analyze(t.ctx, t.tx, "entries"); err != nil {
			return fmt.Errorf("objects: analyze: %w", err)
		}
	}
	res, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO packs (repo, pack, bytes, objects, created) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (repo, pack) DO NOTHING`,
		t.id, pack, size, added, now())
	if err != nil {
		return fmt.Errorf("objects: pack row: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		t.added += size
	}
	return nil
}

// Connected is nil when everything the objects written through t name is in the
// repository, and every tip is an object that is: a push that lands is a push whose
// history can be read. Objects named and not given are looked for in the repository, in
// batches; a ref under refs/heads must be a commit.
func (t *Tx) Connected(tips map[plumbing.ReferenceName]plumbing.Hash) error {
	if err := t.flush(); err != nil {
		return err
	}
	var want []plumbing.Hash
	for h := range t.refers {
		if _, ok := t.got[h]; !ok {
			want = append(want, h)
		}
	}
	for _, h := range tips {
		if _, ok := t.got[h]; !ok {
			want = append(want, h)
		}
	}
	if missing, err := t.Absent(want); err != nil {
		return err
	} else if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissing, missing[0])
	}
	for name, h := range tips {
		if !name.IsBranch() {
			continue
		}
		o, err := t.EncodedObject(plumbing.AnyObject, h)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrMissing, h)
		}
		if o.Type() != plumbing.CommitObject {
			return fmt.Errorf("%w: %s points at a %s", ErrNotCommit, name, o.Type())
		}
	}
	return nil
}

// Commit makes the pack of what was written one at a time, adds the bytes to the
// repository's size, and ends the transaction.
func (t *Tx) Commit() error {
	if t.done {
		return errors.New("objects: transaction is over")
	}
	if err := t.flush(); err != nil {
		_ = t.Rollback()
		return err
	}
	if t.added > 0 {
		if _, err := t.tx.ExecContext(t.ctx, `UPDATE repos SET bytes = bytes + $1 WHERE repo = $2`, t.added, t.id); err != nil {
			_ = t.Rollback()
			return fmt.Errorf("objects: size: %w", err)
		}
	}
	t.done = true
	if err := t.tx.Commit(); err != nil {
		return fmt.Errorf("objects: commit: %w", err)
	}
	return nil
}

// Rollback ends the transaction and keeps nothing. After a Commit it does nothing. A pack
// already uploaded is left for Collect: it may have the name of one another writer is
// about to attach, so it is not the transaction's to delete.
func (t *Tx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	t.b = newBuilder()
	if err := t.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

// builder collects objects written one at a time into a pack.
type builder struct {
	buf  bytes.Buffer
	ents []ent
	seen map[plumbing.Hash]struct{}
}

func newBuilder() *builder {
	b := &builder{seen: map[plumbing.Hash]struct{}{}}
	b.buf.Write([]byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0})
	return b
}

func (b *builder) add(r row) {
	if _, ok := b.seen[r.oid]; ok {
		return
	}
	b.seen[r.oid] = struct{}{}
	off := int64(b.buf.Len())
	b.buf.Write(entryHeader(r.typ, r.size))
	b.buf.Write(r.z)
	b.ents = append(b.ents, ent{oid: r.oid, typ: r.typ, size: r.size, off: off, n: int64(b.buf.Len()) - off})
}

// pack is the pack of the entries in keep, with its name and where each entry is in it.
func (b *builder) pack(keep map[plumbing.Hash]struct{}) ([]byte, string, []ent) {
	src := b.buf.Bytes()
	out := make([]byte, 12, b.buf.Len()+hashSize)
	copy(out, src[:12])
	var ents []ent
	for _, e := range b.ents {
		if _, ok := keep[e.oid]; !ok {
			continue
		}
		ne := e
		ne.off = int64(len(out))
		out = append(out, src[e.off:e.off+e.n]...)
		ents = append(ents, ne)
	}
	binary.BigEndian.PutUint32(out[8:], uint32(len(ents)))
	sum := sha1.Sum(out)
	out = append(out, sum[:]...)
	return out, hex.EncodeToString(sum[:]), ents
}

// deflate is z, the zlib stream of content.
func deflate(content []byte) ([]byte, error) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(content); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return z.Bytes(), nil
}
