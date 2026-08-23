package tools

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/zap-proto/zip"
)

const (
	maxName  = 128
	maxURL   = 2048
	maxBatch = 256
)

// ── GET /v1/tools — discovery (all sources, activated flags) ────────────────────

// toolQuery narrows the discovery listing. Both fields are query parameters and
// both are optional.
//
// Activated is a STRING and not a bool on purpose: this route has always tested
// the raw query value against the literal "true", so `?activated=1` and a bare
// `?activated` have always meant "no filter". A bool field would make zip's
// binder read both as true, which is a different set of tools for the same URL.
type toolQuery struct {
	// Source keeps only tools from one source — connector, function, zap-service,
	// agent, skill or mcp. Empty keeps every source.
	Source string `json:"source"`
	// Activated keeps only the tools activated for the caller's org and project,
	// and only when it is exactly the string "true".
	Activated string `json:"activated"`
}

// toolList is a page of tools. It is never null: a caller with no tools gets an
// empty array.
type toolList struct {
	// Tools is every tool the caller may see, deduplicated by name with source
	// precedence applied.
	Tools []Tool `json:"tools"`
}

// ListTools lists every tool the caller's org and project can reach, from every
// source, each flagged with whether it is activated. This is the discovery
// surface: one flat set of names spanning connector actions, user functions,
// zap-service routes, agents, skills and the org's own external MCP servers,
// deduplicated by name so the highest-precedence source wins a collision. It
// lists; it does not call — dispatch is POST /v1/tools/call.
func (o toolOps) listTools(ctx context.Context, in *toolQuery) (*toolList, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	tools := Default().List(ctx, scope)
	srcFilter := Source(strings.TrimSpace(in.Source))
	activatedOnly := in.Activated == "true"
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if srcFilter != "" && t.Source != srcFilter {
			continue
		}
		if activatedOnly && !t.Activated {
			continue
		}
		out = append(out, t)
	}
	return &toolList{Tools: out}, nil
}

// ── POST /v1/tools/call — the DYNAMIC half of the tool plane ───────────────────

// toolCall names a tool and the arguments to run it with.
type toolCall struct {
	// Name is the tool to run, exactly as GET /v1/tools reports it.
	Name string `json:"name"`
	// Arguments is the tool's own input object, passed through verbatim to
	// whichever source owns it.
	Arguments map[string]any `json:"arguments"`
}

// toolResult is what the tool returned.
type toolResult struct {
	// Name is the tool that ran.
	Name string `json:"name"`
	// Result is the tool's own output, verbatim — its shape is the tool's, not
	// this plane's.
	Result any `json:"result"`
}

// CallTool runs one of the caller's activated tools and answers with its output.
//
// This is the endpoint onto the tool plane's DYNAMIC half — the half no build-time
// catalogue can hold, because it is per-tenant: an org's connected connector
// actions, its authored skills, its agents and functions, and the tools of every
// external MCP server it registered. A tool's existence, its price and its
// activation are all rows, not code, so they cannot be known until the caller is.
//
// One policy, the registry's: resolve by precedence, refuse an unactivated tool
// 403, settle a priced one through the x402 client or fail closed 402, then
// dispatch to the winning source bound to the caller's own (org, project). One
// metered unit, one audit record. A caller can only ever dispatch its own tools.
//
// Discovery is GET /v1/tools — ?activated=true for the callable set.
//
// Example: {"name": "slack_post_message", "arguments": {"channel": "#general", "text": "hi"}}
func (o toolOps) callTool(ctx context.Context, in *toolCall) (*toolResult, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !validToolName(name) {
		return nil, zip.ErrBadRequest("name is required and must be a tool name")
	}
	// Balance BEFORE work. o.meter debits a unit at the bottom of this function; this
	// is the question that debit assumed somebody had already asked.
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	out, err := Default().Dispatch(ctx, p, name, in.Arguments)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownTool):
			return nil, zip.ErrNotFound("unknown tool: " + name)
		case errors.Is(err, ErrNotActivated):
			o.audit(ctx, "tools.call", p.Org, name, "denied", http.StatusForbidden)
			return nil, zip.ErrForbidden("tool not activated for this org/project: " + name)
		case errors.Is(err, ErrNotDispatchable):
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "tool is not dispatchable: %s", name)
		case errors.Is(err, ErrPaymentRequired), errors.Is(err, ErrChargerUnset):
			o.audit(ctx, "tools.call", p.Org, name, "payment_required", http.StatusPaymentRequired)
			return nil, zip.Errorf(http.StatusPaymentRequired, "payment required for tool: %s", name)
		default:
			o.audit(ctx, "tools.call", p.Org, name, "error", http.StatusFailedDependency)
			return nil, zip.Errorf(http.StatusFailedDependency, "tool call failed: %v", err)
		}
	}
	o.meter(ctx)
	o.audit(ctx, "tools.call", p.Org, name, "ok", http.StatusOK)
	return &toolResult{Name: name, Result: out}, nil
}

