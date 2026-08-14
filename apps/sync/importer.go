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
// the forge accepts as a path segment: no slash, no traversal, no surprise. It
// starts with a letter or a digit, which is also what keeps the separator in
// [repo.flat] unambiguous.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// accountRE bounds the upstream ACCOUNT a repository belongs to. It is GitHub's
// own login alphabet, and what matters about it is what it leaves out: no `_`,
// which is the separator [repo.flat] joins on, and no `/`, so a namespace that
// NESTS is not an account this can name.
var accountRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,38}$`)

// repo is one repository this seam syncs: the ACCOUNT it belongs to upstream,
// and its own name. Both are folded, so it is one value however it was spelled,
// and it is comparable, so it keys a map.
type repo struct{ account, name string }

// newRepo names a repository from a caller's (account, name), or refuses.
//
// # Why the account is half the name
//
// A repository's name is unique only WITHIN an account. hanzoai/ai,
// hanzo-apps/ai and hanzo-docs/ai are three real repositories under one IAM
// tenant, and the retired store kept them apart by holding
// <org>/<project>/<name>.git. Drop the account and all three become one: each
// import overwrites the last one's branches, the shared HEAD is re-pointed to
// whichever landed last, the console draws one row for three repositories, and
// the outbound target one of them declared receives another's commits under the
// org's own GitHub credential.
//
// # Why it folds into the name rather than the namespace
//
// The forge namespace is FLAT — Forgejo addresses owner/repo and holds nothing
// deeper — and the owner is not ours to choose ([forge.Owner] is a closed
// table). So the account rides in the repository name, and the fold is
// INJECTIVE: the account may not contain the separator and a name may not begin
// with it, so the FIRST `_` is exactly where the two come apart, and no two
// (account, name) pairs can spell one repository. It is the same trick, for the
// same reason, as a published project's <org>_<slug>.
//
// A pair this cannot spell unambiguously is REFUSED rather than coerced or
// escaped: nothing is imported, which is the safe half of the failure.
//
// # A namespace that nests is such a pair
//
// A GitLab subgroup names a place two coordinates cannot reach:
// group/sub1/widgets and group/sub2/widgets are two repositories, and the flat
// forge has one owner and one name to spell them with. Both ways out are worse
// than refusing — dropping the middle segments makes them ONE repository (the
// overwrite this whole file exists to prevent), and escaping the separator moves
// the ambiguity to whatever escape is chosen. So it is refused exactly as an
// account carrying the join separator is: the error NAMES the namespace, and
// nothing is written.
func newRepo(account, name string) (repo, error) {
	r := repo{account: fold(account), name: fold(name)}
	if r.account != "" && !accountRE.MatchString(r.account) {
		return repo{}, fmt.Errorf("sync: %q is not an account the forge can hold a repository under", r.account)
	}
	if !nameRE.MatchString(r.name) {
		return repo{}, fmt.Errorf("sync: %q is not a repository name the forge can hold", r.name)
	}
	return r, nil
}

// flat is the ONE name r takes on the forge.
//
// An empty account is a repository whose upstream named none, and it flattens to
// `_name` — which no (account, name) with an account can spell, because a name
// never begins with the separator. So "nowhere in particular" is a coordinate of
// its own rather than a collision with everything.
func (r repo) flat() string { return r.account + "_" + r.name }

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

// canonical is the destination an outcome names when the advance went INTO the
// forge. It is empty rather than the forge's hostname: the record is about the
// canonical store, which is one thing however this deployment addresses it, and
// a hostname there would make a moved forge read as a second destination.
const canonical = ""

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
//
// A credential is only ever offered to a host we mint credentials FOR
// ([mirrorInHostAllowed]), and a call that would send one elsewhere is refused
// rather than quietly downgraded: the URL is an argument on the internal plane,
// the token is a live installation credential, and "fetch anonymously instead"
// would hide the fact that somebody asked us to hand it over.
func upstream(rawURL, token string) (remote, error) {
	src, err := mirrorSource(rawURL)
	if err != nil {
		return remote{}, err
	}
	cred := gitCred{}
	if token != "" {
		host := hostOf(src)
		if !mirrorInHostAllowed(host) {
			return remote{}, fmt.Errorf("sync: %s is not a source we hold credentials for", host)
		}
		cred = gitCred{User: mirrorBasicUser(host), Token: token}
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
// holds nothing, so every branch is a create and the whole repository lands. A
// run where EVERY ref was refused is an error rather than a quiet success — it
// took nothing in, and the caller reads a nil here as "synced".
//
// When MirrorURL names a downstream, the same refs are then advanced OUT to it —
// also fast-forward only. See [mirrorOut].
func (importer) ImportRepo(ctx context.Context, req cloud.GitImportReq) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("sync: not mounted")
	}
	org := fold(req.Org)
	r, err := newRepo(req.Project, req.Repo)
	if err != nil {
		return err
	}
	from, err := upstream(req.CloneURL, req.Token)
	if err != nil {
		return err
	}
	c, owner, err := forgeAt(ctx, s, org)
	if err != nil {
		return err
	}
	if err := c.Init(ctx, owner, r.flat(), "synced from "+hostOf(from.URL)); err != nil {
		return err
	}
	to, err := forgeRemote(c, owner, r.flat())
	if err != nil {
		return err
	}
	st, err := storeFor(s, org)
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

	w, err := openTransit(ctx, s.DataDir, org, r.flat())
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
				s.Log.Warn("sync import: tag", "org", org, "repo", r.flat(), "ref", ref, "err", err)
				continue
			}
			return fmt.Errorf("advance %s: %w", ref, err)
		}
		if word, ok := oc.reason(); ok {
			if err := st.Record(ctx, org, r, ref, canonical, word, now); err != nil {
				s.Log.Warn("sync import: record", "org", org, "repo", r.flat(), "ref", ref, "err", err)
			}
		}
		if oc.Conflict {
			s.Log.Warn("sync.import.conflict", "org", org, "repo", r.flat(), "ref", ref, "detail", oc.Detail)
			continue
		}
		landed[ref] = oc.After
	}

	// Point the forge's HEAD where the upstream points its own, so a clone-back
	// resolves the same default — but only once that branch is actually there,
	// which is the only moment it is a true statement.
	if head != "" && landed[head] != "" {
		if err := c.Default(ctx, owner, r.flat(), strings.TrimPrefix(head, "refs/heads/")); err != nil {
			s.Log.Warn("sync import: default branch", "org", org, "repo", r.flat(), "branch", head, "err", err)
		}
	}

	if req.MirrorURL != "" {
		if err := (mirrorControl{}).EnsureMirror(ctx, org, r.account, r.name, req.MirrorURL, true); err != nil {
			s.Log.Warn("sync import: declare mirror", "org", org, "repo", r.flat(), "err", err)
		}
	}
	// The import's own success is the INBOUND advance — the canonical store moved
	// — so a replica that could not be brought into step is logged here rather
	// than failing an import that landed. It is not silent: mirrorOut records the
	// divergence against that replica's host, so the console says this repository
	// is not in step even though the import worked.
	if _, err := mirrorOut(ctx, s, w, org, r, st, to, landed); err != nil {
		s.Log.Warn("sync import: mirror out", "org", org, "repo", r.flat(), "err", err)
	}

	// The SAME push.landed the inbound and native push paths emit, so the code
	// index covers this repository now rather than after its next push. Origin is
	// the source host, so an outbound target on that host suppresses the echo.
	if head != "" && landed[head] != "" {
		cloud.EmitLifecycle(context.WithoutCancel(ctx), cloud.LifecycleEvent{
			Kind: cloud.LifecyclePushLanded, Org: org, Project: r.account, Repo: r.name,
			Branch: strings.TrimPrefix(head, "refs/heads/"), After: landed[head],
			Origin: hostOf(from.URL),
		})
	}
	// AN IMPORT THAT LANDED NOTHING IS NOT AN IMPORT. Every ref refused means the
	// canonical store did not move, and the reconcile stamps "last synced, just
	// now" on this returning nil — the same lie pushOut refuses to tell about a
	// push nothing received. The refusals are already recorded per ref above, so
	// the console still says which ones and against whom; this says the operation
	// itself did not happen. An upstream that advertises no ref at all is not
	// that: there was nothing to take, and the empty repository is the answer.
	if len(landed) == 0 && len(srcTips) > 0 {
		return fmt.Errorf("sync: %s took nothing in; every ref was refused", r.flat())
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
	org := fold(req.Org)
	r, err := newRepo(req.Project, req.Repo)
	if err != nil {
		return cloud.GitSyncResult{}, err
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
	c, owner, err := forgeAt(ctx, s, org)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	if _, err := c.Machine().Repo(ctx, owner, r.flat()); err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return cloud.GitSyncResult{NoOp: true, Detail: "repo not imported"}, nil
		}
		return cloud.GitSyncResult{}, err
	}
	to, err := forgeRemote(c, owner, r.flat())
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	st, err := storeFor(s, org)
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

	w, err := openTransit(ctx, s.DataDir, org, r.flat())
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	defer w.close()

	oc, err := w.advance(ctx, from, to, req.Ref, dstTips[req.Ref], srcTips[req.Ref])
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	if word, ok := oc.reason(); ok {
		if err := st.Record(ctx, org, r, req.Ref, canonical, word, time.Now().Unix()); err != nil {
			s.Log.Warn("sync inbound: record", "org", org, "repo", r.flat(), "ref", req.Ref, "err", err)
		}
	}
	switch {
	case oc.Conflict:
		s.Log.Warn("sync.inbound.conflict",
			"org", org, "repo", r.flat(), "ref", req.Ref,
			"origin", req.Origin, "detail", oc.Detail)
		return cloud.GitSyncResult{Conflict: true, Detail: oc.Detail}, nil
	case oc.Applied:
		// Fan the advance into the lifecycle stream: push-to-deploy fires (the
		// canonical content changed) AND an outbound target on the source's own host
		// suppresses the echo, so no ping-pong occurs. Other targets still receive it.
		cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
			Kind: cloud.LifecyclePushLanded, Org: org, Project: r.account, Repo: r.name,
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
//
// The names are asked about WITHIN one account, because that is the only scope
// in which a repository name means one repository. A caller whose list spans
// several accounts asks once per account; asking about "ai" without saying whose
// draws one row for hanzoai/ai, hanzo-apps/ai and hanzo-docs/ai alike.
func (importer) RepoStatus(ctx context.Context, org, account string, names []string) (map[string]cloud.GitRepoStatus, error) {
	s := mounted.Load()
	if s == nil {
		return nil, fmt.Errorf("sync: not mounted")
	}
	org = fold(org)
	c, owner, err := forgeAt(ctx, s, org)
	if err != nil {
		return nil, err
	}
	repos, err := c.Machine().Repos(ctx, owner)
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(repos))
	for _, have := range repos {
		held[fold(have.Name)] = true
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
		r, err := newRepo(account, n)
		if err != nil {
			// A name this seam could never have imported has nothing to report, and
			// the zero status is exactly that answer.
			out[fold(n)] = cloud.GitRepoStatus{}
			continue
		}
		v := cloud.GitRepoStatus{}
		if held[r.flat()] {
			v.Imported = true
		}
		if roll, ok := states[r]; ok {
			v.Conflict, v.LastSyncedAt = roll.Conflict, roll.At
		}
		out[r.name] = v
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
func (mirrorControl) EnsureMirror(ctx context.Context, org, account, repo, url string, enabled bool) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("sync: not mounted")
	}
	org = fold(org)
	r, err := newRepo(account, repo)
	if err != nil {
		return err
	}
	target, host, err := validateMirrorTarget(url)
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
		return st.SetMirror(ctx, org, r, host, target)
	}
	return st.DropMirror(ctx, org, r, host)
}

// pushOut advances everything the forge holds for repo out to its declared
// targets. It is the whole of a push-only sync's reconcile: nothing comes in,
// and what is already canonical is offered to the replicas.
//
// So it REPORTS whether that happened, and nothing here treats "we tried" as
// "we synced". A push-only reconcile with no target declared, or with a target
// that refused, moved no bytes at all — and the engine advances its cursor and
// stamps "last synced" on what this returns, so an unconditional nil there is a
// repository reading "synced, just now" forever while nothing leaves the forge.
func pushOut(ctx context.Context, org, account, repo string) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("sync: not mounted")
	}
	org = fold(org)
	r, err := newRepo(account, repo)
	if err != nil {
		return err
	}
	c, owner, err := forgeAt(ctx, s, org)
	if err != nil {
		return err
	}
	from, err := forgeRemote(c, owner, r.flat())
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
	if len(tips) == 0 {
		return fmt.Errorf("sync: the forge holds no ref for %s", r.flat())
	}
	w, err := openTransit(ctx, s.DataDir, org, r.flat())
	if err != nil {
		return err
	}
	defer w.close()
	sent, err := mirrorOut(ctx, s, w, org, r, st, from, tips)
	if err != nil {
		return err
	}
	if sent == 0 {
		return fmt.Errorf("sync: %s has no declared outbound target", r.flat())
	}
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
// Per target, and it REPORTS: it returns how many targets it left in step and an
// error naming the ones it did not, and it RECORDS a refused ref against that
// target's host. One downstream's failure still does not stop the others — the
// loop finishes — but it does not disappear either. A push that did not land is
// not a sync, and the caller decides what that means for the operation it is
// part of: an import already advanced the canonical store and only logs, a
// push-only reconcile has done nothing else and fails.
//
// The outcome row it writes names the TARGET'S HOST, so a replica's divergence
// and the forge's are different facts in the same table: neither clears the
// other, and the console's roll-up still says, in one word, that this repository
// is not in step.
//
// It takes the caller's OPEN transit repository rather than opening its own: one
// repository's objects pass through one place at a time, and asking for a second
// while holding the first would be this function waiting for itself.
func mirrorOut(ctx context.Context, s *cloud.Service[state], w *work, org string, r repo, st *store, from remote, landed map[string]string) (int, error) {
	if len(landed) == 0 {
		return 0, nil
	}
	targets, err := st.Mirrors(ctx, org, r)
	if err != nil {
		return 0, fmt.Errorf("list outbound targets: %w", err)
	}
	now := time.Now().Unix()
	sent := 0
	var refused []string
	for _, target := range targets {
		host := hostOf(target)
		to := remote{URL: target, Cred: outboundCred(ctx, org, target)}
		tips, _, err := refs(ctx, to)
		if err != nil {
			s.Log.Warn("sync mirror-out: read target", "org", org, "repo", r.flat(),
				"host", host, "err", sanitizeGitErr(err.Error()))
			refused = append(refused, host+" is unreachable")
			continue
		}
		ok := true
		for _, ref := range sorted(landed) {
			oc, err := w.advance(ctx, from, to, ref, tips[ref], landed[ref])
			if err != nil {
				s.Log.Warn("sync mirror-out: advance", "org", org, "repo", r.flat(),
					"host", host, "ref", ref, "err", sanitizeGitErr(err.Error()))
				refused, ok = append(refused, host+" refused "+ref), false
				continue
			}
			if word, rec := oc.reason(); rec {
				if err := st.Record(ctx, org, r, ref, host, word, now); err != nil {
					s.Log.Warn("sync mirror-out: record", "org", org, "repo", r.flat(),
						"host", host, "ref", ref, "err", err)
				}
			}
			switch {
			case oc.Conflict:
				s.Log.Warn("sync.mirror.conflict", "org", org, "repo", r.flat(),
					"host", host, "ref", ref, "detail", oc.Detail)
				refused, ok = append(refused, host+" has diverged on "+ref), false
			case oc.Applied:
				s.Log.Info("sync mirror-out: advanced", "org", org, "repo", r.flat(),
					"host", host, "ref", ref)
			}
		}
		if ok {
			sent++
		}
	}
	if len(refused) > 0 {
		return sent, fmt.Errorf("sync: %s did not reach %s", r.flat(), strings.Join(refused, "; "))
	}
	return sent, nil
}

// outboundCred resolves the credential for a downstream push.
//
// For a GitHub target it prefers the org's OWN App installation token, resolved
// from the target's account — an org may replicate into several accounts and
// each has its own installation — so a repository imported under one org's
// credential is never pushed with another's. Absent one it returns a ZERO
// credential, which [credAuthHeader] resolves to the token held for that host —
// and absent that too the push is anonymous and fails closed at the remote
// rather than leaking anything.
func outboundCred(ctx context.Context, org, target string) gitCred {
	host := hostOf(target)
	if strings.EqualFold(host, "github.com") {
		if tok, err := integrations.InstallationToken(ctx, org, accountOf(target)); err == nil && tok != "" {
			return gitCred{User: mirrorBasicUser(host), Token: tok}
		}
	}
	return gitCred{}
}
