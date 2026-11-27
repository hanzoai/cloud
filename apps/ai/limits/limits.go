// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package limits

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	commercepeer "github.com/hanzoai/cloud/client/commerce"
	flagsplane "github.com/hanzoai/cloud/client/flag"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/network"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
)

// nanosPerCent converts cents to the store's nano-dollars.
const nanosPerCent = 10_000_000

// The classes a model is sold in, and who may pay — the ai module's own words
// (github.com/hanzoai/ai/object), restated here because this package is linked by
// processes that do not link that module.
const (
	ClassPremium = "premium"
	ClassOurs    = "ours"
	ClassFree    = "free"

	PaysPlan    = "plan"
	PaysPrepaid = "prepaid"
	PaysCredits = "credits"
	PaysFree    = "free"

	CodeUsageCap      = "usage_cap_exceeded"
	CodePlanAllowance = "plan_allowance_used"
	CodeFreePlanCap   = "free_plan_cap"
	CodeModelCap      = "model_cap"
	// CodeInsufficient refuses a priced request nothing pays for from a payer with no
	// plan: 402, add funds. It is the one code for that situation, the wallet gate's
	// own, so a program reads the same answer whichever rule refused. A payer with no
	// plan owes funds, never a plan: prepaid pays for any model, so "a plan is
	// required" is never true of it.
	CodeInsufficient = "insufficient_balance"
	// CodePrepaid refuses a request only the payer's own money pays for — fast
	// mode — when the payer holds none: 402, add funds.
	CodePrepaid = "prepaid_required"
	// CodeEntitlement refuses a request whose model or mode needs a plan entitlement
	// (flags.Entitlements: Claude Fable 5.2, fast mode) the payer's plan does not
	// include by name, whatever money the payer holds: 402, upgrade. Its Hit names
	// the entitlement (Name) and a plan that includes it (Upgrade).
	CodeEntitlement = "entitlement_required"
)

// DefaultFallback is the Hanzo model a capped model hands a conversation to when its
// rule names none: enso-auto, Enso's router, which serves a payer nothing pays for on
// its tier's no-cost rungs at no charge.
const DefaultFallback = "enso-auto"

// FreePlan is the plan key a model's daily caps name for a payer with no plan.
const FreePlan = "free"

// plan is what the payer's subscription says about AI. A zero id includes none.
type plan struct {
	// id is the served rung, the row's own plan: it names the plan's daily caps,
	// its overage switch and the rung that raises it.
	id string
	// seats are the seat types the period was paid for, each with its seats: one
	// entry for a plan sold alone, and a team's every seat type. A flat rung holds
	// one seat. Every figure is each seat type's per seat times its seats, together.
	seats      []seated
	start, end time.Time
	// charged and listed are what the period was paid and what the plan lists for
	// its seats, in cents: a period sold below list (a promo) includes figures
	// pro-rated by charged ÷ listed.
	charged, listed int64
}

// seated is one seat type of a plan and the seats of it paid for.
type seated struct {
	id    string
	seats int64
}

// sum is a per-seat figure over the plan's seats: each seat type's f times its
// seats, together.
func (p plan) sum(f func(id string) int64) int64 {
	var n int64
	for _, s := range p.seats {
		n += f(s.id) * s.seats
	}
	return n
}

// bound is sum for a figure where 0 is unbounded: a seat type it does not bound
// leaves the plan unbounded.
func (p plan) bound(f func(id string) int64) int64 {
	for _, s := range p.seats {
		if f(s.id) <= 0 {
			return 0
		}
	}
	return p.sum(f)
}

// cap is a model rule's share of the plan, over all its seats: each seat type's
// cap per seat times its seats. A seat type the rule does not bound takes at most
// all of its own seats' figure (whole: the class's allowance and the day's
// requests per seat), never another seat type's, so a seat the policy leaves
// unnamed lifts no cap off the seats it names. A rule that bounds no seat type of
// the plan bounds nothing, and a seat type it does not bound whose own figure is
// unbounded leaves the plan unbounded.
func (p plan) cap(r Rule, whole func(id string) Cap) Cap {
	field := func(of func(Cap) int64) int64 {
		var n int64
		bounded := false
		for _, s := range p.seats {
			v := of(r.Caps[s.id])
			if v > 0 {
				bounded = true
			} else if v = of(whole(s.id)); v <= 0 {
				return 0
			}
			n += v * s.seats
		}
		if !bounded {
			return 0
		}
		return n
	}
	return Cap{
		PeriodCents:   field(func(c Cap) int64 { return c.PeriodCents }),
		DailyRequests: field(func(c Cap) int64 { return c.DailyRequests }),
	}
}

// counts reports whether the plan includes AI.
func (p plan) counts() bool { return p.id != "" }

// key is the plan a model's daily caps are read for: its id, or FreePlan.
func (p plan) key() string {
	if p.counts() {
		return p.id
	}
	return FreePlan
}

// paid reports whether a subscription's period in hand was paid in a way that
// includes AI: a card charged for all of it, or a payment SuperAdmins recorded as
// collected outside commerce, a comp among them (client.Subscription.Settled). An
// allowlist: the balance, credit, a mix, a trial or anything unknown is not.
func paid(settled string) bool {
	s := strings.ToLower(strings.TrimSpace(settled))
	if s == "card" || s == "wallet" {
		return true
	}
	processor, ok := strings.CutPrefix(s, "external:")
	return ok && processor != ""
}

// comp reports whether SuperAdmins granted the period free: it is charged nothing
// and includes what its plan lists.
func comp(settled string) bool {
	return strings.EqualFold(strings.TrimSpace(settled), "external:comp")
}

// Way is the plan that raises a rung's limits, by slug and by its name in the
// catalog; zero when none does.
type Way struct {
	Plan string
	Name string
}

// upgrade is the way on from a rung: the cheapest plan on sale in the rung's
// category, priced above it, that the usage policy names — and for no plan, the
// cheapest such plan on sale in any category. A plan on sale only by quote is never
// the way on. Zero when none is, or when the catalog cannot be read: the way on is
// advice, never a reason to refuse. The answer is held for planTTL.
func (l *Limiter) upgrade(ctx context.Context, org, id string) Way {
	return l.way(ctx, id, func(rows []client.PlanRow) Way { return l.wayOn(ctx, org, rows, id) })
}

// way is the way on find chooses among the plan rows, held under key for planTTL
// with the rows it was chosen among. Zero when the catalog cannot be read.
func (l *Limiter) way(ctx context.Context, key string, find func([]client.PlanRow) Way) Way {
	now := l.now()
	l.mu.Lock()
	if now.Sub(l.ways.at) >= planTTL {
		l.ways = cachedWays{}
	}
	if w, ok := l.ways.found[key]; ok {
		l.mu.Unlock()
		return w
	}
	rows, read := l.ways.rows, l.ways.found != nil
	l.mu.Unlock()
	if !read {
		var err error
		if rows, err = l.catalog(ctx); err != nil {
			slog.Warn("limits: plan rows unreadable, so no way on is named", "err", err)
			return Way{}
		}
	}
	w := find(rows)
	l.mu.Lock()
	if l.ways.found == nil {
		l.ways = cachedWays{rows: rows, at: now, found: map[string]Way{}}
	}
	l.ways.found[key] = w
	l.mu.Unlock()
	return w
}

