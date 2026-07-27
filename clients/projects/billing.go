package projects

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Per-deploy metering — product:hosting. Every go-live of a site (a tar-artifact
// deploy, a /v1/sites brief build, a /v1/sites/deploy file manifest, or a git/CI
// completion) is one billable hosting event, gated and metered through the ONE
// shared cloud.ResourceMeter (s.State.bill) exactly like the functions invoke path
// (clients/functions) and the agent run path (clients/agents): gate → work →
// meter-once-on-success. A failed deploy is never billed; a redeploy to the same
// slug is a distinct billable event that returns the SAME URL (idempotent host
// binding guarantees it).
const (
	// deployFeeEnvPrefix is the operator knob for the flat per-deploy fee. The
	// effective fee is cloud.ResourceFeeCents(deployFeeEnvPrefix, deployKind):
	// CLOUD_HOSTING_FEE_CENTS_DEPLOY overrides CLOUD_HOSTING_FEE_CENTS overrides the
	// $1.00 default; 0 makes deploys free (and therefore un-gated).
	deployFeeEnvPrefix = "CLOUD_HOSTING_FEE_CENTS"
	// hostingProvider is the commerce "provider"/attribution label for site-hosting
	// spend, so a deploy debit is distinguishable from agent/functions/s3 spend.
	hostingProvider = "hosting"
	// deployKind is the metering "kind"/unit for one deploy event.
	deployKind = "deploy"
)

// gateHosting fail-closes a deploy for an unfunded org (renders 402) or an
// unreachable commerce (renders 503) BEFORE any work runs. It returns the
// resolved per-deploy fee so the caller can debit exactly that amount on success.
// A fee of 0 (operator-configured free tier) is un-gated: Gate returns nil. The
// caller renders a non-nil error via cloud.DenyResource.
//
// The gate keys on principal.Ledger(c) (the resolved CALLER org, hardened against a
// masquerading admin) and threads the caller's validated project sub-scope
// (principal.ValidatedProject) so a forged X-Project-Id can neither hard-stop nor
// evade a project-scoped hosting cap — the SAME anti-spoof gate the functions/s3
// data planes use.
func gateHosting(s *cloud.Service[state], c *zip.Ctx) (fee int64, err error) {
	fee = cloud.ResourceFeeCents(deployFeeEnvPrefix, deployKind)
	project, projectValidated := principal.ValidatedProject(c)
	return fee, s.State.bill.Gate(c.Context(), principal.Ledger(c), project, projectValidated, deployKind, fee)
}

// meterDeploy debits the caller's org ledger ONCE for a successful deploy. It is
// fire-and-forget (never blocks the response) and a no-op when billing is
// unconfigured or fee<=0. Call it ONLY after publishSite (or a git completion) has
// flipped the site live — never on a failed deploy. It attributes spend to the
// caller's validated project sub-scope so a per-project cap sums correctly.
func meterDeploy(s *cloud.Service[state], c *zip.Ctx, fee int64) {
	s.State.bill.Meter(principal.Ledger(c), principal.Project(c), deployKind, fee, c.RequestID(), cloud.ClientIP(c))
}
