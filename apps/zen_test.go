// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package apps

import (
	"math/big"
	"testing"

	"github.com/hanzoai/decimal"
	hmoney "github.com/hanzoai/money"
	"github.com/hanzoai/zen"
)

// usd builds an exact USD amount from a decimal string, the way zen prices a
// call (18-dp native, never through float).
//
// The decimal here must be hanzoai/decimal, the one hmoney.New takes and the one
// zen prices with — not shopspring's identically-named type. Money has exactly one
// decimal; a second one that merely LOOKS like it is how a price silently becomes
// a different number.
func usd(t *testing.T, s string) hmoney.Amount {
	t.Helper()
	d, err := decimal.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return hmoney.New(d, hmoney.USD)
}

// zen5Usage is one served call at zen5's live rates: 1M in + 1M out, priced at
// the family's 3× margin (retail = cost × margin).
//
//	in : cost 1.392 → retail 4.176
//	out: cost 4.40  → retail 13.20
func zen5Usage(t *testing.T) zen.Usage {
	t.Helper()
	return zen.Usage{
		Tenant:           zen.Tenant{BillingOrg: "acme", User: "acme/alice", Project: "p1"},
		Model:            "zen5",
		PromptTokens:     1_000_000,
		CompletionTokens: 1_000_000,
		Charge:           usd(t, "17.376"), // 4.176 + 13.20 — what the caller pays
		Cost:             usd(t, "5.792"),  // 1.392 + 4.40  — what we pay upstream
		RequestID:        "req-1",
	}
}

// The debit is the RETAIL Charge — what the caller pays — never the upstream
// COGS. This is the money property: it fails if the Amount is built from
// u.Cost. The tier is margin-bearing (retail != cost), so the two values are
// distinguishable and the assertion cannot pass by coincidence.
func TestMeterUsageDebitsRetailNotCost(t *testing.T) {
	u := zen5Usage(t)

	if u.Charge.Cmp(u.Cost) == 0 {
		t.Fatal("fixture is not margin-bearing: retail == cost, so the test could not tell them apart")
	}

	got := meterUsage(u).Amount.Int()

	if want := u.Charge.Minor(); got.Cmp(want) != 0 {
		t.Errorf("debit = %s, want retail Charge %s", got, want)
	}
	if cogs := u.Cost.Minor(); got.Cmp(cogs) == 0 {
		t.Errorf("debit = %s, which is the upstream COGS — the caller must be billed retail, not our cost", got)
	}
}

// At the family's 3× margin the debit is exactly 3× the COGS: we collect the
// full retail price, not the wholesale one. Debiting Cost would collect 1/3 —
// our own COGS — and book zero margin.
func TestMeterUsageCollectsTheFullMargin(t *testing.T) {
	u := zen5Usage(t)

	debit := meterUsage(u).Amount.Int()
	thriceCOGS := new(big.Int).Mul(u.Cost.Minor(), big.NewInt(3))

	if debit.Cmp(thriceCOGS) != 0 {
		t.Errorf("debit = %s, want 3x COGS = %s (retail = cost x margin, margin 3.0)", debit, thriceCOGS)
	}
}

// The debit carries zen's exact 18-dp value with no floor: a sub-cent call must
// not round to zero on the way to the ledger.
func TestMeterUsageKeepsExactSubCentCharge(t *testing.T) {
	u := zen5Usage(t)
	u.Charge = usd(t, "0.004176") // 1k input tokens at 4.176/MTok — well under a cent
	u.Cost = usd(t, "0.001392")

	got := meterUsage(u).Amount.Int()

	if want := u.Charge.Minor(); got.Cmp(want) != 0 {
		t.Errorf("debit = %s, want the exact sub-cent charge %s", got, want)
	}
	if got.Sign() == 0 {
		t.Error("sub-cent charge floored to zero — the call would be served free")
	}
}

// The identity fields ride with the debit unchanged: the debit lands on the org
// that PAYS (BillingOrg), scoped to its project, with the actor for the audit
// trail.
func TestMeterUsageAttribution(t *testing.T) {
	u := zen5Usage(t)
	m := meterUsage(u)

	for _, c := range []struct{ name, got, want string }{
		{"User", m.User, "acme"},
		{"Org", m.Org, "acme"},
		{"Actor", m.Actor, "acme/alice"},
		{"Project", m.Project, "p1"},
		{"Model", m.Model, "zen5"},
		{"Provider", m.Provider, zenProvider},
		{"Service", m.Service, zenService},
		{"Currency", m.Currency, "usd"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if m.TotalTokens != 2_000_000 {
		t.Errorf("TotalTokens = %d, want 2000000", m.TotalTokens)
	}
}
