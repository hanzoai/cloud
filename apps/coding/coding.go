package coding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/client"
)

// RepoRE is the repo-name rule every endpoint validates against, mirroring the git
// subsystem's own name check. It is here and exported because an endpoint that
// invented its own rule would eventually accept a token carrying a path
// separator, and a repo name that can carry a path can address another org's
// namespace.
var RepoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// OrgRE is the tenant-name rule, mirroring the git subsystem's own (git.go
// orgRE). A run's org is interpolated into a git namespace, so it is
// shape-checked rather than merely required: a tenant name that can carry a
// separator or a `..` can address another tenant's repositories.
var OrgRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// BaseRE is the branch-name rule, mirroring the git subsystem's own (push.go
// branchRE) — nested names allowed, so `release/2.1` works.
//
// The load-bearing property is the FIRST character class: a branch is alnum-led,
// so a base can never begin with '-'. The base reaches a `git clone -b <base>`
// argument on a machine we do not own, where a leading dash makes it a flag
// rather than a branch, and `--upload-pack=` / `--config=core.fsmonitor=` are
// each arbitrary command execution on that machine. Length and the absence of a
// space matter too, but the dash is the one that turns data into a program.
var BaseRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// issueRE is an issue a run may name: a number on its own repository, `#` or
// not, or owner/name#number on another — GitHub's own shape for a reference
// across repositories, which both hosts link and close.
var issueRE = regexp.MustCompile(`^(?:([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9_.-]{1,100})#|#?)([1-9][0-9]{0,9})$`)

// Closing is the reference a run's proposal closes, as its pull request writes
// it: `#464` for an issue on the run's own repository, `owner/name#464` for one
// in another, and "" for a run no issue asked for. Anything else is refused: the
// reference is written into the pull request, so it is one of these two shapes
// and nothing a caller could make into other text there.
func Closing(issue client.Issue) (string, error) {
	s := strings.TrimSpace(string(issue))
	if s == "" || s == "0" {
		return "", nil
	}
	switch m := issueRE.FindStringSubmatch(s); {
	case m == nil, m[1] != "" && strings.Trim(m[2], ".") == "":
		return "", fmt.Errorf("coding: %q is not an issue: name its number, or owner/name#number for one in another repository", s)
	case m[1] == "":
		return "#" + m[3], nil
	default:
		return m[1] + "/" + m[2] + "#" + m[3], nil
	}
}

// BranchFor is the branch a run writes when its caller names none, derived from
// the session that owns it: a pure function of the session id, never a name the
// model chooses.
func BranchFor(sessionID string) string { return agentPrefix + shortID(sessionID) }

// BranchForIssue is the branch a run for an issue writes when no branch is named and none is
// carried on: one issue, one branch, so a retry or a follow-up of the work meets the pull request
// the first opened. issue is as Closing writes it. `#464` is agent/issue-464; an issue of another
// repository carries a short digest of that repository's name, so a#464 and b#464 never meet.
// "" for a run no issue asked for.
func BranchForIssue(issue string) string {
	repo, number, found := strings.Cut(issue, "#")
	switch {
	case !found || number == "":
		return ""
	case repo == "":
		return agentPrefix + "issue-" + number
	}
	sum := sha256.Sum256([]byte(strings.ToLower(repo)))
	return agentPrefix + "issue-" + number + "-" + hex.EncodeToString(sum[:4])
}

// agentLeaf is the name after `agent/` a caller may give a branch: one path
// segment, alnum-led so it can never begin with '-' on a `git clone -b` or
// `git push` argument line.
var agentLeaf = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// AgentBranch is the branch a caller's name asks a run to write: `agent/<name>`,
// whether the name came with the prefix or without it, and "" for no name.
//
// This is the one place a name becomes a branch, so every branch a run writes is
// under agent/ — the namespace the forge's ref rules and the runner's own check
// (sandboxrunner.go agentPrefix) both state. A name that is not one plain segment
// is refused rather than cleaned: `..`, a `.lock` suffix and a trailing `.` are
// refs git itself refuses, and a '/' would reach past the namespace's one level.
func AgentBranch(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	leaf := strings.TrimPrefix(name, agentPrefix)
	if !agentLeaf.MatchString(leaf) || strings.Contains(leaf, "..") || strings.HasSuffix(leaf, ".lock") || strings.HasSuffix(leaf, ".") {
		return "", fmt.Errorf("coding: %q is not a branch a run may write; give letters, digits, '.', '_' and '-' and it is written as %s<name>", name, agentPrefix)
	}
	return agentPrefix + leaf, nil
}

