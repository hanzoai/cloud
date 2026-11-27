package agents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/text"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/remote"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// This file mounts the LIVE agent-session control plane under /v1/agent/sessions
// — the canonical registry every surface (the @hanzo/dev CLI outer agent,
// hanzo.bot, console, chat, app) hangs off. It is the VIEW + control + ZAP-stream
// layer over durable execution; the durable run itself is a hanzoai/tasks
// workflow (see sessions_tasks.go), never a bespoke scheduler here.
//
//	POST   /v1/agent/sessions              register a session (opt parentSessionId) -> Session
//	GET    /v1/agent/sessions              list live sessions (filter root/parent/status) -> {sessions:[...]}
//	GET    /v1/agent/sessions/stream       SSE feed of session+event updates (rides ZAP)
//	GET    /v1/agent/sessions/:id          detail + direct children + recent events -> SessionDetail
//	PATCH  /v1/agent/sessions/:id          update status/title -> Session
//	DELETE /v1/agent/sessions/:id          stop if live and remove session + subtree -> {deleted,id}
//	GET    /v1/agent/sessions/:id/tree     the full subagent-flow graph -> TreeNode
//	POST   /v1/agent/sessions/:id/events   append an event (message/tool-call/spawn/log/progress/event) -> Event
//	POST   /v1/agent/sessions/:id/{pause,resume,stop,message}  control command -> {command,event,forwarded}
//
// Every route is org-scoped through principal.Org (a validated principal AND
// a non-empty org), so cross-tenant reads/writes/control are refused fail-closed.

// Event kinds — the closed vocabulary of a session's ordered log.
const (
	KindMessage  = "message"
	KindToolCall = "tool-call"
	KindSpawn    = "spawn"
	KindLog      = "log"
	KindStatus   = "status"
	KindControl  = "control"
	// KindProgress is the run REPORTING ITS OWN PROGRESS — pct, phase and a line
	// saying what it is doing, as the run itself understands them. It is the one
	// kind whose payload this surface reads (progress.go): the shape is ours, so
	// a malformed one is refused rather than stored, and a well-formed one lands
	// on the session's progress as GROUND TRUTH, outranking the model estimate.
	KindProgress = "progress"
	// KindEvent is one event an agent harness wrote as a JSON line, in the
	// harness's own shape (codex exec --json: thread.started, turn.started,
	// item.started, item.updated, item.completed, turn.completed with usage,
	// turn.failed, error). A coding run's harness narrates its work this way, and
	// its raw output goes to KindLog.
	KindEvent = "event"
)

// Control commands — the closed vocabulary of remote steering.
const (
	CmdPause   = client.SessionPause
	CmdResume  = client.SessionResume
	CmdStop    = client.SessionStop
	CmdMessage = client.SessionMessage
)

const (
	maxTitle = 512
	// maxError bounds the reason a session ended in error: long enough for a
	// process's last lines, short enough to list.
	maxError        = 2000
	maxAgentLabel   = 128
	maxActor        = 256
	maxSessionID    = 128
	maxWorkflowRef  = 256
	maxEventPayload = 64 * 1024
	maxControlMsg   = 16 * 1024
	recentEvents    = 50
	treeNodeCap     = 10000
	maxHost         = 256
	maxCwd          = 1024
	maxRepo         = 512
	maxTerminal     = 512
	maxProvider     = 64
	maxAccount      = 256
	// maxChannel bounds the channel label. A team channel is addressed by document id and
	// space uuid, so this is generous for the pair and still a bound.
	maxChannel = 256
	// maxBranch bounds a branch name a run reports.
	maxBranch = 255
)

func validKind(k string) bool {
	switch k {
	case KindMessage, KindToolCall, KindSpawn, KindLog, KindStatus, KindControl, KindProgress, KindEvent:
		return true
	}
	return false
}

// ---- HTTP shapes (the published contract) ----

type sessionView struct {
	// ID is the session's handle, minted here as "sess_" + 32 hex characters. Every
	// later read, patch, event append and control command is addressed with it, and
	// a caller cannot choose it.
	ID string `json:"id"`
	// Org is the caller's OWN tenant, echoed so a client can build the public
	// build URL (/builds/:org/:project) without a second call or a guess. It is
	// never another tenant's — every read is org-scoped before it gets here.
	Org string `json:"org"`
	// Agent is the label the surface running this session calls itself by
	// ("hanzo-dev"), up to 128 characters. Required at register. It is free text,
	// not a reference: it need not name a defined agent, and nothing resolves it.
	Agent string `json:"agent"`
	// Actor is WHO this session belongs to, as "org/sub" — the same identity a run
	// is billed under. A register that names none takes the calling principal. It is
	// what scopes a login revoke, so a session with the wrong actor is a session the
	// right person cannot stop.
	Actor string `json:"actor,omitempty"`
	// Status is one of exactly four: running, paused, done, error. running and
	// paused are LIVE; done and error are TERMINAL and monotonic — once a session
	// reaches one it can never go back, because reopening a finished run would
	// fabricate liveness. A control command never moves it: the surface running the
	// agent reports the new status, and until it does the command is only recorded.
	Status string `json:"status"`
	// ParentSessionID is the session that spawned this one, making this a subagent
	// of it. Empty means this session is a root — a flow of its own. A parent always
	// belongs to the same org, so a tree never crosses a tenant.
	ParentSessionID string `json:"parentSessionId,omitempty"`
	// RootSessionID is the top of this session's tree, inherited from the parent and
	// shared by every node in one flow. A root session's own id, when it has no
	// parent. It is the key one indexed read pulls a whole flow by, and what ?root=
	// narrows a list or a stream to.
	RootSessionID string `json:"rootSessionId"`
	// Title is the human line a card shows ("ship the landing page"), up to 512
	// characters. Free text, and the one field a surface may rewrite as the work
	// turns out to be something else.
	Title string `json:"title,omitempty"`
	// TaskWorkflowID is the hanzoai/tasks durable workflow that actually EXECUTES
	// this session — this registry is the view, control and stream layer over it.
	// Set, a control command is FORWARDED to that engine; empty, the running surface
	// polls for commands instead, which is every session today.
	TaskWorkflowID string `json:"taskWorkflowId,omitempty"`
	// TaskRunID is that workflow's particular run. A workflow is the definition and
	// a run is one execution of it, which is why both are carried.
	TaskRunID string `json:"taskRunId,omitempty"`
	// Execution context (mission-control): the machine/repo/cwd a card shows and
	// the run-target a session is dispatched to. Omitted when a surface didn't report it.
	Host string `json:"host,omitempty"`
	// Cwd is the directory the session is working in NOW, not the one it started in:
	// a linked shell moves around, and a card showing where `hanzo link` was run
	// answers "which work is this" with something that was true once.
	Cwd string `json:"cwd,omitempty"`
	// Repo is the code the session is working on, as the surface reported it. It is
	// truth the SURFACE states, so it is a label rather than something resolved here.
	Repo string `json:"repo,omitempty"`
	// Terminal is where this session can be WATCHED — the URL the machine
	// published for its live terminal. Omitted when it publishes none.
	Terminal string `json:"terminal,omitempty"`
	// Target is the registered run-target this session is dispatched to — a machine
	// the org claimed, resolved same-org when it was set, so it can never point at
	// another tenant's computer. Empty means the session names no machine.
	Target string `json:"target,omitempty"`
	// Provider is the linked AI account's provider (claude | codex | hanzo | …) that
	// served this run. Empty when the surface did not say.
	Provider string `json:"provider,omitempty"`
	// Account is which subscription or API account under that provider served it.
	// Together with Provider it is what a login revoke matches on to stop the
	// sessions a withdrawn account was paying for.
	Account string `json:"account,omitempty"`
	// Channel is the collaborative channel this run was started in (HIP-0523), empty
	// when it came from anywhere else — a CLI, a schedule, an API call. It is what
	// lets a space view show the runs of one channel beside its messages.
	Channel string `json:"channel,omitempty"`
	// The readable build: the product this session built and whether its story
	// is public (provenance.go).
	Project string `json:"project,omitempty"`
	// Published is the author's decision to let anyone read this session's story at
	// the public build route. It only ever widens READ access to a session that
	// already exists and grants nothing else; false, an unpublished session is
	// invisible there no matter who asks. It cannot be true without a Project,
	// because that route is keyed on (org, project).
	Published bool `json:"published,omitempty"`
	// Kind is what sort of run this session is the view of — "coding" for a coding
	// run. Empty for a session no run tagged.
	Kind string `json:"kind,omitempty"`
	// Base is the branch the run started from. Empty when it took the
	// repository's default and did not say which.
	Base string `json:"base,omitempty"`
	// Branch is the branch the run pushes its work to, and the only one it may
	// write.
	Branch string `json:"branch,omitempty"`
	// Environment is where the run executes: "sandbox" for a Hanzo sandbox, or the
	// id of the org's machine it was routed to (the same id Target carries).
	Environment string `json:"environment,omitempty"`
	// PR is the address of the pull request the run proposed. Empty until it
	// proposes one, and for a run that changed nothing.
	PR string `json:"pr,omitempty"`
	// Mode is what the run may do to its repository: "build" edits, commits and
	// pushes its branch; "plan" reads and answers with a plan, writing nothing.
	// Empty for a session no run tagged.
	Mode string `json:"mode,omitempty"`
	// Sandbox is the id of the sandbox the run works in ("m_" and 24 hex), the
	// handle its terminal and its screen are opened with
	// (/v1/sandbox/{id}/terminal, /screen). Set when the run leases it, and kept
	// once the run gives it back, when the id opens nothing. Empty for a run that
	// never leased one — a routed run, or a turn that only talked.
	Sandbox string `json:"sandbox,omitempty"`

	// Events is how many turns the session's log holds, counted at read time. It is
	// the whole log, however few of them RecentEvents carries.
	Events int `json:"events"`
	// Children is the DIRECT fan-out — how many sessions name this one as parent —
	// and not the size of the subtree. Read the tree for that.
	Children int `json:"children"`
	// StartedAt is when the session opened, RFC 3339 in UTC to the second.
	StartedAt string `json:"startedAt"`
	// EndedAt is when its run stopped, same format: it reached done, error or stopped,
	// or it paused. Empty while it runs; a resume clears it, since it runs again.
	EndedAt string `json:"endedAt,omitempty"`
	// CreatedAt is when the row was written, same format. Every path that opens a
	// session stamps it and StartedAt from one clock reading, so the two are equal
	// on every session this surface has ever produced.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is the session's last-activity clock, same format. It moves on a
	// write to the row — a status, a title, a re-dispatch — AND on every appended
	// turn, because the append bumps it in the same transaction. The list is ordered
	// on CreatedAt, so this is the field that says whether a session is still saying
	// anything.
	UpdatedAt string `json:"updatedAt"`
	// LastEvent is the compact latest-activity line for the list projection (nil in
	// register/patch/tree responses; set by list + detail). It lets a swipe card show
	// a live one-line preview without fetching full detail.
	LastEvent *lastEventView `json:"lastEvent,omitempty"`
	// Progress is how far along this run is — a share of its goal, a phase, and a
	// line saying what it is doing. Always present, so a board never branches on
	// whether it is there; `phase` says "unknown" when nothing has estimated it.
	// It is a MODEL ESTIMATE wherever `estimated` is true, and the row's own word
	// where it is false. See progress.go.
	Progress sessionProgress `json:"progress"`
	// Error explains why the session ended in error, when it did. Empty for any other status.
	Error string `json:"error,omitempty"`
	// Park is present while a coding run waits to go on by itself: the model API
	// refused it for a usage limit, or its payer refused it. The session is paused,
	// holds no sandbox, and its work is on its branch. Absent for every other session.
	Park *sessionPark `json:"park,omitempty"`
}

