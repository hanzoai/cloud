// Package marketplace is the shop for tools and agents: browse, install into
// your project, publish your own free or priced.
//
// It serves listing, discovery and install per org/project at /v1/marketplace.
// A monetized listing declares a price + recipient wallet and enforces through
// the x402 seam.
//
// It is a THIN layer over the unified tool plane (apps/tools): discovery reads
// the tool registry (every source, activated flags); "install"/"uninstall" ARE the
// registry's activation writes (marketplace install == tool activation — one store,
// one truth); and a monetized listing's price is enforced per call by x402, which
// this package hands both halves of the door — the price table and the charger (see
// payments.go). Marketplace never dispatches a tool itself and never moves money
// itself.
//
// TWO TRANSPORTS, ONE POLICY — because the shipped topology has no co-residency.
// The three seams this package binds are process-globals (x402.reg, tools.std, and
// wallets' mounted singleton), so they bind within ONE process, and the fleet runs
// ONE PROCESS PER APP. That is not a possible future deployment, it is the only one:
// manifest/apps.go declares marketplace, tools, x402 and wallets as four ordinary
// prefix-routed rows (only `zen` is Coresident, manifest/apps.go:194); the Dockerfile
// builds a plugin binary per row and cmd/cloud loads each as its own CHILD PROCESS
// (cmd/cloud/main.go:257, zip.Load on a binary path); and the fused monolith that
// linked every subsystem was deleted (cmd/cloud/main.go:1-9).
//
// So the wiring above bound nothing in production, and a listed tool was not merely
// unbuyable — it was FREE. The tools process refuses a dispatch whose registry ROW
// declares a price, but a marketplace price lives in the listing store, so a listing
// on a tool that declares none was dispatched for nothing.
//
// The fix is the internal plane (resource_billing_peer.go is the precedent: the same
// split turned every priced create free, and the answer was to ASK the owning
// process). Four ops, each served by the process that owns the answer:
//
//	tools → x402         x402_settle     settle this tool call   (apps/x402/rpc.go)
//	x402  → marketplace  market_price    what it costs, who is paid (rpc.go here)
//	x402  → wallets      wallets_payee   resolve the payee wallet (apps/wallets/rpc.go)
//	x402  → commerce     finance_credit  credit the payee (apps/commerce/credit_rpc.go)
//
// The in-process seam stays the FAST PATH where the owner is co-resident; the plane
// answers where it is not. Both are the same policy and both fail closed, which is
// what payments_test.go (one process) and split_test.go (five real processes) assert
// against each other.
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
	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/audit"
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

// Mount wires /v1/marketplace/* and closes the payment seam both ways, so a
// published listing's price is challenged and settled at every call.
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

	// Close the payment seam, both halves, from the one store that already holds
	// the price and the payee (payments.go). Publish FIRST: from the instant a
	// charger is installed a dispatch can ask what a tool costs, and it must find
	// the table already there rather than a moment of "nothing is priced".
	x402.Publish(&registry{store: store})
	tools.SetCharger(charger{})

	// The same table, published for the processes that do NOT have it: the rail
	// runs in its own binary and asks (rpc.go). Both doors read the one store, so
	// what a tool costs cannot differ by who is asking or over which transport.
	exposePrice(store)

	// cloud.Bridge FIRST, ahead of every leaf: a typed op receives a context.Context
	// and its decoded In and nothing else, so the validated org — and the request the
	// project scope and the audit actor are read off — cross on the context. Serve
	// installs the same middleware binary-wide; nesting is harmless (the inner one is
	// the one the handler sees) and declaring it here is what makes the subsystem
	// self-sufficient when a test or a non-Serve composition root mounts it bare.
	app.Use(cloud.Bridge())

	// Registered on the App with WHOLE paths, not on a group: the surface root IS
	// /v1/marketplace, and Group("/v1/marketplace") composed with an empty leaf yields
	// "/v1/marketplace/" — a different address. One registrar for all six keeps every
	// op's published path exactly the path the router matches.
	za := cloud.ZipApp(app)
	if za == nil {
		return fmt.Errorf("marketplace.Mount: router exposes no op registry")
	}
	o := marketOps{s: s}
	zip.Get(za, "/v1/marketplace", o.discover)
	zip.Get(za, "/v1/marketplace/listings", o.listListings)
	zip.Post(za, "/v1/marketplace/listings", o.publish, zip.WithStatus(http.StatusCreated))
	zip.Delete(za, "/v1/marketplace/listings/:id", o.unpublish)
	zip.Post(za, "/v1/marketplace/install", o.install)
	zip.Post(za, "/v1/marketplace/uninstall", o.uninstall)

	s.Log.Info("marketplace mounted", "prefix", "/v1/marketplace", "brand", deps.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document,
// the MCP tool list and the generated SDK — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// marketOps binds the service to marketplace's typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only bound
// form cmd/zipdoc can lift prose from.
type marketOps struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an ALIAS
// for the unnamed empty struct, not a definition: zip keys the response on 204 only
// when the Out type has no name, so a defined type here would publish "200 with a
// body" about a route that answers 204 with none.
type noContent = struct{}

// tenantOf is the validated org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the caller
// asserted for itself. It IS the principal.Org gate, refusing with the same 403 and
// the same message.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// projectOf is the caller's project sub-scope. It lives in a header rather than in
// the tenant key, so it needs the request; off the HTTP path there is none, and the
// caller lands in the org's default project exactly as an absent header does.
func projectOf(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return principal.DefaultProject
	}
	return principal.Project(c)
}

