package link

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/hanzoai/cloud"
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

// sampleView is one window instance on the wire. Unknown values are OMITTED rather
// than sent as zero, and `confidence` says whether the counters that remain mean
// anything — so a console renders "—" where the meter knew nothing, and never a
// fabricated 0.
type sampleView struct {
	Lane          string  `json:"lane"`
	Window        string  `json:"window"`
	WindowMinutes int32   `json:"windowMinutes,omitempty"`
	WindowStart   string  `json:"windowStart,omitempty"`
	ResetsAt      string  `json:"resetsAt,omitempty"`
	UsedPct       float64 `json:"usedPct"`
	Confidence    string  `json:"confidence"`
	Synthetic     bool    `json:"synthetic,omitempty"`

	Requests          int64 `json:"requests,omitempty"`
	InputTokens       int64 `json:"inputTokens,omitempty"`
	OutputTokens      int64 `json:"outputTokens,omitempty"`
	TotalTokens       int64 `json:"totalTokens,omitempty"`
	CachedInputTokens int64 `json:"cachedInputTokens,omitempty"`

	CostCents      int64  `json:"costCents,omitempty"`
	CostLimitCents int64  `json:"costLimitCents,omitempty"`
	Currency       string `json:"currency,omitempty"`

	Account string `json:"account,omitempty"`
	Plan    string `json:"plan,omitempty"`
	Machine string `json:"machine,omitempty"`
}

func toSampleView(x Sample) sampleView {
	return sampleView{
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
	Source     string  `json:"source"` // account | hanzo
	Scope      string  `json:"scope"`  // user | org
	Provider   string  `json:"provider"`
	Window     string  `json:"window,omitempty"`
	Requests   int64   `json:"requests,omitempty"`
	Tokens     int64   `json:"tokens,omitempty"`
	CostCents  int64   `json:"costCents,omitempty"`
	UsedPct    float64 `json:"usedPct,omitempty"`
	Confidence string  `json:"confidence"`
	Windows    int64   `json:"windows,omitempty"`
}

func toTotalView(t Total) totalView {
	return totalView{
		Source: t.Source, Scope: t.Scope, Provider: t.Provider, Window: t.Window,
		Requests: t.Requests, Tokens: t.Tokens, CostCents: t.CostCents,
		UsedPct: t.UsedPct, Confidence: t.Confidence, Windows: t.Windows,
	}
}

// ── ingest ───────────────────────────────────────────────────────────────────

// sampleReq is one reported sample.
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
type sampleReq struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Plan     string `json:"plan"`
	Kind     string `json:"kind"`
	Machine  string `json:"machine"`

	Lane          string `json:"lane"`
	Window        string `json:"window"`
	WindowMinutes int32  `json:"windowMinutes"`
	WindowStart   string `json:"windowStart"` // RFC3339; bounded
	ResetsAt      string `json:"resetsAt"`    // RFC3339; bounded

	UsedPct    float64 `json:"usedPct"`
	Confidence string  `json:"confidence"`
	Synthetic  bool    `json:"synthetic"`

	Requests          int64 `json:"requests"`
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	TotalTokens       int64 `json:"totalTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens"`

	CostCents      int64  `json:"costCents"`
	CostLimitCents int64  `json:"costLimitCents"`
	Currency       string `json:"currency"`
}

// reportReq accepts one sample or many: a poller reports its lanes in one call.
type reportReq struct {
	Samples []sampleReq `json:"samples"`
	sampleReq
}

// samplesOf flattens the one-or-many body into the batch.
func (r reportReq) samplesOf() []sampleReq {
	if len(r.Samples) > 0 {
		return r.Samples
	}
	if r.Provider == "" && r.Window == "" {
		return nil
	}
	return []sampleReq{r.sampleReq}
}