// BaseOf is where a run starts: the branch given, or the branch a run named by
// after was issued, or the repository's default.
//
// `after` is what makes a follow-up instruction — "now add tests for it" — build
// on work already done instead of beginning again on a fresh clone. Start refines
// the answer once it has read that run's session, which records the branch it
// really wrote; this is the issued name, for a run whose session cannot be read.
//
// A session is turned into a branch HERE rather than accepted as one, so naming a
// run can never name a branch outside agent/. A given base wins — a caller who
// already knows the branch is not overruled by a convenience.
func BaseOf(base, after string) string {
	if base = strings.TrimSpace(base); base != "" {
		return base
	}
	if after = strings.TrimSpace(after); after != "" {
		return BranchFor(after)
	}
	return ""
}

// followBase is where a follow-up on the earlier run's branch starts, given which
// branches the remote holds (on): that branch when it is there; else the earlier
// run's own base when that is there and is another branch; else home, the
// repository's default ("" for the one a clone checks out).
func followBase(on func(branch string) bool, branch, earlierBase, home string) string {
	if on(branch) {
		return branch
	}
	if from := strings.TrimSpace(earlierBase); from != "" && from != branch && on(from) {
		return from
	}
	return home
}

// Event kinds mirrored into the agent session. These are the agents session
// vocabulary (a stable wire contract): a phase is a tool-call, a free line is a
// log, a lifecycle transition is a status, and the run's own word on how far along
// it is a progress. Kept as local constants so coding does not import the agents
// package for four strings.
const (
	kindMessage  = "message"
	kindToolCall = "tool-call"
	kindLog      = "log"
	kindStatus   = "status"
	kindProgress = "progress"
)

// The phases a run reports about itself (agents progress.go). A run waiting for a
// sandbox is queued and has done nothing; one that holds its place is running.
const (
	phaseQueued  = "queued"
	phaseRunning = "running"
)

const (
	statusDone  = "done"
	statusError = "error"
	// A run stopped or paused keeps its work on its branch (steer.go). stopped
	// is the session's terminal for it; paused leaves the session live.
	statusStopped = "stopped"
	statusPaused  = "paused"
)

// Sessions is the live agent-session registry client (clients/agents in-process).
type Sessions interface {
	Open(ctx context.Context, org, actor, agent, title string) (string, error)
	// OpenOn opens a session tagged with the run's dispatch TARGET, so a routed
	// run shows in mission-control on the machine it was sent to, and born in the
	// app conversation it is part of, when it is. Empty values behave exactly like
	// Open.
	OpenOn(ctx context.Context, org, actor, agent, title, target, conversation string) (string, error)
	Log(ctx context.Context, org, sessionID, kind, actor string, payload []byte) error
	Close(ctx context.Context, org, sessionID, status string, errStr ...string) error
	// Tag records what run the session is the view of, as each fact becomes true:
	// the repository and branches once they are known, the pull request once it
	// is proposed. An empty field leaves what the session already says.
	Tag(ctx context.Context, org, sessionID string, t Tag) error
}

// Tag is what a session says about the run it is the view of. Environment is
// "sandbox" for a Hanzo sandbox and the machine's id for a routed run; Mode is
// what the run may do, "build" or "plan"; Sandbox is the id of the sandbox it
// leased, the handle its terminal and its screen are opened with.
type Tag struct {
	Kind, Repo, Base, Branch, Environment, PR, Mode, Sandbox string
	// Project is the project the run was started on, so that project's list of
	// runs holds it.
	Project string
}

// kindCoding is the Kind every coding run's session carries.
const kindCoding = "coding"

// sandboxEnv is the Environment of a run in a Hanzo sandbox; a routed run's is
// its machine's id.
const sandboxEnv = "sandbox"

// PR opens the pull request for a finished run and says where it can be read.
//
// It is ONE client with two backends behind it, because a repository can live in
// two places and a run must not care which: the work item lands on our board
// either way, and the address comes back from the forge for a repository that
// lives only here or from GitHub for one that mirrors there. A caller that had to
// ask which host it was talking to would be two orchestrations pretending to be
// one, and they would drift.
type PR interface {
	Open(ctx context.Context, in PRInput) (PRRef, error)
}

