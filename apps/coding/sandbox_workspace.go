package coding

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/hanzoai/cloud/client"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

// workspace readies the run's tree in the sandbox and answers the commit it
// starts from, which the run's branch is cut from and its change is measured
// against. resumed is a sandbox an earlier run left: its tree is worked in as it
// is. Otherwise the repository is cloned, or, for a run on none, an empty one is
// made.
//
// empty is a clone that holds no file but a README, a licence or a git
// setting: a placeholder rather than code, which is how a run that changed
// nothing is told from one that had nothing to work on (coding.go hollow).
func (in sandbox) workspace(req RunRequest, branch string, resumed bool, step func(name, msg, status string)) (string, bool, error) {
	// The run's own files — its key, its answer — live under /tmp, never in the
	// checkout; an earlier run's answer is not this one's.
	if _, err := in.do([]string{"sh", "-c", `mkdir -p "$(dirname "$1")" && rm -f "$1"`, "sh", answerPath}, "ready the run's files"); err != nil {
		return "", false, err
	}
	// The key has to be a FILE before git can use it — ssh reads an identity from
	// a path and from nowhere else — so this is the one moment the run's secret is
	// written down, and it is written the only way that does not publish it. See
	// [sandbox.arm]. /tmp does not survive a park, so a resumed tree is armed too.
	if strings.TrimSpace(req.Remote) != "" {
		if err := in.arm(req); err != nil {
			return "", false, err
		}
	}
	// The workdir is a volume the kubelet mounts root-owned (fsGroup chowns its
	// group, not its owner), and git refuses a checkout another uid owns. The run
	// owns this sandbox, so it tells its own git the checkout is its.
	if _, err := in.do([]string{"git", "config", "--global", "--add", "safe.directory", in.dir}, "trust the checkout"); err != nil {
		return "", false, err
	}
	cloned := false
	switch {
	case resumed && in.checkedOut():
		// What a site build the earlier run's sandbox lost mid-way left is put
		// back before anything works in the tree (publish.go tidyNext).
		if _, err := in.do([]string{"sh", "-c", tidyNext}, "put back a site build's config"); err != nil {
			return "", false, err
		}
		step("workspace", "working in the tree the earlier run left", "ok")
	case strings.TrimSpace(req.Remote) != "":
		cloned = true
		step("clone", "cloning "+scrubHTTPUserinfo(req.Remote), "running")
		// The clone narrates into the session like everything else. It is the step
		// that most often hangs — a big repo, a slow forge — and a watcher seeing
		// git count objects knows the difference between slow and stuck. Bounded by
		// cloneWait so a stuck clone cannot spend the whole run's hour.
		cbox := in
		cbox.ttl = min(in.ttl, int(cloneWait/time.Second))
		if err := cbox.cloneRemote(&req); err != nil {
			if emptyRemoteCloneErr(err) {
				err = in.initEmptyRemote(req)
			}
			if err != nil {
				return "", false, err
			}
		}
	default:
		step("workspace", "starting in an empty workspace", "running")
		if _, err := in.do([]string{"git", "init", "-q", "-b", "main", "."}, "make the workspace"); err != nil {
			return "", false, err
		}
		if _, err := in.do([]string{"git", "-c", "user.name=" + agentName, "-c", "user.email=" + agentEmail,
			"commit", "-q", "--allow-empty", "-m", "Workspace"}, "make the workspace"); err != nil {
			return "", false, err
		}
	}
	// The run's branch exists BEFORE the first edit, so a tool that commits for
	// itself commits onto the run's branch and never onto the base. A follow-up's
	// checkout is already on it — the tree it resumed, or a clone of that branch —
	// and is left there.
	//
	// A branch the checkout already holds is switched to, not cut again.
	if !in.on(branch) {
		argv := []string{"git", "switch", "-c", branch}
		existingBranch := in.holds(branch)
		if existingBranch {
			argv = []string{"git", "switch", branch}
		}
		if _, err := in.do(argv, "branch"); err != nil {
			if !existingBranch {
				return "", false, err
			}
			if _, merr := in.do([]string{"git", "switch", "-m", branch}, "branch"); merr != nil {
				return "", false, err
			}
		}
	}
	base, err := in.tip()
	if err != nil && cloned && unbornHeadErr(err) {
		if _, cerr := in.do([]string{"git", "-c", "user.name=" + agentName, "-c", "user.email=" + agentEmail,
			"commit", "-q", "--allow-empty", "-m", "Workspace"}, "make the workspace"); cerr == nil {
			base, err = in.tip()
		}
	}
	if err != nil {
		return "", false, err
	}
	// Where the branch started is kept beside it, so what it changed can be read
	// from the tree later (work.go) without asking anything the run was told. It is
	// written once: a follow-up on the same branch leaves it where the branch began.
	//
	// A TREE AN EARLIER RUN LEFT is measured from what the remote holds of it: the
	// commit last pushed to the branch, or where the branch started when nothing
	// was. A run stopped for a new instruction leaves its commits unpushed for its
	// follow-up (#553), and measured from the tip they were not this run's change,
	// so nothing pushed them.
	if !in.has(baseRef + branch) {
		if _, err := in.do([]string{"git", "update-ref", baseRef + branch, base}, "mark where the branch starts"); err != nil {
			return "", false, err
		}
	} else if resumed || req.Resumed {
		if b, err := in.rev(pushedRef + branch); err == nil && b != "" {
			base = b
		} else if b, err := in.rev(baseRef + branch); err == nil && b != "" {
			base = b
		}
	}
	// THE HARNESS'S SCRATCH IS NOT THE RUN'S WORK. dev writes a large command's
	// output to .code/agents/<n>/exec-call-<id>.txt, and its user files to
	// .code/users/, under its working directory, which is this checkout, so `git
	// add -A` would commit and push them and the run's artifacts would list them:
	// a measured run saved thirteen such files as its work. Those two directories
	// are excluded for this checkout alone, as an install's output is; anything
	// else a repository keeps under .code is its own.
	// Then whether the clone holds any file that is not a README, a licence or a
	// git setting — asked of git alone, with shell builtins, so it holds on any
	// image that can clone.
	// The package caches an agent's own install writes into the checkout are kept
	// out the same way (unkept). Each line is added once, so a resumed tree is not
	// given them twice.
	ls, err := in.do(append([]string{"sh", "-c", excluding, "sh"}, unkept...), "keep the harness's scratch out of the run's commit")
	if err != nil {
		return "", false, err
	}
	return base, cloned && strings.TrimSpace(ls.Stdout) == "0", nil
}

