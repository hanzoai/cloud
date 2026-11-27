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

package allowance

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/tenant"
	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"

	aipeer "github.com/hanzoai/cloud/client/ai"
	flagsplane "github.com/hanzoai/cloud/client/flag"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document and
// the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/allowance openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// meter is one thing a plan lets a subject do without paying: its switches, its
// shipped ceilings, and the rows it counts in. There are three: free AI calls,
// free sandbox leases, and automation runs a paid plan includes; no count
// stands in for another.
type gauge struct {
	// name is the meter as AllowanceIn.Meter names it; empty is AI calls.
	name string
	// key prefixes every switch of the meter: `<key>_allowance_<tier>` is a tier's
	// day, `<key>_rate_<tier>` its hour, `<key>_session_<tier>` its session,
	// `<key>_allowance_public` the floor.
	key string
	// label is the unit in a switch's label ("Free calls per day"), and noun
	// opens its description.
	label, noun string
	// daily is the shipped per-UTC-day ceiling for each tier the commerce taxonomy
	// answers with. 0 is unbounded, which only a tier commerce named can be.
	daily map[string]int
	// hourly is the per-UTC-hour ceiling, where a tier has one.
	hourly map[string]int
	// session is the shipped per-session ceiling for each tier, on a meter that
	// counts sessions; nil on one that does not. 0 leaves a tier's session unbounded.
	session map[string]int
	// strangers is the day's ceiling for a caller nobody can name, and what floor
	// falls back to when its switch is unset or non-positive.
	strangers int
	// strangersRate is that caller's hourly ceiling; 0 leaves the hour unbounded
	// for them, since the day already is.
	strangersRate int
	// paid says a wallet holding credit is unbounded on this meter (limits).
	paid bool
}

// limitKey is the per-tier switch: how many units a caller on that NAMED tier may
// take per day. 0 = unbounded, which is a decision an admin makes about a
// subscriber they can name — never an answer anybody falls into.
func (m gauge) limitKey(tier string) string { return m.key + "_allowance_" + tier }

// rateKey names the per-hour switch for a tier. Separate keys rather than one
// key with two numbers, because an admin tunes a rate and a quota for different
// reasons: the rate is about a burst, the quota is about a day's cost.
func (m gauge) rateKey(tier string) string {
	if tier == "" {
		tier = tenant.Public
	}
	return m.key + "_rate_" + tier
}

// sessionKey names the per-session switch for a tier.
func (m gauge) sessionKey(tier string) string { return m.key + "_session_" + tier }

// publicKey is the ceiling for a caller nobody can name: the reserved public lane,
// and any subject whose tier did not resolve. See floor — this switch tunes the
// number and CANNOT turn it off.
func (m gauge) publicKey() string { return m.key + "_allowance_public" }

// row is the store's window key for one of this meter's windows. AI calls keep
// the bare window, so the rows they wrote before there was a second meter still
// count; every other meter prefixes its name.
func (m gauge) row(window string) string {
	if m.name == "" {
		return window
	}
	return m.name + "." + window
}

