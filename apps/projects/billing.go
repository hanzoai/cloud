package projects

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// Site hosting — product:hosting — is billed for what a site holds, never for
// going live.
//
// Every way a static site reaches its prefix is free: an archive upload, a file
// manifest, a generated site, a release, a CI completion, a sandbox build. An edit
// is not a billable event, so a customer who asks for ten changes is not charged
// ten times. What is billed is the storage the org's sites take past the free tier,
// metered hourly on the same cloud.Meter (storage.go).
//
// The one refusal a publish can meet here is the storage gate: an org holding more
// than the free tier must be able to pay for what it holds. Inside the tier a
// publish is admitted whatever the balance, which is what lets an org with nothing
// in its wallet publish and republish a preview.
const (
	// hostingFeeEnvPrefix is the operator knob for hosting's flat fees, resolved by
	// cloud.ResourceFeeCents. Publishing charges none; the one fee left is keeping a
	// project private (visibility.go): CLOUD_HOSTING_FEE_CENTS_PRIVATE, else
	// CLOUD_HOSTING_FEE_CENTS, else the $1.00 default.
	hostingFeeEnvPrefix = "CLOUD_HOSTING_FEE_CENTS"
	// hostingProvider is the commerce "provider"/attribution label for site
	// hosting, so a storage debit is distinguishable from agent, functions and s3
	// spend, and the service a hosting spend cap sums.
	hostingProvider = "hosting"
)

// gateStorage is the one question a publish asks of the wallet: may org hold
// more? Within the free tier the answer is yes without asking, so a publish there
// never reaches the ledger. Past it, the org's own account must cover one month of
// the excess — the storage it is being billed for — or the publish is refused: 402
// when the account cannot, 503 when commerce cannot be asked or the size cannot be
// read.
//
// THE ORG'S ACCOUNT, WHOEVER PUBLISHES. The sweep debits an org's storage to
// account.Org(org) (meterStorage), so the gate weighs that same account: one
// address for the gate and the debit. The publisher's own wallet (principal.Payer)
// is a different address in two cases, and both would split them — a member of the
// shared signup org, whose empty personal wallet would be asked for the whole
// org's holding, and a SuperAdmin acting in a customer org, whose books would be
// asked for storage the customer is billed.
//
// It reads the size the org last measured at (storage.go). Every publish re-lists
// its org when it lands and the sweep re-lists every org hourly, so the size is
// current to the last publish; one publish can carry an org past the tier, and the
// next one is gated.
//
// The caller renders a non-nil error: cloud.DenyResource on an untyped handler,
// cloud.Denied on a typed op or a plane op.
func gateStorage(s *cloud.Service[state], ctx context.Context, org, project string, projectValidated bool) error {
	h, err := s.State.store.Holding(ctx, org)
	if err != nil {
		return fmt.Errorf("read storage: %w", err)
	}
	excess := billable(h.Bytes)
	if excess == 0 {
		return nil
	}
	return s.State.bill.Authorize(ctx, account.Org(org), project, projectValidated, storageKind, monthCents(excess, storageRate(ctx)))
}

// gateHosting is gateStorage for a request, carrying the caller's validated
// project sub-scope, so a forged X-Project-Id can neither hard-stop nor evade a
// project-scoped hosting cap.
func gateHosting(s *cloud.Service[state], c *zip.Ctx, org string) error {
	project, validated := principal.ValidatedProject(c)
	return gateStorage(s, c.Context(), org, project, validated)
}
