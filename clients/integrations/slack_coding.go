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

// parseCoding splits `<repo> <task...>` — the first whitespace token is the repo
// (validated), the rest is the task. ok is false when the repo is missing/invalid
// or the task is empty. Pure.
func parseCoding(rest string) (repo, task string, ok bool) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", false
	}
	i := strings.IndexFunc(rest, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
	if i < 0 {
		return "", "", false // repo only, no task
	}
	repo = strings.TrimSpace(rest[:i])
	task = strings.TrimSpace(rest[i+1:])
	if !codingRepoRE.MatchString(repo) || task == "" {
		return "", "", false
	}
	return repo, task, true
}

const codingUsage = "To start a coding task: `@hanzo code: <repo> <what to do>` — name a native git repo and the change."

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
	repo, task, ok := parseCoding(codingText)
	if !ok {
		_ = slackPostThread(ctx, botToken, channel, threadTS, codingUsage)
		return
	}
	ack, started := startCodingJob(s, org, link.Subject, botToken, channel, threadTS, repo, task)
	_ = slackPostThread(ctx, botToken, channel, threadTS, ack)
	_ = started
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
	repo, task, ok := parseCoding(codingText)
	if !ok {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", codingUsage)
		return
	}
	tok, terr := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if terr != nil {
		s.Log.Warn("slack coding: bot token fetch", "team", teamID, "err", terr)
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "Sorry — I couldn't post back to this channel. Please try again shortly.")
		return
	}
	ack, _ := startCodingJob(s, org, link.Subject, strings.TrimSpace(string(tok)), channel, "", repo, task)
	_ = slackPostResponseURL(ctx, responseURL, "in_channel", ack)
}

// startCodingJob resolves the org's agent credential (fail-closed), acquires a
// coding slot, and spawns the DETACHED run (own long timeout + panic recovery).
// It returns the ack to post and whether a run actually started. A missing
// dispatcher, a missing credential, or a full pool each returns a specific ack
// and started=false — never a silent drop.
func startCodingJob(s *cloud.Service[state], org, userSub, botToken, channel, threadTS, repo, task string) (ack string, started bool) {
	if !codingConfigured {
		return "Coding tasks aren't enabled on this deployment yet.", false
	}
	user, token, err := agentGitCredential(s, context.Background(), org)
	if err != nil {
		s.Log.Warn("slack coding: agent credential", "org", org, "err", err) // never logs the token
		return "This workspace has no coding-agent git credential provisioned yet. Ask an admin to seal one in KMS.", false
	}
	if codingLim == nil || !codingLim.acquire(org) {
		return "I'm at capacity on coding tasks right now — please try again in a few minutes.", false
	}
	// Clone the fiber-buffer-derived strings before the detached goroutine outlives
	// the request (they are subslices of reused request buffers).
	org, userSub = strings.Clone(org), strings.Clone(userSub)
	botToken, channel, threadTS = strings.Clone(botToken), strings.Clone(channel), strings.Clone(threadTS)
	repo, task = strings.Clone(repo), strings.Clone(task)
	req := coding.Req{
		Org: org, UserID: userSub, AgentRef: slackAgentRef(), Repo: repo,
		Prompt: task, CredUser: user, CredToken: token,
		TimeoutSeconds: int(codingTaskTimeout().Seconds()),
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
	tok, err := kmsGet(s, org, agentCredProvider, agentCredToken)
	if err != nil {
		return "", "", err
	}
	token = strings.TrimSpace(string(tok))
	if token == "" {
		return "", "", fmt.Errorf("integrations: empty agent credential")
	}
	user = defaultAgentUser
	if u, uerr := kmsGet(s, org, agentCredProvider, agentCredUser); uerr == nil {
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
