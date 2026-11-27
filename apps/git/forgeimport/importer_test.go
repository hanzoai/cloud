package forgeimport

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/namespace"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// forge is a real git server (git http-backend) over a directory of bare repositories,
// with basic auth.
func forge(t *testing.T, root string) *httptest.Server {
	t.Helper()
	exec, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("no git")
	}
	h := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(exec)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pw, ok := r.BasicAuth(); !ok || pw != "secret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// source makes a bare repository owner/name with history, a branch, an annotated tag
// and a pull ref, and returns a working clone to push more from.
func source(t *testing.T, root, owner, name string) string {
	t.Helper()
	bare := filepath.Join(root, owner, name+".git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, bare, "init", "--bare", "-b", "main", ".")
	work := filepath.Join(t.TempDir(), "w")
	git(t, filepath.Dir(work), "clone", "-q", bare, work)
	git(t, work, "checkout", "-q", "-b", "main")
	for i := range 6 {
		if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte(strings.Repeat("line "+string(rune('a'+i))+"\n", 400)), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "add", ".")
		git(t, work, "commit", "-q", "-m", "c")
	}
	git(t, work, "branch", "feature")
	git(t, work, "tag", "-a", "v1", "-m", "v1")
	git(t, work, "push", "-q", "origin", "main", "feature", "v1")
	git(t, bare, "update-ref", "refs/pull/1/head", git(t, work, "rev-parse", "HEAD"))
	return work
}

func harness(t *testing.T, open sqltest.Open, srv *httptest.Server, workers int) (*Importer, *objects.Memory, func(org string) *objects.Store) {
	t.Helper()
	ctx := context.Background()
	reg, err := OpenRegistry(ctx, open(namespace.System(), "gitimport"))
	if err != nil {
		t.Fatal(err)
	}
	blobs := objects.NewMemory()
	stores := map[string]*objects.Store{}
	var mu sync.Mutex
	get := func(org string) *objects.Store {
		mu.Lock()
		defer mu.Unlock()
		if s, ok := stores[org]; ok {
			return s
		}
		ns := namespace.MustOrgProject(org, "")
		s, err := objects.Open(open(ns, "objects"), blobs, ns)
		if err != nil {
			t.Fatal(err)
		}
		stores[org] = s
		return s
	}
	im, err := New(Config{
		Registry: reg,
		Stores:   func(org string) (*objects.Store, error) { return get(org), nil },
		Org:      func(owner string) string { return map[string]string{"hanzoai": "hanzo", "hanzo-inc": "hanzo"}[owner] },
		Remote: func(owner, repo string) *Remote {
			return &Remote{URL: srv.URL + "/" + owner + "/" + repo + ".git", User: "hanzo", Password: "secret"}
		},
		Workers: workers,
		Resync:  true,
		Verify:  Verify{StrictMax: 1 << 30},
		Holder:  "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	return im, blobs, get
}

func TestImportCopiesVerifiesAndResyncs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		root := t.TempDir()
		work := source(t, root, "hanzoai", "alpha")
		source(t, root, "hanzo-inc", "alpha") // same name, other namespace, same org
		srv := forge(t, root)
		im, _, stores := harness(t, open, srv, 2)
		if err := im.Register(ctx, []Item{{"hanzoai", "alpha", 10}, {"hanzo-inc", "alpha", 20}, {"hanzoai", "gone", 30}}); err != nil {
			t.Fatal(err)
		}
		sum, err := im.Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if sum.Done != 2 || sum.Failed != 1 {
			t.Fatalf("summary = %+v", sum)
		}
		row, _ := im.c.Registry.Get(ctx, "hanzoai", "alpha")
		if row.State != Done || row.Org != "hanzo" || row.StoreID != "hanzoai/alpha" || row.Refs != 4 || row.Verified == 0 ||
			row.VerifyHow != "refs+count+fsck" || row.Objects == 0 || row.LastSHA == "" {
			t.Fatalf("row = %+v", row)
		}
		gone, _ := im.c.Registry.Get(ctx, "hanzoai", "gone")
		if gone.State != Failed || gone.Attempts != 1 || gone.Error == "" {
			t.Fatalf("gone = %+v", gone)
		}
		if strings.Contains(gone.Error, "secret") {
			t.Fatal("the credential is in the error")
		}
		store := stores("hanzo")
		for _, id := range []string{"hanzoai/alpha", "hanzo-inc/alpha"} {
			if ok, _ := store.Has(ctx, id); !ok {
				t.Fatalf("%s is not in the store", id)
			}
		}

		// Nothing moved: nothing is fetched.
		sum, _ = im.Run(ctx)
		if sum.Unchanged != 2 || sum.Done != 0 {
			t.Fatalf("second run = %+v", sum)
		}

		// The forge moves: a new commit on main, a deleted branch, a new tag.
		if err := os.WriteFile(filepath.Join(work, "g.txt"), []byte(strings.Repeat("new\n", 500)), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, work, "add", ".")
		git(t, work, "commit", "-q", "-m", "more")
		git(t, work, "tag", "v2")
		git(t, work, "push", "-q", "origin", "main", "v2", ":feature")
		before := row.Objects
		sum, _ = im.Run(ctx)
		if sum.Done != 1 || sum.Unchanged != 1 {
			t.Fatalf("resync = %+v", sum)
		}
		row, _ = im.c.Registry.Get(ctx, "hanzoai", "alpha")
		if row.State != Done || row.Refs != 4 || row.Objects <= before || row.VerifyHow != "refs+fsck" {
			t.Fatalf("after resync: %+v (was %d objects)", row, before)
		}
		head, _ := store.Repo(ctx, "hanzoai/alpha").Reference("refs/heads/main")
		if head.Hash().String() != git(t, work, "rev-parse", "HEAD") {
			t.Fatal("main did not move")
		}
		if _, err := store.Repo(ctx, "hanzoai/alpha").Reference("refs/heads/feature"); err == nil {
			t.Fatal("the deleted branch is still there")
		}
	})
}

func TestOneRunnerHoldsTheLease(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		root := t.TempDir()
		srv := forge(t, root)
		im, _, _ := harness(t, open, srv, 1)
		reg := im.c.Registry
		if ok, err := reg.Lease(ctx, leaseName, "other", time.Minute); err != nil || !ok {
			t.Fatalf("lease = %v, %v", ok, err)
		}
		if _, err := im.Run(ctx); err != ErrLeased {
			t.Fatalf("run = %v, want ErrLeased", err)
		}
		reg.Release(ctx, leaseName, "other")
		if _, err := im.Run(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAnEmptyRepositoryAndABrokenOneAreRecorded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ctx := context.Background()
		root := t.TempDir()
		bare := filepath.Join(root, "hanzoai", "empty.git")
		_ = os.MkdirAll(bare, 0o755)
		git(t, bare, "init", "--bare", "-b", "main", ".")
		srv := forge(t, root)
		im, _, stores := harness(t, open, srv, 1)
		_ = im.Register(ctx, []Item{{"hanzoai", "empty", 1}})
		sum, err := im.Run(ctx)
		if err != nil || sum.Done != 1 {
			t.Fatalf("run = %+v, %v", sum, err)
		}
		if ok, _ := stores("hanzo").Has(ctx, "hanzoai/empty"); !ok {
			t.Fatal("the empty repository is not in the store")
		}
	})
}