// tiers is the seeded tier names, sorted, so registration order is stable.
func (m gauge) tiers() []string {
	out := make([]string, 0, len(m.daily))
	for t := range m.daily {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// rateFor is a tier's shipped hourly ceiling, and the public lane's.
func (m gauge) rateFor(tier string) int {
	if tier == tenant.Public {
		return m.strangersRate
	}
	return m.hourly[tier]
}

// strangers is the shipped AI-call ceiling per UTC day for an unnamed caller,
// matching the Free plan's session allowance (flags.SeedFreeSession = 50).
const strangers = flags.SeedFreeSession

// sessions is the shipped AI-call ceiling per session window for each tier.
//
// FREE IS THE ONLY BOUNDED SUBSCRIBER. The Free plan is flags.SeedFreeSession (50) calls every
// five hours: a person's window opens at their first free call after the last one
// ended, call 51 is refused until five hours after call one, and then 50 more are
// theirs. A paying subscriber's spend is bounded by their wallet and their rolling
// cap; counting their free-model calls as well would refuse work they have already
// paid for. So every paid tier seeds 0 (unbounded) and the ceiling exists exactly
// where the money does not — and a free-tier wallet that holds credit is paying too
// (limits).
//
// These are the runtime DEFAULTS; the flag store is authoritative once an admin
// edits one in the cockpit.
var sessions = map[string]int{
	"free": flags.SeedFreeSession, "starter": 0, "pro": 0, "enterprise": 0,
}

// seed is the shipped AI-call ceiling per UTC day for each tier: none, so the Free
// plan is held to its session alone. Its keys are the tiers this meter has an
// opinion about (planned), and an admin can add a day to any of them from the
// cockpit.
var seed = map[string]int{"free": 0, "starter": 0, "pro": 0, "enterprise": 0}

// calls is the meter of zero-priced AI calls. It ships with the session alone: no
// tier has a day or an hourly figure, so a free person's calls are theirs to spend
// when they like inside their five hours. The anonymous lane has no session and
// keeps its day (strangers), with its hour unbounded because the stranger's daily count is already
// tighter than any hour could usefully be. An admin can add an hour or a day per
// tier from the cockpit.
var calls = gauge{key: "ai", label: "calls", noun: "Zero-priced AI calls", daily: seed, session: sessions, strangers: strangers, paid: true}

// leases is the meter of free sandbox leases: the lane a lease takes when its
// wallet cannot cover the first hour (apps/sandbox prepaid.go).
//
// EVERY TIER IS BOUNDED. A lease holds a pod on our nodes whatever the plan says,
// and this lane exists only for a wallet that cannot pay for one; a paid tier with
// money leases past it on its wallet and never reaches this count. Twenty a day at
// six an hour is a day of coding runs, each held at most the free lease's TTL.
var leases = gauge{
	name: client.AllowanceSandbox, key: "sandbox", label: "sandboxes", noun: "Free sandbox leases",
	daily:     map[string]int{"free": 20, "starter": 20, "pro": 20, "enterprise": 20},
	hourly:    map[string]int{"free": 6, "starter": 6, "pro": 6, "enterprise": 6},
	strangers: 1,
}

// runs is the meter of automation runs a paid plan includes (apps/auto plan.go):
// a paid plan's run is counted here and charged nothing. Only a paid plan is
// asked, so the free tier has no row and an unnamed tier gets the floor.
var runs = gauge{
	name: client.AllowanceAutomations, key: "automations", label: "automation runs", noun: "Automation runs a paid plan includes",
	daily:     map[string]int{"starter": 20, "pro": 100, "enterprise": 500},
	hourly:    map[string]int{"starter": 6, "pro": 20, "enterprise": 60},
	strangers: 1,
}

// meters is every meter, by the name a plane call gives it.
var meters = map[string]gauge{calls.name: calls, leases.name: leases, runs.name: runs}

func init() {
	for _, m := range []gauge{calls, leases, runs} {
		m.register()
	}
}

// register publishes a meter's switches to the admin cockpit.
func (m gauge) register() {
	flags.Register(flags.Def{
		Key: m.publicKey(), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(m.strangers),
		Label: "Free " + m.label + " per day — public",
		Desc: m.noun + " per UTC day for an anonymous visitor, and for any caller whose plan " +
			"could not be read (per session on a meter that counts sessions). Tunable in both directions; " +
			"0 or less restores the shipped " + strconv.Itoa(m.strangers) + " rather than removing the ceiling.",
		Env: strings.ToUpper(m.key) + "_ALLOWANCE_PUBLIC",
	})
	for tier, n := range m.daily {
		flags.Register(flags.Def{
			Key: m.limitKey(tier), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(n),
			Label: "Free " + m.label + " per day — " + tier,
			Desc:  m.noun + " a " + tier + " caller may make per UTC day. 0 = unbounded.",
			Env:   strings.ToUpper(m.key) + "_ALLOWANCE_" + strings.ToUpper(tier),
		})
	}
	// The hourly rate, for every tier plus the public lane, so a switch exists for
	// each even where the shipped value is 0 (that window does not bound them).
	// Registering only the tiers that HAVE a rate would leave an admin unable to
	// add one without a release, which is the thing this table exists to avoid.
	for _, tier := range append(m.tiers(), tenant.Public) {
		flags.Register(flags.Def{
			Key: m.rateKey(tier), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(m.rateFor(tier)),
			Label: "Free " + m.label + " per hour — " + tier,
			Desc: m.noun + " a " + tier + " caller may make per UTC hour, which is what keeps " +
				"a day's quota from being spent in a minute. 0 = this window does not bound them.",
			Env: strings.ToUpper(m.key) + "_RATE_" + strings.ToUpper(tier),
		})
	}
	// The session, for every tier of a meter that counts one, for the same reason.
	// Its length is the plan session's own switch, so a Free person and a paid plan
	// are held to one kind of window.
	if m.session != nil {
		for _, tier := range m.tiers() {
			flags.Register(flags.Def{
				Key: m.sessionKey(tier), Category: "Gateway", Type: flags.TypeInt, Default: strconv.Itoa(m.session[tier]),
				Label: "Free " + m.label + " per " + strconv.Itoa(flags.SeedSessionHours) + " hours — " + tier,
				Desc: m.noun + " a " + tier + " caller may make per session: a window that opens at their first call " +
					"after the last one ended and runs " + flags.PlanSessionHours + " hours. 0 = this window does not bound them; " +
					"less than 0 restores the shipped " + strconv.Itoa(m.session[tier]) + ".",
			})
		}
	}
	for _, o := range staff {
		flags.Register(flags.Def{
			Key: m.key + "_allowance_org_" + o, Category: "Gateway", Type: flags.TypeInt, Default: "-1",
			Label: "Free " + m.label + " per day — org " + o,
			Desc:  m.noun + " callers in org " + o + " may make per UTC day. -1 = follow tier, 0 = unbounded.",
		})
		flags.Register(flags.Def{
			Key: m.key + "_rate_org_" + o, Category: "Gateway", Type: flags.TypeInt, Default: "-1",
			Label: "Free " + m.label + " per hour — org " + o,
			Desc:  m.noun + " callers in org " + o + " may make per UTC hour. -1 = follow tier, 0 = unbounded.",
		})
	}
	// The session has one org switch, the platform org's. hanzo has none: it is the
	// signup org every consumer joins, so its switch would be every consumer's.
	if m.session != nil {
		flags.Register(flags.Def{
			Key: m.key + "_session_org_" + authz.AdminOrg, Category: "Gateway", Type: flags.TypeInt, Default: "-1",
			Label: "Free " + m.label + " per " + strconv.Itoa(flags.SeedSessionHours) + " hours — org " + authz.AdminOrg,
			Desc:  m.noun + " callers in org " + authz.AdminOrg + " may make per session. -1 = follow tier, 0 = unbounded.",
		})
	}
}

// service is this subsystem's own data: the store, and the tier read that says
// which ceiling applies. A caller whose tier the read cannot name is held to the
// floor, because a deployment that cannot read a plan cannot tell a customer from a
// stranger.
type service struct {
	store *Store
	tier  func(ctx context.Context, subject, org string) (string, error)
	// funded reports whether a wallet holds credit to spend: the prepaid gate's own
	// answer (metering), so a free-tier wallet that has bought credit is paying and its
	// free calls are not counted. nil reads every wallet as unfunded.
	funded func(ctx context.Context, org, payer string) bool
	// pool reads the free pool's standing from the process that spends it. The
	// usage page asks; the gate never does, so admitting a call never waits on it.
	pool func(ctx context.Context, org string) (*client.Pool, error)
	// holder names the account whose plan a person's call in an org spends when the
	// org's books hold none (cloud.Holder). nil lends no plan.
	holder func(ctx context.Context, person, org string) account.Account
	log    luxlog.Logger
	// tally is what the takes since the last report admitted and refused.
	tally tally
}

// tally counts takes by outcome, for the minute's report.
type tally struct{ admitted, refused, unbounded atomic.Int64 }

// reportEvery is how often the allowance says what it admitted and refused.
const reportEvery = time.Minute

// report logs, once a minute while there is anything to say, how many free units
// were admitted, refused, and passed unbounded (a paid plan or a funded wallet). It
// is the live answer to "is the ceiling holding": a refused count that rises while
// the admitted one holds is people at their ceiling, and an admitted count that runs
// past the ceiling times the people active is a ceiling that is not.
func (s *service) report(stop <-chan struct{}) {
	t := time.NewTicker(reportEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			a, r, u := s.tally.admitted.Swap(0), s.tally.refused.Swap(0), s.tally.unbounded.Swap(0)
			if a+r+u > 0 {
				s.log.Info("allowance: free units this minute", "admitted", a, "refused", r, "unbounded", u)
			}
		}
	}
}

// spentOnce logs a subject's first refusal of the day, so the log names each person
// who reached their ceiling once rather than once per call they kept sending.
func (s *service) spentOnce(ctx context.Context, m gauge, org, payer, subject string, out *client.Allowance, now time.Time) {
	if s.log == nil {
		return
	}
	first, err := s.store.c.Once(ctx, quotaName+":spent", m.row(out.Window)+":"+Day(now)+":"+subject, 25*time.Hour)
	if err != nil || !first {
		return
	}
	s.log.Info("allowance: ceiling reached", "meter", cmp.Or(m.name, "calls"), "subject", subject, "payer", payer, "org", org,
		"plan", out.Plan, "window", out.Window, "limit", out.Limit)
}

var mounted *service

// Mount registers the allowance surface and the plane op the AI gate asks.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("allowance: nil app")
	}
	log := luxlog.Default().New("subsystem", "allowance")
	// The plan and the balance are commerce's and commerce is another process, so
	// both are asked over the plane: the tier through cloud.Tier, the balance through
	// the same prepaid gate that refuses a priced call.
	s := &service{store: openStore(), tier: cloud.Tier, funded: fundedBy(deps.Metering), pool: askPool, holder: cloud.Holder, log: log}
	mounted, stopReport = s, make(chan struct{})
	go s.report(stopReport)
	routes(app, s)
	expose()
	log.Info("allowance surface mounted", "prefix", "/v1/allowance")
	return nil
}

