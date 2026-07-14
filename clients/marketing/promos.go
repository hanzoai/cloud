// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// promos.go implements the launch promo — "First 1,000: 90% off your first
// month" (discounts.md) — on the billing rail. The billing rail has no
// subscription-coupon primitive, so a redemption is realized as a NON-CASH
// wallet CREDIT of the discount value through the finance ledger
// (finance.Deposit, Tags "credit:promo-<code>", Ref "promo-<code>:<org>"): the
// credit offsets month 1, month 2 renews at list. The deposit is idempotent on
// its Ref, so a redemption credits AT MOST ONCE per org — and because it is a
// positive credit (never a debit) it can never overdraw a wallet.
//
// ABUSE GUARDS (discounts.md, enforced server-side, never trusted from client):
//   - Hard counter: at most maxRedemptions across all orgs; the 1,001st is
//     declined. Checked + inserted under one serialized mutate.
//   - One redemption per org: PRIMARY KEY (code, org).
//   - One redemption per payment instrument: UNIQUE (code, instrument) — the
//     hard stop against multi-account farming.
//   - Team seat cap: at most teamSeatCap seats bill at the promo rate; seats
//     beyond bill at list, bounding worst-case exposure per org.
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
	Code           string `json:"code"`
	Description    string `json:"description"`
	PercentOff     int    `json:"percentOff"`
	MaxRedemptions int    `json:"maxRedemptions"`
	TeamSeatCap    int    `json:"teamSeatCap"`
	Plans          string `json:"plans"` // csv of eligible plan ids
	Active         bool   `json:"active"`
	CreatedAt      int64  `json:"createdAt"`
}

// Redemption is one org's use of a promo.
type Redemption struct {
	Code          string `json:"code"`
	Org           string `json:"-"`
	Plan          string `json:"plan"`
	Seats         int    `json:"seats"`
	Instrument    string `json:"-"`
	CreditCents   int64  `json:"creditCents"`
	CreditEntryID string `json:"creditEntryId"`
	RedeemedAt    int64  `json:"redeemedAt"`
}

// Quote is a pure eligibility + math result (no side effects).
type Quote struct {
	Code          string `json:"code"`
	Plan          string `json:"plan"`
	Seats         int    `json:"seats"`
	Eligible      bool   `json:"eligible"`
	Reason        string `json:"reason,omitempty"`
	ListCents     int64  `json:"listCents"`
	ChargeCents   int64  `json:"chargeCents"`
	DiscountCents int64  `json:"discountCents"`
	Remaining     int    `json:"remaining"`
}

// promo sentinel errors → HTTP status.
var (
	errPromoExhausted     = errors.New("promo redemption cap reached")
	errInstrumentUsed     = errors.New("payment instrument already redeemed this promo")
	errFinanceUnavailable = errors.New("billing ledger unavailable")
)

// redeemMu serializes the counter-guarded mutate (count → credit → record) so
// two concurrent redeems can never both slip past the cap.
var redeemMu sync.Mutex

// firstThousandCode is the launch promo id.
const firstThousandCode = "first1000"

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
	// Seed the launch promo once (idempotent). Values are the discounts.md spec.
	_, err := s.db.Exec(
		`INSERT INTO marketing_promos (code,description,percent_off,max_redemptions,team_seat_cap,plans,active,created_at)
		 VALUES (?,?,?,?,?,?,1,?) ON CONFLICT(code) DO NOTHING`,
		firstThousandCode, "First 1,000: 90% off your first month (Pro, Max, or Team)",
		90, 1000, 10, "pro,max,team", time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("marketing seed promo: %w", err)
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
		`SELECT code,org,plan,seats,instrument,credit_cents,credit_entry_id,redeemed_at FROM marketing_promo_redemptions WHERE code=? AND org=?`, code, org).
		Scan(&r.Code, &r.Org, &r.Plan, &r.Seats, &r.Instrument, &r.CreditCents, &r.CreditEntryID, &r.RedeemedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Redemption{}, false, nil
	}
	if err != nil {
		return Redemption{}, false, fmt.Errorf("get redemption: %w", err)
	}
	return r, true, nil
}

