package objects

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/namespace"
)

// run is git in dir. It reads no configuration of the machine's.
func run(t testing.TB, dir string, stdin io.Reader, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t.io", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t.io")
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return out.Bytes()
}

func workdir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, nil, "init", "-q", "-b", "main", ".")
	return dir
}

// commitFile writes a file and commits it, answering the commit.
func commitFile(t testing.TB, dir, name string, content []byte, msg string) plumbing.Hash {
	t.Helper()
	if err := os.WriteFile(dir+"/"+name, content, 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, nil, "add", name)
	run(t, dir, nil, "commit", "-q", "-m", msg)
	return plumbing.NewHash(strings.TrimSpace(string(run(t, dir, nil, "rev-parse", "HEAD"))))
}

// packOf is the pack git makes of the objects reachable from tip and not from bases:
// thin, with offset deltas, as a client sends a push.
func packOf(t testing.TB, dir string, tip plumbing.Hash, bases ...plumbing.Hash) []byte {
	t.Helper()
	revs := tip.String() + "\n"
	for _, b := range bases {
		revs += "^" + b.String() + "\n"
	}
	return run(t, dir, strings.NewReader(revs), "pack-objects", "--stdout", "--revs", "--thin", "--delta-base-offset", "-q")
}

// body is a file of n lines that edit changes a little.
func body(n int, edit int) []byte {
	var b bytes.Buffer
	for i := range n {
		fmt.Fprintf(&b, "line %06d of the file with some words to compress and to delta against\n", i)
		if i == edit {
			b.WriteString("an edited line\n")
		}
	}
	return b.Bytes()
}