// unkept is what a run never commits, as .git/info/exclude lines for this
// checkout alone: dev's scratch, then the package caches an install leaves in the
// tree. A pnpm store committed whole was a push GitHub refused — one of its files
// was 127 MB — and an HTTP 408 before that (hanzo-inc/cloud#450). A tracked path
// is not affected, so a repository that commits one of these keeps doing so.
var unkept = []string{"/.code/agents/", "/.code/users/",
	".pnpm-store/", "node_modules/", ".npm/", ".cache/", "__pycache__/", ".venv/"}

// excluding adds each of its arguments to .git/info/exclude once, then prints 1
// when the checkout holds a file that is not boilerplate and 0 when it holds none.
const excluding = `mkdir -p .git/info && f=.git/info/exclude && : >> "$f" && ` +
	`for p; do seen=; while IFS= read -r l; do [ "$l" != "$p" ] || seen=1; done < "$f"; [ -n "$seen" ] || printf '%s\n' "$p" >> "$f"; done && ` +
	`git ls-files -- ` + boilerplate + ` | { IFS= read -r f && echo 1 || echo 0; }`

// boilerplate excludes, as git pathspecs, the files every repository carries and
// none is made of: a README, a licence, a notice, a git setting — by its whole
// name, so src/readme_parser.go is code.
const boilerplate = `':(exclude,icase,glob)**/readme' ':(exclude,icase,glob)**/readme.*' ':(exclude,icase,glob)**/license' ':(exclude,icase,glob)**/license.*' ':(exclude,icase,glob)**/licence' ':(exclude,icase,glob)**/licence.*' ':(exclude,icase,glob)**/copying' ':(exclude,icase,glob)**/copying.*' ':(exclude,icase,glob)**/notice' ':(exclude,icase,glob)**/notice.*' ':(exclude,icase,glob)**/licen[cs]e-*' ':(exclude,glob)**/.gitignore' ':(exclude,glob)**/.gitattributes' ':(exclude,glob)**/.gitkeep'`

// baseRef is where the commit a run's branch starts from is kept, per branch.
const baseRef = "refs/hanzo/base/"

// headsRef is where a local branch is kept.
const headsRef = "refs/heads/"

// pushedRef is the commit a run last pushed to a branch, per branch: what the
// remote holds of a tree a follow-up resumes.
const pushedRef = "refs/hanzo/pushed/"

