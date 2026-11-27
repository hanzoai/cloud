package coding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// Resume adopts an orphaned running coding session after a platform rollout and
// continues its run in its surviving sandbox on the same session ID and branch.
// It returns true when the session was adopted into flight on this process.
func Resume(ctx context.Context, x agents.Session, in client.CodingStartIn, log func(msg string, kv ...any)) bool {
	if x.Kind != kindCoding || x.Box == "" || x.Target != "" {
		return false
	}
	flights.Lock()
	_, active := flights.m[x.ID]
	flights.Unlock()
	if active {
		return true
	}
	subject := strings.TrimPrefix(x.Actor, x.Org+"/")
	if subject == "" || subject == x.Actor {
		return false
	}
	who := zip.Caller{Org: x.Org, User: subject}
	runCtx := zip.WithCaller(cloud.For(context.WithoutCancel(ctx), x.Org), who)
	if in.Repo == "" && x.Repo != "" {
		in.Repo = x.Repo
		if ws, err := workspace(runCtx, x.Org, false); err == nil {
			if owner, _, ok := strings.Cut(x.Repo, "/"); ok && !ours(ws, x.Org, owner) {
				in.Repo = "github.com/" + x.Repo
			}
		}
	}
	if in.Base == "" {
		in.Base = x.Base
	}
	if in.Branch == "" {
		in.Branch = x.Branch
	}
	if in.Mode == "" {
		in.Mode = x.Mode
	}
	if in.Project == "" {
		in.Project = x.Project
	}
	if in.AgentRef == "" {
		in.AgentRef = x.Agent
	}
	_, err := startSession(runCtx, x.Org, subject, in, log, "", &x, nil)
	if err != nil && log != nil {
		log("coding: could not resume orphaned sandbox run", "org", x.Org, "session", x.ID, "sandbox", x.Box, "err", err)
	}
	return err == nil
}

// start is Start carrying on in message ts, which a follow-up does (steer.go).

func needsRepo(in client.CodingStartIn, named, issue string) string {
	switch {
	case in.Pull:
		return "open a pull request (`pull`)"
	case named != "":
		return "push the branch it names (`branch`)"
	case strings.TrimSpace(in.Base) != "":
		return "start from the branch it names (`base`)"
	case issue != "":
		return "close the issue it names (`issue`)"
	}
	return ""
}

// siteOf is the project a run's pushed work is published to (publish.go): a
// build on a project whose code is in a workspace the platform made, so the
// project's own address — what the builder previews — shows it. A run homed on
// GitHub, a plan or a setup, a run on no project, and every run in the estate,
// whose sites build in their own CI, publish nothing.
func siteOf(ws forge.Workspace, github bool, mode, project, repo string) Site {
	if !ws.Made || github || answers(mode) || project == "" {
		return Site{}
	}
	return Site{Project: project, Owner: ws.Owner, Repo: repo}
}

// ErrNotAdmitted is a run GitHub would not admit: the caller's own GitHub does
// not push to the repository, and they do not administer an org whose
// installation holds it. The endpoint answers it 403 — not a malformed request,
// a refusal.
var ErrNotAdmitted = errors.New("coding: GitHub did not admit this run")

// notAdmitted says why a GitHub admission failed: a refusal (the admission's own
// 403 or 404) is ErrNotAdmitted, anything else is the admission being
// unreachable, which says nothing about the caller.
func notAdmitted(repo string, err error) error {
	var he *zip.HTTPError
	if errors.As(err, &he) && (he.Status == http.StatusForbidden || he.Status == http.StatusNotFound) {
		return fmt.Errorf("%w on %s: %s", ErrNotAdmitted, repo, strings.TrimPrefix(he.Msg, "github run: "))
	}
	return fmt.Errorf("coding: could not ask GitHub whether this run may work on %s: %w", repo, err)
}

// narrates reports whether a run posts its own progress: when it is handed a
// message to carry on in, or when no turn is following it into the turn's own.
func narrates(to client.Reply, ts string) bool { return ts != "" || to.Turn == "" }

