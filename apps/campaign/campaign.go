// Package campaign mounts the Hanzo Cloud /v1/campaign/* surface: the top-level
// go-to-market orchestration plane. A Campaign is a VALUE — {name, audience,
// content[], schedule, budget, channels[], status} — that SPANS channels and fans
// out to orthogonal executors. It is the capability layer that CONSUMES the
// connector plane: the campaign object never touches a credential; each channel
// executor resolves the org's connector token itself through the
// integrations.TokenFor custody seam.
//
// THE DECOMPLECT (HIP-0126 — Integrations, Connectors & the Extension Runtime): a
// Connector is a connection (credential custody + auth); a capability is what you
// DO with it. /v1/campaign is a CONSUMER of connectors — the role HIP-0126 gives
// Flows — never a second credential path. "campaign" used to be braided across
// three packages — an ad campaign (clients/ads), an email campaign
// (clients/marketing), social posts (clients/social). This plane lifts the GTM
// campaign to the ONE value it is and makes the channels orthogonal EXECUTORS it
// fans out to (channel.go):
//
//	paid    → /v1/ads       (the ad connectors: meta_ads/google_ads/tiktok_ads/…)
//	organic → /v1/publish   (the social connectors)
//	email   → /v1/marketing (the email connectors: sendgrid/mailchimp/…)
//
// Each channel is ALSO usable standalone at its own surface; this plane composes
// them. Metrics are NOT stored here — a campaign's results are read at query time
// from the ONE analytics plane (metrics.go: analytics.CampaignMetrics over the
// utm_campaign-tagged events) plus each channel connector's reported spend. A
// creative A/B is an experiment whose variant = creative and whose metric = the
// campaign result from analytics; it composes the experiment seam (experiment.go),
// never a second assignment or evidence store.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, and the org is the value passed to every channel executor,
// so a campaign can only ever resolve its OWN org's connector token.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/campaign/summary               per-org roll-up + wired channels
//	GET    /v1/campaign                        list campaigns (?status=)   -> {data:[…]}
//	POST   /v1/campaign                        create a campaign (draft)    -> Campaign (201)
//	GET    /v1/campaign/:id                    campaign detail              -> Campaign
//	PUT    /v1/campaign/:id                    update a draft campaign      -> Campaign
//	DELETE /v1/campaign/:id                    delete a campaign
//	POST   /v1/campaign/:id/launch             fan out to channels          -> Campaign
//	POST   /v1/campaign/:id/pause              pause every live channel     -> Campaign
//	GET    /v1/campaign/:id/metrics            analytics results + spend    -> Metrics
//	POST   /v1/campaign/:id/channels           add a channel                -> Campaign
//	DELETE /v1/campaign/:id/channels/:kind     remove a channel             -> Campaign
//
// serve.go auto-registers GET /v1/campaign/health (no OwnsHealth here).
//
// EVERY ROUTE IS A TYPED OP (zip.Get/Post/Put/Delete with concrete In/Out structs),
// so the surface is ONE registry with N projections: REST, the OpenAPI document, the
// MCP tool list and the CLI all derive from these same registrations. Handler prose
// is lifted into the spec at build time by cmd/zipdoc, because Go does not keep
// comments at run time.
package campaign

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

const (
	// maxField caps a single text field so an unbounded body can't amplify the
	// shared DB or a list response.
	maxField = 2048
	// maxContent caps how many creatives one campaign carries (A/B variant set).
	maxContent = 32
	// maxChannels caps the fan-out breadth of one campaign.
	maxChannels = 12
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
)

// kinds is the channel-kind vocabulary. A channel with an unknown kind is
// rejected at add time; the executor for a valid-but-unwired kind is resolved
// (and honestly recorded "unavailable") only at launch.
var kinds = map[string]bool{
	KindPaid: true, KindOrganic: true, KindEmail: true,
}

