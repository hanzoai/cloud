package team

// This file is the space data plane the SPA connects to after
// selectWorkspace — the transactor. The RPC dispatch and query semantics are
// this package's own; the
// TRANSPORT is rewritten from golang.org/x/net/websocket + core.RequestEvent to
// zip's wsx (fasthttp/websocket) at app.Get("/v1/team/transactor/:token", ...).
//
// The wire is preserved exactly: every frame is a ZAP Envelope wrapping one
// JSON-RPC message; hello negotiates binary:false (JSON, never msgpack);
// serverVersion is the MODEL version (modelVersion(), pinned 0.6.0); findAll
// returns the TotalArray shape; ping→pong!; tx broadcasts through the hub.

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"

	luxlog "github.com/luxfi/log"
	"github.com/valyala/fasthttp"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"
)

// Heartbeat frames (the client's pingConst/pongConst), carried verbatim inside a
// ZAP envelope payload.
const (
	pingFrame = "ping"
	pongFrame = "pong!"
)

// Bot is the minimal projection of an org agent the roster reconcile needs. It is
// the client that keeps clients/team decoupled from clients/agents' concrete Agent
// type — team names only what it projects (id, name, active).
type Bot struct {
	ID     string
	Name   string
	Active bool
}

// BotLister sources an org's bots (the canonical in-process agents). It is
// injected in Mount as an adapter over agents.ListForOrg — the ONE in-process
// client, replacing the removed IAM-SA HTTP enumeration. org is always a VERIFIED
// tenant (the transactor token's extra.org), never a client header.
type BotLister func(ctx context.Context, org string) ([]Bot, error)

// transServer holds the transactor's shared, process-lifetime state: the per-
// space SQLite docs store (the structured data plane — no KV, no Postgres),
// the class hierarchy parsed from the embedded model, the live-broadcast hub, the
// identity client, and the two roster sources (the account store's members +
// the in-process agents lister).
type transServer struct {
	store    *docStore
	hier     *hierarchy
	hub      *hub
	ident    *identity     // who is calling, and what may they touch (account.go)
	accounts *accountStore // human members (this deployment's spaces)
	bots     BotLister     // bot members (the org's in-process agents)
	runAgent AgentRunner   // the Chunter responder's LLM client (agents.RunOnBehalf); nil = responder OFF
	log      luxlog.Logger // best-effort responder logging; nil-safe (tests leave it unset)

	// Chunter responder bounds (chat.go). ALL zero-value-safe so a bare
	// transServer literal (tests, the sync path) is inert-but-correct.
	startedAt int64         // process boot (unix millis); messages older than this are backfill → never answered (0 = no filter, tests)
	sem       chan struct{} // hard concurrency cap on in-flight agent turns (nil = uncapped, tests)
	inflight  sync.Map      // single-flight: (space|space|bot) currently answering → drop duplicates
	breaker   sync.Map      // per-agent circuit breaker: agentID → *agentBreaker (backoff on repeated failure)

	// degraded is the fail-closed posture Mount resolved (no HS256 secret). The
	// untyped WebSocket route gets it through Mount's guard wrapper; the typed
	// statistics op is not a zip.Handler and cannot be wrapped, so it reads this
	// instead (typed.go).
	degraded bool
}

// live is the process-singleton transactor server, published in Mount so the
// in-process projection path (Apply / ingest) and the /v1/team/bots/sync handler
// can write into the per-space store the SPA reads WITHOUT holding a client
// WebSocket. One server, one store.
var live *transServer

