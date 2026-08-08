package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/types"
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
	return runOnBehalfModel(s, ctx, org, userSub, ref, input, "", nil)
}

// runOnBehalfModel is runOnBehalf with the ASKER's model preference and the
// conversation the ask arrived in.
//
// It overrides the agent's own Model only when the caller named one AND the
// agent is the built-in default — a person's Slack preference must not silently
// re-point an agent their org deliberately configured. An unrecognised value is
// ignored rather than forwarded: the menu came from us, so anything else is a
// stale client or a forged payload, and it would bill this org for a model it
// never offered.
//
// history is the turns BEFORE this one, oldest first, and it rides the call
// because only the caller can know them: a conversation is a fact about the room
// the bridge is sitting in. Empty is a first message, which is a real answer and
// not a missing one.
func runOnBehalfModel(s *cloud.Service[state], ctx context.Context, org, userSub, ref, input, model string, history []types.ChatMessage) (Run, error) {
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
		if def, ok := builtinAgent(org, ref, cloud.ChatModel); ok {
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
	if m := strings.TrimSpace(model); m != "" && strings.HasPrefix(a.ID, "builtin-") && knownChatModel(m) {
		a.Model = m
	}
	actor := billingActor(org, userSub)
	reqID, _ := genID("obh")
	return runAgent(s, ctx, a, input, history, actor, reqID, "")
}

// builtinAgent is the definition the conventional chat ref resolves to when an
// org has not defined its own.
//
// ONE name, the convention the bridges already default to (bridgeAgentRef →
// "hanzo"). Anything else is a real miss and stays a miss: an unknown ref must
// not silently become the default agent, or a typo in `code: repo` would run the
// chat agent and look like it worked.
//
// The model is cloud.ChatModel, which is where the tier and the evidence for it
// now live — one constant in the file that owns model policy, instead of a literal
// here behind a BRIDGE_AGENT_MODEL knob no deployment ever set.
//
// The tier did not change. Its JUSTIFICATION did, because the old one was false:
// this called `enso` "the auto-routing SKU that selects per query in the gateway's
// own catalog", and enso does no such thing — one fixed route entry
// (deepseek-v4-pro, reasoning: medium), no ladder, no escalation. Measurement says
// it is nonetheless the right tier for a tool-driving turn, and cloud.ChatModel
// carries those numbers.
//
// It is also NOT cloud.FallbackModel ("best"): that constant's own doc says it
// "keeps a bot's reply landing when the flash tier is saturated; the interactive
// chat path never uses it" — it is the degraded path, and a Slack turn IS the
// interactive chat path.
//
// A person who wants a different tier pins one in the App Home menu, and that pin
// still wins below.
//
// Tools is the fleet's whole door (ToolsAll), not empty: the tool-calling loop
// decides what may be OFFERED, but an agent that declares nothing is offered
// nothing, which is how the default assistant came to report it could not reach a
// cloud that was one socket away.
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
		// The default assistant is offered the fleet's whole door. Its instructions
		// tell it the tools exist and how to call them; without this it was handed
		// an empty offer and correctly reported it could not reach the cloud, while
		// the door served 88 tools one socket away.
		Tools:     []string{ToolsAll},
		CreatedAt: now, UpdatedAt: now,
	}, true
}

const builtinAgentName = "hanzo"

