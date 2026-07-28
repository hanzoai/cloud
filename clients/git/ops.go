package git

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// ops.go is git's TYPED-op seam.
//
// A typed op (zip.Get[In, Out] and friends) is ONE value with N projections: the
// REST route, the JSON Schema in /.well-known/openapi.json, the tool at /mcp and
// the CLI command are all read off the same registry entry. A raw
// func(*zip.Ctx) error is a route and nothing else — invisible to all four — so
// every git route with a real request/response shape is registered typed.
//
// A TypedHandler is func(context.Context, *In) (*Out, error). Two things it does
// not get, and how each is supplied:
//
//   - THE SERVICE. There is no parameter for it, so it arrives as a RECEIVER:
//     ops binds it once and every op is a method value (o.getRepo). That is also
//     the only bound form cmd/zipdoc can lift prose from — it resolves a named
//     function or a method to its declaration, and a closure returned by a helper
//     is a call expression with no declaration to read, so the doc comments would
//     never reach the spec. ops therefore carries STATE and no logic: each method
//     resolves its tenant and calls the same core/helper func the raw handlers do.
//
//   - THE PRINCIPAL. The handler sees no headers, so it cannot read the
//     gateway-minted X-Org-Id itself. bridgePrincipal parks the VALIDATED tenant
//     on the request context once at /v1/git, and tenantOf reads it back. It is a
//     context value and NEVER an In field: an In field would let a caller assert
//     its own org, which is a cross-tenant read. A projection that carries no
//     principal — an anonymous POST /mcp tools/call — therefore reaches the op
//     with no tenant and is refused with exactly the 403 the REST path gives.
type ops struct{ s *cloud.Service[state] }

// tenant is the isolation key of one request: the validated org plus its optional
// project sub-scope. One value, so a handler cannot carry one and forget the other.
type tenant struct {
	org     string
	project string
}

// principalKey is the context key bridgePrincipal parks the tenant under.
type principalKey struct{}

// tenantFrom resolves the tenant from the request — the ONE construction, used by
// the bridge for typed ops and directly by the raw handlers that stayed raw.
func tenantFrom(c *zip.Ctx) (tenant, error) {
	o, ok := org(c)
	if !ok {
		return tenant{}, zip.ErrForbidden("X-Org-Id required")
	}
	return tenant{org: o, project: projectScope(c)}, nil
}

// bridgePrincipal carries the validated tenant from the request headers onto the
// request CONTEXT, which is the only thing a typed handler receives. Installed
// once on /v1/git (routes, git.go) ahead of the ops it feeds. A request with no
// valid org parks nothing, so the ABSENCE of a principal can never read as one.
func bridgePrincipal(c *zip.Ctx) error {
	if t, err := tenantFrom(c); err == nil {
		c.SetContext(context.WithValue(c.Context(), principalKey{}, t))
	}
	return c.Next()
}

// tenantOf reads the bridged tenant. Its 403 is the same one tenantFrom returns,
// so a typed op and a raw handler answer an unauthenticated caller identically.
func tenantOf(ctx context.Context) (tenant, error) {
	t, ok := ctx.Value(principalKey{}).(tenant)
	if !ok {
		return tenant{}, zip.ErrForbidden("X-Org-Id required")
	}
	return t, nil
}

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. The
// registrar needs an Out type and a nil *Out is what makes zip write 204; naming
// it says "nothing comes back" in the spec too.
type noContent struct{}

// repoRef addresses one repo by the name in the URL.
type repoRef struct {
	// Name is the repo's org-unique handle, from the :name path segment. A
	// trailing ".git" is stripped.
	Name string `json:"name"`
}

// internalErr maps a core failure onto the 500 the raw handlers return, so the
// two shapes of handler produce the same body for the same failure.
func internalErr(err error) error { return zip.Errorf(http.StatusInternalServerError, "%v", err) }