// sessionPark is why a parked run waits, and what clears it.
type sessionPark struct {
	// Reason is "limit" (the model API refused the run's calls for a usage limit),
	// "payment" (its payer refused them), "fault" (the model API failed them after
	// the harness tried them again, and the run is tried again by itself) or "restart"
	// (the pod driving the run was replaced, and it goes on at once on the next).
	Reason string `json:"reason"`
	// ResumeAt is when the run is tried again by itself, RFC 3339 in UTC, as the
	// authority that set the limit states it. Absent when nothing names a time: the
	// run then goes on when credits are added, a plan or a cap changes, or a person
	// continues it.
	ResumeAt string `json:"resumeAt,omitempty"`
	// Message is the run's own sentence for it, as its thread's card says it.
	Message string `json:"message"`
}

// parkOf is a session's park, nil when it is not parked.
func parkOf(x Session) *sessionPark {
	if !x.Parked() {
		return nil
	}
	return &sessionPark{Reason: x.Park, ResumeAt: text.Unix(x.ParkAt), Message: ParkSaid(x.Park, x.ParkAt)}
}

// lastEventView is the one-line latest-activity a mission-control card renders in
// the list — kind + actor + a bounded payload preview + timestamp. The full event
// (unbounded payload) is only ever returned in detail/stream, never the list.
type lastEventView struct {
	// Seq is that event's position in the session's log — monotonic from 1, per
	// session. A reader holding it can ask the detail or stream reads for
	// everything after it, so this doubles as the list's resume cursor.
	Seq int64 `json:"seq"`
	// Kind is what the turn was, from the log's closed six: message, tool-call,
	// spawn, log, status, control.
	Kind string `json:"kind"`
	// Actor is who produced the turn, defaulted to the calling principal when the
	// writer named nobody.
	Actor string `json:"actor,omitempty"`
	// Preview is the first 240 bytes of the event's payload, cut without regard for
	// the JSON inside it — it is a string to SHOW, never a value to parse. Read the
	// detail or the stream for the whole payload.
	Preview string `json:"preview,omitempty"`
	// At is when the turn was recorded, RFC 3339 in UTC to the second.
	At string `json:"at"`
}

// lastEventPreviewCap bounds the payload snippet carried in a list row so a page of
// 100 sessions stays small (the full payload rides detail/stream).
const lastEventPreviewCap = 240

func toLastEventView(e Event) *lastEventView {
	p := e.Payload
	if len(p) > lastEventPreviewCap {
		p = p[:lastEventPreviewCap]
	}
	return &lastEventView{Seq: e.Seq, Kind: e.Kind, Actor: e.Actor, Preview: p, At: text.Unix(e.CreatedAt)}
}

type eventView struct {
	// ID is the event's own handle, minted as "evt_" + 32 hex characters. It
	// identifies the turn; Seq is what ORDERS it.
	ID string `json:"id"`
	// SessionID is the session this turn belongs to. Carried on every event so a
	// stream frame stands alone — a subscriber watching a whole tree gets turns from
	// several sessions down one connection.
	SessionID string `json:"sessionId"`
	// Seq is the turn's position in this session's log: monotonic from 1, assigned
	// by the store inside the insert, and unique PER SESSION rather than globally.
	// It is the cursor a reader resumes from after a reconnect — ask for everything
	// after your last-seen seq.
	Seq int64 `json:"seq"`
	// Kind is what the turn IS, from a closed six: message (a model turn),
	// tool-call, spawn (a subagent started), log, status, control (a steering
	// command the running surface consumes). Anything else is refused at the write.
	Kind string `json:"kind"`
	// Actor is who produced the turn. A write that names nobody takes the calling
	// principal, so this is rarely empty in practice.
	Actor string `json:"actor,omitempty"`
	// Payload is the turn's body, embedded as JSON rather than as a string —
	// whatever the writer sent, up to 64 KiB, checked for well-formedness and
	// scanned for credentials before it was stored. Its SHAPE is the writer's
	// business and varies by Kind; this surface does not interpret it, except
	// that every string in it is kept as plain text: terminal escape sequences
	// and control characters other than the line break and the tab are removed.
	Payload json.RawMessage `json:"payload,omitempty"`
	// CreatedAt is when the turn was recorded, RFC 3339 in UTC to the second. Seconds
	// are coarse enough that two turns can share one, which is why Seq and not this
	// is the order.
	CreatedAt string `json:"createdAt"`
}

// sessionDetail is one session plus what only the detail read carries: its direct
// children and its most recent events. sessionView is EMBEDDED (promoted inline on
// the wire) rather than spelled out again — it is the shared list projection, and a
// second copy of its 22 fields is a second thing to forget to update. See the note
// on agentDetail for why the published schema of an embedded shape is currently
// short of its promoted fields, and where that is fixed.
type sessionDetail struct {
	sessionView
	// Children is the session's DIRECT children, one level down, each with its own
	// counts. The promoted `children` integer beside it is how many there are; this
	// is who they are. For the whole subtree, read the tree.
	Children []sessionView `json:"childSessions"`
	// RecentEvents is the page of turns returned for this read, OLDEST of those
	// first — 50 from the tail by default, up to `limit` (1000 max), or forward
	// from `after` when paging through the log. The promoted `events` integer says
	// how many the log holds in total.
	RecentEvents []eventView `json:"recentEvents"`
	// Next is the seq cursor to pass as `after` when more events follow a forward
	// page; omitted on tail reads and when the page reached the end of the log.
	Next int64 `json:"next,omitempty"`
}

// treeNode is one node of the subagent-flow graph: a session plus its children,
// recursively. Node = {session, children:[...]} — the session's own Children int
// is the direct fan-out count, the children array is the materialised subtree.
type treeNode struct {
	// Session is this node's own session, carrying its event count and its direct
	// fan-out. It is the same shape the list and detail reads answer with, minus the
	// last-event preview, which the tree does not fetch.
	Session sessionView `json:"session"`
	// Children is this node's direct children, each a whole node, so the array nests
	// to the depth of the flow. A leaf carries null rather than an empty array. The
	// subtree is materialised in full, up to 10000 nodes, out of one indexed read of
	// the root; nothing is walked node by node.
	Children []treeNode `json:"children"`
}

func toSessionView(x Session, events, children int) sessionView {
	return sessionView{
		ID: x.ID, Org: x.Org, Agent: x.Agent, Actor: x.Actor, Status: x.Status,
		ParentSessionID: x.ParentID, RootSessionID: x.RootID, Title: x.Title,
		TaskWorkflowID: x.TaskWorkflowID, TaskRunID: x.TaskRunID,
		Host: x.Host, Cwd: x.Cwd, Repo: remote.Clean(x.Repo), Terminal: x.Terminal, Target: x.Target,
		Provider: x.Provider, Account: x.Account,
		Channel: x.Channel,
		Project: x.Project, Published: x.Published,
		Kind: x.Kind, Base: x.Base, Branch: x.Branch, Environment: x.Environment, PR: x.PR, Mode: x.Mode,
		Error: x.Error, Park: parkOf(x),
		Sandbox: x.Box, Events: events, Children: children,
		StartedAt: text.Unix(x.StartedAt), EndedAt: text.Unix(x.EndedAt),
		CreatedAt: text.Unix(x.CreatedAt), UpdatedAt: text.Unix(x.UpdatedAt),
		Progress: progressOf(x),
	}
}

