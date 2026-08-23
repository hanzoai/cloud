package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/mint"
)

// github_import.go implements the cloud.GitImporter client (git_import.go in the
// root): the git object-plane half of the GitHub-App bidirectional sync. The
// integrations plane owns the App (token minting, repo list, webhook signature);
// this file owns the repo objects — create + mirror-in (import), the fast-forward-
// ONLY inbound advance, and the per-repo status roll-up. Registered in Mount via
// cloud.RegisterGitImporter so integrations reaches it with no integrations⇄git
// cycle.
//
// CANONICAL-SOURCE INVARIANT (the whole point): native is the source of truth.
// The inbound fetch uses a NON-forcing refspec (no leading '+'), so git itself
// REFUSES a non-fast-forward update — a diverged native ref is NEVER overwritten.
// A rejection is a Conflict (recorded + surfaced), not a force. This is enforced
// by git, not by our own ancestor check, so it cannot be reasoned around.

// githubImporter is the registered cloud.GitImporter. It carries no state — every
// method resolves the mounted service through the package `mounted` var (set in
// Mount before RegisterGitImporter), exactly like the lifecycle reactors, and
// fails closed when the subsystem is unmounted.
type githubImporter struct{}

// ImportRepo creates the native repo if absent, fast-forward mirror-ins every
// branch from the upstream using the per-call installation token, and registers the
// outbound mirror target so a later native push force-safe-mirrors back. Import is
// FAST-FORWARD-ONLY per branch — like the webhook, it NEVER force-overwrites native
// (a re-import of a repo whose native branch diverged records that branch as a
// conflict and preserves native, rather than destroying native-only commits). For
// the first import (empty native) every branch is a create, so the whole repo lands.
// Idempotent.
func (githubImporter) ImportRepo(ctx context.Context, req cloud.GitImportReq) error {
	s := mounted.Load()
	if s == nil {
		return fmt.Errorf("git: not mounted")
	}
	name := normalizeName(req.Repo)
	if !nameRE.MatchString(name) {
		return fmt.Errorf("git: invalid repo name")
	}
	project := req.Project
	if project != "" && !projectRE.MatchString(project) {
		return fmt.Errorf("git: invalid project")
	}
	// Harden the upstream URL the same way the org-supplied /mirror endpoint does:
	// https, no userinfo, SSRF-guarded host. github.com is public so it passes.
	src, err := mirrorSource(req.CloneURL)
	if err != nil {
		return err
	}
	store, err := storeFor(s, req.Org)
	if err != nil {
		return err
	}
	if _, err := ensureRepo(s, ctx, store, req.Org, project, name); err != nil {
		return err
	}
	cred := gitCred{}
	if req.Token != "" {
		cred = gitCred{User: "x-access-token", Token: req.Token}
	}
	outcomes, err := s.State.storage.importFetch(ctx, req.Org, project, name, src, cred)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for branch, oc := range outcomes {
		if oc.Conflict {
			_ = store.RecordConflict(ctx, req.Org, project, name, branch, oc.Detail, now)
		} else {
			_ = store.ClearConflict(ctx, req.Org, project, name, branch)
		}
	}
	if req.MirrorURL != "" {
		if err := ensureMirrorTarget(ctx, store, req.Org, project, name, req.MirrorURL); err != nil {
			s.Log.Warn("git import: register outbound mirror", "org", req.Org, "repo", name, "err", err)
		}
	}
	recordUsage(s, context.WithoutCancel(ctx), req.Org, project, name)
	// Index-on-import: emit the SAME push.landed the push/inbound paths emit so the
	// code index (index_on_push) covers this repo NOW, not only after its next push
	// (the one-way /v1/code-over-all-of-/v1/git guarantee). Origin = source host, so
	// the outbound mirror suppresses the echo back to where we just imported from.
	// Best-effort + detached (EmitLifecycle fans out on a cancel-immune ctx).
	emitImportPush(s, context.WithoutCancel(ctx), req.Org, project, name, src)
	return nil
}

