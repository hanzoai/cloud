// Package wire is the box's HTTP contract, declared ONCE.
//
// Two programs speak it: `cmd/boxd` (inside the box, the server) and
// `apps/sandbox` (in cloud, the scheduler and proxy). They ship in different
// images on different release cadences, so if each held its own copy of these
// structs a field rename would compile clean on both sides and fail only in
// production. Living in one leaf package of one module, the drift is not
// expressible — that is the single property this package exists for.
//
// It has ZERO dependencies beyond encoding/json's struct tags: boxd is a static
// binary that ships inside every sandbox image, and nothing here may drag the
// cloud request tier in behind it.
//
// NOT here, deliberately: patch semantics. See PatchNote below.
package wire

// LibreChatExec is where the code interpreter answers, and it is here — in the
// one package all three cloud-side consumers can import — because the three of
// them DISAGREED about it in production and nothing could see the disagreement.
//
//	apps/exec           a path-preserving reverse proxy: asked the upstream for /v1/exec
//	apps/functions      built upstream + "/exec"
//	the executor        did not exist, so neither was ever wrong out loud
//
// No value of CODE_EXEC_UPSTREAM satisfied both, and appending /v1 to the env
// var only moved the mismatch (the proxy then asks for /v1/v1/exec). A constant
// two packages import is what makes "they agree" a thing a compiler and a test
// can check, instead of a thing two comments claim separately.
//
// The rest of that family (/v1/upload, /v1/download/{id}, /v1/files/{sid}) is
// spelled in boxd's librechat.go, next to the handlers, because boxd is the
// only program in the org that serves them.
const LibreChatExec = "/v1/exec"

// Paths, named once so neither side spells one wrong. This is the surface we
// OWN — unlike LibreChatExec above, which is fixed by a client we do not.
const (
	PathHealth   = "/v1/box/health"
	PathExec     = "/v1/box/proc/exec"
	PathFsList   = "/v1/box/fs/list"
	PathFsRead   = "/v1/box/fs/read"
	PathFsWrite  = "/v1/box/fs/write"
	PathFsDelete = "/v1/box/fs"
	PathFsSearch = "/v1/box/fs/search"
	PathGitClone = "/v1/box/git/clone"
	PathGitPush  = "/v1/box/git/push"
	PathAgentRun = "/v1/box/agent/run"
)

// QueryParams are every query parameter the box surface reads, named once so the
// proxy in cloud forwards exactly these and nothing else.
//
// A proxy that blindly copied the raw query string would be an opaque tunnel:
// it could not tell a real parameter from a smuggled one, and neither side
// would have a list to check against. Enumerating them costs one line per new
// parameter and buys the property that both ends agree on the surface.
var QueryParams = []string{
	"path",  // fs read/write/delete/list target, project-relative
	"depth", // fs list recursion bound
	"q",     // fs search needle
	"regex", // fs search: treat q as a pattern
	"limit", // fs search result cap
}

// KeyHeader is the credential header on every box call — the same header and the
// same KMS-sourced service key the LibreChat contract fixes (CODE_EXEC_API_KEY),
// so a box carries ONE credential, not one per surface. User identity is
// terminated by IAM at the cloud edge and never re-derived here.
const KeyHeader = "X-API-Key"

// Health is what a box says about itself. `Boot` is unix seconds so a caller can
// tell a warm pod that has been idle for an hour from one that just started.
type Health struct {
	OK      bool   `json:"ok"`
	Boot    int64  `json:"boot"`
	Image   string `json:"image,omitempty"`
	Project string `json:"project,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Workdir string `json:"workdir,omitempty"`
}

// ExecRequest runs one command in the box. Argv is preferred and is executed
// WITHOUT a shell; Command is the shell form, and exists because the coding
// agent's own steps are shell lines. Exactly one of the two is honoured, argv
// first — a request carrying both is a caller bug, not a merge.
type ExecRequest struct {
	Argv       []string          `json:"argv,omitempty"`
	Command    string            `json:"command,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TimeoutSec int               `json:"timeoutSec,omitempty"`
	Stdin      string            `json:"stdin,omitempty"`
}

