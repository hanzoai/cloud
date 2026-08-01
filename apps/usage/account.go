package usage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// account.go is the ACCOUNT-USAGE HTTP layer: the collector's record endpoint and
// the per-provider sample dash. It moved here from clients/link's usage plane so the
// ONE usage subsystem owns ALL usage. It records what a developer's OWN AI accounts
// have consumed of their OWN plans (metered from each provider's own login) into the
// warehouse series (datastore.go); the READ faces are summary (usage.go), this dash
// (samples), and analytics.
//
//	POST /v1/usage          report samples (the collector) -> {accepted, stored}
//	GET  /v1/usage/samples  ONE provider account's own dash -> {current, windows}
//
// It records usage and NOTHING else: keeping the link REGISTRY current (which
// accounts are signed in, their latest snapshot) is clients/link's own concern,
// refreshed by POST /v1/links. Recording a sample and registering a link are two
// orthogonal operations, each with exactly one home — a usage report no longer
// writes a Link row, so there is one and only one way to set an account's snapshot.
//
// Every route is org+subject scoped through the same caller() gate as summary: a
// validated principal and a non-empty org, else 401. A caller reads and writes only
// their OWN accounts. The org is NEVER a parameter.

// caller resolves the (org, subject) scope for a request: the VALIDATED IAM owner
// claim (principal.Org — the trusted minted X-Org-Id, never a client header) plus
// c.User() (the owning subject, non-empty once principal.Org returns ok, since Org
// composes Validated). Every account-usage op and the summary's account block gate
// on it — an off-gateway forge with no validated user is refused fail-closed.
//
// It is the ONE identity seam in this package, and it reaches the request because
// the SUBJECT is not the org: principal.OrgFrom carries the tenant and nothing
// else, while the account board is scoped to the caller's OWN linked accounts and
// therefore needs the validated user id too. Off the HTTP path there is no request
// and no principal, so it fails closed and every op refuses — the handler's own
// gate, with no second gate to keep in sync.
func caller(ctx context.Context) (org, user string, ok bool) {
	c, has := cloud.Request(ctx)
	if !has {
		return "", "", false
	}
	org, ok = principal.Org(c)
	if !ok {
		return "", "", false
	}
	return org, trim(c.User()), true
}

// orgOf is caller() for the reads that need the tenant and nothing else. It goes
// through principal.OrgFrom — the org Bridge parked — so the tenant never depends
// on reaching the request.
func orgOf(ctx context.Context) (string, bool) { return principal.OrgFrom(ctx) }

// noStore marks a per-tenant money response uncacheable by the browser and by any
// intermediary. It is a RESPONSE header, which a typed op's signature drops, so it
// is written on the request Bridge parked; off the HTTP path there is no response
// to mark and nothing to do.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// Ranges — the closed allowlist for a sample-dash read window. This is the
// FINE-GRAINED account-usage grammar (a live lane dash cares about the 1h/6h
// scale); the coarser cost/analytics grammar (aiobject.ResolveCloudUsageWindow:
// 24h/7d/30d/custom + a bucket interval) drives summary and analytics. Two lanes,
// two grammars, each complete for its read.
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