// stopReport ends the mounted service's report.
var stopReport chan struct{}

// Shutdown forgets the mounted service; the counts are the KV's. Idempotent.
func Shutdown(_ context.Context) error {
	if stopReport != nil {
		close(stopReport)
		stopReport = nil
	}
	mounted = nil
	return nil
}

// fundedTTL is how long a wallet's funded answer is trusted: credit bought takes
// effect within it, and a free call asks commerce at most once per wallet per period.
const fundedTTL = 30 * time.Second

// fundedBy reads whether a wallet holds credit through the prepaid gate (metering),
// with no amount: the "any positive balance" question the 402 money gate asks of a
// priced call. A funded wallet over a spend cap is still funded — the cap is about
// what it spends, not whether it pays. A gate that cannot answer is not funded: the
// caller keeps the free allowance rather than escaping it.
func fundedBy(m *meter.Client) func(ctx context.Context, org, payer string) bool {
	if m == nil {
		return nil
	}
	var mu sync.Mutex
	seen := map[string]fundedAnswer{}
	return func(ctx context.Context, org, payer string) bool {
		k := org + "\x00" + payer
		mu.Lock()
		a, ok := seen[k]
		mu.Unlock()
		if ok && time.Since(a.at) < fundedTTL {
			return a.funded
		}
		v, err := m.AuthorizeVerdict(ctx, meter.AuthInput{User: payer, Org: org, Service: "allowance"})
		if err != nil {
			return false
		}
		funded := v.Allow || v.Reason == "spend_cap"
		mu.Lock()
		if len(seen) >= fundedMax {
			seen = map[string]fundedAnswer{}
		}
		seen[k] = fundedAnswer{funded: funded, at: time.Now()}
		mu.Unlock()
		return funded
	}
}

