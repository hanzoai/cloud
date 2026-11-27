package provider

// back_test.go pins where an OAuth return lands: the Hanzo page that started the
// connect when it named one of brand.Returns, and the console otherwise. Every
// address a connect names is hostile until checked, so the refusals are driven
// through the same connect and callback a browser takes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// devIntegrations is the hanzo.ai page that holds Settings → Integrations: the
// app at `/`, in Dev, at the builder's settings address.
const devIntegrations = "https://hanzo.ai/?at=-/settings/integrations"

// devProjects is Dev's Projects on hanzo.ai, at its path: its GitHub banner
// starts a connect and names this page, as Sync names /dev/sync.
const devProjects = "https://hanzo.ai/dev/projects"

// hostileReturns are addresses a connect may be handed that must never become a
// Location: other hosts, other schemes, protocol-relative and userinfo tricks,
// look-alike hosts, a customer's subdomain, a port, and header injection.
var hostileReturns = []string{
	"https://evil.com/",
	"https://evil.com/?at=-/settings/integrations",
	"javascript:alert(document.cookie)",
	"JavaScript:alert(1)",
	"data:text/html,<script>alert(1)</script>",
	"//evil.com/",
	"///evil.com/",
	`\\evil.com\`,
	"/relative/path",
	"http://hanzo.ai/",
	"https://hanzo.ai@evil.com/",
	"https://evil.com@hanzo.ai/",
	"https://user:pass@hanzo.ai/",
	`https://hanzo.ai\@evil.com/`,
	"https://hanzo.ai.evil.com/",
	"https://evilhanzo.ai/",
	"https://hanzo.ai:8443/",
	"https://acme.hanzo.app/",
	"https://hanzo.ai//evil.com/",
	"https://hanzo.ai/%2F%2Fevil.com",
	"https://hanzo.ai/\r\nSet-Cookie: x=1",
	"https://hanzo.ai/" + strings.Repeat("a", 4096),
	"https://hanzo.ai/" + strings.Repeat("a", 512),
	" https://evil.com/",
	// Dot segments a browser folds into "//evil.com".
	"https://console.hanzo.ai/.//evil.com",
	"https://hanzo.ai/x/..//evil.com",
	"https://hanzo.ai/%2e//evil.com",
	"https://hanzo.ai/%2e%2e//evil.com",
	"https://hanzo.ai/a//b",
	// Another brand's console is not this deployment's to return to.
	"https://console.lux.cloud/connectors",
	// An app address carrying an address of its own.
	"https://hanzo.ai/?at=-/settings/integrations%3Fgrant%3Dx",
	"https://hanzo.ai/?at=javascript:alert(1)",
	"https://hanzo.ai/?at=//2130706433",
	// A page of an allowed origin that is not a return page: one that redirects
	// off the origin with the query kept, a run or project view, the bare app.
	"https://hanzo.ai/docs",
	"https://hanzo.ai/docs?at=-/settings/integrations",
	"https://hanzo.ai/?at=sess_9f2c1e7a4b8d0e3f6a5b2c1d0e9f8a7b",
	"https://hanzo.ai/?at=acme-shop",
	"https://hanzo.ai/?at=-/settings/integrations&at=-/settings/keys",
	"https://hanzo.ai/",
	"https://console.hanzo.ai/",
	"https://console.hanzo.ai/connectors/slack",
	"https://console.hanzo.ai/connectors/",
	// The registered page, spelled with what an exact match refuses.
	"http://hanzo.ai/?at=-/settings/integrations",
	"https://hanzo.ai:443/?at=-/settings/integrations",
	"https://HANZO.AI/?at=-/settings/integrations",
	"https://x@hanzo.ai/?at=-/settings/integrations",
	// Dev's paths: only the pages that start a connect, never a project or run
	// under them, nor Dev itself.
	"https://hanzo.ai/dev",
	"https://hanzo.ai/dev/",
	"https://hanzo.ai/dev/projects/",
	"https://hanzo.ai/dev/projects/acme/circle",
	"https://hanzo.ai/dev/run/sess_9f2c1e7a4b8d0e3f6a5b2c1d0e9f8a7b",
	"https://hanzo.ai/dev/sync/x",
	"https://hanzo.ai/dev/projects/..//evil.com",
}