// Runner is the bot-gateway coding-task client (clients/bot in-process client).
type Runner interface {
	Run(ctx context.Context, org, userID string, req RunRequest, onStep func(Step)) (RunResult, error)
}

// PRInput / PRRef mirror todo's agent-PR shape without leaking its types into
// the client (the adapter bridges).
// Proposal is how a run's pull request is opened beyond its title and body:
// as a draft, and the GitHub logins it is assigned to and asked to review.
type Proposal struct {
	Draft     bool
	Assignees []string
	Reviewers []string
}

type PRInput struct {
	Org string
	// Actor is the forge login the proposal is opened as — the person the run
	// acts for. It is not Assignee: that is who the todo row is assigned to
	// (the agent), and the two are different facts.
	Actor    string
	Project  string
	Repo     string
	Base     string
	Head     string
	Title    string
	Body     string
	Assignee string
	// Draft opens the proposal as a draft: GitHub's own, and on the forge a title
	// the forge reads as work in progress.
	Draft bool
	// Assignees and Reviewers are GitHub logins the proposal is assigned to and
	// asked to review. A forge proposal ignores them: a forge login is not a
	// GitHub login.
	Assignees []string
	Reviewers []string
	// Request is the request this proposal is opened for, when it was asked with an id: the
	// work item is filed once under it, so a replay after the claimer died between opening
	// the proposal and recording it files no second row.
	Request string
}

type PRRef struct {
	Identifier string
	ProjectKey string
	Number     int
	// URL is where the proposal is READ — the pull request on whichever host the
	// branch was pushed to, GitHub for a repository the forge only mirrors.
	// It is the only part of a finished run a person can click, so it travels all
	// the way out to the chat thread; empty is a real answer (a project-scoped
	// repository has no browsable page) and costs a link, never a run.
	URL string
}

// RunRequest / Step / RunResult mirror the bot coding contract.
type RunRequest struct {
	// Tool names what runs inside the sandbox: dev | gen | claude | codex | python |
	// node. Empty means dev. The runtime owns the name→argv table; cloud only
	// carries the name, so adding a tool is one edit over there and none here.
	Tool string

	// Desktop selects the xvfb IMAGE VARIANT — a tag, not a mode. A desktop run
	// gets an X server, so a real browser window exists to drive; every class can
	// already drive a headless one.
	Desktop bool

	// The repo is OPTIONAL. Remote empty means the run has no checkout — and then
	// Key MUST be empty too, because a credential a run can never use only exists
	// to leak. The runtime refuses the combination.
	//
	// Remote is the forge's SSH address, because the grant behind it is a deploy
	// key and a deploy key is SSH-only: the forge's HTTP path resolves permission
	// from an authenticated USER and has no deploy-key branch at all. For a
	// repository the forge only mirrors it is the GitHub origin's HTTPS address
	// instead (github.go), and the scheme is what tells the runner which.
	Remote            string
	BaseBranch        string
	Branch            string
	Prompt            string
	SessionID         string
	RunTimeoutSeconds int
	// Key is the run's credential — an OpenSSH private key, or a GitHub token
	// over HTTPS — write-only, never logged, scrubbed out of everything that
	// leaves the sandbox. Known is the forge's host key, which is public and pins
	// the host an SSH run will talk to.
	Key   string
	Known string
	// RenewKey refreshes Key before or during a remote git operation when Key is a
	// short-lived token (such as a GitHub App installation token). Nil when Key
	// does not expire during the run.
	RenewKey func(ctx context.Context, force bool) (string, error) `json:"-"`
	// ReadKey is a GitHub token for the same repository that can only read it, the one the run's
	// agent fetches with (cloneArgv, arm): write-only, never logged, and never able to push. Empty
	// for a remote with none, whose clone is shallow. RenewRead refreshes it as RenewKey does Key.
	ReadKey   string
	RenewRead func(ctx context.Context, force bool) (string, error) `json:"-"`
	// Ticket is the run's credential for asking its person a question (key.go):
	// it opens ask_user for this run and nothing else, and like Key it is never
	// an argument and never published.
	Ticket string
	// Plan is a run that answers with a plan and delivers nothing: its Key can
	// only read, and no commit or push step runs, whatever the harness did.
	Plan bool
	// Setup is a Plan whose answer is its codebase's environment (mode.go): its
	// agent is told to find one rather than to plan a task.
	Setup bool
	// Prepared is the codebase's environment: applied after the clone and before
	// the harness (sandboxrunner.go prepare).
	Prepared Prepared
	// Model and Effort are what the agent thinks with; empty is the default.
	Model, Effort string
	// Instructions are the person's own (prefs.go) and Kit is the org's skills
	// and MCP servers (kit.go), both handed to an agent harness where it reads
	// its user's (sandboxrunner.go equip). DeveloperInstructions carries per-run
	// developer_instructions (such as active skills and memories) out of Prompt.
	Instructions          string
	DeveloperInstructions string
	Kit                   Kit
	// OnTurnSteer registers a live in-turn steering callback while a harness turn
	// that supports in-turn queueing (`dev queue`) is executing.
	OnTurnSteer func(func(text string) bool) `json:"-"`
	// Author is the person the run's commit is authored by (author.go); the
	// zero value leaves the agent both author and committer.
	Author Author
	// Site is the project the run's pushed work is published to (publish.go);
	// the zero value publishes nothing.
	Site Site
	// Sandbox is the sandbox an earlier run worked in, which this one resumes and
	// works in again, from the tree it left; empty leases a new one.
	Sandbox string
	// Resumed reports that this run is continuing an already-open session in its
	// existing sandbox after a platform restart.
	Resumed bool
	// Local keeps the run's work in its workspace: committed there, and pushed
	// nowhere — a repository the person can read and not write, or none at all.
	Local bool
	// Parked reports that this run continues one that waited on a limit: its earlier work
	// is on its branch, in a sandbox of its own (park.go).
	Parked bool
}

