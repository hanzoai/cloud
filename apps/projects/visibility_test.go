package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
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
// that fails leaves a private, moderated or DELETED project's source readable,
// which cannot be taken back and which nobody is notified about. So the
// retraction cases are the ones with teeth, and each of them ends by asking a
// reader with no credential whether it can still read the repository — the only
// question that actually means "closed".

// ── a forge to publish into ──────────────────────────────────────────────────

// forgery is a stand-in for the deployment's Forgejo, modelling the four things
// this seam depends on: a repository is born private, its visibility is one
// PATCHable bit, it can be deleted, and an anonymous reader sees a repository
// only while it is public. The last one is the whole invariant, so it is
// modelled rather than assumed — and confirmed against a real forge in
// public_live_test.go.
type forgery struct {
	*httptest.Server
	mu     sync.Mutex
	repos  map[string]*repo // "owner/name"
	seen   []string         // every path asked for, for the tenancy assertion
	made   int              // repositories created
	writes int              // requests that could change something
	deaf   bool             // accept the next visibility write and ignore it
	down   bool             // refuse everything
	onList func()           // runs once, while the repository list is being served
	token  string
}

// repo is one repository on the stand-in forge: whether it is closed, and WHICH
// repository it is. Every create stamps a fresh mark, so a test can tell a new
// repository from one a deleted project left behind — which is the whole of what
// "a reclaimed slug inherits nothing" means.
type repo struct {
	private bool
	mark    int
}

func newForgery(t *testing.T) *forgery {
	t.Helper()
	f := &forgery{repos: map[string]*repo{}, token: "forge-machine-token"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *forgery) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	f.mu.Lock()
	f.seen = append(f.seen, path)
	down := f.down
	if r.Method != http.MethodGet {
		f.writes++
	}
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
	case strings.HasPrefix(path, "/orgs/") && strings.HasSuffix(path, "/repos"):
		owner := strings.TrimSuffix(strings.TrimPrefix(path, "/orgs/"), "/repos")
		if anon {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			f.list(w, r, owner)
			return
		}
		var body struct {
			Name    string `json:"name"`
			Private bool   `json:"private"`
		}
		read(r, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		key := owner + "/" + body.Name
		if _, there := f.repos[key]; there {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.made++
		f.repos[key] = &repo{private: body.Private, mark: f.made}
		w.WriteHeader(http.StatusCreated)
		writeRepo(w, body.Name, body.Private)

	case strings.HasSuffix(path, "/branch_protections"):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))

	case strings.HasPrefix(path, "/repos/"):
		name := strings.TrimPrefix(path, "/repos/")
		f.mu.Lock()
		defer f.mu.Unlock()
		got, there := f.repos[name]
		if !there || (anon && got.private) {
			// A repository the reader may not see and one that is not there are the
			// same answer, which is what stops a 404 from confirming a private repo.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			delete(f.repos, name)
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPatch:
			var body struct {
				Private *bool `json:"private"`
			}
			read(r, &body)
			if body.Private != nil && !f.deaf {
				got.private = *body.Private
			}
			f.deaf = false // deafness is one write long: the retry must be able to land
		}
		writeRepo(w, name, got.private)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// list answers the paged repository walk. It pages for real (the client asks for
// five at a time) so a caller that reads only the first page is a test failure
// rather than a test that never noticed.
func (f *forgery) list(w http.ResponseWriter, r *http.Request, owner string) {
	f.mu.Lock()
	hook := f.onList
	f.onList = nil
	f.mu.Unlock()
	if hook != nil {
		// The list IS the audit's snapshot, so anything that happens here happens
		// mid-walk: after the snapshot was taken, before it is acted on.
		hook()
	}

	f.mu.Lock()
	var names []string
	for key := range f.repos {
		if strings.HasPrefix(key, owner+"/") {
			names = append(names, strings.TrimPrefix(key, owner+"/"))
		}
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "private": f.repos[owner+"/"+n].private})
	}
	f.mu.Unlock()

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if limit <= 0 {
		limit = len(out)
	}
	if page <= 0 {
		page = 1
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(out)))
	w.Header().Set("Content-Type", "application/json")
	from := (page - 1) * limit
	if from > len(out) {
		from = len(out)
	}
	to := from + limit
	if to > len(out) {
		to = len(out)
	}
	_ = json.NewEncoder(w).Encode(out[from:to])
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
	_, ok := f.repos[community+"/"+name]
	return ok
}

