package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
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
	ID string `json:"id"`
	// Org is the caller's OWN tenant, echoed so a client can build the public
	// build URL (/builds/:org/:project) without a second call or a guess. It is
	// never another tenant's — every read is org-scoped before it gets here.
	Org             string `json:"org"`
	Agent           string `json:"agent"`
	Actor           string `json:"actor,omitempty"`
	Status          string `json:"status"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	RootSessionID   string `json:"rootSessionId"`
	Title           string `json:"title,omitempty"`
	TaskWorkflowID  string `json:"taskWorkflowId,omitempty"`
	TaskRunID       string `json:"taskRunId,omitempty"`
	// Execution context (mission-control): the machine/repo/cwd a card shows and
	// the run-target a session is dispatched to. Omitted when a surface didn't report it.
	Host string `json:"host,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
	Repo string `json:"repo,omitempty"`
	// Terminal is where this session can be WATCHED — the URL the machine
	// published for its live terminal. Omitted when it publishes none.
	Terminal string `json:"terminal,omitempty"`
	Target   string `json:"target,omitempty"`
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
	// The readable build: the product this session built and whether its story
	// is public (provenance.go).
	Project   string `json:"project,omitempty"`
	Published bool   `json:"published,omitempty"`

	Events    int    `json:"events"`
	Children  int    `json:"children"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
	CreatedAt string `json:"createdAt"`
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
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`
	Actor   string `json:"actor,omitempty"`
	Preview string `json:"preview,omitempty"`
	At      string `json:"at"`
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
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"createdAt"`
}

// sessionDetail is one session plus what only the detail read carries: its direct
// children and its most recent events. sessionView is EMBEDDED (promoted inline on
// the wire) rather than spelled out again — it is the shared list projection, and a
// second copy of its 22 fields is a second thing to forget to update. See the note
// on agentDetail for why the published schema of an embedded shape is currently
// short of its promoted fields, and where that is fixed.
type sessionDetail struct {
	sessionView
	Children     []sessionView `json:"childSessions"`
	RecentEvents []eventView   `json:"recentEvents"`
}

// treeNode is one node of the subagent-flow graph: a session plus its children,
// recursively. Node = {session, children:[...]} — the session's own Children int
// is the direct fan-out count, the children array is the materialised subtree.
type treeNode struct {
	Session  sessionView `json:"session"`
	Children []treeNode  `json:"children"`
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
// identity every projection keys on. cloud.Bridge is installed once, at the top
// of Mount, ahead of this call.
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
	// UNTYPED, all five: the guard gate answers 422 IN BAND with the findings that
	// refused the write ({status, code, error, findings:[…]}, provenance.go), and
	// zip's error type carries {status, code, error} and nothing else — a typed op
	// would silently drop the findings array that tells the author WHICH secret to
	// rotate. They go typed when zip can express a response with a body per status.
	g.Post("/sessions/:id/events", cloud.Handle(s, appendSessionEvent))
	zip.Get(g, "/sessions/:id/control", o.drain)
	g.Post("/sessions/:id/pause", cloud.Handle(s, pauseSession))
	g.Post("/sessions/:id/resume", cloud.Handle(s, resumeSession))
	g.Post("/sessions/:id/stop", cloud.Handle(s, stopSession))
	g.Post("/sessions/:id/message", cloud.Handle(s, messageSession))

	// The readable build (provenance.go). PUBLIC — no tenancy — because the only
	// rows either route can reach are ones an author explicitly published. A
	// visitor opening a product follows the session that produced it; the owner
	// reads the same session through the org-scoped /sessions routes above.
	zip.Get(g, "/builds", o.builds)
	zip.Get(g, "/builds/:org/:project", o.build)
}

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
	Agent           string `json:"agent"`
	Actor           string `json:"actor"`
	Title           string `json:"title"`
	Status          string `json:"status"`
	ParentSessionID string `json:"parentSessionId"`
	TaskWorkflowID  string `json:"taskWorkflowId"`
	TaskRunID       string `json:"taskRunId"`
	// Execution context — where this session runs (all optional).
	Host   string `json:"host"`
	Cwd    string `json:"cwd"`
	Repo   string `json:"repo"`
	Target string `json:"target"`
	// Terminal is the URL this session's live terminal is published at, so the
	// console can watch it. Optional — a session that publishes nothing is still
	// a session.
	Terminal string `json:"terminal"`
	// Account tag — the linked AI account this session ran under (login manager).
	Provider string `json:"provider"`
	Account  string `json:"account"`
	// The readable build (provenance.go): which product this session builds, and
	// whether its story may be read by the world.
	Project   string `json:"project"`
	Published bool   `json:"published"`
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
	org, err := tenantOf(ctx)
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
	host, cwd, repo, target, cerr := sessionContext(ctx, s, org, body.Host, body.Cwd, body.Repo, body.Target)
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

	id, err := genID("sess")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
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
		p, perr := s.State.store.GetSession(ctx, org, parent)
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

	if err := s.State.store.CreateSession(ctx, x); err != nil {
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

func sessionContext(ctx context.Context, s *cloud.Service[state], org, host, cwd, repo, target string) (string, string, string, string, error) {
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
		if _, err := s.State.store.GetTarget(ctx, org, target); err == errTargetNotFound {
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
	org, err := tenantOf(ctx)
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
	rows, err := s.State.store.ListSessions(ctx, org, f)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]sessionView, 0, len(rows))
	for _, x := range rows {
		ev, _ := s.State.store.CountEvents(ctx, org, x.ID)
		ch, _ := s.State.store.CountChildren(ctx, org, x.ID)
		v := toSessionView(x, ev, ch)
		if last, ok, _ := s.State.store.LastEvent(ctx, org, x.ID); ok {
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
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if len(id) > maxSessionID {
		return nil, zip.ErrNotFound("session not found")
	}
	x, err := s.State.store.GetSession(ctx, org, id)
	if err == errSessionNotFound {
		return nil, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	kids, err := s.State.store.ListSessions(ctx, org, SessionFilter{Parent: id})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "children: %v", err)
	}
	events, err := s.State.store.ListEvents(ctx, org, id, 0, recentEvents)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "events: %v", err)
	}
	evCount, _ := s.State.store.CountEvents(ctx, org, id)
	kidViews := make([]sessionView, 0, len(kids))
	for _, k := range kids {
		kc, _ := s.State.store.CountChildren(ctx, org, k.ID)
		ke, _ := s.State.store.CountEvents(ctx, org, k.ID)
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
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if len(id) > maxSessionID {
		return nil, zip.ErrNotFound("session not found")
	}
	x, err := s.State.store.GetSession(ctx, org, id)
	if err == errSessionNotFound {
		return nil, zip.ErrNotFound("session not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	// One indexed query pulls the whole tree (same RootID); assemble in memory.
	nodes, err := s.State.store.ListTree(ctx, org, x.RootID, treeNodeCap)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "tree: %v", err)
	}
	counts, err := s.State.store.EventCountsByRoot(ctx, org, x.RootID)
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
	ID     string  `json:"id"`
	Status *string `json:"status"`
	Title  *string `json:"title"`
	// Target re-dispatches a session to a run-target (the #48 association). "" detaches.
	Target *string `json:"target"`
	// Terminal publishes (or, with "", withdraws) the URL this session's live
	// terminal can be watched at. A pointer so "absent" and "withdrawn" are
	// different requests: a session that stops sharing must be able to say so.
	Terminal *string `json:"terminal"`
	// Project tags the product this session built; Published is the author's
	// decision to let anyone read the story (provenance.go). Both are pointers so
	// "absent" and "cleared" are different requests.
	Project   *string `json:"project"`
	Published *bool   `json:"published"`
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
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := in.ID
	x, err := s.State.store.GetSession(ctx, org, id)
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
			if _, terr := s.State.store.GetTarget(ctx, org, nt); terr == errTargetNotFound {
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
	x.UpdatedAt = time.Now().Unix()
	if err := s.State.store.UpdateSession(ctx, x); err != nil {
		if err == errSessionNotFound {
			return nil, zip.ErrNotFound("session not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	ev, _ := s.State.store.CountEvents(ctx, org, id)
	ch, _ := s.State.store.CountChildren(ctx, org, id)
	publishSession(s, x, ev, ch)
	v := toSessionView(x, ev, ch)
	return &v, nil
}

// ---- append event ----

type eventReq struct {
	Kind    string          `json:"kind"`
	Actor   string          `json:"actor"`
	Payload json.RawMessage `json:"payload"`
}

func appendSessionEvent(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	x, err := s.State.store.GetSession(c.Context(), org, id)
	if err == errSessionNotFound {
		return zip.ErrNotFound("session not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body eventReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	kind := strings.TrimSpace(body.Kind)
	if !validKind(kind) {
		return zip.ErrBadRequest("kind must be message|tool-call|spawn|log|status|control")
	}
	if len(body.Payload) > maxEventPayload {
		return zip.ErrBadRequest("payload too large")
	}
	if len(body.Payload) > 0 && !json.Valid(body.Payload) {
		return zip.ErrBadRequest("payload must be valid JSON")
	}
	// The guard gate. A transcript turn is stored ONLY after it is proved free of
	// credentials — the same engine the code-security surface runs, at the write
	// boundary, refusing loudly. See guardEvent in provenance.go for why this
	// refuses rather than redacts.
	if leaks := guardEvent(string(body.Payload)); leaks != nil {
		return refuseLeak(c, leaks)
	}
	actor := strings.TrimSpace(body.Actor)
	if actor == "" {
		actor = billingActor(org, c.User())
	}
	if len(actor) > maxActor {
		return zip.ErrBadRequest("actor too long")
	}
	evID, err := genID("evt")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	e, err := s.State.store.AppendEvent(c.Context(), Event{
		ID: evID, SessionID: id, Org: org, Kind: kind, Actor: actor,
		Payload: string(body.Payload), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "append: %v", err)
	}
	publishEvent(s, org, x.RootID, e)
	return c.JSON(http.StatusCreated, toEventView(e))
}

// ---- control (record intent + forward to the tasks engine when task-backed) ----

type controlReq struct {
	Message string          `json:"message"`
	Payload json.RawMessage `json:"payload"`
}

type controlPayload struct {
	Command string          `json:"command"`
	Message string          `json:"message,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func pauseSession(s *cloud.Service[state], c *zip.Ctx) error   { return control(s, c, CmdPause) }
