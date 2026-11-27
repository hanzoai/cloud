package objects

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// chunky is a reader that answers a few hundred bytes at a time, as a socket does.
type chunky struct {
	r io.Reader
}

func (c chunky) Read(p []byte) (int, error) { return c.r.Read(p[:min(len(p), 777)]) }

// A pack larger than the memory a scan keeps is read as a stream, uploaded as it comes,
// and its deltas are read back from the bucket to be resolved.
func TestAPackLargerThanTheSpoolIsStreamed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		if err := writeFile(work+"/random.bin", randomBlob(t, spoolMax+(4<<20))); err != nil {
			t.Fatal(err)
		}
		c1 := commitAll(t, work, "a.txt", body(5000, -1), "one")
		c2 := commitFile(t, work, "a.txt", body(5000, 2500), "two")
		c3 := commitFile(t, work, "a.txt", body(5000, 100), "three")
		pack := packOf(t, work, c3)
		if len(pack) <= spoolMax {
			t.Fatalf("the pack is %d bytes: not larger than the spool", len(pack))
		}
		_ = c1
		_ = c2
		in, err := s.Ingest(ctx, "r", chunky{bytes.NewReader(pack)})
		if err != nil {
			t.Fatal(err)
		}
		if in.size != int64(len(pack)) {
			t.Fatalf("size %d, want %d", in.size, len(pack))
		}
		if blobs.Gets.Load() == 0 {
			t.Fatal("no delta was read back from the bucket")
		}
		if got := blobs.Get(s.packKey("r", in.pack)); !bytes.Equal(got, pack) {
			t.Fatalf("the stored pack is not the pack received (%d of %d bytes)", len(got), len(pack))
		}
		tx, err := s.Begin(ctx, "r")
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Attach(in); err != nil {
			t.Fatal(err)
		}
		name := plumbing.ReferenceName("refs/heads/main")
		if err := tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{name: c3}); err != nil {
			t.Fatal(err)
		}
		if err := tx.CreateReference(plumbing.NewHashReference(name, c3)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		bareFrom(t, ctx, s, "r", c3)
	})
}

// A pack that stops early, carries bytes after its checksum or has a wrong one is
// refused, and the bytes already uploaded are deleted.
func TestAStreamedPackThatIsWrongLeavesNoBlob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		c1 := commitFile(t, work, "a", body(2000, -1), "one")
		pack := packOf(t, work, c1)
		flipped := append([]byte(nil), pack...)
		flipped[len(flipped)/2] ^= 0xff
		for name, p := range map[string][]byte{
			"truncated": pack[:len(pack)-30],
			"trailing":  append(append([]byte(nil), pack...), 'x'),
			"flipped":   flipped,
			"empty":     nil,
		} {
			if _, err := s.Ingest(ctx, "r", chunky{bytes.NewReader(p)}); err == nil {
				t.Fatalf("%s: a bad pack was taken", name)
			}
			if k := blobs.Keys(); len(k) != 0 {
				t.Fatalf("%s: blobs left: %v", name, k)
			}
		}
	})
}

// What a pod has read it does not read again: index rows and pack blocks are remembered
// across requests, and what is remembered is bounded.
func TestAPodRemembersWhatItRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		var tip, prev plumbing.Hash
		for i := range 4 {
			tip = commitFile(t, work, "a", body(1000, i*100), fmt.Sprint(i))
			pack := packOf(t, work, tip)
			if !prev.IsZero() {
				pack = packOf(t, work, tip, prev)
			}
			if err := push(ctx, s, "r", pack, tip, prev); err != nil {
				t.Fatal(err)
			}
			prev = tip
		}
		all, err := s.Repo(ctx, "r").Hashes(plumbing.AnyObject)
		if err != nil {
			t.Fatal(err)
		}
		readAll := func() {
			r := s.Repo(ctx, "r")
			if err := r.Prefetch(all); err != nil {
				t.Fatal(err)
			}
			for _, h := range all {
				if _, err := r.EncodedObject(plumbing.AnyObject, h); err != nil {
					t.Fatal(err)
				}
			}
		}
		readAll()
		first := blobs.Gets.Load()
		if first == 0 {
			t.Fatal("the first read asked the bucket for nothing")
		}
		if s.cache.locs.bytes() == 0 || s.cache.blocks.bytes() == 0 {
			t.Fatal("nothing was remembered")
		}
		readAll()
		if again := blobs.Gets.Load(); again != first {
			t.Fatalf("the second read asked the bucket %d more times", again-first)
		}
		// A repository that is removed is forgotten at once on the pod that removed it.
		if err := s.Remove(ctx, "r"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Repo(ctx, "r").remembered(all[0]); ok {
			t.Fatal("a deleted repository's location is still remembered")
		}
	})
}