func toEventView(e Event) eventView {
	var p json.RawMessage
	if e.Payload != "" {
		// Every write keeps a payload parseable (plainPayload); a row that predates
		// that is made so here, because one unparseable event must not make the
		// whole session unreadable.
		if !json.Valid([]byte(e.Payload)) {
			e.Payload = plainPayload(e.Payload)
		}
		p = json.RawMessage(e.Payload)
	}
	return eventView{
		ID: e.ID, SessionID: e.SessionID, Seq: e.Seq, Kind: e.Kind, Actor: e.Actor,
		Payload: p, CreatedAt: text.Unix(e.CreatedAt),
	}
}

// mountSessions registers the sessions routes. It MUST be called before the
// /v1/agent/:name wildcard (Fiber matches in registration order, so a bare
// :name would otherwise capture "sessions"). Within the block, the static
// /stream route precedes the /:id param for the same reason.
//
// The typed ops are declared on the GROUP, so each op's path is the group's
// prefix composed with its leaf — the same composition the router does, and the
// identity every projection keys on.
func mountSessions(s *cloud.Service[state], app cloud.Router) {
	o := sessionOps{s: s}
	g := app.Group("/v1/agent")
	g.Post("/sessions", o.register, zip.WithStatus(http.StatusCreated))
	g.Get("/sessions", o.list)
	// UNTYPED, and it cannot be otherwise: the stream is an open Server-Sent
	// Events response written by a loop that OUTLIVES the handler
	// (c.SendStreamWriter), and a typed op returns one marshalled value. There is
	// no In/Out that describes a feed.
	g.Raw(http.MethodGet, "/sessions/stream", cloud.Handle(s, sessionsStream))
	g.Get("/sessions/:id", o.get)
	g.Patch("/sessions/:id", o.patch)
	g.Delete("/sessions/:id", o.del)
	g.Get("/sessions/:id/tree", o.tree)
	g.Get("/sessions/:id/progress", o.progress)
	// All five were raw because the guard gate answers 422 IN BAND with the
	// findings that refused the write, and zip's error carried a sentence and no
	// body — so a typed op would have dropped the array that tells an author WHICH
	// secret to rotate. zip v1.31.3 carries extension members on the error, so the
	// refusal keeps its shape and these keep their registry entry. See
	// sessions_typed.go for why the merge is safe HERE and not on the money wire.
	g.Get("/sessions/:id/events", o.events)
	g.Post("/sessions/:id/events", o.appendEvent, zip.WithStatus(http.StatusCreated))
	g.Get("/sessions/:id/control", o.drain)
	g.Post("/sessions/:id/pause", o.pause)
	g.Post("/sessions/:id/resume", o.resume)
	g.Post("/sessions/:id/budget", o.budget)
	g.Post("/sessions/:id/stop", o.stop)
	g.Post("/sessions/:id/message", o.message)
	// The app's conversation (talk.go): the first thing said opens a session and
	// everything after continues it.
	g.Post("/turns", o.talk)
	g.Post("/sessions/:id/answer", o.answer)

	// The readable build (provenance.go). PUBLIC — no tenancy — because the only
	// rows either route can reach are ones an author explicitly published. A
	// visitor opening a product follows the session that produced it; the owner
	// reads the same session through the org-scoped /sessions routes above.
	g.Get("/builds", o.builds)
	g.Get("/builds/:org/:project", o.build)
}

// The control plane's prose, declared beside the wire facts that keep it untyped.
//
// A typed op's prose is lifted from its handler's doc comment by zipdoc, and these
// six are exactly the handlers mountSessions refuses to type: the SSE feed, whose
// loop outlives the handler and has no In/Out that describes a feed, and the five
// guarded writes, whose 422 carries a findings array zip's error type cannot
// express. There is no typed op here to lift from, so openapi.Describe is the client
// — and without it each publishes an operationId and nothing else: an SDK method
// that cannot explain itself, an MCP tool with no description, a CLI command with
// no help.
//
// The four control commands share one handler (control) and therefore one set of
// rules; each statement leads with what its OWN command asks for and then states
// the rules once, because a reader meets exactly one of these at a time.
func init() {
	spec.Describe("/v1/agent/sessions/stream", http.MethodGet,
		"Live session and event updates for the caller's org, as Server-Sent Events.",
		"Holds the connection open as text/event-stream and pushes a frame each time the org's "+
			"registry moves: an `event: session` frame carrying the same session shape the list and "+
			"detail reads answer with (a registration, an update, or a login-manager revoke tearing "+
			"a session down), and an `event: event` frame carrying one appended turn (with `id: <seq>`). Optional "+
			"?root=<session id> narrows the feed to a single subagent tree, and optional ?after=<seq> "+
			"(or the `Last-Event-ID` request header) replays stored events with `seq > after` in order "+
			"before streaming live updates without duplicates.\n\n"+
			"Requires a validated principal carrying an org: 401 without a principal, 403 without an org. Org-scoped "+
			"fail-closed: the bus filters on tenant before it fans out, so a subscriber only ever "+
			"receives its own org's updates, and ?root= narrows that further but can never widen "+
			"it. Within the org a subscriber receives what GET /v1/agent/sessions/{id} would show "+
			"them: a member the frames of the sessions they opened, an org admin or a SuperAdmin "+
			"the org's.\n\n"+
			"Delivery is best-effort and the GET reads remain the source of truth. A subscriber "+
			"that falls more than 256 frames behind is DROPPED — its channel is closed and the "+
			"stream ends — so one stuck dashboard can never back-pressure a session write; the "+
			"client reconnects and re-reads the session endpoints to resynchronise. A `: ping` "+
			"comment every 25 seconds holds the connection open through proxies and is how a "+
			"departed client is noticed.")

	spec.Describe("/v1/agent/sessions/:id/events", http.MethodPost,
		"Append one turn to a session's ordered log.",
		"Records a message, tool-call, spawn, log, status or control turn against the session and "+
			"answers 201 with the stored event, including the monotonic `seq` the store assigned — "+
			"the cursor every reader pages from. The same turn is fanned out live to every stream "+
			"subscriber watching that session's tree.\n\n"+
			"Requires a validated principal carrying an org, and the session must already exist IN "+
			"THAT ORG and be one the caller may see: an id belonging to another tenant, or to "+
			"another member's session, is a 404 exactly like one that does not exist, so the log "+
			"can never be written across a tenant boundary or into a colleague's run. `actor` defaults to "+
			"the calling principal when the body names none. `kind` must be one of the six above, "+
			"and `payload` must be valid JSON of at most 64 KiB.\n\n"+
			"The payload is scanned for credentials BEFORE it is stored, and a hit REFUSES the "+
			"write with 422 rather than redacting it: {status, code: \"secret_in_transcript\", "+
			"error, findings:[…]}, each finding naming the rule, severity, line, a masked preview "+
			"and a SHA-256 fingerprint the author can match against the value they rotate. The "+
			"detected value itself appears nowhere in that body, because it was never stored. That "+
			"in-band findings array is the reason this operation cannot be typed.")

	spec.Describe("/v1/agent/sessions/:id/pause", http.MethodPost,
		"Ask a running session to pause.",
		"Records `pause` as a durable control event on the session and answers 200 with "+
			"{command, event, forwarded} — the stored event carries the `seq` that orders it "+
			"against every other turn. "+controlRules)

	spec.Describe("/v1/agent/sessions/:id/resume", http.MethodPost,
		"Ask a paused session to carry on.",
		"Records `resume` as a durable control event on the session and answers 200 with "+
			"{command, event, forwarded}. The session is NOT required to be paused first: the "+
			"only status this refuses is a finished one, because the live status is the running "+
			"surface's to report rather than this endpoint's to enforce. "+controlRules)

	spec.Describe("/v1/agent/sessions/:id/stop", http.MethodPost,
		"Ask a session to stop for good.",
		"Records `stop` as a durable control event on the session and answers 200 with "+
			"{command, event, forwarded}. Stop is the one command that CANCELS a task-backed "+
			"session's durable workflow instead of signalling it — pause, resume and message are "+
			"cooperative signals the workflow decides how to act on, while this tears it down, "+
			"with the request's `message` recorded as the cancellation reason (a default stands in "+
			"when none is given). "+controlRules)

	spec.Describe("/v1/agent/sessions/:id/message", http.MethodPost,
		"Send text into a running session.",
		"Records `message` as a durable control event carrying the caller's text and answers 200 "+
			"with {command, event, forwarded} — this is how a dashboard steers an agent mid-run. "+
			"It is the one command with a required body: a `message` (up to 16 KiB) or a "+
			"`payload`, and 400 with neither. The credential scan that guards an appended turn "+
			"covers `message` and `payload` here. "+controlRules)
}

