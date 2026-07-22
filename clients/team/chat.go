package team

// chat.go is the Chunter agent-responder: it makes the org's agents (already
// projected as workspace members by the roster reconcile) TALKABLE. When a human
// posts a Chunter ChatMessage addressed to a bot member — a direct message whose
// participants include the bot, or a channel message that @-mentions it — the
// transactor runs that agent through the canonical in-process run path
// (agents.RunOnBehalf, one balance gate + one debit + one recorded run) and posts
// the model's answer back into the SAME conversation as that bot.
//
// SAFETY POSTURE (why every path here is bounded and lazy). The responder is the
// ONE place in the team subsystem that can, per inbound message, initiate an
// OUTBOUND model call — and that call self-routes through the cloud gateway
// (api.hanzo.ai/v1). An unbounded version is a foot-gun: a replayed message
// backlog, a channel firehose, or a persistently-failing model (e.g. the
// publishable-key 403) could each fan out into thousands of concurrent HTTP calls
// and exhaust the writer. So the responder is:
//   - OFF by default — Mount only wires runAgent when TEAM_AGENTS_ENABLED=1, so an
//     un-configured or misconfigured binary NEVER answers (nil runAgent ⇒ inert).
//   - Fresh-only — a message created before this process booted (a replay/backfill)
//     is NEVER answered; only genuinely-live posts trigger a turn.
//   - Single-flight per conversation+bot — at most one in-flight answer per
//     (workspace, space, bot); duplicates are dropped, not queued.
//   - Hard-capped — a global semaphore bounds concurrent turns; over the cap, drop.
//   - Circuit-broken — after repeated failures an agent is skipped for a cooldown
//     (the backoff that turns a 403 storm into a quiet trickle). No retries, ever.
//
// It is the chat twin of bots.go: bots.go is the READ/projection surface (agents
// AS members); this is the WRITE/response surface (agents that ANSWER). The seam to
// the LLM is a single injected func (AgentRunner) so the transactor stays decoupled
// from clients/agents' concrete run machinery and the loop is unit-testable with a
// fake runner.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Chunter class ids the responder recognizes. Kept beside the code that reads them
// (the model-vocabulary-in-one-place rule projections.go follows).
const (
	clChatMessage   = "chunter:class:ChatMessage"
	clDirectMessage = "chunter:class:DirectMessage"
)

// Responder bounds. Conservative defaults; the concurrency cap is overridable in
// Mount via TEAM_AGENTS_MAX_CONCURRENCY.
const (
	agentReplyTimeout    = 90 * time.Second // one agent turn (model call + reply write)
	defaultMaxConcurrent = 4                // global in-flight turn cap default
	backfillGraceMs      = 60_000           // a message created >60s before boot is backfill → never answered
	breakerThreshold     = 3                // consecutive failures before an agent's circuit opens
	breakerCooldown      = 60 * time.Second // how long a tripped agent is skipped
)

// AgentRunner runs agent `agentID` for `org` on behalf of `userSub` with `input`
// and returns the model's text output. It is injected in Mount (only when the
// feature is enabled) as an adapter over agents.RunOnBehalf — the ONE in-process
// run path (billed, metered, recorded) — so clients/team never speaks
// clients/agents' concrete run types and the responder is testable with a fake.
type AgentRunner func(ctx context.Context, org, userSub, agentID, input string) (string, error)

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
	space           string
	attachedTo      string
	attachedToClass string
	collection      string
	message         string // stored markup (HTML)
	authorUID       string // account uuid (social id stripped of the "hanzo:" prefix)
	createdOn       int64  // unix millis; a value below the boot floor is backfill
}