// The prose for the data-plane socket. It is UNTYPED and cannot be otherwise —
// the response is a protocol upgrade, not a value — so zipdoc has nothing to
// lift and without this it publishes an operationId and nothing else. The
// statistics op beside it documents itself from its own doc comment.
//
// The path key is the FIBER pattern exactly as registered in Mount.
func init() {
	openapi.Describe("/v1/team/transactor/:token", http.MethodGet,
		"Open the space data-plane socket",
		"Upgrades to the WebSocket the Team client runs an entire space over: every frame "+
			"is a ZAP envelope wrapping one JSON-RPC message — findAll/findOne reads against the "+
			"space's documents, tx writes that broadcast to the other live sessions, hello "+
			"negotiating JSON rather than msgpack. The response is a protocol upgrade, so there "+
			"is no body to read.\n\n"+
			"THE PATH SEGMENT IS THE CREDENTIAL. It is the space token selectWorkspace "+
			"minted — bearer-equivalent, and sitting in a URL that proxies and access logs "+
			"record, which is exactly why it expires in twelve hours and is re-minted on demand "+
			"rather than being long-lived like the session token. It is decoded and verified "+
			"(signature and expiry) BEFORE the upgrade, so a bad one is a 401 and never a socket "+
			"that is accepted and then dropped, and it must carry both an account and a "+
			"space claim. Nothing ambient authorizes this socket: a WebSocket is exempt from "+
			"CORS, so a cookie-borne credential would make the Origin check the only access "+
			"control on the whole data plane.\n\n"+
			"The tenant is the token's SIGNED org claim and it keys every store path, so no "+
			"header can name another space's data. The upgrade ALSO refuses a browser Origin "+
			"outside the team surfaces with 403 — otherwise any page could open an authenticated "+
			"socket with a token it lured out of a logged-in browser — while a request with no "+
			"Origin at all is admitted, because that is what a non-browser client sends.\n\n"+
			"On connect the space's system spaces are seeded once and the roster is "+
			"reconciled every time, so the org's human members and its bots are present as "+
			"space people without a separate sync call.")
}

// serveWS AUTHORIZES the caller BEFORE the WebSocket upgrade (fail-secure: a
// refusal is a 401, never an upgraded-then-dropped socket), then upgrades and runs
// the frame loop. The org is the VERIFIED tenant — the key for every store path —
// never a client header.
//
// The path segment carries whichever lane the caller is on, and a UUID is not a
// JWT so the two can never be read as each other:
//
// THE PATH SEGMENT IS THE CREDENTIAL — the space token selectWorkspace minted,
// whose signed claims name both the account and the space. Nothing ambient
// authorizes this socket; see admitWS for why it must stay that way and what the
// IAM lane here will look like.
func (srv *transServer) serveWS(c *zip.Ctx) error {
	cl, ws, err := srv.admitWS(c.Param("token"))
	if err != nil || ws == "" {
		return zip.ErrUnauthorized("invalid space token")
	}
	sess := &session{
		server:    srv,
		store:     srv.store,
		hier:      srv.hier,
		account:   cl.account,
		org:       cl.org,
		space:     ws,
		sessionID: c.Query("sessionId"),
	}
	return wsx.Upgrade(func(conn *wsx.Conn) error {
		sess.conn = conn
		sess.seedSpace()       // system spaces (once per space)
		sess.reconcileRoster() // humans + bots as Person/Employee (every connect)
		srv.hub.add(sess)
		defer srv.hub.remove(sess)
		sess.loop(conn)
		return nil
	}, wsx.Config{CheckOrigin: wsOriginOK})(c)
}

// admitWS resolves the socket's caller and the space it may open, from the
// CREDENTIAL IN THE PATH SEGMENT and nothing ambient.
//
// THE TRANSACTOR HAS NO IAM LANE, and that is a decision rather than an omission.
// A browser can put a credential on a WebSocket in exactly two places: the URL, or
// a cookie. The URL is where the HS256 space token already sits, which is
// survivable only because that token is scoped to one space for twelve hours —
// an estate-wide IAM bearer in a path that proxies and access logs record is not.
// And the cookie is worse here than anywhere else in this file: a WebSocket is
// EXEMPT FROM CORS, so a foreign page may open one and read every frame, and
// SameSite=Lax is scoped to the registrable domain — so any first-party page that
// can be made to run script opens an authenticated space socket with the
// victim's ambient cookie and reads and writes the whole stream. An Origin check is
// then the only access control, which makes one wildcard in an allowlist a total
// compromise of the data plane.
//
// The shape that works is the one the sibling socket already uses: collabws.go
// upgrades first and takes the credential IN-BAND in an Auth frame, so nothing
// ambient authorizes anything. Giving the transactor that lane means the client
// sends a frame it does not send today, which is a front change and therefore a
// later phase — named here so the next reader implements THAT rather than
// re-deriving the cookie.
func (srv *transServer) admitWS(seg string) (caller, string, error) {
	cl, err := srv.ident.hs256(seg)
	if err != nil {
		return caller{}, "", err
	}
	// An HS256 SESSION token names no space, and that is not an error here: the
	// statistics read answers it with an empty session map. serveWS, which cannot
	// open a socket onto nothing, imposes its own requirement.
	return cl, cl.space, nil
}

