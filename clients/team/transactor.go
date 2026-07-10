package team

// This file is the workspace data plane the SPA connects to after
// selectWorkspace — the transactor. The RPC dispatch + query semantics are ported
// VERBATIM from github.com/hanzoai/team-go/pkg/transactor/transactor.go; the
// TRANSPORT is rewritten from golang.org/x/net/websocket + core.RequestEvent to
// zip's wsx (fasthttp/websocket) at app.Get("/v1/team/transactor/:token", ...).
//
// The wire is preserved exactly: every frame is a ZAP Envelope wrapping one
// JSON-RPC message; hello negotiates binary:false (JSON, never msgpack);
// serverVersion is the Huly MODEL version (modelVersion(), pinned 0.6.0); findAll
// returns the TotalArray shape; ping→pong!; tx broadcasts through the hub.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/clients/team/token"
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
// the seam that keeps clients/team decoupled from clients/agents' concrete Agent
// type — team names only what it projects (id, name, active).
type Bot struct {
	ID     string
	Name   string
	Active bool
}

// BotLister sources an org's bots (the canonical in-process agents). It is
// injected in Mount as an adapter over agents.ListForOrg — the ONE in-process
// seam, replacing the removed IAM-SA HTTP enumeration. org is always a VERIFIED
// tenant (the transactor token's extra.org), never a client header.
type BotLister func(ctx context.Context, org string) ([]Bot, error)

// transServer holds the transactor's shared, process-lifetime state: the per-
// workspace SQLite docs store (the structured data plane — no KV, no Postgres),
// the class hierarchy parsed from the embedded model, the live-broadcast hub, the
// shared HS256 secret, and the two roster sources (the account store's members +
// the in-process agents lister).
type transServer struct {
	store    *docStore
	hier     *hierarchy
	hub      *hub
	secret   string
	accounts *accountStore // human members (this deployment's workspaces)
	bots     BotLister     // bot members (the org's in-process agents)
}

// live is the process-singleton transactor server, published in Mount so the
// in-process projection path (Apply / ingest) and the /v1/team/bots/sync handler
// can write into the per-workspace store the SPA reads WITHOUT holding a client
// WebSocket. One server, one store.
var live *transServer

// serveWS decodes + AUTHORIZES the workspace token BEFORE the WebSocket upgrade
// (fail-secure: a bad token is a 401, never an upgraded-then-dropped socket),
// then upgrades and runs the frame loop. The org is the token's VERIFIED extra.org
// claim — the tenant key for every store path — never a client header.
func (srv *transServer) serveWS(c *zip.Ctx) error {
	raw := c.Param("token")
	t, err := token.Decode(raw, srv.secret, true)
	if err != nil || t.Account == "" || t.Workspace == "" {
		return zip.ErrUnauthorized("invalid workspace token")
	}
	org, _ := t.Extra["org"].(string)
	sess := &session{
		server:    srv,
		store:     srv.store,
		hier:      srv.hier,
		account:   t.Account,
		org:       org,
		workspace: t.Workspace,
		sessionID: c.Query("sessionId"),
	}
	return wsx.Upgrade(func(conn *wsx.Conn) error {
		sess.conn = conn
		sess.seedWorkspace()   // system spaces (once per workspace)
		sess.reconcileRoster() // humans + bots as Person/Employee (every connect)
		srv.hub.add(sess)
		defer srv.hub.remove(sess)
		sess.loop(conn)
		return nil
	})(c)
}

