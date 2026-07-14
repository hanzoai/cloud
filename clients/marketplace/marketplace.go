// Package marketplace is the /v1/marketplace surface: listing, discovery, and
// install of tools + agents per org/project, plus monetized listings that declare a
// price + recipient wallet and enforce through the x402 seam.
//
// It is a THIN layer over the unified tool plane (clients/tools): discovery reads
// the tool registry (every source, activated flags); "install"/"uninstall" ARE the
// registry's activation writes (marketplace install == tool activation — one store,
// one truth); and a monetized listing's price reaches per-call enforcement via the
// registry's Pricer seam (this package fills it), settled by whatever x402 Charger
// the payments team wires. Marketplace never dispatches a tool itself.
//
// Surface (all org-gated, /v1 only):
//
//	GET    /v1/marketplace                 discovery: catalog (tools+agents) + listing overlay + installed flag
//	GET    /v1/marketplace/listings        the caller org's own published listings
//	POST   /v1/marketplace/listings        publish a listing (optionally monetized)
//	DELETE /v1/marketplace/listings/:id    unpublish
//	POST   /v1/marketplace/install         install (activate) a tool for the caller's (org,project)
//	POST   /v1/marketplace/uninstall       uninstall (deactivate) a tool
package marketplace

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/tools"
	"github.com/zap-proto/zip"
)

const (
	maxTitle = 200
	maxText  = 4096
	maxName  = 128
)

type state struct {
	store *Store
	audit *audit.Recorder
}

var mounted *cloud.Service[state]

// Mount wires /v1/marketplace/* and installs the marketplace Pricer on the tool
// plane so a published listing's price is enforced at dispatch.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("marketplace.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("marketplace.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("marketplace.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("marketplace.Mount: data dir: %w", err)
	}
	store, err := Open(filepath.Join(deps.DataDir, "marketplace.db"))
	if err != nil {
		return fmt.Errorf("marketplace.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "marketplace"), State: state{store: store, audit: deps.Audit}}
	mounted = s

	// Fill the tool plane's price seam: a monetized listing's price + recipient
	// reach per-call dispatch enforcement without tools importing marketplace.
	tools.SetPricer(&pricer{store: store})

	app.Get("/v1/marketplace", cloud.Handle(s, discover))
	app.Get("/v1/marketplace/listings", cloud.Handle(s, listListings))
	app.Post("/v1/marketplace/listings", cloud.Handle(s, publish))
	app.Delete("/v1/marketplace/listings/:id", cloud.Handle(s, unpublish))
	app.Post("/v1/marketplace/install", cloud.Handle(s, install))
	app.Post("/v1/marketplace/uninstall", cloud.Handle(s, uninstall))

	s.Log.Info("marketplace mounted", "prefix", "/v1/marketplace", "brand", deps.Brand)
	return nil
}

// Shutdown closes the store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.State.store != nil {
		err = mounted.State.store.Close()
	}
	mounted = nil
	return err
}

// ── discovery ───────────────────────────────────────────────────────────────────

// marketItem is a discoverable capability: the registry tool enriched with any
// public listing's shop metadata + whether the caller has it installed (activated).
type marketItem struct {
	tools.Tool
	Title     string `json:"title,omitempty"`
	Category  string `json:"category,omitempty"`
	Installed bool   `json:"installed"`
}

