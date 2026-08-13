package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// visibility_test.go proves the seam that decides who can read a project's
// source, and it is written from the RETRACTION side.
//
// The failure here is asymmetric. A publish that fails to reach the forge leaves
// a project un-browsable, which is annoying and self-correcting. A retraction
// that fails leaves a private or moderated project's source readable, which
// cannot be taken back and which nobody is notified about. So the retraction
// cases are the ones with teeth, and each of them ends by asking a reader with
// no credential whether it can still read the repository — the only question
// that actually means "closed".

// ── a forge to publish into ──────────────────────────────────────────────────

// forgery is a stand-in for the deployment's Forgejo, modelling the three things
// this seam depends on: a repository is born private, its visibility is one
// PATCHable bit, and an anonymous reader sees a repository only while it is
// public. The last one is the whole invariant, so it is modelled rather than
// assumed — and confirmed against a real forge in public_live_test.go.
type forgery struct {
	*httptest.Server
	mu      sync.Mutex
	private map[string]bool // "owner/name" ⇒ closed
	seen    []string        // every path asked for, for the tenancy assertion
	made    int             // repositories created
	deaf    bool            // accept the next visibility write and ignore it
	down    bool            // refuse everything
	token   string
}

func newForgery(t *testing.T) *forgery {
	t.Helper()
	f := &forgery{private: map[string]bool{}, token: "forge-machine-token"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *forgery) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	f.mu.Lock()
	f.seen = append(f.seen, path)
	down := f.down
	f.mu.Unlock()
	if down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// A reader with no credential is the world. It sees a repository only while
	// that repository is public, which is what "closed" has to mean.
	anon := r.Header.Get("Authorization") == ""
	if !anon && r.Header.Get("Authorization") != "token "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/orgs/") && strings.HasSuffix(path, "/repos"):
		if anon {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		owner := strings.TrimSuffix(strings.TrimPrefix(path, "/orgs/"), "/repos")
		var body struct {
			Name    string `json:"name"`
			Private bool   `json:"private"`
		}
		read(r, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		key := owner + "/" + body.Name
		if _, there := f.private[key]; there {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.private[key], f.made = body.Private, f.made+1
		w.WriteHeader(http.StatusCreated)
		writeRepo(w, body.Name, body.Private)

	case strings.HasSuffix(path, "/branch_protections"):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))

	case strings.HasPrefix(path, "/repos/"):
		name := strings.TrimPrefix(path, "/repos/")
		f.mu.Lock()
		defer f.mu.Unlock()
		closed, there := f.private[name]
		if !there || (anon && closed) {
			// A repository the reader may not see and one that is not there are the
			// same answer, which is what stops a 404 from confirming a private repo.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPatch {
			var body struct {
				Private *bool `json:"private"`
			}
			read(r, &body)
			if body.Private != nil && !f.deaf {
				f.private[name], closed = *body.Private, *body.Private
			}
			f.deaf = false // deafness is one write long: the retry must be able to land
		}
		writeRepo(w, name, closed)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func read(r *http.Request, into any) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = json.Unmarshal(b, into)
}

func writeRepo(w http.ResponseWriter, name string, private bool) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": name, "private": private, "default_branch": "main",
		"permissions": map[string]bool{"admin": true, "push": true, "pull": true},
	})
}

