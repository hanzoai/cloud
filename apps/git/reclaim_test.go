package git

// A linked repository lives in the objects store, filled from its host, and a replica
// keeps nothing of it on a disk. Proved with the real client against the real server.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// service reaches the mounted service.
func service(t *testing.T) *cloud.Service[state] {
	t.Helper()
	s := mounted.Load()
	if s == nil {
		t.Fatal("no mounted service")
	}
	return s
}

// linkedIn creates repo `name` in org `acme` linked to a real bare repo served
// over git smart-HTTP — the one route that records an origin.
func linkedIn(t *testing.T, app *zip.App, name, content string) (url, commit string) {
	t.Helper()
	root := t.TempDir()
	commit = gitSource(t, root, name, content, "")
	url = serveGitHTTP(t, root) + "/" + name + ".git"
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos/"+name+"/link", "acme",
		map[string]any{"source": url}); code != http.StatusOK {
		t.Fatalf("link %s: %d %s", name, code, b)
	}
	settle(t)
	return url, commit
}

// settle waits for every arrival a link started (link.go arrive).
func settle(t *testing.T) {
	t.Helper()
	service(t).State.arriving.Wait()
}

// A linked repository is read from the store: no directory is made for it, a clone gets
// the host's commit, and what is in the store is the host's HEAD.
func TestALinkedRepositoryLivesInTheStoreAndNotOnTheVolume(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		base := liveServer(t, app)
		_, commit := linkedIn(t, app, "code", "# linked\n")

		s := service(t)
		dst := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", append(orgHeaderArgs("acme"), "clone", "-q", base+"/v1/git/acme/code.git", dst)...)
		if got := gitOut(t, dst, "rev-parse", "HEAD"); got != commit {
			t.Fatalf("the clone gave %s, want %s", got, commit)
		}
		if s.State.storage.exists("acme", "", "code") {
			t.Fatal("a linked repository made a directory on the volume")
		}
		if _, err := os.Stat(s.State.storage.absRepoPath("acme", "", "code")); !os.IsNotExist(err) {
			t.Fatalf("stat: %v", err)
		}
		store, _ := storeFor(s, "acme")
		r, err := store.Get(context.Background(), "acme", "", "code")
		if err != nil {
			t.Fatal(err)
		}
		objs, err := objectsFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _ := objs.Has(context.Background(), r.ID); !ok {
			t.Fatal("the store holds no copy")
		}
		if b := headBranch(objs.Repo(context.Background(), r.ID)); b != "main" {
			t.Fatalf("HEAD names %q", b)
		}
	})
}

// A link records where its history is, and a repo created any other way records
// nothing.
func TestOnlyALinkRecordsAnOrigin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		url, _ := linkedIn(t, app, "fromupstream", "# up\n")
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
			map[string]any{"name": "native"}); code != http.StatusCreated {
			t.Fatalf("create: %d %s", code, b)
		}

		store, err := storeFor(service(t), "acme")
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(context.Background(), "acme", "", "fromupstream")
		if err != nil {
			t.Fatal(err)
		}
		if got.Origin != url {
			t.Fatalf("link origin = %q, want %q", got.Origin, url)
		}
		native, err := store.Get(context.Background(), "acme", "", "native")
		if err != nil {
			t.Fatal(err)
		}
		if native.Origin != "" {
			t.Fatalf("a natively created repo must record no origin, got %q", native.Origin)
		}
	})
}

// A credential in a source URL must not reach the row.
func TestARecordedOriginCarriesNoCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		root := t.TempDir()
		gitSource(t, root, "creds", "# creds\n", "")
		src := serveGitHTTP(t, root)
		withUser := "http://user:hunter2@" + src[len("http://"):] + "/creds.git"

		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/creds/link", "acme",
			map[string]any{"source": withUser}); code != http.StatusOK {
			t.Fatalf("link: %d %s", code, b)
		}
		store, err := storeFor(service(t), "acme")
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(context.Background(), "acme", "", "creds")
		if err != nil {
			t.Fatal(err)
		}
		if got.Origin == "" {
			t.Fatal("no origin recorded")
		}
		if strings.Contains(got.Origin, "hunter2") || strings.Contains(got.Origin, "user:") {
			t.Fatalf("a credential reached the stored origin: %q", got.Origin)
		}
	})
}

// N callers missing at the same instant cost ONE question to the host and one
// transaction, and every one of them reads the host's commit from the store.
func TestConcurrentMissesCostOneFetch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		_, commit := linkedIn(t, app, "hot", "# hot\n")

		s := service(t)
		store, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		r, err := store.Get(context.Background(), "acme", "", "hot")
		if err != nil {
			t.Fatal(err)
		}
		// Forget the copy, as a replica that never read it would find it.
		objs, err := objectsFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if err := objs.Remove(context.Background(), r.ID); err != nil {
			t.Fatal(err)
		}
		s.State.cache.stale("acme", "", "hot")
		before := s.State.cache.fetches.Load()

		const callers = 8
		var wg sync.WaitGroup
		errs := make([]error, callers)
		tips := make([]string, callers)
		wg.Add(callers)
		for i := range callers {
			go func() {
				defer wg.Done()
				repo, done, err := open(context.Background(), s, r)
				if err != nil {
					errs[i] = err
					return
				}
				defer done()
				rev, _, err := repo.Resolve(context.Background(), "")
				errs[i], tips[i] = err, string(rev)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("caller %d failed a concurrent miss: %v", i, err)
			}
			if tips[i] != commit {
				t.Fatalf("caller %d saw %s, want %s", i, tips[i], commit)
			}
		}
		if n := s.State.cache.fetches.Load() - before; n != 1 {
			t.Fatalf("%d questions reached the host for 8 callers", n)
		}
	})
}

// storeTip is the branch's commit in the store's copy of a repository, "" when it has none.
func storeTip(t *testing.T, s *cloud.Service[state], org, name, branch string) string {
	t.Helper()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.Get(context.Background(), org, "", name)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := objectsFor(s, org)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := objs.Repo(context.Background(), r.ID).Reference(plumbing.NewBranchReferenceName(branch))
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}
