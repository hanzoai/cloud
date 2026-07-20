package marketing

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// launchPromo is the seeded spec (discounts.md): 90% off, cap 1000, 10-seat cap,
// Pro/Max/Team.
func launchPromo() Promo {
	return Promo{Code: "first1000", PercentOff: 90, MaxRedemptions: 1000, TeamSeatCap: 10, Plans: "pro,max,team", Active: true}
}

// TestPromoQuote pins the eligibility math to the EXACT figures in discounts.md.
func TestPromoQuote(t *testing.T) {
	p := launchPromo()
	cases := []struct {
		plan             string
		seats            int
		charge, discount int64
		ok               bool
	}{
		{"pro", 1, 490, 4410, true},       // $49 → $4.90, save $44.10
		{"max", 1, 2000, 18000, true},     // $200 → $20.00, save $180
		{"team", 1, 1990, 17910, true},    // $199/seat → $19.90/seat, save $179.10
		{"team", 3, 5970, 53730, true},    // 3 seats at promo rate
		{"team", 12, 59700, 179100, true}, // 10 promo seats + 2 at list; save capped at 10 seats
		{"developer", 1, 0, 0, false},     // free — nothing to discount
		{"enterprise", 1, 0, 0, false},    // unknown plan
	}
	for _, c := range cases {
		charge, discount, ok, reason := p.quote(c.plan, c.seats)
		if ok != c.ok {
			t.Fatalf("%s x%d: eligible=%v want %v (%s)", c.plan, c.seats, ok, c.ok, reason)
		}
		if !c.ok {
			continue
		}
		if charge != c.charge || discount != c.discount {
			t.Fatalf("%s x%d: got charge=%d discount=%d, want charge=%d discount=%d",
				c.plan, c.seats, charge, discount, c.charge, c.discount)
		}
		// charge + discount must reconstruct the full list value of every seat
		// billed (single-unit plans = 1 seat; team = all seats, promo + list).
		seatsMul := int64(1)
		if c.plan == "team" {
			seatsMul = int64(c.seats)
		}
		if charge+discount != planListCents(c.plan)*seatsMul {
			t.Fatalf("%s x%d: charge(%d)+discount(%d) must equal list*seats(%d)",
				c.plan, c.seats, charge, discount, planListCents(c.plan)*seatsMul)
		}
	}
}

// TestPromoUncoveredPlan: a plan outside the promo's set is rejected even if it
// is a paid plan.
func TestPromoUncoveredPlan(t *testing.T) {
	p := Promo{PercentOff: 90, TeamSeatCap: 10, Plans: "pro"}
	if _, _, ok, _ := p.quote("max", 1); ok {
		t.Fatalf("max must be ineligible when the promo covers only pro")
	}
	if _, _, ok, _ := p.quote("pro", 1); !ok {
		t.Fatalf("pro must be eligible")
	}
}

// fakeFinance is an in-memory finance.Client that records deposits and honours
// Ref idempotency (like the real ledger).
type fakeFinance struct {
	deposits []types.DepositInput
	byRef    map[string]string
}

func newFakeFinance() *fakeFinance { return &fakeFinance{byRef: map[string]string{}} }

func (f *fakeFinance) Balance(_ context.Context, _, _, _ string, _ bool) (money.Amount, error) {
	return money.Zero(), nil
}

func (f *fakeFinance) Deposit(_ context.Context, in types.DepositInput) (string, error) {
	if in.Ref != "" {
		if id, ok := f.byRef[in.Ref]; ok {
			return id, nil // idempotent replay — no new credit
		}
	}
	f.deposits = append(f.deposits, in)
	id := "dep_" + in.Ref
	if in.Ref != "" {
		f.byRef[in.Ref] = id
	}
	return id, nil
}
func (f *fakeFinance) RecordUsage(_ context.Context, _ types.UsageInput) error { return nil }
func (f *fakeFinance) SumUsageSince(_ context.Context, _ string, _ bool, _ int64) (int64, error) {
	return 0, nil
}

// insertPromo seeds a custom promo for a test (small cap, so exhaustion is cheap
// to prove).
func insertPromo(t *testing.T, s *Store, code string, maxRedemptions int) {
	t.Helper()
	if _, err := s.db.Exec(
		`INSERT INTO marketing_promos (code,description,percent_off,max_redemptions,team_seat_cap,plans,active,created_at)
		 VALUES (?,?,?,?,?,?,1,?) ON CONFLICT(code) DO UPDATE SET max_redemptions=excluded.max_redemptions`,
		code, "test", 90, maxRedemptions, 10, "pro,max,team", 1,
	); err != nil {
		t.Fatalf("insert promo: %v", err)
	}
}

