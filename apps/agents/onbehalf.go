package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
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
	return runOnBehalfModel(s, ctx, org, userSub, ref, input, "", nil, meter.PayerFor(org, ""))
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
// because only the caller can know them: a conversation is a fact about the channel
// the bridge is sitting in. Empty is a first message, which is a real answer and
// not a missing one.
//
// payer is whose balance is gated and debited. A bridge names the org that
// connected it (principal.PayerFor(org, "")); the app's conversation names the
// person asking (talk.go), as runAgent's rule says: the caller's own account for
// a run asked for over HTTP.
func runOnBehalfModel(s *cloud.Service[state], ctx context.Context, org, userSub, ref, input, model string, history []infer.ChatMessage, payer account.Account) (Run, error) {
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
	trimmedRef := strings.TrimSpace(ref)
	if gRef, ok := strings.CutPrefix(trimmedRef, "group:"); ok {
		return runGroupOnBehalfDirect(s, ctx, sto, org, userSub, gRef, input, payer)
	}
	if gRef, ok := strings.CutPrefix(trimmedRef, "group_"); ok {
		if _, gerr := sto.ResolveGroup(ctx, org, gRef); gerr == nil {
			return runGroupOnBehalfDirect(s, ctx, sto, org, userSub, gRef, input, payer)
		}
	}
	a, err := sto.Resolve(ctx, org, trimmedRef)
	if errors.Is(err, errNotFound) {
		if strings.EqualFold(trimmedRef, builtinAgentName) || trimmedRef == "" {
			if prim, perr := sto.Primary(ctx, org); perr == nil {
				a = prim
				err = nil
			}
		}
	}
	if errors.Is(err, errNotFound) {
		// An org that has never opened the agents UI has NO rows, and the chat
		// bridges ask for the conventional ref ("hanzo") — so @hanzo answered
		// "the agent hit an error handling that" in every workspace that connected
		// Slack and did nothing else. Measured: `agents: agent not found`, for the
		// org that had just linked successfully.
		//
		// The conventional ref therefore resolves to a BUILT-IN default rather than
		// requiring an org to create a row before the default assistant works. It is
		// not persisted: writing a row here would fork the definition per org and
		// make a later product change unable to reach the orgs that had already been
		// seeded. A row the org DOES create wins, because Resolve is tried first.
		if def, ok := builtinAgent(org, ref, aimodel.ChatModel); ok {
			a = def
		} else {
			return Run{}, err
		}
	} else if err != nil {
		return Run{}, err // a real DB error — caller replies generically
	}
	// The actor attributes the spend to the acting principal (org/userSub) for the
	// audit trail; the BALANCE gated + debited is payer. Synthetic request id:
	// in-process, there is no HTTP X-Request-Id; the client IP is empty (no socket).
	if m := strings.TrimSpace(model); m != "" && strings.HasPrefix(a.ID, "builtin-") && knownChatModel(m) {
		a.Model = m
	}
	// The app's conversation has no sandbox of its own (talk.go), and the built-in
	// assistant is told so there rather than of tools it would be refused.
	if l := liveFrom(ctx); l != nil && talks(l.x) && strings.HasPrefix(a.ID, "builtin-") {
		a.Instructions = talkInstructions
		if l.ws != nil && l.ws.instructions != "" {
			a.Instructions += "\n\n" + l.ws.instructions
		}
	}
	// When answering on Slack (even for orgs whose primary hanzo row predates
	// newer built-in instructions), ground the turn in the live Slack channel
	// and thread context so the agent never acts tabula rasa or forgets its
	// full Hanzo toolset and progressive capability discovery.
	if s, ok := ctx.Value(replyKey{}).(said); ok && s.Provider == "slack" {
		if a.IsPrimary && strings.EqualFold(a.Name, builtinAgentName) && !strings.Contains(a.Instructions, "hanzoskills.com/llms.txt") {
			a.Instructions = strings.TrimSpace(a.Instructions) + "\n\n" + progressiveCapabilityInstructions
		}
		a.Instructions = strings.TrimSpace(a.Instructions) + "\n\n" + slackContextGuidance(s)
	}
	actor := billingActor(org, userSub)
	reqID := mint.ID("obh")
	return runAgent(s, ctx, a, input, history, payer, actor, reqID, "")
}