// session is one live transactor connection, scoped to a (workspace, account).
type session struct {
	server    *transServer
	store     *docStore
	hier      *hierarchy
	conn      *wsx.Conn
	wmu       sync.Mutex // serializes writes (loop replies + hub broadcasts)
	account   string
	org       string // IAM tenant (token extra.org); scopes the data path
	workspace string
	sessionID string
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
		// Full-text is served by hanzoai/search, not the SQLite data plane; until
		// that proxy lands an empty result keeps queries non-fatal.
		return s.result(req.ID, map[string]any{"docs": []any{}, "total": 0})
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
// workspace store, filters by query (+ mixin), sorts, limits, and returns a
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
	docs, err := s.store.byClasses(s.org, s.workspace, candidates)
	if err != nil {
		return nil
	}
	isMixin := s.hier.isMixin(class)
	var matched []map[string]any
	for _, doc := range docs {
		if isMixin && !hasMixin(doc, class) {
			continue
		}
		// Mixin queries match the mixin's fields as if top-level (Huly `$as`
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

// tx applies the transaction to the workspace store and broadcasts the applied
// tx(es) so every session's live queries refresh.
func (s *session) tx(id int64, params []json.RawMessage) []byte {
	if len(params) == 0 {
		return s.result(id, map[string]any{})
	}
	res, applied := s.applyTx(params[0])
	if len(applied) > 0 {
		s.server.hub.broadcast(s.workspace, applied)
	}
	return s.result(id, res)
}

// domainRequest serves operation-domain reads. The communication domain (labels,
// notifications, collaborators…) has no SQLite backing yet; returning a
// well-formed empty DomainResult keeps those live queries from crashing on
// `.value` of null.
func (s *session) domainRequest(id int64, params []json.RawMessage) []byte {
	var domain string
	if len(params) > 0 {
		_ = json.Unmarshal(params[0], &domain)
	}
	// The client reads DomainResult.value and, for notifications, calls .pop() on
	// it — so every communication read returns an empty ARRAY (never an object),
	// which keeps labels/notifications/contexts queries non-fatal until the
	// communication plane has its own store.
	return s.result(id, map[string]any{"domain": domain, "value": []any{}})
}

// hello answers the handshake. binary:false forces JSON so no msgpack is ever
// exchanged; lastHash/account let the client build its model + identity.
// serverVersion is the Huly MODEL version (modelVersion, NOT a binary release) —
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
// can match the mixin's fields at top level (Huly `$as`). The original doc is
// never mutated; the caller returns it unchanged.
func mixinView(doc map[string]any, mixin string) map[string]any {
	sub, ok := doc[mixin].(map[string]any)
	if !ok {
		return doc
	}
	merged := make(map[string]any, len(doc)+len(sub))
	for k, v := range doc {
		merged[k] = v
	}
	for k, v := range sub {
		merged[k] = v
	}
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

// reconcile projects the workspace roster into the docs store on EVERY connect
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
// When broadcast is true the applied txes are fanned to the workspace's open
// sessions (the admin re-sync path). Returns the number of live roster entries
// (humans + live bots) processed.
//
// ISOLATION: s.org is the VERIFIED token tenant; both sources are queried
// org-scoped, so a workspace only ever receives its OWN org's humans and agents.
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
	// Humans — the account store's member rows for this workspace, org-scoped.
	if s.server.accounts != nil {
		members, err := s.server.accounts.MembersForWorkspaceUUID(ctx, s.org, s.workspace)
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
		s.server.hub.broadcast(s.workspace, applied)
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
// projection (exists==false), so on a migrated workspace where person-<account>
// already exists it never corrects a mis-attached row — this does.
//
// It re-points every SocialIdentity keyed hanzo:<uid> (the id the workbench
// resolves the account by) whose attachedTo is not person-<uid> back onto
// person-<uid>, reconstituting exactly the shape a FRESH workspace has from the
// start. Properties, by construction:
//   - migrated-only: a fresh (or already-remapped) row is attachedTo==pid, so it is
//     skipped — fresh workspaces are never touched.
//   - idempotent: after one pass every keyed row is canonical, so every later
//     connect is a no-op.
//   - non-destructive: it only UPDATES attachedTo; no row is deleted, so the
//     migrated person doc and all workspace data survive.
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
	if d, _ := s.store.get(s.org, s.workspace, socialKey); d != nil && str(d["_class"]) == clSocialIdentity {
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

// exists reports whether a doc id is already in this session's workspace store.
func (s *session) exists(id string) bool {
	d, _ := s.store.get(s.org, s.workspace, id)
	return d != nil
}

// ── in-process projection bridge (Apply / ingest) ─────────────────────────────

// Apply ingests platform CUD txes into a workspace's store exactly as a live
// client would (same applyTx path, same triggers) and broadcasts the applied
// txes to every open session of that workspace (realtime). account is the
// attribution used for triggers/PersonSpace ownership. No-op until Mount runs.
func Apply(org, workspace, account string, txes ...map[string]any) {
	if live == nil || org == "" || workspace == "" || len(txes) == 0 {
		return
	}
	live.ingest(org, workspace, account, txes...)
}

func (srv *transServer) ingest(org, workspace, account string, txes ...map[string]any) {
	s := &session{server: srv, store: srv.store, hier: srv.hier, org: org, workspace: workspace, account: account}
	s.seedWorkspace() // system spaces must exist so space-scoped queries resolve
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
		srv.hub.broadcast(workspace, applied)
	}
}

// ── live broadcast hub ────────────────────────────────────────────────────────

// hub fans applied txes to every open session of a workspace so live queries
// refresh in real time (and across a user's tabs).
type hub struct {
	mu sync.Mutex
	ws map[string]map[*session]bool
}

func newHub() *hub { return &hub{ws: map[string]map[*session]bool{}} }

func (h *hub) add(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.ws[s.workspace]
	if set == nil {
		set = map[*session]bool{}
		h.ws[s.workspace] = set
	}
	set[s] = true
}

func (h *hub) remove(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.ws[s.workspace]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.ws, s.workspace)
		}
	}
}

func (h *hub) broadcast(workspace string, txes []json.RawMessage) {
	h.mu.Lock()
	var targets []*session
	for s := range h.ws[workspace] {
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
