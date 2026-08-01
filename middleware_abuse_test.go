package cloud

// The abuse gate's specification. Four groups, each answering one question a
// reviewer will ask:
//
//	POLICY OUTCOMES — does each verdict produce the right wire answer?
//	FAIL POLICY     — does an absent/erroring scorer allow ordinary traffic and
//	                  refuse a privileged grant?
//	TENANT SCOPING  — can one org's traffic reach, move or read another's?
//	AGENCY          — is a customer's automation told apart from a bad bot, and
//	                  is it told apart from FACTS rather than from a header?
//
// Every test drives the middleware through a real zip app and a real request, so
// what is asserted is what a client would see.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// abuseApp mounts the gate in front of a trivial handler, with a fresh sensor and
// a policy store forced to mode. A nil store means "no policy configured", which
// must behave as shadow.
//
// The gate reads the identity boundary's OWN attestation, never a header, so the
// app installs `attest` in front of it — the stand-in for SanitizeIdentity. That
// is not test scaffolding around a gap: it is the property under test. An app
// WITHOUT it (abuseAppUnattested below) must see every caller as anonymous no
// matter what headers arrive.
func abuseApp(t *testing.T, mode string) (*zip.App, *edge.Traffic) {
	t.Helper()
	return abuseAppWith(t, mode, withBoundary)
}

// The two deployment shapes a middleware can find itself in. They are the same
// app apart from whether the identity boundary ran, which is exactly the fact the
// gate must turn on.
const (
	withBoundary    = true
	withoutBoundary = false
)

// attest is the test's identity boundary: it mints the principal from the same
// headers SanitizeIdentity mints it from, and parks it where only a boundary can.
func attest() zip.Handler {
	return func(c *zip.Ctx) error {
		if u := c.User(); u != "" {
			principal.Mint(c, principal.Principal{Org: c.Org(), User: u})
		} else {
			principal.Mint(c, principal.Principal{})
		}
		return c.Next()
	}
}

// abuseAppWith mounts the gate with or without the identity boundary in front of
// it. Without it — the shape a hand-written plugin process has — every caller is
// anonymous by construction, whatever headers arrive.
func abuseAppWith(t *testing.T, mode string, boundary bool) (*zip.App, *edge.Traffic) {
	t.Helper()
	tr := edge.NewTraffic()
	deps := Deps{GatewayPolicy: staticModeStore(t, mode), Traffic: tr}
	app := zip.New(zip.Config{})
	if boundary {
		app.Use(attest())
	}
	app.Use(AbuseGate(deps, tr))
	h := func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) }
	app.Get("/v1/models", h)
	app.Get("/v1/models/:name", h)
	app.Post("/v1/iam/mint-user-keys", h)
	app.Get("/v1/kms/orgs/acme/secrets/db", h)
	app.Get("/health", h)
	app.Get("/v1/risk/decisions", h)
	// A route that refuses, so the 401/403 feedback loop can be exercised.
	app.Get("/v1/denied", func(c *zip.Ctx) error {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "no"})
	})
	return app, tr
}

// staticModeStore builds a real edge.Store armed to mode — on the platform row
// (the anonymous lane) and on each tenant these tests drive, BY NAME. Mode does
// not inherit: arming one scope arms exactly that scope, which is the property
// TestPolicy_ArmingThePlatformRowDoesNotArmTenants pins. Using the real store
// rather than a fake keeps the test honest about how Mode actually resolves.
func staticModeStore(t *testing.T, mode string) *edge.Store {
	t.Helper()
	s, err := edge.New(t.TempDir(), "admin", edge.Policy{Mode: mode})
	if err != nil {
		t.Fatalf("edge.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if mode != "" {
		for _, org := range []string{"acme", "globex"} {
			if _, err := s.Put(t.Context(), org, edge.Policy{Mode: mode}); err != nil {
				t.Fatalf("arm %s: %v", org, err)
			}
		}
	}
	return s
}

// call drives one request. org is the validated tenant (the header
// SanitizeIdentity mints); cred is the raw credential the caller presents; ip is
// the forwarded client address.
func abuseHit(app *zip.App, method, path, org, cred, ip string) *http.Response {
	req := httptest.NewRequest(method, path, nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		return &http.Response{StatusCode: 0}
	}
	return resp
}

func abuseVerdict(action string) RiskScorer {
	return func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{ID: "d-" + action, Action: action, Score: 0.9, Cause: "test"}, nil
	}
}