// TestGitHubReturnsToThePageThatAsked is the bug: Settings → Integrations on
// hanzo.ai starts the connect, and GitHub's return must land the person back on
// that page with the grant, not on the console.
func TestGitHubReturnsToThePageThatAsked(t *testing.T) {
	_, app := withHub(t)
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false,
		map[string]string{"return": devIntegrations})
	if r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", r.Code, r.Body)
	}
	cb := asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(stateOf(t, r.Body))+"&code=code-octo-alice", "", "", false, nil)
	if cb.Code != http.StatusFound {
		t.Fatalf("callback want 302, got %d (%s)", cb.Code, cb.Body)
	}
	u, err := url.Parse(cb.Location)
	if err != nil {
		t.Fatalf("Location %q: %v", cb.Location, err)
	}
	q := u.Query()
	if u.Scheme != "https" || u.Host != "hanzo.ai" || u.Path != "/" || q.Get("at") != "-/settings/integrations" {
		t.Fatalf("callback must return to %s, got %q", devIntegrations, cb.Location)
	}
	if q.Get("complete") != "github" || q.Get("grant") == "" {
		t.Fatalf("the page must be handed the grant, got %q", cb.Location)
	}
	// The page completes it as the person who started it.
	done := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/complete", "acme", "alice", false,
		map[string]string{"grant": q.Get("grant")})
	if got := userOut(t, done); done.Code != http.StatusOK || !got.Connected || got.Login != "octo-alice" {
		t.Fatalf("complete from the page: %d %+v", done.Code, got)
	}
}

// TestGitHubReturnsToDevProjects is the bug at its path: Projects on hanzo.ai is
// /dev/projects, not an `?at=` address, and its banner's Connect GitHub came back
// to the console's /connectors, which signs the person in to the console instead
// of finishing the connect. The return must land on Projects with the grant.
func TestGitHubReturnsToDevProjects(t *testing.T) {
	_, app := withHub(t)
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false,
		map[string]string{"return": devProjects})
	if r.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", r.Code, r.Body)
	}
	cb := asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(stateOf(t, r.Body))+"&code=code-octo-alice", "", "", false, nil)
	u, err := url.Parse(cb.Location)
	if err != nil || cb.Code != http.StatusFound {
		t.Fatalf("callback want 302 to a page, got %d %q", cb.Code, cb.Location)
	}
	q := u.Query()
	if u.Scheme != "https" || u.Host != "hanzo.ai" || u.Path != "/dev/projects" {
		t.Fatalf("callback must return to %s, got %q", devProjects, cb.Location)
	}
	if q.Get("complete") != "github" || q.Get("grant") == "" {
		t.Fatalf("the page must be handed the grant, got %q", cb.Location)
	}
}

// TestGitHubRefusesAForeignReturn: an address off the list is not an error the
// person sees — the flow still connects — it is simply never a Location. The
// return falls back to the console, exactly as a connect that named nothing.
func TestGitHubRefusesAForeignReturn(t *testing.T) {
	_, app := withHub(t)
	for _, back := range append([]string{""}, hostileReturns...) {
		r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false,
			map[string]string{"return": back})
		if r.Code != http.StatusOK {
			t.Fatalf("return %q: connect want 200, got %d (%s)", back, r.Code, r.Body)
		}
		cb := asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(stateOf(t, r.Body))+"&code=code-octo-alice", "", "", false, nil)
		if !strings.HasPrefix(cb.Location, "https://platform.hanzo.ai/connectors?complete=github&grant=") {
			t.Errorf("return %q: Location %q, want the platform console", back, cb.Location)
		}
		if strings.ContainsAny(cb.Location, "\r\n") {
			t.Errorf("return %q: Location carries CR/LF", back)
		}
	}
}

// TestAReturnCannotPlantItsOwnAnswer: the page's own query is kept, but the keys
// the callback answers with are the callback's. A return naming its own grant or
// verdict reaches the page with the real one only.
func TestAReturnCannotPlantItsOwnAnswer(t *testing.T) {
	_, app := withHub(t)
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false,
		map[string]string{"return": devIntegrations + "&grant=planted&connected=github&error=github&reason=x&account=y#frag"})
	cb := asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(stateOf(t, r.Body))+"&code=code-octo-alice", "", "", false, nil)
	u, err := url.Parse(cb.Location)
	if err != nil || u.Host != "hanzo.ai" {
		t.Fatalf("Location %q: %v", cb.Location, err)
	}
	q := u.Query()
	if g := q["grant"]; len(g) != 1 || g[0] == "planted" {
		t.Fatalf("grant must be the callback's alone, got %v", g)
	}
	for _, k := range []string{"connected", "error", "reason", "account"} {
		if q.Has(k) {
			t.Errorf("planted %s reached the page: %q", k, cb.Location)
		}
	}
	if u.Fragment != "" {
		t.Errorf("fragment kept: %q", cb.Location)
	}
}

