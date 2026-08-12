package tracker

// owner_test.go pins the two defects that made tracker.hanzo.ai unusable, and
// they are different bugs with the same symptom — a board that shows nothing.
//
//  1. THE ORG WAS WRONG. The IAM tenant is `hanzo`; its work lives on the forge
//     under `hanzoai`. A near-empty `hanzo` org also exists, so asking by name
//     answered 200 with an empty list — a wrong answer wearing a healthy one's
//     clothes. Measured on git.hanzo.ai: `hanzo` = 64 repos / 0 issues,
//     `hanzoai` = 250 repos and the actual work.
//
//  2. THE BOARD LIST WAITED ON THE REPOSITORY INVENTORY. That endpoint charges
//     per repository returned (~21s for one of five pages of the 250-repo org),
//     and nothing bounded it, so the page sat on skeletons. The list is built
//     from issues-search (~1.5s) instead, and the inventory is read only when it
//     is already warm.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/forge"
)

// askedForge records the `owner` of every issues-search and the org of every
// inventory listing, which is how "we asked the wrong org" becomes visible.
type askedForge struct {
	*httptest.Server
	mu    sync.Mutex
	owner []string
	// work is keyed by forge org: only the org that really holds the work
	// answers with any.
	work map[string][]map[string]any
	// inventoryDelay models the endpoint that made the board hang.
	inventoryDelay time.Duration
}

func newAsked(t *testing.T) *askedForge {
	t.Helper()
	done := make(chan struct{})
	f := &askedForge{work: map[string][]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1")
		switch {
		case path == "/repos/issues/search":
			owner := r.URL.Query().Get("owner")
			f.mu.Lock()
			f.owner = append(f.owner, owner)
			rows := f.work[owner]
			f.mu.Unlock()
			if rows == nil {
				rows = []map[string]any{}
			}
			writeJSON(w, rows)
		case strings.HasPrefix(path, "/orgs/") && strings.HasSuffix(path, "/repos"):
			if f.inventoryDelay > 0 {
				select {
				case <-time.After(f.inventoryDelay):
				case <-r.Context().Done():
					return
				case <-done:
					return
				}
			}
			w.Header().Set("X-Total-Count", "0")
			writeJSON(w, []any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// LIFO: release parked handlers before Close waits on them — a background
	// inventory refresh can still be in flight when the test ends.
	t.Cleanup(f.Server.Close)
	t.Cleanup(func() { close(done) })
	return f
}

func (f *askedForge) owners() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.owner...)
}

// row is one issues-search result carrying the repository inline, which is what
// lets the board list be built from issues alone.
func row(repo, title string) map[string]any {
	return map[string]any{
		"id": 1, "number": 1, "title": title, "state": "open",
		"labels":     []map[string]any{},
		"repository": map[string]any{"name": repo, "full_name": "hanzoai/" + repo, "owner": "hanzoai"},
	}
}

// ── defect 1: the IAM org is not the forge org ───────────────────────────────

func TestForgeOwner_TranslatesTheIAMOrgAndRefusesTheRest(t *testing.T) {
	got, err := forgeOwner("hanzo")
	if err != nil || got != "hanzoai" {
		t.Fatalf("forgeOwner(hanzo) = %q, %v; want hanzoai — the board reads the wrong org", got, err)
	}
	// Case and surrounding space must not decide a tenancy question.
	if got, err := forgeOwner("  HANZO "); err != nil || got != "hanzoai" {
		t.Fatalf("forgeOwner(%q) = %q, %v; want hanzoai", "  HANZO ", got, err)
	}
	// REFUSED for everyone else. This used to be identity — "a tenant whose two
	// names agree needs no entry" — which meant an unmapped IAM org WAS a forge
	// coordinate, and the forge namespaces (hanzoai, luxfi, zooai) were names a
	// customer could take at signup. An org with no forge is a 403, never a read
	// of somebody else's namespace.
	for _, org := range []string{"acme", "zoo", "lux", "hanzoai", "luxfi", "zooai"} {
		if got, err := forgeOwner(org); err == nil {
			t.Fatalf("forgeOwner(%q) = %q with no error — an unmapped org became a forge namespace", org, got)
		}
	}
}

// THE regression test for the empty board: the request carries IAM org `hanzo`,
// and the forge must be asked about `hanzoai`.
func TestForgeOwner_TheBoardAsksTheForgeOrgNotTheIAMOrg(t *testing.T) {
	f := newAsked(t)
	// Only the real forge org has work. The namesake answers empty, exactly as
	// the live forge does — which is what made this fail silently.
	f.work["hanzoai"] = []map[string]any{row("cloud", "ship the tracker fix")}
	f.work["hanzo"] = []map[string]any{}
	app := mountAt(t, f.URL, 10*time.Second)

	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects", "hanzo", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("GET projects = %d %s", code, raw)
	}
	var boards []map[string]any
	if err := json.Unmarshal(raw, &boards); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(boards) == 0 {
		t.Fatalf("THE EMPTY BOARD: asked %v, got no boards — the IAM org was used as the forge org", f.owners())
	}
	if key, _ := boards[0]["key"].(string); key != "cloud" {
		t.Fatalf("board key = %q, want cloud; boards=%v", key, boards)
	}
	got := f.owners()
	for _, o := range got {
		if o == "hanzo" {
			t.Fatalf("the forge was asked about the IAM org name: %v", got)
		}
	}
	if len(got) == 0 || got[0] != "hanzoai" {
		t.Fatalf("the forge should have been asked about hanzoai, was asked %v", got)
	}
}