// on reports whether the checkout is already on branch: the tree a follow-up
// resumed, or a clone of the branch it carries on.
func (in sandbox) on(branch string) bool {
	ran, err := runIn(in.ctx, in.id, []string{"git", "-C", in.dir, "symbolic-ref", "--short", "-q", "HEAD"}, in.ttl, "", "")
	return err == nil && ran.ExitCode == 0 && strings.TrimSpace(ran.Stdout) == branch
}

// holds reports whether the checkout holds branch as a local branch. It reads the
// ref's name back rather than the exit of a lookup.
func (in sandbox) holds(branch string) bool {
	ran, err := runIn(in.ctx, in.id, []string{"git", "-C", in.dir, "for-each-ref", "--format=%(refname)", headsRef + branch}, in.ttl, "", "")
	return err == nil && ran.ExitCode == 0 && strings.TrimSpace(ran.Stdout) == headsRef+branch
}

// has reports whether the checkout holds ref.
func (in sandbox) has(ref string) bool {
	ran, err := runIn(in.ctx, in.id, []string{"git", "-C", in.dir, "rev-parse", "--verify", "-q", ref}, in.ttl, "", "")
	return err == nil && ran.ExitCode == 0
}

// rev returns the commit SHA that ref points to in the checkout.
func (in sandbox) rev(ref string) (string, error) {
	ran, err := runIn(in.ctx, in.id, []string{"git", "-C", in.dir, "rev-parse", "--verify", "-q", ref}, in.ttl, "", "")
	if err != nil {
		return "", err
	}
	if ran.ExitCode != 0 {
		return "", fmt.Errorf("git rev-parse %s: exit %d", ref, ran.ExitCode)
	}
	return strings.TrimSpace(ran.Stdout), nil
}

// checkedOut reports whether the sandbox's workdir holds a checkout.
func (in sandbox) checkedOut() bool {
	ran, err := runIn(in.ctx, in.id, []string{"git", "-C", in.dir, "rev-parse", "--verify", "-q", "HEAD"}, in.ttl, "", "")
	return err == nil && ran.ExitCode == 0
}

// cloneRemote clones req.Remote into the sandbox workdir. When req.BaseBranch is
// an unpushed agent/* branch or the conventional "main" default on a repository
// whose upstream HEAD points elsewhere (e.g. master) or has no commits yet, it
// falls back to cloning remote HEAD without -b.
func (in sandbox) cloneRemote(req *RunRequest) error {
	_, err := in.reach(*req, cloneArgv(*req), "clone")
	if err == nil {
		return nil
	}
	if canFallbackCloneBranch(req.BaseBranch) && remoteBranchMissing(err) {
		req.BaseBranch = ""
		_, err = in.reach(*req, cloneArgv(*req), "clone")
	}
	return err
}

func canFallbackCloneBranch(branch string) bool {
	b := strings.TrimSpace(branch)
	return strings.HasPrefix(b, agentPrefix) || b == "main"
}

func remoteBranchMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Remote branch") ||
		strings.Contains(msg, "couldn't find remote ref")
}

func emptyRemoteCloneErr(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "remote head refers to nonexistent ref") ||
		strings.Contains(low, "appear to have cloned an empty repository") ||
		strings.Contains(low, "does not have any commits yet")
}

func unbornHeadErr(err error) bool {
	if err == nil {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "unknown revision or path not in the working tree") ||
		strings.Contains(low, "needed a single revision") ||
		strings.Contains(low, "does not have any commits yet")
}

// initEmptyRemote initializes a fresh git working tree wired to req.Remote when
// git clone could not materialize a checkout for a brand-new empty remote.
func (in sandbox) initEmptyRemote(req RunRequest) error {
	if _, err := in.do([]string{"sh", "-c", "rm -rf .git && git init -q -b main ."}, "make the workspace"); err != nil {
		return err
	}
	if u := stripHTTPUserinfo(strings.TrimSpace(req.Remote)); u != "" {
		if _, err := in.do([]string{"git", "remote", "add", "origin", u}, "make the workspace"); err != nil {
			return err
		}
	}
	for i := 0; i+1 < len(tuned); i += 2 {
		if k, v, ok := strings.Cut(tuned[i+1], "="); ok {
			if _, err := in.do([]string{"git", "config", k, v}, "make the workspace"); err != nil {
				return err
			}
		}
	}
	if treeless(req) {
		if _, err := in.do([]string{"git", "config", "credential.https://github.com.helper", readHelper(readPath)}, "make the workspace"); err != nil {
			return err
		}
	}
	return nil
}

