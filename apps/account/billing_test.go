package account

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/hanzoai/account"
)

// billing_test.go — the pure tenant-scoping rules (billing.go): which account a
// caller bills, and how a request is narrowed to it. The Go port of console's
// billing-scope.test.ts.
//
// These are FUNCTIONS of a query/body and a subject, tested as such. What APPLIES
// them to a live request is PinBillingSubject, in front of the co-resident commerce
// handlers, and billing_coresident_test.go drives that client end to end — including
// the three admission cases (validated customer, trusted in-proc S2S, neither).

// TestBillingSubject proves the top-up subject is resolved through the ONE rule
// (ai/object.Payer) — so a top-up credits the SAME account the ai gate debits and
// the console reads. The signup org bills per-person (matching the gate), which is
// the whole fix: money and gate land on one account.
func TestBillingSubject(t *testing.T) {
	cases := []struct{ org, name, want string }{
		{"acme", "alice", "acme"},       // real org: any member bills the ONE org account
		{"hanzo", "Dave", "hanzo/dave"}, // signup org: each person bills their OWN account
		{"hanzo", "z", "hanzo/z"},       // another signup person — their own account
		{"acme", "", "acme"},            // a member-less credential in a POOLED org: still the pool
		// The signup org is the one place a missing name is not "the org acting" but a
		// credential that FAILED to name its person, and the account beside those
		// members holds the platform's own balance. So it resolves to nothing and the
		// caller refuses; billing it to the pool is what let a token that had resolved
		// nobody spend that balance. A machine in this org is unaffected — it says so
		// with Type/Machine and is answered by the org ledger before this rule.
		{"hanzo", "", ""},
		{"Hanzo", "Z", "hanzo/z"}, // folded
		{"", "x", ""},             // no org → empty subject (cannot bill)
	}
	for _, c := range cases {
		got := account.Payer(account.Credential{Owner: c.org, Name: c.name}).Subject()
		if got != c.want {
			t.Fatalf("Payer(%q,%q).Subject(): want %q, got %q", c.org, c.name, c.want, got)
		}
	}
}

// TestBillingSubject_IgnoresLegacyEnv locks that the killed allowlist envs have NO
// effect: nothing reads them. Set to values that WOULD have flipped every
// resolution — the subject is unchanged. This is the console/top-up half of the
// same proof ai carries (one rule, no config), so the view and the gate can never
// disagree, and the deleted CR env is a genuine no-op.
func TestBillingSubject_IgnoresLegacyEnv(t *testing.T) {
	t.Setenv("PERSONAL_BILLING_ORGS", "hanzo,acme") // would have split acme per-user
	t.Setenv("ORG_BILLING_ORGS", "hanzo")           // would have pooled the signup org
	cases := []struct{ org, name, want string }{
		{"hanzo", "z", "hanzo/z"},        // env cannot pool the signup org
		{"acme", "alice", "acme"},        // env cannot split a real org per-user
		{"maxpower", "dave", "maxpower"}, // untouched
	}
	for _, c := range cases {
		got := account.Payer(account.Credential{Owner: c.org, Name: c.name}).Subject()
		if got != c.want {
			t.Fatalf("legacy env must be ignored: Payer(%q,%q).Subject() want %q, got %q", c.org, c.name, c.want, got)
		}
	}
}

func TestScopedBillingSearch_PinsSubjectDropsOrgKeepsRest(t *testing.T) {
	in := url.Values{}
	in.Set("userId", "victim")     // forged subject — must be OVERWRITTEN
	in.Set("customerId", "victim") // forged subject — must be OVERWRITTEN
	in.Set("user", "victim")       // forged subject — must be OVERWRITTEN
	in.Set("org", "othercorp")     // must be DROPPED (cannot widen scope)
	in.Set("currency", "usd")      // non-subject — must PASS THROUGH
	in.Set("start", "2026-01-01")  // non-subject — must PASS THROUGH

	out := scopedBillingSearch(in, "acme")

	for _, k := range billingSubjectKeys {
		if out.Get(k) != "acme" {
			t.Fatalf("subject key %s must be pinned to acme, got %q", k, out.Get(k))
		}
	}
	if out.Has("org") {
		t.Fatal("org must be dropped")
	}
	if out.Get("currency") != "usd" || out.Get("start") != "2026-01-01" {
		t.Fatalf("non-subject params must pass through, got %v", out)
	}
}

func TestScopedBillingBody(t *testing.T) {
	// a forged subject in a JSON object write is OVERWRITTEN; other fields survive.
	out := scopedBillingBody([]byte(`{"userId":"victim","amount":500,"note":"x"}`), "acme")
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("scoped body is not JSON: %v", err)
	}
	for _, k := range billingSubjectKeys {
		if obj[k] != "acme" {
			t.Fatalf("body subject key %s must be pinned to acme, got %v", k, obj[k])
		}
	}
	if obj["amount"].(float64) != 500 || obj["note"] != "x" {
		t.Fatalf("non-subject body fields must survive, got %v", obj)
	}

	// non-object / empty bodies pass through untouched (never invent a body).
	for _, raw := range []string{"not json", `[1,2,3]`, `"scalar"`, ""} {
		if got := string(scopedBillingBody([]byte(raw), "acme")); got != raw {
			t.Fatalf("non-object body %q must pass through, got %q", raw, got)
		}
	}
}
