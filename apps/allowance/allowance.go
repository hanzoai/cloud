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

// Package allowance is how much a plan lets you do without paying, and how much of
// it you have left today.
//
// It bounds the ONE thing a wallet cannot. Money gates every priced route, but a
// route priced at ZERO leaves the balance gate nothing to refuse — which is
// deliberate, so that a caller with no wallet can still reach the free pool — and
// the free pool runs on our own compute. Without a ceiling that lane is unlimited
// for anyone who can name a free model. The allowance is that ceiling: a COUNT of
// calls, per subject, per period, taken from the caller's plan.
//
// COUNT AND MONEY NEVER STAND IN FOR EACH OTHER. A free call costs nothing, so a
// balance can neither express it nor refuse it; a paid call is bounded by the wallet
// and never reaches this store. Keeping them apart is what lets the free tier be
// generous without issuing credit we do not owe, and lets a paid plan be metered in
// dollars without a second opinion about it.
//
// The surface:
//
//	GET /v1/allowance     what the CALLER has left this period, and when it turns over
//	plane allowance_read  the same, for the AI gate, before it admits a call
//	plane allowance_take  count one SERVED call and answer the same
//
// A CALL IS COUNTED WHERE IT ANSWERED. The gate reads the ceiling to admit a call and
// the count rises when one has been served, because the ceiling bounds spend and spend
// is incurred when a model is reached. A request that reaches none — an unresolvable
// route, a vendor that never replied, a pod being rolled — costs the caller nothing.
// Two calls from one subject can therefore both be admitted by the same last unit;
// serving six on a ceiling of five is the generous direction, and the caller feels the
// other one.
//
// The gate is in another process (the pod runs each subsystem as its own program),
// so it ASKS rather than opening the file — the same discipline the prepaid ledger
// keeps, and for the same reason: one writer.
//
// THE LIMIT IS A PLATFORM SWITCH, per tier, editable live at admin.hanzo.ai through
// the existing /v1/admin/flags cockpit. It is not in the plan catalog because the
// catalog ships with a release and this number is a marketing dial — the same
// argument that already puts the rolling spend cap's tiers there.
//
// UNBOUNDED IS SAID ONLY ABOUT SOMEONE WE CAN NAME. A named tier's switch may be 0,
// which is an admin's decision about a subscriber commerce identified. A caller
// nobody can name — the public lane, an unresolved tier, a tier read that failed —
// gets the floor, and no setting of that switch can turn it off. A route STATED at
// zero can still be served by a vendor who bills us, so "we could not tell who this
// is" must never mean "as much as they want".
package allowance

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document and
// the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/allowance openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// limitKey is the per-tier switch: how many zero-priced calls a caller on that NAMED
// tier may make per period. 0 = unbounded, which is a decision an admin makes about a
// subscriber they can name — never an answer anybody falls into.
func limitKey(tier string) string { return "ai_allowance_" + tier }

// rateKey names the per-hour switch for a tier. Separate keys rather than one
// key with two numbers, because an admin tunes a rate and a quota for different
// reasons: the rate is about a burst, the quota is about a day's cost.
func rateKey(tier string) string {
	if tier == "" {
		tier = tenant.Public
	}
	return "ai_rate_" + tier
}

// publicKey is the ceiling for a caller nobody can name: the reserved public lane,
// and any subject whose tier did not resolve. See floor — this switch tunes the
// number and CANNOT turn it off.
const publicKey = "ai_allowance_public"

// strangers is the shipped ceiling for an unnamed caller, and the value floor falls
// back to when the switch is unset or non-positive. Small on purpose: a stranger is
// not a customer, and the free pool can end at a vendor who bills us whatever the
// route's stated price is.
const strangers = 3