type Step struct {
	Type    string
	Step    string
	Message string
	Status  string
	// Sandbox and Class are the sandbox a step leased and its class, set on the
	// step that leased it and empty on every other.
	Sandbox, Class string
}

type RunResult struct {
	// TimeUp says the runner ended the run for want of time it could not extend, a
	// free lease's hour or a GitHub token's, and kept its work as a stop does.
	TimeUp bool
	// Unrenewable says the runner ended the run a little short of the life of the one model
	// credential its harness can hold (it reads it once, or cannot be pointed at a renewed one),
	// and kept its work as a stop does.
	Unrenewable bool
	// Terminated is the signal the harness was killed by mid-turn (SIGKILL,
	// SIGTERM, …), empty for a harness that exited on its own (sandboxrunner.go
	// terminate). It is what tells a killed run from one that failed.
	Terminated string
	Branch     string
	CommitSha  string
	Diffstat   string
	// Subject is the first commit's subject line, the change named by whoever
	// made it. Empty when unread; the proposal is then titled from the prompt.
	Subject string
	Changed bool
	OK      bool
	LogTail string
	Error   string
	// Plan is a plan run's answer: what the harness said, scrubbed of every
	// credential the run held. Empty for a build.
	Plan string
	// Live is the address the run's work was published at, and Unpublished why
	// a run with a site did not publish; both empty for a run with none.
	Live, Unpublished string
	// Answer is the harness's last message, scrubbed; Usage the tokens its turns
	// used, as it reported them.
	Answer string
	Usage  Usage
	// Artifacts are what the run left, saved beside it (artifact.go), and Commits
	// the commits on its branch, newest first.
	Artifacts []client.Artifact
	Commits   []client.CodingCommit
	// Empty is a clone with no file but a README, a licence or a git setting in
	// it, Unanswered a harness that ended its turn with no last message, and
	// Unacted an agent that, asked to make its change or say none was needed
	// (outcome.go settle), did neither. Each is why a build that changed nothing
	// is not done (hollow).
	Empty, Unanswered, Unacted bool
	// Park is a run the model API refused for a usage limit or by its payer, which ended
	// waiting for that to clear rather than failing: its work is kept on its branch and its
	// sandbox given back (park.go). Nil for every other run.
	Park *Parking
}

// Usage is the tokens a harness reported its turns used.
type Usage struct {
	Input  int64 `json:"input"`
	Cached int64 `json:"cached"`
	Output int64 `json:"output"`
}

// plus is u and v together: a run's usage over every turn its harness took.
func (u Usage) plus(v Usage) Usage {
	return Usage{Input: u.Input + v.Input, Cached: u.Cached + v.Cached, Output: u.Output + v.Output}
}

