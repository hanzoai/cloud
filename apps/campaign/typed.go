package campaign

// typed.go is the campaign surface as TYPED ops — every one of the eleven.
//
// A typed op is ONE registry entry with N projections: the REST route, the
// OpenAPI operation's schema AND prose, the MCP tool an agent calls, the CLI
// command and every generated SDK method all follow from the same declaration.
// An untyped route gets a route and nothing else, which is what this surface was.
//
// The wire is unchanged, and that is the whole exercise. Two details had to be
// carried over deliberately rather than inherited:
//
//   - 201 on create, DECLARED with zip.WithStatus so the document keys its
//     response on the code the route actually sends.
//   - the body REQUIREMENT on the three writes that bind one (create, update,
//     addChannel). zip's typed decode skips an empty body and leaves the In at
//     its zero value; c.Bind refuses it. requireBody replays that refusal at the
//     point in the sequence the raw handler reached it.
//
// LAUNCH AND PAUSE STAY UNTYPED, and it is a measured wire fact rather than an
// omission. Neither has ever read a request body: the raw handler takes the id
// from the URL and ignores whatever was posted, so a caller that sent junk still
// got its campaign launched. zip's invoke refuses a body it cannot parse BEFORE
// the handler runs (typed.go:239), and an In that tolerates one cannot rescue it
// either — encoding/json validates the whole document before it will call a
// custom UnmarshalJSON, so the syntax error is raised without the type ever being
// consulted. Measured: POST …/launch with `{not json` answers 200 untyped and 400
// typed. The fix belongs in zip, not here: hasRequestBody already computes "this
// op binds nothing the URL does not carry", and invoke reading the body only when
// that is false would make both routes typable with no change to either wire.
// TestLaunchAndPauseIgnoreTheirBody pins the tolerance so the refusal stays
// checkable.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.list), which is also the
// only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op that takes nothing off the wire — it is addressed
// entirely by the caller's validated principal.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name, so a defined type here would publish
// "200 with a body" about a route that answers 204 with none.
type noContent = struct{}

// requireBody replays, at the point in the sequence the raw handler reached it,
// the refusal c.Bind has always answered: these writes take a JSON body, and a
// request with none — or with a content type this service does not parse — is a
// 400, not a write of empty values.
//
// zip's typed decode is TOLERANT by construction (it skips an empty body and
// leaves the In at its zero value), so a naive conversion would have changed what
// each of these routes ACCEPTS. This calls the SAME c.Bind over an empty target,
// so it is the same decision and the same message, not a re-implementation free
// to drift.
func requireBody(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil // off the HTTP path there is no body to require
	}
	return c.Bind(&struct{}{})
}

// ----- inputs ---------------------------------------------------------------

// campaignRef addresses ONE campaign by its id, which is the path segment — the
// URL is the addressing authority, and GET and DELETE carry no request body at
// all, so there is nothing a caller could smuggle a second id in through.
type campaignRef struct {
	// ID is the campaign's server-minted handle, "cmp_"-prefixed.
	ID string `json:"id"`
}

// campaignRecord (store.go) and campaignResults (metrics.go) are what this surface
// publishes. Each is declared under its published name — with Campaign and Metrics
// as domain aliases of the same type — because the fleet's schema namespace is FLAT
// (openapi.Compose refuses one name meaning two things, and apps/marketing already
// publishes an email "Campaign", apps/agents a "Metrics"), and because a field's
// doc comment only reaches the document under the name its struct literal carries.

// campaignFilter narrows the org's campaign list. Both fields are query
// parameters and both are optional; an unparseable limit reads as the default,
// exactly as it did before this was typed.
type campaignFilter struct {
	// Status keeps only campaigns in that state: draft, live, paused or failed.
	// Empty means any.
	Status string `json:"status"`
	// Limit bounds the page. 0 or less means the default of 200; anything above
	// 1000 is clamped to 1000.
	Limit int `json:"limit"`
}