// wayOn is upgrade over rows, unheld. A per-seat plan is neither a way on nor has
// one: moving a team between seat plans is a change of its seats, which checkout
// does not sell as an upgrade.
func (l *Limiter) wayOn(ctx context.Context, org string, rows []client.PlanRow, id string) Way {
	var from *client.PlanRow
	if id != "" {
		i := slices.IndexFunc(rows, func(r client.PlanRow) bool { return r.Slug == id })
		if i < 0 || rows[i].PerSeat {
			return Way{}
		}
		from = &rows[i]
	}
	var on []client.PlanRow
	for _, r := range rows {
		if r.Status != "active" || r.ContactSales || r.PerSeat || r.Price <= 0 || r.Slug == id {
			continue
		}
		if from != nil && (r.Category != from.Category || r.Price <= from.Price) {
			continue
		}
		on = append(on, r)
	}
	slices.SortFunc(on, func(a, b client.PlanRow) int {
		return cmp.Or(cmp.Compare(a.Price, b.Price), strings.Compare(a.Slug, b.Slug))
	})
	for _, r := range on {
		if l.named(ctx, org, r.Slug) {
			return Way{Plan: r.Slug, Name: r.Name}
		}
	}
	return Way{}
}

// named reports whether the usage policy names a plan: every one of its figures
// (flags.PlanFields) saved at /v1/admin/usage/policy. A plan the policy does not
// name includes no usage, and its payer is read as on no plan. Each figure is read
// as value reads it, so a plan once named stays named through a flag plane that
// cannot answer.
func (l *Limiter) named(ctx context.Context, org, id string) bool {
	for _, f := range flags.PlanFields {
		if _, ok := l.value(ctx, org, flags.PlanKey(f, id)); !ok {
			return false
		}
	}
	return true
}

// Named reports whether the usage policy names a plan right now: every one of its
// figures set in the flag store. It reads the store fresh, so a flag plane that
// cannot answer is an error; a deployment with no flag plane names no plan.
func Named(ctx context.Context, slug string) (bool, error) {
	id := strings.ToLower(strings.TrimSpace(slug))
	for _, f := range flags.PlanFields {
		v, err := planeFlag(ctx, principal.Reserved, &client.FlagValueIn{Key: flags.PlanKey(f, id), Kind: "string"})
		if errors.Is(err, plane.ErrNoPeer) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("usage policy read: %w", err)
		}
		if v == nil || !v.Set {
			return false, nil
		}
	}
	return true, nil
}

// value is one switch of the usage policy and whether it is known: what the flag
// plane answers when an operator set it, and otherwise the last value it answered
// for the switch, which the limits store keeps across restarts. A flag plane that
// is down, or one whose own store failed and so answers a switch as unset, reads
// as the last value it gave — never as unset, so a payer on a plan the policy
// names keeps its figures and is neither read as on no plan nor refused.
func (l *Limiter) value(ctx context.Context, org, key string) (string, bool) {
	v, err := l.flag(ctx, org, &client.FlagValueIn{Key: key, Kind: "string"})
	if err == nil && v != nil && v.Set {
		l.mu.Lock()
		held, ok := l.known[key]
		if !ok || held != v.Value {
			l.known[key] = v.Value
		}
		l.mu.Unlock()
		if !ok || held != v.Value {
			if err := l.store.know(ctx, key, v.Value); err != nil {
				slog.Warn("limits: usage policy value not kept", "key", key, "err", err)
			}
		}
		return v.Value, true
	}
	l.mu.Lock()
	held, ok := l.known[key]
	l.mu.Unlock()
	return held, ok
}

// Limiter composes the plan (commerce), the policy (flags), the payer's funds (the
// ledger) and what was used (its store). It owns only the store.
type Limiter struct {
	store *limitStore
	now   func() time.Time

	// rung, subs, catalog and funds cross the plane; tests replace them.
	rung    Rung
	subs    Subs
	catalog Catalog
	funds   Funds
	policy  func(ctx context.Context, org string) (map[string]Rule, error) // the model policy; l.rules by default
	credit  func(ctx context.Context, org, class string) bool              // whether granted credit pays for a class; credit by default
	flag    Flag                                                           // the usage policy's figures; planeFlag by default
	// holderOf names the account whose plan a person's call in an org spends when the
	// org's books hold none (cloud.Holder).
	holderOf func(ctx context.Context, person, org string) account.Account

	mu    sync.Mutex
	plans map[string]cachedPlan
	ways  cachedWays
	known map[string]string // each usage policy switch's last value the flag plane gave (value)
	rates map[string]int    // each organization's own requests a minute per person (rate.go)

	// minute is the per-minute limit's count: one window per person, the
	// deployment's (rate.go).
	minute *cloud.Window
}

// cachedWays is the plan rows read once per planTTL and each way on found in them.
type cachedWays struct {
	rows  []client.PlanRow
	at    time.Time
	found map[string]Way
}

type cachedPlan struct {
	p  plan
	at time.Time
}

// planTTL is how long a resolved plan is trusted: a new subscription takes effect
// within a minute.
const planTTL = time.Minute

type (
	// Subs reads a payer's subscriptions.
	Subs func(ctx context.Context, subject, org string) ([]client.Subscription, error)
	// Rung reads the catalog rung commerce serves the payer as ("max-20x") and the id
	// of the subscription row that serves it; both "" with no plan. The rung is
	// commerce's answer, retired slugs already resolved to their successors.
	Rung func(ctx context.Context, subject, org string) (rung, row string, err error)
	// Funds reads what the payer's wallet may spend, in nano-dollars: its own money —
	// the cash it paid in and the credit line left (finance.Funds.Own) — and the credit
	// the platform granted it.
	Funds func(ctx context.Context, subject, org string) (own, credit int64, err error)
	// Catalog reads the plan authority's rows, whatever their status: what the way on
	// from a rung is chosen among.
	Catalog func(ctx context.Context) ([]client.PlanRow, error)
	// Flag reads one switch of the usage policy as the payer's org asks it.
	Flag func(ctx context.Context, org string, in *client.FlagValueIn) (*client.FlagValue, error)
)

// Open is a limiter over the limits store in dir, reading plans through rung and
// subs, the plan rows through catalog, the wallet through funds and the usage
// policy's figures through flag. A nil catalog names no way on; a nil flag asks
// the flag plane.
func Open(dir string, rung Rung, subs Subs, catalog Catalog, funds Funds, flag Flag) (*Limiter, error) {
	store, err := openLimits(dir)
	if err != nil {
		return nil, err
	}
	if funds == nil {
		funds = func(context.Context, string, string) (int64, int64, error) { return 0, 0, errNoFunds }
	}
	if catalog == nil {
		catalog = func(context.Context) ([]client.PlanRow, error) { return nil, nil }
	}
	if flag == nil {
		flag = planeFlag
	}
	known, err := store.knowns(context.Background())
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	rates, err := store.rates(context.Background())
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	l := &Limiter{store: store, now: time.Now, rung: rung, subs: subs, catalog: catalog, funds: funds, plans: map[string]cachedPlan{},
		credit: credit, flag: flag, holderOf: cloud.Holder, known: known, rates: rates, minute: cloud.NewWindow("rpm", 0, 0)}
	l.policy = l.rules
	return l, nil
}