// ---------------------------------------------------------------- policy outcomes

func TestAbuseGate_EveryPolicyOutcome(t *testing.T) {
	cases := []struct {
		action string
		status int
		header string // a response header the outcome must carry
	}{
		{ActionAllow, 200, ""},
		{ActionReview, 200, ""}, // review summons a person; it does not stop traffic.
		{ActionChallenge, 401, "WWW-Authenticate"},
		{ActionRestrict, 429, "Retry-After"},
		{ActionBlock, 403, ""},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			resetScorer(t)
			SetRiskScorer(abuseVerdict(tc.action))
			app, _ := abuseApp(t, edge.ModeLive)

			resp := abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9")
			if resp.StatusCode != tc.status {
				t.Fatalf("%s → %d, want %d", tc.action, resp.StatusCode, tc.status)
			}
			if tc.header != "" && resp.Header.Get(tc.header) == "" {
				t.Fatalf("%s must carry a %s header", tc.action, tc.header)
			}
		})
	}
}

// A refusal names the decision it came from, so a customer can quote one id and
// support can fetch the whole judgement. It must NOT name the score, the rule or
// the features: a refusal that reads out the model is a refusal an attacker tunes
// against.
func TestAbuseGate_RefusalIsAppealableAndSaysNothingElse(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		return RiskVerdict{ID: "d-77", Action: ActionBlock, Score: 0.98, Cause: "peers>=8"}, nil
	})
	app, _ := abuseApp(t, edge.ModeLive)
	resp := abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9")

	body := abuseBody(t, resp)
	if !strings.Contains(body, "d-77") {
		t.Fatalf("a refusal must name its decision id so it can be appealed: %s", body)
	}
	for _, leak := range []string{"0.98", "peers", "score"} {
		if strings.Contains(body, leak) {
			t.Fatalf("a refusal must not read out the model (%q leaked): %s", leak, body)
		}
	}
}

// SHADOW IS THE DEFAULT AND IT REFUSES NOTHING. This is the test that stops a
// statistical judgement from silently starting to block an org's traffic.
func TestAbuseGate_ShadowNeverRefuses(t *testing.T) {
	for _, mode := range []string{"", edge.ModeShadow} {
		t.Run("mode="+mode, func(t *testing.T) {
			resetScorer(t)
			SetRiskScorer(abuseVerdict(ActionBlock))
			app, _ := abuseApp(t, mode)
			resp := abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9")
			if resp.StatusCode != 200 {
				t.Fatalf("shadow must not enforce: got %d, want 200", resp.StatusCode)
			}
		})
	}
}

// A held verdict is what makes an attack cost one screen instead of one per
// request — and it must lapse, so a false positive clears itself.
func TestAbuseGate_HoldsAVerdictSoTheScorerIsAskedOnce(t *testing.T) {
	resetScorer(t)
	asked := 0
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		asked++
		return RiskVerdict{ID: "d-1", Action: ActionBlock}, nil
	})
	app, _ := abuseApp(t, edge.ModeLive)
	for i := 0; i < 5; i++ {
		if got := abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9").StatusCode; got != 403 {
			t.Fatalf("request %d → %d, want 403", i, got)
		}
	}
	if asked != 1 {
		t.Fatalf("the scorer was asked %d times for one held verdict; want 1", asked)
	}
}

// -------------------------------------------------------------------- fail policy

