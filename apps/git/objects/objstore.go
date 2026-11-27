package objects

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// Repo is one repository as go-git's storage.Storer, so go-git reads it (a tree, a
// commit, a blame) and writes it (a tag, a client-less push) as it would a directory.
//
// Objects are in packs in the blob store; the database says which pack and where. A
// read is one row and one range of one pack, and the range is a block, so the next
// object beside it (a tree and its neighbours, written together) is already here.
type Repo struct {
	ctx   context.Context
	q     querier
	s     *Store
	d     dialect.Dialect
	id    string
	tx    *Tx // non-nil on a Repo that was begun on a transaction
	cache *cache.ObjectLRU
}

var _ storer.EncodedObjectStorer = (*Repo)(nil)

// cacheSize bounds the objects one Repo remembers: a walk reads the same trees again
// and again, and a request's Repo is gone when it ends.
const cacheSize = 32 * cache.MiByte

func newRepo(ctx context.Context, q querier, s *Store, id string) *Repo {
	return &Repo{ctx: ctx, q: q, s: s, d: s.d, id: id, cache: cache.NewObjectLRU(cacheSize)}
}

// ID is the repository's id.
func (r *Repo) ID() string { return r.id }

// stored is a stored object: the pack entry's deflated body, inflated when it is read,
// or the content itself when a delta had to be applied to make it.
type stored struct {
	hash plumbing.Hash
	typ  plumbing.ObjectType
	size int64
	z    []byte
	raw  []byte
}

func (o *stored) Hash() plumbing.Hash         { return o.hash }
func (o *stored) Type() plumbing.ObjectType   { return o.typ }
func (o *stored) SetType(plumbing.ObjectType) {}
func (o *stored) Size() int64                 { return o.size }
func (o *stored) SetSize(int64)               {}
func (o *stored) Reader() (io.ReadCloser, error) {
	if o.raw != nil {
		return io.NopCloser(bytes.NewReader(o.raw)), nil
	}
	return zlib.NewReader(bytes.NewReader(o.z))
}
func (o *stored) Writer() (io.WriteCloser, error) {
	return nil, errors.New("objects: a stored object is read only")
}

// row is an object ready to be written.
type row struct {
	oid  plumbing.Hash
	typ  plumbing.ObjectType
	size int64
	z    []byte
}

func (r *Repo) NewEncodedObject() plumbing.EncodedObject { return &plumbing.MemoryObject{} }

// SetEncodedObject names o by its content, deflates it and keeps it. An object that is
// already there is left alone; the hash is the content, so there is nothing to update.
// Written through a transaction it joins the transaction's next pack; written on its
// own it is a pack of one, which is why a writer of many objects begins one.
func (r *Repo) SetEncodedObject(o plumbing.EncodedObject) (plumbing.Hash, error) {
	t := o.Type()
	switch t {
	case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
	default:
		return plumbing.ZeroHash, fmt.Errorf("objects: %s is not an object type a repository keeps", t)
	}
	rd, err := o.Reader()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	content, err := io.ReadAll(io.LimitReader(rd, MaxObject+1))
	_ = rd.Close()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if len(content) > MaxObject {
		return plumbing.ZeroHash, ErrTooLarge
	}
	h := plumbing.ComputeHash(t, content)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(content); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := zw.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	rw := row{oid: h, typ: t, size: int64(len(content)), z: z.Bytes()}
	if r.tx != nil {
		if err := r.tx.add(rw, content); err != nil {
			return plumbing.ZeroHash, err
		}
		r.remember(rw)
		return h, nil
	}
	tx, err := r.s.begin(r.ctx, r.id)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.add(rw, content); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := tx.Commit(); err != nil {
		return plumbing.ZeroHash, err
	}
	r.remember(rw)
	return h, nil
}

// settle writes what a transaction holds back, before a read that lists or counts.
func (r *Repo) settle() error {
	if r.tx != nil {
		return r.tx.flush()
	}
	return nil
}

// remember keeps a small object just written, so the read that follows it (a delta's
// base, a commit's tree) is not a round trip.
func (r *Repo) remember(w row) {
	if w.size <= cacheable {
		r.cache.Put(&stored{hash: w.oid, typ: w.typ, size: w.size, z: w.z})
	}
}

// cacheable is the largest object a Repo remembers.
const cacheable = 1 << 20