// Req is one coding request the trigger surface dispatches. Credential is the
// per-org agent git secret the caller resolved from KMS fail-closed; it is
// relayed to the sandbox and NEVER logged or placed in a session event.
//
// TargetID, when set, ROUTES the run to a registered machine (#48): the run is
// enqueued as a durable task addressed to that target instead of executing in the
// cloud-side sandbox, and the credential is NOT used (the machine authenticates
// with its own). When empty, the local sandbox path runs unchanged.
type Req struct {
	Org      string
	UserID   string // linked Hanzo subject — session attribution + X-User-Id
	AgentRef string // agent label (e.g. "hanzo")
	Repo     string
	Project  string // IAM project slug (todo + git scope); "" = org default
	Base     string // base branch; "" = repo default
	// Branch is the agent branch the run writes, as Start settled it (AgentBranch,
	// or the earlier run's); "" is BranchFor(session).
	Branch string
	// From is the branch the checkout starts on when that is the run's own branch,
	// already on the remote: a follow-up, or a name given for a branch that exists.
	// "" starts from Base. Base stays what the proposal targets.
	From string
	// Pull opens a pull request for the run's branch when it changed something;
	// unset, the branch is pushed and nothing is proposed.
	Pull bool
	// Proposal is how the pull request Pull asks for is opened: as a draft, and
	// who it is assigned to and asked of.
	Proposal Proposal
	Prompt   string
	// Remote, Key and Known are the run's checkout: where the repository is, the
	// key that opens it, and the host key that pins the forge. Start resolves all
	// three together from one grant — see start.go — and a routed run carries
	// none of them.
	Remote string
	Key    string // write-only secret — never logged
	Known  string
	// RenewKey refreshes Key before or during a remote git operation when Key is a
	// short-lived token (github.go ensureToken).
	RenewKey func(ctx context.Context, force bool) (string, error) `json:"-"`
	// ReadKey and RenewRead are RunRequest's: a token that can only read the repository (github.go reading).
	ReadKey   string                                                // write-only secret — never logged
	RenewRead func(ctx context.Context, force bool) (string, error) `json:"-"`
	// Ticket opens ask_user for this run and nothing else (key.go). Never logged.
	Ticket string
	// Actor is the forge login this run acts as — the AUTHENTICATED caller, not
	// the attributed Subject. It is what every repository question is asked as,
	// on BOTH the sandbox and the routed path, so the forge's own ACL decides
	// which repository a run may reach.
	Actor          string
	TimeoutSeconds int
	TargetID       string // when set, route to this registered machine instead of the sandbox
	// Dir is the folder on that machine the run works in, inside one its owner
	// granted; "" is the machine's own choice.
	Dir string
	// Tool / Desktop are the caller's choice of harness and whether it needs a
	// screen. They travel to RunRequest unchanged; classFor and argvFor over in
	// sandboxrunner.go are the only two things that read them.
	Tool    string
	Desktop bool
	// SessionID adopts an ALREADY-OPEN session instead of opening one. The entry
	// point (Start) opens it so it can answer with a real handle the moment the run is
	// admitted, rather than an empty promise the caller cannot watch. Empty keeps
	// the original behaviour — Run opens its own — which is what the unit tests
	// exercise and what a direct Dispatcher caller still gets.
	SessionID string
	// Home is owner/name on GitHub for a run on a repository the forge does not
	// hold (github.go), and empty for the forge's own. A routed run is handed its
	// GitHub address, and its completion verifies and proposes there.
	Home string
	// Mode is what the run may do to its repository (modeBuild, modePlan, modeSetup), and
	// Model and Effort what its agent thinks with; empty takes the defaults.
	// Start has checked all three (mode.go).
	Mode, Model, Effort string
	// Instructions are the person's own instructions for their runs (prefs.go),
	// read when the run was admitted; empty when they set none.
	// DeveloperInstructions carries per-run developer_instructions separate from Prompt.
	Instructions          string
	DeveloperInstructions string
	// OnTurnSteer registers a live in-turn steering callback while a harness turn runs.
	OnTurnSteer func(func(text string) bool) `json:"-"`
	// Issue is the issue the run resolves, as its proposal closes it (Closing):
	// `#464` on Repo, `owner/name#464` on another repository, "" for none. It is
	// already the run's card on the board.
	Issue string
	// Author is the person the run's commit is authored by, read when the run
	// was admitted (author.go).
	Author Author
	// Site is the project the run's pushed work is published to: set by Start
	// for a sandbox build on a project whose code is in a made workspace.
	Site Site
	// Sandbox is the sandbox the run a follow-up follows worked in (RunRequest).
	Sandbox string
	// Resumed reports that this run is continuing an already-open session in its
	// existing sandbox after a platform restart.
	Resumed bool
	// Local keeps the run's work in its workspace (RunRequest).
	Local bool
	// Made is a repository in a workspace the platform made, where a new
	// project's repository begins as one README: its emptiness is not a fault.
	Made bool
	// ReadOnly constrains the whole run on a target machine: no writes to the granted
	// folder or checkout, no commit, no push, no PR. Start refuses it without a
	// target, and the machine's capability is checked before a session opens.
	ReadOnly bool
	// Parked reports that this run continues one that waited on a limit, in the session it
	// was parked in, and Kept what that run left on its branch (park.go). Trigger is what
	// tried it: byWake, byFunds or byContinue. Reason is why it was parked; Chain is when the
	// chain of parks it belongs to began (unix milliseconds, 0 when a person's Continue began
	// it now); Tries is how many starts in a row of it could not be made.
	Parked  bool
	Kept    Kept
	Trigger string
	Reason  string
	Chain   int64
	Tries   int64
	// Fault is the model-API fault the park it goes on from was for, and how many turns in a
	// row ended with it (faultCap).
	Fault Fault
}