// wsOriginOK is the browser-Origin gate on the transactor upgrade: without it
// ANY page could open an authenticated socket with a token it holds (or lure a
// logged-in browser into one). Absent Origin is allowed — non-browser clients
// (CLI, server-side) send none.
func wsOriginOK(ctx *fasthttp.RequestCtx) bool {
	return originAllowed(string(ctx.Request.Header.Peek("Origin")), string(ctx.Host()))
}

// teamOrigins is the EXPLICIT set of pages allowed to open a team socket. It is a
// NAMED set with no suffix arm, and the missing arm is the point.
//
// This gate used to admit any *.hanzo.ai host. A WebSocket is exempt from CORS —
// a page may open one cross-origin and read every frame — so this check is the
// access control for the socket, not a hint about it. A wildcard over a registrable
// domain therefore means every first-party host is part of the team data plane's
// TCB: one marketing subdomain, one preview host, one page that renders
// user-supplied markdown, and a script there opens an authenticated space
// socket. The blast radius of a wildcard here is the whole space, so the set is
// enumerated and grows only on purpose.
var teamOrigins = map[string]bool{
	"hanzo.team": true, "team.hanzo.ai": true, "api.hanzo.team": true,
	"hanzo.ai": true, "localhost": true, "127.0.0.1": true,
}

// originAllowed admits: no Origin (a non-browser client sends none, and a browser
// cannot omit it), the request's own host, and the named team surfaces. Everything
// else is refused.
func originAllowed(origin, host string) bool {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	return teamOrigins[strings.ToLower(u.Hostname())]
}

// statsIn is the statistics read's whole input: the space token, which the
// front carries as a query param on the transactor base. It is a CREDENTIAL,
// not a tenant assertion — the space it names is the one its HS256 signature
// proves, verified below, so a caller cannot name a space it does not hold a
// token for.
type statsIn struct {
	// Token is the space token minted by selectWorkspace.
	Token string `json:"token"`
}

// statsSessions is the live-session block of the statistics body.
type statsSessions struct {
	// ActiveSessions maps a space uuid to its connected sessions. It carries
	// only the token's OWN space, and is empty for a token that names none.
	ActiveSessions map[string][]statsUser `json:"activeSessions"`
}

// statsUser is one connected session in the front's statistics shape.
type statsUser struct {
	// UserID is the account the session is authenticated as.
	UserID string `json:"userId"`
}

// statsOut is the front's statistics shape ({metrics, statistics, admin}).
//
// Metrics is an ANONYMOUS EMPTY STRUCT because that is what the wire is —
// `"metrics":{}` on every response, pinned byte-for-byte by
// TestTypedStatisticsServesBothPaths. A `map[string]any` marshals to the same
// `{}` and PROJECTS A FALSE SCHEMA: zip's schemaOf has no reflect.Interface
// case, so the element type falls to the default and the document asserted
// `additionalProperties: {"type": "object"}` — that every value here is a JSON
// object — for a map that can never hold one, which an SDK generates as a
// `Dict[str, Dict]` field carrying only `{}`. Empty-struct publishes the honest
// shape instead, and ANONYMOUS keeps it out of the fleet's flat schema
// namespace, since there is no value to name.
//
// The class is zip-side and wider than this field. MEASURED over the golden with
// this instance already removed: 15 remain, spread across owners that share
// nothing but the Go type — guide (JourneyStep.args, stepView.args), pricing
// (seven list envelopes), admin (adminCatalogOut), framework (documentList.data),
// Application.metadata, StepSettings.input and runIn.props. Count it, never tally
// it from prose: LLM.md recorded 15 BEFORE this fix, so the figure was already
// stale — the class grows every time an app is typed, because an untyped route
// contributes no schema and therefore cannot state anything false yet. The
// one-line cure is a reflect.Interface case in schemaOf projecting the OPEN
// schema `true`: an unconstrained element is open, not an object.
type statsOut struct {
	// Metrics is the upstream transactor's metrics block. This server does not
	// populate it, so it is always the empty object — the front reads the key,
	// not its contents.
	Metrics struct{} `json:"metrics"`
	// Statistics carries the live sessions.
	Statistics statsSessions `json:"statistics"`
	// Admin is the upstream service's server-panel flag, always false here.
	Admin bool `json:"admin"`
}

