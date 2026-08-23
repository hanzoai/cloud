package o11y

// typed.go is the ONE client between a request and o11y's TYPED ops, and the ONE
// place this package reaches for the request at all.
//
// A typed op — func(context.Context, *In) (*Out, error) — receives a context and
// its decoded input and nothing else, so four facts the o11y handlers need are
// not in its hands: the validated tenant, validated-ness itself, platform-sudo-ness
// and the caller's project scope. All four are per-request VALUES the typed
// signature drops, so they are resolved here and nowhere else:
//
//   - the TENANT and VALIDATED-NESS both come off the context, parked there by
//     cloud.Bridge (installed by whoever composes this app, never by the app
//     itself — see mount) — principal.OrgFrom and
//     principal.ValidatedFrom, the two facts a gate turns on. Neither is EVER an
//     In field: an In field is caller-supplied, so a tenant key read from one is
//     a cross-tenant read the caller asserted for itself.
//   - ADMIN-NESS lives in a header (X-User-IsAdmin) that neither of those
//     carries, so it needs the REQUEST.
//   - the PROJECT is a server-minted header too (X-Project-Id), read through
//     principal.ProjectScope so the "default == empty == whole org" convention
//     stays in the one place that owns it.
//
// Concentrating the two remaining cloud.Request calls in this file is deliberate:
// the escape hatch is pinned (cloud/typed_request_gate_test.go), and one client
// file with one justification beats the same call scattered across handlers.
//
// Every one FAILS CLOSED off the HTTP path — the CLI projection's LocalInvoke
// runs an op with no request at all. No request means no attested caller: not an
// admin, not validated, and no project narrowing. tenantOf refuses outright, so
// an org-scoped op never runs without a tenant.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
)

// o11yPrefix is the subtree this package's cloud-native routes live under — the
// group every typed op is declared on. ONE definition, so the op's path and the
// log line can never name different subtrees. No middleware hangs on this group:
// cloud.Bridge is the composer's to install, once at the root, ahead of every
// route it gates; see mount.
const o11yPrefix = "/v1/o11y"

// productPrefix is the PRODUCT face of that subtree: the reads keyed by a console
// product slug, which answer a different question from the module's same-named
// reads over the whole telemetry store. The dimension is in the address so the two
// never share a name — see scope.go.
const productPrefix = o11yPrefix + "/product"

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make -C
// apps/o11y openapi` and by the Dockerfile's `go generate -run zipdoc ./...`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

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
//
// It reads the bit Bridge PARKED, beside the org. This used to reach for the
// request to recompute principal.Validated(c) — the same answer by the longer
// way, and the ONE of this file's three resolvers that never needed a header of
// its own.
func callerValidated(ctx context.Context) bool {
	return principal.ValidatedFrom(ctx)
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