type fundedAnswer struct {
	funded bool
	at     time.Time
}

// fundedMax bounds the wallets remembered; past it the map starts again.
const fundedMax = 1 << 16

// ops binds the mounted service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from.
type ops struct{ s *service }

// routes registers the product's read. There is no write face: a count is taken by
// the gate over the plane and by nothing else, so there is no address at which a
// caller could spend or forgive their own allowance.
func routes(app cloud.Router, s *service) {
	o := ops{s: s}
	app.Group("/v1/allowance").Get("", o.get)
}

// Answers what the CALLER has left of their plan's free-call allowance in the window
// that holds them, and the instant that window reopens.
//
// This is the number a product shows beside the composer — "2 of 3 left" —
// and the moment to offer a plan is when it reaches zero. It READS: asking does not
// spend, so a page that polls it costs the caller nothing. A Free person's window is
// a session that opens at their first call after the last one ended and runs five
// hours; while none runs, resets is 0 and the next call opens one.
//
// A bounded caller is POOLED: the Free plan is limited usage from one pool every
// free user shares — the platform's vendor accounts for free models — so the answer
// says so (pooled) and carries that pool's standing: available, busy or exhausted,
// and when it refills. The pool is read from the process that spends it and is
// absent when that process does not answer, never guessed. A paid plan is not
// pooled and carries no pool: its usage is metered in money.
//
// The subject is the caller's own, resolved from the verified credential, and can
// never be named in the request — so this is a mirror, not a lookup of someone else.
// An unauthenticated caller is refused: there is no allowance without someone to
// hold it.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o ops) get(ctx context.Context, _ *cloud.Unit) (*client.Allowance, error) {
	c, has := cloud.Request(ctx)
	if !has {
		return nil, zip.ErrForbidden("allowance: a validated principal is required")
	}
	w := meter.Payer(c)
	if w.Zero() {
		return nil, principal.Refused(c)
	}
	// A read is per-caller and changes on every call, so no hop may keep it.
	c.SetHeader("Cache-Control", "no-store")
	out, err := o.s.read(ctx, calls, w.Org(), w.Subject(), client.Person(zip.CallerOf(ctx)), time.Now())
	if err != nil {
		return nil, err
	}
	o.s.pooled(ctx, w.Org(), out)
	return out, nil
}