// mark is WHICH repository this name currently holds. Two reads that disagree
// are two different repositories, however identical their names.
func (f *forgery) mark(t *testing.T, name string) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	got, ok := f.repos[community+"/"+name]
	if !ok {
		t.Fatalf("no repository %q", name)
	}
	return got.mark
}

// close shuts a repository behind the seam's back, to stage the failure the
// audit must NOT fix.
func (f *forgery) close(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if got, ok := f.repos[community+"/"+name]; ok {
		got.private = true
	}
}

func (f *forgery) set(field *bool, v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*field = v
}

func (f *forgery) wrote() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// asked is how many requests this forge has been sent, of any kind.
func (f *forgery) asked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
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
		DataDir: t.TempDir(), KMS: vault{token: f.token},
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
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

// drained waits until no project has work in flight, so a case that asserts what
// the NEXT call does is not racing the last one.
func drained(t *testing.T, s *cloud.Service[state]) {
	t.Helper()
	settled(t, "the queue to empty", func() bool {
		s.State.queue.mu.Lock()
		defer s.State.queue.mu.Unlock()
		return len(s.State.queue.work) == 0
	})
}

// visibilityOf writes a project's visibility STRAIGHT TO THE ROW, with no
// reconcile behind it — which is how every case here stages the state a process
// that died between the two writes leaves behind.
func visibilityOf(t *testing.T, s *cloud.Service[state], org, slug, vis string) {
	t.Helper()
	ctx := context.Background()
	p, err := s.State.store.GetProject(ctx, org, slug)
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	p.Visibility = vis
	if err := s.State.store.UpdateProject(ctx, p); err != nil {
		t.Fatalf("write the row: %v", err)
	}
}

// create makes one public project through the surface, as a publisher does.
func create(t *testing.T, app *zip.App, slug string) {
	t.Helper()
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": slug, "slug": slug}); code != http.StatusCreated {
		t.Fatalf("create %s want 201, got %d (%s)", slug, code, body)
	}
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
	create(t, app, "board")
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
		create(t, app, "secret")
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
		create(t, app, "spam")
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
	create(t, app, "secret")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_secret") })

	f.set(&f.deaf, true) // the next visibility write is accepted and ignored
	if code, body := do(t, app, http.MethodPatch, "/v1/projects/secret", "acme",
		map[string]any{"visibility": "private"}); code != http.StatusOK {
		t.Fatalf("go private want 200, got %d (%s)", code, body)
	}
	settled(t, "the retry to close the source", func() bool { return !f.readable(t, "acme_secret") })
}

// A repository that has been ARCHIVED on the forge can still be closed. Closing
// needs neither the repository to be writable nor to exist, so it does not go
// through the ensure — which refuses an archived repository, and would make an
// archived one the only kind this seam could never retract.
func TestClosingIsNotGatedOnWritability(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	m, err := machine(s, t.Context())
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	// Straight at the retraction with no row behind it: this is what the audit
	// does to a repository nothing permits, and what apply does before a delete.
	there, err := retract(t.Context(), m, "acme_board")
	if err != nil || !there {
		t.Fatalf("retract = (%v, %v), want (true, nil)", there, err)
	}
	if f.readable(t, "acme_board") {
		t.Fatal("the retraction did not close the repository")
	}
	// And a repository that is not there at all is already closed: absence is
	// success, not an error to retry forever.
	there, err = retract(t.Context(), m, "acme_nothing")
	if err != nil || there {
		t.Fatalf("retract of a missing repository = (%v, %v), want (false, nil)", there, err)
	}
}

// ── deletion ─────────────────────────────────────────────────────────────────

// Deleting a project takes its source with it. Anything less leaves a repository
// nobody will ever write again, readable, with no row left to say it must not
// be — and no write coming that would notice.
func TestDeletingAProjectTakesItsSourceWithIt(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	if code, body := do(t, app, http.MethodDelete, "/v1/projects/board", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d (%s)", code, body)
	}
	// Asserted with no waiting: the slug is free to reclaim the moment the delete
	// answers, so the retirement has to have HAPPENED by then, not be scheduled.
	if f.readable(t, "acme_board") {
		t.Fatal("a deleted project's source is still world-readable")
	}
	if f.exists("acme_board") {
		t.Fatal("a deleted project's source is still on the forge")
	}
}

