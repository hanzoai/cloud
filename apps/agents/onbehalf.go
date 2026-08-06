package agents

import (
	"context"
	"errors"
	"time"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
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
		return Run{}, fmt.Errorf("%w: agents", cloud.ErrNoPeer)
	}
	return runOnBehalf(mounted, ctx, org, userSub, ref, input)
}

func runOnBehalf(s *cloud.Service[state], ctx context.Context, org, userSub, ref, input string) (Run, error) {
	org = strings.TrimSpace(org)
	if org == "" || len(org) > principal.MaxOrgLen {
		return Run{}, fmt.Errorf("agents: invalid org")
	}
	if len(input) > maxInput {
		return Run{}, fmt.Errorf("agents: input too large")
	}
	if s.State.ai == nil {
		return Run{}, fmt.Errorf("agents: inference is not configured on this deployment")
	}
	sto, err := s.State.storeFor(org)
	if err != nil {
		return Run{}, err
	}
	a, err := sto.Resolve(ctx, org, strings.TrimSpace(ref))
	if errors.Is(err, errNotFound) {
		// An org that has never opened the agents UI has NO rows, and the chat
		// bridges ask for the conventional ref ("hanzo") — so @hanzo answered
		// "the agent hit an error handling that" in every workspace that connected
		// Slack and did nothing else. Measured: `agents: agent not found`, for the
		// org that had just linked successfully.
		//
		// The conventional ref therefore resolves to a BUILT-IN default rather than
		// requiring an org to create a row before the front door works. It is not
		// persisted: writing a row here would fork the definition per org and make
		// a later product change unable to reach the orgs that had already been
		// seeded. A row the org DOES create wins, because Resolve is tried first.
		if def, ok := builtinAgent(org, ref, s.State.failoverModel); ok {
			a = def
		} else {
			return Run{}, err
		}
	} else if err != nil {
		return Run{}, err // a real DB error — caller replies generically
	}
	// The actor attributes the spend to the acting principal (org/userSub) for the
	// audit trail; the BALANCE gated + debited is always a.Org (== org), never the
	// caller. Synthetic request id: in-process, there is no HTTP X-Request-Id; the
	// client IP is empty (no socket).
	actor := billingActor(org, userSub)
	reqID, _ := genID("obh")
	return runAgent(s, ctx, a, input, actor, reqID, "")
}

// builtinAgent is the definition the conventional chat ref resolves to when an
// org has not defined its own.
//
// ONE name, the convention the bridges already default to (bridgeAgentRef →
// "hanzo"). Anything else is a real miss and stays a miss: an unknown ref must
// not silently become the default agent, or a typo in `code: repo` would run the
// chat agent and look like it worked.
//
// The model is the deployment's own failover model — the same one every other
// run falls back to — so a deployment configures its chat brain exactly where it
// configures inference, and this carries no second source of truth.
//
// Tools is deliberately EMPTY. The tool-calling loop is what decides what an
// agent may reach, and handing the default agent a tool set here would be
// deciding that in the wrong place.
func builtinAgent(org, ref, model string) (Agent, bool) {
	if !strings.EqualFold(strings.TrimSpace(ref), builtinAgentName) {
		return Agent{}, false
	}
	if strings.TrimSpace(model) == "" {
		return Agent{}, false // no model configured: an honest miss, not a broken run
	}
	now := time.Now().Unix()
	return Agent{
		ID: "builtin-" + builtinAgentName, Org: org, Name: builtinAgentName, Model: model,
		Instructions: builtinAgentInstructions,
		Description:  "The default Hanzo assistant that answers in chat.",
		Status:       "ready", ExecutionMode: ModeOneShot,
		CreatedAt:    now, UpdatedAt: now,
	}, true
}

const builtinAgentName = "hanzo"

// builtinAgentInstructions is what the default assistant is TOLD it is. Kept
// short on purpose: a long persona spends context a user's actual question needs,
// and every sentence here is one the model reads on every turn.
const builtinAgentInstructions = "You are Hanzo, the assistant for the Hanzo cloud. " +
	"Answer in Slack: be brief, concrete, and say plainly when you do not know or " +
	"cannot reach something rather than guessing."

