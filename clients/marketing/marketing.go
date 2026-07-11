// Package marketing mounts the Hanzo Cloud /v1/marketing/* surface: a native-Go,
// per-org marketing-campaign store on Base/SQLite. It is the in-process fold of
// github.com/hanzoai/marketing (a gin service coupled to the external commerce
// datastore) onto the ONE cloud framework (zip/Fiber + cloud.Deps + per-org
// SQLite) — the same shape every other in-repo subsystem uses (clients/crm is the
// twin), NOT a proxy to a standalone marketing pod.
//
// The Campaign entity is faithful to the marketing repo's domain: a named
// campaign on a delivery Channel (email/sms/social/meta/google/tiktok — the repo's
// platforms + channels), a lifecycle Status (draft/active/paused/completed), an
// Objective, and Budget/Spend in minor units (cents). The genetic-optimizer / ML
// forecasting / ad-platform integrations from the Python side (src/marketing/*)
// are NOT folded here yet — this is the reviewable domain + CRUD seam they hang
// off (see the package README note in the fold report).
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, so one tenant can never read or mutate another's data.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/marketing/summary            per-org roll-up (total/active/budget/spend)
//	GET    /v1/marketing/campaigns          list campaigns (?status=)      -> {data:[…]}
//	POST   /v1/marketing/campaigns          create a campaign              -> Campaign (201)
//	GET    /v1/marketing/campaigns/:id      campaign detail                -> Campaign
//	PUT    /v1/marketing/campaigns/:id      update a campaign              -> Campaign
//	DELETE /v1/marketing/campaigns/:id      delete a campaign
//
// serve.go auto-registers GET /v1/marketing/health (this subsystem does not set
// OwnsHealth, so the generic always-ok liveness route serves it).
package marketing

import (
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

// statuses is the campaign lifecycle. Empty defaults to draft.
var statuses = map[string]bool{
	"draft": true, "active": true, "paused": true, "completed": true,
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

	b.Log.Info("marketing mounted", "brand", deps.Brand)
	return nil
}

// routes registers the marketing surface: the campaign CRUD + the summary roll-up.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/marketing/summary", cloud.Handle(s, summary))

	app.Get("/v1/marketing/campaigns", cloud.Handle(s, listCampaigns))
	app.Post("/v1/marketing/campaigns", cloud.Handle(s, createCampaign))
	app.Get("/v1/marketing/campaigns/:id", cloud.Handle(s, getCampaign))
	app.Put("/v1/marketing/campaigns/:id", cloud.Handle(s, updateCampaign))
	app.Delete("/v1/marketing/campaigns/:id", cloud.Handle(s, deleteCampaign))
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
	now := time.Now().Unix()
	camp := Campaign{
		ID: id, Org: org, Name: name, Channel: channel, Status: status,
		Objective: clip(body.Objective), Budget: nonNeg(body.Budget), Spend: nonNeg(body.Spend),
		CreatedAt: now, UpdatedAt: now,
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
		UpdatedAt: time.Now().Unix(),
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

// Shutdown closes the marketing store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
