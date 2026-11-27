package coding

// work.go reads what a coding run did, from where it did it.
//
// A run works in its sandbox, and its sandbox is kept after it (sandboxrunner.go),
// so its tree is read THERE: its commits, its change against where it started,
// its directories and files, whatever repository it cloned or none at all. Once
// the sandbox is parked or gone, what it changed is read from what it saved
// (artifact.go); its tree waits for the sandbox to be resumed.
//
// Two rules decide a read, and each belongs to somebody else:
//
//	WHO MAY READ A RUN   the sessions app's rule (agents.Visible): a member reads
//	                     the runs they started, an org admin or a SuperAdmin the
//	                     org's, and another's is absent. The run is read through its
//	                     session because the session is the run.
//	WHO MAY READ ITS BOX the sandbox app's rule: every read of the sandbox is made
//	                     as the caller, and the sandbox is its lessee's and their
//	                     org admins'.
//
// Its pull request is the forge's, and is read there when the run proposed one
// on it (Merge merges it there).

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
	"golang.org/x/sync/singleflight"
)

// visibleRun is the session a read names, as the sessions app lets the caller
// see it — a value so a test can stand in for agents.
var visibleRun = agents.Visible

// reading is a run opened for a read: its repository, and the forge as the caller.
type reading struct {
	as           *forge.Client
	owner, name  string
	base, branch string
	// into is the branch the run proposed into: its base, and in a workspace the
	// platform made the repository's default branch (forge.go propose).
	into string
	// pr is the address the run's session says its pull request is read at.
	pr string
	// run is the session; ws and actor are the workspace and the forge login the
	// read acted in; repo is the repository as the forge holds it, which says
	// whether its home is GitHub.
	run   agents.Session
	ws    forge.Workspace
	actor string
	repo  forge.Repo
}

// read opens the run session names for a read by the caller on ctx.
func read(ctx context.Context, session string) (reading, error) {
	x, err := visibleRun(ctx, session)
	if err != nil {
		return reading{}, err
	}
	// Any other session answers as an absent one: this surface reads coding runs,
	// and a session that is not one has no run here to read.
	if x.Kind != kindCoding {
		return reading{}, zip.ErrNotFound("coding: no coding run has that session")
	}
	owner, name, ok := strings.Cut(x.Repo, "/")
	if !ok || !RepoRE.MatchString(name) {
		return reading{}, zip.ErrNotFound("coding: that run's repository is not on the forge")
	}
	ws, err := workspace(ctx, x.Org, false)
	if err != nil {
		return reading{}, zip.Errorf(http.StatusBadGateway, "coding: could not read the run's workspace: %s", err.Error())
	}
	if ws.Owner == "" || !strings.EqualFold(owner, ws.Owner) {
		return reading{}, zip.ErrNotFound("coding: that run's repository is not on the forge")
	}
	c, err := forgeClient(ctx)
	if err != nil {
		return reading{}, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	var actor string
	if !ws.Made {
		if actor, err = resolveActor(ctx); err != nil {
			return reading{}, zip.ErrForbidden(err.Error())
		}
	}
	as := acting(c, ws, actor)
	// The repository first, as the caller: it is what tells "you cannot open this
	// repository" from "the run's branch is not there yet", which the forge
	// answers with the same 404.
	repo, err := as.Repo(ctx, owner, name)
	if err != nil {
		return reading{}, refusal(err, "the run's repository")
	}
	base := cmp.Or(x.Base, repo.Branch)
	into := base
	if ws.Made {
		into = repo.Branch
	}
	return reading{as: as, owner: owner, name: name, base: base, into: into, branch: x.Branch, pr: x.PR,
		run: x, ws: ws, actor: actor, repo: repo}, nil
}

// landed reports whether the run's branch is on the forge yet.
func (r reading) landed(ctx context.Context) (bool, error) {
	if r.branch == "" {
		return false, nil
	}
	_, err := r.as.Resolve(ctx, r.owner, r.name, r.branch)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, forge.ErrUnknownActor):
		// The repository was readable a moment ago, so its 404 is the branch's.
		return false, nil
	}
	return false, refusal(err, "the run's branch")
}

