package objects

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// These measure the store at the sizes it is built for. They are off unless asked, and use
// the real object store when S3_ADMIN_* names one:
//
//	go test -run TestMeasurePush/postgres -pushmb=5120 ./apps/git/objects
//	go test -run TestMeasureEstate/postgres -bigmb=1024 -smallobjs=500000 ./apps/git/objects
var (
	pushmb    = flag.Int("pushmb", 0, "megabytes of random blobs for TestMeasurePush")
	smallObjs = flag.Int("smallobjs", 0, "objects in the repository of small files TestMeasureEstate builds")
)

// maxrss is the process's peak resident set, in bytes.
func maxrss() int64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) << 10
}

// heapPeak samples the Go heap while it runs and answers its peak.
func heapPeak() (stop func() uint64) {
	var peak atomic.Uint64
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapInuse > peak.Load() {
				peak.Store(m.HeapInuse)
			}
			select {
			case <-done:
				return
			case <-time.After(25 * time.Millisecond):
			}
		}
	}()
	return func() uint64 { close(done); <-finished; return peak.Load() }
}

func mb(n int64) string { return fmt.Sprintf("%.0f MB", float64(n)/(1<<20)) }

func blobsFor(t testing.TB) (Blobs, *realCounter) {
	rc := &realCounter{Blobs: realBlobs(t)}
	return rc, rc
}

// TestMeasurePush streams a push of -pushmb megabytes of incompressible blobs from a file
// through Ingest into the object store and reports the peak memory of the process.
func TestMeasurePush(t *testing.T) {
	if *pushmb == 0 {
		t.Skip("-pushmb is 0")
	}
	ctx := context.Background()
	b, rc := blobsFor(t)
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s, err := openWith(t, open, b)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, "p", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		const each = 4 << 20
		for i := range *pushmb << 20 / each {
			if err := os.WriteFile(fmt.Sprintf("%s/f%05d.bin", work, i), randomBlob(t, each), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// Stored, not deflated: the blobs do not compress and the test is of the store.
		run(t, work, nil, "-c", "core.compression=0", "add", ".")
		run(t, work, nil, "commit", "-q", "-m", "p")
		tip := plumbing.NewHash(strings.TrimSpace(string(run(t, work, nil, "rev-parse", "HEAD"))))
		file := t.TempDir() + "/push.pack"
		at := time.Now()
		cmd := exec.Command("git", "-c", "pack.compression=0", "-c", "pack.window=0", "pack-objects", "--stdout", "--revs", "-q")
		cmd.Dir = work
		cmd.Stdin = strings.NewReader(tip.String() + "\n")
		out, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = out
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		_ = out.Close()
		packing := time.Since(at)
		if err := os.RemoveAll(work); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		fi, _ := os.Stat(file)

		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		before := maxrss()
		rc.puts.Store(0)
		stop := heapPeak()
		at = time.Now()
		in, err := s.Ingest(ctx, "p", bufio.NewReaderSize(f, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		ingest := time.Since(at)
		heap := stop()
		after := maxrss()

		tx, err := s.Begin(ctx, "p")
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Attach(in); err != nil {
			t.Fatal(err)
		}
		name := plumbing.ReferenceName("refs/heads/main")
		if err := tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{name: tip}); err != nil {
			t.Fatal(err)
		}
		if err := tx.CreateReference(plumbing.NewHashReference(name, tip)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		t.Logf("push of %s, %d objects: pack-objects %v; ingest (read, scan, hash, multipart upload) %v = %.0f MB/s",
			mb(fi.Size()), in.Objects(), packing.Round(time.Second), ingest.Round(time.Millisecond), float64(fi.Size())/(1<<20)/ingest.Seconds())
		t.Logf("memory: Go heap peak during the ingest %s; process peak RSS %s before, %s after (the test process, not git)",
			mb(int64(heap)), mb(before), mb(after))
	})
}

// fastImport builds a repository of n small files in dir with git fast-import.
func fastImport(t testing.TB, dir string, n int) plumbing.Hash {
	t.Helper()
	cmd := exec.Command("git", "fast-import", "--quiet", "--done")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(in, 1<<20)
	for i := range n {
		c := fmt.Sprintf("file %d\n%s\n", i, strings.Repeat(fmt.Sprintf("%08x ", i*2654435761), 12+i%20))
		fmt.Fprintf(w, "blob\nmark :%d\ndata %d\n%s\n", i+1, len(c), c)
	}
	fmt.Fprintf(w, "commit refs/heads/main\ncommitter t <t@t.io> 1700000000 +0000\ndata 5\nsmall\n")
	for i := range n {
		fmt.Fprintf(w, "M 100644 :%d d%04d/f%06d.txt\n", i+1, i/500, i)
	}
	fmt.Fprint(w, "\ndone\n")
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, errb.String())
	}
	return plumbing.NewHash(strings.TrimSpace(string(run(t, dir, nil, "rev-parse", "HEAD"))))
}

