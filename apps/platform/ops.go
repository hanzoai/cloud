package platform

// ops.go is what every route on this surface registers THROUGH.
//
// A typed op — zip.Get[In, Out] and friends — is ONE registry entry, and that
// entry is the single value the REST route, the OpenAPI document, the MCP tool,
// the CLI command and the generated SDK method are all projections of. A raw
// app.Get(path, func(*zip.Ctx) error) serves the same bytes and appears in none
// of them: it publishes an operationId and nothing else, so the SDK method cannot
// explain itself and the CLI command has no help text. That was the state of all
// 32 routes here, and the prose that should have lived on the handlers was kept
// instead in a hand-written openapi.Register/Describe table that had to be edited
// in lockstep with the router and, being a second source, could not be.
//
// A typed handler receives a context.Context and its decoded In and nothing else,
// so the three facts these handlers need arrive by the two canonical routes and no
// third:
//
//	the ORG      — the VALIDATED tenant key, never an In field. An In field is
//	               caller-supplied, so a tenant key read from one is a cross-tenant
//	               read the caller asserted for itself. See [ops.caller].
//	the REQUEST  — this surface does not merely read the caller's identity, it
//	               SPENDS it: /v1/run bills the caller's own ledger, /v1/runner
//	               compares a shared credential in constant time, and every deploy
//	               writes the actor and request id into the audit log. cloud.Bridge
//	               parks it; cloud.Request takes it back off.
//	the SERVICE  — a receiver. A typed handler has no parameter for it, so every op
//	               is a method on a value that holds the service, which is also the
//	               only bound form cmd/zipdoc can lift prose from.
//
// cloud.Bridge is NOT installed here, and no subsystem installs it. It belongs to
// whoever COMPOSES the app: only a composer knows the identity boundary has already
// run — the org is trustworthy only once SanitizeIdentity has minted it — and that
// no route is registered ahead of it. cloud.Serve installs it once for the whole
// binary; the tests here install it on their own hermetic app for the same reason.
// A copy installed by this package could only hang on a /v1/platform node while
// every op below registers on the root app, and zip judges the NODE rather than the
// path, so it would refuse to compose middleware that could never run.
//
// FAIL CLOSED OFF THE HTTP PATH. The CLI projection's LocalInvoke runs an op with
// no request at all, so caller and admit both find none and refuse. There is no
// second gate to keep in sync: an anonymous POST /mcp is refused by the same line
// that refuses an anonymous GET.

import (
	"context"
	"github.com/hanzoai/cloud/apps/principal"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document,
// the MCP tool list, the SDKs and the CLI — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the per-tenant platform service to its typed ops. Every route under
// /v1/platform/projects, plus the flat console reads, /v1/run and /v1/runner, is a
// method on this value.
type ops struct{ s *cloud.Service[state] }

// board binds the fleet drift service to its typed ops. It is a SECOND receiver
// rather than a field on ops because the two surfaces observe different things
// through different clients: ops is one tenant's namespace, board is the platform's
// own service tier across every platform namespace.
type board struct{ s *cloud.Service[fleetState] }

// noInput is the In of an op that takes nothing off the wire — no body, no query,
// no path segment. It is shared rather than redeclared per op because an empty
// struct carries no contract to document; the ops that DO take input each declare
// their own named In beside the handler that binds it.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name, so a defined type here would publish
// "200 with a body" about a route that answers 204 with none.
type noContent = struct{}

// caller resolves the two things a tenant op needs before it does anything: the
// VALIDATED org this request acts as, and the request itself.
//
// The org comes from [tenant] — the platform's one tenancy rule, which requires a
// validated principal and sanitizes the org into the same namespace key every
// cluster write derives from. It is deliberately not principal.OrgFrom: that
// returns the owner claim verbatim, and this surface keys namespaces and image
// refs on it, so it must be the injective sanitized form and it must carry the
// admin bucket the rest of the surface already resolves.
//
// The refusal is the same 403 the raw handlers wrote, in the same words, for the
// same two causes — no validated principal, or no org — so a client that reads the
// message sees no change.
func (o ops) caller(ctx context.Context) (*zip.Ctx, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", principal.RefusedFrom(ctx)
	}
	org, ok := tenant(c)
	if !ok {
		return nil, "", principal.Refused(c)
	}
	return c, org, nil
}

// request is the caller's own request for the ops that authorize on something
// OTHER than a tenant key — /v1/runner compares a shared build credential, and the
// release reads run behind cloud.Super. They resolve no org, so asking for one
// would refuse a legitimate machine caller that carries none.
func (o ops) request(ctx context.Context) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("invalid build token")
	}
	return c, nil
}

// admit is the fleet board's gate, called once at the top of every op. It is the
// same fail-closed predicate cloud.Guard applied when these three routes were raw
// handlers — cloud.Scope.Admits over cloud.AuthorityOf, refused with the scope's
// own sentence (cloud.Refuse) — moved from around the handler to the first line
// inside it, because a typed op has no zip.Handler for a wrapper to compose with.
//
// Moving it changed nothing about the rule and gained two things: it is visible
// where the handler is read, and it applies on the MCP and CLI projections too,
// which never pass through the router a wrapper would have lived on.
//
// It returns the request because an admitted op always needs it — the ROLE only
// opens the door, and the tenant boundary is then applied inside each handler by
// scopedNamespaces, which reads the caller's own validated org off it.
func (b board) admit(ctx context.Context, need cloud.Scope) (*zip.Ctx, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, need.Refusal()
	}
	if !need.Admits(cloud.AuthorityOf(c)) {
		return nil, need.Refusal()
	}
	return c, nil
}

// ── the addresses this surface is reached by ─────────────────────────────────
//
// A path segment binds onto the In field whose json tag names it, and it binds
// LAST — after the body and after the query — because the URL is the addressing
// authority: POST /projects/web/apps/api/deploy deploys web/api whatever a body
// claims. Each address is spelled out as its own type rather than embedded from a
// shared carrier, because cmd/zipdoc keys a field's prose on the OUTER type's name
// and go/types does not promote an embedded struct's fields, so an embedded
// carrier would publish its shape with no prose on any field of it.

// projectRef addresses one of the caller org's projects.
type projectRef struct {
	// Project is the project's name, from the path.
	Project string `json:"project"`
}

// appRef addresses one application under one project.
type appRef struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
}

// deploymentRef addresses one deployment of one application.
type deploymentRef struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
	// ID is the deployment's id, from the path.
	ID string `json:"id"`
}

// domainRef addresses one hostname attached to one application.
type domainRef struct {
	// Project is the project the application lives under, from the path.
	Project string `json:"project"`
	// App is the application's slug, from the path.
	App string `json:"app"`
	// Host is the hostname, from the path.
	Host string `json:"host"`
}

// slugOf normalizes a path segment that names a project or an application: the
// value is an org-unique handle AND a CR name AND part of a URL, so it is folded
// to the one lowercase, trimmed form every store read and cluster write uses.
func slugOf(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