// held is the object a transaction has written and not yet flushed, if it has.
func (r *Repo) held(h plumbing.Hash) (*stored, bool) {
	if r.tx == nil {
		return nil, false
	}
	p, ok := r.tx.held[h]
	if !ok {
		return nil, false
	}
	return &stored{hash: h, typ: p.typ, size: p.size, z: p.z}, true
}

// loc is where the index says an object is.
type loc struct {
	oid  plumbing.Hash
	pack string
	at   int64
	n    int64
	typ  plumbing.ObjectType
	size int64
	base []byte
}

const locCols = `oid, pack, at, n, type, size, base`

func scanLoc(sc interface{ Scan(...any) error }) (loc, error) {
	var l loc
	var oid []byte
	var typ int64
	err := sc.Scan(&oid, &l.pack, &l.at, &l.n, &typ, &l.size, &l.base)
	l.oid, l.typ = hashOf(oid), plumbing.ObjectType(typ)
	return l, err
}

// locKey is where a location is remembered by the pod.
func (r *Repo) locKey(h plumbing.Hash) string {
	return r.s.tag + "|" + r.id + "|" + string(h[:])
}

// remembered is the location of h if this pod read it lately. A Repo that is in a
// transaction reads and remembers nothing: what it sees is not committed.
func (r *Repo) remembered(h plumbing.Hash) (loc, bool) {
	if r.tx != nil {
		return loc{}, false
	}
	return r.s.cache.locs.get(r.locKey(h))
}

func (r *Repo) keep(l loc) {
	if r.tx == nil {
		r.s.cache.locs.put(r.locKey(l.oid), l, locCost, locTTL)
	}
}

// lookup is the index row of h.
func (r *Repo) lookup(h plumbing.Hash) (loc, bool, error) {
	if l, ok := r.remembered(h); ok {
		return l, true, nil
	}
	l, err := scanLoc(r.q.QueryRowContext(r.ctx, `SELECT `+locCols+` FROM entries WHERE repo = $1 AND oid = $2`, r.id, h[:]))
	if errors.Is(err, sql.ErrNoRows) {
		return loc{}, false, nil
	}
	if err != nil {
		return loc{}, false, fmt.Errorf("objects: read %s: %w", h, err)
	}
	r.keep(l)
	return l, true, nil
}

// lookupChunk is how many names one index query asks for.
const lookupChunk = 1000

// locs is the index rows of oids, those that have one: what this pod remembers, and the
// rest in one query for each lookupChunk of them.
func (r *Repo) locs(oids []plumbing.Hash) (map[plumbing.Hash]loc, error) {
	out := make(map[plumbing.Hash]loc, len(oids))
	var ask []plumbing.Hash
	for _, h := range oids {
		if l, ok := r.remembered(h); ok {
			out[h] = l
		} else {
			ask = append(ask, h)
		}
	}
	for len(ask) > 0 {
		n := min(lookupChunk, len(ask))
		args := []any{r.id}
		for _, h := range ask[:n] {
			args = append(args, h[:])
		}
		rows, err := r.q.QueryContext(r.ctx,
			`SELECT `+locCols+` FROM entries WHERE repo = $1 AND oid IN (`+dialect.Marks(2, n)+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("objects: index: %w", err)
		}
		for rows.Next() {
			l, err := scanLoc(rows)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("objects: index: %w", err)
			}
			out[l.oid] = l
			r.keep(l)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("objects: index: %w", err)
		}
		ask = ask[n:]
	}
	return out, nil
}

// Prefetch reads the index rows of oids in one round trip per lookupChunk and keeps them,
// so the reads that follow (the children of a tree, the bases of a thin pack) are not a
// round trip each. Names the repository does not have are ignored.
func (r *Repo) Prefetch(oids []plumbing.Hash) error {
	if r.tx != nil || len(oids) == 0 {
		return nil
	}
	_, err := r.locs(oids)
	return err
}

// EncodedObject reads one object, of type t or of any type.
func (r *Repo) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if o, ok := r.cache.Get(h); ok {
		if t == plumbing.AnyObject || o.Type() == t {
			return o, nil
		}
		return nil, plumbing.ErrObjectNotFound
	}
	if o, ok := r.held(h); ok {
		if t != plumbing.AnyObject && o.typ != t {
			return nil, plumbing.ErrObjectNotFound
		}
		return o, nil
	}
	l, ok, err := r.lookup(h)
	if err != nil {
		return nil, err
	}
	if !ok || t != plumbing.AnyObject && l.typ != t {
		return nil, plumbing.ErrObjectNotFound
	}
	o, err := r.load(l, 0)
	if err != nil {
		return nil, err
	}
	if o.size <= cacheable {
		r.cache.Put(o)
	}
	return o, nil
}