// TestAFailedGitHubReturnLandsOnThePage: once the state is proven, a refusal on
// GitHub's side returns to the page that asked, so it can say what happened.
func TestAFailedGitHubReturnLandsOnThePage(t *testing.T) {
	_, app := withHub(t)
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false,
		map[string]string{"return": devIntegrations})
	cb := asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state="+url.QueryEscape(stateOf(t, r.Body))+"&error=access_denied", "", "", false, nil)
	u, _ := url.Parse(cb.Location)
	if u == nil || u.Host != "hanzo.ai" || u.Query().Get("error") != "github" || u.Query().Get("reason") != "authorization denied" {
		t.Fatalf("a denied authorize must return to the page with the reason, got %q", cb.Location)
	}
	// A state that never verifies names no page, so it can only go to the console.
	cb = asPerson(t, app, http.MethodGet, "/v1/provider/github/user/callback?state=forged.deadbeef&code=code-x", "", "", false, nil)
	if !strings.HasPrefix(cb.Location, "https://platform.hanzo.ai/connectors?error=github") {
		t.Fatalf("a forged state must return to the platform console, got %q", cb.Location)
	}
}

// TestProviderReturnsToThePageThatAsked is the org flow: Slack connected from
// Integrations on hanzo.ai lands back there, success and failure alike.
func TestProviderReturnsToThePageThatAsked(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	app := newApp(t, newKMS(t))
	for _, tc := range []struct{ code, key, value string }{
		{"goodcode", "connected", "slack"},
		{"badcode", "error", "slack"},
	} {
		res := cfReq(t, app, http.MethodPost, "/v1/provider/slack/connect", "acme", true, map[string]string{"return": devIntegrations})
		if res.Code != http.StatusOK {
			t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
		}
		var out struct {
			AuthorizeURL string `json:"authorizeUrl"`
		}
		_ = json.Unmarshal(res.Body, &out)
		au, _ := url.Parse(out.AuthorizeURL)
		cb := req(t, app, http.MethodGet, "/v1/provider/slack/callback?code="+tc.code+"&state="+url.QueryEscape(au.Query().Get("state")), "", nil)
		u, err := url.Parse(cb.Location)
		if err != nil || cb.Code != http.StatusFound || u.Host != "hanzo.ai" || u.Query().Get("at") != "-/settings/integrations" {
			t.Fatalf("%s: callback must return to the page, got %d %q", tc.code, cb.Code, cb.Location)
		}
		if u.Query().Get(tc.key) != tc.value {
			t.Fatalf("%s: want %s=%s, got %q", tc.code, tc.key, tc.value, cb.Location)
		}
	}
	// A foreign return falls back to the console.
	res := cfReq(t, app, http.MethodPost, "/v1/provider/slack/connect", "acme", true, map[string]string{"return": "https://evil.com/"})
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	_ = json.Unmarshal(res.Body, &out)
	au, _ := url.Parse(out.AuthorizeURL)
	cb := req(t, app, http.MethodGet, "/v1/provider/slack/callback?code=goodcode&state="+url.QueryEscape(au.Query().Get("state")), "", nil)
	if !strings.HasPrefix(cb.Location, "https://platform.hanzo.ai/connectors?connected=slack") {
		t.Fatalf("a foreign return must land on the platform console, got %q", cb.Location)
	}
}

// TestBackToNamesOnlyRegisteredPages is the check itself: an address names one
// of this deployment's return pages — however its path and `at` are escaped, and
// whatever else its query and fragment carry — or nothing.
func TestBackToNamesOnlyRegisteredPages(t *testing.T) {
	for raw, want := range map[string]string{
		devIntegrations: devIntegrations,
		"https://hanzo.ai/?at=-%2Fsettings%2Fintegrations":                                 devIntegrations,
		"https://hanzo.ai?at=-/settings/integrations":                                      devIntegrations,
		"https://hanzo.ai/?chat=c1&at=-/settings/integrations&grant=planted&reason=x#frag": devIntegrations,
		"https://console.hanzo.ai/connectors":                                              "https://console.hanzo.ai/connectors",
		"https://console.hanzo.ai/%63onnectors?error=github&state=s&code=c":                "https://console.hanzo.ai/connectors",
		"https://platform.hanzo.ai/connectors":                                             "https://platform.hanzo.ai/connectors",
		"https://hanzo.ai/?at=-/sync":                                                      "https://hanzo.ai/?at=-/sync",
		"https://hanzo.ai/?at=-%2Fsync&connected=github":                                   "https://hanzo.ai/?at=-/sync",
		devProjects: devProjects,
		"https://hanzo.ai/dev/projects?q=circle#frag":    devProjects,
		"https://hanzo.ai/dev/%70rojects":                devProjects,
		"https://hanzo.ai/dev/sync":                      "https://hanzo.ai/dev/sync",
		"https://hanzo.ai/dev/sync?connected=github&x=1": "https://hanzo.ai/dev/sync",
	} {
		if got := backTo(raw); got != want {
			t.Errorf("backTo(%q) = %q, want %q", raw, got, want)
		}
	}
	for _, raw := range hostileReturns {
		if got := backTo(raw); got != "" {
			t.Errorf("backTo(%q) = %q, want refused", raw, got)
		}
	}
}

