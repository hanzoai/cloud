package featureflags

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient points a Client at a fake Insights /flags server and installs it as the
// process seam, restoring the prior seam on cleanup.
func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	prev := mounted
	c := &Client{base: base, token: "phc_test", distinctID: "test", hc: &http.Client{Timeout: 2 * time.Second}, ttl: time.Minute}
	mounted = c
	t.Cleanup(func() { mounted = prev })
	return c
}

// flagServer returns an httptest server serving a controllable /flags response.
func flagServer(t *testing.T, body func() map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body())
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnvFallbackAndDefault(t *testing.T) {
	// No Insights configured -> env fallback, then literal default.
	prev := mounted
	mounted = &Client{} // not configured
	t.Cleanup(func() { mounted = prev })

	// waitlist_open default is "true" (no env set).
	t.Setenv("WAITLIST_OPEN", "")
	if !Bool("waitlist_open") {
		t.Fatalf("waitlist_open default should be true")
	}
	// env override beats the literal default.
	t.Setenv("WAITLIST_OPEN", "false")
	if Bool("waitlist_open") {
		t.Fatalf("waitlist_open env=false should win")
	}
	// int default.
	if Int("waitlist_access_capacity") != 0 {
		t.Fatalf("capacity default should be 0")
	}
	t.Setenv("WAITLIST_ACCESS_CAPACITY", "250")
	if Int("waitlist_access_capacity") != 250 {
		t.Fatalf("capacity env should be 250, got %d", Int("waitlist_access_capacity"))
	}
	// network id read-only default.
	if Int("network_id_localnet") != 1337 {
		t.Fatalf("localnet id default should be 1337")
	}
	// unknown key is safe.
	if Bool("nope") || Int("nope") != 0 || String("nope") != "" {
		t.Fatalf("unknown key must be zero-valued")
	}
}

func TestEvaluateFromInsights(t *testing.T) {
	srv := flagServer(t, func() map[string]any {
		return map[string]any{
			"featureFlags":        map[string]any{"waitlist_open": true, "public_signup": false},
			"featureFlagPayloads": map[string]any{"waitlist_access_capacity": 500},
		}
	})
	newTestClient(t, srv.URL)

	if !Bool("waitlist_open") {
		t.Fatalf("insights waitlist_open should be true")
	}
	if Bool("public_signup") {
		t.Fatalf("insights public_signup should be false")
	}
	if got := Int("waitlist_access_capacity"); got != 500 {
		t.Fatalf("insights payload capacity should be 500, got %d", got)
	}
}

func TestInsightsOverridesEnv(t *testing.T) {
	srv := flagServer(t, func() map[string]any {
		return map[string]any{"featureFlags": map[string]any{"waitlist_open": false}}
	})
	newTestClient(t, srv.URL)
	t.Setenv("WAITLIST_OPEN", "true") // env says open, insights says closed -> insights wins
	if Bool("waitlist_open") {
		t.Fatalf("insights (false) must override env (true)")
	}
}

func TestHotApplyAfterTTL(t *testing.T) {
	open := true
	srv := flagServer(t, func() map[string]any {
		return map[string]any{"featureFlags": map[string]any{"waitlist_open": open}}
	})
	c := newTestClient(t, srv.URL)

	if !Bool("waitlist_open") {
		t.Fatalf("first read should be true")
	}
	// Flip the flag at the source, then expire the cache -> next read reflects it (hot-apply).
	open = false
	c.mu.Lock()
	c.snap.at = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	if Bool("waitlist_open") {
		t.Fatalf("after TTL expiry the flip must be visible (hot-apply)")
	}
}

func TestGracefulDegradeOnError(t *testing.T) {
	// Unreachable base -> fetch fails -> env/default, never a panic.
	prev := mounted
	mounted = &Client{base: "http://127.0.0.1:0", token: "phc_test", hc: &http.Client{Timeout: 200 * time.Millisecond}, ttl: time.Minute}
	t.Cleanup(func() { mounted = prev })
	t.Setenv("WAITLIST_OPEN", "false")
	if Bool("waitlist_open") {
		t.Fatalf("unreachable insights must fall back to env=false")
	}
}

func TestBoard(t *testing.T) {
	srv := flagServer(t, func() map[string]any {
		return map[string]any{"featureFlags": map[string]any{"waitlist_open": true}}
	})
	newTestClient(t, srv.URL)
	t.Setenv("INSIGHTS_APP_URL", "https://insights.hanzo.ai/")
	t.Setenv("INSIGHTS_PROJECT_ID", "7")

	b := Board()
	if b.Engine != "insights" || !b.Configured {
		t.Fatalf("board engine/configured wrong: %+v", b)
	}
	if b.ManageURL != "https://insights.hanzo.ai/project/7/feature_flags" {
		t.Fatalf("manage url wrong: %s", b.ManageURL)
	}
	var open, netID *SwitchView
	for i := range b.Switches {
		switch b.Switches[i].Key {
		case "waitlist_open":
			open = &b.Switches[i]
		case "network_id_mainnet":
			netID = &b.Switches[i]
		}
	}
	if open == nil || open.Source != "insights" || open.Value != "true" {
		t.Fatalf("waitlist_open switch view wrong: %+v", open)
	}
	if netID == nil || !netID.ReadOnly || netID.Value != "1" {
		t.Fatalf("network_id_mainnet should be read-only default 1: %+v", netID)
	}
}

func TestParsers(t *testing.T) {
	if b, ok := asBool(json.RawMessage("true")); !b || !ok {
		t.Fatal("asBool true")
	}
	if b, ok := asBool(json.RawMessage(`"on"`)); !b || !ok {
		t.Fatal("asBool variant on")
	}
	if _, ok := asBool(json.RawMessage("")); ok {
		t.Fatal("asBool empty not present")
	}
	if n, ok := asInt(json.RawMessage("500")); n != 500 || !ok {
		t.Fatal("asInt number")
	}
	if n, ok := asInt(json.RawMessage(`"42"`)); n != 42 || !ok {
		t.Fatal("asInt string number")
	}
	if s, ok := asString(json.RawMessage(`"hi"`)); s != "hi" || !ok {
		t.Fatal("asString")
	}
}
