// Package lineage answers one question about one commit: was it ever a state of the
// branch that hands out version numbers?
//
// A version number is only meaningful inside one lineage. "v1.801.551" means the
// commit after v1.801.550 along ONE line of development; the same string laid over
// an unrelated history names nothing, because the ordering that gives it meaning
// does not exist there. So the register that allocates those numbers — the arbiter
// — may only allocate one to a commit its release branch has been.
//
// STATES OF THE BRANCH, NOT EVERYTHING REACHABLE FROM IT. Those are different sets
// and the difference is the whole guard. Merging a history into the branch makes
// every commit in it an ancestor, and an ancestor test would then admit all of them
// — permanently, from one ordinary merge that fast-forwards and needs no force. But
// a branch never WAS those commits: it went from the commit before the merge
// straight to the merge itself, and what the merge absorbed was never a state it
// passed through. `rev-list --first-parent` is exactly that sequence of states, so
// it is exactly the set a number may name.
//
// The question goes to git, because ancestry IS the commit graph and git is what
// computes over it. A `--filter=tree:0` fetch brings back that branch's commits and
// nothing else — no trees, no blobs — which is all either walk reads: a few
// megabytes and about a second for a repository of tens of thousands of commits,
// cheap enough to ask on every claim.
//
// Asking git also keeps the answer independent of what anything CALLS the commit or
// the repository. A clone URL, a repository slug and a checkout directory are all
// names, and a forge can answer one name with a different repository; the shape of
// the graph is a property of the objects themselves and cannot be renamed into being.
package lineage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Arbiter is the repository and branch whose history defines one version sequence.
//
// Remote is a clone URL naming the arbiter by ITS OWN name: a forge answers a
// repository's former name with a redirect to whatever holds that name now, so this
// path refuses redirects rather than following them (see env). A Remote that has to
// be redirected to reach its target fails the fetch, which is the honest outcome —
// the repository at the end of a redirect is not the one the caller named.
type Arbiter struct {
	// Remote is the clone URL fetched for the release branch.
	Remote string
	// Branch is the branch whose history numbers the sequence.
	Branch string
}

// Cloud is the arbiter of the ghcr.io/hanzoai/cloud version sequence: every
// v<X.Y.Z> in that sequence names a commit this branch has been.
//
// It is ONE value, because there is one claimant. A second one reading a second
// repository is how a sequence comes to be numbered by two histories, and the
// remedy is not to make them agree — it is for there to be one.
var Cloud = Arbiter{
	Remote: "https://git.hanzo.ai/hanzo-inc/cloud",
	Branch: "main",
}

// ErrForeign reports a commit the arbiter's release branch has never been.
// Callers wrap it into their own refusal; tests assert on it, so a refusal that
// happened for an unrelated reason cannot be mistaken for a lineage refusal.
var ErrForeign = errors.New("commit is not in the arbiter's release lineage")

// fetchTimeout bounds the whole conversation with the arbiter. A claim that cannot
// establish lineage does not proceed, so a hung forge stops the release rather than
// stalling it indefinitely.
const fetchTimeout = 2 * time.Minute

// line is the local ref the arbiter's release branch is fetched into, and remote is
// the name given to the arbiter itself. Both are private names in a repository this
// package creates empty, so nothing already on disk can stand in for what the
// arbiter actually answered.
const (
	line   = "refs/lineage/line"
	remote = "arbiter"
)

