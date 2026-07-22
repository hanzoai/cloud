package integrations

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/coding"
)

// slack_coding.go turns the @hanzo Slack front-door into an ENGINEER: a message
// `@hanzo code: <repo> <task>` (or `/hanzo code: <repo> <task>`) branches off the
// chat reply path (slack_events.go → the shared bridge brain) into a durable coding
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
	// codingTaskTimeout bounds one detached coding run end to end. It is far longer
	// than a chat turn (bridgeAgentTimeout) because a real coding run clones, runs a
	// model-driven edit loop, and pushes. Overridable via SLACK_CODING_TIMEOUT_SEC.
	codingTaskDefaultTimeout = 25 * time.Minute
	// codingDefaultConcurrency / codingDefaultOrgConcurrency bound simultaneous
	// coding runs (heavy: a sandbox each) across all orgs and per org, so a
	// workspace insider cannot exhaust sandbox capacity. Overridable via
	// SLACK_CODING_CONCURRENCY / SLACK_CODING_ORG_CONCURRENCY.
	codingDefaultConcurrency    = 8
	codingDefaultOrgConcurrency = 2
	// agentCredProvider / agentCredToken / agentCredUser name the per-org agent git
	// credential in the integrations KMS namespace (/orgs/{org}/integrations/agent).
	// An operator/automation seals the org's agent hk- key there; the coding path
	// reads it fail-closed and never logs it.
	agentCredProvider = "agent"
	agentCredToken    = "git-token"
	agentCredUser     = "git-user"
	defaultAgentUser  = "x-access-token"
)

// codingRepoRE mirrors the git repo name rule (clients/git nameRE): a safe
// identifier, so a hostile "repo" token can never smuggle a path or a second org.
var codingRepoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// codingLim bounds detached coding runs (global + per-org), separate from the
// shared chat-turn pool (bridgeLim) because a coding run is long-lived. Initialized
// once in slackBridgeReady.
var codingLim *orgLimiter

// codingDispatcher is the assembled coding orchestrator, injected by the
// composition root (which alone can import clients/git for the CloneURL/VerifyRef
// seams). Zero until SetCodingDispatcher runs; codingConfigured gates use.
var (
	codingDispatcher coding.Dispatcher
	codingConfigured bool
)

// SetCodingDispatcher injects the coding orchestrator. Called once at wiring time
// by the composition root; production never reassigns it.
func SetCodingDispatcher(d coding.Dispatcher) {
	codingDispatcher = d
	codingConfigured = true
}

// codingIntent reports whether a prompt is a coding request and returns the text
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
	ack, started := startCodingJob(s, org, link.Subject, botToken, channel, threadTS, repo, task, targetID)
	_ = slackPostThread(ctx, botToken, channel, threadTS, ack)
	_ = started
}

// resolveCodingTarget turns the parsed machine reference into a target id, org-
// scoped and fail-closed: an empty reference is the ordinary sandbox run (""), a
// non-empty reference that resolves to no target in THIS org is an honest error
// (never a silent local fallback, never another tenant's machine). The returned
// error's message is the user-facing text.
func resolveCodingTarget(ctx context.Context, org, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil // no `on <machine>` — the ordinary cloud-sandbox run
	}
	t, err := agents.ResolveTarget(ctx, org, ref)
	if err != nil {
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
	tok, terr := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if terr != nil {
		s.Log.Warn("slack coding: bot token fetch", "team", teamID, "err", terr)
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "Sorry — I couldn't post back to this channel. Please try again shortly.")
		return
	}
	ack, _ := startCodingJob(s, org, link.Subject, strings.TrimSpace(string(tok)), channel, "", repo, task, targetID)
	_ = slackPostResponseURL(ctx, responseURL, "in_channel", ack)
}

