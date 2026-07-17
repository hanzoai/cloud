// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package apps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/clients/metering"
	cloudmoney "github.com/hanzoai/cloud/clients/money"
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

// dollars is the EXPECTED money, built from a plain dollar literal through cloud's
// OWN ParseUSD — deliberately a DIFFERENT constructor than the code under test uses.
//
// That is what pins the UNIT. These tests once asserted meterUsage(u).Amount.Int()
// against u.Charge.Minor(): both sides re-derived the number through the same
// conversion, so the assertion only proved an integer round-tripped and was blind
// to what the integer MEANT. It passed while every zen debit was 10^16 too small.
// Comparing money to a known dollar amount cannot be blind that way: if the debit
// is off by any factor, it is not $17.376 and the test fails.
func dollars(t *testing.T, s string) cloudmoney.Amount {
	t.Helper()
	a, err := cloudmoney.ParseUSD(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return a
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

	got := meterUsage(u).Amount

	// The known dollar value the fixture charges — a $17.376 call debits $17.376.
	if want := dollars(t, "17.376"); got.Cmp(want) != 0 {
		t.Errorf("debit = $%s, want the retail Charge $%s", got, want)
	}
	if cogs := dollars(t, "5.792"); got.Cmp(cogs) == 0 {
		t.Errorf("debit = $%s, which is the upstream COGS — the caller must be billed retail, not our cost", got)
	}
}

// At the family's 3× margin the debit is exactly 3× the COGS: we collect the
// full retail price, not the wholesale one. Debiting Cost would collect 1/3 —
// our own COGS — and book zero margin.
func TestMeterUsageCollectsTheFullMargin(t *testing.T) {
	u := zen5Usage(t)

	debit := meterUsage(u).Amount
	// 3x the COGS in the SAME unit as the debit — $5.792 + $5.792 + $5.792.
	// Summing the credit Amount keeps the comparison in exact dollars; the old
	// version multiplied Cost.Minor() (cents, 579 after rounding away 5.792's
	// third decimal) and compared it to a debit that was not cents at all.
	cogs := dollars(t, "5.792")
	thriceCOGS := cogs.Add(cogs).Add(cogs)

	if debit.Cmp(thriceCOGS) != 0 {
		t.Errorf("debit = $%s, want 3x COGS = $%s (retail = cost x margin, margin 3.0)", debit, thriceCOGS)
	}
	if want := dollars(t, "17.376"); debit.Cmp(want) != 0 {
		t.Errorf("debit = $%s, want $%s", debit, want)
	}
}

// The debit carries zen's exact 18-dp value with no floor: a sub-cent call must
// not round to zero on the way to the ledger.
func TestMeterUsageKeepsExactSubCentCharge(t *testing.T) {
	u := zen5Usage(t)
	u.Charge = usd(t, "0.004176") // 1k input tokens at 4.176/MTok — well under a cent
	u.Cost = usd(t, "0.001392")

	got := meterUsage(u).Amount

	if want := dollars(t, "0.004176"); got.Cmp(want) != 0 {
		t.Errorf("debit = $%s, want the exact sub-cent charge $%s", got, want)
	}
	// metering.Record drops a zero Amount before it ever reaches the ledger
	// (`if !c.Enabled() || amt.IsZero() ...  return nil`), so a floored sub-cent
	// charge is not a small debit — it is NO DEBIT ROW, and the call is free.
	if got.IsZero() {
		t.Error("sub-cent charge floored to zero — Record drops a zero Amount, so the call is served free with no debit row")
	}
}

// The unit trap this seam shipped with, pinned so it cannot come back. zen prices
// an exact 18-dp value but tags it money.USD, whose Currency declares 2 decimals —
// so Charge.Minor() renders CENTS, while cloudmoney.FromInt reads its argument as
// 18-dp. Composing them silently divides every debit by 10^16.
//
// The tests above already fail if credit() regresses to that composition; this one
// names WHY, and proves the two conversions are still distinguishable — an
// assertion that cannot tell right from wrong is worse than no assertion.
func TestCreditIsNotTheMinorUnit(t *testing.T) {
	charge := usd(t, "17.376")

	if got, want := credit(charge), dollars(t, "17.376"); got.Cmp(want) != 0 {
		t.Fatalf("credit($17.376) = $%s, want $%s", got, want)
	}

	// The conversion that shipped, spelled out.
	old := cloudmoney.FromAtto(charge.Minor())
	if old.Cmp(credit(charge)) == 0 {
		t.Fatal("FromInt(Minor()) agrees with credit() — the fixture can no longer tell the units apart, so these tests prove nothing")
	}
	if old.Cents() != 0 {
		t.Errorf("FromInt(Minor()).Cents() = %d, want 0 — that this was ALWAYS 0 is what made the spend gate admit every request", old.Cents())
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

// The spend gate must REFUSE a request whose estimate exceeds the balance.
//
// This is the sharpest edge of the unit bug and the reason it is a security
// finding, not only a revenue one. AuthorizeVerdict gates size like this:
//
//	funded := available > 0
//	if in.AmountCents > 0 { funded = available >= in.AmountCents }
//
// The estimate reached it as FromInt(est.Minor()).Cents(), which is ALWAYS 0 —
// so the size branch was DEAD and every request rode `available > 0`. Any org
// with a single cent of balance could draw an unbounded call. The debits were
// dust too, so the balance never fell and the cap could never trip.
func TestCommerceGateRefusesAnOverCapRequest(t *testing.T) {
	const availableCents = 500 // the org holds $5.00

	var authorized atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/billing/balance":
			_, _ = io.WriteString(w, `{"available":`+strconv.Itoa(availableCents)+`}`)
		case "/v1/billing/spend-alerts/authorize":
			authorized.Store(true)
			_, _ = io.WriteString(w, `{"allow":true}`)
		default:
			_, _ = io.WriteString(w, `[]`)
		}
	}))
	defer srv.Close()

	m, err := metering.New(metering.Config{BaseURL: srv.URL, Token: "t", Org: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	gate := commerceGate(m)
	if gate == nil {
		t.Fatal("gate is nil — the client must be enabled for this proof to mean anything")
	}
	tenant := zen.Tenant{BillingOrg: "acme", User: "acme/alice", Project: "p1"}

	// $17.376 against a $5.00 balance: over cap, must be refused.
	if err := gate(context.Background(), tenant, "zen5", usd(t, "17.376")); err == nil {
		t.Error("gate ADMITTED a $17.376 request against a $5.00 balance — the estimate is reaching AuthorizeVerdict as 0 cents, so the size check never runs")
	}

	// The same balance must still admit a request it can actually cover, or the
	// test would pass by refusing everything.
	if err := gate(context.Background(), tenant, "zen5", usd(t, "1.00")); err != nil {
		t.Errorf("gate refused an affordable $1.00 request against a $5.00 balance: %v", err)
	}
	if !authorized.Load() {
		t.Error("the affordable request never reached the spend-cap authorize step")
	}
}

// The estimate must reach the balance check as the RIGHT number of cents. The
// refusal test above proves the gate says no; this proves it says no for the
// right reason — that $17.376 is folded to 1738 cents, not to 0.
func TestCommerceGateFoldsTheEstimateToRealCents(t *testing.T) {
	for _, c := range []struct {
		charge string
		want   int64
	}{
		{"17.376", 1738}, // rounds half-away-from-zero at the cent
		{"1000.00", 100000},
		{"1.00", 100},
		{"0.004176", 0}, // sub-cent gates as "any positive balance"
	} {
		if got := credit(usd(t, c.charge)).Cents(); got != c.want {
			t.Errorf("$%s folds to %d cents, want %d", c.charge, got, c.want)
		}
	}
}

// A zero-value zen price must convert and fold without panicking. zen leaves
// Charge/Cost as the zero Amount for a free SKU, and that value carries the
// EMPTY currency code, not "USD" — so this also pins that credit() reads the
// decimal rather than dispatching on the currency. The old attoToNano guarded a
// nil *big.Int here; nano() needs no guard because decimal.Coef() returns a real
// zero big.Int, never nil, but the property is worth holding.
func TestCreditAndNanoHandleTheZeroPrice(t *testing.T) {
	var free hmoney.Amount // zero value: no currency, no coefficient

	got := credit(free)
	if !got.IsZero() {
		t.Errorf("credit(zero) = $%s, want $0", got)
	}
	if n := nano(got); n != 0 {
		t.Errorf("nano(credit(zero)) = %d, want 0", n)
	}
	if c := got.Cents(); c != 0 {
		t.Errorf("credit(zero).Cents() = %d, want 0", c)
	}
	// And a free call books no debit row, which is correct — nothing is owed.
	u := zen5Usage(t)
	u.Charge, u.Cost = free, free
	if amt := meterUsage(u).Amount; !amt.IsZero() {
		t.Errorf("free call debits $%s, want $0", amt)
	}
}

// nano carries real money to the warehouse margin columns. It is the last place
// the unit could silently collapse: attoToNano(cents) divided a cents integer by
// 1e9 and produced 0 for every charge under $10,000,000.
func TestNanoFoldsCreditToRealNano(t *testing.T) {
	for _, c := range []struct {
		charge string
		want   int64
	}{
		{"17.376", 17_376_000_000},
		{"5.792", 5_792_000_000},
		{"0.004176", 4_176_000},
		{"1000.00", 1_000_000_000_000},
	} {
		if got := nano(credit(usd(t, c.charge))); got != c.want {
			t.Errorf("nano($%s) = %d, want %d", c.charge, got, c.want)
		}
	}
}
