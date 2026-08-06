package marketing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
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

// TestRedeemGuards proves every abuse guard: exactly-once per org, once per
// instrument, an instrument being REQUIRED, and the hard redemption cap.
func TestRedeemGuards(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	insertPromo(t, st, "cap2", 2)
	p, err := st.GetPromo(ctx, "cap2")
	if err != nil {
		t.Fatalf("get promo: %v", err)
	}

	// org "a" redeems Pro on card1 → claim recorded once, at $44.10.
	_, discount, ok, _ := p.quote("pro", claimSeats)
	if !ok || discount != 4410 {
		t.Fatalf("pro discount want 4410, got %d ok=%v", discount, ok)
	}
	r, already, err := st.redeem(ctx, p, "a", "pro", "card1", discount, 100)
	if err != nil || already {
		t.Fatalf("a first redeem: already=%v err=%v", already, err)
	}
	if r.DiscountCents != 4410 {
		t.Fatalf("recorded claim want 4410, got %d", r.DiscountCents)
	}
	if r.Seats != claimSeats {
		t.Fatalf("recorded seats want %d, got %d", claimSeats, r.Seats)
	}

	// Same org again → idempotent no-op returning the original.
	if _, again, err := st.redeem(ctx, p, "a", "pro", "card1", discount, 101); err != nil || !again {
		t.Fatalf("a second redeem want already=true, got already=%v err=%v", again, err)
	}

	// An ABSENT instrument is refused outright — it is the anti-farming key, and
	// omitting it used to skip the guard entirely.
	if _, _, err := st.redeem(ctx, p, "noinst", "pro", "", discount, 102); !errors.Is(err, errInstrumentRequired) {
		t.Fatalf("empty instrument want errInstrumentRequired, got %v", err)
	}
	// Whitespace is not an instrument either.
	if _, _, err := st.redeem(ctx, p, "noinst", "pro", "   ", discount, 102); !errors.Is(err, errInstrumentRequired) {
		t.Fatalf("blank instrument want errInstrumentRequired, got %v", err)
	}

	// A different org reusing card1 → instrument guard.
	if _, _, err := st.redeem(ctx, p, "b", "pro", "card1", discount, 103); !errors.Is(err, errInstrumentUsed) {
		t.Fatalf("shared instrument want errInstrumentUsed, got %v", err)
	}

	// org "b" on its own card → ok (fills the cap: 2 redemptions).
	if _, _, err := st.redeem(ctx, p, "b", "pro", "card2", discount, 104); err != nil {
		t.Fatalf("b redeem: %v", err)
	}

	// org "c" → cap reached, declined.
	if _, _, err := st.redeem(ctx, p, "c", "pro", "card3", discount, 105); !errors.Is(err, errPromoExhausted) {
		t.Fatalf("over-cap want errPromoExhausted, got %v", err)
	}

	// The counter reflects exactly the two successful redemptions — every refusal
	// above recorded nothing.
	if n, _ := st.CountRedemptions(ctx, "cap2"); n != 2 {
		t.Fatalf("redemption count want 2, got %d", n)
	}
}

// TestRedeemCeiling: the per-redemption ceiling is a backstop no arithmetic can
// walk past. A claim over maxClaimCents — or a nonsense non-positive one — is
// REFUSED, not clamped, and records nothing.
func TestRedeemCeiling(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	insertPromo(t, st, "ceil", 10)
	p, _ := st.GetPromo(ctx, "ceil")

	// The $1,791 figure the old body-supplied seats=10 produced is now refused
	// even if some future caller manages to compute it.
	for _, cents := range []int64{179100, maxClaimCents + 1, 0, -5} {
		if _, _, err := st.redeem(ctx, p, "greedy", "team", "card1", cents, 100); !errors.Is(err, errClaimTooLarge) {
			t.Fatalf("claim %d want errClaimTooLarge, got %v", cents, err)
		}
	}
	// Exactly at the ceiling is allowed — the bound is inclusive.
	if _, _, err := st.redeem(ctx, p, "atcap", "max", "card2", maxClaimCents, 101); err != nil {
		t.Fatalf("claim at ceiling must be allowed, got %v", err)
	}
	if n, _ := st.CountRedemptions(ctx, "ceil"); n != 1 {
		t.Fatalf("only the at-ceiling claim may be recorded, count=%d", n)
	}
}

