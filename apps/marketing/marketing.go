// Package marketing is lifecycle email: drip sequences that reach the right people.
//
// Audiences resolve from the IAM roster, promo codes redeem as wallet credit,
// and every send passes through the per-org suppression list.
//
// Subsystems (all org-scoped, /v1 only):
//
//   - Campaigns — named campaign on a delivery Channel (email/sms/social/meta/
//     google/tiktok), a lifecycle Status (draft/scheduled/active/paused/
//     completed), Budget/Spend in cents, and a send time (scheduled_at). This is
//     a SECOND campaign record beside apps/campaign (the go-to-market plane) and
//     apps/ads (the paid one); apps/campaign is the canonical owner of a campaign
//     that spans channels, and this one holds only what an email send needs.
//   - Email sequences — ordered drip Steps sent as DURABLE tasks on the embedded
//     hanzoai/tasks engine (drip.go): each enrollment's next_run_at lives in
//     SQLite, a per-minute engine schedule sweeps due steps, every step is
//     claimed once (idempotent) and delivered through the ONE send gate.
//   - Audiences — who to reach, resolved to real mailboxes through Hanzo IAM
//     (roster.go): an audience with no event filter is EVERY mailable customer
//     in the org, and one with an event narrows that roster to the cohort the
//     analytics warehouse (hanzo.events) selected. Honest-empty when the roster
//     or warehouse cannot be read — never a fabricated number, never a send to
//     nobody reported as a success.
//   - Promo codes — the "First 1,000: 90% off month 1" launch promo (discounts.md)
//     realized as a non-cash wallet credit through the finance ledger, with the
//     hard 1,000 cap, one-per-org, one-per-instrument and team-seat-cap guards.
//   - Content calendar — scheduled posts as documents, published by a
//     task-executed hook; social publish returns an honest 501 because no
//     publisher is wired here (see calendar.go). It targets the SAME networks as
//     apps/social, which owns the connected accounts and the publish edge — a
//     scheduled social post has two stores today and apps/social is the one.
//   - Suppression / unsubscribe — a per-org opt-out list enforced at the ONE send
//     seam (suppress.go); every send path passes through it, plus a signed
//     public one-click unsubscribe.
//
// THE ONE SEND SEAM. Every marketing delivery funnels through state.deliver,
// which checks the per-org suppression list and then hands off to the platform
// notify rail (notify.Send) — marketing never builds a second sender.
//
// A PRODUCT ANNOUNCEMENT ("a new model is available") is therefore not a feature
// of its own: it is a one-step sequence with an audience enrolled into it —
// POST /v1/marketing/sequences/:id/enroll with an audienceId instead of an
// address. It reuses the drip engine and so inherits every guarantee already
// proven there — claimed-once delivery, the suppression gate, the signed
// unsubscribe footer — which is precisely why there is no blast engine beside it.
//
// EVERY ROUTE IS A TYPED OP. Each one registers through zip.Get/Post/Put/Delete
// with concrete In/Out structs, so the surface is ONE registry with N
// projections: REST, the OpenAPI document, the MCP tool list and the CLI are all
// derived from these same registrations. Nothing about them is written twice —
// the prose in each handler's doc comment is lifted into the spec by the
// build-time cmd/zipdoc pass, because Go does not keep comments at run time.
//
// TENANT ISOLATION is enforced SERVER-SIDE on every request: the org is the
// value SanitizeIdentity minted from the VALIDATED bearer owner claim (HIP-0026),
// carried to the typed seam by cloud.Bridge and read back with
// principal.OrgFrom — NEVER a client-supplied header and never an In field.
// Every store query filters WHERE org=?, so one tenant can never read or mutate
// another's data. The MCP projection carries no principal, so every org-scoped
// op refuses there through that same gate. serve.go auto-registers
// GET /v1/marketing/health (no OwnsHealth here).
package marketing

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
	"github.com/hanzoai/cloud/internal/shorten"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	// maxField caps a single text field so an unbounded body can't amplify the
	// shared DB or a list response. Campaign fields are short labels.
	maxField = 1024
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
)

// channels is the delivery-surface vocabulary, drawn from the marketing repo's
// platforms (meta/google/tiktok) + channels (email/sms/social). A create/update
// with an unknown channel is rejected; empty defaults to email.
var channels = map[string]bool{
	"email": true, "sms": true, "social": true, "meta": true, "google": true, "tiktok": true,
}

// statuses is the campaign lifecycle. Empty defaults to draft. "scheduled" marks
// a campaign with a future send time (scheduled_at).
var statuses = map[string]bool{
	"draft": true, "scheduled": true, "active": true, "paused": true, "completed": true,
}

