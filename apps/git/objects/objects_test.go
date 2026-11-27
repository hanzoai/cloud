package objects

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/namespace"
)

func store(t *testing.T, open sqltest.Open) *Store {
	t.Helper()
	s, _ := storeWith(t, open, NewMemory())
	return s
}

// storeWith opens a store over db with pack bytes in m.
func storeWith(t *testing.T, open sqltest.Open, m *Memory) (*Store, *Memory) {
	t.Helper()
	s, err := Open(open(namespace.System(), "objects"), m, namespace.System())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, m
}

func blob(t *testing.T, w storage.Storer, content string) plumbing.Hash {
	t.Helper()
	o := w.NewEncodedObject()
	o.SetType(plumbing.BlobObject)
	wr, _ := o.Writer()
	_, _ = wr.Write([]byte(content))
	_ = wr.Close()
	h, err := w.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func tree(t *testing.T, w storage.Storer, names map[string]plumbing.Hash) plumbing.Hash {
	t.Helper()
	tr := &object.Tree{}
	for n, h := range names {
		tr.Entries = append(tr.Entries, object.TreeEntry{Name: n, Mode: filemode.Regular, Hash: h})
	}
	// git orders entries by name; Encode does not.
	for i := range tr.Entries {
		for j := i + 1; j < len(tr.Entries); j++ {
			if tr.Entries[j].Name < tr.Entries[i].Name {
				tr.Entries[i], tr.Entries[j] = tr.Entries[j], tr.Entries[i]
			}
		}
	}
	o := w.NewEncodedObject()
	if err := tr.Encode(o); err != nil {
		t.Fatal(err)
	}
	h, err := w.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func commit(t *testing.T, w storage.Storer, root plumbing.Hash, msg string, parents ...plumbing.Hash) plumbing.Hash {
	t.Helper()
	sig := object.Signature{Name: "t", Email: "t@t.io", When: time.Unix(1_700_000_000, 0).UTC()}
	c := &object.Commit{Author: sig, Committer: sig, Message: msg, TreeHash: root, ParentHashes: parents}
	o := w.NewEncodedObject()
	if err := c.Encode(o); err != nil {
		t.Fatal(err)
	}
	h, err := w.SetEncodedObject(o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A repository is made, written in one transaction, and read back through go-git as a
// directory would be.
func TestObjectsRoundTrip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := store(t, open)
		if err := s.Create(ctx, "r1", "main"); err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, "r1", "main"); !errors.Is(err, ErrExists) {
			t.Fatalf("second create = %v; want ErrExists", err)
		}
		tx, err := s.Begin(ctx, "r1")
		if err != nil {
			t.Fatal(err)
		}
		b := blob(t, tx, "hello\n")
		root := tree(t, tx, map[string]plumbing.Hash{"a.txt": b})
		c1 := commit(t, tx, root, "one")
		c2 := commit(t, tx, root, "two", c1)
		ref := plumbing.NewHashReference("refs/heads/main", c2)
		if err := tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{ref.Name(): c2}); err != nil {
			t.Fatalf("a whole push is not connected: %v", err)
		}
		if err := tx.CreateReference(ref); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		r := s.Repo(ctx, "r1")
		got, err := r.Reference("refs/heads/main")
		if err != nil || got.Hash() != c2 {
			t.Fatalf("ref = %v, %v; want %s", got, err, c2)
		}
		head, err := r.Reference(plumbing.HEAD)
		if err != nil || head.Type() != plumbing.SymbolicReference || head.Target() != "refs/heads/main" {
			t.Fatalf("HEAD = %v, %v", head, err)
		}
		cm, err := object.GetCommit(r, c2)
		if err != nil || cm.Message != "two" {
			t.Fatalf("commit = %v, %v", cm, err)
		}
		tr, err := cm.Tree()
		if err != nil {
			t.Fatal(err)
		}
		f, err := tr.File("a.txt")
		if err != nil {
			t.Fatal(err)
		}
		if body, _ := f.Contents(); body != "hello\n" {
			t.Fatalf("blob = %q", body)
		}
		if n, err := r.Count(); err != nil || n != 4 {
			t.Fatalf("objects = %d, %v; want 4", n, err)
		}
		if err := r.HasEncodedObject(b); err != nil {
			t.Fatal(err)
		}
		if _, err := r.EncodedObject(plumbing.TreeObject, b); !errors.Is(err, plumbing.ErrObjectNotFound) {
			t.Fatalf("a blob read as a tree = %v", err)
		}
		if size, err := r.EncodedObjectSize(b); err != nil || size != 6 {
			t.Fatalf("size = %d, %v", size, err)
		}
		n, err := s.Size(ctx, "r1")
		if err != nil || n <= 0 {
			t.Fatalf("repo size = %d, %v; want a counter that moved", n, err)
		}
		it, err := r.IterEncodedObjects(plumbing.CommitObject)
		if err != nil {
			t.Fatal(err)
		}
		var commits int
		_ = it.ForEach(func(plumbing.EncodedObject) error { commits++; return nil })
		if commits != 2 {
			t.Fatalf("commits = %d; want 2", commits)
		}

		// A transaction rolled back keeps nothing, objects included.
		tx, _ = s.Begin(ctx, "r1")
		blob(t, tx, "never kept")
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.Repo(ctx, "r1").Count(); n != 4 {
			t.Fatalf("after rollback: %d objects; want 4", n)
		}

		// A repository that is not there is not begun, and not found.
		if _, err := s.Begin(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("begin of a missing repo = %v", err)
		}
		if err := s.Remove(ctx, "r1"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.Has(ctx, "r1"); ok {
			t.Fatal("r1 survived Remove")
		}
		if n, _ := s.Repo(ctx, "r1").Count(); n != 0 {
			t.Fatalf("r1 left %d objects", n)
		}
	})
}

