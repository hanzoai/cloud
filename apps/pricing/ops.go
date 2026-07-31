package pricing

// The typed-op seam for the pricing surface.
//
// A typed op (zip.Get[In, Out] and friends) is ONE registry entry with N
// projections — the REST route, the OpenAPI operation's schema and prose, the
// MCP tool, the CLI command and the generated SDK method all follow from it. An
// untyped route gets a route and nothing else. This file holds what every op on
// this surface shares: the receiver zipdoc lifts prose through, the two identity
// readers, and the input every op that takes nothing off the wire uses.
//
// WHAT STAYS RAW, and why it is a property of the wire rather than of effort:
//
//   - the fourteen /v1/pricing/{compute,cloud,subscriptions,…} routes and
//     /v1/pricing-policy are a VERBATIM proxy of the @hanzo/pricing bundle: the
//     bundle picks the status (200, or 503 when a section is absent) and its
//     bytes are written unmodified (passthrough). A typed op answers the ONE
//     status it declared, over a Go re-marshal — two wire changes, so they stay
//     raw until the proxy class is expressible.
//   - PATCH /v1/admin/catalog/models/* addresses a model id that may contain
//     '/', so it routes through a greedy wildcard. fiber names that parameter
//     `*1`; binding it would need an input field tagged `json:"*1"`, which is
//     what every projection would then publish. A schema nobody can read is
//     worse than none.
//   - PATCH /v1/admin/catalog/providers/:name carries `overrides`, a raw JSON
//     merge patch (RFC 7386). zip reflects json.RawMessage as an ARRAY OF
//     INTEGERS (it is []byte), so typing it publishes a false schema; and
//     retyping the field to map[string]any moves `{"overrides":null}` from
//     "clear the override" to "leave it alone", which is a wire change.
//
// "Cannot be a typed op" is not "cannot be described". Both PATCHes bind the
// SAME named struct and answer the SAME named one, so openapi.Register declares
// those two shapes off the very types the handler uses — pure description, no
// route, status, field or byte moves, and no prose (Register has nowhere to put
// any). It states what a typed op cannot: `overrides` reflects there as an
// unconstrained object, which is what a merge patch honestly is, and the
// wildcard model id stays a path segment nobody has to model. Without it the
// only two writes on the admin catalog reach every generated SDK
// indistinguishable from a route that takes no body.
//
// The fifteen proxy reads get no Register: they carry no request body, and their
// response is the bundle's bytes, which this package does not model.
//
// init, not routes: Register panics on a duplicate declaration, and routes runs
// once per Mount.

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func init() {
	openapi.Register("/v1/admin/catalog/models/*", "PATCH", patchBody{}, Overlay{})
	openapi.Register("/v1/admin/catalog/providers/:name", "PATCH", patchBody{}, Overlay{})
}

// Prefixes are the absolute subtrees this subsystem answers on. It serves five,
// not the one the /v1/<name> convention would assume: the catalog read plane
// (/v1/pricing, /v1/pricing-policy), the self-service enablement plane
// (/v1/enablement) and the two admin planes over the same overlay store
// (/v1/admin/catalog, /v1/admin/enablement).
//
// Declaring them is not decoration. cloud.Declare builds the prefix table that
// resolves a request's subsystem label and its declared Price from this, and
// cloud's scope refuses middleware a subsystem installs outside what it declared
// — so an undeclared subtree is one whose requests are attributed to somebody
// else and on which this subsystem cannot install the typed-op Bridge. The same
// five are listed in manifest/apps.go, which the light host reads to route to
// this plugin; that copy is a literal on purpose (the host must not import an
// app package), so the two are kept equal by hand.
var Prefixes = []string{
	"/v1/admin/catalog",
	"/v1/admin/enablement",
	"/v1/enablement",
	"/v1/pricing",
	"/v1/pricing-policy",
}

// ops binds the typed ops to a receiver. A zip.TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the subsystem — so
// what an op needs beyond its input arrives as a receiver, and every op is a
// method value (o.listModels). That is also the only bound form cmd/zipdoc can
// lift prose from: a closure returned by a helper is a call expression with
// nothing to read, which is why the three model-list ops below are three methods
// over one helper rather than one closure factory called three times.
//
// log is the subsystem logger Mount built. The raw handlers used the REQUEST
// logger (c.Log()); a typed op has no request, and the failures logged here —
// the goja host, the overlay store — are the subsystem's, not the caller's.
type ops struct{ log luxlog.Logger }

// pricingNoInput is the In of an op that takes nothing off the wire: no path
// segment, no query, no body. Its identity comes from the context, never from a
// field — see callerOrg.
type pricingNoInput struct{}

// callerOrg is the VALIDATED org for a typed op — the one the identity boundary
// asserted and cloud.Bridge parked on the context, never a field of In. An In
// field is caller-supplied, so a tenant key read from one is a cross-tenant read
// the caller asserted for itself.
//
// Empty is a legitimate answer here and NOT an error: the pricing catalog is a
// public read surface, and an anonymous caller simply sees the public (ga) view
// with no org overlay and no opt-in affordance. It is the same answer
// principal.Org gave the raw handlers through trustedOrg, so the forged-tenant
// refusal the enablement attack tests pin is unchanged: a request carrying
// X-Org-Id with no validated principal parks nothing and resolves to "".
func callerOrg(ctx context.Context) string {
	org, _ := principal.OrgFrom(ctx)
	return org
}

// callerIsAdmin is c.IsAdmin() for a typed op: the gateway-minted
// X-User-IsAdmin claim, granted only to a validated SuperAdmin. It needs the
// REQUEST rather than the tenant because admin-ness lives in a header
// principal.OrgFrom does not carry.
//
// False off the HTTP path, where there is no request and therefore no attested
// caller — which fails closed, since every admin route here gates on it.
func callerIsAdmin(ctx context.Context) bool {
	if c, ok := cloud.Request(ctx); ok {
		return c.IsAdmin()
	}
	return false
}

// dispatchErr turns a non-200 answer from the @hanzo/pricing bundle into the
// error a typed op returns: the bundle's OWN status, carrying the bundle's own
// message. The raw handlers wrote that body through verbatim; an op states it as
// an error so the status still comes from the bundle and the message still comes
// from the bundle.
func dispatchErr(status int, body json.RawMessage) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return zip.Errorf(status, "%s", e.Error)
	}
	return zip.Errorf(status, "pricing catalog unavailable")
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/pricing openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
