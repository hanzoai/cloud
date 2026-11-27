package coding

import (
	"context"
	"fmt"
	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/account"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// deliver commits what the tool left behind and pushes it to the run's ref: the
// work of a run that was interrupted (keep) or looped after editing (#522). A run
// that finished commits after each turn and pushes once its checks are over (Run).
func (in sandbox) deliver(req RunRequest, branch, base string, out *RunResult, step func(name, msg, status string)) error {
	if err := in.commit(req, base, out, step); err != nil {
		return err
	}
	return in.push(req, branch, out, step)
}

// commit commits what the tool left behind and fills in what actually happened.
//
// The commit's EXIT CODE IS NOT READ. `git commit` with nothing staged fails, and
// that failure is indistinguishable from a tool that had already committed — so
// the question is answered by the sha instead, which is a fact rather than an
// opinion about one. When the tip has not moved, nothing changed, and nothing is
// pushed: a PR against a branch with no commits teaches a reviewer to ignore the
// agent, which is worse than the missing PR it replaces.
func (in sandbox) commit(req RunRequest, base string, out *RunResult, step func(name, msg, status string)) error {
	_ = in.lend(false)
	// A site build an earlier run's sandbox lost mid-way can have left its
	// wrapper in this tree (publish.go tidyNext); it is put back before staging.
	if _, err := in.do([]string{"sh", "-c", tidyNext}, "put back a site build's config"); err != nil {
		return err
	}
	if _, err := in.do([]string{"git", "add", "-A"}, "stage"); err != nil {
		return err
	}
	// A FILE GITHUB WOULD REFUSE IS LEFT OUT, not the whole push: one file over its
	// limit fails every byte of the run's work (hanzo-inc/cloud#450). It is
	// unstaged, kept out of a later stage, and named.
	big, err := in.do([]string{"sh", "-c", oversize, "sh", strconv.Itoa(maxFile)}, "leave out files too large to push")
	if err != nil {
		return err
	}
	if left := strings.TrimSpace(big.Stdout); left != "" {
		step("commit", "left out, over 100 MB: "+strings.ReplaceAll(left, "\n", ", "), "running")
	}
	// The identity rides the invocation for the same reason the credential does:
	// `git -c` before a subcommand is not written into the repository's config.
	commit := []string{"git", "-c", "user.name=" + agentName, "-c", "user.email=" + agentEmail, "commit", "--quiet"}
	if a := req.Author.line(); a != "" {
		commit = append(commit, "--author="+a)
	}
	// The message is made of the task's words, so it arrives on stdin and is in no
	// command line (argvFor).
	commit = append(commit, "-F", "-")
	if _, err := runIn(in.ctx, in.id, commit, in.ttl, in.session, message(req.Prompt, in.session), in.secrets()...); err != nil {
		return fmt.Errorf("coding: commit: %w", err)
	}

	tip, err := in.tip()
	if err != nil {
		return err
	}
	if tip == base {
		out.Changed, out.CommitSha, out.Diffstat = false, "", ""
		// A measurement, not a verdict: a build is asked once more after it (settle).
		step("done", "the repository is unchanged", "running")
		return nil // and Changed stays false, which is now a measurement
	}

	stat, err := in.do([]string{"git", "diff", "--stat", base, tip}, "diff")
	if err != nil {
		return err
	}
	// The subject of the run's first commit names the change in the harness's
	// own words, which makes a better proposal title than the prompt that asked
	// for it. A failed read costs the title, never the run.
	if log, lerr := in.do([]string{"git", "log", "--reverse", "--format=%s", base + ".." + tip}, "read the subject"); lerr == nil {
		out.Subject = firstLine(log.Stdout)
	}
	out.Changed, out.CommitSha, out.Diffstat = true, tip, strings.TrimSpace(stat.Stdout)
	return nil
}

// pushCommitted pushes only commits the agent already explicitly created on the
// branch (tip != base), leaving any uncommitted in-progress edits untouched in
// the surviving sandbox for clean resumption.
func (in sandbox) pushCommitted(req RunRequest, branch, base string, step func(name, msg, status string)) (RunResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(in.ctx), keepWait)
	defer cancel()
	_, _ = sandboxpeer.SandboxStop(ctx, &client.StopIn{ID: in.id})
	var out RunResult
	if base == "" {
		return out, nil
	}
	in.ctx = ctx
	_ = in.lend(false)
	tip, err := in.tip()
	if err != nil || tip == "" || tip == base {
		return out, err
	}
	stat, err := in.do([]string{"git", "diff", "--stat", base, tip}, "diff")
	if err != nil {
		return out, err
	}
	if log, lerr := in.do([]string{"git", "log", "--reverse", "--format=%s", base + ".." + tip}, "read the subject"); lerr == nil {
		out.Subject = firstLine(log.Stdout)
	}
	out.Changed, out.CommitSha, out.Diffstat = true, tip, strings.TrimSpace(stat.Stdout)
	err = in.push(req, branch, &out, step)
	return out, err
}