func discover(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	project := principal.Project(c)
	catalog := tools.Default().List(c.Context(), tools.Scope{Org: org, Project: project})
	byTool, err := s.State.store.PublicByTool(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "listings: %v", err)
	}
	out := make([]marketItem, 0, len(catalog))
	for _, t := range catalog {
		item := marketItem{Tool: t, Installed: t.Activated}
		if l, ok := byTool[t.Name]; ok {
			item.Title = l.Title
			item.Category = l.Category
			if l.PriceCents > 0 {
				item.Price = &tools.Price{AmountCents: l.PriceCents, Currency: l.Currency, Recipient: l.Recipient}
			}
		}
		out = append(out, item)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

// ── listings (publish / unpublish) ──────────────────────────────────────────────

func listListings(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := s.State.store.ListByOrg(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	if rows == nil {
		rows = []Listing{}
	}
	return c.JSON(http.StatusOK, map[string]any{"listings": rows})
}

type publishReq struct {
	Tool        string `json:"tool"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	PriceCents  int64  `json:"priceCents"`
	Currency    string `json:"currency"`
	Recipient   string `json:"recipient"`
	Public      bool   `json:"public"`
}

// publish records a listing for a tool the publisher can actually offer (it must
// resolve in the publisher's scope). A monetized listing (PriceCents>0) MUST name a
// recipient wallet — the x402 payout target. Ownership policy (who may monetize a
// platform-owned tool) is a follow-on; existence is enforced here.
func publish(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body publishReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	body.Tool = strings.TrimSpace(body.Tool)
	body.Title = strings.TrimSpace(body.Title)
	if body.Tool == "" || len(body.Tool) > maxName {
		return zip.ErrBadRequest("tool is required")
	}
	if body.Title == "" || len(body.Title) > maxTitle {
		return zip.ErrBadRequest("title is required (<=200 chars)")
	}
	if len(body.Description) > maxText {
		return zip.ErrBadRequest("description too long")
	}
	if body.PriceCents < 0 {
		return zip.ErrBadRequest("priceCents must be >= 0")
	}
	if body.PriceCents > 0 && strings.TrimSpace(body.Recipient) == "" {
		return zip.ErrBadRequest("a monetized listing requires a recipient wallet")
	}
	// The tool must exist in the publisher's scope — no phantom listings.
	if !tools.Default().Exists(c.Context(), tools.Scope{Org: org, Project: principal.Project(c)}, body.Tool) {
		return zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", body.Tool)
	}
	created, err := s.State.store.Create(c.Context(), Listing{
		PublisherOrg: org, Tool: body.Tool, Title: body.Title, Description: clip(body.Description),
		Category: clip(body.Category), PriceCents: body.PriceCents, Currency: strings.TrimSpace(body.Currency),
		Recipient: strings.TrimSpace(body.Recipient), Public: body.Public,
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "publish: %v", err)
	}
	record(s, c, org, "listing:"+created.ID, "published", http.StatusCreated)
	return c.JSON(http.StatusCreated, created)
}

func unpublish(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := strings.TrimSpace(c.Param("id"))
	removed, err := s.State.store.Delete(c.Context(), org, id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "unpublish: %v", err)
	}
	if !removed {
		return zip.ErrNotFound("listing not found")
	}
	record(s, c, org, "listing:"+id, "unpublished", http.StatusOK)
	return c.NoContent(http.StatusNoContent)
}

// ── install / uninstall (== tool activation) ────────────────────────────────────

type installReq struct {
	Tool string `json:"tool"`
}

// install activates a tool for the caller's (org, project) — the marketplace
// "install" IS the tool-plane activation write, so the two never drift.
func install(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body installReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	body.Tool = strings.TrimSpace(body.Tool)
	if body.Tool == "" || len(body.Tool) > maxName {
		return zip.ErrBadRequest("tool is required")
	}
	project := principal.Project(c)
	if !tools.Default().Exists(c.Context(), tools.Scope{Org: org, Project: project}, body.Tool) {
		return zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", body.Tool)
	}
	if err := tools.Default().Activate(c.Context(), org, project, body.Tool, c.User()); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "install: %v", err)
	}
	record(s, c, org, "install:"+body.Tool, "installed", http.StatusOK)
	return c.JSON(http.StatusOK, map[string]any{"tool": body.Tool, "installed": true})
}

func uninstall(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body installReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	body.Tool = strings.TrimSpace(body.Tool)
	if body.Tool == "" {
		return zip.ErrBadRequest("tool is required")
	}
	if err := tools.Default().Deactivate(c.Context(), org, principal.Project(c), body.Tool); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "uninstall: %v", err)
	}
	record(s, c, org, "install:"+body.Tool, "uninstalled", http.StatusOK)
	return c.JSON(http.StatusOK, map[string]any{"tool": body.Tool, "installed": false})
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func clip(s string) string {
	if len(s) > maxText {
		return s[:maxText]
	}
	return strings.TrimSpace(s)
}

func record(s *cloud.Service[state], c *zip.Ctx, org, resourceID, result string, status int) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    "marketplace." + result,
		Resource:  audit.Resource{Type: "marketplace", ID: resourceID},
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: c.IsAdmin()},
		Outcome:   audit.Outcome{Result: result, Status: status},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  cloud.ClientIP(c),
		RequestID: c.RequestID(),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("audit append failed", "err", err)
	}
}