func TestAbuseGate_FailsOpenForOrdinaryTrafficAndClosedForAGrant(t *testing.T) {
	broken := []struct {
		name    string
		install RiskScorer
	}{
		{"scorer absent", nil},
		{"scorer erroring", func(context.Context, string, RiskQuery) (RiskVerdict, error) {
			return RiskVerdict{}, errors.New("down")
		}},
	}
	for _, b := range broken {
		t.Run(b.name, func(t *testing.T) {
			resetScorer(t)
			SetRiskScorer(b.install)
			app, _ := abuseApp(t, edge.ModeLive)

			if got := abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "198.51.100.4").StatusCode; got != 200 {
				t.Fatalf("ordinary traffic must FAIL OPEN when the scorer is down: got %d, want 200", got)
			}
			if got := abuseHit(app, "POST", "/v1/iam/mint-user-keys", "acme", "sk-live-2", "198.51.100.4").StatusCode; got != 403 {
				t.Fatalf("minting a credential must FAIL CLOSED when the scorer is down: got %d, want 403", got)
			}
			if got := abuseHit(app, "GET", "/v1/kms/orgs/acme/secrets/db", "acme", "sk-live-3", "198.51.100.4").StatusCode; got != 403 {
				t.Fatalf("reading the key store must FAIL CLOSED when the scorer is down: got %d, want 403", got)
			}
		})
	}
}

// The fail-CLOSED branch is reachable only in live mode. An unarmed deployment —
// which is every deployment until an operator arms it — must not have its key
// store and its credential minting refused because a scorer was never installed.
// That would be an outage wearing a defense's clothes.
func TestAbuseGate_ShadowDoesNotFailClosedOnAGrant(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(nil)
	app, _ := abuseApp(t, edge.ModeShadow)
	if got := abuseHit(app, "POST", "/v1/iam/mint-user-keys", "acme", "sk-live-1", "198.51.100.4").StatusCode; got != 200 {
		t.Fatalf("an unarmed org must not be locked out of credential minting: got %d, want 200", got)
	}
}

// The gate must never call the scorer on the scorer's own surface: it runs
// in-process, so screening /v1/risk would re-enter it on its own answer.
func TestAbuseGate_NeverScreensTheScorersOwnSurface(t *testing.T) {
	resetScorer(t)
	asked := 0
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		asked++
		return RiskVerdict{ID: "d", Action: ActionBlock}, nil
	})
	app, _ := abuseApp(t, edge.ModeLive)
	for _, p := range []string{"/v1/risk/decisions", "/health"} {
		if got := abuseHit(app, "GET", p, "acme", "sk-live-1", "198.51.100.4").StatusCode; got != 200 {
			t.Fatalf("%s must be exempt: got %d", p, got)
		}
	}
	if asked != 0 {
		t.Fatalf("the scorer was asked %d times about exempt paths; want 0", asked)
	}
}

// A nil sensor must be a passthrough, exactly like every other gate on this path:
// an unwired deployment is never blocked.
func TestAbuseGate_UnwiredIsAPassthrough(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(abuseVerdict(ActionBlock))
	app := zip.New(zip.Config{})
	app.Use(AbuseGate(Deps{}, nil))
	app.Get("/v1/models", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	if got := abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "").StatusCode; got != 200 {
		t.Fatalf("an unwired gate must pass through: got %d", got)
	}
}

// ----------------------------------------------------------------- tenant scoping

// One org's traffic must not be able to reach, move or read another's. This is
// the property the whole product rests on.
func TestAbuseGate_TenantsAreIsolated(t *testing.T) {
	resetScorer(t)
	var askedFor []string
	SetRiskScorer(func(_ context.Context, org string, _ RiskQuery) (RiskVerdict, error) {
		askedFor = append(askedFor, org)
		if org == "acme" {
			return RiskVerdict{ID: "d-acme", Action: ActionBlock}, nil
		}
		return RiskVerdict{ID: "d-other", Action: ActionAllow}, nil
	})
	app, tr := abuseApp(t, edge.ModeLive)

	// acme is blocked, on its own credential, from its own address.
	if got := abuseHit(app, "GET", "/v1/models", "acme", "sk-acme", "203.0.113.1").StatusCode; got != 403 {
		t.Fatalf("acme → %d, want 403", got)
	}
	// globex, presenting the SAME credential string from the SAME address, is
	// untouched: the sensor keys on (org, credential), so acme's hold cannot reach
	// it and its counters are its own.
	if got := abuseHit(app, "GET", "/v1/models", "globex", "sk-acme", "203.0.113.1").StatusCode; got != 200 {
		t.Fatalf("globex → %d, want 200 — acme's verdict crossed the tenant boundary", got)
	}
	for _, org := range askedFor {
		if org != "acme" && org != "globex" {
			t.Fatalf("the scorer was asked about %q, which is neither caller's org", org)
		}
	}

	// And neither org's view can contain the other's rows.
	av := tr.View("acme", edge.ModeLive, abuseNow())
	gv := tr.View("globex", edge.ModeLive, abuseNow())
	if av.Org != "acme" || gv.Org != "globex" {
		t.Fatal("a view must report the org it was taken for")
	}
	if len(av.Callers) == 0 || len(gv.Callers) == 0 {
		t.Fatal("each org must see its own caller")
	}
	if av.Requests == 0 || gv.Requests == 0 {
		t.Fatal("each org must count its own requests")
	}
	if av.Requests != 1 || gv.Requests != 1 {
		t.Fatalf("counts leaked across tenants: acme=%d globex=%d, want 1 each", av.Requests, gv.Requests)
	}
}

