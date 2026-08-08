// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// This file imports NO money package, and that absence is the point: with the
// deposit gone there is no finance client, no money.Amount and no DepositInput
// left in the promo path, so re-introducing a mint here would have to start by
// re-introducing an import.

// promos.go implements the launch promo — "First 1,000: 90% off your first
// month" (discounts.md).
//
// A REDEMPTION RECORDS A CLAIM. IT DOES NOT MOVE MONEY.
//
// This surface used to realize a redemption as a wallet CREDIT through the
// finance ledger, and that was a self-service money mint: the only gate was
// "any validated principal", the plan and seat count came off the REQUEST BODY
// unvalidated, and nothing ever collected the charge the discount was supposed
// to be against. A caller could post plan=team&seats=10 and deposit $1,791 of
// real spendable credit into their own org, once per org, up to the 1,000-org
// cap — with open signup and a personal org per account, ~$1.79M of self-serve
// credit. The deposit is gone.
//
// CREDIT INTO AN ORG IS AN ADMIN DECISION — made deliberately, through the
// admin surface, against an auditable ledger. That is the same rule that
// deleted the automatic $5 starter grant (see the SpendGate prose in
// middleware_spend.go): an automatic path that creates money is not a feature
// to fix but a mechanism to remove, because a money-mint left switched off is
// one flag away from switched on. So there is no deposit here to re-enable and
// no finance client to hand it. What a redemption produces is a ROW — the
// org, the server-derived plan, the discount it claims, and when — which is
// exactly the auditable evidence an admin grants against.
//
// ABUSE GUARDS (enforced server-side, never trusted from the client):
//   - Plan is DERIVED, never accepted. cloud.PlanChecker (the org's live
//     ACTIVE/TRIALING paid subscription) is the only source; an org with no
//     qualifying subscription cannot redeem. RedeemInput carries no plan and
//     no seats, so there is no field left to inflate.
//   - FAIL CLOSED on an unreadable plan authority. The spend gate deliberately
//     fails OPEN on the same read (refusing on an outage 402s every paying
//     customer at once); here the asymmetry INVERTS — failing open on a claim
//     that money is later granted against would let an outage manufacture the
//     evidence. An authority that cannot answer refuses.
//   - Instrument is REQUIRED and fails closed. It is the anti-farming key, and
//     an ABSENT instrument is not "unused", it is unverifiable — see
//     instrumentUsed.
//   - Hard counter: at most maxRedemptions across all orgs; the next is
//     declined. Checked + inserted under one serialized mutate.
//   - One redemption per org: PRIMARY KEY (code, org).
//   - One redemption per payment instrument: UNIQUE (code, instrument).
//   - Ceiling: maxClaimCents bounds any single recorded claim, so no
//     combination of catalog values can record an unbounded figure.
//   - Free plan excluded: Developer is $0, nothing to discount.

// planListCents is the month list price in minor units (USD cents), from
// discounts.md. Team is per seat. Developer/free/unknown → 0 (not discountable).
func planListCents(plan string) int64 {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "pro":
		return 4900
	case "max":
		return 20000
	case "team":
		return 19900
	default:
		return 0
	}
}