// planeFlag reads one switch from the flag plane as org asks it.
func planeFlag(ctx context.Context, org string, in *client.FlagValueIn) (*client.FlagValue, error) {
	return flagsplane.FlagsValue(cloud.For(ctx, org), in)
}

// planeCatalog reads the plan authority's rows from commerce over the plane, as the
// platform. No commerce in this deployment is no rows.
func planeCatalog(ctx context.Context) ([]client.PlanRow, error) {
	out, err := commercepeer.BillingPlanRows(cloud.For(ctx, principal.Reserved))
	if errors.Is(err, plane.ErrNoPeer) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("plane plan rows read: %w", err)
	}
	if out == nil {
		return nil, nil
	}
	return out.Rows, nil
}

// planeRung reads the payer's served rung and the row that serves it from commerce's
// tier op over the plane.
func planeRung(ctx context.Context, subject, org string) (string, string, error) {
	out, err := commercepeer.BillingTier(cloud.For(ctx, org), &client.TierIn{Subject: subject})
	if errors.Is(err, plane.ErrNoPeer) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("plane tier read: %w", err)
	}
	if out == nil {
		return "", "", nil
	}
	return strings.ToLower(strings.TrimSpace(out.Plan)), strings.TrimSpace(out.Subscription), nil
}

// holder is who a request's plan is read for, and that plan: the payer's own, or —
// when the payer holds none and the person making the call owns the payer's org —
// the person's. A key in an org someone owns, a project's, spends its owner's plan
// before the org's money; a member of an org someone else owns never does. The
// returned Ask names the holder; what is paid past the plan is still the payer's.
func (l *Limiter) holder(ctx context.Context, q Ask) (Ask, plan, error) {
	p, err := l.planOf(ctx, q.Subject, q.Org)
	if err != nil || p.counts() || l.holderOf == nil {
		return q, p, err
	}
	acct := l.holderOf(ctx, q.Actor, q.Org)
	if acct.Zero() {
		return q, p, nil
	}
	hp, err := l.planOf(ctx, acct.Subject(), acct.Org())
	if err != nil || !hp.counts() {
		return q, p, nil // a plan nobody can read lends nothing
	}
	h := q
	h.Subject, h.Org = acct.Subject(), acct.Org()
	return h, hp, nil
}

// planeSubs reads a payer's subscriptions from commerce over the plane.
func planeSubs(ctx context.Context, subject, org string) ([]client.Subscription, error) {
	out, err := commercepeer.BillingSubscriptions(cloud.For(ctx, org), &client.SubsIn{UserID: subject})
	if errors.Is(err, plane.ErrNoPeer) {
		return nil, nil // no commerce in this deployment: no plan to read
	}
	if err != nil {
		return nil, fmt.Errorf("plane subscriptions read: %w", err)
	}
	if out == nil {
		return nil, nil
	}
	return out.Rows, nil
}

// ladder names the rung whose figures a served rung includes, "" for none: a
// renamed slug reads as the rung that replaced it (flags.Renamed: pro-2x is pro-3x,
// max-10x is max-15x) once the usage policy names that rung, and as itself until
// then, so a deploy that lands before scripts/ladder-writes.sh has saved the renamed
// rung's usage leaves its holders on the figures they had; a rung the usage policy
// does not name (named) includes none. A rung bought by the year includes what the
// same rung includes by the month, renewed every month (period).
func (l *Limiter) ladder(ctx context.Context, org, served string) string {
	id := strings.ToLower(strings.TrimSpace(served))
	if to, ok := flags.Renamed[id]; ok && l.named(ctx, org, to) {
		id = to
	}
	if id == "" || !l.named(ctx, org, id) {
		return ""
	}
	return id
}

// perSeat are the rungs billed per seat. Every other rung is flat.
var perSeat = map[string]bool{"team": true, "team-premium": true}

// planOf resolves the payer's plan from the one row that serves its rung, as
// commerce names it: that row's own payment, period, seats and charge, never
// another row's. The row must be active and its period paid in a way that counts.
// Only a per-seat rung scales by seats, and only by the seats its period was
// charged for; a period sold below list includes pro-rated figures. A comp is
// recorded at $0 and counts at list price, its full figures. A team holds
// its other seat types as the row's lines, each paid for its own seats: the row's
// Seats count them all, so the row's own plan holds what they leave.
func (l *Limiter) planOf(ctx context.Context, subject, org string) (plan, error) {
	key := org + "\x00" + subject
	now := l.now()
	l.mu.Lock()
	if c, ok := l.plans[key]; ok && now.Sub(c.at) < planTTL && (c.p.end.IsZero() || now.Before(c.p.end)) {
		l.mu.Unlock()
		return c.p, nil
	}
	l.mu.Unlock()

	served, rowID, err := l.rung(ctx, subject, org)
	if err != nil {
		return plan{}, err
	}
	var p plan
	if served != "" && rowID != "" {
		rows, err := l.subs(ctx, subject, org)
		if err != nil {
			return plan{}, err
		}
		for _, s := range rows {
			if s.ID != rowID {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(s.Status), "active") || !paid(s.Settled) || (s.ChargedCents <= 0 && !comp(s.Settled)) {
				break
			}
			id := l.ladder(ctx, org, served)
			if id == "" {
				break
			}
			own := s.Seats
			for _, line := range s.Lines {
				own -= line.Seats
			}
			own = max(1, own)
			p = plan{id: id, charged: s.ChargedCents, listed: s.Plan.Price * int64(own)}
			if perSeat[id] {
				p.seats = []seated{{id, int64(own)}}
			} else {
				p.seats = []seated{{id, 1}}
			}
			for _, line := range s.Lines {
				if line.Seats <= 0 {
					continue
				}
				if lid := l.ladder(ctx, org, line.Plan); lid != "" {
					p.seats = append(p.seats, seated{lid, int64(line.Seats)})
					p.listed += line.Price * int64(line.Seats)
				}
			}
			if comp(s.Settled) {
				p.charged = p.listed
			}
			p.start, p.end = period(s, now)
			break
		}
	}
	l.mu.Lock()
	l.plans[key] = cachedPlan{p: p, at: now}
	l.mu.Unlock()
	return p, nil
}

// period is the window a plan's figures renew over: one month of the
// subscription's current billing period. A monthly period is itself; a yearly
// term renews every calendar month from its start, its last month running to the
// term's end, and never past it. A period that has not begun is held to the
// month before its start, so spend made before it is counted somewhere. A row
// that states no period is read as the calendar month it is in, UTC.
func period(s client.Subscription, now time.Time) (time.Time, time.Time) {
	start, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(s.CurrentPeriodStart))
	end, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(s.CurrentPeriodEnd))
	if start.IsZero() || end.IsZero() || !end.After(start) {
		u := now.UTC()
		start = time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(0, 1, 0)
	}
	start, end, now = start.UTC(), end.UTC(), now.UTC()
	if now.Before(start) {
		return monthsAfter(start, -1), start
	}
	if !strings.EqualFold(strings.TrimSpace(s.Plan.Interval), "year") {
		return start, end
	}
	return renewal(start, end, now)
}