// push sends what commit measured to the run's ref, when it changed something.
func (in sandbox) push(req RunRequest, branch string, out *RunResult, step func(name, msg, status string)) error {
	if !out.Changed {
		return nil
	}
	// A LOCAL RUN KEEPS ITS COMMIT IN ITS WORKSPACE: a repository the person may
	// read and not write, or none at all, has nowhere its branch may go.
	if req.Local || strings.TrimSpace(req.Remote) == "" {
		step("done", "committed in the workspace", "running")
		in.mark(branch, out.CommitSha)
		return nil
	}
	step("push", "pushing "+branch, "running")
	if _, err := in.reach(req, pushArgv(req, branch), "push"); err != nil {
		out.Changed, out.CommitSha, out.Diffstat = false, "", ""
		return err
	}
	in.mark(branch, out.CommitSha)
	return nil
}

// mark records what the run kept of branch — pushed, or committed in a local
// workspace — so a follow-up in this tree measures its change from there
// (workspace). A mark that did not land costs a follow-up a larger diff, never a
// lost commit.
func (in sandbox) mark(branch, sha string) {
	_, _ = runIn(in.ctx, in.id, []string{"git", "-C", in.dir, "update-ref", pushedRef + branch, sha}, in.ttl, "", "")
}

// steered is how a run stopped for a new instruction ends: its task is stopped,
// its tree is left for the follow-up that resumes it, and what it changed so far
// is saved beside the run (artifact.go), uncommitted work and all. Nothing is
// committed or pushed (#553). The save is what keeps the work when the follow-up
// cannot resume this sandbox — it did not start, or the sandbox was retired.
func (in sandbox) steered(org, base string, step func(name, msg, status string)) RunResult {
	// ONE keepWait for the stop and the save, which is inside the time its
	// follow-up waits for it to let go (followWait).
	kept, done := context.WithTimeout(context.WithoutCancel(in.ctx), keepWait)
	defer done()
	_, _ = sandboxpeer.SandboxStop(kept, &client.StopIn{ID: in.id})
	in.ctx = kept
	var out RunResult
	if base != "" {
		in.save(org, base, &out, step)
	}
	return out
}

// maxFile is the largest file a run commits: GitHub refuses a push that carries a
// file over 100 MiB.
const maxFile = 100 << 20

// oversize unstages every staged file larger than $1 bytes, excludes it from a
// later stage, and prints its path: sized as git stored it, with git alone.
const oversize = `git -c core.quotePath=false diff --cached --diff-filter=AM --name-only | while IFS= read -r p; do ` +
	`s=$(git cat-file -s ":$p" 2>/dev/null) || continue; [ "$s" -gt "$1" ] || continue; ` +
	`git reset -q -- "$p" && printf '/%s\n' "$p" >> .git/info/exclude && printf '%s\n' "$p"; done`