// paying states, on the run's context, what the request that started it is paid
// by: its signed billing claim and the name the boundary minted, the two inputs
// account.Payer reads beside the org (cloud.Detach carries the same pair). The
// run context states only the tenant, and a sandbox the run leases over the
// plane is paid by principal.PayerFrom from the stated caller. Without the claim
// an org owner's run paid from a personal wallet while their requests paid from
// the org's pool; without the name a person's run paid from <org>/<sub>, their
// IAM sub where their username belongs, a wallet nothing funds, and was refused
// while the wallet their requests pay from held money.
func paying(run, req context.Context) context.Context {
	from := zip.CallerOf(req)
	c := zip.CallerOf(run)
	c.Name = strings.Clone(strings.TrimSpace(from.Name))
	c.Account = strings.Clone(strings.TrimSpace(from.Account))
	return zip.WithCaller(run, c)
}

// runContext is the context ONE coding run executes on: detached from the
// endpoint's request, carrying the tenant it acts for, bounded by the run
// budget.
//
// It takes an org and a budget and NOTHING ELSE — deliberately. A ctx parameter
// here would be an invitation to pass the endpoint's, which both cancels the run
// when the endpoint answers and silently discards the tenant. The signature is
// the guard; bridgeRunContext is the same shape for the same reason.
//
// The run is held to the time it asked for by a clock (extend.go), which a caller
// may move later while the run works, as far as reach: the context's own deadline
// is that far, because a plane call reads its read deadline off it.
func runContext(org string, timeoutSeconds int, reach time.Duration) (context.Context, context.CancelFunc, *clock) {
	soft := budget(timeoutSeconds)
	ctx, ends := context.WithTimeoutCause(cloud.For(context.Background(), org), max(soft, reach), errTimeUp)
	ctx, stop := context.WithCancelCause(ctx)
	k := newClock(time.Now(), soft, reach, stop)
	return ctx, func() {
		k.end()
		stop(nil)
		ends()
	}, k
}

// ---- the bounded pool ------------------------------------------------------

// ---- config (env, read at call time — operator-injected from KMS) ----------

// budget is how long ONE run may take: the caller's timeout, else CODING_TIMEOUT_SEC, else as
// long as any run may, and never more than that (ceiling).
//
// A real run clones, drives a model edit loop, runs tests and pushes, and a migration takes
// days; a run that outlasts its budget keeps its work and a follow-up (`after`) carries the
// task on. What bounds a run is the ceiling and nothing else: its model credential is renewed
// for as long as the run lasts (key.go), its push access is renewed when it is used (github.go
// ensureToken) or does not expire (forge.go), and a paid run keeps its sandbox for as long as
// its wallet covers the next hour (apps/sandbox reap.go). Without a bound a caller could hold a
// pool slot — one of two its org has — for ever.
func budget(timeoutSeconds int) time.Duration {
	asked := ceiling()
	switch {
	case timeoutSeconds > 0:
		asked = time.Duration(timeoutSeconds) * time.Second
	default:
		if v, err := strconv.Atoi(env.Or("CODING_TIMEOUT_SEC", "")); err == nil && v > 0 {
			asked = time.Duration(v) * time.Second
		}
	}
	return min(asked, ceiling())
}

// issueChannel is the Slack channel an org's issue runs narrate in when their
// issue has no thread: CODING_ISSUE_CHANNEL, as `org:channel` pairs separated
// by commas ("hanzo:C0123ABCD"). An org it does not name has none.
func issueChannel(org string) client.Reply {
	for _, pair := range strings.Split(env.Or("CODING_ISSUE_CHANNEL", ""), ",") {
		o, channel, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if ok && channel != "" && strings.EqualFold(o, org) {
			return client.Reply{Provider: slack, Channel: channel}
		}
	}
	return client.Reply{}
}

func agentRefOr(ref string) string {
	if r := strings.TrimSpace(ref); r != "" {
		return r
	}
	return "hanzo"
}
