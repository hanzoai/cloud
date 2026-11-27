package team

// chat.go is the Chunter agent-responder: it makes the org's agents (already
// projected as space members by the roster reconcile) TALKABLE. When a human
// posts a Chunter ChatMessage addressed to a bot member — a direct message whose
// participants include the bot, or a channel message that @-mentions it — the
// transactor runs that agent through the canonical in-process run path
// (agents.RunOnBehalf, one balance gate + one debit + one recorded run) and posts
// the model's answer back into the SAME conversation as that bot.
//
// WHO PAYS, AND WHO DECIDES. A turn runs through agents.RunOnBehalf, which gates
// on the ORG's balance and debits the org — so the cost is the org's, and so is
// the decision: an agent answers only in a conversation it is a MEMBER of (a DM
// with it, or a channel or teamspace somebody added it to), and only while the org
// keeps it active. Which agents answer where is the org's rows, not a
// deployment variable; there is no switch in the environment.
//
// SAFETY POSTURE (why every path here is bounded and lazy). The responder is the
// ONE place in the team subsystem that can, per inbound message, initiate an
// OUTBOUND model call — and that call self-routes through the cloud gateway
// (api.hanzo.ai/v1). An unbounded version is a foot-gun: a replayed message
// backlog, a channel firehose, or a persistently-failing model (e.g. the
// publishable-key 403) could each fan out into thousands of concurrent HTTP calls
// and exhaust the writer. So the responder is:
//   - Fresh-only — a message created before this process booted (a replay/backfill)
//     is NEVER answered; only genuinely-live posts trigger a turn.
//   - Single-flight per conversation+bot — at most one in-flight answer per
//     (space, space, bot); duplicates are dropped, not queued.
//   - Hard-capped — a global semaphore bounds concurrent turns; over the cap, drop.
//   - Circuit-broken — after repeated failures an agent is skipped for a cooldown
//     (the backoff that turns a 403 storm into a quiet trickle). No retries, ever.
//
// A nil runAgent is inert (maybeAgentReply returns at the top): that is how a
// test, or the projection path, holds a transServer that never answers.
//
// It is the chat twin of bots.go: bots.go is the READ/projection surface (agents
// AS members); this is the WRITE/response surface (agents that ANSWER). The client to
// the LLM is a single injected func (AgentRunner) so the transactor stays decoupled
// from clients/agents' concrete run machinery and the loop is unit-testable with a
// fake runner.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/text"
)

// Chunter class ids the responder recognizes. Kept beside the code that reads them
// (the model-vocabulary-in-one-place rule projections.go follows).
const (
	clChatMessage   = "chunter:class:ChatMessage"
	clDirectMessage = "chunter:class:DirectMessage"
)

// Responder bounds. Conservative defaults; the process ceiling is overridable
// in Mount via TEAM_AGENTS_MAX_CONCURRENCY. The per-org and per-caller caps are
// what keep one org — or one person — from taking every turn this process has.
const (
	agentReplyTimeout    = 90 * time.Second // one agent turn (model call + reply write)
	defaultMaxConcurrent = 32               // process-wide in-flight ceiling default
	orgMaxConcurrent     = 4                // in-flight turns one org may hold
	callerMaxConcurrent  = 2                // in-flight turns one person may have asked for
	backfillGraceMs      = 60_000           // a message created >60s before boot is backfill → never answered
	breakerThreshold     = 3                // consecutive failures before an agent's circuit opens
	breakerCooldown      = 60 * time.Second // how long a tripped agent is skipped
	historyMax           = 30               // earlier messages of the conversation one turn is shown
	historyTextMax       = 2000             // characters of any one of them
)

// AgentRunner runs agent `agentID` for `org` on behalf of `userSub` with `input`
// — `history` being the conversation it was said in, oldest first and without
// `input` — and returns the model's text output. It is injected in Mount as an adapter over agents.RunOnBehalf — the ONE in-process
// run path (billed, metered, recorded) — so clients/team never speaks
// clients/agents' concrete run types and the responder is testable with a fake.
type AgentRunner func(ctx context.Context, org, userSub, agentID, input string, history []client.Turn) (string, error)

// turns bounds the agent turns in flight three ways at once: across the
// process, per org, and per person asking. Over any cap a turn is DROPPED, never
// queued. A nil *turns is uncapped (a bare transServer in a test).
type turns struct {
	mu                            sync.Mutex
	total                         int
	perOrg, perCaller             map[string]int
	maxTotal, maxOrg, maxPerAsker int
}

