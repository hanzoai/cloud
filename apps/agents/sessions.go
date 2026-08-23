package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// This file mounts the LIVE agent-session control plane under /v1/agents/sessions
// — the canonical registry every surface (the @hanzo/dev CLI outer agent,
// hanzo.bot, console, chat, app) hangs off. It is the VIEW + control + ZAP-stream
// layer over durable execution; the durable run itself is a hanzoai/tasks
// workflow (see sessions_tasks.go), never a bespoke scheduler here.
//
//	POST   /v1/agents/sessions              register a session (opt parentSessionId) -> Session
//	GET    /v1/agents/sessions              list live sessions (filter root/parent/status) -> {sessions:[...]}
//	GET    /v1/agents/sessions/stream       SSE feed of session+event updates (rides ZAP)
//	GET    /v1/agents/sessions/:id          detail + direct children + recent events -> SessionDetail
//	PATCH  /v1/agents/sessions/:id          update status/title -> Session
//	GET    /v1/agents/sessions/:id/tree     the full subagent-flow graph -> TreeNode
//	POST   /v1/agents/sessions/:id/events   append an event (message/tool-call/spawn/log) -> Event
//	POST   /v1/agents/sessions/:id/{pause,resume,stop,message}  control command -> {command,event,forwarded}
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
)

// Control commands — the closed vocabulary of remote steering.
const (
	CmdPause   = "pause"
	CmdResume  = "resume"
	CmdStop    = "stop"
	CmdMessage = "message"
)

const (
	maxTitle        = 512
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
)

func validKind(k string) bool {
	switch k {
	case KindMessage, KindToolCall, KindSpawn, KindLog, KindStatus, KindControl:
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
	// The readable build: the product this session built and whether its story
	// is public (provenance.go).
	Project string `json:"project,omitempty"`
	// Published is the author's decision to let anyone read this session's story at
	// the public build route. It only ever widens READ access to a session that
	// already exists and grants nothing else; false, an unpublished session is
	// invisible there no matter who asks. It cannot be true without a Project,
	// because that route is keyed on (org, project).
	Published bool `json:"published,omitempty"`

	// Events is how many turns the session's log holds, counted at read time. It is
	// the whole log, however few of them RecentEvents carries.
	Events int `json:"events"`
	// Children is the DIRECT fan-out — how many sessions name this one as parent —
	// and not the size of the subtree. Read the tree for that.
	Children int `json:"children"`
	// StartedAt is when the session opened, RFC 3339 in UTC to the second.
	StartedAt string `json:"startedAt"`
	// EndedAt is when it reached done or error, same format. Empty while it is still
	// running or paused, which is how absence reads here: not over yet.
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
	return &lastEventView{Seq: e.Seq, Kind: e.Kind, Actor: e.Actor, Preview: p, At: rfc3339(e.CreatedAt)}
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
	// business and varies by Kind; this surface does not interpret it.
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
	// RecentEvents is the 50 most recent turns, OLDEST of those first — a transcript
	// to read down, not a feed. The promoted `events` integer says how many the log
	// holds in total; page the rest from a seq.
	RecentEvents []eventView `json:"recentEvents"`
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
		Host: x.Host, Cwd: x.Cwd, Repo: x.Repo, Terminal: x.Terminal, Target: x.Target,
		Provider: x.Provider, Account: x.Account,
		Project: x.Project, Published: x.Published,
		Events: events, Children: children,
		StartedAt: rfc3339(x.StartedAt), EndedAt: rfc3339(x.EndedAt),
		CreatedAt: rfc3339(x.CreatedAt), UpdatedAt: rfc3339(x.UpdatedAt),
	}
}

func toEventView(e Event) eventView {
	var p json.RawMessage
	if e.Payload != "" {
		p = json.RawMessage(e.Payload)
	}
	return eventView{
		ID: e.ID, SessionID: e.SessionID, Seq: e.Seq, Kind: e.Kind, Actor: e.Actor,
		Payload: p, CreatedAt: rfc3339(e.CreatedAt),
	}
}

