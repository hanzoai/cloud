package git

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
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

// notifyQ is this reactor's durable queue on the ONE engine (see reactor.go).
var notifyQ = &queue{
	name: "git-notify",
	wf:   NotifyWorkflow,
	acts: []any{NotifyTargetsActivity, NotifyPostActivity},
}

// notifiable reports whether a lifecycle kind is delivered to Slack at all.
// BuildStarted is not a notify kind.
func notifiable(kind cloud.LifecycleKind) bool {
	switch kind {
	case cloud.LifecyclePushLanded, cloud.LifecycleDeployLive, cloud.LifecycleDeployFailed:
		return true
	}
	return false
}

// notifyLifecycle ENQUEUES durable delivery of a lifecycle event to every channel
// subscribed to its repo, and delivers inline when the engine is not ready.
//
// Delivery is durable because it used to be inline: a deploy landing during a rolling
// upgrade lost its Slack notice with nothing to retry it. Now a worker drains the
// queue with retry/backoff, so the notice survives the producer's restart.
//
// Fail-soft (the ai durable-ingest contract): before the engine is wired — or for a
// fact carrying no discriminator to key idempotently on — delivery happens inline on
// this goroutine. Notify is therefore never dark; the engine only upgrades it from
// best-effort-inline to durable-retryable.
func notifyLifecycle(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	if !notifiable(ev.Kind) || ev.Repo == "" {
		return
	}
	id := notifyID(ev)
	if id == "" {
		deliverInline(s, ctx, ev)
		return
	}
	if err := notifyQ.start(ctx, id, ev); err != nil {
		deliverInline(s, ctx, ev)
	}
}

// notifyID keys one delivery on the FACT — org, repo, kind, and the fact's own
// discriminator (the pushed commit, else the deployment id). ExecuteWorkflow is
// idempotent on the id, so a redelivered event is delivered once, not twice.
//
// Empty when the event carries neither: such a fact cannot be deduped, and reusing a
// discriminator-free id would collapse two DISTINCT events into one execution and drop
// a notice. The caller delivers those inline instead.
func notifyID(ev cloud.LifecycleEvent) string {
	d := strings.TrimSpace(ev.After)
	if d == "" {
		d = strings.TrimSpace(ev.DeployID)
	}
	if d == "" {
		return ""
	}
	return "git.notify:" + ev.Org + ":" + ev.Repo + ":" + string(ev.Kind) + ":" + d
}

// NotifyWorkflow delivers one lifecycle fact to every subscribed channel: one activity
// resolves the targets, then ONE activity per channel.
//
// Per-channel activities are what make retry safe. Durable execution replays a
// completed activity from history instead of re-running it, so a retry never re-posts
// to a channel that already received the message — the duplicate-delivery hazard of
// retrying one activity that loops over every channel.
//
// A channel whose retries are exhausted does not withhold the others: the pre-durable
// behaviour was that a failed post is logged and never fatal, and that is preserved.
// Exported for worker registration; not called directly.
func NotifyWorkflow(ctx workflow.Context, ev cloud.LifecycleEvent) error {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    4,
		},
	})
	var targets []string
	if err := workflow.ExecuteActivity(actCtx, NotifyTargetsActivity, ev).Get(actCtx, &targets); err != nil {
		return err
	}
	for _, channel := range targets {
		// Sequential: a repo's channel list is small, and one channel's failure is
		// isolated by its own retry policy. The activity logs the cause.
		_ = workflow.ExecuteActivity(actCtx, NotifyPostActivity,
			notifyPost{Event: ev, Channel: channel}).Get(actCtx, nil)
	}
	return nil
}

// notifyPost is one durable delivery: the fact, and the single channel it goes to.
type notifyPost struct {
	Event   cloud.LifecycleEvent `json:"event"`
	Channel string               `json:"channel"`
}

// NotifyTargetsActivity resolves the channels subscribed to the event's repo that opted
// into its kind. It is an activity because it reads a store — a workflow must stay
// deterministic on replay, so the resolved list is recorded in history instead.
// Exported for worker registration; not called directly.
func NotifyTargetsActivity(ctx context.Context, ev cloud.LifecycleEvent) ([]string, error) {
	return notifyTargets(mounted.Load(), ctx, ev)
}

// NotifyPostActivity renders and posts one message to one channel. Exported for worker
// registration; not called directly.
func NotifyPostActivity(ctx context.Context, in notifyPost) error {
	s := mounted.Load()
	if s == nil {
		return nil
	}
	summary, blocks := lifecycleMessage(s, ctx, in.Event)
	if err := slackNotify(ctx, in.Event.Org, in.Channel, summary, blocks); err != nil {
		s.Log.Warn("git notify: slack post failed", "org", in.Event.Org, "repo", in.Event.Repo,
			"channel", in.Channel, "kind", in.Event.Kind, "err", err)
		return err
	}
	return nil
}

// notifyTargets lists the channels subscribed to ev's repo that opted into ev.Kind.
// The ONE place the subscription read + kind filter live, shared by the durable
// activity and the inline fallback.
func notifyTargets(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	store, err := storeFor(s, ev.Org)
	if err != nil {
		s.Log.Warn("git notify: open store", "org", ev.Org, "err", err)
		return nil, err
	}
	subs, err := store.ListSubscriptions(ctx, ev.Org, ev.Project, ev.Repo)
	if err != nil {
		s.Log.Warn("git notify: list subscriptions", "org", ev.Org, "repo", ev.Repo, "err", err)
		return nil, err
	}
	out := make([]string, 0, len(subs))
	for _, sub := range subs {
		if subscribedTo(sub.Events, ev.Kind) {
			out = append(out, sub.Channel)
		}
	}
	return out, nil
}

// deliverInline posts to every subscribed channel on the caller's goroutine — the
// fail-soft path when the engine is not wired. Best-effort per channel, exactly as
// delivery behaved before it became durable.
func deliverInline(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	targets, err := notifyTargets(s, ctx, ev)
	if err != nil || len(targets) == 0 {
		return
	}
	summary, blocks := lifecycleMessage(s, ctx, ev)
	for _, channel := range targets {
		if err := slackNotify(ctx, ev.Org, channel, summary, blocks); err != nil {
			s.Log.Warn("git notify: slack post failed",
				"org", ev.Org, "repo", ev.Repo, "channel", channel, "kind", ev.Kind, "err", err)
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
// the rich body. EVERY user-derived value that lands in a mrkdwn string is
// slackEscape'd — the org/repo (a deploy event's repo derives from an unconstrained
// RepoURL), the branch (the receive-pack fire path applies no branchRE, and git
// refnames legitimately allow < > & !), the pusher, the commit subject, and the
// deploy detail — so none can smuggle a <!channel> broadcast or a disguised link.
// The clone URL in the link is server-built from validated identifiers, so it is
// used verbatim as the link target.
func lifecycleMessage(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) (string, []any) {
	repo := slackEscape(ev.Org + "/" + ev.Repo)
	link := cloneURL(s, ev.Org, ev.Repo)

	var emoji, title, summary string
	var fields []any
	switch ev.Kind {
	case cloud.LifecyclePushLanded:
		emoji, title = ":arrow_up:", "Push"
		summary = "Push to " + repo + " (" + slackEscape(ev.Branch) + ")"
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
		mrkdwnField("*Repository*\n" + slackEscape(ev.Org+"/"+ev.Repo)),
		mrkdwnField("*Branch*\n" + slackEscape(ev.Branch)),
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
		mrkdwnField("*Repository*\n" + slackEscape(ev.Org+"/"+ev.Repo)),
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