// keep is deliver for a run interrupted mid-task. The run's own context is done,
// so the work is stopped and delivered on a context of its own — bounded, and
// still the run's tenant — before the lease ends as it always does.
//
// The task is stopped FIRST. The call that started it was cancelled, which is not
// a promise that the process in the pod was, and a commit taken while the harness
// is still writing would keep half a file.
func (in sandbox) keep(req RunRequest, branch, base string, step func(name, msg, status string)) (RunResult, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(in.ctx), keepWait)
	defer cancel()
	_, _ = sandboxpeer.SandboxStop(ctx, &client.StopIn{ID: in.id})
	var out RunResult
	if base == "" {
		return out, nil // no checkout, so nothing to keep
	}
	in.ctx = ctx
	step("keep", "keeping the work so far", "running")
	err := in.deliver(req, branch, base, &out, step)
	return out, err
}

// park is how a run the model API refused ends when the refusal can be waited out: its
// work is kept as an interrupted run's is (keep, save), the sandbox is given back, and
// the result says why it waits and when it may be asked again. It answers an error when
// the work could not be kept, and then nothing has been let go.
//
// The sandbox is ended only after the work is on the branch. A plan has no branch and
// keeps nothing; a run on no remote never reaches here (Run).
func (in sandbox) park(ctx context.Context, org, id, reason, note string, ran *client.Ran, req RunRequest, branch, base string, out RunResult,
	step func(name, msg, status string)) (RunResult, error) {
	if !req.Plan {
		kept, err := in.keep(req, branch, base, step)
		if err != nil {
			return out, err
		}
		out.Changed, out.CommitSha, out.Diffstat, out.Subject = kept.Changed, kept.CommitSha, kept.Diffstat, kept.Subject
	}
	in.save(org, base, &out, step)
	step("park", "the model API refused the run; its work is kept and its sandbox given back until that clears", "running")
	if _, err := sandboxpeer.SandboxEnd(context.WithoutCancel(ctx), &client.EndIn{ID: id}); err != nil {
		// Parked all the same: its work is on the branch, and apps/sandbox retires a sandbox
		// nobody resumes.
		slog.Warn("coding: a parked run's sandbox was not ended", "sandbox", id, "session", req.SessionID, "err", err)
	}
	out.Error = ""
	out.Park = &Parking{Reason: reason, At: clearsAt(ctx, reason)}
	switch reason {
	case agents.ParkFault:
		// A FAULT NAMES NO AUTHORITY AND NO TIME: the park counts it as a failed try, says what failed,
		// and is given its time by parked.
		out.Park = &Parking{Reason: reason, Retry: true, Note: note, Print: faultOf(ran), Progressed: in.answered != nil && *in.answered}
	case agents.ParkLimit:
		if out.Park.At.IsZero() {
			if wait, ok := transientLimit(ran); ok {
				// A transient rate or concurrency 429 (rate_limit_exceeded, paid_lane_full)
				// is not backed by an allowance or free-pool reset window; retry with
				// bounded backoff (<= transientRetryCap) instead of stranding or sleeping hours.
				out.Park.Retry = true
				out.Park.Note = note
				if wait > transientRetryCap {
					wait = transientRetryCap
				}
				if wait > 0 {
					out.Park.At = time.Now().Add(wait)
				}
			} else if at := limitResetsAt(ran); !at.IsZero() && !at.After(time.Now().Add(agents.MaxLife())) {
				out.Park.At = at
				out.Park.Note = note
			}
		}
	}
	return out, nil
}

// cloneWait bounds a repository clone so a stalled remote or huge tree cannot
// hold a sandbox open for the whole run's budget (hanzo-inc/cloud#522).
const cloneWait = 10 * time.Minute

