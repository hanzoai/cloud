package git

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// Smart-HTTP git protocol (git v1/v2, the widely-supported baseline). Three
// endpoints per repo let `git clone` / `git fetch` / `git push` operate natively
// over HTTPS against the on-disk bare repo:
//
//	GET  info/refs?service=git-upload-pack|git-receive-pack  ref advertisement
//	POST git-upload-pack                                     clone/fetch
//	POST git-receive-pack                                    push
//
// The streaming git CLI does the heavy lifting (gitexec.go): info/refs shells
// out to `git <service> --stateless-rpc --advertise-refs`, and the POST bodies
// stream through `git <service> --stateless-rpc` — request body → git stdin,
// git stdout → HTTP response — so a multi-GB pack never lands in this process's
// memory. This file is only the HTTP framing (pkt-line service header, content
// types, gzip, Git-Protocol passthrough) around the ONE pack seam the SSH
// transport (ssh.go) also drives. Patterns ported from the upstream forge
// (routers/web/repo/githttp.go).

const (
	svcUploadPack  = "git-upload-pack"
	svcReceivePack = "git-receive-pack"
)

// notByHand is the sentence that matters most to the consumer these descriptions
// reach. Twelve of git's published operations are the git wire protocol, so every
// generated SDK offers twelve methods and every spec-derived CLI twelve commands
// that a human should never call directly — and the honest thing the document can
// do is say so.
const notByHand = " This is git's own wire protocol, not an API call to make by " +
	"hand: point a git client at the clone URL and it makes this request itself."

// The four path families the SAME three handlers answer on. A family is a real
// difference in who can reach the route and how the repository is addressed, so
// each states its own, and the twelve descriptions are generated from three
// leads and four suffixes rather than written twelve times.
var packMounts = []struct{ prefix, note string }{
	{"/v1/git/:org/:repo", " Addressed under the API prefix, so `git clone " +
		"https://<host>/v1/git/<org>/<repo>.git` works on any host the binary serves."},
	{"/v1/git/:org/:project/:repo", " Addressed under the API prefix, with the " +
		"PROJECT as a middle path segment: project scope otherwise rides a header a " +
		"git client cannot send, so this path is the only usable remote for a " +
		"project-scoped repository."},
	{"/:org/:repo", " Addressed at the git host's root, so `git clone " +
		"https://<git-host>/<org>/<repo>.git` works with the canonical URL and no " +
		"prefix. Served ONLY on the dedicated git host; on the API and console hosts " +
		"it falls through, so a bare /:org/:repo can never shadow another surface."},
	{"/:org/:project/:repo", " Addressed at the git host's root with the PROJECT as " +
		"a middle path segment — the canonical-URL form of the project-scoped remote, " +
		"since a git client has no header to carry a project. Served only on the " +
		"dedicated git host; elsewhere it falls through."},
}

