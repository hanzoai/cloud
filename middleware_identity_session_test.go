package cloud

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// A first-party session cookie presented WITHOUT a bearer must resolve ANONYMOUS —
// never a principal, never a panic — so the session bridge can never widen auth.
//
// This used to be conditional on Beego's global session manager being nil, which
// is how the retired Casdoor iam-v1 embed stored sessions. That embed is gone and
// IAM v2 is zip-native, so nothing populates that global and sessionAccessToken is
// now unconditionally "". The property under test is unchanged and is now
// unconditional too: no skip, no framework global, just the guarantee.
func TestSessionBridge_NoSessionManager_IsAnonymous(t *testing.T) {
	// The cookie name the retired embed used. Any opaque session id must be
	// ignored regardless of what it is called.
	const name = "beegosessionID"

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
