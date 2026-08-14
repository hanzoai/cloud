package books

// typed.go — the seam between a books ROUTE and a typed op.
//
// A typed op is ONE registry entry with four projections: the REST route, the
// OpenAPI operation (schema AND prose), the MCP tool an agent picks by reading
// that prose, and the generated CLI/SDK method. An untyped handler is a route
// and nothing else — invisible to all four — so every books READ is declared
// here as zip.Get(g, …) rather than as a raw *zip.Ctx handler.
//
// Three facts a books handler needs are NOT in a typed op's hands (it receives a
// context and its decoded In, and nothing else), so each gets exactly one home:
//
//   - The VALIDATED org: principal.OrgFrom(ctx), parked by cloud.Bridge. It is
//     never an In field. An In field is caller-supplied, so a tenant key read
//     from one is a cross-tenant read the caller asserted for itself — and this
//     surface is a LEDGER, where that is another org's money.
//   - The ledger selector (?sandbox=true). That one IS caller-supplied — it
//     picks between two of the CALLER'S OWN books — so it binds from the URL
//     like any other query parameter, through sandboxOf, which stays the one
//     rule the handlers still untyped read it by.
//   - Cache-Control: no-store. A typed op returns its Out and has no response to
//     set a header on, so the header every books answer has always carried moves
//     to the ONE place every books answer passes through: noStore, on the group.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make -C
// apps/books openapi` and by the per-app build chain.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// booksOps binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so
// it arrives as a RECEIVER and every op is a method value (o.listAccounts),
// which is also the only bound form cmd/zipdoc can lift prose from.
type booksOps struct{ s *cloud.Service[*state] }

// tenant is the VALIDATED org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, the exact value principal.Org gives the
// untyped handlers beside these. `action` completes the refusal each route has
// always answered with ("sign in to view books", "sign in to export books"), so
// the 401 is unchanged route by route.
func tenant(ctx context.Context, action string) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", zip.ErrUnauthorized("sign in to " + action)
	}
	return org, nil
}

// ledger opens the caller's OWN book store for the selected ledger, refusing
// before it opens anything when there is no validated principal. Every typed
// read starts here, so the tenant gate and the live/sandbox choice are made once
// instead of once per op.
func (o booksOps) ledger(ctx context.Context, sandbox, action string) (*store, error) {
	org, err := tenant(ctx, action)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.storeFor(org, sandboxOf(sandbox))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	return st, nil
}

// sandboxFrom is the ledger selector for an op whose In is its request BODY. It is
// the ONE fact those ops take off the request, so it is the whole of what
// cloud.Request is reached for in this package.
//
// A BODYLESS op (GET) names the selector as an In field and zip documents it as the
// query parameter it is. A body-carrying op cannot: a POST's In IS its request body
// in the document (zip openapi.go declares query parameters only where there is no
// requestBody) and zip's binder fills an In field from the BODY as well as the URL,
// so naming it would publish the choice as a body property AND start accepting it
// there — a wire these routes have never had.
// TestTheLedgerSelectorStaysOnTheURLForBodyWrites (wire_test.go) is that
// measurement, and it goes red the day the selector is named on an In.
//
// So those ops read it where it rides, through the same sandboxQuery the untyped
// handlers beside them use: one rule, one reader, so the two planes can never
// disagree about which books an op lands in.
//
// LIVE off the HTTP path (an in-process CLI invoke has no request and therefore no
// URL) — the answer an HTTP request that omits the parameter gives, so the two
// planes agree rather than one inventing a ledger.
func sandboxFrom(ctx context.Context) bool {
	if c, ok := cloud.Request(ctx); ok {
		return sandboxQuery(c)
	}
	return false
}

// sandboxOf is the ledger selector, and the ONE rule both the typed ops and the
// handlers still untyped read `sandbox` by: the literal "true", case-insensitive,
// selects the org's SANDBOX book, anything else its live one.
//
// It stays a STRING on the wire rather than becoming a bool. Bool binding would
// additionally accept "1" and a bare "?sandbox" — which today read as false —
// so a caller asking for their live books by either spelling would silently be
// handed the sandbox's empty ones. Typing describes the wire; it does not move it.
func sandboxOf(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "true") }

// limitOr is the row cap a books read applies: the caller's positive limit, else
// the route's own default. Non-positive covers every way a limit fails to
// arrive — absent, unparseable, zero, negative — which is exactly the set the
// string parse it replaces fell back on.
func limitOr(n, dflt int) int {
	if n <= 0 {
		return dflt
	}
	return n
}

// noStore carries the one header every books response has always sent: per-org
// money must never be cached. It sits on the GROUP because a typed op returns its
// Out and nothing else, and it sets the header on SUCCESS only — an error answer
// never carried it, and still does not.
func noStore() zip.Handler {
	return func(c *zip.Ctx) error {
		err := c.Continue()
		if err == nil {
			c.SetHeader("Cache-Control", "no-store")
		}
		return err
	}
}