// Two writers that read the same ref value cannot both move it, on either engine, with
// sixteen at once.
func TestOnlyOneWriterMovesARef(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := store(t, open)
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		r := s.Repo(ctx, "r")
		old := plumbing.NewHashReference("refs/heads/main", plumbing.NewHash("1111111111111111111111111111111111111111"))
		if err := r.SetReference(old); err != nil {
			t.Fatal(err)
		}
		var won atomic.Int32
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				next := plumbing.NewHashReference("refs/heads/main", plumbing.NewHash(fmt.Sprintf("%040x", i+2)))
				switch err := s.Repo(ctx, "r").CheckAndSetReference(next, old); {
				case err == nil:
					won.Add(1)
				case errors.Is(err, storage.ErrReferenceHasChanged):
				default:
					t.Errorf("writer %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d writers moved one ref; want exactly one", won.Load())
		}
		// Create and delete compare too.
		if err := r.CreateReference(old); !errors.Is(err, storage.ErrReferenceHasChanged) {
			t.Fatalf("create over a ref = %v", err)
		}
		cur, _ := r.Reference("refs/heads/main")
		if err := r.DeleteReference(old); !errors.Is(err, storage.ErrReferenceHasChanged) {
			t.Fatalf("delete at a stale value = %v", err)
		}
		if err := r.DeleteReference(cur); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reference("refs/heads/main"); !errors.Is(err, plumbing.ErrReferenceNotFound) {
			t.Fatalf("after delete: %v", err)
		}
	})
}

// A push that names an object it did not send, and the repository lacks, is refused whole.
func TestAPushWithAHoleIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := store(t, open)
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		tx, err := s.Begin(ctx, "r")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		ghost := plumbing.NewHash("2222222222222222222222222222222222222222")
		root := tree(t, tx, map[string]plumbing.Hash{"x": ghost})
		c := commit(t, tx, root, "holey")
		err = tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{"refs/heads/main": c})
		if !errors.Is(err, ErrMissing) {
			t.Fatalf("Connected = %v; want ErrMissing", err)
		}
		// A ref that names a tree is not a branch.
		err = tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{"refs/heads/x": root})
		if err == nil {
			t.Fatal("a branch at a tree was connected")
		}
		// A tip that is not in the repository.
		if err := tx.Connected(map[plumbing.ReferenceName]plumbing.Hash{"refs/tags/t": ghost}); !errors.Is(err, ErrMissing) {
			t.Fatalf("a tip that is nowhere = %v", err)
		}
	})
}

