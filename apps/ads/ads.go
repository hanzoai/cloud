// Package ads mounts the Hanzo Cloud /v1/ads/* surface: a native-Go, per-org
// ad-campaign store on Base/SQLite. It is the NET-NEW ads domain built directly
// on the ONE cloud framework (zip/Fiber + cloud.Deps + per-org SQLite) — the same
// shape every other in-repo subsystem uses (clients/crm and clients/marketing are
// the twins), NOT a proxy to a standalone ads pod (there is none — ads.hanzo.ai is
// net-new).
//
// The Campaign entity is the root of the ad hierarchy (campaign → ad sets → ads):
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
//	POST   /v1/ads/campaigns          create a campaign              -> Campaign (201)
//	GET    /v1/ads/campaigns/:id      campaign detail                -> Campaign
//	PUT    /v1/ads/campaigns/:id      update a campaign              -> Campaign
//	DELETE /v1/ads/campaigns/:id      delete a campaign
//
// serve.go auto-registers GET /v1/ads/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
package ads

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	// shared DB or a list response. Campaign fields are short labels.
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
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("ads.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "ads.db"))
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

// routes registers the ads surface: the campaign CRUD + the summary roll-up. Every
// route is a TYPED op — zip.<Verb> registers the route AND the registry entry OpenAPI
// / MCP / the CLI are projected from — and it takes the ABSOLUTE path because the
// registry keys on it.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The bridge FIRST: fiber runs middleware in registration order, so one installed
	// after these leaves would never run — and every op below resolves its tenant
	// through the request it parks.
	app.Group("/v1/ads").Use(cloud.Bridge())

	zip.Get(z, "/v1/ads/summary", o.summary)

	zip.Get(z, "/v1/ads/campaigns", o.listCampaigns)
	zip.Post(z, "/v1/ads/campaigns", o.createCampaign, zip.WithStatus(http.StatusCreated))
	zip.Get(z, "/v1/ads/campaigns/:id", o.getCampaign)
	zip.Put(z, "/v1/ads/campaigns/:id", o.updateCampaign)
	zip.Delete(z, "/v1/ads/campaigns/:id", o.deleteCampaign)
	zip.Post(z, "/v1/ads/campaigns/:id/launch", o.launchCampaign)
}

// ops binds the service to ads' typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listCampaigns), which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// orgOf resolves the org — the tenant-isolation KEY — for a typed op. The org is
// EXACTLY what SanitizeIdentity minted from the validated IAM owner claim, carried
// across the typed seam by cloud.Bridge, never read from the input. An op that cannot
// name its tenant refuses rather than reading across orgs.
func orgOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// CampaignRef addresses one campaign.
type CampaignRef struct {
	// ID is the campaign id from the path, as returned by create.
	ID string `json:"id"`
}