func runGroupOnBehalfDirect(s *cloud.Service[state], ctx context.Context, sto *Store, org, userSub, gRef, input string, payer account.Account) (Run, error) {
	grp, err := sto.ResolveGroup(ctx, org, strings.TrimSpace(gRef))
	if errors.Is(err, errGroupNotFound) {
		return Run{}, errNotFound
	}
	if err != nil {
		return Run{}, err
	}
	actor := billingActor(org, userSub)
	out, err := runGroupForOrg(s, ctx, sto, org, grp, input, payer, actor, mint.ID("grpobh"), "")
	if err != nil {
		return Run{}, err
	}
	return Run{
		ID:         out.ID,
		Org:        org,
		AgentName:  "group:" + grp.Name,
		Status:     out.Status,
		Model:      out.Policy,
		Input:      input,
		Output:     out.Output,
		Error:      out.Error,
		DurationMs: out.DurationMs,
		CreatedAt:  time.Now().Unix(),
		Actor:      actor,
		ToolCalls:  len(out.Votes),
	}, nil
}

// builtinAgent is the definition the conventional chat ref resolves to when an
// org has not defined its own.
//
// ONE name, the convention the bridges already default to (bridgeAgentRef →
// "hanzo"). Anything else is a real miss and stays a miss: an unknown ref must
// not silently become the default agent, or a typo in `code: repo` would run the
// chat agent and look like it worked.
//
// The model is aimodel.ChatModel, which is where the tier and the evidence for it
// now live — one constant in the file that owns model policy, instead of a literal
// here behind a BRIDGE_AGENT_MODEL knob no deployment ever set.
//
// It is NOT aimodel.FallbackModel: that is the runner's degraded path, and a Slack
// turn is the interactive chat path.
//
// A person who wants a different tier pins one in the App Home menu, and that pin
// still wins below.
//
// Tools is the surface's whole tool surface (ToolsAll), not empty: the tool-calling
// loop decides what may be OFFERED, but an agent that declares nothing is offered
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
		// The default assistant is offered the surface's whole tool surface. Its
		// instructions tell it the tools exist and how to call them; without this it
		// was handed an empty offer and correctly reported it could not reach the
		// cloud, while the MCP server served 88 tools one socket away.
		Tools:     []string{ToolsAll},
		IsPrimary: true,
		Layer:     0,
		CreatedAt: now, UpdatedAt: now,
	}, true
}

const builtinAgentName = "hanzo"

// builtinAgentInstructions is what the default assistant is TOLD it is. Kept
// short on purpose: a long persona spends context a user's actual question needs,
// and every sentence here is one the model reads on every turn.
const builtinAgentInstructions = instructionsHead + reviewInSandbox + instructionsMid + sandboxHands + instructionsTail

// talkInstructions is what the app's conversation is told (talk.go): the same
// assistant, with no computer of its own — a turn there is offered no sandbox op
// (agents.go), so anything that needs one is a coding run, and it is told so
// rather than told of tools it would be refused.
const talkInstructions = instructionsHead + reviewInRun + instructionsMid + runHands + instructionsTail

const instructionsHead = "" +
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
	"ALWAYS read the provided thread and recent channel history FIRST before replying. " +
	"Never act tabula rasa or ask clarifying questions whose answer is already in the " +
	"conversation history or inspectable with your tools. When someone says 'fix this', " +
	"'look into this', 'why is this broken', 'ship it', or refers to an error, stack trace, " +
	"log, file, PR, or service discussed above, inspect that exact context and act directly.\n\n" +
	"Answer in chat: be sharp, direct, concise, and engineering-grade — no filler, " +
	"no sycophancy, no generic disclaimers. NEVER claim you cannot run code, cannot access " +
	"repositories, logs, clusters, or APIs, or ask the user to paste information you can " +
	"inspect with your tools, sandbox, or coding runs. Inspect first, act directly, and " +
	"say plainly only after trying if a specific tool or permission failed.\n\n" +
	// CHOICES ARE BUTTONS. A numbered list in a reply is a question nothing can
	// answer back into; ask_user puts the options under the person's thumb and
	// hands their choice back to this turn (ask.go).
	"When the person has to choose between options, call `ask_user` with them — they " +
	"see buttons and you get their choice — rather than listing numbered choices in " +
	"your reply.\n\n" +
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
	client.Describe + "` with that op name first, then call it. " +
	// Scoped to the questions it is actually true of. Unscoped — "you are answering
	// about THIS organization's live cloud" — it read as a statement of what the
	// whole conversation is about, which is the other half of why every reply came
	// back framed as cloud support.
	"When a question is about THIS organization's live cloud, look it up with a " +
	"tool instead of answering from memory: your training data does not contain it. " +
	"The same holds for any other system a tool can reach — use it.\n\n" +
	// WHERE THE ISSUES ARE, because the answer is not inferable and the wrong guess
	// is the one a model reaches for first. Asked what issues someone had filed on
	// our repositories, it searched the WEB for a GitHub profile, met the login wall
	// every logged-out scrape meets, and reported the absence as a fact about the
	// person. It holds `task`, whose issues are GitHub's — the App mirrors them in
	// (apps/provider/github_issues.go) — so the tool was there and the fact
	// connecting the question to it was not.
	"Issues, pull requests and epics — INCLUDING the ones on our GitHub repositories, " +
	"which are mirrored in — live in `task`, not on the open web. Use it and never " +
	"a web search for them: `list_task_issues` answers across every project in the " +
	"org at once, and filters by project, assignee and kind (use kind 'pr' to list " +
	"pull requests across all repositories at once). " +
	"When someone asks to review or check pull requests (e.g. 'review my PRs' or 'check this PR'), " +
	"FIRST check the recent messages in the conversation/channel for any PR links, numbers (e.g. #115724), " +
	"commit SHAs, or repository names just shared; THEN call `task` (`list_task_issues` with kind 'pr'), "

