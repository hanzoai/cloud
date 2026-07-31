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
//
// EVERY ROUTE IS A TYPED OP. Each registers through zip.Get/Post/Delete with
// concrete In/Out structs, so the surface is ONE registry with N projections:
// REST, the OpenAPI document, the MCP tool list and the CLI all derive from these
// same registrations, and each handler's doc comment is lifted into the spec by
// the build-time cmd/zipdoc pass.
package marketplace

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("marketplace.Mount: nil app")
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

	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("marketplace.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below resolves
	// its tenant through it. Bounded to marketplace's own subtree, which the bare
	// surface path is part of.
	app.Group("/v1/marketplace").Use(cloud.Bridge())

	// Surface root stays flat: the full path is spelled on each op, so
	// "/v1/marketplace" is the bare surface path and not "/v1/marketplace/".
	zip.Get(zapp, "/v1/marketplace", o.discover)
	zip.Get(zapp, "/v1/marketplace/listings", o.listListings)
	zip.Post(zapp, "/v1/marketplace/listings", o.publish, zip.WithStatus(http.StatusCreated))
	zip.Delete(zapp, "/v1/marketplace/listings/:id", o.unpublish)
	zip.Post(zapp, "/v1/marketplace/install", o.install)
	zip.Post(zapp, "/v1/marketplace/uninstall", o.uninstall)

	s.Log.Info("marketplace mounted", "prefix", "/v1/marketplace", "brand", deps.Brand)
	return nil
}

// ops binds the service to marketplace's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// None is the input of an op that takes none: no body, no query, no path param.
type None struct{}

// tenant resolves the org — the tenant-isolation KEY — for an org-scoped op. It
// is EXACTLY what SanitizeIdentity minted from the validated IAM owner claim,
// carried across the typed seam by cloud.Bridge: never read from the input,
// because an input is what the caller says about itself.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// scopeOf pairs the resolved org with the caller's project, which narrows the
// caller's OWN org and lives on the request rather than the typed input.
func scopeOf(ctx context.Context, org string) tools.Scope {
	sc := tools.Scope{Org: org}
	if c, ok := cloud.Request(ctx); ok {
		sc.Project = principal.Project(c)
	}
	return sc
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

// MarketItem is a discoverable capability: the registry tool enriched with any
// public listing's shop metadata + whether the caller has it installed (activated).
//
// The tool's own fields are spelled out rather than embedded: Go INLINES an
// embedded struct's fields into the object, but the schema projector renders an
// embedded field as a nested $ref property — so an embedded tools.Tool would put
// a "Tool" object in every generated client that the wire never carries.
type MarketItem struct {
	// Name is the tool's registry name, unique in the caller's scope.
	Name string `json:"name"`
	// Source is the plane the tool comes from (builtin, mcp, connector, ...).
	Source tools.Source `json:"source"`
	// Description is the tool's own one-line description.
	Description string `json:"description"`
	// Schema is the tool's JSON-Schema input contract, absent when it declares none.
	Schema json.RawMessage `json:"inputSchema,omitempty"`
	// Price is the per-call price, present only for a monetized listing.
	Price *ListingPrice `json:"price,omitempty"`
	// Dispatchable reports whether the tool can be called, not merely listed.
	Dispatchable bool `json:"dispatchable"`
	// Activated reports whether the tool is switched on for the caller's scope.
	Activated bool `json:"activated"`
	// Title is the listing's shop title, present only for a listed tool.
	Title string `json:"title,omitempty"`
	// Category is the listing's shop category, present only for a listed tool.
	Category string `json:"category,omitempty"`
	// Installed reports whether the tool is activated for the caller's scope.
	Installed bool `json:"installed"`
}

// ListingPrice is a monetized listing's per-call price, the same wire object the
// tool plane carries. It is declared here rather than reused by reference because
// a schema name is FLEET-wide: two apps rendering one Go type under one name with
// different field prose is a name the woven document cannot resolve.
type ListingPrice struct {
	// AmountCents is the per-call price in minor units.
	AmountCents int64 `json:"amountCents"`
	// Currency is the ISO 4217 code; empty means USD.
	Currency string `json:"currency"`
	// Recipient is the seller payout wallet the x402 settlement targets.
	Recipient string `json:"recipient"`
}

// Discovery is the discovery answer: every capability resolvable in the caller's scope.
type Discovery struct {
	// Items is the catalog, one entry per resolvable tool or agent.
	Items []MarketItem `json:"items"`
}

// discover lists every tool and agent resolvable in the caller's (org, project),
// overlaid with any public listing's title, category and price, and flags the
// ones the caller already has installed.
//
// Response: {"items": [{"name": "conn_alpha", "installed": true, "title": "Alpha"}]}
func (o ops) discover(ctx context.Context, _ *None) (*Discovery, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	catalog := tools.Default().List(ctx, scopeOf(ctx, org))
	byTool, err := o.s.State.store.PublicByTool(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "listings: %v", err)
	}
	out := make([]MarketItem, 0, len(catalog))
	for _, t := range catalog {
		item := MarketItem{
			Name: t.Name, Source: t.Source, Description: t.Description, Schema: t.Schema,
			Price: price(t.Price), Dispatchable: t.Dispatchable, Activated: t.Activated,
			Installed: t.Activated,
		}
		if l, ok := byTool[t.Name]; ok {
			item.Title = l.Title
			item.Category = l.Category
			if l.PriceCents > 0 {
				item.Price = &ListingPrice{AmountCents: l.PriceCents, Currency: l.Currency, Recipient: l.Recipient}
			}
		}
		out = append(out, item)
	}
	return &Discovery{Items: out}, nil
}

