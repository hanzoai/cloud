// Package marketing mounts the Hanzo Cloud /v1/marketing/* surface: the native-Go
// GTM engine folded from github.com/hanzoai/marketing onto the ONE cloud
// framework (zip/Fiber + cloud.Deps + per-org SQLite), the twin of clients/crm —
// NOT a proxy to a standalone pod, and nothing Python in the mount path. The
// marketing repo keeps the CONTENT system (GTM.md, checklist.yaml, ads/,
// analysis/, discounts.md) as data; this is the engine that acts on it.
//
// Subsystems (all org-scoped, /v1 only):
//
//   - Campaigns — named campaign on a delivery Channel (email/sms/social/meta/
//     google/tiktok), a lifecycle Status (draft/scheduled/active/paused/
//     completed), Budget/Spend in cents, and a send time (scheduled_at).
//   - Email sequences — ordered drip Steps sent as DURABLE tasks on the embedded
//     hanzoai/tasks engine (drip.go): each enrollment's next_run_at lives in
//     SQLite, a per-minute engine schedule sweeps due steps, every step is
//     claimed once (idempotent) and delivered through the ONE send gate.
//   - Audiences — cohort filters evaluated live against the org's analytics
//     events (hanzo.events), honest-empty when the warehouse is not wired.
//   - Promo codes — the "First 1,000: 90% off month 1" launch promo (discounts.md)
//     realized as a non-cash wallet credit through the finance ledger, with the
//     hard 1,000 cap, one-per-org, one-per-instrument and team-seat-cap guards.
//   - Content calendar — scheduled posts as documents, published by a
//     task-executed hook; social publish returns an honest 501 until a connector
//     is wired (see calendar.go).
//   - Suppression / unsubscribe — a per-org opt-out list enforced at the ONE send
//     seam (suppress.go); every send path passes through it, plus a signed
//     public one-click unsubscribe.
//
// THE ONE SEND SEAM. Every marketing delivery funnels through state.deliver,
// which checks the per-org suppression list and then hands off to the platform
// notify rail (notify.Send) — marketing never builds a second sender.
//
// TENANT ISOLATION is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
// serve.go auto-registers GET /v1/marketing/health (no OwnsHealth here).
package marketing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the marketing surface onto app per HIP-0106. It keeps a package
// global (mounted) for Shutdown, so it constructs the Service value directly —
// the same "complex flavour" clients/crm uses.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("marketing.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("marketing.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("marketing.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("marketing.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "marketing.db"))
	if err != nil {
		return fmt.Errorf("marketing.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "marketing")
	s := &cloud.Service[state]{Base: b, State: state{store: store}}
	mounted = s

	routes(app, s)

	// Bind the drip + calendar engine to the durable tasks clock. The engine is
	// wired after MountAll, so this waits for it in the background (fail-soft: no
	// engine simply means scheduled sends stay pending until one appears).
	go startDrip(context.Background(), b.Log)

	b.Log.Info("marketing mounted", "brand", deps.Brand)
	return nil
}

// routes registers the whole GTM surface (all org-scoped, /v1 only). Route
// registration order is publish order; zip is first-match, and each path here has
// a distinct shape (segment count), so none shadows another.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/marketing/summary", cloud.Handle(s, summary))

	// Campaigns (create / schedule / status).
	app.Get("/v1/marketing/campaigns", cloud.Handle(s, listCampaigns))
	app.Post("/v1/marketing/campaigns", cloud.Handle(s, createCampaign))
	app.Get("/v1/marketing/campaigns/:id", cloud.Handle(s, getCampaign))
	app.Put("/v1/marketing/campaigns/:id", cloud.Handle(s, updateCampaign))
	app.Post("/v1/marketing/campaigns/:id/schedule", cloud.Handle(s, scheduleCampaign))
	app.Delete("/v1/marketing/campaigns/:id", cloud.Handle(s, deleteCampaign))

	// Email drip sequences (durable steps on the tasks engine).
	app.Get("/v1/marketing/sequences", cloud.Handle(s, listSequences))
	app.Post("/v1/marketing/sequences", cloud.Handle(s, createSequence))
	app.Get("/v1/marketing/sequences/:id", cloud.Handle(s, getSequence))
	app.Post("/v1/marketing/sequences/:id/status", cloud.Handle(s, setSequenceStatus))
	app.Get("/v1/marketing/sequences/:id/steps", cloud.Handle(s, listStepsHandler))
	app.Post("/v1/marketing/sequences/:id/steps", cloud.Handle(s, addStep))
	app.Post("/v1/marketing/sequences/:id/enroll", cloud.Handle(s, enroll))
	app.Get("/v1/marketing/sequences/:id/enrollments", cloud.Handle(s, listEnrollments))
	app.Post("/v1/marketing/sequences/:id/enrollments/:eid/cancel", cloud.Handle(s, cancelEnrollment))

	// Audiences (cohort filters over the org's analytics events).
	app.Get("/v1/marketing/audiences", cloud.Handle(s, listAudiences))
	app.Post("/v1/marketing/audiences", cloud.Handle(s, createAudience))
	app.Get("/v1/marketing/audiences/:id", cloud.Handle(s, getAudience))
	app.Get("/v1/marketing/audiences/:id/preview", cloud.Handle(s, previewAudience))
	app.Delete("/v1/marketing/audiences/:id", cloud.Handle(s, deleteAudience))

	// Promo codes (launch discount → non-cash wallet credit).
	app.Get("/v1/marketing/promos", cloud.Handle(s, listPromos))
	app.Get("/v1/marketing/promos/:code/eligibility", cloud.Handle(s, quotePromo))
	app.Post("/v1/marketing/promos/:code/redeem", cloud.Handle(s, redeemPromo))
	app.Get("/v1/marketing/promos/:code/redemption", cloud.Handle(s, getRedemption))

	// Content calendar (scheduled posts + task-executed publish hooks).
	app.Get("/v1/marketing/calendar", cloud.Handle(s, listCalendarPosts))
	app.Post("/v1/marketing/calendar", cloud.Handle(s, createCalendarPost))
	app.Get("/v1/marketing/calendar/:id", cloud.Handle(s, getCalendarPost))
	app.Put("/v1/marketing/calendar/:id", cloud.Handle(s, updateCalendarPost))
	app.Post("/v1/marketing/calendar/:id/publish", cloud.Handle(s, publishCalendarPost))
	app.Delete("/v1/marketing/calendar/:id", cloud.Handle(s, deleteCalendarPost))

	// Suppression / unsubscribe. Management is org-scoped; the one-click
	// unsubscribe is PUBLIC (signed token, no principal).
	app.Get("/v1/marketing/suppressions", cloud.Handle(s, listSuppressions))
	app.Post("/v1/marketing/suppressions", cloud.Handle(s, addSuppression))
	app.Delete("/v1/marketing/suppressions", cloud.Handle(s, removeSuppression))
	app.Get("/v1/marketing/unsubscribe", cloud.Handle(s, unsubscribe))
}

