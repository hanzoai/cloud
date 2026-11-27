package objects

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"
)

// Pack writes the named objects to w as a version 2 packfile, each entry the bytes the
// store already holds: an object that was stored whole is copied as it is, and an object
// that was stored as a delta is copied as a REF_DELTA when its base is one of the objects
// being sent, which a client resolves as it reads. Only a delta whose base is not sent is
// applied and deflated again.
//
// The objects are read in windows, each in pack order, and a run of entries that lie
// together in one pack is one range request. A window is bounded by the bytes it holds,
// and an entry larger than a window is streamed from the blob store to w without being
// held, so a repository with one 200 MB blob and a thousand small files holds neither.
func (r *Repo) Pack(ctx context.Context, w io.Writer, oids []plumbing.Hash) error {
	if err := r.settle(); err != nil {
		return err
	}
	sum := sha1.New()
	out := io.MultiWriter(w, sum)
	var head [12]byte
	copy(head[:4], "PACK")
	binary.BigEndian.PutUint32(head[4:], 2)
	binary.BigEndian.PutUint32(head[8:], uint32(len(oids)))
	if _, err := out.Write(head[:]); err != nil {
		return err
	}
	set := make(map[plumbing.Hash]struct{}, len(oids))
	for _, h := range oids {
		set[h] = struct{}{}
	}
	depth := readAhead
	if r.tx != nil {
		depth = 0 // a transaction is one connection; its reads do not overlap
	}
	if err := r.run(ctx, out, oids, set, depth); err != nil {
		return err
	}
	_, err := w.Write(sum.Sum(nil))
	return err
}

const (
	// group is the bytes of entries read in one request, unless one entry is larger.
	group = 4 << 20
	// gap is the dead bytes between two entries that are still read in one request.
	gap = 128 << 10
	// look is how many objects are placed in the index at once.
	look = 2048
	// readAhead is how many groups are asked of the blob store ahead of the one being
	// written: a clone is bound by the store's latency, not its bandwidth, until the
	// next requests are already in flight. It bounds memory too: a group is held from the
	// request that fills it until it is written, so a clone holds readAhead groups (16 MiB).
	readAhead = 4
)

// batch is a run of entries that lie together in one pack, read in one request, or one
// entry too large to hold, streamed.
type batch struct {
	ents       []loc
	pack       string
	start, end int64
	big        bool
	buf        []byte
	err        error
	done       chan struct{}
}

// run places oids in the index a window at a time, plans the requests, and writes the
// entries in order. With depth > 0 the requests run ahead of the writer, in parallel.
func (r *Repo) run(ctx context.Context, out io.Writer, oids []plumbing.Hash, set map[plumbing.Hash]struct{}, depth int) error {
	consume := func(b *batch) error {
		if b.big {
			return r.stream(ctx, out, b.ents[0], set)
		}
		if b.done != nil {
			<-b.done
		}
		if b.err != nil {
			return b.err
		}
		for _, l := range b.ents {
			if err := r.emit(out, l, b.buf[l.at-b.start:l.at-b.start+l.n], set); err != nil {
				return err
			}
		}
		return nil
	}
	if depth == 0 {
		return r.plan(ctx, oids, func(b *batch) error {
			if !b.big {
				b.fetchNow(ctx, r)
			}
			return consume(b)
		})
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan *batch, depth)
	sem := make(chan struct{}, depth)
	planned := make(chan error, 1)
	go func() {
		defer close(queue)
		planned <- r.plan(pctx, oids, func(b *batch) error {
			if !b.big {
				select {
				case sem <- struct{}{}:
				case <-pctx.Done():
					return pctx.Err()
				}
				b.done = make(chan struct{})
				go func() {
					defer close(b.done)
					b.fetchNow(pctx, r)
				}()
			}
			select {
			case queue <- b:
				return nil
			case <-pctx.Done():
				return pctx.Err()
			}
		})
	}()
	var err error
	for b := range queue {
		if err == nil {
			if err = consume(b); err != nil {
				cancel()
			}
		}
		if !b.big {
			// A group is held from the request that fills it until it is written, so the
			// memory of a clone is depth groups, whatever the store's speed.
			<-sem
		}
		b.buf = nil
	}
	perr := <-planned
	if err != nil {
		return err
	}
	if perr == nil {
		perr = ctx.Err()
	}
	return perr
}

