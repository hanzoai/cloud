package cloud

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/beego/v2/server/web"
	"github.com/zap-proto/zip"
)

// The EMBED session→principal bridge (validatedPrincipal → sessionAccessToken) must
// be a SAFE NO-OP whenever there is no in-process IAM session manager — i.e. on any
// binary that does not mount clients/iamsvc (and in these tests, where GlobalSessions
// is never initialised). A first-party-session cookie present WITHOUT a bearer and
// WITHOUT a session manager must resolve ANONYMOUS (never a principal, never a panic),
// so the bridge can never widen auth on a non-embed deployment.
func TestSessionBridge_NoSessionManager_IsAnonymous(t *testing.T) {
	if web.GlobalSessions != nil {
		t.Skip("a session manager is initialised in this process; the no-op path is not exercised")
	}
	name := web.BConfig.WebConfig.Session.SessionName
	if name == "" {
		name = "beegosessionID"
	}

	app, got := newIdentityApp(t, nil) // nil validator: no JWT path can validate either
	probe(t, app, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: name, Value: "deadbeefdeadbeefdeadbeefdeadbeef"})
	})

	if got.user != "" || got.org != "" || got.admin {
		t.Fatalf("session cookie without a session manager must be anonymous; got user=%q org=%q admin=%v",
			got.user, got.org, got.admin)
	}
}

// The same-origin gate (RED H3) admits the ambient-cookie session bridge ONLY for a
// same-origin request, so it can never be a CSRF vector even on a state-changing GET.
func TestSessionBridgeSameOrigin(t *testing.T) {
	var verdict bool
	app := zip.New(zip.Config{})
	app.Get("/probe", func(c *zip.Ctx) error {
		verdict = sessionBridgeSameOrigin(c)
		return c.JSON(http.StatusOK, map[string]bool{"ok": verdict})
	})
	check := func(name string, mutate func(*http.Request), want bool) {
		t.Helper()
		verdict = !want // poison so a skipped handler fails
		req := httptest.NewRequest(http.MethodGet, "http://console.hanzo.ai/probe", nil)
		if mutate != nil {
			mutate(req)
		}
		if _, err := app.Fiber().Test(req); err != nil {
			t.Fatalf("%s: test request: %v", name, err)
		}
		if verdict != want {
			t.Fatalf("%s: got %v, want %v", name, verdict, want)
		}
	}
	hdr := func(k, v string) func(*http.Request) { return func(r *http.Request) { r.Header.Set(k, v) } }

	check("sec-fetch same-origin", hdr("Sec-Fetch-Site", "same-origin"), true)
	check("sec-fetch none", hdr("Sec-Fetch-Site", "none"), true)
	check("sec-fetch cross-site", hdr("Sec-Fetch-Site", "cross-site"), false)
	check("sec-fetch same-site (sibling subdomain)", hdr("Sec-Fetch-Site", "same-site"), false)
	check("no signal at all", nil, true)
	check("origin cross-host", hdr("Origin", "https://evil.example.com"), false)
	check("origin same-host", hdr("Origin", "https://console.hanzo.ai"), true)
}