// InboundSync fast-forward-only advances one branch from an upstream push. Only an
// ALREADY-IMPORTED repo is synced (a push to an un-imported repo is a no-op —
// never auto-create on a webhook). On a fast-forward it advances native + emits a
// push.landed carrying Origin (so push-to-deploy fires AND the outbound mirror
// suppresses the echo). On a divergence it records a conflict and leaves native
// UNCHANGED. On an equal tip (the loop echo) it is a no-op.
func (githubImporter) InboundSync(ctx context.Context, req cloud.GitInboundReq) (cloud.GitSyncResult, error) {
	s := mounted.Load()
	if s == nil {
		return cloud.GitSyncResult{}, fmt.Errorf("git: not mounted")
	}
	name := normalizeName(req.Repo)
	if !nameRE.MatchString(name) {
		return cloud.GitSyncResult{}, fmt.Errorf("git: invalid repo name")
	}
	// A FULL ref, so branches and tags are the same path. The suffix still goes
	// through branchRE: it is what keeps a ref from escaping its namespace or
	// carrying a traversal, and that property matters identically for a tag.
	if !refRE.MatchString(req.Ref) {
		return cloud.GitSyncResult{}, fmt.Errorf("git: invalid ref")
	}
	project := req.Project
	store, err := storeFor(s, req.Org)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	// Only sync imported repos. An un-imported push is a no-op (the user imports
	// explicitly; a webhook never provisions a new native repo behind their back).
	if _, err := store.Get(ctx, req.Org, project, name); err != nil {
		if errors.Is(err, errNotFound) {
			return cloud.GitSyncResult{NoOp: true, Detail: "repo not imported"}, nil
		}
		return cloud.GitSyncResult{}, err
	}
	src, err := mirrorSource(req.CloneURL)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	cred := gitCred{}
	if req.Token != "" {
		cred = gitCred{User: "x-access-token", Token: req.Token}
	}
	res, err := s.State.storage.inboundFastForward(ctx, req.Org, project, name, req.Ref, src, cred, "")
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	switch {
	case res.Conflict:
		_ = store.RecordConflict(ctx, req.Org, project, name, req.Ref, res.Detail, time.Now().Unix())
		s.Log.Warn("git.inbound.conflict",
			"org", req.Org, "project", project, "repo", name, "branch", req.Ref,
			"origin", req.Origin, "detail", res.Detail)
		return cloud.GitSyncResult{Conflict: true, Detail: res.Detail}, nil
	case res.Applied:
		_ = store.ClearConflict(ctx, req.Org, project, name, req.Ref)
		recordUsage(s, context.WithoutCancel(ctx), req.Org, project, name)
		// Fan the inbound advance into the lifecycle stream: push-to-deploy fires
		// (native content changed) AND the outbound mirror suppresses the echo —
		// Origin == the source host it just arrived from, so mirror_out skips that
		// target and no ping-pong occurs. Other targets (e.g. gitlab) still receive it.
		cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
			Kind: cloud.LifecyclePushLanded, Org: req.Org, Project: project, Repo: name,
			Branch: req.Ref, Before: res.Before, After: res.After, Origin: req.Origin,
		})
		return cloud.GitSyncResult{Applied: true, Before: res.Before, After: res.After}, nil
	default:
		return cloud.GitSyncResult{NoOp: true, Detail: "up to date"}, nil
	}
}

// RepoStatus returns the per-repo import + sync status for names (org-scoped). Two
// queries regardless of name count: the org's repo rows (Imported + LastSyncedAt)
// and the set of repos with an unresolved inbound conflict.
func (githubImporter) RepoStatus(ctx context.Context, org, project string, names []string) (map[string]cloud.GitRepoStatus, error) {
	s := mounted.Load()
	if s == nil {
		return nil, fmt.Errorf("git: not mounted")
	}
	store, err := storeFor(s, org)
	if err != nil {
		return nil, err
	}
	repos, err := store.List(ctx, org, project)
	if err != nil {
		return nil, err
	}
	updated := make(map[string]int64, len(repos))
	for _, r := range repos {
		updated[r.Name] = r.UpdatedAt
	}
	conflicts, err := store.ConflictRepoSet(ctx, org, project)
	if err != nil {
		return nil, err
	}
	out := make(map[string]cloud.GitRepoStatus, len(names))
	for _, n := range names {
		n = normalizeName(n)
		st := cloud.GitRepoStatus{}
		if ua, ok := updated[n]; ok {
			st.Imported = true
			st.LastSyncedAt = ua
		}
		if conflicts[n] {
			st.Conflict = true
		}
		out[n] = st
	}
	return out, nil
}