// Promo is a launch-promo definition (a small, seeded set — today just the one).
type Promo struct {
	// Code is the promo id, e.g. "first1000".
	Code string `json:"code"`
	// Description is the human-readable offer.
	Description string `json:"description"`
	// PercentOff is the discount applied to ONE month's list price.
	PercentOff int `json:"percentOff"`
	// MaxRedemptions is the hard fleet-wide cap; the redemption past it is
	// declined.
	MaxRedemptions int `json:"maxRedemptions"`
	// TeamSeatCap is how many Team seats bill at the promo rate; seats beyond it
	// bill at list.
	TeamSeatCap int `json:"teamSeatCap"`
	// Plans is the csv of eligible plan ids ("pro,max,team").
	Plans string `json:"plans"`
	// Active is false for a promo that is no longer offered; an inactive promo
	// quotes as ineligible and refuses to redeem.
	Active bool `json:"active"`
	// CreatedAt is unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// Redemption is one org's use of a promo.
type Redemption struct {
	// Code is the promo redeemed.
	Code       string `json:"code"`
	Org        string `json:"-"`
	Instrument string `json:"-"`
	// Plan and Seats are what was redeemed against. Both are DERIVED server-side
	// — Plan from the org's live paid subscription, Seats from claimSeats — and
	// neither is ever read from the request.
	Plan  string `json:"plan"`
	Seats int    `json:"seats"`
	// DiscountCents is the month-one discount this redemption CLAIMS, in USD
	// cents. It is a recorded figure, NOT a balance: nothing was credited and no
	// wallet moved. An admin granting against this claim is what would make it
	// money, and that decision happens on the admin surface, not here.
	DiscountCents int64 `json:"discountCents"`
	// RedeemedAt is unix seconds.
	RedeemedAt int64 `json:"redeemedAt"`
}

// Quote is a pure eligibility + math result (no side effects).
type Quote struct {
	// Code, Plan and Seats echo what was quoted.
	Code  string `json:"code"`
	Plan  string `json:"plan"`
	Seats int    `json:"seats"`
	// Eligible says whether a redeem would be accepted right now; Reason says
	// why not when it would not.
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
	// ListCents is the undiscounted month price, ChargeCents what would be
	// charged, DiscountCents the difference — all in USD cents.
	ListCents     int64 `json:"listCents"`
	ChargeCents   int64 `json:"chargeCents"`
	DiscountCents int64 `json:"discountCents"`
	// Remaining is how many redemptions are left under the fleet-wide cap.
	Remaining int `json:"remaining"`
}

// promo sentinel errors → HTTP status.
var (
	errPromoExhausted     = errors.New("promo redemption cap reached")
	errInstrumentUsed     = errors.New("payment instrument already redeemed this promo")
	errInstrumentRequired = errors.New("payment instrument required to redeem")
	errNoQualifyingPlan   = errors.New("no active paid subscription to redeem against")
	errPlanUnverifiable   = errors.New("subscription could not be verified")
	errClaimTooLarge      = errors.New("computed discount exceeds the per-redemption ceiling")
)

// maxClaimCents is the HARD server-side ceiling on any single recorded claim, in
// USD cents. It is a backstop, not the business rule: the plan is derived from
// the org's own subscription and claimSeats bounds the seat count, so a claim
// should never approach this. If one does, the arithmetic upstream is wrong and
// the redemption is REFUSED rather than clamped — a silent clamp would record a
// wrong figure and hide the bug that produced it.
//
// $250 sits comfortably above the largest legitimate single-seat month (Max at
// $200 list → $180 discount) and far below what an unbounded seat count could
// once produce ($1,791 at the old body-supplied seats=10).
const maxClaimCents int64 = 25000

// claimSeats is the seat count a redemption is recorded at. It is ONE, always.
//
// The seat count is not resolvable from a server-side authority on this surface
// — cloud.PlanChecker answers the tier, not the quantity, and the org's seat
// roster lives behind the team surface. Rather than accept a number the caller
// asserts about itself (which is the defect this file exists to close), a
// redemption records the SINGLE-SEAT floor. That is the smallest honest claim,
// never an inflatable ceiling, and an admin evaluating the claim resolves the
// org's real seat count against real subscription data at grant time.
const claimSeats = 1

// redeemMu serializes the counter-guarded mutate (count → credit → record) so
// two concurrent redeems can never both slip past the cap.
var redeemMu sync.Mutex

// firstThousandCode is the id of the WITHDRAWN launch promo. It survives as the
// key the migration purges, not as a campaign — see migratePromos.
const firstThousandCode = "first1000"

// campaignsLive is the promo subsystem's master state. IT IS OFF, and OFF is the
// default the code ships in — not a state an environment has to remember to
// configure.
//
// No code, coupon or self-service redemption is authorized. Credit enters an org
// exactly one way: a MANUAL, per-org admin grant through admin.hanzo.ai, against
// an auditable ledger (apps/admin/customer GrantCredit, super-admin gated). The
// "First 1,000" campaign was never authorized and shipped live by accident,
// which is the whole reason this constant exists.
//
// IT IS READ FROM NOTHING. No env var, no platform switch, no database column,
// no Deps field — the value is right here, so turning campaigns on is a code
// change that goes through review, not a flag someone flips at 2am to unblock a
// demo. TestCampaignsShipOff asserts the shipped value, so an edit that flips it
// fails the build rather than sliding through.
//
// It is a var only so the hardening tests can exercise the enabled path and
// prove that a REVIVED campaign is still not exploitable (see liveCampaigns in
// promos_test.go). Nothing in the running binary writes it.
//
// Turning it on is not sufficient to run a campaign and is not meant to be:
// there is no promo row to redeem either (the seed is deleted and the migration
// purges the old one), so reviving a campaign takes a deliberate decision about
// WHAT the campaign is, not just a boolean. The guards this file enforces —
// server-derived plan, required instrument, per-org and per-instrument
// uniqueness, the hard ceiling, and above all NO CREDIT MINTING — hold whatever
// this is set to, so a revival cannot resurrect the original hole.
var campaignsLive = false

func (s *Store) migratePromos() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS marketing_promos (
  code            TEXT PRIMARY KEY,
  description     TEXT NOT NULL DEFAULT '',
  percent_off     INTEGER NOT NULL,
  max_redemptions INTEGER NOT NULL,
  team_seat_cap   INTEGER NOT NULL DEFAULT 10,
  plans           TEXT NOT NULL DEFAULT 'pro,max,team',
  active          INTEGER NOT NULL DEFAULT 1,
  created_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS marketing_promo_redemptions (
  code            TEXT NOT NULL,
  org             TEXT NOT NULL,
  plan            TEXT NOT NULL,
  seats           INTEGER NOT NULL DEFAULT 1,
  instrument      TEXT NOT NULL DEFAULT '',
  credit_cents    INTEGER NOT NULL DEFAULT 0,
  credit_entry_id TEXT NOT NULL DEFAULT '',
  redeemed_at     INTEGER NOT NULL,
  PRIMARY KEY (code, org)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_promo_instrument ON marketing_promo_redemptions(code, instrument) WHERE instrument <> '';
CREATE INDEX IF NOT EXISTS ix_promo_redemptions_org ON marketing_promo_redemptions(org);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("marketing migrate promos: %w", err)
	}
	// NOTHING IS SEEDED HERE, AND THE first1000 SEED IS PURGED.
	//
	// This used to INSERT the "First 1,000" campaign with active=1 on every
	// migrate, which is how an UNAUTHORIZED campaign came to be live in the first
	// place: a code change put a redeemable promo into every deployment's database
	// without anyone deciding to run it. A campaign is a business decision, and a
	// business decision does not belong in a schema migration.
	//
	// Deleting the INSERT is not enough on its own — every database that ever ran
	// the old migration still HAS the row, and a promo already in the table needs
	// no code to be redeemed. So the migration now DELETES it. The purge is
	// idempotent and runs on every boot, which also means a row re-inserted by
	// hand does not survive a restart.
	//
	// REDEMPTIONS ARE DELIBERATELY NOT DELETED. marketing_promo_redemptions is the
	// evidence of what happened while the campaign was live; destroying it would
	// destroy the audit trail exactly when it matters. The rows are inert once the
	// promo is gone (redeem resolves the promo first and 404s), and they never
	// corresponded to credit in any case — see the header.
	if _, err := s.db.Exec(`DELETE FROM marketing_promos WHERE code=?`, firstThousandCode); err != nil {
		return fmt.Errorf("marketing purge unauthorized promo: %w", err)
	}
	return nil
}

func (s *Store) GetPromo(ctx context.Context, code string) (Promo, error) {
	var p Promo
	var active int
	err := s.db.QueryRowContext(ctx,
		`SELECT code,description,percent_off,max_redemptions,team_seat_cap,plans,active,created_at FROM marketing_promos WHERE code=?`, code).
		Scan(&p.Code, &p.Description, &p.PercentOff, &p.MaxRedemptions, &p.TeamSeatCap, &p.Plans, &active, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Promo{}, errNotFound
	}
	if err != nil {
		return Promo{}, fmt.Errorf("get promo: %w", err)
	}
	p.Active = active != 0
	return p, nil
}

func (s *Store) ListPromos(ctx context.Context) ([]Promo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT code,description,percent_off,max_redemptions,team_seat_cap,plans,active,created_at FROM marketing_promos ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list promos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Promo, 0, 4)
	for rows.Next() {
		var p Promo
		var active int
		if err := rows.Scan(&p.Code, &p.Description, &p.PercentOff, &p.MaxRedemptions, &p.TeamSeatCap, &p.Plans, &active, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Active = active != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CountRedemptions(ctx context.Context, code string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM marketing_promo_redemptions WHERE code=?`, code).Scan(&n); err != nil {
		return 0, fmt.Errorf("count redemptions: %w", err)
	}
	return n, nil
}

func (s *Store) GetRedemption(ctx context.Context, code, org string) (Redemption, bool, error) {
	var r Redemption
	err := s.db.QueryRowContext(ctx,
		`SELECT code,org,plan,seats,instrument,credit_cents,redeemed_at FROM marketing_promo_redemptions WHERE code=? AND org=?`, code, org).
		Scan(&r.Code, &r.Org, &r.Plan, &r.Seats, &r.Instrument, &r.DiscountCents, &r.RedeemedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Redemption{}, false, nil
	}
	if err != nil {
		return Redemption{}, false, fmt.Errorf("get redemption: %w", err)
	}
	return r, true, nil
}

// instrumentUsed reports whether this promo has already been redeemed on this
// payment instrument — the anti-farming guard.
//
// AN EMPTY INSTRUMENT IS "USED", NOT "UNUSED". This returned false for "",
// which read as "that instrument is free, go ahead" and made the guard opt-in:
// omitting the field entirely skipped the only check standing between one
// person and one redemption per account they could create. An absent instrument
// is not evidence of a fresh card, it is the ABSENCE of evidence, and a guard
// that cannot verify must refuse. The uniqueness index deliberately excludes ”
// (WHERE instrument <> ”), so the database will not catch this either — the
// refusal has to happen here.
//
// redeem() rejects an empty instrument up front with errInstrumentRequired so
// the caller gets an actionable message. This stays fail-closed regardless, so
// no future caller can reach the guard and be waved through by it.
func (s *Store) instrumentUsed(ctx context.Context, code, instrument string) (bool, error) {
	if strings.TrimSpace(instrument) == "" {
		return true, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM marketing_promo_redemptions WHERE code=? AND instrument=?`, code, instrument).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("instrument check: %w", err)
	}
	return true, nil
}

// ---- pure eligibility math (unit-tested) ----

// coversPlan reports whether the promo applies to plan.
func (p Promo) coversPlan(plan string) bool {
	plan = strings.ToLower(strings.TrimSpace(plan))
	for _, c := range strings.Split(p.Plans, ",") {
		if strings.TrimSpace(c) == plan {
			return true
		}
	}
	return false
}

// quote computes the month-1 charge and the discount VALUE (the wallet credit),
// in minor units, for a plan+seats under the promo. It is a pure function — the
// heart of the promo, tested directly. For per-seat (team) plans the promo rate
// applies to at most TeamSeatCap seats; the rest bill at list.
func (p Promo) quote(plan string, seats int) (chargeCents, discountCents int64, ok bool, reason string) {
	list := planListCents(plan)
	if list == 0 {
		return 0, 0, false, "plan is free or unknown; nothing to discount"
	}
	if !p.coversPlan(plan) {
		return 0, 0, false, "promo does not cover plan " + plan
	}
	perDiscount := (list*int64(p.PercentOff) + 50) / 100 // round half up
	perCharge := list - perDiscount
	if strings.EqualFold(plan, "team") {
		if seats <= 0 {
			seats = 1
		}
		promoSeats := seats
		if promoSeats > p.TeamSeatCap {
			promoSeats = p.TeamSeatCap
		}
		listSeats := seats - promoSeats
		return int64(promoSeats)*perCharge + int64(listSeats)*list, int64(promoSeats) * perDiscount, true, ""
	}
	return perCharge, perDiscount, true, ""
}

// ---- redeem (serialized, exactly-once) ----

// redeem records org's claim on a promo: guard the cap + one-per-org + one-per-
// instrument + the ceiling, then write the row. All under one mutex so the cap
// can never be raced past. Returns the recorded redemption; a repeat by the same
// org is an idempotent no-op returning the original.
//
// IT MOVES NO MONEY, and takes no finance client to move it with. plan and
// discountCents arrive already DERIVED by redeemPromo from the org's own
// subscription — never from the request — and this function's job is to decide
// whether the claim may be recorded at all, not to fund it.
func (s *Store) redeem(ctx context.Context, p Promo, org, plan string, instrument string, discountCents int64, now int64) (Redemption, bool, error) {
	redeemMu.Lock()
	defer redeemMu.Unlock()

	if prev, ok, err := s.GetRedemption(ctx, p.Code, org); err != nil {
		return Redemption{}, false, err
	} else if ok {
		return prev, true, nil // already redeemed — idempotent
	}
	// The ceiling is checked under the lock with every other guard, so it holds on
	// the value actually about to be written rather than one computed earlier.
	if discountCents <= 0 || discountCents > maxClaimCents {
		return Redemption{}, false, errClaimTooLarge
	}
	count, err := s.CountRedemptions(ctx, p.Code)
	if err != nil {
		return Redemption{}, false, err
	}
	if count >= p.MaxRedemptions {
		return Redemption{}, false, errPromoExhausted
	}
	if strings.TrimSpace(instrument) == "" {
		return Redemption{}, false, errInstrumentRequired
	}
	if used, err := s.instrumentUsed(ctx, p.Code, instrument); err != nil {
		return Redemption{}, false, err
	} else if used {
		return Redemption{}, false, errInstrumentUsed
	}
	r := Redemption{
		Code: p.Code, Org: org, Plan: plan, Seats: claimSeats, Instrument: instrument,
		DiscountCents: discountCents, RedeemedAt: now,
	}
	// credit_cents is the column's historical name; it stores the CLAIMED discount
	// and no longer corresponds to any credit. credit_entry_id keeps its NOT NULL
	// DEFAULT '' and is never written, because there is no ledger entry to name.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_promo_redemptions (code,org,plan,seats,instrument,credit_cents,redeemed_at)
		 VALUES (?,?,?,?,?,?,?)`,
		r.Code, r.Org, r.Plan, r.Seats, r.Instrument, r.DiscountCents, r.RedeemedAt); err != nil {
		return Redemption{}, false, fmt.Errorf("record redemption: %w", err)
	}
	return r, false, nil
}

// ---- handlers ----

// PromoRef addresses one promo.
type PromoRef struct {
	// Code is the promo code from the path, e.g. "first1000".
	Code string `json:"code"`
}

// QuoteQuery asks what a promo would cost a given plan and seat count.
type QuoteQuery struct {
	// Code is the promo code from the path.
	Code string `json:"code"`
	// Plan is the plan being priced: pro, max or team. Anything else (including
	// the free Developer plan) has no list price and so nothing to discount.
	Plan string `json:"plan"`
	// Seats is the Team seat count; 0 means 1, and it is ignored for the
	// single-seat plans.
	Seats int `json:"seats"`
}

// PromoStatus is one promo with its live redemption counters.
type PromoStatus struct {
	Promo Promo `json:"promo"`
	// Redeemed is how many orgs have taken it, Remaining how many are left under
	// the fleet-wide cap.
	Redeemed  int `json:"redeemed"`
	Remaining int `json:"remaining"`
}

// PromoList is every promo the deployment offers.
type PromoList struct {
	Data []PromoStatus `json:"data"`
}

// RedeemInput redeems a promo for the caller's org.
//
// IT CARRIES NO PLAN AND NO SEATS, deliberately. Both used to be read straight
// off the request body and multiplied into a wallet deposit, which let a caller
// name their own price. They are now derived from the org's live subscription,
// and the fields are GONE rather than validated — a field that does not exist
// cannot be trusted by the next person to touch this handler.
type RedeemInput struct {
	// Code is the promo code from the path.
	Code string `json:"code"`
	// Instrument identifies the payment method. It is the anti-farming key: one
	// redemption per instrument, fleet-wide, and it is REQUIRED — an absent
	// instrument is refused, never waved through.
	Instrument string `json:"instrument"`
}

// RedeemResult is a completed redemption and the month-one math behind it.
type RedeemResult struct {
	Redemption Redemption `json:"redemption"`
	// ChargeCents is what month one costs after the discount, DiscountCents the
	// discount that produced it. Both are quoted figures against the org's
	// derived plan — NOTHING WAS CREDITED and no wallet moved.
	ChargeCents   int64 `json:"chargeCents"`
	DiscountCents int64 `json:"discountCents"`
	// AlreadyRedeemed is true when this org had already taken the promo and the
	// call was an idempotent replay.
	AlreadyRedeemed bool `json:"alreadyRedeemed"`
}

// listPromos returns every promo the deployment offers with its live counters:
// how many orgs have redeemed it and how many redemptions remain under the cap.
// The promos are fleet-wide, not per-org — only the counters move.
//
// Response: {"data": [{"promo": {"code": "first1000", "percentOff": 90, "maxRedemptions": 1000, "active": true}, "redeemed": 137, "remaining": 863}]}
func (o ops) listPromos(ctx context.Context, _ *struct{}) (*PromoList, error) {
	if _, err := tenant(ctx); err != nil {
		return nil, err
	}
	promos, err := o.s.State.store.ListPromos(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]PromoStatus, 0, len(promos))
	for _, p := range promos {
		n, _ := o.s.State.store.CountRedemptions(ctx, p.Code)
		out = append(out, PromoStatus{Promo: p, Redeemed: n, Remaining: max0(p.MaxRedemptions - n)})
	}
	return &PromoList{Data: out}, nil
}

// quotePromo prices a promo against a plan and seat count. It is PURE: nothing
// is redeemed, credited or counted, so it is safe to call from a pricing page on
// every keystroke. An inactive promo or an exhausted cap quotes ineligible with
// the reason rather than erroring.
//
// Example: {"code": "first1000", "plan": "team", "seats": 12}
// Response: {"code": "first1000", "plan": "team", "seats": 12, "eligible": true, "listCents": 19900, "chargeCents": 418900, "discountCents": 179100, "remaining": 863}
func (o ops) quotePromo(ctx context.Context, in *QuoteQuery) (*Quote, error) {
	if _, err := tenant(ctx); err != nil {
		return nil, err
	}
	p, err := o.s.State.store.GetPromo(ctx, strings.TrimSpace(in.Code))
	if err != nil {
		return nil, mapErr(err, "promo not found")
	}
	plan := strings.ToLower(strings.TrimSpace(in.Plan))
	seats := in.Seats
	if seats <= 0 {
		seats = 1
	}
	charge, discount, ok, reason := p.quote(plan, seats)
	n, _ := o.s.State.store.CountRedemptions(ctx, p.Code)
	remaining := max0(p.MaxRedemptions - n)
	switch {
	case !campaignsLive:
		// The subsystem is off, so nothing quotes as available — a pricing page
		// must never advertise an offer that redeem would refuse.
		ok, reason = false, "promo redemption is closed"
	case remaining == 0:
		ok, reason = false, "promo redemption cap reached"
	case !p.Active:
		ok, reason = false, "promo is not active"
	}
	return &Quote{
		Code: p.Code, Plan: plan, Seats: seats, Eligible: ok, Reason: reason,
		ListCents: planListCents(plan), ChargeCents: charge, DiscountCents: discount, Remaining: remaining,
	}, nil
}

// redeemPromo records the caller org's claim on a promo. NOTHING IS CREDITED:
// the redemption is a row, and credit into an org is an admin decision made on
// the admin surface against an auditable ledger.
//
// The plan is DERIVED from the org's live ACTIVE/TRIALING paid subscription and
// can never be named by the caller — an org with no qualifying subscription is
// refused, and so is one whose subscription cannot be read. The seat count is
// the single-seat floor (claimSeats), so the recorded figure has no input that
// can inflate it.
//
// Guards run under one lock so the cap cannot be raced past: the fleet-wide
// redemption cap, one redemption per org, one per payment instrument (REQUIRED),
// and the per-redemption ceiling.
//
// It is IDEMPOTENT: an org that already redeemed gets its original redemption
// back with alreadyRedeemed true.
//
// Example: {"code": "first1000", "instrument": "pm_1QxYz2AbCdEf"}
func (o ops) redeemPromo(ctx context.Context, in *RedeemInput) (*RedeemResult, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	// The subsystem is OFF. Refused before any promo is looked up, so the answer
	// cannot depend on what happens to be sitting in the table.
	if !campaignsLive {
		return nil, zip.ErrForbidden("promo redemption is closed")
	}
	p, err := o.s.State.store.GetPromo(ctx, strings.TrimSpace(in.Code))
	if err != nil {
		return nil, mapErr(err, "promo not found")
	}
	if !p.Active {
		return nil, zip.ErrConflict("promo is not active")
	}
	// The plan the org ACTUALLY holds — the one fact this decision turns on, and
	// the one the caller has no say in.
	plan, err := o.orgPlan(ctx, org)
	switch {
	case errors.Is(err, errNoQualifyingPlan):
		return nil, zip.ErrForbidden("no active paid subscription to redeem against")
	case errors.Is(err, errPlanUnverifiable):
		return nil, zip.Errorf(http.StatusServiceUnavailable, "subscription could not be verified; try again")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve plan: %v", err)
	}
	charge, discount, eligible, reason := p.quote(plan, claimSeats)
	if !eligible {
		// The org holds a real plan the promo does not cover (or one with no list
		// price). Not a client error to correct — there is no input to change.
		return nil, zip.ErrForbidden(reason)
	}
	r, already, err := o.s.State.store.redeem(ctx, p, org, plan, strings.TrimSpace(in.Instrument), discount, time.Now().Unix())
	switch {
	case errors.Is(err, errPromoExhausted):
		return nil, zip.ErrConflict("promo redemption cap reached")
	case errors.Is(err, errInstrumentRequired):
		return nil, zip.ErrBadRequest("payment instrument required to redeem")
	case errors.Is(err, errInstrumentUsed):
		return nil, zip.ErrForbidden("payment instrument already redeemed this promo")
	case errors.Is(err, errClaimTooLarge):
		// The ceiling tripped, which means the catalog math is wrong. Refuse and
		// make it loud rather than record a figure nobody intended.
		return nil, zip.Errorf(http.StatusInternalServerError, "redeem: discount failed the safety ceiling")
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "redeem: %v", err)
	}
	if !already {
		cloud.Created(ctx)
	}
	return &RedeemResult{Redemption: r, ChargeCents: charge, DiscountCents: discount, AlreadyRedeemed: already}, nil
}

// orgPlan resolves the plan tier org actually holds, from its live ACTIVE or
// TRIALING paid subscription. It is the ONLY source of the plan a redemption is
// recorded against.
//
// IT FAILS CLOSED, and that is the opposite of what the same read does at the
// spend gate. SpendGate treats an unreadable plan authority as LicenceUnknown
// and ADMITS, because refusing on an outage would 402 every paying customer at
// once — there, the cost of a wrong "no" dwarfs the cost of a wrong "yes". Here
// the asymmetry inverts: a wrong "yes" burns a slot out of a capped campaign and
// records evidence that an org is owed money, on the say-so of machinery that
// could not actually confirm it. A claim manufactured by an outage is worse than
// a redemption the org can retry in a minute, so an authority that cannot answer
// refuses.
func (o ops) orgPlan(ctx context.Context, org string) (string, error) {
	plans := o.s.State.plans
	if plans == nil {
		// Commerce not co-resident (split deploy / disabled stub). The subscription
		// is unreadable, not absent — never assume a plan.
		return "", errPlanUnverifiable
	}
	tier, paid, err := plans.ActivePaidPlan(ctx, org)
	if err != nil {
		return "", errPlanUnverifiable
	}
	if !paid {
		// A definitive "this org has no live paid subscription".
		return "", errNoQualifyingPlan
	}
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		// Paid but unnameable — cannot price it, so cannot record a claim on it.
		return "", errPlanUnverifiable
	}
	return tier, nil
}

// getRedemption returns the caller org's OWN redemption of a promo — an
// org-scoped read, so it can never surface another tenant's. Not found when this
// org has not redeemed it.
//
// Example: {"code": "first1000"}
func (o ops) getRedemption(ctx context.Context, in *PromoRef) (*Redemption, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	r, found, err := o.s.State.store.GetRedemption(ctx, strings.TrimSpace(in.Code), org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "redemption: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("no redemption for this org")
	}
	return &r, nil
}

// max0 clamps a count to >= 0.
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