// A delete does not YIELD its retirement to a write already in flight. That
// write's own run would carry it — it re-reads the row and finds none — but only
// until the slug is reclaimed, and then it would find the new row and adopt the
// repository it was meant to destroy.
func TestADeleteRetiresTheSourceBesideAWriteInFlight(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	release := make(chan struct{})
	s.State.queue.add("acme/board", func() { <-release }) // a reconcile, in flight
	defer close(release)

	if code, body := do(t, app, http.MethodDelete, "/v1/projects/board", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d (%s)", code, body)
	}
	if f.exists("acme_board") {
		t.Fatal("the delete left its retirement to the write in flight")
	}
}

// A forge that is away cannot fail a delete. The row is authoritative and is
// already gone; refusing to delete a project because the forge is unreachable
// would leave one nobody can address, so the retirement is retried behind the
// answer and, past that, caught by the audit.
func TestAForgeOutageDoesNotFailADelete(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	f.set(&f.down, true)
	if code, body := do(t, app, http.MethodDelete, "/v1/projects/board", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete against a dead forge want 204, got %d (%s)", code, body)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/projects/board", "acme", nil); code != http.StatusNotFound {
		t.Fatalf("get after delete want 404, got %d", code)
	}
	// What is left behind is exactly what the audit exists to find: a readable
	// repository with no row permitting it. TestTheAuditClosesARepositoryNoRowPermits
	// is that case.
	f.set(&f.down, false)
}

// A reclaimed slug gets a FRESH repository. Ensure is idempotent by name, so a
// repository left behind by a deleted project is one the next project of that
// name adopts — commits and all — and then publishes. Deleting it is what makes
// the name safe to hand out again.
func TestAReclaimedSlugGetsAFreshSource(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	// PRIVATE first: its commits are the ones that must not resurface.
	if code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Secret", "slug": "board", "visibility": "private"}); code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	settled(t, "the repository to exist", func() bool { return f.exists("acme_board") })
	drained(t, s)
	first := f.mark(t, "acme_board")

	if code, body := do(t, app, http.MethodDelete, "/v1/projects/board", "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204, got %d (%s)", code, body)
	}
	if f.exists("acme_board") {
		t.Fatal("the deleted project's source survived the delete")
	}

	// The slug is reclaimed — and the new project is public, so an inherited
	// repository would publish the deleted project's commits.
	create(t, app, "board")
	settled(t, "the new project's repository", func() bool { return f.readable(t, "acme_board") })
	if got := f.mark(t, "acme_board"); got == first {
		t.Fatalf("the reclaimed slug inherited the deleted project's repository (mark %d)", got)
	}
}

// ── the shape of the seam ────────────────────────────────────────────────────

