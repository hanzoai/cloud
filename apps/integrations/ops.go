package integrations

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops.go is where integrations registers its TYPED ops.
//
// A typed op (zip.Get[In, Out] and friends) is ONE registry entry with N
// projections: the REST route, the JSON Schema in /.well-known/openapi.json, the
// tool at /mcp and the CLI command are all read off it. A raw
// func(*zip.Ctx) error serves the same bytes and is invisible to all four — so
// every route on this surface with a real request/response shape is registered
// typed, and the ones that stay raw say why at their registration.
//
// A TypedHandler is func(context.Context, *In) (*Out, error). Three things it is
// never handed, and where each comes from:
//
//   - THE SERVICE. There is no parameter for it, so it arrives as a RECEIVER:
//     ops binds it once and every op is a method value (o.list). That is also the
//     only bound form cmd/zipdoc can lift prose from — it resolves a named
//     function or a method to its declaration, while a closure returned by a
//     helper is a call expression with no declaration to read, so the doc
//     comments would never reach the spec.
//   - THE ORG. cloud.Bridge parks the VALIDATED org on the request context and
//     principal.OrgFrom reads it back. It is a context value and NEVER an In
//     field: an In field is caller-supplied, so a tenant key read from one is a
//     cross-tenant read the caller asserted for itself. This subsystem does not
//     install it: whoever composes the program does, once at the root, because it
//     must run after the identity check that mints the org and before any
//     subsystem's routes — an order only the composer can hold (serve.go).
//   - THE OTHER IDENTITY FACTS. Two more live only in headers: the caller's
//     own-org admin bit (what the AdminOnly connectors gate on) and their user id
//     (what the per-USER /v1/connectors plane keys every row by). bridgeFacts
//     parks them beside the org, and it IS this subsystem's own.
//
// So bridgeFacts is the one thing registered here, at the ROOT and gated by PATH
// (scope.Use), rather than on a group per served prefix. A group per prefix reads
// better and does not run: these routes are registered on the root node, so the
// group node would declare middleware over a subtree holding no routes, which zip
// refuses to compose — the app exits instead of listening. The gate is the prefix
// set manifest/apps.go declares for this subsystem, so bridgeFacts reaches
// /v1/integrations, /v1/connectors and the connector webhook, and nothing that
// belongs to anyone else. Ahead of the ops, always: fiber runs middleware in
// registration order, so one installed after its leaves never runs.
//
// FAIL CLOSED OFF THE HTTP PATH. zip publishes every typed op as an MCP tool at
// POST /mcp and as a CLI command, neither of which passes through those prefixes
// and therefore neither carries a bridge. An op reached that way sees no org
// parked and refuses with exactly the 403 an unauthenticated REST call gets — the
// handler's own gate, with no second gate to keep in sync (pinned by
// ops_projection_test.go).

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// ops binds the mounted Service so each typed op can be a method value. It carries
// STATE and no logic: every method resolves its org and then calls the same
// helpers the raw handlers call.
type ops struct{ s *cloud.Service[state] }

// facts are the identity values this surface reads that cloud.Bridge does not
// carry. The ORG is deliberately absent: cloud.Bridge already carries it, and one
// fact with two carriers is one carrier too many.
type facts struct {
	// admin is principal.IsOrgAdmin — admin OF ONE'S OWN org, the AdminOnly
	// connector gate. NOT SuperAdmin (see principal's two-predicate note).
	admin bool
	// user is the validated principal's user id, the per-USER connector plane's
	// row key. CLONED at the bridge: c.User() is a zero-copy view into the reused
	// fasthttp request buffer, and this value keys rows and KMS paths that outlive
	// the request.
	user string
}

// factsKey is the context key bridgeFacts parks facts under. Unexported zero-size
// type, so no other package can mint or read one.
type factsKey struct{}

// bridgeFacts carries the header-only identity facts onto the request context —
// the twin of the org cloud.Bridge parks. A request that never passed a bridge
// reads back the zero facts, so absence is "not an admin, no user", which every
// gate below refuses.
func bridgeFacts(c *zip.Ctx) error {
	c.SetContext(context.WithValue(c.Context(), factsKey{}, facts{
		admin: principal.IsOrgAdmin(c),
		user:  strings.Clone(strings.TrimSpace(c.User())),
	}))
	return c.Next()
}

func factsOf(ctx context.Context) facts {
	f, _ := ctx.Value(factsKey{}).(facts)
	return f
}

// orgAdmin reports the bridged own-org admin bit.
func orgAdmin(ctx context.Context) bool { return factsOf(ctx).admin }

// caller resolves the validated (org,user) pair the per-USER connector plane keys
// every row by: authed's two steps plus the user id, refused 400 when it could not
// be a path segment. Unchanged from the raw form it replaces — missing identity is
// 403, because authed fails before any user check runs.
func caller(ctx context.Context) (org, user string, err error) {
	org, err = authed(ctx, principalRequired)
	if err != nil {
		return "", "", err
	}
	user = factsOf(ctx).user
	if !validUser(user) {
		return "", "", zip.ErrBadRequest("invalid user id")
	}
	return org, user, nil
}

// authed is the two-step org gate every org-scoped handler on this surface opens
// with, unchanged from its raw form: no validated principal → 403 carrying the
// op's own message, an org that is not a DNS-1123 label → 400. forbidden is the
// message that op has always answered with.
func authed(ctx context.Context, forbidden string) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden(forbidden)
	}
	if !validOrg(org) {
		return "", zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	return org, nil
}

// principalRequired is the 403 message the read/status ops have always answered
// an unvalidated caller with. Named once so the typed and raw halves cannot drift.
const principalRequired = "a validated principal is required"

// noArgs is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire.
type noArgs struct{}

// providerRef addresses one connector by the :provider path segment.
type providerRef struct {
	// Provider is the registry id of the connector — "slack", "github",
	// "cloudflare". Unknown ids are 404, as are the user-plane (/v1/connectors)
	// providers, which this surface never resolves.
	Provider string `json:"provider"`
}

// id is the provider id as the router yielded it, trimmed exactly as the raw
// handlers' providerParam trims it.
func (r providerRef) id() string { return strings.TrimSpace(r.Provider) }

// githubRepoRef addresses one GitHub repository by the :repo path segment.
//
// The name carries "github" because the OpenAPI schema namespace is FLAT across
// the fleet — zip keys a schema on the bare Go type name, and openapi.Weave
// refuses two apps that mean different things by one name. apps/git already
// publishes a `repoRef` keyed by `name` (a repo hosted BY us); this one is keyed
// by `repo` (a repo GitHub grants our App). Same word, two shapes, so the type
// that is about GitHub says so — the same way githubReposOut and githubPagesView
// already do in this package. The collision was latent while every op taking this
// input was bodyless (GET/DELETE emit no request schema); the first one with a
// body — POST …/pages/builds — made the weave refuse.
type githubRepoRef struct {
	// Repo is the repository's short name within the org's installation, with no
	// owner prefix (the owner is server-derived from the grant). A trailing ".git"
	// is stripped.
	Repo string `json:"repo"`
}

// name is the repo name normalized exactly as the raw Pages handlers normalized it.
func (r githubRepoRef) name() string { return strings.TrimSuffix(strings.TrimSpace(r.Repo), ".git") }