// reach lists what a clone of tip sends, the way the server plans it: each tree is read
// once and its subtrees are placed in the index in one round trip before they are read.
func reach(r *Repo, tip plumbing.Hash, have map[plumbing.Hash]struct{}) ([]plumbing.Hash, error) {
	var out []plumbing.Hash
	seen := map[plumbing.Hash]struct{}{}
	add := func(h plumbing.Hash) bool {
		if _, ok := seen[h]; ok {
			return false
		}
		if _, ok := have[h]; ok {
			return false
		}
		seen[h] = struct{}{}
		out = append(out, h)
		return true
	}
	c, err := object.GetCommit(r, tip)
	if err != nil {
		return nil, err
	}
	add(tip)
	var walk func(h plumbing.Hash) error
	walk = func(h plumbing.Hash) error {
		if !add(h) {
			return nil
		}
		tr, err := object.GetTree(r, h)
		if err != nil {
			return err
		}
		var kids []plumbing.Hash
		for _, e := range tr.Entries {
			if e.Mode == filemode.Dir {
				kids = append(kids, e.Hash)
			}
		}
		if err := r.Prefetch(kids); err != nil {
			return err
		}
		for _, e := range tr.Entries {
			if e.Mode == filemode.Dir {
				if err := walk(e.Hash); err != nil {
					return err
				}
			} else if e.Mode != filemode.Submodule {
				add(e.Hash)
			}
		}
		return nil
	}
	return out, walk(c.TreeHash)
}

// named is every object reachable from tip, which is what a client that has it holds.
func named(r *Repo, tip plumbing.Hash) (map[plumbing.Hash]struct{}, error) {
	hs, err := reach(r, tip, nil)
	if err != nil {
		return nil, err
	}
	m := make(map[plumbing.Hash]struct{}, len(hs))
	for _, h := range hs {
		m[h] = struct{}{}
	}
	return m, nil
}

type counted struct {
	n int64
}

func (c *counted) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// serve is one clone or fetch as the server does it: plan, then write the pack, on a Repo
// of its own. It answers the times and the requests the store was asked.
func serveOnce(t testing.TB, ctx context.Context, s *Store, rc *realCounter, id string, tip plumbing.Hash, have map[plumbing.Hash]struct{}, clone bool) (plan, pack time.Duration, oids int, n int64, gets int) {
	t.Helper()
	r := s.Repo(ctx, id)
	rc.ranges.Store(0)
	at := time.Now()
	hs, err := reach(r, tip, have)
	if err != nil {
		t.Fatal(err)
	}
	plan = time.Since(at)
	at = time.Now()
	var w counted
	if err := r.Pack(ctx, &w, hs); err != nil {
		t.Fatal(err)
	}
	return plan, time.Since(at), len(hs), w.n, int(rc.ranges.Load())
}