// controlRules is the half every steering command shares, said once. The four
// commands are one handler (control) and differ only in the word recorded and how
// it is forwarded, so a rule written per command would be the same paragraph four
// times and rot in three of them.
const controlRules = "\n\n" +
	"Requires a validated principal carrying an org, and the session must exist IN THAT ORG and be " +
	"one the caller may see — a member steers the sessions they opened, an org admin or a " +
	"SuperAdmin the org's. Any other id is a 404, so nobody steers another's agents. A FINISHED session (done or " +
	"error) refuses every command with 409: a run that has ended cannot be steered.\n\n" +
	"THE COMMAND IS AN INTENT, NOT A STATE CHANGE. Nothing here writes the session's status. " +
	"A 200 means the command was durably recorded and delivered, never that the agent has " +
	"actually paused, resumed or stopped; the status becomes paused, done or error only when the " +
	"surface running the agent reports it back through a session update. That surface learns of " +
	"the command in one of two ways: a task-backed session (one carrying a workflow id, with a " +
	"tasks backend wired) has it forwarded to the durable-execution engine, and `forwarded` says " +
	"so; everything else is record-only, and the running surface — a locally started `hanzo code` " +
	"session, for one — drains it by polling the session's control endpoint. Today that is every " +
	"session: the only controller wired forwards nothing, so `forwarded` is false and polling is " +
	"how a command arrives. If a forward is attempted and fails, the answer is 502 stating that " +
	"the command was recorded but not forwarded: the intent is never lost."

// sessionOps binds the service to the typed session ops. A TypedHandler takes no
// service parameter, so it arrives as a RECEIVER and every op is a method value
// (o.list) — also the only bound form cmd/zipdoc can lift prose from.
type sessionOps struct{ s *cloud.Service[state] }

// sessionRef addresses one session. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says.
type sessionRef struct {
	// ID is the session to act on, from the path.
	ID string `json:"id"`
}

// sessionDetailIn addresses one session detail read with optional event paging.
type sessionDetailIn struct {
	// ID is the session to read, from the path.
	ID string `json:"id"`
	// After pages forward through the session's events starting strictly after
	// this seq integer; omitted reads the tail of the log instead.
	After string `json:"after"`
	// Limit bounds how many events recentEvents carries (1..1000; default 50 on a
	// tail read, or 200 when paging forward with after).
	Limit int `json:"limit"`
}

// eventPage asks for one page of a session's log.
type eventPage struct {
	// ID is the session to read, from the path.
	ID string `json:"id"`
	// After is the seq of the last event already read; the page starts after it.
	// Absent or zero starts at the first event.
	After int64 `json:"after"`
	// Limit caps the page. Absent, zero or over 1000 reads as 200.
	Limit int `json:"limit"`
}

// eventList is one page of a session's log, oldest first.
type eventList struct {
	// Events is the page, in seq order.
	Events []eventView `json:"events"`
	// Next is the seq to pass as after for the page beyond this one; absent when
	// this page reached the end of the log as it stands now.
	Next int64 `json:"next,omitempty"`
}

// sessionQuery filters the caller org's live sessions.
type sessionQuery struct {
	// Root scopes the page to one subagent tree (its root session id).
	Root string `json:"root"`
	// Parent scopes the page to the direct children of one session. Ignored when
	// root is set; with neither, only ROOT sessions come back.
	Parent string `json:"parent"`
	// Status filters to running, paused, done or error.
	Status string `json:"status"`
	// Project filters to the sessions tagged with one product slug.
	Project string `json:"project"`
	// Channel filters to the sessions started in one collaborative channel — the query a
	// space view runs to show what has been run in it.
	Channel string `json:"channel"`
	// Kind filters to the sessions of one kind of run: "coding" lists coding runs,
	// each carrying its repo, base, branch, environment and pull request.
	Kind string `json:"kind"`
	// Limit caps the page. Absent, zero or over 500 reads as 100.
	Limit int `json:"limit"`
	// After is the `next` of the previous page. Absent starts at the newest.
	After string `json:"after"`
}

// sessionList is a page of the caller org's sessions, newest first.
type sessionList struct {
	// Sessions is the matching sessions, each with its event and child counts and
	// a one-line preview of its latest event.
	Sessions []sessionView `json:"sessions"`
	// Next is the cursor for the page after this one; empty on the last page.
	Next string `json:"next,omitempty"`
}

// markOf is the cursor that resumes a list after x.
func markOf(x Session) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(x.CreatedAt, 10) + ":" + x.ID))
}

// unmark reads a cursor markOf wrote.
func unmark(s string) (Mark, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Mark{}, err
	}
	at, id, ok := strings.Cut(string(raw), ":")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return Mark{}, fmt.Errorf("malformed")
	}
	return Mark{Created: n, ID: id}, nil
}

// ---- register ----

type registerReq struct {
	// Agent is the label the surface opening this session calls itself by
	// ("hanzo-dev"). REQUIRED, up to 128 characters, and free text — nothing
	// resolves it against a defined agent.
	Agent string `json:"agent"`
	// Title is the human line a card shows, up to 512 characters. Optional, and
	// changeable later.
	Title string `json:"title"`
	// Status opens the session in one of running, paused, done or error. Empty means
	// running. A TERMINAL status here (done, error) records a session that has
	// already finished — its end time is stamped now — and nothing can move it
	// afterwards.
	Status string `json:"status"`
	// ParentSessionID makes this a subagent of that session: it inherits the
	// parent's root, so one flow stays one tree. The parent must exist IN THE SAME
	// ORG — a foreign or unknown id is a 400, never a tree across tenants. Empty
	// opens a root session.
	ParentSessionID string `json:"parentSessionId"`
	// TaskWorkflowID links this session to the hanzoai/tasks workflow that executes
	// it, up to 256 characters. Set it and control commands are forwarded to that
	// engine; leave it and the running surface polls for them instead.
	TaskWorkflowID string `json:"taskWorkflowId"`
	// TaskRunID is that workflow's particular run, same bound. Recorded, not
	// resolved: this surface does not check the workflow exists.
	TaskRunID string `json:"taskRunId"`
	// Execution context — where this session runs (all optional).
	Host string `json:"host"`
	// Cwd is the directory the session starts in, up to 1024 characters. It can be
	// moved later, because a linked shell walks around.
	Cwd string `json:"cwd"`
	// Repo is the code being worked on, up to 512 characters. A label the surface
	// states; nothing resolves it against the forge.
	Repo string `json:"repo"`
	// Target names a run-target the org has registered. Unlike Host and Repo it IS
	// resolved: a target that does not exist in this org is a 400, so a session can
	// never claim to run on another tenant's machine. Empty names no machine.
	Target string `json:"target"`
	// Terminal is the URL this session's live terminal is published at, so the
	// console can watch it. Optional — a session that publishes nothing is still
	// a session.
	Terminal string `json:"terminal"`
	// Account tag — the linked AI account this session ran under (login manager).
	Provider string `json:"provider"`
	// Account is which subscription or API account under that provider served the
	// run, up to 256 characters. It is what lets a revoke of that login stop exactly
	// the sessions it was paying for.
	Account string `json:"account"`
	// Channel is the collaborative channel this run was started in (HIP-0523), so a
	// space view can list the sessions of one channel. It is PROVENANCE and is set
	// only here: there is deliberately no way to move a session to another channel, so
	// it is absent from the patch input and from UpdateSession's SET list.
	Channel string `json:"channel"`
	// The readable build (provenance.go): which product this session builds, and
	// whether its story may be read by the world.
	Project string `json:"project"`
	// Published opens this session's story to the public build route. It is refused
	// without a Project, because that route is keyed on (org, project) — a build
	// with no product is not a story anyone can open. False keeps it org-only.
	Published bool `json:"published"`

	// BudgetMicroUSD caps what this session may spend, integer micro-USD. Zero
	// means no cap; a cap can be raised or removed later, never added.
	BudgetMicroUSD int64 `json:"budget_micro_usd"`
}