// Statistics returns the transactor's live sessions for the space the caller's
// credential names — the endpoint the front's space switcher and server panel
// poll on the transactor base. `token` carries the same two lanes the socket's path
// segment does: a space UUID names the space and is authorized against the
// membership rows, an HS256 space token names it in its signed claims.
// activeSessions carries ONLY that one space, never another tenant's sessions.
// An unverifiable credential, or one the caller is no member under, is 401.
//
// Example: {"token": "eyJhbGciOiJIUzI1NiJ9…"}
func (srv *transServer) statistics(ctx context.Context, in *statsIn) (*statsOut, error) {
	if srv.degraded {
		return nil, unavailable()
	}
	_, ws, err := srv.admitWS(in.Token)
	if err != nil {
		return nil, zip.ErrUnauthorized("invalid token")
	}
	active := map[string][]statsUser{}
	if ws != "" {
		active[ws] = srv.hub.users(ws)
	}
	// Metrics needs no initialiser: the zero value of an empty struct already
	// marshals to the `{}` the front reads, so there is nothing to allocate.
	return &statsOut{Statistics: statsSessions{ActiveSessions: active}}, nil
}

// session is one live transactor connection, scoped to a (space, account).
type session struct {
	server    *transServer
	store     *docStore
	hier      *hierarchy
	conn      *wsx.Conn
	wmu       sync.Mutex // serializes writes (loop replies + hub broadcasts)
	account   string
	org       string // IAM tenant (token extra.org); scopes the data path
	space     string
	sessionID string
	// derived accumulates synthetic txes the trigger projection mints (e.g. the
	// DocNotifyContext docs a channel create fans out). tx() drains + broadcasts
	// them with the applied txes so every live session's queries refresh.
	derived []json.RawMessage
}

// loop is the frame pump: every WS frame is a ZAP envelope wrapping a JSON-RPC
// message. Decode → dispatch → reply (ZAP-wrapped). msgpack never appears — hello
// negotiates binary:false so the client serializes JSON. ZAP frames are BINARY WS
// frames (the "binary:false" is the payload serialization, not the frame type).
func (s *session) loop(conn *wsx.Conn) {
	for {
		_, frame, err := conn.ReadMessage()
		if err != nil {
			return // client closed / read error
		}
		env, err := Decode(frame)
		if err != nil {
			continue // not a ZAP frame; ignore
		}
		reply := s.handle(env.Payload)
		if reply == nil {
			continue
		}
		if err := s.send(KindResponse, env.ID, reply); err != nil {
			return
		}
	}
}

// send writes one ZAP-wrapped payload, serialized against concurrent broadcasts.
func (s *session) send(kind Kind, id uint32, payload []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.WriteMessage(wsx.BinaryMessage, Encode(Envelope{ID: id, Kind: kind, Payload: payload}))
}

