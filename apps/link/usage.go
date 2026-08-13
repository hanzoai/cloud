package link

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/hanzoai/cloud/internal/mint"

	"github.com/zap-proto/zip"
)

// usage.go mounts the ACCOUNT-USAGE surface under /v1/links/usage — the three views
// a developer who connected their own AI accounts asks for:
//
//	POST /v1/links/usage          report samples (the collector) -> {accepted, links}
//	GET  /v1/links/usage          ONE provider account's own dash -> {current, windows}
//	GET  /v1/links/usage/summary  the GLOBAL view: every account + Hanzo-routed
//
// The third view is the point of the plane: "my Claude Max plan" and "what I spend
// through Hanzo" on one board. They come from different ledgers with different
// meanings, so every row is LABELLED — by source (whose meter), by scope (whose
// usage), and by confidence (how real the numbers are) — and the two are never
// added together. A plan's percent is not money; a provider's own spend is not a
// Hanzo charge. The rows sit side by side and say what they are.
//
// Every route is org+subject scoped through the same caller() gate as the rest of
// the package: a validated principal and a non-empty org, else 403. A caller reads
// and writes only their OWN accounts. The org is NEVER a parameter.

// Ranges — the closed allowlist for a read window. ONE resolver serves both sides
// of the global view, so the account rows and the Hanzo rows always cover the SAME
// period; two resolvers could drift and turn the union into a lie.
const (
	Range1h  = "1h"
	Range24h = "24h"
	Range7d  = "7d"
	Range30d = "30d"
)

