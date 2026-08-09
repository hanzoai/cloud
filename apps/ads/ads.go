// Package ads is your paid ad campaigns, launched and paused from one place.
//
// A campaign carries an objective, a budget and reported spend, and runs on
// Meta, Google, TikTok, Reddit, LinkedIn or Microsoft with the org's own
// connector token.
//
// It is also the PAID executor of the go-to-market plane: apps/campaign fans its
// paid channel out to LaunchPaid/PaidSpend/PausePaid (provider.go), and this
// surface runs the same campaigns standalone.
//
// The AdCampaign entity is the root of the ad hierarchy (campaign → ad sets → ads):
// a named campaign on an ad Platform (meta/google/tiktok/x), a lifecycle Status
// (draft/active/paused/completed), an Objective, and Budget/Spend in minor units
// (cents). The ad-set and ad legs of the hierarchy are follow-ups that hang off
// this seam; this is the reviewable domain + campaign CRUD they attach to.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/ads/summary            per-org roll-up (total/active/budget/spend)
//	GET    /v1/ads/campaigns          list campaigns (?status=)      -> {data:[…]}
//	POST   /v1/ads/campaigns          create a campaign              -> AdCampaign (201)
//	GET    /v1/ads/campaigns/:id      campaign detail                -> AdCampaign
//	PUT    /v1/ads/campaigns/:id      update a campaign              -> AdCampaign
//	DELETE /v1/ads/campaigns/:id      delete a campaign
//	POST   /v1/ads/campaigns/:id/launch  run it on the provider      -> AdCampaign
//
// serve.go auto-registers GET /v1/ads/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
package ads

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

const (
	// maxField caps a single text field so an unbounded body can't amplify the
	// shared DB or a list response. AdCampaign fields are short labels.
	maxField = 1024
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
)

// platforms is the ad-network vocabulary. A create/update with an unknown
// platform is rejected; empty defaults to meta.
var platforms = map[string]bool{
	"meta": true, "google": true, "tiktok": true, "x": true,
}

// statuses is the campaign lifecycle. Empty defaults to draft.
var statuses = map[string]bool{
	"draft": true, "active": true, "paused": true, "completed": true,
}

