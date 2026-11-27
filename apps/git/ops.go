package git

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk/principal"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// ops.go is git's TYPED-op client.
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
//     with no tenant and is refused with exactly the 403 the REST path gives. A
//     call that DOES carry one resolves it from the caller zip carries on the
//     context instead — see tenantOfCaller.
type ops struct{ s *cloud.Service[state] }

// tenant is the isolation key of one request: the validated org plus its optional
// project sub-scope. One value, so a handler cannot carry one and forget the other.
type tenant struct {
	org     string
	project string
	// user is the validated principal INSIDE that org (the gateway-minted
	// X-User-Id). It is not an isolation key — org is — but it is who a record
	// names (a pull request's author, a push's pusher), and bridging it here is
	// the ONLY way a typed op can learn it: the handler receives a
	// context and its decoded In, never the request. Like org it is read from the
	// validated request and never from an In field, which the caller supplies.
	user string
	// admin is whether the caller administers this org (an org admin or a
	// SuperAdmin), by the platform's one rule (cloud.Admin). It is what may
	// change a repository's CI through the file push (push.go).
	admin bool
}

// principalKey is the context key bridgePrincipal parks the tenant under.
type principalKey struct{}

// tenantFrom resolves the tenant from the request — the ONE construction, used by
// the bridge for typed ops and directly by the raw handlers that stayed raw.
func tenantFrom(c *zip.Ctx) (tenant, error) {
	o, ok := org(c)
	if !ok {
		return tenant{}, principal.Refused(c)
	}
	return tenant{org: o, project: projectScope(c), user: strings.TrimSpace(c.User()),
		admin: cloud.Admin.Admits(cloud.AuthorityOf(c))}, nil
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
//
// It has TWO readers, and the second is not a second rule. An MCP tools/call
// invokes the op DIRECTLY — no route, so bridgePrincipal never ran and nothing
// parked a tenant — and zip carries the caller over that path instead. That is
// the same second reader principal.OrgFrom and principal.ProjectFrom already
// have; without it every git tool was refused even for a fully validated caller,
// so the whole subsystem was unreachable as a tool.
func tenantOf(ctx context.Context) (tenant, error) {
	if t, ok := ctx.Value(principalKey{}).(tenant); ok {
		return t, nil
	}
	return tenantOfCaller(ctx)
}

// tenantOfCaller builds the tenant from the caller zip carries on the context —
// the identity the request stated, read by the SAME rules tenantFrom applies to
// a request: principal.OrgOf refuses an org with no validated user, orgRE refuses
// a path-unsafe one, and the project only ever narrows that org.
//
// It is never a caller-supplied In field, and it is not a wider trust: what a
// caller may state is decided by where it arrived. The edge deletes X-User-Id and
// re-mints it only from a credential it verified (SanitizeIdentity), so a forged
// one never reaches here; on the internal plane socket, which no edge route
// touches, a sibling's statement is worth what that socket is worth (cloud.UseMCP).
func tenantOfCaller(ctx context.Context) (tenant, error) {
	caller := zip.CallerOf(ctx)
	o, ok := principal.OrgOf(caller.User, caller.Org)
	if !ok || !orgRE.MatchString(o) {
		return tenant{}, principal.RefusedFrom(ctx)
	}
	return tenant{
		org:     o,
		project: foldProject(principal.ProjectFrom(ctx)),
		user:    strings.TrimSpace(caller.User),
		admin:   cloud.Admin.Admits(cloud.AuthorityIn(ctx)),
	}, nil
}

// repoRef addresses one repo by the name in the URL.
type repoRef struct {
	// Name is the repo's org-unique handle, from the :name path segment. A
	// trailing ".git" is stripped.
	Name string `json:"name"`
}

// internalErr maps a core failure onto the 500 the raw handlers return, so the
// two shapes of handler produce the same body for the same failure.
func internalErr(err error) error { return zip.Errorf(http.StatusInternalServerError, "%v", err) }