// ExecResult is one finished command. A non-zero ExitCode is a SUCCESSFUL call
// carrying a failed program — the HTTP status stays 200, because "your test
// suite failed" and "the box is broken" are different facts and a caller that
// cannot tell them apart will retry the wrong one.
type ExecResult struct {
	ExitCode   int    `json:"exitCode"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	DurationMs int64  `json:"durationMs"`
	TimedOut   bool   `json:"timedOut,omitempty"`
}

// Entry is one filesystem row. Path is ALWAYS project-relative and always
// begins with "/" — the box's absolute workdir is an implementation detail of
// the box, and leaking it would make every caller string-strip a prefix.
type Entry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mode  string `json:"mode"`
	IsDir bool   `json:"isDir"`
}

type ListResult struct {
	Entries []Entry `json:"entries"`
}

// WriteRequest creates or overwrites one file. ContentB64 exists for bytes that
// are not valid UTF-8; Content is the ordinary path. Both set ⇒ ContentB64 wins,
// since a caller that base64'd something meant it.
type WriteRequest struct {
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	ContentB64 string `json:"contentB64,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

// Match is one search hit. Text is trimmed and truncated box-side: a match on a
// minified bundle is one line and several megabytes.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type SearchResult struct {
	Matches []Match `json:"matches"`
}

// Credential is a git credential in the request BODY — never argv, never a URL,
// never a log line. The rule apps/coding/task.go already states, restated where
// the bytes actually are.
type Credential struct {
	Username string `json:"username"`
	Token    string `json:"token"`
}

type CloneRequest struct {
	URL        string     `json:"url"`
	Ref        string     `json:"ref,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Dir        string     `json:"dir,omitempty"`
	Credential Credential `json:"credential,omitempty"`
	Depth      int        `json:"depth,omitempty"`
}

type CloneResult struct {
	Head string `json:"head"`
	Dir  string `json:"dir"`
}

type PushRequest struct {
	Branch     string     `json:"branch"`
	Message    string     `json:"message,omitempty"`
	Credential Credential `json:"credential,omitempty"`
}

type PushResult struct {
	CommitSha string `json:"commitSha"`
	Diffstat  string `json:"diffstat"`
	Changed   bool   `json:"changed"`
}

// AgentRunRequest asks the box to run @hanzo/dev against a prompt.
type AgentRunRequest struct {
	Prompt     string `json:"prompt"`
	SessionID  string `json:"sessionId,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
}

// Frame is ONE line of an agent run's NDJSON stream.
//
// These field names and tags are byte-identical to `apps/coding/task.go`'s
// unexported `message` struct. That is the whole reason apps/coding can rebind
// its Runner seam from the bot gateway to a box without its orchestrator, its
// session mirroring or its tests moving a line. If you are tempted to add a
// field here, add it there in the same commit or the rebind quietly drops it.
type Frame struct {
	Type      string `json:"type"` // step | log | result | error
	Step      string `json:"step,omitempty"`
	Message   string `json:"message,omitempty"`
	Status    string `json:"status,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CommitSha string `json:"commitSha,omitempty"`
	Diffstat  string `json:"diffstat,omitempty"`
	Changed   bool   `json:"changed,omitempty"`
	OK        bool   `json:"ok,omitempty"`
	LogTail   string `json:"logTail,omitempty"`
}

// PatchNote records what is deliberately ABSENT from this contract.
//
// The design sketch gave boxd an /v1/box/fs/patch endpoint applying
// update/rewrite ops. It is not here, and it should not be added.
//
// "What does `update` mean when oldStr appears twice" is ONE fact. It is
// already defined, in TypeScript, in `hanzo.app/lib/agent/patch.ts`, and
// `tests/integration/sandbox-fs.test.ts` asserts that the in-memory filesystem
// and the sandbox filesystem answer it identically — an assertion that is
// load-bearing, because a model getting different edit behaviour depending on
// which box it landed on is silent corruption.
//
// A Go reimplementation here would be a SECOND definition of that fact, on the
// far side of a network boundary, that the TS test cannot see: the test stubs
// the HTTP layer, so a Go/TS disagreement would pass every test both repos have
// and corrupt files in production. Read + apply + write costs one extra round
// trip and keeps one definition. That trade is not close.
const PatchNote = "patch semantics live in the client, once; see hanzo.app/lib/agent/patch.ts"