// ref is where the run's files are read: its branch once the forge holds it, and
// the branch it started from until then.
func (r reading) ref(ctx context.Context) (string, error) {
	on, err := r.landed(ctx)
	if err != nil || !on {
		return r.base, err
	}
	return r.branch, nil
}

// A change is read for a person looking at a run, so what one read costs is
// bounded twice over. The forge bounds the ANSWER: the newest 250 commits, a diff
// cut at 8 MiB or 3000 files, one page of reviews each cut to 16 KiB — every cut
// said on the wire rather than refused (forge/change.go, forge/pull.go). This
// bounds the READS: a caller has at most maxChangeReads in flight, and callers
// asking the same change at once share one read of it.
const (
	// maxChangeReads is how many reads of a change one caller may have in flight.
	// A view polling one run needs one; past this a caller is asked to wait.
	maxChangeReads = 2
	// changeWait bounds one shared read of the forge. It is the read's own and not
	// its first asker's, so one who stops waiting does not fail the others.
	changeWait = time.Minute
)

var (
	changeReaders = &readers{held: map[string]int{}}
	changeFlights singleflight.Group
)

// readers counts the change reads each caller has in flight.
type readers struct {
	mu   sync.Mutex
	held map[string]int
}

// take claims one of who's reads, and reports false when they already hold all
// of them.
func (r *readers) take(who string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held[who] >= maxChangeReads {
		return false
	}
	r.held[who]++
	return true
}

// give returns one of who's reads.
func (r *readers) give(who string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held[who]--; r.held[who] <= 0 {
		delete(r.held, who)
	}
}

// Changes is what the run a session names changed: its commits, its files, and
// its pull request — read from its sandbox while that runs, else from what it
// saved.
func Changes(ctx context.Context, session string) (*client.CodingChanges, error) {
	who := cloud.Who(ctx)
	caller := who.Org + "/" + who.User
	if !changeReaders.take(caller) {
		return nil, zip.Errorf(http.StatusTooManyRequests,
			"coding: %d reads of a run's change are already in flight for you; ask again when one answers", maxChangeReads)
	}
	defer changeReaders.give(caller)
	x, err := visibleRun(ctx, session)
	if err != nil {
		return nil, err
	}
	if x.Kind != kindCoding {
		return nil, zip.ErrNotFound("coding: no coding run has that session")
	}
	out := &client.CodingChanges{Repo: x.Repo, Base: x.Base, Head: x.Branch,
		Commits: []client.CodingCommit{}, Files: []client.CodingFile{}}
	if b, ok := boxOf(ctx, x.Box); ok {
		if err := b.change(out); err != nil {
			return nil, err
		}
	} else {
		ix, err := readIndex(ctx, x.Org, x.ID)
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
		}
		out.Base = cmp.Or(out.Base, ix.Base)
		out.Commits = append(out.Commits, ix.Commits...)
		if patch, err := savedPatch(ctx, x.Org, x.ID, ix); err != nil {
			return nil, err
		} else {
			out.Files, out.MoreFiles = diffFiles(patch)
		}
	}
	if x.Branch != "" {
		if r, err := read(ctx, session); err == nil {
			out.Base = cmp.Or(out.Base, r.base)
			out.Pull, _ = r.pull(ctx)
		}
	}
	if out.Repo != "" && out.Base == "" {
		out.Base = "main"
	}
	if out.Pull == nil {
		out.Pull = recorded(x.PR)
	}
	// What was said to the run while no pod held it, and whether it has taken it. A
	// ledger that cannot be read is said, not served as an empty inbox.
	if l := flights.ledger(); l != nil {
		rows, err := l.inbox(ctx, x.Org, x.ID)
		if err != nil {
			return nil, unavailable(err)
		}
		out.Steers = steersOf(rows)
	}
	return out, nil
}

