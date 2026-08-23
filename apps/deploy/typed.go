package deploy

// typed.go is the ONE client between a /v1/deploy request and this plane's TYPED
// ops, and the ONE place the package reaches for the request from inside one.
//
// A typed op — func(context.Context, *In) (*Out, error) — receives a context and
// its decoded input and nothing else, so the fact every route here turns on is
// not in its hands: the caller's SCOPE. That scope is not the org
// principal.OrgFrom carries, and two of its facts say why:
//
//   - a platform SuperAdmin has NO org at all. It is the whole-FLEET view, keyed
//     on X-User-IsAdmin — a header principal.OrgFrom does not carry — and an org
//     is not merely absent from it, it would be wrong: the SuperAdmin reads every
//     platform namespace, not one tenant's.
//   - a normal org's scope is the INJECTIVE namespace.Sanitize slug, which
//     is the name of the tenant-<org> namespace its App CRs live in, not the
//     verbatim owner claim OrgFrom returns.
//
// So the scope is resolved through the request, by the SAME resolveScope
// (scope.go) every raw handler beside these ops uses — one rule, one place, no
// second slug and no second admin predicate.
//
// It FAILS CLOSED off the HTTP path. The CLI projection's LocalInvoke runs an op
// with no request at all, so there is no attested caller, no admin bit and no
// org: scopeOf refuses with exactly the 403 an unauthenticated REST call gets.
// An MCP tools/call at POST /mcp IS an ordinary HTTP request and carries both, so
// it is gated exactly like the REST route it mirrors — which is why the gate is
// here, in the op, and not in the middleware wrapped around the route: middleware
// runs on the REST projection alone, and a gate that only one projection runs is
// a hole in the other three.

import (
	"context"

	"github.com/hanzoai/cloud"
)

// zipdoc lifts the doc comment off each typed op and off each field of its In and
// Out into zipdoc_gen.go, which is the ONLY way that prose reaches the published
// document and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/deploy openapi` and by the Dockerfile's `go generate -run zipdoc
// ./...`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each typed op can be a method value. It
// carries STATE and no logic: every method resolves its scope and then reads the
// same cluster the raw handlers beside it read.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op that takes nothing off the wire — no body, no query,
// no path segment. Its whole input is the caller's own scope, which is never a
// field: an In field is caller-supplied, so a tenant key read from one is a
// cross-tenant read the caller asserted for itself.
type noInput struct{}

// appRef addresses ONE application by name.
type appRef struct {
	// Name is the application to read, from the path. It must be a DNS-1123 label
	// (lowercase alphanumerics and hyphens, starting and ending alphanumeric) —
	// every operator App CR's metadata.name satisfies that, and anything else is a
	// 400 rather than a lookup.
	Name string `json:"name"`
}

// revisionRef addresses ONE revision of one application.
type revisionRef struct {
	// Name is the application to read, from the path. It must be a DNS-1123 label.
	Name string `json:"name"`
	// Revision is the revision to describe, from the path. The empty revision and
	// "HEAD" both mean "whatever this application currently declares".
	Revision string `json:"revision"`
}

// scopeOf is the caller's tenant scope for a typed op: the whole fleet for a
// platform SuperAdmin, its own org's App CRs for a validated org member, and a
// refusal for anyone else. It delegates to resolveScope (scope.go), so a typed op
// and the raw handler beside it key the SAME boundary.
//
// The refusal is the plain 403; a browser NAVIGATION is turned into the sign-in
// bounce by the one middleware that owns that reshaping (bounce, scope.go), so
// the decision lives here and its presentation lives there.
func scopeOf(ctx context.Context) (scope, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return scope{}, forbidden() // off the HTTP path there is no attested caller
	}
	sc, ok := resolveScope(c)
	if !ok {
		return scope{}, forbidden()
	}
	return sc, nil
}

// superAdminOf is scopeOf narrowed to platform sudo — the gate the console's own
// configuration reads (settings, version) and its CD-plane read (gitops) keep,
// because those are fleet facts with no tenant dimension. It is the SAME
// predicate guard() applies to the raw handlers beside them.
func superAdminOf(ctx context.Context) (scope, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return scope{}, err
	}
	if !sc.superAdmin {
		return scope{}, forbidden()
	}
	return sc, nil
}

// consoleUser is the identity the session read reports: the validated
// principal's user ID, and whether it is a platform SuperAdmin. Both live in
// headers (X-User-Id, X-User-IsAdmin) that principal.OrgFrom does not carry,
// and the session read cannot go through scopeOf because it must ANSWER for an
// anonymous caller rather than refuse one. Empty and not-admin off the HTTP path.
func consoleUser(ctx context.Context) (user string, superAdmin bool) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", false
	}
	return c.User(), c.IsAdmin()
}

// appName is the {name} path segment as every route on this plane reads it:
// lowercased with whitespace removed, then required to be a DNS-1123 label. The
// normalization and the check are one function so a typed op and a raw handler
// cannot disagree about which names resolve.
func appName(raw string) (string, error) {
	name := regexpLower(raw)
	if !appNameRE.MatchString(name) {
		return "", errBadName
	}
	return name, nil
}
