package integrations

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// github_pages_adversarial_test.go is Red's refutation battery. It fires real
// attack payloads through the actual Fiber router + handlers against a recording
// mock GitHub that (unlike the friendly test mock) mints a DISTINCT token per
// installation and picks the grant set from that token — so two tenants can share
// a repo NAME and we can prove containment.

// ── adversarial mock: per-installation tokens + raw-URI recording ─────────────

type advMock struct {
	mu       sync.Mutex
	rawURIs  []string // r.RequestURI for every call (raw, pre-clean)
	authSeen []string // Authorization header for every /repos/*/pages call
	bodies   []string // request body for every /repos/*/pages call

	grants    map[string][]map[string]any // token -> granted repos
	pagesCode int                         // status for the site GET/POST/PUT/DELETE (0 => sensible default)
	pagesBody string                      // body for the site resource
	echoAuth  bool                        // reflect the Authorization header into the pages error body
	srv       *httptest.Server
}

var mintIDRE = regexp.MustCompile(`/app/installations/(\d+)/access_tokens`)

func newAdvMock(t *testing.T) *advMock {
	t.Helper()
	m := &advMock{grants: map[string][]map[string]any{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/access_tokens"):
			// Mint a token that encodes the installation id, so the list call
			// downstream can be attributed to the right tenant.
			id := "x"
			if mm := mintIDRE.FindStringSubmatch(p); mm != nil {
				id = mm[1]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "tok-" + id, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case strings.HasPrefix(p, "/app/installations/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"login": "acct"}})
		case p == "/installation/repositories":
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			repos := m.grants[tok]
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": repos})
		case strings.Contains(p, "/pages"):
			m.recordPages(r)
			m.servePages(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *advMock) recordPages(r *http.Request) {
	var body string
	if r.Body != nil {
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
	}
	m.mu.Lock()
	m.rawURIs = append(m.rawURIs, r.RequestURI)
	m.authSeen = append(m.authSeen, r.Header.Get("Authorization"))
	m.bodies = append(m.bodies, body)
	m.mu.Unlock()
}

func (m *advMock) servePages(w http.ResponseWriter, r *http.Request) {
	code := m.pagesCode
	if code == 0 {
		switch r.Method {
		case http.MethodGet:
			code = http.StatusOK
		case http.MethodPost:
			code = http.StatusCreated
		default:
			code = http.StatusNoContent
		}
	}
	if m.echoAuth {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"message":"error, auth was ` + r.Header.Get("Authorization") + `"}`))
		return
	}
	if m.pagesBody != "" {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(m.pagesBody))
		return
	}
	w.WriteHeader(code)
	if r.Method == http.MethodGet && code/100 == 2 {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "built", "html_url": "https://acct.github.io/x", "source": map[string]any{"branch": "main", "path": "/"},
		})
	}
}

// pagesRepoPaths returns the distinct /repos/<owner>/<name>/pages[/builds] paths hit.
func (m *advMock) pagesRepoPaths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, u := range m.rawURIs {
		out = append(out, u)
	}
	return out
}

// ── Attack 1: cross-tenant with a SHARED repo name ────────────────────────────

// TestAdv_CrossTenant_SharedRepoName models two tenants whose installations each
// grant a repo literally named "widgets" (different owners), plus a repo that lives
// ONLY in beta's grant. It proves acme's Pages calls resolve to acme's owner, never
// beta's, and that acme can never address beta-only "private" — 404, ZERO GitHub
// Pages call to beta's repo.
func TestAdv_CrossTenant_SharedRepoName(t *testing.T) {
	m := newAdvMock(t)
	m.grants["tok-777"] = []map[string]any{
		{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
	}
	m.grants["tok-888"] = []map[string]any{
		{"name": "widgets", "full_name": "beta-co/widgets", "default_branch": "main", "clone_url": "https://github.com/beta-co/widgets.git"},
		{"name": "private", "full_name": "beta-co/private", "default_branch": "main", "clone_url": "https://github.com/beta-co/private.git"},
	}
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acct")
	connectOrg(t, "beta", "888", "acct")

	// acme's widgets → acme-gh/widgets
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("acme widgets want 200, got %d (%s)", r.Code, r.Body)
	}
	// beta's widgets → beta-co/widgets
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "beta", nil); r.Code != http.StatusOK {
		t.Fatalf("beta widgets want 200, got %d (%s)", r.Code, r.Body)
	}
	// acme asks for beta-only "private" → 404, and beta-co/private must NEVER be hit.
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/private/pages", "acme", nil); r.Code != http.StatusNotFound {
		t.Fatalf("acme cross-tenant private want 404, got %d (%s)", r.Code, r.Body)
	}

	for _, u := range m.pagesRepoPaths() {
		// acme must never have caused a beta-co path; the private repo must never be hit at all.
		if strings.Contains(u, "beta-co/private") {
			t.Fatalf("CROSS-TENANT LEAK: beta-co/private was reached: %v", m.rawURIs)
		}
	}
	// Positive: both owner-correct paths were used, proving owner is grant-derived.
	joined := strings.Join(m.rawURIs, " ")
	if !strings.Contains(joined, "/repos/acme-gh/widgets/pages") || !strings.Contains(joined, "/repos/beta-co/widgets/pages") {
		t.Fatalf("expected both owner-correct paths, saw %v", m.rawURIs)
	}
	t.Logf("cross-tenant paths hit: %v", m.rawURIs)
}

// ── Attack 2: owner / path injection via :repo ────────────────────────────────

// TestAdv_OwnerPathInjection fires encoded-slash, traversal, null, case, unicode and
// dot payloads at every Pages verb and asserts (a) the response is a 4xx and (b) no
// GitHub /repos path other than the single granted acme-gh/widgets is ever reached.
func TestAdv_OwnerPathInjection(t *testing.T) {
	m := newAdvMock(t)
	m.grants["tok-777"] = []map[string]any{
		{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
	}
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acct")

	payloads := []string{
		"acme-gh%2Fwidgets",             // encoded slash: try to inject owner
		"..%2F..%2Fother-org%2Fwidgets", // encoded traversal
		"widgets%2F..%2Fsecret",         // encoded traversal off widgets
		"widgets%2Fpages",               // encoded slash to widen path
		"widgets/../secret",             // raw traversal
		"..",                            // dot-dot
		".git",                          // strips to empty
		"widgets%00",                    // null byte
		"widgets%0d%0aX-Injected:%201",  // CRLF in the segment
		"WIDGETS",                       // case variant (must not reach acme-gh/widgets)
		"wîdgets",                       // unicode homoglyph
		"other",                         // simply ungranted
		"acme-gh",                       // the owner as a repo name (ungranted)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, p := range payloads {
			path := "/v1/integrations/github/repos/" + p + "/pages"
			r := req(t, app, method, path, "acme", map[string]any{"cname": "www.example.com"})
			if r.Code < 400 || r.Code >= 500 {
				// A 2xx/3xx here would mean the payload resolved to SOME repo.
				// A 5xx would be a crash. Both are failures.
				if r.Code != http.StatusBadGateway { // 502 only acceptable if it never hit a repos path (checked below)
					t.Errorf("%s %q: want 4xx, got %d (%s)", method, p, r.Code, r.Body)
				}
			}
		}
	}
	// The ONLY /repos path that may ever appear is the granted one.
	for _, u := range m.rawURIs {
		if strings.Contains(u, "/repos/") && !strings.HasPrefix(u, "/repos/acme-gh/widgets/pages") {
			t.Fatalf("PATH INJECTION: unexpected GitHub path reached: %q (all: %v)", u, m.rawURIs)
		}
	}
	t.Logf("injection: repos paths reached = %v", m.rawURIs)
}

// ── Attack 3: CNAME host injection ────────────────────────────────────────────

func TestAdv_CNAMEInjection(t *testing.T) {
	m := newAdvMock(t)
	m.grants["tok-777"] = []map[string]any{
		{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
	}
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acct")

	long := strings.Repeat("a.", 130) + "com" // > 253 bytes, otherwise regex-valid

	cases := []struct {
		name   string
		cname  string
		accept bool // true => must reach GitHub PUT with a clean value; false => 400, no PUT
	}{
		{"crlf-header-inject", "www.example.com\r\nX-Injected: evil", false},
		{"embedded-lf", "www.exa\nmple.com", false},
		{"embedded-cr", "www.exa\rmple.com", false},
		{"trailing-crlf", "www.example.com\r\n", true}, // TrimSpace strips → clean
		{"trailing-lf", "www.example.com\n", true},
		{"leading-space", "  www.example.com", true},
		{"trailing-dot", "www.example.com.", false},
		{"at-sign", "user@evil.com", false},
		{"null-byte", "www.example.com\x00", false},
		{"embedded-null", "www.exa\x00mple.com", false},
		{"space-inside", "www.exa mple.com", false},
		{"leading-dash-label", "-lead.example.com", false},
		{"nodot", "localhost", false},
		{"punycode", "xn--e1afmkfd.example.com", true},
		{"over-253", long, false},
		{"tab-inside", "www.exa\tmple.com", false},
	}
	for _, tc := range cases {
		m.mu.Lock()
		m.rawURIs = nil
		m.bodies = nil
		m.mu.Unlock()
		r := req(t, app, http.MethodPut, "/v1/integrations/github/repos/widgets/pages", "acme",
			map[string]any{"cname": tc.cname})
		sawPut := false
		var putBody string
		for i, u := range m.rawURIs {
			if strings.HasPrefix(u, "/repos/acme-gh/widgets/pages") {
				sawPut = true
				putBody = m.bodies[i]
			}
		}
		if tc.accept {
			if r.Code != http.StatusOK {
				t.Errorf("%s: accepted domain want 200, got %d (%s)", tc.name, r.Code, r.Body)
			}
			if !sawPut {
				t.Errorf("%s: accepted domain must reach GitHub PUT", tc.name)
			}
			// The value that reached GitHub must carry no raw CR/LF/NUL/space.
			if strings.ContainsAny(putBody, "\r\n\x00") {
				t.Errorf("%s: raw control char reached GitHub body: %q", tc.name, putBody)
			}
		} else {
			if r.Code != http.StatusBadRequest {
				t.Errorf("%s: malformed cname want 400, got %d (%s)", tc.name, r.Code, r.Body)
			}
			if sawPut {
				t.Errorf("%s: malformed cname must NEVER be PUT to GitHub (body %q)", tc.name, putBody)
			}
		}
	}
}

// TestAdv_RegexNewlineAnchor documents Go's default `$` behavior: it must NOT match
// before a trailing newline (else a bare-LF domain would slip the raw regex, even
// though the handler's TrimSpace also guards it).
func TestAdv_RegexNewlineAnchor(t *testing.T) {
	for _, s := range []string{"www.example.com\n", "www.example.com\r\n", "www.example.com\r"} {
		if validCustomDomain(s) {
			t.Errorf("validCustomDomain(%q) must be false (regex $ anchored to end-of-text)", s)
		}
	}
}

// ── Attack 4: token leakage ───────────────────────────────────────────────────

// TestAdv_TokenNeverInURLorError proves the installation token rides ONLY the
// Authorization header (never the URL/query), and a GitHub 403/500 surfaces no token.
func TestAdv_TokenNeverInURLorError(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusUnauthorized} {
		m := newAdvMock(t)
		m.grants["tok-777"] = []map[string]any{
			{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
		}
		m.pagesCode = code
		m.pagesBody = `{"message":"denied"}`
		withGithubApp(t, m.srv)
		app := newApp(t, newKMS(t))
		connectOrg(t, "acme", "777", "acct")

		r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
		if strings.Contains(string(r.Body), "tok-777") || strings.Contains(string(r.Body), "ghs_") {
			t.Fatalf("code %d: response leaked token: %s", code, r.Body)
		}
		// Token must have been sent to GitHub in the Authorization header, never the URL.
		if len(m.authSeen) == 0 || m.authSeen[0] != "Bearer tok-777" {
			t.Fatalf("code %d: expected Bearer tok-777 in Authorization, saw %v", code, m.authSeen)
		}
		for _, u := range m.rawURIs {
			if strings.Contains(u, "tok-777") || strings.Contains(u, "ghs_") {
				t.Fatalf("code %d: token appeared in the request URL: %q", code, u)
			}
		}
	}
}

// TestAdv_TokenEchoedByGitHub is the defense-in-depth probe: IF GitHub itself echoed
// the Authorization header into an error body, does the handler pass it through? This
// documents whether the no-leak property depends on GitHub not reflecting the token.
func TestAdv_TokenEchoedByGitHub(t *testing.T) {
	m := newAdvMock(t)
	m.grants["tok-777"] = []map[string]any{
		{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
	}
	m.pagesCode = http.StatusUnprocessableEntity // 422 → surfaced verbatim by pagesErr
	m.echoAuth = true
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acct")

	r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil)
	leaked := strings.Contains(string(r.Body), "tok-777")
	t.Logf("GitHub-echoes-token → handler leaks it in surfaced 4xx body: %v (status %d) body=%s", leaked, r.Code, r.Body)
	// Not a hard failure: GitHub does not echo the Authorization header in practice.
	// This test exists to make the dependency explicit for the report.
}

// ── Attack 5: confused deputy / silent success ────────────────────────────────

func TestAdv_ConfusedDeputy(t *testing.T) {
	// (a) PUT with only unknown fields → 400, no GitHub PUT.
	{
		m := newAdvMock(t)
		m.grants["tok-777"] = []map[string]any{
			{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
		}
		withGithubApp(t, m.srv)
		app := newApp(t, newKMS(t))
		connectOrg(t, "acme", "777", "acct")
		r := req(t, app, http.MethodPut, "/v1/integrations/github/repos/widgets/pages", "acme",
			map[string]any{"foo": "bar", "unknown": 123, "buildType": ""})
		if r.Code != http.StatusBadRequest {
			t.Errorf("unknown-only PUT want 400, got %d (%s)", r.Code, r.Body)
		}
		for _, u := range m.rawURIs {
			if strings.HasPrefix(u, "/repos/") {
				t.Errorf("unknown-only PUT must not reach GitHub, hit %q", u)
			}
		}
	}
	// (b) POST with neither branch nor workflow AND empty default branch → 400.
	{
		m := newAdvMock(t)
		m.grants["tok-777"] = []map[string]any{
			{"name": "empty", "full_name": "acme-gh/empty", "default_branch": "", "clone_url": "https://github.com/acme-gh/empty.git"},
		}
		withGithubApp(t, m.srv)
		app := newApp(t, newKMS(t))
		connectOrg(t, "acme", "777", "acct")
		r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/empty/pages", "acme", map[string]any{})
		if r.Code != http.StatusBadRequest {
			t.Errorf("empty-default-branch POST want 400, got %d (%s)", r.Code, r.Body)
		}
		for _, u := range m.rawURIs {
			if strings.HasPrefix(u, "/repos/") {
				t.Errorf("no-source POST must not reach GitHub, hit %q", u)
			}
		}
	}
	// (c) DELETE + build on a NEVER-enabled repo (GitHub 404) → honest 404, not fake 200.
	{
		m := newAdvMock(t)
		m.grants["tok-777"] = []map[string]any{
			{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
		}
		m.pagesCode = http.StatusNotFound
		withGithubApp(t, m.srv)
		app := newApp(t, newKMS(t))
		connectOrg(t, "acme", "777", "acct")
		if r := req(t, app, http.MethodDelete, "/v1/integrations/github/repos/widgets/pages", "acme", nil); r.Code != http.StatusNotFound {
			t.Errorf("DELETE never-enabled want 404, got %d (%s)", r.Code, r.Body)
		}
		if r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/widgets/pages/builds", "acme", nil); r.Code != http.StatusNotFound {
			t.Errorf("BUILD never-enabled want 404, got %d (%s)", r.Code, r.Body)
		}
	}
	// (d) Every verb on an ungranted repo → 404, ZERO GitHub Pages call.
	{
		m := newAdvMock(t)
		m.grants["tok-777"] = []map[string]any{
			{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
		}
		withGithubApp(t, m.srv)
		app := newApp(t, newKMS(t))
		connectOrg(t, "acme", "777", "acct")
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			r := req(t, app, method, "/v1/integrations/github/repos/ungranted/pages", "acme", map[string]any{"branch": "main"})
			if r.Code != http.StatusNotFound {
				t.Errorf("%s ungranted want 404, got %d (%s)", method, r.Code, r.Body)
			}
		}
		r := req(t, app, http.MethodPost, "/v1/integrations/github/repos/ungranted/pages/builds", "acme", nil)
		if r.Code != http.StatusNotFound {
			t.Errorf("build ungranted want 404, got %d (%s)", r.Code, r.Body)
		}
		for _, u := range m.rawURIs {
			if strings.HasPrefix(u, "/repos/") {
				t.Errorf("ungranted repo must never reach GitHub Pages, hit %q", u)
			}
		}
	}
}

// TestAdv_GrantRevokedBetweenListAndCall models the TOCTOU: the grant list still
// contains widgets, but the Pages call itself now 404s (access revoked). The handler
// must surface an honest 404 — GitHub is the final authority on the token's scope, so
// the race yields no cross-tenant access.
func TestAdv_GrantRevokedBetweenListAndCall(t *testing.T) {
	m := newAdvMock(t)
	m.grants["tok-777"] = []map[string]any{
		{"name": "widgets", "full_name": "acme-gh/widgets", "default_branch": "main", "clone_url": "https://github.com/acme-gh/widgets.git"},
	}
	m.pagesCode = http.StatusNotFound // revoked at the API even though still listed
	withGithubApp(t, m.srv)
	app := newApp(t, newKMS(t))
	connectOrg(t, "acme", "777", "acct")
	if r := req(t, app, http.MethodGet, "/v1/integrations/github/repos/widgets/pages", "acme", nil); r.Code != http.StatusNotFound {
		t.Fatalf("revoked-after-list want honest 404, got %d (%s)", r.Code, r.Body)
	}
	_ = fmt.Sprint // keep fmt imported if unused paths change
}