func newTurns(total, org, caller int) *turns {
	return &turns{perOrg: map[string]int{}, perCaller: map[string]int{},
		maxTotal: total, maxOrg: org, maxPerAsker: caller}
}

// take claims one turn for (org, caller), reporting false when any cap is full.
func (l *turns) take(org, caller string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	who := org + "|" + caller
	if l.total >= l.maxTotal || l.perOrg[org] >= l.maxOrg || l.perCaller[who] >= l.maxPerAsker {
		return false
	}
	l.total++
	l.perOrg[org]++
	l.perCaller[who]++
	return true
}

// give returns a turn take granted.
func (l *turns) give(org, caller string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	who := org + "|" + caller
	l.total--
	if l.perOrg[org]--; l.perOrg[org] <= 0 {
		delete(l.perOrg, org)
	}
	if l.perCaller[who]--; l.perCaller[who] <= 0 {
		delete(l.perCaller, who)
	}
}

// agentBreaker is one agent's failure circuit. Guarded by its own mutex; stored in
// transServer.breaker keyed by agent id.
type agentBreaker struct {
	mu        sync.Mutex
	fails     int
	openUntil int64 // unix millis; while now < openUntil the agent is skipped
}

// chatMsg is the parsed shape of one inbound Chunter ChatMessage create the
// responder needs: where it lives (space + the attach coordinates a reply mirrors),
// its author, its text, and WHEN it was created (the freshness gate).
type chatMsg struct {
	objectID        string
	class           string // ChatMessage, or ThreadMessage for a reply in a thread
	space           string
	attachedTo      string
	attachedToClass string
	collection      string
	// thread is the conversation a REPLY lives in (its objectId / objectClass):
	// the channel or document the thread's first message was said in. Empty for a
	// message that is not a reply.
	thread      string
	threadClass string
	message     string // stored markup (HTML)
	authorUID   string // account uuid (social id stripped of the "hanzo:" prefix)
	createdOn   int64  // unix millis; a value below the boot floor is backfill
}

// parseChatMessage returns the chatMsg for an applied tx iff it is a create of a
// chunter:class:ChatMessage or of a reply in a thread (ThreadMessage). Every
// other applied tx (roster txes, todo/docs
// writes, updates, removes) returns ok=false and is ignored. It reads the FLATTENED
// applied tx, so a create wrapped in TxCollectionCUD (which applyTx unwraps, setting
// attachedTo/attachedToClass/collection on the inner tx) is recognized identically
// to a bare TxCreateDoc.
func parseChatMessage(raw json.RawMessage) (chatMsg, bool) {
	var t map[string]any
	if err := json.Unmarshal(raw, &t); err != nil {
		return chatMsg{}, false
	}
	if str(t["_class"]) != clTxCreate {
		return chatMsg{}, false
	}
	if cls := str(t["objectClass"]); cls != clChatMessage && cls != clThreadMessage {
		return chatMsg{}, false
	}
	m := chatMsg{
		objectID:        str(t["objectId"]),
		class:           str(t["objectClass"]),
		space:           str(t["objectSpace"]),
		attachedTo:      str(t["attachedTo"]),
		attachedToClass: str(t["attachedToClass"]),
		collection:      str(t["collection"]),
		authorUID:       stripHanzo(str(firstNonNil(t["createdBy"], t["modifiedBy"]))),
		createdOn:       asInt64(firstNonNil(t["createdOn"], t["modifiedOn"])),
	}
	if a, ok := t["attributes"].(map[string]any); ok {
		m.message = str(a["message"])
		if m.class == clThreadMessage {
			m.thread, m.threadClass = str(a["objectId"]), str(a["objectClass"])
		}
	}
	// A top-level channel/DM message carries both (space == attachedTo == the
	// conversation id). Without them there is no conversation to answer into.
	if m.space == "" || m.attachedTo == "" {
		return chatMsg{}, false
	}
	return m, true
}