func TestTheCacheIsBounded(t *testing.T) {
	c := newLRU[[]byte](16 * 1000)
	for i := range 1000 {
		c.put(fmt.Sprint(i), make([]byte, 100), 100, 0)
	}
	if n := c.bytes(); n > 16*1000 {
		t.Fatalf("the cache holds %d bytes of 16000", n)
	}
	c.put("old", []byte{1}, 1, time.Nanosecond)
	time.Sleep(time.Millisecond)
	if _, ok := c.get("old"); ok {
		t.Fatal("an expired entry was served")
	}
}

// slow is a Blobs whose range reads take a while, and that counts how many are in flight.
type slow struct {
	Blobs
	cur, max, reqs atomic.Int64
}

func (s *slow) Range(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	s.reqs.Add(1)
	c := s.cur.Add(1)
	for {
		m := s.max.Load()
		if c <= m || s.max.CompareAndSwap(m, c) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	defer s.cur.Add(-1)
	return s.Blobs.Range(ctx, key, off, n)
}

// A clone asks the store for several groups at once, and the pack it writes is the same
// bytes a serial read writes.
func TestAClonesRangeReadsRunAhead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		sl := &slow{Blobs: NewMemory()}
		s, err := openWith(t, open, sl)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		for i := range 12 {
			if err := writeFile(fmt.Sprintf("%s/f%02d.bin", work, i), randomBlob(t, 3<<20)); err != nil {
				t.Fatal(err)
			}
		}
		tip := commitAll(t, work, "a", body(10, -1), "one")
		if err := push(ctx, s, "r", packOf(t, work, tip), tip, plumbing.ZeroHash); err != nil {
			t.Fatal(err)
		}
		r := s.Repo(ctx, "r")
		oids, err := r.Hashes(plumbing.AnyObject)
		if err != nil {
			t.Fatal(err)
		}
		set := make(map[plumbing.Hash]struct{}, len(oids))
		for _, h := range oids {
			set[h] = struct{}{}
		}
		var serial, ahead bytes.Buffer
		sl.max.Store(0)
		at := time.Now()
		if err := r.run(ctx, &serial, oids, set, 0); err != nil {
			t.Fatal(err)
		}
		serialTook := time.Since(at)
		if m := sl.max.Load(); m != 1 {
			t.Fatalf("the serial read had %d requests in flight", m)
		}
		sl.max.Store(0)
		at = time.Now()
		if err := r.run(ctx, &ahead, oids, set, readAhead); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d groups at 20 ms a request: serial %v, read ahead %v", sl.reqs.Load(), serialTook.Round(time.Millisecond), time.Since(at).Round(time.Millisecond))
		if m := sl.max.Load(); m < 2 {
			t.Fatalf("the read ahead had %d requests in flight", m)
		}
		if !bytes.Equal(serial.Bytes(), ahead.Bytes()) {
			t.Fatal("the read ahead wrote different entries")
		}
		bareFrom(t, ctx, s, "r", tip)

		// A reader that gives up stops the planner and the fetchers.
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := s.Repo(cctx, "r").Pack(cctx, io.Discard, oids); err == nil {
			t.Fatal("a cancelled clone finished")
		}
	})
}

