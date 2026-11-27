package objects

import (
	"bytes"
	"compress/zlib"
	"container/list"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

// ErrBadPack is a pack the client got wrong: damaged, truncated, a delta whose base
// is nowhere, an object the store will not take. It is the client's to fix and never
// the server's fault, which receive-pack reports differently.
var ErrBadPack = errors.New("objects: bad pack")

// Incoming is a received pack that has been read and uploaded and is not yet visible.
// Reading it names every object in it, resolves its deltas (a thin pack's bases come
// from the repository), and finds what each object names; uploading it puts its bytes
// in the blob store under a name of its own. Nothing in the database has changed, so a
// pack that is never attached is an object in a bucket that nothing points at, which
// the collector removes. Attach makes it part of a repository.
type Incoming struct {
	repo   string
	pack   string // the pack's name in the blob store
	size   int64
	ents   []ent
	refers map[plumbing.Hash]struct{}
}

// Objects is how many objects the pack holds.
func (in *Incoming) Objects() int { return len(in.ents) }

// ent is an object in a pack: where its entry is and what it is once resolved.
type ent struct {
	oid  plumbing.Hash
	typ  plumbing.ObjectType
	size int64
	off  int64
	n    int64
	base []byte // the oid of the delta base, or nil for an entry that is not a delta
}

const (
	// scanBuffer is the read-ahead of the scan: the most of a pack that is in memory
	// beyond the one object being read.
	scanBuffer = 1 << 20
	// spoolMax is the largest pack kept in memory while it is read, so resolving the
	// deltas of a small push needs no request to the blob store. A larger pack is read
	// back from the bucket, a block at a time.
	spoolMax = 16 << 20
)

// Ingest reads the received pack from r while it uploads it to the blob store for
// repository id. It holds no lock and writes no row: a long upload does not hold a
// connection or the repository.
//
// The pack is a stream. Each entry is inflated and named as it passes, a blob into a
// hash and never into memory, and the bytes go on to the bucket as a multipart upload,
// so a pack of any size costs a read buffer, the largest tree or commit in it, and the
// names of its objects. A delta is not kept: its place is noted, and once the upload has
// landed it is read back from the bucket, a block at a time, to be applied to its base.
// The pack is named by chance and not by its checksum, which is not known until the
// end, so a name is nobody's but this upload's and a pack that is refused is deleted at
// once.
func (s *Store) Ingest(ctx context.Context, id string, r io.Reader) (*Incoming, error) {
	var rnd [16]byte
	_, _ = rand.Read(rnd[:])
	name := hex.EncodeToString(rnd[:])
	key := s.packKey(id, name)
	up := s.startUpload(ctx, key)
	sr := &scanReader{src: r, sink: up.pw, buf: make([]byte, scanBuffer), h: sha1.New(), spooling: true}
	g := &ing{ctx: ctx, s: s, id: id, cache: newContentLRU(64 << 20), byOid: map[plumbing.Hash]int{}, byOff: map[int64]int{}, refers: map[plumbing.Hash]struct{}{}}

	fail := func(err error) (*Incoming, error) {
		up.abort(err)
		s.discard(key)
		return nil, err
	}
	if err := g.scan(sr); err != nil {
		return fail(err)
	}
	if err := up.finish(); err != nil {
		s.discard(key)
		return nil, err
	}
	if sr.spooling {
		g.src = memAt(sr.spool)
	} else {
		g.src = &blobAt{ctx: ctx, blobs: s.blobs, key: key, size: sr.total, blocks: newLRU[[]byte](64 << 20)}
	}
	if err := g.resolve(); err != nil {
		s.discard(key)
		return nil, err
	}
	in := &Incoming{repo: id, pack: name, size: sr.total, refers: g.refers}
	seen := make(map[plumbing.Hash]struct{}, len(g.es))
	for i := range g.es {
		e := &g.es[i]
		if _, ok := seen[e.oid]; ok {
			continue
		}
		seen[e.oid] = struct{}{}
		var base []byte
		if e.delta {
			if e.parent >= 0 {
				b := g.es[e.parent].oid
				base = b[:]
			} else {
				b := e.ext
				base = b[:]
			}
		}
		in.ents = append(in.ents, ent{oid: e.oid, typ: e.typ, size: e.rsize, off: e.off, n: e.end - e.off, base: base})
	}
	if len(in.ents) == 0 {
		s.discard(key)
	}
	return in, nil
}

// discard deletes a pack this process uploaded and nobody has been told of. It does not
// depend on the request's context, which may be what ended.
func (s *Store) discard(key string) {
	_ = s.blobs.Delete(context.WithoutCancel(context.Background()), key)
}

// upload is a pack on its way to the blob store from a writer.
type upload struct {
	pw   *io.PipeWriter
	done chan error
}

func (s *Store) startUpload(ctx context.Context, key string) *upload {
	pr, pw := io.Pipe()
	u := &upload{pw: pw, done: make(chan error, 1)}
	go func() {
		err := s.blobs.Put(ctx, key, pr, -1)
		if err == nil {
			err = io.EOF
		}
		// A Put that ends early must not leave the writer blocked.
		pr.CloseWithError(err)
		if err == io.EOF {
			err = nil
		}
		u.done <- err
	}()
	return u
}

// finish ends the stream and waits for the object to be complete.
func (u *upload) finish() error {
	_ = u.pw.Close()
	return <-u.done
}

// abort ends the stream in failure, which makes the multipart upload abort.
func (u *upload) abort(cause error) {
	_ = u.pw.CloseWithError(cause)
	<-u.done
}

// scanReader is the pack as it arrives: a buffer in front of the source that hashes what
// has been consumed, passes everything it reads on to the upload, and keeps the pack
// whole while it is small. It is a flate.Reader, so inflating from it takes exactly the
// bytes of a stream and no more, which is how an entry's end is found.
type scanReader struct {
	src  io.Reader
	sink io.Writer
	buf  []byte
	r, w int
	hp   int   // buf[hp:r] is consumed and not yet hashed
	base int64 // the offset in the pack of buf[0]
	h    hash.Hash
	// total is the bytes read from the source so far.
	total    int64
	spool    []byte
	spooling bool
	err      error // the source's, or the sink's, once it has failed
}

// refill discards what is consumed and reads more.
func (s *scanReader) refill() {
	if s.err != nil {
		return
	}
	s.h.Write(s.buf[s.hp:s.r])
	copy(s.buf, s.buf[s.r:s.w])
	s.base += int64(s.r)
	s.w -= s.r
	s.r, s.hp = 0, 0
	n, err := s.src.Read(s.buf[s.w:])
	if n > 0 {
		chunk := s.buf[s.w : s.w+n]
		if _, werr := s.sink.Write(chunk); werr != nil {
			s.err = werr
			return
		}
		s.total += int64(n)
		if s.spooling {
			if int64(len(s.spool))+int64(n) > spoolMax {
				s.spool, s.spooling = nil, false
			} else {
				s.spool = append(s.spool, chunk...)
			}
		}
		s.w += n
	}
	if err != nil {
		s.err = err
	}
}

func (s *scanReader) Read(p []byte) (int, error) {
	if s.r == s.w {
		s.refill()
		if s.r == s.w {
			return 0, s.err
		}
	}
	n := copy(p, s.buf[s.r:s.w])
	s.r += n
	return n, nil
}

func (s *scanReader) ReadByte() (byte, error) {
	if s.r == s.w {
		s.refill()
		if s.r == s.w {
			return 0, s.err
		}
	}
	b := s.buf[s.r]
	s.r++
	return b, nil
}

// peek is up to n bytes at the read position, fewer only at the end of the pack.
func (s *scanReader) peek(n int) []byte {
	for s.w-s.r < n && s.err == nil {
		s.refill()
	}
	return s.buf[s.r:min(s.r+n, s.w)]
}

func (s *scanReader) skip(n int) { s.r += n }

// pos is the offset of the next unread byte.
func (s *scanReader) pos() int64 { return s.base + int64(s.r) }

// sum is the SHA-1 of every byte consumed.
func (s *scanReader) sum() []byte {
	s.h.Write(s.buf[s.hp:s.r])
	s.hp = s.r
	return s.h.Sum(nil)
}

// packAt reads a pack that has been received.
type packAt interface {
	at(off, n int64) ([]byte, error)
}

type memAt []byte

func (m memAt) at(off, n int64) ([]byte, error) {
	if off < 0 || off+n > int64(len(m)) {
		return nil, bad("an entry at %d runs past the pack", off)
	}
	return m[off : off+n], nil
}

// blobAt reads a pack back from the blob store in blocks.
type blobAt struct {
	ctx    context.Context
	blobs  Blobs
	key    string
	size   int64
	blocks *lru[[]byte]
}

const ingestBlock = 1 << 20

func (b *blobAt) get(off, n int64) ([]byte, error) {
	rc, err := b.blobs.Range(b.ctx, b.key, off, n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	out, err := io.ReadAll(io.LimitReader(rc, n))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) != n {
		return nil, fmt.Errorf("objects: pack %s is shorter than it was written", b.key)
	}
	return out, nil
}

func (b *blobAt) at(off, n int64) ([]byte, error) {
	if off < 0 || off+n > b.size {
		return nil, bad("an entry at %d runs past the pack", off)
	}
	if n > 2*ingestBlock {
		return b.get(off, n)
	}
	out := make([]byte, 0, n)
	for pos := off; pos < off+n; {
		bi := pos / ingestBlock
		k := fmt.Sprint(bi)
		blk, ok := b.blocks.get(k)
		if !ok {
			var err error
			if blk, err = b.get(bi*ingestBlock, min(ingestBlock, b.size-bi*ingestBlock)); err != nil {
				return nil, err
			}
			b.blocks.put(k, blk, len(blk), 0)
		}
		start := pos - bi*ingestBlock
		take := min(int64(len(blk))-start, off+n-pos)
		out = append(out, blk[start:start+take]...)
		pos += take
	}
	return out, nil
}

// pe is one entry of a pack being read.
type pe struct {
	off, end int64
	hn       int // bytes of header and reference
	typ      plumbing.ObjectType
	size     int64 // inflated length of the entry's stream
	delta    bool
	ofs      int64
	ref      plumbing.Hash
	// resolved
	done   bool
	oid    plumbing.Hash
	rsize  int64
	parent int // index of the base entry, or -1 when it is in the repository
	ext    plumbing.Hash
}

type ing struct {
	ctx    context.Context
	s      *Store
	id     string
	src    packAt
	es     []pe
	byOid  map[plumbing.Hash]int
	byOff  map[int64]int
	cache  *contentLRU
	refers map[plumbing.Hash]struct{}
	repo   *Repo
}

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadPack, fmt.Sprintf(format, a...))
}