// poolWait bounds the pool read on the usage page. The allowance is the answer the
// page needs; the pool is beside it, so a slow ai costs the page the pool and never
// the allowance.
const poolWait = 2 * time.Second

// pooled marks a bounded standing as served from the shared pool and attaches the
// pool's standing where it can be read.
func (s *service) pooled(ctx context.Context, org string, out *client.Allowance) {
	if out == nil || out.Limit <= 0 {
		return // not bounded: a paid plan, metered in money rather than drawn from the pool
	}
	out.Pooled = true
	if s.pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, poolWait)
	defer cancel()
	p, err := s.pool(ctx, org)
	if err != nil || p == nil || p.State == "" {
		if err != nil {
			s.log.Warn("allowance: free pool unreadable", "err", err)
		}
		return
	}
	out.Pool = p
}

// askPool reads the pool's standing from ai over the plane.
func askPool(ctx context.Context, org string) (*client.Pool, error) {
	return aipeer.AIPool(cloud.For(ctx, org), &client.PoolIn{})
}

// bound is one ceiling over one window: the rate, the session, or the quota.
type bound struct {
	window string // "hour" | "session" | "day" — what the answer names
	row    string // the store's key for this window of this meter (meter.row)
	period string // the instant's place in that window; a row from another one reads as zero
	limit  int64  // 0 or less: this window does not bound the subject
	resets time.Time
	// span is a session's length; 0 for a calendar window. A session's period and
	// reset are its start's, which the store resolves (Store.Take).
	span time.Duration
	// opens says no session ran: the call being counted opens one at its instant.
	opens bool
}

