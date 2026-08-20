package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubSlackAPI stands in for Slack's Web API and points the REGISTERED slack
// provider at it for the duration of one test.
//
// It swaps the registry entry rather than mutating a URL the catalog already
// read. The catalog builds each Provider from its spec at init, so a test that
// reassigned `slackWebAPIBase` afterwards would change a string nothing looks at
// again and quietly exercise the real slack.com. Rebuilding the provider from a
// spec is also the honest test: it drives `oauthProvider` — the ONE token
// exchange every connector uses — instead of a copy written for the test.
func stubSlackAPI(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.FormValue("code") == "goodcode" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":           true,
				"access_token": "xoxb-real-token",
				"scope":        "chat:write,users:read",
				"bot_user_id":  "U0BOT",
				"team":         map[string]string{"id": "T0TEAM", "name": "Acme Inc"},
			})
			return
		}
		// Slack refuses AT 200 — the case slackFinish exists to catch.
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_code"})
	})
	mux.HandleFunc("/auth.revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if r.FormValue("token") == "badtoken" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "revoked": true})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	sp := slackSpec()
	sp.tokenURL = ts.URL + "/oauth.v2.access"
	sp.revokeURL = ts.URL + "/auth.revoke"

	orig := registry["slack"]
	registry["slack"] = oauthProvider(sp)
	t.Cleanup(func() { registry["slack"] = orig })
}