// campaignWrite is the writable half of a campaign — everything a caller may set.
// The id, the org, the status and the timestamps are all server-owned and are
// never read off the wire: the org is the validated bearer claim, the status is
// the launch machine's, and a create always begins as a draft.
type campaignWrite struct {
	// Name is the campaign's display name. Required; trimmed and capped at 2048
	// characters.
	Name string `json:"name"`
	// Audience is the segment or audience selector this campaign targets.
	Audience string `json:"audience"`
	// Content is the ordered creative set. Content[0] is the active creative and
	// the rest are A/B variants; at most 32, empty entries dropped.
	Content []string `json:"content"`
	// Channels are the fan-out targets, at most one per kind (paid, organic,
	// email) and at most 12. A channel's status and provider id are server-owned:
	// whatever the caller sends for them is replaced with "pending".
	Channels []ChannelSpec `json:"channels"`
	// ScheduleAt is when the campaign should run, in unix seconds. Negative reads
	// as 0 (immediately).
	ScheduleAt int64 `json:"scheduleAt"`
	// Budget is the campaign's total budget in CENTS. Negative reads as 0.
	Budget int64 `json:"budget"`
}

// campaignUpdate is campaignWrite addressed at one existing campaign.
type campaignUpdate struct {
	// ID is the campaign to update, from the path.
	ID string `json:"id"`
	campaignWrite
}

// channelAdd adds or replaces one channel on a campaign.
type channelAdd struct {
	// ID is the campaign to add the channel to, from the path.
	ID string `json:"id"`
	// Kind is the channel kind and the identity a campaign holds at most one of:
	// paid, organic or email.
	Kind string `json:"kind"`
	// Platform is the provider within the kind — meta, google, x, instagram, or
	// the email provider.
	Platform string `json:"platform"`
	// Account is the provider account this channel runs under: an ad-account, a
	// page, or a mailing-list id.
	Account string `json:"account"`
}

// channelRef addresses ONE channel of one campaign, by campaign id and kind.
type channelRef struct {
	// ID is the campaign, from the path.
	ID string `json:"id"`
	// Kind is the channel to remove: paid, organic or email.
	Kind string `json:"kind"`
}

// metricsQuery is a campaign metrics read over a time window.
type metricsQuery struct {
	// ID is the campaign to report on, from the path.
	ID string `json:"id"`
	// Range is the lookback window: 24h, 7d, 30d or 90d. Anything else, including
	// empty, reads as 30d.
	Range string `json:"range"`
	// Start is an explicit RFC3339 window start. Honored only together with End,
	// and only when End is after it.
	Start string `json:"start"`
	// End is an explicit RFC3339 window end.
	End string `json:"end"`
}

// ----- outputs --------------------------------------------------------------

// campaignPage is one page of the org's campaigns, newest first.
type campaignPage struct {
	// Data are the campaigns on this page.
	Data []campaignRecord `json:"data"`
}

// campaignSummary is the org's go-to-market roll-up: how much is in flight, what
// it is budgeted at, and which channel executors this deployment can actually
// reach.
type campaignSummary struct {
	// Campaigns is how many campaigns the org has, in any state.
	Campaigns int `json:"campaigns"`
	// Live is how many of them are currently live.
	Live int `json:"live"`
	// Budget is the sum of every campaign's budget, in CENTS.
	Budget int64 `json:"budget"`
	// Channels are the channel kinds this deployment has an executor wired for.
	// A kind absent here is a kind a launch will honestly record as unavailable.
	Channels []string `json:"channels"`
}

// ----- ops ------------------------------------------------------------------

// SummarizeCampaigns returns the org's go-to-market roll-up: how many campaigns
// exist, how many are live, their total budget in cents, and which channel
// executors this deployment can actually reach.
//
// The channel list is the deployment's honest capability, not a wish: a kind
// missing from it is one a launch will record as "unavailable" rather than fail
// on.
func (o ops) summary(ctx context.Context, _ *noInput) (*campaignSummary, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	total, live, budget, err := o.s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &campaignSummary{Campaigns: total, Live: live, Budget: budget, Channels: registeredKinds()}, nil
}