// state is marketing's own data; shared deps (logger, brand) live in the embedded
// cloud.Base, reached as s.Log / s.Brand.
type state struct {
	store *Store
	// plans answers "does this org hold a live paid subscription, and which
	// tier?" — the ONLY source of the plan a promo redemption is recorded
	// against (promos.go orgPlan), because the alternative is believing what the
	// request body claims. It is an OPTIONAL capability resolved from
	// deps.Commerce by type-assertion, exactly as SpendGate resolves it, so the
	// narrow types.CommerceClient interface stays untouched. nil when commerce
	// cannot answer (split deploy, disabled stub) — and unlike the spend gate,
	// which admits on an unreadable authority, a nil here REFUSES the redemption.
	plans cloud.PlanChecker
}

// mounted is the active service so Shutdown can release the store, and so the
// durable sweep (DripSweepActivity) reaches the SAME store and KMS the request
// path uses — one engine, one store, one send gate.
var mounted *cloud.Service[state]

// Mount wires the marketing surface onto app per HIP-0106. It keeps a package
// global (mounted) for Shutdown, so it constructs the Service value directly —
// the same "complex flavour" clients/crm uses.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("marketing.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("marketing.Mount: empty DataDir")
	}
	// marketing registers TYPED ops, which live on the *zip.App's registry — the
	// one value OpenAPI, MCP and the CLI are projected from. A Router that is not
	// backed by one must fail the mount rather than serve routes no projection knows.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("marketing.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("marketing.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "marketing")
	// The subscription read is an optional capability on the commerce client; a
	// build that cannot answer yields nil, and promos.go refuses rather than
	// guesses. Asserting on a nil deps.Commerce is safe and also yields nil.
	plans, _ := deps.Commerce.(cloud.PlanChecker)
	s := &cloud.Service[state]{Base: b, State: state{store: store, plans: plans}}
	mounted = s

	routes(app, zapp, s)

	// Bind the drip + calendar engine to the durable tasks clock. The engine is
	// wired after MountAll, so this waits for it in the background (fail-soft: no
	// engine simply means scheduled sends stay pending until one appears).
	go startDrip(context.Background(), b.Log)

	b.Log.Info("marketing mounted", "brand", deps.Brand)
	return nil
}

// routes registers the whole GTM surface (all org-scoped, /v1 only). EVERY route
// is a typed op: zip.<Verb>(zapp, …) registers the route AND the registry entry
// OpenAPI / MCP / the CLI are projected from, and it takes the ABSOLUTE path
// because the registry keys on it.
//
// Registration order is publish order; zip is first-match, and each path here has
// a distinct shape (segment count), so none shadows another.
func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	// Every org-scoped op below resolves its tenant from the request on the context,
	// and whoever composes the app parks it there — at the root, ahead of these
	// leaves, since fiber runs middleware in registration order. This surface
	// installs none of its own: one it installed for itself could only hang on a
	// /v1/marketing node, and every op below registers on zapp, the root app, so
	// that node would carry middleware over an empty subtree and zip refuses to
	// compose it.

	zip.Get(zapp, "/v1/marketing/summary", o.summary)

	// Campaigns (create / schedule / status).
	zip.Get(zapp, "/v1/marketing/campaigns", o.listCampaigns)
	zip.Post(zapp, "/v1/marketing/campaigns", o.createCampaign)
	zip.Get(zapp, "/v1/marketing/campaigns/:id", o.getCampaign)
	zip.Put(zapp, "/v1/marketing/campaigns/:id", o.updateCampaign)
	zip.Post(zapp, "/v1/marketing/campaigns/:id/schedule", o.scheduleCampaign)
	zip.Delete(zapp, "/v1/marketing/campaigns/:id", o.deleteCampaign)

	// Email drip sequences (durable steps on the tasks engine).
	zip.Get(zapp, "/v1/marketing/sequences", o.listSequences)
	zip.Post(zapp, "/v1/marketing/sequences", o.createSequence)
	zip.Get(zapp, "/v1/marketing/sequences/:id", o.getSequence)
	zip.Post(zapp, "/v1/marketing/sequences/:id/status", o.setSequenceStatus)
	zip.Get(zapp, "/v1/marketing/sequences/:id/steps", o.listSteps)
	zip.Post(zapp, "/v1/marketing/sequences/:id/steps", o.addStep)
	zip.Post(zapp, "/v1/marketing/sequences/:id/enroll", o.enroll)
	zip.Get(zapp, "/v1/marketing/sequences/:id/enrollments", o.listEnrollments)
	zip.Post(zapp, "/v1/marketing/sequences/:id/enrollments/:eid/cancel", o.cancelEnrollment)

	// Audiences (cohort filters over the org's analytics events).
	zip.Get(zapp, "/v1/marketing/audiences", o.listAudiences)
	zip.Post(zapp, "/v1/marketing/audiences", o.createAudience)
	zip.Get(zapp, "/v1/marketing/audiences/:id", o.getAudience)
	zip.Get(zapp, "/v1/marketing/audiences/:id/preview", o.previewAudience)
	zip.Delete(zapp, "/v1/marketing/audiences/:id", o.deleteAudience)

	// Promo codes (launch discount → non-cash wallet credit).
	zip.Get(zapp, "/v1/marketing/promos", o.listPromos)
	zip.Get(zapp, "/v1/marketing/promos/:code/eligibility", o.quotePromo)
	zip.Post(zapp, "/v1/marketing/promos/:code/redeem", o.redeemPromo)
	zip.Get(zapp, "/v1/marketing/promos/:code/redemption", o.getRedemption)

	// Content calendar (scheduled posts + task-executed publish hooks).
	zip.Get(zapp, "/v1/marketing/calendar", o.listCalendarPosts)
	zip.Post(zapp, "/v1/marketing/calendar", o.createCalendarPost)
	zip.Get(zapp, "/v1/marketing/calendar/:id", o.getCalendarPost)
	zip.Put(zapp, "/v1/marketing/calendar/:id", o.updateCalendarPost)
	zip.Post(zapp, "/v1/marketing/calendar/:id/publish", o.publishCalendarPost)
	zip.Delete(zapp, "/v1/marketing/calendar/:id", o.deleteCalendarPost)

	// Suppression / unsubscribe. Management is org-scoped; the one-click
	// unsubscribe is PUBLIC (signed token, no principal).
	zip.Get(zapp, "/v1/marketing/suppressions", o.listSuppressions)
	zip.Post(zapp, "/v1/marketing/suppressions", o.addSuppression)
	zip.Delete(zapp, "/v1/marketing/suppressions", o.removeSuppression)
	zip.Get(zapp, "/v1/marketing/unsubscribe", o.unsubscribe)
}

