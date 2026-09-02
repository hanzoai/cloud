package pricing

// The typed-op client for the pricing surface.
//
// A typed op (zip.Get[In, Out] and friends) is ONE registry entry with N
// projections — the REST route, the OpenAPI operation's schema and prose, the
// MCP tool, the CLI command and the generated SDK method all follow from it. An
// untyped route gets a route and nothing else. This file holds what every op on
// this surface shares: the receiver zipdoc lifts prose through, the two identity
// readers, and the input every op that takes nothing off the wire uses.
//
// WHAT STAYS RAW. ONE of thirty-three, and it is not a matter of effort. The
// partition is PINNED by typed_wire_test.go (untypedByDesign +
// TestEveryRouteIsTypedOrNamed), so the next route here is typed by default, and
// the reason below is additionally PROVED there against the toolchain in go.mod
// rather than asserted: a blocker fixed upstream turns the suite red.
//
//   - PATCH /v1/admin/pricing/catalog/models/* addresses a model id that may contain
//     '/', so it routes through a greedy wildcard — and typing it does not merely
//     publish a bad parameter, it REFUSES THE WHOLE DOCUMENT. zip keys a typed op
//     by the fiber pattern (".../models/*"): Template rewrites `:name` segments and
//     passes `*` through verbatim (zip@v1.36.3/address.go:61). This document keys
//     the same route by its URI template (".../models/{wildcard1}",
//     openapi/openapi.go:811, because fiber's own `*1` is not a legal template
//     name). openapi.Fold looks the op's route up under zip's spelling, does not
//     find it, and errors (openapi/openapi.go:705) — and Spec builds ONE document,
//     so every other pricing operation goes down with it.
//
//     That is the WHOLE reason, and it used to be stated as two. The second half
//     said the input would additionally need a field tagged `json:"*1"`, which
//     every projection would publish. That is FALSE at this pin and has been since
//     `url:` gained its own name: urlFieldName reads the `url:` tag ahead of
//     `json:` (zip@v1.36.3/openapi.go:884), so `Name string` tagged
//     `json:"-" url:"*1"` binds fiber's capture and publishes nothing in the body.
//     A dead clause is worse than a missing one — a reader who checks it, finds it
//     false and converts the route is wrong for a reason nothing warned them
//     about — so it is corrected here rather than left standing.
// PATCH /v1/admin/pricing/catalog/providers/:name was on this list and is now an op. Its
// reason was that `overrides`, an RFC 7386 merge patch stored and echoed verbatim,
// pins the Go type to json.RawMessage — which zip published as an ARRAY OF
// INTEGERS, because schemaOf took the Slice arm before it asked whether the type
// has a marshaler of its own. zip v1.18.9 asks first, so a RawMessage now
// publishes as the unconstrained "any JSON" it is, and the refusal expired. The
// escape named here — "a schemaOf that can describe an arbitrary JSON value" — is
// exactly what arrived. typed_wire_test.go is what noticed, by pinning the lie and
// going red when it stopped being told.
//
// One residual delta, recorded rather than hidden: zip decodes the body before the
// handler runs, so a NON-admin sending malformed JSON now sees 400 instead of the
// 403 the raw handler answered (it checked IsAdmin first). The authorizer is
// deliberately post-decode in zip — it authorizes the decoded value so the
// decision cannot diverge from execution — so this is not avoidable without
// giving up the op. It reveals only that the body was unparseable.
//
// The fourteen fixed sections (/v1/pricing/{compute,cloud,subscriptions,…})
// were on this list too, on the grounds that they proxy the
// bundle's bytes verbatim. They do not: apps/goja re-marshals the bundle's
// answer with Go's encoding/json before it ever reaches a handler, so decoding
// and re-marshalling it is byte-identical. They are ops now — see sections.go.

import (
	"context"
	"encoding/json"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Prefixes are the absolute subtrees this subsystem answers on: the customer
// plane (/v1/pricing — the catalog read AND the self-service enablement leaves
// under it) and the operator plane (/v1/admin/pricing — the catalog overlay
// editor and the enablement mutations, HIP-0139 §3.2).
//
// Declaring them is not decoration. cloud.Declare builds the prefix table that
// resolves a request's subsystem label and its declared Price from this, and
// cloud's scope refuses middleware a subsystem installs outside what it declared
// — so an undeclared subtree is one whose requests are attributed to somebody
// else. The same
// two are listed in manifest/apps.go, which the light host reads to route to
// this plugin; that copy is a literal on purpose (the host must not import an
// app package), so the two are kept equal by hand.
var Prefixes = []string{
	"/v1/admin/pricing",
	"/v1/pricing",
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

// callerGrant is what the credential this request arrived on may reach — the
// second half of the rule cloud/grant.go states, read off the boundary's own
// attestation so a request cannot state its own limit.
//
// Nil off the HTTP path and nil for a session, both of which Reachable passes
// through untouched. That is the same open default cloud.GrantOf answers with,
// and it fails closed for nothing: the org rule and the scope gate have already
// run by the time a catalog is narrowed.
func callerGrant(ctx context.Context) cloud.Grant {
	if c, ok := cloud.Request(ctx); ok {
		return cloud.GrantOf(c)
	}
	return nil
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