// request is the JSON-RPC envelope carried inside the ZAP payload.
type request struct {
	ID     int64             `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

// handle dispatches one RPC and returns the JSON reply payload (or nil).
func (s *session) handle(payload []byte) []byte {
	if string(payload) == pingFrame {
		return []byte(pongFrame)
	}
	var req request
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil
	}
	switch req.Method {
	case "hello":
		return s.hello(req.ID)
	case "loadModel":
		return s.loadModel(req.ID)
	case "getAccount":
		return s.result(req.ID, s.accountObj())
	case "findAll":
		return s.findAll(req.ID, req.Params)
	case "findOne":
		return s.findOne(req.ID, req.Params)
	case "tx":
		return s.tx(req.ID, req.Params)
	case "domainRequest":
		return s.domainRequest(req.ID, req.Params)
	case "searchFulltext":
		return s.searchFulltext(req.ID, req.Params)
	case "loadChunk":
		return s.result(req.ID, map[string]any{"idx": 0, "docs": []any{}, "finished": true})
	case "getDomainHash":
		return s.result(req.ID, "")
	case "loadDocs":
		return s.result(req.ID, []any{})
	default:
		return s.result(req.ID, nil)
	}
}

// findAll resolves the queried class to its concrete descendants, scans the
// space store, filters by query (+ mixin), sorts, limits, and returns a
// TotalArray — the wire shape the client's rpc reviver expects.
func (s *session) findAll(id int64, params []json.RawMessage) []byte {
	var class string
	if len(params) > 0 {
		_ = json.Unmarshal(params[0], &class)
	}
	var query map[string]any
	if len(params) > 1 {
		_ = json.Unmarshal(params[1], &query)
	}
	var opts struct {
		Sort   json.RawMessage `json:"sort"`
		Limit  *int            `json:"limit"`
		Lookup map[string]any  `json:"lookup"`
	}
	if len(params) > 2 {
		_ = json.Unmarshal(params[2], &opts)
	}
	matched := s.queryDocs(class, query)
	resultSort(matched, parseSort(opts.Sort))
	total := len(matched)
	if opts.Limit != nil && *opts.Limit >= 0 && *opts.Limit < total {
		matched = matched[:*opts.Limit]
	}
	matched = s.applyLookups(matched, opts.Lookup)
	return s.result(id, totalArray(matched, total))
}

func (s *session) findOne(id int64, params []json.RawMessage) []byte {
	var class string
	if len(params) > 0 {
		_ = json.Unmarshal(params[0], &class)
	}
	var query map[string]any
	if len(params) > 1 {
		_ = json.Unmarshal(params[1], &query)
	}
	matched := s.queryDocs(class, query)
	if len(matched) == 0 {
		return s.result(id, nil)
	}
	return s.result(id, matched[0])
}

// queryDocs is the shared find core: candidate scan by class hierarchy, then
// mixin + query filtering.
func (s *session) queryDocs(class string, query map[string]any) []map[string]any {
	candidates := s.hier.candidates(class)
	docs, err := s.store.byClasses(s.org, s.space, candidates)
	if err != nil {
		return nil
	}
	isMixin := s.hier.isMixin(class)
	var matched []map[string]any
	for _, doc := range docs {
		if isMixin && !hasMixin(doc, class) {
			continue
		}
		// Mixin queries match the mixin's fields as if top-level (Team `$as`
		// semantics): overlay the mixin sub-object for matching, but return the full
		// doc — the client casts it itself.
		matchDoc := doc
		if isMixin {
			matchDoc = mixinView(doc, class)
		}
		if query != nil && !matchQuery(matchDoc, query) {
			continue
		}
		matched = append(matched, doc)
	}
	return matched
}

// tx applies the transaction to the space store and broadcasts the applied
// tx(es) so every session's live queries refresh.
func (s *session) tx(id int64, params []json.RawMessage) []byte {
	if len(params) == 0 {
		return s.result(id, map[string]any{})
	}
	res, applied := s.applyTx(params[0])
	if len(s.derived) > 0 {
		applied = append(applied, s.derived...)
		s.derived = nil
	}
	if len(applied) > 0 {
		s.server.hub.broadcast(s.space, applied)
		// Fire agent replies for any bot-addressed Chunter message. Async + guarded
		// inside; only the client WS write path reaches here (the roster/sync path
		// calls applyTx directly), so a projection can never trigger a reply.
		s.server.maybeAgentReply(s.org, s.space, applied)
	}
	return s.result(id, res)
}

// domainRequest serves operation-domain reads. This is the NEWER
// @hcengineering/communication operation-domain (findMessagesMeta / findLabels /
// findNotificationContexts / findCollaborators) — a SEPARATE messaging subsystem
// with its own wire shape, NOT the classic Inbox. The workbench Inbox
// (notification-resources) reads DocNotifyContext + ActivityInboxNotification +
// CommonInboxNotification through findAll, which the SQLite docs store already
// backs (the contexts via seed.go's projectNotifyContexts, the notifications via
// notify.go's projectNotifications) — so the activity feed does NOT depend on this
// path. The communication plane has no SQLite backing; a well-formed empty ARRAY
// (never an object) keeps its live queries non-fatal (the client reads
// DomainResult.value and, for notifications, calls .pop() on it).
func (s *session) domainRequest(id int64, params []json.RawMessage) []byte {
	var domain string
	if len(params) > 0 {
		_ = json.Unmarshal(params[0], &domain)
	}
	return s.result(id, map[string]any{"domain": domain, "value": []any{}})
}

// hello answers the handshake. binary:false forces JSON so no msgpack is ever
// exchanged; lastHash/account let the client build its model + identity.
// serverVersion is the MODEL version (modelVersion, NOT a binary release) —
// the number the front's version check compares against.
func (s *session) hello(id int64) []byte {
	return mustJSON(map[string]any{
		"id":             id, // -1
		"result":         "hello",
		"binary":         false,
		"useCompression": false,
		"serverVersion":  modelVersion(),
		"lastHash":       modelHash,
		"reconnect":      false,
		"account":        s.accountObj(),
	})
}

// loadModel returns the full platform model. We always send full=true (the
// embedded model is the source of truth); the client rebuilds its hierarchy.
func (s *session) loadModel(id int64) []byte {
	return mustJSON(map[string]any{
		"id": id,
		"result": map[string]any{
			"full":         true,
			"hash":         modelHash,
			"transactions": json.RawMessage(modelJSON),
		},
	})
}

// accountObj is the core Account the client caches from hello/getAccount.
func (s *session) accountObj() map[string]any {
	return map[string]any{
		"uuid":            s.account,
		"role":            "OWNER",
		"primarySocialId": "hanzo:" + s.account,
		"socialIds":       []string{"hanzo:" + s.account},
		"fullSocialIds":   []any{},
	}
}

func (s *session) result(id int64, value any) []byte {
	return mustJSON(map[string]any{"id": id, "result": value})
}

// totalArray is the serialized FindResult shape: the rpc reviver turns
// {dataType:'TotalArray',total,value} back into an array carrying .total.
func totalArray(docs []map[string]any, total int) map[string]any {
	if docs == nil {
		docs = []map[string]any{}
	}
	return map[string]any{"dataType": "TotalArray", "total": total, "value": docs}
}

func hasMixin(doc map[string]any, mixin string) bool {
	_, ok := doc[mixin].(map[string]any)
	return ok
}

// mixinView overlays a doc's mixin sub-object onto a shallow copy so a mixin query
// can match the mixin's fields at top level (Team `$as`). The original doc is
// never mutated; the caller returns it unchanged.
func mixinView(doc map[string]any, mixin string) map[string]any {
	sub, ok := doc[mixin].(map[string]any)
	if !ok {
		return doc
	}
	merged := make(map[string]any, len(doc)+len(sub))
	maps.Copy(merged, doc)
	maps.Copy(merged, sub)
	return merged
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":{"code":"marshal"}}`)
	}
	return b
}

