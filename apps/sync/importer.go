package sync

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/forge"
	"github.com/zap-proto/zip"
)

// importer.go implements the cloud.GitImporter and cloud.GitMirrorController
// seams against the FORGE.
//
// Both used to be answered by an embedded git server that held bare
// repositories on this fleet's own disks. The repositories live on the forge
// now, so the two questions those seams ask —
//
//	"bring this upstream in, without ever overwriting what we hold"
//	"and keep these other places in step with it"
//
// — are answered the same way, over the git protocol, against a host rather than
// a directory. What did NOT change is the rule: the forge is canonical and an
// inbound sync may only fast-forward it. advance.go is where that is enforced;
// this file is the plumbing around it — which repository, which refs, which
// credential, and what to remember afterwards.
//
// # What each side knows
//
// The FORGE knows what a repository is: its refs, its default branch, whether it
// exists at all. So "have we imported this?" is a read of the forge, not a row
// in a table that could disagree with it.
//
// The STORE (store.go) knows what we TRIED: when a ref was last advanced and
// whether an upstream was refused for diverging. The forge has nowhere to put
// that — a refused push leaves no trace on the receiving end, which is exactly
// what makes it safe and exactly why somebody has to write it down.
//
// # Two credentials, and neither is stored
//
// The UPSTREAM is reached with the credential the caller already minted for this
// event (a GitHub App installation token, valid for an hour) or, absent one,
// with the token held for that host. The FORGE is reached with the deployment's
// own machine credential, read from KMS per [forge.Source]'s refresh window.
// Both ride an env-injected http.extraHeader and neither is ever written down —
// which is also why the forge's own push-mirror feature is not used here: it
// would require handing the forge a long-lived credential for the upstream to
// keep, and it replicates with --mirror, which is force plus prune. Force is the
// one thing this seam exists to never do.

// nameRE bounds a repository name. It is the retired store's rule and the shape
// the forge accepts as a path segment: no slash, no traversal, no surprise.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// importer is the registered cloud.GitImporter. It carries no state: every
// method resolves the mounted service through the package `mounted` var and
// fails closed when the subsystem is unmounted, exactly like the reconcile func.
type importer struct{}

// mirrorControl is the registered cloud.GitMirrorController.
type mirrorControl struct{}

// ── reaching the forge ───────────────────────────────────────────────────────

// forgeAt resolves the forge client and the namespace THIS org owns on it.
//
// The namespace comes from [forge.Owner], a closed table: an org that is not in
// it is refused rather than becoming a forge coordinate by being spelled a
// certain way. That is the tenancy control, and it is upstream of everything
// else here — no argument reaching this file can select a namespace.
func forgeAt(ctx context.Context, s *cloud.Service[state], org string) (*forge.Client, string, error) {
	owner, err := forge.Owner(org)
	if err != nil {
		return nil, "", fmt.Errorf("sync: %w", err)
	}
	c, err := s.State.forge.Client(ctx, s.KMS, s.Domain)
	if err != nil {
		return nil, "", fmt.Errorf("sync: the forge is unreachable: %w", err)
	}
	return c, owner, nil
}

// forgeRemote is the forge end of an advance for one repository.
func forgeRemote(c *forge.Client, owner, name string) (remote, error) {
	r, err := c.Remote(owner, name)
	if err != nil {
		return remote{}, err
	}
	return remote{URL: r.URL, Cred: gitCred{User: r.User, Token: r.Token}}, nil
}

// upstream is the other end: the tenant's source, hardened, with whatever
// credential this call was given. An empty token leaves a zero gitCred, and the
// per-host token then applies — which is nothing for a host we hold nothing for,
// and a public repository needs nothing.
func upstream(rawURL, token string) (remote, error) {
	src, err := mirrorSource(rawURL)
	if err != nil {
		return remote{}, err
	}
	cred := gitCred{}
	if token != "" {
		cred = gitCred{User: mirrorBasicUser(hostOf(src)), Token: token}
	}
	return remote{URL: src, Cred: cred}, nil
}

// ── import: bring a whole repository in ──────────────────────────────────────

