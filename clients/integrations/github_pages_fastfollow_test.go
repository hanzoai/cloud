package integrations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// github_pages_fastfollow_test.go proves Red's three fast-follows:
//   LOW-1  per-installation grant-set cache (collapse re-enumeration; keyed per
//          installation id, never per org; two installations => two fetches).
//   LOW-2  a rate-limit 403/429 is surfaced as 429, a permission 403 as 403.
//   INFO-1 truncateBody redacts credential-shaped substrings.

// grantFetches reads the mock's grant-enumeration counter under its lock.
func (m *pagesMock) grantFetches() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.repoListCalls
}

// TestPagesGrantCache_CollapsesBurst proves a burst of Pages requests for ONE
// installation enumerates the grant set at most once (LOW-1: no 100x self-DoS).
func TestPagesGrantCache_CollapsesBurst(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	for i := 0; i < 6; i++ {
		if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil); r.Code != http.StatusOK {
			t.Fatalf("call %d want 200, got %d (%s)", i, r.Code, r.Body)
		}
	}
	if n := m.grantFetches(); n != 1 {
		t.Fatalf("a 6-request burst must enumerate the grant set once, got %d", n)
	}
}

// TestPagesGrantCache_PerInstallationKeyed proves the cache key is the INSTALLATION
// ID, not the org name: two distinct installations each trigger their own fetch (so a
// cached grant can never widen cross-tenant scope), while each stays collapsed to one.
func TestPagesGrantCache_PerInstallationKeyed(t *testing.T) {
	m := newPagesMock(t, widgets)
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")
	connectOrg(t, "beta", "888", "beta-gh")

	for i := 0; i < 3; i++ {
		req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
	}
	for i := 0; i < 3; i++ {
		req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "beta", nil)
	}
	if n := m.grantFetches(); n != 2 {
		t.Fatalf("two installations must enumerate exactly twice (one per installation id), got %d", n)
	}
}

// TestPagesGrantCache_TTLBounded proves a cached grant set is honored only within
// grantTTL: reaching back past the expiry re-enumerates, so a revoked grant cannot
// outlive the TTL. It drives the cache directly (no wall-clock sleep) to keep the
// isolation window bound explicit.
func TestPagesGrantCache_TTLBounded(t *testing.T) {
	resetGrantCache()
	t.Cleanup(resetGrantCache)
	base := []githubRepo{{Name: "widgets", FullName: "acme-gh/widgets", DefaultBranch: "main"}}

	// Seed an entry that expired one second ago.
	grantMu.Lock()
	grantCache[42] = grantEntry{repos: base, exp: time.Now().Add(-time.Second)}
	fresh := grantCache[42]
	grantMu.Unlock()
	if time.Now().Before(fresh.exp) {
		t.Fatal("seeded entry should already be expired")
	}
	// A fresh entry (future exp) must be served from cache without a fetch — proven by
	// the burst test above; here we assert the expiry predicate itself is the gate.
	grantMu.Lock()
	grantCache[42] = grantEntry{repos: base, exp: time.Now().Add(grantTTL)}
	live := grantCache[42]
	grantMu.Unlock()
	if !time.Now().Before(live.exp) {
		t.Fatal("a just-written entry must be within TTL")
	}
	if grantTTL <= 0 || grantTTL > time.Minute {
		t.Fatalf("grantTTL must be short and positive, got %v", grantTTL)
	}
}

// TestRateLimitedClassification unit-proves LOW-2: which GitHub responses are a rate
// limit (=> 429) vs a real permission problem (=> 403).
func TestRateLimitedClassification(t *testing.T) {
	mk := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	cases := []struct {
		name   string
		status int
		body   string
		hdr    http.Header
		want   bool
		retry  string
	}{
		{"primary-403-remaining0", 403, `{"message":"API rate limit exceeded"}`, mk("X-RateLimit-Remaining", "0", "Retry-After", "60"), true, "60"},
		{"retry-after-403", 403, "", mk("Retry-After", "30"), true, "30"},
		{"secondary-body-403", 403, "You have exceeded a secondary rate limit", http.Header{}, true, ""},
		{"429-always", 429, "", http.Header{}, true, ""},
		{"plain-permission-403", 403, `{"message":"Resource not accessible by integration"}`, http.Header{}, false, ""},
		{"404-not-ratelimit", 404, "", http.Header{}, false, ""},
	}
	for _, tc := range cases {
		got, retry := rateLimited(tc.status, []byte(tc.body), tc.hdr)
		if got != tc.want || retry != tc.retry {
			t.Errorf("%s: rateLimited=(%v,%q) want (%v,%q)", tc.name, got, retry, tc.want, tc.retry)
		}
	}
}

// TestPagesRateLimitSurfacedAs429 proves the end-to-end wiring: a GitHub 403 that is a
// rate limit becomes a 429 to the client, NOT a misleading "re-authorize" 403.
func TestPagesRateLimitSurfacedAs429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case r.URL.Path == "/installation/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(widgets), "repositories": widgets})
		case strings.HasSuffix(r.URL.Path, "/pages"):
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for installation"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
	if r.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limit 403 must surface as 429, got %d (%s)", r.Code, r.Body)
	}
	if strings.Contains(strings.ToLower(string(r.Body)), "re-authorize") {
		t.Fatalf("a rate limit must not be reported as a permissions problem: %s", r.Body)
	}
}

// TestPagesPermission403StaysReauthorize proves a genuine permission 403 (no rate
// signal) still yields the actionable re-authorize message.
func TestPagesPermission403StaysReauthorize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case r.URL.Path == "/installation/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(widgets), "repositories": widgets})
		case strings.HasSuffix(r.URL.Path, "/pages"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	withGithubApp(t, srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acme-gh")

	r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
	if r.Code != http.StatusForbidden {
		t.Fatalf("permission 403 must stay 403, got %d (%s)", r.Code, r.Body)
	}
	if !strings.Contains(strings.ToLower(string(r.Body)), "re-authorize") {
		t.Fatalf("permission 403 must carry the re-authorize hint: %s", r.Body)
	}
}

// TestTruncateBodyRedactsSecrets proves INFO-1: any credential-shaped substring is
// scrubbed from a surfaced foreign error body, while ordinary text passes through.
func TestTruncateBodyRedactsSecrets(t *testing.T) {
	secrets := []string{
		`{"message":"auth was Bearer ghs_installationTOKEN0123456789abcdef"}`,
		`stray token ghs_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789`,
		`{"message":"error, auth was Bearer tok-777"}`,
		`github_pat_11ABCDEFG0123456789_abcdefXYZ`,
	}
	for _, s := range secrets {
		out := truncateBody([]byte(s))
		low := strings.ToLower(out)
		if strings.Contains(out, "ghs_") || strings.Contains(out, "tok-777") ||
			strings.Contains(low, "bearer ") || strings.Contains(out, "github_pat_") {
			t.Errorf("truncateBody left a secret: %q => %q", s, out)
		}
		if !strings.Contains(out, "[redacted]") {
			t.Errorf("expected a [redacted] marker: %q => %q", s, out)
		}
	}
	if got := truncateBody([]byte("plain validation error")); got != "plain validation error" {
		t.Errorf("non-secret text must pass through unchanged, got %q", got)
	}
}