// renewal is the month of the term [start, end) that holds now: the k-th calendar
// month from start. The last whole month runs to end, so a term one day longer
// than its months (a year begun on February 29) has no sliver of a thirteenth;
// once the term is over, its last month holds and nothing renews.
func renewal(start, end, now time.Time) (time.Time, time.Time) {
	months := 0
	for !monthsAfter(start, months+1).After(end) {
		months++
	}
	if months == 0 {
		return start, end
	}
	if !now.Before(end) {
		now = end.Add(-time.Nanosecond)
	}
	k := (now.Year()-start.Year())*12 + int(now.Month()) - int(start.Month())
	if monthsAfter(start, k).After(now) {
		k--
	}
	k = max(0, min(k, months-1))
	to := monthsAfter(start, k+1)
	if k == months-1 {
		to = end
	}
	return monthsAfter(start, k), to
}

// monthsAfter is t moved k calendar months on, its day held to the last of the
// month it lands in: a term begun on the 31st renews on the 28th, 29th or 30th of
// a shorter month, not in the month after it.
func monthsAfter(t time.Time, k int) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(k), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	day := min(t.Day(), first.AddDate(0, 1, -1).Day())
	return time.Date(first.Year(), first.Month(), day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// allowance is one class's included usage: per billing period and, when set, per
// shorter window, in nano-dollars at list price.
type allowance struct {
	period int64
	window int64
	span   time.Duration // the window's length; 0 = no window
	// unbounded is a class saved as flags.Unbounded: its period is unboundedNanos,
	// which no use reaches, and it has no window.
	unbounded bool
}

// unboundedNanos is what an unbounded class includes, in the nano-dollars every
// allowance counts: more than any period's use could reach, and small enough
// that the sums and the four-fifths test over it never overflow.
const unboundedNanos = int64(1) << 60

// figure is one integer of the usage policy as value knows it, def where it knows
// none.
func (l *Limiter) figure(ctx context.Context, org, key string, def int) int {
	if v, ok := l.value(ctx, org, key); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

// on is one switch of the usage policy, as figure.
func (l *Limiter) on(ctx context.Context, org, key string, def bool) bool {
	if v, ok := l.value(ctx, org, key); ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

// termsOf reads a plan's figures from the flag store, scaled by its seats: each
// seat type's own saved figures times its seats, together. A plan is read here
// only once the policy names it (named), so every figure is a saved one. A short
// window's length is the served rung's.
func (l *Limiter) termsOf(ctx context.Context, org string, p plan) terms {
	read := func(field, id string) int64 {
		return max(0, int64(l.figure(ctx, org, flags.PlanKey(field, id), 0)))
	}
	// per is a field read per seat type.
	per := func(field string) func(id string) int64 {
		return func(id string) int64 { return read(field, id) }
	}
	// A class's short window is each seat type's own, so which seat type a row
	// names first moves nothing: its length is the shortest any seat type sets,
	// and its figure each seat type's window per seat where it sets one and its
	// whole period's where it does not.
	class := func(name string) allowance {
		// One seat type saved unbounded makes the pool unbounded: its seats include
		// every use, so no sum of the others bounds it. Not pro-rated: a share of
		// unbounded is unbounded.
		for _, s := range p.seats {
			if l.figure(ctx, org, flags.PlanKey(name, s.id), 0) == flags.Unbounded {
				return allowance{period: unboundedNanos, unbounded: true}
			}
		}
		a := allowance{period: p.share(p.sum(per(name)) * nanosPerCent)}
		var span int64
		for _, s := range p.seats {
			if h := read(name+"_window_hours", s.id); h > 0 && (span == 0 || h < span) {
				span = h
			}
		}
		if span > 0 {
			a.window = p.share(p.sum(func(id string) int64 {
				if read(name+"_window_hours", id) > 0 {
					return read(name+"_window", id)
				}
				return read(name, id)
			}) * nanosPerCent)
			a.span = time.Duration(span) * time.Hour
		}
		return a
	}
	day := per("day")
	// whole is one seat of a seat type, all of it, per class: what a model cap the
	// policy does not set for that seat type stands at (plan.cap).
	whole := map[string]func(id string) Cap{}
	for _, name := range []string{ClassPremium, ClassOurs} {
		cents := per(name)
		whole[name] = func(id string) Cap { return Cap{PeriodCents: cents(id), DailyRequests: day(id)} }
	}
	// Past the included usage, prepaid pays only where every seat type allows it.
	overage := true
	for _, s := range p.seats {
		overage = overage && l.on(ctx, org, flags.PlanKey("overage", s.id), true)
	}
	return terms{
		session: p.window(p.bound(per("session"))),
		day:     p.window(p.bound(day)),
		allow: map[string]allowance{
			ClassPremium: class(ClassPremium),
			ClassOurs:    class(ClassOurs),
		},
		whole:   whole,
		overage: overage,
		block:   flags.Session(l.figure(ctx, org, flags.PlanSessionHours, flags.SeedSessionHours)),
		start:   p.start,
		end:     p.end,
	}
}

// share pro-rates a figure by what the period was charged against what it lists:
// a full charge keeps the figure, a promo sale gets its fraction.
func (p plan) share(v int64) int64 {
	if p.listed <= 0 || p.charged >= p.listed {
		return v
	}
	return v * p.charged / p.listed
}

// window pro-rates a request window, never to zero, which would read unbounded.
func (p plan) window(v int64) int64 {
	if v <= 0 {
		return v
	}
	return max(1, p.share(v))
}

// Rule is one model's policy, keyed by the model id or a `*` glob of ids: its class,
// whether granted credit pays for it, its daily cap per plan: the requests served free
// where nothing the payer holds pays (FreePlan for no plan; 0 = it needs a paid plan,
// prepaid or granted credit; a plan not named has no cap rule), the most of each plan's class allowance its models may
// take (Caps, shared by every model a glob matches), and the Hanzo model that
// answers a conversation when that share is used (Fallback, DefaultFallback when
// empty).
type Rule struct {
	Class     string         `json:"class"`
	GrantsPay bool           `json:"grantsPay"`
	DailyCaps map[string]int `json:"dailyCaps,omitempty"`
	Caps      map[string]Cap `json:"caps,omitempty"`
	Fallback  string         `json:"fallback,omitempty"`
}

// Cap is a model's share of one plan's class allowance, per seat at list price: at
// most PeriodCents a billing period and DailyRequests a UTC day of what the plan
// pays for; 0 leaves either unbounded.
type Cap struct {
	// PeriodCents is the most of the plan's class allowance the rule's models take
	// per billing period, per seat, in US cents at list price; 0 = unbounded.
	PeriodCents int64 `json:"periodCents,omitempty"`
	// DailyRequests is the most requests the plan pays for the rule's models per
	// UTC day, per seat; 0 = unbounded.
	DailyRequests int64 `json:"dailyRequests,omitempty"`
}

// bounded reports whether the cap bounds anything.
func (c Cap) bounded() bool { return c.PeriodCents > 0 || c.DailyRequests > 0 }

// fallback is the model a conversation is handed to past the rule's cap.
func (r Rule) fallback() string {
	if f := strings.TrimSpace(r.Fallback); f != "" {
		return f
	}
	return DefaultFallback
}

// Match finds the rule for a model: the rule keyed by its exact id, else the most
// specific glob that matches it (the longest key). It answers the key the rule is
// kept under, which is what a capped share is counted by.
func Match(rs map[string]Rule, model string) (string, Rule, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if r, ok := rs[m]; ok {
		return m, r, true
	}
	best := ""
	for k := range rs {
		if strings.Contains(k, "*") && glob(k, m) && (len(k) > len(best) || (len(k) == len(best) && k < best)) {
			best = k
		}
	}
	if best == "" {
		return "", Rule{}, false
	}
	return best, rs[best], true
}

// glob matches s against a pattern whose only metacharacter is `*`, any run of
// characters, `/` included.
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// ParseRules reads the model policy record. A record that does not parse is an
// error, never an empty policy: an empty one would lift every model's rule.
func ParseRules(raw string) (map[string]Rule, error) {
	out := map[string]Rule{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("model policy: %w", err)
	}
	return out, nil
}

// rules is the model policy as the flag store holds it, read as value reads every
// switch of the usage policy: the saved record, the last one the flag plane gave
// while it cannot answer, and the policy with nothing saved (flags.SeedModelPolicy)
// only where none was ever read.
func (l *Limiter) rules(ctx context.Context, org string) (map[string]Rule, error) {
	text := flags.SeedModelPolicy
	if v, ok := l.value(ctx, org, flags.ModelPolicy); ok && strings.TrimSpace(v) != "" {
		text = v
	}
	return ParseRules(text)
}

// credit reports whether granted credit pays for a model of class that has no rule of
// its own, as the flag store holds it now. A class not seeded (free) takes none.
func credit(ctx context.Context, org, class string) bool {
	seed, ok := flags.SeedCredit[class]
	if !ok {
		return false
	}
	return flagsplane.Bool(cloud.For(ctx, org), flags.CreditKey(class), seed)
}

// Ask names one request the policy decides: who pays, in which org, the model, its
// class as the catalog reads it, its Hanzo family, whether it has a list price, the
// apps the caller's validated token was minted for, and whether it may reach a
// family's paid upstream.
type Ask struct {
	Subject string // the billing subject that pays
	Org     string // the org whose books it is
	Actor   string // the person making the call, "<home org>/<name>"; "" for none
	Model   string // the model the request names
	Family  string // "enso" or "zen" for a Hanzo family's SKU, else ""
	Class   string // premium, ours or free, as the catalog reads the model
	Priced  bool   // the request has a list price
	Apps    []string
	Spend   bool // a chat request, which may reach its family's paid rungs
	// Session says the request continues one already counted (a live transcript's
	// push or close): decide who pays, count nothing.
	Session bool
	// Past says the plan cannot pay for this request though it was admitted — the gate
	// had no room for it on the plan, or its grant was one the paid lane does not seat —
	// so decide what pays past the plan on the paid lane: the payer's own money where
	// the plan allows overage, else the plan's refusal. It counts nothing.
	Past bool
	// Fast says a chat request asked for fast mode: the model API races it across
	// providers, or a vendor serves it on its own faster lane (speed: "fast"), so one
	// answer costs several times the standard. It needs a plan that includes the fast
	// entitlement, is paid from the payer's own money alone, whatever the plan
	// includes, and counts nothing of the plan.
	Fast bool
}

// Grant is the policy's answer for an admitted request: who pays (Pays), in which
// class, where that class stands (State: ok, near or limited), whether only the
// payer's own money may pay — cash and the line, never granted credit (Cash) — and,
// for a covered request, what a Hanzo family may spend on paid
// upstream (Spend, nano-dollars). Settle a covered grant once per answer.
type Grant struct {
	Plan  string
	Pays  string
	Class string
	State string
	Cash  bool
	Spend int64

	subject, org, class string
	terms               terms  // the plan's figures at admission: settle records against them
	share               string // the rule key a capped model's use is also counted under

	// What admission counted, so a request that was not served gives it back.
	counted bool
	at      int64
	day     string
	takes   []string
}

// Counted reports whether admitting the request counted anything it should give
// back if it is not served.
func (g *Grant) Counted() bool { return g != nil && (g.counted || len(g.takes) > 0) }

// Covered reports whether the plan or a free cap pays, so no wallet is asked.
func (g *Grant) Covered() bool { return g != nil && (g.Pays == PaysPlan || g.Pays == PaysFree) }

// Hit is why the policy refuses a request: its code, the window it names (session or
// day, for a spent request window), the class, the sentence a caller reads — which
// names no figure — when it lifts, and the plan that raises it.
type Hit struct {
	Code     string
	Name     string
	Class    string
	Message  string
	ResetsAt time.Time
	Upgrade  string
	// Model is the capped model, or the glob its share is kept under; Fallback is
	// the Hanzo model that answers a conversation in its place (model_cap only).
	Model    string
	Fallback string
}

// classOf is the class the policy puts a request in: free whenever it has no list
// price, else the model's rule, else the catalog's reading.
func classOf(q Ask, r Rule) string {
	switch {
	case !q.Priced:
		return ClassFree
	case r.Class == ClassPremium || r.Class == ClassOurs:
		return r.Class
	case q.Class == ClassPremium || q.Class == ClassOurs:
		return q.Class
	}
	return ClassPremium
}

// state is where a class stands at used of limit: limited when it is used up, near
// past four fifths, ok otherwise.
func state(used, limit int64) string {
	switch {
	case limit <= 0 || used >= limit:
		return "limited"
	case used*5 >= limit*4:
		return "near"
	}
	return "ok"
}

// speechModel reports whether model is one of Hanzo's in-house speech or
// transcription models (zen-voice*, zen-scribe*, koe*, oto*, zen3-tts), served
// on the Free plan up to FreeSpeechCap requests a UTC day when no explicit model
// policy rule overrides it.
const FreeSpeechCap = 500

func speechModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "hanzo/")
	return strings.HasPrefix(m, "zen-voice") || strings.HasPrefix(m, "zen-scribe") ||
		m == "koe" || strings.HasPrefix(m, "koe-") || m == "oto" || strings.HasPrefix(m, "oto-") || m == "zen3-tts"
}

// Admit decides who pays for one request before it is served (see the package
// doc's order). A grant admits it; a hit refuses it; neither leaves it to the free
// allowance and the wallet exactly as with no plans at all.
func (l *Limiter) Admit(ctx context.Context, q Ask) (*Grant, *Hit, error) {
	rs, err := l.policy(ctx, q.Org)
	if err != nil {
		return nil, nil, err
	}
	key, rule, explicit := Match(rs, q.Model)
	class := classOf(q, rule)
	if !explicit {
		rule = Rule{GrantsPay: class != ClassFree && l.credit(ctx, q.Org, class)}
		if speechModel(q.Model) {
			rule.DailyCaps = map[string]int{FreePlan: FreeSpeechCap}
		}
	}
	// From here q names the plan's holder and pool the payer, who pays past the plan.
	pool := q
	q, p, err := l.holder(ctx, q)
	if err != nil && class != ClassFree {
		return nil, nil, err
	}
	now := l.now()

	// A model or mode a plan must include by name — Claude Fable 5.2, fast mode — is
	// refused before anything pays for it when the plan does not: not its included
	// usage, not prepaid cash, not credit, and never for a payer with no plan.
	if hit := l.entitle(ctx, q, p, class); hit != nil {
		return nil, hit, nil
	}

	// Fast mode is paid from prepaid — the payer's cash, then the org's credit line —
	// and never from the plan's included usage, however much it includes (an
	// Unlimited plan too), granted credit or a free cap. With none of its own money
	// the payer is told to add funds.
	if q.Fast && q.Priced {
		own, _, err := l.funds(ctx, pool.Subject, pool.Org)
		if err != nil {
			return nil, nil, err
		}
		if own <= 0 {
			return nil, &Hit{Code: CodePrepaid, Class: class,
				Message: "Fast mode is paid from funds you add, never from a plan's included usage or granted credit. Add funds, or ask without fast mode:"}, nil
		}
		return &Grant{Plan: p.id, Pays: PaysPrepaid, Class: class, State: "ok", Cash: true}, nil, nil
	}

	var t terms
	if p.counts() {
		t = l.termsOf(ctx, q.Org, p)
	}

	// The free lane. A plan's request windows bound it; without a plan, the free
	// allowance does.
	if class == ClassFree {
		if !p.counts() {
			return nil, nil, nil
		}
		a, err := l.store.admit(ctx, q.Org, q.Subject, ClassOurs, now, t, q.Spend && q.Family != "", !q.Session, false)
		if err != nil {
			return nil, nil, err
		}
		if a.full != "" {
			return nil, &Hit{Code: CodeUsageCap, Name: a.full, ResetsAt: a.resets, Upgrade: l.upgrade(ctx, q.Org, p.id).Plan}, nil
		}
		return &Grant{Plan: p.id, Pays: PaysPlan, Class: ClassFree, State: "ok", Spend: a.spend,
			subject: q.Subject, org: q.Org, class: ClassOurs, terms: t, counted: a.counted, at: a.at}, nil, nil
	}

	// 1. The plan's included usage of the class, while its request windows hold and
	// the model has share of it left. A spent window (window) refuses, past what pays
	// beyond the plan, until that window reopens.
	var capped, window *Hit
	if p.counts() && t.allow[class].period > 0 && !q.Past {
		c := p.cap(rule, t.seat(class))
		if full, resets, err := l.overShare(ctx, q, key, c, p, t, now); err != nil {
			return nil, nil, err
		} else if full {
			capped = &Hit{Code: CodeModelCap, Class: class, Model: capName(key, q.Model), Fallback: rule.fallback(),
				ResetsAt: resets, Upgrade: l.upgrade(ctx, q.Org, p.id).Plan,
				Message: "This model has used its share of your plan for now. Try " + display(rule.fallback()) + ", buy usage credits, or upgrade:"}
		} else {
			a, err := l.store.admit(ctx, q.Org, q.Subject, class, now, t, true, !q.Session, true)
			if err != nil {
				return nil, nil, err
			}
			if a.full != "" {
				window = &Hit{Code: CodeUsageCap, Name: a.full, Class: class, ResetsAt: a.resets, Upgrade: l.upgrade(ctx, q.Org, p.id).Plan}
			}
			if a.full == "" && a.spend > 0 {
				g := &Grant{Plan: p.id, Pays: PaysPlan, Class: class, State: state(a.used, t.allow[class].period), Spend: a.spend,
					subject: q.Subject, org: q.Org, class: class, terms: t, counted: a.counted, at: a.at, day: today(now)}
				if c.bounded() {
					g.share = key
					if c.DailyRequests > 0 && !q.Session {
						if _, _, err := l.store.take(ctx, q.Org, q.Subject, shareKey(key), now, c.DailyRequests); err != nil {
							return nil, nil, err
						}
						g.takes = append(g.takes, shareKey(key))
					}
				}
				return g, nil, nil
			}
		}
	}

	// 2. Money, once the plan's usage is spent and the plan allows overage, and always
	// for a payer with no plan: prepaid — the payer's own cash, then the org's credit
	// line — then granted credit where the model takes it.
	own, credit, err := l.funds(ctx, pool.Subject, pool.Org)
	if err != nil {
		return nil, nil, err
	}
	used := l.used(ctx, q.Org, q.Subject, class, t)
	overage := !p.counts() || t.overage
	if overage && own > 0 {
		return &Grant{Plan: p.id, Pays: PaysPrepaid, Class: class, State: overageState(p, used, t, class), Cash: !rule.GrantsPay}, nil, nil
	}
	// Past the plan for a request the paid lane serves only on money the payer bought:
	// granted credit and a free cap seat nothing there, so with no cash the plan's own
	// refusal stands, naming when its usage reopens.
	if q.Past {
		return nil, l.unpaid(ctx, q.Org, p, q.Model, class, t), nil
	}
	// A spent window stands before granted credit, which the paid lane would not seat.
	if window != nil {
		return nil, window, nil
	}
	if overage && rule.GrantsPay && credit > 0 {
		return &Grant{Plan: p.id, Pays: PaysCredits, Class: class, State: overageState(p, used, t, class)}, nil, nil
	}
	if capped != nil {
		return nil, capped, nil
	}

	// 3. The model's daily cap for this plan: the requests it serves free to a payer
	// nothing above paid for.
	if cap, ok := rule.DailyCaps[p.key()]; ok {
		if cap <= 0 {
			return nil, l.unpaid(ctx, q.Org, p, q.Model, class, t), nil
		}
		var n int64
		var full bool
		if q.Session {
			n, err = l.store.peek(ctx, q.Org, q.Subject, strings.ToLower(q.Model), now)
		} else {
			n, full, err = l.store.take(ctx, q.Org, q.Subject, strings.ToLower(q.Model), now, int64(cap))
		}
		if err != nil {
			return nil, nil, err
		}
		if full {
			return nil, &Hit{Code: CodeFreePlanCap, Class: class, ResetsAt: midnight(now), Upgrade: l.upgrade(ctx, q.Org, p.id).Plan,
				Message: planName(p) + ": today's " + display(q.Model) + " requests are used. Upgrade for more:"}, nil
		}
		g := &Grant{Plan: p.id, Pays: PaysFree, Class: class, State: state(n, int64(cap)), subject: q.Subject, org: q.Org, day: today(now)}
		if !q.Session {
			g.takes = []string{strings.ToLower(q.Model)}
		}
		if g.State == "limited" {
			g.State = "near" // the last request inside the cap is still inside it
		}
		return g, nil, nil
	}

	// Nothing pays. A model granted credit does not pay for is refused whatever the
	// payer holds, so credit is never what quietly serves it.
	if p.counts() || !rule.GrantsPay {
		return nil, l.unpaid(ctx, q.Org, p, q.Model, class, t), nil
	}
	return nil, nil, nil
}

// overShare reports whether the model has used its share of the plan's class
// allowance — its period's share, or its UTC day's requests — and when that lifts.
// A cap that bounds nothing never refuses.
func (l *Limiter) overShare(ctx context.Context, q Ask, key string, c Cap, p plan, t terms, now time.Time) (bool, time.Time, error) {
	if key == "" || !c.bounded() {
		return false, time.Time{}, nil
	}
	if c.PeriodCents > 0 {
		used, err := l.store.share(ctx, q.Org, q.Subject, key, t.start.Unix())
		if err != nil {
			return false, time.Time{}, err
		}
		if used >= p.share(c.PeriodCents*nanosPerCent) {
			return true, t.end, nil
		}
	}
	if c.DailyRequests > 0 {
		n, err := l.store.peek(ctx, q.Org, q.Subject, shareKey(key), now)
		if err != nil {
			return false, time.Time{}, err
		}
		if n >= c.DailyRequests {
			return true, midnight(now), nil
		}
	}
	return false, time.Time{}, nil
}

// today is the UTC day a daily count is kept under.
func today(now time.Time) string { return now.UTC().Format("2006-01-02") }

// Release gives back what an admitted request was counted against when it was not
// served: its place in the request windows and each daily count it took. A grant
// that counted nothing is a no-op.
func (l *Limiter) Release(ctx context.Context, g *Grant) error {
	if !g.Counted() {
		return nil
	}
	return l.store.release(ctx, g.org, g.subject, g.counted, g.at, g.day, g.takes)
}

// shareKey is the cap-table key a rule's daily requests are counted under, apart
// from any model's own daily cap.
func shareKey(key string) string { return "share:" + key }

// capName is how a capped share is named to a client: the model itself when its rule
// is its own, else the glob it is counted under.
func capName(key, model string) string {
	if strings.Contains(key, "*") {
		return key
	}
	return strings.ToLower(strings.TrimSpace(model))
}

// unpaid is the refusal for a request nothing will pay for: a plan whose included
// usage of the class is used, or, for a payer with no plan, funds it has not added
// (insufficient_balance, naming the model).
func (l *Limiter) unpaid(ctx context.Context, org string, p plan, model, class string, t terms) *Hit {
	if p.counts() {
		return &Hit{Code: CodePlanAllowance, Class: class, ResetsAt: t.end, Upgrade: l.upgrade(ctx, org, p.id).Plan,
			Message: "Your plan's included usage of " + classNoun(class) + " is used for now. Add funds or upgrade:"}
	}
	return &Hit{Code: CodeInsufficient, Class: class, Model: model, Upgrade: l.upgrade(ctx, org, "").Plan,
		Message: display(model) + " is paid from funds you add, or by a paid plan; granted credit does not pay for it:"}
}

// overageState is where a class stands when something past the plan pays for it.
func overageState(p plan, used int64, t terms, class string) string {
	if !p.counts() || t.allow[class].period <= 0 {
		return "ok"
	}
	if s := state(used, t.allow[class].period); s != "limited" {
		return s
	}
	return "near"
}

// used is what a class has used of its period, 0 with no plan or on a read error.
func (l *Limiter) used(ctx context.Context, org, subject, class string, t terms) int64 {
	if t.start.IsZero() {
		return 0
	}
	n, err := l.store.spent(ctx, org, subject, class, t.start.Unix())
	if err != nil {
		return 0
	}
	return n
}

// classNoun is how a class is named to a person.
func classNoun(class string) string {
	if class == ClassOurs {
		return "Hanzo models"
	}
	return "premium models"
}

// planName is how a plan is named to a person in a refusal.
func planName(p plan) string {
	if !p.counts() {
		return "Free plan"
	}
	return "Your plan"
}

// display is a model's name in a sentence: Kai and Jev by name, anything else by id.
func display(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "kai" || strings.HasPrefix(m, "kai-"):
		return "Kai"
	case strings.Contains(m, "jev"):
		return "Jev"
	case m == "enso" || strings.HasPrefix(m, "enso-") || strings.HasPrefix(m, "hanzo/enso"):
		return "Enso"
	}
	return model
}