// readable asks as THE WORLD: no credential at all. This is the question every
// retraction case ends on.
func (f *forgery) readable(t *testing.T, name string) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.URL+"/v1/repos/"+community+"/"+name, nil)
	if err != nil {
		t.Fatalf("anonymous read: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("anonymous read: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func (f *forgery) exists(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.private[community+"/"+name]
	return ok
}

func (f *forgery) set(field *bool, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*field = v
}

// ── the deployment ───────────────────────────────────────────────────────────

// vault is the deployment's KMS, holding the one forge credential. The seam
// reads it from here and never from a field, an env file or a request.
type vault struct{ token string }

func (v vault) GetSecret(_ context.Context, ref string) ([]byte, error) {
	if ref == forge.TokenRef {
		return []byte(v.token), nil
	}
	return nil, errors.New("no such secret")
}
func (v vault) PutSecret(context.Context, string, []byte) error { return nil }
func (v vault) DeleteSecret(context.Context, string) error      { return nil }
func (v vault) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("not signed here")
}

// mountShared mounts the projects surface against a stand-in forge, exactly as
// the unified binary does — the credential through KMS, the host through the one
// override a deployment whose forge is not its own sibling uses.
func mountShared(t *testing.T) (*zip.App, *forgery) {
	t.Helper()
	f := newForgery(t)
	t.Setenv("CLOUD_FORGE_HOST", f.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{
		Logger: luxlog.New("test"), DataDir: t.TempDir(), KMS: vault{token: f.token},
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app, f
}

// settled waits for the seam's off-thread reconcile. It polls rather than
// synchronises because the production path is off-thread on purpose (a forge
// must not be able to slow or fail a project write), so a test that could only
// pass synchronously would be testing something we do not ship.
func settled(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// adminPatchProject PATCHes a project as a SuperAdmin. Moderation is the only
// admin-gated field on the project body, and asserting it needs a caller the
// ordinary `do` helper cannot make — so this is the ONE place that builds one.
func adminPatchProject(t *testing.T, app *zip.App, org, slug string, in map[string]any) projectsProject {
	t.Helper()
	b, _ := json.Marshal(in)
	req := httptest.NewRequest(http.MethodPatch, "/v1/projects/"+slug, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_admin")
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("admin patch: %v", err)
	}
	rb, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin patch want 200, got %d (%s)", resp.StatusCode, rb)
	}
	var out projectsProject
	_ = json.Unmarshal(rb, &out)
	return out
}

// ── publishing ───────────────────────────────────────────────────────────────

// Creating a public project gives it a repository anyone can read, with no
// second "share" step that can be forgotten.
func TestPublishingReachesTheForge(t *testing.T) {
	app, f := mountShared(t)
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Board", "slug": "board", "description": "a board"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })

	// It is published into the community namespace and NOWHERE ELSE. A tenant
	// slug that reached the estate's own namespace would let a project named
	// `cloud` decide who may read hanzoai/cloud.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.seen {
		if !strings.Contains(p, community) {
			t.Fatalf("the seam touched %q, outside the community namespace", p)
		}
	}
}

// A project created PRIVATE is never briefly readable: the repository is born
// closed, so there is no window between existing and being locked.
func TestAPrivateProjectIsBornClosed(t *testing.T) {
	app, f := mountShared(t)
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Secret", "slug": "secret", "visibility": "private"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	settled(t, "the repository to exist", func() bool { return f.exists("acme_secret") })
	if f.readable(t, "acme_secret") {
		t.Fatal("a project created private had a readable repository")
	}
}

// ── retraction: the cases with teeth ─────────────────────────────────────────

// Both ways a project stops being visible — the publisher going private, and the
// platform moderating it — must close the source, or the code stays readable
// after the listing is gone.
func TestRetractionClosesTheSource(t *testing.T) {
	t.Run("publisher goes private", func(t *testing.T) {
		app, f := mountShared(t)
		if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
			map[string]any{"name": "Secret", "slug": "secret"}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_secret") })

		// Private is metered like every other paid surface. With no fee configured
		// the gate is open, which is the free-tier operator default — so this
		// asserts the SEAM, not the price.
		code, body := do(t, app, http.MethodPatch, "/v1/projects/secret", "acme",
			map[string]any{"visibility": "private"})
		if code != http.StatusOK {
			t.Fatalf("go private want 200, got %d (%s)", code, body)
		}
		var p projectsProject
		_ = json.Unmarshal(body, &p)
		if p.Visibility != Private {
			t.Fatalf("visibility = %q, want private", p.Visibility)
		}
		settled(t, "the source to close", func() bool { return !f.readable(t, "acme_secret") })
	})

	t.Run("moderation", func(t *testing.T) {
		app, f := mountShared(t)
		if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
			map[string]any{"name": "Spam", "slug": "spam"}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_spam") })

		if p := adminPatchProject(t, app, "acme", "spam",
			map[string]any{"hidden": true, "hiddenReason": "spam"}); !p.Hidden {
			t.Fatal("admin hide did not take")
		}
		settled(t, "a moderated project's source to close", func() bool { return !f.readable(t, "acme_spam") })

		// And lifting it restores the publisher's own choice, in one write.
		if p := adminPatchProject(t, app, "acme", "spam", map[string]any{"hidden": false}); p.Hidden {
			t.Fatal("admin lift did not take")
		}
		settled(t, "the listing to be restored", func() bool { return f.readable(t, "acme_spam") })
	})
}

// A retraction is not dropped because the forge lied about it once. The write is
// confirmed by a read, so a forge that accepts the change and does not apply it
// is a FAILURE here, and the failure is retried until it lands.
func TestARetractionSurvivesAForgeThatLies(t *testing.T) {
	app, f := mountShared(t)
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Secret", "slug": "secret"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_secret") })

	f.set(&f.deaf, true) // the next visibility write is accepted and ignored
	if code, body := do(t, app, http.MethodPatch, "/v1/projects/secret", "acme",
		map[string]any{"visibility": "private"}); code != http.StatusOK {
		t.Fatalf("go private want 200, got %d (%s)", code, body)
	}
	settled(t, "the retry to close the source", func() bool { return !f.readable(t, "acme_secret") })
}