// RegisterSession opens a live agent session in the caller's org — the row every
// surface (the CLI's outer agent, hanzo.bot, the console, chat) hangs its
// activity off. A session with a parentSessionId becomes a subagent of that
// session and inherits its root, so one flow is one tree; without one it is
// itself a root. Registering with a terminal status records a session that has
// already finished.
//
// Example: {"agent": "hanzo-dev", "title": "ship the landing page", "host": "gpu-01"}
func (o sessionOps) register(ctx context.Context, in *registerReq) (*sessionView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	body := *in
	agent := strings.TrimSpace(body.Agent)
	if agent == "" {
		return nil, zip.ErrBadRequest("agent is required")
	}
	if len(agent) > maxAgentLabel {
		return nil, zip.ErrBadRequest("agent too long")
	}
	if len(body.Title) > maxTitle {
		return nil, zip.ErrBadRequest("title too long")
	}
	status := strings.TrimSpace(body.Status)
	if status == "" {
		status = StatusRunning
	}
	if !validStatus(status) {
		return nil, zip.ErrBadRequest("status must be running|paused|done|error")
	}
	// The session is the caller's: the subject the boundary attested, never a
	// field a client can set — a stated actor filed a session in a colleague's
	// list. A credential that names no stable subject opens nothing.
	sub, ok := principal.SubjectFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a session is opened by a caller with a stable subject")
	}
	actor := billingActor(org, sub)
	if len(actor) > maxActor {
		return nil, zip.ErrBadRequest("actor too long")
	}
	if len(body.TaskWorkflowID) > maxWorkflowRef || len(body.TaskRunID) > maxWorkflowRef {
		return nil, zip.ErrBadRequest("task workflow/run reference too long")
	}
	host, cwd, repo, target, cerr := sessionContext(ctx, sto, org, body.Host, body.Cwd, body.Repo, body.Target)
	if cerr != nil {
		return nil, cerr
	}
	terminal, terr := sessionTerminal(body.Terminal)
	if terr != nil {
		return nil, terr
	}
	provider := strings.TrimSpace(body.Provider)
	account := strings.TrimSpace(body.Account)
	if len(provider) > maxProvider {
		return nil, zip.ErrBadRequest("provider too long")
	}
	if len(account) > maxAccount {
		return nil, zip.ErrBadRequest("account too long")
	}
	project := strings.TrimSpace(body.Project)
	if len(project) > maxProject {
		return nil, zip.ErrBadRequest("project too long")
	}
	if body.Published && project == "" {
		return nil, zip.ErrBadRequest("published requires a project — a build with no product is not a story anyone can open")
	}
	channel := strings.TrimSpace(body.Channel)
	if len(channel) > maxChannel {
		return nil, zip.ErrBadRequest("channel too long")
	}

	id := mint.ID("sess")
	now := time.Now().Unix()
	x := Session{
		ID: id, Org: org, Agent: agent, Actor: actor, Status: status,
		Title:          strings.TrimSpace(body.Title),
		TaskWorkflowID: strings.TrimSpace(body.TaskWorkflowID),
		TaskRunID:      strings.TrimSpace(body.TaskRunID),
		Host:           host, Cwd: cwd, Repo: repo, Terminal: terminal, Target: target,
		Provider: provider, Account: account,
		Channel: channel,
		Project: project, Published: body.Published,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
		// The runtime meter's two facts, settled at the act that opened the
		// session: whose wallet pays, and the instant its clock starts.
		Payer: payerOf(ctx, org).Subject(), MeteredAt: now,
		BudgetMicroUSD: body.BudgetMicroUSD,
	}
	// A session opened finished, or opened paused, has nothing running in it: its
	// run's end is its start, so the runtime meter bills it nothing (a pause writes
	// its end; Store.UpdateSession).
	if isTerminalStatus(status) || status == StatusPaused {
		x.EndedAt = now
	}

	// Subagent linkage. A parent MUST exist IN THE SAME ORG — the tree can never
	// cross a tenant boundary — and be one the caller may see: a member's flow is
	// theirs, and nobody else's session hangs a node in it. RootID is inherited
	// from the parent (all nodes in one flow share it); a session with no parent
	// is itself a root.
	parent := strings.TrimSpace(body.ParentSessionID)
	if parent != "" {
		p, perr := sto.GetSession(ctx, org, parent)
		if perr == errSessionNotFound || (perr == nil && !readerOf(ctx, org).sees(p)) {
			return nil, zip.ErrBadRequest("parentSessionId not found in this org")
		}
		if perr != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "parent: %v", perr)
		}
		x.ParentID = p.ID
		x.RootID = p.RootID
	} else {
		x.RootID = id
	}

	if err := sto.CreateSession(ctx, x); err != nil {
		if err == errParentNotFound {
			return nil, zip.ErrBadRequest("parentSessionId not found in this org")
		}
		return nil, written("persist", err)
	}
	publishSession(s, x, 0, 0)
	v := toSessionView(x, 0, 0)
	return &v, nil
}

// sessionContext validates + binds a session's execution context (host/cwd/repo/
// target). Target, when set, MUST resolve to a run-target in the SAME org (fail-
// closed, exactly like a parent session) so a session can never claim to run on
// another tenant's machine — the #48 dispatch association is tenant-safe.
// sessionTerminal bounds and checks the published terminal URL. It must be https:
// the console FRAMES this value, so anything else is a way to get a javascript:
// or file: URL rendered on a signed-in page. Empty is fine — a session that
// publishes no terminal simply cannot be watched.
func sessionTerminal(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) > maxTerminal {
		return "", zip.ErrBadRequest("terminal url too long")
	}
	if !strings.HasPrefix(v, "https://") {
		return "", zip.ErrBadRequest("terminal url must be https")
	}
	return v, nil
}

func sessionContext(ctx context.Context, sto *Store, org, host, cwd, repo, target string) (string, string, string, string, error) {
	host = strings.TrimSpace(host)
	if len(host) > maxHost {
		return "", "", "", "", zip.ErrBadRequest("host too long")
	}
	cwd = strings.TrimSpace(cwd)
	if len(cwd) > maxCwd {
		return "", "", "", "", zip.ErrBadRequest("cwd too long")
	}
	repo = remote.Clean(repo)
	if len(repo) > maxRepo {
		return "", "", "", "", zip.ErrBadRequest("repo too long")
	}
	target = strings.TrimSpace(target)
	if target != "" {
		if len(target) > maxSessionID {
			return "", "", "", "", zip.ErrBadRequest("target too long")
		}
		// A machine the caller does not reach — another member's, in a personal
		// org — is answered as no machine, so no session is tagged with it and
		// the answer tells nothing of what exists.
		if _, err := sto.reachable(ctx, org, target); err == errTargetNotFound {
			return "", "", "", "", zip.ErrBadRequest("target not found in this org")
		} else if err != nil {
			return "", "", "", "", zip.Errorf(http.StatusInternalServerError, "target: %v", err)
		}
	}
	return host, cwd, repo, target, nil
}

// ---- list ----

// ListSessions returns the caller org's live sessions, newest first — each with
// its event count, its direct-child count and a one-line preview of its latest
// event. With no filter it returns ROOT sessions only, so a dashboard shows one
// row per flow rather than one per subagent; ?root= or ?parent= descends.
//
// Example: {"status": "running", "limit": 20}
func (o sessionOps) list(ctx context.Context, in *sessionQuery) (*sessionList, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	f := SessionFilter{
		Root:    trimField(in.Root),
		Parent:  trimField(in.Parent),
		Status:  trimField(in.Status),
		Project: trimField(in.Project),
		Channel: trimField(in.Channel),
		Kind:    trimField(in.Kind),
	}
	// The page bound, read once: 0 (absent) and anything over 500 are 100, which
	// is what an unparseable ?limit= produced before. One more row than the page
	// is read, and only to know whether another page follows.
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	f.Limit = limit + 1
	if a := trimField(in.After); a != "" {
		m, err := unmark(a)
		if err != nil {
			return nil, zip.ErrBadRequest("after is not a cursor this listing issued")
		}
		f.After = m
	}
	if f.Status != "" && !validStatus(f.Status) {
		return nil, zip.ErrBadRequest("status must be running|paused|done|error")
	}
	// A member sees the sessions they opened; a SuperAdmin, and an admin of a
	// customer's org, sees the org's (readerOf). The actor is the attested
	// caller on every way in, never a field a client can set — and a caller
	// with no person behind it lists what the org's own work opened, not
	// everything.
	r := readerOf(ctx, org)
	if !r.filter(&f) {
		return &sessionList{Sessions: []sessionView{}}, nil
	}
	rows, err := sto.ListSessions(ctx, org, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = markOf(rows[limit-1])
	}
	out := make([]sessionView, 0, len(rows))
	for _, x := range rows {
		ev, _ := sto.CountEvents(ctx, org, x.ID)
		ch, _ := sto.CountChildren(ctx, org, x.ID, r)
		v := toSessionView(x, ev, ch)
		if last, ok, _ := sto.LastEvent(ctx, org, x.ID); ok {
			v.LastEvent = toLastEventView(last)
		}
		// Answer from the row and bring the estimate up to date BEHIND the answer,
		// so a board stays a read and gains the new number on its next poll. Most
		// calls do nothing at all: refresh returns immediately for a run that is
		// terminal, recently estimated, or has said nothing since. See progress.go.
		s.State.prog.refresh(sto, org, x)
		out = append(out, v)
	}
	return &sessionList{Sessions: out, Next: next}, nil
}

// ---- detail ----

