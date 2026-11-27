package dns

import (
	"errors"
	"net/http"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// prefix is the one namespace this subsystem answers under. Every address in
// [routes] carries it, and the group leaf is what remains after it.
const prefix = "/v1/dns"

// route is one address the DNS control plane answers and everything this head
// can state about it. Path is the WHOLE fiber pattern — the form the prose
// registry keys on and the form Mount's group and leaf compose — so the address
// registered and the address described are one string.
type route struct {
	method  string
	path    string
	summary string
	lead    string
}

// routes is the DNS control plane's API as the plane itself registers it —
// hanzoai/dns, plugin/hanzodns/api.go: registerRoutes mounts /v1/dns/health,
// /v1/dns/zones, /v1/dns/zones/ and /v1/dns/sync, and apiRouter dispatches the
// zone and record addresses under them by segment count and method.
//
// It is read TWICE and written once: Mount registers from it and init describes
// from it, so an address cannot be served without prose or described without
// being served.
//
// PUT and PATCH are the same operation at the plane — both reach
// handleUpdateRecord, which decodes a RecordPatch whose every field is optional
// and applies only the fields present. PUT is therefore a partial update too,
// and the prose says so rather than promising the replacement the verb usually
// implies.
var routes = []route{
	{http.MethodGet, prefix + "/health",
		"Check the DNS control plane",
		"Reports whether the DNS control plane is answering."},

	{http.MethodGet, prefix + "/zones",
		"List your org's DNS zones",
		"Lists every DNS zone the calling org holds, authoritative and provider-backed alike."},
	{http.MethodPost, prefix + "/zones",
		"Create a DNS zone",
		"Creates a zone for the calling org — authoritative, or backed by a DNS provider the org has connected."},

	{http.MethodGet, prefix + "/zones/:zone",
		"Read one DNS zone",
		"Reads one of the calling org's zones by name."},
	{http.MethodDelete, prefix + "/zones/:zone",
		"Delete a DNS zone",
		"Removes one of the calling org's zones, and the records in it, from the DNS control plane."},

	{http.MethodGet, prefix + "/zones/:zone/records",
		"List a zone's DNS records",
		"Lists the records in one zone. A provider-backed zone is read from the provider, which is its source of truth."},
	{http.MethodPost, prefix + "/zones/:zone/records",
		"Create a DNS record",
		"Creates a record in one zone. A provider-backed zone is written at the provider first, then mirrored locally so the resolver serves it."},

	{http.MethodGet, prefix + "/zones/:zone/records/:record",
		"Read one DNS record",
		"Reads one record of one zone by its id."},
	{http.MethodPut, prefix + "/zones/:zone/records/:record",
		"Amend a DNS record",
		"Amends one record of one zone. Only the fields the body carries change; this is the same partial update PATCH performs, not a replacement of the whole record."},
	{http.MethodPatch, prefix + "/zones/:zone/records/:record",
		"Amend a DNS record",
		"Amends one record of one zone. Only the fields the body carries change; the rest keep the values they hold at the plane."},
	{http.MethodDelete, prefix + "/zones/:zone/records/:record",
		"Delete a DNS record",
		"Removes one record from one zone."},

	{http.MethodPost, prefix + "/sync",
		"Push a set of zones and records in one call",
		"Replaces the calling org's zones and their records in bulk. The owning org is the caller's own validated claim, never the body, so a sync reaches nobody else's zones."},
}

// relay is the half of every address's prose that is identical for all of them,
// because the HANDLER is identical for all of them: one relay serves the whole
// table. Stated once so twelve descriptions cannot drift into twelve accounts of
// one forward.
const relay = " The plane owns the authoritative zone and record store behind " +
	"every name pointed at Hanzo; this head keeps none of it. The address and the " +
	"query string ARE the plane's own, relayed verbatim, and the plane's answer comes " +
	"back unchanged — its status code, its Content-Type, and its Location on a " +
	"redirect this head never follows.\n\n" +
	"It travels under the CALLER'S OWN identity and substitutes no service " +
	"credential, which would collapse tenants: the caller's validated session bearer " +
	"goes upstream as Authorization and the server-validated org as X-Org-Id, so a " +
	"caller in one org reaches only that org's zones, exactly as if it had called the " +
	"plane directly. The upstream host comes only from deployment config, never from " +
	"the request, so no path can re-target another host.\n\n" +
	"Fails closed before a byte leaves cloud: no validated principal is 403; an API " +
	"key is 401, because a pk-/sk- key is not a JWT the OIDC-gated plane can " +
	"validate and there is no substitute credential to send in its place; a path that " +
	"normalizes outside /v1/dns, or still carries a percent-escape or a `..` after one " +
	"decode, is 400; an unconfigured plane is 503 and an unreachable one 502."

// The prose for every address this subsystem serves. A relay authors no body —
// the request bytes are the caller's, relayed unread, and the response bytes are
// the plane's, relayed unparsed — so prose is the whole of what it can declare,
// and it declares it per address rather than once for a wildcard. Declared
// through the same registry openapi.Register uses, so a description renders only
// while the router actually serves its route.
func init() {
	for _, r := range routes {
		spec.Describe(r.path, r.method, r.summary, r.lead+relay)
	}
}

// edge is the head: a validated caller, a scoped path, and the plane this surface
// answers from. It holds no endpoint and no credential — those are the plane's, and
// only the plane's.
type edge struct{ p *hanzoPlane }

// Mount registers the DNS control plane's addresses under /v1/dns, every one
// answered by the same relay. Registered as a subsystem in apps.Wire(); on by
// default.
func Use(app cloud.Router, deps cloud.Deps) error {
	e := &edge{p: newHanzoPlane()}
	g := app.Group(prefix)
	// Untyped: every address relays, so there is no In or Out to infer. The prose
	// each one publishes is the openapi.Describe above.
	for _, r := range routes {
		g.Raw(r.method, strings.TrimPrefix(r.path, prefix), e.forward)
	}
	if luxlog.Default() != nil {
		luxlog.Default().Info("dns relay mounted", "addresses", len(routes))
	}
	return nil
}

// forward relays one /v1/dns request to the DNS control plane under the CALLER'S
// OWN validated identity and returns the plane's response verbatim.
func (e *edge) forward(c *zip.Ctx) error {
	// Fail closed: only a validated principal with a real org proceeds. A forged or
	// anonymous X-Org-Id yields ("", false) and is refused HERE -- it never reaches
	// the DNS plane. This is the cloud-side tenant check; the DNS plane enforces its
	// own org-scoping on top.
	org, ok := principal.Org(c)
	if !ok {
		// principal.Refused decides the status — 401 for nobody, 403 for a caller
		// with no tenant — and this plane's envelope carries it.
		var he *zip.HTTPError
		errors.As(principal.Refused(c), &he)
		return c.JSON(he.Status, fail(strings.ToLower(http.StatusText(he.Status)), he.Msg))
	}

	// Path + query pass through verbatim, but ONLY within /v1/dns. uri.Path() is
	// normalized and decoded by ONE layer, so a single-encoded dot-segment traversal
	// has ALREADY been resolved here -- e.g. /v1/dns/../../admin normalizes to
	// /admin -- and fails the prefix check. Guard the normalized path so the forward
	// is LOCKED under /v1/dns; the caller can never walk the request onto another
	// path of the DNS host.
	uri := c.Fiber().Request().URI()
	p := string(uri.Path())
	if p != prefix && !strings.HasPrefix(p, prefix+"/") {
		return c.JSON(http.StatusBadRequest, fail("bad_request", "path must be under /v1/dns"))
	}
	// Second layer: a DOUBLE-encoded traversal survives one decode as a literal `%2e`
	// (e.g. /v1/dns/%252e%252e/admin -> /v1/dns/%2e%2e/admin) that KEEPS the prefix,
	// so the upstream would decode the second layer and resolve outside /v1/dns. A
	// residual `%` (still-encoded byte) or `..` (residual traversal) in the
	// once-decoded path is the tell -- neither occurs in a legitimate DNS-API path
	// (zone labels are DNS names / punycode xn--). Fail closed before a byte leaves
	// cloud. The upstream host still comes ONLY from env (never caller-influenced),
	// so a path can never re-target another host: no SSRF.
	if strings.ContainsRune(p, '%') || strings.Contains(p, "..") {
		return c.JSON(http.StatusBadRequest, fail("bad_request", "path must be under /v1/dns"))
	}

	// BEARER RELAY -- the caller's OWN validated bearer, unchanged. The DNS plane
	// re-validates it and derives the org from the `owner` claim; cloud substitutes
	// NO service credential. X-Org-Id carries the server-validated org for a
	// trusted-proxy DNS mode. Both are the caller's own identity, so org A can never
	// reach org B. Because this is a fresh request, no other inbound header crosses.
	//
	// REFUSE HERE when there is no relayable bearer, rather than forwarding without
	// one. CallerBearer returns "" for an API KEY (middleware_identity: `tok == "" ||
	// isAPIKey(tok)`), because a pk-/sk- key is not a JWT and the OIDC-gated DNS
	// plane cannot validate it. This head was written for the console, which carries
	// a session bearer, so that case went unhandled: the request was forwarded with
	// NO Authorization at all and the caller got the DNS plane's own
	// {"code":"unauthorized","message":"missing Authorization header"} -- an upstream
	// error that reads like the DNS plane is broken, when the real answer is that
	// this credential type cannot reach it. Measured against the live plane
	// (ghcr.io/hanzoai/dns:0.11.1) with an API key: exactly that 401.
	//
	// Failing closed here also keeps the tenant story honest. X-Org-Id is set below
	// from the server-validated org, so a headerless forward would arrive carrying an
	// org claim and no proof of identity -- safe only for as long as the DNS plane
	// keeps rejecting it. Cloud should not depend on an upstream to refuse what it
	// can refuse itself.
	bearer := cloud.CallerBearer(c)
	if bearer == "" {
		return c.JSON(http.StatusUnauthorized, fail("unauthorized",
			"the DNS plane requires a session bearer; an API key cannot be relayed to it"))
	}

	// The plane reads VALUES and never a request: the head has already decided the
	// caller is real and the path is in bounds, and what remains is the call itself.
	a, err := e.p.send(c.Context(), Call{
		Org:         org,
		Bearer:      bearer,
		Method:      c.Method(),
		Path:        p,
		Query:       string(uri.QueryString()),
		Body:        c.Body(),
		ContentType: c.Header("Content-Type"),
	})
	switch {
	case errors.Is(err, ErrUnconfigured):
		return c.JSON(http.StatusServiceUnavailable, fail("unconfigured", "dns control plane is not configured"))
	case err != nil:
		// The provider's own error stays with the provider: a caller learns that the
		// plane did not answer, never why, and never an upstream URL.
		return c.JSON(http.StatusBadGateway, fail("bad_gateway", "dns control plane unavailable"))
	}

	ct := a.ContentType
	if ct == "" {
		ct = "application/json"
	}
	c.SetHeader("Content-Type", ct)
	// Relay Location so a plane's 3xx (which the adapter never follows) passes back
	// verbatim: status + Location, for the caller to act on.
	if a.Location != "" {
		c.SetHeader("Location", a.Location)
	}
	return c.Bytes(a.Status, a.Body)
}

// fail is the standard cloud-generated error body, matching the DNS plane's
// {code,message} shape so a client parses one error schema across the hop.
func fail(code, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg}
}