// The mapping must not change WHO the forge answers as. Translating the org and
// escalating the actor would be one change doing two things, and the second one
// is a privilege escalation.
func TestForgeOwner_TranslationDoesNotChangeTheSudoActor(t *testing.T) {
	var actors []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actors = append(actors, r.Header.Get("Sudo"))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/search") {
			writeJSON(w, []map[string]any{row("cloud", "work")})
			return
		}
		w.Header().Set("X-Total-Count", "0")
		writeJSON(w, []any{})
	}))
	t.Cleanup(srv.Close)
	app := mountAt(t, srv.URL, 10*time.Second)

	if code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects", "hanzo", "alice", nil); code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(actors) == 0 {
		t.Fatal("the forge was never called")
	}
	for _, a := range actors {
		if a != "alice" {
			t.Fatalf("Sudo actor = %q, want alice — the org mapping must not touch the identity", a)
		}
	}
}

// ── defect 2: the inventory is off the critical path ─────────────────────────

// The board list must not WAIT on the repository inventory. The stub makes that
// endpoint take three seconds — a tenth of what it costs in production — and the
// list still has to come back promptly, from issues-search.
func TestForgeProjects_DoesNotBlockOnTheRepositoryInventory(t *testing.T) {
	f := newAsked(t)
	f.work["hanzoai"] = []map[string]any{row("cloud", "ship it")}
	f.inventoryDelay = 3 * time.Second
	app := mountAt(t, f.URL, 30*time.Second)

	start := time.Now()
	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects", "hanzo", "alice", nil)
	took := time.Since(start)

	if code != http.StatusOK {
		t.Fatalf("GET projects = %d %s", code, raw)
	}
	var boards []map[string]any
	_ = json.Unmarshal(raw, &boards)
	if len(boards) != 1 {
		t.Fatalf("got %d boards, want 1 from issues-search: %s", len(boards), raw)
	}
	if took > 2*time.Second {
		t.Fatalf("the board list waited on the repository inventory: took %s", took)
	}
}

// A board a caller names is read as ONE repository, not found by listing the
// org. On a 250-repo org the difference is ~1s against ~100s.
func TestForgeProject_ReadsOneRepositoryRatherThanTheInventory(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/v1")
		mu.Lock()
		paths = append(paths, p)
		mu.Unlock()
		if p == "/repos/hanzoai/cloud" {
			writeJSON(w, map[string]any{"name": "cloud", "full_name": "hanzoai/cloud"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	app := mountAt(t, srv.URL, 10*time.Second)

	code, raw := asUser(t, app, http.MethodGet, "/v1/tracker/projects/cloud", "hanzo", "alice", nil)
	if code != http.StatusOK {
		t.Fatalf("GET project = %d %s", code, raw)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if strings.HasPrefix(p, "/orgs/") {
			t.Fatalf("the board-detail read listed the org inventory: %v", paths)
		}
	}
	if len(paths) != 1 || paths[0] != "/repos/hanzoai/cloud" {
		t.Fatalf("want exactly one direct repository read, got %v", paths)
	}
}

// ── the mapping, against the REAL forge ──────────────────────────────────────

// A mapping is only right if its target EXISTS AND HOLDS THE WORK. Pointing at a
// namesake is the exact failure this file exists for, and NO STUB CAN CATCH IT:
// a stub answers whatever it was told to, so it would confirm the mapping we
// wrote rather than the org we meant. Only the forge knows.
//
// Gated on a credential so it never runs in CI, in the same shape as
// forge/live_test.go — this is the check to re-run when an org is renamed or a
// tenant is added to the table.
//
//	FORGE_LIVE_TOKEN=… FORGE_LIVE_ACTOR=z go test -run LiveOwner ./apps/tracker/
func TestLiveOwner_EveryMappingTargetActuallyHoldsWork(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("FORGE_LIVE_TOKEN"))
	actor := strings.TrimSpace(os.Getenv("FORGE_LIVE_ACTOR"))
	if token == "" || actor == "" {
		t.Skip("set FORGE_LIVE_TOKEN and FORGE_LIVE_ACTOR to check the mapping against the real forge")
	}
	host := strings.TrimSpace(os.Getenv("FORGE_LIVE_HOST"))
	if host == "" {
		host = "git.hanzo.ai"
	}
	cl, err := forge.New(host, token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	for _, iam := range []string{"hanzo"} {
		owner, oerr := forgeOwner(iam)
		if oerr != nil {
			t.Fatalf("%s has no forge namespace: %v", iam, oerr)
		}
		rows, err := cl.As(actor).Issues(ctx, owner, forge.IssueFilter{State: "all", Limit: 1})
		if err != nil {
			t.Errorf("%s -> %s: the mapping target does not answer: %v", iam, owner, err)
			continue
		}
		if len(rows) == 0 {
			t.Errorf("%s -> %s: the mapping target has NO WORK on it. "+
				"An empty org answers 200 and reads as a healthy board with nothing on it, "+
				"which is how the wrong org went unnoticed — check this is not a namesake", iam, owner)
		}
	}
}