// usageWindowView is one window instance on the wire. Unknown values are OMITTED
// rather than sent as zero, and `confidence` says whether the counters that remain
// mean anything — so a console renders "—" where the meter knew nothing, and never
// a fabricated 0.
type usageWindowView struct {
	// Lane is the meter lane this instance belongs to, e.g. a provider's own
	// rolling-window meter.
	Lane string `json:"lane"`
	// Window is the window class: 6h, day, week or month.
	Window string `json:"window"`
	// WindowMinutes is the window's real length in minutes when the meter
	// reported one; omitted when it did not.
	WindowMinutes int32 `json:"windowMinutes,omitempty"`
	// WindowStart is when this window opened, RFC3339 UTC; omitted when unknown.
	WindowStart string `json:"windowStart,omitempty"`
	// ResetsAt is when this window rolls over, RFC3339 UTC; omitted when unknown.
	ResetsAt string `json:"resetsAt,omitempty"`
	// UsedPct is how much of the window's allowance is consumed, 0–100.
	UsedPct float64 `json:"usedPct"`
	// Confidence says how much the counters beside it mean — a meter that
	// reported only a percentage leaves them at zero, and this is how a reader
	// tells that from a true zero.
	Confidence string `json:"confidence"`
	// Synthetic marks an instance the meter inferred rather than read.
	Synthetic bool `json:"synthetic,omitempty"`

	// Requests is how many requests were made in the window; omitted when the
	// meter did not report it.
	Requests int64 `json:"requests,omitempty"`
	// InputTokens is prompt tokens consumed in the window; omitted when unknown.
	InputTokens int64 `json:"inputTokens,omitempty"`
	// OutputTokens is completion tokens produced in the window; omitted when
	// unknown.
	OutputTokens int64 `json:"outputTokens,omitempty"`
	// TotalTokens is the window's total tokens; omitted when unknown.
	TotalTokens int64 `json:"totalTokens,omitempty"`
	// CachedInputTokens is the prompt tokens served from the provider's cache;
	// omitted when unknown.
	CachedInputTokens int64 `json:"cachedInputTokens,omitempty"`

	// CostCents is what the window cost on the PROVIDER's own plan, in US cents.
	// It is not a Hanzo charge.
	CostCents int64 `json:"costCents,omitempty"`
	// CostLimitCents is the plan's spend ceiling for the window, in US cents.
	CostLimitCents int64 `json:"costLimitCents,omitempty"`
	// Currency is the provider's currency when it is not US cents.
	Currency string `json:"currency,omitempty"`

	// Account is the linked provider account the window belongs to.
	Account string `json:"account,omitempty"`
	// Plan is the subscription plan the account is on, as the provider names it.
	Plan string `json:"plan,omitempty"`
	// Machine is the host whose meter reported the window.
	Machine string `json:"machine,omitempty"`
}

