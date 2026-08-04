package billing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
)

// TestCoResidentUsage proves usage() answers from the finance ledger (never the
// self-dispatching commerce hop) and shapes the commerce GetUsage envelope the
// console parses. fakeFinance + publishFinance live in balance_test.go.
func TestCoResidentUsage(t *testing.T) {
	publishFinance(t, &fakeFinance{usageRows: []finance.UsageRow{
		{ID: "u1", Cents: 150, Model: "gpt-x", CreatedAt: 1_700_000_000},
		{ID: "u2", Cents: 75, Model: "embed-y", CreatedAt: 1_700_000_100},
	}})

	body, coResident, err := coResidentUsage(context.Background(), "acme", "", "")
	if err != nil {
		t.Fatalf("coResidentUsage: %v", err)
	}
	if !coResident {
		t.Fatal("want coResident=true when finance is published")
	}
	var env struct {
		User  string `json:"user"`
		Count int    `json:"count"`
		Usage []struct {
			TransactionID string         `json:"transactionId"`
			Amount        int64          `json:"amount"`
			Metadata      map[string]any `json:"metadata"`
			CreatedAt     string         `json:"createdAt"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope not valid JSON: %v\n%s", err, body)
	}
	if env.User != "acme" || env.Count != 2 || len(env.Usage) != 2 {
		t.Fatalf("bad envelope: user=%q count=%d rows=%d", env.User, env.Count, len(env.Usage))
	}
	if env.Usage[0].TransactionID != "u1" || env.Usage[0].Amount != 150 {
		t.Errorf("row0 = %+v", env.Usage[0])
	}
	if env.Usage[0].Metadata["model"] != "gpt-x" {
		t.Errorf("row0 metadata missing model: %+v", env.Usage[0].Metadata)
	}
	if env.Usage[0].CreatedAt == "" {
		t.Error("row0 createdAt should be RFC3339, got empty")
	}
}

// TestCoResidentUsageSplitDeploy proves that with no co-resident finance, usage()
// falls through to the commerce S2S proxy (coResident=false), unchanged.
func TestCoResidentUsageSplitDeploy(t *testing.T) {
	finance.Publish(nil)
	body, coResident, err := coResidentUsage(context.Background(), "acme", "", "")
	if err != nil {
		t.Fatalf("coResidentUsage: %v", err)
	}
	if coResident || body != nil {
		t.Fatalf("want fall-through (coResident=false, nil body); got coResident=%v body=%s", coResident, body)
	}
}

// TestCoResidentUsageGroupBy proves the ?groupBy=product reduction still runs on the
// co-resident path (a token-metered row attributes to product "inference").
func TestCoResidentUsageGroupBy(t *testing.T) {
	publishFinance(t, &fakeFinance{usageRows: []finance.UsageRow{{ID: "u1", Cents: 150, Model: "gpt-x", CreatedAt: 1_700_000_000}}})
	body, ok, err := coResidentUsage(context.Background(), "acme", "", "product")
	if err != nil || !ok {
		t.Fatalf("coResidentUsage groupBy: ok=%v err=%v", ok, err)
	}
	var grouped struct {
		GroupBy string `json:"groupBy"`
		Groups  []struct {
			Product     string `json:"product"`
			Requests    int    `json:"requests"`
			AmountCents int64  `json:"amountCents"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &grouped); err != nil {
		t.Fatalf("grouped not valid JSON: %v\n%s", err, body)
	}
	if grouped.GroupBy != "product" || len(grouped.Groups) != 1 {
		t.Fatalf("bad grouped envelope: %s", body)
	}
}

// The envelope's `decimal` carries the exact debit; `amount` stays the cents the
// console parses today. A sub-cent charge is the proof either way: amount says 0,
// decimal says the money. Rows built without the exact value (these fixtures'
// first two, and any builder that predates the field) omit `decimal` rather than
// asserting a zero the cents deny.
func TestCoResidentUsage_DecimalCarriesTheExactDebit(t *testing.T) {
	sub, err := money.ParseUSD("0.0025")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	publishFinance(t, &fakeFinance{usageRows: []finance.UsageRow{
		{ID: "old", Cents: 150, Model: "gpt-x", CreatedAt: 1_700_000_000},
		{ID: "tiny", Cents: 0, Amount: sub, Model: "zen-1", CreatedAt: 1_700_000_100},
	}})

	body, _, err := coResidentUsage(context.Background(), "acme", "", "")
	if err != nil {
		t.Fatalf("coResidentUsage: %v", err)
	}
	var env struct {
		Usage []struct {
			TransactionID string `json:"transactionId"`
			Amount        int64  `json:"amount"`
			Decimal       string `json:"decimal"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope not valid JSON: %v\n%s", err, body)
	}
	if len(env.Usage) != 2 {
		t.Fatalf("rows = %d, want 2", len(env.Usage))
	}
	if env.Usage[0].Decimal != "" {
		t.Errorf("row without an exact value published decimal=%q — a fabricated zero beside amount=%d", env.Usage[0].Decimal, env.Usage[0].Amount)
	}
	tiny := env.Usage[1]
	if tiny.Amount != 0 {
		t.Fatalf("tiny.amount = %d, want 0 — the fixture must be sub-cent", tiny.Amount)
	}
	if tiny.Decimal == "" {
		t.Fatal("tiny.decimal is EMPTY — the exact debit never reached the envelope; the customer reads zero")
	}
	got, err := money.ParseUSD(strings.TrimSuffix(strings.TrimSpace(tiny.Decimal), " USD"))
	if err != nil {
		t.Fatalf("decimal %q does not parse as USD: %v", tiny.Decimal, err)
	}
	if got.Cmp(sub) != 0 {
		t.Fatalf("decimal = %q (%s), want %s", tiny.Decimal, got, sub)
	}
}