// parseSample validates a reported sample and turns it into a bounded value. The
// CLOSED vocabularies (provider presence, window, kind) are 400s, never silently
// rewritten — a caller whose window class we did not understand must be told, or
// their dash would quietly fill with a class they never reported. Everything else
// is clamped by Sanitize.
func parseSample(in sampleReq, now time.Time) (Sample, error) {
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

type reportResp struct {
	Accepted int        `json:"accepted"`
	Stored   bool       `json:"stored"` // false = warehouse unavailable; Links still updated
	Links    []linkView `json:"links"`
}

// reportUsage ingests a batch of samples: it refreshes the Link rows (the DURABLE
// truth the accounts overview renders) and appends the samples to the warehouse
// series (history).
//
// The order is deliberate. The Link upsert is the transaction that must not be
// lost; the warehouse write is fail-soft beside it. A datastore outage costs a poll
// of history and nothing else — the report still succeeds and the account still
// shows as live and current.
func reportUsage(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body reportReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	raw := body.samplesOf()
	if len(raw) == 0 {
		return zip.ErrBadRequest("at least one sample is required")
	}
	if len(raw) > maxSamples {
		return zip.ErrBadRequest(fmt.Sprintf("at most %d samples per report", maxSamples))
	}
	now := time.Now()
	samples := make([]Sample, 0, len(raw))
	for _, in := range raw {
		x, err := parseSample(in, now)
		if err != nil {
			return err
		}
		samples = append(samples, x)
	}

	// Refresh one Link per distinct account the batch reported. This is the
	// existing upsert with its existing identity — a report keeps the accounts
	// overview current without a second registration step.
	links := make([]linkView, 0, 4)
	for _, g := range groupByAccount(samples) {
		id, err := genID("link")
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}
		unix := now.Unix()
		stored, err := s.State.store.Upsert(c.Context(), Link{
			ID: id, Org: org, User: user, Machine: g.Machine, Provider: g.Provider,
			Account: g.Account, Plan: g.Plan, Kind: g.Kind, Status: StatusLinked,
			LastSeen: unix, Usage: g.Usage, CreatedAt: unix, UpdatedAt: unix,
		})
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
		links = append(links, toLinkView(stored))
	}

	// History is fail-soft: a warehouse outage must never fail a report or block a
	// device. `stored` tells the caller which happened — honestly.
	stored := true
	if err := s.State.store.WriteSamples(c.Context(), org, user, samples, now); err != nil {
		s.Log.Debug("account usage write skipped", "org", org, "err", err)
		stored = false
	}
	return c.JSON(http.StatusAccepted, reportResp{Accepted: len(samples), Stored: stored, Links: links})
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

type dashResp struct {
	Provider  string       `json:"provider"`
	Account   string       `json:"account,omitempty"`
	Range     string       `json:"range"`
	From      string       `json:"from"`
	To        string       `json:"to"`
	Source    string       `json:"source"`    // account — the provider's own meter
	Scope     string       `json:"scope"`     // user
	Available bool         `json:"available"` // false = warehouse unavailable, NOT "no usage"
	Current   []sampleView `json:"current"`   // the live state of each lane: the dash headline
	Windows   []sampleView `json:"windows"`   // every instance in range, newest first
}

// usageDash is the PER-PROVIDER view: one connected account's own consumption of
// its own plan — "my Claude Max plan is 47% through its 6h window, resets at 14:20".
//
// `current` is the newest instance of each lane (the headline); `windows` is the
// history behind it. Both are computed from ONE deduped read — no second query.
func usageDash(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	provider := trim(c.Query("provider"))
	if provider == "" {
		return zip.ErrBadRequest("provider is required")
	}
	if len(provider) > maxProvider {
		return zip.ErrBadRequest("provider too long")
	}
	acct := trim(c.Query("account"))
	if len(acct) > maxAccount {
		return zip.ErrBadRequest("account too long")
	}
	window := trim(c.Query("window"))
	if window != "" && !validWindow(window) {
		return zip.ErrBadRequest("window must be one of 6h, day, week, month")
	}
	rangeLabel := trim(c.Query("range"))
	from, to, err := resolveRange(rangeLabel, time.Now())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if rangeLabel == "" {
		rangeLabel = Range24h
	}
	out := dashResp{
		Provider: provider, Account: acct, Range: rangeLabel,
		From: rfc3339Of(from), To: rfc3339Of(to),
		Source: SourceAccount, Scope: ScopeUser,
		Current: []sampleView{}, Windows: []sampleView{},
	}
	rows, ok := s.State.store.Series(c.Context(), org, user, provider, acct, window, from, to)
	if !ok {
		return c.JSON(http.StatusOK, out) // Available=false: honest "unavailable"
	}
	out.Available = true
	for _, x := range rows {
		out.Windows = append(out.Windows, toSampleView(x))
	}
	for _, x := range currentOf(rows) {
		out.Current = append(out.Current, toSampleView(x))
	}
	return c.JSON(http.StatusOK, out)
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

type summaryResp struct {
	Range string      `json:"range"`
	From  string      `json:"from"`
	To    string      `json:"to"`
	Rows  []totalView `json:"rows"`
	// Per-source availability: a partial warehouse never fabricates the other half.
	Account sourceState `json:"account"`
	Hanzo   sourceState `json:"hanzo"`
}

type sourceState struct {
	Available bool   `json:"available"`
	Scope     string `json:"scope"`
	Source    string `json:"source"` // the table of record
	Note      string `json:"note"`
}

// usageSummary is the GLOBAL view: every provider account the caller connected,
// beside what the org routed through Hanzo — one board.
//
// The union is honest by construction:
//   - each row says its SOURCE (whose meter) and SCOPE (whose usage)
//   - the two are concatenated, NEVER added: a plan's percent is not money, and a
//     provider's own spend is not a Hanzo charge
//   - each side reports its own availability, so half a warehouse never turns the
//     other half into zeros
//   - both sides resolve the SAME [from, to) from ONE resolver, so the comparison
//     is over one period
func usageSummary(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rangeLabel := trim(c.Query("range"))
	from, to, err := resolveRange(rangeLabel, time.Now())
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if rangeLabel == "" {
		rangeLabel = Range24h
	}
	out := summaryResp{
		Range: rangeLabel, From: rfc3339Of(from), To: rfc3339Of(to), Rows: []totalView{},
		Account: sourceState{Scope: ScopeUser, Source: accountUsageTable,
			Note: "your own linked accounts, metered from each provider's own login; plan consumption, not a Hanzo charge"},
		Hanzo: sourceState{Scope: ScopeOrg, Source: cloudUsageTable,
			Note: "your org's Hanzo-routed inference; cost of record"},
	}
	if rows, ok := s.State.store.AccountTotals(c.Context(), org, user, from, to); ok {
		out.Account.Available = true
		for _, t := range rows {
			out.Rows = append(out.Rows, toTotalView(t))
		}
	}
	if rows, ok := s.State.store.HanzoTotals(c.Context(), org, from, to); ok {
		out.Hanzo.Available = true
		for _, t := range rows {
			out.Rows = append(out.Rows, toTotalView(t))
		}
	}
	return c.JSON(http.StatusOK, out)
}