// TestInstrumentUsedFailsClosed pins the specific defect: instrumentUsed("")
// returned false — "that instrument is free, go ahead" — which made the
// anti-farming guard opt-in by omission. An unverifiable instrument must read as
// USED so no caller can ever be waved through by the guard.
func TestInstrumentUsedFailsClosed(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	for _, empty := range []string{"", "   ", "\t"} {
		used, err := st.instrumentUsed(ctx, "first1000", empty)
		if err != nil {
			t.Fatalf("instrumentUsed(%q): %v", empty, err)
		}
		if !used {
			t.Fatalf("instrumentUsed(%q) must fail CLOSED (true), got false", empty)
		}
	}
	// A real, unseen instrument is still correctly reported unused.
	if used, err := st.instrumentUsed(ctx, "first1000", "pm_fresh"); err != nil || used {
		t.Fatalf("unseen instrument want (false,nil), got (%v,%v)", used, err)
	}
}

// ---- the exploit, at the HTTP seam ----
//
// These drive the SHIPPED route through the real router, because the hole was a
// handler that trusted its input — a store-level test would have proved the
// store fine and missed it entirely.

// planStub is a cloud.PlanChecker: the org's live paid subscription, which is the
// ONLY thing allowed to name the plan a redemption is recorded against.
type planStub struct {
	tier string
	paid bool
	err  error
	orgs []string // every org it was asked about, to prove the caller's own org is used
}

func (p *planStub) ActivePaidPlan(_ context.Context, org string) (string, bool, error) {
	p.orgs = append(p.orgs, org)
	return p.tier, p.paid, p.err
}

// mountPromoRoutes registers the real routes over a fresh store with a given
// subscription authority. Campaigns stay OFF — the shipped state.
func mountPromoRoutes(t *testing.T, plans cloud.PlanChecker) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	s := &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.NewNoOpLogger()},
		State: state{store: testStore(t), plans: plans},
	}
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	compose(app)
	routes(app, cloud.ZipApp(app), s)
	return app, s
}

// revivedPromoRoutes is mountPromoRoutes with a campaign FORCED LIVE and a promo
// row present — the counterfactual the hardening tests need.
//
// The subsystem ships off, so without this every test below would pass trivially
// on the closed-for-business refusal and prove nothing about the guards. These
// tests exist to answer the question that matters if the decision is ever
// reversed: WHEN a campaign is live, is the original hole still closed? The
// campaign state is restored after each test.
func revivedPromoRoutes(t *testing.T, plans cloud.PlanChecker) (*zip.App, *cloud.Service[state]) {
	t.Helper()
	prev := campaignsLive
	campaignsLive = true
	t.Cleanup(func() { campaignsLive = prev })

	app, s := mountPromoRoutes(t, plans)
	insertPromo(t, s.State.store, firstThousandCode, 1000)
	return app, s
}