// maxChain bounds how deep a delta chain is followed on a read.
const maxChain = 4096

// load reads the entry l describes. A whole object is its stored stream; a delta is
// applied to its base, which is read the same way.
func (r *Repo) load(l loc, depth int) (*stored, error) {
	raw, err := r.read(l.pack, l.at, l.n)
	if err != nil {
		return nil, err
	}
	return r.loadRaw(l, raw, depth)
}

// loadRaw is load for an entry whose bytes are in hand.
func (r *Repo) loadRaw(l loc, raw []byte, depth int) (*stored, error) {
	hd, err := parseHead(raw)
	if err != nil {
		return nil, fmt.Errorf("objects: %s: %w", l.oid, err)
	}
	if l.base == nil {
		return &stored{hash: l.oid, typ: l.typ, size: l.size, z: raw[hd.n:]}, nil
	}
	if depth > maxChain {
		return nil, fmt.Errorf("objects: %s: delta chain is longer than %d", l.oid, maxChain)
	}
	bl, ok, err := r.lookup(hashOf(l.base))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("objects: %s: its delta base %s is not in the repository", l.oid, hashOf(l.base))
	}
	bo, err := r.baseObject(bl, depth+1)
	if err != nil {
		return nil, err
	}
	rd, err := bo.Reader()
	if err != nil {
		return nil, err
	}
	base, err := io.ReadAll(rd)
	_ = rd.Close()
	if err != nil {
		return nil, err
	}
	delta, err := inflate(raw[hd.n:], hd.size)
	if err != nil {
		return nil, fmt.Errorf("objects: %s: %w", l.oid, err)
	}
	out, err := packfile.PatchDelta(base, delta)
	if err != nil {
		return nil, fmt.Errorf("objects: %s: %w", l.oid, err)
	}
	return &stored{hash: l.oid, typ: l.typ, size: int64(len(out)), raw: out}, nil
}

// baseObject is a delta's base: this Repo's cache, or the entry.
func (r *Repo) baseObject(l loc, depth int) (*stored, error) {
	if o, ok := r.cache.Get(l.oid); ok {
		if s, ok := o.(*stored); ok {
			return s, nil
		}
	}
	o, err := r.load(l, depth)
	if err != nil {
		return nil, err
	}
	if o.size <= cacheable {
		r.cache.Put(o)
	}
	return o, nil
}

// HasEncodedObject is nil when the object is there.
func (r *Repo) HasEncodedObject(h plumbing.Hash) error {
	if _, ok := r.cache.Get(h); ok {
		return nil
	}
	if _, ok := r.held(h); ok {
		return nil
	}
	var n int
	if err := r.q.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM entries WHERE repo = $1 AND oid = $2`, r.id, h[:]).Scan(&n); err != nil {
		return fmt.Errorf("objects: has %s: %w", h, err)
	}
	if n == 0 {
		return plumbing.ErrObjectNotFound
	}
	return nil
}

// EncodedObjectSize is the object's content length.
func (r *Repo) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	if o, ok := r.held(h); ok {
		return o.size, nil
	}
	var n int64
	err := r.q.QueryRowContext(r.ctx, `SELECT size FROM entries WHERE repo = $1 AND oid = $2`, r.id, h[:]).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, plumbing.ErrObjectNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("objects: size %s: %w", h, err)
	}
	return n, nil
}

// IterEncodedObjects lists the repository's objects of type t, or all of them. The
// names are read at once (twenty bytes an object) and the objects one at a time as the
// iterator moves, so a walk of a large repository holds the names and one object.
func (r *Repo) IterEncodedObjects(t plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	hashes, err := r.Hashes(t)
	if err != nil {
		return nil, err
	}
	return storer.NewEncodedObjectLookupIter(r, t, hashes), nil
}

// Hashes names the repository's objects of type t, or all of them, in hash order.
func (r *Repo) Hashes(t plumbing.ObjectType) ([]plumbing.Hash, error) {
	if err := r.settle(); err != nil {
		return nil, err
	}
	query, args := `SELECT oid FROM entries WHERE repo = $1 ORDER BY oid`, []any{r.id}
	if t != plumbing.AnyObject {
		query, args = `SELECT oid FROM entries WHERE repo = $1 AND type = $2 ORDER BY oid`, []any{r.id, int64(t)}
	}
	rows, err := r.q.QueryContext(r.ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("objects: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []plumbing.Hash
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("objects: list: %w", err)
		}
		out = append(out, hashOf(b))
	}
	return out, rows.Err()
}

// Count is the number of objects the repository holds.
func (r *Repo) Count() (int64, error) {
	if err := r.settle(); err != nil {
		return 0, err
	}
	var n int64
	if err := r.q.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM entries WHERE repo = $1`, r.id).Scan(&n); err != nil {
		return 0, fmt.Errorf("objects: count: %w", err)
	}
	return n, nil
}