// maybeAgentReply is the transactor hook (called from session.tx, the client WS
// write path — NEVER from the roster/sync applyTx path, so a projection can never
// trigger a reply). For every inbound, FRESH, human ChatMessage addressed to an
// active bot member it fires one async, bounded agent turn per bot. It returns
// immediately; the model call and the reply write happen in a recovered, capped
// goroutine so the WS read loop is never blocked and a model/DB error can never
// crash the session.
func (srv *transServer) maybeAgentReply(org, space string, applied []json.RawMessage) {
	if srv.runAgent == nil || srv.bots == nil {
		return // no runner wired: a transServer that never answers
	}
	// Cheap gate first: only touch the agents registry if a FRESH chat message was
	// written. A message created before this process booted is a replay/backfill and
	// is never answered (the anti-storm invariant the boot-backlog test locks).
	floor := srv.startedAt - backfillGraceMs
	var msgs []chatMsg
	for _, raw := range applied {
		m, ok := parseChatMessage(raw)
		if !ok {
			continue
		}
		if srv.startedAt > 0 && m.createdOn > 0 && m.createdOn < floor {
			continue // backfill — never answer
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		return
	}
	bots, err := srv.bots(context.Background(), org)
	if err != nil {
		// SAY SO. This was `if err != nil || len(bots) == 0 { return }`, which read
		// as prudence and was the reason a broken responder was invisible: the
		// roster read failed on every message in every deployment (a per-process
		// global, now the plane), and a discarded error left the boot line "Chunter
		// agent responder ENABLED" as the only thing anyone could see. An enabled
		// responder that cannot list the org's agents is a FAULT, and a fault that
		// is never reported is one nobody fixes.
		if srv.log != nil {
			srv.log.Warn("team: agent roster unavailable, no mention can be answered", "org", org, "err", err)
		}
		return
	}
	if len(bots) == 0 {
		return // this org genuinely has no agents — not a fault, and not worth a line
	}
	byUID := make(map[string]Bot, len(bots))
	for _, b := range bots {
		if b.Active {
			byUID[botUserID(b.ID)] = b
		}
	}
	if len(byUID) == 0 {
		return
	}
	for _, m := range msgs {
		// Loop guard: a message a bot authored (its own reply, or another bot's)
		// never triggers a reply.
		if _, isBot := byUID[m.authorUID]; isBot {
			continue
		}
		targets := srv.replyTargets(org, space, m, byUID)
		if len(targets) == 0 {
			continue
		}
		// The turn is spent on the org's balance on behalf of whoever asked, so
		// the asker must be a MEMBER of the space, and not a guest: a guest's
		// reach is what names them, never the org's wallet.
		if !srv.mayAsk(org, space, m.authorUID) {
			continue
		}
		for _, bot := range targets {
			go srv.replyAsBot(org, space, m, bot)
		}
	}
}

// mayAsk reports whether an account may spend an agent turn in this space: it
// holds a non-guest member row there.
func (srv *transServer) mayAsk(org, space, account string) bool {
	if srv.accounts == nil {
		return false
	}
	role, ok := srv.accounts.Membership(context.Background(), org, space, account)
	return ok && role != roleGuest
}

// replyTargets is the addressing policy: which bots should answer message m.
//   - Direct message: every active bot participant answers (a DM to a bot is, by
//     construction, addressed to it).
//   - A PUBLIC channel (or a public teamspace's document, or a thread in
//     either): a bot answers an @-mention. Every active agent of the org is a
//     member of the space, and a public conversation is the whole space's.
//   - A PRIVATE channel or teamspace: a bot answers an @-mention only when it is in
//     that conversation's members — somebody added it. A private conversation is
//     its members', and an agent outside it would read what it was not given.
//
// That is openTo, the one visibility rule, asked of the bot as a non-guest
// member. A bot is never double-targeted (DM + mention) — the seen set
// collapses them.
func (srv *transServer) replyTargets(org, space string, m chatMsg, byUID map[string]Bot) []Bot {
	seen := map[string]bool{}
	var out []Bot
	where := srv.conversation(org, space, m)
	if where != nil && str(where["_class"]) == clDirectMessage {
		for _, mem := range toStringSlice(where["members"]) {
			if b, ok := byUID[mem]; ok && !seen[mem] {
				seen[mem] = true
				out = append(out, b)
			}
		}
	}
	for uid, b := range byUID {
		if !seen[uid] && mentionsBot(m.message, uid) && openTo(roleMember, uid, where) {
			seen[uid] = true
			out = append(out, b)
		}
	}
	return out
}

// conversation is the document whose members decide who m may address: the
// channel it was said in (a reply's thread's channel), or — for a comment on a
// document — that document's teamspace. Nil when it cannot be found.
func (srv *transServer) conversation(org, space string, m chatMsg) map[string]any {
	id, cls := m.attachedTo, m.attachedToClass
	if m.class == clThreadMessage {
		id, cls = m.thread, m.threadClass
	}
	doc, _ := srv.store.get(org, space, id)
	if doc == nil {
		return nil
	}
	if cls == clDocument || str(doc["_class"]) == clDocument {
		ts, _ := srv.store.get(org, space, str(doc["space"]))
		return ts
	}
	return doc
}

// replyAsBot runs one bounded agent turn and posts its answer as the bot, in the
// SAME conversation (space + attach coordinates mirrored from the inbound message)
// and through the SAME write path (applyTx + broadcast) the SPA uses. It is
// single-flight per (space, space, bot), concurrency-capped per process, org and asker, and
// circuit-broken per agent; recovered + timeout-bounded so a panicking model
// adapter or a hung call can neither crash the transactor nor leak.
func (srv *transServer) replyAsBot(org, space string, m chatMsg, bot Bot) {
	defer func() {
		if r := recover(); r != nil && srv.log != nil {
			srv.log.Error("team: agent reply panicked", "agent", bot.ID, "err", r)
		}
	}()

	// Single-flight: at most one in-flight answer per conversation+bot. A burst of
	// messages to the same DM while a turn is running collapses to one.
	flightKey := space + "|" + m.space + "|" + bot.ID
	if _, busy := srv.inflight.LoadOrStore(flightKey, struct{}{}); busy {
		return
	}
	defer srv.inflight.Delete(flightKey)

	// Circuit breaker: skip a persistently-failing agent for a cooldown (the backoff
	// that turns a 403/5xx storm into a quiet trickle).
	if srv.breakerOpen(bot.ID) {
		return
	}

	// Hard concurrency caps — process, org, asker. Over any of them we DROP
	// (bounded, no queue) rather than pile up goroutines and model calls.
	if !srv.turns.take(org, m.authorUID) {
		return
	}
	defer srv.turns.give(org, m.authorUID)

	prompt := plainText(m.message)
	if prompt == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentReplyTimeout)
	defer cancel()
	out, err := srv.runAgent(ctx, org, m.authorUID, bot.ID, prompt, srv.earlier(org, space, m, bot))
	if err != nil {
		srv.breakerRecord(bot.ID, false)
		if srv.log != nil {
			srv.log.Warn("team: agent reply failed", "agent", bot.ID, "err", err)
		}
		return
	}
	srv.breakerRecord(bot.ID, true)
	if strings.TrimSpace(out) == "" {
		return
	}
	botUID := botUserID(bot.ID)
	// The answer goes where the question was: the same channel, the same thread,
	// the same document's comments — mirrored from the inbound message's own
	// coordinates, so a reply in a thread is answered in that thread.
	attrs := map[string]any{"message": answerMarkup(out)}
	class := clChatMessage
	if m.class == clThreadMessage {
		class = clThreadMessage
		attrs["objectId"], attrs["objectClass"] = m.thread, m.threadClass
	}
	tx := attachedCreateTx(newMsgID(), class, m.space, m.attachedTo, m.attachedToClass,
		pick(m.collection, "messages"), "hanzo:"+botUID, attrs)
	// A detached session bound to (org, space) and attributed to the bot,
	// through the one write path — so its answer is broadcast with the counters
	// and inbox rows it produces, exactly like a person's.
	sess := &session{server: srv, store: srv.store, hier: srv.hier, org: org, space: space, account: botUID}
	_, _ = apply(sess, srv, space, tx)
}

