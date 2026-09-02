package affiliate

// typed.go is the ONE place this package's typed ops reach for the request, and
// the only file here that calls cloud.Request. A typed op receives a context and
// its decoded input and nothing else, so the per-request facts the handlers turn
// on are resolved here and nowhere else:
//
//   - the TENANT comes off the context (principal.OrgFrom, parked by
//     cloud.Bridge, which the composer installs — never this package). It is
//     never an In field: an In field is caller-supplied, so a tenant key read
//     from one is a cross-tenant read the caller asserted for itself.
//   - PLATFORM SUDO (X-User-IsAdmin) gates every /v1/admin route here, and that
//     claim rides a header the org does not carry.
//   - the ACTOR (X-User-Id) is what an application row and the user-level
//     referral mirror record — an attribution, never an authority.
//   - requireBody replays the c.Bind refusal the raw write handlers answered on
//     a bodyless request: zip's typed decode is tolerant and would otherwise
//     turn that 400 into a write of zero values.
//
// Every resolver fails closed off the HTTP path: no request means no attested
// admin, no actor, and nothing to require a body of.

import (
	"context"

	"github.com/hanzoai/cloud"
)

// zipdoc lifts the doc comment off each typed op into zipdoc_gen.go — the only
// way that prose reaches the published document, the MCP tool list and the CLI.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the mounted service into the typed handlers, exactly as the raw
// handlers reached it through cloud.Handle.
type ops struct{ s *cloud.Service[state] }

// actor is the validated user id (X-User-Id) an application or a user-level
// referral edge is attributed to. Empty off the HTTP path, where the write
// records no actor rather than inventing one — exactly what the raw handlers
// did with an absent header.
func actor(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return c.User()
	}
	return ""
}

// requireBody replays, at the point in the sequence the raw handler reached it,
// the c.Bind refusal a bodyless (or unparseable-content-type) request has always
// answered. zip's typed decode is tolerant by construction — it skips an empty
// body and leaves the In at its zero value — so without this a bodyless apply
// would enroll, a bodyless handle post would opt the caller out, and a bodyless
// rate post would set a rate of zero. It calls the SAME c.Bind over an empty
// target, so it is one decision rather than a second implementation free to
// drift; a no-op off the HTTP path, where there is no body to require.
func requireBody(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil
	}
	return c.Bind(&struct{}{})
}
