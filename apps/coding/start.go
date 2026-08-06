package coding

// start.go is the ONE way a coding run begins.
//
// Every door — the Slack `code:` trigger, `POST /v1/coding`, and anything added
// later — arrives here. That is not tidiness: a door that assembled its own
// Dispatcher would be a second ENGINE with its own pool and its own in-flight
// set, and a run started from chat would be invisible to the app that shares
// its name. One Start, one pool, one process.
//
// # The tenant, and the bug that made every run fail
//
// A run is a long chain of cross-process calls: open the session (agents), read
// the clone URL (git), dispatch the sandbox (bot), verify the pushed ref (git),
// file the PR (tracker). Every one of those authorizes on the CALLER's org,
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

	// agentCredProvider / agentCredToken / agentCredUser name the per-org agent
	// git credential in the integrations KMS namespace. An operator seals the
	// org's key there; the engine reads it fail-closed, at the one moment it
	// dispatches, and never logs it.
	agentCredProvider = "agent"
	agentCredToken    = "git-token"
	agentCredUser     = "git-user"
	defaultAgentUser  = "x-access-token"
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
// org is the CALLER's tenant, read off the caller by the door and never taken
// from the request body. Everything the run then does happens in that org and
// nowhere else: its session, its repo, its credential, its PR.
//
// It is synchronous up to the point the run is admitted — validate, resolve the
// credential, open the session — and detached after it. That split is what lets
// a door answer immediately with a real handle instead of an empty promise, and
// it is why the session is opened HERE rather than inside Run.
func Start(ctx context.Context, org string, in plane.CodingStartIn, log func(msg string, kv ...any)) (Accepted, error) {
	org = strings.TrimSpace(org)
	if !OrgRE.MatchString(org) {
		// Shape-checked, not merely non-empty. The org becomes a KMS PATH SEGMENT
		// (credRef) and a git namespace, so an org carrying a separator or a dot
		// segment would address another tenant's secret. It arrives from the
		// gateway or the plane already validated; this is the second lock, on the
		// side that would actually be harmed if the first ever failed.
		return Accepted{}, fmt.Errorf("coding: a run needs a tenant")
	}
	subject := strings.TrimSpace(in.Subject)
	if subject == "" {
		// A run that lost its human must not execute AS THE ORG: that bills the
		// tenant for an unattributable act and hands an unlinked caller the org's
		// agent and its repos. Refused, never defaulted.
		return Accepted{}, fmt.Errorf("coding: a run needs a linked subject")
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
		Project: strings.TrimSpace(in.Project), Base: strings.TrimSpace(in.Base),
		Prompt: prompt, TimeoutSeconds: in.TimeoutSeconds, TargetID: strings.TrimSpace(in.TargetID),
	}

	// THE CREDENTIAL IS RESOLVED HERE AND NOWHERE ELSE.
	//
	// A routed run needs none — the machine authenticates git with its own — so
	// the fetch is skipped and a workspace with no sealed credential can still
	// route. A sandbox run without one fails closed: an empty token is an error,
	// never a run that proceeds and discovers at push time it cannot write.
	if req.TargetID == "" {
		user, token, err := agentCredential(ctx, org)
		if err != nil {
			pool.release(org)
			return Accepted{}, err
		}
		req.CredUser, req.CredToken = user, token
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

// agentCredential reads the org's agent git credential from KMS over the plane,
// fail-closed: an unreachable KMS, an absent secret, or an empty value each
// return an error and NEVER a value. The username is a fixed basic-auth label
// (git ignores it; the token is the secret) unless an operator sealed one.
//
// The org is NOT an argument to KMS. plane.SecretIn has no org field, on purpose:
// the tenant rides the caller, so this reads the caller's own namespace and a
// run can never address another tenant's secret by naming it.
func agentCredential(ctx context.Context, org string) (user, token string, err error) {
	sec, err := plane.Ask[plane.SecretIn, plane.Secret](ctx, "kms", plane.KMSGet,
		&plane.SecretIn{Ref: credRef(org, agentCredToken)})
	if err != nil {
		return "", "", fmt.Errorf("coding: no agent git credential for this org: %w", err)
	}
	if sec == nil {
		return "", "", fmt.Errorf("coding: no agent git credential for this org")
	}
	token = strings.TrimSpace(string(sec.Value))
	if token == "" {
		return "", "", fmt.Errorf("coding: the agent git credential for this org is empty")
	}
	user = defaultAgentUser
	if u, uerr := plane.Ask[plane.SecretIn, plane.Secret](ctx, "kms", plane.KMSGet,
		&plane.SecretIn{Ref: credRef(org, agentCredUser)}); uerr == nil && u != nil {
		if v := strings.TrimSpace(string(u.Value)); v != "" {
			user = v
		}
	}
	return user, token, nil
}

// credRef addresses the org's sealed agent credential. org is shape-checked by
// Start before it ever reaches here, which is what makes this concatenation safe. It mirrors the
// integrations KMS layout, which is where an operator seals it today, so this
// reads the credential that already exists rather than minting a second path
// for the same secret.
func credRef(org, name string) string {
	return "/orgs/" + org + "/integrations/" + agentCredProvider + "/" + name
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

func budget(timeoutSeconds int) time.Duration {
	if timeoutSeconds > 0 {
		return time.Duration(timeoutSeconds) * time.Second
	}
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CODING_TIMEOUT_SEC"))); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	return defaultRunBudget
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