// TestRedeemIgnoresBodyPlanAndSeats IS THE EXPLOIT REGRESSION.
//
// The old handler read plan and seats off the request body and multiplied them
// into a wallet deposit: {"plan":"team","seats":10} minted 10 × $179.10 =
// $1,791.00 of real spendable credit per org. The plan now comes from the org's
// subscription and the seat count is the single-seat floor, so the SAME body —
// including absurd seat counts — cannot move the recorded figure at all.
func TestRedeemIgnoresBodyPlanAndSeats(t *testing.T) {
	// The org actually holds Pro. Whatever the body claims, Pro is what it gets.
	plans := &planStub{tier: "pro", paid: true}
	app, s := revivedPromoRoutes(t, plans)

	// The original exploit body, plus escalations of it. Each gets its own org and
	// its own instrument, so nothing is refused for an unrelated reason and every
	// case is a genuine first redemption.
	bodies := []string{
		`{"instrument":"pm_a","plan":"team","seats":10}`,      // the exploit, verbatim
		`{"instrument":"pm_b","plan":"team","seats":1000000}`, // escalated
		`{"instrument":"pm_c","plan":"max","seats":-1}`,       // negative seats
		`{"instrument":"pm_d","plan":"enterprise"}`,           // a plan with no list price
		`{"instrument":"pm_e"}`,                               // honest body, for comparison
	}
	wantOrgs := make([]string, 0, len(bodies))
	for i, body := range bodies {
		org := fmt.Sprintf("org%d", i)
		wantOrgs = append(wantOrgs, org)
		code, out := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", org, body)
		if code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("body %s: want 200/201, got %d (%v)", body, code, out)
		}
		// $44.10 — the Pro single-seat discount — every single time.
		if got := out["discountCents"]; got != float64(4410) {
			t.Fatalf("body %s: discountCents want 4410, got %v (full: %v)", body, got, out)
		}
		red, _ := out["redemption"].(map[string]any)
		if red["plan"] != "pro" {
			t.Fatalf("body %s: recorded plan want pro (the org's REAL plan), got %v", body, red["plan"])
		}
		if red["seats"] != float64(claimSeats) {
			t.Fatalf("body %s: recorded seats want %d, got %v", body, claimSeats, red["seats"])
		}
		if red["discountCents"] != float64(4410) {
			t.Fatalf("body %s: recorded claim want 4410, got %v", body, red["discountCents"])
		}
	}
	// The authority was asked about the CALLER'S org every time, in order, and
	// never about an org named anywhere in the body.
	if len(plans.orgs) != len(wantOrgs) {
		t.Fatalf("plan lookups want %d, got %d (%v)", len(wantOrgs), len(plans.orgs), plans.orgs)
	}
	for i, org := range plans.orgs {
		if org != wantOrgs[i] {
			t.Fatalf("plan lookup %d want org %q, got %q", i, wantOrgs[i], org)
		}
	}
	// And the ceiling was never approached: nothing recorded exceeds a Pro month.
	rows, err := s.State.store.db.Query(`SELECT credit_cents FROM marketing_promo_redemptions`)
	if err != nil {
		t.Fatalf("query claims: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cents int64
		if err := rows.Scan(&cents); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if cents != 4410 {
			t.Fatalf("recorded claim want 4410, got %d", cents)
		}
	}
}

// TestRedeemMintsNoCredit is the load-bearing money test: with a REAL ledger
// co-resident and published, a successful self-service redemption deposits
// NOTHING. Credit into an org is an admin decision; this route is not it.
func TestRedeemMintsNoCredit(t *testing.T) {
	fin := newFakeFinance()
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })

	app, _ := revivedPromoRoutes(t, &planStub{tier: "team", paid: true})
	code, out := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", "acme",
		`{"instrument":"pm_team","plan":"team","seats":10}`)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("redeem want 200/201, got %d (%v)", code, out)
	}
	// The redemption succeeded and quoted the single-seat Team discount...
	if out["discountCents"] != float64(17910) {
		t.Fatalf("discountCents want 17910 (ONE seat), got %v", out["discountCents"])
	}
	// ...and not one cent reached the ledger.
	if len(fin.deposits) != 0 {
		t.Fatalf("a promo redemption must deposit NOTHING, got %d deposit(s): %+v", len(fin.deposits), fin.deposits)
	}
}

// TestRedeemRequiresSubscription: the promo is a discount on a plan, so an org
// with no live paid subscription has nothing to discount and is refused — it
// cannot burn a slot out of the capped campaign either.
func TestRedeemRequiresSubscription(t *testing.T) {
	app, s := revivedPromoRoutes(t, &planStub{paid: false}) // resolved: no paid plan
	code, out := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", "freeloader",
		`{"instrument":"pm_1"}`)
	if code != http.StatusForbidden {
		t.Fatalf("no subscription want 403, got %d (%v)", code, out)
	}
	if n, _ := s.State.store.CountRedemptions(context.Background(), firstThousandCode); n != 0 {
		t.Fatalf("a refused redemption must record nothing, count=%d", n)
	}
}

// TestRedeemFailsClosedOnUnverifiablePlan: where the SPEND GATE admits on an
// unreadable subscription authority (refusing there would 402 every paying
// customer during an outage), this surface REFUSES — an outage must not be able
// to manufacture a claim that money is later granted against.
func TestRedeemFailsClosedOnUnverifiablePlan(t *testing.T) {
	cases := map[string]cloud.PlanChecker{
		"authority errored":  &planStub{err: errors.New("commerce unreachable")},
		"authority absent":   nil, // commerce not co-resident (split deploy / stub)
		"paid but unnamable": &planStub{tier: "  ", paid: true},
	}
	for name, plans := range cases {
		t.Run(name, func(t *testing.T) {
			app, s := revivedPromoRoutes(t, plans)
			code, out := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", "acme",
				`{"instrument":"pm_1"}`)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("unverifiable plan want 503, got %d (%v)", code, out)
			}
			if n, _ := s.State.store.CountRedemptions(context.Background(), firstThousandCode); n != 0 {
				t.Fatalf("a refused redemption must record nothing, count=%d", n)
			}
		})
	}
}

