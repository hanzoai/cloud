// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// org.go — WHOSE BOOKS a money op acts on, resolved once for the whole package.
//
// This lived in payments.go while the typed payment op was the only caller that
// mattered, and the file it lived in named the caller rather than the rule. It is
// the package's tenancy rule and 44 ops read it, so it has a file that says so —
// which is also what stopped it being deleted as collateral when the payment op
// it was filed under was retired.

import (
	"context"
	"net/http"

	"github.com/hanzoai/commerce/models/organization"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

// orgOf resolves the caller's org to the commerce organization the money core
// needs, through commerce's OWN canonical resolver — the same binding the balance
// read and the browser charge use, so a payment can never land in a namespace a
// read would not look in.
//
// It refuses rather than defaulting. An unvalidated caller has no org, and the
// honest answer to "whose card is this and whose balance goes up" is nobody, not
// the platform's.
//
// It names what it RETURNS rather than the role the org is playing. "Paying" is
// true of a charge and false of the reads that also call it, so a name asserting
// it made every non-money caller either lie or write a second resolver — and two
// resolvers is two tenancy rules.
func orgOf(ctx context.Context, op string) (*organization.Organization, error) {
	// callerOrg, and nothing beside it. It is the package's tenancy rule and it
	// already answers both shapes a money op is reached in — a request, where the
	// org is a header and must be vouched for, and a stated caller, where there is
	// no request and no header in play. A pre-step here that consulted only the
	// validated principal was a SECOND copy of the request half, and two copies of
	// one rule is two rules: they disagree about the trusted service, which
	// legitimately carries no session, and only one of them is right. So there is
	// one, and every money op reads it.
	name, err := callerOrg(ctx, op)
	if err != nil {
		return nil, err
	}
	// Commerce must be co-resident for its ledger to be writable in-process. A
	// missing embed is an ERROR, never a silent no-op: money that quietly did not
	// move is worse than money that loudly refused to.
	if e := currentEmbedded(); e == nil || e.App() == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable,
			"%s: commerce is not co-resident in this process", op)
	}
	org, err := commerceorg.Resolve(ctx, name)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%s: resolve org %q: %v", op, name, err)
	}
	return org, nil
}

var _ = cloud.Bridge // money ops read identity through the app-wide bridge