// ── roster reconcile (bots-as-members) ────────────────────────────────────────

// reconcileRoster is the connect-time roster projection (no live broadcast — the
// client reads the populated store on its first findAll).
func (s *session) reconcileRoster() { s.reconcile(false) }

// reconcile projects the space roster into the docs store on EVERY connect
// (idempotent, no sentinel): every human member (from the account store) AND every
// one of the org's agents (from the in-process agents lister) becomes a
// contact:class:Person + contact:mixin:Employee (+ a hanzo social identity) so they
// render in Contacts → Employees — the decompleced #19 win (bots sourced from the
// canonical agents store in-process, no IAM-SA HTTP hop, no Base collection).
//
// It ALSO runs the removal half (Red): a previously-projected BOT Employee whose
// agent id is no longer in the authoritative live set is DEACTIVATED — otherwise a
// deleted agent would linger as a stale active=true team member forever. Its Person
// survives (authorship history); humans are never touched. Removal runs ONLY when
// the bot list is authoritative (lister present AND no error), so a transient
// lister failure is never misread as "all agents gone".
//
// When broadcast is true the applied txes are fanned to the space's open
// sessions (the admin re-sync path). Returns the number of live roster entries
// (humans + live bots) processed.
//
// ISOLATION: s.org is the VERIFIED token tenant; both sources are queried
// org-scoped, so a space only ever receives its OWN org's humans and agents.
func (s *session) reconcile(broadcast bool) int {
	ctx := context.Background()
	var applied []json.RawMessage
	apply := func(txes ...map[string]any) {
		for _, t := range txes {
			raw, err := json.Marshal(t)
			if err != nil {
				continue
			}
			_, a := s.applyTx(raw)
			applied = append(applied, a...)
		}
	}

	count := 0
	// Humans — the account store's member rows for this space, org-scoped.
	if s.server.accounts != nil {
		members, err := s.server.accounts.MembersForSpaceUUID(ctx, s.org, s.space)
		if err == nil {
			for _, m := range members {
				apply(MemberTxes(Member{
					UserID: m.UserID, Name: pick(m.DisplayName, m.UserID),
					Role: m.Role, IsBot: m.IsBot, Active: !m.IsBot || m.Active,
				}, s.exists(PersonRef(m.UserID)))...)
				apply(s.remapMigratedSocialIds(m.UserID)...)
				count++
			}
		}
	}

	// Bots — the org's in-process agents. desired is the authoritative live set; we
	// only compute it (and later remove) when the lister SUCCEEDS, so a transient
	// error never nukes every bot member.
	desired := map[string]bool{}
	botsListed := false
	if s.server.bots != nil {
		bots, err := s.server.bots(ctx, s.org)
		if err == nil {
			botsListed = true
			for _, b := range bots {
				uid := botUserID(b.ID)
				if uid == "" {
					continue
				}
				desired[uid] = true
				apply(MemberTxes(Member{
					UserID: uid, Name: pick(b.Name, uid), Role: "member", IsBot: true, Active: b.Active,
				}, s.exists(PersonRef(uid)))...)
				apply(s.remapMigratedSocialIds(uid)...)
				count++
			}
		}
	}

	// Removal-reconcile: deactivate any stale bot Employee (position=="Agent") whose
	// agent id is no longer live. mixinTx flips only Employee.active (name + Person
	// untouched); already-inactive bots are skipped (idempotent-quiet).
	if botsListed {
		for _, doc := range s.queryDocs(mixinEmployee, map[string]any{"position": "Agent"}) {
			uid := str(doc["personUuid"])
			if uid == "" || desired[uid] {
				continue
			}
			if emp, ok := doc[mixinEmployee].(map[string]any); ok {
				if active, _ := emp["active"].(bool); !active {
					continue
				}
			}
			apply(mixinTx(str(doc["_id"]), clPerson, spaceContacts, mixinEmployee, acctSystem, map[string]any{"active": false}))
		}
	}

	if broadcast && len(applied) > 0 {
		s.server.hub.broadcast(s.space, applied)
	}
	return count
}