// AddAlternate is refused: a repository's objects are its own.
func (r *Repo) AddAlternate(string) error {
	return errors.New("objects: a repository has no alternates")
}

// Absent are those of want the repository does not have.
func (r *Repo) Absent(want []plumbing.Hash) ([]plumbing.Hash, error) {
	if err := r.settle(); err != nil {
		return nil, err
	}
	return r.absent(want)
}

func (r *Repo) absent(want []plumbing.Hash) ([]plumbing.Hash, error) {
	const chunk = 500
	var missing []plumbing.Hash
	if r.tx == nil {
		var ask []plumbing.Hash
		for _, h := range want {
			if _, ok := r.remembered(h); !ok {
				ask = append(ask, h)
			}
		}
		want = ask
	}
	for len(want) > 0 {
		n := min(chunk, len(want))
		batch := want[:n]
		want = want[n:]
		args := []any{r.id}
		for _, h := range batch {
			args = append(args, h[:])
		}
		rows, err := r.q.QueryContext(r.ctx,
			`SELECT oid FROM entries WHERE repo = $1 AND oid IN (`+dialect.Marks(2, len(batch))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("objects: connected: %w", err)
		}
		found := map[plumbing.Hash]struct{}{}
		for rows.Next() {
			var b []byte
			if err := rows.Scan(&b); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("objects: connected: %w", err)
			}
			found[hashOf(b)] = struct{}{}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, fmt.Errorf("objects: connected: %w", err)
		}
		for _, h := range batch {
			if _, ok := found[h]; !ok {
				missing = append(missing, h)
			}
		}
	}
	return missing, nil
}

// ---- reading pack bytes ----

// blockSize is the unit pack bytes are read and remembered in. Objects pushed together
// lie together in a pack, and a walk reads a commit, its tree and the trees under it
// one after another, so a block answers many reads for the price of one.
const blockSize = 256 << 10

// read answers n bytes of pack from offset off.
func (r *Repo) read(pack string, off, n int64) ([]byte, error) {
	if n > 4*blockSize {
		return r.fetch(pack, off, n)
	}
	out := make([]byte, 0, n)
	for pos := off; pos < off+n; {
		bi := pos / blockSize
		blk, err := r.block(pack, bi)
		if err != nil {
			return nil, err
		}
		start := pos - bi*blockSize
		if start >= int64(len(blk)) {
			return nil, fmt.Errorf("objects: pack %s is shorter than the index says (%d of %d)", pack, pos, off+n)
		}
		take := min(int64(len(blk))-start, off+n-pos)
		out = append(out, blk[start:start+take]...)
		pos += take
	}
	return out, nil
}

func (r *Repo) block(pack string, bi int64) ([]byte, error) {
	k := r.s.tag + "|" + r.id + "|" + pack + "|" + strconv.FormatInt(bi, 10)
	c := r.s.cache
	if b, ok := c.blocks.get(k); ok {
		return b, nil
	}
	// Readers of one block share one request.
	v, err, _ := c.flight.Do(k, func() (any, error) {
		if b, ok := c.blocks.get(k); ok {
			return b, nil
		}
		b, err := r.fetch(pack, bi*blockSize, blockSize)
		if err != nil {
			return nil, err
		}
		c.blocks.put(k, b, len(b), 0)
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// fetch is one range of a pack from the blob store.
func (r *Repo) fetch(pack string, off, n int64) ([]byte, error) {
	return r.fetchWith(r.ctx, pack, off, n)
}

func (r *Repo) fetchWith(ctx context.Context, pack string, off, n int64) ([]byte, error) {
	rc, err := r.s.blobs.Range(ctx, r.s.packKey(r.id, pack), off, n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	// The size is known, so the buffer is exact: growing one by doubling holds two of
	// them at once.
	b := make([]byte, n)
	got, err := io.ReadFull(rc, b)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("objects: read pack %s: %w", pack, err)
	}
	return b[:got], nil
}