// Reserve reads what paid upstream a covered Hanzo-family request may use when its
// gate counts it elsewhere (the co-resident zen counts in its gate and asks here in
// its spend). Nil when no plan covers it; a grant that may spend nothing when what
// the plan includes of Hanzo's models is used.
func (l *Limiter) Reserve(ctx context.Context, q Ask) (*Grant, error) {
	q, p, err := l.holder(ctx, q)
	if err != nil || !p.counts() {
		return nil, err
	}
	t := l.termsOf(ctx, q.Org, p)
	a, err := l.store.admit(ctx, q.Org, q.Subject, ClassOurs, l.now(), t, true, false, false)
	if err != nil {
		return nil, err
	}
	return &Grant{Plan: p.id, Pays: PaysPlan, Class: ClassOurs, State: "ok", Spend: a.spend,
		subject: q.Subject, org: q.Org, class: ClassOurs, terms: t}, nil
}

// Settle records what one covered answer used of its plan, in nano-dollars, never
// past what its class has left. A grant the plan does not pay, or no use, is a no-op.
func (l *Limiter) Settle(ctx context.Context, g *Grant, nanos int64) error {
	if g == nil || g.Pays != PaysPlan || g.class == "" || nanos <= 0 {
		return nil
	}
	recorded, err := l.store.settle(ctx, g.org, g.subject, g.class, g.share, nanos, l.now(), g.terms)
	if err == nil && recorded < nanos {
		slog.Warn("ai plan: use past what the class includes was not recorded",
			"org", g.org, "subject", g.subject, "class", g.class, "nanos", nanos, "recorded_nanos", recorded)
	}
	return err
}