// resolveRange maps a range label to an absolute [from, to) window. Pure: `now` is
// injected. An unknown label is an error, never a silent default — a caller who
// asked for a window we do not have must be told, not shown a different one.
func resolveRange(label string, now time.Time) (from, to time.Time, err error) {
	now = now.UTC()
	switch trim(label) {
	case "", Range24h:
		return now.Add(-24 * time.Hour), now, nil
	case Range1h:
		return now.Add(-time.Hour), now, nil
	case Range7d:
		return now.Add(-7 * 24 * time.Hour), now, nil
	case Range30d:
		return now.Add(-30 * 24 * time.Hour), now, nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("range must be one of 1h, 24h, 7d, 30d")
}

// ── views ────────────────────────────────────────────────────────────────────

// readingView is one window instance on the wire. Unknown values are OMITTED rather
// than sent as zero, and `confidence` says whether the counters that remain mean
// anything — so a console renders "—" where the meter knew nothing, and never a
// fabricated 0.
type readingView struct {
	// Lane names the meter's own lane label for this measurement.
	Lane string `json:"lane"`
	// Window is the window class: 6h, day, week or month.
	Window string `json:"window"`
	// WindowMinutes is the window's length as the meter reported it.
	WindowMinutes int32 `json:"windowMinutes,omitempty"`
	// WindowStart is when the measured window opened, RFC 3339 UTC.
	WindowStart string `json:"windowStart,omitempty"`
	// ResetsAt is when the window resets, RFC 3339 UTC.
	ResetsAt string `json:"resetsAt,omitempty"`
	// UsedPct is how much of the window's allowance is consumed, 0..100.
	UsedPct float64 `json:"usedPct"`
	// Confidence says whether the counters that remain mean anything, as the
	// meter graded itself.
	Confidence string `json:"confidence"`
	// Synthetic marks a sample the collector derived rather than observed.
	Synthetic bool `json:"synthetic,omitempty"`

	// Requests is the window's request count.
	Requests int64 `json:"requests,omitempty"`
	// InputTokens is the window's prompt-token count.
	InputTokens int64 `json:"inputTokens,omitempty"`
	// OutputTokens is the window's completion-token count.
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// TotalTokens is the window's total token count.
	TotalTokens int64 `json:"totalTokens,omitempty"`
	// CachedInputTokens is the window's cached-prompt-token count.
	CachedInputTokens int64 `json:"cachedInputTokens,omitempty"`

	// CostCents is the window's spend in cents, as the provider's meter states it.
	CostCents int64 `json:"costCents,omitempty"`
	// CostLimitCents is the window's spend cap in cents, when the meter knows one.
	CostLimitCents int64 `json:"costLimitCents,omitempty"`
	// Currency is the ISO currency the cost fields are stated in.
	Currency string `json:"currency,omitempty"`

	// Account is the provider-side account the sample belongs to.
	Account string `json:"account,omitempty"`
	// Plan is the provider plan label the account is on.
	Plan string `json:"plan,omitempty"`
	// Machine is the machine the collector observed the account on.
	Machine string `json:"machine,omitempty"`
}

func toSampleView(x Sample) readingView {
	return readingView{
		Lane: x.Lane, Window: x.Window, WindowMinutes: x.WindowMinutes,
		WindowStart: rfc3339Of(x.WindowStart), ResetsAt: rfc3339Of(x.ResetsAt),
		UsedPct: x.UsedPct, Confidence: x.Confidence, Synthetic: x.Synthetic,
		Requests: x.Requests, InputTokens: x.InputTokens, OutputTokens: x.OutputTokens,
		TotalTokens: x.TotalTokens, CachedInputTokens: x.CachedInputTokens,
		CostCents: x.CostCents, CostLimitCents: x.CostLimitCents, Currency: x.Currency,
		Account: x.Account, Plan: x.Plan, Machine: x.Machine,
	}
}

func rfc3339Of(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// totalView is one row of the global view. Source and scope are what keep the board
// honest — see the Source/Scope const blocks.
type totalView struct {
	// Source is whose meter the row came from: account or hanzo.
	Source string `json:"source"`
	// Scope is whose usage the row measures: user or org.
	Scope string `json:"scope"`
	// Provider is the provider the row totals.
	Provider string `json:"provider"`
	// Window is the window class the row totals, on the account side.
	Window string `json:"window,omitempty"`
	// Requests is the period's request count.
	Requests int64 `json:"requests,omitempty"`
	// Tokens is the period's total token count.
	Tokens int64 `json:"tokens,omitempty"`
	// CostCents is the period's spend in cents, in the row's own ledger.
	CostCents int64 `json:"costCents,omitempty"`
	// UsedPct is the plan consumption percentage, on the account side.
	UsedPct float64 `json:"usedPct,omitempty"`
	// Confidence says how real the row's numbers are.
	Confidence string `json:"confidence"`
	// Windows is how many window instances the row folds.
	Windows int64 `json:"windows,omitempty"`
}

func toTotalView(t Total) totalView {
	return totalView{
		Source: t.Source, Scope: t.Scope, Provider: t.Provider, Window: t.Window,
		Requests: t.Requests, Tokens: t.Tokens, CostCents: t.CostCents,
		UsedPct: t.UsedPct, Confidence: t.Confidence, Windows: t.Windows,
	}
}

// ── ingest ───────────────────────────────────────────────────────────────────

// readingReq is one reported sample.
//
// THERE IS NO `ts`. The server owns the observation clock, always — and that is a
// security property, not a convenience. `ts` is the version that decides which read
// of a window wins; a client that could set it could pin a stale or flattering
// snapshot as newest forever, and no later truthful poll would ever overwrite it.
//
// The historical-window case does not need one. A sample says WHICH window it
// measures with `windowStart` (or `resetsAt` + `windowMinutes`, which the meter
// reports anyway) — all bounded to a sane interval around now. That separates two
// clocks a single `ts` would braid together: WHEN THE WINDOW WAS (the client's fact
// to state) and WHEN WE LEARNED IT (ours). A backfill of a real historical window
// lands at the right instant with an honest observation time.
type readingReq struct {
	// Provider is the AI provider whose meter reported this sample. Required.
	Provider string `json:"provider"`
	// Account is the provider-side account the sample belongs to.
	Account string `json:"account"`
	// Plan is the provider plan label the account is on.
	Plan string `json:"plan"`
	// Kind is subscription or apikey; anything else is refused.
	Kind string `json:"kind"`
	// Machine is the machine the collector observed the account on. Required.
	Machine string `json:"machine"`

	// Lane names the meter's own lane label for this measurement.
	Lane string `json:"lane"`
	// Window is the window class, one of 6h, day, week, month; anything else is
	// refused rather than silently reclassified.
	Window string `json:"window"`
	// WindowMinutes is the window's length as the meter reported it.
	WindowMinutes int32 `json:"windowMinutes"`
	// WindowStart is when the measured window opened, RFC 3339, bounded to a
	// sane interval around now.
	WindowStart string `json:"windowStart"`
	// ResetsAt is when the window resets, RFC 3339, bounded.
	ResetsAt string `json:"resetsAt"`

	// UsedPct is how much of the window's allowance is consumed, clamped 0..100.
	UsedPct float64 `json:"usedPct"`
	// Confidence says how real the counters are, as the meter graded itself.
	Confidence string `json:"confidence"`
	// Synthetic marks a sample the collector derived rather than observed.
	Synthetic bool `json:"synthetic"`

	// Requests is the window's request count.
	Requests int64 `json:"requests"`
	// InputTokens is the window's prompt-token count.
	InputTokens int64 `json:"inputTokens"`
	// OutputTokens is the window's completion-token count.
	OutputTokens int64 `json:"outputTokens"`
	// TotalTokens is the window's total token count.
	TotalTokens int64 `json:"totalTokens"`
	// CachedInputTokens is the window's cached-prompt-token count.
	CachedInputTokens int64 `json:"cachedInputTokens"`

	// CostCents is the window's spend in cents, as the provider's meter states it.
	CostCents int64 `json:"costCents"`
	// CostLimitCents is the window's spend cap in cents, when the meter knows one.
	CostLimitCents int64 `json:"costLimitCents"`
	// Currency is the ISO currency the cost fields are stated in.
	Currency string `json:"currency"`
}

// ingestReq accepts one sample or many: a poller reports its lanes in one call.
type ingestReq struct {
	// Samples is the batch form, up to 256 samples; leave it empty to send one
	// sample inline on the same fields.
	Samples []readingReq `json:"samples"`
	readingReq
}

// samplesOf flattens the one-or-many body into the batch.
func (r ingestReq) samplesOf() []readingReq {
	if len(r.Samples) > 0 {
		return r.Samples
	}
	if r.Provider == "" && r.Window == "" {
		return nil
	}
	return []readingReq{r.readingReq}
}

// parseSample validates a reported sample and turns it into a bounded value. The
// CLOSED vocabularies (provider presence, window, kind) are 400s, never silently
// rewritten — a caller whose window class we did not understand must be told, or
// their dash would quietly fill with a class they never reported. Everything else
// is clamped by Sanitize.
func parseSample(in readingReq, now time.Time) (Sample, error) {
	if trim(in.Provider) == "" {
		return Sample{}, zip.ErrBadRequest("provider is required")
	}
	if trim(in.Machine) == "" {
		return Sample{}, zip.ErrBadRequest("machine is required")
	}
	if !validWindow(trim(in.Window)) {
		return Sample{}, zip.ErrBadRequest("window must be one of 6h, day, week, month")
	}
	if k := trim(in.Kind); k != "" && !validKind(k) {
		return Sample{}, zip.ErrBadRequest("kind must be subscription or apikey")
	}
	ws, err := parseInstant(in.WindowStart)
	if err != nil {
		return Sample{}, zip.ErrBadRequest("windowStart must be RFC3339")
	}
	ra, err := parseInstant(in.ResetsAt)
	if err != nil {
		return Sample{}, zip.ErrBadRequest("resetsAt must be RFC3339")
	}
	s := Sample{
		Provider: in.Provider, Account: in.Account, Plan: in.Plan, Kind: in.Kind,
		Machine: in.Machine, Lane: in.Lane, Window: in.Window,
		WindowMinutes: in.WindowMinutes, WindowStart: ws, ResetsAt: ra,
		UsedPct: in.UsedPct, Confidence: in.Confidence, Synthetic: in.Synthetic,
		Requests: in.Requests, InputTokens: in.InputTokens, OutputTokens: in.OutputTokens,
		TotalTokens: in.TotalTokens, CachedInputTokens: in.CachedInputTokens,
		CostCents: in.CostCents, CostLimitCents: in.CostLimitCents, Currency: in.Currency,
	}
	return s.Sanitize(now), nil
}

func parseInstant(s string) (time.Time, error) {
	if trim(s) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, trim(s))
}

// ingestResp is the collector's receipt for one report.
type ingestResp struct {
	// Accepted is how many samples this report landed.
	Accepted int `json:"accepted"`
	// Stored reports whether history was durably written; false means the
	// warehouse was unavailable and only the link rows were refreshed.
	Stored bool `json:"stored"`
	// Links is the link row each distinct (machine, provider, account) in the
	// batch refreshed.
	Links []linkView `json:"links"`
}

// ReportUsage reports usage samples from the device collector.
//
// It ingests a batch of usage samples and answers with how many were accepted,
// whether history was durably stored, and the links they refreshed. A report
// also REFRESHES one link per distinct (machine, provider, account) it names, so
// a running collector keeps the accounts overview current without a separate
// registration call.
//
// A caller can only ever report for THEMSELVES: org and subject come from the
// validated bearer, never from the body, so no sample can be attributed to
// another user or tenant. History is FAIL-SOFT and stored says which happened —
// a warehouse outage still accepts the report and refreshes the links rather
// than failing the device, and answers 202 either way. Send either one sample
// inline or up to 256 in samples; an empty batch or an over-long one is 400, as
// is a provider, window class or kind outside the closed vocabulary — an
// unrecognized window is refused rather than rewritten, because a silently
// reclassified sample would fill a dashboard with a class nobody reported.
func (o ops) reportUsage(ctx context.Context, body *ingestReq) (*ingestResp, error) {
	org, user, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	raw := body.samplesOf()
	if len(raw) == 0 {
		return nil, zip.ErrBadRequest("at least one sample is required")
	}
	if len(raw) > maxSamples {
		return nil, zip.ErrBadRequest(fmt.Sprintf("at most %d samples per report", maxSamples))
	}
	now := time.Now()
	samples := make([]Sample, 0, len(raw))
	for _, in := range raw {
		x, err := parseSample(in, now)
		if err != nil {
			return nil, err
		}
		samples = append(samples, x)
	}

	// Refresh one Link per distinct account the batch reported. This is the
	// existing upsert with its existing identity — a report keeps the accounts
	// overview current without a second registration step.
	links := make([]linkView, 0, 4)
	for _, g := range groupByAccount(samples) {
		id := mint.ID("link")
		unix := now.Unix()
		stored, err := o.s.State.store.Upsert(ctx, Link{
			ID: id, Org: org, User: user, Machine: g.Machine, Provider: g.Provider,
			Account: g.Account, Plan: g.Plan, Kind: g.Kind, Status: StatusLinked,
			LastSeen: unix, Usage: g.Usage, CreatedAt: unix, UpdatedAt: unix,
		})
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		links = append(links, toLinkView(stored))
	}

	// History is fail-soft: a warehouse outage must never fail a report or block a
	// device. `stored` tells the caller which happened — honestly.
	stored := true
	if err := o.s.State.store.WriteSamples(ctx, org, user, samples, now); err != nil {
		o.s.Log.Debug("account usage write skipped", "org", org, "err", err)
		stored = false
	}
	return &ingestResp{Accepted: len(samples), Stored: stored, Links: links}, nil
}

// account is one account's slice of a reported batch, folded into the shape the
// Link row carries.
type account struct {
	Machine, Provider, Account, Plan, Kind, Usage string
}

// groupByAccount folds a batch into one Link refresh per (machine, provider,
// account) — the Link's own identity — with a usage snapshot projected from that
// account's lanes. Deterministic order, so a report is reproducible.
func groupByAccount(samples []Sample) []account {
	type key struct{ machine, provider, acct string }
	order := make([]key, 0, 4)
	byKey := map[key][]Sample{}
	for _, x := range samples {
		k := key{x.Machine, x.Provider, x.Account}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], x)
	}
	out := make([]account, 0, len(order))
	for _, k := range order {
		group := byKey[k]
		a := account{Machine: k.machine, Provider: k.provider, Account: k.acct, Kind: KindSubscription}
		// The plan/kind of the freshest lane describes the account.
		newest := newestOf(group)
		a.Plan, a.Kind = newest.Plan, newest.Kind
		a.Usage = snapshotOf(group)
		out = append(out, a)
	}
	return out
}