// price projects the tool plane's price onto the marketplace wire object. nil in,
// nil out — an unpriced tool carries no price key.
func price(p *tools.Price) *ListingPrice {
	if p == nil {
		return nil
	}
	return &ListingPrice{AmountCents: p.AmountCents, Currency: p.Currency, Recipient: p.Recipient}
}

// ── listings (publish / unpublish) ──────────────────────────────────────────────

// ListingList is the caller org's own published listings.
type ListingList struct {
	// Listings is the caller org's listings; empty, never null, when it has none.
	Listings []Listing `json:"listings"`
}

// listListings returns the listings the caller's org has published, monetized or not.
func (o ops) listListings(ctx context.Context, _ *None) (*ListingList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListByOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	if rows == nil {
		rows = []Listing{}
	}
	return &ListingList{Listings: rows}, nil
}

// PublishRequest is a new listing: which tool to offer, how to describe it in the
// shop, and — for a monetized listing — at what price and to which wallet.
type PublishRequest struct {
	// Tool is the registry tool name to list; it must resolve in the publisher's scope.
	Tool string `json:"tool"`
	// Title is the shop title, required, at most 200 characters.
	Title string `json:"title"`
	// Description is the shop copy, at most 4096 characters.
	Description string `json:"description"`
	// Category groups the listing in discovery.
	Category string `json:"category"`
	// PriceCents is the per-call price in minor units; 0 publishes it free.
	PriceCents int64 `json:"priceCents"`
	// Currency is the price's currency code.
	Currency string `json:"currency"`
	// Recipient is the seller payout wallet, required when priceCents is above 0.
	Recipient string `json:"recipient"`
	// Public exposes the listing in every org's discovery, not just the publisher's.
	Public bool `json:"public"`
}

