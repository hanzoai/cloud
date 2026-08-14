package integrations

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/shorten"
	"github.com/hanzoai/cloud/plane"
)

// slack_coding.go turns the @hanzo Slack front-door into an ENGINEER: a message
// `@hanzo code: <repo> <task>` (or `/hanzo code: <repo> <task>`) branches off the
// chat reply path (slack_events.go → the shared channel brain) into a durable coding
// run — a fresh agent works a NATIVE /v1/git repo in a sandbox, pushes a branch,
// opens a native PR work item, and reports back IN THE SAME THREAD. Everything
// that is NOT the `code:` trigger stays on the existing chat path, unchanged.
//
// TRIGGER (exactly one, documented): the prompt (after the leading @mention is
// stripped) begins, case-insensitively, with `code:`. The first whitespace token
// after it is the target repo; the rest is the task. No repo => a usage reply,
// fail-closed (we never guess a repo).
//
// OPTIONAL MACHINE ROUTING (#48): `code: <repo> on <machine> <task>` routes the run
// to a registered run-target (a `hanzo code --serve` daemon) instead of the cloud
// sandbox. `<machine>` is a target id or its friendly label, resolved org-scoped;
// an unknown/foreign machine is an HONEST error, never a silent local fallback. The
// bare `on` keyword must be the token immediately after the repo to route — a task
// that merely contains "on" later is untouched.
//
// ISOLATION: the run executes strictly in the caller's org (resolved server-side
// from the Slack-verified team_id, exactly like the chat path). The sandbox is
// pointed only at THIS org's clone URL and handed a credential IAM-scoped to this
// org; the session + PR are org-scoped; the result card posts back only via THIS
// org's bot token to the originating thread.

const (
	// codingDispatchTimeout bounds the ADMISSION call, not the run. Handing a run
	// to the engine is a validate + credential read + session open; anything
	// slower than this is a wedged peer, and the user gets an honest ack instead
	// of a webhook that hangs.
	codingDispatchTimeout = 30 * time.Second
)

// There is no per-org agent git credential any more, and the constants that
// named one are gone rather than left unused.
//
// They pointed at /orgs/{org}/integrations/agent/git-token, where an operator
// sealed the org's `sk-` key. IAM resolves such a key to a user, so cloud minted
// a full org principal from it and the process running untrusted model output
// held something that opened /v1/kms/secrets and every other org-scoped API. A
// run now gets a push GRANT from the forge instead (apps/git/grant.go), which
// authenticates nobody and opens one ref in one repository.
//
// Deleted, not deprecated: a constant naming a secret is an instruction to seal
// one, and the whole point is that there is nothing left to seal.

// codingRepoRE mirrors the git repo name rule (clients/git nameRE): a safe
// identifier, so a hostile "repo" token can never smuggle a path or a second org.
var codingRepoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// after the `code:` trigger. Pure.
func codingIntent(text string) (rest string, ok bool) {
	t := strings.TrimSpace(text)
	if len(t) < len("code:") {
		return "", false
	}
	if !strings.EqualFold(t[:len("code:")], "code:") {
		return "", false
	}
	return strings.TrimSpace(t[len("code:"):]), true
}

// parseCoding splits `<repo> [on <machine>] <task...>` — the first whitespace token
// is the repo (validated); if the NEXT token is exactly `on`, the token after it is
// the run-target reference (id or label) and the remainder is the task; otherwise
// the remainder from the repo is the task and target is empty. ok is false when the
// repo is missing/invalid, the `on` form names no machine, or the task is empty.
// Pure — target RESOLUTION (org-scoped) is the caller's job.
func parseCoding(rest string) (repo, target, task string, ok bool) {
	repo, rem, ok := firstToken(strings.TrimSpace(rest))
	if !ok || !codingRepoRE.MatchString(repo) {
		return "", "", "", false
	}
	// Optional `on <machine>` routing prefix — only when `on` is the very next token.
	if tok, after, hasTok := firstToken(rem); hasTok && strings.EqualFold(tok, "on") {
		machine, taskRem, hasMachine := firstToken(after)
		if !hasMachine || strings.TrimSpace(taskRem) == "" {
			return "", "", "", false // `on` with no machine, or no task after it
		}
		return repo, machine, strings.TrimSpace(taskRem), true
	}
	if strings.TrimSpace(rem) == "" {
		return "", "", "", false // repo only, no task
	}
	return repo, "", strings.TrimSpace(rem), true
}

// firstToken splits s into its first whitespace-delimited token and the remainder.
// ok is false only when s has no token. Pure.
func firstToken(s string) (token, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t\n")
	if s == "" {
		return "", "", false
	}
	i := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
	if i < 0 {
		return s, "", true
	}
	return s[:i], strings.TrimSpace(s[i+1:]), true
}