// ── activation API (task 5) ─────────────────────────────────────────────────────

// activationSet is the activated-tool set for one (org, project). It is never
// null: a scope with nothing activated gets an empty array.
type activationSet struct {
	// Enabled is every tool name activated for the caller's org and project.
	Enabled []string `json:"enabled"`
}

// GetActivation reports which tools are switched on for the caller's org and
// project. Activation is what makes a tool dispatchable and what makes it visible
// to an agent, so this is the set the MCP tool list is drawn from — every other
// tool in the registry is discoverable but refused at call time.
func (o toolOps) getActivation(ctx context.Context, _ *noInput) (*activationSet, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	enabled, err := o.s.State.activation.List(ctx, scope.Org, scope.Project)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	return &activationSet{Enabled: enabled}, nil
}

// activationReq is a batch of activation toggles. Activate is applied first, so a
// name in both lists ends up deactivated.
type activationReq struct {
	// Activate switches these tool names on for the caller's org and project.
	Activate []string `json:"activate"`
	// Deactivate switches these tool names off.
	Deactivate []string `json:"deactivate"`
}

// PutActivation switches tools on and off for the caller's org and project, and
// answers with the resulting activated set. It is the ONE write path that turns
// skills, plugins and connectors into callable tools — an unactivated tool is
// listed by discovery but refused 403 at dispatch. Activate is applied before
// Deactivate, so a name in both lists ends up off. More than 256 toggles in one
// request is refused 413.
//
// Example: {"activate": ["cloud_get_ping"], "deactivate": []}
func (o toolOps) putActivation(ctx context.Context, in *activationReq) (*activationSet, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Activate)+len(in.Deactivate) > maxBatch {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "too many toggles (max %d)", maxBatch)
	}
	actor := callerOf(ctx)
	for _, name := range in.Activate {
		if !validToolName(name) {
			return nil, zip.ErrBadRequest("invalid tool name: " + name)
		}
		if err := o.s.State.activation.Activate(ctx, scope.Org, scope.Project, name, "", actor); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "activate: %v", err)
		}
	}
	for _, name := range in.Deactivate {
		if err := o.s.State.activation.Deactivate(ctx, scope.Org, scope.Project, name); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "deactivate: %v", err)
		}
	}
	enabled, err := o.s.State.activation.List(ctx, scope.Org, scope.Project)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list activation: %v", err)
	}
	if enabled == nil {
		enabled = []string{}
	}
	o.audit(ctx, "tools.call", scope.Org, "activation", "ok", http.StatusOK)
	return &activationSet{Enabled: enabled}, nil
}

// ── external MCP servers (task 2) ───────────────────────────────────────────────

// mcpServerList is the caller org's registered external MCP servers. It is never
// null: an org with none gets an empty array.
type mcpServerList struct {
	// Servers is every external MCP server this org has registered. No secret
	// VALUE is ever included — only whether one is set.
	Servers []MCPServer `json:"servers"`
}

// ListServers lists the external MCP servers the caller's org has registered.
// Each record carries the URL and the name of the header its credential is
// injected into; the credential VALUE lives only in KMS and is never returned,
// so hasSecret is the whole of what this surface says about it.
func (o toolOps) listServers(ctx context.Context, _ *noInput) (*mcpServerList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := o.s.State.servers.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list servers: %v", err)
	}
	if servers == nil {
		servers = []MCPServer{}
	}
	return &mcpServerList{Servers: servers}, nil
}

