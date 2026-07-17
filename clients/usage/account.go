package usage

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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
// composes Validated). Every account-usage handler and the summary's account block
// gate on it — an off-gateway forge with no validated user is refused fail-closed.
func caller(c *zip.Ctx) (org, user string, ok bool) {
	org, ok = principal.Org(c)
	if !ok {
		return "", "", false
	}
	return org, trim(c.User()), true
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

// TotalView is one row of the account-usage board (the summary's `accounts.rows`).
// Source and scope are what keep the board honest — see the Source/Scope consts.
type TotalView struct {
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

// reportResp answers a record: how many samples were accepted, and — honestly —
// whether the warehouse stored them. `stored:false` means the datastore was
// unavailable; the samples were validated and accepted but not persisted, so a
// device can retry without being blocked.
type reportResp struct {
	Accepted int  `json:"accepted"`
	Stored   bool `json:"stored"`
}

// record ingests a batch of samples and appends them to the warehouse series. It is
// FAIL-SOFT: a datastore outage costs a poll of history (stored:false), never a
// failed request. It records usage ONLY — the link registry is refreshed separately
// via POST /v1/links, so there is one and only one way to update an account row.
func record(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to report usage")
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

	// History is fail-soft: a warehouse outage must never fail a report or block a
	// device. `stored` tells the caller which happened — honestly.
	stored := true
	if err := s.State.warehouse.WriteSamples(c.Context(), org, user, samples, now); err != nil {
		s.Log.Debug("account usage write skipped", "org", org, "err", err)
		stored = false
	}
	return c.JSON(http.StatusAccepted, reportResp{Accepted: len(samples), Stored: stored})
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

// samples is the PER-PROVIDER view: one connected account's own consumption of its
// own plan — "my Claude Max plan is 47% through its 6h window, resets at 14:20".
//
// `current` is the newest instance of each lane (the headline); `windows` is the
// history behind it. Both are computed from ONE deduped read — no second query.
func samples(s *cloud.Service[state], c *zip.Ctx) error {
	org, user, ok := caller(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view usage")
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
	rows, ok := s.State.warehouse.Series(c.Context(), org, user, provider, acct, window, from, to)
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
