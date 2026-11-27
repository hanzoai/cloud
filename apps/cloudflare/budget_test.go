package cloudflare

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/supply"
)

// envelope is a Cloudflare API v4 failure with one error.
func envelope(code int, msg string) string {
	b, _ := json.Marshal(map[string]any{"success": false, "errors": []map[string]any{{"code": code, "message": msg}}, "result": nil})
	return string(b)
}

// problem decodes a refusal body.
func problem(t *testing.T, body string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, body)
	}
	return got
}

// pulses hears every signal sdk/supply sends for the rest of the test. It is
// installed through supply.SetAnnounce rather than a bus subscription: a test
// binary that links a bus driver has no local bus to subscribe to.
func pulses(t *testing.T) <-chan bus.Pulse {
	t.Helper()
	ch := make(chan bus.Pulse, 16)
	t.Cleanup(supply.SetAnnounce(func(p bus.Pulse) error {
		select {
		case ch <- p:
		default:
		}
		return nil
	}))
	return ch
}

// heardStop waits for a supply pulse about org.
func heardStop(t *testing.T, ch <-chan bus.Pulse, org string) bus.Pulse {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case p := <-ch:
			if p.Kind == "supply" && p.Org == org {
				return p
			}
		case <-deadline:
			t.Fatalf("no supply pulse for org %s", org)
		}
	}
}

// Workers AI's daily allocation running out (3036, 429) is a budget stop on the
// workers_ai layer — not a 502 and not a capacity 429 — and it is said on the pulse.
func TestWorkersAIAllocationIsBudgetExhausted(t *testing.T) {
	ch := pulses(t)
	rec := &capture{}
	app := harness(t, map[string]string{"org-3036": "tok"}, rec, func(path string) (int, string) {
		if strings.Contains(path, "/ai/run/") {
			return http.StatusTooManyRequests, envelope(3036, "You have used up your daily free allocation of 10,000 neurons. Please upgrade to Cloudflare's Workers Paid plan if you would like to continue usage.")
		}
		return 0, ""
	})
	status, body, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/ai/run/@cf/meta/llama-3-8b-instruct", "u", "org-3036", false, `{"prompt":"hi"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", status, body)
	}
	got := problem(t, body)
	if got["code"] != supply.Code || got["layer"] != "workers_ai" || got["provider"] != "cloudflare" {
		t.Fatalf("body = %s, want code budget_exhausted, layer workers_ai, provider cloudflare", body)
	}
	if !strings.Contains(body, "daily free allocation") {
		t.Fatalf("body = %s, want Cloudflare's own sentence carried", body)
	}
	if p := heardStop(t, ch, "org-3036"); !strings.Contains(p.Title, "Workers AI") {
		t.Fatalf("pulse title = %q, want it to name Workers AI", p.Title)
	}
}

// Capacity is not budget: Workers AI's 3040 is a 429 too, and it stays what it was.
func TestWorkersAICapacityIsNotBudgetExhausted(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"org-3040": "tok"}, rec, func(path string) (int, string) {
		if strings.Contains(path, "/ai/run/") {
			return http.StatusTooManyRequests, envelope(3040, "Capacity temporarily exceeded, please try again.")
		}
		return 0, ""
	})
	status, body, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/ai/run/@cf/meta/llama-3-8b-instruct", "u", "org-3040", false, `{"prompt":"hi"}`)
	if strings.Contains(body, supply.Code) {
		t.Fatalf("a capacity refusal rendered as a budget stop: %d %s", status, body)
	}
	if status == http.StatusServiceUnavailable {
		t.Fatalf("a capacity refusal answered 503, the budget stop's status: %s", body)
	}
}

// Each stop names its own layer, wherever on the plane it is met. Cloudflare
// documents the Durable Objects sentence and the two codes verbatim; for the
// Workers limit and an AI Gateway spend limit it documents only the status (1103 /
// 429), so those are matched on the words a spending limit is said in, never on a
// guessed code.
func TestEachSpendingLimitNamesItsLayer(t *testing.T) {
	for _, c := range []struct {
		name, path, msg string
		code            int
		layer           supply.Layer
	}{
		{"durable objects", "/accounts/x/workers/durable_objects/namespaces", "Exceeded the account's spending cap for Durable Objects.", 0, supply.DurableObjects},
		{"workers ai allocation", "/accounts/x/ai/run/m", "You have used up your daily free allocation of 10,000 neurons.", 3036, supply.WorkersAI},
		{"workers visitor error", "/accounts/x/workers/scripts", "", 1103, supply.Workers},
		{"workers by its words", "/accounts/x/workers/scripts", "Exceeded the account's spending limit for Workers.", 0, supply.Workers},
		{"ai gateway by its words", "/accounts/x/ai-gateway/gateways/g", "Spend limit reached for this gateway.", 0, supply.AIGateway},
		{"layer from the path", "/accounts/x/ai-gateway/gateways/g", "Budget exceeded.", 0, supply.AIGateway},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := budgetLayer(c.path, c.code, c.msg); got != c.layer {
				t.Fatalf("budgetLayer(%d, %q) = %q, want %q", c.code, c.msg, got, c.layer)
			}
		})
	}
	// Refusals that are not a budget stay unclassified: a paid-only model (an
	// entitlement), a bad token (auth), a capacity 429.
	for _, c := range []struct {
		code int
		msg  string
	}{
		{5035, "This model requires a Workers Paid plan. See pricing for details."},
		{10000, "Authentication error"},
		{3040, "Capacity temporarily exceeded, please try again."},
		{7003, "Could not route to /accounts/x/workers/scripts, perhaps your object identifier is invalid?"},
	} {
		if got := budgetLayer("/accounts/x/ai/run/m", c.code, c.msg); got != "" {
			t.Errorf("budgetLayer(%d, %q) = %q, want unclassified", c.code, c.msg, got)
		}
	}
}

// A typed relay op meeting a spending limit answers the class too, not 502.
func TestATypedRelayMeetingASpendingLimitAnswersTheClass(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"org-do": "tok"}, rec, func(path string) (int, string) {
		if strings.Contains(path, "/workers/scripts") {
			return http.StatusForbidden, envelope(0, "Exceeded the account's spending limit for Workers.")
		}
		return 0, ""
	})
	status, body, _ := doReq(t, app, http.MethodGet, "/v1/cloudflare/workers/scripts", "u", "org-do", false, "")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", status, body)
	}
	if got := problem(t, body); got["code"] != supply.Code || got["layer"] != "workers" {
		t.Fatalf("body = %s, want budget_exhausted on layer workers", body)
	}
}