// scan reads every entry as it arrives: where it starts and ends, and for a base object
// its name. The source's own errors (a dropped connection, a failed upload) are returned
// as they are; what is wrong with the pack is ErrBadPack.
func (g *ing) scan(sr *scanReader) error {
	hd := sr.peek(12)
	if len(hd) < 12 || string(hd[:4]) != "PACK" {
		if sr.err != nil && sr.err != io.EOF {
			return sr.err
		}
		return bad("not a pack")
	}
	if v := binary.BigEndian.Uint32(hd[4:]); v != 2 && v != 3 {
		return bad("pack version %d", v)
	}
	count := int64(binary.BigEndian.Uint32(hd[8:]))
	sr.skip(12)
	g.es = make([]pe, 0, min(count, 1<<16))
	var zr io.ReadCloser
	for i := int64(0); i < count; i++ {
		if err := g.ctx.Err(); err != nil {
			return err
		}
		pos := sr.pos()
		h, err := parseHead(sr.peek(64))
		if err != nil {
			if sr.err != nil && sr.err != io.EOF {
				return sr.err
			}
			return bad("entry %d: %v", i, err)
		}
		sr.skip(h.n)
		e := pe{off: pos, hn: h.n, typ: h.typ, size: h.size, ofs: h.ofs, ref: h.ref, parent: -1}
		switch h.typ {
		case plumbing.OFSDeltaObject, plumbing.REFDeltaObject:
			e.delta = true
		}
		if !e.delta && h.size > MaxObject {
			return fmt.Errorf("%w: object of %d bytes: %w", ErrBadPack, h.size, ErrTooLarge)
		}
		if zr == nil {
			zr, err = zlib.NewReader(sr)
		} else {
			err = zr.(zlib.Resetter).Reset(sr, nil)
		}
		if err != nil {
			if sr.err != nil && sr.err != io.EOF {
				return sr.err
			}
			return bad("entry %d: %v", i, err)
		}
		fault := func(what string, n int64, err error) error {
			if sr.err != nil && sr.err != io.EOF {
				return sr.err
			}
			_ = err
			return bad("entry %d: %s of %d bytes is not the %d it says", i, what, n, h.size)
		}
		switch {
		case e.delta:
			n, err := io.Copy(io.Discard, io.LimitReader(zr, h.size+1))
			if err != nil || n != h.size {
				return fault("delta", n, err)
			}
		case h.typ == plumbing.BlobObject:
			hs := plumbing.NewHasher(h.typ, h.size)
			n, err := io.Copy(hs, io.LimitReader(zr, h.size+1))
			if err != nil || n != h.size {
				return fault("blob", n, err)
			}
			e.oid, e.rsize, e.done = hs.Sum(), h.size, true
		default:
			var buf bytes.Buffer
			n, err := io.Copy(&buf, io.LimitReader(zr, h.size+1))
			if err != nil || n != h.size {
				return fault(h.typ.String(), n, err)
			}
			e.oid, e.rsize, e.done = plumbing.ComputeHash(h.typ, buf.Bytes()), h.size, true
			if err := names(h.typ, buf.Bytes(), func(x plumbing.Hash) { g.refers[x] = struct{}{} }); err != nil {
				return bad("%s %s: %v", h.typ, e.oid, err)
			}
		}
		e.end = sr.pos()
		idx := len(g.es)
		g.es = append(g.es, e)
		g.byOff[pos] = idx
		if e.done {
			if _, ok := g.byOid[e.oid]; !ok {
				g.byOid[e.oid] = idx
			}
		}
	}
	want := sr.sum()
	var tr [hashSize]byte
	if _, err := io.ReadFull(sr, tr[:]); err != nil {
		if sr.err != nil && sr.err != io.EOF {
			return sr.err
		}
		return bad("the pack ends before its checksum")
	}
	if !bytes.Equal(want, tr[:]) {
		return bad("the pack's checksum does not match its bytes")
	}
	var extra [1]byte
	if n, _ := sr.Read(extra[:]); n != 0 {
		return bad("bytes after the pack's checksum")
	}
	if sr.err != nil && sr.err != io.EOF {
		return sr.err
	}
	return nil
}