// TestOldNoncesTableGainsBack: a store created before oauth_nonces had `back`
// opens, and a return address round-trips through it.
func TestOldNoncesTableGainsBack(t *testing.T) {
	// SQLite only, by nature (see TestOldConnectionsTableIsBroughtToTheDeclaredShape):
	// the old file is built by hand and the store put over its handle.
	t.Setenv(locus.Env, "")
	pre, err := locus.Open(namespace.System(), subsystem, t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = pre.Close() }()
	if _, err := pre.Exec(`CREATE TABLE oauth_nonces (nonce TEXT PRIMARY KEY, org TEXT NOT NULL, provider TEXT NOT NULL, created_at INTEGER NOT NULL);
INSERT INTO oauth_nonces VALUES ('inflight','acme','slack',1700000000);`); err != nil {
		t.Fatalf("seed old shape: %v", err)
	}
	s := storeOver(t, pre)
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate an old database: %v", err)
	}
	ctx := context.Background()
	if back, ok, err := s.ConsumeNonce(ctx, "inflight", "acme", "slack"); err != nil || !ok || back != "" {
		t.Fatalf("an in-flight nonce from before must consume with no return: %q %v %v", back, ok, err)
	}
	if err := s.PutNonce(ctx, "n", "acme", "github-user", devIntegrations); err != nil {
		t.Fatalf("put: %v", err)
	}
	if back, ok, err := s.ConsumeNonce(ctx, "n", "acme", "github-user"); err != nil || !ok || back != devIntegrations {
		t.Fatalf("return must round-trip: %q %v %v", back, ok, err)
	}
	if back, ok, _ := s.ConsumeNonce(ctx, "n", "acme", "github-user"); ok || back != "" {
		t.Fatalf("a consumed nonce answers nothing: %q %v", back, ok)
	}
}

// TestARefusedCodeIsSaidThroughTheEdge: GitHub refusing the parked code reaches
// the page as the platform's own sentence. A 502 is answered by the edge with its
// own HTML page and no CORS headers, so the page could only say "Failed to fetch".
func TestARefusedCodeIsSaidThroughTheEdge(t *testing.T) {
	_, app := withHub(t)
	r := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/connect", "acme", "alice", false,
		map[string]string{"return": devIntegrations})
	grant := callbackGrant(t, app, stateOf(t, r.Body), "not-a-github-code")
	done := asPerson(t, app, http.MethodPost, "/v1/provider/github/user/complete", "acme", "alice", false,
		map[string]string{"grant": grant})
	if done.Code != http.StatusFailedDependency || !strings.Contains(string(done.Body), "connect again") {
		t.Fatalf("a refused code want 424 with its reason, got %d (%s)", done.Code, done.Body)
	}
}

// TestConsoleURLDefaultsToBrandCustomerConsole proves that when CLOUD_CONSOLE_URL
// is unset, OAuth callbacks (including CLI `hanzo provider github user connect`)
// land on the active brand's customer console (https://platform.hanzo.ai for
// hanzo, never console.hanzo.ai / admin-console whose admin org rejects social
// federation with "federation is not permitted for this application").
func TestConsoleURLDefaultsToBrandCustomerConsole(t *testing.T) {
	t.Setenv(consoleURLEnv, "")
	t.Setenv("CLOUD_BRAND", "hanzo")
	if got := consoleURL(); got != "https://platform.hanzo.ai" {
		t.Fatalf("hanzo default consoleURL = %q, want https://platform.hanzo.ai", got)
	}

	t.Setenv("CLOUD_BRAND", "lux")
	if got := consoleURL(); got != "https://console.lux.cloud" {
		t.Fatalf("lux default consoleURL = %q, want https://console.lux.cloud", got)
	}

	t.Setenv(consoleURLEnv, "https://custom.console.example/")
	if got := consoleURL(); got != "https://custom.console.example" {
		t.Fatalf("explicit CLOUD_CONSOLE_URL = %q, want https://custom.console.example", got)
	}
}