// earlier is the conversation m was said in, oldest first and without m: the
// messages before it in the same channel, DM or document, or — for a reply in a
// thread — the message that opened the thread and the replies before it.
//
// A bot was shown only the one message that addressed it, so in a thread it
// could not see what "this" was, and in a DM every message was its first. The
// agents side has taken a conversation since the Slack bridge needed one
// (client.RunOnBehalfIn.History); this is the team bridge sending its own.
//
// The bot's own messages come back as Self, so the model reads them as its
// answers rather than as things it was told. Outside a DM a turn starts with who
// said it, because a channel is several people and the model otherwise reads
// them as one. A failed read is not a failed turn: it answers with less context.
func (srv *transServer) earlier(org, space string, m chatMsg, bot Bot) []client.Turn {
	class := clChatMessage
	if m.class == clThreadMessage {
		class = clThreadMessage
	}
	docs, err := srv.store.byClasses(org, space, []string{class})
	if err != nil {
		if srv.log != nil {
			srv.log.Warn("team: conversation unavailable, the agent answers without it", "org", org, "err", err)
		}
		return nil
	}
	var said []map[string]any
	for _, d := range docs {
		if str(d["attachedTo"]) != m.attachedTo || str(d["_id"]) == m.objectID {
			continue
		}
		// Something said AFTER the question is not part of what it asked about.
		if m.createdOn > 0 && whenSaid(d) > m.createdOn {
			continue
		}
		said = append(said, d)
	}
	if m.class == clThreadMessage {
		if root, _ := srv.store.get(org, space, m.attachedTo); root != nil {
			said = append(said, root)
		}
	}
	if len(said) == 0 {
		return nil
	}
	sort.Slice(said, func(i, j int) bool {
		a, b := whenSaid(said[i]), whenSaid(said[j])
		if a != b {
			return a < b
		}
		return str(said[i]["_id"]) < str(said[j]["_id"])
	})
	if len(said) > historyMax {
		said = said[len(said)-historyMax:]
	}

	where := m.attachedToClass
	if m.class == clThreadMessage {
		where = m.threadClass
	}
	var names map[string]string
	if where != clDirectMessage {
		names = srv.speakers(org, space)
	}
	self := botUserID(bot.ID)
	turns := make([]client.Turn, 0, len(said))
	for _, d := range said {
		words := text.Trim(plainText(str(d["message"])), historyTextMax)
		if words == "" {
			continue
		}
		who := authorOf(d)
		t := client.Turn{Sender: who, Self: who == self, Text: words, At: whenSaid(d)}
		if name := names[who]; name != "" && !t.Self {
			t.Text = name + ": " + words
		}
		turns = append(turns, t)
	}
	return turns
}