// TestMeasureEstate pushes a repository of a few large objects (-bigmb) and one of many
// small ones (-smallobjs), clones each, and fetches one commit on the large one, reporting
// the time of each stage and the requests made to the object store.
func TestMeasureEstate(t *testing.T) {
	if *bigmb == 0 && *smallObjs == 0 {
		t.Skip("-bigmb and -smallobjs are 0")
	}
	ctx := context.Background()
	b, rc := blobsFor(t)
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s, err := openWith(t, open, b)
		if err != nil {
			t.Fatal(err)
		}
		land := func(id string, work string, tip plumbing.Hash, pack []byte, old plumbing.Hash) (ingest, commit time.Duration) {
			at := time.Now()
			in, err := s.Ingest(ctx, id, bytes.NewReader(pack))
			if err != nil {
				t.Fatal(err)
			}
			ingest = time.Since(at)
			at = time.Now()
			tx, err := s.Begin(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Attach(in); err != nil {
				t.Fatal(err)
			}
			name := plumbing.ReferenceName("refs/heads/main")
			if err := tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{name: tip}); err != nil {
				t.Fatal(err)
			}
			if old.IsZero() {
				err = tx.CreateReference(plumbing.NewHashReference(name, tip))
			} else {
				err = tx.CheckAndSetReference(plumbing.NewHashReference(name, tip), plumbing.NewHashReference(name, old))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			return ingest, time.Since(at)
		}
		// cloneIn is a client's index-pack over what the server sends.
		cloneIn := func(id string, tip plumbing.Hash, oids []plumbing.Hash) time.Duration {
			pr, pw := io.Pipe()
			go func() { _ = pw.CloseWithError(s.Repo(ctx, id).Pack(ctx, pw, oids)) }()
			dir := t.TempDir()
			run(t, dir, nil, "init", "-q", "--bare", ".")
			at := time.Now()
			run(t, dir, pr, "index-pack", "--stdin")
			return time.Since(at)
		}

		if *bigmb > 0 {
			if err := s.Create(ctx, "big", "main"); err != nil {
				t.Fatal(err)
			}
			work := workdir(t)
			const each = 4 << 20
			for i := range *bigmb << 20 / each {
				if err := os.WriteFile(fmt.Sprintf("%s/f%04d.bin", work, i), randomBlob(t, each), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			run(t, work, nil, "add", ".")
			run(t, work, nil, "commit", "-q", "-m", "big")
			tip := plumbing.NewHash(strings.TrimSpace(string(run(t, work, nil, "rev-parse", "HEAD"))))
			at := time.Now()
			pack := run(t, work, strings.NewReader(tip.String()+"\n"), "-c", "pack.compression=0", "-c", "pack.window=0", "pack-objects", "--stdout", "--revs", "-q")
			packing := time.Since(at)
			ing, com := land("big", work, tip, pack, plumbing.ZeroHash)
			t.Logf("[large] %d MB: pack-objects %v, ingest %v, attach+commit %v",
				*bigmb, packing.Round(time.Millisecond), ing.Round(time.Millisecond), com.Round(time.Millisecond))
			pack = nil

			for _, label := range []string{"cold", "warm"} {
				if label == "cold" {
					s.cache.locs.purge("")
					s.cache.blocks.purge("")
				}
				plan, pk, n, bytes, gets := serveOnce(t, ctx, s, rc, "big", tip, nil, true)
				t.Logf("[large] clone (%s cache): plan %v, pack %v (%d objects, %s, %d range requests, %.0f MB/s)",
					label, plan.Round(time.Millisecond), pk.Round(time.Millisecond), n, mb(bytes), gets, float64(bytes)/(1<<20)/pk.Seconds())
			}
			hs, _ := reach(s.Repo(ctx, "big"), tip, nil)
			t.Logf("[large] clone through git index-pack: %v", cloneIn("big", tip, hs).Round(time.Millisecond))

			// One commit on top, and a client that has the first.
			have, err := named(s.Repo(ctx, "big"), tip)
			if err != nil {
				t.Fatal(err)
			}
			tip2 := commitAll(t, work, "note.txt", body(50, 3), "small change")
			thin := packOf(t, work, tip2, tip)
			ing, com = land("big", work, tip2, thin, tip)
			t.Logf("[incremental] one-commit push of %d bytes: ingest %v, attach+commit %v", len(thin), ing.Round(time.Millisecond), com.Round(time.Millisecond))
			for _, label := range []string{"cold", "warm"} {
				if label == "cold" {
					s.cache.locs.purge("")
					s.cache.blocks.purge("")
				}
				plan, pk, n, bytes, gets := serveOnce(t, ctx, s, rc, "big", tip2, have, false)
				t.Logf("[incremental] fetch of the new commit on a %d MB repository (%s cache): plan %v, pack %v (%d objects, %s, %d range requests)",
					*bigmb, label, plan.Round(time.Millisecond), pk.Round(time.Millisecond), n, mb(bytes), gets)
			}
		}

		if *smallObjs > 0 {
			if err := s.Create(ctx, "small", "main"); err != nil {
				t.Fatal(err)
			}
			work := workdir(t)
			at := time.Now()
			tip := fastImport(t, work, *smallObjs)
			gen := time.Since(at)
			at = time.Now()
			pack := run(t, work, strings.NewReader(tip.String()+"\n"), "-c", "pack.window=0", "pack-objects", "--stdout", "--revs", "-q")
			packing := time.Since(at)
			ing, com := land("small", work, tip, pack, plumbing.ZeroHash)
			var entries int64
			_ = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE repo = 'small'`).Scan(&entries)
			t.Logf("[small] %d files, %d objects, pack %s: generate %v, pack-objects %v, ingest %v, attach+commit (%d index rows) %v",
				*smallObjs, entries, mb(int64(len(pack))), gen.Round(time.Millisecond), packing.Round(time.Millisecond),
				ing.Round(time.Millisecond), entries, com.Round(time.Millisecond))
			pack = nil
			for _, label := range []string{"cold", "warm"} {
				if label == "cold" {
					s.cache.locs.purge("")
					s.cache.blocks.purge("")
				}
				stop := heapPeak()
				plan, pk, n, bytes, gets := serveOnce(t, ctx, s, rc, "small", tip, nil, true)
				heap := stop()
				t.Logf("[small] clone (%s cache): plan %v, pack %v (%d objects, %s, %d range requests), heap peak %s",
					label, plan.Round(time.Millisecond), pk.Round(time.Millisecond), n, mb(bytes), gets, mb(int64(heap)))
			}
			hs, _ := reach(s.Repo(ctx, "small"), tip, nil)
			t.Logf("[small] clone through git index-pack: %v", cloneIn("small", tip, hs).Round(time.Millisecond))
			// One commit on top.
			have, err := named(s.Repo(ctx, "small"), tip)
			if err != nil {
				t.Fatal(err)
			}
			tip2 := commitAll(t, work, "new.txt", body(20, -1), "one more")
			ing, com = land("small", work, tip2, packOf(t, work, tip2, tip), tip)
			plan, pk, n, bytes, gets := serveOnce(t, ctx, s, rc, "small", tip2, have, false)
			t.Logf("[small] one-commit fetch on %d objects: ingest %v, commit %v; plan %v, pack %v (%d objects, %s, %d range requests)",
				*smallObjs, ing.Round(time.Millisecond), com.Round(time.Millisecond), plan.Round(time.Millisecond), pk.Round(time.Millisecond), n, mb(bytes), gets)
		}
		t.Logf("process peak RSS %s", mb(maxrss()))
	})
}