// startCodingJob resolves the org's agent credential (fail-closed), acquires a
// coding slot, and spawns the DETACHED run (own long timeout + panic recovery).
// It returns the ack to post and whether a run actually started. A missing
// dispatcher, a missing credential, or a full pool each returns a specific ack
// and started=false — never a silent drop.
//
// A ROUTED run (targetID != "") needs NO org credential: the machine authenticates
// git with its own already-held credential, so the KMS agent-credential fetch is
// skipped and a workspace without one can still route to a linked machine.
func startCodingJob(s *cloud.Service[state], org, userSub, botToken, channel, threadTS, repo, task, targetID string) (ack string, started bool) {
	if !codingConfigured {
		return "Coding tasks aren't enabled on this deployment yet.", false
	}
	var user, token string
	if strings.TrimSpace(targetID) == "" {
		var err error
		user, token, err = agentGitCredential(s, context.Background(), org)
		if err != nil {
			s.Log.Warn("slack coding: agent credential", "org", org, "err", err) // never logs the token
			return "This workspace has no coding-agent git credential provisioned yet. Ask an admin to seal one in KMS.", false
		}
	}
	if codingLim == nil || !codingLim.acquire(org) {
		return "I'm at capacity on coding tasks right now — please try again in a few minutes.", false
	}
	// Clone the fiber-buffer-derived strings before the detached goroutine outlives
	// the request (they are subslices of reused request buffers).
	org, userSub = strings.Clone(org), strings.Clone(userSub)
	botToken, channel, threadTS = strings.Clone(botToken), strings.Clone(channel), strings.Clone(threadTS)
	repo, task, targetID = strings.Clone(repo), strings.Clone(task), strings.Clone(targetID)
	req := coding.Req{
		Org: org, UserID: userSub, AgentRef: slackAgentRef(), Repo: repo,
		Prompt: task, CredUser: user, CredToken: token,
		TimeoutSeconds: int(codingTaskTimeout().Seconds()),
		TargetID:       targetID,
	}
	go func() {
		defer codingLim.release(org)
		defer func() {
			if r := recover(); r != nil {
				s.Log.Error("slack coding: run panic (recovered)", "org", org, "repo", repo, "err", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), codingTaskTimeout())
		defer cancel()
		res := codingDispatcher.Run(ctx, req)
		summary, blocks := codingResultCard(s.Brand, org, repo, res)
		if perr := PostSlackBlocksThread(ctx, botToken, channel, threadTS, summary, blocks); perr != nil {
			s.Log.Warn("slack coding: result post", "org", org, "repo", repo, "err", perr)
		}
	}()
	if targetID != "" {
		return "🛰️ On it — routing `" + repo + "` to your machine `" + slackEscape(targetID) + "`. Follow it live in mission-control.", true
	}
	return "🛠️ On it — working `" + repo + "`. I'll post the branch + PR here when the run finishes.", true
}

// agentGitCredential reads the org's agent git credential from KMS, fail-closed:
// unmounted, KMS-down, or an absent/empty token each return an error and NEVER a
// value. The username is a fixed basic-auth label (git ignores it; the token is
// the hk- secret) unless an operator sealed a specific one.
func agentGitCredential(s *cloud.Service[state], ctx context.Context, org string) (user, token string, err error) {
	if !validOrg(org) {
		return "", "", fmt.Errorf("integrations: invalid org")
	}
	if !kmsReady(s) {
		return "", "", fmt.Errorf("integrations: kms not ready")
	}
	tok, err := kmsGet(s, kmsPath(org, agentCredProvider), agentCredToken)
	if err != nil {
		return "", "", err
	}
	token = strings.TrimSpace(string(tok))
	if token == "" {
		return "", "", fmt.Errorf("integrations: empty agent credential")
	}
	user = defaultAgentUser
	if u, uerr := kmsGet(s, kmsPath(org, agentCredProvider), agentCredUser); uerr == nil {
		if v := strings.TrimSpace(string(u)); v != "" {
			user = v
		}
	}
	return user, token, nil
}

// codingResultCard renders a coding run's Result as (fallback summary, Block Kit
// blocks): repo, branch, tracker PR key, session link, and pass/fail. Every
// user/agent-derived value is slackEscape'd (reusing the git-notify escaper) so a
// diffstat, branch, or error can never inject Slack mrkdwn. Pure.
func codingResultCard(brand, org, repo string, res coding.Result) (string, []any) {
	repoLabel := slackEscape(org + "/" + repo)
	// A ROUTED run is ACCEPTED here, not finished: the machine drives it to terminal
	// and streams into the session, so the card reports "queued on <machine>, follow
	// it live" rather than a premature pass/fail. A routed run that failed to enqueue
	// (Routed && !OK) still falls through to the failure card below.
	if res.Routed && res.OK {
		machine := slackEscape(res.TargetID)
		summary := "Routed " + repoLabel + " to " + machine
		blocks := []any{
			mrkdwnSection(":satellite: *Routed to a machine* — " + repoLabel),
			map[string]any{"type": "section", "fields": []any{
				mrkdwnField("*Repository*\n" + repoLabel),
				mrkdwnField("*Machine*\n`" + machine + "`"),
			}},
		}
		if res.SessionID != "" {
			blocks = append(blocks, map[string]any{
				"type":     "context",
				"elements": []any{map[string]any{"type": "mrkdwn", "text": "Follow it live · session `" + slackEscape(res.SessionID) + "` · " + brandLabel(brand) + " coding"}},
			})
		}
		return summary, blocks
	}
	var emoji, title, summary string
	switch {
	case !res.OK:
		emoji, title = ":x:", "Coding task failed"
		summary = "Coding task failed: " + repoLabel
	case !res.Changed:
		emoji, title = ":white_check_mark:", "No changes needed"
		summary = "No changes needed: " + repoLabel
	default:
		emoji, title = ":sparkles:", "Branch pushed"
		summary = "Branch pushed to " + repoLabel + " (" + slackEscape(res.Branch) + ")"
	}

	header := mrkdwnSection(emoji + " *" + title + "* — " + repoLabel)
	fields := []any{
		mrkdwnField("*Repository*\n" + repoLabel),
		mrkdwnField("*Branch*\n" + codeOrDash(res.Branch)),
	}
	if res.OK && res.Changed {
		pr := "—"
		if res.PR.Identifier != "" {
			pr = "`" + slackEscape(res.PR.Identifier) + "`"
		}
		commit := codeOrDash(shortSHA(res.CommitSha))
		fields = append(fields,
			mrkdwnField("*PR*\n"+pr),
			mrkdwnField("*Commit*\n"+commit),
		)
	}
	if !res.OK && res.Error != "" {
		fields = append(fields, mrkdwnField("*Error*\n"+slackEscape(truncate(res.Error, 300))))
	}
	blocks := []any{header, map[string]any{"type": "section", "fields": fields}}

	if strings.TrimSpace(res.Diffstat) != "" && res.OK && res.Changed {
		blocks = append(blocks, mrkdwnSection("*Diff*\n```"+slackEscape(truncate(res.Diffstat, 1200))+"```"))
	}
	if res.SessionID != "" {
		blocks = append(blocks, map[string]any{
			"type":     "context",
			"elements": []any{map[string]any{"type": "mrkdwn", "text": "Session `" + slackEscape(res.SessionID) + "` · " + brandLabel(brand) + " coding"}},
		})
	}
	return summary, blocks
}

func codeOrDash(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	return "`" + slackEscape(s) + "`"
}

func brandLabel(brand string) string {
	brand = strings.TrimSpace(brand)
	if brand == "" {
		return "Hanzo"
	}
	return strings.ToUpper(brand[:1]) + brand[1:]
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
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

func mrkdwnSection(text string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}
}

func mrkdwnField(text string) map[string]any {
	return map[string]any{"type": "mrkdwn", "text": text}
}

// shortSHA abbreviates a commit hash to git's 7-char convention.
func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ── config (env, read at call time — operator-injected from KMS) ──────────────

func codingTaskTimeout() time.Duration {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SLACK_CODING_TIMEOUT_SEC"))); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	return codingTaskDefaultTimeout
}

func codingConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SLACK_CODING_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return codingDefaultConcurrency
}

func codingOrgConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SLACK_CODING_ORG_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return codingDefaultOrgConcurrency
}