func (b *batch) fetchNow(ctx context.Context, r *Repo) {
	buf, err := r.fetchWith(ctx, b.pack, b.start, b.end-b.start)
	if err == nil && int64(len(buf)) != b.end-b.start {
		err = fmt.Errorf("objects: pack %s is shorter than the index says", b.pack)
	}
	b.buf, b.err = buf, err
}

// plan places oids in the index in windows and hands each request, in the order the
// entries are written, to yield.
func (r *Repo) plan(ctx context.Context, oids []plumbing.Hash, yield func(*batch) error) error {
	for len(oids) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(look, len(oids))
		locs, err := r.locs(oids[:n])
		if err != nil {
			return err
		}
		order := make([]loc, 0, n)
		for _, h := range oids[:n] {
			l, ok := locs[h]
			if !ok {
				return fmt.Errorf("objects: pack: %s: %w", h, plumbing.ErrObjectNotFound)
			}
			order = append(order, l)
		}
		slices.SortStableFunc(order, func(a, b loc) int {
			switch {
			case a.pack != b.pack:
				if a.pack < b.pack {
					return -1
				}
				return 1
			case a.at < b.at:
				return -1
			case a.at > b.at:
				return 1
			}
			return 0
		})
		for i := 0; i < len(order); {
			e := order[i]
			if e.n > group {
				if err := yield(&batch{ents: order[i : i+1], big: true}); err != nil {
					return err
				}
				i++
				continue
			}
			j, start, end := i, e.at, e.at+e.n
			for j+1 < len(order) {
				nx := order[j+1]
				if nx.pack != e.pack || nx.n > group || nx.at < end || nx.at-end > gap || nx.at+nx.n-start > group {
					break
				}
				j++
				end = nx.at + nx.n
			}
			if err := yield(&batch{ents: order[i : j+1], pack: e.pack, start: start, end: end}); err != nil {
				return err
			}
			i = j + 1
		}
		oids = oids[n:]
	}
	return nil
}

// emit writes the entry l, whose stored bytes are raw.
func (r *Repo) emit(out io.Writer, l loc, raw []byte, set map[plumbing.Hash]struct{}) error {
	if l.base == nil {
		_, err := out.Write(raw)
		return err
	}
	if _, ok := set[hashOf(l.base)]; ok {
		hd, err := parseHead(raw)
		if err != nil {
			return fmt.Errorf("objects: pack: %s: %w", l.oid, err)
		}
		if _, err := out.Write(refDeltaHead(hd.size, hashOf(l.base))); err != nil {
			return err
		}
		_, err = out.Write(raw[hd.n:])
		return err
	}
	o, err := r.loadRaw(l, raw, 0)
	if err != nil {
		return err
	}
	rd, err := o.Reader()
	if err != nil {
		return err
	}
	content, err := io.ReadAll(rd)
	_ = rd.Close()
	if err != nil {
		return err
	}
	z, err := deflate(content)
	if err != nil {
		return err
	}
	if _, err := out.Write(entryHeader(l.typ, int64(len(content)))); err != nil {
		return err
	}
	_, err = out.Write(z)
	return err
}

// stream writes an entry too large to hold, from the blob store straight to out. A
// delta whose base is not sent is the exception: applying it needs the whole entry.
func (r *Repo) stream(ctx context.Context, out io.Writer, l loc, set map[plumbing.Hash]struct{}) error {
	if l.base != nil {
		if _, ok := set[hashOf(l.base)]; !ok {
			raw, err := r.fetch(l.pack, l.at, l.n)
			if err != nil {
				return err
			}
			return r.emit(out, l, raw, set)
		}
	}
	at, n := l.at, l.n
	if l.base != nil {
		raw, err := r.fetch(l.pack, l.at, min(64, l.n))
		if err != nil {
			return err
		}
		hd, err := parseHead(raw)
		if err != nil {
			return fmt.Errorf("objects: pack: %s: %w", l.oid, err)
		}
		if _, err := out.Write(refDeltaHead(hd.size, hashOf(l.base))); err != nil {
			return err
		}
		at, n = at+int64(hd.n), n-int64(hd.n)
	}
	rc, err := r.s.blobs.Range(ctx, r.s.packKey(r.id, l.pack), at, n)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	copied, err := io.Copy(out, io.LimitReader(rc, n))
	if err != nil {
		return err
	}
	if copied != n {
		return fmt.Errorf("objects: pack %s is shorter than the index says", l.pack)
	}
	return nil
}
