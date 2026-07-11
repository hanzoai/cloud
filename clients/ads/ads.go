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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("ads.Mount: nil zip.App")
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

// routes registers the ads surface: the campaign CRUD + the summary roll-up.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/ads/summary", cloud.Handle(s, summary))

	app.Get("/v1/ads/campaigns", cloud.Handle(s, listCampaigns))
	app.Post("/v1/ads/campaigns", cloud.Handle(s, createCampaign))
	app.Get("/v1/ads/campaigns/:id", cloud.Handle(s, getCampaign))
	app.Put("/v1/ads/campaigns/:id", cloud.Handle(s, updateCampaign))
	app.Delete("/v1/ads/campaigns/:id", cloud.Handle(s, deleteCampaign))
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
	platform, okPl := normPlatform(body.Platform)
	if !okPl {
		return zip.ErrBadRequest("platform must be one of meta, google, tiktok, x")
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
		ID: id, Org: org, Name: name, Platform: platform, Status: status,
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
	platform, okPl := normPlatform(body.Platform)
	if !okPl {
		return zip.ErrBadRequest("platform must be one of meta, google, tiktok, x")
	}
	status, okSt := normStatus(body.Status)
	if !okSt {
		return zip.ErrBadRequest("status must be one of draft, active, paused, completed")
	}
	camp := Campaign{
		ID: idParam(c), Org: org, Name: name, Platform: platform, Status: status,
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

// Shutdown closes the ads store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
