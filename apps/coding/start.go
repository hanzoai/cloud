package coding

// start.go is the ONE way a coding run begins.
//
// `POST /v1/coding` is the door, and anything added later arrives here too. That
// is not tidiness: a door that assembled its own
// Dispatcher would be a second ENGINE with its own pool and its own in-flight
// set, and a run started from chat would be invisible to the app that shares
// its name. One Start, one pool, one process.
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
// would have been discarded anyway. Every seam call in every coding run
// therefore answered `authorize: no org on the call`: no session, an empty clone
// URL the dispatcher reads as "git is not available", and a run dead before a
// model was ever asked anything. The chat turn died of this shape one file over
// and was fixed there (bridgeRunContext); this is the same fix for the other
// half, and runContext below is the only place a coding run's context is made.
//
// It is not a laundering hole. For SUPPLIES a tenant where there is none and
// cannot override one, and the org it supplies was resolved server-side — from a
// Slack-signature-verified team_id, or from the authenticated caller of
// /v1/coding — never from a field a client can set.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/plane"
)

const (
	// runBudget bounds one coding run end to end. Far longer than a chat turn: a
	// real run clones, drives a model edit loop, runs tests, and pushes.
	defaultRunBudget = 25 * time.Minute

	// The pool bounds SANDBOXES, not model turns — a different resource with a
	// different lifetime from the chat pool, which is why it is sized separately.
	// The per-org cap is the availability isolation: one tenant cannot starve the
	// others out of sandbox capacity.
	defaultConcurrency    = 8
	defaultOrgConcurrency = 2

	// maxRunBudget is the longest run the engine will admit, whatever a caller
	// asks for. Without it TimeoutSeconds was unbounded: a caller could hold a
	// pool slot — one of two its org has — for a day, and ask the forge to
	// delegate a push for just as long. A budget nobody bounds is a resource
	// nobody bounds.
	maxRunBudget = 30 * time.Minute
)

// Accepted is what a door returns the instant a run is admitted. A run takes
// minutes; the door answers in milliseconds and hands back the session that is
// the run's record, its live stream, and the handle for every later question.
type Accepted struct {
	SessionID string
	Branch    string
	Repo      string
	Routed    bool
	TargetID  string
}

// ErrBusy is the honest refusal when the pool is full. It is separated from a
// validation failure because a caller should retry this one and only this one.
var ErrBusy = fmt.Errorf("coding: at capacity")

var (
	engineOnce sync.Once
	engine     Dispatcher
	pool       *limiter
)

// Engine returns the process's ONE Dispatcher, assembled on first use. log
// carries the run's best-effort failures (a dropped session mirror, a PR that
// would not file) into the host's log rather than dropping them.
func Engine(log func(msg string, kv ...any)) Dispatcher {
	engineOnce.Do(func() {
		engine = NewDispatcher(log)
		pool = newLimiter(concurrency(), orgConcurrency())
	})
	return engine
}

