package sync

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
)

// git_provider.go is the FIRST sync provider: GitHub/GitLab ⇆ the FORGE
// (git.hanzo.ai, where this estate's repositories live). It carries no git logic
// of its own — Reconcile composes the git seams (cloud.InboundGitSync /
// cloud.ImportGitRepo / cloud.EnsureGitMirror), which importer.go answers
// against the forge. The forge is the ONE git store, and it is CANONICAL:
//
//   - INBOUND (a source push): cloud.InboundGitSync advances the matching ref
//     fast-forward only — a diverged forge ref is a Conflict, never overwritten
//     (the split-brain guard, enforced by git itself, the forge preserved).
//   - RECONCILE (a manual run / initial sync): for a pulling direction,
//     cloud.ImportGitRepo advances every ref of the upstream INTO the forge and,
//     when the sync also pushes, declares the outbound target and advances the
//     same refs back out to it; for a push-only direction, cloud.EnsureGitMirror
//     declares that target and pushOut advances the forge's refs to it, with
//     nothing coming in.
//
// Nothing this provider drives is ever a force. Both directions are the same
// fast-forward advance with the two ends swapped, so a downstream that has moved
// on is REPORTED rather than overwritten — which matters because a bidirectional
// sync's "downstream" is a place people push.
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
	if ev.Ref == "" || ev.After == "" {
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
	native := fold(sy.Target.Locator)
	source := sy.Source.Locator
	// The ACCOUNT the source belongs to, which is half of which repository this
	// is: a name is unique only within one, and the sync's own source URL is
	// where this row's account is written down.
	account := accountOf(source)
	tok, err := gitToken(ctx, sy.Source.Provider, sy.Org, source, ev.Token)
	if err != nil {
		return false, err
	}
	if a.inbound {
		// Advance the pushed ref fast-forward only; a diverged forge ref is a
		// Conflict (preserved) and an up-to-date ref is a no-op — both "no change".
		res, err := cloud.InboundGitSync(cloud.For(ctx, owner), cloud.GitInboundReq{
			Org: owner, Project: account, Repo: native, Ref: ev.Ref,
			CloneURL: source, Token: tok, Origin: hostOf(source),
		})
		if err != nil {
			return false, err
		}
		return res.Applied, nil
	}
	// Manual reconcile toward the direction. Pull/both fast-forward advances EVERY
	// ref IN (and, when it also pushes, declares the outbound target and advances
	// the same refs back out); push-only declares the target and advances out only.
	// Off would not reach here.
	changed := false
	if dirPulls(sy.Direction) {
		mirrorURL := ""
		if dirPushes(sy.Direction) {
			mirrorURL = source
		}
		// The reconcile runs on a SCHEDULE, so there is no request to forward and
		// the import — which crosses to the git app over the internal plane —
		// would arrive anonymous, where the callee refuses a tenant it cannot see.
		// cloud.For states the org this sync acts for; an inbound request always
		// wins over a stated one, so a job supplies an identity where there is
		// none and can never launder one.
		if err := cloud.ImportGitRepo(cloud.For(ctx, owner), cloud.GitImportReq{
			Org: owner, Project: account, Repo: native,
			CloneURL: source, Token: tok, MirrorURL: mirrorURL,
		}); err != nil {
			return false, fmt.Errorf("import: %w", err)
		}
		changed = true
	} else if dirPushes(sy.Direction) {
		if err := cloud.EnsureGitMirror(ctx, owner, account, native, source, true); err != nil {
			return false, fmt.Errorf("ensure mirror: %w", err)
		}
		// DECLARING A TARGET SENDS NOTHING TO IT. Push-only means nothing comes in,
		// so advancing the forge's refs out IS this direction's whole reconcile —
		// without it a push-only sync recorded an intention and moved no bytes,
		// which is what it did while the pushing lived on a lifecycle in an app
		// that no longer receives one.
		//
		// And it is the whole of it, so its failure is this reconcile's failure:
		// returning "changed" for a push nothing received is what stamps "last
		// synced, just now" on a repository that is not synced at all.
		if err := pushOut(cloud.For(ctx, owner), owner, account, native); err != nil {
			return false, fmt.Errorf("mirror out: %w", err)
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
//   - nothing, which is not the same as anonymous: an empty token leaves a zero
//     gitCred, and the git plane then resolves the credential for the SOURCE'S OWN
//     HOST. That is the one credential path, and naming a second env var here to
//     "share" it was what made it two.
//
// It NEVER hard-fails: a PUBLIC repo needs no credential, so failing the whole
// reconcile because the App is not connected would wrongly freeze the public mirrors
// too. A PRIVATE repo with no available credential simply fails at the git layer
// (logged by the engine) — the honest signal to connect the App or set the mirror
// token, never a leak. GitLab (and any other) has no minted token here, so it fetches
// with whatever the event carried.
func gitToken(ctx context.Context, provider, org, source, eventToken string) (string, error) {
	if strings.TrimSpace(eventToken) != "" {
		return eventToken, nil
	}
	if strings.EqualFold(provider, provGitHub) {
		if tok, err := integrations.InstallationToken(ctx, org, accountOf(source)); err == nil && strings.TrimSpace(tok) != "" {
			return tok, nil
		}
		// NO FALLBACK TOKEN HERE, deliberately. Returning "" leaves the import with a
		// zero gitCred, and the git plane then resolves the credential for the source's
		// OWN HOST (credAuthHeader → mirrorAuthHeader → mirrorCredential). This
		// package used to name a second env var for the same secret and read it whole,
		// which stopped being the same secret the moment that credential became
		// per-host: a deployment setting only the per-host name left this returning ""
		// and a private repo fetching anonymously, while its comment claimed "ONE
		// credential path, not two".
		return "", nil
	}
	return "", nil
}

// gitRepoMatches reports whether ev names this sync's repo. The source repo's short
// name (last path segment of the clone URL, minus .git) is the identity; it equals
// the native target name for a git sync.
func gitRepoMatches(sy Sync, ev Event) bool {
	want := fold(ev.Repo)
	if want == "" {
		want = repoNameFromLocator(ev.Locator)
	}
	if want == "" {
		return false
	}
	return want == repoNameFromLocator(sy.Source.Locator) || want == fold(sy.Target.Locator)
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
	return fold(locator)
}

// fold is the canonical spelling of a name — an org, an account, a repository.
//
// ONE operation, because it is one question: two spellings that differ only in
// case or in surrounding space name the same thing, upstream and on the forge
// alike. Folding some of them and not others is what let ` Hanzo ` and `hanzo`
// take different gates and different store files while addressing one namespace.
func fold(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// hostOf returns the lowercased host of a URL — the loop-prevention Origin stamp the
// outbound mirror matches. "" on a parse miss.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// accountOf reads the ACCOUNT out of a clone URL — the first path segment of
// https://host/<account>/<repo>.git. Empty when the URL names none, which lets
// the single-connection case resolve as before.
//
// It is one function because it answers one question in two places: WHOSE
// installation token to mint, and WHICH repository this is (a name is unique
// only within an account — see [newRepo]).
func accountOf(source string) string {
	u, err := url.Parse(strings.TrimSpace(source))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}