// ImportRepo makes the forge hold an upstream repository, fast-forward only.
//
// It creates the forge repository if it is not there — EMPTY, so the first
// branch to land is a create rather than a divergence — then advances every
// branch the upstream advertises and creates every tag it does not already have.
// Idempotent: a second import of an unchanged upstream costs two ref
// advertisements and moves nothing.
//
// FAST-FORWARD ONLY, per branch, exactly like the webhook path: a branch the
// forge has moved on independently is recorded as a conflict and LEFT ALONE,
// rather than destroyed to make the upstream fit. On a first import the forge
// holds nothing, so every branch is a create and the whole repository lands.
//
// When MirrorURL names a downstream, the same refs are then advanced OUT to it —
// also fast-forward only. See [mirrorOut].
func (importer) ImportRepo(ctx context.Context, req cloud.GitImportReq) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("sync: not mounted")
	}
	name := normalizeGitName(req.Repo)
	if !nameRE.MatchString(name) {
		return fmt.Errorf("sync: invalid repo name")
	}
	from, err := upstream(req.CloneURL, req.Token)
	if err != nil {
		return err
	}
	c, owner, err := forgeAt(ctx, s, req.Org)
	if err != nil {
		return err
	}
	if err := c.Init(ctx, owner, name, "synced from "+hostOf(from.URL)); err != nil {
		return err
	}
	to, err := forgeRemote(c, owner, name)
	if err != nil {
		return err
	}
	st, err := storeFor(s, req.Org)
	if err != nil {
		return err
	}

	srcTips, head, err := refs(ctx, from)
	if err != nil {
		return fmt.Errorf("read the source's refs: %w", err)
	}
	dstTips, _, err := refs(ctx, to)
	if err != nil {
		return fmt.Errorf("read the forge's refs: %w", err)
	}

	w, err := openTransit(ctx, s.DataDir, req.Org, name)
	if err != nil {
		return err
	}
	defer w.close()

	now := time.Now().Unix()
	landed := map[string]string{} // ref → the tip the forge now holds
	for _, ref := range sorted(srcTips) {
		// TAGS ARE CREATE-ONLY. A tag the forge already publishes is not re-pointed
		// — the push would refuse it anyway (git rejects any non-forced update to an
		// existing refs/tags/ ref), so skipping is the same answer without the round
		// trip, and without recording a "conflict" for a tag that is simply already
		// there.
		if strings.HasPrefix(ref, "refs/tags/") && dstTips[ref] != "" {
			landed[ref] = dstTips[ref]
			continue
		}
		oc, err := w.advance(ctx, from, to, ref, dstTips[ref], srcTips[ref])
		if err != nil {
			// A tag that will not land must not fail a branch-complete import; a
			// BRANCH that will not land means the import did not happen.
			if strings.HasPrefix(ref, "refs/tags/") {
				s.Log.Warn("sync import: tag", "org", req.Org, "repo", name, "ref", ref, "err", err)
				continue
			}
			return fmt.Errorf("advance %s: %w", ref, err)
		}
		if err := st.Record(ctx, req.Org, name, ref, oc.reason(), now); err != nil {
			s.Log.Warn("sync import: record", "org", req.Org, "repo", name, "ref", ref, "err", err)
		}
		if oc.Conflict {
			s.Log.Warn("sync.import.conflict", "org", req.Org, "repo", name, "ref", ref, "detail", oc.Detail)
			continue
		}
		landed[ref] = oc.After
	}

	// Point the forge's HEAD where the upstream points its own, so a clone-back
	// resolves the same default — but only once that branch is actually there,
	// which is the only moment it is a true statement.
	if head != "" && landed[head] != "" {
		if err := c.Default(ctx, owner, name, strings.TrimPrefix(head, "refs/heads/")); err != nil {
			s.Log.Warn("sync import: default branch", "org", req.Org, "repo", name, "branch", head, "err", err)
		}
	}

	if req.MirrorURL != "" {
		if err := (mirrorControl{}).EnsureMirror(ctx, req.Org, req.Project, name, req.MirrorURL, true); err != nil {
			s.Log.Warn("sync import: declare mirror", "org", req.Org, "repo", name, "err", err)
		}
	}
	mirrorOut(ctx, s, w, req.Org, name, st, to, landed)

	// The SAME push.landed the inbound and native push paths emit, so the code
	// index covers this repository now rather than after its next push. Origin is
	// the source host, so an outbound target on that host suppresses the echo.
	if head != "" && landed[head] != "" {
		cloud.EmitLifecycle(context.WithoutCancel(ctx), cloud.LifecycleEvent{
			Kind: cloud.LifecyclePushLanded, Org: req.Org, Project: req.Project, Repo: name,
			Branch: strings.TrimPrefix(head, "refs/heads/"), After: landed[head],
			Origin: hostOf(from.URL),
		})
	}
	return nil
}