// GetSession returns one session with its direct child sessions and a page of
// its events, oldest of those first (50 from the tail by default, up to `limit`
// up to 1000, or forward from `after` with a `next` cursor). The children are
// those the caller may see, by the rule the session itself is read by: a
// member's own, and every one for an admin of the org. In a held org, where
// self-serve signups land beside the platform's staff, a session is its
// person's alone.
//
// Example: {"id": "sess_1"}
func (o sessionOps) get(ctx context.Context, in *sessionDetailIn) (*sessionDetail, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	if in.Limit < 0 {
		return nil, zip.ErrBadRequest("limit must be non-negative")
	}
	var (
		hasAfter bool
		afterSeq int64
	)
	if rawAfter := strings.TrimSpace(in.After); rawAfter != "" {
		n, perr := strconv.ParseInt(rawAfter, 10, 64)
		if perr != nil || n < 0 {
			return nil, zip.ErrBadRequest("after must be a non-negative integer")
		}
		hasAfter = true
		afterSeq = n
	}
	id := in.ID
	// A run whose process went away reads as ended, with its reason (reap.go).
	s.State.orphaned(ctx, sto, org, id)
	x, err := visible(ctx, sto, org, id)
	if err != nil {
		return nil, err
	}
	// The children the caller may see (sees), filtered where the list is: a
	// subagent another member opened under this session is theirs.
	f := SessionFilter{Parent: id}
	r := readerOf(ctx, org)
	var kids []Session
	if r.filter(&f) {
		kids, err = sto.ListSessions(ctx, org, f)
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "children: %v", err)
	}
	var (
		events []Event
		next   int64
	)
	if hasAfter {
		limit := in.Limit
		if limit <= 0 {
			limit = 200
		} else if limit > 1000 {
			limit = 1000
		}
		events, err = sto.ListEvents(ctx, org, id, afterSeq, limit+1)
		if err == nil && len(events) > limit {
			events = events[:limit]
			next = events[limit-1].Seq
		}
	} else {
		limit := in.Limit
		if limit <= 0 {
			limit = recentEvents
		} else if limit > 1000 {
			limit = 1000
		}
		events, err = sto.TailEvents(ctx, org, id, limit)
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "events: %v", err)
	}
	evCount, _ := sto.CountEvents(ctx, org, id)
	kidViews := make([]sessionView, 0, len(kids))
	for _, k := range kids {
		kc, _ := sto.CountChildren(ctx, org, k.ID, r)
		ke, _ := sto.CountEvents(ctx, org, k.ID)
		kidViews = append(kidViews, toSessionView(k, ke, kc))
	}
	evViews := make([]eventView, 0, len(events))
	for _, e := range events {
		evViews = append(evViews, toEventView(e))
	}
	s.State.prog.refresh(sto, org, x)
	return &sessionDetail{
		sessionView:  toSessionView(x, evCount, len(kids)),
		Children:     kidViews,
		RecentEvents: evViews,
		Next:         next,
	}, nil
}

// Reads a session's log in order, a page at a time: everything after the seq a
// reader last saw, so a poll resumes exactly where it stopped. It is how to see
// what a run did and why it ended — each model turn, tool call, status and log
// line, as the run wrote them. A run whose process went away is ended first,
// with its reason as the last event.
//
// Example: {"id": "sess_1", "after": 0, "limit": 100}
func (o sessionOps) events(ctx context.Context, in *eventPage) (*eventList, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	s.State.orphaned(ctx, sto, org, id)
	if _, err := visible(ctx, sto, org, id); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	evs, err := sto.ListEvents(ctx, org, id, max(in.After, 0), limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "events: %v", err)
	}
	out := &eventList{Events: make([]eventView, 0, len(evs))}
	for _, e := range evs {
		out.Events = append(out.Events, toEventView(e))
	}
	if len(evs) == limit {
		out.Next = evs[len(evs)-1].Seq
	}
	return out, nil
}

// reader is who asks for a session, reduced to the two facts the visibility rule
// reads: the caller as a session records who opened it, and whether they
// administer the org.
type reader struct {
	actor string // billingActor(org, sub): the org itself when no person is asking; empty when the caller names no stable subject
	admin bool   // a SuperAdmin, or an admin of the acting org when it is a customer's
	super bool   // a SuperAdmin: also reads, and only reads, a session recorded with no actor
}

// readerOf reads the caller off ctx on EVERY way in. cloud.Who is the identity
// boundary's minted headers on a request (X-User-Id, X-User-IsAdmin,
// X-User-IsOrgAdmin — what principal.Super and principal.IsOrgAdmin read)
// and the stated caller on the plane, so the rule holds with no request behind
// the call instead of being skipped there. Nobody at all reads as the org's own
// work, which opened no person's session.
//
// In a personal org an admin of the org is one more stranger, as they are to
// its machines (targetOwns). A session is where a run takes its next words
// from — the surface executing it drains the commands posted to it (drain), on
// its person's own computer or their machine — so reading one names the machine
// it is on and steering one puts work there. Only a SuperAdmin reaches another
// person's.
//
// The actor is the subject the boundary attested (principal.SubjectFrom), so a
// credential that names no stable subject — a token with no sub, whose user is a
// username or a display name — is nobody's and reads no person's session.
func readerOf(ctx context.Context, org string) reader {
	who := cloud.Who(ctx)
	r := reader{admin: who.Admin || who.OrgAdmin && !personal(org), super: who.Admin}
	if sub, ok := principal.SubjectFrom(ctx); ok {
		r.actor = billingActor(org, sub)
	}
	return r
}

// sees is the one rule, the same the listing filters by (Store.ListSessions): a
// member sees the sessions they opened, and an admin of a customer's org or a
// SuperAdmin sees the org's. A session recorded with no actor — opened before
// sessions were attributed — names nobody and is nobody's: a SuperAdmin alone
// reads one, and nobody acts on it (actable). (Regression: it was the org's and
// every member's, so in a shared org every customer read the others'.)
//
// THE APP'S CONVERSATION IS ITS PERSON'S (talk.go), as a Chat conversation is, and
// so is every coding run in its tree (private): no admin of the org lists, watches or
// steers another member's. A SuperAdmin may read one by its id, and each such read
// is audited (visible); nothing more.
func (r reader) sees(x Session) bool {
	if x.Actor == "" {
		return r.super
	}
	if private(x) {
		return r.actor != "" && x.Actor == r.actor
	}
	return r.admin || r.actor != "" && x.Actor == r.actor
}

// filter narrows a listing to what r sees: a member to their own actor, an org
// admin to every attributed session, a SuperAdmin to all of them. It reports
// false for a reader who sees none at all — a caller naming no stable subject —
// whose listing is empty without asking the store.
func (r reader) filter(f *SessionFilter) bool {
	switch {
	case r.super:
		f.Private, f.Holder = true, r.actor
	case r.admin:
		f.Attributed, f.Private, f.Holder = true, true, r.actor
	case r.actor == "":
		return false
	default:
		f.Actor, f.Attributed = r.actor, true
	}
	return true
}

// visible is one session of org as the caller may read it and act on it — every
// op on one session asks it, so who may rename, publish, pause or stop a run is
// who may see it. Another member's session answers exactly as an absent one does,
// so a session id says nothing about whose it is. A SuperAdmin's read of another
// person's conversation, or a run in it, is audited, and not made when it cannot
// be (overheard).
func visible(ctx context.Context, sto *Store, org, id string) (Session, error) {
	x, past, err := sought(ctx, sto, org, id)
	if err != nil {
		return Session{}, err
	}
	if past {
		if err := overheard(ctx, x); err != nil {
			return Session{}, err
		}
	}
	return x, nil
}

// sought is one session of org as the caller may read it, and whether reading it
// reaches past its person: a SuperAdmin reading another's conversation or a run in
// it, which visible audits.
func sought(ctx context.Context, sto *Store, org, id string) (Session, bool, error) {
	if len(id) > maxSessionID {
		return Session{}, false, zip.ErrNotFound("session not found")
	}
	x, err := sto.GetSession(ctx, org, id)
	if err == errSessionNotFound {
		return Session{}, false, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return Session{}, false, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if r := readerOf(ctx, org); !r.sees(x) {
		if !r.super || !private(x) {
			return Session{}, false, zip.ErrNotFound("session not found")
		}
		return x, true, nil
	}
	return x, false, nil
}

// actable is one session of org as the caller may act on it — rename, publish,
// budget, post to, pause, resume, stop, delete or drain. It is visible, less a
// session recorded with no actor: that one a SuperAdmin may read and nobody may
// move, so it answers as an absent one does. Nothing a SuperAdmin may only read
// is acted on, so no read is audited here.
func actable(ctx context.Context, sto *Store, org, id string) (Session, error) {
	x, _, err := sought(ctx, sto, org, id)
	if err != nil {
		return Session{}, err
	}
	if !canAct(readerOf(ctx, org), x) {
		return Session{}, zip.ErrNotFound("session not found")
	}
	return x, nil
}

// canAct reports whether reader r may mutate or tear down session x: the
// session must be attributed, visible to r, and — for a private app
// conversation or a run in it — owned by r's own actor (a SuperAdmin reads one
// and never moves or deletes it).
func canAct(r reader, x Session) bool {
	if x.Actor == "" {
		return false
	}
	if private(x) {
		return r.actor != "" && x.Actor == r.actor
	}
	return r.sees(x)
}

// Visible is one session of the caller's org, as GET /v1/agent/sessions/{id}
// lets the caller read it — for an op in this process that reads a run through
// its session, so who may read a run's work is decided by the rule that decides
// who may read the run. A session this process does not hold is [cloud.ErrNoPeer].
func Visible(ctx context.Context, id string) (Session, error) {
	if mounted == nil {
		return Session{}, fmt.Errorf("%w: agents (this process does not own the session store)", cloud.ErrNoPeer)
	}
	sto, org, err := tenantStore(ctx, &mounted.State)
	if err != nil {
		return Session{}, err
	}
	return visible(ctx, sto, org, strings.TrimSpace(id))
}

// ---- progress ----

// SessionProgress returns how far along one run is: the share of its goal that
// is done, whether it is running, blocked or finished, and a line saying what it
// is doing right now.
//
// It is a MODEL ESTIMATE read off the run's own transcript, not a measurement —
// `estimated` says so on every answer, and a run whose progress cannot be told
// reports phase "unknown" with no percentage rather than a zero it does not
// mean. A session that has already finished answers from its own status instead,
// and is marked not estimated.
//
// The list and detail reads carry the same value; this address is the one that
// WAITS. Where the stored estimate has gone stale it is remade before answering,
// so a human deciding whether to step into a run gets a current reading rather
// than the last poll's — which costs one small completion, charged to the same
// wallet the session already names, at most once every thirty seconds per run.
//
// Example: {"id": "sess_1"}
func (o sessionOps) progress(ctx context.Context, in *sessionRef) (*sessionProgress, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	// A run whose process went away reads as ended, with its reason (reap.go):
	// whatever polls this for done is told, rather than told "running" forever.
	s.State.orphaned(ctx, sto, org, id)
	x, err := visible(ctx, sto, org, id)
	if err != nil {
		return nil, err
	}
	// The estimate is remade in line, not behind the answer. A stale reading is
	// still returned when it cannot be — measure keeps the last good estimate and
	// publishes its age — so a gateway that is down costs freshness, never the
	// address.
	if prog := s.State.prog; prog.stale(x, time.Now().Unix()) && prog.claim(org, id) {
		defer prog.release(org, id)
		if p, err := prog.measure(ctx, sto, org, x); err == nil {
			x.ProgressPct, x.ProgressPhase, x.ProgressActivity = p.Pct, p.Phase, p.Activity
			x.ProgressAt, x.ProgressEstimated = p.At, p.Estimated
		}
	}
	v := progressOf(x)
	return &v, nil
}

// ---- tree ----

// SessionTree returns the subagent-flow graph rooted at this session: the session,
// its children, their children, each node carrying its own event count. One
// indexed read pulls the whole flow (every node of a flow shares a root id), so
// the shape is assembled in memory rather than by walking the store per node. It
// holds the sessions the caller may see — a member's own, every one for an admin
// of the org — and a session they may not see is absent with all beneath it.
//
// Example: {"id": "sess_1"}
func (o sessionOps) tree(ctx context.Context, in *sessionRef) (*treeNode, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	x, err := visible(ctx, sto, org, id)
	if err != nil {
		return nil, err
	}
	// One indexed query pulls the whole tree (same RootID); assemble in memory.
	nodes, err := sto.ListTree(ctx, org, x.RootID, treeNodeCap)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "tree: %v", err)
	}
	who := readerOf(ctx, org)
	nodes = slices.DeleteFunc(nodes, func(n Session) bool { return !who.sees(n) })
	counts, err := sto.EventCountsByRoot(ctx, org, x.RootID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "counts: %v", err)
	}
	node := buildSubtree(nodes, counts, id)
	return &node, nil
}