// A pack written from the store is one git reads: index-pack accepts it, fsck finds
// nothing wrong, and every object is the one that went in.
func TestThePackIsOneGitReads(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := store(t, open)
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		tx, err := s.Begin(ctx, "r")
		if err != nil {
			t.Fatal(err)
		}
		var oids []plumbing.Hash
		files := map[string]plumbing.Hash{}
		big := bytes.Repeat([]byte("0123456789abcdef"), 40_000) // 640 KB, past one read group's budget with its neighbours
		for i := range 300 {
			body := fmt.Sprintf("file %d\n", i)
			if i%100 == 0 {
				body += string(big) + fmt.Sprint(i)
			}
			h := blob(t, tx, body)
			files[fmt.Sprintf("f%03d", i)] = h
			oids = append(oids, h)
		}
		root := tree(t, tx, files)
		c := commit(t, tx, root, "many")
		oids = append(oids, root, c)
		if err := tx.CreateReference(plumbing.NewHashReference("refs/heads/main", c)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		var pack bytes.Buffer
		if err := s.Repo(ctx, "r").Pack(ctx, &pack, oids); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		git := func(stdin io.Reader, args ...string) string {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			cmd.Stdin = stdin
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			return string(out)
		}
		git(nil, "init", "-q", "--bare", ".")
		git(&pack, "index-pack", "--stdin", "--strict")
		git(nil, "update-ref", "refs/heads/main", c.String())
		git(nil, "fsck", "--strict", "--no-dangling")
		if out := git(nil, "cat-file", "-p", files["f001"].String()); out != "file 1\n" {
			t.Fatalf("blob through git = %q", out)
		}
		if _, err := os.Stat(filepath.Join(dir, "objects", "pack")); err != nil {
			t.Fatal(err)
		}
		// A pack that names an object the store lacks is not written.
		err = s.Repo(ctx, "r").Pack(ctx, io.Discard, []plumbing.Hash{plumbing.NewHash("3333333333333333333333333333333333333333")})
		if !errors.Is(err, plumbing.ErrObjectNotFound) {
			t.Fatalf("pack of a missing object = %v", err)
		}
	})
}

// Shallow, config and index are kept, and an unset one reads as go-git expects.
func TestShallowConfigAndIndex(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := store(t, open)
		if err := s.Create(ctx, "r", "trunk"); err != nil {
			t.Fatal(err)
		}
		r := s.Repo(ctx, "r")
		cfg, err := r.Config()
		if err != nil || !cfg.Core.IsBare {
			t.Fatalf("config = %+v, %v; want a bare repository's", cfg, err)
		}
		cfg.Raw.Section("user").SetOption("name", "kept")
		if err := r.SetConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if cfg, _ = r.Config(); cfg.Raw.Section("user").Option("name") != "kept" {
			t.Fatal("config was not kept")
		}
		var _ *config.Config = cfg

		a, b := plumbing.NewHash("4444444444444444444444444444444444444444"), plumbing.NewHash("5555555555555555555555555555555555555555")
		if err := r.SetShallow([]plumbing.Hash{b, a}); err != nil {
			t.Fatal(err)
		}
		if sh, err := r.Shallow(); err != nil || len(sh) != 2 {
			t.Fatalf("shallow = %v, %v", sh, err)
		}
		if err := r.SetShallow(nil); err != nil {
			t.Fatal(err)
		}
		if sh, _ := r.Shallow(); len(sh) != 0 {
			t.Fatalf("shallow after clearing = %v", sh)
		}

		idx, err := r.Index()
		if err != nil || idx.Version != 2 || len(idx.Entries) != 0 {
			t.Fatalf("unset index = %+v, %v", idx, err)
		}
		idx.Entries = append(idx.Entries, &index.Entry{Name: "a", Hash: a, Mode: filemode.Regular})
		if err := r.SetIndex(idx); err != nil {
			t.Fatal(err)
		}
		if got, err := r.Index(); err != nil || len(got.Entries) != 1 || got.Entries[0].Hash != a {
			t.Fatalf("index = %+v, %v", got, err)
		}

		// A submodule's storer is a repository of its own, empty until written.
		m, err := r.Module("lib")
		if err != nil {
			t.Fatal(err)
		}
		if refs, _ := m.CountLooseRefs(); refs != 0 {
			t.Fatalf("a new module has %d refs", refs)
		}
		if err := r.SetReference(plumbing.NewHashReference("bad ref", a)); err == nil {
			t.Fatal("a ref name with a space was kept")
		}
	})
}

// An object over the limit is refused by name and nothing is kept.
func TestAnObjectOverTheLimitIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		s := store(t, open)
		if err := s.Create(ctx, "r", "main"); err != nil {
			t.Fatal(err)
		}
		o := &plumbing.MemoryObject{}
		o.SetType(plumbing.BlobObject)
		w, _ := o.Writer()
		_, _ = w.Write(make([]byte, MaxObject+1))
		if _, err := s.Repo(ctx, "r").SetEncodedObject(o); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("a %d byte blob = %v; want ErrTooLarge", MaxObject+1, err)
		}
		if n, _ := s.Repo(ctx, "r").Count(); n != 0 {
			t.Fatalf("the refused object left %d rows", n)
		}
	})
}