// builtinAgentInstructions is what the default assistant is TOLD it is. Kept
// short on purpose: a long persona spends context a user's actual question needs,
// and every sentence here is one the model reads on every turn.
const builtinAgentInstructions = "" +
	// THE NAME, stated as a correction and not merely as a fact, because the model
	// contradicts it out of its own training: enso is the SKU this runs on, the
	// weights know that word as their name, and every reply opened "I'm Enso by
	// Hanzo AI". A prompt that only says "you are Hanzo" leaves two names true at
	// once and the trained one wins. Naming the wrong answer is what displaces it.
	"You are Hanzo, an AI assistant made by Hanzo AI. Your name is Hanzo. " +
	"Enso is the name of the model you run on, not your name — never introduce " +
	"yourself as Enso.\n\n" +
	// WHAT IT IS FOR. The previous line — "the assistant for the Hanzo cloud" — was
	// read as a SUBJECT rather than as an ability: asked anything at all, the reply
	// came back framed as cloud support and closed with "is there something I can
	// help you with in the Hanzo cloud?". Being able to run this org's cloud is one
	// of its hands, not the topic of conversation, and the two have to be said
	// separately or the narrow one swallows the broad one.
	"You are a general assistant. Coding, research, writing, analysis, marketing, " +
	"maths, planning and plain questions are all yours to answer, and you do the " +
	"work when asked rather than describing how it would be done. Answer what was " +
	"actually asked, on its own terms. Do not steer a conversation back to the " +
	"Hanzo cloud and do not close a reply by offering cloud help — running this " +
	"organization's cloud is something you are very good at, not what you are for.\n\n" +
	// THE GREETING, said once. It introduced itself on every single message, because
	// every message WAS its first: nothing carried the conversation, so a third turn
	// looked exactly like a first one. History fixes the cause; this fixes what the
	// model does with it, because a friendly model shown a transcript will still say
	// hello again unless told not to.
	"Introduce yourself at most once, and only when nothing has been said before. " +
	"When there are earlier turns in this conversation, just answer — no greeting, " +
	"no restating who you are, no offer of further help at the end.\n\n" +
	"Answer in chat: be brief, concrete, and say plainly when you do not know or " +
	"cannot reach something rather than guessing.\n\n" +
	// THE TOOL PROTOCOL. Without this the tools are unusable, and the failure is
	// silent: the model sees 88 tools whose only argument is an `op` enum of bare
	// names with no schemas, cannot tell what any of them take, and answers from
	// memory instead — which reads as "the assistant is stupid" rather than as a
	// missing sentence. The surface was collapsed from 1,189 flat tools (977 KB,
	// ~244k tokens just to list) to 88 grouped ones precisely so the schemas could
	// be fetched on demand; the fetch has to be described or the trade is a loss.
	"Your tools are grouped one per subsystem, and a tool IS its subsystem's name. " +
	"Each takes an `op` (choose from its enum) and an `input` object. The enum lists " +
	"operation names only — to see what an operation accepts or returns, call `" +
	fleet.Describe + "` with that op name first, then call it. " +
	// Scoped to the questions it is actually true of. Unscoped — "you are answering
	// about THIS organization's live cloud" — it read as a statement of what the
	// whole conversation is about, which is the other half of why every reply came
	// back framed as cloud support.
	"When a question is about THIS organization's live cloud, look it up with a " +
	"tool instead of answering from memory: your training data does not contain it. " +
	"The same holds for any other system a tool can reach — use it.\n\n" +
	// THE OPEN WEB, said explicitly, because the sentence above is not enough on
	// its own. Scoping tools to "THIS organization's live cloud" is true and was
	// read as exhaustive: asked the weather, the model reasoned that none of its
	// tools were for that and declined — while holding `websearch` and `crawl`.
	// A door that can answer and does not is worse than no door, so the rule is
	// stated as a rule: search, then answer.
	"Your reach is not limited to this cloud. `websearch` searches the open web and " +
	"`crawl` fetches a page. For anything current or factual you do not know — " +
	"weather, news, prices, documentation, a company, a person — SEARCH FIRST and " +
	"answer from what you find. Never refuse a question as outside your tools, and " +
	"never tell someone to go ask a search engine: you have one. Say you could not " +
	"find it only after looking."

// knownChatModel accepts only a model this deployment offers for chat.
//
// The enso family is the SKU set the App Home menu is built from. Anything else
// is refused rather than forwarded — an arbitrary string from a client would let
// a caller pick what their org pays for.
func knownChatModel(m string) bool {
	switch m {
	case "enso", "enso-flash", "enso-ultra":
		return true
	}
	return false
}