// Verify reports nil when sha is a state the arbiter's release branch has been, and
// an error naming sha, the arbiter and the branch when it is not.
//
// It is the ONE question asked in front of allocating a version number, and it runs
// before the allocation rather than after: a number handed to a commit is a claim on
// the sequence, so the commit has to earn it first.
//
// token authenticates the fetch and travels as a header — never in argv, never as
// URL userinfo — because a process's command line is readable by anything sharing
// the container and git echoes a URL credential back in its own error text. An empty
// token is passed through unused: whether the arbiter needs one is the arbiter's
// business, and a fetch that is refused for want of a credential says so.
func (a Arbiter) Verify(ctx context.Context, token, sha string) error {
	if err := hex(sha); err != nil {
		return fmt.Errorf("%w: %v", ErrForeign, err)
	}
	if a.Remote == "" || a.Branch == "" {
		return errors.New("lineage: no arbiter configured — a version number cannot be allocated without the repository that numbers the sequence")
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "lineage")
	if err != nil {
		return fmt.Errorf("lineage: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	env := a.env(dir, token)
	if out, _, err := git(ctx, dir, env, "init", "--quiet", "--bare"); err != nil {
		return fmt.Errorf("lineage: init: %w: %s", err, out)
	}
	// The arbiter is added as a named remote so the partial fetch below can record it
	// as the source to consult for an object it did not bring back. That is what lets
	// the two refusals stay distinct: a commit the arbiter holds is retrieved and
	// judged not reachable, while a commit it does not hold cannot be retrieved at all.
	if out, _, err := git(ctx, dir, env, "remote", "add", remote, a.Remote); err != nil {
		return fmt.Errorf("lineage: naming %s: %w: %s", a.Remote, err, out)
	}
	// The branch's commits, and only its commits. Anything reachable from the branch
	// arrives; anything else does not, which is the distinction being measured.
	if out, _, err := git(ctx, dir, env, "fetch", "--quiet", "--no-tags", "--filter=tree:0",
		remote, "+refs/heads/"+a.Branch+":"+line); err != nil {
		return fmt.Errorf("lineage: cannot read %s from %s, so %s cannot be shown to belong to it: %w: %s",
			a.Branch, a.Remote, sha, err, out)
	}

	// IS IT THERE AT ALL. git separates the answers by exit status: 0 reachable, 1
	// present but not reachable, anything else a failure to decide. This is the cheap
	// half of the question and it is asked first because it is the half that can tell
	// a commit from somewhere else entirely apart from one this repository holds —
	// two situations that send a reader to different places, and the refusal has to
	// say which.
	out, code, err := git(ctx, dir, env, "merge-base", "--is-ancestor", sha, line)
	switch {
	case err == nil && code == 0:
		// Reachable. Which is necessary and not sufficient — see below.
	case code == 1:
		return fmt.Errorf("%w: %s is not reachable from %s on %s — a version number in this sequence orders commits along that branch, and this commit is not on it",
			ErrForeign, sha, a.Branch, a.Remote)
	case code == 128:
		// The arbiter does not hold the object at all. Unreachable for the strongest
		// possible reason, and worth saying separately: "not on the branch" and "not
		// in the repository" send a reader to different places.
		return fmt.Errorf("%w: %s is not a commit %s holds, so it cannot be on %s",
			ErrForeign, sha, a.Remote, a.Branch)
	default:
		return fmt.Errorf("lineage: cannot decide whether %s is on %s at %s: %w: %s",
			sha, a.Branch, a.Remote, err, out)
	}

	// WAS THE BRANCH EVER THIS COMMIT. Reachable is the wrong bar, and the gap
	// between the two is what a number can be taken through: merging any history
	// into the branch makes all of it reachable, for good, from a merge that
	// fast-forwards and needs no force — so a rule written on reachability admits
	// whatever was merged, which is the opposite of naming one line of development.
	//
	// The first-parent walk is the branch's own sequence of states: each step is
	// where the branch pointer stood, and a merge contributes the merge commit and
	// not the history it absorbed. That set is what a version number can order, so
	// that set is what may hold one.
	//
	// A full walk, because membership has no cheaper exact form and the trunk is
	// small — thousands of lines, tens of milliseconds, against a fetch that already
	// cost a second.
	out, _, err = git(ctx, dir, env, "rev-list", "--first-parent", line)
	if err != nil {
		return fmt.Errorf("lineage: cannot read the states of %s at %s, so %s cannot be shown to be one: %w: %s",
			a.Branch, a.Remote, sha, err, out)
	}
	for state := range strings.SplitSeq(out, "\n") {
		if state == sha {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is reachable from %s on %s but was never a state of it — the branch reached it by merging the history holding it, and a version number orders the states the branch passed through, not everything they brought with them",
		ErrForeign, sha, a.Branch, a.Remote)
}

// env is the environment every git subprocess here runs with, built from nothing but
// PATH so no credential and no tracing setting in the parent reaches the child.
//
// followRedirects=false is load-bearing rather than incidental: a forge answers a
// repository's former name with a redirect to whatever holds that name now, so
// following one would verify against a repository the caller did not name. Refusing
// redirects makes Remote mean the repository at Remote.
func (a Arbiter) env(home, token string) []string {
	// One arbiter is reached over one transport, and naming it leaves every other
	// one unreachable — including the transports that run a command rather than
	// speak a protocol.
	proto := "file"
	if strings.HasPrefix(a.Remote, "https://") {
		proto = "https"
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=" + proto,
		"LC_ALL=C",
	}
	// Refusing redirects is a property of the transport and belongs with the rest of
	// the transport's configuration, unconditionally. Attached instead to the branch
	// below, it would hold only while a credential happened to be set, and an
	// anonymous read of a public repository — which this is, whenever the arbiter
	// grants one — would follow a former name to whatever holds it now. A safety
	// property that depends on a credential being present is not a property.
	cfg := [][2]string{
		{"credential.helper", ""},
		{"http.followRedirects", "false"},
	}
	// A credential is presented over https only. A test arbiter is a local path with
	// no host to authenticate to, and any other transport would put the token
	// somewhere it was not meant to go.
	if token != "" && strings.HasPrefix(a.Remote, "https://") {
		cfg = append(cfg,
			[2]string{"http.extraHeader", "Authorization: Basic " +
				base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))},
		)
	}
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(cfg)))
	for i, kv := range cfg {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return env
}

// git runs one git subcommand and returns its output, its exit status and any error.
// The status is returned separately because merge-base answers this package's whole
// question with it. Arguments are a slice, never a shell string, so nothing in a sha
// can be read as a command; WaitDelay bounds the reaping of a child that ignores the
// context's kill.
func git(ctx context.Context, dir string, env []string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	code := cmd.ProcessState.ExitCode()
	return strings.TrimSpace(string(out)), code, err
}

// hex accepts the full object names git prints — 40 hex digits for sha1, 64 for
// sha256 — and refuses everything else, including the abbreviations git would
// otherwise resolve. A claim names one commit exactly; an abbreviation names
// whichever commit happens to be unambiguous today.
func hex(sha string) error {
	if len(sha) != 40 && len(sha) != 64 {
		return fmt.Errorf("%q is not a full commit name", sha)
	}
	for _, r := range sha {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return fmt.Errorf("%q is not a full commit name", sha)
		}
	}
	return nil
}