// answer is the harness's last message: what it printed, when it wrote no
// events; else the file it wrote it to, or its last message event. final is
// whether it is a last message at all: false when the harness wrote an empty
// file, which is a turn that ended without one.
func (in sandbox) answer(said outcome, stdout string) (string, bool) {
	if said.plain {
		return stdout, true
	}
	if ran, err := runIn(in.ctx, in.id, []string{"cat", answerPath}, in.ttl, "", "", in.secrets()...); err == nil && ran.ExitCode == 0 {
		if s := strings.TrimSpace(ran.Stdout); s != "" {
			return s, true
		}
		return said.answer, false
	}
	return said.answer, said.answer != ""
}

// save keeps what the run left beside it (artifact.go): its change, uploaded
// from the sandbox, and the ports it serves. A save that fails is said, and costs
// the run nothing: its work is in its workspace either way.
func (in sandbox) save(org, base string, out *RunResult, step func(name, msg, status string)) {
	arts, commits, err := in.store(org, base, step)
	if err != nil {
		step("artifacts", in.scrub(err.Error()), "error")
	}
	out.Artifacts, out.Commits = append(arts, in.previews()...), commits
}

// ── the work leaves the sandbox ──────────────────────────────────────────────
//
// Everything above is how a run HAPPENS; this is how it SURVIVES. Without it the
// rest is a model talking to itself: the sandbox is per-run and reaped, so an
// edit that stays in the checkout is deleted by the lease ending. The Runner did
// lease → clone → run → end and threw the work away.
//
// It is four facts and one rule.
//
//	the branch   is the one cloud issued for the session, created before the
//	             first edit.
//	the change   is the difference between two object ids — what we checked out
//	             and what is there now. Not a status parse and not a guess: it is
//	             the only reading that stays true both when a tool leaves the tree
//	             dirty and when it commits for itself, and a run that reports "no
//	             changes" because it never looked is the failure this file was.
//	the tip      is what rev-parse says AFTER the commit, so the sha we report is
//	             one that exists.
//	the diffstat is measured over exactly that range, so what a reviewer reads and
//	             what was pushed are the same thing.
//
//	THE RULE: the credential is applied to ONE INVOCATION and never recorded.

// agentPrefix is the machine namespace every branch a run writes is under, built
// by BranchFor and AgentBranch alike. apps/git states the same rule over full
// refs (refpolicy.go agentRefPrefix) and the two are deliberately independent:
// this one keeps an honest run inside its lane, that one keeps a compromised one
// there.
const agentPrefix = "agent/"

// Who made the commit. The agent is the COMMITTER of every run's commit, and
// the person the run is for is its AUTHOR in a workspace the platform made
// (author.go) — each in the field git keeps for them, so the history says both
// who asked for a change and what made it. Anywhere else, and with no person to
// name, the agent is both.
const (
	agentName  = "hanzo-agent"
	agentEmail = "agent@hanzo.ai"
)

// sandbox is one leased sandbox, addressed. It carries what every command of a
// run needs — where, for how long, who is watching, and the grant that must never
// come back out — so no call site repeats them and no error path can forget the
// last one.
type sandbox struct {
	ctx     context.Context
	id      string
	dir     string // the sandbox's workdir, where the checkout is
	ttl     int
	session string
	// key is the run's private key, the credential (cred, below) is the gateway token
	// and ticket is the run's ask credential (key.go). All are held to be
	// BLINDED, never to be used: the copy git reads is the file [sandbox.arm]
	// wrote, and the copies the harness reads arrived on its stdin. They are the
	// strings that must never appear in a session event, a Slack line or a log.
	key, ticket string
	// cred is the run's model credential (key.go): what a turn starts with, and every
	// token it has held, which are blinded.
	cred *bearer
	// env are the values of the codebase's secrets (environment.go), blinded like
	// the three above: they reach commands through envPath, and never come back out.
	env []string
	// grant are the values of a site upload grant's fields (publish.go), blinded
	// once the grant is in the sandbox.
	grant []string
	// extraSecrets holds any refreshed tokens minted mid-run (e.g. renewed GitHub
	// tokens) so every output stream blinds them alongside key.
	secretMu     *sync.Mutex
	extraSecrets *[]string
	// beat is told, while a harness turn runs, what it is doing (beating); nil
	// says nothing.
	beat func(line string)
	// onTurnSteer registers a live `dev queue` steerer for the active turn; nil
	// when the harness does not support in-turn queueing.
	onTurnSteer func(func(string) bool)
	// knownThread holds the active dev thread UUID across turns in this run.
	knownThread *string
	// readKey is the token the agent's own git reads with, renewRead refreshes it, and lent is
	// the one last written to readPath (lend). It is blinded like key.
	readKey   string
	renewRead func(ctx context.Context, force bool) (string, error)
	lent      *string
	// answered is set when a harness turn of this run ended without error (turn).
	answered *bool
}