func (s *Store) instrumentUsed(ctx context.Context, code, instrument string) (bool, error) {
	if instrument == "" {
		return false, nil
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

// redeem realizes a promo for org: guard the cap + one-per-org + one-per-
// instrument, credit the discount value to the org's wallet (exactly-once via
// the deposit Ref), then record the redemption. All under one mutex so the cap
// can never be raced past. Returns the recorded redemption; a repeat by the same
// org is an idempotent no-op returning the original.
func (s *Store) redeem(ctx context.Context, p Promo, org, plan string, seats int, instrument string, discountCents int64, fin finance.Client, now int64) (Redemption, bool, error) {
	redeemMu.Lock()
	defer redeemMu.Unlock()

	if prev, ok, err := s.GetRedemption(ctx, p.Code, org); err != nil {
		return Redemption{}, false, err
	} else if ok {
		return prev, true, nil // already redeemed — idempotent
	}
	count, err := s.CountRedemptions(ctx, p.Code)
	if err != nil {
		return Redemption{}, false, err
	}
	if count >= p.MaxRedemptions {
		return Redemption{}, false, errPromoExhausted
	}
	if used, err := s.instrumentUsed(ctx, p.Code, instrument); err != nil {
		return Redemption{}, false, err
	} else if used {
		return Redemption{}, false, errInstrumentUsed
	}
	if fin == nil {
		return Redemption{}, false, errFinanceUnavailable
	}
	// Credit BEFORE recording, so a failed credit leaves nothing marked redeemed
	// (the org can retry). The deposit is idempotent on Ref, so a retry that
	// races past a prior partial never double-credits.
	entryID, err := fin.Deposit(ctx, types.DepositInput{
		Org:      org,
		Subject:  org,
		Amount:   money.FromCents(discountCents),
		Currency: "usd",
		Notes:    "Launch promo " + p.Code + " (90% off month 1)",
		Tags:     "credit:promo-" + p.Code,
		Ref:      "promo-" + p.Code + ":" + org,
	})
	if err != nil {
		return Redemption{}, false, fmt.Errorf("promo credit: %w", err)
	}
	r := Redemption{
		Code: p.Code, Org: org, Plan: plan, Seats: seats, Instrument: instrument,
		CreditCents: discountCents, CreditEntryID: entryID, RedeemedAt: now,
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO marketing_promo_redemptions (code,org,plan,seats,instrument,credit_cents,credit_entry_id,redeemed_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		r.Code, r.Org, r.Plan, r.Seats, r.Instrument, r.CreditCents, r.CreditEntryID, r.RedeemedAt); err != nil {
		return Redemption{}, false, fmt.Errorf("record redemption: %w", err)
	}
	return r, false, nil
}

// ---- handlers ----

func listPromos(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := tenant(c); !ok {
		return zip.ErrForbidden("org scope required")
	}
	promos, err := s.State.store.ListPromos(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]map[string]any, 0, len(promos))
	for _, p := range promos {
		n, _ := s.State.store.CountRedemptions(c.Context(), p.Code)
		out = append(out, map[string]any{"promo": p, "redeemed": n, "remaining": max0(p.MaxRedemptions - n)})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// quotePromo is the pure eligibility + math endpoint (no side effects).
func quotePromo(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := tenant(c); !ok {
		return zip.ErrForbidden("org scope required")
	}
	p, err := s.State.store.GetPromo(c.Context(), strings.TrimSpace(c.Param("code")))
	if err != nil {
		return mapErr(err, "promo not found")
	}
	plan := strings.ToLower(strings.TrimSpace(c.Query("plan")))
	seats, _ := strconv.Atoi(strings.TrimSpace(c.Query("seats")))
	if seats <= 0 {
		seats = 1
	}
	charge, discount, ok, reason := p.quote(plan, seats)
	n, _ := s.State.store.CountRedemptions(c.Context(), p.Code)
	remaining := max0(p.MaxRedemptions - n)
	if !p.Active || remaining == 0 {
		ok = false
		if remaining == 0 {
			reason = "promo redemption cap reached"
		} else {
			reason = "promo is not active"
		}
	}
	return c.JSON(http.StatusOK, Quote{
		Code: p.Code, Plan: plan, Seats: seats, Eligible: ok, Reason: reason,
		ListCents: planListCents(plan), ChargeCents: charge, DiscountCents: discount, Remaining: remaining,
	})
}

// redeemPromo redeems the promo for the caller's org.
func redeemPromo(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	p, err := s.State.store.GetPromo(c.Context(), strings.TrimSpace(c.Param("code")))
	if err != nil {
		return mapErr(err, "promo not found")
	}
	if !p.Active {
		return zip.ErrConflict("promo is not active")
	}
	var body struct {
		Plan       string `json:"plan"`
		Seats      int    `json:"seats"`
		Instrument string `json:"instrument"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	seats := body.Seats
	if seats <= 0 {
		seats = 1
	}
	charge, discount, eligible, reason := p.quote(body.Plan, seats)
	if !eligible {
		return zip.ErrBadRequest(reason)
	}
	r, already, err := s.State.store.redeem(c.Context(), p, org, strings.ToLower(strings.TrimSpace(body.Plan)), seats, strings.TrimSpace(body.Instrument), discount, finance.Current(), time.Now().Unix())
	switch {
	case errors.Is(err, errPromoExhausted):
		return zip.ErrConflict("promo redemption cap reached")
	case errors.Is(err, errInstrumentUsed):
		return zip.ErrForbidden("payment instrument already redeemed this promo")
	case errors.Is(err, errFinanceUnavailable):
		return zip.Errorf(http.StatusServiceUnavailable, "billing ledger unavailable; try again")
	case err != nil:
		return zip.Errorf(http.StatusInternalServerError, "redeem: %v", err)
	}
	status := http.StatusCreated
	if already {
		status = http.StatusOK
	}
	return c.JSON(status, map[string]any{
		"redemption": r, "chargeCents": charge, "discountCents": discount, "alreadyRedeemed": already,
	})
}

// getRedemption returns the caller org's own redemption (org-scoped read).
func getRedemption(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	r, found, err := s.State.store.GetRedemption(c.Context(), strings.TrimSpace(c.Param("code")), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "redemption: %v", err)
	}
	if !found {
		return zip.ErrNotFound("no redemption for this org")
	}
	return c.JSON(http.StatusOK, r)
}

// max0 clamps a count to >= 0.
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