// ── the shape of the seam ────────────────────────────────────────────────────

// Firing on every write is only safe if a repeat is free: the repository is
// ensured rather than created, so a project written twice has one repository and
// the second write is a reconcile.
func TestRefiringIsIdempotent(t *testing.T) {
	app, f := mountShared(t)
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Board", "slug": "board"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	for i := 0; i < 3; i++ {
		if code, body := do(t, app, http.MethodPatch, "/v1/projects/board", "acme",
			map[string]any{"description": "again"}); code != http.StatusOK {
			t.Fatalf("update want 200, got %d (%s)", code, body)
		}
	}
	settled(t, "the reconciles to finish", func() bool { return f.readable(t, "acme_board") })
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.made != 1 {
		t.Fatalf("%d repositories created for one project; a repeat must reconcile, not build", f.made)
	}
}

// The project row is the source of truth, so a forge that is down must not be
// able to fail a project write. The row lands, the reconcile retries behind it.
func TestAForgeOutageDoesNotFailAProjectWrite(t *testing.T) {
	app, f := mountShared(t)
	f.set(&f.down, true)
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Alone", "slug": "alone"}); code != http.StatusCreated {
		t.Fatalf("create against a dead forge want 201, got %d (%s)", code, body)
	}
	if code, body := do(t, app, http.MethodPatch, "/v1/projects/alone", "acme",
		map[string]any{"visibility": "private"}); code != http.StatusOK {
		t.Fatalf("update against a dead forge want 200, got %d (%s)", code, body)
	}
	// And the write is the truth: the row says private even though the forge never
	// heard about it.
	code, body := do(t, app, http.MethodGet, "/v1/projects/alone", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get want 200, got %d (%s)", code, body)
	}
	var p projectsProject
	_ = json.Unmarshal(body, &p)
	if p.Visibility != Private {
		t.Fatalf("visibility = %q, want private", p.Visibility)
	}
}

// A deployment with no KMS — and so no forge credential — publishes nothing, and
// a project write still succeeds. Nothing is created, so nothing is exposed.
func TestNoCredentialPublishesNothing(t *testing.T) {
	f := newForgery(t)
	t.Setenv("CLOUD_FORGE_HOST", f.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Board", "slug": "board"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 0 {
		t.Fatalf("a deployment with no credential still reached the forge: %v", f.seen)
	}
}