// Shutdown detaches the payment seams and closes the store, in that order.
// Idempotent.
//
// Detaching first is the whole point: both seams close over the store, so leaving
// them installed past Close would leave a price table answering from a closed
// database — and a price lookup that errors fails a dispatch closed, turning a
// clean shutdown into 402s on every tool in the process. Nothing priced, nothing
// charged, no dangling reader.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	x402.Publish(nil)
	tools.SetCharger(nil)
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

// marketCatalog is one discovery read.
type marketCatalog struct {
	// Items is every capability the caller can see in their own (org, project),
	// each carrying any public listing's shop metadata and whether it is installed.
	Items []marketItem `json:"items"`
}

// Discover lists every tool and agent the caller can reach in their own org and
// project, enriched with any public listing's title, category and price, and with
// installed=true on the ones already activated for that scope. It is the shop
// window: one read that answers what exists, what it costs and what is already on.
func (o marketOps) discover(ctx context.Context, _ *noInput) (*marketCatalog, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	catalog := tools.Default().List(ctx, tools.Scope{Org: org, Project: projectOf(ctx)})
	byTool, err := o.s.State.store.PublicByTool(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "listings: %v", err)
	}
	out := make([]marketItem, 0, len(catalog))
	for _, t := range catalog {
		item := marketItem{Tool: t, Installed: t.Activated}
		if l, ok := byTool[t.Name]; ok {
			item.Title = l.Title
			item.Category = l.Category
			if l.Price.Sign() > 0 {
				item.Price = &tools.Price{Amount: l.Price, Currency: l.Currency, Recipient: l.Recipient}
			}
		}
		out = append(out, item)
	}
	return &marketCatalog{Items: out}, nil
}

// ── listings (publish / unpublish) ──────────────────────────────────────────────

// listingPage is the caller org's own published listings.
type listingPage struct {
	// Listings is every listing this org has published, private ones included
	// (Public says which are discoverable by others).
	Listings []Listing `json:"listings"`
}

// ListListings returns the listings the caller's own org has published — what this
// org is offering, not what it can buy. A publisher only ever sees its own rows.
func (o marketOps) listListings(ctx context.Context, _ *noInput) (*listingPage, error) {
	org, err := tenantOf(ctx)
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
	return &listingPage{Listings: rows}, nil
}

// publishReq offers one tool on the marketplace.
type publishReq struct {
	// Tool is the registry name of the capability being offered. It must already
	// resolve in the publisher's own scope — there are no phantom listings.
	Tool string `json:"tool"`
	// Title is the shop-window name, 1-200 characters. Required.
	Title string `json:"title"`
	// Description is the long copy, clipped at 4096 characters.
	Description string `json:"description"`
	// Category groups the listing in the shop window.
	Category string `json:"category"`
	// Price is the per-call price as a decimal USD string, exact to 18 places —
	// "0.0025" is a quarter of a cent and stays one. Empty or "0" (the default)
	// publishes it free; any positive price makes the listing monetized and
	// requires Recipient.
	Price string `json:"price"`
	// Currency denominates Price.
	Currency string `json:"currency"`
	// Recipient is the seller's payout wallet ID, in the publishing org — the
	// wallet x402 pays. Required for a monetized listing.
	Recipient string `json:"recipient"`
	// Public makes the listing discoverable by other orgs. Private otherwise.
	Public bool `json:"public"`
}

