package o11y

// typed.go is the ONE seam between a request and o11y's TYPED ops, and the ONE
// place this package reaches for the request at all.
//
// A typed op — func(context.Context, *In) (*Out, error) — receives a context and
// its decoded input and nothing else, so three facts the o11y handlers need are
// not in its hands: the validated tenant, platform-sudo-ness, and the caller's
// project scope. All three are per-request VALUES the typed signature drops, so
// they are resolved here and nowhere else:
//
//   - the TENANT comes off the context, parked there by cloud.Bridge (installed
//     at the top of Mount). It is NEVER an In field: an In field is
//     caller-supplied, so a tenant key read from one is a cross-tenant read the
//     caller asserted for itself.
//   - ADMIN-NESS and VALIDATED-NESS live in headers (X-User-IsAdmin, X-User-Id)
//     that principal.OrgFrom does not carry, so they need the REQUEST.
//   - the PROJECT is a server-minted header too (X-Project-Id), read through
//     principal.ProjectScope so the "default == empty == whole org" convention
//     stays in the one place that owns it.
//
// Concentrating the three cloud.Request calls in this file is deliberate: the
// escape hatch is pinned (cloud/typed_request_gate_test.go), and one seam file
// with one justification beats the same call scattered across three handlers.
//
// Every one FAILS CLOSED off the HTTP path — the CLI projection's LocalInvoke
// runs an op with no request at all. No request means no attested caller: not an
// admin, not validated, and no project narrowing. tenantOf refuses outright, so
// an org-scoped op never runs without a tenant.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// o11yPrefix is the subtree this package's cloud-native routes live under — the
// group every typed op is declared on, and the group cloud.Bridge is installed
// on. ONE definition, so the op's path, the middleware's reach and the log line
// can never name different subtrees.
const o11yPrefix = "/v1/o11y"

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make -C
// apps/o11y openapi` and by the Dockerfile's `go generate -run zipdoc ./...`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// tenantOf is the validated org for a typed op — the one the gateway asserted
// and cloud.Bridge parked on the context. The 403 text is the same one every
// untyped o11y handler answered with, so the wire is unchanged.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// callerIsAdmin is admin() for a typed op: the validated platform SuperAdmin bit
// (X-User-IsAdmin, set only for the reserved admin org after SanitizeIdentity).
// It needs the REQUEST because that bit rides in a header principal.OrgFrom does
// not carry. False off the HTTP path — no request, no platform sudo.
func callerIsAdmin(ctx context.Context) bool {
	if c, ok := cloud.Request(ctx); ok {
		return admin(c)
	}
	return false
}

// callerValidated is principal.Validated for a typed op — "is there a verified
// principal at all", which is WEAKER than tenantOf (that one also requires an
// org). The status read gates on this one: infra health is not tenant-
// partitioned, so an org-less but validated caller is served. False off the HTTP
// path.
func callerValidated(ctx context.Context) bool {
	if c, ok := cloud.Request(ctx); ok {
		return principal.Validated(c)
	}
	return false
}

// callerProject is principal.ProjectScope for a typed op: the caller's project
// as a storage key, "" for the org's default project (the whole-org view). It
// only ever NARROWS the caller's own org — the org gate above has already run —
// so the "" it answers off the HTTP path is the same whole-org default a request
// without X-Project-Id gets, on a path where tenantOf has already refused.
func callerProject(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return principal.ProjectScope(c)
	}
	return ""
}