// state is ads' own data; shared deps (logger, brand) live in the embedded
// cloud.Base, reached as s.Log / s.Brand.
type state struct {
	store *Store
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the ads surface onto app per HIP-0106. It keeps a package global
// (mounted) for Shutdown, so it constructs the Service value directly — the same
// "complex flavour" clients/crm uses.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("ads.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("ads.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("ads.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("ads.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "ads")
	s := &cloud.Service[state]{Base: b, State: state{store: store}}
	mounted = s

	routes(app, s)

	b.Log.Info("ads mounted", "brand", deps.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/ads openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the ads surface: the campaign CRUD + the summary roll-up.
//
// Every route but one is a TYPED op — one registry entry, which is what the
// OpenAPI operation, the MCP tool, the CLI command and every generated SDK
// method are projected from. The exception is named at its registration below.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/ads")
	// A typed op receives only a context, so the validated org it reads is parked
	// there by cloud.Bridge. This subsystem does not install it: the program's
	// composer does, once at the root, after the identity check that mints the org
	// and before any subsystem registers a route — an order only the composer can
	// hold.

	// Ops are declared ON THE GROUP: every zip.Router is an OpTarget, and the op's
	// path is the group's prefix composed with the leaf — the identity every
	// projection keys on, resolved the same way by zipdoc.
	o := ops{s: s}

	zip.Get(g, "/summary", o.summary)

	zip.Get(g, "/campaigns", o.listCampaigns)
	zip.Post(g, "/campaigns", o.createCampaign, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/campaigns/:id", o.getCampaign)
	zip.Put(g, "/campaigns/:id", o.updateCampaign)
	zip.Delete(g, "/campaigns/:id", o.deleteCampaign)

	// UNTYPED BY DESIGN — launch is deliberately BODY-TOLERANT. Its optional
	// {account} body is read with the Bind error DISCARDED
	// (`_ = c.Bind(&body)`, launchCampaign below), so a malformed or non-JSON
	// body launches the campaign on the stored account and answers 200 today. A
	// typed op cannot express that: zip v1.18.11's op.invoke unconditionally
	// jsonenc.Unmarshals any non-empty body and returns ErrBadRequest on failure
	// (zip/typed.go:239-243), turning that 200 into a 400. It converts when zip
	// can declare a body-tolerant op; until then this route publishes its address
	// and nothing else. apps/ads/typed_wire_test.go holds it as a CLOSED list.
	g.Post("/campaigns/:id/launch", cloud.Handle(s, launchCampaign))
}

// launch's prose, declared beside the wire fact that keeps it untyped. A typed
// op's prose is lifted from its doc comment by zipdoc; this route has no typed op
// to lift from, so without a Describe it publishes an operationId and nothing
// else — an SDK method that cannot explain itself and a CLI command with no help
// text. Declared through the same registry Register uses, so it renders only
// while the router actually serves the route.
func init() {
	openapi.Describe("/v1/ads/campaigns/:id/launch", http.MethodPost,
		"Run one of your stored campaigns on its ad network",
		"Creates the campaign on its platform under the CALLER ORG'S own connected ad account, "+
			"records the provider campaign id, flips the stored campaign to active and answers the "+
			"updated record. No ad-network token is held here: it is resolved from KMS through the "+
			"org's connector at launch time, BEFORE any provider call, so an org that has not "+
			"connected that platform gets 424 and no spend can ever start on a connection the org "+
			"did not make. Meta is executed for real; a campaign on a platform whose provider is not "+
			"wired yet answers 501 even when the connector is connected, and an edge failure at the "+
			"platform is 502. The optional {account} body overrides the target ad account for this "+
			"launch and is TOLERANT — a malformed or non-JSON body is ignored and the campaign "+
			"launches on its stored account rather than being refused. A campaign id another org "+
			"owns reads as not found.")
}

// ops binds the store to the typed ads ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name.
type noContent = struct{}

// ---- shared helpers (mirror clients/crm) ----

// tenant resolves the org — the tenant-isolation KEY — for a request. It uses
// principal.Org EXACTLY as SanitizeIdentity minted it from the validated IAM
// owner claim (HIP-0026): never lowercased, stripped, or truncated.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

// clip trims and bounds a text field to maxField.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

// clampLimit bounds a requested page size to (0, maxLimit], defaulting anything
// that is not a positive integer. A query value zip could not parse as an int
// arrives here as 0, which is exactly the "absent or unusable" case the untyped
// strconv.Atoi branch answered with the default — so the wire is unchanged.
func clampLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// normPlatform lower-cases + defaults (empty → meta) and validates against the
// fixed vocabulary.
func normPlatform(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "meta", true
	}
	return v, platforms[v]
}

// normStatus lower-cases + defaults (empty → draft) and validates against the
// fixed lifecycle vocabulary.
func normStatus(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "draft", true
	}
	return v, statuses[v]
}

// nonNeg clamps a signed amount to >= 0 (budget/spend are minor units, never negative).
func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// mapErr maps a store sentinel error to the right HTTP error. Non-sentinel errors
// become a 500 with the wrapped message.
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

// ---- campaigns ----

// campaignRef addresses one campaign. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says — which is
// also what the untyped handlers did, reading c.Param("id") and ignoring any
// body id. `json:"-"` keeps it out of the body entirely, so a request cannot
// even name a second target.
type campaignRef struct {
	// ID is the campaign to act on, from the path.
	ID string `json:"-" url:"id"`
}

// campaignInput is the user-owned half of a campaign — everything a create or an
// update reads off the wire. The server owns the rest (id, org, externalId,
// createdAt, updatedAt) and a request property the server always discards is one
// a generated client should never offer, so they are absent here rather than
// present-and-ignored the way binding the whole row made them.
//
// `url:"-"` on every field is what keeps this a BODY: zip's binder fills an In
// field from the query string as well as the body, and these routes have never
// taken a campaign's fields there — without the opt-out `?status=active` would
// silently overwrite what the body asked for.
type campaignInput struct {
	// Name is the campaign's display label. Required; trimmed and bounded to 1024 bytes.
	Name string `json:"name" url:"-"`
	// Platform is the ad network: meta, google, tiktok or x. Empty defaults to meta.
	Platform string `json:"platform" url:"-"`
	// Account is the provider ad-account this campaign runs on (Meta act_<id>). Optional.
	Account string `json:"account" url:"-"`
	// Status is the lifecycle state: draft, active, paused or completed. Empty defaults to draft.
	Status string `json:"status" url:"-"`
	// Objective is the campaign goal as the provider names it. Optional, bounded to 1024 bytes.
	Objective string `json:"objective" url:"-"`
	// Budget is the campaign budget in MINOR units (cents). Negative values clamp to 0.
	Budget int64 `json:"budget" url:"-"`
	// Spend is the amount spent so far in MINOR units (cents). Negative values clamp to 0.
	Spend int64 `json:"spend" url:"-"`
}