// The prose for git's twelve smart-HTTP operations. None can be a typed op — the
// request and response are a binary pack stream — so zipdoc has nothing to lift,
// and left bare they published twelve operationIds a reader could not tell from
// twelve ordinary JSON calls. openapi.Describe attaches prose to routes the
// router already carries; the four families below are four distinct router
// patterns, so each is described separately rather than folded into one.
func init() {
	for _, m := range packMounts {
		openapi.Describe(m.prefix+"/info/refs", http.MethodGet,
			"Advertise a repository's refs to a git client",
			"The ref-advertisement phase of git's smart-HTTP protocol — the first "+
				"request a clone, a fetch and a push all make. `?service=` selects which: "+
				"`git-upload-pack` advertises for a fetch, `git-receive-pack` for a push, "+
				"and any other value is 400.\n\n"+
				"ANONYMOUS ONLY FOR FETCH, AND ONLY ON A PUBLIC REPOSITORY. The push "+
				"advertisement always requires an authenticated org, and where a path org "+
				"is present it must equal the authenticated one. A private repository "+
				"reached without its org is 404, indistinguishable from one that does not "+
				"exist."+m.note+notByHand)

		openapi.Describe(m.prefix+"/"+svcUploadPack, http.MethodPost,
			"Serve a clone or fetch",
			"The pack-transfer phase of a clone or fetch: the request and the response "+
				"are git's binary pack protocol, streamed straight through git itself — "+
				"request body to git's stdin, git's stdout to the response — so a "+
				"multi-gigabyte clone never lands in this process's memory.\n\n"+
				"A PUBLIC repository is fetched anonymously; a private one requires its own "+
				"org, and a wrong or absent org is 404 rather than a hint that the "+
				"repository exists. A Content-Type other than "+
				"`application/x-"+svcUploadPack+"-request` is 400."+m.note+notByHand)

		openapi.Describe(m.prefix+"/"+svcReceivePack, http.MethodPost,
			"Accept a push, and turn it into a build",
			"The pack-transfer phase of a push, and the point at which a push becomes an "+
				"EVENT. NEVER ANONYMOUS: a push always requires an authenticated org, and "+
				"the org in the path must equal it.\n\n"+
				"Once the pack is on disk the repository's storage usage is metered and a "+
				"build is fired for every branch whose tip actually moved, computed from "+
				"the before/after branch diff rather than from what the client claimed. "+
				"That runs on a cancel-immune context, so a client that hangs up the moment "+
				"its push lands still gets its build, and it runs even when git itself "+
				"exited non-zero — the refs on disk are the ground truth. Repacking "+
				"housekeeping is detached and never blocks the response.\n\n"+
				"A Content-Type other than `application/x-"+svcReceivePack+"-request` is "+
				"400."+m.note+notByHand)
	}
}

// gitProtocol returns the validated client Git-Protocol header (empty when
// absent/malformed) to forward as the subprocess GIT_PROTOCOL — protocol v2 is a
// large-repo perf win (cheaper negotiation, partial clone).
func gitProtocol(c *zip.Ctx) string { return c.Header("Git-Protocol") }