// Firing on every write is only safe if a repeat is free: the repository is
// ensured rather than created, so a project written twice has one repository and
// the second write is a reconcile.
func TestRefiringIsIdempotent(t *testing.T) {
	app, f := mountShared(t)
	create(t, app, "board")
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
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	create(t, app, "board")
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
	create(t, app, "secret")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_secret") })
	drained(t, s)

	// The row goes private without the seam being told — exactly what a process
	// that died between the two writes leaves behind.
	visibilityOf(t, s, "acme", "secret", Private)
	if !f.readable(t, "acme_secret") {
		t.Fatal("the source closed on its own; the leak this audit exists for was not staged")
	}

	if err := sweep(s, t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	drained(t, s)
	if f.readable(t, "acme_secret") {
		t.Fatal("the audit left an unlisted project world-readable")
	}
}

// The audit's question is asked FROM THE FORGE: for each open repository, is
// there a live row that permits it? Asked the other way round — for each row
// that must be closed, is its repository open? — a DELETED project has no row to
// ask about, so the one repository nobody will ever write again is the one
// nothing checks.
func TestTheAuditClosesARepositoryNoRowPermits(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	// Seven, because the repository walk is PAGED five to a page: the orphan is on
	// the second page, so an audit that stopped at the first would not find it.
	for i := 0; i < 7; i++ {
		create(t, app, fmt.Sprintf("app-%d", i))
	}
	settled(t, "every repository to be readable", func() bool {
		for i := 0; i < 7; i++ {
			if !f.readable(t, fmt.Sprintf("acme_app-%d", i)) {
				return false
			}
		}
		return true
	})
	drained(t, s)

	// The row goes away without the forge hearing about it: a delete that died
	// between the two writes, which is what leaves a repository no row can ever
	// speak for again.
	if _, deleted, err := s.State.store.DeleteProject(t.Context(), "acme", "app-6"); err != nil || !deleted {
		t.Fatalf("delete the row: deleted=%v err=%v", deleted, err)
	}
	if !f.readable(t, "acme_app-6") {
		t.Fatal("the orphan closed on its own; the leak this audit exists for was not staged")
	}

	if err := sweep(s, t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	drained(t, s)
	if f.readable(t, "acme_app-6") {
		t.Fatal("the audit left a deleted project's source world-readable")
	}
	// CLOSED, not deleted. The audit holds an ABSENCE, not a deletion, so it takes
	// the recoverable half: a store that is empty or half-restored must be able to
	// cost a namespace of closed repositories, never one of deleted ones.
	if !f.exists("acme_app-6") {
		t.Fatal("the audit deleted a repository")
	}
	// And it left every live project alone.
	for i := 0; i < 6; i++ {
		if name := fmt.Sprintf("acme_app-%d", i); !f.readable(t, name) {
			t.Fatalf("the audit closed %s, which a live row permits open", name)
		}
	}
}

// A repository named by nothing this seam could have minted is open with no row
// that can ever speak for it, so it is closed too.
func TestTheAuditClosesAnOpenRepositoryNothingPublished(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	f.mu.Lock()
	f.made++
	f.repos[community+"/stray"] = &repo{mark: f.made}
	f.mu.Unlock()

	if err := sweep(s, t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	drained(t, s)
	if f.readable(t, "stray") {
		t.Fatal("the audit left open a repository no project can name")
	}
	if !f.readable(t, "acme_board") {
		t.Fatal("the audit closed a live project")
	}
}

// The audit may only ever CLOSE. A public project whose repository is shut is
// the opposite failure, and not this one's to fix: the direction that can be
// wrong is the direction that leaks, and an audit that could open a repository
// is one bad row read away from publishing a private project.
func TestTheAuditNeverOpens(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	f.close("acme_board")
	if err := sweep(s, t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	drained(t, s)
	if f.readable(t, "acme_board") {
		t.Fatal("the audit opened a repository")
	}
	// Nor does its per-repository check, reached directly.
	if err := vet(s, t.Context(), "acme", "board", "acme_board"); err != nil {
		t.Fatalf("vet: %v", err)
	}
	if f.readable(t, "acme_board") {
		t.Fatal("the audit's own check opened a repository")
	}
}

// The audit must not act on a stale snapshot. Its list of repositories is a
// snapshot by construction, so the row is re-read AFTER it — twice, once to
// decide whether to bother and once inside the check that actually closes — and
// a project that goes public in between keeps its listing.
func TestTheAuditLetsGoAProjectThatGoesPublicMidWalk(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "board")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_board") })
	drained(t, s)

	// Stage the leak the audit is for: the row says private, the repository is
	// open. Then the publisher changes their mind WHILE the walk is being served.
	visibilityOf(t, s, "acme", "board", Private)
	f.mu.Lock()
	f.onList = func() { visibilityOf(t, s, "acme", "board", Public) }
	f.mu.Unlock()

	if err := sweep(s, t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	drained(t, s)
	if !f.readable(t, "acme_board") {
		t.Fatal("the audit closed a project whose row permits it open")
	}

	// And the same one level further in: the check that DECIDES runs behind the
	// queue, long after the walk, and re-reads the row there too.
	if err := vet(s, t.Context(), "acme", "board", "acme_board"); err != nil {
		t.Fatalf("vet: %v", err)
	}
	if !f.readable(t, "acme_board") {
		t.Fatal("the audit's own check closed a project whose row permits it open")
	}
}

// In the steady state the audit writes NOTHING. Every open repository is a
// public project, and finding that out is one local row read each.
func TestTheAuditWritesNothingInTheSteadyState(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	for _, slug := range []string{"one", "two"} {
		create(t, app, slug)
	}
	settled(t, "both repositories", func() bool { return f.readable(t, "acme_one") && f.readable(t, "acme_two") })
	drained(t, s)

	before := f.wrote()
	if err := sweep(s, t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	drained(t, s)
	if got := f.wrote(); got != before {
		t.Fatalf("the audit made %d writes over a healthy namespace; it must make none", got-before)
	}
}

// The audit is not a single shot at the least reliable moment this process has.
// A forge that is away at boot is retried, so the one thing that recovers a
// missed close actually runs.
func TestTheAuditRetriesUntilItLands(t *testing.T) {
	app, f := mountShared(t)
	s := mounted
	create(t, app, "secret")
	settled(t, "the repository to be readable", func() bool { return f.readable(t, "acme_secret") })
	drained(t, s)
	visibilityOf(t, s, "acme", "secret", Private)
	if !f.readable(t, "acme_secret") {
		t.Fatal("the leak was not staged")
	}

	// The forge is away, which at boot is the ordinary case rather than the
	// exotic one: this process comes up beside it.
	f.set(&f.down, true)
	if err := sweep(s, t.Context()); err == nil {
		t.Fatal("a sweep against a dead forge reported success")
	}

	asked := f.asked()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go audit(s, ctx)
	settled(t, "the audit to try and fail", func() bool { return f.asked() > asked })
	f.set(&f.down, false)
	settled(t, "the audit to land and close the source", func() bool { return !f.readable(t, "acme_secret") })
}

// A deployment with no KMS can never read a forge credential, so the audit says
// so once and stops rather than retrying against a permanent condition.
func TestTheAuditStopsWithoutACredential(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	done := make(chan struct{})
	go func() { defer close(done); audit(mounted, context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the audit is retrying a deployment that has no credential to read")
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

// The name reads BACK to the one project that can have minted it. That is what
// lets the audit start at the forge — the only place a deleted project's
// leftovers are still visible — and find the row that would have to permit it.
func TestARepositoryNameReadsBackToItsProject(t *testing.T) {
	for _, tc := range []struct{ org, slug string }{
		{"acme", "board"},
		{"a", "b-c"},
		{"a-b", "c"},
		{"acme-2", "site-9"},
	} {
		name, ok := repoName(tc.org, tc.slug)
		if !ok {
			t.Fatalf("repoName(%q, %q) refused", tc.org, tc.slug)
		}
		org, slug, ok := parts(name)
		if !ok || org != tc.org || slug != tc.slug {
			t.Fatalf("parts(%q) = (%q, %q, %v), want (%q, %q, true)", name, org, slug, ok, tc.org, tc.slug)
		}
	}
	// A name this seam could not have minted belongs to no project, so nothing in
	// the store can be permitting it: the audit closes it rather than guessing.
	for _, name := range []string{"", "board", "a_b_c", "_board", "acme_", "Acme_board", "acme_Board"} {
		if org, slug, ok := parts(name); ok {
			t.Fatalf("parts(%q) = (%q, %q, true), want a refusal", name, org, slug)
		}
	}
}

// ── ordering ─────────────────────────────────────────────────────────────────

// Two writes to one project must not race each other to the forge. The second is
// the one that is true, so a write that arrives while a run is in flight has to
// cause another run — and only one, however many arrive.
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

// The delete path runs its retirement HERE rather than scheduling it, because
// the slug is free to reclaim as soon as it answers — but it still takes the
// project's place in the queue, so it can never interleave with a reconcile
// already in flight, and a write that lands under it still causes the run that
// follows.
func TestTheDeletePathHoldsTheProjectsPlace(t *testing.T) {
	var q queue
	ran := make(chan string, 4)
	empty := func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.work) == 0
	}

	held, more := q.hold("acme/board", func() { ran <- "held" })
	if !held || more {
		t.Fatalf("hold on an idle project = (%v, %v), want (true, false)", held, more)
	}
	<-ran
	if !empty() {
		t.Fatal("the queue kept the project after a hold")
	}

	// With something already running, the hold is REFUSED rather than run beside
	// it — and the caller then queues behind it, which is what forget does.
	release := make(chan struct{})
	q.add("acme/board", func() { ran <- "running"; <-release })
	<-ran
	if held, _ := q.hold("acme/board", func() { ran <- "interleaved" }); held {
		t.Fatal("the hold ran beside a reconcile already in flight")
	}
	close(release)
	settled(t, "the queue to empty", empty)

	// A write that lands UNDER a hold is reported rather than started here: the
	// run belongs to the caller's goroutine, and a queue that re-ran it from
	// another one would be writing what the caller is reading.
	held, more = q.hold("acme/board", func() {
		ran <- "held again"
		q.add("acme/board", func() { ran <- "never started by the hold" })
	})
	if !held || !more {
		t.Fatalf("a write under a hold = (%v, %v), want (true, true)", held, more)
	}
	<-ran
	if !empty() {
		t.Fatal("the queue kept the project after a hold that reported the write")
	}
	select {
	case got := <-ran:
		t.Fatalf("unexpected run: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
}