// sorted returns a map's keys in a stable order, so two identical imports do the
// same work in the same sequence — a map's own order is random, and a random
// order makes a partial failure impossible to reason about twice.
func sorted(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── inbound: advance one ref from an upstream push ───────────────────────────

// InboundSync fast-forward-only advances ONE ref from an upstream push.
//
// Only an ALREADY-IMPORTED repository is synced — a push to one nobody imported
// is a no-op, because a webhook never provisions a repository behind somebody's
// back. "Imported" is a read OF THE FORGE: the forge holds the repositories, so
// it is the thing that can answer.
//
// On a fast-forward it advances the forge and emits a push.landed carrying
// Origin (so push-to-deploy fires AND an outbound target on the source's own
// host suppresses the echo). On a divergence it records a conflict and leaves
// the forge UNCHANGED. On an equal tip — the loop echo — it is a no-op that
// costs two ref advertisements and no transfer at all.
func (importer) InboundSync(ctx context.Context, req cloud.GitInboundReq) (cloud.GitSyncResult, error) {
	s := mounted.Load()
	if s == nil {
		return cloud.GitSyncResult{}, fmt.Errorf("sync: not mounted")
	}
	name := normalizeGitName(req.Repo)
	if !nameRE.MatchString(name) {
		return cloud.GitSyncResult{}, fmt.Errorf("sync: invalid repo name")
	}
	// A FULL ref, so branches and tags take the same path. The check is what keeps
	// a ref from escaping its namespace or carrying a traversal, and it refuses
	// the machine namespace outright — an upstream does not get to place a branch
	// where a coding run's own work lives.
	if !refRE.MatchString(req.Ref) {
		return cloud.GitSyncResult{}, fmt.Errorf("sync: invalid ref")
	}
	if !syncable(req.Ref) {
		return cloud.GitSyncResult{NoOp: true, Detail: "the machine namespace is not synced"}, nil
	}
	from, err := upstream(req.CloneURL, req.Token)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	c, owner, err := forgeAt(ctx, s, req.Org)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	if _, err := c.Machine().Repo(ctx, owner, name); err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return cloud.GitSyncResult{NoOp: true, Detail: "repo not imported"}, nil
		}
		return cloud.GitSyncResult{}, err
	}
	to, err := forgeRemote(c, owner, name)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	st, err := storeFor(s, req.Org)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}

	srcTips, _, err := refs(ctx, from, req.Ref)
	if err != nil {
		return cloud.GitSyncResult{}, fmt.Errorf("read the source's %s: %w", req.Ref, err)
	}
	dstTips, _, err := refs(ctx, to, req.Ref)
	if err != nil {
		return cloud.GitSyncResult{}, fmt.Errorf("read the forge's %s: %w", req.Ref, err)
	}

	w, err := openTransit(ctx, s.DataDir, req.Org, name)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	defer w.close()

	oc, err := w.advance(ctx, from, to, req.Ref, dstTips[req.Ref], srcTips[req.Ref])
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	if err := st.Record(ctx, req.Org, name, req.Ref, oc.reason(), time.Now().Unix()); err != nil {
		s.Log.Warn("sync inbound: record", "org", req.Org, "repo", name, "ref", req.Ref, "err", err)
	}
	switch {
	case oc.Conflict:
		s.Log.Warn("sync.inbound.conflict",
			"org", req.Org, "repo", name, "ref", req.Ref,
			"origin", req.Origin, "detail", oc.Detail)
		return cloud.GitSyncResult{Conflict: true, Detail: oc.Detail}, nil
	case oc.Applied:
		// Fan the advance into the lifecycle stream: push-to-deploy fires (the
		// canonical content changed) AND an outbound target on the source's own host
		// suppresses the echo, so no ping-pong occurs. Other targets still receive it.
		cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
			Kind: cloud.LifecyclePushLanded, Org: req.Org, Project: req.Project, Repo: name,
			Branch: req.Ref, Before: oc.Before, After: oc.After, Origin: req.Origin,
		})
		return cloud.GitSyncResult{Applied: true, Before: oc.Before, After: oc.After}, nil
	default:
		return cloud.GitSyncResult{NoOp: true, Detail: "up to date"}, nil
	}
}

