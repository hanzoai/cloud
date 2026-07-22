package sync

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
)

// gitMirrorTokenEnv is the git plane's shared mirror credential (git.mirrorEnvToken):
// the KMS-injected token mirror.go fetches a private source with, sent ONLY to the
// allowlisted source host. The sync provider falls back to it when the per-org GitHub
// App is not connected, so native mirroring uses ONE credential path, not two.
const gitMirrorTokenEnv = "GIT_MIRROR_TOKEN"

// git_provider.go is the FIRST sync provider: GitHub/GitLab ⇆ Hanzo Git (the NATIVE
// /v1/git plane in this same binary). It carries no git logic of its own — Reconcile
// composes the native git object-plane seams (cloud.ImportGitRepo / cloud.InboundGitSync
// / cloud.EnsureGitMirror, which clients/git registers at Mount), so the native git
// store is the ONE git store and no byte transits an external git host:
//
//   - INBOUND (a source push): cloud.InboundGitSync advances the matching branch
//     fast-forward only — a diverged native ref is a Conflict, never overwritten (the
//     split-brain guard, enforced by git itself, native preserved).
//   - RECONCILE (a manual run / initial sync): for a pulling direction, cloud.ImportGitRepo
//     fast-forward mirrors every branch of the upstream INTO native (and, when the sync
//     also pushes, registers the outbound native→upstream mirror so the native push
//     lifecycle propagates every later commit back); for a push-only direction,
//     cloud.EnsureGitMirror declares that outbound target without importing.
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

// Reconcile drives sy's endpoints toward agreement for ev on the native git store,
// returning whether native (the target) changed.
func (gitProvider) Reconcile(ctx context.Context, sy Sync, ev Event) (bool, error) {
	a := resolve(sy, ev)
	if !a.do {
		return false, nil
	}
	owner := sy.Org
	native := normalizeGitName(sy.Target.Locator)
	source := sy.Source.Locator
	tok, err := gitToken(ctx, sy.Source.Provider, sy.Org, ev.Token)
	if err != nil {
		return false, err
	}
	if a.inbound {
		// Advance the pushed branch fast-forward only; a diverged native ref is a
		// Conflict (preserved) and an up-to-date ref is a no-op — both "no change".
		res, err := cloud.InboundGitSync(ctx, cloud.GitInboundReq{
			Org: owner, Repo: native, Branch: ev.Branch,
			CloneURL: source, Token: tok, Origin: hostOf(source),
		})
		if err != nil {
			return false, err
		}
		return res.Applied, nil
	}
	// Manual reconcile toward the direction. Pull/both fast-forward mirror EVERY branch
	// IN (and, when it also pushes, register the outbound native→source mirror so the
	// native push lifecycle propagates later commits back); push-only declares that
	// outbound target without importing. Off would not reach here.
	changed := false
	if dirPulls(sy.Direction) {
		mirrorURL := ""
		if dirPushes(sy.Direction) {
			mirrorURL = source
		}
		if err := cloud.ImportGitRepo(ctx, cloud.GitImportReq{
			Org: owner, Repo: native, CloneURL: source, Token: tok, MirrorURL: mirrorURL,
		}); err != nil {
			return false, fmt.Errorf("import: %w", err)
		}
		changed = true
	} else if dirPushes(sy.Direction) {
		if err := cloud.EnsureGitMirror(ctx, owner, "", native, source, true); err != nil {
			return false, fmt.Errorf("ensure mirror: %w", err)
		}
		changed = true
	}
	return changed, nil
}

// gitToken resolves the credential for a source fetch, in preference order:
//
//   - the webhook-supplied token (already minted for this event), else
//   - for GitHub, the per-org GitHub App installation token (short-lived, scoped to
//     the org's OWN installation) when the App is connected AND its creds are present,
//     else
//   - the git plane's shared mirror credential (GIT_MIRROR_TOKEN), the SAME token
//     mirror.go fetches a private source with — so native mirroring has ONE credential
//     path — else
//   - anonymous ("").
//
// It NEVER hard-fails: a PUBLIC repo needs no credential, so failing the whole
// reconcile because the App is not connected would wrongly freeze the public mirrors
// too. A PRIVATE repo with no available credential simply fails at the git layer
// (logged by the engine) — the honest signal to connect the App or set the mirror
// token, never a leak. GitLab (and any other) has no minted token here, so it fetches
// with whatever the event carried.
func gitToken(ctx context.Context, provider, org, eventToken string) (string, error) {
	if strings.TrimSpace(eventToken) != "" {
		return eventToken, nil
	}
	if strings.EqualFold(provider, provGitHub) {
		if tok, err := integrations.InstallationToken(ctx, org); err == nil && strings.TrimSpace(tok) != "" {
			return tok, nil
		}
		return strings.TrimSpace(os.Getenv(gitMirrorTokenEnv)), nil
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