// cloneArgv checks out the repo, quietly: a failed clone's first line is then
// its fault, and the first line is what a person is shown.
//
// ONE COMMIT, WITH ITS FILES. A run checks out every file of the commit, so a
// blob filter only moves the download: the clone fetches the trees, then asks the
// remote a second time, on a new connection with a second discovery, for every
// blob the checkout needs. Measured (hanzoai/vfs docs/git.md): five HTTP
// exchanges instead of three, the server's pack work 1.8 times, 35% more CPU
// and 21.7 s against 13.1 s on a 166,000-file tree, and 14.4 s against 10.2 s
// over HTTP on a 2.5 GB repository, the large one the filter was meant for.
func cloneArgv(req RunRequest) []string {
	// Disable LFS smudge only for this clone, including when git-lfs is not installed.
	// Do not persist these overrides: explicit later LFS pulls must still work.
	argv := append(gitAs(req), "-c", "filter.lfs.process=", "-c", "filter.lfs.smudge=", "-c", "filter.lfs.required=false", "clone", "--quiet", "--single-branch")
	if treeless(req) {
		// Every commit and no tree: a depth-1 clone has no merge base. What the agent reads later is
		// fetched with the read-only credential the checkout names (readHelper).
		argv = append(argv, "--filter=tree:0", "-c", "credential.https://github.com.helper="+readHelper(readPath))
	} else {
		argv = append(argv, "--depth", "1")
	}
	argv = append(argv, tuned...)
	if b := strings.TrimSpace(req.BaseBranch); b != "" {
		argv = append(argv, "-b", b)
	}
	return append(argv, req.Remote, ".")
}

// tuned is written into the new checkout's own configuration, so the clone and
// every git command the run and its agent make afterwards inherit it.
//
//	core.untrackedCache  the second and later `git status` read no directory that
//	                     did not change: 39% fewer system calls, and 4 directory
//	                     reads for 2,866.
//	gc.auto, maintenance.auto
//	                     a run's repository is disposable and its branch is
//	                     pushed. Garbage collection starts when an agent stages
//	                     thousands of files, and costs the commit that starts it
//	                     four to twelve times its own time.
//
// core.fsync is left at git's default on purpose: it already skips loose
// objects, and turning that on costs one fsync per file staged. checkout.workers
// is not set: it tripled the write calls of a clone and did not shorten it on
// local disk. It halves a clone only where each directory call costs 100 us, so
// it is for the volume mount to decide.
var tuned = []string{
	"-c", "core.untrackedCache=true",
	"-c", "gc.auto=0",
	"-c", "maintenance.auto=false",
}

// pushArgv writes ONE ref, named in full, and never forces.
//
// The refspec is explicit rather than `push origin <branch>` because the local
// name and the remote ref are then the same string we checked and the run was
// issued, with no configured remote and no push.default in between to resolve it
// into something else.
//
// It matters more here than it used to. The credential this replaces was
// per-REF: the forge refused any command naming another ref, so an explicit
// refspec was one of two independent statements of the same rule. A deploy key
// is per-REPOSITORY — Forgejo has no per-ref credential, and its token scopes
// are categories rather than repositories — so this refspec, and the branch
// check at the top of Run, are now what keep an honest run in its lane. The
// dishonest case is bounded by the repository instead of by the ref, and the
// integrity gate (the forge's own view of the branch, read afterwards) is what
// decides whether the work is proposed.
func pushArgv(req RunRequest, branch string) []string {
	return append(gitAs(req), "push", req.Remote, "HEAD:refs/heads/"+branch)
}

// gitAs is `git`, carrying the run's key — for this invocation alone.
//
// Three properties, and each one is the reason a line is there.
//
// IT DOES NOT PERSIST. `git -c` before the subcommand is not written into the new
// repository's config — unlike `git clone -c`, which is — so the checkout the
// model then edits names no identity file. The key is still on disk (ssh reads
// an identity from a path and from nowhere else), but it is under /tmp rather
// than inside the tree `git add -A` stages.
//
// IT IS THE ONLY IDENTITY OFFERED. IdentitiesOnly stops ssh walking an agent or
// a default ~/.ssh/id_* first; without it a sandbox image that happens to carry
// a key would authenticate as something other than this run.
//
// THE HOST IS PINNED, TO EXACTLY ONE KEY. StrictHostKeyChecking=yes against a
// known_hosts file CLOUD wrote means the run trusts the key cloud holds, not
// whatever answers on the sandbox's network — which is the one network in this
// system carrying untrusted output. `accept-new` would have been
// trust-on-first-use per run, which is not a pin at all. GlobalKnownHostsFile is
// sent to /dev/null in the same breath: ssh consults /etc/ssh/ssh_known_hosts as
// well, so an image (or a layer added to one) carrying an entry for the forge
// would be a second trusted key that our pin never sees.
//
// One function, because clone and push are the same act — reach that remote as
// this key — and a second spelling is a second place to get it wrong.
//
// OVER HTTPS THE CREDENTIAL IS A TOKEN. It is the first line of stdin (reach),
// exported the way the gateway credential is (carry), and read by a helper only
// this invocation names. The empty helper first drops every helper the image or
// the checkout configured, so none can store it; the helper answers github.com
// alone, so a url rewrite in the checkout cannot send it elsewhere; and hooks
// are off, because they would inherit it.
func gitAs(req RunRequest) []string {
	if strings.TrimSpace(req.Key) == "" {
		return []string{"git"}
	}
	if https(req.Remote) {
		return carry([]string{"git",
			"-c", "credential.helper=",
			"-c", "credential.https://github.com.helper=",
			"-c", `credential.https://github.com.helper=!f() { echo username=x-access-token; echo "password=$` + tokenVar + `"; }; f`,
			"-c", "core.hooksPath=/dev/null"}, tokenVar)
	}
	return []string{"git", "-c", "core.sshCommand=ssh -i " + keyPath +
		" -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes" +
		" -o UserKnownHostsFile=" + knownPath + " -o GlobalKnownHostsFile=/dev/null"}
}

