package cloud

// typed.go is the seam between a REQUEST and a TYPED op. ZipApp (scope.go) gets a
// subsystem to the registry; this gets each request across it.
//
// zip.Get[In,Out] is the ONE registration every projection reads — REST,
// OpenAPI, MCP and the CLI all derive from that single registry entry, which is
// exactly why a route that is NOT a typed op is invisible to all four. But a
// typed handler receives only a context.Context and its decoded In, so three
// facts a cloud handler needs are not in its hands:
//
//   - the VALIDATED org, and beside it the bare fact that the caller was
//     VALIDATED AT ALL — the two facts a gate turns on, since a plane with no
//     org-scoped rows (engine's shared runtime) authenticates without a tenant.
//     Both live in headers the typed handler cannot see, and neither may EVER
//     become an In field: an In field is caller-supplied, so a tenant key read
//     from one is a cross-tenant read the caller asserted for itself.
//   - the REQUEST itself, for a subsystem that FORWARDS the caller's identity
//     instead of only reading it. A tenant-scoped proxy (clients/visor) passes
//     the caller's own identity headers — and, where no service credential is
//     configured, their bearer — to the upstream that owns the resource, so an
//     op that cannot reach the request drops the caller's identity on the far
//     side of the hop. Reaching it is not an escape from "typed": the request is
//     the same value the raw handler beside it holds, and an op that wants only
//     its tenant asks principal.OrgFrom and never sees a header.
//   - the response status. zip writes 200 (204 for a nil Out) and has no
//     vocabulary for 201 Created or 202 Accepted, so a route converted without
//     one silently downgrades its status — a wire break for every client that
//     checks it.
//
// All three are the same shape of problem — a per-request VALUE the typed
// signature drops — so they share ONE middleware, installed where it precedes
// the typed routes it serves: fiber runs middleware in registration order, so
// one installed after its leaves never runs. Serve installs it once for the
// whole binary, right after the identity boundary that makes the org
// trustworthy; a subsystem whose routes all sit under a prefix it owns may
// install it on that group instead. Nesting is harmless — the inner one is the
// one the handler sees, and the outer finds nothing to apply.
//
// FAIL CLOSED OFF THE HTTP PATH. The CLI projection's LocalInvoke runs an op
// with no request at all, so Request, principal.OrgFrom and principal.ValidatedFrom
// all read nothing there and a gated op refuses — the handler's own 403 gate, with
// no second gate to keep in sync. An MCP tools/call at POST /mcp is an ordinary
// HTTP request and does carry them, so it is gated exactly like the REST route
// it mirrors: a validated principal is served, an anonymous one is refused.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// bound is everything Bridge carries for one request: the request itself, and
// the slot an op writes a non-default status into. ONE value under one key, so
// the middleware costs one allocation however many facts have to cross.
type bound struct {
	req    *zip.Ctx
	status int
}

// boundKey names the request-scoped slot. Unexported zero-size type:
// unforgeable from another package.
type boundKey struct{}

// Bridge carries into a typed op the request facts its signature drops, and
// carries back out the one fact it cannot state. Install it BEFORE the typed
// routes it serves — fiber runs middleware in registration order, so one
// installed after its leaves never runs — and after the identity boundary, so
// the identity it parks is the validated one.
//
// It parks the facts a gate turns on, in ONE expression, so they are always set
// together and can never disagree: the validated ORG (principal.WithOrg, for a
// plane with rows to scope), VALIDATED-NESS itself (principal.WithValidated, for
// a plane whose reads are deployment-global and whose gate is therefore
// authentication), the vouching BRAND (principal.WithBrand, for a plane that
// compares it with the deployment's), and the PROJECT (principal.WithProject, the
// sub-scope that narrows within the org). Every one is read back through
// principal, so a gate that needs any of them does not reach for the request.
//
// They are all SERVER-MINTED identity, which is what makes them belong here and
// is the line: a fact a caller supplies is an In field, and a fact that needs the
// request itself — admin-ness, the payer, a proxied identity — is cloud.Request,
// pinned by typed_request_gate_test.go.
func Bridge() zip.Handler {
	return func(c *zip.Ctx) error {
		b := &bound{req: c}
		ctx := principal.WithOrgs(principal.WithProject(principal.WithBrand(principal.WithValidated(principal.WithOrg(c.Context(), c), c), c), c), c)
		c.SetContext(context.WithValue(ctx, boundKey{}, b))
		err := c.Continue()
		// Success only: an error already carries its own status. fasthttp writes
		// the response after the whole chain returns, so setting it here still
		// lands on the wire.
		if err == nil && b.status != 0 {
			c.Status(b.status)
		}
		return err
	}
}

// Request returns the request a typed op is serving, for the ops that must
// FORWARD the caller's identity rather than merely read it (see the package
// note). Absent off the HTTP path, where the honest answer is that there is no
// request — a caller that needs one refuses rather than inventing an identity.
func Request(ctx context.Context) (*zip.Ctx, bool) {
	b, ok := ctx.Value(boundKey{}).(*bound)
	if !ok || b.req == nil {
		return nil, false
	}
	return b.req, true
}

// Created marks the response 201 Created and Accepted marks it 202 Accepted.
//
// Deprecated: declare the status on the op instead — `zip.WithStatus(201)` —
// which is the same fact in the one place every projection reads.
//
// These exist because zip once had no vocabulary for a success status other than
// 200 and 204, so the only way to answer 201 was to set it per request from
// inside the handler. That works on the wire and nowhere else: the status is a
// CONTRACT detail, and setting it here writes it into a side channel no
// projection can read. The document keeps saying 200, so does every SDK
// generated from it, and the route has always sent 201. It is the same failure
// as a query parameter's required-ness being invisible — a contract detail that
// exists only at run time is not a contract.
//
// zip v1.18.2 closed the gap: `zip.Post(app, path, fn, zip.WithStatus(201))`
// keys the document's response object on 201, so a generated client expects what
// the service sends. New ops declare it; these two stay so the call sites that
// predate it keep working while they are converted, and they still set the
// status they always did.
//
// Both are a no-op off the HTTP path, where there is no status to set.
func Created(ctx context.Context)  { setStatus(ctx, http.StatusCreated) }
func Accepted(ctx context.Context) { setStatus(ctx, http.StatusAccepted) }

func setStatus(ctx context.Context, code int) {
	if b, ok := ctx.Value(boundKey{}).(*bound); ok {
		b.status = code
	}
}