func toSampleView(x Sample) usageWindowView {
	return usageWindowView{
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

// TotalView is one row of the account-usage board (the summary's `accounts.rows`).
// Source and scope are what keep the board honest — see the Source/Scope consts.
type TotalView struct {
	// Source is where the row came from: "account" is the provider's own meter
	// on the caller's linked account, "hanzo" is Hanzo-routed inference. The two
	// are never summed.
	Source string `json:"source"`
	// Scope is whose row it is: "user" for the caller's own linked accounts,
	// "org" for the whole tenant's Hanzo-routed usage.
	Scope string `json:"scope"`
	// Provider is the upstream the usage was measured against.
	Provider string `json:"provider"`
	// Window is the meter window class the row rolls up, when it has one.
	Window string `json:"window,omitempty"`
	// Requests is how many requests the row covers.
	Requests int64 `json:"requests,omitempty"`
	// Tokens is the total tokens the row covers.
	Tokens int64 `json:"tokens,omitempty"`
	// CostCents is the row's cost in US cents. For an "account" row this is the
	// PROVIDER's own charge, not a Hanzo one.
	CostCents int64 `json:"costCents,omitempty"`
	// UsedPct is how much of a plan window the row consumed, 0–100. It is a
	// share, never money.
	UsedPct float64 `json:"usedPct,omitempty"`
	// Confidence says how much the counters mean; a percentage-only meter leaves
	// them at zero.
	Confidence string `json:"confidence"`
	// Windows is how many window instances rolled up into the row.
	Windows int64 `json:"windows,omitempty"`
}

func toTotalView(t Total) TotalView {
	return TotalView{
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
	// Provider is the upstream the account belongs to, e.g. anthropic. Required.
	Provider string `json:"provider"`
	// Account is the linked account the window was metered from.
	Account string `json:"account"`
	// Plan is the subscription plan the account is on, as the provider names it.
	Plan string `json:"plan"`
	// Kind is subscription or apikey. Empty is accepted; anything else is
	// refused.
	Kind string `json:"kind"`
	// Machine is the host whose meter read the window. Required.
	Machine string `json:"machine"`

	// Lane is the meter lane within the account.
	Lane string `json:"lane"`
	// Window is the window class: 6h, day, week or month. Required, and a class
	// this surface does not know is refused rather than rewritten.
	Window string `json:"window"`
	// WindowMinutes is the window's real length in minutes, as the meter reports
	// it.
	WindowMinutes int32 `json:"windowMinutes"`
	// WindowStart is when the measured window opened, RFC3339. Empty is allowed;
	// anything else that is not RFC3339 is refused.
	WindowStart string `json:"windowStart"`
	// ResetsAt is when the measured window rolls over, RFC3339. Empty is
	// allowed; anything else that is not RFC3339 is refused.
	ResetsAt string `json:"resetsAt"`

	// UsedPct is how much of the window's allowance is consumed, 0–100.
	UsedPct float64 `json:"usedPct"`
	// Confidence says how much the counters below mean.
	Confidence string `json:"confidence"`
	// Synthetic marks a window the meter inferred rather than read.
	Synthetic bool `json:"synthetic"`

	// Requests is how many requests the window covers.
	Requests int64 `json:"requests"`
	// InputTokens is prompt tokens consumed in the window.
	InputTokens int64 `json:"inputTokens"`
	// OutputTokens is completion tokens produced in the window.
	OutputTokens int64 `json:"outputTokens"`
	// TotalTokens is the window's total tokens.
	TotalTokens int64 `json:"totalTokens"`
	// CachedInputTokens is the prompt tokens the provider served from cache.
	CachedInputTokens int64 `json:"cachedInputTokens"`

	// CostCents is what the window cost on the PROVIDER's own plan, in US cents.
	CostCents int64 `json:"costCents"`
	// CostLimitCents is the plan's spend ceiling for the window, in US cents.
	CostLimitCents int64 `json:"costLimitCents"`
	// Currency is the provider's currency when it is not US cents.
	Currency string `json:"currency"`
}

// reportReq accepts one sample or many: a poller reports its lanes in one call.
// EITHER send `samples` with a batch, OR send one sample's fields at the top level
// — the two shapes are the same wire the collector has always had.
//
// The single-sample fields are spelled out rather than embedded. encoding/json
// PROMOTES an embedded struct's fields, so the wire would be identical either way,
// but zip's schema walk skips an embedded unexported type — which would publish a
// body of `samples` alone and document none of the twenty fields a single-sample
// report actually sends.
type reportReq struct {
	// Samples is the batch form: every lane a poller measured, in one call. When
	// it is non-empty the top-level sample fields are ignored.
	Samples []sampleReq `json:"samples"`

	// Provider is the upstream the account belongs to, e.g. anthropic. Required
	// on every sample.
	Provider string `json:"provider"`
	// Account is the linked account the window was metered from.
	Account string `json:"account"`
	// Plan is the subscription plan the account is on, as the provider names it.
	Plan string `json:"plan"`
	// Kind is subscription or apikey. Empty is accepted; anything else is
	// refused.
	Kind string `json:"kind"`
	// Machine is the host whose meter read the window. Required on every sample.
	Machine string `json:"machine"`

	// Lane is the meter lane within the account.
	Lane string `json:"lane"`
	// Window is the window class: 6h, day, week or month. Required, and a class
	// this surface does not know is refused rather than rewritten.
	Window string `json:"window"`
	// WindowMinutes is the window's real length in minutes, as the meter reports
	// it.
	WindowMinutes int32 `json:"windowMinutes"`
	// WindowStart is when the measured window opened, RFC3339. This is how a
	// backfill states WHICH window it measured; the server always owns the
	// observation clock, so there is no timestamp field.
	WindowStart string `json:"windowStart"`
	// ResetsAt is when the measured window rolls over, RFC3339.
	ResetsAt string `json:"resetsAt"`

	// UsedPct is how much of the window's allowance is consumed, 0–100.
	UsedPct float64 `json:"usedPct"`
	// Confidence says how much the counters below mean.
	Confidence string `json:"confidence"`
	// Synthetic marks a window the meter inferred rather than read.
	Synthetic bool `json:"synthetic"`

	// Requests is how many requests the window covers.
	Requests int64 `json:"requests"`
	// InputTokens is prompt tokens consumed in the window.
	InputTokens int64 `json:"inputTokens"`
	// OutputTokens is completion tokens produced in the window.
	OutputTokens int64 `json:"outputTokens"`
	// TotalTokens is the window's total tokens.
	TotalTokens int64 `json:"totalTokens"`
	// CachedInputTokens is the prompt tokens the provider served from cache.
	CachedInputTokens int64 `json:"cachedInputTokens"`

	// CostCents is what the window cost on the PROVIDER's own plan, in US cents.
	CostCents int64 `json:"costCents"`
	// CostLimitCents is the plan's spend ceiling for the window, in US cents.
	CostLimitCents int64 `json:"costLimitCents"`
	// Currency is the provider's currency when it is not US cents.
	Currency string `json:"currency"`
}

// single projects the top-level fields back into the one-sample form, so the batch
// and the single shape reach parseSample through exactly one path.
func (r reportReq) single() sampleReq {
	return sampleReq{
		Provider: r.Provider, Account: r.Account, Plan: r.Plan, Kind: r.Kind,
		Machine: r.Machine, Lane: r.Lane, Window: r.Window,
		WindowMinutes: r.WindowMinutes, WindowStart: r.WindowStart, ResetsAt: r.ResetsAt,
		UsedPct: r.UsedPct, Confidence: r.Confidence, Synthetic: r.Synthetic,
		Requests: r.Requests, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens,
		TotalTokens: r.TotalTokens, CachedInputTokens: r.CachedInputTokens,
		CostCents: r.CostCents, CostLimitCents: r.CostLimitCents, Currency: r.Currency,
	}
}

// samplesOf flattens the one-or-many body into the batch.
func (r reportReq) samplesOf() []sampleReq {
	if len(r.Samples) > 0 {
		return r.Samples
	}
	if r.Provider == "" && r.Window == "" {
		return nil
	}
	return []sampleReq{r.single()}
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

// reportResp answers a record: how many samples were accepted, and — honestly —
// whether the warehouse stored them. `stored:false` means the datastore was
// unavailable; the samples were validated and accepted but not persisted, so a
// device can retry without being blocked.
type reportResp struct {
	// Accepted is how many samples passed validation. Every one of them was
	// accepted, or the whole report was refused — there is no partial success.
	Accepted int `json:"accepted"`
	// Stored is whether the warehouse actually persisted them. False means the
	// datastore was unavailable and the poll of history was lost; the request
	// still succeeded, so a device retries without being blocked.
	Stored bool `json:"stored"`
}

// record ingests a batch of account-usage samples — what a developer's OWN AI
// accounts have consumed of their OWN plans, metered from each provider's own
// login — and appends them to the warehouse series. Answers 202.
//
// Send either a `samples` array or one sample's fields at the top level. Every
// sample needs a provider, a machine and a known window class; an unknown window or
// kind is refused rather than silently rewritten, because a dash filled with a class
// nobody reported is worse than an error. There is no timestamp field: the server
// owns the observation clock, and a sample says which window it measured with
// windowStart or resetsAt.
//
// It is FAIL-SOFT on storage: a warehouse outage costs a poll of history
// (stored:false), never a failed request. It records usage ONLY — the link registry
// is refreshed separately via POST /v1/links, so there is one and only one way to
// update an account row.
func (o ops) record(ctx context.Context, in *reportReq) (*reportResp, error) {
	org, user, ok := caller(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to report usage")
	}
	raw := in.samplesOf()
	if len(raw) == 0 {
		return nil, zip.ErrBadRequest("at least one sample is required")
	}
	if len(raw) > maxSamples {
		return nil, zip.ErrBadRequest(fmt.Sprintf("at most %d samples per report", maxSamples))
	}
	now := time.Now()
	samples := make([]Sample, 0, len(raw))
	for _, s := range raw {
		x, err := parseSample(s, now)
		if err != nil {
			return nil, err
		}
		samples = append(samples, x)
	}

	// History is fail-soft: a warehouse outage must never fail a report or block a
	// device. `stored` tells the caller which happened — honestly.
	stored := true
	if err := o.s.State.warehouse.WriteSamples(ctx, org, user, samples, now); err != nil {
		o.s.Log.Debug("account usage write skipped", "org", org, "err", err)
		stored = false
	}
	return &reportResp{Accepted: len(samples), Stored: stored}, nil
}

// ── reads ────────────────────────────────────────────────────────────────────

// dashResp is one linked account's own lane dash.
type dashResp struct {
	// Provider is the upstream that was asked about, echoed back.
	Provider string `json:"provider"`
	// Account is the linked account that was asked about, when one was named.
	Account string `json:"account,omitempty"`
	// Range is the window that was served: 1h, 24h, 7d or 30d.
	Range string `json:"range"`
	// From is the inclusive start of that window, RFC3339 UTC.
	From string `json:"from"`
	// To is the exclusive end of that window, RFC3339 UTC.
	To string `json:"to"`
	// Source names the meter of record — the provider's own login, not Hanzo.
	Source string `json:"source"`
	// Scope says whose rows these are: the caller's own linked accounts.
	Scope string `json:"scope"`
	// Available is false when the warehouse could not be read. That means "no
	// answer", NOT "no usage" — the two lists below are then empty for a reason.
	Available bool `json:"available"`
	// Current is the newest window instance of each lane — the dash headline.
	Current []usageWindowView `json:"current"`
	// Windows is every instance in range, newest first — the history behind it.
	Windows []usageWindowView `json:"windows"`
}

// usageSamplesQuery selects which account's lane dash to read.
type usageSamplesQuery struct {
	// Account narrows to ONE linked account of that provider. Empty covers every
	// account the caller has linked there.
	Account string `json:"account"`
	// Provider is the upstream to read, e.g. anthropic. Required.
	Provider string `json:"provider"`
	// Range is the window to read: 1h, 24h, 7d or 30d. Empty means 24h, and any
	// other label is refused rather than silently replaced.
	Range string `json:"range"`
	// Window narrows to ONE window class: 6h, day, week or month. Empty covers
	// every class.
	Window string `json:"window"`
}

// samples is the PER-PROVIDER view: one connected account's own consumption of its
// own plan — "my plan is 47% through its 6h window, resets at 14:20".
//
// `current` is the newest instance of each lane (the headline); `windows` is the
// history behind it. Both come from ONE deduped read, so they can never disagree.
// The rows are the caller's OWN linked accounts, scoped to the validated principal
// and its subject — never another user's, and never another org's.
func (o ops) samples(ctx context.Context, in *usageSamplesQuery) (*dashResp, error) {
	org, user, ok := caller(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to view usage")
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
	out := dashResp{
		Provider: provider, Account: acct, Range: rangeLabel,
		From: rfc3339Of(from), To: rfc3339Of(to),
		Source: SourceAccount, Scope: ScopeUser,
		Current: []usageWindowView{}, Windows: []usageWindowView{},
	}
	rows, ok := o.s.State.warehouse.Series(ctx, org, user, provider, acct, window, from, to)
	if !ok {
		return &out, nil // Available=false: honest "unavailable"
	}
	out.Available = true
	for _, x := range rows {
		out.Windows = append(out.Windows, toSampleView(x))
	}
	for _, x := range currentOf(rows) {
		out.Current = append(out.Current, toSampleView(x))
	}
	return &out, nil
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

// windowRank orders the window classes by breadth, so the dash renders the lanes
// narrowest-first (6h before week) rather than in read order.
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