// reviewInSandbox ends the pull-request clause where a turn has a sandbox of its own.
const reviewInSandbox = "" +
	"`git` (`list_git_repos`), or `sandbox` (`run_in_sandbox` with curl against https://api.github.com/repos/<owner>/<repo>/pulls/<num> " +
	"or gh pr list / gh pr view) to inspect the diff and review the PR directly. "

// reviewInRun ends it in the app's conversation, whose turns have none: the diff is
// read by a coding run, which has git and gh.
const reviewInRun = "" +
	"or `git` (`list_git_repos`) to find it, then start a coding run (`agent`, op `create_agent_coding`) " +
	"that reads its diff with git or gh and reviews it. "

const instructionsMid = "" +
	"NEVER say you cannot list pull requests across repositories or do not know which repositories exist. " +
	"To create or file an issue on GitHub, call `task` with op `create_task_project_issue` " +
	"(setting key to the repository name or owner/name, e.g. 'hanzoai/commerce', with title and description; " +
	"for Hanzo ID / hanzo.id / IAM / auth / login issues, the repository is 'hanzoai/iam'). " +
	"To read other Slack channels/threads or post updates to Slack channels (e.g. #hanzo-dev), call `provider` " +
	"with op `list_provider_slack_channels`, `list_provider_slack_messages`, or `create_provider_slack_message`.\n\n" +
	// WHAT IT HAS BEEN TOLD, kept. A conversation settles things — who owns what,
	// what was decided — and a turn that only answered left them nowhere, so the
	// next turn asked again. The graph is where a fact outlives the reply; a fact
	// filed here cites the message it was said in, and only a public channel files
	// one, because the whole organization reads the graph (attest.go).
	"You have a memory: the `graph` tool. When a conversation in a public channel " +
	"settles something durable — a decision, an owner, a date, a preference — file it " +
	"with `graphAssert`. What is said in a direct message or a private channel is never " +
	"filed: everyone in the organization reads the graph. " +
	"Before answering about this organization's people, projects or past decisions, " +
	"look with `graphResolve` for one entity or `graphAnswer` for a question across " +
	"everything, and say which facts your answer rests on.\n\n" +
	// CODING IS A RUN, NOT AN ANSWER, and the model has to be told that or it does
	// not ask: an agent offered a tool it is not told about declines the question,
	// and a tool it cannot name it cannot call. The REST operation is
	// post_agent_coding and the MCP server projects it as `create_agent_coding`
	// (client/verbs.go); a model reads its toolset, not the OpenAPI. A chat turn is
	// a bounded budget (tools.go toolRunBudget) and a coding run is minutes, so the
	// turn's job is to START one and say so: the op answers with a session id
	// immediately, leases its own sandbox and files a pull request, and the turn
	// adds the run's outcome to the reply itself (follow.go).
	//
	// THE REPOSITORY IS WHAT THE PERSON SAID. Asked to change "the hero on
	// hanzo.ai", a model that is not told otherwise goes looking for the repository
	// first and spends the turn searching; the run resolves a site or product name
	// against the org's own repositories and answers with the candidates when it
	// cannot, so passing the words through is both quicker and right.
	"Changing code is a CODING RUN and not something you answer from the chat turn: " +
	"call `agent` with op `create_agent_coding` when someone asks you to fix a bug, write code, " +
	"refactor, change text on a site, or open a pull request against one of this " +
	"organization's repositories. It runs the full Hanzo CLI / dev agent in a sandbox with " +
	"git, compilers, and test runners. Pass the repository or site they named as the repo, " +
	"as they said it — the run finds the repository it means, and answers with the " +
	"candidates when it cannot — and the whole request (including relevant thread/channel " +
	"error details) as the prompt. It answers at once with a session id and keeps working " +
	"after your reply; its pull request, or what went wrong, is added to your reply when it " +
	"lands. So start it and say you have, rather than describing the change or refusing for want of time.\n\n"