// ---- shared helpers (mirror clients/crm) ----

// tenant resolves the org — the tenant-isolation KEY — for a request. It uses
// principal.Org EXACTLY as SanitizeIdentity minted it from the validated IAM
// owner claim (HIP-0026): never lowercased, stripped, or truncated.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

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

func limitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
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

// toInt64 coerces a datastore cell (which the driver may return as any numeric
// Go type) to int64. Used for warehouse aggregate reads (audience counts).
func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	default:
		return 0
	}
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

func createCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Campaign
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	channel, okCh := normChannel(body.Channel)
	if !okCh {
		return zip.ErrBadRequest("channel must be one of email, sms, social, meta, google, tiktok")
	}
	status, okSt := normStatus(body.Status)
	if !okSt {
		return zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	id, err := genID("camp")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	// A future send time implies the "scheduled" state unless the caller pinned
	// another status explicitly.
	if body.ScheduledAt > 0 && strings.TrimSpace(body.Status) == "" {
		status = "scheduled"
	}
	now := time.Now().Unix()
	camp := Campaign{
		ID: id, Org: org, Name: name, Channel: channel, Status: status,
		Objective: clip(body.Objective), Budget: nonNeg(body.Budget), Spend: nonNeg(body.Spend),
		ScheduledAt: nonNeg(body.ScheduledAt), CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreateCampaign(c.Context(), camp)
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func listCampaigns(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	rows, err := s.State.store.ListCampaigns(c.Context(), org, status, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, camp)
}

func updateCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body Campaign
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	channel, okCh := normChannel(body.Channel)
	if !okCh {
		return zip.ErrBadRequest("channel must be one of email, sms, social, meta, google, tiktok")
	}
	status, okSt := normStatus(body.Status)
	if !okSt {
		return zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	camp := Campaign{
		ID: idParam(c), Org: org, Name: name, Channel: channel, Status: status,
		Objective: clip(body.Objective), Budget: nonNeg(body.Budget), Spend: nonNeg(body.Spend),
		ScheduledAt: nonNeg(body.ScheduledAt), UpdatedAt: time.Now().Unix(),
	}
	saved, err := s.State.store.UpdateCampaign(c.Context(), camp)
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func deleteCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.State.store.DeleteCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("campaign not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// scheduleCampaign sets a campaign's send time and moves it to "scheduled". A
// scheduledAt of 0 clears the schedule and returns it to "draft".
func scheduleCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("org scope required")
	}
	var body struct {
		ScheduledAt int64 `json:"scheduledAt"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	camp.ScheduledAt = nonNeg(body.ScheduledAt)
	if camp.ScheduledAt > 0 {
		camp.Status = "scheduled"
	} else if camp.Status == "scheduled" {
		camp.Status = "draft"
	}
	camp.UpdatedAt = time.Now().Unix()
	saved, err := s.State.store.UpdateCampaign(c.Context(), camp)
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// ---- summary ----

func summary(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	total, active, budget, spend, err := s.State.store.Counts(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"campaigns": total, "active": active, "budget": budget, "spend": spend,
	})
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