// recorded is the pull request a run's session names, for one the forge does not
// hold — GitHub's, for a repository the forge mirrors: where it is and its number,
// which is all the session recorded when the run proposed it. Its state, title and
// reviews are the host's to say and are left empty rather than guessed.
func recorded(address string) *client.CodingPull {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	n, _ := strconv.ParseInt(path.Base(address), 10, 64)
	return &client.CodingPull{Number: n, URL: address, Reviews: []client.CodingReview{}}
}

// box is a run's sandbox, read as the caller.
type box struct {
	ctx context.Context
	id  string
}

// boxOf is the run's sandbox when it is running for the caller to read.
func boxOf(ctx context.Context, id string) (box, bool) {
	if strings.TrimSpace(id) == "" {
		return box{}, false
	}
	m, err := sandboxpeer.SandboxRun(ctx, &client.RunIn{ID: id, Argv: []string{"true"}, TimeoutSec: 10})
	if err != nil || m == nil {
		return box{}, false
	}
	return box{ctx: ctx, id: id}, true
}

// run is one read in the box: stdout, or the refusal it ended with.
func (b box) run(argv []string, what string) (string, error) {
	ran, err := sandboxpeer.SandboxRun(b.ctx, &client.RunIn{ID: b.id, Argv: argv, TimeoutSec: 60})
	if err != nil {
		return "", err
	}
	if ran == nil || ran.ExitCode != 0 {
		msg := "no answer"
		if ran != nil {
			msg = strings.TrimSpace(ran.Stderr)
		}
		return "", zip.ErrNotFound("coding: " + what + ": " + msg)
	}
	return ran.Stdout, nil
}

// changeRead reads the checkout's commits since its branch started and its
// change, working tree and new files included, in a scratch index.
const changeRead = `set -eu
cd "$1"
printf 'base\t%s\n' "$(git symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null || true)"
b=$(git rev-parse -q --verify "` + baseRef + `$(git branch --show-current)" || git rev-list --max-parents=0 HEAD | tail -1)
i=$(mktemp)
trap 'rm -f "$i"' EXIT
[ -f .git/index ] && cp .git/index "$i"
export GIT_INDEX_FILE="$i"
git add -A >/dev/null 2>&1
git log -n 250 --format='commit%x09%H%x09%an%x09%aI%x09%s' "$b..HEAD"
echo diff
git -c core.quotepath=false diff --cached --no-renames ` + diffPrefixes + ` "$b" | head -c 8388608`

// diffPrefixes pins the two paths of a file header to a/ and b/ whatever the
// checkout's git configuration says: diffFiles reads a path off them, and
// `diff.noprefix` or `diff.mnemonicPrefix` would leave every path empty.
const diffPrefixes = `--src-prefix=a/ --dst-prefix=b/`

// change reads the box's change into out.
func (b box) change(out *client.CodingChanges) error {
	s, err := b.run([]string{"bash", "-c", changeRead, "bash", workdir}, "read the run's change")
	if err != nil {
		return err
	}
	readChange(s, out)
	return nil
}

// readChange reads changeRead's output into out.
func readChange(s string, out *client.CodingChanges) {
	head, patch, _ := strings.Cut(s, "diff\n")
	for l := range strings.SplitSeq(head, "\n") {
		// A run started on the repository's default branch names no base; the
		// checkout knows which branch that is.
		if c, ok := strings.CutPrefix(l, "base\t"); ok && out.Base == "" {
			out.Base = strings.TrimPrefix(c, "origin/")
			continue
		}
		if c, ok := strings.CutPrefix(l, "commit\t"); ok {
			if f := strings.SplitN(c, "\t", 4); len(f) == 4 {
				out.Commits = append(out.Commits, client.CodingCommit{SHA: f[0], Author: f[1], Date: f[2], Message: f[3]})
			}
		}
	}
	out.MoreCommits = len(out.Commits) == 250
	out.Files, out.MoreFiles = diffFiles(patch)
}

// workdir is where a coding run's sandbox keeps its checkout.
const workdir = "/work"

