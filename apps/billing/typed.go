package billing

// typed.go is the ONE place this package's typed ops reach for the request, and
// the only file here that calls cloud.Request. A typed op — func(ctx, *In)
// (*Out, error) — receives its decoded input and a context and nothing else, so
// the facts below are resolved here and nowhere else:
//
//   - the TENANT comes off the context (principal.OrgFrom, parked by
//     cloud.Bridge, which the composer installs — never this package). It is
//     never an In field: an In field is caller-supplied, so a tenant key read
//     from one is a cross-tenant read the caller asserted for itself.
//   - the wallet SUBJECT is the payer half of the money address, resolved by
//     the ONE rule (principal.Subject over the minted X-User-Name + the signed
//     billing_account claim) — headers only the request carries.
//   - the USER for the per-account breakdown is the validated X-User-Id, which
//     the org alone does not carry.
//
// Every resolver fails closed off the HTTP path: no request means no attested
// payer and no wallet to read, so an op refuses rather than inventing one.

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op into zipdoc_gen.go — the only
// way that prose reaches the published document, the MCP tool list and the CLI.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the mounted service into the typed handlers, exactly as the raw
// handlers reach it through cloud.Handle.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op whose whole input is its URL and its principal.
type noInput struct{}

// payer resolves the caller of a finance read: the validated org (the ledger)
// and the wallet subject within it. The org comes off the context; the subject
// needs the request, because the payer rides headers (X-User-Name, the signed
// billing_account claim) the org does not carry. The 401 is the same answer
// every finance read has always given an absent identity.
func payer(ctx context.Context) (org, subject string, err error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", "", zip.ErrUnauthorized("sign in to view finance")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		// Off the HTTP path there is no credential to resolve a wallet from.
		return "", "", zip.ErrUnauthorized("sign in to view finance")
	}
	return org, subjectFor(c, org), nil
}

// caller resolves the (org, user) pair the per-account breakdown is scoped to.
// The user is the validated X-User-Id — guaranteed non-empty once the org
// resolved, exactly as the raw handler relied on — and it needs the request
// because the org alone does not carry it.
func caller(ctx context.Context) (org, user string, err error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", "", zip.ErrUnauthorized("sign in to view billing")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", zip.ErrUnauthorized("sign in to view billing")
	}
	return org, c.User(), nil
}