// remapMigratedSocialIds repairs the ONE invariant the Jul-5 team-go→cloud
// migration broke: a member's confirmed hanzo social identity MUST attach to the
// deterministic person-<account> (the same Person that carries the account's
// Employee mixin). The migration carried SocialIdentity rows still attached to a
// team-go-era Person id, so hanzo:<account> pointed at the WRONG person and the
// workbench refused the transactor connect ("Confirmed social identity is attached
// to the wrong person"). MemberTxes only (re)creates the social identity on FIRST
// projection (exists==false), so on a migrated space where person-<account>
// already exists it never corrects a mis-attached row — this does.
//
// It re-points every SocialIdentity keyed hanzo:<uid> (the id the workbench
// resolves the account by) whose attachedTo is not person-<uid> back onto
// person-<uid>, reconstituting exactly the shape a FRESH space has from the
// start. Properties, by construction:
//   - migrated-only: a fresh (or already-remapped) row is attachedTo==pid, so it is
//     skipped — fresh spaces are never touched.
//   - idempotent: after one pass every keyed row is canonical, so every later
//     connect is a no-op.
//   - non-destructive: it only UPDATES attachedTo; no row is deleted, so the
//     migrated person doc and all space data survive.
//
// Returns the re-pointed txes so the caller folds them into the reconcile apply/
// broadcast path (admin re-sync fans them to open sessions).
func (s *session) remapMigratedSocialIds(uid string) []map[string]any {
	if uid == "" {
		return nil
	}
	pid := PersonRef(uid)
	socialKey := "hanzo:" + uid

	// Candidates: any SocialIdentity carrying key==hanzo:<uid>, plus the canonical
	// _id==hanzo:<uid> row directly (belt-and-suspenders, in case a migrated row lost
	// its key field). De-duped by _id.
	candidates := s.queryDocs(clSocialIdentity, map[string]any{"key": socialKey})
	if d, _ := s.store.get(s.org, s.space, socialKey); d != nil && str(d["_class"]) == clSocialIdentity {
		candidates = append(candidates, d)
	}

	seen := map[string]bool{}
	var txes []map[string]any
	for _, sid := range candidates {
		id := str(sid["_id"])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if str(sid["attachedTo"]) == pid {
			continue // already canonical — fresh row or a prior remap
		}
		txes = append(txes, updateTx(id, clSocialIdentity, spaceContacts, acctSystem, map[string]any{
			"attachedTo":      pid,
			"attachedToClass": clPerson,
			"collection":      "socialIds",
		}))
	}
	return txes
}