// ---- shared helpers (mirror clients/crm) ----

// ops binds the service to marketing's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listCampaigns). That is
// also the only bound form cmd/zipdoc can lift prose from: it resolves a named
// function or a method to its declaration, while a closure returned by a helper
// is a call expression with nothing to read, so the doc comments would never
// reach the spec. ops therefore carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// clip trims and bounds a text field to maxField.
func clip(s string) string {
	return shorten.To(strings.TrimSpace(s), maxField)
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

// normChannel lower-cases + defaults (empty → email) and validates against the
// fixed vocabulary.
func normChannel(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "email", true
	}
	return v, channels[v]
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

// toString coerces a datastore cell to a string (warehouse distinct_id reads).
func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}

// mapErr maps a sentinel error to the right HTTP error. Non-sentinel errors
// become a 500 with the wrapped message.
func mapErr(err error, notFoundMsg string) error {
	switch {
	case errors.Is(err, errNotFound):
		return zip.ErrNotFound(notFoundMsg)
	case errors.Is(err, errConflict):
		return zip.ErrConflict("already exists")
	case errors.Is(err, errIAMUnavailable), errors.Is(err, errWarehouse):
		return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// ---- campaigns ----

// CampaignRef addresses one campaign.
type CampaignRef struct {
	// ID is the campaign id from the path, as returned by create.
	ID string `json:"id"`
}

// CampaignQuery filters the campaign list.
type CampaignQuery struct {
	// Status keeps only campaigns in that lifecycle state (draft, scheduled,
	// active, paused, completed). Empty means every campaign.
	Status string `json:"status"`
	// Limit caps the rows returned; 0 means 200 and nothing above 1000 is honoured.
	Limit int `json:"limit"`
}

// CampaignList is a page of campaigns, most recently updated first.
type CampaignList struct {
	// Data is the page; an empty array when the org has no matching campaign.
	Data []Campaign `json:"data"`
}

// ScheduleInput sets or clears a campaign's send time.
type ScheduleInput struct {
	// ID is the campaign id from the path.
	ID string `json:"id"`
	// ScheduledAt is the unix send time. 0 clears the schedule.
	ScheduledAt int64 `json:"scheduledAt"`
}

// Summary is the org's campaign roll-up — the marketing overview cards.
type Summary struct {
	// Campaigns is how many campaigns the org has, counted in every lifecycle
	// state including draft and completed.
	Campaigns int `json:"campaigns"`
	// Active is how many of them sit in the "active" state exactly. A scheduled
	// or paused campaign counts in Campaigns and not here, so Active is never a
	// share of anything but the whole.
	Active int `json:"active"`
	// Budget is every campaign's budget summed, in USD cents.
	Budget int64 `json:"budget"`
	// Spend is every campaign's spend summed, in USD cents. It adds up figures
	// callers wrote on the campaigns themselves — this app meters no delivery
	// against a budget — so it is a reported total, not an observed one.
	Spend int64 `json:"spend"`
}

// createCampaign registers a campaign in the caller's org. Name is required;
// channel defaults to email and status to draft, and a future scheduledAt with
// no explicit status makes the campaign "scheduled". Budget and spend are cents
// and are clamped to >= 0. The id, createdAt and updatedAt of the input are
// ignored — the server assigns them.
//
// Example: {"name": "Spring Launch", "channel": "meta", "objective": "signups", "budget": 50000, "scheduledAt": 1780000000}
func (o ops) createCampaign(ctx context.Context, in *Campaign) (*Campaign, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	channel, okCh := normChannel(in.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("channel must be one of email, sms, social, meta, google, tiktok")
	}
	status, okSt := normStatus(in.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	id := mint.ID("camp")
	// A future send time implies the "scheduled" state unless the caller pinned
	// another status explicitly.
	if in.ScheduledAt > 0 && strings.TrimSpace(in.Status) == "" {
		status = "scheduled"
	}
	now := time.Now().Unix()
	saved, err := o.s.State.store.CreateCampaign(ctx, Campaign{
		ID: id, Org: org, Name: name, Channel: channel, Status: status,
		Objective: clip(in.Objective), Budget: nonNeg(in.Budget), Spend: nonNeg(in.Spend),
		ScheduledAt: nonNeg(in.ScheduledAt), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, mapErr(err, "")
	}
	cloud.Created(ctx)
	return &saved, nil
}

// listCampaigns returns the org's campaigns, most recently updated first,
// optionally narrowed to one lifecycle status.
//
// Example: {"status": "active", "limit": 25}
func (o ops) listCampaigns(ctx context.Context, in *CampaignQuery) (*CampaignList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListCampaigns(ctx, org, strings.ToLower(strings.TrimSpace(in.Status)), limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &CampaignList{Data: rows}, nil
}

// getCampaign returns one of the caller org's campaigns. A campaign belonging to
// another org reads as not found.
//
// Example: {"id": "camp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) getCampaign(ctx context.Context, in *CampaignRef) (*Campaign, error) {
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

// updateCampaign replaces a campaign's editable fields. It is a full write, not
// a patch: every field takes the value in the body, and an omitted one is
// cleared. The id comes from the path — the body cannot retarget another
// campaign — and createdAt is never rewritten.
//
// Example: {"name": "Spring Launch", "channel": "meta", "status": "active", "objective": "signups", "budget": 50000, "spend": 12500}
func (o ops) updateCampaign(ctx context.Context, in *Campaign) (*Campaign, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name := clip(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	channel, okCh := normChannel(in.Channel)
	if !okCh {
		return nil, zip.ErrBadRequest("channel must be one of email, sms, social, meta, google, tiktok")
	}
	status, okSt := normStatus(in.Status)
	if !okSt {
		return nil, zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	saved, err := o.s.State.store.UpdateCampaign(ctx, Campaign{
		ID: strings.TrimSpace(in.ID), Org: org, Name: name, Channel: channel, Status: status,
		Objective: clip(in.Objective), Budget: nonNeg(in.Budget), Spend: nonNeg(in.Spend),
		ScheduledAt: nonNeg(in.ScheduledAt), UpdatedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
}

// deleteCampaign removes one of the caller org's campaigns and answers 204. A
// campaign belonging to another org reads as not found and is left untouched.
//
// Example: {"id": "camp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60"}
func (o ops) deleteCampaign(ctx context.Context, in *CampaignRef) (*struct{}, error) {
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

// scheduleCampaign sets a campaign's send time and moves it to "scheduled". A
// scheduledAt of 0 clears the schedule and returns it to "draft".
//
// Example: {"id": "camp_9f2a1c7d4e8b0a6f3d2c5b1e7a9f4c60", "scheduledAt": 1780000000}
func (o ops) scheduleCampaign(ctx context.Context, in *ScheduleInput) (*Campaign, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	camp, err := o.s.State.store.GetCampaign(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	camp.ScheduledAt = nonNeg(in.ScheduledAt)
	if camp.ScheduledAt > 0 {
		camp.Status = "scheduled"
	} else if camp.Status == "scheduled" {
		camp.Status = "draft"
	}
	camp.UpdatedAt = time.Now().Unix()
	saved, err := o.s.State.store.UpdateCampaign(ctx, camp)
	if err != nil {
		return nil, mapErr(err, "campaign not found")
	}
	return &saved, nil
}

// ---- summary ----

// summary rolls up the caller org's campaigns: how many there are, how many are
// active, and the summed budget and spend in cents.
//
// Response: {"campaigns": 12, "active": 3, "budget": 500000, "spend": 128400}
func (o ops) summary(ctx context.Context, _ *struct{}) (*Summary, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	total, active, budget, spend, err := o.s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &Summary{Campaigns: total, Active: active, Budget: budget, Spend: spend}, nil
}

// Shutdown drains the drip poller and closes the marketing store. Idempotent.
func Shutdown() error {
	stopDrip()
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