// seed is the shipped default for each tier the commerce taxonomy answers with.
//
// FREE IS THE ONLY BOUNDED SUBSCRIBER. A paying subscriber's spend is bounded by
// their wallet and their rolling cap; counting their free-model calls as well would
// refuse work they have already paid for. So every paid tier seeds 0 (unbounded) and
// the ceiling exists exactly where the money does not.
//
// These are the runtime DEFAULTS; the flag store is authoritative once an admin
// edits one in the cockpit.
var seed = map[string]int{
	"free": 50, "starter": 0, "pro": 0, "enterprise": 0,
}

// rate is the per-HOUR ceiling, where a tier has one. A daily quota alone is a bad
// shape for a free tier — fifty a day is fifty in the first minute — so free is
// also held to ten an hour, which is what makes the fifty last a day rather than a
// lunch break. A paid tier has no rate here for the same reason it has no quota:
// its spend is bounded by a wallet.
var rate = map[string]int{
	"free": 10,
}

// strangersRate is the anonymous hourly ceiling. Zero: three a day is already
// tighter than any hour could usefully be, and a second window would only be a
// second thing to reason about for a caller who gets three.
const strangersRate = 0

// tiers is the seeded tier names, sorted, so registration order is stable.
func tiers() []string {
	out := make([]string, 0, len(seed))
	for t := range seed {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// rateFor is a tier's shipped hourly ceiling, and the public lane's.
func rateFor(tier string) int {
	if tier == tenant.Public {
		return strangersRate
	}
	return rate[tier]
}

func init() {
	flags.Register(flags.Def{
		Key: publicKey, Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(strangers),
		Label: "Free calls per day — public",
		Desc: "Zero-priced AI calls per UTC day for an anonymous visitor, and for any caller whose plan " +
			"could not be read. Tunable in both directions; 0 or less restores the shipped " +
			strconv.Itoa(strangers) + " rather than removing the ceiling.",
	})
	for tier, calls := range seed {
		flags.Register(flags.Def{
			Key: limitKey(tier), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(calls),
			Label: "Free calls per day — " + tier,
			Desc:  "Zero-priced AI calls a " + tier + " caller may make per UTC day. 0 = unbounded.",
		})
	}
	// The hourly rate, for every tier plus the public lane, so a switch exists for
	// each even where the shipped value is 0 (that window does not bound them).
	// Registering only the tiers that HAVE a rate would leave an admin unable to
	// add one without a release, which is the thing this table exists to avoid.
	for _, tier := range append(tiers(), tenant.Public) {
		flags.Register(flags.Def{
			Key: rateKey(tier), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(rateFor(tier)),
			Label: "Free calls per hour — " + tier,
			Desc: "Zero-priced AI calls a " + tier + " caller may make per UTC hour, which is what keeps " +
				"a day's quota from being spent in a minute. 0 = this window does not bound them.",
		})
	}
}

// service is this subsystem's own data: the store, and the tier reader that says
// which ceiling applies. tier may be nil where commerce is not reachable — every
// caller is then held to the floor, because a deployment that cannot read a plan
// cannot tell a customer from a stranger.
type service struct {
	store *Store
	tier  cloud.TierReaderFunc
	log   luxlog.Logger
}

var mounted *service

// Mount registers the allowance surface and the plane op the AI gate asks.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("allowance.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("allowance.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("allowance.Mount: open store: %w", err)
	}
	log := luxlog.Default().New("subsystem", "allowance")
	// cloud.TierReader is installed by BuildDeps, which completes before MountAll —
	// so it is already there when this reads it, in whichever process mounts this.
	s := &service{store: store, tier: cloud.TierReader(), log: log}
	mounted = s
	routes(app, s)
	expose()
	log.Info("allowance surface mounted", "prefix", "/v1/allowance", "tier", s.tier != nil)
	return nil
}

// Shutdown releases the store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}

// ops binds the mounted service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from.
type ops struct{ s *service }

// routes registers the product's read. There is no write face: a count is taken by
// the gate over the plane and by nothing else, so there is no address at which a
// caller could spend or forgive their own allowance.
func routes(app cloud.Router, s *service) {
	o := ops{s: s}
	zip.Get(app.Group("/v1"), "/allowance", o.get)
}

// noArgs is the empty input of a read that takes its whole scope from the caller.
type noArgs struct{}