// deltaData is the inflated stream of delta entry i, read back from the pack.
func (g *ing) deltaData(i int) ([]byte, error) {
	e := &g.es[i]
	raw, err := g.src.at(e.off+int64(e.hn), e.end-e.off-int64(e.hn))
	if err != nil {
		return nil, err
	}
	d, err := inflate(raw, e.size)
	if err != nil {
		return nil, bad("delta at %d: %v", e.off, err)
	}
	return d, nil
}

// resolve gives every delta its name, type and size. A delta's base is an entry that is
// earlier in the pack (OFS_DELTA), an entry named anywhere in the pack (REF_DELTA), or,
// in a thin pack, an object the repository already holds.
func (g *ing) resolve() error {
	var pending []int
	for i := range g.es {
		if g.es[i].delta {
			pending = append(pending, i)
		}
	}
	for len(pending) > 0 {
		if err := g.ctx.Err(); err != nil {
			return err
		}
		var next []int
		for _, i := range pending {
			e := &g.es[i]
			switch e.typ {
			case plumbing.OFSDeltaObject:
				p, ok := g.byOff[e.off-e.ofs]
				if !ok || e.ofs <= 0 {
					return bad("an OFS_DELTA at %d names no entry", e.off)
				}
				if !g.es[p].done {
					next = append(next, i)
					continue
				}
				e.parent = p
			default:
				p, ok := g.byOid[e.ref]
				if !ok || !g.es[p].done {
					next = append(next, i)
					continue
				}
				e.parent = p
			}
			if err := g.finish(i); err != nil {
				return err
			}
		}
		if len(next) == len(pending) {
			// Nothing moved: what is left depends on objects the pack does not hold.
			moved := false
			var missing []plumbing.Hash
			for _, i := range next {
				if g.es[i].typ == plumbing.REFDeltaObject {
					missing = append(missing, g.es[i].ref)
				}
			}
			if g.repo == nil {
				g.repo = g.s.Repo(g.ctx, g.id)
			}
			// One round trip for the lot: a thin pack names its bases in the repository.
			if err := g.repo.Prefetch(missing); err != nil {
				return err
			}
			for _, i := range next {
				e := &g.es[i]
				if e.typ != plumbing.REFDeltaObject {
					continue
				}
				o, err := g.repo.EncodedObject(plumbing.AnyObject, e.ref)
				if errors.Is(err, plumbing.ErrObjectNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				e.parent, e.ext = -1, e.ref
				if err := g.finishExt(i, o); err != nil {
					return err
				}
				moved = true
			}
			if !moved {
				return bad("a delta's base is not in the pack or the repository")
			}
			rest := next[:0]
			for _, i := range next {
				if !g.es[i].done {
					rest = append(rest, i)
				}
			}
			next = rest
		}
		pending = next
	}
	return nil
}

func (g *ing) finishExt(i int, base plumbing.EncodedObject) error {
	rd, err := base.Reader()
	if err != nil {
		return err
	}
	b, err := io.ReadAll(rd)
	_ = rd.Close()
	if err != nil {
		return err
	}
	g.cache.put(-1-i, b) // keyed by the delta that wanted it
	return g.settle(i, base.Type(), b)
}

func (g *ing) finish(i int) error {
	e := &g.es[i]
	b, err := g.content(e.parent)
	if err != nil {
		return err
	}
	return g.settle(i, g.es[e.parent].typ, b)
}

// settle applies delta i to base, names the result and records what it names.
func (g *ing) settle(i int, typ plumbing.ObjectType, base []byte) error {
	e := &g.es[i]
	data, err := g.deltaData(i)
	if err != nil {
		return err
	}
	if err := checkDelta(data, len(base)); err != nil {
		return err
	}
	out, err := packfile.PatchDelta(base, data)
	if err != nil {
		return bad("delta at %d: %v", e.off, err)
	}
	e.typ, e.rsize, e.oid, e.done = typ, int64(len(out)), plumbing.ComputeHash(typ, out), true
	if _, ok := g.byOid[e.oid]; !ok {
		g.byOid[e.oid] = i
	}
	g.cache.put(i, out)
	if typ != plumbing.BlobObject {
		if err := names(typ, out, func(x plumbing.Hash) { g.refers[x] = struct{}{} }); err != nil {
			return bad("%s %s: %v", typ, e.oid, err)
		}
	}
	return nil
}

// content is the full content of resolved entry i, from the cache or by inflating it
// (and its bases) again.
func (g *ing) content(i int) ([]byte, error) {
	if b, ok := g.cache.get(i); ok {
		return b, nil
	}
	e := &g.es[i]
	if !e.delta {
		raw, err := g.src.at(e.off+int64(e.hn), e.end-e.off-int64(e.hn))
		if err != nil {
			return nil, err
		}
		return inflate(raw, e.size)
	}
	var base []byte
	var err error
	if e.parent >= 0 {
		base, err = g.content(e.parent)
	} else {
		var ok bool
		if base, ok = g.cache.get(-1 - i); !ok {
			o, err2 := g.repo.EncodedObject(plumbing.AnyObject, e.ext)
			if err2 != nil {
				return nil, err2
			}
			rd, err2 := o.Reader()
			if err2 != nil {
				return nil, err2
			}
			base, err = io.ReadAll(rd)
			_ = rd.Close()
		}
	}
	if err != nil {
		return nil, err
	}
	data, err := g.deltaData(i)
	if err != nil {
		return nil, err
	}
	out, err := packfile.PatchDelta(base, data)
	if err != nil {
		return nil, bad("delta at %d: %v", e.off, err)
	}
	g.cache.put(i, out)
	return out, nil
}

// inflate is the zlib stream z, which must come to size bytes.
func inflate(z []byte, size int64) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	out := make([]byte, 0, size)
	buf := bytes.NewBuffer(out)
	if _, err := io.Copy(buf, io.LimitReader(zr, size+1)); err != nil {
		return nil, err
	}
	if int64(buf.Len()) != size {
		return nil, fmt.Errorf("objects: an entry of %d bytes inflates to %d", size, buf.Len())
	}
	return buf.Bytes(), nil
}