// bounds are the windows a subject is held to, TIGHTEST FIRST: the hour, the
// session and the day, each where the meter sets one. A window whose limit is 0
// does not bound them.
//
// Order is the answer's order: the first window that refuses is the one the caller
// is told about, so "six an hour" is what a person who just took six leases reads —
// naming the daily twenty there would be true and useless.
func (s *service) bounds(ctx context.Context, m gauge, payer, org, subject string, now time.Time) (tier string, out []bound) {
	tier, hourly, session, daily := s.owned(ctx, m, payer, org, subject)
	var span time.Duration
	if session > 0 {
		span = flags.Session(flagsplane.Int(ctx, flags.PlanSessionHours, flags.SeedSessionHours))
	}
	return tier, []bound{
		{window: "hour", row: m.row("hour"), period: Hour(now), limit: hourly, resets: NextHour(now)},
		{window: "session", row: m.row("session"), limit: session, span: span},
		{window: "day", row: m.row("day"), period: Day(now), limit: daily, resets: Midnight(now)},
	}
}

// unix is an instant as the wire's unix seconds: 0 for none, a session that is not
// running, which opens at the next call.
func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// standing turns a set of windows into the one answer the wire carries: the window
// that BINDS. A refused window binds; otherwise the one with least left does, so a
// caller reading "3 of 10" is reading the number that will actually stop them.
func standing(tier string, bs []bound, used map[string]int64, now time.Time) *client.Allowance {
	out := &client.Allowance{Plan: tier}
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
			out.Window, out.Limit, out.Used, out.Spent, out.Resets = b.window, b.limit, u, true, unix(b.resets)
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
	out.Window, out.Limit, out.Used, out.Resets = chosen.window, chosen.limit, chosenUsed, unix(chosen.resets)
	return out
}

// read answers subject's standing on meter m without taking anything. payer is the
// wallet whose plan and balance decide the ceiling.
func (s *service) read(ctx context.Context, m gauge, org, payer, subject string, now time.Time) (*client.Allowance, error) {
	tier, bs := s.bounds(ctx, m, payer, org, subject, now)
	bs = bounded(bs)
	used := map[string]int64{}
	if len(bs) > 0 {
		for i, u := range s.store.Read(ctx, subject, bs, now) {
			used[bs[i].window] = u
		}
	}
	out := standing(tier, bs, used, now)
	out.Start = opened(bs)
	return out, nil
}

// outage is how soon a call refused while the shared count is not answering is told
// to ask again. Such a refusal is this pod's share of a ceiling (coord.Quota), not a
// window the caller spent, so it names no window's reset: the KV is asked again
// within seconds, and a minute is a wait a client can sit through (Retry-After).
const outage = time.Minute

// take admits one call against subject's windows on meter m, counting it, and answers
// the standing that follows. Spent says THIS call was refused, and then nothing was
// counted; a call that takes the last unit is admitted, with nothing left.
func (s *service) take(ctx context.Context, m gauge, org, payer, subject string, now time.Time) (*client.Allowance, error) {
	tier, bs := s.bounds(ctx, m, payer, org, subject, now)
	bs = bounded(bs)
	if len(bs) == 0 {
		s.tally.unbounded.Add(1)
		return standing(tier, bs, nil, now), nil
	}
	counts, taken := s.store.Take(ctx, subject, bs, now)
	used := map[string]int64{}
	for i, u := range counts {
		used[bs[i].window] = u
	}
	out := standing(tier, bs, used, now)
	out.Start = opened(bs)
	if taken {
		s.tally.admitted.Add(1)
		out.Spent = false
		return out, nil
	}
	s.tally.refused.Add(1)
	if !out.Spent {
		// The KV refused on a ceiling of its own: this pod's share while the shared
		// count is not answering (coord.Quota), which can be none at all. Refused all
		// the same, on the widest window, and told to ask again soon (outage) rather
		// than at a reset no window the caller spent has.
		w := bs[len(bs)-1]
		out.Window, out.Limit, out.Used, out.Resets = w.window, w.limit, w.limit, now.Add(outage).Unix()
	}
	out.Spent = true
	s.spentOnce(ctx, m, org, payer, subject, out, now)
	return out, nil
}