// An anonymous caller has no tenant. Its traffic must not be attributed to, or
// able to move, any org's numbers.
func TestAbuseGate_AnonymousTrafficMovesNoTenant(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(abuseVerdict(ActionAllow))
	app, tr := abuseApp(t, edge.ModeLive)
	for i := 0; i < 5; i++ {
		abuseHit(app, "GET", "/v1/models", "", "", "198.51.100.77")
	}
	if v := tr.View("acme", edge.ModeLive, abuseNow()); v.Requests != 0 || len(v.Callers) != 0 {
		t.Fatalf("anonymous traffic reached a tenant's numbers: %+v", v)
	}
}

// ------------------------------------------------------------------------ agency

// The differentiator: a customer's automation and a bad bot are told apart by
// the credential we issued, never by anything the client says about itself.
func TestAbuseGate_ClassesAgentTrafficApartFromBots(t *testing.T) {
	resetScorer(t)
	var lanes []string
	SetRiskScorer(func(_ context.Context, _ string, q RiskQuery) (RiskVerdict, error) {
		lanes = append(lanes, q.Agency)
		return RiskVerdict{ID: "d", Action: ActionAllow}, nil
	})
	app, _ := abuseApp(t, edge.ModeLive)

	// A machine credential we issued: the agent lane, whatever the user-agent says.
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.5")
	if len(lanes) == 0 || lanes[0] != AgencyAgent {
		t.Fatalf("attributable machine traffic must be the agent lane, got %v", lanes)
	}

	// The same request claiming to be a browser is STILL the agent lane: we read
	// the credential, not the client's self-description.
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-acme")
	req.Header.Set("Authorization", "Bearer sk-live-2")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Chrome/126")
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatal(err)
	}
	if lanes[len(lanes)-1] != AgencyAgent {
		t.Fatalf("a user-agent string must not move a caller between lanes, got %q", lanes[len(lanes)-1])
	}
}

// -------------------------------------------------------------------- the sensor

// The 401/403 feedback loop: a credential that keeps failing to authenticate is
// how stuffing and a replayed stolen token look, and the count only exists after
// the handler has run.
func TestAbuseGate_CountsAuthFailuresAfterTheHandler(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(abuseVerdict(ActionAllow))
	app, tr := abuseApp(t, edge.ModeShadow)
	for i := 0; i < 3; i++ {
		abuseHit(app, "GET", "/v1/denied", "acme", "sk-live-1", "203.0.113.5")
	}
	v := tr.View("acme", edge.ModeShadow, abuseNow())
	if len(v.Callers) != 1 {
		t.Fatalf("want one caller, got %d", len(v.Callers))
	}
	if v.Callers[0].Failures != 3 {
		t.Fatalf("failures = %d, want 3", v.Callers[0].Failures)
	}
}

