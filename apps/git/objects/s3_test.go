package objects

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/namespace"
)

// bigmb is the size of the repository TestBigRepository pushes and clones, in megabytes.
// It is 0 (the test is skipped) unless a run asks: go test -run TestBigRepository -bigmb=1024
var bigmb = flag.Int("bigmb", 0, "megabytes of random blobs for TestBigRepository")

// realBlobs is a bucket on the object store the process opens (object.Open): the one
// S3_ADMIN_* names when a deployment's driver is linked and configured, local disk
// otherwise. It is made for the test and emptied after it.
func realBlobs(t testing.TB) Blobs {
	t.Helper()
	store, err := object.Open(&object.Local{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var token [5]byte
	_, _ = rand.Read(token[:])
	bucket := "git-objects-test-" + hex.EncodeToString(token[:])
	b := Bucket(store, bucket)
	t.Cleanup(func() {
		ctx := context.Background()
		_ = b.List(ctx, "", func(o Blob) error { return b.Delete(ctx, o.Key) })
		// A bucket is volumes on the server; leaving them is how a long run exhausts it.
		_ = store.Drop(ctx, bucket)
	})
	return b
}

type realCounter struct {
	Blobs
	puts, ranges atomic.Int64
}

func (c *realCounter) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	c.puts.Add(1)
	return c.Blobs.Put(ctx, key, r, size)
}

func (c *realCounter) Range(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	c.ranges.Add(1)
	return c.Blobs.Range(ctx, key, off, n)
}

// Pushes, a clone, compaction and collection against a real object store: every blob
// request the store makes is one the S3 client and the server answer.
func TestRealObjectStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		rb := &realCounter{Blobs: realBlobs(t)}
		s, err := openWith(t, open, rb)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		var prev, tip plumbing.Hash
		for i := range 9 {
			tip = commitFile(t, work, "a.txt", body(3000, i*200), fmt.Sprintf("c%d", i))
			pack := packOf(t, work, tip)
			if !prev.IsZero() {
				pack = packOf(t, work, tip, prev)
			}
			if err := push(ctx, s, "r", pack, tip, prev); err != nil {
				t.Fatal(err)
			}
			prev = tip
		}
		bareFrom(t, ctx, s, "r", tip)
		if merged, err := s.Compact(ctx, "r"); err != nil || !merged {
			t.Fatalf("compact = %v, %v", merged, err)
		}
		bareFrom(t, ctx, s, "r", tip)
		s.now = func() time.Time { return time.Now().Add(2 * Grace) }
		n, err := s.Collect(ctx)
		if err != nil || n < 2 {
			t.Fatalf("collect = %d, %v; want the replaced packs", n, err)
		}
		var left []string
		_ = rb.List(ctx, "", func(o Blob) error { left = append(left, o.Key); return nil })
		var rows int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs`).Scan(&rows)
		if len(left) != rows {
			t.Fatalf("%d blobs left for %d packs: %v", len(left), rows, left)
		}
		bareFrom(t, ctx, s, "r", tip)
		t.Logf("%d uploads, %d range requests", rb.puts.Load(), rb.ranges.Load())
	})
}

func openWith(t testing.TB, open sqltest.Open, b Blobs) (*Store, error) {
	t.Helper()
	s, err := Open(open(namespace.System(), "objects"), b, namespace.System())
	if err == nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
}

// TestBigRepository pushes and clones a repository of -bigmb megabytes of blobs git
// cannot compress, and reports what each stage took. It uses the object store the process
// opens: the one S3_ADMIN_* names, local disk otherwise.
func TestBigRepository(t *testing.T) {
	if *bigmb == 0 {
		t.Skip("-bigmb is 0")
	}
	ctx := context.Background()
	rc := &realCounter{Blobs: realBlobs(t)}
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		s, err := openWith(t, open, rc)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, "big", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		const each = 4 << 20
		files := *bigmb << 20 / each
		for i := range files {
			if err := os.WriteFile(fmt.Sprintf("%s/f%04d.bin", work, i), randomBlob(t, each), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		run(t, work, nil, "add", ".")
		run(t, work, nil, "commit", "-q", "-m", "big")
		tip := plumbing.NewHash(string(bytes.TrimSpace(run(t, work, nil, "rev-parse", "HEAD"))))

		at := time.Now()
		pack := packOf(t, work, tip)
		packing := time.Since(at)

		at = time.Now()
		in, err := s.Ingest(ctx, "big", bytes.NewReader(pack))
		if err != nil {
			t.Fatal(err)
		}
		ingest := time.Since(at)

		at = time.Now()
		tx, err := s.Begin(ctx, "big")
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
		commit := time.Since(at)

		r := s.Repo(ctx, "big")
		oids, err := r.Hashes(plumbing.AnyObject)
		if err != nil {
			t.Fatal(err)
		}
		rc.ranges.Store(0)
		at = time.Now()
		var served countWriter
		if err := r.Pack(ctx, &served, oids); err != nil {
			t.Fatal(err)
		}
		serve := time.Since(at)

		// The clone as a client sees it: the served bytes through git's own index-pack.
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(s.Repo(ctx, "big").Pack(ctx, pw, oids)) }()
		dir := t.TempDir()
		run(t, dir, nil, "init", "-q", "--bare", ".")
		at = time.Now()
		run(t, dir, pr, "index-pack", "--stdin", "--strict")
		clone := time.Since(at)
		run(t, dir, nil, "update-ref", "refs/heads/main", tip.String())

		t.Logf("%d MB, %d objects, pack %d bytes, served %d bytes", *bigmb, len(oids), len(pack), served.n)
		t.Logf("pack-objects %v | ingest+upload %v | attach+commit %v | serve to discard %v (%d ranges) | clone through index-pack %v",
			packing.Round(time.Millisecond), ingest.Round(time.Millisecond), commit.Round(time.Millisecond),
			serve.Round(time.Millisecond), rc.ranges.Load(), clone.Round(time.Millisecond))
	})
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