// ensureMirrorTarget registers a downstream outbound mirror for the repo,
// idempotently: a target to the same host already present is a no-op (not an
// error) so a re-import never fails on the existing mirror.
func ensureMirrorTarget(ctx context.Context, store *Store, org, project, repo, rawURL string) error {
	target, host, err := validateMirrorTarget(rawURL)
	if err != nil {
		return err
	}
	err = store.CreateMirror(ctx, MirrorTarget{
		ID: mint.ID("mir"), Org: org, Project: project, Repo: repo,
		Host: host, URL: target, CreatedAt: time.Now().Unix(),
	})
	if errors.Is(err, errConflict) {
		return nil // already registered — idempotent
	}
	return err
}

// ── import: fast-forward every branch (never force-overwrite native) ─────────

// importFetch fast-forward mirror-ins every branch the source advertises, plus its
// tags (create-only) and HEAD, returning the per-branch outcome. It reuses the
// single-branch ff primitive per branch, so a diverged native branch is a Conflict
// (native preserved), never a force-overwrite — the SAME canonical-source rule the
// webhook uses, applied to the whole repo. The first branch's fetch pulls the shared
// history; later branches reuse those objects (cheap negotiation).
func (s *storage) importFetch(ctx context.Context, org, project, name, srcURL string, cred gitCred) (map[string]ffResult, error) {
	env := mirrorGitEnv(srcURL, cred)
	branches, tags, head, err := s.lsRemoteHeads(ctx, srcURL, env)
	if err != nil {
		return nil, fmt.Errorf("list source refs: %w", err)
	}
	out := make(map[string]ffResult, len(branches))
	for _, r := range branches {
		b := r.name
		if !branchRE.MatchString(b) {
			continue // skip a source branch whose name can't be a native ref
		}
		// The advertised tip rides along, so a branch that has not moved costs no
		// fetch at all — inboundFastForward compares it to the local tip it already
		// reads and answers NoOp.
		//
		// The tip is in the ls-remote output and used to be thrown away, so a
		// reconcile spawned one `git fetch` per branch, every pass, to discover that
		// nothing had moved. Measured on this fleet: ~7 branches per repository
		// across 1685 declared syncs, so roughly 11,800 fetch subprocesses and their
		// network round trips per sweep, nearly all of them no-ops. That is the
		// reason a full pass took hours, which is the reason the fleet redeployed
		// before it finished.
		// THE FULL REF, which is what inboundFastForward documents and requires.
		// lsRemoteHeads strips refs/heads/, and handing the SHORT name on made the
		// refspec "2017:2017" — ambiguous on the source, where it matches both the
		// branch and a tag of that name, and DWIM'd to refs/heads/2017 on the way
		// in. git then refused to write a tag object to a branch and the whole
		// import died at that one branch:
		//
		//	cannot update ref 'refs/heads/2017': trying to write non-commit
		//	object ... to branch 'refs/heads/2017'
		//
		// It read as a tag bug and was a naming bug. A short name resolves fine
		// until something else answers to it, so every ordinary branch worked and
		// only repositories tagging a release after its branch failed — silently,
		// as a warn line, which is why the native git held a fraction of what was
		// declared for it. The short name is still what the OUTCOME is keyed by,
		// because that is the branch a caller asked about.
		res, err := s.inboundFastForward(ctx, org, project, name, "refs/heads/"+b, srcURL, cred, r.oid)
		if err != nil {
			return out, fmt.Errorf("fetch branch %s: %w", b, err)
		}
		out[b] = res
	}
	// Tags: create-only (non-forcing), so an existing native tag is never clobbered.
	// Best-effort — a tag that can't create must not fail a branch-complete import.
	//
	// Skipped entirely when the advertisement shows we already hold every tag it
	// names. Without this a steady-state reconcile still paid one fetch per repo to
	// learn nothing, which across 1685 syncs is 1685 round trips a sweep.
	if s.missingTag(ctx, org, project, name, tags) {
		s.fetchTags(ctx, org, project, name, srcURL, env)
	}
	// Point HEAD at the source default branch when native now has it (so a
	// clone-back resolves the same default the source has).
	if head != "" {
		s.setHeadIfPresent(ctx, org, project, name, head, env)
	}
	return out, nil
}

