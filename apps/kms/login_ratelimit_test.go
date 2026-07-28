package kms_test

// V1: the public /v1/kms/auth/login broker is unauthenticated and fans out to IAM,
// so it is rate-limited PER SOURCE IP to blunt credential-stuffing-through-cloud and
// outbound-connection pinning. These tests prove the limiter is WIRED to the route
// (429 past the per-window ceiling) and — the security-load-bearing part — that a
// rate-limited request NEVER reaches IAM (the 429 short-circuits before the outbound
// exchange, so the broker cannot be used to hammer IAM).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestLoginBrokerRateLimited(t *testing.T) {
	var iamHits int64
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&iamHits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "iam-token", "expires_in": 3600, "token_type": "Bearer",
		})
	}))
	defer iam.Close()

	cfg := baseCfg(t, masterKeyB64(t))
	cfg.IAMIssuer = iam.URL // login broker → {issuer}/v1/iam/oauth/token
	app, _ := newApp(t, cfg)

	body := `{"clientId":"maxpower-platform-kms","clientSecret":"shh"}`

	// fiber's in-process Test presents a single synthetic source IP, so all requests
	// share one per-IP bucket. Drive well past the per-window ceiling (loginRateLimit,
	// currently 60) and assert the tail is rate-limited. Counts aren't asserted
	// exactly (only that BOTH outcomes occur) so the test can't flake on the window.
	const attempts = 80
	var got200, got429 int
	for i := 0; i < attempts; i++ {
		resp := do(t, app, "POST", "/v1/kms/auth/login", "", body, false, nil)
		switch resp.StatusCode {
		case 200:
			got200++
		case 429:
			got429++
		default:
			t.Fatalf("attempt %d: unexpected status %d (want 200 or 429)", i, resp.StatusCode)
		}
	}
	if got200 == 0 {
		t.Fatal("no login succeeded before the ceiling — limiter mis-wired (blocking legitimate traffic)")
	}
	if got429 == 0 {
		t.Fatalf("no login was rate-limited across %d attempts — the limiter is not applied to /v1/kms/auth/login", attempts)
	}
	// The load-bearing assertion: every 429 short-circuited BEFORE the outbound IAM
	// exchange, so IAM was hit exactly once per ALLOWED login and never for a denied
	// one. If a rate-limited request reached IAM, iamHits would exceed got200.
	if hits := atomic.LoadInt64(&iamHits); hits != int64(got200) {
		t.Fatalf("IAM hit %d times but only %d logins were allowed — a rate-limited request reached IAM (broker is still a fan-out amplifier)", hits, got200)
	}
}