// ── status: what the console renders ─────────────────────────────────────────

// RepoStatus reports, for each named repository, whether the forge holds it and
// what the last advance did.
//
// Two reads regardless of how many names are asked about: the org's repository
// inventory from the forge (which the forge client holds through a bounded
// staleness window, so a console poll is not a fan-out), and this org's outcome
// roll-up from one grouped query.
//
// A name the forge does not hold is present with a ZERO status rather than
// absent, which is what the retired implementation answered and what the plane
// leg turns back into absence — so neither leg can be told apart by its result.
func (importer) RepoStatus(ctx context.Context, org, _ string, names []string) (map[string]cloud.GitRepoStatus, error) {
	s := mounted.Load()
	if s == nil {
		return nil, fmt.Errorf("sync: not mounted")
	}
	c, owner, err := forgeAt(ctx, s, org)
	if err != nil {
		return nil, err
	}
	repos, err := c.Machine().Repos(ctx, owner)
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(repos))
	for _, r := range repos {
		held[normalizeGitName(r.Name)] = true
	}
	st, err := storeFor(s, org)
	if err != nil {
		return nil, err
	}
	states, err := st.States(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make(map[string]cloud.GitRepoStatus, len(names))
	for _, n := range names {
		name := normalizeGitName(n)
		v := cloud.GitRepoStatus{}
		if held[name] {
			v.Imported = true
		}
		if r, ok := states[name]; ok {
			v.Conflict, v.LastSyncedAt = r.Conflict, r.At
		}
		out[name] = v
	}
	return out, nil
}

// ── outbound: keep the declared replicas in step ─────────────────────────────

// EnsureMirror declares (enabled) or withdraws (!enabled) one outbound target
// for a repository. Idempotent both ways.
//
// It DECLARES and does not push: the pushing happens on the next advance of that
// repository ([mirrorOut]), so a target that exists is a fact about the
// repository rather than a job somebody has to keep running.
//
// The URL passes validateMirrorTarget — https, no userinfo, on the outbound
// allowlist — so a caller can never declare a push to an internal host, and
// never to the forge itself.
func (mirrorControl) EnsureMirror(ctx context.Context, org, _, repo, url string, enabled bool) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("sync: not mounted")
	}
	name := normalizeGitName(repo)
	if !nameRE.MatchString(name) {
		return fmt.Errorf("sync: invalid repo name")
	}
	canonical, host, err := validateMirrorTarget(url)
	if err != nil {
		// A caller's own mistake, and it says so with a status: the plane op returns
		// this error as it comes, so a bad URL must arrive as a 400 rather than as
		// our fault.
		return zip.ErrBadRequest(err.Error())
	}
	st, err := storeFor(s, org)
	if err != nil {
		return err
	}
	if enabled {
		return st.SetMirror(ctx, org, name, host, canonical)
	}
	return st.DropMirror(ctx, org, name, host)
}