// createServerReq registers one external MCP server: either a URL the org typed
// in, or a catalog listing it picked off the shelf. Exactly one of URL and
// Listing — they are two ways to name the same thing, and a request that gave
// both would be asking for two servers.
type createServerReq struct {
	// Name labels the server for the org. Required with URL; with Listing it
	// defaults to the listing's own title.
	Name string `json:"name"`
	// URL is the server's JSON-RPC endpoint. It must be an http(s) URL naming a
	// PUBLIC host: loopback, link-local, private and cloud-metadata addresses are
	// refused here and again when the dialer connects.
	URL string `json:"url"`
	// Listing enables a CATALOG entry instead — the id from GET /v1/tools/catalog.
	// The endpoint is the listing's own streamable-http remote, so a listing that
	// only ships a stdio package is refused: there is nothing to reach yet.
	Listing string `json:"listing"`
	// AuthHeader is the request header the credential is injected into, e.g.
	// "Authorization". Empty means the server needs no credential.
	AuthHeader string `json:"authHeader"`
	// Secret is the credential VALUE. It is sealed into KMS under a per-org ref
	// and never stored in SQLite, never listed, and never returned.
	Secret string `json:"secret"`
}

// CreateServer gives the caller's org one more external MCP server, so its tools
// join the org's tool plane and the fleet's MCP server. It is the ONE way an org
// gains a server, whether it typed the URL in or enabled a catalog listing: both
// write the SAME record, and `source` says which it was. A second registration
// path would be a second place for a server to exist, and then a second place to
// forget to check the credential.
//
// The credential VALUE is sealed in KMS under a per-org ref; the row keeps only
// the URL, the header name to inject it into, and a has-secret flag — so a secret
// with no KMS configured is refused 503 rather than stored in the clear. The URL
// is SSRF-validated here and re-checked by the dialer at connect time, which is
// the DNS-rebinding defense.
//
// Enabling a listing the org already enabled REVISES that server rather than
// adding a near-duplicate beside it, so a retried enable is the same one server.
// Answers 201 with the stored record.
//
// Example: {"listing": "com.stripe_mcp", "authHeader": "Authorization", "secret": "Bearer …"}
func (o toolOps) createServer(ctx context.Context, in *createServerReq) (*MCPServer, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	url := strings.TrimSpace(in.URL)
	listing := strings.TrimSpace(in.Listing)
	id := ""
	if url != "" && listing != "" {
		return nil, zip.ErrBadRequest("give a url or a listing, not both")
	}
	if listing != "" {
		l, err := o.listing(ctx, listing)
		if err != nil {
			return nil, err
		}
		if l.Hidden && !adminOf(ctx) {
			return nil, zip.ErrNotFound("listing not found")
		}
		if url = l.Endpoint(); url == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity,
				"%s publishes no streamable-http endpoint; it ships a package that has to be run", l.Name)
		}
		if name == "" {
			// The title is the publisher's, so it is adopted but not trusted to be
			// short: an over-long one would otherwise refuse the enablement with
			// "name is required", to a caller who supplied no name at all.
			name = cmp.Or(l.Title, l.Name)
			if len(name) > maxName {
				name = l.Name
			}
		}
		// The server id PREFIXES every tool name this server contributes, so an
		// enabled listing's tools read "acme-com_create_payment_link" rather than
		// carrying a random handle a model has no way to interpret. It names the
		// publisher's whole DOMAIN and not the memorable label inside it, because
		// acme-com and acme-sh are two different publishers and the prefix is read
		// on the screen where a credential is pasted. A PREFERENCE, not a demand:
		// the store resolves a collision within the org.
		id = brand(l.Vendor)
	}
	if name == "" || len(name) > maxName {
		return nil, zip.ErrBadRequest("name is required (<=128 chars)")
	}
	if len(url) > maxURL {
		return nil, zip.ErrBadRequest("url too long")
	}
	if err := validateServerURL(url); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	header := strings.TrimSpace(in.AuthHeader)
	if header != "" && !validHeader.MatchString(header) {
		return nil, zip.ErrBadRequest("authHeader must be an HTTP header name")
	}
	hasSecret := in.Secret != ""
	if len(in.Secret) > maxSecret {
		return nil, zip.ErrBadRequest("secret too large")
	}
	if hasSecret && o.s.State.kms == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "KMS not configured; refusing to store an MCP server secret")
	}
	// The credential is sealed BEFORE the row is written, and the row's id is
	// resolved before either. That ordering is what makes the whole thing need no
	// undo: a failed seal leaves the store exactly as it was, rather than a row
	// claiming a credential nobody stored — which does not fail loudly, it makes
	// the server's tools quietly stop appearing.
	id, fresh, err := o.s.State.servers.Resolve(ctx, org, listing, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve server id: %v", err)
	}
	if hasSecret {
		if err := o.s.State.kms.PutSecret(ctx, authRef(org, id), []byte(in.Secret)); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "seal server secret: %v", err)
		}
	} else if !fresh {
		// Re-registering WITHOUT a credential drops the one that was there. The row
		// is about to say so, and a credential outliving the thing it belonged to
		// is the state nobody audits.
		o.forget(ctx, org, id)
	}
	created, err := o.s.State.servers.Write(ctx, MCPServer{
		ID: id, Org: org, Name: name, URL: url, AuthHeader: header,
		HasSecret: hasSecret, Listing: listing,
	}, fresh)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create server: %v", err)
	}
	o.audit(ctx, "tools.call", org, "server:"+created.ID, "created", http.StatusCreated)
	return &created, nil
}

