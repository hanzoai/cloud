package host

// A money refusal names the page that clears it, on the caller's own brand: Hanzo's
// checkout (hanzo.ai/pay/amount/card) for a refusal money clears, and the console
// page of a brand with no checkout.
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
	"sync/atomic"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

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
	if b.Agent != agent || b.Topup != cloud.TopupPath {
		t.Errorf("agent %q topup %q, want %q %q (body %s)", b.Agent, b.Topup, agent, cloud.TopupPath, raw)
	}
}

func TestEveryMoneyRefusalLinksTheBillingPage(t *testing.T) {
	t.Run("edge gate, out of funds", func(t *testing.T) {
		fc := &fakeCommerce{balanceBody: `{"available":0}`}
		var ran atomic.Bool
		app := newGateApp(t, mustClient(t, fc.server(t).URL), &ran)
		req := httptest.NewRequest(http.MethodPost, "/v1/agent/run", nil)
		req.Host = "api.lux.cloud"
		req.Header.Set("X-Org-Id", "hanzo")
		req.Header.Set("X-User-Id", "alice")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("status = %d, want 402 (%s)", resp.StatusCode, raw)
		}
		assertLinks(t, raw, "https://console.lux.cloud/billing/credits", "https://lux.network/llms.txt")
	})

	t.Run("propagated out of funds", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
		req.Host = "api.hanzo.ai"
		resp, err := newErrApp(meter.ErrInsufficientBalance).Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assertLinks(t, raw, "https://hanzo.ai/pay/amount/card", "https://hanzoskills.com/hanzo.md")
	})

	t.Run("typed op, out of funds", func(t *testing.T) {
		app := zip.New(zip.Config{ErrorHandler: cloud.ErrorHandler})
		app.Use(cloud.DenyEnvelope())
		app.Raw(http.MethodPost, "/v1/thing", func(c *zip.Ctx) error {
			return cloud.Denied(meter.ErrInsufficientBalance)
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/thing", nil)
		req.Host = "api.zoo.cloud"
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		assertLinks(t, raw, "https://console.zoo.cloud/billing/credits", "https://zoo.ngo/llms.txt")
	})

	t.Run("spend gate, unpaid", func(t *testing.T) {
		switches(t, map[string]bool{SwitchPaywallEnforced: true})
		publishLedger(t, &spendLedger{credit: atto(0)})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Host = "api.hanzo.ai"
		for k, v := range member {
			req.Header.Set(k, v)
		}
		resp, err := spendProbe(&planStub{}).Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("status = %d, want 402 (%s)", resp.StatusCode, raw)
		}
		assertLinks(t, raw, "https://hanzo.ai/pay/amount/card", "https://hanzoskills.com/hanzo.md")
	})
}