const codingUsage = "To start a coding task: `@hanzo code: <repo> <what to do>` — name a native git repo and the change. Add `on <machine>` to run it on one of your linked machines."

// handleSlackCoding runs the @mention/DM coding path for a PRE-RESOLVED org: it
// resolves the caller's linked identity, parses the request, acks in-thread, and
// starts the detached run. The bot token is already fetched by the caller (the
// reply sink). Every returned path posts exactly one user-visible message.
func handleSlackCoding(s *cloud.Service[state], ctx context.Context, org, botToken, teamID, channel, threadTS, slackUser, codingText string) {
	link, linked, err := getSlackUserLink(s, org, slackUser)
	if err != nil {
		s.Log.Warn("slack coding: user link lookup", "team", teamID, "err", err)
		_ = slackPostThread(ctx, botToken, channel, threadTS, "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly.")
		return
	}
	if !linked {
		if u, serr := slackLinkURL(s, teamID, slackUser); serr == nil {
			_ = slackPostEphemeral(ctx, botToken, channel, slackUser, "Connect your Hanzo account to use @hanzo: "+u)
		} else {
			_ = slackPostEphemeral(ctx, botToken, channel, slackUser, "Connect your Hanzo account to use @hanzo.")
		}
		return
	}
	repo, target, task, ok := parseCoding(codingText)
	if !ok {
		_ = slackPostThread(ctx, botToken, channel, threadTS, codingUsage)
		return
	}
	targetID, terr := resolveCodingTarget(ctx, org, target)
	if terr != nil {
		_ = slackPostThread(ctx, botToken, channel, threadTS, terr.Error())
		return
	}
	ack, started := startCodingJob(s, org, link.Subject, channel, threadTS, repo, task, targetID)
	_ = slackPostThread(ctx, botToken, channel, threadTS, ack)
	_ = started
}

// resolveCodingTarget turns the parsed machine reference into a target id, org-
// scoped and fail-closed: an empty reference is the ordinary sandbox run (""), a
// non-empty reference that resolves to no target in THIS org is an honest error
// (never a silent local fallback, never another tenant's machine). The returned
// error's message is the user-facing text.
//
// It ASKS AGENTS over the plane rather than calling agents.ResolveTarget, which
// reads that package's `mounted` global. A plugin is a process: the global is nil
// here, so the direct call answered "not mounted" for every `on <machine>` and
// every routed run died at the parse step — the same failure the chat turn had
// until it moved onto the plane. The org still comes from THIS side, resolved
// from the Slack-verified team_id, and is never a field the human can set.
func resolveCodingTarget(_ context.Context, org, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil // no `on <machine>` — the ordinary cloud-sandbox run
	}
	// A DETACHED, TENANT-STATED context, and the inbound ctx is deliberately
	// ignored. This lookup used to ride the webhook's request context, where
	// cloud.For is a silent no-op (zip reads a stated caller only where there is
	// no request) — so the call left with no org and agents refused it, which
	// read back to the user as "no linked machine named X" for machines that
	// existed. The parameter is kept and unused so the signature still matches
	// every caller; naming it _ is what makes passing a request ctx harmless.
	ctx, cancel := codingCallContext(org)
	defer cancel()
	t, err := plane.Ask[plane.TargetRefIn, plane.TargetRef](ctx, "agents", plane.AgentsResolveTarget,
		&plane.TargetRefIn{Org: org, Ref: ref})
	if err != nil || t == nil || strings.TrimSpace(t.ID) == "" {
		return "", fmt.Errorf("No linked machine named `%s` — check `hanzo code --serve` is running and the name is right.", slackEscape(ref))
	}
	return t.ID, nil
}

// handleSlackSlashCoding runs the slash-command coding path: the ack is delivered
// via the (host-pinned) response_url in_channel; the result later posts to the
// slash's channel via the org bot token (which we resolve here, since a
// long-running result outlives the short-lived response_url).
func handleSlackSlashCoding(s *cloud.Service[state], ctx context.Context, org, teamID, channel, slackUser, codingText, responseURL string) {
	link, linked, err := getSlackUserLink(s, org, slackUser)
	if err != nil {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly.")
		return
	}
	if !linked {
		if u, serr := slackLinkURL(s, teamID, slackUser); serr == nil {
			_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "Connect your Hanzo account to use @hanzo: "+u)
		} else {
			_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "Connect your Hanzo account to use @hanzo.")
		}
		return
	}
	repo, target, task, ok := parseCoding(codingText)
	if !ok {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", codingUsage)
		return
	}
	targetID, rerr := resolveCodingTarget(ctx, org, target)
	if rerr != nil {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", rerr.Error())
		return
	}
	// The slash path posts its result to the channel, not the (short-lived)
	// response_url, and it no longer needs to fetch the bot token to do it: the
	// engine reports through the org's own send door, which owns the token.
	ack, _ := startCodingJob(s, org, link.Subject, channel, "", repo, task, targetID)
	_ = slackPostResponseURL(ctx, responseURL, "in_channel", ack)
}

