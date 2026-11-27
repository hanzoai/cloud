package objects

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// Repacking is geometric. A push adds a pack; a repository whose packs are every size
// from a few kilobytes to many gigabytes is kept so that each pack is at least twice the
// size of all the smaller ones together. When it is not, the packs up to and including
// the largest that breaks the rule are merged into one. A busy repository then merges
// its newest small packs often and cheaply, and rewrites its large ones rarely: every
// byte is rewritten about log2(size / smallest) times in its life, not once per push, and
// a repository already in shape costs one query to find out.
//
// A merge is a stream. The input packs are read in sequence, each entry copied through
// to a multipart upload without being held, and the new place of each object is written
// to a side table as it goes, so the memory of a merge is a read buffer and a page of
// index rows, whatever the size of the repository. The index is moved in one transaction
// at the end, and only if every row is still where the merge read it.
const (
	// factor is how many times larger than the sum of the smaller packs a pack must be to
	// be left alone.
	factor = 2
	// mergeBudget is the most input one merge reads. A repository that needs more is
	// merged again on the next pass.
	mergeBudget = 8 << 30
	// maxMerge is the most packs one merge takes.
	maxMerge = 1000
	// entryPage is how many index rows a merge reads at once.
	entryRows = 2000
	// moveBatch is how many new places a merge writes at once.
	moveBatch = 1000
	// leaseTTL is how long a lease holds without being renewed.
	leaseTTL = 5 * time.Minute
)

// Grace is how long a pack that compaction replaced, a deleted repository's pack, or a
// pack nothing points at is kept before it is deleted. It is longer than any request: a
// reader that resolved an object's place before the index changed is still reading the
// place it was told, and a push uploads its pack before the transaction that attaches it.
const Grace = time.Hour

// lease takes the named lease, or reports that another process holds it. The returned
// context ends if the lease is lost; release gives it up. A lease only saves work: every
// operation that takes one is safe to run twice at once.
func (s *Store) lease(ctx context.Context, name string) (context.Context, func(), bool, error) {
	var tok [8]byte
	_, _ = rand.Read(tok[:])
	holder := hex.EncodeToString(tok[:])
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO leases (name, holder, expires) VALUES ($1, $2, $3)
		 ON CONFLICT (name) DO UPDATE SET holder = excluded.holder, expires = excluded.expires
		 WHERE leases.expires < $4`, name, holder, now+int64(leaseTTL/time.Second), now)
	if err != nil {
		return ctx, nil, false, fmt.Errorf("objects: lease %s: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ctx, nil, false, nil
	}
	lctx, cancel := context.WithCancel(ctx)
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(leaseTTL / 3)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-lctx.Done():
				return
			case <-t.C:
				res, err := s.db.ExecContext(lctx,
					`UPDATE leases SET expires = $1 WHERE name = $2 AND holder = $3`, s.now().Unix()+int64(leaseTTL/time.Second), name, holder)
				if err != nil {
					continue
				}
				if n, _ := res.RowsAffected(); n == 0 {
					cancel()
					return
				}
			}
		}
	}()
	release := func() {
		close(stop)
		cancel()
		_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM leases WHERE name = $1 AND holder = $2`, name, holder)
	}
	return lctx, release, true, nil
}