// savedPatch is the patch a run saved, or "" when it saved none.
func savedPatch(ctx context.Context, org, session string, ix index) (string, error) {
	for _, a := range ix.Artifacts {
		if a.Kind != kindPatch {
			continue
		}
		r, _, err := runs.open(ctx, prefix(org, session)+"/"+keyOf(a))
		if errors.Is(err, errNoArtifact) {
			return "", nil
		}
		if err != nil {
			return "", zip.Errorf(http.StatusBadGateway, "coding: read the run's patch: %v", err)
		}
		defer func() { _ = r.Close() }()
		b, err := io.ReadAll(io.LimitReader(r, maxDiff))
		if err != nil {
			return "", zip.Errorf(http.StatusBadGateway, "coding: read the run's patch: %v", err)
		}
		return string(b), nil
	}
	return "", nil
}

// The bounds a change is answered within: its diff, its files, one file's patch,
// and the patches carried across all of them.
const (
	maxDiff      = 8 << 20
	maxFiles     = 3000
	maxFilePatch = 64 << 10
	maxPatches   = 1 << 20
)

// diffFiles reads a git diff, one entry per file, within the bounds above; more
// says it was cut.
func diffFiles(diff string) ([]client.CodingFile, bool) {
	out := []client.CodingFile{}
	carried, more := 0, len(diff) >= maxDiff
	for _, part := range strings.Split(diff, "\ndiff --git ")[0:] {
		part = strings.TrimPrefix(part, "diff --git ")
		if strings.TrimSpace(part) == "" {
			continue
		}
		if len(out) == maxFiles {
			return out, true
		}
		header, body, _ := strings.Cut(part, "\n@@")
		f := client.CodingFile{Status: "modified"}
		for l := range strings.SplitSeq(header, "\n") {
			switch {
			case strings.HasPrefix(l, "new file mode"):
				f.Status = "added"
			case strings.HasPrefix(l, "deleted file mode"):
				f.Status = "deleted"
			case strings.HasPrefix(l, "+++ b/"):
				f.Path = strings.TrimPrefix(l, "+++ b/")
			case strings.HasPrefix(l, "--- a/") && f.Path == "":
				f.Path = strings.TrimPrefix(l, "--- a/")
			}
		}
		if f.Path == "" {
			// A binary or mode-only change names its paths on the first line alone.
			first, _, _ := strings.Cut(header, "\n")
			if _, b, ok := strings.Cut(first, " b/"); ok {
				f.Path = b
			}
		}
		if body != "" {
			patch := "@@" + body
			for l := range strings.SplitSeq(patch, "\n") {
				switch {
				case strings.HasPrefix(l, "+"):
					f.Additions++
				case strings.HasPrefix(l, "-"):
					f.Deletions++
				}
			}
			switch {
			case carried >= maxPatches:
				f.Truncated = true
			case len(patch) > maxFilePatch:
				f.Patch, f.Truncated = patch[:maxFilePatch], true
			default:
				f.Patch = patch
			}
			carried += len(f.Patch)
		}
		out = append(out, f)
	}
	return out, more
}

// pull is the run's pull request on the forge, with its reviews, or nil when it
// has none there.
//
// The session's address names it when the run proposed it on the forge, and that
// is read first. An address anywhere else is a proposal the forge does not hold —
// GitHub's, for a repository the forge mirrors — and is not looked for here, where
// a pull request with the same number would be somebody else's. With no address at
// all, the run may have proposed it and not yet said so, so its branch is looked
// up by head — the branch in THIS repository (forge.Pull.From), since a fork may
// open a pull request from a branch of the same name.
//
// Either way what is shown must propose the run's branch of the run's repository.
// A named pull request that does not is not the run's, whoever recorded it.
func (r reading) pull(ctx context.Context) (*client.CodingPull, error) {
	if p, ok, err := r.pullOnHub(ctx); ok {
		return p, err
	}
	p, found, err := r.find(ctx)
	if err != nil || !found {
		return nil, err
	}
	reviews, more, err := r.as.Reviews(ctx, r.owner, r.name, p.Number)
	if err != nil {
		return nil, err
	}
	out := &client.CodingPull{
		Number: p.Number, URL: p.URL, Title: p.Title, State: p.State, Mergeable: p.Mergeable,
		Reviews: make([]client.CodingReview, 0, len(reviews)), MoreReviews: more,
	}
	for _, v := range reviews {
		out.Reviews = append(out.Reviews, client.CodingReview{Author: v.Author, State: v.State, Body: v.Body,
			Truncated: v.Truncated, At: when(v.At)})
	}
	return out, nil
}