// pushOut advances everything the forge holds for repo out to its declared
// targets. It is the whole of a push-only sync's reconcile: nothing comes in,
// and what is already canonical is offered to the replicas.
func pushOut(ctx context.Context, org, repo string) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("sync: not mounted")
	}
	name := normalizeGitName(repo)
	if !nameRE.MatchString(name) {
		return fmt.Errorf("sync: invalid repo name")
	}
	c, owner, err := forgeAt(ctx, s, org)
	if err != nil {
		return err
	}
	from, err := forgeRemote(c, owner, name)
	if err != nil {
		return err
	}
	st, err := storeFor(s, org)
	if err != nil {
		return err
	}
	tips, _, err := refs(ctx, from)
	if err != nil {
		return fmt.Errorf("read the forge's refs: %w", err)
	}
	w, err := openTransit(ctx, s.DataDir, org, name)
	if err != nil {
		return err
	}
	defer w.close()
	mirrorOut(ctx, s, w, org, name, st, from, tips)
	return nil
}

// mirrorOut advances the refs the forge now holds OUT to every declared target.
//
// It is the SAME advance, with the two ends swapped, and that is the whole
// design: a downstream is not force-fed. The retired outbound force-pushed the
// branch that moved, which is safe for a replica nobody writes and quietly
// destructive for one somebody does — and the GitHub-App import declares its own
// upstream as a target, which is precisely a place people write. Fast-forward
// out means a downstream that has moved on is reported, not overwritten.
//
// `landed` is what to offer. An import passes the refs it just advanced, minus
// any that conflicted — those have already been found to disagree, and offering
// them would earn a second refusal for the same fact. A push-only reconcile
// passes everything the forge holds, because nothing came in.
//
// Best-effort per target: one downstream's failure is LOGGED and never affects
// the import or the other targets. It is not written to the outcome table on
// purpose — that table answers "could the CANONICAL store be advanced", which is
// the question the console renders, and folding a downstream's state into the
// same row would make one word mean two things.
//
// It takes the caller's OPEN transit repository rather than opening its own: one
// repository's objects pass through one place at a time, and asking for a second
// while holding the first would be this function waiting for itself.
func mirrorOut(ctx context.Context, s *cloud.Service[state], w *work, org, repo string, st *store, from remote, landed map[string]string) {
	if len(landed) == 0 {
		return
	}
	targets, err := st.Mirrors(ctx, org, repo)
	if err != nil {
		s.Log.Warn("sync mirror-out: list targets", "org", org, "repo", repo, "err", err)
		return
	}
	for _, target := range targets {
		to := remote{URL: target, Cred: outboundCred(ctx, org, target)}
		tips, _, err := refs(ctx, to)
		if err != nil {
			s.Log.Warn("sync mirror-out: read target", "org", org, "repo", repo,
				"host", hostOf(target), "err", sanitizeGitErr(err.Error()))
			continue
		}
		for _, ref := range sorted(landed) {
			oc, err := w.advance(ctx, from, to, ref, tips[ref], landed[ref])
			switch {
			case err != nil:
				s.Log.Warn("sync mirror-out: advance", "org", org, "repo", repo,
					"host", hostOf(target), "ref", ref, "err", sanitizeGitErr(err.Error()))
			case oc.Conflict:
				s.Log.Warn("sync.mirror.conflict", "org", org, "repo", repo,
					"host", hostOf(target), "ref", ref, "detail", oc.Detail)
			case oc.Applied:
				s.Log.Info("sync mirror-out: advanced", "org", org, "repo", repo,
					"host", hostOf(target), "ref", ref)
			}
		}
	}
}

// outboundCred resolves the credential for a downstream push.
//
// For a GitHub target it prefers the org's OWN App installation token, resolved
// from the target's account — an org may replicate into several accounts and
// each has its own installation — so a repository imported under one org's
// credential is never pushed with another's. Absent one it returns a ZERO
// credential, which mirrorGitEnv resolves to the token held for that host — and
// absent that too the push is anonymous and fails closed at the remote rather
// than leaking anything.
func outboundCred(ctx context.Context, org, target string) gitCred {
	host := hostOf(target)
	if strings.EqualFold(host, "github.com") {
		if tok, err := integrations.InstallationToken(ctx, org, githubOwnerOf(target)); err == nil && tok != "" {
			return gitCred{User: mirrorBasicUser(host), Token: tok}
		}
	}
	return gitCred{}
}