// Answers what the CALLER has left of their plan's free-call allowance this period,
// and the instant the count starts again.
//
// This is the number a product shows beside the composer — "17 of 20 left today" —
// and the moment to offer a plan is when it reaches zero. It READS: asking does not
// spend, so a page that polls it costs the caller nothing.
//
// The subject is the caller's own, resolved from the verified credential, and can
// never be named in the request — so this is a mirror, not a lookup of someone else.
// An unauthenticated caller is refused: there is no allowance without someone to
// hold it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) get(ctx context.Context, _ *noArgs) (*plane.Allowance, error) {
	c, has := cloud.Request(ctx)
	if !has {
		return nil, zip.ErrForbidden("allowance: a validated principal is required")
	}
	w, ok := principal.WalletOf(c)
	if !ok {
		return nil, zip.ErrForbidden("allowance: a validated principal is required")
	}
	// A read is per-caller and changes on every call, so no hop may keep it.
	c.SetHeader("Cache-Control", "no-store")
	return o.s.read(ctx, w.Ledger, w.Account, time.Now())
}

// bound is one ceiling over one window: the rate, or the quota.
type bound struct {
	window string // "hour" | "day" — the store's row key, and what the answer names
	period string // the instant's place in that window; a row from another one reads as zero
	limit  int64  // 0 or less: this window does not bound the subject
	resets time.Time
}

// bounds are the windows a subject is held to, TIGHTEST FIRST.
//
// Two of them, because a daily quota alone is a bad shape for a free tier: fifty a
// day is fifty in the first minute, which is a script's afternoon rather than a
// person's. The hourly rate is what makes the daily number last, and the daily
// number is what stops twenty-four good hours costing us a day's inference.
//
// Order is the answer's order: the first window that refuses is the one the caller
// is told about, so "ten an hour" is what a person who just sent ten reads —
// naming the daily fifty there would be true and useless.
func (s *service) bounds(ctx context.Context, subject, org string, now time.Time) (tier string, out []bound) {
	tier, hourly, daily := s.limits(ctx, subject, org)
	return tier, []bound{
		{window: "hour", period: Hour(now), limit: hourly, resets: NextHour(now)},
		{window: "day", period: Day(now), limit: daily, resets: Midnight(now)},
	}
}

// standing turns a set of windows into the one answer the wire carries: the window
// that BINDS. A refused window binds; otherwise the one with least left does, so a
// caller reading "3 of 10" is reading the number that will actually stop them.
func standing(tier string, bs []bound, used map[string]int64, now time.Time) *plane.Allowance {
	out := &plane.Allowance{Plan: tier}
	var chosen *bound
	var chosenUsed int64
	for i := range bs {
		b := &bs[i]
		if b.limit <= 0 {
			continue
		}
		u := used[b.window]
		switch {
		case u >= b.limit:
			// Refused. The first such window wins because bounds is tightest-first.
			out.Window, out.Limit, out.Used, out.Spent, out.Resets = b.window, b.limit, u, true, b.resets.Unix()
			return out
		case chosen == nil || b.limit-u < chosen.limit-chosenUsed:
			chosen, chosenUsed = b, u
		}
	}
	if chosen == nil {
		// No window bounds this subject: a named paid tier. Limit 0 has always been
		// this answer's word for unbounded, and Resets still says when a count that
		// does not exist would have turned over — off the CALLER's clock, because a
		// service handed an instant may not go asking the wall what time it is.
		out.Resets = Midnight(now).Unix()
		return out
	}
	out.Window, out.Limit, out.Used, out.Resets = chosen.window, chosen.limit, chosenUsed, chosen.resets.Unix()
	return out
}

// read answers subject's standing without taking anything.
func (s *service) read(ctx context.Context, org, subject string, now time.Time) (*plane.Allowance, error) {
	tier, bs := s.bounds(ctx, subject, org, now)
	used := map[string]int64{}
	for _, b := range bs {
		if b.limit <= 0 {
			continue
		}
		u, err := s.store.Read(ctx, subject, b.window, b.period)
		if err != nil {
			return nil, err
		}
		used[b.window] = u
	}
	return standing(tier, bs, used, now), nil
}

