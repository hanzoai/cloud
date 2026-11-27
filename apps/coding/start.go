package coding

// start.go is the ONE way a coding run begins.
//
// `POST /v1/agent/coding` is the endpoint, and anything added later arrives
// here too. That is not tidiness: an endpoint that assembled its own Dispatcher
// would be a second ENGINE with its own pool and its own in-flight set, and a
// run started from chat would be invisible to the app that shares its name. One
// Start, one pool, one process.
//
// # The tenant, and the bug that made every run fail
//
// A run is a long chain of cross-process calls: open the session (agents), read
// the clone URL (git), dispatch the sandbox (bot), verify the pushed ref (git),
// file the PR (todo). Every one of those authorizes on the CALLER's org,
// never on an argument, because a caller able to name the org could name
// somebody else's.
//
// So the org has to ride the caller. `cloud.For` states it — but zip reads a
// stated caller only where there is NO REQUEST behind the context
// (caller.go:352-356), deliberately, so that stating an identity can never
// override an authenticated one. Applied to a context that has a request, it is
// a SILENT NO-OP.
//
// The coding path did exactly that. The run was spawned on a bare
// context.Background() with no tenant stated at all, and the routed path's
// target lookup ran on the inbound webhook's request context, where a statement
// would have been discarded anyway. Every client call in every coding run
// therefore answered `authorize: no org on the call`: no session, an empty clone
// URL the dispatcher reads as "git is not available", and a run dead before a
// model was ever asked anything. The chat turn died of this shape one file over
// and was fixed there (bridgeRunContext); this is the same fix for the other
// half, and runContext below is the only place a coding run's context is made.
//
// It is not a laundering hole. For SUPPLIES a tenant where there is none and
// cannot override one, and the org it supplies was resolved server-side — from a
// Slack-signature-verified team_id, or from the authenticated caller of
// /v1/agent/coding — never from a field a client can set.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/readiness"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Accepted is what an endpoint returns the instant a run is admitted. A run
// takes minutes; the endpoint answers in milliseconds and hands back the session
// that is the run's record, its live stream, and the handle for every later
// question.
type Accepted struct {
	SessionID string
	Branch    string
	Repo      string
	Routed    bool
	TargetID  string
	// Carried, Left and Dropped say what became of the instructions queued on the run a
	// start follows: in this run's task, still queued there, and set aside as fitting no
	// task. Note says it in words.
	Carried, Left, Dropped int
	Note                   string
}

// ErrWorkspace is an org's code workspace that could not be read or made right
// now (workspace.go). It is the platform's failure and never the caller's, so it
// is told apart from a malformed request: the endpoint answers it as
// unavailable, and trying again is the remedy.
var ErrWorkspace = errors.New("coding: this organization's code workspace is unavailable right now")

// ErrDraining is a start refused because this process is being replaced: nothing asked for
// was wrong, and trying again reaches the next pod.
var ErrDraining = errors.New("coding: the platform is restarting; try again")

var (
	engineOnce sync.Once
	engine     Dispatcher
)

// Engine returns the process's ONE Dispatcher, assembled on first use. log
// carries the run's best-effort failures (a dropped session mirror, a PR that
// would not file) into the host's log rather than dropping them.
func Engine(log func(msg string, kv ...any)) Dispatcher {
	engineOnce.Do(func() {
		engine = NewDispatcher(log)
	})
	return engine
}

// Start admits one coding run and returns its handle.
//
// org and subject are the CALLER's, read off the caller by the endpoint and
// never taken from the request body — the two are parameters for exactly that
// reason, and the symmetry is the contract: an endpoint that can name one can
// name the other, and neither is nameable. Everything the run then does happens
// in that org and nowhere else: its session, its repo, its credential, its PR;
// and it is attributed to that person.
//
// It is synchronous up to the point the run is admitted — validate, resolve the
// credential, open the session — and detached after it. That split is what lets
// an endpoint answer immediately with a real handle instead of an empty promise,
// and it is why the session is opened HERE rather than inside Run.
// codingTracer is this app's tracer, resolved lazily so a process that never
// starts a run never touches the provider.
func codingTracer() trace.Tracer { return otel.Tracer("hanzo.ai/cloud/coding") }

// Start admits a run, and with a request id admits it once: however often and wherever
// the request is made, and across any restart between, a retry answers the first's
// Accepted (ledger.go). The request is committed to the ledger just before the run is
// spawned, so a retry of an attempt that died earlier starts the run, and of one that
// died later answers it.
func Start(ctx context.Context, org, subject string, in client.CodingStartIn, log func(msg string, kv ...any)) (Accepted, error) {
	return requested(ctx, flights.ledger(), org, subject, "start", in.RequestID, printOf(normalized(in)), func(ctx context.Context) (Accepted, error) {
		return start(ctx, org, subject, in, log, "")
	})
}

func start(ctx context.Context, org, subject string, in client.CodingStartIn, log func(msg string, kv ...any), ts string) (Accepted, error) {
	return startSession(ctx, org, subject, in, log, ts, nil, nil)
}

