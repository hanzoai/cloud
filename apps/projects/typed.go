package projects

// typed.go is the ONE seam between a /v1/projects request and this plane's TYPED
// ops, and the ONE place the package reaches for the request from inside one.
//
// A typed op — func(context.Context, *In) (*Out, error) — receives a context and
// its decoded input and nothing else. Three facts every route here turns on are
// therefore not in its hands, and none of them may EVER become an In field: an
// In field is caller-supplied, so a tenant key or a payer read from one is a
// cross-tenant read (or a cross-tenant SPEND) the caller asserted for itself.
//
//   - THE TENANT, and it is not the org principal.OrgFrom carries. An org-less
//     platform SuperAdmin is bucketed under the literal "admin" org (org(), in
//     projects.go), which OrgFrom cannot express — it refuses an empty org
//     outright, so reading the tenant through it alone would turn that live admin
//     bucket into a 403.
//   - PLATFORM SUDO ITSELF (X-User-IsAdmin), which two routes branch on: the
//     moderation field on a project update, and the platform-operator vouch that
//     binds a custom domain without a DNS proof.
//   - THE PAYER. Every deploy path gates and debits through principal.Ledger —
//     the SELECTED billing org, which a SuperAdmin masquerade deliberately moves
//     OFF the effective org — plus the validated project sub-scope, the request
//     id and the client IP the meter attributes spend by.
//
// So the request is resolved ONCE, here, by callerOf, and handed to the same
// org()/gateHosting()/meterDeploy()/resolve() the raw deploy handler beside these
// ops uses. One rule, one place, no second admin predicate and no second ledger.
//
// It FAILS CLOSED off the HTTP path. The CLI projection's LocalInvoke runs an op
// with no request at all, so there is no attested caller and no org: callerOf
// refuses with exactly the 403 an unauthenticated REST call gets. An MCP
// tools/call at POST /mcp IS an ordinary HTTP request and carries both, so it is
// gated exactly like the REST route it mirrors.
//
// THE MONEY WIRE stays the money wire. A refused balance is the fleet's NESTED
// {"error":{"code","message"}} 402/503, which zip's own error envelope does not
// speak — so a gated op returns cloud.Denied and the routes are registered
// through cloud.DenyEnvelope, which writes those bytes back verbatim. Same
// status, same body, same code every metered Hanzo client already reads.
//
// SCHEMA NAMES ARE PACKAGE-QUALIFIED (projectsProject, not Project). The
// published document has ONE namespace for schemas and openapi/compose.go refuses
// one name with two shapes: `projectView` was already claimed by apps/platform
// and `deployment` by apps/o11y, both with different fields. Qualifying is what
// keeps two products' nouns from colliding in a single spec.

import (
	"context"
	"github.com/hanzoai/cloud/apps/principal"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and off each field of its In
// and Out into zipdoc_gen.go, which is the ONLY way that prose reaches the
// published document and the MCP tool list — Go drops comments at compile time.
// Run by `make -C apps/projects openapi` and by the Dockerfile's
// `go generate -run zipdoc ./...`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so each typed op can be a method value — the
// only bound form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// void is "nothing on the wire", in either direction: the In of an op whose
// whole input is its URL and its caller, and the Out of one that answers 204 No
// Content. It is an ALIAS on purpose — an unnamed type publishes no schema, so a
// 204 is documented as the empty answer it actually is rather than being given a
// body it never sends.
type void = struct{}

// callerOf is the ONE identity resolution for a typed op: the validated request
// and the tenant it acts in. Everything past it — the hosting gate, the meter,
// the visibility gate, the moderation and operator-vouch branches — takes the
// request it hands back, so those rules are not re-implemented per op.
//
// The tenant is org() (projects.go), the same function the untyped deploy
// handler beside these ops calls, so both planes key the SAME boundary.
func (o ops) callerOf(ctx context.Context) (*zip.Ctx, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", principal.RefusedFrom(ctx) // no request ⇒ no attested caller
	}
	org, ok := org(c)
	if !ok {
		return nil, "", principal.RefusedFrom(ctx)
	}
	return c, org, nil
}

// siteOf resolves the target project for an op addressed by slug: the tenant
// from the validated principal, then the project keyed by (org, slug).
//
// Cross-tenant reach dies here, once. The org is never read from the request
// body, and the project lookup is keyed by it — so a foreign slug misses exactly
// like a nonexistent one and both render the same 404. There is no branch that
// can tell a caller "this project exists, but not for you".
func (o ops) siteOf(ctx context.Context, slug string) (*zip.Ctx, string, Project, error) {
	c, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, "", Project{}, err
	}
	p, err := o.project(ctx, org, slug)
	if err != nil {
		return nil, "", Project{}, err
	}
	return c, org, p, nil
}

// project reads one project by (org, slug), mapping a miss to the 404 every
// route on this surface answers and a store failure to the 500 it always did.
func (o ops) project(ctx context.Context, org, slug string) (Project, error) {
	return loadProject(o.s, ctx, org, slugOf(slug))
}

// slugOf normalizes a slug: lowercased and trimmed. It is the ONE normalization
// on this surface — slugParam (the untyped deploy route's path read) is this
// function — so a typed op and the raw handler beside it address the same row.
func slugOf(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// requireBody replays, at the point in the sequence the raw handler reached it,
// the refusal c.Bind has always answered: these writes take a JSON body, and a
// request with none — or with a content type this service does not parse — is a
// 400, not a write of empty values.
//
// zip's typed decode is TOLERANT by construction (it skips an empty body and
// leaves the In at its zero value), so a naive conversion would have changed
// what each of these routes ACCEPTS: a bodyless PATCH would have answered 200
// with nothing changed instead of the 400 it has always sent. This calls the
// SAME c.Bind over an empty target, so it is the same decision and the same
// message, not a re-implementation free to drift.
func requireBody(c *zip.Ctx) error { return c.Bind(&struct{}{}) }

// ---- addressing inputs ----

// projectsRef addresses ONE project by its org-unique slug.
// projectsStar is what star/unstar answer with: the state the project is now
// in for this person. It reports the RESULT rather than echoing the request, so
// an idempotent re-star and a first star are indistinguishable to a caller —
// which is the truth about what happened.
type projectsStar struct {
	// Starred is whether THIS caller has starred the project after the toggle —
	// their own bookmark, not a property the project carries, so two people see
	// two answers for one project.
	Starred bool `json:"starred"`
}

type projectsRef struct {
	// Slug is the project to act on, from the path. It is unique within the
	// caller's org and nowhere else, so another tenant's slug is a 404.
	Slug string `json:"slug"`
}

// projectsDeploymentRef addresses ONE deployment of one project.
type projectsDeploymentRef struct {
	// Slug is the project the deployment belongs to, from the path.
	Slug string `json:"slug"`
	// ID is the deployment id, from the path. A deployment of another project —
	// or of another tenant's project — is not found.
	ID string `json:"id"`
}

// projectsDomainRef addresses ONE custom hostname held by one project.
type projectsDomainRef struct {
	// Slug is the project the host is attached to, from the path.
	Slug string `json:"slug"`
	// Host is the custom hostname, from the path. It is cleaned to its canonical
	// form (lowercased, trailing dot dropped) before anything is looked up.
	Host string `json:"host"`
}

// projectsReleaseRef addresses ONE release of one site.
type projectsReleaseRef struct {
	// Slug is the site the release belongs to, from the path.
	Slug string `json:"slug"`
	// Release is the content-addressed release id ("rel_" + 32 hex chars), from
	// the path. Anything that is not that shape is not found, rather than being
	// interpolated into a storage prefix.
	Release string `json:"release"`
}