// give hands back the unit a take at at counted against subject's windows on meter
// m — the session among them the one that opened at start, the take's own answer —
// and answers the standing now.
func (s *service) give(ctx context.Context, m gauge, org, payer, subject string, at time.Time, start int64) (*client.Allowance, error) {
	_, bs := s.bounds(ctx, m, payer, org, subject, at)
	if bs = bounded(bs); len(bs) > 0 {
		s.store.Give(ctx, subject, bs, start)
	}
	return s.read(ctx, m, org, payer, subject, time.Now())
}

// bounded is the windows of bs that bound the subject.
func bounded(bs []bound) []bound {
	out := bs[:0:0]
	for _, b := range bs {
		if b.limit > 0 {
			out = append(out, b)
		}
	}
	return out
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
//
// A WALLET THAT HOLDS CREDIT IS PAYING, whatever its tier. On a meter that says so
// (calls) a bounded wallet is asked whether it is funded — the prepaid gate's own
// question — and a funded one is unbounded: its priced calls are paid for, and its
// free ones are not counted. The lease meter does not ask, because the free lease is
// exactly the lane of a wallet whose credit cannot cover a lease.
//
// The PUBLIC LANE'S FLOOR IS A DAY: a visitor is held to the visitor's ceiling per
// UTC day. A NAMED subject whose plan could not be read is held to the same figure
// per SESSION on a meter that counts sessions (unnamed) — the count a Free person
// is already spending — so a commerce blip opens no second count beside it.
func (s *service) limits(ctx context.Context, m gauge, payer, org string) (tier string, hourly, session, daily int64) {
	// The public lane has no plan to look up and no lookup that could fail, so it
	// never asks. The org is minted by the identity boundary and never by a client.
	if org == tenant.Public {
		return tenant.Public, int64(flagsplane.Int(ctx, m.rateKey(tenant.Public), m.rateFor(tenant.Public))), 0, m.floor(ctx)
	}
	tier, hourly, session, daily = s.planned(ctx, m, payer, org)
	if (hourly > 0 || session > 0 || daily > 0) && m.paid && s.funded != nil && s.funded(ctx, org, payer) {
		return tier, 0, 0, 0
	}
	return tier, hourly, session, daily
}

// owned is limits, unless the payer's plan bounds the call and the person making it
// owns the payer's org: then the person's own plan's ceiling, on a meter a paid plan
// lifts (calls). A project's key spends its owner's plan; a member of an org someone
// else owns is held to that org's.
func (s *service) owned(ctx context.Context, m gauge, payer, org, subject string) (tier string, hourly, session, daily int64) {
	tier, hourly, session, daily = s.limits(ctx, m, payer, org)
	if hourly <= 0 && session <= 0 && daily <= 0 || !m.paid || s.holder == nil {
		return tier, hourly, session, daily
	}
	acct := s.holder(ctx, subject, org)
	if acct.Zero() {
		return tier, hourly, session, daily
	}
	if t, h, se, d := s.limits(ctx, m, acct.Subject(), acct.Org()); m.decided(t) {
		return t, h, se, d
	}
	return tier, hourly, session, daily
}

// decided reports whether tier is one this meter has an opinion about: a plan
// commerce named, never the floor a plan nobody could read lands on.
func (m gauge) decided(tier string) bool {
	_, ok := m.daily[tier]
	return ok
}

// planned is the ceiling the payer's plan sets on meter m, as commerce names it.
func (s *service) planned(ctx context.Context, m gauge, subject, org string) (tier string, hourly, session, daily int64) {
	if s.tier == nil {
		return m.unnamed(ctx, "")
	}
	name, err := s.tier(ctx, subject, org)
	if err != nil || name == "" {
		return m.unnamed(ctx, "")
	}
	// A tier this app has never had an opinion about is not an opinion. seed is that
	// list, and a name outside it means commerce's taxonomy has grown past ours —
	// which is a disagreement about billing, and the safe reading of one is the
	// strict one. It is also loud: the operator sees the cap and registers the
	// switch, rather than discovering a new tier was free all along.
	if _, decided := m.daily[name]; !decided {
		return m.unnamed(ctx, name)
	}
	// 0 in any is an admin's explicit unbounded for that window. A session figure
	// below 0 is no figure: the tier's shipped one, as an org's -1 follows its tier.
	hourly = int64(flagsplane.Int(ctx, m.rateKey(name), m.rateFor(name)))
	if m.session != nil {
		session = int64(m.session[name])
		if n := flagsplane.Int(ctx, m.sessionKey(name), m.session[name]); n >= 0 {
			session = int64(n)
		}
	}
	daily = int64(flagsplane.Int(ctx, m.limitKey(name), m.daily[name]))
	// A staff org's own switch, when set (-1 is unset), replaces its plan's. Only the
	// platform org has a session switch: hanzo's would be every consumer's session.
	if slices.Contains(staff, org) {
		if n := flagsplane.Int(ctx, m.key+"_rate_org_"+org, -1); n >= 0 {
			hourly = int64(n)
		}
		if n := flagsplane.Int(ctx, m.key+"_allowance_org_"+org, -1); n >= 0 {
			daily = int64(n)
		}
	}
	if m.session != nil && org == authz.AdminOrg {
		if n := flagsplane.Int(ctx, m.key+"_session_org_"+org, -1); n >= 0 {
			session = int64(n)
		}
	}
	return name, hourly, session, daily
}

// unnamed is the ceiling for a subject whose plan could not be read, tier being
// whatever commerce did answer: the floor, which cannot be zero. On a meter that
// counts sessions it is held on the session, so the call lands on the count a Free
// person is already spending; on any other, on the day.
func (m gauge) unnamed(ctx context.Context, tier string) (string, int64, int64, int64) {
	hourly := int64(flagsplane.Int(ctx, m.rateKey(tier), m.rateFor(tier)))
	if m.session != nil {
		return tier, hourly, m.floor(ctx), 0
	}
	return tier, hourly, 0, m.floor(ctx)
}

// staff are the orgs with their own day and hour switches. admin is the reserved
// platform org, whose people exercise the product past the free plan's caps without
// a plan of their own. hanzo is NOT Hanzo's people alone: it is the signup org every
// consumer joins (account.SignupOrg), so ai_allowance_org_hanzo and
// ai_rate_org_hanzo hold EVERY consumer to their figure. Only a SuperAdmin sets a
// flag.
var staff = []string{authz.AdminOrg, account.SignupOrg}

// floor is the ceiling for a caller nobody could name: the switch, read through the
// one rule that keeps it a ceiling.
func (m gauge) floor(ctx context.Context) int64 {
	return m.ceiling(flagsplane.Int(ctx, m.publicKey(), m.strangers))
}

// ceiling turns a switch setting into a limit for an unnamed caller. It is TOTAL and
// never answers zero: an operator may tune the number in either direction, and a
// non-positive setting restores the shipped value rather than removing the bound. So
// there is no value of the switch, and no failure of the tier read, that ends in
// unlimited free inference for a stranger.
func (m gauge) ceiling(n int) int64 {
	if n > 0 {
		return int64(n)
	}
	return int64(m.strangers)
}
