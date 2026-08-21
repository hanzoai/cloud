package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/plane"
)

// recCommerce records the X-Org-Id + method + path of the last forwarded request so a
// test can prove the /v1/admin control plane targets the RIGHT tenant namespace, and
// serves the promo + spend-alert shapes verbatim.
type recCommerce struct {
	server     *httptest.Server
	mu         sync.Mutex
	lastOrg    string
	lastMethod string
	lastPath   string
}

func newRecCommerce() *recCommerce {
	f := &recCommerce{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastOrg = r.Header.Get("X-Org-Id")
		f.lastMethod = r.Method
		f.lastPath = r.URL.Path
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/platform/promo"):
			io.WriteString(w, `{"percentOff":50,"plans":["pro"],"active":true}`)
		case strings.HasSuffix(r.URL.Path, "/alerts"):
			io.WriteString(w, `[{"id":"a1","threshold":10000,"enforce":true,"period":"2026-07","resetsAt":"2026-08-01T00:00:00Z"}]`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	return f
}

func (f *recCommerce) seen() (string, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastMethod, f.lastPath, f.lastOrg
}

func envStatus(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Status
}

// The promo control plane is SuperAdmin-only (core.Guard) and forwards to commerce's
// platform-promo endpoint.
func TestLimits_Promo_SuperOnly(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	com := newRecCommerce()
	defer com.server.Close()
	do := mount(t, iam.server.URL, com.server.URL, "")

	// SuperAdmin GET → 200 ok, forwarded to /v1/platform/promo.
	resp, body := do("GET", "/v1/admin/promos", superHdr)
	if resp.StatusCode != http.StatusOK || envStatus(t, body) != "ok" {
		t.Fatalf("super GET promos = %d %s", resp.StatusCode, body)
	}
	if m, p, _ := com.seen(); m != "GET" || !strings.HasSuffix(p, "/platform/promo") {
		t.Fatalf("forwarded %s %s, want GET .../platform/promo", m, p)
	}

	// SuperAdmin PUT → forwarded as PUT.
	if resp, _ := do("PUT", "/v1/admin/promos", superHdr); resp.StatusCode != http.StatusOK {
		t.Fatalf("super PUT promos = %d", resp.StatusCode)
	}
	if m, _, _ := com.seen(); m != "PUT" {
		t.Fatalf("promo PUT forwarded as %s, want PUT", m)
	}

	// A non-super org admin is REFUSED at the platform gate (403), never reaching commerce.
	if resp, _ := do("GET", "/v1/admin/promos", orgAdminHdr); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("org-admin GET promos = %d, want 403 (platform-only)", resp.StatusCode)
	}
}

// Cap oversight is org-scoped: a SuperAdmin targets any org via ?org=; a scoped admin
// is hard-pinned to their OWN org (a client ?org= is ignored — the escalation line).
func TestLimits_SpendCaps_OrgScoped(t *testing.T) {
	iam := newScopeIAM()
	defer iam.server.Close()
	com := newRecCommerce()
	defer com.server.Close()
	// The caps are reached BY NAME now — /v1/billing is billing's address and the
	// rows are commerce's — so the peer records which org each call was answered
	// FOR. That is the same question the forwarded header used to answer, asked
	// where the answer now lives.
	caps := newCapsPeer(t)
	do := mount(t, iam.server.URL, com.server.URL, "")

	// SuperAdmin with ?org=maxpower → the call is answered for maxpower.
	resp, body := do("GET", "/v1/admin/caps?org=maxpower", superHdr)
	if resp.StatusCode != http.StatusOK || envStatus(t, body) != "ok" {
		t.Fatalf("super caps = %d %s", resp.StatusCode, body)
	}
	if org, op := caps.seen(); org != "maxpower" || op != plane.BillingAlerts {
		t.Fatalf("answered for org=%q op=%q, want maxpower %s", org, op, plane.BillingAlerts)
	}

	// SuperAdmin WITHOUT ?org → org required (honest error, no guessed tenant).
	if _, body := do("GET", "/v1/admin/caps", superHdr); envStatus(t, body) != "error" {
		t.Fatalf("super caps without org must be an error envelope, got %s", body)
	}

	// A scoped org admin naming a FOREIGN ?org=hanzo is hard-pinned to their OWN org.
	do("GET", "/v1/admin/caps?org=hanzo", orgAdminHdr)
	if org, _ := caps.seen(); org != "maxpower" {
		t.Fatalf("scoped admin answered for org=%q, want maxpower (client ?org= must be ignored)", org)
	}
}