// ── the audit ────────────────────────────────────────────────────────────────

// A close that never landed — the process died between the row and the forge —
// is found and closed at the next boot. This is the one failure the retries
// above cannot cover, because there is no process left to retry in.
func TestTheAuditClosesWhatWasLeftOpen(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Secret", "slug": "secret"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_secret") })

	// The row goes private without the seam being told — exactly what a process
	// that died between the two writes leaves behind.
	ctx := t.Context()
	p, err := s.State.store.GetProject(ctx, "acme", "secret")
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	p.Visibility = Private
	if err := s.State.store.UpdateProject(ctx, p); err != nil {
		t.Fatalf("write the row: %v", err)
	}
	if !f.readable(t, "acme_secret") {
		t.Fatal("the source closed on its own; the leak this audit exists for was not staged")
	}

	sweep(s, ctx)
	if f.readable(t, "acme_secret") {
		t.Fatal("the audit left an unlisted project world-readable")
	}
}

// The audit reads only what can leak. A public project is allowed to be readable
// and must not cost a forge call at every boot.
func TestTheAuditReadsOnlyWhatCanLeak(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	for _, slug := range []string{"one", "two"} {
		if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
			map[string]any{"name": slug, "slug": slug}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
	}
	settled(t, "both repositories", func() bool { return f.readable(t, "acme_one") && f.readable(t, "acme_two") })

	rows, err := s.State.store.Unlisted(t.Context())
	if err != nil {
		t.Fatalf("Unlisted: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("Unlisted returned %d public projects; it must return only what must be closed", len(rows))
	}
}

// ── the name ─────────────────────────────────────────────────────────────────

// No two projects may spell one repository. The namespace is flat, so the
// derivation is the whole of that guarantee: `-` would let org `a-b` publish
// over org `a`'s project, and publishing over it means deciding who may read it.
func TestRepoNameIsInjective(t *testing.T) {
	one, ok := repoName("a", "b-c")
	if !ok {
		t.Fatal("a/b-c has no name")
	}
	two, ok := repoName("a-b", "c")
	if !ok {
		t.Fatal("a-b/c has no name")
	}
	if one == two {
		t.Fatalf("two tenants spell one repository: %q", one)
	}

	for _, tc := range []struct{ org, slug string }{
		{"", "board"},
		{"acme", ""},
		{"ACME", "board"},
		{"acme", "Board"},
		{"acme/../hanzoai", "cloud"},
		{"acme", "board.git"},
		{"acme_x", "board"}, // the separator itself is not part of the alphabet
		{"acme", "bo_ard"},
	} {
		if name, ok := repoName(tc.org, tc.slug); ok {
			t.Fatalf("repoName(%q, %q) = %q, want a refusal", tc.org, tc.slug, name)
		}
	}
}

// ── ordering ─────────────────────────────────────────────────────────────────

// Two writes to one project must not race each other to the forge. The second is
// the one that is true, so a write that arrives while a reconcile is running has
// to cause another run — and only one, however many arrive.
func TestAWriteUnderARunningReconcileCausesAnotherRun(t *testing.T) {
	var q queue
	started, release, done := make(chan struct{}, 4), make(chan struct{}), make(chan struct{}, 4)
	q.add("acme/board", func() {
		started <- struct{}{}
		<-release
		done <- struct{}{}
	})
	<-started // the first run is in flight

	// Three writes land under it. They must collapse into exactly one more run:
	// the run they cause re-reads the row, so it would do what all three want.
	for i := 0; i < 3; i++ {
		q.add("acme/board", func() { started <- struct{}{}; done <- struct{}{} })
	}
	close(release)
	<-done
	<-started
	<-done

	select {
	case <-started:
		t.Fatal("three writes under one run caused more than one further run")
	case <-time.After(200 * time.Millisecond):
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.work) != 0 {
		t.Fatalf("the queue kept %d projects after finishing", len(q.work))
	}
}
