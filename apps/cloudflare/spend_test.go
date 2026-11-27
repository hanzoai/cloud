package cloudflare

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// spendPolicies is a Cloudflare spend-policies answer: a hard Workers cap at 85%
// of its limit, a Durable Objects cap well under it, a notification-only policy
// past its threshold, and a hard cap whose spend Cloudflare could not read.
const spendPolicies = `{"success":true,"errors":[],"result":[
 {"id":"p-w","name":"workers cap","product_id":"workers","action":"usage_block","enabled":true,"threshold_cents":10000,"current_state":{"balance_cents":8500,"status":"below_limit"}},
 {"id":"p-do","product_id":"durable_objects","action":"usage_block","enabled":true,"threshold_cents":5000,"current_state":{"balance_cents":1000,"status":"below_limit"}},
 {"id":"p-n","product_id":"workers","action":"notification","enabled":true,"threshold_cents":100,"current_state":{"balance_cents":900,"status":"limit_reached"}},
 {"id":"p-u","product_id":"durable_objects","action":"usage_block","enabled":true,"threshold_cents":5000}
]}`

// The spend limits read is each limit measured against the warning threshold, and
// a HARD cap past the threshold is said on the alert path before Cloudflare stops
// the layer. A notification-only policy is reported but is not a cap, and a spend
// Cloudflare did not report is unknown rather than zero.
func TestSpendLimitsWarnBelowTheHardCap(t *testing.T) {
	t.Setenv("CLOUD_PROVIDER_BUDGET_WARN_PCT", "80")
	ch := pulses(t)
	rec := &capture{}
	app := harness(t, map[string]string{"org-spend": "tok-S"}, rec, func(path string) (int, string) {
		if strings.HasSuffix(path, "/billing/spend-policies") {
			return http.StatusOK, spendPolicies
		}
		return 0, ""
	})
	status, body, _ := doReq(t, app, http.MethodGet, "/v1/cloudflare/spend-limits", "u", "org-spend", false, "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", status, body)
	}
	r, ok := rec.find("/billing/spend-policies")
	if !ok || r.path != "/accounts/"+testAccountID+"/billing/spend-policies" || r.auth != "Bearer tok-S" {
		t.Fatalf("spend read = %+v (found %v), want GET /accounts/{acct}/billing/spend-policies with the org's token", r, ok)
	}

	var out spendLimitsOut
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("body: %v (%s)", err, body)
	}
	if out.WarnPct != 80 {
		t.Fatalf("warnPct = %d, want 80", out.WarnPct)
	}
	byID := map[string]spendLimit{}
	for _, l := range out.Limits {
		byID[l.ID] = l
	}
	for id, want := range map[string]struct {
		layer, level string
		hard         bool
	}{
		"p-w":  {"workers", "warn", true},
		"p-do": {"durable_objects", "below", true},
		"p-n":  {"workers", "at_cap", false},
		"p-u":  {"durable_objects", "unknown", true},
	} {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("limit %s missing from %s", id, body)
		}
		if got.Layer != want.layer || got.Level != want.level || got.Hard != want.hard {
			t.Errorf("limit %s = %+v, want layer %s, level %s, hard %v", id, got, want.layer, want.level, want.hard)
		}
	}
	if byID["p-u"].SpentCents != nil {
		t.Errorf("an unread spend reported as %d, want absent", *byID["p-u"].SpentCents)
	}
	if got := byID["p-w"]; got.SpentCents == nil || *got.SpentCents != 8500 || got.LimitCents != 10000 || got.UsedPct != 85 {
		t.Errorf("workers cap = %+v, want 8500 of 10000, 85%%", got)
	}

	p := heardStop(t, ch, "org-spend")
	if !strings.Contains(p.Title, "Workers") || !strings.Contains(p.Title, "85%") {
		t.Fatalf("warning pulse title = %q, want Workers at 85%%", p.Title)
	}
}

// A token without billing access is Cloudflare's 403, relayed as it is: the read
// is refused, not answered with an empty list that would read as "no limits".
func TestSpendLimitsWithoutBillingAccessIsRefused(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"org-nobill": "tok"}, rec, func(path string) (int, string) {
		if strings.HasSuffix(path, "/billing/spend-policies") {
			return http.StatusForbidden, envelope(10000, "Authentication error")
		}
		return 0, ""
	})
	status, body, _ := doReq(t, app, http.MethodGet, "/v1/cloudflare/spend-limits", "u", "org-nobill", false, "")
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", status, body)
	}
}