// publish records a listing for a tool the publisher can actually offer (it must
// resolve in the publisher's scope). A monetized listing (priceCents>0) MUST name a
// recipient wallet — the x402 payout target. Ownership policy (who may monetize a
// platform-owned tool) is a follow-on; existence is enforced here.
//
// Example: {"tool": "conn_alpha", "title": "Alpha", "priceCents": 500, "currency": "usd", "recipient": "0xabc", "public": true}
func (o ops) publish(ctx context.Context, in *PublishRequest) (*Listing, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	body.Tool = strings.TrimSpace(body.Tool)
	body.Title = strings.TrimSpace(body.Title)
	if body.Tool == "" || len(body.Tool) > maxName {
		return nil, zip.ErrBadRequest("tool is required")
	}
	if body.Title == "" || len(body.Title) > maxTitle {
		return nil, zip.ErrBadRequest("title is required (<=200 chars)")
	}
	if len(body.Description) > maxText {
		return nil, zip.ErrBadRequest("description too long")
	}
	if body.PriceCents < 0 {
		return nil, zip.ErrBadRequest("priceCents must be >= 0")
	}
	if body.PriceCents > 0 && strings.TrimSpace(body.Recipient) == "" {
		return nil, zip.ErrBadRequest("a monetized listing requires a recipient wallet")
	}
	// The tool must exist in the publisher's scope — no phantom listings.
	if !tools.Default().Exists(ctx, scopeOf(ctx, org), body.Tool) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", body.Tool)
	}
	created, err := o.s.State.store.Create(ctx, Listing{
		PublisherOrg: org, Tool: body.Tool, Title: body.Title, Description: clip(body.Description),
		Category: clip(body.Category), PriceCents: body.PriceCents, Currency: strings.TrimSpace(body.Currency),
		Recipient: strings.TrimSpace(body.Recipient), Public: body.Public,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "publish: %v", err)
	}
	record(o.s, ctx, org, "listing:"+created.ID, "published", http.StatusCreated)
	return &created, nil
}

// ListingRef addresses one of the caller org's listings by id.
type ListingRef struct {
	// ID is the listing id from the path, as returned by publish.
	ID string `json:"id"`
}

// unpublish removes one of the caller org's listings and answers 204. A listing
// belonging to another org reads as not found.
//
// Example: {"id": "lst_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) unpublish(ctx context.Context, in *ListingRef) (*struct{}, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	removed, err := o.s.State.store.Delete(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "unpublish: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("listing not found")
	}
	record(o.s, ctx, org, "listing:"+id, "unpublished", http.StatusOK)
	return nil, nil
}

// ── install / uninstall (== tool activation) ────────────────────────────────────

// InstallRequest names the tool to install or uninstall.
type InstallRequest struct {
	// Tool is the registry tool name.
	Tool string `json:"tool"`
}

// InstallResult reports the tool's activation state after the write.
type InstallResult struct {
	// Tool is the tool the write applied to.
	Tool string `json:"tool"`
	// Installed is the resulting activation state for the caller's (org, project).
	Installed bool `json:"installed"`
}

// install activates a tool for the caller's (org, project) — the marketplace
// "install" IS the tool-plane activation write, so the two never drift.
//
// Example: {"tool": "conn_alpha"}
// Response: {"tool": "conn_alpha", "installed": true}
func (o ops) install(ctx context.Context, in *InstallRequest) (*InstallResult, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Tool)
	if name == "" || len(name) > maxName {
		return nil, zip.ErrBadRequest("tool is required")
	}
	scope := scopeOf(ctx, org)
	if !tools.Default().Exists(ctx, scope, name) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", name)
	}
	if err := tools.Default().Activate(ctx, org, scope.Project, name, userOf(ctx)); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "install: %v", err)
	}
	record(o.s, ctx, org, "install:"+name, "installed", http.StatusOK)
	return &InstallResult{Tool: name, Installed: true}, nil
}

// uninstall deactivates a tool for the caller's (org, project).
//
// Example: {"tool": "conn_alpha"}
// Response: {"tool": "conn_alpha", "installed": false}
func (o ops) uninstall(ctx context.Context, in *InstallRequest) (*InstallResult, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Tool)
	if name == "" {
		return nil, zip.ErrBadRequest("tool is required")
	}
	if err := tools.Default().Deactivate(ctx, org, scopeOf(ctx, org).Project, name); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "uninstall: %v", err)
	}
	record(o.s, ctx, org, "install:"+name, "uninstalled", http.StatusOK)
	return &InstallResult{Tool: name, Installed: false}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func clip(s string) string {
	if len(s) > maxText {
		return s[:maxText]
	}
	return strings.TrimSpace(s)
}

// userOf is the validated caller's subject, taken off the request the bridge
// parked. Empty off the HTTP path, where there is no caller to name.
func userOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}

func record(s *cloud.Service[state], ctx context.Context, org, resourceID, result string, status int) {
	if s.State.audit == nil {
		return
	}
	c, ok := cloud.Request(ctx)
	if !ok {
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