// newestOf returns the sample measuring the most recent window instance.
func newestOf(group []Sample) Sample {
	best := group[0]
	for _, x := range group[1:] {
		if x.WindowStart.After(best.WindowStart) {
			best = x
		}
	}
	return best
}

// windowRank orders the window classes by breadth, so a projection can pick the
// WIDEST lane a meter reported rather than mixing lanes.
func windowRank(w string) int {
	switch w {
	case Window6h:
		return 1
	case WindowDay:
		return 2
	case WindowWeek:
		return 3
	case WindowMonth:
		return 4
	}
	return 0
}

// snapshotOf projects an account's lanes into the Usage snapshot the accounts
// overview renders and the route policy reads for headroom. Pure.
//
// It NEVER sums across window classes: they NEST (a 6h lane's consumption is also
// inside the week lane's), so adding them would count the same work twice. The
// percents come from their own lanes — 6h drives SessionPct, week drives WeeklyPct,
// which is exactly what headroomPct compares — and the absolute counters come from
// the WIDEST single lane reported, carrying that same lane's confidence with them,
// so a number and the flag that qualifies it always come from ONE meter.
func snapshotOf(group []Sample) string {
	var u Usage
	var widest Sample
	// Percents: the freshest instance of each lane class.
	if s, ok := freshest(group, Window6h); ok {
		u.SessionPct = clampPct(s.UsedPct)
		u.ResetsAt = rfc3339Of(s.ResetsAt)
	}
	if w, ok := freshest(group, WindowWeek); ok {
		u.WeeklyPct = clampPct(w.UsedPct)
		if u.ResetsAt == "" {
			u.ResetsAt = rfc3339Of(w.ResetsAt)
		}
	}
	// Counters + money: the widest single lane, never a mix.
	for _, x := range group {
		if windowRank(x.Window) > windowRank(widest.Window) {
			widest = x
		}
	}
	u.Tokens = widest.TotalTokens
	u.InputTokens = widest.InputTokens
	u.OutputTokens = widest.OutputTokens
	u.SpendCents = widest.CostCents
	u.Currency = widest.Currency
	u.Confidence = widest.Confidence
	u.UpdatedAt = rfc3339Of(time.Now())
	b, err := json.Marshal(u)
	if err != nil {
		return ""
	}
	return string(b)
}