// Window is one window as a payer reads it: what is used of what it allows, and when
// it starts again — zero for a session that is not running, which starts at the next
// request. Requests for the session and the day; nano-dollars for a class.
type Window struct {
	Used, Limit int64
	ResetsAt    time.Time
}

// Percent is the share of the window used, 0 to 100, rounded up to the next five so
// any use shows and no figure can be read back out of it; an unbounded window reads 0.
func (w Window) Percent() int {
	if w.Limit <= 0 || w.Used <= 0 {
		return 0
	}
	p := min(100, (w.Used*100+w.Limit-1)/w.Limit)
	return int(min(100, (p+4)/5*5))
}

// Class is one class as a payer reads it: its period and, when the plan sets one, its
// short window, and who pays for its next request.
type Class struct {
	Period Window
	Window *Window
	Paying string // plan, prepaid, credits or "" when nothing does
	// Unbounded says the plan includes all of the class's use: Period counts what
	// was used and states no limit.
	Unbounded bool
}

// Standing is a payer's plan, its request windows, and each class it includes. A zero
// Plan covers nothing.
type Standing struct {
	Plan         string
	Start, End   time.Time
	Session, Day Window
	Classes      map[string]Class
	// Upgrade is the plan that raises these limits, zero when none does.
	Upgrade Way
	// Funded says its wallet may spend something (cash, the line left, or granted
	// credit).
	Funded bool
	// Paused are the models whose share of the plan is used, and when each lifts.
	Paused []Paused
	// Pays is who would pay for the next priced request of each class (premium, ours):
	// plan, prepaid (funds the payer added), credits (granted credit, where it pays for
	// the class) or "" when nothing does, so the request is refused until funds are
	// added. A class is absent when the wallet could not be read: nothing is known, and
	// never "none". It is the policy's own answer, so a client shows Top up without
	// knowing which credit pays for which class.
	Pays map[string]string
	// Entitlements is every plan entitlement (flags.Entitlements) as the payer's plan
	// stands on it: the reading Admit refuses by, so a client can hide what a request
	// would be refused for.
	Entitlements map[string]Entitlement
}