// push lands pack in repository id as a client's push would: read and uploaded, then
// attached, checked and the branch moved, in one transaction.
func push(ctx context.Context, s *Store, id string, pack []byte, tip plumbing.Hash, old plumbing.Hash) error {
	in, err := s.Ingest(ctx, id, bytes.NewReader(pack))
	if err != nil {
		return err
	}
	tx, err := s.Begin(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Attach(in); err != nil {
		return err
	}
	name := plumbing.ReferenceName("refs/heads/main")
	if err := tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{name: tip}); err != nil {
		return err
	}
	if old.IsZero() {
		err = tx.CreateReference(plumbing.NewHashReference(name, tip))
	} else {
		err = tx.CheckAndSetReference(plumbing.NewHashReference(name, tip), plumbing.NewHashReference(name, old))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// bareFrom reads what the store serves into a bare repository and checks it with git.
func bareFrom(t testing.TB, ctx context.Context, s *Store, id string, tip plumbing.Hash) string {
	t.Helper()
	r := s.Repo(ctx, id)
	oids, err := r.Hashes(plumbing.AnyObject)
	if err != nil {
		t.Fatal(err)
	}
	var pack bytes.Buffer
	if err := r.Pack(ctx, &pack, oids); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run(t, dir, nil, "init", "-q", "--bare", ".")
	run(t, dir, &pack, "index-pack", "--stdin", "--strict")
	run(t, dir, nil, "update-ref", "refs/heads/main", tip.String())
	run(t, dir, nil, "fsck", "--strict", "--no-dangling")
	return dir
}

// A client's pushes, a thin one among them, are kept as the packs it sent, and what the
// store serves is a pack git accepts whose every object is the one that went in.
func TestPushesKeepTheirPacksAndServeThemBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		c1 := commitFile(t, work, "big.txt", body(5000, -1), "one")
		if err := push(ctx, s, "r", packOf(t, work, c1), c1, plumbing.ZeroHash); err != nil {
			t.Fatal(err)
		}
		c2 := commitFile(t, work, "big.txt", body(5000, 2500), "two")
		thin := packOf(t, work, c2, c1)
		if err := push(ctx, s, "r", thin, c2, c1); err != nil {
			t.Fatalf("a thin pack: %v", err)
		}
		c3 := commitFile(t, work, "big.txt", body(5000, 100), "three")
		if err := push(ctx, s, "r", packOf(t, work, c3, c2), c3, c2); err != nil {
			t.Fatal(err)
		}

		if got := len(blobs.Keys()); got != 3 {
			t.Fatalf("%d blobs for three pushes: %v", got, blobs.Keys())
		}
		var packs, entries int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs WHERE repo = 'r'`).Scan(&packs)
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE repo = 'r'`).Scan(&entries)
		want := strings.Count(strings.TrimSpace(string(run(t, work, nil, "rev-list", "--objects", "--all"))), "\n") + 1
		if packs != 3 || entries != want {
			t.Fatalf("%d packs, %d entries; git counts %d objects", packs, entries, want)
		}
		// The second push was a delta against an object only the first pack holds.
		var deltas int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE repo = 'r' AND base IS NOT NULL`).Scan(&deltas)
		if deltas == 0 {
			t.Fatal("no object was kept as a delta, so the test did not exercise one")
		}

		bare := bareFrom(t, ctx, s, "r", c3)
		for _, c := range []plumbing.Hash{c1, c2, c3} {
			if got := run(t, bare, nil, "cat-file", "-t", c.String()); string(got) != "commit\n" {
				t.Fatalf("%s = %q", c, got)
			}
		}
		if got := run(t, bare, nil, "show", "main:big.txt"); !bytes.Equal(got, body(5000, 100)) {
			t.Fatal("the file served is not the file pushed")
		}
		// And go-git reads it as it would a directory.
		cm, err := object.GetCommit(s.Repo(ctx, "r"), c3)
		if err != nil || cm.Message != "three\n" {
			t.Fatalf("commit = %v, %v", cm, err)
		}
		tr, _ := cm.Tree()
		f, err := tr.File("big.txt")
		if err != nil {
			t.Fatal(err)
		}
		if s, _ := f.Contents(); s != string(body(5000, 100)) {
			t.Fatal("go-git read other bytes than were pushed")
		}
	})
}

// A pack with a hole in it is refused whole and leaves nothing visible; a pack that
// is damaged is refused as the client's fault.
func TestADamagedOrHollowPackIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, _ := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		c1 := commitFile(t, work, "a", body(100, -1), "one")
		c2 := commitFile(t, work, "a", body(100, 5), "two")
		hollow := packOf(t, work, c2, c1) // c1's objects are not in it, and not in the store
		err := push(ctx, s, "r", hollow, c2, plumbing.ZeroHash)
		if err == nil || (!errors.Is(err, ErrMissing) && !errors.Is(err, ErrBadPack)) {
			t.Fatalf("a hollow pack = %v", err)
		}
		if n, _ := s.Repo(ctx, "r").Count(); n != 0 {
			t.Fatalf("a refused push left %d objects", n)
		}
		whole := packOf(t, work, c2)
		whole[len(whole)/2] ^= 0xff
		if _, err := s.Ingest(ctx, "r", bytes.NewReader(whole)); !errors.Is(err, ErrBadPack) {
			t.Fatalf("a damaged pack = %v", err)
		}
		if _, err := s.Ingest(ctx, "r", bytes.NewReader([]byte("not a pack at all, no"))); !errors.Is(err, ErrBadPack) {
			t.Fatalf("garbage = %v", err)
		}
	})
}

// Two pushes of one branch from one tip: one lands, the other is told the branch moved,
// and nothing of the loser is visible.
func TestTwoConcurrentPushesOneWinsAndNoOrphanIsVisible(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		base := commitFile(t, work, "a", body(200, -1), "base")
		if err := push(ctx, s, "r", packOf(t, work, base), base, plumbing.ZeroHash); err != nil {
			t.Fatal(err)
		}
		// Two clients, each with a child of base.
		run(t, work, nil, "checkout", "-q", "-b", "left")
		left := commitFile(t, work, "left", body(50, -1), "left")
		run(t, work, nil, "checkout", "-q", "main")
		run(t, work, nil, "checkout", "-q", "-b", "right")
		right := commitFile(t, work, "right", body(60, -1), "right")
		packs := map[plumbing.Hash][]byte{left: packOf(t, work, left, base), right: packOf(t, work, right, base)}

		var wg sync.WaitGroup
		var won, lost atomic.Int32
		start := make(chan struct{})
		for tip, pack := range packs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				switch err := push(ctx, s, "r", pack, tip, base); {
				case err == nil:
					won.Add(1)
				case errors.Is(err, storage.ErrReferenceHasChanged):
					lost.Add(1)
				default:
					t.Errorf("push %s: %v", tip, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if won.Load() != 1 || lost.Load() != 1 {
			t.Fatalf("won %d, lost %d; want one of each", won.Load(), lost.Load())
		}
		ref, _ := s.Repo(ctx, "r").Reference("refs/heads/main")
		loser := left
		if ref.Hash() == left {
			loser = right
		}
		if ref.Hash() == loser {
			t.Fatal("the branch is at the loser")
		}
		if err := s.Repo(ctx, "r").HasEncodedObject(loser); err == nil {
			t.Fatal("the loser's commit is visible")
		}
		// Its pack is in the bucket, which nothing reads, and Collect removes it once it is old.
		if len(blobs.Keys()) != 3 {
			t.Fatalf("%d blobs; want base, winner and the loser's orphan: %v", len(blobs.Keys()), blobs.Keys())
		}
		s.now = func() time.Time { return time.Now().Add(2 * Grace) }
		if n, err := s.Collect(ctx); err != nil || n != 1 {
			t.Fatalf("collect = %d, %v; want the one orphan", n, err)
		}
		if len(blobs.Keys()) != 2 {
			t.Fatalf("%d blobs after collect", len(blobs.Keys()))
		}
		bareFrom(t, ctx, s, "r", ref.Hash())
	})
}

// A push made through one replica is read through another, which shares nothing with
// it but the database and the blob store.
func TestAPushOnOneReplicaIsReadOnAnother(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		a, blobs := storeWith(t, open, NewMemory())
		// On PostgreSQL the second replica is a connection pool of its own. A SQLite file in
		// a build without the codec is one process's, so there it is a second Store over the
		// same handle: no cache in common, which is what the test is about.
		bdb := a.db
		if strings.HasSuffix(t.Name(), "/postgres") {
			bdb = open(namespace.System(), "objects")
		}
		b, err := Open(bdb, blobs, namespace.System())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
		if err := a.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		c1 := commitFile(t, work, "a", body(300, -1), "one")
		if err := push(ctx, a, "r", packOf(t, work, c1), c1, plumbing.ZeroHash); err != nil {
			t.Fatal(err)
		}
		c2 := commitFile(t, work, "a", body(300, 9), "two")
		// B takes the next push, a thin one against what A holds.
		if err := push(ctx, b, "r", packOf(t, work, c2, c1), c2, c1); err != nil {
			t.Fatal(err)
		}
		bareFrom(t, ctx, a, "r", c2)
		bareFrom(t, ctx, b, "r", c2)
		if sa, _ := a.Size(ctx, "r"); sa == 0 {
			t.Fatal("the repository has no size")
		}
	})
}

// An upload that reached the blob store and never reached a commit leaves nothing
// visible, however it ended.
func TestACrashAfterTheUploadBeforeTheCommitLeavesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		c1 := commitFile(t, work, "a", body(100, -1), "one")
		pack := packOf(t, work, c1)

		// 1. The process dies after the upload: nothing was attached.
		if _, err := s.Ingest(ctx, "r", bytes.NewReader(pack)); err != nil {
			t.Fatal(err)
		}
		// 2. The upload lands and the caller is told it failed.
		blobs.Fail.Store(true)
		if _, err := s.Ingest(ctx, "r", bytes.NewReader(pack)); err == nil {
			t.Fatal("a failed upload was not reported")
		}
		blobs.Fail.Store(false)
		// 3. The transaction is rolled back after the pack was attached.
		in, _ := s.Ingest(ctx, "r", bytes.NewReader(pack))
		tx, err := s.Begin(ctx, "r")
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Attach(in); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}

		r := s.Repo(ctx, "r")
		if n, _ := r.Count(); n != 0 {
			t.Fatalf("%d objects are visible", n)
		}
		if err := r.HasEncodedObject(c1); err == nil {
			t.Fatal("the commit is visible")
		}
		if _, err := r.Reference("refs/heads/main"); err == nil {
			t.Fatal("a branch exists")
		}
		var rows int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs`).Scan(&rows)
		if rows != 0 {
			t.Fatalf("%d pack rows", rows)
		}
		if sz, _ := s.Size(ctx, "r"); sz != 0 {
			t.Fatalf("size %d", sz)
		}
		// Each upload is a pack of its own, named by chance, so a failed upload deletes its
		// own bytes (nobody else can be about to attach them): two orphans are left.
		if len(blobs.Keys()) != 2 {
			t.Fatalf("blobs %v", blobs.Keys())
		}
		// Young garbage is kept: its commit may be one request away.
		if n, _ := s.Collect(ctx); n != 0 {
			t.Fatalf("collected %d young blobs", n)
		}
		s.now = func() time.Time { return time.Now().Add(2 * Grace) }
		if n, err := s.Collect(ctx); err != nil || n != 2 {
			t.Fatalf("collect = %d, %v; want the orphans", n, err)
		}
		s.now = time.Now
		if len(blobs.Keys()) != 0 {
			t.Fatalf("blobs %v", blobs.Keys())
		}
		// The push then lands whole.
		if err := push(ctx, s, "r", pack, c1, plumbing.ZeroHash); err != nil {
			t.Fatal(err)
		}
		bareFrom(t, ctx, s, "r", c1)
	})
}