// faultCap is how many turns in a row may end with the same model-API server error before a run
// is ended instead of parked again: a fault that does not clear is not waited on for a day.
const faultCap = 5

// endFaults is the reason a run ended for that, said on its last status with the error's name.
const endFaults = "fault_exhausted"

// Fault is a model-API server error a run met and how many turns in a row ended with it. Print names
// it (faultOf) and Count says how many times.
type Fault struct {
	Print string `json:"print,omitempty"`
	Count int    `json:"count,omitempty"`
}

// next is the fault after one more turn ended with print: counted on when it is the same error and
// no turn of the run answered since, begun again otherwise.
func (f Fault) next(print string, progressed bool) Fault {
	if progressed || f.Print != print {
		return Fault{Print: print, Count: 1}
	}
	return Fault{Print: print, Count: f.Count + 1}
}

// RoutedRun is the spec coding hands the Route client to enqueue on the durable
// engine. It mirrors agents.RoutedRun so coding.go stays pure (no agents import);
// the adapter bridges the two, exactly as PRInput/RunRequest mirror their
// downstream types. It carries no credential: the executing machine authenticates
// git + model routing with its own already-held creds, and a run in a workspace the
// platform made is minted a key of its own when it is offered (Made).
type RoutedRun struct {
	Org            string
	TargetID       string
	SessionID      string
	Repo           string
	Project        string
	Base           string
	Branch         string
	Prompt         string
	CloneURL       string
	TimeoutSeconds int
	// Actor + AgentRef are the dispatching user + agent label, carried so the durable
	// completion path can attribute the session close and file the PR with the same
	// assignee the local path uses. Neither is a secret and neither crosses to the
	// machine (the durable view the machine claims omits them).
	Actor    string
	AgentRef string
	// ForgeActor is the login the proposal is opened as when the machine reports
	// back. It is carried for the same reason Branch is: the completion happens
	// minutes later in another process, and re-deriving it there would be a
	// second answer to who this run acts for.
	ForgeActor string
	// Home is Req's, carried to the completion for the same reason. Whether the
	// person may act there is NOT carried: the completion is admitted on what
	// GitHub and IAM answer then, not on what was true at the start.
	Home string
	// Model and Effort ride to the machine, which runs its own harness with them.
	Model, Effort string
	// Instructions ride too: they are the person's, and the machine's own
	// credential may be somebody else's. The org's skills and MCP servers do not —
	// the machine reads what its org holds with that credential, and a server's
	// credential is never put on a durable queue or handed out of our custody.
	Instructions string
	// Pull is Req's, carried to the completion that would open the proposal.
	Pull bool
	// Made is Req's: the repository is in a workspace the platform made, so the run is
	// minted a deploy key when it is offered, and the completion verifies and proposes
	// in that workspace. Read is a plan or a question, which gets a read-only key.
	Made, Read bool
	// Dir is Req's, and it rides to the machine.
	Dir string
	// ReadOnly is Req's: the machine's harness must contain this one run, answer
	// without pushing, and report changed:false.
	ReadOnly bool
}