// Paused is one capped model (or glob) as a payer reads it: no figure.
type Paused struct {
	Model    string
	Fallback string
	ResetsAt time.Time
}

// Read is the payer's standing, for the usage bars.
func (l *Limiter) Read(ctx context.Context, subject, org string) (Standing, error) {
	p, err := l.planOf(ctx, subject, org)
	if err != nil {
		return Standing{}, err
	}
	out := Standing{Plan: p.id, Upgrade: l.upgrade(ctx, org, p.id), Entitlements: l.entitlements(ctx, org, p)}
	if !p.counts() {
		// No plan: prepaid pays for any class, granted credit for the classes that take
		// it. A wallet that cannot be read leaves Pays unsaid; the plan's answer never
		// waits on it.
		if own, credit, err := l.funds(ctx, subject, org); err == nil {
			out.Pays = map[string]string{}
			for _, class := range flags.PlanClasses {
				switch {
				case own > 0:
					out.Pays[class] = PaysPrepaid
				case credit > 0 && l.credit(ctx, org, class):
					out.Pays[class] = PaysCredits
				default:
					out.Pays[class] = ""
				}
			}
		}
		return out, nil
	}
	now := l.now()
	t := l.termsOf(ctx, org, p)
	st, err := l.store.read(ctx, org, subject, now, t)
	if err != nil {
		return Standing{}, err
	}
	own, credit, err := l.funds(ctx, subject, org)
	if err != nil {
		return Standing{}, err
	}
	out.Funded = own > 0 || credit > 0
	// past reports whether something pays for a class past the plan: where the plan
	// allows overage, the payer's own money always, granted credit where it pays for
	// the class.
	past := func(class string) bool {
		return t.overage && (own > 0 || credit > 0 && l.credit(ctx, org, class))
	}
	out.Start, out.End = p.start, p.end
	out.Session = Window{Used: st.sessionUsed, Limit: t.session}
	out.Day = Window{Used: st.dayUsed, Limit: t.day, ResetsAt: midnight(now)}
	if st.sessionStart != 0 {
		out.Session.ResetsAt = time.Unix(st.sessionStart, 0).Add(t.block).UTC()
	}
	out.Classes = map[string]Class{}
	for _, class := range flags.PlanClasses {
		a := t.allow[class]
		if a.unbounded {
			used, err := l.store.spent(ctx, org, subject, class, t.start.Unix())
			if err != nil {
				return Standing{}, err
			}
			out.Classes[class] = Class{Period: Window{Used: used, ResetsAt: p.end}, Paying: PaysPlan, Unbounded: true}
			continue
		}
		if a.period <= 0 {
			// The plan includes none of the class: credits pay for it, else nothing does.
			c := Class{Period: Window{ResetsAt: p.end}}
			if past(class) {
				c.Paying = PaysCredits
			}
			out.Classes[class] = c
			continue
		}
		used, err := l.store.spent(ctx, org, subject, class, t.start.Unix())
		if err != nil {
			return Standing{}, err
		}
		c := Class{Period: Window{Used: used, Limit: a.period, ResetsAt: p.end}}
		left := a.period - used
		if a.span > 0 {
			wused, err := l.store.spent(ctx, org, subject, class, now.Add(-a.span).Unix())
			if err != nil {
				return Standing{}, err
			}
			c.Window = &Window{Used: wused, Limit: a.window, ResetsAt: now.Add(a.span).UTC()}
			left = min(left, a.window-wused)
		}
		switch {
		case left > 0:
			c.Paying = PaysPlan
		case past(class):
			c.Paying = PaysCredits
		}
		out.Classes[class] = c
	}
	out.Pays = map[string]string{}
	for class, c := range out.Classes {
		out.Pays[class] = c.Paying
	}
	rs, err := l.policy(ctx, org)
	if err != nil {
		return Standing{}, err
	}
	for key, r := range rs {
		c := p.cap(r, t.seat(classOf(Ask{Priced: true}, r)))
		if !c.bounded() {
			continue
		}
		full, resets, err := l.overShare(ctx, Ask{Subject: subject, Org: org}, key, c, p, t, now)
		if err != nil {
			return Standing{}, err
		}
		if full {
			out.Paused = append(out.Paused, Paused{Model: key, Fallback: r.fallback(), ResetsAt: resets})
		}
	}
	slices.SortFunc(out.Paused, func(a, b Paused) int { return strings.Compare(a.Model, b.Model) })
	return out, nil
}