// serverRef addresses one external MCP server. The id is the path segment: the
// URL is the addressing authority.
type serverRef struct {
	// ID is the server to deregister, from the path.
	ID string `json:"id"`
}

// DeleteServer deregisters one of the caller org's external MCP servers, so its
// tools leave the registry. Scoped to the caller's org, so an id belonging to
// another tenant is a 404 and not a delete. Answers 204 with no body; a server
// this org does not have is 404.
func (o toolOps) deleteServer(ctx context.Context, in *serverRef) (*noContent, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	removed, err := o.s.State.servers.Delete(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete server: %v", err)
	}
	if !removed {
		return nil, zip.ErrNotFound("server not found")
	}
	o.forget(ctx, org, id)
	o.audit(ctx, "tools.call", org, "server:"+id, "deleted", http.StatusOK)
	return nil, nil
}

// forget destroys the credential a deregistered server held, so a customer's
// secret does not outlive the thing it belonged to — which is what a deletion
// request and a rotation both actually mean.
//
// It OVERWRITES rather than deletes, because types.KMSClient has GetSecret,
// PutSecret and Sign and no removal at all. The KMS service itself has one
// (apps/kms Client.Delete), so the gap is the interface and KMSPeer, not the
// store; closing it properly is a fleet-wide change to a client fifteen packages
// implement, and it is written up in apps/tools/LLM.md rather than smuggled in
// here. Overwriting destroys the credential material today, which is the part
// that matters.
//
// Best effort by construction: the row is already gone, so nothing reads this ref
// again, and refusing the delete because custody was briefly unreachable would
// leave the server the caller asked to remove.
func (o toolOps) forget(ctx context.Context, org, id string) {
	if o.s.State.kms == nil {
		return
	}
	if err := o.s.State.kms.PutSecret(ctx, authRef(org, id), nil); err != nil {
		o.s.Log.Warn("could not destroy a deregistered server credential", "org", org, "server", id, "err", err)
	}
}

// ── the catalog: what the public registries publish ─────────────────────────────

// catalogQuery narrows the catalog listing. Every field is a query parameter and
// every one is optional; the zero value is the whole visible shelf.
//
// Featured and Official are STRINGS and not bools for the reason every other
// filter on this plane is: the route compares the raw query value to the literal
// "true", so `?featured=1` and a bare `?featured` mean "no filter" rather than
// silently selecting a different set for the same URL.
type catalogQuery struct {
	// Q matches the name, title or description, case-insensitively.
	Q string `json:"q"`
	// Featured keeps only the listings we put on the front of the shelf, and only
	// when it is exactly the string "true".
	Featured string `json:"featured"`
	// Official keeps only the vendors' OWN servers — not third-party copies of
	// them — and only when it is exactly the string "true".
	Official string `json:"official"`
	// Limit bounds the page: default 50, maximum 200. A value that is not a
	// positive integer reads as the default.
	Limit int `json:"limit"`
	// Offset skips that many listings.
	Offset int `json:"offset"`
}

// mcpCatalog is a page of the catalog. Never null: an unsynced deployment gets
// an empty array, not a null.
type mcpCatalog struct {
	// Catalog is this page of listings, featured first, then by name.
	Catalog []MCPListing `json:"catalog"`
	// Total is how many listings the filter matched, which is more than this page
	// holds whenever there is a next one.
	Total int `json:"total"`
	// Limit is the page size that was actually applied — the default or the clamp,
	// when the request asked for neither or for too much.
	Limit int `json:"limit"`
	// Offset is where this page started, so a caller pages from what the server
	// did rather than from what it asked for.
	Offset int `json:"offset"`
}