// TestRedeemRequiresInstrumentOverHTTP: omitting the anti-farming key is
// REFUSED. It used to be the way to bypass the guard entirely.
func TestRedeemRequiresInstrumentOverHTTP(t *testing.T) {
	app, s := revivedPromoRoutes(t, &planStub{tier: "pro", paid: true})
	for _, body := range []string{`{}`, `{"instrument":""}`, `{"instrument":"   "}`, `{"plan":"team","seats":10}`} {
		code, out := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", "farmer", body)
		if code != http.StatusBadRequest {
			t.Fatalf("body %s: missing instrument want 400, got %d (%v)", body, code, out)
		}
	}
	if n, _ := s.State.store.CountRedemptions(context.Background(), firstThousandCode); n != 0 {
		t.Fatalf("refused redemptions must record nothing, count=%d", n)
	}
}

// TestRedeemOncePerOrgOverHTTP: a second redemption by the same org never
// produces a second claim, and the recorded figure cannot be moved by retrying
// with a bigger body.
func TestRedeemOncePerOrgOverHTTP(t *testing.T) {
	app, s := revivedPromoRoutes(t, &planStub{tier: "pro", paid: true})
	first, out := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", "acme", `{"instrument":"pm_1"}`)
	if first != http.StatusOK && first != http.StatusCreated {
		t.Fatalf("first redeem want 200/201, got %d (%v)", first, out)
	}
	if out["alreadyRedeemed"] != false {
		t.Fatalf("first redeem alreadyRedeemed want false, got %v", out["alreadyRedeemed"])
	}
	// Retry, now claiming Team with 10 seats and a different card.
	code, again := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", "acme",
		`{"instrument":"pm_2","plan":"team","seats":10}`)
	if code != http.StatusOK {
		t.Fatalf("replay want 200, got %d (%v)", code, again)
	}
	if again["alreadyRedeemed"] != true {
		t.Fatalf("replay alreadyRedeemed want true, got %v", again["alreadyRedeemed"])
	}
	red, _ := again["redemption"].(map[string]any)
	if red["discountCents"] != float64(4410) || red["plan"] != "pro" {
		t.Fatalf("replay must return the ORIGINAL claim (pro/4410), got %v", red)
	}
	if n, _ := s.State.store.CountRedemptions(context.Background(), firstThousandCode); n != 1 {
		t.Fatalf("one org must produce exactly one redemption, count=%d", n)
	}
}