// tokenVar holds a GitHub token for the one git invocation it is fed to.
const tokenVar = "HANZO_GIT_TOKEN"

// readPath is where the token the agent's git reads with is kept, and readHelper the checkout's credential
// helper for the file at path: github.com alone. GitHub refuses a push with that token; gitAs drops the helper
// for the commands that carry the run's own.
const readPath = "/tmp/hanzo-run/read"

func readHelper(path string) string {
	return `!f() { echo username=x-access-token; echo "password=$(cat ` + path + `)"; }; f`
}

// treeless reports whether the run's clone keeps every commit and no tree: a GitHub remote the
// agent holds a read-only credential for, so what it fetches later can be fetched.
func treeless(req RunRequest) bool { return https(req.Remote) && strings.TrimSpace(req.ReadKey) != "" }

// https reports whether a remote is reached with a token rather than a key.
func https(remote string) bool { return strings.HasPrefix(remote, "https://") }

// tip reads the checkout's current commit.
func (in sandbox) tip() (string, error) {
	ran, err := in.do([]string{"git", "rev-parse", "HEAD"}, "read the tip")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(ran.Stdout), nil
}

// do is a command whose failure is the RUN's failure.
//
// runIn answers a non-zero exit as data, deliberately — "the tests failed" and
// "the sandbox is broken" are different facts. For the commands here they are the
// same fact: a clone that did not clone or a push that did not push leaves
// nothing to report, so the exit code is read and the run stops.
func (in sandbox) do(argv []string, what string) (*client.Ran, error) {
	return in.exec(argv, "", what)
}

// reach is do for the two commands that talk to the remote. Over HTTPS the
// run's token is their stdin and no other command's (gitAs); over SSH the key
// is already a file (arm). When the token is short-lived (GitHub App token),
// reach refreshes it before the command when it nears expiry and retries once
// with a forced refresh if the remote refuses the token as expired.
func (in sandbox) reach(req RunRequest, argv []string, what string) (*client.Ran, error) {
	key := strings.TrimSpace(req.Key)
	if https(req.Remote) && req.RenewKey != nil {
		if fresh, err := req.RenewKey(in.ctx, false); err == nil && strings.TrimSpace(fresh) != "" {
			key = strings.TrimSpace(fresh)
			in.hide(key)
		}
	}
	stdin := ""
	if https(req.Remote) && key != "" {
		stdin = key + "\n"
	}
	ran, err := in.exec(argv, stdin, what)
	if err != nil && https(req.Remote) && req.RenewKey != nil && gitAuthFailed(err.Error()) {
		if fresh, rerr := req.RenewKey(in.ctx, true); rerr == nil && strings.TrimSpace(fresh) != "" && strings.TrimSpace(fresh) != key {
			key = strings.TrimSpace(fresh)
			in.hide(key)
			return in.exec(argv, key+"\n", what)
		}
	}
	return ran, err
}

