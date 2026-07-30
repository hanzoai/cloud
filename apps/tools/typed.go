package tools

// typed.go is the ONE seam between a request and the tool plane's TYPED ops, and
// the ONE place this package reaches for the request at all.
//
// A typed op — func(context.Context, *In) (*Out, error) — receives a context and
// its decoded input and nothing else, so four facts the tools handlers need are
// not in its hands:
//
//   - the validated TENANT. It comes off the context, parked there by
//     cloud.Bridge (installed once for the whole binary in Serve, after the
//     identity boundary and before MountAll). It is NEVER an In field: an In
//     field is caller-supplied, so a tenant key read from one is a cross-tenant
//     read the caller asserted for itself.
//   - the PROJECT, the org's sub-scope every activation and listing is keyed on.
//     It rides in a server-minted header (X-Project-Id) that principal.OrgFrom
//     does not carry, so it needs the REQUEST — read through principal.Project,
//     the same function the untyped handlers beside these ops used.
//   - the ACTOR of a write. The activation store records who turned a tool on,
//     and that validated user id is X-User-Id, which principal.OrgFrom does not
//     carry either.
//   - the ATTRIBUTION an audit record carries: the user, their email,
//     admin-ness, the method, the path, the source IP and the request id are all
//     request facts.
//
// Concentrating those cloud.Request calls in this file is deliberate: the escape
// hatch is pinned (cloud/typed_request_gate_test.go), and one seam file with one
// justification beats the same call scattered across eleven handlers.
//
// Every resolver FAILS CLOSED off the HTTP path — the CLI projection's
// LocalInvoke runs an op with no request at all. No request means no attested
// caller: no project narrowing, no actor, and no audit record (an unattributable
// record is worse than none). tenantOf refuses outright, so an org-scoped op
// never runs without a tenant.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make -C
// apps/tools openapi` and by the Dockerfile's `go generate -run zipdoc ./...`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// toolOps is the receiver every typed op of this package hangs off. A method
// value is the only bound form cmd/zipdoc can lift prose from — a closure
// returned by a helper is a call expression with nothing to read — which is why
// the two source views (skills, mcp) are methods here rather than two
// applications of listBySource.
type toolOps struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name, so a defined type here would publish
// "200 with a body" about a route that answers 204 with none.
type noContent = struct{}

// tenantOf is the validated org for a typed op — the one the gateway asserted
// and cloud.Bridge parked on the context, never a field of In. The 403 text is
// the one every untyped tools handler answered with, so the wire is unchanged.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// projectOf is principal.Project for a typed op: the org's sub-scope, from the
// server-minted X-Project-Id. It needs the REQUEST because principal.OrgFrom
// carries the org alone. Off the HTTP path it answers the default project, which
// is what an absent header has always meant — and no op reaches it there, since
// tenantOf refuses first.
func projectOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return principal.Project(c)
	}
	return principal.DefaultProject
}

// scopeOf is the (org, project) a listing resolves for — tenantOf AND-ed with
// projectOf, so the project can only ever narrow the caller's OWN org.
func scopeOf(ctx context.Context) (Scope, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return Scope{}, err
	}
	return Scope{Org: org, Project: projectOf(ctx)}, nil
}

// callerOf is the validated user id an activation write is recorded under
// (c.User(), X-User-Id) — the actor, not the tenant. Empty off the HTTP path,
// where there is no attested caller to name.
func callerOf(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}

// principalOf is the whole validated caller a DISPATCH needs — org, project,
// user, owner, admin-ness and the credential headers a provider replays — which
// is strictly more than tenantOf's org. It comes off the REQUEST because the
// credential set does, and it fails closed off the HTTP path for the same reason
// tenantOf does: no request means no attested caller, and a dispatch with no
// caller has no scope to be confined to.
func principalOf(ctx context.Context) (Principal, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return Principal{}, zip.ErrForbidden("a validated principal is required")
	}
	p, ok := PrincipalFrom(c)
	if !ok {
		return Principal{}, zip.ErrForbidden("a validated principal is required")
	}
	return p, nil
}

// meter records the one orchestration unit a tool call bills — meterUnit with the
// request resolved off the context. Off the HTTP path there is nothing to bill.
func (o toolOps) meter(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		meterUnit(o.s, c)
	}
}

// audit appends one audit record from a typed op — audrecordAction with the
// request resolved off the context. Off the HTTP path there is no actor, no
// method, no path and no source IP, so it records NOTHING rather than an
// unattributable row.
func (o toolOps) audit(ctx context.Context, action, org, resourceID, result string, status int) {
	if c, ok := cloud.Request(ctx); ok {
		audrecordAction(o.s, c, action, org, resourceID, result, status)
	}
}
