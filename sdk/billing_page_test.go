package cloud

// A money refusal names the page that clears it, on the caller's own brand: Hanzo's
// checkout (hanzo.ai/pay/amount/card) for a refusal money clears, the console's
// page for a spend cap, and the console page of a brand with no checkout.
//
// Every 402 used to say "Add credits at console.hanzo.ai": the console's front door
// rather than its billing page, and Hanzo's front door on a Lux or Zoo host. A body
// that says what to do has to say where, and the where has to be the caller's.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

func TestBillingURLIsTheCallersConsole(t *testing.T) {
	for _, tc := range []struct{ host, code, want string }{
		// Hanzo takes credit at its checkout's amount step, the page every Add credits
		// in its apps opens, whichever of its hosts refused; its console is platform.
		{"api.hanzo.ai", "insufficient_balance", "https://hanzo.ai/pay/amount/card"},
		{"api.hanzo.ai", "allowance_spent", "https://hanzo.ai/pay/amount/card"},
		{"console.hanzo.ai", "payment_required", "https://hanzo.ai/pay/amount/card"},
		{"api.hanzo.ai", "spend_cap_exceeded", "https://platform.hanzo.ai/billing/budgets"},
		// A brand with no checkout of its own takes it on its console.
		{"api.lux.cloud", "insufficient_balance", "https://console.lux.cloud/billing/credits"},
		{"api.zoo.cloud:443", "spend_cap_exceeded", "https://console.zoo.cloud/billing/budgets"},
		{"api.hanzo.ai", "forbidden", ""},
		// A brand that runs no console is sent nowhere: a link to a host that does
		// not answer is worse than none.
		{"api.pars.network", "insufficient_balance", ""},
		{"api.bootno.de", "payment_required", ""},
	} {
		if got := BillingURL(tc.host, tc.code); got != tc.want {
			t.Errorf("BillingURL(%q, %q) = %q, want %q", tc.host, tc.code, got, tc.want)
		}
	}
}

// refusalBody decodes a money refusal in any of the three shapes the fleet writes —
// the money wire's nested error object, zip's problem document, SpendGate's flat
// Refusal — into the two facts a caller acts on.
func refusalBody(t *testing.T, raw []byte) (message, url string) {
	t.Helper()
	var b struct {
		Error      json.RawMessage `json:"error"`
		Message    string          `json:"message"`
		Detail     string          `json:"detail"`
		BillingURL string          `json:"billingUrl"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	var nested struct {
		Message    string `json:"message"`
		BillingURL string `json:"billingUrl"`
	}
	if len(b.Error) > 0 && b.Error[0] == '{' {
		_ = json.Unmarshal(b.Error, &nested)
	}
	message = nested.Message + b.Message + b.Detail
	url = nested.BillingURL + b.BillingURL
	return message, url
}

func assertLinks(t *testing.T, raw []byte, want, agent string) {
	t.Helper()
	msg, url := refusalBody(t, raw)
	if url != want {
		t.Errorf("billingUrl = %q, want %q (body %s)", url, want, raw)
	}
	if !strings.Contains(msg, want) {
		t.Errorf("message %q does not name %q — a person reading the refusal has nowhere to go", msg, want)
	}
	// An agent reads its brand's manifest and the API that takes a deposit, under
	// the same names whichever of the three shapes refused it.
	var b struct {
		Error        json.RawMessage `json:"error"`
		Agent, Topup string
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Error) > 0 && b.Error[0] == '{' {
		_ = json.Unmarshal(b.Error, &b)
	}
	if b.Agent != agent || b.Topup != TopupPath {
		t.Errorf("agent %q topup %q, want %q %q (body %s)", b.Agent, b.Topup, agent, TopupPath, raw)
	}
}

// A refusal on a brand with no console still says what to do, and names no page.
func TestARefusalNamesNoConsoleTheBrandDoesNotRun(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	req.Host = "api.pars.network"
	resp, err := newErrApp(meter.ErrInsufficientBalance).Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (%s)", resp.StatusCode, raw)
	}
	msg, url := refusalBody(t, raw)
	if url != "" || strings.Contains(msg, "http") || strings.HasSuffix(msg, " at ") {
		t.Errorf("a Pars refusal names a page Pars does not serve: message %q, billingUrl %q", msg, url)
	}
	if msg != "Add credits" {
		t.Errorf("message = %q, want %q", msg, "Add credits")
	}
}

// A refusal that says more than its message keeps saying it on the money wire: a
// spent plan window names itself, when it resets and where to raise it, and its
// Retry-After is the header, never a body member.
func TestARefusalsDetailRidesTheMoneyWire(t *testing.T) {
	refusal := (&zip.HTTPError{Status: http.StatusTooManyRequests, Code: "usage_cap_exceeded", Msg: "used"}).
		With(map[string]any{"limit": "day", "resets_at": "2026-10-02T00:00:00Z", "upgrade_url": "https://pay.hanzo.ai/cart?plan=max-20x", "retry_after": 3600})
	c := zip.New(zip.Config{DisableStartupMessage: true}).TestCtx(http.MethodPost, "/v1/chat/completions")
	if err := Enveloped(func(*zip.Ctx) error { return Denied(refusal) })(c); err != nil {
		t.Fatal(err)
	}
	resp := c.Fiber().Response()
	if resp.StatusCode() != http.StatusTooManyRequests || string(resp.Header.Peek("Retry-After")) != "3600" {
		t.Fatalf("status %d Retry-After %q", resp.StatusCode(), resp.Header.Peek("Retry-After"))
	}
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		t.Fatal(err)
	}
	e := body.Error
	if e["code"] != "usage_cap_exceeded" || e["message"] != "used" || e["limit"] != "day" ||
		e["resets_at"] != "2026-10-02T00:00:00Z" || e["upgrade_url"] != "https://pay.hanzo.ai/cart?plan=max-20x" {
		t.Fatalf("body %+v", e)
	}
	if _, ok := e["retry_after"]; ok {
		t.Fatalf("retry_after is a header, not a body member: %+v", e)
	}
}