func TestGeometricRoll(t *testing.T) {
	for _, c := range []struct {
		sizes []int64
		want  int
	}{
		{nil, 0},
		{[]int64{5}, 0},
		{[]int64{1, 1}, 2},
		{[]int64{1, 1, 100}, 2},
		{[]int64{1, 1, 1, 1}, 4},
		{[]int64{1, 2, 6, 18, 54}, 0},
		{[]int64{1, 2, 3, 8, 16}, 5},
		{[]int64{1, 2, 6, 18, 30}, 5},
		{[]int64{10, 10, 10, 10, 1000}, 4},
		{[]int64{10, 20, 1000, 3000}, 0},
		{[]int64{10, 20, 1000, 1500}, 4},
	} {
		if got := roll(c.sizes); got != c.want {
			t.Errorf("roll(%v) = %d, want %d", c.sizes, got, c.want)
		}
	}
}

// Merging is by size: the small packs a busy repository collects are merged, a large one
// it already holds is not rewritten, and the merge is streamed.
func TestGeometricMergeLeavesTheLargePackAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		if err := writeFile(work+"/big.bin", randomBlob(t, 3<<20)); err != nil {
			t.Fatal(err)
		}
		tip := commitAll(t, work, "a", body(3000, -1), "big")
		if err := push(ctx, s, "r", packOf(t, work, tip), tip, plumbing.ZeroHash); err != nil {
			t.Fatal(err)
		}
		var bigKey string
		for _, k := range blobs.Keys() {
			bigKey = k
		}
		prev := tip
		for i := range 6 {
			tip = commitFile(t, work, "a", body(3000, i*100), fmt.Sprint(i))
			if err := push(ctx, s, "r", packOf(t, work, tip, prev), tip, prev); err != nil {
				t.Fatal(err)
			}
			prev = tip
		}
		merged, err := s.Compact(ctx, "r")
		if err != nil || !merged {
			t.Fatalf("compact = %v, %v", merged, err)
		}
		var packs int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs WHERE repo = 'r'`).Scan(&packs)
		if packs != 2 {
			t.Fatalf("%d packs after; want the large one and the merge of the six", packs)
		}
		var still int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs WHERE repo = 'r' AND pack = $1`, packNameOf(bigKey)).Scan(&still)
		if still != 1 {
			t.Fatal("the large pack was rewritten")
		}
		if again, err := s.Compact(ctx, "r"); err != nil || again {
			t.Fatalf("a repository in shape was merged again: %v, %v", again, err)
		}
		var moves int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM moves`).Scan(&moves)
		if moves != 0 {
			t.Fatalf("%d side-table rows left", moves)
		}
		bareFrom(t, ctx, s, "r", tip)
	})
}

func packNameOf(key string) string {
	i := bytes.LastIndex([]byte(key), []byte("pack-"))
	return key[i+5 : len(key)-len(".pack")]
}

// Two replicas do not merge one repository at once, and a lease lapses.
func TestOneProcessMergesARepositoryAtATime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, _ := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		var prev plumbing.Hash
		for i := range 4 {
			tip := commitFile(t, work, "a", body(300, i*10), fmt.Sprint(i))
			pack := packOf(t, work, tip)
			if !prev.IsZero() {
				pack = packOf(t, work, tip, prev)
			}
			if err := push(ctx, s, "r", pack, tip, prev); err != nil {
				t.Fatal(err)
			}
			prev = tip
		}
		_, release, ok, err := s.lease(ctx, "repack:r")
		if err != nil || !ok {
			t.Fatalf("lease = %v, %v", ok, err)
		}
		if merged, err := s.Compact(ctx, "r"); err != nil || merged {
			t.Fatalf("a repository another process is merging was merged: %v, %v", merged, err)
		}
		if _, _, ok, _ := s.lease(ctx, "repack:r"); ok {
			t.Fatal("a held lease was taken")
		}
		release()
		if merged, err := s.Compact(ctx, "r"); err != nil || !merged {
			t.Fatalf("compact = %v, %v", merged, err)
		}
		// A holder that died: the lease lapses.
		if _, _, ok, _ := s.lease(ctx, "x"); !ok {
			t.Fatal("no lease")
		}
		s.now = func() time.Time { return time.Now().Add(2 * leaseTTL) }
		if _, release, ok, _ := s.lease(ctx, "x"); !ok {
			t.Fatal("a lapsed lease was not taken")
		} else {
			release()
		}
	})
}

// Maintenance merges what is out of shape, deletes the stray packs of a live repository
// by its own prefix and those of a deleted one by shard, and never a named pack.
func TestMaintenanceCollectsByRepositoryAndByShard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		mem := NewMemory()
		s, _ := storeWith(t, open, mem)
		for _, id := range []string{"a", "b"} {
			if err := s.Create(ctx, id, "main"); err != nil {
				t.Fatal(err)
			}
			work := workdir(t)
			var prev plumbing.Hash
			for i := range 5 {
				tip := commitFile(t, work, "f", body(300, i*10), fmt.Sprint(i))
				pack := packOf(t, work, tip)
				if !prev.IsZero() {
					pack = packOf(t, work, tip, prev)
				}
				if err := push(ctx, s, id, pack, tip, prev); err != nil {
					t.Fatal(err)
				}
				prev = tip
			}
		}
		// A stray pack in a live repository and in one that was never there.
		stray := s.packKey("a", "stray")
		gone := s.packKey("gone", "orphan")
		for _, k := range []string{stray, gone} {
			if err := mem.Put(ctx, k, bytes.NewReader([]byte("x")), 1); err != nil {
				t.Fatal(err)
			}
		}
		mem.Now = func() time.Time { return time.Now().Add(-2 * Grace) }
		_ = mem.Put(ctx, s.packKey("a", "stray2"), bytes.NewReader([]byte("x")), 1)
		mem.Now = time.Now

		compacted, collected, err := s.Maintain(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if compacted < 2 {
			t.Fatalf("made %d merges, want at least one in each repository", compacted)
		}
		// Young strays are kept, and so is every named pack.
		if collected != 1 {
			t.Fatalf("collected %d, want only the old stray", collected)
		}
		if mem.Get(stray) == nil || mem.Get(gone) == nil {
			t.Fatal("a young pack was deleted")
		}
		for _, id := range []string{"a", "b"} {
			var n int
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs WHERE repo = $1`, id).Scan(&n)
			if n >= 5 {
				t.Fatalf("%s still has %d packs", id, n)
			}
		}
		// Past Grace, the shard sweep finds the deleted repository's pack and the repository
		// sweep the live one's, and the replaced packs go when their grace is over.
		s.now = func() time.Time { return time.Now().Add(2 * Grace) }
		mem.Now = func() time.Time { return time.Now().Add(-2 * Grace) }
		_ = mem.Put(ctx, s.packKey("gone", "orphan"), bytes.NewReader([]byte("x")), 1)
		_ = mem.Put(ctx, stray, bytes.NewReader([]byte("x")), 1)
		mem.Now = time.Now
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Collect(ctx) }()
		wg.Wait()
		if mem.Get(stray) != nil || mem.Get(gone) != nil {
			t.Fatalf("strays were not collected: %v", mem.Keys())
		}
		var rows int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs`).Scan(&rows)
		if n := len(mem.Keys()); n != rows {
			t.Fatalf("%d blobs left for %d packs: %v", n, rows, mem.Keys())
		}
		bareFromAll(t, ctx, s, "a")
		bareFromAll(t, ctx, s, "b")
	})
}

func bareFromAll(t *testing.T, ctx context.Context, s *Store, id string) {
	t.Helper()
	r := s.Repo(ctx, id)
	ref, err := r.Reference("refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	bareFrom(t, ctx, s, id, ref.Hash())
}

func writeFile(path string, b []byte) error { return os.WriteFile(path, b, 0o644) }

// commitAll writes a file and commits everything in the work tree.
func commitAll(t testing.TB, dir, name string, content []byte, msg string) plumbing.Hash {
	t.Helper()
	if err := writeFile(dir+"/"+name, content); err != nil {
		t.Fatal(err)
	}
	run(t, dir, nil, "add", ".")
	run(t, dir, nil, "commit", "-q", "-m", msg)
	return plumbing.NewHash(strings.TrimSpace(string(run(t, dir, nil, "rev-parse", "HEAD"))))
}
