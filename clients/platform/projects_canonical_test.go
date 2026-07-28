package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixedIdent hands out one org-scoped credential — the shape EnsureOrgIdentity
// returns once the minter has run.
type fixedIdent struct{ id, secret string }

func (f fixedIdent) EnsureOrgIdentity(context.Context, string) (string, string, error) {
	return f.id, f.secret, nil
}

// canonIAM fakes the canonical IAM: a client_credentials token endpoint plus the
// two project reads, enforcing the machine-identity contract the real authz does.
type canonIAM struct {
	tokenCalls atomic.Int32
	projects   map[string]bool // "org/name"
}

func (m *canonIAM) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/iam/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		m.tokenCalls.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") == "" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + r.Form.Get("client_id"), "expires_in": 3600})
	})
	authed := func(r *http.Request) bool {
		return strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-")
	}
	mux.HandleFunc("GET /v1/iam/projects", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			w.WriteHeader(401)
			return
		}
		owner := r.URL.Query().Get("owner")
		out := []map[string]string{}
		for k := range m.projects {
			if strings.HasPrefix(k, owner+"/") {
				out = append(out, map[string]string{"owner": owner, "name": strings.TrimPrefix(k, owner+"/")})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"projects": out, "total": len(out)})
	})
	mux.HandleFunc("POST /v1/iam/projects/get", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			w.WriteHeader(401)
			return
		}
		var ref struct{ Owner, Name string }
		_ = json.NewDecoder(r.Body).Decode(&ref)
		if !m.projects[ref.Owner+"/"+ref.Name] {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"status":404,"error":"not found"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"owner": ref.Owner, "name": ref.Name})
	})
	return mux
}

func canonFor(url string) (*canonicalProjects, *canonIAM) {
	m := &canonIAM{projects: map[string]bool{}}
	return &canonicalProjects{
		base:  url,
		ident: fixedIdent{id: "acme-platform-kms", secret: "s"},
		hc:    &http.Client{Timeout: 5 * time.Second},
		toks:  map[string]orgToken{},
	}, m
}

// TestCanonicalProjectsRoundTrip: List/Get/Exists against the canonical store,
// with the token minted once and reused — one identity, one login, many reads.
func TestCanonicalProjectsRoundTrip(t *testing.T) {
	c, m := canonFor("")
	srv := httptest.NewServer(m.handler(t))
	defer srv.Close()
	c.base = srv.URL
	m.projects["acme/web"] = true
	ctx := context.Background()

	rows, err := c.List(ctx, "acme")
	if err != nil || len(rows) != 1 || rows[0].Name != "web" {
		t.Fatalf("List = %v, %v", rows, err)
	}
	ok, err := c.Exists(ctx, "acme", "web")
	if err != nil || !ok {
		t.Fatalf("Exists(web) = %v, %v", ok, err)
	}
	// The convention requireProject depends on: absent is (nil, nil), not an error.
	p, err := c.Get(ctx, "acme", "ghost")
	if err != nil || p != nil {
		t.Fatalf("Get(ghost) = %v, %v — absent must be nil with no error", p, err)
	}
	ok, err = c.Exists(ctx, "acme", "ghost")
	if err != nil || ok {
		t.Fatalf("Exists(ghost) = %v, %v", ok, err)
	}
	if n := m.tokenCalls.Load(); n != 1 {
		t.Fatalf("token minted %d times across 4 reads, want 1 (cached)", n)
	}
}

// TestCanonicalProjectsIAMDownIsAnError: an unreachable canonical store must
// surface as an error — requireProject 503s and the run path proceeds under the
// implicit default with a warning; neither invents an answer.
func TestCanonicalProjectsIAMDownIsAnError(t *testing.T) {
	c, _ := canonFor("http://127.0.0.1:1")
	if _, err := c.List(context.Background(), "acme"); err == nil {
		t.Fatal("an unreachable IAM must be an error, never an empty success")
	}
	if _, err := c.Exists(context.Background(), "acme", "web"); err == nil {
		t.Fatal("Exists against an unreachable IAM must error")
	}
}

// TestCanonicalProjectsNoIdentityFailsClosed: no identity provider (no KMS
// plane) means no read — never an unauthenticated request on the wire.
func TestCanonicalProjectsNoIdentityFailsClosed(t *testing.T) {
	m := &canonIAM{projects: map[string]bool{}}
	srv := httptest.NewServer(m.handler(t))
	defer srv.Close()
	c := &canonicalProjects{base: srv.URL, ident: nil, hc: &http.Client{Timeout: time.Second}, toks: map[string]orgToken{}}
	if _, err := c.List(context.Background(), "acme"); err == nil {
		t.Fatal("nil identity provider must fail closed")
	}
	if n := m.tokenCalls.Load(); n != 0 {
		t.Fatal("no request may reach IAM without an identity")
	}
}

// TestNewProjectStoreSelector pins the ONE selector: an external IAM (IAM_URL)
// chooses the canonical HTTP client; its absence means this binary IS the IAM
// and the embedded store stays.
func TestNewProjectStoreSelector(t *testing.T) {
	t.Setenv("IAM_URL", "")
	if _, ok := newProjectStore("https://hanzo.id", nil).(iamProjects); !ok {
		t.Fatal("no IAM_URL: the embedded store is the canonical one")
	}
	t.Setenv("IAM_URL", "http://iam.hanzo.svc")
	if _, ok := newProjectStore("https://hanzo.id", nil).(*canonicalProjects); !ok {
		t.Fatal("IAM_URL set: the canonical HTTP client must be selected")
	}
}