// remoteRef is one advertised branch: its short name and the object it points at.
//
// The OID is kept because the advertisement ALREADY carries it. Discarding it meant
// every reconcile re-fetched every branch to discover that nothing had moved — see
// importFetch.
type remoteRef struct {
	name string
	oid  string
}

// lsRemoteHeads lists the source's branches (refs/heads/*), its tags
// (refs/tags/*) and its HEAD symref (the default branch) in one bounded ls-remote —
// the ref list is bounded by ref count, not pack size, so it is safe to buffer.
//
// Each entry carries the advertised object id, which is what lets importFetch skip a
// ref that has not moved without fetching to find out.
func (s *storage) lsRemoteHeads(ctx context.Context, srcURL string, env []string) (branches, tags []remoteRef, head string, err error) {
	cmd, err := gitCmd(ctx, env,
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"ls-remote", "--symref", srcURL)
	if err != nil {
		return nil, nil, "", err
	}
	var out bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, "", fmt.Errorf("ls-remote: %w: %s", err, sanitizeGitErr(stderr.String()))
	}
	for line := range strings.SplitSeq(out.String(), "\n") {
		// "ref: refs/heads/main\tHEAD" — the default-branch symref.
		if rest, ok := strings.CutPrefix(line, "ref: "); ok {
			if tab := strings.IndexByte(rest, '\t'); tab > 0 {
				if strings.TrimSpace(rest[tab+1:]) == "HEAD" {
					head = strings.TrimSpace(rest[:tab])
				}
			}
			continue
		}
		// "<hash>\trefs/heads/<branch>" — a branch tip.
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 {
			continue
		}
		if b, ok := strings.CutPrefix(strings.TrimSpace(line[tab+1:]), "refs/heads/"); ok && b != "" {
			branches = append(branches, remoteRef{name: b, oid: strings.TrimSpace(line[:tab])})
			continue
		}
		// Tags from the SAME advertisement, so a steady-state reconcile can skip the
		// tag fetch too.
		//
		// The peeled entry is dropped only to avoid redundant work, NOT for
		// correctness: git resolves the "^{}" suffix itself, so rev-parse of
		// refs/tags/<x>^{} returns the same commit the advertisement peels to and the
		// comparison would match either way. Measured — removing this filter keeps
		// every test green. What it costs is one extra rev-parse subprocess per
		// annotated tag, on a path that already runs once per repository per sweep.
		if tg, ok := strings.CutPrefix(strings.TrimSpace(line[tab+1:]), "refs/tags/"); ok && tg != "" && !strings.HasSuffix(tg, "^{}") {
			tags = append(tags, remoteRef{name: tg, oid: strings.TrimSpace(line[:tab])})
		}
	}
	return branches, tags, head, nil
}

// missingTag reports whether the source advertises a tag we do not already hold at
// that exact object. Any single one is enough to make the tag fetch worth its round
// trip; none means there is nothing to learn.
//
// Create-only is what makes this safe to skip: an existing native tag is never
// re-pointed by the fetch, so a tag whose object differs locally is one the fetch
// would refuse anyway — and it is still fetched here, because refusing is the
// documented answer and silence is not.
func (s *storage) missingTag(ctx context.Context, org, project, name string, tags []remoteRef) bool {
	if len(tags) == 0 {
		return false
	}
	bareDir := s.absRepoPath(org, project, name)
	for _, t := range tags {
		if s.revParse(ctx, bareDir, "refs/tags/"+t.name) != t.oid {
			return true
		}
	}
	return false
}

