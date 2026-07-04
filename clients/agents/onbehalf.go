package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/principal"
)

// RunOnBehalf runs agent `ref` for `org` ON BEHALF OF `userSub`, IN-PROCESS —
// no gateway hop, no Cloudflare/IPv6 exposure. It is the clean in-process twin of
// the HTTP s.run handler: the CALLER (e.g. the Slack integrations bridge) has
// ALREADY authenticated org+userSub server-side, so this entry takes them
// DIRECTLY and never reads an HTTP principal / JWT / zip.Ctx. It resolves the
// agent org-scoped, runs it through the SAME runAgent → executeRun → meter path
// as s.run (one run path: one balance gate, one debit, one recorded run, one live
// session), and bills billingActor(org, userSub) against ORG's ledger.
//
// ISOLATION: org is the ONLY tenant key. Store.Resolve is org-scoped, so a caller
// for org A can never resolve, run, or bill against org B's agent — exactly the
// property the HTTP handler relies on the gateway-minted X-Org-Id for.
//
// A non-nil error means NO run happened: not mounted, invalid org, oversized
// input, inference not configured, agent-not-found (errNotFound), or a
// balance-gate denial (out-of-funds / commerce-unknown). A run that executed but
// whose model failed returns a recorded error-status Run and a nil error.
func RunOnBehalf(ctx context.Context, org, userSub, ref, input string) (Run, error) {
	if mounted == nil {
		return Run{}, fmt.Errorf("agents: not mounted")
	}
	return mounted.runOnBehalf(ctx, org, userSub, ref, input)
}

func (s *svc) runOnBehalf(ctx context.Context, org, userSub, ref, input string) (Run, error) {
	org = strings.TrimSpace(org)
	if org == "" || len(org) > principal.MaxOrgLen {
		return Run{}, fmt.Errorf("agents: invalid org")
	}
	if len(input) > maxInput {
		return Run{}, fmt.Errorf("agents: input too large")
	}
	if s.ai == nil {
		return Run{}, fmt.Errorf("agents: inference is not configured on this deployment")
	}
	a, err := s.store.Resolve(ctx, org, strings.TrimSpace(ref))
	if err != nil {
		return Run{}, err // errNotFound or a real DB error — caller replies generically
	}
	// The actor attributes the spend to the acting principal (org/userSub) for the
	// audit trail; the BALANCE gated + debited is always a.Org (== org), never the
	// caller. Synthetic request id: in-process, there is no HTTP X-Request-Id; the
	// client IP is empty (no socket).
	actor := billingActor(org, userSub)
	reqID, _ := genID("obh")
	return s.runAgent(ctx, a, input, actor, reqID, "")
}