// speakers is the space's roster as account → the name it is spoken by, read
// from the Person documents the space already holds (people and bots alike).
func (srv *transServer) speakers(org, space string) map[string]string {
	out := map[string]string{}
	docs, err := srv.store.byClasses(org, space, []string{clPerson})
	if err != nil {
		return out
	}
	for _, d := range docs {
		if uid, name := str(d["personUuid"]), spokenName(str(d["name"])); uid != "" && name != "" {
			out[uid] = name
		}
	}
	return out
}

// spokenName renders the stored "last,first" (personName) as "first last".
func spokenName(stored string) string {
	last, first, ok := strings.Cut(stored, ",")
	if !ok {
		return strings.TrimSpace(stored)
	}
	return strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
}

// whenSaid is a stored message's creation stamp (unix millis).
func whenSaid(d map[string]any) int64 {
	return asInt64(firstNonNil(d["createdOn"], d["modifiedOn"]))
}

// breakerOpen reports whether agent `id`'s circuit is currently tripped.
func (srv *transServer) breakerOpen(id string) bool {
	v, ok := srv.breaker.Load(id)
	if !ok {
		return false
	}
	b := v.(*agentBreaker)
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().UnixMilli() < b.openUntil
}

// breakerRecord folds one turn's outcome into agent `id`'s circuit: a success
// resets it; breakerThreshold consecutive failures trip it for breakerCooldown.
func (srv *transServer) breakerRecord(id string, ok bool) {
	v, _ := srv.breaker.LoadOrStore(id, &agentBreaker{})
	b := v.(*agentBreaker)
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.fails = 0
		b.openUntil = 0
		return
	}
	b.fails++
	if b.fails >= breakerThreshold {
		b.openUntil = time.Now().UnixMilli() + breakerCooldown.Milliseconds()
		b.fails = 0 // count fresh after the cooldown
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// stripHanzo drops the "hanzo:" social-id prefix, yielding the bare account uuid
// (the key the bot map and DM.members use). A value without the prefix is returned
// unchanged.
func stripHanzo(id string) string { return strings.TrimPrefix(id, "hanzo:") }

// toStringSlice coerces a JSON array (from a stored doc) to []string, dropping
// non-string entries. Nil-safe.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// asInt64 coerces a JSON number (float64) or int to int64. Non-numeric ⇒ 0.
func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// newMsgID mints a fresh opaque doc id for a reply message. Chunter treats _id as an
// opaque Ref; 16 random bytes hex-encoded is collision-free in practice and a valid
// single path/ref segment.
func newMsgID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read never fails on supported platforms; fall back to a time seed so a
		// reply id is still unique-enough rather than empty.
		return "msg" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