// Publish offers one tool on the marketplace, optionally monetized. The tool must
// already resolve in the publisher's own scope, so a listing can never advertise a
// capability that does not exist; a listing with a price must name the payout wallet
// the x402 seam settles to, so a monetized offer is never unpayable. The price is
// exact to 18 decimal places, so a per-call price below a cent is a real price and
// not a rounded-away zero. The listing is owned by the publishing org, paid into a
// wallet of that same org, and answers 201 with the created row.
//
// Example: {"tool": "summarize", "title": "Summarize", "price": "0.0025", "recipient": "wal_9f2", "public": true}
func (o marketOps) publish(ctx context.Context, in *publishReq) (*Listing, error) {
	org, err := tenantOf(ctx)
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
	price, err := money.ParseUSD(strings.TrimSpace(body.Price))
	if err != nil {
		return nil, zip.ErrBadRequest("price must be a decimal USD amount with at most 18 decimals")
	}
	if price.IsNeg() {
		return nil, zip.ErrBadRequest("price must be >= 0")
	}
	if price.Sign() > 0 && strings.TrimSpace(body.Recipient) == "" {
		return nil, zip.ErrBadRequest("a monetized listing requires a recipient wallet")
	}
	// The tool must exist in the publisher's scope — no phantom listings.
	if !tools.Default().Exists(ctx, tools.Scope{Org: org, Project: projectOf(ctx)}, body.Tool) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", body.Tool)
	}
	created, err := o.s.State.store.Create(ctx, Listing{
		PublisherOrg: org, Tool: body.Tool, Title: body.Title, Description: clip(body.Description),
		Category: clip(body.Category), Price: price, Currency: strings.TrimSpace(body.Currency),
		Recipient: strings.TrimSpace(body.Recipient), Public: body.Public,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "publish: %v", err)
	}
	record(ctx, o.s, org, "listing:"+created.ID, "published", http.StatusCreated)
	return &created, nil
}

// listingRef addresses one listing. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever else arrives.
type listingRef struct {
	// ID is the listing to unpublish, from the path.
	ID string `json:"id"`
}

// Unpublish withdraws one of the caller org's listings from the marketplace and
// answers 204. Only the publishing org can remove its own listing; an id that is
// unknown, or belongs to another org, is the same 404, so a probe learns nothing
// about what exists. Removing a listing removes its price from per-call enforcement;
// it does not uninstall the tool for anyone who already installed it.
//
// Example: {"id": "lst_1"}
func (o marketOps) unpublish(ctx context.Context, in *listingRef) (*noContent, error) {
	org, err := tenantOf(ctx)
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
	record(ctx, o.s, org, "listing:"+id, "unpublished", http.StatusOK)
	return nil, nil
}

// ── install / uninstall (== tool activation) ────────────────────────────────────

// installReq names the tool to install or uninstall.
type installReq struct {
	// Tool is the registry name of the capability to activate (or deactivate) for
	// the caller's own org and project. Required.
	Tool string `json:"tool"`
}

// installState reports a tool's activation for the caller's (org, project).
type installState struct {
	// Tool is the capability the write applied to.
	Tool string `json:"tool"`
	// Installed is its activation after the write.
	Installed bool `json:"installed"`
}

// Install activates one tool for the caller's own org and project. A marketplace
// install IS the tool plane's activation write — one store, one truth — so an
// installed capability is immediately dispatchable and a monetized one is priced
// from its listing at every call. The tool must resolve in the caller's scope, so
// installing something that does not exist is refused rather than recorded.
//
// Example: {"tool": "summarize"}
func (o marketOps) install(ctx context.Context, in *installReq) (*installState, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	tool := strings.TrimSpace(in.Tool)
	if tool == "" || len(tool) > maxName {
		return nil, zip.ErrBadRequest("tool is required")
	}
	project := projectOf(ctx)
	if !tools.Default().Exists(ctx, tools.Scope{Org: org, Project: project}, tool) {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown tool: %s", tool)
	}
	if err := tools.Default().Activate(ctx, org, project, tool, callerOf(ctx)); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "install: %v", err)
	}
	record(ctx, o.s, org, "install:"+tool, "installed", http.StatusOK)
	return &installState{Tool: tool, Installed: true}, nil
}

// Uninstall deactivates one tool for the caller's own org and project, so it stops
// being dispatchable there. It is the exact inverse of install and touches the same
// activation record; deactivating something that was never active is not an error.
// The listing itself is untouched — this withdraws the caller's use of a capability,
// not anyone's offer of it.
//
// Example: {"tool": "summarize"}
func (o marketOps) uninstall(ctx context.Context, in *installReq) (*installState, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	tool := strings.TrimSpace(in.Tool)
	if tool == "" {
		return nil, zip.ErrBadRequest("tool is required")
	}
	if err := tools.Default().Deactivate(ctx, org, projectOf(ctx), tool); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "uninstall: %v", err)
	}
	record(ctx, o.s, org, "install:"+tool, "uninstalled", http.StatusOK)
	return &installState{Tool: tool, Installed: false}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────────

func clip(s string) string {
	if len(s) > maxText {
		return s[:maxText]
	}
	return strings.TrimSpace(s)
}

// callerOf is the validated principal behind the request — the actor an activation
// is recorded under. Empty off the HTTP path, where there is no caller to name.
func callerOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}

// record appends one audit row. It takes the CONTEXT rather than the request because
// its callers are typed ops, and it reaches the request through the same seam they
// do; off the HTTP path there is no actor and no path to attribute, so it records
// nothing rather than an anonymous half-row.
func record(ctx context.Context, s *cloud.Service[state], org, resourceID, result string, status int) {
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
	if _, err := s.State.audit.Append(ctx, rec); err != nil {
		s.Log.Warn("audit append failed", "err", err)
	}
}