// Merge merges the pull request the run a session names proposed into its base,
// and answers the pull request after.
//
// Who may is who may read the run (agents.Visible): its person, an admin of its
// org. What of the code is the forge's to allow, as it is for every read here —
// the person, sudoed, in the estate's namespaces, so the forge's own rules on
// the base decide; the machine in a workspace the platform made, where the run's
// org is the whole of who may reach it and nobody holds a forge account.
//
// What is merged is what the caller can read: the run's own branch of the run's
// own repository, at the commit the forge holds now — a push after that read is
// refused by the forge rather than merged unseen. A pull request already merged
// answers as it is; one that is closed, conflicts, or is refused by a rule on
// the base is 409 with the forge's reason. Nothing is forced.
//
// With a request id the merge is asked once, however often and wherever it is asked:
// a retry answers what the first answered (ledger.go).
func Merge(ctx context.Context, in *client.CodingMergeIn) (*client.CodingMerged, error) {
	org, subject := asker(ctx)
	return requested(ctx, flights.ledger(), org, subject, scoped("merge", in.Session), in.RequestID,
		printOf(struct{ Session string }{strings.TrimSpace(in.Session)}),
		func(ctx context.Context) (*client.CodingMerged, error) { return merge(ctx, in.Session) })
}

func merge(ctx context.Context, session string) (*client.CodingMerged, error) {
	r, err := read(ctx, session)
	if err != nil {
		return nil, err
	}
	if r.branch == "" {
		return nil, zip.ErrConflict("coding: that run pushed no branch, so it has nothing to merge")
	}
	// The effect is the host's merge, and it is made once per request: a claimer that was
	// retaken stops here (fenced), and one that dies after the host merged is replayed
	// into a pull request the host already reports merged, which is answered as it is.
	if err := fenced(ctx); err != nil {
		return nil, err
	}
	// A repository whose home is GitHub has its pull request there (githubmerge.go).
	if m, ok, err := r.mergeOnHub(ctx); ok {
		return m, err
	}
	got, found, err := r.find(ctx)
	if err != nil {
		return nil, refusal(err, "the run's pull request")
	}
	if !found {
		return nil, zip.ErrConflict("coding: that run has no pull request yet; POST /v1/agent/coding/{session}/pull opens one")
	}
	switch got.State {
	case "merged":
		return &client.CodingMerged{Number: got.Number, URL: got.URL, State: got.State, Base: got.Base}, nil
	case "closed":
		return nil, zip.ErrConflict("coding: the run's pull request was closed without merging")
	}
	if !got.Mergeable {
		return nil, zip.ErrConflict("coding: the run's branch conflicts with " + got.Base + "; ask a run to bring it up to date")
	}
	done, err := r.as.Merge(ctx, r.owner, r.name, got.Number, got.Tip, got.Title)
	switch {
	case errors.Is(err, forge.ErrNotNow):
		return nil, zip.ErrConflict("coding: the forge would not merge it: " + err.Error())
	case errors.Is(err, forge.ErrExists):
		return nil, zip.ErrConflict("coding: the run's branch moved since it was read; read it again before merging")
	case err != nil:
		return nil, refusal(err, "the run's pull request")
	}
	return &client.CodingMerged{Number: done.Number, URL: done.URL, State: done.State, Base: done.Base}, nil
}