// swept is when the named job last ran, 0 if it never has.
func (s *Store) mark(ctx context.Context, name string) {
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO sweeps (name, at) VALUES ($1, $2) ON CONFLICT (name) DO UPDATE SET at = excluded.at`, name, s.now().Unix())
}

// roll is the packs to merge, the smallest first, for a repository whose packs are the
// sizes given in ascending order: the packs up to the largest one that is not at least
// factor times the sum of those smaller than it. Fewer than two is nothing to merge.
func roll(sizes []int64) int {
	prefix := make([]int64, len(sizes)+1)
	for i, n := range sizes {
		prefix[i+1] = prefix[i] + n
	}
	for i := len(sizes) - 1; i >= 1; i-- {
		if sizes[i] < factor*prefix[i] {
			return i + 1
		}
	}
	return 0
}

// Compact makes one geometric merge of repository id's packs and reports whether it
// merged anything. The merged pack is uploaded before the index moves, the index rows
// are moved only if each is still where the merge read it (a second merge, or a delete,
// makes this one give up and delete its upload), and the replaced packs are deleted after
// Grace. Only one process merges a repository at a time.
func (s *Store) Compact(ctx context.Context, id string) (bool, error) {
	ctx, release, ok, err := s.lease(ctx, "repack:"+id)
	if err != nil || !ok {
		return false, err
	}
	defer release()
	rows, err := s.db.QueryContext(ctx, `SELECT pack, bytes FROM packs WHERE repo = $1 ORDER BY bytes, pack`, id)
	if err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	var old []string
	var sizes []int64
	for rows.Next() {
		var p string
		var n int64
		if err := rows.Scan(&p, &n); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("objects: compact %s: %w", id, err)
		}
		old, sizes = append(old, p), append(sizes, n)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	take := roll(sizes)
	var total int64
	for i := 0; i < take; i++ {
		if i >= maxMerge || total+sizes[i] > mergeBudget && i > 0 {
			take = i
			break
		}
		total += sizes[i]
	}
	if take < 2 {
		return false, nil
	}
	old, sizes = old[:take], sizes[:take]
	return s.merge(ctx, id, old, sizes)
}

// merge streams the entries of the packs old into one pack and moves the index to it.
func (s *Store) merge(ctx context.Context, id string, old []string, sizes []int64) (merged bool, err error) {
	args := []any{id}
	for _, p := range old {
		args = append(args, p)
	}
	var count int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM entries WHERE repo = $1 AND pack IN (`+dialect.Marks(2, len(old))+`)`, args...).Scan(&count); err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	if count == 0 {
		return false, nil
	}
	var rnd [16]byte
	_, _ = rand.Read(rnd[:])
	name := hex.EncodeToString(rnd[:])
	key := s.packKey(id, name)
	job := hex.EncodeToString(rnd[:8])
	up := s.startUpload(ctx, key)
	defer func() {
		if !merged {
			s.discard(key)
		}
		_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM moves WHERE job = $1`, job)
	}()
	fail := func(err error) (bool, error) {
		up.abort(err)
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}

	sum := sha1.New()
	cw := &tally{w: io.MultiWriter(up.pw, sum)}
	var head [12]byte
	copy(head[:4], "PACK")
	binary.BigEndian.PutUint32(head[4:], 2)
	binary.BigEndian.PutUint32(head[8:], uint32(count))
	if _, err := cw.Write(head[:]); err != nil {
		return fail(err)
	}

	var emitted int64
	var batch []move
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := s.putMoves(ctx, job, batch)
		batch = batch[:0]
		return err
	}
	for pi, pack := range old {
		rc, err := s.blobs.Range(ctx, s.packKey(id, pack), 0, sizes[pi])
		if err != nil {
			return fail(err)
		}
		pos := int64(0)
		last := int64(-1)
		for {
			page, err := s.entryPage(ctx, id, pack, last)
			if err != nil {
				_ = rc.Close()
				return fail(err)
			}
			if len(page) == 0 {
				break
			}
			for _, l := range page {
				if l.at < pos {
					_ = rc.Close()
					return fail(fmt.Errorf("pack %s: entries overlap at %d", pack, l.at))
				}
				if _, err := io.CopyN(io.Discard, rc, l.at-pos); err != nil {
					_ = rc.Close()
					return fail(fmt.Errorf("pack %s is shorter than the index says: %w", pack, err))
				}
				pos = l.at + l.n
				at := cw.n
				if err := copyEntry(cw, rc, l); err != nil {
					_ = rc.Close()
					return fail(err)
				}
				batch = append(batch, move{oid: l.oid, at: at, n: cw.n - at})
				emitted++
				if len(batch) >= moveBatch {
					if err := flush(); err != nil {
						_ = rc.Close()
						return fail(err)
					}
				}
			}
			last = page[len(page)-1].at
		}
		_ = rc.Close()
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	if emitted != count {
		return fail(fmt.Errorf("the index changed under the merge (%d entries, expected %d)", emitted, count))
	}
	if _, err := up.pw.Write(sum.Sum(nil)); err != nil {
		return fail(err)
	}
	total := cw.n + hashSize
	if err := up.finish(); err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}

	tx, err := s.d.Serialize(ctx, s.db, id)
	if err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos WHERE repo = $1`, id).Scan(&present); err != nil || present == 0 {
		return false, err
	}
	uargs := []any{name, id, job}
	for _, p := range old {
		uargs = append(uargs, p)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE entries SET pack = $1, at = m.at, n = m.n FROM moves AS m
		 WHERE entries.repo = $2 AND m.job = $3 AND entries.oid = m.oid AND entries.pack IN (`+dialect.Marks(4, len(old))+`)`, uargs...)
	if err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n != count {
		// Somebody else moved or removed some of them: this merge is not the one that lands.
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO packs (repo, pack, bytes, objects, created) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (repo, pack) DO NOTHING`,
		id, name, total, count, now()); err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	var replaced int64
	for i, p := range old {
		replaced += sizes[i]
		if _, err := tx.ExecContext(ctx, `DELETE FROM packs WHERE repo = $1 AND pack = $2`, id, p); err != nil {
			return false, fmt.Errorf("objects: compact %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO doomed (key, after) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`,
			s.packKey(id, p), s.now().Add(Grace).Unix()); err != nil {
			return false, fmt.Errorf("objects: compact %s: %w", id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE repos SET bytes = bytes + $1 WHERE repo = $2`, total-replaced, id); err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("objects: compact %s: %w", id, err)
	}
	return true, nil
}

type move struct {
	oid plumbing.Hash
	at  int64
	n   int64
}

// tally counts what is written through it.
type tally struct {
	w io.Writer
	n int64
}

func (c *tally) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// copyEntry writes the entry l, which is next in r, to w. An OFS_DELTA names its base by
// distance, which a merge changes, so it is written as the REF_DELTA the index already
// knows it to be. The entry is never held whole: its header is read, the rest is copied.
func copyEntry(w io.Writer, r io.Reader, l loc) error {
	head := make([]byte, min(l.n, 64))
	if _, err := io.ReadFull(r, head); err != nil {
		return fmt.Errorf("%s: %w", l.oid, err)
	}
	if l.base != nil {
		hd, err := parseHead(head)
		if err != nil {
			return fmt.Errorf("%s: %w", l.oid, err)
		}
		if hd.typ == plumbing.OFSDeltaObject {
			if _, err := w.Write(refDeltaHead(hd.size, hashOf(l.base))); err != nil {
				return err
			}
			head = head[hd.n:]
		}
	}
	if _, err := w.Write(head); err != nil {
		return err
	}
	rest := l.n - int64(min(l.n, 64))
	_, err := io.CopyN(w, r, rest)
	return err
}

// entryPage is the next page of index rows of one pack after offset last, in the order
// they lie in the pack. A page is a query, so a merge holds no connection while it reads
// the bucket.
func (s *Store) entryPage(ctx context.Context, id, pack string, last int64) ([]loc, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+locCols+` FROM entries WHERE repo = $1 AND pack = $2 AND at > $3 ORDER BY at LIMIT $4`, id, pack, last, int64(entryRows))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []loc
	for rows.Next() {
		l, err := scanLoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// putMoves records where a merge put objects, for the transaction that moves the index.
func (s *Store) putMoves(ctx context.Context, job string, ms []move) error {
	var a dialect.Args
	var b strings.Builder
	b.WriteString(`INSERT INTO moves (job, oid, at, n, created) VALUES `)
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("(" + a.Mark(job) + "," + a.Mark(m.oid[:]) + "," + a.Mark(m.at) + "," + a.Mark(m.n) + "," + a.Mark(now()) + ")")
	}
	b.WriteString(` ON CONFLICT (job, oid) DO NOTHING`)
	_, err := s.db.ExecContext(ctx, b.String(), a.Values()...)
	return err
}

// ---- collection ----

// Collect deletes the blobs nothing needs, everywhere, now: packs that were replaced or
// whose repository was deleted once their grace is over, and packs the database does not
// name that are older than Grace, which is what an upload that never reached its commit
// leaves. It reports how many blobs it deleted. Maintain does the same in pieces, on a
// schedule, a repository and a shard of the bucket at a time.
func (s *Store) Collect(ctx context.Context) (n int, err error) {
	// A location this pod remembers may name a pack that is about to go.
	defer func() {
		if n > 0 {
			s.cache.locs.purge(s.tag + "|")
		}
	}()
	n, err = s.collectDoomed(ctx)
	if err != nil {
		return n, err
	}
	ids, err := s.repoIDs(ctx)
	if err != nil {
		return n, err
	}
	for _, id := range ids {
		k, err := s.collectRepo(ctx, id)
		n += k
		if err != nil {
			return n, err
		}
	}
	for shard := range 16 {
		k, err := s.collectShard(ctx, shard)
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (s *Store) repoIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo FROM repos ORDER BY repo`)
	if err != nil {
		return nil, fmt.Errorf("objects: collect: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("objects: collect: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// collectDoomed deletes the blobs whose grace is over, a batch at a time.
func (s *Store) collectDoomed(ctx context.Context) (int, error) {
	n := 0
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT key FROM doomed WHERE after <= $1 ORDER BY after, key LIMIT 500`, s.now().Unix())
		if err != nil {
			return n, fmt.Errorf("objects: collect: %w", err)
		}
		var due []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				_ = rows.Close()
				return n, fmt.Errorf("objects: collect: %w", err)
			}
			due = append(due, k)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return n, fmt.Errorf("objects: collect: %w", err)
		}
		if len(due) == 0 {
			break
		}
		for _, k := range due {
			if err := s.blobs.Delete(ctx, k); err != nil {
				return n, err
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM doomed WHERE key = $1`, k); err != nil {
				return n, fmt.Errorf("objects: collect: %w", err)
			}
			n++
		}
	}
	cut := s.now().Add(-24 * time.Hour).Unix()
	_, _ = s.db.ExecContext(ctx, `DELETE FROM moves WHERE created < $1`, cut)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM leases WHERE expires < $1`, cut)
	return n, nil
}

// doomedUnder is the blob keys under prefix that are waiting out their grace.
func (s *Store) doomedUnder(ctx context.Context, prefix string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM doomed WHERE key LIKE $1`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("objects: collect: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]struct{}{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("objects: collect: %w", err)
		}
		out[k] = struct{}{}
	}
	return out, rows.Err()
}

// collectRepo deletes the packs under one repository's prefix that the database does not
// name and that are older than Grace. It lists one repository's keys, not the bucket's.
func (s *Store) collectRepo(ctx context.Context, id string) (int, error) {
	prefix := s.repoPrefix(id)
	cutoff := s.now().Add(-Grace)
	var cand []string
	// What the bucket holds first, and what the database names after: a pack committed
	// between the two is in the second list, and one uploaded after the first was
	// listed is not a candidate.
	if err := s.blobs.List(ctx, prefix, func(b Blob) error {
		if strings.HasSuffix(b.Key, ".pack") && b.Modified.Before(cutoff) {
			cand = append(cand, b.Key)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if len(cand) == 0 {
		return 0, nil
	}
	named := map[string]struct{}{}
	rows, err := s.db.QueryContext(ctx, `SELECT pack FROM packs WHERE repo = $1`, id)
	if err != nil {
		return 0, fmt.Errorf("objects: collect: %w", err)
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("objects: collect: %w", err)
		}
		named[s.packKey(id, p)] = struct{}{}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, fmt.Errorf("objects: collect: %w", err)
	}
	doomed, err := s.doomedUnder(ctx, prefix)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range cand {
		if _, ok := named[k]; ok {
			continue
		}
		if _, ok := doomed[k]; ok {
			continue
		}
		if err := s.blobs.Delete(ctx, k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// collectShard lists one sixteenth of the bucket's keys for this store (those whose
// repository digest starts with one hex digit) as a stream, and deletes the old packs of
// repositories the database no longer holds. It is how the packs of a deleted repository
// that Remove could not delete are found. It keeps the digest of every repository, and
// holds one repository's keys at a time.
func (s *Store) collectShard(ctx context.Context, shard int) (int, error) {
	ids, err := s.repoIDs(ctx)
	if err != nil {
		return 0, err
	}
	known := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		known[s.repoDir(id)] = struct{}{}
	}
	cutoff := s.now().Add(-Grace)
	base := s.prefix + "/"
	n := 0
	var dir string
	var cand []string
	flush := func() error {
		defer func() { cand = cand[:0] }()
		if len(cand) == 0 {
			return nil
		}
		doomed, err := s.doomedUnder(ctx, base+dir+"/")
		if err != nil {
			return err
		}
		for _, k := range cand {
			if _, ok := doomed[k]; ok {
				continue
			}
			if err := s.blobs.Delete(ctx, k); err != nil {
				return err
			}
			n++
		}
		return nil
	}
	err = s.blobs.List(ctx, base+fmt.Sprintf("%x", shard), func(b Blob) error {
		d, _, ok := strings.Cut(strings.TrimPrefix(b.Key, base), "/")
		if !ok || !strings.HasSuffix(b.Key, ".pack") || !b.Modified.Before(cutoff) {
			return nil
		}
		if _, held := known[d]; held {
			return nil
		}
		if d != dir {
			if err := flush(); err != nil {
				return err
			}
			dir = d
		}
		cand = append(cand, b.Key)
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, flush()
}

// ---- maintenance ----

const (
	// repackEvery is how long a repository that was looked at is left before it is looked
	// at again, and repackBatch how many a pass looks at.
	repackEvery = 10 * time.Minute
	repackBatch = 64
	// gcEvery and gcBatch are the same for the collection of a repository's own prefix.
	gcEvery = 6 * time.Hour
	gcBatch = 64
	// shardEvery is how often a shard of the bucket is listed for the packs of deleted
	// repositories, and shardBatch how many a pass lists.
	shardEvery = 24 * time.Hour
	shardBatch = 2
	// rounds is the most merges a pass makes in one repository.
	rounds = 8
)

// due is the names, from a query of repository ids, whose job of kind last ran before the
// cutoff, oldest first.
func (s *Store) due(ctx context.Context, query string, since time.Duration, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, s.now().Add(-since).Unix(), int64(limit))
	if err != nil {
		return nil, fmt.Errorf("objects: maintain: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("objects: maintain: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Maintain does the next part of the store's upkeep and reports what it did: the
// repositories whose packs are out of shape and were last looked at longest ago are
// merged, the repositories not collected for longest have their stray packs deleted, a
// shard or two of the bucket is listed for deleted repositories' packs, and the packs
// whose grace is over are deleted. Each piece is a bounded amount of work and takes a
// lease, so any number of replicas may call it on any schedule and divide the work.
func (s *Store) Maintain(ctx context.Context) (compacted, collected int, err error) {
	ids, err := s.due(ctx,
		`SELECT p.repo FROM packs p LEFT JOIN sweeps w ON w.name = 'repack:' || p.repo
		 WHERE COALESCE(w.at, 0) < $1 GROUP BY p.repo, w.at HAVING COUNT(*) >= 2
		 ORDER BY COALESCE(w.at, 0), p.repo LIMIT $2`, repackEvery, repackBatch)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return compacted, collected, ctx.Err()
		}
		s.mark(ctx, "repack:"+id)
		for range rounds {
			ok, err := s.Compact(ctx, id)
			if err != nil {
				return compacted, collected, err
			}
			if !ok {
				break
			}
			compacted++
		}
	}
	ids, err = s.due(ctx,
		`SELECT r.repo FROM repos r LEFT JOIN sweeps w ON w.name = 'gc:' || r.repo
		 WHERE COALESCE(w.at, 0) < $1 ORDER BY COALESCE(w.at, 0), r.repo LIMIT $2`, gcEvery, gcBatch)
	if err != nil {
		return compacted, collected, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return compacted, collected, ctx.Err()
		}
		lctx, release, ok, err := s.lease(ctx, "gc:"+id)
		if err != nil {
			return compacted, collected, err
		}
		if !ok {
			continue
		}
		n, err := s.collectRepo(lctx, id)
		release()
		collected += n
		if err != nil {
			return compacted, collected, err
		}
		s.mark(ctx, "gc:"+id)
	}
	shardNames := make([]string, 16)
	for i := range shardNames {
		shardNames[i] = fmt.Sprintf("shard:%x", i)
	}
	ran := 0
	for i, name := range shardNames {
		if ran >= shardBatch {
			break
		}
		var at int64
		err := s.db.QueryRowContext(ctx, `SELECT at FROM sweeps WHERE name = $1`, name).Scan(&at)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return compacted, collected, fmt.Errorf("objects: maintain: %w", err)
		}
		if at >= s.now().Add(-shardEvery).Unix() {
			continue
		}
		lctx, release, ok, err := s.lease(ctx, name)
		if err != nil {
			return compacted, collected, err
		}
		if !ok {
			continue
		}
		n, err := s.collectShard(lctx, i)
		release()
		collected += n
		if err != nil {
			return compacted, collected, err
		}
		s.mark(ctx, name)
		ran++
	}
	n, err := s.collectDoomed(ctx)
	collected += n
	if collected > 0 {
		s.cache.locs.purge(s.tag + "|")
	}
	return compacted, collected, err
}
