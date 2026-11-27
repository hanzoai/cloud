package cloud

// The dead-card regression: a refusal a handler PROPAGATES must reach the client
// as the status the refusal chose, carrying a code the console can branch on.
// Before ErrorHandler every one of these rendered 500 {"error":"…"} unless the
// handler remembered to call Denied — which is why an unfunded chat drew "something
// went wrong" where a paywall belonged.
//
// They drive real requests through the zip/fiber stack against an app configured
// exactly as Serve configures it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/fault"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/supply"
	"github.com/zap-proto/zip"
)

// newErrApp serves ONE route that returns err — the propagating handler.
func newErrApp(err error) *zip.App {
	app := zip.New(zip.Config{ErrorHandler: ErrorHandler})
	app.Raw(http.MethodGet, "/v1/thing", func(c *zip.Ctx) error { return err })
	return app
}

func getThing(t *testing.T, err error) (int, string) {
	t.Helper()
	resp, rerr := newErrApp(err).Test(httptest.NewRequest(http.MethodGet, "/v1/thing", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if rerr != nil {
		t.Fatalf("Test request: %v", rerr)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(body)
}

// TestPropagatedRefusalsKeepTheirStatusAndCode is the whole defect: each of these
// errors already NAMES a status, and every one of them used to render 500.
func TestPropagatedRefusalsKeepTheirStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		// The money sentinels — statuses spelled as errors. This is the unfunded
		// chat/console surface.
		{"out of funds", meter.ErrInsufficientBalance, http.StatusPaymentRequired, "insufficient_balance"},
		{"over a scope cap", meter.ErrSpendCapExceeded, http.StatusPaymentRequired, "spend_cap_exceeded"},
		// Wrapped, because a handler almost always adds context on the way up.
		{"wrapped out of funds", fmt.Errorf("chat: %w", meter.ErrInsufficientBalance), http.StatusPaymentRequired, "insufficient_balance"},
		// An upstream that chose a status keeps it, and gains a code where it sent
		// none. This is the shape zip rebuilds from a plane callee (call.go
		// remoteError) and the shape Guard refuses with.
		{"upstream 403", zip.ErrForbidden("admin required"), http.StatusForbidden, "forbidden"},
		{"upstream 401", zip.ErrUnauthorized("sign in"), http.StatusUnauthorized, "unauthorized"},
		{"upstream 402", zip.Errorf(http.StatusPaymentRequired, "upgrade to continue"), http.StatusPaymentRequired, "payment_required"},
		// An upstream's OWN code always wins over the status-derived one.
		{
			"upstream 402 with a code", &zip.HTTPError{Status: http.StatusPaymentRequired, Code: "card_required", Msg: "add a card"},
			http.StatusPaymentRequired, "card_required",
		},
		// Wrapped across a hop: errors.As still finds it, so the number survives.
		{
			"wrapped upstream 402", fmt.Errorf("commerce: %w", &zip.HTTPError{Status: http.StatusPaymentRequired, Code: "insufficient_balance", Msg: "no funds"}),
			http.StatusPaymentRequired, "insufficient_balance",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := getThing(t, tc.err)
			if status != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", status, tc.status, body)
			}
			if !strings.Contains(body, `"code":"`+tc.code+`"`) {
				t.Fatalf("body %s missing code %q", body, tc.code)
			}
		})
	}
}

// TestWithoutTheClientAnUnfundedRequestIs500 is the bug, kept executable: zip's
// DEFAULT handler reads only a *zip.HTTPError, so the sentinel that means 402
// renders as an internal fault with no code on it — the dead card. It is why
// Serve installs ErrorHandler, and it fails the moment anyone stops.
func TestWithoutTheClientAnUnfundedRequestIs500(t *testing.T) {
	app := zip.New(zip.Config{}) // no ErrorHandler: zip's own
	app.Raw(http.MethodGet, "/v1/thing", func(c *zip.Ctx) error { return meter.ErrInsufficientBalance })
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/thing", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("default handler status = %d, want 500 — if this changed, the client may no longer be needed", resp.StatusCode)
	}
	if status, _ := getThing(t, meter.ErrInsufficientBalance); status != http.StatusPaymentRequired {
		t.Fatalf("with the client status = %d, want 402", status)
	}
}

