package forge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// orgStub models the one thing the shared countingForge does not: an
// organisation that does not exist yet. A GET of a missing org is a 404, a GET
// of a present one is a 200, POST /orgs brings one into being, and a repo walk
// of a missing org is a 404 — which is the shape [Client.EnsureOrg] and
// [Client.Inventory]'s missing-namespace handling are written against.
type orgStub struct {
	*httptest.Server
	mu     sync.Mutex
	exists map[string]bool
	posts  int // how many org creates were sent
}

func newOrgStub(t *testing.T, seeded ...string) *orgStub {
	t.Helper()
	s := &orgStub{exists: map[string]bool{}}
	for _, o := range seeded {
		s.exists[o] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/orgs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Username string `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.posts++
		if s.exists[body.Username] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		s.exists[body.Username] = true
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"username": body.Username})
	})
	mux.HandleFunc("/v1/orgs/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/orgs/")
		name, repos := rest, false
		if i := strings.Index(rest, "/repos"); i >= 0 {
			name, repos = rest[:i], true
		}
		s.mu.Lock()
		ok := s.exists[name]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if repos {
			w.Header().Set("X-Total-Count", "0")
			_ = json.NewEncoder(w).Encode([]Repo{})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"username": name})
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func (s *orgStub) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(s.URL, "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Machine()
}

// EnsureOrg brings a missing namespace into being with ONE create, and a
// present one costs a read and no write — the idempotence [apply] relies on to
// call it before every publish without minting a second org each time.
func TestEnsureOrgCreatesAMissingNamespaceOnce(t *testing.T) {
	s := newOrgStub(t)
	c := s.client(t)

	if err := c.EnsureOrg(t.Context(), "hanzo-community"); err != nil {
		t.Fatalf("EnsureOrg (create): %v", err)
	}
	if !s.exists["hanzo-community"] {
		t.Fatal("EnsureOrg returned success but the org was not created")
	}
	if s.posts != 1 {
		t.Fatalf("first EnsureOrg sent %d creates, want 1", s.posts)
	}

	// Idempotent: it is there now, so a second call reads and does not create.
	if err := c.EnsureOrg(t.Context(), "hanzo-community"); err != nil {
		t.Fatalf("EnsureOrg (present): %v", err)
	}
	if s.posts != 1 {
		t.Fatalf("second EnsureOrg sent another create (total %d); a present org must not be re-made", s.posts)
	}
}

// A 409 from a create that raced another is the state asked for, not a failure.
func TestEnsureOrgTreatsAConflictAsSuccess(t *testing.T) {
	s := newOrgStub(t, "hanzo-community")
	// Force the create path by making the existence read miss once: flip it absent
	// for the GET but keep POST returning 409. Simplest: a stub already-seeded org
	// answers the GET as present, so drive the conflict directly with a fresh stub
	// whose POST always conflicts.
	cs := &orgStub{exists: map[string]bool{"hanzo-community": true}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/orgs", func(w http.ResponseWriter, r *http.Request) {
		cs.posts++
		w.WriteHeader(http.StatusConflict)
	})
	mux.HandleFunc("/v1/orgs/", func(w http.ResponseWriter, r *http.Request) {
		// First GET says missing (forces the create), the read-back says present.
		cs.mu.Lock()
		defer cs.mu.Unlock()
		if cs.posts == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"username": "hanzo-community"})
	})
	cs.Server = httptest.NewServer(mux)
	t.Cleanup(cs.Server.Close)
	c := cs.client(t)
	if err := c.EnsureOrg(t.Context(), "hanzo-community"); err != nil {
		t.Fatalf("EnsureOrg must treat a 409 as the state it wanted: %v", err)
	}
	_ = s
}

// The close-only audit reads a namespace that may not exist yet — the community
// catalogue before the first publish. A missing org is not an error there: it
// has no repositories, so the answer is an empty COMPLETE list, and the audit
// closes nothing rather than retrying a 404 forever.
func TestInventoryOfAMissingOrgIsEmptyNotAnError(t *testing.T) {
	s := newOrgStub(t) // nothing seeded → every org is missing
	c := s.client(t)

	repos, whole, err := c.Inventory(t.Context(), "hanzo-community")
	if err != nil {
		t.Fatalf("Inventory of a missing org must not error: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("Inventory of a missing org returned %d repos, want 0", len(repos))
	}
	if !whole {
		t.Fatal("Inventory of a missing org must report the list COMPLETE (nothing to audit), not truncated")
	}

	// And once the namespace exists but is empty, the same empty-complete answer.
	if err := c.EnsureOrg(t.Context(), "hanzo-community"); err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	if repos, whole, err = c.Inventory(t.Context(), "hanzo-community"); err != nil || len(repos) != 0 || !whole {
		t.Fatalf("Inventory of a present empty org: repos=%d whole=%v err=%v", len(repos), whole, err)
	}
}
