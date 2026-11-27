package git

// A linked repository's copy in the store follows its host exactly (branches, tags,
// deletions, HEAD), costs the host a listing when nothing changed, is dropped when the
// row is linked elsewhere, and works against a real object store.

import (
	"context"
	"github.com/hanzoai/cloud/sdk/object"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// storeRefs is the branches and tags of a linked repository's copy in the store.
func storeRefs(t *testing.T, org, name string) map[string]string {
	t.Helper()
	s := service(t)
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
	refs, err := objs.Repo(context.Background(), r.ID).References()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, ref := range refs {
		switch {
		case ref.Name() == plumbing.HEAD:
			out["HEAD"] = ref.Target().String()
		case ref.Name().IsBranch() || ref.Name().IsTag():
			out[ref.Name().String()] = ref.Hash().String()
		}
	}
	return out
}

// settleNow makes the next read ask the host.
func settleNow(t *testing.T) {
	t.Helper()
	c := service(t).State.cache
	c.mu.Lock()
	c.ttl = 0
	c.mu.Unlock()
}

// readLinked reads the refs of a linked repository through the API, which refreshes it.
func readLinked(t *testing.T, app *zip.App, name string) {
	t.Helper()
	if code, body := do(t, app, http.MethodGet, "/v1/git/repos/"+name+"/refs", "acme", nil); code != http.StatusOK {
		t.Fatalf("refs: %d %s", code, body)
	}
}

// The copy is the host's refs exactly: a new branch and tag arrive, a branch the host
// moved forward moves, a branch and a tag the host deleted are pruned, and HEAD follows
// the host's default.
func TestTheStoreCopyFollowsEveryRefOfItsHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		src, first := h.repo(t, "acme", "widgets", "# widgets\n", false)
		linkAs(t, app, "acme", "u_acme", "widgets", src)
		bare := filepath.Join(h.root, "acme", "widgets.git")

		got := storeRefs(t, "acme", "widgets")
		if got["refs/heads/main"] != first || got["HEAD"] != "refs/heads/main" || got["refs/tags/v1"] == "" {
			t.Fatalf("first copy = %v", got)
		}

		dev := h.commit(t, "acme", "widgets", "dev", map[string]string{"dev.txt": "dev\n"})
		gitRun(t, "", "--git-dir="+bare, "tag", "v2", dev)
		next := h.commit(t, "acme", "widgets", "main", map[string]string{"NEXT.md": "more\n"})
		settleNow(t)
		readLinked(t, app, "widgets")
		got = storeRefs(t, "acme", "widgets")
		if got["refs/heads/main"] != next || got["refs/heads/dev"] != dev || got["refs/tags/v2"] != dev {
			t.Fatalf("after new refs = %v", got)
		}

		gitRun(t, "", "--git-dir="+bare, "update-ref", "-d", "refs/heads/dev")
		gitRun(t, "", "--git-dir="+bare, "tag", "-d", "v2")
		gitRun(t, "", "--git-dir="+bare, "symbolic-ref", "HEAD", "refs/heads/main")
		settleNow(t)
		readLinked(t, app, "widgets")
		got = storeRefs(t, "acme", "widgets")
		if _, ok := got["refs/heads/dev"]; ok {
			t.Fatalf("a deleted branch is still in the copy: %v", got)
		}
		if _, ok := got["refs/tags/v2"]; ok {
			t.Fatalf("a deleted tag is still in the copy: %v", got)
		}
		if got["refs/heads/main"] != next || got["refs/tags/v1"] == "" {
			t.Fatalf("after deletions = %v", got)
		}
	})
}

// A host that has not changed costs a listing and no pack.
func TestAnUnchangedHostCostsNoPack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		src, _ := h.repo(t, "acme", "widgets", "# widgets\n", false)
		linkAs(t, app, "acme", "u_acme", "widgets", src)
		packs := func() (n int) {
			h.mu.Lock()
			defer h.mu.Unlock()
			for _, l := range h.sent {
				if strings.HasPrefix(l, "POST ") && strings.Contains(l, "git-upload-pack") {
					n++
				}
			}
			return n
		}
		before := packs()
		if before == 0 {
			t.Fatal("the first copy asked the host for no pack")
		}
		for range 3 {
			settleNow(t)
			readLinked(t, app, "widgets")
		}
		if after := packs(); after != before {
			t.Fatalf("%d packs asked of an unchanged host", after-before)
		}
		h.commit(t, "acme", "widgets", "main", map[string]string{"x": "y"})
		settleNow(t)
		readLinked(t, app, "widgets")
		if after := packs(); after != before+1 {
			t.Fatalf("a changed host cost %d packs, want one", after-before)
		}
	})
}

// A row linked to another repository does not keep the first one's history.
func TestRelinkingDropsTheOldCopy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		h := serveHost(t)
		a, ca := h.repo(t, "acme", "alpha", "# alpha\n", false)
		b, cb := h.repo(t, "acme", "beta", "# beta\n", false)
		linkAs(t, app, "acme", "u_acme", "thing", a)
		if got := storeRefs(t, "acme", "thing")["refs/heads/main"]; got != ca {
			t.Fatalf("copy of alpha = %s, want %s", got, ca)
		}
		linkAs(t, app, "acme", "u_acme", "thing", b)
		if got := storeRefs(t, "acme", "thing")["refs/heads/main"]; got != cb {
			t.Fatalf("after relinking the copy is %s, want beta's %s", got, cb)
		}
	})
}

// The same, over the process's object store (a real hanzoai/s3 server when S3_ADMIN_* names one): the copy's packs
// are objects in a bucket, the volume holds no directory, and a refresh adds a pack.
func TestALinkedRepositoryOnARealObjectStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bucket := "git-linked-test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
		bucket = bucket[:min(len(bucket), 60)]
		store, err := object.Open(&object.Local{Dir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		real := objects.Bucket(store, bucket)
		was := packBlobs
		packBlobs = func() objects.Blobs { return real }
		t.Cleanup(func() {
			packBlobs = was
			ctx := context.Background()
			_ = real.List(ctx, "", func(o objects.Blob) error { return real.Delete(ctx, o.Key) })
			_ = store.Drop(ctx, bucket)
		})
		app := mountApp(t)
		base := liveServer(t, app)
		h := serveHost(t)
		src, first := h.repo(t, "acme", "widgets", "# widgets\n", false)
		linkAs(t, app, "acme", "u_acme", "widgets", src)
		count := func() (n int) {
			_ = real.List(context.Background(), "", func(objects.Blob) error { n++; return nil })
			return n
		}
		one := count()
		if one == 0 {
			t.Fatal("no pack reached the bucket")
		}
		dst := filepath.Join(t.TempDir(), "clone")
		gitRun(t, "", gitAs("acme", "u_acme", "clone", "-q", base+"/v1/git/acme/widgets.git", dst)...)
		if got := gitOut(t, dst, "rev-parse", "HEAD"); got != first {
			t.Fatalf("clone HEAD %s, want %s", got, first)
		}
		next := h.commit(t, "acme", "widgets", "main", map[string]string{"NEXT.md": "more\n"})
		settleNow(t)
		readLinked(t, app, "widgets")
		if got := storeRefs(t, "acme", "widgets")["refs/heads/main"]; got != next {
			t.Fatalf("copy at %s, want %s", got, next)
		}
		if count() <= one {
			t.Fatal("the refresh stored no pack")
		}
		if service(t).State.storage.exists("acme", "", "widgets") {
			t.Fatal("a directory was made on the volume")
		}
	})
}