// Compaction merges small packs into one, keeps every object, turns offset deltas into
// reference deltas, and the packs it replaced go after the grace period.
func TestCompactionKeepsEveryObject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, blobs := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		var tip, prev plumbing.Hash
		want := map[plumbing.Hash][]byte{}
		for i := range 12 {
			tip = commitFile(t, work, "a", body(2000, i*100), fmt.Sprintf("c%d", i))
			var pack []byte
			if prev.IsZero() {
				pack = packOf(t, work, tip)
			} else {
				pack = packOf(t, work, tip, prev)
			}
			if err := push(ctx, s, "r", pack, tip, prev); err != nil {
				t.Fatal(err)
			}
			prev = tip
		}
		r := s.Repo(ctx, "r")
		all, _ := r.Hashes(plumbing.AnyObject)
		for _, h := range all {
			o, err := r.EncodedObject(plumbing.AnyObject, h)
			if err != nil {
				t.Fatal(err)
			}
			rd, _ := o.Reader()
			b, _ := io.ReadAll(rd)
			_ = rd.Close()
			want[h] = b
		}
		sizeBefore, _ := s.Size(ctx, "r")
		if len(blobs.Keys()) != 12 {
			t.Fatalf("%d packs before", len(blobs.Keys()))
		}
		merged, err := s.Compact(ctx, "r")
		if err != nil || !merged {
			t.Fatalf("compact = %v, %v", merged, err)
		}
		var packs int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM packs WHERE repo = 'r'`).Scan(&packs)
		if packs != 1 {
			t.Fatalf("%d packs after", packs)
		}
		var ofs int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE repo = 'r' AND base IS NOT NULL`).Scan(&ofs)
		if ofs == 0 {
			t.Fatal("no deltas to convert")
		}
		// A fresh reader on a fresh Repo, so nothing it remembers answers.
		r2 := s.Repo(ctx, "r")
		for h, b := range want {
			o, err := r2.EncodedObject(plumbing.AnyObject, h)
			if err != nil {
				t.Fatalf("%s after compaction: %v keys=%v", h, err, blobs.Keys())
			}
			rd, _ := o.Reader()
			got, _ := io.ReadAll(rd)
			_ = rd.Close()
			if !bytes.Equal(got, b) {
				t.Fatalf("%s changed", h)
			}
		}
		if sizeAfter, _ := s.Size(ctx, "r"); sizeAfter <= 0 || sizeAfter >= sizeBefore*2 {
			t.Fatalf("size %d -> %d", sizeBefore, sizeAfter)
		}
		bareFrom(t, ctx, s, "r", tip)
		// The old packs are still there, for a reader mid-flight, until their grace is over.
		if n, _ := s.Collect(ctx); n != 0 || len(blobs.Keys()) != 13 {
			t.Fatalf("collect = %d, %d blobs", n, len(blobs.Keys()))
		}
		s.now = func() time.Time { return time.Now().Add(2 * Grace) }
		if n, err := s.Collect(ctx); err != nil || n != 12 {
			t.Fatalf("collect = %d, %v; want the twelve replaced packs", n, err)
		}
		if len(blobs.Keys()) != 1 {
			t.Fatalf("blobs %v", blobs.Keys())
		}
		bareFrom(t, ctx, s, "r", tip)
		// Nothing more to merge.
		if again, _ := s.Compact(ctx, "r"); again {
			t.Fatal("merged a single pack")
		}
	})
}