// state is campaign's own data; shared deps (logger, brand) live in cloud.Base.
type state struct {
	store *Store
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the campaign surface onto app per HIP-0106. It keeps a package
// global (mounted) for Shutdown, so it constructs the Service value directly —
// the same "complex flavour" clients/ads uses.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("campaign.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("campaign.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("campaign.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("campaign.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "campaign.db"))
	if err != nil {
		return fmt.Errorf("campaign.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "campaign")
	s := &cloud.Service[state]{Base: b, State: state{store: store}}
	mounted = s

	routes(app, s)

	b.Log.Info("campaign mounted", "brand", deps.Brand, "channels", registeredKinds())
	return nil
}

// routes registers the campaign surface. Registration order IS match order (zip
// is first-match): the static /summary is registered before /:id so it is never
// captured by the id param, and the deeper /:id/… routes have a distinct segment
// count so none shadows another.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its tenant through it. Bounded to campaign's own subtree.
	app.Group("/v1/campaign").Use(cloud.Bridge())

	zip.Get(z, "/v1/campaign/summary", o.summary)

	zip.Get(z, "/v1/campaign", o.listCampaigns)
	zip.Post(z, "/v1/campaign", o.createCampaign, zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/campaign/:id", o.getCampaign)
	zip.Put(z, "/v1/campaign/:id", o.updateCampaign)
	zip.Delete(z, "/v1/campaign/:id", o.deleteCampaign)

	zip.Post(z, "/v1/campaign/:id/launch", o.launchCampaign)
	zip.Post(z, "/v1/campaign/:id/pause", o.pauseCampaign)
	zip.Get(z, "/v1/campaign/:id/metrics", o.metricsCampaign)

	zip.Post(z, "/v1/campaign/:id/channels", o.addChannel)
	zip.Delete(z, "/v1/campaign/:id/channels/:kind", o.removeChannel)
}

// ---- shared helpers (mirror clients/ads) ----

// ops binds the service to campaign's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, the only bound form
// cmd/zipdoc can lift prose from. ops carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the org — the tenant-isolation KEY — EXACTLY as SanitizeIdentity
// minted it from the validated IAM owner claim (HIP-0026), carried across the typed
// seam by cloud.Bridge. It is never an input field: an input is what the caller says
// about itself.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("valid bearer required")
	}
	return org, nil
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// clip trims and bounds a text field to maxField.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

// limitOf bounds a caller's page size: absent, unparseable or non-positive means
// defaultLimit, and nothing above maxLimit is honoured.
func limitOf(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// clipContent bounds + trims a creative set.
func clipContent(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = clip(v)
		if v == "" {
			continue
		}
		out = append(out, v)
		if len(out) >= maxContent {
			break
		}
	}
	return out
}

// normChannel validates + normalizes one ChannelSpec at add/create time. Status
// is always reset to pending here (a client can never assert a launched state);
// ExternalID/Detail are server-owned and cleared.
func normChannel(in ChannelSpec) (ChannelSpec, bool) {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !kinds[kind] {
		return ChannelSpec{}, false
	}
	return ChannelSpec{
		Kind:     kind,
		Platform: strings.ToLower(clip(in.Platform)),
		Account:  clip(in.Account),
		Status:   chanPending,
	}, true
}

// normChannels validates + de-duplicates a channel set by kind (one executor per
// kind per campaign — the fan-out is over kinds).
func normChannels(in []ChannelSpec) ([]ChannelSpec, bool) {
	out := make([]ChannelSpec, 0, len(in))
	seen := map[string]bool{}
	for _, ch := range in {
		n, ok := normChannel(ch)
		if !ok {
			return nil, false
		}
		if seen[n.Kind] {
			continue
		}
		seen[n.Kind] = true
		out = append(out, n)
		if len(out) >= maxChannels {
			break
		}
	}
	return out, true
}

// mapErr maps a store sentinel error to the right HTTP error.
func mapErr(err error, notFoundMsg string) error {
	switch err {
	case errNotFound:
		return zip.ErrNotFound(notFoundMsg)
	case errConflict:
		return zip.ErrConflict("already exists")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// shortErr renders a one-line, secret-free failure reason for a channel Detail.
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ---- wire types ----

// CampaignRef addresses one campaign.
type CampaignRef struct {
	// ID is the campaign id from the path, as returned by create.
	ID string `json:"id"`
}

// CampaignPage is the bound + filter a campaign list accepts.
type CampaignPage struct {
	// Status narrows to one lifecycle state (draft, live, paused, failed); empty
	// means every campaign.
	Status string `json:"status"`
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// CampaignList is a page of campaigns.
type CampaignList struct {
	// Data is the page; an empty array when the org has created no campaign.
	Data []Campaign `json:"data"`
}

// Summary is the org's campaign roll-up plus the channels this deployment can run.
type Summary struct {
	// Campaigns is how many campaigns the org has.
	Campaigns int `json:"campaigns"`
	// Live is how many of them are currently live.
	Live int `json:"live"`
	// Budget is the summed budget in cents.
	Budget int64 `json:"budget"`
	// Channels are the channel kinds with an executor wired in this deployment.
	Channels []string `json:"channels"`
}

// ChannelInput adds or replaces one channel on a campaign. Status, externalId and
// detail are server-owned: a caller can never assert a launched state.
type ChannelInput struct {
	// ID is the campaign id from the path.
	ID string `json:"id"`
	// Kind is the executor family: paid, organic or email. Required.
	Kind string `json:"kind"`
	// Platform is the provider within the kind (meta, google, x, a mail provider).
	Platform string `json:"platform"`
	// Account is the provider account reference — an ad-account, page or list id.
	Account string `json:"account,omitempty"`
}

// ChannelRef addresses one channel of one campaign, by kind.
type ChannelRef struct {
	// ID is the campaign id from the path.
	ID string `json:"id"`
	// Kind is the channel to remove: paid, organic or email.
	Kind string `json:"kind"`
}

// MetricsQuery is the window a campaign's results are read over.
type MetricsQuery struct {
	// ID is the campaign id from the path.
	ID string `json:"id"`
	// Range is 24h, 7d, 30d or 90d; empty means 30d. Ignored when start and end are given.
	Range string `json:"range"`
	// Start is the RFC3339 window start; honoured only together with end.
	Start string `json:"start"`
	// End is the RFC3339 window end; honoured only together with start.
	End string `json:"end"`
}

// ---- CRUD ----

// createCampaign stores a new campaign in the caller's org as a draft. Name is
// required; every channel kind must be paid, organic or email, and one channel is
// kept per kind. Budget is cents and is clamped to >= 0. The id, status and
// timestamps of the input are ignored — the server assigns them.
//
// Example: {"name": "Spring Launch", "audience": "signups", "content": ["Try it free"], "channels": [{"kind": "paid", "platform": "meta"}], "budget": 50000}
func (o ops) createCampaign(ctx context.Context, in *Campaign) (*Campaign, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	name := clip(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	channels, okCh := normChannels(body.Channels)
	if !okCh {
		return nil, zip.ErrBadRequest("each channel kind must be one of paid, organic, email")
	}
	id, err := genID("cmp")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	camp := Campaign{
		ID: id, Org: org, Name: name, Audience: clip(body.Audience),
		Content: clipContent(body.Content), Channels: channels,
		ScheduleAt: nonNeg(body.ScheduleAt), Budget: nonNeg(body.Budget),
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreateCampaign(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// listCampaigns returns the caller org's campaigns, most recently updated first,
// optionally narrowed to one lifecycle status.
//
// Example: {"status": "live", "limit": 50}
func (o ops) listCampaigns(ctx context.Context, in *CampaignPage) (*CampaignList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	rows, err := s.State.store.ListCampaigns(ctx, org, status, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &CampaignList{Data: rows}, nil
}

// getCampaign returns one of the caller org's campaigns. A campaign belonging to
// another org reads as not found.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) getCampaign(ctx context.Context, in *CampaignRef) (*Campaign, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &camp, nil
}

// updateCampaign edits a campaign's core fields; name is required. Channels are
// replaced only while the campaign is a draft — once launched they carry provider
// state, so add and remove them through the channels sub-resource instead and a live
// launch is never silently orphaned.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "name": "Spring Launch", "budget": 75000}
func (o ops) updateCampaign(ctx context.Context, in *Campaign) (*Campaign, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	body := *in
	name := clip(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	current.Name = name
	current.Audience = clip(body.Audience)
	current.Content = clipContent(body.Content)
	current.ScheduleAt = nonNeg(body.ScheduleAt)
	current.Budget = nonNeg(body.Budget)
	if current.Status == StatusDraft {
		channels, okCh := normChannels(body.Channels)
		if !okCh {
			return nil, zip.ErrBadRequest("each channel kind must be one of paid, organic, email")
		}
		current.Channels = channels
	}
	current.UpdatedAt = time.Now().Unix()
	saved, err := s.State.store.Save(ctx, current)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
}

// deleteCampaign removes one of the caller org's campaigns and answers 204. It
// deletes the record only — a live channel is not paused or retracted first.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) deleteCampaign(ctx context.Context, in *CampaignRef) (*struct{}, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := s.State.store.DeleteCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("campaign not found")
	}
	return nil, nil
}

// ---- channels sub-resource ----

// addChannel adds one fan-out channel to a campaign, replacing any channel already
// registered for that kind — a campaign runs one executor per kind. The channel
// starts pending; launching is a separate call.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "kind": "paid", "platform": "meta", "account": "act_123"}
func (o ops) addChannel(ctx context.Context, in *ChannelInput) (*Campaign, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
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
	saved, err := s.State.store.Save(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
}

// removeChannel drops one channel kind from a campaign. The channel is removed from
// the record only — a live execution at the provider is not paused first.
//
// Example: {"id": "cmp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "kind": "paid"}
func (o ops) removeChannel(ctx context.Context, in *ChannelRef) (*Campaign, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
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
	saved, err := s.State.store.Save(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
}

// ---- summary ----

// summary reports the caller org's campaign counts and total budget, plus which
// channel kinds this deployment actually has an executor wired for.
//
// Response: {"campaigns": 12, "live": 3, "budget": 250000, "channels": ["paid", "email"]}
func (o ops) summary(ctx context.Context, _ *struct{}) (*Summary, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	total, live, budget, err := s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &Summary{Campaigns: total, Live: live, Budget: budget, Channels: registeredKinds()}, nil
}

// Shutdown closes the campaign store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