// buildSubtree assembles the flat tree rows into the node rooted at rootAtID.
// children map is built once; each node's Children int (fan-out) comes from the
// map, its Events from counts. A missing rootAtID yields an empty node (the
// caller already verified the session exists, so this is defensive).
func buildSubtree(nodes []Session, counts map[string]int, rootAtID string) treeNode {
	childrenOf := map[string][]Session{}
	byID := map[string]Session{}
	for _, n := range nodes {
		byID[n.ID] = n
		childrenOf[n.ParentID] = append(childrenOf[n.ParentID], n)
	}
	var build func(x Session) treeNode
	build = func(x Session) treeNode {
		kids := childrenOf[x.ID]
		node := treeNode{Session: toSessionView(x, counts[x.ID], len(kids))}
		for _, k := range kids {
			node.Children = append(node.Children, build(k))
		}
		return node
	}
	root, ok := byID[rootAtID]
	if !ok {
		return treeNode{}
	}
	return build(root)
}

// ---- patch (status/title, surface-owned truth) ----

// patchSessionIn is a partial update. Every field is optional — a field the
// request omits is left alone — and the session is addressed by the path.
//
// The mutable fields are spelled out HERE rather than in an embedded body struct
// that has exactly one user: zip's schema walk skips an embedded field of an
// unexported type, so an embedded body would publish a request schema holding
// only `id` and every generated client would be unable to send anything.
type patchSessionIn struct {
	// ID is the session to update, from the path.
	ID string `json:"id"`
	// Status moves the session to running, paused, done or error. A session that has
	// already finished refuses any change with 409 — done and error are monotonic —
	// and moving INTO one stamps the end time. This is the surface REPORTING what
	// happened; a control command never writes it.
	Status *string `json:"status"`
	// Title rewrites the human line, up to 512 characters — usually because the work
	// turned out to be something other than what it was opened as.
	Title *string `json:"title"`
	// Target re-dispatches a session to a run-target (the #48 association). "" detaches.
	Target *string `json:"target"`
	// Terminal publishes (or, with "", withdraws) the URL this session's live
	// terminal can be watched at. A pointer so "absent" and "withdrawn" are
	// different requests: a session that stops sharing must be able to say so.
	Terminal *string `json:"terminal"`
	// Project tags the product this session built; Published is the author's
	// decision to let anyone read the story (provenance.go). Both are pointers so
	// "absent" and "cleared" are different requests.
	Project *string `json:"project"`
	// Published opens the session's story to the public build route; false withdraws
	// it, and withdrawing is always allowed. PUBLISHING is refused unless the
	// session names a Project — the one set in this same request, or the one already
	// stored — because that route is keyed on (org, project). It widens READ access
	// to what is already there and grants nothing else.
	Published *bool `json:"published"`
	// Error is why the session ended in error: the exit status or refusal and the
	// last lines the process wrote. Kept only on a session in error.
	Error *string `json:"error"`
	// Cwd is where the session is working NOW.
	//
	// It was write-once — captured at register and never again — which is right
	// for a run that starts in a directory and stays there, and wrong for a linked
	// shell, which is a place a person moves around in. The console showed the
	// directory `hanzo link` happened to be run from and kept showing it after the
	// shell had walked away, so the field answered "which work is this" with an
	// answer that was true once. A pointer, so an unchanged path is an omitted
	// field rather than a repeated write.
	Cwd *string `json:"cwd"`
}

// PatchSession updates a session's surface-owned truth: its status, its title,
// the run-target it is dispatched to, and the product it built plus whether that
// build's story is public. A FINISHED session stays finished — reopening a
// done/error run would fabricate liveness — and publishing is refused unless the
// session names the project it built, because the public build route is keyed on
// (org, project).
//
// Example: {"id": "sess_1", "status": "done"}
func (o sessionOps) patch(ctx context.Context, in *patchSessionIn) (*sessionView, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	x, err := actable(ctx, sto, org, id)
	if err != nil {
		return nil, err
	}
	body := *in
	if body.Status != nil {
		ns := strings.TrimSpace(*body.Status)
		if !validStatus(ns) {
			return nil, zip.ErrBadRequest("status must be running|paused|done|error")
		}
		// A finished session stays finished (truthful, monotonic terminal state):
		// reopening a done/error run would fabricate liveness.
		if isTerminalStatus(x.Status) && ns != x.Status {
			return nil, zip.Errorf(http.StatusConflict, "session is %s; cannot change status", x.Status)
		}
		x.Status = ns
		if isTerminalStatus(ns) && x.EndedAt == 0 {
			x.EndedAt = time.Now().Unix()
		}
	}
	// A reason is no reopening: a session already in error takes one.
	if body.Error != nil && x.Status == StatusError {
		if why := strings.TrimSpace(*body.Error); why != "" {
			x.Error = text.Cut(why, maxError)
		}
	}
	if body.Title != nil {
		if len(*body.Title) > maxTitle {
			return nil, zip.ErrBadRequest("title too long")
		}
		x.Title = strings.TrimSpace(*body.Title)
	}
	if body.Project != nil {
		np := strings.TrimSpace(*body.Project)
		if len(np) > maxProject {
			return nil, zip.ErrBadRequest("project too long")
		}
		x.Project = np
	}
	if body.Published != nil {
		// Publishing is the author's act and it is what makes the PUBLIC build
		// route answer at all, so it is refused unless the session names the
		// product it built — /v1/agent/builds is keyed on (org, project).
		if *body.Published && x.Project == "" {
			return nil, zip.ErrBadRequest("published requires a project — a build with no product is not a story anyone can open")
		}
		x.Published = *body.Published
	}
	if body.Target != nil {
		nt := strings.TrimSpace(*body.Target)
		if nt != "" {
			if len(nt) > maxSessionID {
				return nil, zip.ErrBadRequest("target too long")
			}
			// The rule register keeps (sessionContext): a machine the caller does
			// not reach is answered as no machine, and no session is moved onto it.
			if _, terr := sto.reachable(ctx, org, nt); terr == errTargetNotFound {
				return nil, zip.ErrBadRequest("target not found in this org")
			} else if terr != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "target: %v", terr)
			}
		}
		x.Target = nt // "" detaches
	}
	if body.Terminal != nil {
		nt, terr := sessionTerminal(*body.Terminal)
		if terr != nil {
			return nil, terr
		}
		x.Terminal = nt // "" withdraws
	}
	if body.Cwd != nil {
		nc := strings.TrimSpace(*body.Cwd)
		// The SAME bound register applies (sessionContext) — one rule for one
		// field, whichever endpoint the value arrives through.
		if len(nc) > maxCwd {
			return nil, zip.ErrBadRequest("cwd too long")
		}
		x.Cwd = nc
	}
	x.UpdatedAt = time.Now().Unix()
	if err := sto.UpdateSession(ctx, x); err != nil {
		if err == errSessionNotFound {
			return nil, zip.ErrNotFound("session not found")
		}
		return nil, written("update", err)
	}
	// The store decides a pause's end and a resume's new start (UpdateSession), so
	// the answer is the row as written, not the struct this handler sent.
	if y, err := sto.GetSession(ctx, org, id); err == nil {
		x = y
	}
	ev, _ := sto.CountEvents(ctx, org, id)
	ch, _ := sto.CountChildren(ctx, org, id, readerOf(ctx, org))
	publishSession(s, x, ev, fanOut(ctx, sto, x))
	v := toSessionView(x, ev, ch)
	return &v, nil
}