// TestRedeemRefusesUnvalidatedPrincipal: the org is the tenant-isolation key and
// comes from the VALIDATED principal, never a header a caller can forge. An
// off-gateway caller presenting only X-Org-Id gets 403 and mints nothing —
// following the tenant-isolation precedent in ops_test.go.
func TestRedeemRefusesUnvalidatedPrincipal(t *testing.T) {
	app, s := revivedPromoRoutes(t, &planStub{tier: "max", paid: true})
	req := httptest.NewRequest(http.MethodPost, "/v1/marketing/promos/first1000/redeem",
		strings.NewReader(`{"instrument":"pm_1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "victim") // no X-User-Id: nothing validated this
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unvalidated principal want 403, got %d", resp.StatusCode)
	}
	if n, _ := s.State.store.CountRedemptions(context.Background(), firstThousandCode); n != 0 {
		t.Fatalf("an unvalidated caller must record nothing, count=%d", n)
	}
}

// ---- the subsystem ships OFF ----

// TestCampaignsShipOff is the guarantee itself: the value compiled into the
// binary is OFF. If someone flips campaignsLive, this fails — which is the
// point, because turning campaigns on must be a reviewed decision and not a
// quiet edit.
func TestCampaignsShipOff(t *testing.T) {
	if campaignsLive {
		t.Fatal("campaignsLive must ship false: no code, coupon or self-service " +
			"redemption is authorized — credit enters an org only via a manual " +
			"admin grant at admin.hanzo.ai")
	}
}

// TestNoPromoSeeded: migrating seeds NOTHING. The unauthorized "First 1,000"
// campaign used to be INSERTed active=1 by this very migration, which is how it
// came to be live in every deployment without anyone deciding to run it.
func TestNoPromoSeeded(t *testing.T) {
	st := testStore(t)
	if _, err := st.GetPromo(context.Background(), firstThousandCode); !errors.Is(err, errNotFound) {
		t.Fatalf("first1000 must not be seeded, got err=%v", err)
	}
	promos, err := st.ListPromos(context.Background())
	if err != nil {
		t.Fatalf("list promos: %v", err)
	}
	if len(promos) != 0 {
		t.Fatalf("a fresh store must carry NO promos, got %d: %+v", len(promos), promos)
	}
}

// TestMigratePurgesSeededPromo: deleting the INSERT does not help a database
// that already ran the old migration, so migrate DELETES the row. This simulates
// exactly that: an existing deployment whose table already holds an active
// first1000, which needs no code at all to be redeemed.
func TestMigratePurgesSeededPromo(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	// Re-create the row the old migration left behind, and a redemption against it.
	insertPromo(t, st, firstThousandCode, 1000)
	if _, err := st.GetPromo(ctx, firstThousandCode); err != nil {
		t.Fatalf("precondition: seeded promo should exist, got %v", err)
	}
	if _, _, err := st.redeem(ctx, Promo{Code: firstThousandCode, MaxRedemptions: 1000},
		"legacyorg", "pro", "card1", 4410, 100); err != nil {
		t.Fatalf("precondition redeem: %v", err)
	}

	// Boot again — the purge runs on every migrate.
	if err := st.migratePromos(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.GetPromo(ctx, firstThousandCode); !errors.Is(err, errNotFound) {
		t.Fatalf("migrate must PURGE the unauthorized promo, got err=%v", err)
	}
	// The redemption history survives: it is the audit trail of what happened
	// while the campaign was live, and destroying it would destroy the evidence.
	if n, _ := st.CountRedemptions(ctx, firstThousandCode); n != 1 {
		t.Fatalf("redemption history must be PRESERVED as evidence, count=%d", n)
	}
}

// TestRedeemRefusedWhileClosed: with the subsystem off, a redemption attempt is
// refused — even for an org holding a real paid plan, and even if a promo row
// somehow exists in the table.
func TestRedeemRefusedWhileClosed(t *testing.T) {
	fin := newFakeFinance()
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })

	app, s := mountPromoRoutes(t, &planStub{tier: "team", paid: true})
	// Hand-insert a live promo, so the refusal cannot be mistaken for "no row".
	insertPromo(t, s.State.store, firstThousandCode, 1000)

	code, out := call(t, app, http.MethodPost, "/v1/marketing/promos/first1000/redeem", "acme",
		`{"instrument":"pm_1","plan":"team","seats":10}`)
	if code != http.StatusForbidden {
		t.Fatalf("redemption while closed want 403, got %d (%v)", code, out)
	}
	if n, _ := s.State.store.CountRedemptions(context.Background(), firstThousandCode); n != 0 {
		t.Fatalf("a closed subsystem must record nothing, count=%d", n)
	}
	if len(fin.deposits) != 0 {
		t.Fatalf("a closed subsystem must deposit nothing, got %+v", fin.deposits)
	}
}

// TestQuoteClosedWhileOff: the pricing surface must not advertise an offer that
// redeem would refuse, even if a promo row exists.
func TestQuoteClosedWhileOff(t *testing.T) {
	app, s := mountPromoRoutes(t, &planStub{tier: "pro", paid: true})
	insertPromo(t, s.State.store, firstThousandCode, 1000)

	code, out := call(t, app, http.MethodGet,
		"/v1/marketing/promos/first1000/eligibility?plan=pro&seats=1", "acme", "")
	if code != http.StatusOK {
		t.Fatalf("eligibility want 200, got %d (%v)", code, out)
	}
	if out["eligible"] != false {
		t.Fatalf("nothing may quote as eligible while closed, got %v", out)
	}
	if out["reason"] != "promo redemption is closed" {
		t.Fatalf("reason want the closed message, got %v", out["reason"])
	}
}