// freshest returns the most recent instance of one window class, if the batch has one.
func freshest(group []Sample, window string) (Sample, bool) {
	var best Sample
	found := false
	for _, x := range group {
		if x.Window != window {
			continue
		}
		if !found || x.WindowStart.After(best.WindowStart) {
			best, found = x, true
		}
	}
	return best, found
}

// ── reads ────────────────────────────────────────────────────────────────────

// dashIn selects one provider account's own series. All four values ride the
// query string.
type dashIn struct {
	// Provider is the provider whose meter to read. Required.
	Provider string `json:"provider"`
	// Account narrows to one account when a user has several with the provider.
	Account string `json:"account"`
	// Window selects a window class: 6h, day, week or month. Empty reads all.
	Window string `json:"window"`
	// Range is the period, one of 1h, 24h, 7d or 30d; empty means 24h, and an
	// unknown label is 400, never a quiet fallback.
	Range string `json:"range"`
}

// boardResp is one provider account's own usage series over one window.
type boardResp struct {
	// Provider is the provider whose meter answered.
	Provider string `json:"provider"`
	// Account is the account the series narrows to, when one was named.
	Account string `json:"account,omitempty"`
	// Range is the resolved period label.
	Range string `json:"range"`
	// From and To are the resolved [from, to) window, RFC 3339 UTC.
	From string `json:"from"`
	To   string `json:"to"`
	// Source is always "account": the provider's own meter, not a Hanzo charge.
	Source string `json:"source"`
	// Scope is always "user": the caller's own linked accounts.
	Scope string `json:"scope"`
	// Available reports whether the warehouse answered; false is an honest "we
	// have no data", NOT zero usage.
	Available bool `json:"available"`
	// Current is the live state of each lane — the dash headline.
	Current []readingView `json:"current"`
	// Windows is every window instance in range, newest first.
	Windows []readingView `json:"windows"`
}

