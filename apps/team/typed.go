package team

// typed.go is what a TYPED team op can learn about its caller, and nothing else.
//
// A zip.Get[In, Out] handler receives a context.Context and its decoded In. Team
// authenticates two different ways, and neither is an In field:
//
//   - the GATEWAY principal (X-User-Id / X-Org-Id, validated by the identity
//     boundary), which cloud.Bridge parks on the context — the bots surface;
//   - team's OWN HS256 session token (Authorization: Bearer, else the HttpOnly
//     account-token cookie), minted by this service's OAuth callback and signed
//     with SERVER_SECRET — the billing and files surfaces.
//
// The second one is why cloud.Request is here: a team session token rides in a
// header or a cookie, and principal.OrgFrom cannot carry either. Resolving it is
// an identity gate, exactly the class the pin admits — and keeping it in THIS
// file is why the pin has one team entry instead of one per plane.
//
// EVERY resolver below fails closed off the HTTP path: the CLI projection's
// LocalInvoke runs an op with no request at all, so `sessionOf` and `admin` find
// nothing and the op refuses rather than inventing an identity.

import (
	"context"
	"net/http"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// unavailable is THE refusal a DEGRADED team subsystem makes — one status, one
// message, whichever handler shape asks for it. Mount's `guard` wrapper returns
// it for the untyped routes; a typed op returns it from its own first line,
// because a TypedHandler is not a zip.Handler and cannot be wrapped by one
// (and a router wrapped with zip's With() is a prefix cmd/zipdoc cannot
// resolve, which would file every doc comment under the wrong path).
func unavailable() error {
	return zip.Errorf(http.StatusServiceUnavailable, "team: signing secret not configured")
}

// tenant is the VALIDATED org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. It IS principal.Org, read at the one seam that
// cannot call it, so the trust decision stays in one function.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("validated org required")
	}
	return org, nil
}

// admin reports that the caller is a platform SuperAdmin (c.IsAdmin() — the
// X-User-IsAdmin the identity boundary mints only for a validated owner ==
// AdminOrg). It needs the REQUEST rather than the tenant because admin-ness
// lives in a header principal.OrgFrom does not carry. False off the HTTP path:
// no request, no attested caller, no admin rights.
func admin(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	return ok && c.IsAdmin()
}

// sessionOf resolves (account, org) from the request's VERIFIED team session or
// workspace token — the SAME orgPrincipal resolution the untyped billing and
// files handlers use, reached from a typed op. Off the HTTP path there is no
// request and therefore no token, which fails closed with the same error an
// absent one gives.
func sessionOf(ctx context.Context, secret string) (account, org string, err error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", errNoOrg
	}
	return orgPrincipal(c, secret)
}

// noStore marks a response uncacheable by any intermediary — per-tenant data
// must never be served to the next caller. A response header is a fact about
// the HTTP response, so it needs the request; off the HTTP path there is no
// response to mark and this is a no-op.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// none is NOTHING on the wire — the In of an op addressed entirely by the
// caller's own identity, and the Out of one whose success carries no body. One
// name because it is one value.
//
// It is an ALIAS for the anonymous empty struct, not a definition, on purpose:
// zip declares a request or response SCHEMA only for a type that HAS a name, so
// a named empty struct would document a body neither side sends — a required
// `{}` request on an op that reads no body, and an empty object on a 204 that
// carries nothing. Nameless, the document says what the wire does: no
// requestBody, and "204 No Content".
type none = struct{}