// Nanos reads an exact USD amount as nano-dollars, rounded up.
func Nanos(usd string) (int64, error) {
	if strings.TrimSpace(usd) == "" {
		return 0, nil
	}
	a, err := money.ParseUSD(usd)
	if err != nil {
		return 0, err
	}
	return NanosOfAtto(a.Atto()), nil
}

// NanosOfAtto folds atto-dollars (1e-18 USD) to nano-dollars, rounded up. Past what
// an int64 holds (about $9.2 billion) it answers the most it holds, never a wrapped
// negative.
func NanosOfAtto(atto *big.Int) int64 {
	if atto == nil || atto.Sign() <= 0 {
		return 0
	}
	q, r := new(big.Int).QuoRem(atto, big.NewInt(1_000_000_000), new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return math.MaxInt64
	}
	return q.Int64()
}

var (
	sharedOnce sync.Once
	shared     *Limiter
	sharedErr  error
)

// Shared is this process's limiter, over the one limits store in its data dir.
// Every gate in the process asks the same one, so the store has one writer.
func Shared() (*Limiter, error) {
	sharedOnce.Do(func() {
		shared, sharedErr = Open(cloud.DataDir(), planeRung, planeSubs, planeCatalog, walletFunds, nil)
	})
	return shared, sharedErr
}

// errNoFunds is what a limiter opened without a wallet reader answers.
var errNoFunds = errors.New("limits: no wallet reader")

// walletFunds reads what a payer may spend, in nano-dollars: its own money
// (finance.Funds.Own — cash, then the line left) and the credit the platform granted
// it. A model that refuses granted credit is paid from its own money alone.
func walletFunds(ctx context.Context, subject, org string) (int64, int64, error) {
	f, err := fundsOf(ctx, subject, org)
	if err != nil {
		return 0, 0, err
	}
	return NanosOfAtto(f.Own().Atto()), NanosOfAtto(f.Credit.Atto()), nil
}

// fundsOf reads the payer's wallet as the ledger states it: from the ledger when this
// process holds it, else from commerce over the plane. finance decides what of it is
// the payer's — the line is the org's own account's, and a member's wallet holds none.
func fundsOf(ctx context.Context, subject, org string) (finance.Funds, error) {
	if f, ok := finance.Current().(finance.Funder); ok {
		return f.Funds(ctx, org, subject, network.SandboxOnly())
	}
	bal, err := commercepeer.FinanceBalance(cloud.For(ctx, org), &client.BalanceIn{Subject: subject, Currency: "usd"})
	if err != nil {
		return finance.Funds{}, fmt.Errorf("plane balance read: %w", err)
	}
	if bal == nil {
		return finance.Funds{}, fmt.Errorf("plane balance read: commerce answered nothing")
	}
	return balanceFunds(bal)
}

// balanceFunds reads the spendable parts of commerce's balance reply back into the
// ledger's Funds. A figure the reply leaves empty is one its ledger does not report
// — a ledger from before the line reports none — and pays nothing.
func balanceFunds(bal *client.Balance) (finance.Funds, error) {
	z := money.Zero()
	f := finance.Funds{Cash: z, Credit: z, Line: z, Owed: z, Limit: z}
	for _, x := range []struct {
		name string
		m    client.Money
		dst  *money.Amount
	}{{"cash", bal.Cash, &f.Cash}, {"line", bal.Line, &f.Line}, {"credit", bal.Credit, &f.Credit}} {
		if strings.TrimSpace(x.m.Decimal) == "" {
			continue
		}
		a, err := money.ParseUSD(x.m.Decimal)
		if err != nil {
			return finance.Funds{}, fmt.Errorf("plane balance read: %s: %w", x.name, err)
		}
		*x.dst = a
	}
	return f, nil
}