// UsageDash shows one provider account's own usage dashboard.
//
// It answers the time series for a SINGLE provider account — the windows in
// range plus the currently-open ones — as that provider's own meter reported it:
// "my plan is 47% through its 6h window, resets at 14:20". current is the newest
// instance of each lane (the headline); windows is the history behind it, both
// computed from ONE deduped read. provider is required; an unknown window class
// or range is 400, never a quiet fallback to a different one. When no series is
// available the response is a 200 with available:false and empty lists — an
// honest "we have no data", which is a different claim from zero usage.
func (o ops) usageDash(ctx context.Context, in *dashIn) (*boardResp, error) {
	org, user, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	provider := trim(in.Provider)
	if provider == "" {
		return nil, zip.ErrBadRequest("provider is required")
	}
	if len(provider) > maxProvider {
		return nil, zip.ErrBadRequest("provider too long")
	}
	acct := trim(in.Account)
	if len(acct) > maxAccount {
		return nil, zip.ErrBadRequest("account too long")
	}
	window := trim(in.Window)
	if window != "" && !validWindow(window) {
		return nil, zip.ErrBadRequest("window must be one of 6h, day, week, month")
	}
	rangeLabel := trim(in.Range)
	from, to, err := resolveRange(rangeLabel, time.Now())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if rangeLabel == "" {
		rangeLabel = Range24h
	}
	out := &boardResp{
		Provider: provider, Account: acct, Range: rangeLabel,
		From: rfc3339Of(from), To: rfc3339Of(to),
		Source: SourceAccount, Scope: ScopeUser,
		Current: []readingView{}, Windows: []readingView{},
	}
	rows, ok := o.s.State.store.Series(ctx, org, user, provider, acct, window, from, to)
	if !ok {
		return out, nil // Available=false: honest "unavailable"
	}
	out.Available = true
	for _, x := range rows {
		out.Windows = append(out.Windows, toSampleView(x))
	}
	for _, x := range currentOf(rows) {
		out.Current = append(out.Current, toSampleView(x))
	}
	return out, nil
}