// ListCatalog lists the MCP servers the public registries publish, as we hold
// them: our canonical copy of registry.modelcontextprotocol.io, plus what we
// decided about each entry.
//
// This is the SHELF an org picks from. A listing with a streamable-http endpoint
// can be enabled as-is — POST /v1/tools/mcp/servers with its id — and its tools then
// join the org's tool plane and the fleet's MCP server. A listing that only ships a
// stdio package needs a process to run it, which is why the transports are on
// every entry rather than implied.
//
// Hidden entries are absent: they are the ones we took off the shelf. A platform
// SuperAdmin sees them, because the same query answers "what is on the shelf" and
// "what is in the catalog" and two queries would drift apart.
//
// It is PAGED — 50 by default, 200 at most. The public registry publishes tens of
// thousands of servers, so an unbounded answer is a twenty-megabyte response and a
// storefront that renders in a minute. total is the whole match, not the page.
func (o toolOps) listCatalog(ctx context.Context, in *catalogQuery) (*mcpCatalog, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	if o.s.State.catalog == nil {
		return &mcpCatalog{Catalog: []MCPListing{}, Limit: catalogPage}, nil
	}
	q := Query{
		Text:     strings.TrimSpace(in.Q),
		Featured: in.Featured == "true",
		Official: in.Official == "true",
		Hidden:   adminOf(ctx),
		Limit:    in.Limit,
		Offset:   in.Offset,
	}
	out, total, err := o.s.State.catalog.List(ctx, q)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list catalog: %v", err)
	}
	limit, offset := q.page()
	return &mcpCatalog{Catalog: out, Total: total, Limit: limit, Offset: offset}, nil
}

// listingRef addresses one catalog listing. The id is the path segment: the URL
// is the addressing authority.
type listingRef struct {
	// ID is the listing, from the path. It is the publisher's reverse-DNS name
	// with its one slash written as an underscore — "com.stripe_mcp".
	ID string `json:"id"`
}

// GetListing returns one catalog entry in full: the publisher's description, its
// repository and site, every package form with the runtime that launches it, and
// every hosted endpoint. It is what a branding page renders, and what tells a
// caller whether the listing can be enabled here and now (a streamable-http
// remote) or needs somewhere to run first (a stdio package).
//
// A HIDDEN listing is not served to an org — a shelf that renders what it does
// not list would be a way around the shelf — but is served to a SuperAdmin, who
// is the one deciding whether to put it back.
func (o toolOps) getListing(ctx context.Context, in *listingRef) (*MCPListing, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	l, err := o.listing(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if l.Hidden && !adminOf(ctx) {
		return nil, zip.ErrNotFound("listing not found")
	}
	return &l, nil
}

// mcpCatalogSync reports what one upstream pass changed.
type mcpCatalogSync struct {
	// Added is how many listings the catalog did not have before.
	Added int `json:"added"`
	// Updated is how many the publisher has changed since we last looked.
	Updated int `json:"updated"`
	// Total is how many listings the catalog holds now.
	Total int `json:"total"`
	// Registry is the upstream this pass read.
	Registry string `json:"registry"`
}

// SyncCatalog pulls the public MCP registry into our canonical copy and reports
// what changed. SuperAdmin only; every other caller is refused.
//
// It is IDEMPOTENT: a listing is keyed by the publisher's own reverse-DNS name,
// so a second pass over an unchanged registry rewrites the same rows and reports
// added=0, updated=0. It never deletes — a listing that vanishes upstream may be
// one an org has already enabled, and dropping its description would not drop its
// server. And it never touches CURATION: hidden, featured, an admin-set official
// and a logo survive every sync, because the write does not name those columns.
func (o toolOps) syncCatalog(ctx context.Context, _ *noInput) (*mcpCatalogSync, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !adminOf(ctx) {
		return nil, zip.ErrForbidden("admin required")
	}
	if o.s.State.catalog == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the catalog store is not open")
	}
	added, updated, err := o.s.State.catalog.Sync(ctx)
	if err != nil {
		o.s.Log.Error("catalog sync failed", "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "sync failed: %v", err)
	}
	total, err := o.s.State.catalog.Count(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "count catalog: %v", err)
	}
	o.audit(ctx, "catalog.sync", org, "catalog", "ok", http.StatusOK)
	return &mcpCatalogSync{Added: added, Updated: updated, Total: total, Registry: registryURL()}, nil
}