func gitAuthFailed(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "authentication failed") ||
		strings.Contains(low, "invalid username or token") ||
		strings.Contains(low, "http basic: access denied") ||
		strings.Contains(low, "401")
}

func (in sandbox) exec(argv []string, stdin, what string) (*client.Ran, error) {
	ran, err := runIn(in.ctx, in.id, argv, in.ttl, in.session, stdin, in.secrets()...)
	if err != nil {
		return nil, fmt.Errorf("coding: %s: %s", what, in.scrub(err.Error()))
	}
	if ran.ExitCode != 0 {
		return nil, fmt.Errorf("coding: %s: exit %d: %s", what, ran.ExitCode,
			in.scrub(strings.TrimSpace(tail(ran.Stdout, ran.Stderr))))
	}
	return ran, nil
}

// message is what the commit says: the task, and the run that did it.
//
// The session is in the trailer because the commit outlives every other record of
// the run — the Slack thread scrolls away, the sandbox is reaped — and a commit
// nobody can trace back to the conversation that asked for it is an orphan in
// somebody's history.
func message(prompt, session string) string {
	m := headline(prompt)
	if m == "" {
		m = "chore: update workspace"
	}
	return m + "\n\nRun: " + session + "\n"
}

// scrub takes the grant out of anything that leaves the sandbox.
//
// git redacts the userinfo from the URLs it prints — verified, including through
// an insteadOf rewrite — and this does not rely on that. Everything here becomes a
// session event, a Slack line, a log field and a span; a credential is one string
// away from all four, and being sure costs four lines. It hangs off the sandbox
// rather than sitting loose so that every path out of a command goes through it
// without anyone having to remember.
func (in sandbox) scrub(s string) string {
	s = scrubHTTPUserinfo(s)
	if len(in.secrets()) == 0 {
		return s
	}
	// The key is multi-line PEM, so it is scrubbed BOTH whole and line by line: a
	// tool that echoes a file's contents with its own indentation, or a JSON
	// encoder that re-wraps the newlines, would leave a whole-string match
	// finding nothing while every secret line is still there. The armour lines
	// are skipped — they are the same public constant in every OpenSSH key and
	// redacting them only makes the output unreadable.
	var rep []string
	for _, sec := range in.secrets() {
		for line := range strings.SplitSeq(sec, "\n") {
			line = strings.TrimSpace(line)
			if len(line) < 16 || strings.HasPrefix(line, "-----") {
				continue
			}
			rep = append(rep, line, redacted)
		}
		rep = append(rep, sec, redacted)
	}
	return strings.NewReplacer(rep...).Replace(s)
}

// scrubHTTPUserinfo replaces any userinfo segment in http(s):// URLs with
// [redacted] so embedded tokens or passwords never leak in step or error lines.
func scrubHTTPUserinfo(s string) string {
	return rewriteHTTPUserinfo(s, redacted+"@")
}

// stripHTTPUserinfo removes any userinfo segment from an http(s):// URL before
// writing a remote URL into git config.
func stripHTTPUserinfo(s string) string {
	return rewriteHTTPUserinfo(s, "")
}

func rewriteHTTPUserinfo(s, replacement string) string {
	var out strings.Builder
	rem := s
	for {
		idx := -1
		schemeLen := 0
		low := strings.ToLower(rem)
		for _, sc := range []string{"https://", "http://"} {
			if i := strings.Index(low, sc); i >= 0 && (idx < 0 || i < idx) {
				idx, schemeLen = i, len(sc)
			}
		}
		if idx < 0 {
			if out.Len() == 0 {
				return s
			}
			out.WriteString(rem)
			return out.String()
		}
		authStart := idx + schemeLen
		authEnd := len(rem)
		for i := authStart; i < len(rem); i++ {
			switch rem[i] {
			case '/', '?', '#', ' ', '\t', '\r', '\n', '"', '\'':
				authEnd = i
				i = len(rem)
			}
		}
		authority := rem[authStart:authEnd]
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			out.WriteString(rem[:authStart])
			out.WriteString(replacement)
			out.WriteString(authority[at+1:])
		} else {
			out.WriteString(rem[:authEnd])
		}
		rem = rem[authEnd:]
	}
}