// Result is the terminal outcome the trigger surface renders.
type Result struct {
	SessionID string
	Repo      string
	Branch    string
	CommitSha string
	Diffstat  string
	Changed   bool
	OK        bool
	Verified  bool // pushed branch confirmed present in native git
	PR        PRRef
	LogTail   string
	Error     string
	// Terminated is the signal the run's harness was killed by, empty otherwise
	// (RunResult.Terminated). The session's error status carries it.
	Terminated string
	// Routed reports that the run was ENQUEUED to a target machine rather than run
	// in the cloud sandbox. When true, OK means "accepted + queued" (not
	// "completed"): the terminal outcome flows through the session stream as the
	// machine executes. TargetID is the machine it was routed to.
	Routed   bool
	TargetID string
	// Parked reports a run that ended waiting for a refusal to clear (park.go): its session
	// is paused, saying why, and the run goes on by itself.
	Parked bool
	// Fault is the model-API fault that ended the run for good (faultCap): its last status says
	// reason fault_exhausted, the fault's name and how many times it met it.
	Fault Fault
}

// Runner-facing behavioral defaults.
const (
	maxTitlePrompt = 120
	maxPromptLen   = 32 << 10 // 32 KiB — a task prompt, not a document
)

// Dispatcher wires the clients. The two git functions are injected (not an
// interface) because they are pure reads with no cloud-side state. Both take a
// ctx: git is another PROCESS, so both are calls that can be slow, refused, or
// cancelled with the run.
type Dispatcher struct {
	Sessions  Sessions
	PR        PR
	Runner    Runner
	CloneURL  func(ctx context.Context, org, actor, repo string) string
	VerifyRef func(ctx context.Context, org, repo, branch string) (string, bool)
	// Log is an optional structured log client for best-effort mirror failures; nil
	// is fine (mirror failures are non-fatal and simply dropped).
	Log func(msg string, kv ...any)
	// Route enqueues a routed run on the durable engine (the tasks-engine binding
	// in routed.go). Nil disables routing — a run with a TargetID then fails
	// closed rather than silently running in the sandbox.
	Route func(ctx context.Context, run RoutedRun) error
	// TargetGate is the fail-closed liveness+existence check for a routed run's
	// target (agents.TargetDispatchable): the target exists in this org, is
	// online, has a live runner, and holds dir (when set) inside a granted folder.
	// Nil disables routing.
	TargetGate func(ctx context.Context, org, targetID, dir string, readOnly bool) error
	// Watch observes every event the run mirrors into its session — the SAME
	// events, at the same moment, from the one place a run narrates itself.
	//
	// It exists because a run is watched from more than one place and must not
	// grow a second narration to serve each. The session stream is the primary
	// feed; a chat thread is a second reader of the same story. Adding a hook
	// here rather than a Slack call inside the orchestration is what keeps
	// coding.go ignorant of chat: it emits events, and who listens is a
	// composition decision made where the run is started.
	//
	// Nil is the ordinary case and costs nothing. It is best-effort by
	// construction — it runs beside a mirror that is itself best-effort, and a
	// watcher that fails must never fail the run whose work already happened.
	Watch func(ctx context.Context, kind string, payload []byte)

	// Environment resolves a codebase's environment for a sandbox run
	// (environment.go prepare): its scripts, and its secrets' values. Nil runs every
	// codebase bare.
	Environment func(ctx context.Context, org, repo string, setup bool) (Prepared, error)
	// Equip resolves the org's skills and MCP servers for an agent's sandbox run
	// as its person carries them (kit.go). Nil runs every agent bare.
	Equip func(ctx context.Context, actor string) (Kit, error)
	// Suggest sends a setup run's answer to the environment app and answers what
	// of it was kept as the codebase's proposal. Nil keeps nothing, and the answer
	// is still the run's result.
	Suggest func(ctx context.Context, org, repo, session, answer string) (*client.Suggestion, error)
	// Issue puts the run's issue on the board. Nil means no board.
	Issue func(ctx context.Context, org string, in client.IssueIn) error
	// Park holds a run the model API refused while it waits (park.go): the durable step.
	// Start binds it per run, with who the run is for and the task it was asked. Nil, a run
	// so refused fails.
	Park func(ctx context.Context, p Parked) error
	// track moves this run's issue when its status changes; see tracking.
	track func(ctx context.Context, status string)
}

// Run executes one coding job end to end and returns its Result. It never
// panics on a client failure (each is turned into a recorded error); the caller
// runs it under a bounded, recovered goroutine with a deadline ctx.