// sandboxHands is the computer a turn has for its reply (box.go). THE SANDBOX is
// said because each sandbox op's own prose says the opposite: its `id` is "from an
// earlier lease", so a model told nothing else leases first and carries the id. A
// run holds one for it — a sandbox call that names none is the run's, leased on
// first use and given back when the run ends. What is left to say is what it is
// for, and that nothing in it outlives the reply.
const sandboxHands = "" +
	"You have a computer for this reply: `run_in_sandbox`, `read_sandbox_file`, " +
	"`write_sandbox_file` and `computer_in_sandbox` on the `sandbox` tool need no id. " +
	"`computer_in_sandbox` drives a full desktop GUI (screenshot, click, drag, scroll, " +
	"type, key, navigate, autofill, save_credential and shell) and shows you the live screen after every action. " +
	"When a page asks to sign in or enter a key stored in the org's Unified Vault " +
	"(Hanzo KMS, 1Password or LastPass), call `computer_in_sandbox` using the autofill " +
	"action with the credential field set to the site domain or name (and field login_totp when 2FA follows password) — secret values and " +
	"TOTP codes are typed inside the sandbox from KMS and never shown in text. " +
	"When helping a sandbox desktop app save credentials securely, stage a JSON object (name, domain, url, username, password, totp) " +
	"with mode 0600 at /dev/shm/.hanzo-credential.json in RAM tmpfs (never under /workspace) and call `computer_in_sandbox` using the save_credential action to seal it directly into Hanzo KMS. " +
	"Use your computer to run scripts, browse pages, operate apps, or inspect repos; a " +
	"pull-request code change to a repository is still a coding run.\n\n"

// runHands is what the app's conversation has instead: every computer it uses is a
// coding run's, `dev` itself with the CLI's own tools.
const runHands = "" +
	"You have no computer of your own here. Anything that needs one — running a script, " +
	"cloning a repository and reading it, reviewing a pull request's diff, or changing code — " +
	"is a coding run (`agent`, op `create_agent_coding`), which works in a sandbox with the " +
	"`dev` CLI's own tools and keeps it between runs of this conversation.\n\n"

const instructionsTail = "" +
	// THE OPEN WEB, said explicitly. Scoping tools to "THIS organization's live
	// cloud" is true and was read as exhaustive: asked the weather, the model
	// reasoned that none of its tools were for that and declined — while holding
	// `websearch` and `crawl`. A tool that can answer and does not is worse than no
	// tool, so the rule is stated as a rule: search, then answer.
	"Your reach is not limited to this cloud. `websearch` searches the open web and " +
	"`crawl` fetches a page. For anything current or factual you do not know — " +
	"weather, news, prices, documentation, a company, a person — SEARCH FIRST and " +
	"answer from what you find. Never refuse a question as outside your tools, and " +
	"never tell someone to go ask a search engine: you have one. Say you could not " +
	"find it only after looking.\n\n" +
	progressiveCapabilityInstructions + "\n\n" +
	recursiveImprovementInstructions

// slackContextGuidance grounds a Slack-originated turn with its exact channel
// and thread coordinates and reinforces context-first, high-agency execution even
// when an org's persisted primary agent predates newer built-in instructions.
func slackContextGuidance(s said) string {
	where := "Slack channel " + s.Channel
	if strings.TrimSpace(s.Thread) != "" {
		where += " (thread " + s.Thread + ")"
	}
	return "Slack context: You are replying in " + where + ". " +
		"The conversation history above already includes recent messages from this Slack thread/channel " +
		"(with speaker names, alert attachments, blocks, and file metadata). Read it FIRST — never act " +
		"tabula rasa or ask for details already present above. You have full Hanzo MCP tools, sandbox/CLI " +
		"execution (sandbox), autonomous coding runs (agent op create_agent_coding), issue/PR triage " +
		"(task, git), and Slack history/channel reads (provider) if you need older messages or other channels."
}

// knownChatModel accepts only a model this deployment offers for chat.
//
// The enso family is the SKU set the App Home menu is built from. Anything else
// is refused rather than forwarded — an arbitrary string from a client would let
// a caller pick what their org pays for.
func knownChatModel(m string) bool {
	switch m {
	// Zen models (canonical) and enso aliases (legacy)
	case "zen6", "zen6-flash", "zen5-flash", "zen", "zen-coder",
		"enso-auto", "enso", "enso-flash", "enso-pro", "enso-ultra":
		return true
	}
	return false
}