// checkDelta reads the two lengths a delta starts with and refuses one whose base is
// not the object it was made against or whose result is larger than the store takes,
// before the result is allocated.
func checkDelta(d []byte, baseLen int) error {
	src, n := uvarint(d)
	if n == 0 {
		return bad("a delta with no header")
	}
	dst, m := uvarint(d[n:])
	if m == 0 {
		return bad("a delta with no header")
	}
	if src != uint64(baseLen) {
		return bad("a delta made against %d bytes applied to %d", src, baseLen)
	}
	if dst > MaxObject {
		return fmt.Errorf("%w: object of %d bytes: %w", ErrBadPack, dst, ErrTooLarge)
	}
	return nil
}

// uvarint is the little-endian base-128 length a delta starts with.
func uvarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7f) << (7 * uint(i))
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

// contentLRU is a byte-bounded cache of resolved contents, for one ingest.
type contentLRU struct {
	max, used int
	ll        *list.List
	m         map[int]*list.Element
}

type contentItem struct {
	k int
	b []byte
}

func newContentLRU(max int) *contentLRU {
	return &contentLRU{max: max, ll: list.New(), m: map[int]*list.Element{}}
}

func (c *contentLRU) get(k int) ([]byte, bool) {
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		return e.Value.(*contentItem).b, true
	}
	return nil, false
}

func (c *contentLRU) put(k int, b []byte) {
	if len(b) > c.max {
		return
	}
	if e, ok := c.m[k]; ok {
		c.used -= len(e.Value.(*contentItem).b)
		c.ll.Remove(e)
	}
	c.m[k] = c.ll.PushFront(&contentItem{k, b})
	c.used += len(b)
	for c.used > c.max {
		e := c.ll.Back()
		it := e.Value.(*contentItem)
		c.used -= len(it.b)
		delete(c.m, it.k)
		c.ll.Remove(e)
	}
}
