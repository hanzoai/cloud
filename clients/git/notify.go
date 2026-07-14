package git

import (
	"bytes"
	"context"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
)

// notify.go is the Slack-notify git-lifecycle subscriber: on a push landing or a
// deploy going live/failing, it posts a Block Kit message to every Slack channel
// subscribed to that repo — GitHub-Slack-app parity. It OWNS no Slack custody: the
// per-org bot token and the chat.postMessage call belong to clients/integrations
// (the OAuth plane that sealed the token into KMS), reached through the ONE
// delivery seam below. Token custody, posting, and org-scoping are never
// re-implemented here.

// slackNotify is the ONE Slack delivery door — resolves the org's KMS-sealed bot
// token and posts a Block Kit message (integrations.NotifySlack). A package var
// ONLY so a test can capture the (org, channel, blocks) a lifecycle event routes to
// without mounting integrations + KMS; production never reassigns it.
var slackNotify = integrations.NotifySlack

// notifyLifecycle delivers a lifecycle event to every channel subscribed to its
// repo. Best-effort: a store or Slack error is logged (never a token) and never
// affects the git path or the other subscriptions. Only the kinds a channel opted
// into (or all, by default) are delivered; BuildStarted is not a notify kind.
func notifyLifecycle(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	switch ev.Kind {
	case cloud.LifecyclePushLanded, cloud.LifecycleDeployLive, cloud.LifecycleDeployFailed:
		// deliverable
	default:
		return
	}
	if ev.Repo == "" {
		return
	}
	store, err := storeFor(s, ev.Org)
	if err != nil {
		s.Log.Warn("git notify: open store", "org", ev.Org, "err", err)
		return
	}
	subs, err := store.ListSubscriptions(ctx, ev.Org, ev.Project, ev.Repo)
	if err != nil {
		s.Log.Warn("git notify: list subscriptions", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	summary, blocks := lifecycleMessage(s, ctx, ev)
	for _, sub := range subs {
		if !subscribedTo(sub.Events, ev.Kind) {
			continue
		}
		if err := slackNotify(ctx, ev.Org, sub.Channel, summary, blocks); err != nil {
			s.Log.Warn("git notify: slack post failed",
				"org", ev.Org, "repo", ev.Repo, "channel", sub.Channel, "kind", ev.Kind, "err", err)
		}
	}
}

// subscribedTo reports whether a subscription's event filter (a CSV, empty = all)
// includes kind.
func subscribedTo(csv string, kind cloud.LifecycleKind) bool {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return true
	}
	for _, e := range strings.Split(csv, ",") {
		if strings.TrimSpace(e) == string(kind) {
			return true
		}
	}
	return false
}

// lifecycleMessage renders a lifecycle event as (fallback summary, Block Kit
// blocks) in the GitHub-Slack-app house style: a header line, a fielded section,
// and a linked-repo context. summary is the notification/preview text; blocks is
// the rich body. Both are safe to post — every value is either a validated
// identifier or a bounded git-output string.
func lifecycleMessage(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) (string, []any) {
	repo := ev.Org + "/" + ev.Repo
	link := cloneURL(s, ev.Org, ev.Repo)

	var emoji, title, summary string
	var fields []any
	switch ev.Kind {
	case cloud.LifecyclePushLanded:
		emoji, title = ":arrow_up:", "Push"
		summary = "Push to " + repo + " (" + ev.Branch + ")"
		fields = pushFields(s, ctx, ev)
	case cloud.LifecycleDeployLive:
		emoji, title = ":rocket:", "Deploy live"
		summary = "Deploy live: " + repo
		fields = deployFields(ev)
	case cloud.LifecycleDeployFailed:
		emoji, title = ":x:", "Deploy failed"
		summary = "Deploy failed: " + repo
		fields = deployFields(ev)
	}

	header := mrkdwnSection(emoji + " *" + title + "* — <" + link + "|" + repo + ">")
	section := map[string]any{"type": "section", "fields": fields}
	brand := strings.TrimSpace(s.Brand)
	if brand == "" {
		brand = "Hanzo"
	} else {
		brand = strings.ToUpper(brand[:1]) + brand[1:]
	}
	ctxBlock := map[string]any{
		"type":     "context",
		"elements": []any{map[string]any{"type": "mrkdwn", "text": brand + " Git"}},
	}
	return summary, []any{header, section, ctxBlock}
}

// pushFields builds the Repository/Branch/Pushed-by/Commit fields for a push,
// including the commit's subject line (best-effort — a bounded git read).
func pushFields(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) []any {
	// Pusher is gateway-validated but still user-derived — escape it, like the
	// commit subject, so no field can smuggle Slack mrkdwn (links / <!channel>).
	pusher := slackEscape(strings.TrimSpace(ev.Pusher))
	if pusher == "" {
		pusher = "—"
	}
	commit := shortSHA(ev.After)
	if subject := commitSubject(s, ctx, ev); subject != "" {
		commit = "`" + commit + "` " + slackEscape(subject)
	} else if commit != "" {
		commit = "`" + commit + "`"
	}
	return []any{
		mrkdwnField("*Repository*\n" + ev.Org + "/" + ev.Repo),
		mrkdwnField("*Branch*\n" + ev.Branch),
		mrkdwnField("*Pushed by*\n" + pusher),
		mrkdwnField("*Commit*\n" + commit),
	}
}

// deployFields builds the Repository/Status/Deployment/Detail fields for a deploy.
func deployFields(ev cloud.LifecycleEvent) []any {
	status := "live"
	if ev.Kind == cloud.LifecycleDeployFailed {
		status = "failed"
	}
	// Detail carries a build/deploy message that can include a user-derived commit
	// ref or error text — escape it so it can't inject Slack mrkdwn.
	detail := slackEscape(strings.TrimSpace(ev.Detail))
	if detail == "" {
		detail = "—"
	}
	dep := strings.TrimSpace(ev.DeployID)
	if dep == "" {
		dep = "—"
	}
	fields := []any{
		mrkdwnField("*Repository*\n" + ev.Org + "/" + ev.Repo),
		mrkdwnField("*Status*\n" + status),
		mrkdwnField("*Deployment*\n" + dep),
		mrkdwnField("*Detail*\n" + detail),
	}
	return fields
}

// slackEscape neutralizes the three characters that carry meaning in Slack mrkdwn
// text — &, <, > — so user-derived content (a commit subject, a pusher id, a deploy
// detail) can never inject a link, a <!channel> broadcast, or a disguised URL into
// a posted message (Red MED-4). & is escaped first so the &lt;/&gt; entities aren't
// double-escaped.
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

// shortSHA returns the first 7 chars of a commit hash (or the whole thing if
// shorter), matching git's abbreviated-hash convention.
func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// commitSubject reads the one-line commit subject (%s) for the pushed commit from
// the bare repo — a bounded, single-commit ref read (not a pack op, so it is not
// gated by the pack semaphore). Best-effort: any error yields "" and the message
// simply omits the subject.
func commitSubject(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) string {
	if !commitRE.MatchString(ev.After) {
		return ""
	}
	bareDir := s.State.storage.absRepoPath(ev.Org, ev.Project, ev.Repo)
	cmd, err := gitCmd(ctx, nil, "--git-dir="+bareDir, "show", "-s", "--format=%s", ev.After)
	if err != nil {
		return ""
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	subject := strings.TrimSpace(out.String())
	if len(subject) > 120 {
		subject = subject[:117] + "…"
	}
	return subject
}

// commitRE matches a full 40-hex git commit hash — the guard before it is passed
// to `git show` (defense-in-depth; After already comes from git output).
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
