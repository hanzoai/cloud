package git

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// store_git_test.go proves a repository kept in the objects store is served by the real
// smart-HTTP handler to the real git CLI, on both engines, with no directory behind it.

// work is a working tree with a committer, on main.
func work(t *testing.T) string {
	t.Helper()
	w := t.TempDir()
	gitRun(t, w, "init", "-q", "-b", "main")
	return w
}

func commitFile(t *testing.T, w, name, content, msg string) string {
	t.Helper()
	p := filepath.Join(w, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, w, "add", "-A")
	gitRun(t, w, "commit", "-q", "-m", msg)
	return gitOut(t, w, "rev-parse", "HEAD")
}

// pushed sends w's refspecs to org/name through the handler, as a person would.
func pushed(t *testing.T, app *zip.App, org, name, w string, refspecs ...string) {
	t.Helper()
	base := liveServer(t, app)
	args := append(orgHeaderArgs(org), "push", "-q", base+"/v1/git/"+org+"/"+name+".git")
	gitRun(t, w, append(args, refspecs...)...)
}

// seeded writes the branches and tags of w, and every object, into org/name in the store
// as a push from a local path did: no request, so no body limit and no reactor.
func seeded(t *testing.T, org, name, w string) {
	t.Helper()
	ctx := context.Background()
	s := mounted.Load()
	meta, err := storeFor(s, org)
	if err != nil {
		t.Fatal(err)
	}
	r, err := meta.Get(ctx, org, "", name)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := objectsFor(s, org)
	if err != nil {
		t.Fatal(err)
	}
	src, err := gogit.PlainOpen(w)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := objs.Begin(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	it, err := src.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		t.Fatal(err)
	}
	if err := it.ForEach(func(o plumbing.EncodedObject) error { _, err := tx.SetEncodedObject(o); return err }); err != nil {
		t.Fatal(err)
	}
	refs, err := src.Storer.IterReferences()
	if err != nil {
		t.Fatal(err)
	}
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference && (ref.Name().IsBranch() || ref.Name().IsTag()) {
			return tx.SetReference(ref)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// rowOf is the metadata row of org/name.
func rowOf(t *testing.T, org, name string) Repo {
	t.Helper()
	meta, err := storeFor(mounted.Load(), org)
	if err != nil {
		t.Fatal(err)
	}
	r, err := meta.Get(context.Background(), org, "", name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func createRepo(t *testing.T, app *zip.App, org, name string) {
	t.Helper()
	if code, b := do(t, app, "POST", "/v1/git/repos", org, map[string]any{"name": name}); code != 201 {
		t.Fatalf("create %s: %d %s", name, code, b)
	}
}

// noDirectory fails if repo has a directory under the data dir: a repository kept in the
// store has none.
func noDirectory(t *testing.T, org, name string) {
	t.Helper()
	if _, err := os.Stat(mounted.Load().State.storage.absRepoPath(org, "", name)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s/%s has a directory (%v); its history is meant to be rows", org, name, err)
	}
}

// Every kind of fetch a git client makes is answered from rows: clone, shallow clone,
// the incremental fetch whose negotiation finds what the client has, tags, a branch
// deleted, a branch forced, a commit fetched by name, and a partial clone's blob.
func TestTheStoreServesEveryKindOfFetch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		createRepo(t, app, "acme", "code")
		noDirectory(t, "acme", "code")
		url := base + "/v1/git/acme/code.git"
		hdr := orgHeaderArgs("acme")

		w := work(t)
		c1 := commitFile(t, w, "README.md", "one\n", "first")
		commitFile(t, w, "src/a/b.go", "package b\n", "second")
		c3 := commitFile(t, w, "src/a/c.go", "package c\n", "third")
		gitRun(t, w, "tag", "-a", "v1", "-m", "release one")
		gitRun(t, w, "tag", "light")
		gitRun(t, w, "checkout", "-q", "-b", "dev", c1)
		d1 := commitFile(t, w, "dev.txt", "dev\n", "on dev")
		gitRun(t, w, "checkout", "-q", "main")
		gitRun(t, w, "remote", "add", "origin", url)
		gitRun(t, w, append(hdr, "push", "origin", "--all")...)
		gitRun(t, w, append(hdr, "push", "origin", "--tags")...)
		noDirectory(t, "acme", "code")

		// What the server advertises is what was pushed.
		ls := gitOut(t, "", append(hdr, "ls-remote", url)...)
		for _, want := range []string{c3 + "\tHEAD", c3 + "\trefs/heads/main", d1 + "\trefs/heads/dev", "refs/tags/v1\n", "refs/tags/v1^{}", c3 + "\trefs/tags/light"} {
			if !strings.Contains(ls+"\n", want) {
				t.Fatalf("ls-remote lacks %q:\n%s", want, ls)
			}
		}

		// A full clone has every branch and tag, and is sound.
		full := filepath.Join(t.TempDir(), "full")
		gitRun(t, "", append(hdr, "clone", "-q", url, full)...)
		if got := gitOut(t, full, "rev-parse", "HEAD"); got != c3 {
			t.Fatalf("cloned HEAD %s != %s", got, c3)
		}
		if got := gitOut(t, full, "rev-parse", "origin/dev"); got != d1 {
			t.Fatalf("origin/dev %s != %s", got, d1)
		}
		gitRun(t, full, "fsck", "--strict")
		if got := gitOut(t, full, "cat-file", "-t", "v1"); got != "tag" {
			t.Fatalf("v1 is a %s; want the annotated tag", got)
		}

		// A shallow clone is one commit, and sound.
		shallow := filepath.Join(t.TempDir(), "shallow")
		gitRun(t, "", append(hdr, "clone", "-q", "--depth", "1", url, shallow)...)
		if n := gitOut(t, shallow, "rev-list", "--count", "HEAD"); n != "1" {
			t.Fatalf("a depth 1 clone has %s commits", n)
		}
		gitRun(t, shallow, "fsck", "--strict")
		if got := gitOut(t, shallow, "show", "HEAD:src/a/c.go"); got != "package c" {
			t.Fatalf("shallow blob = %q", got)
		}
		// Deepening it brings the rest.
		gitRun(t, shallow, append(hdr, "fetch", "-q", "--unshallow")...)
		if n := gitOut(t, shallow, "rev-list", "--count", "HEAD"); n != "3" {
			t.Fatalf("an unshallowed clone has %s commits; want 3", n)
		}
		gitRun(t, shallow, "fsck", "--strict")

		// The incremental fetch: the client has history, the server finds where it ends.
		c4 := commitFile(t, w, "src/a/d.go", "package d\n", "fourth")
		commitFile(t, w, "README.md", "one, again\n", "fifth")
		c5 := gitOut(t, w, "rev-parse", "HEAD")
		gitRun(t, w, append(hdr, "push", "origin", "main")...)
		gitRun(t, full, append(hdr, "pull", "-q", "--ff-only")...)
		if got := gitOut(t, full, "rev-parse", "HEAD"); got != c5 {
			t.Fatalf("after pull HEAD %s != %s", got, c5)
		}
		if got := gitOut(t, full, "rev-parse", "HEAD~1"); got != c4 {
			t.Fatalf("HEAD~1 %s != %s", got, c4)
		}
		gitRun(t, full, "fsck", "--strict")

		// Up to date is a fetch that has nothing to send.
		gitRun(t, full, append(hdr, "fetch", "-q")...)

		// A commit fetched by its name into a repository that has nothing.
		byname := t.TempDir()
		gitRun(t, byname, "init", "-q")
		gitRun(t, byname, append(hdr, "fetch", "-q", url, c4)...)
		if got := gitOut(t, byname, "rev-parse", "FETCH_HEAD"); got != c4 {
			t.Fatalf("FETCH_HEAD %s != %s", got, c4)
		}

		// A branch is deleted, and another forced to a rewritten history.
		gitRun(t, w, append(hdr, "push", "origin", ":dev")...)
		if ls := gitOut(t, "", append(hdr, "ls-remote", "--heads", url)...); strings.Contains(ls, "refs/heads/dev") {
			t.Fatalf("dev survived its deletion:\n%s", ls)
		}
		gitRun(t, w, "checkout", "-q", "-b", "topic", c1)
		commitFile(t, w, "t.txt", "t1\n", "topic one")
		gitRun(t, w, append(hdr, "push", "origin", "topic")...)
		gitRun(t, w, "reset", "-q", "--hard", c1)
		t2 := commitFile(t, w, "t.txt", "t2\n", "topic rewritten")
		gitRun(t, w, append(hdr, "push", "--force", "origin", "topic")...)
		if got := gitOut(t, "", append(hdr, "ls-remote", url, "refs/heads/topic")...); !strings.HasPrefix(got, t2) {
			t.Fatalf("topic = %q; want it forced to %s", got, t2)
		}

		// A partial clone gets the commits and trees, and the blob when it reads it.
		part := filepath.Join(t.TempDir(), "part")
		gitRun(t, "", append(hdr, "clone", "-q", "--filter=blob:none", url, part)...)
		if got := gitOut(t, part, "config", "--get", "remote.origin.promisor"); got != "true" {
			t.Fatalf("partial clone promisor = %q", got)
		}
		if got := gitOut(t, part, "show", "HEAD:src/a/b.go"); got != "package b" {
			t.Fatalf("lazily fetched blob = %q", got)
		}

		noDirectory(t, "acme", "code")
	})
}

// mountReplica mounts another instance of the plugin over the same data dir, and so over the
// same database, the way a second pod does: its own handler, its own pools.
func mountReplica(t *testing.T, dataDir string) *zip.App {
	t.Helper()
	first := mounted.Load()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	t.Setenv("CLOUD_DATA_DIR", dataDir)
	t.Setenv("CLOUD_DOMAIN", "api.hanzo.test")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	if first != nil {
		t.Cleanup(func() {
			first.State.end()
			first.State.arriving.Wait()
			first.State.vault.sweeping.Wait()
			_ = first.State.stores.CloseAll()
			_ = first.State.objects.CloseAll()
		})
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// barrier lets n requests through together.
type barrier struct {
	mu      sync.Mutex
	n       int
	arrived int
	open    chan struct{}
}

func (b *barrier) wait() {
	b.mu.Lock()
	b.arrived++
	if b.arrived == b.n {
		close(b.open)
	}
	b.mu.Unlock()
	<-b.open
}

// gate fronts a replica and holds each push's pack until the barrier opens, so two clients
// that both read the ref at the same commit both send what they built on it.
func gate(t *testing.T, target string, b *barrier) string {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			b.wait()
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Two replicas, two handlers and two pools on one database, are pushed the same ref at
// the same moment by two clients that read it at the same commit. One push lands; the
// other is rejected, nothing of it is kept, and the ref is the winner's.
func TestTwoReplicasPushingOneRefOnlyOneWins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		a := mountReplica(t, dir)
		// A second pod exists where the database is shared. A SQLite file is one pod's:
		// two handles on it are two copies, so there the two clients meet in one handler.
		b := a
		if locus.DSN() != "" {
			b = mountReplica(t, dir)
		}
		createRepo(t, a, "acme", "race")
		hdr := orgHeaderArgs("acme")

		seed := work(t)
		c0 := commitFile(t, seed, "f.txt", "base\n", "base")
		gitRun(t, seed, "remote", "add", "origin", liveServer(t, a)+"/v1/git/acme/race.git")
		gitRun(t, seed, append(hdr, "push", "origin", "main")...)

		bar := &barrier{n: 2, open: make(chan struct{})}
		urlA := gate(t, liveServer(t, a), bar) + "/v1/git/acme/race.git"
		urlB := gate(t, liveServer(t, b), bar) + "/v1/git/acme/race.git"
		clones := make([]string, 2)
		built := make([]string, 2)
		for i, u := range []string{urlA, urlB} {
			clones[i] = filepath.Join(t.TempDir(), fmt.Sprint("c", i))
			gitRun(t, "", append(hdr, "clone", "-q", u, clones[i])...)
			built[i] = commitFile(t, clones[i], fmt.Sprintf("by%d.txt", i), fmt.Sprintf("writer %d\n", i), fmt.Sprintf("writer %d", i))
		}

		type result struct {
			out string
			err error
		}
		results := make([]result, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cmd := gitTestCmd(clones[i], append(hdr, "push", "origin", "main")...)
				out, err := cmd.CombinedOutput()
				results[i] = result{string(out), err}
			}()
		}
		wg.Wait()

		won, lost := -1, -1
		for i, r := range results {
			switch {
			case r.err == nil && won < 0:
				won = i
			case r.err != nil && lost < 0:
				lost = i
			default:
				t.Fatalf("both pushes %v; want exactly one to land\n0: %v\n%s\n1: %v\n%s", map[bool]string{true: "failed", false: "landed"}[r.err != nil],
					results[0].err, results[0].out, results[1].err, results[1].out)
			}
		}
		if won < 0 || lost < 0 {
			t.Fatalf("want one winner and one loser:\n0: %v\n%s\n1: %v\n%s", results[0].err, results[0].out, results[1].err, results[1].out)
		}
		if !strings.Contains(results[lost].out, "rejected") || !strings.Contains(results[lost].out, "non-fast-forward") {
			t.Fatalf("the loser was not told its push was a non-fast-forward:\n%s", results[lost].out)
		}

		// The ref is the winner's, on either replica, and the loser's commit is nowhere.
		got := gitOut(t, "", append(hdr, "ls-remote", liveServer(t, b)+"/v1/git/acme/race.git", "refs/heads/main")...)
		if !strings.HasPrefix(got, built[won]) {
			t.Fatalf("main = %q; want the winner's %s (c0 %s)", got, built[won], c0)
		}
		store, err := storeFor(mounted.Load(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		r, err := store.Get(context.Background(), "acme", "", "race")
		if err != nil {
			t.Fatal(err)
		}
		objs, err := objectsFor(mounted.Load(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		repo := objs.Repo(context.Background(), r.ID)
		if err := repo.HasEncodedObject(plumbing.NewHash(built[lost])); !errors.Is(err, plumbing.ErrObjectNotFound) {
			t.Fatalf("the rejected push's commit is in the store (%v)", err)
		}
		if err := repo.HasEncodedObject(plumbing.NewHash(built[won])); err != nil {
			t.Fatalf("the winning push's commit is not in the store: %v", err)
		}
	})
}

// legacy makes a repository as a binary from before the store made it: the metadata row,
// a bare directory, history pushed into the directory with the CLI, part of it packed and
// part loose, two branches and two tags.
func legacy(t *testing.T, org, name string) (r Repo, dir, tip string) {
	t.Helper()
	s := mounted.Load()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatal(err)
	}
	r = Repo{ID: "repo_" + name, Org: org, Name: name, DefaultBranch: "main", CreatedAt: 1, UpdatedAt: 1}
	if err := store.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := s.State.storage.initBare(org, "", name, "main"); err != nil {
		t.Fatal(err)
	}
	dir = s.State.storage.absRepoPath(org, "", name)
	w := work(t)
	for i := range 30 {
		commitFile(t, w, fmt.Sprintf("d%d/f%d.txt", i%5, i), strings.Repeat(fmt.Sprintf("line %d\n", i), 50), fmt.Sprintf("c%d", i))
	}
	gitRun(t, w, "tag", "-a", "v1", "-m", "one")
	gitRun(t, w, "checkout", "-q", "-b", "side")
	commitFile(t, w, "side.txt", "side\n", "side")
	gitRun(t, w, "checkout", "-q", "main")
	gitRun(t, w, "push", "-q", dir, "--all")
	gitRun(t, w, "push", "-q", dir, "--tags")
	gitRun(t, "", "--git-dir="+dir, "repack", "-a", "-d", "-q")
	tip = commitFile(t, w, "late.txt", "after the repack, so loose\n", "late")
	gitRun(t, w, "tag", "light")
	gitRun(t, w, "push", "-q", dir, "main")
	gitRun(t, w, "push", "-q", dir, "--tags")
	return r, dir, tip
}

func gitCat(t *testing.T, dir string) []string {
	t.Helper()
	out := gitOut(t, "", "--git-dir="+dir, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	lines := strings.Split(out, "\n")
	sort.Strings(lines)
	return lines
}

// A repository left as a directory is brought into the store on its first use, equal: the
// same objects, each hashing to its name, the same refs and HEAD, and the directory is left
// read-only and no longer written. Afterwards it is served from rows.
func TestALegacyDirectoryIsImportedEqual(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		r, dir, tip := legacy(t, "acme", "old")
		hdr := orgHeaderArgs("acme")
		ctx := context.Background()
		objs, err := objectsFor(mounted.Load(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := objs.Has(ctx, r.ID); ok {
			t.Fatal("the store held the repository before it was used")
		}
		wantObjects := gitCat(t, dir)
		wantRefs := gitOut(t, "", "--git-dir="+dir, "for-each-ref", "--format=%(objectname) %(refname)")
		wantHead := gitOut(t, "", "--git-dir="+dir, "symbolic-ref", "HEAD")
		if len(wantObjects) < 100 {
			t.Fatalf("the legacy repository is too small to mean anything: %d objects", len(wantObjects))
		}

		// The first use is a clone through the handler.
		clone := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", append(hdr, "clone", "-q", base+"/v1/git/acme/old.git", clone)...)
		if got := gitOut(t, clone, "rev-parse", "HEAD"); got != tip {
			t.Fatalf("cloned HEAD %s != %s", got, tip)
		}
		gitRun(t, clone, "fsck", "--strict")

		if ok, _ := objs.Has(ctx, r.ID); !ok {
			t.Fatal("the first use did not bring the repository into the store")
		}
		repo := objs.Repo(ctx, r.ID)

		// Every object, by name, type and size, and each one hashes to its name.
		hashes, err := repo.Hashes(plumbing.AnyObject)
		if err != nil {
			t.Fatal(err)
		}
		var gotObjects []string
		for _, h := range hashes {
			o, err := repo.EncodedObject(plumbing.AnyObject, h)
			if err != nil {
				t.Fatal(err)
			}
			rd, _ := o.Reader()
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(rd)
			_ = rd.Close()
			if plumbing.ComputeHash(o.Type(), buf.Bytes()) != h {
				t.Fatalf("object %s does not hash to its name", h)
			}
			gotObjects = append(gotObjects, fmt.Sprintf("%s %s %d", h, o.Type(), o.Size()))
		}
		sort.Strings(gotObjects)
		if strings.Join(gotObjects, "\n") != strings.Join(wantObjects, "\n") {
			t.Fatalf("the store holds %d objects and the directory %d, and they differ", len(gotObjects), len(wantObjects))
		}

		// Every ref, and HEAD.
		refs, err := repo.References()
		if err != nil {
			t.Fatal(err)
		}
		var gotRefs []string
		for _, ref := range refs {
			if ref.Name() == plumbing.HEAD {
				if ref.Target().String() != wantHead {
					t.Fatalf("HEAD -> %s; want %s", ref.Target(), wantHead)
				}
				continue
			}
			gotRefs = append(gotRefs, ref.Hash().String()+" "+ref.Name().String())
		}
		if strings.Join(gotRefs, "\n") != wantRefs {
			t.Fatalf("refs differ:\nstore:\n%s\ndirectory:\n%s", strings.Join(gotRefs, "\n"), wantRefs)
		}

		// The directory is read-only and untouched by what follows.
		err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if info, _ := d.Info(); info.Mode().Perm()&0o222 != 0 {
				return fmt.Errorf("%s is writable (%v)", p, info.Mode())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		commitFile(t, clone, "after.txt", "after the import\n", "after")
		after := gitOut(t, clone, "rev-parse", "HEAD")
		gitRun(t, clone, append(hdr, "push", "-q", "origin", "main")...)
		if got := gitOut(t, "", "--git-dir="+dir, "rev-parse", "main"); got != tip {
			t.Fatalf("the directory's main moved to %s; it is a dead copy and was written", got)
		}
		if got := gitOut(t, "", append(hdr, "ls-remote", base+"/v1/git/acme/old.git", "refs/heads/main")...); !strings.HasPrefix(got, after) {
			t.Fatalf("main = %q; want the push to land in the store at %s", got, after)
		}

		// Deleting the repository removes its rows and the read-only directory.
		if code, b := do(t, app, "DELETE", "/v1/git/repos/old", "acme", nil); code != 204 {
			t.Fatalf("delete: %d %s", code, b)
		}
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the directory survived the delete: %v", err)
		}
		if ok, _ := objs.Has(ctx, r.ID); ok {
			t.Fatal("the rows survived the delete")
		}
	})
}

// A directory that cannot be copied equal stays a directory: the import fails, nothing is
// kept in the store, the directory is left writable, and it is served by the CLI as it was.
func TestADirectoryThatCannotBeImportedStaysADirectory(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		r, dir, tip := legacy(t, "acme", "damaged")
		hdr := orgHeaderArgs("acme")

		// An unreachable loose object that is not an object: git never reads it, and a
		// copy that reads every object cannot.
		bad := filepath.Join(dir, "objects", "ab", strings.Repeat("c", 38))
		if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bad, []byte("this is not a zlib stream"), 0o644); err != nil {
			t.Fatal(err)
		}

		clone := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", append(hdr, "clone", "-q", base+"/v1/git/acme/damaged.git", clone)...)
		if got := gitOut(t, clone, "rev-parse", "HEAD"); got != tip {
			t.Fatalf("cloned HEAD %s != %s", got, tip)
		}
		objs, err := objectsFor(mounted.Load(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := objs.Has(context.Background(), r.ID); ok {
			t.Fatal("a copy that failed its checks was kept")
		}
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("the directory was frozen though it was not copied: %v %v", info, err)
		}

		// It is served and written as a directory still.
		next := commitFile(t, clone, "more.txt", "more\n", "more")
		gitRun(t, clone, append(hdr, "push", "-q", "origin", "main")...)
		if got := gitOut(t, "", "--git-dir="+dir, "rev-parse", "main"); got != next {
			t.Fatalf("the directory's main = %s; want the push %s", got, next)
		}
		if ok, _ := objs.Has(context.Background(), r.ID); ok {
			t.Fatal("a push to a directory made rows")
		}
	})
}

// A repository of thousands of objects round-trips, and what it costs is logged.
func TestALargerRepositoryRoundTrips(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		createRepo(t, app, "acme", "big")
		hdr := orgHeaderArgs("acme")
		url := base + "/v1/git/acme/big.git"

		w := work(t)
		// Forty commits of thousands of loose objects start a background gc, which races the
		// next commit; the test is of the server, not of the client's housekeeping.
		gitRun(t, w, "config", "gc.auto", "0")
		for i := range 3000 {
			p := filepath.Join(w, fmt.Sprintf("pkg%02d/sub%02d/f%04d.go", i%40, i%7, i))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(fmt.Sprintf("package p\n// file %d\n%s", i, strings.Repeat("var x = 1\n", 20))), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitRun(t, w, "add", "-A")
		gitRun(t, w, "commit", "-q", "-m", "tree")
		for c := range 40 {
			for i := range 25 {
				n := (c*97 + i*131) % 3000
				p := filepath.Join(w, fmt.Sprintf("pkg%02d/sub%02d/f%04d.go", n%40, n%7, n))
				if err := os.WriteFile(p, []byte(fmt.Sprintf("package p\n// file %d rev %d\n", n, c)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gitRun(t, w, "commit", "-q", "-am", fmt.Sprintf("rev %d", c))
		}
		head := gitOut(t, w, "rev-parse", "HEAD")
		objects := len(strings.Split(gitOut(t, w, "rev-list", "--objects", "--all"), "\n"))
		gitRun(t, w, "remote", "add", "origin", url)

		start := time.Now()
		gitRun(t, w, append(hdr, "push", "-q", "origin", "main")...)
		pushed := time.Since(start)

		start = time.Now()
		dst := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", append(hdr, "clone", "-q", url, dst)...)
		cloned := time.Since(start)
		if got := gitOut(t, dst, "rev-parse", "HEAD"); got != head {
			t.Fatalf("cloned HEAD %s != %s", got, head)
		}
		gitRun(t, dst, "fsck", "--strict")

		commitFile(t, w, "pkg00/new.go", "package p\n", "one more")
		gitRun(t, w, append(hdr, "push", "-q", "origin", "main")...)
		start = time.Now()
		gitRun(t, dst, append(hdr, "pull", "-q", "--ff-only")...)
		fetched := time.Since(start)
		if got, want := gitOut(t, dst, "rev-parse", "HEAD"), gitOut(t, w, "rev-parse", "HEAD"); got != want {
			t.Fatalf("after pull HEAD %s != %s", got, want)
		}
		for _, d := range []time.Duration{pushed, cloned, fetched} {
			if d > 60*time.Second {
				t.Fatalf("push %v, clone %v, incremental fetch %v: one took too long", pushed, cloned, fetched)
			}
		}
		noDirectory(t, "acme", "big")

		// The same history as a directory an older binary left: its first use copies it in,
		// and then serves it.
		s := mounted.Load()
		old := Repo{ID: "repo_biglegacy", Org: "acme", Name: "biglegacy", DefaultBranch: "main", CreatedAt: 1, UpdatedAt: 1}
		meta, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if err := meta.Create(context.Background(), old); err != nil {
			t.Fatal(err)
		}
		if err := s.State.storage.initBare("acme", "", "biglegacy", "main"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.State.storage.thaw("acme", "", "biglegacy") }) // it is made read-only when copied in
		gitRun(t, w, "push", "-q", s.State.storage.absRepoPath("acme", "", "biglegacy"), "main")
		start = time.Now()
		gitRun(t, "", append(hdr, "clone", "-q", base+"/v1/git/acme/biglegacy.git", filepath.Join(t.TempDir(), "legacy"))...)
		imported := time.Since(start)
		if imported > 120*time.Second {
			t.Fatalf("copying %d objects in and cloning them took %v", objects, imported)
		}
		t.Logf("%d objects: push %v, clone %v, incremental fetch %v; a directory's first clone (copy in + clone) %v",
			objects, pushed.Round(time.Millisecond), cloned.Round(time.Millisecond), fetched.Round(time.Millisecond), imported.Round(time.Millisecond))
	})
}

// A client that goes away mid-clone ends the pack and gives its slot back.
func TestAFetchEndsWhenTheClientGoesAway(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		createRepo(t, app, "acme", "gone")
		w := work(t)
		for i := range 12 {
			blob := make([]byte, 1<<20)
			if _, err := rand.Read(blob); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(w, fmt.Sprintf("b%02d.bin", i)), blob, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitRun(t, w, "add", "-A")
		gitRun(t, w, "commit", "-q", "-m", "twelve megabytes that do not compress")
		tip := gitOut(t, w, "rev-parse", "HEAD")
		seeded(t, "acme", "gone", w)

		body := string(packetWrite("want "+tip+" side-band-64k\n")) + "0000" + string(packetWrite("done\n"))
		req, err := http.NewRequest(http.MethodPost, base+"/v1/git/acme/gone.git/git-upload-pack", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "u_acme")
		req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(resp.Body, make([]byte, 8192)); err != nil {
			t.Fatalf("read the start of the pack: %v", err)
		}
		_ = resp.Body.Close() // the client is gone

		deadline := time.Now().Add(10 * time.Second)
		for len(packSem) != 0 {
			if time.Now().After(deadline) {
				t.Fatal("the pack slot was not given back after the client went away")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// The sweep brings in every directory a host still holds, with nobody asking for them.
func TestTheSweepBringsInEveryDirectory(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountApp(t)
		s := mounted.Load()
		one, _, _ := legacy(t, "acme", "one")
		two, _, _ := legacy(t, "beta", "two")
		t.Cleanup(func() {
			s.State.storage.thaw("acme", "", "one")
			s.State.storage.thaw("beta", "", "two")
		})

		s.State.vault.sweep(s)

		for _, r := range []Repo{one, two} {
			objs, err := objectsFor(s, r.Org)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := objs.Has(context.Background(), r.ID); err != nil || !ok {
				t.Fatalf("%s/%s was not brought in by the sweep (%v)", r.Org, r.Name, err)
			}
		}
	})
}
