package sync

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud/clients/integrations"
)

// git_provider.go is the FIRST sync provider: GitHub/GitLab ⇆ Hanzo Git (Gitea). It
// carries no git logic of its own — Reconcile composes Gitea's own primitives (gitea.go),
// so Gitea is the ONE git store and no byte transits the retired cloud embedded server:
//
//   - INBOUND (a source push): gitea.mirrorIn advances the matching branch fast-forward
//     only — a diverged Gitea ref is a conflict, never overwritten (the split-brain
//     guard, now on Gitea).
//   - RECONCILE (a manual run / initial sync): for a pulling direction, gitea.mirrorIn
//     mirrors every branch of the upstream INTO Gitea; for a pushing direction,
//     gitea.ensurePushMirror declares a Gitea push-mirror (sync_on_commit) so Gitea
//     itself propagates every later commit to the upstream — no cloud-side reactor.
//
// The short-lived GitHub App installation token rides IN the event when a webhook
// already minted it; for a manual run the provider mints a fresh one per org. It is
// never stored, never logged (values, not places).

const (
	provGitHub = "github"
	provGitLab = "gitlab"
	provNative = "hanzo-git" // the native side of a git sync's target
)

type gitProvider struct{}

func (gitProvider) Kind() string { return "git" }

// act is the resolved decision for one git event: whether to reconcile, and in
// which mode. It is what resolve returns — the pure, testable core of Reconcile.
type act struct {
	do      bool
	inbound bool // a source push → fast-forward advance native; else a full reconcile
}

// resolve decides what ev means for sy WITHOUT any I/O — the testable core. A push
// from the sync's SOURCE provider that names its repo, when the direction pulls, is
// an inbound advance; a manual run is a full reconcile; anything else is a no-op
// (outbound native pushes are mirror_out's job, never the engine's).
func resolve(sy Sync, ev Event) act {
	if ev.Manual {
		return act{do: sy.Direction != dirOff}
	}
	if !strings.EqualFold(ev.Provider, sy.Source.Provider) {
		return act{}
	}
	if !gitRepoMatches(sy, ev) {
		return act{}
	}
	if !dirPulls(sy.Direction) {
		return act{}
	}
	if ev.Branch == "" || ev.After == "" {
		return act{}
	}
	return act{do: true, inbound: true}
}

// Reconcile drives sy's endpoints toward agreement for ev on Gitea (the one git store),
// returning whether Gitea (the target) changed.
func (gitProvider) Reconcile(ctx context.Context, sy Sync, ev Event) (bool, error) {
	a := resolve(sy, ev)
	if !a.do {
		return false, nil
	}
	gt, err := giteaFromEnv()
	if err != nil {
		return false, err // fail closed when the Gitea store is unconfigured
	}
	owner := sy.Org
	native := normalizeGitName(sy.Target.Locator)
	source := sy.Source.Locator
	tok, err := gitToken(ctx, sy.Source.Provider, sy.Org, ev.Token)
	if err != nil {
		return false, err
	}
	if a.inbound {
		// Advance the pushed branch fast-forward only; a diverged Gitea ref is a
		// conflict (preserved) and an up-to-date ref is a no-op — both "no change".
		return gt.mirrorIn(ctx, owner, native, source, tok, ev.Branch)
	}
	// Manual reconcile toward the direction. Pull/both mirror EVERY branch in; push/both
	// declare the outbound Gitea push-mirror so Gitea propagates later commits. Off would
	// not reach here.
	changed := false
	if dirPulls(sy.Direction) {
		c, err := gt.mirrorIn(ctx, owner, native, source, tok, "")
		if err != nil {
			return false, fmt.Errorf("mirror-in: %w", err)
		}
		changed = c
	}
	if dirPushes(sy.Direction) {
		if err := gt.ensurePushMirror(ctx, owner, native, source, tok); err != nil {
			return false, fmt.Errorf("ensure push-mirror: %w", err)
		}
		changed = true
	}
	return changed, nil
}

// gitToken resolves the credential for a source fetch: the webhook-supplied token
// wins; else a GitHub App installation token is minted per org. GitLab (and any
// other) has no minted token here, so it fetches with whatever the event carried
// (anonymous for a public repo) — fail-closed for a private repo, never a leak.
func gitToken(ctx context.Context, provider, org, eventToken string) (string, error) {
	if strings.TrimSpace(eventToken) != "" {
		return eventToken, nil
	}
	if strings.EqualFold(provider, provGitHub) {
		tok, err := integrations.InstallationToken(ctx, org)
		if err != nil {
			return "", fmt.Errorf("mint github token: %w", err)
		}
		return tok, nil
	}
	return "", nil
}

// gitRepoMatches reports whether ev names this sync's repo. The source repo's short
// name (last path segment of the clone URL, minus .git) is the identity; it equals
// the native target name for a git sync.
func gitRepoMatches(sy Sync, ev Event) bool {
	want := normalizeGitName(ev.Repo)
	if want == "" {
		want = repoNameFromLocator(ev.Locator)
	}
	if want == "" {
		return false
	}
	return want == repoNameFromLocator(sy.Source.Locator) || want == normalizeGitName(sy.Target.Locator)
}

// repoNameFromLocator extracts the short repo name from a clone URL or an
// "<owner>/<repo>" locator (last path segment, trailing .git stripped, lowercased).
func repoNameFromLocator(locator string) string {
	locator = strings.TrimSpace(locator)
	if locator == "" {
		return ""
	}
	if u, err := url.Parse(locator); err == nil && u.Path != "" {
		locator = u.Path
	}
	locator = strings.TrimSuffix(locator, ".git")
	locator = strings.Trim(locator, "/")
	if i := strings.LastIndexByte(locator, '/'); i >= 0 {
		locator = locator[i+1:]
	}
	return normalizeGitName(locator)
}

// normalizeGitName lowercases + trims a repo name (git repo names are case-folded
// in this store, matching the git plane's normalizeName).
func normalizeGitName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// hostOf returns the lowercased host of a URL — the loop-prevention Origin stamp the
// outbound mirror matches. "" on a parse miss.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
