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
)

// github_import.go implements the cloud.GitImporter seam (git_import.go in the
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
	if !branchRE.MatchString(req.Branch) {
		return cloud.GitSyncResult{}, fmt.Errorf("git: invalid branch")
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
	res, err := s.State.storage.inboundFastForward(ctx, req.Org, project, name, req.Branch, src, cred)
	if err != nil {
		return cloud.GitSyncResult{}, err
	}
	switch {
	case res.Conflict:
		_ = store.RecordConflict(ctx, req.Org, project, name, req.Branch, res.Detail, time.Now().Unix())
		s.Log.Warn("git.inbound.conflict",
			"org", req.Org, "project", project, "repo", name, "branch", req.Branch,
			"origin", req.Origin, "detail", res.Detail)
		return cloud.GitSyncResult{Conflict: true, Detail: res.Detail}, nil
	case res.Applied:
		_ = store.ClearConflict(ctx, req.Org, project, name, req.Branch)
		recordUsage(s, context.WithoutCancel(ctx), req.Org, project, name)
		// Fan the inbound advance into the lifecycle stream: push-to-deploy fires
		// (native content changed) AND the outbound mirror suppresses the echo —
		// Origin == the source host it just arrived from, so mirror_out skips that
		// target and no ping-pong occurs. Other targets (e.g. gitlab) still receive it.
		cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
			Kind: cloud.LifecyclePushLanded, Org: req.Org, Project: project, Repo: name,
			Branch: req.Branch, Before: res.Before, After: res.After, Origin: req.Origin,
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
	id, err := genID("mir")
	if err != nil {
		return err
	}
	err = store.CreateMirror(ctx, MirrorTarget{
		ID: id, Org: org, Project: project, Repo: repo,
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
	branches, head, err := s.lsRemoteHeads(ctx, srcURL, env)
	if err != nil {
		return nil, fmt.Errorf("list source refs: %w", err)
	}
	out := make(map[string]ffResult, len(branches))
	for _, b := range branches {
		if !branchRE.MatchString(b) {
			continue // skip a source branch whose name can't be a native ref
		}
		res, err := s.inboundFastForward(ctx, org, project, name, b, srcURL, cred)
		if err != nil {
			return out, fmt.Errorf("fetch branch %s: %w", b, err)
		}
		out[b] = res
	}
	// Tags: create-only (non-forcing), so an existing native tag is never clobbered.
	// Best-effort — a tag that can't create must not fail a branch-complete import.
	s.fetchTags(ctx, org, project, name, srcURL, env)
	// Point HEAD at the source default branch when native now has it (so a
	// clone-back resolves the same default the source has).
	if head != "" {
		s.setHeadIfPresent(ctx, org, project, name, head, env)
	}
	return out, nil
}

// lsRemoteHeads lists the source's branches (refs/heads/*) and its HEAD symref
// (the default branch) in one bounded ls-remote — the ref list is bounded by ref
// count, not pack size, so it is safe to buffer.
func (s *storage) lsRemoteHeads(ctx context.Context, srcURL string, env []string) (branches []string, head string, err error) {
	cmd, err := gitCmd(ctx, env,
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"ls-remote", "--symref", srcURL)
	if err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("ls-remote: %w: %s", err, sanitizeGitErr(stderr.String()))
	}
	for _, line := range strings.Split(out.String(), "\n") {
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
			branches = append(branches, b)
		}
	}
	return branches, head, nil
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

// inboundFastForward advances refs/heads/<branch> to the upstream tip IFF it is a
// fast-forward, using a NON-forcing refspec so git itself refuses (and preserves
// native on) a divergence. Returns:
//   - Applied  the fetch fast-forwarded native (Before != After)
//   - NoOp     already up to date (the loop echo: upstream tip == native tip)
//   - Conflict git rejected the update as non-fast-forward — native UNCHANGED
//
// A non-rejection failure (network/auth) is returned as an error; native is
// unchanged in that case too (a failed fetch never mutates a ref).
func (s *storage) inboundFastForward(ctx context.Context, org, project, name, branch, srcURL string, cred gitCred) (ffResult, error) {
	bareDir := s.absRepoPath(org, project, name)
	env := mirrorGitEnv(srcURL, cred)
	localRef := "refs/heads/" + branch
	before := s.revParse(ctx, bareDir, localRef) // "" when the branch is new to native

	// The DESTINATION refspec has NO leading '+', so git enforces fast-forward on
	// refs/heads/<branch>. protocol.version=2 = cheaper negotiation; credential.
	// helper= disables any leaky helper; --no-write-fetch-head keeps the bare repo
	// clean; the pack streams to disk under a pack slot (bounded memory).
	refspec := localRef + ":" + localRef
	args := append(packConfigArgs(""),
		"-c", "protocol.version=2", "-c", "credential.helper=",
		"--git-dir="+bareDir, "fetch", "--no-write-fetch-head", srcURL, refspec)
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
			Detail: "native branch " + branch + " has commits not on the upstream; fast-forward not possible (native preserved)",
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
	return trimHash(out.String())
}

// trimHash trims surrounding whitespace/newline from a git rev-parse hash.
func trimHash(s string) string {
	return string(bytes.TrimSpace([]byte(s)))
}