func resumeSession(s *cloud.Service[state], c *zip.Ctx) error  { return control(s, c, CmdResume) }
func stopSession(s *cloud.Service[state], c *zip.Ctx) error    { return control(s, c, CmdStop) }
func messageSession(s *cloud.Service[state], c *zip.Ctx) error { return control(s, c, CmdMessage) }

// control records a steering command as a durable control event (the intent the
// running surface consumes) and, when the session is backed by a hanzoai/tasks
// workflow AND a tasks backend is wired, forwards it to the engine's signal/
// cancel API. Org/actor-authorized: principal.Org already requires a validated
// principal AND same-org ownership of the session, so no other tenant can steer.
func control(s *cloud.Service[state], c *zip.Ctx, command string) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	x, err := s.State.store.GetSession(c.Context(), org, id)
	if err == errSessionNotFound {
		return zip.ErrNotFound("session not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if isTerminalStatus(x.Status) {
		return zip.Errorf(http.StatusConflict, "session is %s; cannot %s a finished session", x.Status, command)
	}
	// The control body is optional (pause/resume/stop often carry none); only
	// parse when present so a bodyless command is not a 400.
	var body controlReq
	if len(c.Body()) > 0 {
		if err := c.Bind(&body); err != nil {
			return err
		}
	}
	if len(body.Message) > maxControlMsg {
		return zip.ErrBadRequest("message too long")
	}
	if len(body.Payload) > maxEventPayload {
		return zip.ErrBadRequest("payload too large")
	}
	if len(body.Payload) > 0 && !json.Valid(body.Payload) {
		return zip.ErrBadRequest("payload must be valid JSON")
	}
	// The guard gate. A transcript turn is stored ONLY after it is proved free of
	// credentials — the same engine the code-security surface runs, at the write
	// boundary, refusing loudly. See guardEvent in provenance.go for why this
	// refuses rather than redacts.
	if leaks := guardEvent(string(body.Payload)); leaks != nil {
		return refuseLeak(c, leaks)
	}
	if command == CmdMessage && strings.TrimSpace(body.Message) == "" && len(body.Payload) == 0 {
		return zip.ErrBadRequest("message requires a 'message' or 'payload'")
	}

	actor := billingActor(org, c.User())
	cp, _ := json.Marshal(controlPayload{Command: command, Message: body.Message, Payload: body.Payload})
	evID, err := genID("evt")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	e, err := s.State.store.AppendEvent(c.Context(), Event{
		ID: evID, SessionID: id, Org: org, Kind: KindControl, Actor: actor,
		Payload: string(cp), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "record control: %v", err)
	}
	publishEvent(s, org, x.RootID, e)

	// Forward to the durable-execution engine when this session is task-backed.
	// The intent is ALREADY durably recorded above, so a forward failure is
	// reported (502) without losing the command; a session with no workflow link
	// or no wired backend is record-only (stream-consuming surfaces act on it).
	forwarded := false
	if x.TaskWorkflowID != "" && s.State.tasks != nil && s.State.tasks.Enabled() {
		var ferr error
		if command == CmdStop {
			ferr = s.State.tasks.Cancel(c.Context(), x.TaskWorkflowID, x.TaskRunID, reasonOf(body.Message))
		} else {
			ferr = s.State.tasks.Signal(c.Context(), x.TaskWorkflowID, x.TaskRunID, command, signalPayload(body))
		}
		if ferr != nil {
			return zip.Errorf(http.StatusBadGateway, "control recorded but tasks forward failed: %v", ferr)
		}
		forwarded = true
	}
	return c.JSON(http.StatusOK, map[string]any{
		"command": command, "event": toEventView(e), "forwarded": forwarded,
	})
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
	if s.State.store == nil {
		return
	}
	status := StatusDone
	if r.Status != "ok" {
		status = StatusError
	}
	id, err := genID("sess")
	if err != nil {
		s.Log.Warn("run session: rng", "err", err)
		return
	}
	ts := r.CreatedAt
	if ts == 0 {
		ts = time.Now().Unix()
	}
	x := Session{
		ID: id, Org: a.Org, Agent: a.Name, Actor: actor, Status: status,
		RootID: id, Title: runTitle(r.Input),
		StartedAt: ts, EndedAt: ts, CreatedAt: ts, UpdatedAt: ts,
	}
	if err := s.State.store.CreateSession(ctx, x); err != nil {
		s.Log.Warn("run session: create", "org", a.Org, "agent", a.Name, "err", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"runId": r.ID, "status": r.Status, "model": r.Model,
		"durationMs": r.DurationMs, "error": r.Error,
	})
	evID, err := genID("evt")
	if err != nil {
		publishSession(s, x, 0, 0)
		return
	}
	e, aerr := s.State.store.AppendEvent(ctx, Event{
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