// fetchTags fetches the source's tags into native with create-only (non-forcing)
// semantics, so an existing native tag is never clobbered. Best-effort.
func (s *storage) fetchTags(ctx context.Context, org, project, name, srcURL string, env []string) {
	bareDir := s.absRepoPath(org, project, name)
	args := append(packConfigArgs(""),
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "fetch", "--no-write-fetch-head", srcURL, "refs/tags/*:refs/tags/*")
	cmd, err := gitCmd(ctx, env, args...)
	if err != nil {
		return
	}
	cmd.Stderr = &cappedBuffer{cap: stderrCap}
	_ = withPackSlot(ctx, cmd.Run) // a non-ff tag update rejects; native's tag stays
}

// setHeadIfPresent points the bare repo's HEAD at ref (e.g. refs/heads/main) when
// that ref exists in native, so a clone resolves the source's default branch.
func (s *storage) setHeadIfPresent(ctx context.Context, org, project, name, ref string, env []string) {
	bareDir := s.absRepoPath(org, project, name)
	// The upstream chooses this ref, so it is checked (writer 8 in refpolicy.go).
	if checkHeadRef(ref) != nil {
		return
	}
	if s.revParse(ctx, bareDir, ref) == "" {
		return // native doesn't have that branch (it diverged/absent) — leave HEAD
	}
	cmd, err := gitCmd(ctx, env, "--git-dir="+bareDir, "symbolic-ref", "HEAD", ref)
	if err != nil {
		return
	}
	cmd.Stderr = &cappedBuffer{cap: stderrCap}
	_ = cmd.Run()
}

// ── the fast-forward-only fetch (the split-brain guard) ──────────────────────

// ffResult is the outcome of inboundFastForward.
type ffResult struct {
	Applied, NoOp, Conflict bool
	Before, After           string
	Detail                  string
}

// nonFFRE matches git's rejection of a non-fast-forward fetch. When git refuses to
// update refs/heads/<branch> because the upstream tip is not a descendant of the
// native tip, it prints "! [rejected] <b> -> <b> (non-fast-forward)" and exits
// non-zero WITHOUT changing the ref — that is precisely the divergence we treat as
// a Conflict (native preserved), distinct from a network/auth error.
var nonFFRE = regexp.MustCompile(`(?i)\[rejected\]|non-fast-forward|would clobber existing tag`)