const redacted = "[redacted]"

// runIn runs one command in the sandbox and narrates it into the run's session.
//
// The session travels with the command rather than the output travelling back
// here, because the bytes are IN THE SANDBOX and this call does not return until
// the command is over. Streamed from where they are produced, a twenty-five
// minute agent edit loop is watchable; collected here, it is a silence.
func runIn(ctx context.Context, id string, argv []string, ttl int, session, stdin string, blind ...string) (*client.Ran, error) {
	ran, err := sandboxpeer.SandboxRun(ctx,
		&client.RunIn{ID: id, Argv: argv, TimeoutSec: ttl, Session: session, Stdin: stdin, Blind: blind})
	if err != nil {
		return nil, err
	}
	if ran == nil {
		return nil, fmt.Errorf("no result")
	}
	return ran, nil
}

// tail keeps the end of the output, which is where a failure says why. The cap
// is a Slack message's worth: this is read by a person in a thread, not archived.
func tail(stdout, stderr string) string {
	s := stdout
	if strings.TrimSpace(stderr) != "" {
		s = strings.TrimRight(s, "\n") + "\n" + stderr
	}
	const cap = 3000
	if len(s) > cap {
		return "…" + s[len(s)-cap:]
	}
	return s
}

// harness names the tool that ran the task, as a person would: dev when none
// was asked for.
func harness(tool string) string {
	if t := strings.TrimSpace(tool); t != "" {
		return t
	}
	return "dev"
}

// lastLine is the last line of output that says anything.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func statusOf(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}

// settleRepeated is called by the settle loop when a resume answered with a
// repeated call and nothing changed. It marks the run failed, names the repeat,
// and returns true so the caller can keep any partial work and stop.
func settleRepeated(out *RunResult, repeated string, scrub func(string) string) bool {
	if repeated == "" || out.Changed {
		return false
	}
	out.OK = false
	out.Error = "the agent repeated the same tool call while settling and changed nothing: " + shown(repeated, scrub)
	return true
}

// keptOnLoop copies the work an interrupted or looping run kept onto out, and
// names the branch that partial work landed on when anything changed.
func keptOnLoop(out *RunResult, kept RunResult, branch string) {
	out.Changed, out.CommitSha, out.Diffstat, out.Subject = kept.Changed, kept.CommitSha, kept.Diffstat, kept.Subject
	if kept.Changed {
		out.Error += "; its work so far is on " + branch
	}
}

// ── the funded-account gate ──────────────────────────────────────────────────

// minFundedDecimal is what the gate asks commerce to authorize for a run: about
// fifteen minutes of compute, enough that no honest run is refused for a rounding
// error. A constant rather than a catalog read, because the real charge is metered
// as the lease runs and a gate that predicted it would be a second pricing
// implementation.
const minFundedDecimal = "0.04" // ~15 min at ~$0.15/hour