// take counts one call and answers the standing that follows it.
//
// EVERY WINDOW IS ASKED BEFORE ANY IS CHARGED. Taking hour-then-day would spend the
// hour on a call the day then refuses, so a caller at their daily ceiling would burn
// an hourly slot every time they were turned away — and the hour would never
// recover while they kept trying.
func (s *service) take(ctx context.Context, org, subject string, now time.Time) (*plane.Allowance, error) {
	tier, bs := s.bounds(ctx, subject, org, now)
	used := map[string]int64{}
	for _, b := range bs {
		if b.limit <= 0 {
			continue
		}
		u, err := s.store.Read(ctx, subject, b.window, b.period)
		if err != nil {
			return nil, err
		}
		used[b.window] = u
		if u >= b.limit {
			return standing(tier, bs, used, now), nil
		}
	}
	for _, b := range bs {
		if b.limit <= 0 {
			continue
		}
		u, _, err := s.store.Take(ctx, subject, b.window, b.period, b.limit)
		if err != nil {
			return nil, err
		}
		used[b.window] = u
	}
	return standing(tier, bs, used, now), nil
}

// limit resolves which ceiling applies to a subject.
//
// UNBOUNDED IS ONLY EVER SAID ABOUT SOMEONE WE CAN NAME. A named tier's switch may be
// 0, because that is an admin's decision about a subscriber commerce identified. Every
// other answer — the public lane, an empty tier, a tier read that errored, a
// deployment with no tier reader at all — lands on the floor, which cannot be zero.
//
// The direction matters because the free pool does not always end at our own compute:
// a route STATED at zero can be served by a vendor who bills us anyway. So "we could
// not tell who this is" must never mean "let them have as much as they want" — that
// is a bill with no payer, and one script is enough to run it up.
//
// The cost of failing closed here is bounded and short: during a commerce blip a
// paying caller keeps every PRICED route (their own gate, with its own fallback) and
// is briefly held to the visitor ceiling on the free pool alone.
func (s *service) limits(ctx context.Context, subject, org string) (tier string, hourly, daily int64) {
	// The public lane has no plan to look up and no lookup that could fail, so it
	// never asks. The org is minted by the identity boundary and never by a client.
	if org == tenant.Public {
		return tenant.Public, int64(flags.Int(rateKey(tenant.Public))), s.floor()
	}
	if s.tier == nil {
		return "", int64(flags.Int(rateKey(""))), s.floor()
	}
	name, err := s.tier(ctx, subject, org)
	if err != nil || name == "" {
		return "", int64(flags.Int(rateKey(""))), s.floor()
	}
	// A tier this app has never had an opinion about is not an opinion. seed is that
	// list, and a name outside it means commerce's taxonomy has grown past ours —
	// which is a disagreement about billing, and the safe reading of one is the
	// strict one. It is also loud: the operator sees the cap and registers the
	// switch, rather than discovering a new tier was free all along.
	if _, decided := seed[name]; !decided {
		return name, int64(flags.Int(rateKey(name))), s.floor()
	}
	// 0 in either is an admin's explicit unbounded for that window.
	return name, int64(flags.Int(rateKey(name))), int64(flags.Int(limitKey(name)))
}

// floor is the ceiling for a caller nobody could name: the switch, read through the
// one rule that keeps it a ceiling.
func (s *service) floor() int64 { return ceiling(flags.Int(publicKey)) }

// ceiling turns a switch setting into a limit for an unnamed caller. It is TOTAL and
// never answers zero: an operator may tune the number in either direction, and a
// non-positive setting restores the shipped value rather than removing the bound. So
// there is no value of the switch, and no failure of the tier read, that ends in
// unlimited free inference for a stranger.
func ceiling(n int) int64 {
	if n > 0 {
		return int64(n)
	}
	return strangers
}