// inboundFastForward advances a ref to the upstream tip IFF it is a
// fast-forward, using a NON-forcing refspec so git itself refuses (and preserves
// native on) a divergence. Returns:
//   - Applied  the fetch fast-forwarded native (Before != After)
//   - NoOp     already up to date (the loop echo: upstream tip == native tip)
//   - Conflict git rejected the update as non-fast-forward — native UNCHANGED
//
// A non-rejection failure (network/auth) is returned as an error; native is
// unchanged in that case too (a failed fetch never mutates a ref).
func (s *storage) inboundFastForward(ctx context.Context, org, project, name, ref, srcURL string, cred gitCred, want string) (ffResult, error) {
	bareDir := s.absRepoPath(org, project, name)
	env := mirrorGitEnv(srcURL, cred)
	// ref arrives FULL (refs/heads/<branch> or refs/tags/<tag>), so branches and
	// tags take the same path. The non-forcing refspec below is what makes that
	// safe for tags too: re-pointing an existing tag is not a fast-forward, so git
	// rejects it and native keeps the tag it already published.
	localRef := ref
	before := s.revParse(ctx, bareDir, localRef) // "" when the ref is new to native

	// ALREADY OURS? Then there is nothing to fetch, and `before` is the proof.
	//
	// `want` is the object the source ADVERTISED for this ref, or "" from a caller
	// that has no advertisement (the webhook endpoint). The comparison lives here,
	// beside the local tip it needs, so there is ONE place that decides a ref has
	// not moved and ONE rev-parse to decide it with — importFetch asked the same
	// question a moment earlier and then asked again through this function.
	//
	// NoOp is exactly what the fetch would have reported, so a caller's result is
	// unchanged; only the round trip is gone.
	if want != "" && before == want {
		return ffResult{NoOp: true, Before: before, After: before}, nil
	}

	// THE REF POLICY, on the inbound writer (writer 7 in refpolicy.go).
	//
	// Fast-forward-only is not the same guarantee as the policy's, and the gap is
	// exactly the attack: appending a commit to a branch a reviewer has already
	// read IS a fast-forward, and it is what the policy calls "changing what a
	// name already points at". So the command is stated and judged like any
	// other. `before` is empty for a ref new to native, which is what Creates()
	// reads; the new tip is unknown until git runs, and only Deletes() reads it —
	// a fetch never deletes.
	oldOID := before
	if oldOID == "" {
		oldOID = zeroOID
	}
	if verr := checkRefPolicy([]refCommand{{Old: oldOID, New: "", Ref: localRef}},
		defaultBranchOf(ctx, bareDir), ""); verr != nil {
		return ffResult{Conflict: true, Before: before, After: before, Detail: verr.Error()}, nil
	}

	// The DESTINATION refspec has NO leading '+', so git enforces fast-forward on
	// the ref. protocol.version=2 = cheaper negotiation; credential.
	// helper= disables any leaky helper; --no-write-fetch-head keeps the bare repo
	// clean; the pack streams to disk under a pack slot (bounded memory).
	//
	// --no-tags because THIS FETCH IS FOR ONE BRANCH. git otherwise auto-follows
	// tags that point into the history it just downloaded, and a tag sharing a
	// name with a branch is then resolved by short name onto the ref this very
	// refspec created: "cannot update ref 'refs/heads/2017': trying to write
	// non-commit object ... to branch". The whole import failed at that branch, so
	// a repository carrying a release tag named like its release branch could not
	// be imported at all — and nothing said so except a warn line, which is why
	// the native git held a fraction of what it was declared to hold.
	//
	// Nothing is lost by refusing them here: tags are fetched by fetchTags
	// immediately after, create-only into refs/tags/*, which is where a tag goes.
	refspec := localRef + ":" + localRef
	args := append(packConfigArgs(""),
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "fetch", "--no-write-fetch-head", "--no-tags", srcURL, refspec)
	cmd, err := gitCmd(ctx, env, args...)
	if err != nil {
		return ffResult{}, err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	runErr := withPackSlot(ctx, cmd.Run)

	after := s.revParse(ctx, bareDir, localRef)
	if runErr == nil {
		if after == before {
			return ffResult{NoOp: true, Before: before, After: after}, nil
		}
		return ffResult{Applied: true, Before: before, After: after}, nil
	}
	// Non-zero exit. A fast-forward rejection is the divergence signal — native is
	// preserved (after == before), a Conflict not an error. Anything else is a real
	// fetch failure surfaced to the caller (native still unchanged).
	msg := sanitizeGitErr(stderr.String())
	if nonFFRE.MatchString(msg) {
		return ffResult{
			Conflict: true, Before: before, After: after,
			Detail: "native ref " + ref + " has commits not on the upstream; fast-forward not possible (native preserved)",
		}, nil
	}
	return ffResult{}, fmt.Errorf("git fetch: %w: %s", runErr, msg)
}

// revParse resolves a ref to its commit hash, returning "" when the ref does not
// exist (a new branch) or on any error. `--verify --quiet` prints the hash + exits
// 0 when the ref resolves, and exits non-zero with no output otherwise.
func (s *storage) revParse(ctx context.Context, bareDir, ref string) string {
	cmd, err := gitCmd(ctx, nil, "--git-dir="+bareDir, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &cappedBuffer{cap: stderrCap}
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}
