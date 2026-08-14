package forge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// public_test.go proves the visibility write, and it is written from the closing
// direction because that is the one that leaks when it is wrong.
//
// The stub here is deliberately NOT the one in forge_test.go: that one answers
// reads, and every property below is about what happens between a write and the
// read that confirms it.

// repos is a fake forge holding repositories and their one bit of visibility.
//
// deaf models the failure this method exists for: a forge that answers 200 to
// the PATCH and does not apply it. That is not a hypothetical — an unrecognised
// field in an edit body is dropped rather than refused — and it is exactly the
// shape of a silent false success.
type repos struct {
	*httptest.Server
	mu      sync.Mutex
	private map[string]bool // "owner/name" ⇒ closed
	patched int
	deaf    bool
	// keeps is deaf for the OTHER retraction: a forge that answers a delete and
	// keeps the repository. Same shape of silent false success, and the same
	// answer — the verdict is a read, not a status code.
	keeps bool
	token string
}

func newRepos(t *testing.T) *repos {
	t.Helper()
	s := &repos{private: map[string]bool{}, token: "machine-token-value"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/repos/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/v1/repos/")
		s.mu.Lock()
		defer s.mu.Unlock()
		closed, there := s.private[name]
		if !there {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			if !s.keeps {
				delete(s.private, name)
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPatch:
			s.patched++
			var body struct {
				Private *bool `json:"private"`
			}
			b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = json.Unmarshal(b, &body)
			if body.Private != nil && !s.deaf {
				s.private[name] = *body.Private
				closed = *body.Private
			}
			fallthrough
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Repo{
				Name: name, Private: closed, Branch: "main",
				Perm: &Perm{Admin: true, Push: true, Pull: true},
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func (s *repos) add(name string, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.private[name] = closed
}

func (s *repos) closed(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.private[name]
}

func (s *repos) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(s.URL, s.token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Machine()
}

// Opening and closing both land, and both are confirmed against the forge.
func TestSetPublic_OpensAndCloses(t *testing.T) {
	s := newRepos(t)
	s.add("hanzo-community/acme_board", true)
	c := s.client(t)

	if err := c.SetPublic(t.Context(), "hanzo-community", "acme_board", true); err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.closed("hanzo-community/acme_board") {
		t.Fatal("the repository is still private after being opened")
	}
	if err := c.SetPublic(t.Context(), "hanzo-community", "acme_board", false); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !s.closed("hanzo-community/acme_board") {
		t.Fatal("the repository is still public after being closed")
	}
}

// THE PROPERTY THIS METHOD EXISTS FOR: a forge that accepts the change and does
// not apply it must not read as success. A caller told "closed" for a repository
// that is open has a leak it will never retry.
func TestSetPublic_ADeafForgeIsAFailure(t *testing.T) {
	s := newRepos(t)
	s.add("hanzo-community/acme_secret", false) // open
	s.deaf = true

	err := s.client(t).SetPublic(t.Context(), "hanzo-community", "acme_secret", false)
	if err == nil {
		t.Fatal("closing a repository the forge did not close reported success")
	}
	if !strings.Contains(err.Error(), "did not change") {
		t.Fatalf("err = %v, want it to say the change did not take", err)
	}
	if s.closed("hanzo-community/acme_secret") {
		t.Fatal("the stub applied the change, so the deaf case was never exercised")
	}
}

// The wire: /v1 and never /api/v1, the machine credential, `private` as the
// negation of public, and a confirming GET after the write.
func TestSetPublic_TheWire(t *testing.T) {
	var mu sync.Mutex
	var got []*http.Request
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		mu.Lock()
		got = append(got, r)
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Repo{Name: "acme_board", Private: true, Branch: "main"})
	}))
	defer srv.Close()

	c, err := New(srv.URL, "machine-token-value")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Machine().SetPublic(t.Context(), "hanzo-community", "acme_board", false); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("%d requests, want the write and the read that confirms it", len(got))
	}
	if got[0].Method != http.MethodPatch || got[1].Method != http.MethodGet {
		t.Fatalf("methods = %s then %s, want PATCH then GET", got[0].Method, got[1].Method)
	}
	for _, r := range got {
		if r.URL.Path != "/v1/repos/hanzo-community/acme_board" {
			t.Fatalf("path = %q, want the /v1 repository path", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "token machine-token-value" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		// The machine identity carries no Sudo: a platform act is not attributed to
		// a person who did not make it.
		if v := r.Header.Get("Sudo"); v != "" {
			t.Fatalf("Sudo = %q on a machine call", v)
		}
	}
	if bodies[0] != `{"private":true}`+"\n" && strings.TrimSpace(bodies[0]) != `{"private":true}` {
		t.Fatalf("body = %q, want {\"private\":true}", bodies[0])
	}
}

// An unscoped client refuses, and nothing reaches the wire: the same fail-closed
// property every other call in this package has, on the one call that writes.
func TestSetPublic_NoActorRefuses(t *testing.T) {
	s := newRepos(t)
	s.add("hanzo-community/acme_board", true)
	c, err := New(s.URL, s.token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.SetPublic(t.Context(), "hanzo-community", "acme_board", true); !errors.Is(err, ErrNoActor) {
		t.Fatalf("err = %v, want ErrNoActor", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.patched != 0 {
		t.Fatalf("%d writes reached the forge from an unscoped client", s.patched)
	}
}

// A repository that is not there is ErrNotFound on a machine call, so a caller
// for which absence IS the wanted state can say so — closing something that does
// not exist is not a failure to close it.
func TestSetPublic_AbsentRepositoryIsNotFound(t *testing.T) {
	s := newRepos(t)
	err := s.client(t).SetPublic(t.Context(), "hanzo-community", "acme_gone", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A name that is not a path segment is refused before anything is sent, so no
// call site can be the one that forgot.
func TestSetPublic_RefusesNamesThatAreNotSegments(t *testing.T) {
	s := newRepos(t)
	for _, tc := range []struct{ owner, repo string }{
		{"", "acme_board"},
		{"hanzo-community", ""},
		{"hanzo-community", "../hanzoai/cloud"},
		{"hanzo-community", "acme/board"},
	} {
		if err := s.client(t).SetPublic(t.Context(), tc.owner, tc.repo, true); err == nil {
			t.Fatalf("SetPublic(%q, %q) was accepted", tc.owner, tc.repo)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.patched != 0 {
		t.Fatalf("%d writes reached the forge for a refused name", s.patched)
	}
}

// ── the other retraction ─────────────────────────────────────────────────────
//
// Closing a repository and deleting it are the same kind of act — a caller
// taking something back — and they fail the same way, so they are proved the
// same way: by a read afterwards, never by the status of the write.

// A delete removes the repository, and a repository that was never there is
// success rather than an error: absence is the state the caller asked for, so a
// retried delete can land on nothing and still be done.
func TestDelete_RemovesItAndToleratesAnAbsentOne(t *testing.T) {
	s := newRepos(t)
	s.add("hanzo-community/acme_board", false)
	c := s.client(t)

	if err := c.Delete(t.Context(), "hanzo-community", "acme_board"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	s.mu.Lock()
	_, there := s.private["hanzo-community/acme_board"]
	s.mu.Unlock()
	if there {
		t.Fatal("the repository is still on the forge after a delete")
	}
	if err := c.Delete(t.Context(), "hanzo-community", "acme_board"); err != nil {
		t.Fatalf("deleting what is already gone: %v", err)
	}
}

// A forge that accepts the delete and keeps the repository is a FAILURE here.
// Reported as success it would be a repository nobody's row speaks for any more,
// left readable, with the one caller who could have noticed told it was done.
func TestDelete_AForgeThatKeepsItIsAFailure(t *testing.T) {
	s := newRepos(t)
	s.add("hanzo-community/acme_board", false)
	s.mu.Lock()
	s.keeps = true
	s.mu.Unlock()

	if err := s.client(t).Delete(t.Context(), "hanzo-community", "acme_board"); err == nil {
		t.Fatal("a delete the forge did not apply was reported as success")
	}
}

// The names are refused before anything is sent, exactly as the visibility write
// refuses them: a value bearing a separator addresses a different repository
// than the call site wrote.
func TestDelete_RefusesNamesThatAreNotSegments(t *testing.T) {
	s := newRepos(t)
	s.add("hanzo-community/acme_board", false)
	for _, tc := range []struct{ owner, repo string }{
		{"", "acme_board"},
		{"hanzo-community", ""},
		{"hanzo-community", "../hanzoai/cloud"},
		{"hanzo-community", "acme/board"},
	} {
		if err := s.client(t).Delete(t.Context(), tc.owner, tc.repo); err == nil {
			t.Fatalf("Delete(%q, %q) was accepted", tc.owner, tc.repo)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, there := s.private["hanzo-community/acme_board"]; !there {
		t.Fatal("a refused name still deleted a repository")
	}
}

// An unscoped client refuses, and refuses BEFORE the wire: the delete is the
// most destructive call in this package, so it is the last one that may fall
// back to the machine identity.
func TestDelete_NoActorRefuses(t *testing.T) {
	s := newRepos(t)
	s.add("hanzo-community/acme_board", false)
	c, err := New(s.URL, s.token)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Delete(t.Context(), "hanzo-community", "acme_board"); !errors.Is(err, ErrNoActor) {
		t.Fatalf("err = %v, want ErrNoActor", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, there := s.private["hanzo-community/acme_board"]; !there {
		t.Fatal("an unscoped client deleted a repository")
	}
}