// parseChatMessage returns the chatMsg for an applied tx iff it is a create of a
// chunter:class:ChatMessage. Every other applied tx (roster txes, tracker/docs
// writes, updates, removes) returns ok=false and is ignored. It reads the FLATTENED
// applied tx, so a create wrapped in TxCollectionCUD (which applyTx unwraps, setting
// attachedTo/attachedToClass/collection on the inner tx) is recognized identically
// to a bare TxCreateDoc.
func parseChatMessage(raw json.RawMessage) (chatMsg, bool) {
	var t map[string]any
	if err := json.Unmarshal(raw, &t); err != nil {
		return chatMsg{}, false
	}
	if str(t["_class"]) != clTxCreate || str(t["objectClass"]) != clChatMessage {
		return chatMsg{}, false
	}
	m := chatMsg{
		objectID:        str(t["objectId"]),
		space:           str(t["objectSpace"]),
		attachedTo:      str(t["attachedTo"]),
		attachedToClass: str(t["attachedToClass"]),
		collection:      str(t["collection"]),
		authorUID:       stripHanzo(str(firstNonNil(t["createdBy"], t["modifiedBy"]))),
		createdOn:       asInt64(firstNonNil(t["createdOn"], t["modifiedOn"])),
	}
	if a, ok := t["attributes"].(map[string]any); ok {
		m.message = str(a["message"])
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
func (srv *transServer) maybeAgentReply(org, workspace string, applied []json.RawMessage) {
	if srv.runAgent == nil || srv.bots == nil {
		return // responder disabled (TEAM_AGENTS_ENABLED unset ⇒ no runner wired)
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
	if err != nil || len(bots) == 0 {
		return
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
		for _, bot := range srv.replyTargets(org, workspace, m, byUID) {
			go srv.replyAsBot(org, workspace, m, bot)
		}
	}
}

// replyTargets is the addressing policy: which bots should answer message m.
//   - Direct message: every active bot participant answers (a DM to a bot is, by
//     construction, addressed to it).
//   - Channel / group: only a bot the message explicitly @-mentions answers (a bot
//     does not answer every line in a shared channel).
//
// A bot is never double-targeted (DM + mention) — the seen set collapses them.
func (srv *transServer) replyTargets(org, workspace string, m chatMsg, byUID map[string]Bot) []Bot {
	seen := map[string]bool{}
	var out []Bot
	if doc, _ := srv.store.get(org, workspace, m.space); doc != nil && str(doc["_class"]) == clDirectMessage {
		for _, mem := range toStringSlice(doc["members"]) {
			if b, ok := byUID[mem]; ok && !seen[mem] {
				seen[mem] = true
				out = append(out, b)
			}
		}
	}
	for uid, b := range byUID {
		if !seen[uid] && mentionsBot(m.message, uid) {
			seen[uid] = true
			out = append(out, b)
		}
	}
	return out
}

// replyAsBot runs one bounded agent turn and posts its answer as the bot, in the
// SAME conversation (space + attach coordinates mirrored from the inbound message)
// and through the SAME write path (applyTx + broadcast) the SPA uses. It is
// single-flight per (workspace, space, bot), globally concurrency-capped, and
// circuit-broken per agent; recovered + timeout-bounded so a panicking model
// adapter or a hung call can neither crash the transactor nor leak.
func (srv *transServer) replyAsBot(org, workspace string, m chatMsg, bot Bot) {
	defer func() {
		if r := recover(); r != nil && srv.log != nil {
			srv.log.Error("team: agent reply panicked", "agent", bot.ID, "err", r)
		}
	}()

	// Single-flight: at most one in-flight answer per conversation+bot. A burst of
	// messages to the same DM while a turn is running collapses to one.
	flightKey := workspace + "|" + m.space + "|" + bot.ID
	if _, busy := srv.inflight.LoadOrStore(flightKey, struct{}{}); busy {
		return
	}
	defer srv.inflight.Delete(flightKey)

	// Circuit breaker: skip a persistently-failing agent for a cooldown (the backoff
	// that turns a 403/5xx storm into a quiet trickle).
	if srv.breakerOpen(bot.ID) {
		return
	}

	// Hard concurrency cap: never more than N turns in flight process-wide. Over the
	// cap we DROP (bounded, no queue) rather than pile up goroutines/HTTP calls.
	if srv.sem != nil {
		select {
		case srv.sem <- struct{}{}:
			defer func() { <-srv.sem }()
		default:
			return
		}
	}

	prompt := plainText(m.message)
	if prompt == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentReplyTimeout)
	defer cancel()
	out, err := srv.runAgent(ctx, org, m.authorUID, bot.ID, prompt)
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
	tx := attachedCreateTx(newMsgID(), clChatMessage, m.space, m.attachedTo, m.attachedToClass,
		pick(m.collection, "messages"), "hanzo:"+botUID, map[string]any{"message": htmlMarkup(out)})
	raw, err := json.Marshal(tx)
	if err != nil {
		return
	}
	// A detached system session bound to (org, workspace) — the same shape the
	// /v1/team/bots/sync reconcile uses to write into a workspace off the WS loop.
	sess := &session{server: srv, store: srv.store, hier: srv.hier, org: org, workspace: workspace, account: botUID}
	if _, ap := sess.applyTx(raw); len(ap) > 0 {
		srv.hub.broadcast(workspace, ap)
	}
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

var tagRe = regexp.MustCompile(`<[^>]*>`)

// plainText renders stored message markup (HTML) to the plain text an LLM should
// read: tags dropped, entities unescaped, block tags becoming spaces, whitespace
// collapsed. Deterministic and dependency-free (no markup library on the hot path).
func plainText(markup string) string {
	// Turn common block boundaries into spaces so words don't fuse across tags.
	s := regexp.MustCompile(`(?i)<(/p|br|/div|/li)\s*/?>`).ReplaceAllString(markup, " ")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

// flattenMarkup normalizes an agent answer to PLAIN text, so htmlMarkup can wrap
// it cleanly. Models often reply in HTML/markdown-ish markup (`<p>yes.</p>`); left
// as-is, htmlMarkup would ESCAPE those tags and Chunter would render the literal
// string `<p>yes.</p>`. Block boundaries become newlines (paragraph structure is
// preserved), remaining tags are stripped, entities unescaped. Idempotent on genuine
// plain text (no tags → unchanged but for a safe entity unescape).
func flattenMarkup(text string) string {
	s := regexp.MustCompile(`(?i)</(p|div|li|h[1-6])>|<br\s*/?>`).ReplaceAllString(text, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	// Collapse 3+ newlines to a single blank-line boundary; trim each line's tail.
	s = regexp.MustCompile(`\n{2,}`).ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}

// htmlMarkup wraps an agent's answer as the minimal ProseMirror-compatible markup
// Chunter stores: each non-empty line an escaped <p>. The input is first flattened
// to plain text (models may reply in HTML), so tags are never double-escaped into
// visible text. Empty input yields an empty paragraph (never invalid markup).
func htmlMarkup(text string) string {
	text = flattenMarkup(text)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var b strings.Builder
	for _, ln := range lines {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(html.EscapeString(ln))
		b.WriteString("</p>")
	}
	if b.Len() == 0 {
		return "<p></p>"
	}
	return b.String()
}

// mentionsBot reports whether message markup @-references the bot member. Chunter
// stores a mention as a reference node carrying the referenced Person's _id, which
// is the deterministic PersonRef(uid) — so a substring match on that id is the
// mention test (no markup parse needed).
func mentionsBot(message, uid string) bool {
	return uid != "" && strings.Contains(message, PersonRef(uid))
}

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