// secrets is what every command of this run must never publish.
//
// It travels WITH each command (client.RunIn.Blind) rather than being applied to
// what comes back, because a sandbox narrates straight into the session as bytes
// are produced — that stream never passes through this process, so a scrub here
// would run minutes after the secret was already delivered to a durable event
// store, an SSE feed and a chat thread. apps/sandbox/blind.go states the rest.
func (in sandbox) secrets() []string { return in.secretsOf(in.cred.known()) }

// secretsOf is secrets with the model credential's tokens given, so that a command fed the
// credential of one moment is blinded against the tokens of that same moment.
func (in sandbox) secretsOf(creds []string) []string {
	var out []string
	if strings.TrimSpace(in.key) != "" {
		out = append(out, in.key)
	}
	out = append(out, creds...)
	if strings.TrimSpace(in.ticket) != "" {
		out = append(out, in.ticket)
	}
	var extra []string
	if in.extraSecrets != nil {
		if in.secretMu != nil {
			in.secretMu.Lock()
		}
		extra = slices.Clone(*in.extraSecrets)
		if in.secretMu != nil {
			in.secretMu.Unlock()
		}
	}
	for _, v := range slices.Concat(in.env, in.grant, extra) {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// Where the run's credential lives inside the sandbox.
//
// Under /tmp and not the checkout: the checkout is what the model edits and what
// `git add -A` stages, so a key kept there is one careless commit away from
// being pushed to the branch this very grant opens.
const (
	keyPath   = "/tmp/hanzo-run/key"
	knownPath = "/tmp/hanzo-run/known_hosts"
	// envPath holds the codebase's secrets as shell exports, and startLog is what
	// the start command writes. Both under /tmp for the reason the key is.
	envPath  = "/tmp/hanzo-run/env"
	startLog = "/tmp/hanzo-run/start.log"
)

// prepare applies a codebase's environment to the checkout (environment.go), and
// answers what the agent is told about it ahead of its task.
//
// THE SECRETS ARRIVE ON STDIN, into a file only this uid can read, as the key does
// (arm): argv is public inside the pod and echoed into the run's narration. Every
// later command that needs them sources the file (sourced), and every stream out of
// every command is blinded against their values (secrets).
//
// A FAILED INSTALL DOES NOT FAIL THE RUN. The step says it failed, with the
// script's last word, and the agent is told — an install that broke is often the
// very thing it was asked to fix, and a run refused here could not fix it. A
// transport failure is different: the sandbox did not answer, and nothing after
// this would either.
func (in sandbox) prepare(env Prepared, step func(name, msg, status string)) (string, error) {
	var brief strings.Builder
	if len(env.Missing) > 0 {
		step("secrets", "not set: "+strings.Join(env.Missing, ", "), "error")
		brief.WriteString("These environment variables are named on this codebase and not set: " +
			strings.Join(env.Missing, ", ") + ".\n")
	}
	if len(env.Env) > 0 {
		step("secrets", "exporting "+strings.Join(slices.Sorted(maps.Keys(env.Env)), ", "), "running")
		if _, err := in.exec([]string{"sh", "-c", `umask 077 && mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", envPath},
			exports(env.Env), "carry the environment's secrets"); err != nil {
			return "", err
		}
		step("secrets", "exported", "ok")
	}
	if install := strings.TrimSpace(env.Install); install != "" {
		step("install", "running the environment's install script", "running")
		ran, err := runIn(in.ctx, in.id, in.sourced([]string{"sh", "-c", install}), min(in.ttl, installTimeout), in.session, "", in.secrets()...)
		if err != nil {
			return "", fmt.Errorf("coding: install: %s", in.scrub(err.Error()))
		}
		// WHAT THE INSTALL LEFT IS NOT THE RUN'S WORK. deliver stages with `git add
		// -A`, so a dependency tree the repository does not ignore would be
		// committed and pushed as the agent's change. Every untracked path the
		// install created is excluded for this checkout alone — .git/info/exclude,
		// never the tracked .gitignore — so the agent's own files still land.
		if _, err := in.do([]string{"sh", "-c",
			`git ls-files --others --exclude-standard --directory | sed 's|^|/|' >> .git/info/exclude`},
			"keep the install out of the run's commit"); err != nil {
			return "", err
		}
		if ran.ExitCode != 0 {
			last := lastLine(in.scrub(tail(ran.Stdout, ran.Stderr)))
			step("install", fmt.Sprintf("exit %d: %s", ran.ExitCode, last), "error")
			fmt.Fprintf(&brief, "This codebase's install script failed with exit %d. Its last line: %s\n", ran.ExitCode, last)
		} else {
			step("install", "installed", "ok")
			brief.WriteString("This codebase's install script has already run in this checkout.\n")
		}
	}
	if start := strings.TrimSpace(env.Start); start != "" {
		step("start", "starting "+firstLine(start), "running")
		// Detached from this command's streams, so the exec returns while the
		// program keeps running for the rest of the lease.
		if _, err := in.exec(in.sourced([]string{"sh", "-c", `nohup sh -c "$1" </dev/null >"$2" 2>&1 &`, "sh", start, startLog}),
			"", "start"); err != nil {
			step("start", in.scrub(err.Error()), "error")
		} else {
			step("start", "running in the background; output in "+startLog, "ok")
			fmt.Fprintf(&brief, "This codebase's start command is running in the background (%s); its output is in %s.\n", firstLine(start), startLog)
		}
	}
	if brief.Len() == 0 {
		return "", nil
	}
	return brief.String() + "\n", nil
}

// userFiles is where an agent harness reads its user's own instructions and its
// user's skills, as the sandbox's shell resolves them: the variable the harness
// reads first, else its default. Both are constants, never a caller's text, so
// they are spelled into the scripts that write there.
//
//	dev     ${DEV_HOME:-$HOME/.hanzo/dev}/AGENTS.md          $HOME/.agents/skills
//	codex   ${CODEX_HOME:-$HOME/.codex}/AGENTS.md            $HOME/.agents/skills
//	claude  ${CLAUDE_CONFIG_DIR:-$HOME/.claude}/CLAUDE.md    ${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills
//
// dev is codex underneath with its own home (hanzo-config product_home, which
// becomes CODEX_HOME), and codex reads the global AGENTS.md in its home and a
// user's skills from ~/.agents/skills, the Agent Skills user root. claude reads
// CLAUDE.md and skills/ in its config directory.
func userFiles(tool string) (instructions, skills string) {
	switch strings.TrimSpace(tool) {
	case "claude":
		return `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/CLAUDE.md`, `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/skills`
	case "codex":
		return `${CODEX_HOME:-$HOME/.codex}/AGENTS.md`, `$HOME/.agents/skills`
	default:
		return `${DEV_HOME:-$HOME/.hanzo/dev}/AGENTS.md`, `$HOME/.agents/skills`
	}
}

// equip hands an agent harness what its person and its org set up for it,
// where the harness reads its user's own (userFiles): the person's instructions
// (prefs.go) and the org's skills (kit.go). The org's MCP servers ride the
// harness's own argv (argvFor), at the run's own address for each.
//
// THE INSTRUCTIONS ARE ADDED, NOT WRITTEN OVER: an image that carries a global
// instructions file keeps it, and the person's follow it. A skill of the same
// name as one the image carries is the org's in its own run.
//
// Every write is a command whose failure is the run's: an agent started without
// what its person set would be a different run from the one they asked for.
func (in sandbox) equip(req RunRequest, step func(name, msg, status string)) error {
	instructions, skills := userFiles(req.Tool)
	if req.Instructions != "" {
		step("instructions", "adding your instructions to "+harness(req.Tool)+"'s "+path.Base(instructions), "running")
		if _, err := in.exec([]string{"sh", "-c",
			`f="` + instructions + `" && mkdir -p "$(dirname "$f")" && { if [ -s "$f" ]; then echo; fi; cat; } >> "$f"`},
			req.Instructions+"\n", "add your instructions"); err != nil {
			return err
		}
		step("instructions", "added", "ok")
	}
	if len(req.Kit.Skills) > 0 {
		names := make([]string, 0, len(req.Kit.Skills))
		for _, sk := range req.Kit.Skills {
			names = append(names, sk.Name)
		}
		step("skills", "carrying "+strings.Join(names, ", "), "running")
		archive, err := skillArchive(req.Kit.Skills)
		if err != nil {
			return err
		}
		if _, err := in.exec([]string{"sh", "-c", `d="` + skills + `" && mkdir -p "$d" && base64 -d | tar -x -f - -C "$d"`},
			archive, "write the org's skills"); err != nil {
			return err
		}
		step("skills", "carried", "ok")
	}
	if len(req.Kit.Omitted) > 0 {
		step("skills", "left out, over what a run carries: "+strings.Join(req.Kit.Omitted, ", "), "error")
	}
	if len(req.Kit.Servers) > 0 {
		names := make([]string, 0, len(req.Kit.Servers))
		for _, s := range req.Kit.Servers {
			names = append(names, s.Name)
		}
		step("mcp", "the org's MCP servers: "+strings.Join(names, ", "), "ok")
	}
	if len(req.Kit.Excess) > 0 {
		step("mcp", "left out, over what a run carries: "+strings.Join(req.Kit.Excess, ", "), "error")
	}
	return nil
}

// skillArchive is the org's skills as the command that writes them reads them: a
// tar of <name>/SKILL.md, base64 so it crosses as text.
func skillArchive(skills []client.SkillDoc) (string, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, sk := range skills {
		if err := tw.WriteHeader(&tar.Header{Name: sk.Name + "/SKILL.md", Mode: 0o644, Size: int64(len(sk.Content)),
			ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
			return "", fmt.Errorf("coding: archive the skill %s: %w", sk.Name, err)
		}
		if _, err := tw.Write([]byte(sk.Content)); err != nil {
			return "", fmt.Errorf("coding: archive the skill %s: %w", sk.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("coding: archive the org's skills: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()) + "\n", nil
}

// solo runs argv so that its own exit, or its death, ends the command, and
// records in the file group names the process group a turn's leftovers are
// stopped by (end).
//
// An exec answers once every holder of its stdout and stderr has closed them,
// not when its process exits. A harness that opens a browser to check its work
// leaves that browser holding both, and the run's sandbox_run then waits out the
// whole exec budget after the turn is over. So argv writes to two files, two
// tails relay them as they grow, and the tails stop once argv has exited and
// they have drained. What argv leaves running keeps running — a server it
// started still answers a preview — and writes only to those files.
//
// THE TAILS FOLLOW THE HARNESS, AND EVERYTHING DIES WITH THE WRAPPER. They used
// to follow a sleep the wrapper killed after argv exited, so a wrapper killed
// first left the sleep, and with it the tails and the exec, alive until the run's
// budget ran out: the 32-minute hang of sess_06e8f87b…, where the model's
// `pkill -f "go build"` matched the task in the wrapper's own command line. Now
// the harness and both tails run under `setpriv --pdeathsig KILL`, so a wrapper
// that dies by any signal takes them with it, and the exec ends.
//
// They follow the harness's pid and not the wrapper's because the pod's PID 1 is
// `sleep infinity`, which reaps nothing: an orphan that exits stays a zombie, and
// `tail --pid` reads a zombie as alive. The wrapper is the parent that reaps the
// harness and both tails; nothing it starts outlives it as an orphan except what
// the harness itself left running.
//
// The tails read the files by path: the image's tail (uutils 0.8.0) follows no
// growth through /dev/fd/N, so the files stay linked until the tails are done.

// arm writes the run's credential into the sandbox.
//
// THE KEY ARRIVES ON STDIN. Argv is public — every process in the pod can read
// another's command line out of /proc, and the argv of a run's commands is
// echoed into its own session narration — and the process about to run in this
// pod executes a language model's output. Stdin reaches this one command and
// nothing else, which is the same reason the model credential travels that way
// (key.go).
//
// `umask 077` before the write, because ssh REFUSES a key file others can read
// ("UNPROTECTED PRIVATE KEY FILE") — so the mode is not belt-and-braces here, it
// is the difference between a run that clones and one that does not.
//
// The host key goes on argv deliberately: it is the forge's PUBLIC identity, and
// keeping it visible is what lets a person reading a failed run see which host
// was pinned.
//
// A GITHUB HOME WRITES NOTHING OF THE RUN'S TOKEN. It is fed to each command that
// reaches the remote (gitAs), so it is never on disk and is absent while the harness
// runs; TLS against the image's roots is its pin. What is written is the agent's
// read-only token (lend).
func (in sandbox) arm(req RunRequest) error {
	if https(req.Remote) {
		return in.lend(true)
	}
	if strings.TrimSpace(req.Known) == "" {
		// Refused, never softened to "trust whatever answers". An unpinned host is
		// a run that will accept any server standing in front of the forge — with
		// a write key in hand and a checkout it is about to trust.
		return fmt.Errorf("coding: no forge host key to pin; refusing to clone unpinned")
	}
	_, err := runIn(in.ctx, in.id, []string{"sh", "-c",
		`umask 077 && mkdir -p "$(dirname "$1")" && cat > "$1" && printf '%s\n' "$3" > "$2"`,
		"sh", keyPath, knownPath, req.Known},
		in.ttl, in.session, req.Key, in.secrets()...)
	if err != nil {
		return fmt.Errorf("coding: install the run credential: %s", in.scrub(err.Error()))
	}
	return nil
}

// lend writes the agent's read token (renewed when it nears its hour) to readPath on stdin, for this uid alone,
// when it is not the one already there, or always when force is set: a replaced pod lost its /tmp.
func (in sandbox) lend(force bool) error {
	tok := in.readKey
	if tok == "" {
		return nil
	}
	if in.renewRead != nil {
		if fresh, err := in.renewRead(in.ctx, false); err == nil && strings.TrimSpace(fresh) != "" {
			tok = strings.TrimSpace(fresh)
		}
	}
	if in.secretMu != nil {
		in.secretMu.Lock()
	}
	same := !force && in.lent != nil && tok == *in.lent
	if in.secretMu != nil {
		in.secretMu.Unlock()
	}
	if same {
		return nil
	}
	in.hide(tok)
	_, err := runIn(in.ctx, in.id, []string{"sh", "-c",
		`umask 077 && mkdir -p "$(dirname "$1")" && cat > "$1.new" && mv "$1.new" "$1"`, "sh", readPath},
		in.ttl, in.session, tok, in.secrets()...)
	if err != nil {
		return fmt.Errorf("coding: install the agent's read token: %s", in.scrub(err.Error()))
	}
	if in.secretMu != nil {
		in.secretMu.Lock()
	}
	if in.lent != nil {
		*in.lent = tok
	}
	if in.secretMu != nil {
		in.secretMu.Unlock()
	}
	return nil
}

// hide adds a token minted mid-run to what every stream is blinded against.
func (in sandbox) hide(tok string) {
	if in.extraSecrets == nil {
		return
	}
	if in.secretMu != nil {
		in.secretMu.Lock()
		defer in.secretMu.Unlock()
	}
	if !slices.Contains(*in.extraSecrets, tok) {
		*in.extraSecrets = append(*in.extraSecrets, tok)
	}
}

// configureGoEnv configures default GOPRIVATE/GONOSUMDB for Go toolchains and,
// when the run carries a GitHub read-only token, wires global git read auth and
// SSH-to-HTTPS insteadOf rewrites so `go test` and `go build` can resolve private
// Hanzo Go modules outside the checkout directory without failing on proxy/auth setup.
func (in sandbox) configureGoEnv(req RunRequest) error {
	if !agentic(req.Tool) {
		return nil
	}
	goprivate, gonosumdb := goPrivateEnv(req.Prepared.Env)
	const goEnvSh = `if command -v go >/dev/null 2>&1; then go env -w "GOPRIVATE=$1" "GONOSUMDB=$2" 2>/dev/null || true; fi
f="${GOENV:-${XDG_CONFIG_HOME:-$HOME/.config}/go/env}"
mkdir -p "$(dirname "$f")" && {
  if [ -f "$f" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
      case "$line" in GOPRIVATE=*|GONOSUMDB=*) ;; *) printf '%s\n' "$line" ;; esac
    done < "$f"
  fi
  printf 'GOPRIVATE=%s\nGONOSUMDB=%s\n' "$1" "$2"
} > "$f.tmp" && cp "$f.tmp" "$f" && rm -f "$f.tmp"`
	if _, err := in.do([]string{"sh", "-c", goEnvSh, "sh", goprivate, gonosumdb}, "configure Go private modules"); err != nil {
		return err
	}
	if treeless(req) {
		const gitGoPrivateSh = `git config --global --replace-all credential.https://github.com.helper "$1" && ` +
			`git config --global --replace-all url.https://github.com/.insteadOf git@github.com: && ` +
			`git config --global --add url.https://github.com/.insteadOf ssh://git@github.com/`
		if _, err := in.do([]string{"sh", "-c", gitGoPrivateSh, "sh", readHelper(readPath)}, "configure git read auth for private modules"); err != nil {
			return err
		}
	}
	return nil
}

// rearm puts back what a run's commands need that a replaced pod lost with its
// /tmp and $HOME: the run's key (arm), Go private-module config, and git's trust
// in the checkout.
func (in sandbox) rearm(req RunRequest) error {
	if _, err := in.do([]string{"git", "config", "--global", "--add", "safe.directory", in.dir}, "trust the checkout"); err != nil {
		return err
	}
	if strings.TrimSpace(req.Remote) != "" {
		if err := in.arm(req); err != nil {
			return err
		}
	}
	return in.configureGoEnv(req)
}