// affordable asks COMMERCE whether org can pay for minFundedDecimal of compute,
// and refuses only what the lease could not answer for itself: a spent cap, a
// refusal commerce states, a gate that cannot be asked.
//
// AN EMPTY WALLET IS NOT REFUSED HERE. The lease is the free lane's (apps/sandbox
// prepaid.go): a plan's free sandboxes serve a wallet that cannot cover the
// first hour, bill nobody and end within the hour, and the lease refuses with the
// price and the count once they are used. Refusing NoFunds here as well closed the
// free lane in front of it, so a free plan could start no run at all.
//
// It asks the gate rather than reading a balance and doing arithmetic here.
// finance_authorize is labelled "the prepaid gate" in plane.go and it already
// knows things a subtraction does not: spend caps, which is why Verdict carries
// NoFunds and CapSpent as SEPARATE bits — one says add money, the other says
// wait for the period to roll. A balance comparison in this file would be a
// second pricing implementation that can disagree with the first.
//
// The org is stated on a DETACHED context because commerce takes the org from
// the CALLER identity and never from an argument, and a Runner has no inbound
// request to carry one. cloud.For is read only where no request is behind the
// ctx — on a request-bearing one it is a silent no-op that cost four deploys.
//
// It fails CLOSED on an unreadable verdict, and plane.go says why in the type
// itself: Reason "is an upstream failure the caller must treat as UNKNOWN and
// fail closed on, and never read as permission." Failing open costs a pod held
// for four hours by an account that cannot pay; failing closed costs a retry.
func affordable(ctx context.Context, org string) error {
	if isLocalDev() {
		return nil
	}
	payer := cloud.PayerOf(ctx).Wallet
	payerOrg, subject := org, org
	if !payer.Zero() {
		payerOrg, subject = payer.Org(), payer.Subject()
	} else {
		payer = account.Org(org)
	}
	if cloud.TierAllows(context.Background(), payerOrg, payer) {
		return nil
	}
	v, err := plane.Call[client.AuthorizeIn, client.Verdict](
		cloud.For(context.Background(), payerOrg), "commerce", client.FinanceAuthorize,
		&client.AuthorizeIn{
			Subject: subject,
			Amount:  client.Money{Decimal: minFundedDecimal, Currency: "usd"},
			Service: "sandbox",
		})
	if err != nil {
		return fmt.Errorf("compute is not free and the prepaid gate is unreachable: %w", err)
	}
	if v == nil {
		return fmt.Errorf("compute is not free and the prepaid gate answered nothing")
	}
	switch {
	case v.OK:
		return nil
	case v.CapSpent:
		return unpayable{fmt.Errorf("this org has spent its cap for the period; a sandbox cannot start until it rolls over")}
	case v.NoFunds:
		return nil
	default:
		return fmt.Errorf("the prepaid gate refused a sandbox: %s", v.Reason)
	}
}

// scienceScopes returns the confined autonomous-science delegation sub-scopes
// ("research", "workflow:gpu") only when a run targets a known science/ML
// repository slug or carries explicit operator/agent science directives in
// Instructions / DeveloperInstructions — never from free-form Prompt text or
// substring matches inside repository names.
func scienceScopes(req RunRequest) []string {
	owner, repo := remoteOwnerRepo(req.Remote)
	if isScienceRepo(owner, repo) {
		return []string{"research", "workflow:gpu"}
	}
	directives := strings.ToLower(req.Instructions + "\n" + req.DeveloperInstructions)
	for _, marker := range []string{
		"/v1/research/",
		"/v1/workflow/namespaces/gpu-jobs/",
		"research.bench",
	} {
		if strings.Contains(directives, marker) {
			return []string{"research", "workflow:gpu"}
		}
	}
	if hasStandaloneHandle(directives, "@sci") {
		return []string{"research", "workflow:gpu"}
	}
	return nil
}

func remoteOwnerRepo(remote string) (string, string) {
	s := strings.TrimSpace(strings.ToLower(remote))
	if s == "" {
		return "", ""
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
		if slash := strings.IndexByte(s, '/'); slash >= 0 {
			s = s[slash+1:]
		} else {
			return "", ""
		}
	} else if colon := strings.IndexByte(s, ':'); colon >= 0 && !strings.Contains(s[:colon], "/") {
		s = s[colon+1:]
	}
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

func isScienceRepo(owner, repo string) bool {
	if owner == "" || repo == "" {
		return false
	}
	if owner == "zenlm" {
		return true
	}
	switch owner {
	case "hanzoai", "hanzo-inc", "hanzo":
	default:
		return false
	}
	switch repo {
	case "ml", "kai", "zen", "science", "research":
		return true
	}
	return strings.HasPrefix(repo, "kai-") ||
		strings.HasPrefix(repo, "ml-") ||
		strings.HasSuffix(repo, "-research")
}

func hasStandaloneHandle(s, handle string) bool {
	for {
		idx := strings.Index(s, handle)
		if idx < 0 {
			return false
		}
		beforeOK := idx == 0 || !isIdentByte(s[idx-1])
		end := idx + len(handle)
		afterOK := end == len(s) || !isIdentByte(s[end])
		if beforeOK && afterOK {
			return true
		}
		s = s[idx+1:]
	}
}

func isIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}