// CampaignQuery filters the campaign list.
type CampaignQuery struct {
	// Status narrows the list to one lifecycle state: draft, active, paused or completed.
	Status string `json:"status"`
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// CampaignList is the org's campaigns.
type CampaignList struct {
	// Data is the matching campaigns, newest first.
	Data []Campaign `json:"data"`
}

// LaunchReq launches a stored campaign on its provider.
type LaunchReq struct {
	// ID is the campaign id from the path.
	ID string `json:"id"`
	// Account sets or overrides the provider ad account to spend from; empty uses the
	// account already stored on the campaign.
	Account string `json:"account"`
}

// AdsSummary is the org's campaign roll-up.
type AdsSummary struct {
	// Campaigns is how many campaigns the org has.
	Campaigns int `json:"campaigns"`
	// Active is how many of them are in the active state.
	Active int `json:"active"`
	// Budget is the total budget across campaigns, in minor units.
	Budget int64 `json:"budget"`
	// Spend is the total recorded spend across campaigns, in minor units.
	Spend int64 `json:"spend"`
}

// ---- shared helpers (mirror clients/crm) ----

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

func limitOf(n int) int {
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

// createCampaign registers an ad campaign in the caller's org. Name is required;
// platform defaults to meta and status to draft, budget and spend are minor units
// clamped to >= 0, and the id, createdAt, updatedAt and externalId of the input are
// ignored — the server assigns them. Nothing is spent: a campaign is a plan until it
// is launched.
//
// Example: {"name": "Spring Launch", "platform": "meta", "objective": "signups", "budget": 50000}
func (o ops) createCampaign(ctx context.Context, body *Campaign) (*Campaign, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	platform, okPl := normPlatform(body.Platform)
	if !okPl {
		return nil, zip.ErrBadRequest("platform must be one of meta, google, tiktok, x")
	}
	status, okSt := normStatus(body.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	id, err := genID("camp")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	camp := Campaign{
		ID: id, Org: org, Name: name, Platform: platform, Account: clip(body.Account), Status: status,
		Objective: clip(body.Objective), Budget: nonNeg(body.Budget), Spend: nonNeg(body.Spend),
		CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreateCampaign(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "")
	}
	return &saved, nil
}

// listCampaigns returns the caller org's ad campaigns, optionally filtered by status.
//
// Example: {"status": "active", "limit": 50}
func (o ops) listCampaigns(ctx context.Context, in *CampaignQuery) (*CampaignList, error) {
	s := o.s
	org, err := orgOf(ctx)
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

// getCampaign returns one of the caller org's ad campaigns.
//
// Example: {"id": "camp_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) getCampaign(ctx context.Context, in *CampaignRef) (*Campaign, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &camp, nil
}

// updateCampaign replaces one of the caller org's ad campaigns. It is a full replace,
// not a patch: name is required and every omitted field resets to its default. The
// campaign updated is the one the PATH names — a body id cannot retarget it — and
// createdAt and externalId are server-owned.
//
// Example: {"id": "camp_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "name": "Spring Launch", "platform": "meta", "status": "paused", "budget": 75000}
func (o ops) updateCampaign(ctx context.Context, body *Campaign) (*Campaign, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	platform, okPl := normPlatform(body.Platform)
	if !okPl {
		return nil, zip.ErrBadRequest("platform must be one of meta, google, tiktok, x")
	}
	status, okSt := normStatus(body.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	camp := Campaign{
		ID: strings.TrimSpace(body.ID), Org: org, Name: name, Platform: platform, Account: clip(body.Account), Status: status,
		Objective: clip(body.Objective), Budget: nonNeg(body.Budget), Spend: nonNeg(body.Spend),
		UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.State.store.UpdateCampaign(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
}

// deleteCampaign removes one of the caller org's ad campaigns and answers 204. It
// deletes the stored plan only — a campaign already running at the provider is not
// stopped there.
//
// Example: {"id": "camp_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) deleteCampaign(ctx context.Context, in *CampaignRef) (*struct{}, error) {
	s := o.s
	org, err := orgOf(ctx)
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

// ---- launch (consumes the connector plane) ----

// launchCampaign runs a stored campaign on its provider and starts spending. It holds
// no token: the org's connected ad-account credential is resolved from KMS at call
// time and the launch FAILS CLOSED with 424 when the org has not connected that
// platform, so it can never spend on a connection the org did not make. On success the
// provider campaign id is recorded and the campaign goes active.
//
// Example: {"id": "camp_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "account": "act_1234567890"}
func (o ops) launchCampaign(ctx context.Context, in *LaunchReq) (*Campaign, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	account := clip(in.Account)
	if account == "" {
		account = camp.Account
	}
	ref, lerr := LaunchPaid(ctx, org, PaidPlan{
		Platform: camp.Platform, Account: account, Name: camp.Name,
		Objective: camp.Objective, BudgetCents: camp.Budget,
	})
	if lerr != nil {
		return nil, mapProviderErr(lerr)
	}
	saved, err := s.State.store.MarkLaunched(ctx, org, camp.ID, ref.Account, ref.ExternalID, time.Now().Unix())
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
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

// summary returns the caller org's campaign counts and money totals.
//
// Response: {"campaigns": 12, "active": 3, "budget": 500000, "spend": 128400}
func (o ops) summary(ctx context.Context, _ *struct{}) (*AdsSummary, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	total, active, budget, spend, err := s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &AdsSummary{Campaigns: total, Active: active, Budget: budget, Spend: spend}, nil
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