// startSession is the one start. existing is a session a run goes on in rather than one
// it opens: an orphaned run resumed in its sandbox (Resume), or, with parked, a run that
// waited on a limit and goes on in a sandbox of its own from the branch it kept (park.go).
func startSession(ctx context.Context, org, subject string, in client.CodingStartIn, log func(msg string, kv ...any), ts string, existing *agents.Session, parked *park) (Accepted, error) {
	if readiness.Draining() {
		return Accepted{}, ErrDraining
	}
	// ONE SPAN OVER THE PART THAT CAN REFUSE. Everything from here to the detach
	// is synchronous and every branch of it can end the run before any work
	// happens — a tenant that does not shape-check, a repo name, a missing actor,
	// no push credential. The asynchronous half is already traced by the
	// Dispatcher; this end was not traced at all, so a refusal left one 4xx in
	// the access log and nothing tying it to the session it just closed.
	ctx, span := codingTracer().Start(ctx, "coding.start", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(
		attribute.String("hanzo.org", org),
		attribute.String("hanzo.coding.repo", strings.TrimSpace(in.Repo)),
		attribute.Bool("hanzo.coding.routed", strings.TrimSpace(in.TargetID) != ""),
		attribute.Bool("hanzo.coding.resumed", strings.TrimSpace(in.After) != ""),
	)

	proposal, err := proposalOf(in.Draft, in.Assignees, in.Reviewers)
	if err != nil {
		return Accepted{}, err
	}

	org = strings.TrimSpace(org)
	if !OrgRE.MatchString(org) {
		// Shape-checked, not merely non-empty. The org becomes a git namespace and
		// the tenant every client call authorizes on, so an org carrying a separator
		// or a dot segment would address another tenant. It arrives from the
		// gateway or the plane already validated; this is the second lock, on the
		// side that would actually be harmed if the first ever failed.
		return Accepted{}, fmt.Errorf("coding: a run needs a tenant")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		// A run that lost its human must not execute AS THE ORG: that bills the
		// tenant for an unattributable act and hands an unlinked caller the org's
		// agent and its repos. Refused, never defaulted.
		//
		// It says "person", not "linked subject": linking is how ONE surface gets
		// here (a Slack sender resolving to a hanzo user), and this refusal now
		// reaches every surface. An API caller holding a token has nothing to link.
		return Accepted{}, fmt.Errorf("coding: a run needs the person it is for")
	}
	ref := strings.TrimSpace(in.Repo)
	// Task is Prompt under another name, and Prompt wins when both are given. The
	// input carries the one that won, so a follow-up (steer.go) reads it back.
	prompt := cmp.Or(strings.TrimSpace(in.Prompt), strings.TrimSpace(in.Task))
	if prompt == "" {
		return Accepted{}, fmt.Errorf("coding: say what the run should do — `prompt` (or `task`) is required")
	}
	in.Prompt, in.Task = prompt, ""
	devInst := truncateUTF8(cmp.Or(strings.TrimSpace(in.DeveloperInstructions), strings.TrimSpace(in.Instructions)), maxDeveloperInstructionsLen)
	in.DeveloperInstructions, in.Instructions, in.RequestID = devInst, "", ""
	// The issue the run resolves, on its own repository or another (#553), read
	// before anything is spent. The input carries it as the proposal writes it.
	issue, err := Closing(in.Issue)
	if err != nil {
		return Accepted{}, err
	}
	in.Issue = client.Issue(issue)
	// A run an issue asked for, from a surface with no conversation to narrate
	// in — a label put on in GitHub, on an issue no thread asked for — narrates in
	// the org's issue channel, where there is one.
	if issue != "" && strings.TrimSpace(in.Reply.Channel) == "" {
		in.Reply = issueChannel(org)
	}
	// Base and Project are shape-checked for the same reason Repo and Org are,
	// and they were the two that were not.
	//
	// Base is the worse of the two. It travels to RunRequest.BaseBranch and, on
	// the routed path, onto a CUSTOMER'S machine, where it lands in a `git clone
	// -b <base>` argument position. A value beginning with '-' is then not a
	// branch but a FLAG — `--upload-pack=...` or `--config=core.fsmonitor=...`
	// makes git run a command of the caller's choosing on the executor. BaseRE
	// is git's own branch shape, which is alnum-led and therefore cannot begin
	// with a dash; that is the property doing the work, not the length bound.
	base := BaseOf(in.Base, in.After)
	if base != "" && !BaseRE.MatchString(base) {
		return Accepted{}, fmt.Errorf("coding: %q is not a branch name", base)
	}
	// The branch a caller names is shape-checked here, for the reason Base is, and
	// is always under agent/ (AgentBranch).
	named, err := AgentBranch(in.Branch)
	if err != nil {
		return Accepted{}, err
	}
	// A folder is on a machine, and a run works in it instead of a repository.
	if in.Dir != "" && (strings.TrimSpace(in.TargetID) == "" || ref != "") {
		return Accepted{}, fmt.Errorf("coding: a run works in a folder of a machine you name in `targetId`, and not in a repository too")
	}
	project := strings.TrimSpace(in.Project)
	if project != "" && !RepoRE.MatchString(project) {
		return Accepted{}, fmt.Errorf("coding: %q is not a project name", project)
	}
	if err := CheckTool(in.Tool); err != nil {
		return Accepted{}, err
	}
	// What the run may do and what it thinks with, checked before anything is
	// spent (mode.go). The input carries them as they will run, so a follow-up
	// (steer.go) asks the same thing again.
	mode, model, effort, err := checkAsk(in.Tool, in.TargetID, in.Mode, in.Model, in.Effort)
	if err != nil {
		return Accepted{}, err
	}
	// A read-only run is a constraint on the MACHINE's harness. Our own sandbox
	// already hands plan/setup runs a credential that can only read, so readOnly
	// without a target would be a second, weaker promise — refuse it and point at
	// the one that is real. A pull request needs a push, which readOnly forbids.
	if in.ReadOnly && strings.TrimSpace(in.TargetID) == "" {
		return Accepted{}, zip.ErrBadRequest("coding: readOnly needs a targetId; a run in our sandbox with a read-only credential asks for mode: plan")
	}
	if in.ReadOnly && in.Pull {
		return Accepted{}, zip.ErrBadRequest("coding: a readOnly run cannot open a pull request, because it never pushes")
	}
	// The repository is a REFERENCE here — a name, the org's `owner/name`, or a
	// site — and it becomes a repository only when it is resolved against the
	// forge (repo.go). Its SHAPE is checked now, before anything is spent: every
	// segment is a name the git subsystem's own rule admits, so no token carries a
	// path, and an owner that is not this org's namespace is refused outright.
	//
	// No reference runs in an empty workspace of the run's own.
	//
	// A reference to GitHub itself (forge.GitHub) is not the forge's to
	// resolve: no mirror stands for it, so GitHub admits the run instead, below.
	//
	// Which namespace is the org's own is its WORKSPACE (workspace.go), read —
	// not made — before the reference is: a bare `owner/name` in it is the
	// forge's, and anywhere else is GitHub's.
	ws, err := workspace(ctx, org, false)
	if err != nil {
		return Accepted{}, fmt.Errorf("%w: %w", ErrWorkspace, err)
	}
	var name string
	var direct struct{ owner, name string }
	if o, n, ok := forge.GitHub(ws.Owner, org, ref); ref != "" && ok {
		direct.owner, direct.name = o, n
	} else if ref != "" {
		owner, n, err := reference(ref)
		if err != nil {
			return Accepted{}, err
		}
		if !ours(ws, org, owner) {
			return Accepted{}, fmt.Errorf("coding: %q is not one of this organization's repositories; "+
				"a repository on GitHub is named github.com/<owner>/<name>", ref)
		}
		// A name whose code is on GitHub and not in the forge namespace runs there
		// (repo.go homes): `cloud` in hanzo is github.com/hanzoai/cloud. Only a
		// bare name or one under the org's own name: `hanzoai/cloud` names the
		// open-source edition's namespace, and stays there.
		if o, h, ok := home(org, owner, n); ok {
			direct.owner, direct.name = o, h
		} else {
			name = n
		}
	} else if strings.TrimSpace(in.TargetID) != "" {
		// A RUN ON YOUR OWN MACHINE WITH NO REPOSITORY works in an empty directory
		// there, as one in a sandbox works in an empty workspace (below): nothing
		// is cloned and nothing is pushed. An automation that reads, researches or
		// posts asks for nothing more, and every one of them used to be refused
		// here (#779). What only a repository gives is refused by name.
		if what := needsRepo(in, named, issue); what != "" {
			return Accepted{}, fmt.Errorf("coding: a run on your own machine needs the repo it works in to %s; name it in `repo`", what)
		}
	}
	// What the person set for their runs (prefs.go), read as them: their own
	// instructions, and the model and effort a run that takes them thinks with
	// when its request names none. What the request names wins, the platform's
	// defaults come after both, and a default is checked exactly as a named one —
	// after the request's own passed, so a refusal here is the settings'.
	own, err := preferred(ctx)
	if err != nil {
		return Accepted{}, err
	}
	if takes(in.Tool, in.TargetID) && (model == "" && own.Model != "" || effort == "" && own.Effort != "") {
		if _, model, effort, err = checkAsk(in.Tool, in.TargetID, mode, cmp.Or(model, own.Model), cmp.Or(effort, own.Effort)); err != nil {
			return Accepted{}, fmt.Errorf("%w, from your settings", err)
		}
	}
	in.Mode, in.Model, in.Effort = mode, model, effort
	span.SetAttributes(attribute.String("hanzo.coding.mode", mode))
	if len(prompt) > maxPromptLen {
		prompt = prompt[:maxPromptLen]
	}
	// A run on the forge works in the org's workspace, and an org's first such
	// run MAKES it — a private namespace of its own (forge.Provision) — here,
	// before a session is opened or a slot is taken, so a workspace that cannot
	// be made is said plainly rather than met by whichever step first needed it.
	// A run on GitHub needs none.
	if direct.owner == "" && name != "" {
		if ws.Owner == "" {
			if ws, err = workspace(ctx, org, true); err != nil {
				return Accepted{}, fmt.Errorf("%w: %w", ErrWorkspace, err)
			}
		}
	}
	// A FOLLOW-UP WORKS WHERE ITS EARLIER RUN DID: the sandbox that run held is
	// resumed, tree and all, when the caller may read that run — and only once
	// this run's repository is known to be that run's (below). Its session also
	// says the branch it wrote and where that started, which a follow-up carries on.
	var earlier agents.Session
	if existing != nil {
		earlier = *existing
	} else if after := strings.TrimSpace(in.After); after != "" {
		if x, err := visibleRun(ctx, after); err == nil && x.Kind == kindCoding {
			earlier = x
		}
		// ONE RUN AT A TIME IN ONE SANDBOX. A follow-up works in its earlier run's
		// sandbox and on its branch, so an earlier run still working — or paused,
		// its work waiting there to be continued — has both in use: two harnesses
		// in one tree, two pushes to one branch. It is refused, for every caller;
		// a run carried on while it works is steered (steer.go), which stops it
		// first.
		if earlier.Status == agents.StatusRunning || earlier.Status == agents.StatusPaused {
			return Accepted{}, zip.ErrConflict(fmt.Sprintf(
				"coding: the run %s is %s; stop it or let it finish before carrying it on", earlier.ID, earlier.Status))
		}
	}

	d := Engine(log)
	// A RUN ON A MACHINE is checked here, on the caller's own request: the machine
	// is theirs to reach, and a runner on it is listening. The run checks again at
	// dispatch, and its queue checks once more, on a context the request is gone
	// from, so both are asked as the same caller, stated, and not as nobody — whom
	// a held org's machines, each its owner's alone, answer as not found.
	if target := strings.TrimSpace(in.TargetID); target != "" {
		if err := d.TargetGate(ctx, org, target, in.Dir, in.ReadOnly); err != nil {
			if refused := (*zip.HTTPError)(nil); errors.As(err, &refused) &&
				(refused.Code == agents.FolderNotGranted || refused.Code == agents.ReadOnlyUnsupported) {
				return Accepted{}, refused // 403 for a revoked folder, 422 for a missing capability
			}
			return Accepted{}, fmt.Errorf("coding: the machine %s is not available: %w", target, err)
		}
		gate, route, who := d.TargetGate, d.Route, cloud.Who(ctx)
		d.TargetGate = func(run context.Context, o, id, dir string, readOnly bool) error {
			return gate(zip.WithCaller(run, who), o, id, dir, readOnly)
		}
		d.Route = func(run context.Context, r RoutedRun) error {
			return route(zip.WithCaller(run, who), r)
		}
	}
	req := Req{
		Org: org, UserID: subject, AgentRef: in.AgentRef, Repo: name,
		Project: project, Base: base, Tool: strings.TrimSpace(in.Tool), Desktop: in.Desktop,
		Prompt: prompt, TimeoutSeconds: in.TimeoutSeconds, TargetID: strings.TrimSpace(in.TargetID), Dir: in.Dir,
		ReadOnly: in.ReadOnly,
		Mode:     mode, Model: model, Effort: effort, Instructions: own.Instructions, DeveloperInstructions: devInst, Issue: issue,
		Author: authorIn(ctx, ws, direct.owner == "" && name != ""),
		Pull:   in.Pull, Proposal: proposal,
	}

	// The session is opened on the ENDPOINT's context, which already carries the
	// tenant (the endpoint stated it, or it arrived on the wire). It is the one
	// synchronous client call, and it is what makes the handle real.
	//
	// Its actor is the validated subject, which the session-open boundary keys
	// as org/subject (agents.BillingActor) — so the session is theirs to read and
	// to steer, not another person's.
	var sessionID, existingBranch string
	if existing != nil {
		sessionID = existing.ID
		existingBranch = existing.Branch
		req.Resumed = parked == nil
		req.Sandbox = existing.Box
		if parked != nil {
			req.Parked, req.Kept, req.Trigger, req.Reason, req.Fault = true, parked.spec.Kept, parked.trigger, parked.reason, parked.spec.Fault
			if parked.trigger != byContinue { // a person's Continue begins a chain, and a count, of its own
				req.Chain, req.Tries = parked.created, parked.tries
			}
		}
	} else {
		// A RUN IN A DEV CONVERSATION IS BORN IN IT, its person's alone from its
		// first write (agents.OpenSessionOn): the one the call names, or the one the
		// run it carries on is in.
		within := cmp.Or(strings.TrimSpace(in.Conversation), agents.ConversationOf(earlier))
		// The session-open boundary (agents/session/open) keys the actor as
		// billingActor(org, subject). State the bare subject here so it does not
		// prefix an already composite value a second time — a key's subject is
		// hanzo/z and must own its sessions as hanzo/hanzo/z (agents.owner_test).
		sessionID, err = d.Sessions.OpenOn(ctx, org, subject, agentRefOr(in.AgentRef),
			codingTitle(cmp.Or(name, direct.name, "workspace"), prompt), req.TargetID, within)
		if err != nil {
			return Accepted{}, fmt.Errorf("coding: could not start a session: %w", err)
		}
	}
	req.SessionID = sessionID
	// WHICH BRANCH, in order: the one a resumed or parked run already wrote, which no caller's
	// name moves; the one the caller named; the one the run's issue names, so a retry or a
	// follow-up of that issue's work meets the same pull request; the session's own.
	branch := cmp.Or(existingBranch, named, BranchForIssue(issue), BranchFor(sessionID))

	// Bind this run's narration, if the endpoint gave it somewhere to talk. The
	// Dispatcher is a VALUE, so attaching a per-run watcher is a copy and never a
	// mutation of the shared engine — two concurrent runs cannot end up narrating
	// into each other's threads. It is bound once the session exists, because the
	// message carries the session's controls.
	//
	// A run a TURN is following narrates nothing of its own: the turn shows its
	// steps and its outcome in the turn's one message (apps/agents/follow.go), and
	// a second message beside it is the thing that must not happen. Handed a
	// message to carry on in, it narrates there — nobody else is writing it.
	var pr *progress
	if narrates(in.Reply, ts) {
		pr = newProgress(org, sessionID, in.Reply, ts)
	}
	if pr != nil {
		d.Watch = pr.watch
	}

	var (
		granted *grant
		repo    string
		tagRepo string
		// home is the repository's default branch, as GitHub names it; the forge's
		// is the one a clone with no branch checks out.
		home string
	)

	// Every refusal from here on has already opened a session, and each one ends
	// the same way. THE REASON GOES ON THE SESSION, not only to the caller: a run
	// that dies here and is closed `error` with nothing attached reads as
	// "status: error, events: 0", and every diagnosis starts in the cluster log.
	// It is LOGGED as well, because the two answer different questions — the
	// session carries it for whoever opens that run, and the log is what shows a
	// hundred runs failing for one reason.
	refuse := func(what, actor string, err error) (Accepted, error) {
		if log != nil {
			log("coding: "+what, "org", org, "repo", ref, "session", sessionID, "err", err)
		}
		if existing != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, what)
			return Accepted{}, err
		}
		_ = d.Sessions.Tag(ctx, org, sessionID, Tag{
			Kind: kindCoding, Repo: cmp.Or(tagRepo, ref), Environment: cmp.Or(req.TargetID, sandboxEnv),
			Mode: mode, Project: strings.ToLower(project),
		})
		d.mirror(ctx, org, sessionID, actor, kindStatus, map[string]any{"status": "error", "error": err.Error()})
		_ = d.Sessions.Close(ctx, org, sessionID, statusError, err.Error())
		span.RecordError(err)
		span.SetStatus(codes.Error, what)
		return Accepted{}, err
	}

	// THE CREDENTIAL IS RESOLVED HERE AND NOWHERE ELSE, and it is resolved AFTER
	// the session, because the session id NAMES the key on the forge — it is what
	// an operator reading the repository's settings sees, and what ties a live
	// credential back to the run holding it.
	//
	// It carries the REMOTE with it. A grant is a key registered on one
	// repository, so "which repository" and "the right to push to it" are one
	// fact and are resolved together; asking a separate client where to clone from
	// would be a second answer that could disagree with the credential.
	//
	// A ROUTED RUN NEEDS NO CREDENTIAL — the machine authenticates git with its
	// own — BUT IT STILL NEEDS THE ACTOR, and that is the distinction this used to
	// get wrong. The credential is what the sandbox path takes from here; the
	// ENTITLEMENT is what both paths take, because both act on a repository.
	//
	// Without it, a routed run was a confused deputy with a longer reach than the
	// sandbox one: cloud confirmed the repository as a site administrator, handed
	// the address to a machine holding the org's own broad credentials, and
	// streamed what it found back into the caller's session. Naming a repository
	// you cannot read was enough.
	//
	// So the actor is resolved ONCE, for every run, before either path.
	// onHub admits the run on owner/n on GitHub (onGitHub) and binds it there: its
	// checkout, and, for a run that pushes, where its branch is verified and
	// proposed. A run GitHub admits only to read keeps its work local.
	onHub := func(owner, n string) error {
		h, dflt, login, local, err := onGitHub(ctx, owner, n, answers(mode))
		if err != nil {
			return notAdmitted(owner+"/"+n, err)
		}
		if dflt == "" && h != nil && h.token != "" {
			dflt = h.defaultBranch(ctx)
		}
		home = strings.TrimSpace(dflt)
		if local && !answers(mode) && req.TargetID != "" {
			return fmt.Errorf("%w on %s/%s: it may only be read, and a run on your own machine pushes its branch", ErrNotAdmitted, owner, n)
		}
		repo = h.owner + "/" + h.name
		req.Actor, req.Repo, req.Home, tagRepo, req.Local = cmp.Or(login, subject), repo, repo, repo, local && !answers(mode)
		if base == "" && home != "" {
			base = home
			req.Base = base
		}
		if req.TargetID != "" {
			// A routed run pushes with the machine's own credential; the token only
			// proved the admission, and it ends here.
			if h.token != "" {
				if err := h.revoke(context.WithoutCancel(ctx)); err != nil && log != nil {
					log("coding: a routed run's admission token was not revoked", "org", org, "repo", repo, "err", err)
				}
			}
			return nil
		}
		req.Remote = h.remote()
		if h.token != "" {
			granted = &grant{Grant: forge.Grant{Remote: h.remote(), Key: h.token}, hub: h}
			hold(granted)
			req.Key = granted.Key
			req.RenewKey = h.ensureToken
			if r := h.readable(ctx, local, func(c context.Context) (*hub, error) {
				r, _, _, err := admit(c, h.owner, h.name, true)
				return r, err
			}); r != nil {
				req.ReadKey, req.RenewRead = r.token, r.ensureToken
			}
		}
		if !req.Local {
			d.VerifyRef, d.PR = h.landed, planePR{home: h}
		}
		return nil
	}
	switch {
	case name == "" && direct.owner == "":
		// A RUN ON NO REPOSITORY works in an empty workspace of its own: nothing is
		// cloned, nothing is made on the forge, and its work stays in its sandbox.
		req.Local = true
	case direct.owner != "":
		// A REPOSITORY ON GITHUB ALONE is admitted by GitHub, as the caller: their
		// own connected GitHub, or their org's installation with them administering
		// the org (apps/provider github_run.go), or nobody's for a public one. The
		// forge is not asked anything — it holds no copy to hold an ACL — so nobody
		// needs an account on it.
		if err := onHub(direct.owner, direct.name); err != nil {
			return refuse("GitHub did not admit the run", subject, err)
		}
	default:
		// THE ACTOR is the person's forge login in the estate's namespaces, where
		// the forge's ACL applied to them decides. A workspace the platform made
		// has no forge accounts in it: the machine acts there, and what admits the
		// person is that the workspace is their validated org's (workspace.go).
		//
		// The estate's namespaces are GitHub's too, so a person the forge cannot
		// act as — no account there — or a repository it does not hold is asked of
		// GitHub before the run is refused.
		var actor string
		if !ws.Made {
			if actor, err = resolveActor(ctx); err != nil {
				if herr := onHub(ws.Owner, name); herr != nil {
					return refuse("the run has no actor to act as", "", err)
				}
				break
			}
		}
		req.Actor = actor

		// WHICH REPOSITORY, asked of the forge as the actor, for both paths: the
		// sandbox run clones it and the routed run hands its address to a machine.
		var dflt string
		if repo, dflt, err = resolveRepo(ctx, ws, actor, name, project); err != nil {
			if errors.Is(err, errVacant) || onHub(ws.Owner, name) != nil {
				return refuse("the repository did not resolve", actor, err)
			}
			break
		}
		home = dflt
		req.Repo, tagRepo, req.Made = repo, ws.Owner+"/"+repo, ws.Made

		if req.TargetID == "" {
			// The run's branch is verified and proposed in the workspace it was
			// resolved in. Bound on this run's copy of the Dispatcher, as Watch is.
			d.VerifyRef, d.PR = verifyIn(ws.Owner), planePR{ws: ws}
			g, err := delegate(ctx, ws, actor, repo, sessionID, answers(mode))
			if err != nil {
				return refuse("the forge refused a push credential", actor, err)
			}
			granted = g
			req.Remote, req.Key, req.Known = g.Remote, g.Key, g.Known
			req.Site = siteOf(ws, g.hub != nil, mode, project, repo)
			// A run homed on GitHub is verified and proposed THERE. Bound on this
			// run's copy of the Dispatcher, as Watch is, so no other run moves with it.
			if g.hub != nil {
				d.VerifyRef, d.PR = g.hub.landed, planePR{home: g.hub}
				req.RenewKey = g.hub.ensureToken
				if r := g.hub.readable(ctx, answers(mode), func(c context.Context) (*hub, error) {
					return mint(c, g.hub.owner, g.hub.name, true)
				}); r != nil {
					req.ReadKey, req.RenewRead = r.token, r.ensureToken
				}
				if home == "" {
					home = g.hub.defaultBranch(ctx)
				}
			}
		}
	}

	// THE EARLIER RUN'S SANDBOX IS REUSED ONLY FOR THE SAME REPOSITORY. Its tree
	// is that repository's checkout, and this run pushes whatever tree it works in
	// to the remote it resolved: a follow-up naming another repository on an
	// earlier run's session would otherwise push one repository's code into
	// another — the private backend into the open-source edition. Anything else is
	// a fresh clone of the repository this run named.
	//
	// And it is claimed here, before anything runs: a sandbox is one live run's at a
	// time (agents TagSession). The earlier run may be over while another works in
	// its sandbox — the run a steer started there, or another follow-up of it —
	// and then this one is refused rather than started beside it.
	if earlier.Box != "" && earlier.Repo == tagRepo && req.TargetID == "" && existing == nil {
		if err := d.Sessions.Tag(ctx, org, sessionID, Tag{Sandbox: earlier.Box}); err != nil {
			return refuse("the earlier run's sandbox is in use", subject, zip.ErrConflict(fmt.Sprintf(
				"coding: another run is still working in the sandbox of %s; stop it or let it finish before carrying it on: %v",
				earlier.ID, err)))
		}
		req.Sandbox = earlier.Box
	}

	// A FOLLOW-UP WRITES THE BRANCH ITS EARLIER RUN WROTE, so one piece of work is
	// one branch and one pull request however many instructions it takes. The
	// push never forces, so it only adds to that branch. A branch the caller names
	// wins, and a run on a machine keeps its own branch: the machine cuts it from
	// its base, so the earlier branch is its base instead.
	//
	// What the proposal targets stays where it was — the earlier run's base — unless
	// the caller named a base: the branch cannot be proposed into itself.
	if after := strings.TrimSpace(in.After); after != "" && earlier.Repo == tagRepo && earlier.Branch != "" {
		switch {
		case (named == "" || named == earlier.Branch) && req.TargetID == "":
			branch = earlier.Branch
			if strings.TrimSpace(in.Base) == "" {
				base = cmp.Or(earlier.Base, home)
			}
		case strings.TrimSpace(in.Base) == "":
			base = earlier.Branch
		}
		req.Base = base
	}
	// A BRANCH THAT IS ALREADY ON THE REMOTE IS CARRIED ON, not replaced: a
	// follow-up's, or an `agent/` branch a run names (#553). The checkout starts
	// on it and the run adds commits, pushed without force, so it can only add to
	// the branch and its pull request. It is pushed with the credential the run
	// was admitted with, which reaches that branch already: naming it grants
	// nothing the caller does not hold.
	req.Branch = branch
	if (branch != BranchFor(sessionID) || parked != nil) && req.TargetID == "" && !req.Local && repo != "" && d.VerifyRef != nil {
		if _, ok := d.VerifyRef(ctx, org, repo, branch); ok {
			req.From = branch
		}
	}

	// A FOLLOW-UP GOES ON FROM ITS EARLIER RUN'S BRANCH ONLY WHERE THAT BRANCH IS.
	// A run that stopped before it pushed kept its branch in its own sandbox. Once
	// that sandbox is gone, the follow-up's clone asks the remote for a branch it
	// never had — "Remote branch agent/… not found in upstream origin", exit 128,
	// seven seconds in — and its pull request would have no base. Such a follow-up
	// starts where the earlier run did: its base, or the repository's default when
	// that is not there either. A tree the earlier sandbox still holds is worked in
	// as before (req.Sandbox); its unpushed commits come along in it.
	if after := strings.TrimSpace(in.After); after != "" && strings.TrimSpace(in.Base) == "" && base == BranchFor(after) &&
		req.TargetID == "" {
		on := func(branch string) bool {
			if req.Local || repo == "" || d.VerifyRef == nil {
				return !strings.HasPrefix(branch, agentPrefix)
			}
			_, ok := d.VerifyRef(ctx, org, repo, branch)
			return ok
		}
		if next := followBase(on, base, earlier.Base, home); next != base {
			base = next
			req.Base = base
			if log != nil {
				log("coding: a follow-up's earlier branch is not on the remote; it starts from that run's base",
					"org", org, "session", sessionID, "after", after, "base", baseOr(base))
			}
		}
	}

	// THE SESSION SAYS WHAT IT RUNS, so a board that lists coding runs reads the
	// repository, the branches and where it executes off the session rather than
	// out of its transcript. Best-effort: a run whose label did not land is still
	// the run.
	// A plan or a setup writes no branch, and a local run pushes none, so neither
	// names one — to the session or to the caller.
	if answers(mode) || req.Local {
		branch = ""
	}
	tagBase := base
	if tagRepo != "" && tagBase == "" {
		tagBase = cmp.Or(home, "main")
	}
	if err := d.Sessions.Tag(ctx, org, sessionID, Tag{
		Kind: kindCoding, Repo: tagRepo, Base: tagBase, Branch: branch, Environment: cmp.Or(req.TargetID, sandboxEnv),
		// A project's slug is lower-case wherever the projects app keeps it, so the
		// tag its list of runs is read by is too.
		Mode: mode, Project: strings.ToLower(project),
	}); err != nil && log != nil {
		log("coding: the session was not told what it runs", "org", org, "session", sessionID, "err", err)
	}

	// THE REQUEST IS COMMITTED HERE, before anything runs: its outcome is recorded if
	// the claim is still this attempt's, which is what makes the run happen once. A
	// claimer that was retaken (it was slow, not dead) finds out now and spawns nothing.
	// WHAT WAS LEFT QUEUED on the run this one follows (`after`) is claimed for its task,
	// whole and in order, each row that fits; the answer says how many, and how many it left.
	var carried carrying
	if existing == nil && earlier.ID != "" && earlier.Kind == kindCoding {
		carried = carryAfter(ctx, org, subject, earlier.ID, req.Prompt, log)
	}
	acc := Accepted{
		SessionID: sessionID, Branch: branch, Repo: repo,
		Routed: req.TargetID != "", TargetID: req.TargetID,
		Carried: len(carried.rows), Left: carried.left, Dropped: len(carried.drops), Note: carried.note(),
	}
	if c := claimOf(ctx); c != nil {
		if err := c.Settle(ctx, acc); err != nil {
			carried.release(ctx, log)
			_ = withdraw(context.WithoutCancel(ctx), org, granted)
			if errors.Is(err, errClaim) {
				err = zip.ErrConflict("coding: another attempt at this request now owns it; ask again for its answer")
			} else {
				err = unavailable(err)
			}
			return refuse("the request could not be committed", subject, err)
		}
	}
	// A PARKED RUN GOES ON in the session it waited in, and the session says so in one
	// update that only one try wins (agents.UnparkSession): of two that raced, the other
	// launches nothing. A session somebody stopped meanwhile is not gone on in.
	if parked != nil {
		ok, err := unparkSession(ctx, org, sessionID)
		if err == nil && !ok {
			err = errStale
		}
		if err != nil {
			_ = withdraw(context.WithoutCancel(ctx), org, granted)
			return refuse("the parked run could not go on", subject, err)
		}
	}
	// WHAT WAS SAID TO A RESUMED RUN while no pod held it joins its task, in order,
	// and is marked taken before the run starts, so no read of the inbox hands it over
	// a second time (inbox.go).
	if said := backlog(ctx, existing, org, sessionID, subject, req.Prompt, log); len(said) > 0 {
		in.Prompt = withSaid(in.Prompt, said)
		req.Prompt = withSaid(req.Prompt, said)
	}
	// What it claimed joins the task, and is delivered once the run has been spawned.
	if len(carried.rows) > 0 {
		in.Prompt = withSaid(in.Prompt, carried.rows)
		req.Prompt = withSaid(req.Prompt, carried.rows)
	}
	// Detach, and state the tenant again on the way out.
	//
	// Both halves are load-bearing. DETACHED because the run outlives the
	// endpoint's request by minutes — on the endpoint's context the model call is
	// cancelled the instant we answer. STATED because detaching drops the request
	// the tenant was riding on, and every client call left in the run authorizes
	// on the caller. This is the exact pairing the chat bridge uses, and the exact
	// one whose absence made every coding run fail.
	//
	// A run that pushes with a GitHub token renews its token before remote
	// operations when the admission token nears its one-hour expiry (github.go
	// ensureToken), so it may run up to the full requested budget.
	req.TimeoutSeconds = timeoutOr(req.TimeoutSeconds)
	runCtx, cancel, clk := runContext(org, req.TimeoutSeconds, ceiling())
	runCtx = paying(runCtx, ctx)
	// A SANDBOX run can be stopped, paused and redirected with its work kept
	// (steer.go). Its flight lands LAST, after the credential is given back.
	//
	// NO COUNT OF RUNS ADMITS ONE. How many a person or an org has going is not a
	// limit: what each run spends is, and the sandbox it leases is admitted by its
	// payer's balance (apps/sandbox prepaid.go), the free lane by its own rules.
	var f *flight
	var res Result
	if req.TargetID == "" {
		var stop context.CancelCauseFunc
		runCtx, stop = context.WithCancelCause(runCtx)
		in.Base = base
		// A run the model API refuses waits for that to clear, holding what it needs to go
		// on (park.go). Bound on this run's copy of the Dispatcher, as Watch is; the task is
		// read when the run parks, so it carries what was said to the run since.
		d.Park = func(pctx context.Context, p Parked) error {
			who := zip.CallerOf(runCtx)
			return flights.parkRun(pctx, holding{org: org, session: sessionID, subject: subject, in: in, ts: pr.message,
				name: strings.TrimSpace(who.Name)}, p)
		}
		// The run's ticket, for its harness to ask its person with (key.go).
		req.Ticket = ticket()
		f = &flight{org: org, subject: subject, session: sessionID, in: in, sink: pr, cancel: stop, done: make(chan struct{}),
			ctx: runCtx, ticket: ticketKey(req.Ticket), clock: clk}
		req.OnTurnSteer = f.setSteerer
		fly(f)
	}
	go func() {
		if f != nil {
			defer func() { f.land(res, context.Cause(runCtx)) }()
		}
		defer cancel()
		// The grant dies with the run. On a cancel-immune context, because the run that most
		// needs its credential withdrawn is the one that hit its deadline.
		defer func() {
			if err := withdraw(context.WithoutCancel(runCtx), org, granted); err != nil && log != nil {
				log("coding: the run's push access was not withdrawn",
					"org", org, "repo", repo, "grant", granted.String(), "err", err)
			}
		}()
		defer func() {
			// A run executes untrusted model output through a long client chain. An
			// unrecovered panic here would take down every tenant sharing this
			// process, so it is contained — registered last so it runs first — and
			// the session is CLOSED with it: a run that crashed is over, and a
			// session left running reads as work still going to everyone watching.
			if r := recover(); r != nil {
				if log != nil {
					log("coding: run panic (recovered)", "org", org, "repo", repo, "err", r)
				}
				res = d.fail(context.WithoutCancel(runCtx), org, sessionID, subject, Result{},
					fmt.Sprintf("the coding run crashed: %v", r), "")
			}
		}()
		// Run is its own terminal: it mirrors the outcome into the session and
		// closes it, on a cancel-immune context, whether it succeeded or failed.
		// There is nothing left to report here and nobody left to report it to —
		// the endpoint answered minutes ago, and the session is the record.
		res = d.Run(runCtx, req)
	}()
	carried.finish(ctx, log)

	return acc, nil
}

// needsRepo names what a run on a machine asks for that only a repository can
// give, or "" when it asks for none of it. A run with no repository works in an
// empty directory: there is no pull request to open, no branch to push, no base
// to start from, and no change that could close an issue. named is the branch the
// caller named (AgentBranch), issue the one it resolves (Closing). Base is read
// as the caller wrote it, not as a follow-up derives it from `after`.