// mountSessions registers the sessions routes. It MUST be called before the
// /v1/agents/:name wildcard (Fiber matches in registration order, so a bare
// :name would otherwise capture "sessions"). Within the block, the static
// /stream route precedes the /:id param for the same reason.
//
// The typed ops are declared on the GROUP, so each op's path is the group's
// prefix composed with its leaf — the same composition the router does, and the
// identity every projection keys on.
func mountSessions(s *cloud.Service[state], app cloud.Router) {
	o := sessionOps{s: s}
	g := app.Group("/v1/agents")
	zip.Post(g, "/sessions", o.register, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/sessions", o.list)
	// UNTYPED, and it cannot be otherwise: the stream is an open Server-Sent
	// Events response written by a loop that OUTLIVES the handler
	// (c.SendStreamWriter), and a typed op returns one marshalled value. There is
	// no In/Out that describes a feed.
	g.Get("/sessions/stream", cloud.Handle(s, sessionsStream))
	zip.Get(g, "/sessions/:id", o.get)
	zip.Patch(g, "/sessions/:id", o.patch)
	zip.Get(g, "/sessions/:id/tree", o.tree)
	// All five were raw because the guard gate answers 422 IN BAND with the
	// findings that refused the write, and zip's error carried a sentence and no
	// body — so a typed op would have dropped the array that tells an author WHICH
	// secret to rotate. zip v1.31.3 carries extension members on the error, so the
	// refusal keeps its shape and these keep their registry entry. See
	// sessions_typed.go for why the merge is safe HERE and not on the money wire.
	zip.Post(g, "/sessions/:id/events", o.appendEvent, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/sessions/:id/control", o.drain)
	zip.Post(g, "/sessions/:id/pause", o.pause)
	zip.Post(g, "/sessions/:id/resume", o.resume)
	zip.Post(g, "/sessions/:id/stop", o.stop)
	zip.Post(g, "/sessions/:id/message", o.message)

	// The readable build (provenance.go). PUBLIC — no tenancy — because the only
	// rows either route can reach are ones an author explicitly published. A
	// visitor opening a product follows the session that produced it; the owner
	// reads the same session through the org-scoped /sessions routes above.
	zip.Get(g, "/builds", o.builds)
	zip.Get(g, "/builds/:org/:project", o.build)
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
	openapi.Describe("/v1/agents/sessions/stream", http.MethodGet,
		"Live session and event updates for the caller's org, as Server-Sent Events.",
		"Holds the connection open as text/event-stream and pushes a frame each time the org's "+
			"registry moves: an `event: session` frame carrying the same session shape the list and "+
			"detail reads answer with (a registration, an update, or a login-manager revoke tearing "+
			"a session down), and an `event: event` frame carrying one appended turn. Optional "+
			"?root=<session id> narrows the feed to a single subagent tree.\n\n"+
			"Requires a validated principal carrying an org; 403 without one. Org-scoped "+
			"fail-closed: the bus filters on tenant before it fans out, so a subscriber only ever "+
			"receives its own org's updates, and ?root= narrows that further but can never widen "+
			"it.\n\n"+
			"Delivery is best-effort and the GET reads remain the source of truth. A subscriber "+
			"that falls more than 256 frames behind is DROPPED — its channel is closed and the "+
			"stream ends — so one stuck dashboard can never back-pressure a session write; the "+
			"client reconnects and re-reads the session endpoints to resynchronise. A `: ping` "+
			"comment every 25 seconds holds the connection open through proxies and is how a "+
			"departed client is noticed.")

	openapi.Describe("/v1/agents/sessions/:id/events", http.MethodPost,
		"Append one turn to a session's ordered log.",
		"Records a message, tool-call, spawn, log, status or control turn against the session and "+
			"answers 201 with the stored event, including the monotonic `seq` the store assigned — "+
			"the cursor every reader pages from. The same turn is fanned out live to every stream "+
			"subscriber watching that session's tree.\n\n"+
			"Requires a validated principal carrying an org, and the session must already exist IN "+
			"THAT ORG: an id belonging to another tenant is a 404 exactly like one that does not "+
			"exist, so the log can never be written across a tenant boundary. `actor` defaults to "+
			"the calling principal when the body names none. `kind` must be one of the six above, "+
			"and `payload` must be valid JSON of at most 64 KiB.\n\n"+
			"The payload is scanned for credentials BEFORE it is stored, and a hit REFUSES the "+
			"write with 422 rather than redacting it: {status, code: \"secret_in_transcript\", "+
			"error, findings:[…]}, each finding naming the rule, severity, line, a masked preview "+
			"and a SHA-256 fingerprint the author can match against the value they rotate. The "+
			"detected value itself appears nowhere in that body, because it was never stored. That "+
			"in-band findings array is the reason this operation cannot be typed.")

	openapi.Describe("/v1/agents/sessions/:id/pause", http.MethodPost,
		"Ask a running session to pause.",
		"Records `pause` as a durable control event on the session and answers 200 with "+
			"{command, event, forwarded} — the stored event carries the `seq` that orders it "+
			"against every other turn. "+controlRules)

	openapi.Describe("/v1/agents/sessions/:id/resume", http.MethodPost,
		"Ask a paused session to carry on.",
		"Records `resume` as a durable control event on the session and answers 200 with "+
			"{command, event, forwarded}. The session is NOT required to be paused first: the "+
			"only status this refuses is a finished one, because the live status is the running "+
			"surface's to report rather than this endpoint's to enforce. "+controlRules)

	openapi.Describe("/v1/agents/sessions/:id/stop", http.MethodPost,
		"Ask a session to stop for good.",
		"Records `stop` as a durable control event on the session and answers 200 with "+
			"{command, event, forwarded}. Stop is the one command that CANCELS a task-backed "+
			"session's durable workflow instead of signalling it — pause, resume and message are "+
			"cooperative signals the workflow decides how to act on, while this tears it down, "+
			"with the request's `message` recorded as the cancellation reason (a default stands in "+
			"when none is given). "+controlRules)

	openapi.Describe("/v1/agents/sessions/:id/message", http.MethodPost,
		"Send text into a running session.",
		"Records `message` as a durable control event carrying the caller's text and answers 200 "+
			"with {command, event, forwarded} — this is how a dashboard steers an agent mid-run. "+
			"It is the one command with a required body: a `message` (up to 16 KiB) or a "+
			"`payload`, and 400 with neither. The credential scan that guards an appended turn "+
			"covers `payload` here; `message` is bounded but not scanned. "+controlRules)
}

// controlRules is the half every steering command shares, said once. The four
// commands are one handler (control) and differ only in the word recorded and how
// it is forwarded, so a rule written per command would be the same paragraph four
// times and rot in three of them.
const controlRules = "\n\n" +
	"Requires a validated principal carrying an org, and the session must exist IN THAT ORG — a " +
	"foreign id is a 404, so no tenant can steer another's agents. A FINISHED session (done or " +
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

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

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
	// Limit caps the page. Absent, zero or over 500 reads as 100.
	Limit int `json:"limit"`
}

// sessionList is a page of the caller org's sessions, newest first.
type sessionList struct {
	// Sessions is the matching sessions, each with its event and child counts and
	// a one-line preview of its latest event.
	Sessions []sessionView `json:"sessions"`
}

// ---- register ----

type registerReq struct {
	// Agent is the label the surface opening this session calls itself by
	// ("hanzo-dev"). REQUIRED, up to 128 characters, and free text — nothing
	// resolves it against a defined agent.
	Agent string `json:"agent"`
	// Actor is the "org/sub" identity to record the session under, up to 256
	// characters. Omit it and the calling principal is used, which is almost always
	// what you want: it is what a login revoke matches on to stop this session.
	Actor string `json:"actor"`
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
	// The readable build (provenance.go): which product this session builds, and
	// whether its story may be read by the world.
	Project string `json:"project"`
	// Published opens this session's story to the public build route. It is refused
	// without a Project, because that route is keyed on (org, project) — a build
	// with no product is not a story anyone can open. False keeps it org-only.
	Published bool `json:"published"`
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
	actor := strings.TrimSpace(body.Actor)
	if actor == "" {
		actor = billingActor(org, callerOf(ctx))
	}
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

	id := mint.ID("sess")
	now := time.Now().Unix()
	x := Session{
		ID: id, Org: org, Agent: agent, Actor: actor, Status: status,
		Title:          strings.TrimSpace(body.Title),
		TaskWorkflowID: strings.TrimSpace(body.TaskWorkflowID),
		TaskRunID:      strings.TrimSpace(body.TaskRunID),
		Host:           host, Cwd: cwd, Repo: repo, Terminal: terminal, Target: target,
		Provider: provider, Account: account,
		Project: project, Published: body.Published,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if isTerminalStatus(status) {
		x.EndedAt = now
	}

	// Subagent linkage. A parent MUST exist IN THE SAME ORG — the tree can never
	// cross a tenant boundary. RootID is inherited from the parent (all nodes in
	// one flow share it); a session with no parent is itself a root.
	parent := strings.TrimSpace(body.ParentSessionID)
	if parent != "" {
		p, perr := sto.GetSession(ctx, org, parent)
		if perr == errSessionNotFound {
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
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
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
	repo = strings.TrimSpace(repo)
	if len(repo) > maxRepo {
		return "", "", "", "", zip.ErrBadRequest("repo too long")
	}
	target = strings.TrimSpace(target)
	if target != "" {
		if len(target) > maxSessionID {
			return "", "", "", "", zip.ErrBadRequest("target too long")
		}
		if _, err := sto.GetTarget(ctx, org, target); err == errTargetNotFound {
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
		// ListSessions owns the page bound: it reads 0 (absent) and anything over
		// 500 as its own 100, which is what an unparseable ?limit= produced before.
		Limit: in.Limit,
	}
	if f.Status != "" && !validStatus(f.Status) {
		return nil, zip.ErrBadRequest("status must be running|paused|done|error")
	}
	rows, err := sto.ListSessions(ctx, org, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]sessionView, 0, len(rows))
	for _, x := range rows {
		ev, _ := sto.CountEvents(ctx, org, x.ID)
		ch, _ := sto.CountChildren(ctx, org, x.ID)
		v := toSessionView(x, ev, ch)
		if last, ok, _ := sto.LastEvent(ctx, org, x.ID); ok {
			v.LastEvent = toLastEventView(last)
		}
		out = append(out, v)
	}
	return &sessionList{Sessions: out}, nil
}

// ---- detail ----

// GetSession returns one session with its direct child sessions and its 50 most
// recent events, oldest of those first.
//
// Example: {"id": "sess_1"}
func (o sessionOps) get(ctx context.Context, in *sessionRef) (*sessionDetail, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if len(id) > maxSessionID {
		return nil, zip.ErrNotFound("session not found")
	}
	x, err := sto.GetSession(ctx, org, id)
	if err == errSessionNotFound {
		return nil, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	kids, err := sto.ListSessions(ctx, org, SessionFilter{Parent: id})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "children: %v", err)
	}
	events, err := sto.ListEvents(ctx, org, id, 0, recentEvents)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "events: %v", err)
	}
	evCount, _ := sto.CountEvents(ctx, org, id)
	kidViews := make([]sessionView, 0, len(kids))
	for _, k := range kids {
		kc, _ := sto.CountChildren(ctx, org, k.ID)
		ke, _ := sto.CountEvents(ctx, org, k.ID)
		kidViews = append(kidViews, toSessionView(k, ke, kc))
	}
	evViews := make([]eventView, 0, len(events))
	for _, e := range events {
		evViews = append(evViews, toEventView(e))
	}
	return &sessionDetail{
		sessionView:  toSessionView(x, evCount, len(kids)),
		Children:     kidViews,
		RecentEvents: evViews,
	}, nil
}

// ---- tree ----

// SessionTree returns the subagent-flow graph rooted at this session: the session,
// its children, their children, each node carrying its own event count. One
// indexed read pulls the whole flow (every node of a flow shares a root id), so
// the shape is assembled in memory rather than by walking the store per node.
//
// Example: {"id": "sess_1"}
func (o sessionOps) tree(ctx context.Context, in *sessionRef) (*treeNode, error) {
	s := o.s
	sto, org, err := tenantStore(ctx, &s.State)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if len(id) > maxSessionID {
		return nil, zip.ErrNotFound("session not found")
	}
	x, err := sto.GetSession(ctx, org, id)
	if err == errSessionNotFound {
		return nil, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	// One indexed query pulls the whole tree (same RootID); assemble in memory.
	nodes, err := sto.ListTree(ctx, org, x.RootID, treeNodeCap)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "tree: %v", err)
	}
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
	x, err := sto.GetSession(ctx, org, id)
	if err == errSessionNotFound {
		return nil, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
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
		// product it built — /v1/agents/builds is keyed on (org, project).
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
			if _, terr := sto.GetTarget(ctx, org, nt); terr == errTargetNotFound {
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
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	ev, _ := sto.CountEvents(ctx, org, id)
	ch, _ := sto.CountChildren(ctx, org, id)
	publishSession(s, x, ev, ch)
	v := toSessionView(x, ev, ch)
	return &v, nil
}

// ---- append event ----

// eventReq is one appended turn. The session is addressed by the path, so the body
// carries only what the turn IS.
type eventReq struct {
	// Kind is the turn's kind — message, tool-call, spawn, log, status or control.
	// Outside that closed vocabulary is a 400.
	Kind string `json:"kind"`
	// Actor is who produced the turn. Empty defaults to the calling principal.
	Actor string `json:"actor"`
	// Payload is the turn's body: any valid JSON up to 64 KiB. Scanned for
	// credentials before it is stored — a hit refuses the whole write with 422 and
	// nothing is persisted.
	Payload json.RawMessage `json:"payload"`
}

// ---- control (record intent + forward to the tasks engine when task-backed) ----

// controlReq is a steering command's optional body. pause, resume and stop usually
// carry none; message requires one of the two fields.
type controlReq struct {
	// Message is free text for the running agent, up to 16 KiB. On a stop it is
	// recorded as the cancellation reason.
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

// ---- run integration (#5): a /v1/agents/:name/run opens a root session ----

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
	}
	if err := sto.CreateSession(ctx, x); err != nil {
		s.Log.Warn("run session: create", "org", a.Org, "agent", a.Name, "err", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"runId": r.ID, "status": r.Status, "model": r.Model,
		"durationMs": r.DurationMs, "error": r.Error,
	})
	evID := mint.ID("evt")
	e, aerr := sto.AppendEvent(ctx, Event{
		ID: evID, SessionID: id, Org: a.Org, Kind: KindLog, Actor: actor,
		Payload: string(payload), CreatedAt: ts,
	})
	publishSession(s, x, 1, 0)
	if aerr == nil {
		publishEvent(s, a.Org, x.RootID, e)
	}
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

func publishSession(s *cloud.Service[state], x Session, events, children int) {
	if s.State.bus == nil {
		return
	}
	v := toSessionView(x, events, children)
	s.State.bus.publish(streamUpdate{Org: x.Org, RootID: x.RootID, Type: "session", Session: &v})
}

func publishEvent(s *cloud.Service[state], org, rootID string, e Event) {
	if s.State.bus == nil {
		return
	}
	v := toEventView(e)
	s.State.bus.publish(streamUpdate{Org: org, RootID: rootID, Type: "event", Event: &v})
}

// ---- small query helpers ----
//
// queryInt is gone with the last untyped reader: a typed op's page size arrives
// on its In, bound by zip from the URL, and the store clamps it — one bound, in
// the one place that owns the query.

func trimField(v string) string { return strings.TrimSpace(v) }