// TestGenuineFaultsStayInternal: the client must not launder a fault into something
// a client is invited to retry or pay for. 500 is the ABSENCE of a decision.
func TestGenuineFaultsStayInternal(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("boom")},
		{"typed 500", zip.ErrInternal("store unavailable")},
		{"wrapped typed 500", fmt.Errorf("read: %w", zip.ErrInternal("store unavailable"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := getThing(t, tc.err)
			if status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body %s)", status, body)
			}
			if strings.Contains(body, `"code":"payment_required"`) || strings.Contains(body, `"code":"forbidden"`) {
				t.Fatalf("a fault rendered as an actionable refusal: %s", body)
			}
		})
	}
}

// TestOutagesAreNotRefusals holds the fail-closed line. An unreachable peer comes
// back as zip's own 502 for the call; reading that as a refusal would answer the
// money wire with a transport message where "Billing temporarily unavailable"
// belongs, and would tell a console to draw a paywall for an outage.
func TestOutagesAreNotRefusals(t *testing.T) {
	unreachable := fmt.Errorf("gate: commerce unreachable: %w",
		zip.Errorf(http.StatusBadGateway, "zip: call finance_authorize at /run/commerce.sock: connection refused"))

	// The money wire answers its ONE unknown-balance shape, never the transport's.
	if he := Denial(unreachable, ""); he.Status != http.StatusServiceUnavailable || he.Code != "balance_unavailable" {
		t.Fatalf("denial(unreachable) = (%d,%q), want (503,\"balance_unavailable\")", he.Status, he.Code)
	}
	// The renderer keeps the upstream's own 5xx and adds no actionable code to it.
	status, body := getThing(t, unreachable)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", status, body)
	}
	if strings.Contains(body, `"code":"`) {
		t.Fatalf("an outage gained a refusal code: %s", body)
	}
}

// TestAProviderBudgetStopIsItsOwnClass: a handler that propagates a provider's
// budget stop — bare, or wrapped on its way up — answers budget_exhausted naming
// the layer, never the bare 500 an undecided error gets nor a code that reads as
// capacity, sign-in or the caller's own money.
func TestAProviderBudgetStopIsItsOwnClass(t *testing.T) {
	stop := &supply.Exhausted{Provider: "cloudflare", Layer: supply.DurableObjects, Upstream: 429,
		Msg: "Exceeded the account's spending cap for Durable Objects."}
	for _, err := range []error{stop, fmt.Errorf("durable object call: %w", stop)} {
		status, body := getThing(t, err)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (body %s)", status, body)
		}
		var got map[string]any
		if jerr := json.Unmarshal([]byte(body), &got); jerr != nil {
			t.Fatalf("body is not JSON: %v (%s)", jerr, body)
		}
		if got["code"] != "budget_exhausted" || got["layer"] != "durable_objects" || got["provider"] != "cloudflare" {
			t.Fatalf("body = %s, want code budget_exhausted, layer durable_objects, provider cloudflare", body)
		}
	}
	// A capacity 503 stays a capacity 503: it gains no budget code.
	if _, body := getThing(t, zip.Errorf(http.StatusServiceUnavailable, "overloaded")); strings.Contains(body, "budget_exhausted") {
		t.Fatalf("a capacity 503 rendered as a budget stop: %s", body)
	}
}

// TestRefusalIsNotMutatedByRendering: refused returns a COPY, so rendering a
// process-wide sentinel cannot rewrite the value every later caller reads.
func TestRefusalIsNotMutatedByRendering(t *testing.T) {
	upstream := &zip.HTTPError{Status: http.StatusForbidden, Msg: "admin required"}
	if _, _ = getThing(t, upstream); upstream.Code != "" {
		t.Fatalf("rendering wrote Code=%q back onto the caller's error", upstream.Code)
	}
}