// curateReq is a patch of our decisions about one listing. Every field is a
// POINTER because this is a patch and not a replacement: an absent field leaves
// what is there, so featuring a listing cannot silently un-hide it.
type curateReq struct {
	// ID is the listing to curate, from the path.
	ID string `json:"id"`
	// Hidden takes the listing off the org-visible shelf, or puts it back.
	Hidden *bool `json:"hidden"`
	// Featured puts the listing on the front of the shelf, or takes it off.
	Featured *bool `json:"featured"`
	// Official overrides the derivation: setting it makes this answer FINAL, so
	// no later sync re-derives over it. That is the difference between a default
	// and a decision — the derivation can only tell that a domain-verified
	// publisher serves the endpoint, not that the product is theirs.
	Official *bool `json:"official"`
	// Logo is the brand mark to render, an https URL. Empty clears ours and lets
	// the next sync adopt the publisher's own icon again.
	Logo *string `json:"logo"`
}

// CurateListing sets what WE say about one catalog entry — hidden, featured,
// official, logo — and answers with the stored listing. SuperAdmin only; every
// other caller is refused.
//
// Curation is the half of a catalog row a sync cannot write, and this is the only
// thing that writes it. The upstream half is never editable here: a description
// that disagreed with the publisher's would be a fork of their listing, and the
// next sync would silently undo it.
//
// Example: {"featured": true, "official": false}
func (o toolOps) curateListing(ctx context.Context, in *curateReq) (*MCPListing, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !adminOf(ctx) {
		return nil, zip.ErrForbidden("admin required")
	}
	if in.Logo != nil {
		if logo := strings.TrimSpace(*in.Logo); logo != "" && !strings.HasPrefix(logo, "https://") {
			return nil, zip.ErrBadRequest("logo must be an https URL")
		}
	}
	if o.s.State.catalog == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the catalog store is not open")
	}
	l, err := o.s.State.catalog.Curate(ctx, strings.TrimSpace(in.ID), Curation{
		Hidden: in.Hidden, Featured: in.Featured, Official: in.Official, Logo: in.Logo,
	})
	if err != nil {
		if errors.Is(err, ErrUnknownTool) {
			return nil, zip.ErrNotFound("listing not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "curate listing: %v", err)
	}
	o.audit(ctx, "catalog.curate", org, "listing:"+l.ID, "ok", http.StatusOK)
	return &l, nil
}

// listing resolves one catalog entry, mapping an absent store and an unknown id
// to the answers the wire gives.
func (o toolOps) listing(ctx context.Context, id string) (MCPListing, error) {
	if o.s.State.catalog == nil {
		return MCPListing{}, zip.Errorf(http.StatusServiceUnavailable, "the catalog store is not open")
	}
	l, err := o.s.State.catalog.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrUnknownTool) {
			return MCPListing{}, zip.ErrNotFound("listing not found")
		}
		return MCPListing{}, zip.Errorf(http.StatusInternalServerError, "read listing: %v", err)
	}
	return l, nil
}

// ── shared helpers ──────────────────────────────────────────────────────────────

// validToolName bounds an activation target: the flat tool-name shape every source
// emits ([a-z0-9._:/-] plus "_"), so a hostile name can't become a store-key trick.
func validToolName(name string) bool {
	if name == "" || len(name) > maxName {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == ':', r == '/':
		default:
			return false
		}
	}
	return true
}

func meterUnit(s *cloud.Service[state], c *zip.Ctx) {
	s.Bill.Meter(principal.Payer(c), principal.Project(c), meterKind,
		cloud.ResourceFeeCents(feeEnvPrefix, meterKind), c.RequestID(), cloud.ClientIP(c))
}

// audrecordAction is audrecord with the action named: the plugin builder records
// plugin.build, which is a different act on a different resource than a call.
func audrecordAction(s *cloud.Service[state], c *zip.Ctx, action, org, resourceID, result string, status int) {
	if s.State.audit == nil {
		return
	}
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    action,
		Resource:  audit.Resource{Type: "tools", ID: resourceID},
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