// exists reports whether a doc id is already in this session's space store.
func (s *session) exists(id string) bool {
	d, _ := s.store.get(s.org, s.space, id)
	return d != nil
}

// ── in-process projection bridge (Apply / ingest) ─────────────────────────────

// Apply ingests platform CUD txes into a space's store exactly as a live
// client would (same applyTx path, same triggers) and broadcasts the applied
// txes to every open session of that space (realtime). account is the
// attribution used for triggers/PersonSpace ownership. No-op until Mount runs.
func Apply(org, space, account string, txes ...map[string]any) {
	if live == nil || org == "" || space == "" || len(txes) == 0 {
		return
	}
	live.ingest(org, space, account, txes...)
}

func (srv *transServer) ingest(org, space, account string, txes ...map[string]any) {
	s := &session{server: srv, store: srv.store, hier: srv.hier, org: org, space: space, account: account}
	s.seedSpace() // system spaces must exist so space-scoped queries resolve
	var applied []json.RawMessage
	for _, t := range txes {
		raw, err := json.Marshal(t)
		if err != nil {
			continue
		}
		_, a := s.applyTx(raw)
		applied = append(applied, a...)
	}
	if len(applied) > 0 {
		srv.hub.broadcast(space, applied)
	}
}

// ── live broadcast hub ────────────────────────────────────────────────────────

// hub fans applied txes to every open session of a space so live queries
// refresh in real time (and across a user's tabs).
type hub struct {
	mu sync.Mutex
	ws map[string]map[*session]bool
}

func newHub() *hub { return &hub{ws: map[string]map[*session]bool{}} }

func (h *hub) add(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.ws[s.space]
	if set == nil {
		set = map[*session]bool{}
		h.ws[s.space] = set
	}
	set[s] = true
}

func (h *hub) remove(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.ws[s.space]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.ws, s.space)
		}
	}
}

// users lists a space's live sessions in the front's statistics shape.
func (h *hub) users(space string) []statsUser {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []statsUser{}
	for s := range h.ws[space] {
		out = append(out, statsUser{UserID: s.account})
	}
	return out
}

func (h *hub) broadcast(space string, txes []json.RawMessage) {
	h.mu.Lock()
	var targets []*session
	for s := range h.ws[space] {
		targets = append(targets, s)
	}
	h.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	// A no-id {result:[tx...]} message is the client's tx-broadcast shape.
	parts := make([]string, len(txes))
	for i, tx := range txes {
		parts[i] = string(tx)
	}
	payload := []byte(`{"result":[` + strings.Join(parts, ",") + `]}`)
	for _, s := range targets {
		_ = s.send(KindPush, 0, payload)
	}
}