// ListCampaigns returns the org's campaigns, newest first, optionally narrowed to
// one status.
//
// A campaign is the top-level go-to-market object: a value that SPANS channels
// (paid, organic, email) and fans out to the executor for each. The listing is
// org-scoped server-side, so one org can never see another's campaigns.
//
// Example: {"status": "live", "limit": 50}
func (o ops) list(ctx context.Context, in *campaignFilter) (*campaignPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListCampaigns(ctx, org,
		strings.ToLower(strings.TrimSpace(in.Status)), clampLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	page := make([]campaignRecord, 0, len(rows))
	page = append(page, rows...)
	return &campaignPage{Data: page}, nil
}

// clampLimit bounds a requested page size exactly as limitOf did off the query
// string: absent or non-positive means the default, and the maximum is a ceiling.
func clampLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// CreateCampaign creates a campaign as a DRAFT and returns it.
//
// A draft is inert: nothing is sent, no connector is touched and no budget is
// committed until the campaign is launched. The channels named here are validated
// and de-duplicated by kind (one executor per kind), and every channel starts
// "pending" whatever the caller claims — a client can never assert a launched
// state.
//
// Example: {"name": "Spring launch", "budget": 250000, "content": ["Ship faster"],
// "channels": [{"kind": "paid", "platform": "meta"}]}
func (o ops) create(ctx context.Context, in *campaignWrite) (*campaignRecord, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	channels, okCh := normChannels(in.Channels)
	if !okCh {
		return nil, zip.ErrBadRequest("each channel kind must be one of paid, organic, email")
	}
	id := mint.ID("cmp")
	now := time.Now().Unix()
	saved, err := o.s.State.store.CreateCampaign(ctx, Campaign{
		ID: id, Org: org, Name: name, Audience: clip(in.Audience),
		Content: clipContent(in.Content), Channels: channels,
		ScheduleAt: nonNeg(in.ScheduleAt), Budget: nonNeg(in.Budget),
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, mapErr(err, "")
	}
	return record(saved), nil
}

// record answers a stored campaign by pointer. campaignRecord IS Campaign — one
// type, two spellings — so this only exists to keep every call site one line
// rather than a temporary per return.
func record(c campaignRecord) *campaignRecord { return &c }

// GetCampaign returns one campaign of the caller's org — its name, audience,
// creatives, channels with their per-channel launch state, schedule, budget and
// status. 404 when the org has no campaign with that id.
func (o ops) get(ctx context.Context, in *campaignRef) (*campaignRecord, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := o.s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return record(camp), nil
}

// UpdateCampaign rewrites a campaign's core fields — name, audience, creatives,
// schedule and budget — and returns the updated campaign.
//
// Channels are replaced ONLY while the campaign is still a draft. Once it is
// launched its channels carry provider state (an external id, a live status), so
// they are added and removed explicitly through the channels sub-resource
// instead; a whole-object write would silently orphan a running execution.
//
// Example: {"name": "Spring launch", "budget": 500000}
func (o ops) update(ctx context.Context, in *campaignUpdate) (*campaignRecord, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	current, err := o.s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	current.Name = name
	current.Audience = clip(in.Audience)
	current.Content = clipContent(in.Content)
	current.ScheduleAt = nonNeg(in.ScheduleAt)
	current.Budget = nonNeg(in.Budget)
	if current.Status == StatusDraft {
		channels, okCh := normChannels(in.Channels)
		if !okCh {
			return nil, zip.ErrBadRequest("each channel kind must be one of paid, organic, email")
		}
		current.Channels = channels
	}
	current.UpdatedAt = time.Now().Unix()
	saved, err := o.s.State.store.Save(ctx, current)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return record(saved), nil
}

// DeleteCampaign removes one campaign of the caller's org and answers 204 with no
// body. 404 when the org has no campaign with that id.
//
// It deletes the RECORD, not the executions: a campaign whose channels are live
// on a provider should be paused first, or those executions keep running with
// nothing here to report them.
func (o ops) del(ctx context.Context, in *campaignRef) (*noContent, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := o.s.State.store.DeleteCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("campaign not found")
	}
	return nil, nil
}

// CampaignMetrics returns a campaign's results over a window: the analytics
// funnel (impressions, clicks, conversions, revenue, visitors), the spend each
// channel's connector reports, and the derived growth KPIs — CTR, CVR, CAC and
// ROAS.
//
// There is exactly ONE metrics plane and nothing is stored here: the funnel is an
// analytics query over the campaign's utm_campaign-tagged events, and the spend is
// each provider's own number read through the org's connector. A warehouse that is
// not emitting yet degrades to available:false with zeroes — honest-empty, never a
// 500 and never a fabricated number. When the campaign runs more than one creative
// and an experiment is wired, abTest carries the A/B analysis.
//
// Example: {"id": "cmp_1f…", "range": "7d"}
func (o ops) metrics(ctx context.Context, in *metricsQuery) (*campaignResults, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := o.s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	start, end, rangeLabel := window(in.Start, in.End, in.Range)

	// Funnel — the ONE analytics query for a campaign's results (org + campaign
	// bound POSITIONALLY inside analytics; honest-empty when the events table is
	// absent). "" = whole-campaign (all creatives).
	ev, aerr := event.CampaignMetrics(ctx, org, camp.ID, "", start, end)
	if aerr != nil {
		// A datastore outage is honest-empty here too — the campaign still reports
		// its spend + channels; the funnel is simply unavailable this read.
		o.s.Log.Debug("campaign analytics unavailable (honest-empty)", "campaign", camp.ID, "err", aerr)
	}

	// Spend — each live channel's connector-reported spend, fanned in fail-soft.
	spendCents, chMetrics := channelSpend(ctx, org, camp)

	m := campaignResults{
		CampaignID:  camp.ID,
		Name:        camp.Name,
		Status:      camp.Status,
		Range:       rangeLabel,
		Start:       start.UTC().Format(time.RFC3339),
		End:         end.UTC().Format(time.RFC3339),
		Available:   ev.Available,
		Impressions: ev.Impressions,
		Clicks:      ev.Clicks,
		Conversions: ev.Conversions,
		Revenue:     ev.Revenue,
		Visitors:    ev.Visitors,
		SpendCents:  spendCents,
		Channels:    chMetrics,
		Source:      ev.Source,
	}
	m.CTR = ratio(ev.Clicks, ev.Impressions)
	m.CVR = ratio(ev.Conversions, ev.Clicks)
	m.CAC = perConversion(spendCents, ev.Conversions)
	m.ROAS = roas(ev.Revenue, spendCents)
	// A/B lens: the experiments primitive's pull-model analysis (nil when the
	// campaign runs a single creative or no experiment is wired).
	m.ABTest = analyzeExperiment(ctx, org, camp, start, end)
	return &m, nil
}

// AddCampaignChannel adds a channel to a campaign, or REPLACES the one it already
// has of that kind, and returns the updated campaign.
//
// A campaign carries at most one channel per kind, because the kind IS the
// executor: adding a second "paid" channel would mean two ad accounts running one
// campaign with no way to tell their results apart. The new channel starts
// "pending" — adding it does not launch it.
//
// Example: {"id": "cmp_1f…", "kind": "email", "platform": "sendgrid", "account": "list_42"}
func (o ops) addChannel(ctx context.Context, in *channelAdd) (*campaignRecord, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := o.s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
	}
	n, okCh := normChannel(ChannelSpec{Kind: in.Kind, Platform: in.Platform, Account: in.Account})
	if !okCh {
		return nil, zip.ErrBadRequest("channel kind must be one of paid, organic, email")
	}
	// Replace an existing channel of the same kind, else append (one per kind).
	replaced := false
	for i := range camp.Channels {
		if camp.Channels[i].Kind == n.Kind {
			camp.Channels[i] = n
			replaced = true
			break
		}
	}
	if !replaced {
		if len(camp.Channels) >= maxChannels {
			return nil, zip.ErrBadRequest("too many channels")
		}
		camp.Channels = append(camp.Channels, n)
	}
	camp.UpdatedAt = time.Now().Unix()
	saved, err := o.s.State.store.Save(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return record(saved), nil
}

// RemoveCampaignChannel drops one channel from a campaign and returns the updated
// campaign. 404 when the campaign carries no channel of that kind.
//
// It removes the channel from the PLAN. A channel that is live at its provider
// should be paused first — dropping the row here leaves nothing to pause it with
// afterwards.
func (o ops) removeChannel(ctx context.Context, in *channelRef) (*campaignRecord, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := o.s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	out := make([]ChannelSpec, 0, len(camp.Channels))
	for _, ch := range camp.Channels {
		if ch.Kind != kind {
			out = append(out, ch)
		}
	}
	if len(out) == len(camp.Channels) {
		return nil, zip.ErrNotFound("channel not found")
	}
	camp.Channels = out
	camp.UpdatedAt = time.Now().Unix()
	saved, err := o.s.State.store.Save(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return record(saved), nil
}