// startCodingJob hands one run to the ENGINE and returns the ack to post.
//
// It no longer runs the job. It used to assemble a Dispatcher in THIS process
// and drive the whole 25-minute orchestration from the chat surface, which made
// the chat process a second coding engine — its own pool, its own in-flight set,
// its own copy of the rules — and left a run started from Slack with no address
// the app could name. Now the engine lives in one process (agents, which holds
// the session store, the durable engine and the routed mailbox) and this is what
// an adapter should be: parse, authorize, dispatch, reply.
//
// THE CREDENTIAL NO LONGER PASSES THROUGH HERE. The engine reads it from KMS at
// the moment it dispatches a sandbox. A token with write access to every repo in
// the org used to be fetched by this function and carried down through three
// packages that had no use for it; deleting that is the single largest reduction
// in the secret's custody in this change.
func startCodingJob(s *cloud.Service[state], org, userSub, channel, threadTS, repo, task, targetID string) (ack string, started bool) {
	// Clone the fiber-buffer-derived strings: they are subslices of a reused
	// request buffer and the plane call outlives the handler that produced them.
	org, userSub = strings.Clone(org), strings.Clone(userSub)
	channel, threadTS = strings.Clone(channel), strings.Clone(threadTS)
	repo, task, targetID = strings.Clone(repo), strings.Clone(task), strings.Clone(targetID)

	// STATE THE TENANT, ON A CONTEXT WITH NO REQUEST BEHIND IT. Both halves are
	// load-bearing and this is the exact pairing whose absence made every coding
	// run fail: the engine's balance gate, session store, git reads and todo
	// write all authorize on the CALLER's org, and a stated caller is only
	// readable off a detached context.
	ctx, cancel := codingCallContext(org)
	defer cancel()

	out, err := plane.Ask[plane.CodingStartIn, plane.CodingStarted](ctx, "agents", plane.CodingStart,
		&plane.CodingStartIn{
			Subject: userSub, Repo: repo, Prompt: task, AgentRef: slackAgentRef(),
			TargetID: targetID, ReplyChannel: channel, ReplyThread: threadTS,
		})
	if err != nil {
		// Never logs the task or a token; the org and repo are enough to find the run.
		s.Log.Warn("slack coding: dispatch", "org", org, "repo", repo, "err", err)
		return codingDispatchAck(err), false
	}
	if out == nil {
		return "Sorry \u2014 I couldn't start that coding task. Please try again shortly.", false
	}
	if out.Routed {
		return "\U0001f6f0\ufe0f On it \u2014 routing `" + slackEscape(repo) + "` to your machine `" + slackEscape(out.TargetID) + "`. I'll report in this thread.", true
	}
	return "\U0001f6e0\ufe0f On it \u2014 working `" + slackEscape(repo) + "` on `" + slackEscape(out.Branch) + "`. I'll report in this thread.", true
}

// codingDispatchAck turns the engine's refusal into the one sentence the user
// should read. Capacity is separated because it is the only one worth retrying,
// and an unprovisioned credential is separated because it is the one an admin
// can actually fix.
func codingDispatchAck(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "at capacity"):
		return "I'm at capacity on coding tasks right now \u2014 please try again in a few minutes."
	case strings.Contains(msg, "agent git credential"):
		return "This workspace has no coding-agent git credential provisioned yet. Ask an admin to seal one in KMS."
	}
	return "Sorry \u2014 I couldn't start that coding task. Please try again shortly."
}

// codingCallContext is the context a coding DISPATCH travels on: detached from
// the webhook request, carrying the tenant, bounded by a short budget because
// dispatch is an admission and not the run.
//
// It takes an org and nothing else, for the same reason channelRunContext does: a
// ctx parameter is an invitation to pass the webhook's, and on the webhook's ctx
// the statement is silently discarded and the call leaves with no tenant at all.
// The signature is the guard.
func codingCallContext(org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cloud.For(context.Background(), org), codingDispatchTimeout)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return shorten.To(s, n) + "…"
	}
	return s
}

// ── Block Kit builders (the integrations Slack plane's own; git/notify.go has a
// private twin it cannot export across the package boundary) ──────────────────

// slackEscape neutralizes the three mrkdwn-meaningful characters (&, <, >) so
// agent/user-derived content (a branch, a diffstat, an error) can never inject a
// link or a <!channel> broadcast. & first so the entities aren't double-escaped.
func slackEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// shortSHA abbreviates a commit hash to git's 7-char convention.