// Pull opens the pull request for the branch a run pushed, as the run's person and
// with no agent run, and answers it.
//
// A pull request is the step after the work, taken when the work is ready, so
// opening it asks for no new commit. A run that already has an open one answers
// with that one, and so does a branch that has one on its host, which makes
// asking twice safe. One the run proposed and that was closed is not "open": a
// fresh one is opened for the branch as it stands.
//
// Who may is who may read the run (agents.Visible), and what is proposed is the
// run's own branch of the run's own repository: the same branch Merge merges. On
// GitHub, for a repository the forge mirrors, the caller is admitted as a run on
// it is (admit), and the pull request opens there.
//
// With a request id the pull request is opened once, however often and wherever it is
// asked: a retry answers what the first answered (ledger.go).
func Pull(ctx context.Context, in *client.CodingPullIn) (*client.CodingPulled, error) {
	org, subject := asker(ctx)
	return requested(ctx, flights.ledger(), org, subject, scoped("pull", in.Session), in.RequestID,
		printOf(struct {
			Session, Title, Body, Base string
			Draft                      bool
			Assignees, Reviewers       []string
		}{strings.TrimSpace(in.Session), in.Title, in.Body, strings.TrimSpace(in.Base), in.Draft, in.Assignees, in.Reviewers}),
		func(ctx context.Context) (*client.CodingPulled, error) { return pull(ctx, in) })
}

func pull(ctx context.Context, in *client.CodingPullIn) (*client.CodingPulled, error) {
	proposal, err := proposalOf(in.Draft, in.Assignees, in.Reviewers)
	if err != nil {
		return nil, err
	}
	r, err := read(ctx, in.Session)
	if err != nil {
		return nil, err
	}
	if r.branch == "" {
		return nil, zip.ErrConflict("coding: that run pushed no branch, so it has nothing to propose")
	}
	into := cmp.Or(strings.TrimSpace(in.Base), r.into)

	// The one the run already has, if it is still open.
	if strings.TrimSpace(r.pr) != "" {
		got, found, err := r.find(ctx)
		switch {
		case err != nil:
			return nil, refusal(err, "the run's pull request")
		case !found:
			// GitHub's, which the forge cannot read: the address the run recorded.
			if p := recorded(r.pr); p != nil && p.Number > 0 {
				return &client.CodingPulled{Number: p.Number, URL: p.URL, Base: into}, nil
			}
		case got.State != "closed":
			return &client.CodingPulled{Number: got.Number, URL: got.URL, Base: got.Base}, nil
		}
	}

	open := planePR{ws: r.ws}
	if o, n, ok := r.repo.Home(); ok {
		h, _, _, err := admit(ctx, o, n, false)
		if err != nil {
			err = notAdmitted(o+"/"+n, err)
			if errors.Is(err, ErrNotAdmitted) {
				return nil, zip.ErrForbidden(err.Error())
			}
			return nil, zip.Errorf(http.StatusBadGateway, "%s", err.Error())
		}
		defer func() { _ = h.revoke(context.WithoutCancel(ctx)) }()
		if _, on := h.landed(ctx, o, n, r.branch); !on {
			return nil, zip.ErrConflict("coding: the run's branch is not on GitHub yet")
		}
		open.home = h
	} else if on, err := r.landed(ctx); err != nil {
		return nil, err
	} else if !on {
		return nil, zip.ErrConflict("coding: the run's branch is not on the forge yet")
	}

	// The effect is the host's pull request, made once per request: a claimer that was
	// retaken stops here (fenced), and a replay after one that died finds the branch's
	// pull request open on the host, which answers it as it is (above).
	if err := fenced(ctx); err != nil {
		return nil, err
	}
	x := r.run
	subject := pullSubject(ctx, r, open.home)
	ref, err := open.Open(ctx, PRInput{
		Org: x.Org, Actor: r.actor, Project: x.Project, Repo: r.name, Base: into, Head: r.branch,
		Title:    cmp.Or(strings.TrimSpace(in.Title), codingTitle(r.name, cmp.Or(subject, x.Title))),
		Body:     cmp.Or(in.Body, prBody(x.Title, into, r.branch, "", "", x.ID, r.actor, "")),
		Assignee: x.Agent, Draft: proposal.Draft, Assignees: proposal.Assignees, Reviewers: proposal.Reviewers,
		Request: filed(ctx),
	})
	var warning string
	if errors.Is(err, errPeople) && ref.URL != "" {
		// Open, and the person is told who was not added rather than handed an
		// error for a pull request that exists.
		warning, err = err.Error(), nil
	}
	if err != nil {
		return nil, refusal(err, "the run's pull request")
	}
	if ref.URL == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "coding: the host opened no pull request for %s", r.branch)
	}
	// The session says where its pull request is, so the changes it answers carry
	// the link. The pull request is open either way; a session that could not be
	// told reads it from the host.
	_ = planeSessions{}.Tag(ctx, x.Org, x.ID, Tag{PR: ref.URL})
	pulled := recorded(ref.URL)
	return &client.CodingPulled{Number: pulled.Number, URL: ref.URL, Base: into, Warning: warning}, nil
}