// A credential must never appear anywhere the sensor can be read from. The
// fingerprint is keyed with a per-process salt, so it cannot be tested against a
// candidate key off-box either.
func TestAbuseGate_NeverSurfacesACredential(t *testing.T) {
	resetScorer(t)
	SetRiskScorer(abuseVerdict(ActionAllow))
	app, tr := abuseApp(t, edge.ModeShadow)
	const secret = "sk-live-supersecret-value"
	abuseHit(app, "GET", "/v1/models", "acme", secret, "203.0.113.5")

	v := tr.View("acme", edge.ModeShadow, abuseNow())
	blob := fmt.Sprintf("%+v", v)
	if strings.Contains(blob, secret) || strings.Contains(blob, "supersecret") {
		t.Fatalf("the credential reached the traffic view: %s", blob)
	}
	if len(v.Callers) != 1 || v.Callers[0].Cred == "" {
		t.Fatalf("the caller must still be identifiable by fingerprint: %+v", v)
	}
	if got := Fingerprint(secret); got != v.Callers[0].Cred {
		t.Fatalf("the fingerprint must be stable within a process: %q vs %q", got, v.Callers[0].Cred)
	}
	if Fingerprint(secret) == Fingerprint(secret+"x") {
		t.Fatal("two credentials must not share a fingerprint")
	}
}

func abuseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b := make([]byte, 4096)
	n, _ := resp.Body.Read(b)
	return string(b[:n])
}

// nowish is the clock the sensor tests read. A single call site so a future move
// to an injected clock has one place to change.
func abuseNow() time.Time { return time.Now() }

// A hold must not buy a free minute at a time. When it lapses, the caller is
// asked about AGAIN on its next request — whether or not the local pattern still
// looks unusual, because the scorer sees things the sensor cannot: prior accounts
// on a device, a spend curve, a chargeback. Waiting for a rolling minute of
// request counts to re-trip would wait forever.
func TestAbuseGate_ALapsedHoldForcesTheQuestionAgain(t *testing.T) {
	resetScorer(t)
	asked := 0
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		asked++
		return RiskVerdict{ID: "d-1", Action: ActionBlock}, nil
	})
	tr := edge.NewTraffic()
	deps := Deps{GatewayPolicy: staticModeStore(t, edge.ModeLive), Traffic: tr}
	app := zip.New(zip.Config{})
	app.Use(attest())
	app.Use(AbuseGate(deps, tr))
	app.Get("/v1/models", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })

	sig := edge.Signal{Org: "acme", Cred: Fingerprint("sk-live-1"), IP: "203.0.113.9"}

	// First request screens and is held.
	if got := abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9").StatusCode; got != 403 {
		t.Fatalf("first request → %d, want 403", got)
	}
	if asked != 1 {
		t.Fatalf("asked %d times, want 1", asked)
	}
	// Held: no further questions while it is in force.
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9")
	if asked != 1 {
		t.Fatalf("a held verdict must not re-ask: asked %d", asked)
	}

	// Lapse it. The pattern is now unremarkable — a handful of requests, one
	// path, no failures, no peers — so nothing but the lapse can force the ask.
	// Backdate it rather than sleeping: a hold whose deadline is already in the
	// past is unambiguously lapsed, where a millisecond one is a race.
	tr.Hold(sig, edge.Hold{Action: ActionBlock}, time.Second, time.Now().Add(-time.Minute))
	if _, ok := tr.Held(sig, time.Now()); ok {
		t.Fatal("the hold did not lapse")
	}
	before := asked
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9")
	if asked != before+1 {
		t.Fatalf("a lapsed hold must force the question again: asked %d, want %d", asked, before+1)
	}

	// And once the scorer allows, the lapsed hold is dropped so it stops forcing
	// a screen on every request thereafter.
	SetRiskScorer(func(context.Context, string, RiskQuery) (RiskVerdict, error) {
		asked++
		return RiskVerdict{ID: "d-ok", Action: ActionAllow}, nil
	})
	tr.Hold(sig, edge.Hold{Action: ActionBlock}, time.Second, time.Now().Add(-time.Minute))
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9") // re-judged: allow, hold released
	cleared := asked
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.9")
	if asked != cleared {
		t.Fatalf("a released hold must stop forcing screens: asked %d, want %d", asked, cleared)
	}
}

// ------------------------------------------------------- the forgery regressions