// TestDenialKeepsAnUpstreamStatus: denial and ErrorHandler read ONE classifier, so
// the money wire no longer re-decides a 402 commerce already chose into its 503
// "balance unknown" — which is the same dead card with a different number.
func TestDenialKeepsAnUpstreamStatus(t *testing.T) {
	he := Denial(fmt.Errorf("gate: %w", &zip.HTTPError{Status: http.StatusPaymentRequired, Code: "insufficient_balance", Msg: "no funds"}), "")
	if he.Status != http.StatusPaymentRequired || he.Code != "insufficient_balance" {
		t.Fatalf("denial = (%d,%q), want (402,\"insufficient_balance\")", he.Status, he.Code)
	}
	// Its own fallback is unchanged: an undecidable gate is still 503, never free.
	if he := Denial(errors.New("dial tcp: connection refused"), ""); he.Status != http.StatusServiceUnavailable || he.Code != "balance_unavailable" {
		t.Fatalf("denial(unknown) = (%d,%q), want (503,\"balance_unavailable\")", he.Status, he.Code)
	}
}

// TestUnauthorizedNamesTheScheme: a 401 carries WWW-Authenticate (RFC 9110
// §11.6.1), so a client can tell which credential would have been accepted, and
// names the resource metadata on the host it called (RFC 9728 §5.1,
// hanzo-inc/cloud#420). A handler that issued its own challenge keeps it.
func TestUnauthorizedNamesTheScheme(t *testing.T) {
	resp, err := newErrApp(zip.ErrUnauthorized("no key")).Test(httptest.NewRequest(http.MethodGet, "/v1/thing", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got, want := resp.Header.Get("WWW-Authenticate"), `Bearer resource_metadata="https://example.com/.well-known/oauth-protected-resource"`; got != want {
		t.Errorf("401 WWW-Authenticate = %q, want %q", got, want)
	}

	app := zip.New(zip.Config{ErrorHandler: ErrorHandler})
	app.Raw(http.MethodGet, "/v1/thing", func(c *zip.Ctx) error {
		c.SetHeader("WWW-Authenticate", `Bearer error="step_up_required"`)
		return zip.ErrUnauthorized("step up")
	})
	resp, err = app.Test(httptest.NewRequest(http.MethodGet, "/v1/thing", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("WWW-Authenticate"); got != `Bearer error="step_up_required"` {
		t.Errorf("handler's own challenge was replaced: %q", got)
	}

	resp, err = newErrApp(zip.ErrForbidden("admin required")).Test(httptest.NewRequest(http.MethodGet, "/v1/thing", nil), zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("WWW-Authenticate"); got != "" {
		t.Errorf("403 carries a challenge %q; only a 401 asks for credentials", got)
	}
}

// TestUnauthorizedLinksTheKeysPageOnItsBrand: a caller nobody identified is sent
// to the page that mints a key, on the console of the brand it called. A 401 an
// upstream coded for itself (a step-up) is about something else and carries no
// key page, and a brand that runs no console is sent nowhere.
func TestUnauthorizedLinksTheKeysPageOnItsBrand(t *testing.T) {
	for _, tc := range []struct {
		host string
		err  error
		want string
	}{
		{"api.hanzo.ai", zip.ErrUnauthorized("no key"), "https://platform.hanzo.ai/api-keys"},
		{"api.lux.cloud", zip.ErrUnauthorized("no key"), "https://console.lux.cloud/api-keys"},
		{"api.zoo.cloud", zip.ErrUnauthorized("no key"), "https://console.zoo.cloud/api-keys"},
		{"api.pars.network", zip.ErrUnauthorized("no key"), ""},
		{"api.hanzo.ai", &zip.HTTPError{Status: http.StatusUnauthorized, Code: "step_up_required", Msg: "step up"}, ""},
		{"api.hanzo.ai", zip.ErrForbidden("admin required"), ""},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
		req.Host = tc.host
		resp, err := newErrApp(tc.err).Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Keys string `json:"keysUrl"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: body is not JSON: %v", tc.host, err)
		}
		if body.Keys != tc.want {
			t.Errorf("%s %v: keysUrl = %q, want %q", tc.host, tc.err, body.Keys, tc.want)
		}
	}
}

// TestRefusalsAnAgentClearsNameItsManifest: an agent that meets a refusal it can
// clear by itself reads, beside the page a person would open, its brand's manifest
// (`agent`) and, where money clears it, the API that takes a deposit (`topup`).
// The manifest is the brand's that was called and nobody else's; a host no brand
// claims is told nothing; a refusal no agent action clears carries neither.
func TestRefusalsAnAgentClearsNameItsManifest(t *testing.T) {
	const hanzo, lux = "https://hanzoskills.com/hanzo.md", "https://lux.network/llms.txt"
	for _, tc := range []struct {
		name, host   string
		err          error
		status       int
		agent, topup string
	}{
		{"no key", "api.hanzo.ai", zip.ErrUnauthorized("no key"), 401, hanzo, ""},
		{"no key on Lux", "api.lux.cloud", zip.ErrUnauthorized("no key"), 401, lux, ""},
		{"no key on a brand with no manifest", "api.pars.network", zip.ErrUnauthorized("no key"), 401, "", ""},
		{"no key on an unclaimed host", "acme.example", zip.ErrUnauthorized("no key"), 401, "", ""},
		{"step-up", "api.hanzo.ai", &zip.HTTPError{Status: 401, Code: "step_up_required", Msg: "step up"}, 401, "", ""},
		{"out of funds", "api.hanzo.ai", meter.ErrInsufficientBalance, 402, hanzo, TopupPath},
		{"out of funds on Lux", "api.lux.cloud", fmt.Errorf("chat: %w", meter.ErrInsufficientBalance), 402, lux, TopupPath},
		{"upstream out of funds", "api.hanzo.ai", &zip.HTTPError{Status: 402, Code: "insufficient_balance", Msg: "no funds"}, 402, hanzo, TopupPath},
		{"free allowance spent", "api.hanzo.ai", &zip.HTTPError{Status: 402, Code: "allowance_spent", Msg: "spent"}, 402, hanzo, TopupPath},
		{"uncoded 402", "api.hanzo.ai", zip.Errorf(402, "pay"), 402, hanzo, TopupPath},
		{"spend cap", "api.hanzo.ai", meter.ErrSpendCapExceeded, 402, hanzo, ""},
		{"rate window", "api.hanzo.ai", &zip.HTTPError{Status: 429, Code: "rate_limited", Msg: "slow down"}, 429, hanzo, ""},
		{"forbidden", "api.hanzo.ai", zip.ErrForbidden("admin required"), 403, "", ""},
		{"not found", "api.hanzo.ai", zip.ErrNotFound("no such thing"), 404, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
			req.Host = tc.host
			resp, err := newErrApp(tc.err).Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, raw)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.status, raw)
			}
			agent, _ := body["agent"].(string)
			topup, _ := body["topup"].(string)
			if agent != tc.agent || topup != tc.topup {
				t.Errorf("agent %q topup %q, want %q %q: %s", agent, topup, tc.agent, tc.topup, raw)
			}
			if _, ok := body["agent"]; ok && tc.agent == "" {
				t.Errorf("an empty agent member rides the body: %s", raw)
			}
		})
	}
}

// TestTheMoneyWireCarriesTheSameMembers: the money wire nests its refusal under
// `error`, and what an agent reads rides there under the same names, from the one
// classifier both renderings share.
func TestTheMoneyWireCarriesTheSameMembers(t *testing.T) {
	he := Denial(meter.ErrInsufficientBalance, "api.hanzo.ai")
	body := DenyBody(he)["error"].(map[string]any)
	if body["agent"] != "https://hanzoskills.com/hanzo.md" || body["topup"] != TopupPath || body["code"] != "insufficient_balance" {
		t.Errorf("money wire body = %v", body)
	}
}

// A 401 is built from the request's own Host and never a forwarded one, even
// where the app trusts its proxies: the challenge names the host the request was
// sent to, and neither it nor the body names a host a caller wrote.
func TestTheChallengeReadsTheRequestsOwnHost(t *testing.T) {
	app := zip.New(zip.Config{ErrorHandler: ErrorHandler, TrustProxy: true,
		TrustedProxies: []string{"0.0.0.0/0", "::/0"}, ProxyHeader: "X-Forwarded-For"})
	app.Raw(http.MethodGet, "/v1/thing", func(c *zip.Ctx) error { return zip.ErrUnauthorized("no key") })
	for host, want := range map[string]string{
		"api.lux.network": `Bearer resource_metadata="https://api.lux.network/.well-known/oauth-protected-resource"`,
		"api.hanzo.ai":    `Bearer resource_metadata="https://api.hanzo.ai/.well-known/oauth-protected-resource"`,
		"localhost:8000":  `Bearer resource_metadata="http://localhost:8000/.well-known/oauth-protected-resource"`,
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
		req.Host = host
		req.Header.Set("X-Forwarded-Host", "red-probe.example")
		req.Header.Set("X-Forwarded-Proto", "http")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if got := resp.Header.Get("WWW-Authenticate"); got != want {
			t.Errorf("401 on %s: WWW-Authenticate = %q, want %q", host, got, want)
		}
		if strings.Contains(string(b), "red-probe") {
			t.Errorf("401 on %s names the forwarded host in its body: %s", host, b)
		}
	}
}

// EVERY NON-2XX HAS A KEY AND A REASON. Whatever the error — a refusal a handler
// chose, a 4xx, a fault nobody chose, an error with no sentence — the body carries
// `fingerprint` and a non-empty `detail`, and the key is the one the fault states.
//
// MUTATION: drop the Detail write in ErrorHandler and every case here loses its key.
func TestEveryErrorBodyCarriesItsFingerprintAndReason(t *testing.T) {
	for name, err := range map[string]error{
		"a refusal":       zip.ErrForbidden("no"),
		"a bare 404":      &zip.HTTPError{Status: http.StatusNotFound},
		"an undecided":    errors.New("boom"),
		"a typed 500":     zip.ErrInternal("store unavailable"),
		"an outage":       &zip.HTTPError{Status: http.StatusBadGateway, Msg: "peer away"},
		"no words at all": &zip.HTTPError{Status: http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := getThing(t, err)
			var got struct {
				Detail      string `json:"detail"`
				Fingerprint string `json:"fingerprint"`
			}
			if jerr := json.Unmarshal([]byte(body), &got); jerr != nil {
				t.Fatalf("%d %s: %v", status, body, jerr)
			}
			if len(got.Fingerprint) != 16 || strings.TrimSpace(got.Detail) == "" {
				t.Fatalf("%d: no key or no reason: %s", status, body)
			}
			want := fault.Request(http.MethodGet, "/v1/thing", status, "", err).Fingerprint()
			if he, ok := errors.AsType[*zip.HTTPError](err); ok {
				want = fault.Request(http.MethodGet, "/v1/thing", status, he.Code, err).Fingerprint()
			}
			if got.Fingerprint != want {
				t.Fatalf("the body's key %s is not the fault's %s", got.Fingerprint, want)
			}
		})
	}
}

// The sentinel a handler holds is never written to by rendering it.
func TestRenderingAnErrorLeavesItsSentinelAlone(t *testing.T) {
	sentinel := zip.ErrForbidden("no")
	getThing(t, sentinel)
	if len(sentinel.Detail) != 0 {
		t.Fatalf("rendering wrote %v into the sentinel's members", sentinel.Detail)
	}
}