// pullSubject is the subject of the run's commit for its pull request title:
// from the indexed commits, the live sandbox, the GitHub branch, or the forge's
// branch comparison. Empty when none of those has a commit message to read.
func pullSubject(ctx context.Context, r reading, home *hub) string {
	if ix, err := readIndex(ctx, r.run.Org, r.run.ID); err == nil && len(ix.Commits) > 0 {
		if s := firstLine(ix.Commits[0].Message); s != "" {
			return s
		}
	}
	if b, ok := boxOf(ctx, r.run.Box); ok {
		var ch client.CodingChanges
		if b.change(&ch) == nil && len(ch.Commits) > 0 {
			if s := firstLine(ch.Commits[0].Message); s != "" {
				return s
			}
		}
	}
	if home != nil {
		if s := home.commitSubject(ctx, r.branch); s != "" {
			return s
		}
	}
	if r.as != nil && r.into != "" && r.branch != "" {
		if commits, _, err := r.as.Compare(ctx, r.owner, r.name, r.into, r.branch); err == nil && len(commits) > 0 {
			if s := firstLine(commits[0].Message); s != "" {
				return s
			}
		}
	}
	return ""
}

// find is the run's pull request on the forge, read the way [reading.pull]
// describes, and false when it has none there.
func (r reading) find(ctx context.Context) (forge.Pull, bool, error) {
	switch n, named := r.as.PullOf(r.pr, r.owner, r.name); {
	case named:
		got, err := r.as.Pull(ctx, r.owner, r.name, n)
		if errors.Is(err, forge.ErrUnknownActor) || errors.Is(err, forge.ErrNotFound) {
			return forge.Pull{}, false, nil // the pull request the run named is not there to read
		}
		if err != nil {
			return forge.Pull{}, false, err
		}
		return got, got.From(r.owner, r.name, r.branch), nil
	case strings.TrimSpace(r.pr) != "":
		return forge.Pull{}, false, nil
	}
	return r.as.PullFor(ctx, r.owner, r.name, r.into, r.branch)
}

// runBox is the running sandbox of the run a session names, for a read of its
// tree: 409 when it is parked, 404 when it is gone.
func runBox(ctx context.Context, session string) (box, agents.Session, error) {
	x, err := visibleRun(ctx, session)
	if err != nil {
		return box{}, x, err
	}
	if x.Kind != kindCoding {
		return box{}, x, zip.ErrNotFound("coding: no coding run has that session")
	}
	if b, ok := boxOf(ctx, x.Box); ok {
		return b, x, nil
	}
	if x.Box == "" {
		return box{}, x, zip.ErrNotFound("coding: that run has no sandbox")
	}
	return box{}, x, zip.ErrConflict("coding: the run's sandbox " + x.Box + " is not running; resume it " +
		"(POST /v1/sandbox/" + x.Box + "/resume) to read its tree, or read what it changed from its artifacts")
}

// clean is a repo-relative path, or false for one that climbs out.
func clean(p string) (string, bool) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", true
	}
	if c := path.Clean(p); c != p || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return p, true
}