// setGitNoCache applies git's smart-HTTP no-cache headers (the upstream forge's setHeaderNoCache).
func setGitNoCache(c *zip.Ctx) {
	c.SetHeader("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
	c.SetHeader("Pragma", "no-cache")
	c.SetHeader("Cache-Control", "no-cache, max-age=0, must-revalidate")
}

// infoRefs serves GET /info/refs — the ref-advertisement phase. The service is
// selected by the ?service= query param; both upload-pack (fetch) and
// receive-pack (push) advertise here.
func infoRefs(s *cloud.Service[state], c *zip.Ctx) error {
	service := c.Query("service")
	if service != svcUploadPack && service != svcReceivePack {
		return zip.ErrBadRequest("service must be git-upload-pack or git-receive-pack")
	}
	// Anonymous read is allowed ONLY for the fetch advertisement of a PUBLIC
	// repo; the push advertisement (receive-pack) always requires the org.
	org, project, name, err := resolvePackRepo(s, c, service == svcUploadPack)
	if err != nil {
		return err
	}

	// Advertisement is bounded by ref count (not pack size) — safe to buffer.
	bareDir := s.State.storage.absRepoPath(org, project, name)
	refs, err := advertiseRefs(c.Context(), bareDir, service, gitProtocol(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "advertise refs: %v", err)
	}

	// Smart-HTTP requires a pkt-line "# service=<name>\n" + flush before the
	// advertisement body (git http-backend contract). packetWrite encodes the
	// service line; "0000" is the flush pkt.
	c.SetHeader("Content-Type", "application/x-"+service+"-advertisement")
	setGitNoCache(c)
	var out bytes.Buffer
	out.Write(packetWrite("# service=" + service + "\n"))
	out.WriteString("0000")
	out.Write(refs)
	return c.Bytes(http.StatusOK, out.Bytes())
}

// uploadPack serves POST /git-upload-pack — the clone/fetch phase. The RESPONSE
// is the packfile (potentially multi-GB), so it STREAMS: the request body feeds
// `git upload-pack --stateless-rpc` stdin and git's stdout is handed to fasthttp
// as the response body — no pack bytes are buffered in this process.
func uploadPack(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, err := resolvePackRepo(s, c, true) // public repos fetch anonymously
	if err != nil {
		return err
	}
	if ct := c.Header("Content-Type"); ct != "application/x-"+svcUploadPack+"-request" {
		return zip.ErrBadRequest("unexpected content-type for " + svcUploadPack)
	}
	body := packRequestBody(c)
	bareDir := s.State.storage.absRepoPath(org, project, name)
	stream, err := startPackRPC(c.Context(), s.Log, bareDir, svcUploadPack, gitProtocol(c), body)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	c.SetHeader("Content-Type", "application/x-"+svcUploadPack+"-result")
	setGitNoCache(c)
	return c.SendStream(stream)
}

// receivePack serves POST /git-receive-pack — the push phase. The heavy data is
// the INBOUND pack (request body → `git receive-pack --stateless-rpc` stdin →
// index-pack to disk); the RESPONSE is only the small report-status, so it runs
// SYNCHRONOUSLY: apply the pack, then re-meter storage and fire push-to-deploy
// for every branch the push advanced (branch-tip diff), then return the report —
// the SAME side effects an SSH push produces, deterministic before the client's
// push returns. Memory stays bounded: the pack streams to disk, only the tiny
// report is buffered.
func receivePack(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, name, err := resolvePackRepo(s, c, false) // push is NEVER anonymous
	if err != nil {
		return err
	}
	if ct := c.Header("Content-Type"); ct != "application/x-"+svcReceivePack+"-request" {
		return zip.ErrBadRequest("unexpected content-type for " + svcReceivePack)
	}
	bareDir := s.State.storage.absRepoPath(org, project, name)

	// THE REF POLICY, applied before a single object is indexed.
	//
	// It runs here because this is the point every push passes through and none
	// can decline to — a rule in the client, the sandbox or the orchestrator is a
	// rule the compromised party gets to skip. Refusing BEFORE receive-pack runs
	// also means a refused push costs no disk: the pack is never unpacked.
	//
	// The body is read once and reused: packRequestBody already copies it out of
	// the framework's reused buffer, so the policy reads the same bytes git will,
	// and there is no window in which the two could disagree about what was asked.
	raw := append([]byte(nil), c.Body()...)
	cmds, _, caps, perr := parseRefCommandsCaps(raw)
	if perr != nil {
		// A push whose commands we could not read is refused, not waved through:
		// a policy applied to commands you could not parse is not a policy. This
		// one IS an HTTP error, because a body that is not the protocol has no
		// report-status framing to answer in.
		return zip.ErrBadRequest("unreadable push: " + perr.Error())
	}
	if verr := checkRefPolicy(cmds, defaultBranchOf(c.Context(), bareDir)); verr != nil {
		s.Log.Warn("git push refused by ref policy", "org", org, "repo", name, "reason", verr.Error())
		// 200 with a report-status, NOT a 403. The push is refused either way;
		// the difference is whether the human reads the reason or reads
		// "RPC failed; HTTP 403" and concludes the forge is broken. Nothing is
		// applied — receive-pack never runs and the pack is never unpacked.
		c.SetHeader("Content-Type", "application/x-"+svcReceivePack+"-result")
		setGitNoCache(c)
		return c.Bytes(http.StatusOK, refusalReport(cmds, caps, verr.Error()))
	}

	body := bytes.NewReader(raw)
	before := branchTips(c.Context(), bareDir)

	cmd, err := gitCmd(c.Context(), gitProtocolEnv(gitProtocol(c)),
		append(packConfigArgs(svcReceivePack), packSubcommand(svcReceivePack), "--stateless-rpc", bareDir)...)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	var report bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = body, &report, stderr
	runErr := withPackSlot(c.Context(), cmd.Run)

	// The pack has landed on disk; meter + fire builds on a cancel-immune context
	// (the branch diff is ground truth, so it runs even on a non-zero exit). The
	// pusher is the gateway-validated user id (X-User-Id) — best-effort attribution.
	bg := context.WithoutCancel(c.Context())
	recordUsage(s, bg, org, project, name)
	fireBranchBuilds(s, bg, org, project, name, c.User(), before, branchTips(bg, bareDir))
	// Keep clones fast: opportunistic housekeeping (a no-op until git's own
	// thresholds trigger a repack). Detached + slot-yielding, never blocks push.
	go autoMaintain(s.Log, bareDir)

	if runErr != nil {
		s.Log.Warn("git receive-pack failed", "org", org, "repo", name, "err", runErr, "stderr", strings.TrimSpace(stderr.String()))
		if report.Len() == 0 {
			return zip.Errorf(http.StatusInternalServerError, "receive-pack failed")
		}
	}
	c.SetHeader("Content-Type", "application/x-"+svcReceivePack+"-result")
	setGitNoCache(c)
	return c.Bytes(http.StatusOK, report.Bytes())
}

// packRequestBody returns a reader over the pack request body. The HTTP
// framework applies Content-Encoding transparently — Fiber's Body() inflates a
// gzip/deflate/br/zstd request per its header — so c.Body() is the DECODED
// pkt-line stream and the pack driver reads it verbatim. Decoding again here
// double-inflated every request the client compressed (git gzips the
// ref-negotiation once a repo carries enough refs), which failed clone/fetch
// for all but trivially small repos. The body is copied out of the framework's
// reused request buffer so it stays valid across the deferred response stream
// that feeds git stdin. It is bounded by the edge BodyLimit (the OOM vector was
// the multi-GB RESPONSE pack, now streamed; SSH carries large pushes unbuffered).
func packRequestBody(c *zip.Ctx) io.Reader {
	return bytes.NewReader(append([]byte(nil), c.Body()...))
}

// resolvePackRepo is the shared front-half of every smart-HTTP pack handler:
// resolve the org, validate the repo name, enforce the URL org segment matches
// the authenticated org (path-vs-identity guard), and confirm the repo exists.
// Returns the (org, project, name) the pack driver operates on.
//
// The project sub-scope comes from the :project path segment when the request
// used the three-segment form, and from X-Project-Id otherwise. The path wins
// because it is what `git clone` can express: a URL is the only channel a git
// client has, and a header cannot reach a remote.
//
// allowPublic is the READ concession: with no authenticated org, a fetch-side
// caller (upload-pack) may still resolve a repo that is (a) addressed by an
// explicit, orgRE-safe :org path segment and (b) marked Public. A private or
// missing repo answers the SAME 404, so anonymous probing cannot distinguish
// existence. Push (receive-pack) never passes allowPublic.
//
// An anonymous caller may name a project in the PATH but never in the header.
// The two are not equivalent: the header asserts the caller's own scope and is
// unvalidated without a principal, whereas the path segment only addresses a
// different repo — one that still has to be Public to be served. Refusing the
// path form instead would leave every public project-scoped repo unclonable.
// packProject resolves the sub-scope a pack request operates in.
//
// The :project path segment wins when present, for both authenticated and
// anonymous callers: it addresses a repo rather than asserting a scope, and the
// repo's own Public flag still decides whether an anonymous caller may read it.
// It is validated against projectRE because it becomes a storage path segment.
//
// With no path segment the header applies, and only with a principal behind it —
// an unauthenticated X-Project-Id is unvalidated input, so it degrades to the
// org level exactly as before.
func packProject(c *zip.Ctx, authed bool) (string, error) {
	if p := c.Param("project"); p != "" {
		if len(p) > 128 || !projectRE.MatchString(p) {
			return "", zip.ErrBadRequest("project path segment is not a valid identifier")
		}
		return p, nil
	}
	if !authed {
		return "", nil
	}
	return projectScope(c), nil
}

func resolvePackRepo(s *cloud.Service[state], c *zip.Ctx, allowPublic bool) (string, string, string, error) {
	orgID, authed := org(c)
	if !authed {
		if !allowPublic {
			return "", "", "", zip.ErrForbidden("X-Org-Id required")
		}
		orgID = c.Param("org")
		if orgID == "" || !orgRE.MatchString(orgID) {
			return "", "", "", zip.ErrForbidden("X-Org-Id required")
		}
	}
	name, err := repoNameParam(c)
	if err != nil {
		return "", "", "", err
	}
	project, err := packProject(c, authed)
	if err != nil {
		return "", "", "", err
	}
	if p := c.Param("org"); p != "" && p != orgID {
		return "", "", "", zip.ErrForbidden("org path does not match authenticated org")
	}
	store, serr := storeFor(s, orgID)
	if serr != nil {
		return "", "", "", zip.Errorf(http.StatusInternalServerError, "open store: %v", serr)
	}
	r, gerr := store.Get(c.Context(), orgID, project, name)
	if gerr != nil || (!authed && !r.Public) {
		return "", "", "", zip.ErrNotFound("repo not found")
	}
	return orgID, project, name, nil
}

// fireBranchBuilds fires a push-to-deploy build for every branch whose tip
// advanced (created or updated) between the before/after snapshots. Deleted
// branches (present in before, gone in after) and unchanged branches are
// skipped — matching the old semantics (branch refs created/updated, never
// deletes/tags). Best-effort and non-fatal: the push already landed. pusher is
// the (best-effort) identity that pushed, threaded onto the lifecycle event.
func fireBranchBuilds(s *cloud.Service[state], ctx context.Context, org, project, name, pusher string, before, after map[string]string) {
	for branch, newHash := range after {
		if before[branch] == newHash {
			continue // unchanged
		}
		fireBranchBuild(s, ctx, org, project, name, branch, before[branch], newHash, pusher)
	}
}

// fireBranchBuild fires the reactions for ONE advanced branch — the ONE place both
// cloud.OnGitPush (the single-registrant deploy trigger, UNCHANGED) and
// cloud.EmitLifecycle (the many-subscriber stream: mirror-out, Slack-notify) are
// called; every push transport (receive-pack over HTTP/SSH, the client-less /push)
// funnels through here. The org/project/name/branch/before/pusher are cloned because
// they are subslices of fiber's reused request buffers (a reactor that ENQUEUES the
// event would otherwise see them mutate into a later request's path); `after` (the
// new commit) comes from a fresh git-output string.
func fireBranchBuild(s *cloud.Service[state], ctx context.Context, org, project, name, branch, before, after, pusher string) {
	org, project, name = strings.Clone(org), strings.Clone(project), strings.Clone(name)
	branch, before, pusher = strings.Clone(branch), strings.Clone(before), strings.Clone(pusher)
	if err := cloud.OnGitPush(ctx, cloud.GitPushEvent{
		Org: org, Project: project, Repo: name,
		Ref: "refs/heads/" + branch, Commit: after, CloneURL: cloneURL(s, org, project, name),
	}); err != nil {
		s.Log.Warn("git push-to-deploy trigger failed", "org", org, "repo", name, "branch", branch, "err", err)
	}
	// Fan the same fact out to the lifecycle stream (best-effort, detached — never
	// blocks the push). Origin is "" for a native push (the mirror-out reactor's
	// loop-prevention seam; a future inbound sync stamps the source host).
	cloud.EmitLifecycle(ctx, cloud.LifecycleEvent{
		Kind: cloud.LifecyclePushLanded,
		Org:  org, Project: project, Repo: name,
		Branch: branch, Before: before, After: after, Pusher: pusher,
	})
}