// REGRESSION — the differentiator must not be settable by the caller. The lane
// used to turn on `c.Org() != "" || c.User() != ""`, and BOTH disjuncts are
// header reads: X-Org-Id survives the boundary on the anonymous path by design,
// and in a process with no boundary installed nothing strips either header. So
// two headers plus an sk--shaped string that never validated moved a bad bot into
// the AGENT lane — the lane whose entire meaning is "a credential WE minted, to a
// named tenant, that we can revoke".
//
// Here the gate runs with NO identity boundary in front of it, which is exactly
// the plugin-process shape. Every header the caller can write is written, and it
// must still be judged as anonymous.
func TestAbuseGate_HeadersAloneDoNotBuyTheAgentLane(t *testing.T) {
	resetScorer(t)
	var lanes, orgs []string
	SetRiskScorer(func(_ context.Context, org string, q RiskQuery) (RiskVerdict, error) {
		lanes, orgs = append(lanes, q.Agency), append(orgs, org)
		return RiskVerdict{ID: "d", Action: ActionAllow}, nil
	})
	app, tr := abuseAppWith(t, edge.ModeLive, withoutBoundary)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-Org-Id", "acme")       // forged: no boundary minted it
	req.Header.Set("X-User-Id", "u-acme")    // forged
	req.Header.Set("X-User-IsAdmin", "true") // forged
	req.Header.Set("Authorization", "Bearer sk-live-forged")
	req.Header.Set("X-Forwarded-For", "203.0.113.66")
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatal(err)
	}

	if len(lanes) == 0 {
		t.Fatal("the anonymous lane must still be screened — it is the lane a bad bot calls from")
	}
	if lanes[0] == AgencyAgent {
		t.Fatal("two headers moved an unattributable caller into the agent lane")
	}
	// And the tenant is not the one the header named: an unverified caller must
	// not be able to write into — or read the posture of — someone else's org.
	if orgs[0] != "" {
		t.Fatalf("the scorer was asked about %q, which no boundary attested", orgs[0])
	}
	if v := tr.View("acme", edge.ModeLive, abuseNow()); v.Requests != 0 || len(v.Callers) != 0 {
		t.Fatalf("a forged X-Org-Id wrote into acme's sensor state: %+v", v)
	}
}

// The same request WITH a boundary in front is the agent lane — so the test above
// is pinning the forgery, not merely a gate that never classes anything.
func TestAbuseGate_AVerifiedMachineCredentialIsTheAgentLane(t *testing.T) {
	resetScorer(t)
	var lanes, orgs []string
	SetRiskScorer(func(_ context.Context, org string, q RiskQuery) (RiskVerdict, error) {
		lanes, orgs = append(lanes, q.Agency), append(orgs, org)
		return RiskVerdict{ID: "d", Action: ActionAllow}, nil
	})
	app, _ := abuseAppWith(t, edge.ModeLive, withBoundary)
	abuseHit(app, "GET", "/v1/models", "acme", "sk-live-1", "203.0.113.5")

	if len(lanes) == 0 || lanes[0] != AgencyAgent {
		t.Fatalf("a verified machine credential must be the agent lane, got %v", lanes)
	}
	if orgs[0] != "acme" {
		t.Fatalf("the scorer was asked about %q, want the attested acme", orgs[0])
	}
}

// REGRESSION — one capital letter used to flip fail-closed to fail-OPEN. The
// grant list is compared with strings.HasPrefix against the raw c.Path(), while
// fiber routes case-INSENSITIVELY and ignores a trailing slash: `/V1/KMS/...`
// reaches the key store and missed every prefix, so the scorer's silence ALLOWED
// a read of the key store instead of refusing it.
func TestAbuseGate_AGrantIsAGrantHoweverItIsSpelled(t *testing.T) {
	for _, path := range []string{
		"/v1/kms/orgs/acme/secrets/db",
		"/V1/KMS/orgs/acme/secrets/db",
		"/v1/KMS/orgs/acme/secrets/db",
		"/v1/kms/orgs/acme/secrets/db/",
	} {
		t.Run(path, func(t *testing.T) {
			resetScorer(t) // no scorer: the fail policy answers.
			app, _ := abuseApp(t, edge.ModeLive)
			got := abuseHit(app, "GET", path, "acme", "sk-live-1", "203.0.113.9").StatusCode
			if got != 403 {
				t.Fatalf("%s → %d; an unscored read of the key store must be refused", path, got)
			}
		})
	}
}