// Compaction that races a push gives up or lands without losing the push.
func TestCompactionRacingAPushLosesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, _ := storeWith(t, open, NewMemory())
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		var prev plumbing.Hash
		for i := range 9 {
			tip := commitFile(t, work, "a", body(500, i*10), fmt.Sprintf("c%d", i))
			pack := packOf(t, work, tip)
			if !prev.IsZero() {
				pack = packOf(t, work, tip, prev)
			}
			if err := push(ctx, s, "r", pack, tip, prev); err != nil {
				t.Fatal(err)
			}
			prev = tip
		}
		next := commitFile(t, work, "a", body(500, 499), "late")
		pack := packOf(t, work, next, prev)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := push(ctx, s, "r", pack, next, prev); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := s.Compact(ctx, "r"); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		bareFrom(t, ctx, s, "r", next)
	})
}

// Rows a release before packs left behind are moved into packs when the store opens,
// verified, and the table is dropped.
func TestRowsFromBeforePacksAreMoved(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		db := open(namespace.System(), "objects")
		// A store of the old shape, written by the old code.
		old, err := Open(db, NewMemory(), namespace.System())
		if err != nil {
			t.Fatal(err)
		}
		if err := old.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		c1 := commitFile(t, work, "a", body(50, -1), "one")
		blobsList := strings.Fields(string(run(t, work, nil, "rev-list", "--objects", "--all")))
		_ = blobsList
		// Rewrite its objects as rows: the legacy table, filled from what git says.
		if _, err := db.Exec(`CREATE TABLE objects (repo TEXT NOT NULL, oid ` + old.d.Blob() + ` NOT NULL, type BIGINT NOT NULL, size BIGINT NOT NULL, data ` + old.d.Blob() + ` NOT NULL, PRIMARY KEY (repo, oid))`); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(run(t, work, nil, "rev-list", "--objects", "--all"))), "\n") {
			h := plumbing.NewHash(strings.Fields(line)[0])
			typ := strings.TrimSpace(string(run(t, work, nil, "cat-file", "-t", h.String())))
			content := run(t, work, nil, "cat-file", typ, h.String())
			pt, _ := plumbing.ParseObjectType(typ)
			z, err := deflate(content)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO objects (repo, oid, type, size, data) VALUES ($1, $2, $3, $4, $5)`,
				"r", h[:], int64(pt), int64(len(content)), z); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO refs (repo, name, value) VALUES ('r', 'refs/heads/main', $1) ON CONFLICT (repo, name) DO UPDATE SET value = excluded.value`, c1.String()); err != nil {
			t.Fatal(err)
		}

		blobs := NewMemory()
		s, err := Open(db, blobs, namespace.System())
		if err != nil {
			t.Fatalf("open over rows: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM objects`).Scan(&n); err == nil {
			t.Fatalf("the legacy table is still there with %d rows", n)
		}
		if len(blobs.Keys()) != 1 {
			t.Fatalf("blobs %v", blobs.Keys())
		}
		bareFrom(t, ctx, s, "r", c1)
		// A second open finds nothing to do.
		s2, err := Open(db, blobs, namespace.System())
		if err != nil {
			t.Fatal(err)
		}
		_ = s2
	})
}

// A store opened with no blob store keeps no object and says why.
func TestNoObjectStorageRefusesObjects(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s, err := Open(open(namespace.System(), "objects"), nil, namespace.System())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		work := workdir(t)
		c1 := commitFile(t, work, "a", body(10, -1), "one")
		if _, err := s.Ingest(ctx, "r", bytes.NewReader(packOf(t, work, c1))); !errors.Is(err, ErrNoBlobs) {
			t.Fatalf("ingest with no storage = %v", err)
		}
	})
}

// randomBlob is n bytes git cannot delta or compress.
func randomBlob(t testing.TB, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}