// currentOf picks the newest instance of each lane — the live state. Rows arrive
// newest-first, so the first sighting of a lane is its current instance.
func currentOf(rows []Sample) []Sample {
	seen := map[string]bool{}
	out := make([]Sample, 0, 4)
	for _, x := range rows {
		k := x.Account + "\x00" + x.Lane
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return windowRank(out[i].Window) < windowRank(out[j].Window)
	})
	return out
}

// summaryIn selects the summary's period. It rides the query string.
type summaryIn struct {
	// Range is the period, one of 1h, 24h, 7d or 30d; empty means 24h, and an
	// unknown label is 400, never a silent substitution.
	Range string `json:"range"`
}

// summaryResp is the global usage board over one window.
type summaryResp struct {
	// Range is the resolved period label.
	Range string `json:"range"`
	// From and To are the one [from, to) window BOTH halves resolved, RFC 3339 UTC.
	From string `json:"from"`
	To   string `json:"to"`
	// Rows is the union of both ledgers, each row labelled by source and scope —
	// concatenated, NEVER summed: a plan's percentage is not money.
	Rows []totalView `json:"rows"`
	// Account and Hanzo report each ledger's own availability, so a partial
	// warehouse never fabricates the other half.
	Account sourceState `json:"account"`
	Hanzo   sourceState `json:"hanzo"`
}

// sourceState is one ledger's own account of itself.
type sourceState struct {
	// Available reports whether this ledger answered; false is honest
	// "unavailable", never a zero that would read as no usage.
	Available bool `json:"available"`
	// Scope is whose usage the ledger measures: user or org.
	Scope string `json:"scope"`
	// Source is the table of record behind the ledger.
	Source string `json:"source"`
	// Note says in prose what the ledger's numbers mean.
	Note string `json:"note"`
}

// UsageSummary shows plan consumption and Hanzo spend side by side.
//
// It answers the global usage board over one window: the caller's own linked
// accounts, metered from each provider's own login, alongside their org's
// Hanzo-routed inference. These come from different ledgers and mean different
// things, so every row is LABELLED by source, by scope and by availability, and
// THE TWO ARE NEVER SUMMED — a plan's percentage is not money, and a provider's
// own spend is not a Hanzo charge. The rows sit side by side and say what they
// are.
//
// One resolver fixes the window for both halves, so the two sets always cover
// the same period. range is one of 1h, 24h, 7d or 30d and defaults to 24h;
// anything else is 400 rather than a silent substitution. A ledger that cannot
// answer reports available:false instead of a zero that would read as "no usage".
func (o ops) usageSummary(ctx context.Context, in *summaryIn) (*summaryResp, error) {
	org, user, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	rangeLabel := trim(in.Range)
	from, to, err := resolveRange(rangeLabel, time.Now())
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	if rangeLabel == "" {
		rangeLabel = Range24h
	}
	out := &summaryResp{
		Range: rangeLabel, From: rfc3339Of(from), To: rfc3339Of(to), Rows: []totalView{},
		Account: sourceState{Scope: ScopeUser, Source: accountUsageTable,
			Note: "your own linked accounts, metered from each provider's own login; plan consumption, not a Hanzo charge"},
		Hanzo: sourceState{Scope: ScopeOrg, Source: cloudUsageTable,
			Note: "your org's Hanzo-routed inference; cost of record"},
	}
	if rows, ok := o.s.State.store.AccountTotals(ctx, org, user, from, to); ok {
		out.Account.Available = true
		for _, t := range rows {
			out.Rows = append(out.Rows, toTotalView(t))
		}
	}
	if rows, ok := o.s.State.store.HanzoTotals(ctx, org, from, to); ok {
		out.Hanzo.Available = true
		for _, t := range rows {
			out.Rows = append(out.Rows, toTotalView(t))
		}
	}
	return out, nil
}