// Start admits one coding run and returns its handle.
//
// org and subject are the CALLER's, read off the caller by the door and never
// taken from the request body — the two are parameters for exactly that reason,
// and the symmetry is the contract: a door that can name one can name the other,
// and neither is nameable. Everything the run then does happens in that org and
// nowhere else: its session, its repo, its credential, its PR; and it is
// attributed to that person.
//
// It is synchronous up to the point the run is admitted — validate, resolve the
// credential, open the session — and detached after it. That split is what lets
// a door answer immediately with a real handle instead of an empty promise, and
// it is why the session is opened HERE rather than inside Run.
func Start(ctx context.Context, org, subject string, in plane.CodingStartIn, log func(msg string, kv ...any)) (Accepted, error) {
	org = strings.TrimSpace(org)
	if !OrgRE.MatchString(org) {
		// Shape-checked, not merely non-empty. The org becomes a git namespace and
		// the tenant every seam call authorizes on, so an org carrying a separator
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
	repo := strings.TrimSpace(in.Repo)
	prompt := strings.TrimSpace(in.Prompt)
	if repo == "" || prompt == "" {
		return Accepted{}, fmt.Errorf("coding: repo and task are required")
	}
	if !RepoRE.MatchString(repo) {
		// A hostile repo token could otherwise smuggle a path segment and address
		// another org's namespace. Same rule the git subsystem's own name check uses.
		return Accepted{}, fmt.Errorf("coding: %q is not a repo name", repo)
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
	project := strings.TrimSpace(in.Project)
	if project != "" && !RepoRE.MatchString(project) {
		return Accepted{}, fmt.Errorf("coding: %q is not a project name", project)
	}
	if err := CheckTool(in.Tool); err != nil {
		return Accepted{}, err
	}
	if len(prompt) > maxPromptLen {
		prompt = prompt[:maxPromptLen]
	}

	d := Engine(log)
	if !pool.acquire(org) {
		return Accepted{}, ErrBusy
	}
	// Bind this run's narration, if the door gave it somewhere to talk. The
	// Dispatcher is a VALUE, so attaching a per-run watcher is a copy and never a
	// mutation of the shared engine — two concurrent runs cannot end up narrating
	// into each other's threads.
	if pr := newProgress(org, in.ReplyChannel, in.ReplyThread); pr != nil {
		d.Watch = pr.watch
	}

	req := Req{
		Org: org, UserID: subject, AgentRef: in.AgentRef, Repo: repo,
		Project: project, Base: base, Tool: strings.TrimSpace(in.Tool), Desktop: in.Desktop,
		Prompt: prompt, TimeoutSeconds: in.TimeoutSeconds, TargetID: strings.TrimSpace(in.TargetID),
	}

	// The session is opened on the DOOR's context, which already carries the
	// tenant (the door stated it, or it arrived on the wire). It is the one
	// synchronous seam call, and it is what makes the handle real.
	sessionID, err := d.Sessions.OpenOn(ctx, org, subject, agentRefOr(in.AgentRef),
		codingTitle(repo, prompt), req.TargetID)
	if err != nil {
		pool.release(org)
		return Accepted{}, fmt.Errorf("coding: could not start a session: %w", err)
	}
	req.SessionID = sessionID
	branch := BranchFor(sessionID)

	// THE CREDENTIAL IS RESOLVED HERE AND NOWHERE ELSE, and it is resolved AFTER
	// the session, because the session id NAMES the key on the forge — it is what
	// an operator reading the repository's settings sees, and what ties a live
	// credential back to the run holding it.
	//
	// It carries the REMOTE with it. A grant is a key registered on one
	// repository, so "which repository" and "the right to push to it" are one
	// fact and are resolved together; asking a separate seam where to clone from
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
	actor, aerr := resolveActor(ctx)
	if aerr != nil {
		_ = d.Sessions.Close(ctx, org, sessionID, statusError)
		pool.release(org)
		return Accepted{}, aerr
	}
	req.Actor = actor

	var granted forge.Grant
	if req.TargetID == "" {
		g, err := delegate(ctx, org, actor, repo, sessionID)
		if err != nil {
			_ = d.Sessions.Close(ctx, org, sessionID, statusError)
			pool.release(org)
			return Accepted{}, err
		}
		granted = g
		req.Remote, req.Key, req.Known = g.Remote, g.Key, g.Known
	}

	// Detach, and state the tenant again on the way out.
	//
	// Both halves are load-bearing. DETACHED because the run outlives the door's
	// request by minutes — on the door's context the model call is cancelled the
	// instant we answer. STATED because detaching drops the request the tenant was
	// riding on, and every seam call left in the run authorizes on the caller.
	// This is the exact pairing the chat bridge uses, and the exact one whose
	// absence made every coding run fail.
	runCtx, cancel := runContext(org, req.TimeoutSeconds)
	go func() {
		defer cancel()
		defer pool.release(org)
		// The grant dies with the run. Registered before the panic guard so it runs
		// after it — a run that panicked still gives the capability back — and on a
		// cancel-immune context, because the run that most needs its credential
		// withdrawn is the one that hit its deadline, and that is exactly when
		// runCtx is already dead.
		//
		// A failure here is SAID OUT LOUD rather than dropped. Nothing expires a
		// key on the forge, so a withdrawal that did not happen is a live push
		// credential and not a tidiness problem; the next run on this repository
		// sweeps it, and this line is how anyone knows to look.
		defer func() {
			if err := withdraw(context.WithoutCancel(runCtx), org, granted); err != nil && log != nil {
				log("coding: the run's push access was not withdrawn",
					"org", org, "repo", repo, "key", granted.ID, "err", err)
			}
		}()
		defer func() {
			// A run executes untrusted model output through a long seam chain. An
			// unrecovered panic here would take down every tenant sharing this
			// process, so it is contained — registered last so it runs first.
			if r := recover(); r != nil && log != nil {
				log("coding: run panic (recovered)", "org", org, "repo", repo, "err", r)
			}
		}()
		// Run is its own terminal: it mirrors the outcome into the session and
		// closes it, on a cancel-immune context, whether it succeeded or failed.
		// There is nothing left to report here and nobody left to report it to —
		// the door answered minutes ago, and the session is the record.
		d.Run(runCtx, req)
	}()

	return Accepted{
		SessionID: sessionID, Branch: branch, Repo: repo,
		Routed: req.TargetID != "", TargetID: req.TargetID,
	}, nil
}

// runContext is the context ONE coding run executes on: detached from the door's
// request, carrying the tenant it acts for, bounded by the run budget.
//
// It takes an org and a budget and NOTHING ELSE — deliberately. A ctx parameter
// here would be an invitation to pass the door's, which both cancels the run
// when the door answers and silently discards the tenant. The signature is the
// guard; bridgeRunContext is the same shape for the same reason.
func runContext(org string, timeoutSeconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cloud.For(context.Background(), org), budget(timeoutSeconds))
}

// ---- the bounded pool ------------------------------------------------------

// limiter bounds concurrent runs two ways: a GLOBAL cap across all orgs, and a
// PER-ORG cap. Tenant isolation of data already holds via the resolved org; this
// is the AVAILABILITY isolation that stops one workspace exhausting the sandbox
// capacity every other workspace shares.
type limiter struct {
	mu       sync.Mutex
	inflight map[string]int
	perOrg   int
	global   chan struct{}
}

func newLimiter(global, perOrg int) *limiter {
	if global < 1 {
		global = 1
	}
	if perOrg < 1 {
		perOrg = 1
	}
	if perOrg > global {
		perOrg = global
	}
	return &limiter{inflight: make(map[string]int), perOrg: perOrg, global: make(chan struct{}, global)}
}

// acquire takes one global and one per-org slot, non-blocking. It returns false
// with NOTHING acquired when the org is at its cap or the pool is full — the
// per-org check precedes the global take, and the count only rises on a
// successful send, so a refusal can never leak a slot.
func (l *limiter) acquire(org string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[org] >= l.perOrg {
		return false
	}
	select {
	case l.global <- struct{}{}:
		l.inflight[org]++
		return true
	default:
		return false
	}
}

func (l *limiter) release(org string) {
	l.mu.Lock()
	if n := l.inflight[org]; n > 1 {
		l.inflight[org] = n - 1
	} else {
		delete(l.inflight, org)
	}
	l.mu.Unlock()
	<-l.global
}

// ---- config (env, read at call time — operator-injected from KMS) ----------

// budget is how long ONE run may take, and therefore how long its delegated
// push stays usable. Capped at maxRunBudget: a caller names a timeout, it does
// not name an unbounded one.
func budget(timeoutSeconds int) time.Duration {
	d := defaultRunBudget
	switch {
	case timeoutSeconds > 0:
		d = time.Duration(timeoutSeconds) * time.Second
	default:
		if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CODING_TIMEOUT_SEC"))); err == nil && v > 0 {
			d = time.Duration(v) * time.Second
		}
	}
	return min(d, maxRunBudget)
}

func concurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CODING_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return defaultConcurrency
}

func orgConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CODING_ORG_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return defaultOrgConcurrency
}

func agentRefOr(ref string) string {
	if r := strings.TrimSpace(ref); r != "" {
		return r
	}
	return "hanzo"
}