// TestRedeemGuards proves every abuse guard from discounts.md: exactly-once per
// org, once per instrument, the hard redemption cap, and the credited amount.
func TestRedeemGuards(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	fin := newFakeFinance()
	insertPromo(t, st, "cap2", 2)
	p, err := st.GetPromo(ctx, "cap2")
	if err != nil {
		t.Fatalf("get promo: %v", err)
	}

	// org "a" redeems Pro on card1 → credited $44.10, once.
	_, discount, ok, _ := p.quote("pro", 1)
	if !ok || discount != 4410 {
		t.Fatalf("pro discount want 4410, got %d ok=%v", discount, ok)
	}
	r, already, err := st.redeem(ctx, p, "a", "pro", 1, "card1", discount, fin, 100)
	if err != nil || already {
		t.Fatalf("a first redeem: already=%v err=%v", already, err)
	}
	if r.CreditCents != 4410 {
		t.Fatalf("recorded credit want 4410, got %d", r.CreditCents)
	}

	// Same org again → idempotent no-op (already=true, no new deposit).
	if _, again, err := st.redeem(ctx, p, "a", "pro", 1, "card1", discount, fin, 101); err != nil || !again {
		t.Fatalf("a second redeem want already=true, got already=%v err=%v", again, err)
	}

	// A different org reusing card1 → instrument guard.
	if _, _, err := st.redeem(ctx, p, "b", "pro", 1, "card1", discount, fin, 102); !errors.Is(err, errInstrumentUsed) {
		t.Fatalf("shared instrument want errInstrumentUsed, got %v", err)
	}

	// org "b" on its own card → ok (fills the cap: 2 redemptions).
	if _, _, err := st.redeem(ctx, p, "b", "pro", 1, "card2", discount, fin, 103); err != nil {
		t.Fatalf("b redeem: %v", err)
	}

	// org "c" → cap reached, declined.
	if _, _, err := st.redeem(ctx, p, "c", "pro", 1, "card3", discount, fin, 104); !errors.Is(err, errPromoExhausted) {
		t.Fatalf("over-cap want errPromoExhausted, got %v", err)
	}

	// Exactly two credits landed (a, b), each $44.10, each with the org-scoped Ref
	// and the promo credit tag.
	if len(fin.deposits) != 2 {
		t.Fatalf("want 2 credits, got %d (%+v)", len(fin.deposits), fin.deposits)
	}
	for _, d := range fin.deposits {
		if d.Amount.Cents() != 4410 {
			t.Fatalf("credit amount want 4410, got %d", d.Amount.Cents())
		}
		if d.Tags != "credit:promo-cap2" {
			t.Fatalf("credit tag want credit:promo-cap2, got %q", d.Tags)
		}
		if d.Ref != "promo-cap2:"+d.Org {
			t.Fatalf("credit ref want promo-cap2:%s, got %q", d.Org, d.Ref)
		}
	}
	// The counter reflects exactly the two successful redemptions.
	if n, _ := st.CountRedemptions(ctx, "cap2"); n != 2 {
		t.Fatalf("redemption count want 2, got %d", n)
	}
}

// TestRedeemFinanceUnavailable: with no ledger co-resident the redemption is
// refused and NOTHING is recorded (the org can retry once billing is up).
func TestRedeemFinanceUnavailable(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	insertPromo(t, st, "nf", 10)
	p, _ := st.GetPromo(ctx, "nf")
	if _, _, err := st.redeem(ctx, p, "a", "pro", 1, "card1", 4410, nil, 100); !errors.Is(err, errFinanceUnavailable) {
		t.Fatalf("want errFinanceUnavailable, got %v", err)
	}
	if n, _ := st.CountRedemptions(ctx, "nf"); n != 0 {
		t.Fatalf("failed redeem must record nothing, count=%d", n)
	}
}

// TestSeededLaunchPromo: migrate seeds the real first1000 promo with the
// discounts.md parameters.
func TestSeededLaunchPromo(t *testing.T) {
	st := testStore(t)
	p, err := st.GetPromo(context.Background(), firstThousandCode)
	if err != nil {
		t.Fatalf("seeded promo missing: %v", err)
	}
	if p.PercentOff != 90 || p.MaxRedemptions != 1000 || p.TeamSeatCap != 10 || !p.Active {
		t.Fatalf("seeded promo params wrong: %+v", p)
	}
}