// updateCampaignIn is campaignInput addressed at one existing campaign.
//
// The fields are spelled out rather than embedded because zipdoc keys a field's
// prose on the OUTER type's name, so an embedded carrier publishes its shape
// with no prose on any field of it.
type updateCampaignIn struct {
	// ID is the campaign to update, from the path.
	ID string `json:"-" url:"id"`
	// Name is the campaign's display label. Required; trimmed and bounded to 1024 bytes.
	Name string `json:"name" url:"-"`
	// Platform is the ad network: meta, google, tiktok or x. Empty defaults to meta.
	Platform string `json:"platform" url:"-"`
	// Account is the provider ad-account this campaign runs on (Meta act_<id>). Optional.
	Account string `json:"account" url:"-"`
	// Status is the lifecycle state: draft, active, paused or completed. Empty defaults to draft.
	Status string `json:"status" url:"-"`
	// Objective is the campaign goal as the provider names it. Optional, bounded to 1024 bytes.
	Objective string `json:"objective" url:"-"`
	// Budget is the campaign budget in MINOR units (cents). Negative values clamp to 0.
	Budget int64 `json:"budget" url:"-"`
	// Spend is the amount spent so far in MINOR units (cents). Negative values clamp to 0.
	Spend int64 `json:"spend" url:"-"`
}

// listCampaignsIn narrows the org's campaign listing. Both fields come from the
// query string.
type listCampaignsIn struct {
	// Status filters to one lifecycle state (draft, active, paused, completed).
	// Empty returns every campaign the org has.
	Status string `json:"status"`
	// Limit caps how many campaigns come back: default 200, maximum 1000. A
	// value that is not a positive integer reads as the default.
	Limit int `json:"limit"`
}

// campaignList is the org's campaigns as one listing answers them.
type campaignList struct {
	// Data is the matching campaigns, newest-updated first.
	Data []AdCampaign `json:"data"`
}

// adSummary is the org's ad spend at a glance. Amounts are MINOR units (cents).
type adSummary struct {
	// Active is how many of those campaigns are in the active state.
	Active int `json:"active"`
	// Budget is the summed budget of every campaign in the org, in cents.
	Budget int64 `json:"budget"`
	// Campaigns is how many campaigns the org has, in every state.
	Campaigns int `json:"campaigns"`
	// Spend is the summed spend of every campaign in the org, in cents.
	Spend int64 `json:"spend"`
}