// Tree is one directory of the run's tree, one level down, directories first:
// its workspace as it is now, .git aside.
func Tree(ctx context.Context, session, dir string) (*client.CodingTree, error) {
	dir, ok := clean(dir)
	if !ok {
		return nil, zip.ErrBadRequest("coding: path must stay inside the tree")
	}
	b, x, err := runBox(ctx, session)
	if err != nil {
		return nil, err
	}
	s, err := b.run([]string{"find", path.Join(workdir, dir), "-mindepth", "1", "-maxdepth", "1",
		"-not", "-name", ".git", "-printf", `%y\t%s\t%f\n`}, "that directory")
	if err != nil {
		return nil, err
	}
	out := &client.CodingTree{Ref: cmp.Or(x.Branch, "workspace"), Entries: []client.CodingEntry{}}
	var files []client.CodingEntry
	for l := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		f := strings.SplitN(l, "\t", 3)
		if len(f) != 3 {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		e := client.CodingEntry{Name: f[2], Path: path.Join(dir, f[2]), Type: "blob", Size: size}
		if f[0] == "d" {
			e.Type, e.Size = "tree", 0
			out.Entries = append(out.Entries, e)
			continue
		}
		files = append(files, e)
	}
	slices.SortFunc(out.Entries, func(a, b client.CodingEntry) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(files, func(a, b client.CodingEntry) int { return strings.Compare(a.Name, b.Name) })
	out.Entries = append(out.Entries, files...)
	return out, nil
}

// viewCap is the most of one file a read carries.
const viewCap = 1 << 20

// Blob is one file of the run's tree, where Tree reads, in the shape the git
// browse surface answers: text verbatim, anything else base64, and a file past
// the 1 MiB view cap marked truncated with no content.
func Blob(ctx context.Context, session, file string) (*client.CodingBlob, error) {
	file, ok := clean(file)
	if !ok || file == "" {
		return nil, zip.ErrBadRequest("coding: path is required, inside the tree")
	}
	b, _, err := runBox(ctx, session)
	if err != nil {
		return nil, err
	}
	s, err := b.run([]string{"sh", "-c", `[ -f "$1" ] && stat -c %s -- "$1" && head -c ` + strconv.Itoa(viewCap) + ` -- "$1"`,
		"sh", path.Join(workdir, file)}, "that file")
	if err != nil {
		return nil, err
	}
	sizeLine, data, _ := strings.Cut(s, "\n")
	size, _ := strconv.ParseInt(strings.TrimSpace(sizeLine), 10, 64)
	out := &client.CodingBlob{Path: file, Size: size, Encoding: "utf8", Truncated: size > viewCap}
	switch {
	case out.Truncated:
	case binary([]byte(data)):
		out.Binary, out.Encoding, out.Content = true, "base64", base64.StdEncoding.EncodeToString([]byte(data))
	default:
		out.Content = data
	}
	return out, nil
}

// binary reports bytes that are not text: a NUL in the first 8000, which is
// git's own test, or bytes that are not UTF-8, which a JSON string cannot carry
// as they are.
func binary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 || !utf8.Valid(b)
}

// when is an instant as the surface writes one, RFC 3339 in UTC; empty for none.
func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// refusal is a forge read's failure as the status it deserves. Absent, and not
// the caller's to see, are one 404 — the forge declines to say which, and so does
// this. A name that is not a path is the caller's to fix. A refusal is 403, and
// anything else is the forge failing.
func refusal(err error, what string) error {
	switch {
	case errors.Is(err, forge.ErrBadName):
		return zip.ErrBadRequest("coding: " + err.Error())
	case errors.Is(err, forge.ErrUnknownActor), errors.Is(err, forge.ErrNotFound),
		errors.Is(err, forge.ErrNotDir), errors.Is(err, forge.ErrNotFile):
		return zip.ErrNotFound("coding: " + what + " is not on the forge, or is not yours to read")
	case errors.Is(err, forge.ErrRefused):
		return zip.ErrForbidden("coding: the forge refused a read of " + what)
	}
	return zip.Errorf(http.StatusBadGateway, "coding: the forge could not be read: %v", err)
}