// ---- delete ----

// sessionDeleted confirms one session (and any subagent subtree beneath it)
// has been stopped and removed from the caller org's registry.
type sessionDeleted struct {
	// Deleted is true once the session and its events have been removed.
	Deleted bool `json:"deleted"`
	// ID is the removed session's handle.
	ID string `json:"id"`
}

// DeleteSession stops a session if it is still live, settles any unbilled
// runtime, and removes the session, its subagent subtree, and their recorded
// events from the caller org's registry.
//
// Only the session's owner — or, in a customer org for non-private sessions, an
// org admin or SuperAdmin — may delete it; another member's session, a
// cross-tenant id, and an unknown id all answer 404.
//
// Example: {"id": "sess_1"}
func (o sessionOps) del(ctx context.Context, in *sessionRef) (*sessionDeleted, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	x, err := actable(ctx, sto, org, id)
	if err != nil {
		return nil, err
	}

	// Collect x and any descendants in its subtree so deleting a root session
	// tears down live subagents and leaves no orphaned child rows behind.
	r := readerOf(ctx, org)
	targets := []Session{x}
	if tree, terr := sto.ListTree(ctx, org, x.RootID, treeNodeCap); terr == nil {
		targets = subtreeSessions(tree, x, r)
	} else {
		return nil, zip.Errorf(http.StatusInternalServerError, "tree: %v", terr)
	}

	ids := make([]string, 0, len(targets))
	for _, n := range targets {
		if !isTerminalStatus(n.Status) {
			if _, _, serr := stop(ctx, sto, n, "deleted by its owner"); serr != nil {
				return nil, zip.Errorf(http.StatusInternalServerError, "stop: %v", serr)
			}
			if fresh, gerr := sto.GetSession(ctx, org, n.ID); gerr == nil {
				n = fresh
			}
		}
		closeSessionRuntime(ctx, s, sto, org, n)
		ids = append(ids, n.ID)
	}

	deleted, err := sto.DeleteSessions(ctx, org, ids)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if deleted == 0 {
		return nil, zip.ErrNotFound("session not found")
	}
	return &sessionDeleted{Deleted: true, ID: x.ID}, nil
}

// subtreeSessions returns root and every descendant of root in nodes that
// reader r may act on, in breadth-first order.
func subtreeSessions(nodes []Session, root Session, r reader) []Session {
	children := make(map[string][]Session)
	for _, n := range nodes {
		if n.ParentID != "" {
			children[n.ParentID] = append(children[n.ParentID], n)
		}
	}
	out := []Session{root}
	seen := map[string]bool{root.ID: true}
	for i := 0; i < len(out); i++ {
		for _, k := range children[out[i].ID] {
			if seen[k.ID] || !canAct(r, k) {
				continue
			}
			seen[k.ID] = true
			out = append(out, k)
		}
	}
	return out
}

// ---- append event ----

// ---- control (record intent + forward to the tasks engine when task-backed) ----

// controlReq is a steering command's optional body. pause, resume and stop usually
// carry none; message requires one of the two fields.
type controlReq struct {
	// Message is free text for the running agent, up to 16 KiB, scanned for
	// credentials before it is stored like the payload. On a stop it is recorded
	// as the cancellation reason.
	Message string `json:"message"`
	// Payload is a structured argument for the command: any valid JSON up to 64 KiB,
	// scanned for credentials before it is stored (a hit refuses the command with
	// 422). It is what a task-backed session receives as the forwarded signal's
	// argument.
	Payload json.RawMessage `json:"payload"`
}

type controlPayload struct {
	Command string          `json:"command"`
	Message string          `json:"message,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func reasonOf(msg string) string {
	if m := strings.TrimSpace(msg); m != "" {
		return m
	}
	return "stopped via control plane"
}

func signalPayload(b controlReq) []byte {
	if len(b.Payload) > 0 {
		return b.Payload
	}
	if b.Message != "" {
		m, _ := json.Marshal(b.Message)
		return m
	}
	return nil
}

// ---- run integration (#5): a /v1/agent/:name/run opens a root session ----

// openRunSession records a completed agent run as a ROOT session so every run is
// visible in the same registry the @hanzo/dev outer-agent flows use. Best-effort:
// a bookkeeping failure NEVER fails the run (the run + its billing already
// happened). A cloud one-shot run is a synchronous completion, so the session is
// born terminal with one log event; TaskWorkflowID is left empty because the run
// is not (yet) a tasks workflow — when runs are promoted to hanzoai/tasks
// ExecuteWorkflow, set TaskWorkflowID/TaskRunID here from the workflow handle.
func openRunSession(s *cloud.Service[state], ctx context.Context, a Agent, r Run, actor string) {
	// The run's own agent names the org, and the run already happened under it —
	// this is bookkeeping for a completed execution, not a new authorization.
	sto, err := s.State.storeFor(a.Org)
	if err != nil {
		return
	}
	status := StatusDone
	if r.Status != "ok" {
		status = StatusError
	}
	id := mint.ID("sess")
	ts := r.CreatedAt
	if ts == 0 {
		ts = time.Now().Unix()
	}
	x := Session{
		ID: id, Org: a.Org, Agent: a.Name, Actor: actor, Status: status,
		RootID: id, Title: runTitle(r.Input),
		StartedAt: ts, EndedAt: ts, CreatedAt: ts, UpdatedAt: ts,
		// Born terminal — started and ended in one call — so its runtime is zero
		// and its watermark IS its end. It is therefore in no sweep set from the
		// moment it is written, which is right: what a run costs is the per-run
		// fee runAgent already charged, and billing an instant twice would be
		// billing one act under two names.
		Payer: payerOf(ctx, a.Org).Subject(), MeteredAt: ts,
	}
	if err := sto.CreateSession(ctx, x); err != nil {
		s.Log.Warn("run session: create", "org", a.Org, "agent", a.Name, "err", err)
		return
	}
	n := 0
	// The answer is a message, so a client renders the text; the metadata
	// stays in the log event beside it.
	if r.Output != "" {
		text, _ := json.Marshal(map[string]string{"text": r.Output})
		if e, err := sto.AppendEvent(ctx, Event{
			ID: mint.ID("evt"), SessionID: id, Org: a.Org, Kind: KindMessage, Actor: actor,
			Payload: string(text), CreatedAt: ts,
		}); err == nil {
			n++
			defer publishEvent(s, x, e)
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"runId": r.ID, "status": r.Status, "model": r.Model,
		"durationMs": r.DurationMs, "error": r.Error,
	})
	e, aerr := sto.AppendEvent(ctx, Event{
		ID: mint.ID("evt"), SessionID: id, Org: a.Org, Kind: KindLog, Actor: actor,
		Payload: string(payload), CreatedAt: ts,
	})
	if aerr == nil {
		n++
		defer publishEvent(s, x, e)
	}
	publishSession(s, x, n, 0)
}

func runTitle(input string) string {
	t := strings.TrimSpace(input)
	if t == "" {
		return "agent run"
	}
	if len(t) > 120 {
		t = t[:120]
	}
	return t
}

// ---- stream publish helpers (nil-safe: a bus-less Service, e.g. a direct-construct
// unit test, simply skips the live fan-out; the store is still the truth) ----
//
// A NIL SERVICE is that same case one step further out, and is answered the same
// way: a test that drives a pass over a bare *state (meter_test.go, reap_test.go)
// holds no Service at all, and a stream with no subscriber is nothing to publish
// to. It is never production — the passes are started by Mount, which sets it.

func publishSession(s *cloud.Service[state], x Session, events, children int) {
	if s == nil || s.State.bus == nil {
		return
	}
	v := toSessionView(x, events, children)
	s.State.bus.publish(streamUpdate{Org: x.Org, RootID: x.RootID, Actor: x.Actor, Channel: x.Channel, Type: "session", Session: &v})
}

// publishEvent fans out one event of session x, carrying x's org, flow and owner
// so the stream can tell who may watch it (sessionsStream).
func publishEvent(s *cloud.Service[state], x Session, e Event) {
	if s == nil || s.State.bus == nil {
		return
	}
	v := toEventView(e)
	s.State.bus.publish(streamUpdate{Org: x.Org, RootID: x.RootID, Actor: x.Actor, Channel: x.Channel, Type: "event", Event: &v})
}

// ---- small query helpers ----
//
// queryInt is gone with the last untyped reader: a typed op's page size arrives
// on its In, bound by zip from the URL, and the store clamps it — one bound, in
// the one place that owns the query.

func trimField(v string) string { return strings.TrimSpace(v) }
