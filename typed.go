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
//   - the VALIDATED org. It lives in a header the typed handler cannot see, and
//     it must NEVER become an In field: an In field is caller-supplied, so a
//     tenant key read from one is a cross-tenant read the caller asserted for
//     itself.
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
// with no request at all, so both Request and principal.OrgFrom read nothing
// there and an org-scoped op refuses — the handler's own 403 gate, with no
// second gate to keep in sync. An MCP tools/call at POST /mcp is an ordinary
// HTTP request and does carry both, so it is gated exactly like the REST route
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
// the org it parks is the validated one.
func Bridge() zip.Handler {
	return func(c *zip.Ctx) error {
		b := &bound{req: c}
		c.SetContext(context.WithValue(principal.WithOrg(c.Context(), c), boundKey{}, b))
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
// Together they are the whole vocabulary a typed op is missing — 200 is the
// default, 204 is a nil Out, and every other code is a returned zip error — so
// they are two named facts rather than a general status setter that could put a
// 4xx here and diverge from the error path. Both are a no-op off the HTTP path,
// where there is no status to set.
func Created(ctx context.Context)  { setStatus(ctx, http.StatusCreated) }
func Accepted(ctx context.Context) { setStatus(ctx, http.StatusAccepted) }

func setStatus(ctx context.Context, code int) {
	if b, ok := ctx.Value(boundKey{}).(*bound); ok {
		b.status = code
	}
}