// createCampaign registers a new ad campaign for the caller's org and answers
// 201 with the stored row. It only records the campaign — nothing is sent to the
// ad network until POST /v1/ads/campaigns/{id}/launch runs it. The org is
// stamped by the server from the validated principal, so a body can never place
// a campaign in another tenant.
//
// Example: {"name": "Spring Launch", "platform": "meta", "objective": "conversions", "budget": 50000}
func (o ops) createCampaign(ctx context.Context, in *campaignInput) (*AdCampaign, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	platform, okPl := normPlatform(in.Platform)
	if !okPl {
		return nil, zip.ErrBadRequest("platform must be one of meta, google, tiktok, x")
	}
	status, okSt := normStatus(in.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	id := mint.ID("camp")
	now := time.Now().Unix()
	camp := AdCampaign{
		ID: id, Org: org, Name: name, Platform: platform, Account: clip(in.Account), Status: status,
		Objective: clip(in.Objective), Budget: nonNeg(in.Budget), Spend: nonNeg(in.Spend),
		CreatedAt: now, UpdatedAt: now,
	}
	saved, err := o.s.State.store.CreateCampaign(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// listCampaigns returns the caller org's ad campaigns, most recently updated
// first, optionally narrowed to one lifecycle status. The listing is bounded by
// the org: another tenant's campaigns are not reachable from here at all.
//
// Example: {"status": "active", "limit": 50}
func (o ops) listCampaigns(ctx context.Context, in *listCampaignsIn) (*campaignList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	rows, err := o.s.State.store.ListCampaigns(ctx, org, status, clampLimit(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &campaignList{Data: rows}, nil
}

// getCampaign returns one of the caller org's campaigns. An id another org owns
// reads as not found, so the response cannot confirm that it exists.
//
// Example: {"id": "camp_2f9c1d"}
func (o ops) getCampaign(ctx context.Context, in *campaignRef) (*AdCampaign, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := o.s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &camp, nil
}

// updateCampaign replaces the user-owned fields of one of the caller org's
// campaigns and answers the stored row. It is a full replace, not a patch: every
// field is written from the request, so an omitted one is cleared. externalId is
// launch-owned and is never touched here, so editing a campaign cannot break its
// link to a live provider execution.
//
// Example: {"id": "camp_2f9c1d", "name": "Spring Launch", "platform": "meta", "status": "paused", "budget": 75000}
func (o ops) updateCampaign(ctx context.Context, in *updateCampaignIn) (*AdCampaign, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	platform, okPl := normPlatform(in.Platform)
	if !okPl {
		return nil, zip.ErrBadRequest("platform must be one of meta, google, tiktok, x")
	}
	status, okSt := normStatus(in.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	camp := AdCampaign{
		ID: strings.TrimSpace(in.ID), Org: org, Name: name, Platform: platform, Account: clip(in.Account), Status: status,
		Objective: clip(in.Objective), Budget: nonNeg(in.Budget), Spend: nonNeg(in.Spend),
		UpdatedAt: time.Now().Unix(),
	}
	saved, err := o.s.State.store.UpdateCampaign(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
}

// deleteCampaign removes one of the caller org's campaigns and answers 204 with
// no body. It deletes the stored record only: a campaign already launched keeps
// running on the ad network, which must be stopped there. An id another org owns
// reads as not found.
//
// Example: {"id": "camp_2f9c1d"}
func (o ops) deleteCampaign(ctx context.Context, in *campaignRef) (*noContent, error) {
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

// ---- launch (consumes the connector plane) ----

// launchCampaign runs a stored ad campaign on its provider using the ORG'S
// connected ad-account token. It is the standalone proof that /v1/ads consumes the
// connector plane: no token is held here — LaunchPaid (provider.go) resolves it
// from KMS through integrations.TokenFor and FAILS CLOSED when the org has not
// connected the platform (424), so a launch can never spend on a connection the
// org did not make. On success the provider campaign id is recorded (MarkLaunched)
// and the campaign goes active. An optional body {account} sets/overrides the
// target ad account when the stored campaign has none.
func launchCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	var body struct {
		Account string `json:"account"`
	}
	_ = c.Bind(&body)
	account := clip(body.Account)
	if account == "" {
		account = camp.Account
	}
	ref, lerr := LaunchPaid(c.Context(), org, PaidPlan{
		Platform: camp.Platform, Account: account, Name: camp.Name,
		Objective: camp.Objective, BudgetCents: camp.Budget,
	})
	if lerr != nil {
		return mapProviderErr(lerr)
	}
	saved, err := s.State.store.MarkLaunched(c.Context(), org, camp.ID, ref.Account, ref.ExternalID, time.Now().Unix())
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// mapProviderErr renders a provider-execution error as the honest HTTP status: a
// missing/rejected connection is 424 (connect the account first), an unwired
// platform is 501, a transient edge failure is 502.
func mapProviderErr(err error) error {
	switch {
	case errors.Is(err, errNotConnected):
		return zip.Errorf(http.StatusFailedDependency, "connect your ad account for this platform first")
	case errors.Is(err, errUnsupportedPlatform):
		return zip.Errorf(http.StatusNotImplemented, "%v", err)
	case errors.Is(err, errUpstream):
		return zip.Errorf(http.StatusBadGateway, "%v", err)
	default:
		return zip.Errorf(http.StatusBadRequest, "%v", err)
	}
}

// ---- summary ----

// noInput is the In of an op addressed entirely by the caller's own validated
// principal: it takes nothing off the wire.
type noInput struct{}

// summary rolls the caller org's ad campaigns up into four numbers: how many
// campaigns exist, how many are active, and the summed budget and spend across
// all of them. Budget and spend are MINOR units (cents), the same units the
// campaign rows carry. It counts only this org's campaigns.
func (o ops) summary(ctx context.Context, _ *noInput) (*adSummary, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	total, active, budget, spend, err := o.s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &adSummary{Campaigns: total, Active: active, Budget: budget, Spend: spend}, nil
}

// Shutdown closes the ads store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
