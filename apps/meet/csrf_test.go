package meet

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestAmbientCookieMintNeedsCSRF pins the anti-CSRF gate on meet's one write.
//
// getToken is not a read dressed as a POST. It MINTS A CREDENTIAL to a live
// conversation, and it was reachable with nothing but an ambient session cookie:
// callerToken (middleware_identity.go) accepts five cookie names, and admits
// selects the IAM lane on principal.Minted BEFORE it ever looks at an
// Authorization header. So a signed-in tab could mint — and so could a page on
// any other origin the deployment's CORS policy reflects, which is *.hanzo.ai
// with credentials, a wildcard covering hosts that serve arbitrary user content.
// The consequence is not data disclosure, it is a stranger in a colleague's call.
//
// The gate is method-discriminating and installed once on the group, so this also
// pins what must NOT change: the lobby read passes untouched, and a
// header-authenticated caller — every API client, the gateway-fronted path, and
// the native SPA at meet.hanzo.ai, which is cross-origin and bearer-only and
// sends no cookie at all — is unaffected.
//
// Mirrors apps/tracker/typed_wire_test.go TestAmbientCookieWritesNeedCSRF,
// because it is the same gate over the same account.RequireCSRF.
func TestAmbientCookieMintNeedsCSRF(t *testing.T) {
	app := mount(t, "team-secret", "APIkey", "apisecret")

	// A request the way a signed-in tab makes it: a session COOKIE and no
	// Authorization header. That combination is exactly what account's
	// ambientCookieAuth recognises, and the only one the gate acts on.
	browser := func(t *testing.T, method, path, csrf string, body any) int {
		t.Helper()
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		rq := httptest.NewRequest(method, path, r)
		if body != nil {
			rq.Header.Set("Content-Type", "application/json")
		}
		rq.Header.Set("Cookie", "hanzo_iam_token=session-value")
		rq.Header.Set("X-Org-Id", "acme")
		rq.Header.Set("X-User-Id", "u_acme")
		if csrf != "" {
			rq.Header.Set("X-CSRF-Token", csrf)
		}
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	t.Run("the mint is refused without a token", func(t *testing.T) {
		got := browser(t, http.MethodPost, "/v1/meet/getToken", "",
			map[string]any{"roomName": "acme_standup", "participantName": "Ada"})
		if got != http.StatusForbidden {
			t.Errorf("POST /v1/meet/getToken with a session cookie and no CSRF token = %d, want 403 — "+
				"a cross-site page can mint a join token for a room it was never admitted to", got)
		}
	})

	t.Run("a forged token is refused", func(t *testing.T) {
		got := browser(t, http.MethodPost, "/v1/meet/getToken", "not-a-real-token",
			map[string]any{"roomName": "acme_standup", "participantName": "Ada"})
		if got != http.StatusForbidden {
			t.Errorf("mint with a forged CSRF token = %d, want 403", got)
		}
	})

	t.Run("the refusal happens BEFORE the handler", func(t *testing.T) {
		// 403 must be the GATE's, not the handler's own admission refusal — those
		// are different numbers here (mint answers 401 for "not admitted" and 503
		// when unconfigured), so a 403 can only have come from the gate. If the
		// gate ran after the handler it would be an audit trail, not a control.
		if got := browser(t, http.MethodPost, "/v1/meet/getToken", "", nil); got != http.StatusForbidden {
			t.Errorf("an empty body still reaches the handler first: %d", got)
		}
	})

	t.Run("the lobby read is not gated", func(t *testing.T) {
		// A GET changes nothing, and requiring a token to load the lobby would
		// mean fetching one before the page that fetches one.
		if got := browser(t, http.MethodGet, "/v1/meet/session", "", nil); got == http.StatusForbidden {
			t.Errorf("GET /v1/meet/session from a signed-in tab = 403 — reads change nothing")
		}
	})

	t.Run("health is not gated", func(t *testing.T) {
		if got := browser(t, http.MethodGet, "/v1/meet/health", "", nil); got == http.StatusForbidden {
			t.Errorf("GET /v1/meet/health = 403 — a probe carries no CSRF token and never will")
		}
	})

	t.Run("a header-authenticated caller is unaffected", func(t *testing.T) {
		// Not CSRF-able: a cross-site page cannot set Authorization. Gating it
		// would break every API client, the gateway path, and the native SPA —
		// which is cross-origin and therefore sends no cookie at all — for no gain.
		rq := httptest.NewRequest(http.MethodPost, "/v1/meet/getToken",
			bytes.NewReader([]byte(`{"roomName":"acme_standup","participantName":"Ada"}`)))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("Authorization", "Bearer some-iam-token")
		resp, err := app.Test(rq, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("bearer mint: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		// It will be refused on its MERITS (that token admits nobody), but it must
		// not be refused for a missing CSRF token — 403 is the gate's number.
		if resp.StatusCode == http.StatusForbidden {
			t.Errorf("a bearer-authenticated mint = 403 — the gate is running on the header lane")
		}
	})

	t.Run("the routes did not move", func(t *testing.T) {
		// The gate arrived by putting these on a Group, and a group is a prefix:
		// a leaf path written with the prefix still on it would double it and
		// every published client would 404. Asserted by ASKING, not by reading.
		for _, p := range []string{"/v1/meet/getToken", "/v1/meet/session", "/v1/meet/health"} {
			m := http.MethodGet
			if p == "/v1/meet/getToken" {
				m = http.MethodPost
			}
			rq := httptest.NewRequest(m, p, nil)
			resp, err := app.Test(rq, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatalf("%s %s: %v", m, p, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				t.Errorf("%s %s = 404 — the group prefix was doubled", m, p)
			}
		}
	})
}
