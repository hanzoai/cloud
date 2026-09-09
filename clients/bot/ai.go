package bot

// The ai family: the turn.
//
// A turn is one exchange. A person's message goes into a session's transcript,
// a model is asked to answer it, and the answer goes into the same transcript.
// Everything else in this file is around that: reading a transcript back,
// stopping a turn part way, and saying which models and which agents a caller
// may name when starting one.
//
//	chat.send          start a turn; answer immediately with the run it started
//	chat.abort         stop a turn that is still running
//	chat.history       a page of one session's transcript
//	chat.startup       the same page, reached by a short link
//	chat.message.get   one message, when a page was too large to carry it
//	chat.metadata      what a client may type into a session
//	models.list        the models the inference gateway serves
//	agents.list        the configured agents, and which one is default
//	agents.update      change one agent's name, workspace or model
//
// chat.send answers before the turn finishes. The client mints the run id — it
// sends one as idempotencyKey and every event for the turn carries it back —
// so the answer is {runId, status} and the turn is then carried by `chat`
// events: one delta and one terminal. Each of them carries the full accumulated
// message, which is what lets a client that missed a frame converge without
// re-reading the transcript.
//
// Inference is the cloud's completions client and nothing else. There is no
// model call in this file and no client of its own: deps.AI is on the surface's
// state, and a turn hands it a prompt. It authenticates with the binary's IAM
// M2M identity, and the request names the caller's org as the data scope and
// the caller's home org as the ledger, so no provider credential is stored
// here and no debit lands on the tenant a platform admin is acting on. A
// deployment with no gateway configured has the fail-closed client, and a turn
// there records an honest error rather than a fabricated answer.
//
// A turn runs on its own goroutine rather than through clients/tasks. That
// queue is lease-and-acknowledge for workers that come and go; a turn is one
// interactive exchange that publishes as it goes and must be cancellable by
// the person waiting on it, which is a different shape of work, not the same
// shape done twice.
//
// The live turns are the one authority on what is running. A session row
// records how the last run ended and a run document records the run itself,
// but neither can cancel work: only the goroutine's own handle can, so every
// method that stops a run — chat.abort and sessions.abort alike — resolves it
// through chatStop and there is no second way to stop work.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
)

func init() {
	Register("chat.send", Write, chatSend)
	Register("chat.abort", Write, chatAbort)
	Register("chat.history", Read, chatHistory)
	Register("chat.startup", Read, chatStartup)
	Register("chat.metadata", Read, chatMetadata)
	Register("chat.message.get", Read, chatMessageGet)
	Register("models.list", Read, modelList)
	Register("agents.list", Read, agentList)
	Register("agents.update", Admin, agentUpdate)
	Declare("chat", "session.message")
}

const (
	// Where this family's documents live: one collection per session for its
	// transcript, one document per run so a retry finds its first answer, and
	// one document per configured agent.
	chatIn  = "chat"
	runIn   = "run"
	agentIn = "agent"

	// Bounds, transcribed from the parameter schemas and from what one
	// document may hold.
	chatMost   = 1000      // CHAT_HISTORY_MAX_ENTRIES
	chatPage   = 80        // the page a client gets when it states no limit
	runIDMax   = 256       // CHAT_INPUT_RUN_ID_MAX_CHARS
	messageMax = 128 << 10 // one message, kept well inside MaxDoc

	// agentMost bounds the roster one list carries. An org configures agents
	// by hand, so the ceiling is far above what anyone reaches.
	agentMost = 1000

	// promptScan is how many of a transcript's newest messages a prompt is
	// built from, before the character budget below trims it further. A
	// conversation older than this is older than the budget can carry anyway.
	promptScan = 200

	// promptBudget is how much of a transcript one prompt carries, in
	// characters, most recent first.
	promptBudget = 24000

	// turnMost is how long a turn may run before its context ends it.
	turnMost = 10 * time.Minute
)

// chatQueue is the one way a send may join a session that already has a run.
// ChatSendParamsSchema.queueMode declares four; steer, followup and collect
// each need an admission queue that holds a message until the running turn can
// take it, and there is none here. Accepting a mode and then behaving as if it
// had not been sent is worse than refusing it, so the other three are refused
// by name.
const chatQueue = "interrupt"

// modelViews is the visibility scope a catalog read may ask for
// (ModelsListParamsSchema.view).
var modelViews = []string{"default", "configured", "provider-config", "all"}

// ---- the transcript -------------------------------------------------------

// chatBlock is one content block. The vocabulary a transcript accepts is
// text, input_text, output_text, thinking, image, audio, video, canvas and
// tool_result (with its legacy spelling toolresult); a turn run here produces
// text and nothing else.
type chatBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// chatMark is the metadata envelope a client reads a message's identity from
// (packages/gateway-client/src/session-projection-message-identity.ts): the
// message id, its position in the transcript, and the run that produced it.
// Seq is one-based, because a client reads it as a positive integer and treats
// zero as no position at all.
type chatMark struct {
	ID    string `json:"id"`
	Seq   int    `json:"seq"`
	RunID string `json:"runId,omitempty"`
	Idem  string `json:"idempotencyKey,omitempty"`
}

// chatCost is what one answer cost, in the field names a client reads off an
// assistant message (ui/src/pages/chat/components/chat-message-timestamp.ts).
type chatCost struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// chatEntry is one message as this surface stores and returns it. A minimal
// message is a role, its content and when it happened; the rest says more
// about a message that has more to say.
type chatEntry struct {
	Role      string      `json:"role"`
	Content   []chatBlock `json:"content"`
	Timestamp int64       `json:"timestamp"`
	Model     string      `json:"model,omitempty"`
	Usage     *chatCost   `json:"usage,omitempty"`
	Mark      chatMark    `json:"__openclaw"`
}

// said is the message's text: the text blocks, joined.
func (e *chatEntry) said() string {
	var b strings.Builder
	for _, block := range e.Content {
		if block.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(block.Text)
	}
	return b.String()
}

// chatAt is the collection one session's transcript lives in. The session key
// chooses the collection, so a read of one session cannot reach another.
func chatAt(key string) string { return chatIn + "/" + key }

// chatSay builds one message.
func chatSay(role, text, run, idem string, at int64) chatEntry {
	return chatEntry{
		Role:      role,
		Content:   []chatBlock{{Type: "text", Text: text}},
		Timestamp: at,
		Mark:      chatMark{ID: mint("msg"), RunID: run, Idem: idem},
	}
}

// chatRead loads a window of one session's transcript, oldest first. The
// document id is the message's position zero padded, so id order is transcript
// order and a window of it is exact — a page never reads the whole
// conversation, and a long one never silently truncates.
func chatRead(ctx context.Context, st *Store, key string, limit, offset int) ([]chatEntry, error) {
	docs, err := st.Range(ctx, chatAt(key), limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]chatEntry, 0, len(docs))
	for _, d := range docs {
		var e chatEntry
		if err := json.Unmarshal(d.Doc, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// chatLen is how many messages a session's transcript holds. Because a message
// is written at its own position and nothing is deleted, it is also the
// position of the last message.
func chatLen(ctx context.Context, st *Store, key string) (int, error) {
	return st.Count(ctx, chatAt(key))
}

// chatAdd appends one message to a session's transcript and stamps it with the
// position it took. The document id is that position, zero padded, so the file
// itself carries the order.
//
// Counting the transcript and writing into it are one act. They have to be:
// the position is chosen from the count, the id is the position, and a write
// replaces what is at an id — so two turns appending to one session outside a
// transaction both read the same count, both write at the same id, and the
// first message is gone with nothing to say it ever arrived. Both writers of a
// transcript come through here, which is why there is one of them.
func chatAdd(ctx context.Context, st *Store, key string, e *chatEntry) error {
	return st.Do(ctx, func(st *Store) error {
		n, err := chatLen(ctx, st, key)
		if err != nil {
			return err
		}
		e.Mark.Seq = n + 1
		return st.Put(ctx, chatAt(key), fmt.Sprintf("%08d", e.Mark.Seq), e)
	})
}

// chatTail is the newest messages of a transcript, for the prompt.
func chatTail(ctx context.Context, st *Store, key string, want int) ([]chatEntry, error) {
	total, err := chatLen(ctx, st, key)
	if err != nil {
		return nil, err
	}
	return chatRead(ctx, st, key, want, max(total-want, 0))
}

// chatSaid tells the bot's connections that a message landed in a session's
// transcript, in the shape a client reads a session event in
// (ui/src/lib/sessions/reconcile.ts: parseSessionChangedEvent). hasActiveRun is
// what tells a client whether to wait for more of this turn or to settle.
func chatSaid(org, bot string, s *session, e *chatEntry, running bool) {
	Publish(org, bot, "", "session.message", map[string]any{
		"key":          s.Key,
		"sessionKey":   s.Key,
		"sessionId":    s.SessionID,
		"agentId":      s.agent(),
		"runId":        e.Mark.RunID,
		"messageId":    e.Mark.ID,
		"messageSeq":   e.Mark.Seq,
		"hasActiveRun": running,
		// The message travels as a value: the connection that carries it
		// encodes it on its own goroutine, after this call has returned.
		"message": *e,
		"ts":      e.Timestamp,
	})
}

// ---- a run in flight ------------------------------------------------------

// chatTurn is one turn being run. Exactly one goroutine drives it, which is
// what lets the event counter and the accumulated text be plain fields.
type chatTurn struct {
	org     string
	bill    string
	project string
	bot     string
	key     string
	agent   string
	run     string
	model   string
	st      *Store
	log     luxlog.Logger
	ctx     context.Context
	stop    context.CancelFunc
	seq     int
	// told records that this turn has already been asked to stop. A turn
	// unwinds after it is cancelled, and during that window it is still the
	// session's turn; the flag is what keeps a second abort from reporting
	// that it stopped something the first one had, and what keeps a turn whose
	// model answered just as it was cancelled from writing that answer into a
	// transcript the replacement has already moved on from.
	told atomic.Bool
}

// halt asks the turn to stop and reports whether this call was the one that
// asked. Stopping is idempotent; saying so twice is not.
func (t *chatTurn) halt() bool {
	first := t.told.CompareAndSwap(false, true)
	t.stop()
	return first
}

// turns is every turn in flight. It is indexed twice over the same set: by run,
// which is how an abort naming a run finds it, and by session, which is how a
// send learns the session is busy. The session index is also the claim — a
// session admits one turn at a time, and taking the claim is what reserves it.
//
// Both keys carry the org and the bot, so a lookup cannot cross a tenant or a
// partition. A session key is a client-chosen string, so collisions between
// tenants are the norm rather than an edge case.
var turns = struct {
	sync.Mutex
	byRun map[string]*chatTurn
	onKey map[string]*chatTurn
}{byRun: map[string]*chatTurn{}, onKey: map[string]*chatTurn{}}

// turnAt addresses a turn within one tenant and one bot.
func turnAt(org, bot, id string) string { return org + "\x00" + bot + "\x00" + id }

func (t *chatTurn) runAt() string { return turnAt(t.org, t.bot, t.run) }
func (t *chatTurn) keyAt() string { return turnAt(t.org, t.bot, t.key) }

// chatClaim reserves a session for t. It answers the turn already holding the
// session, if there is one, and whether t took the claim.
//
// The reservation is the whole check-and-act: the test for a live turn and the
// insertion happen under one lock, so two sends racing on one socket cannot
// both find the session free. displace makes the claim take precedence — the
// prior turn is evicted from both indexes here and cancelled by the caller
// outside the lock, and its own later drop cannot evict the replacement.
func chatClaim(t *chatTurn, displace bool) (*chatTurn, bool) {
	turns.Lock()
	defer turns.Unlock()
	prior := turns.onKey[t.keyAt()]
	if prior != nil && !displace {
		return prior, false
	}
	if prior != nil {
		delete(turns.byRun, prior.runAt())
	}
	turns.onKey[t.keyAt()] = t
	turns.byRun[t.runAt()] = t
	return prior, true
}

// chatDrop releases what t holds. The session claim goes only if t still holds
// it: a turn displaced by an interrupt unwinds after its replacement has
// claimed the session, and must not take the replacement's claim with it.
func chatDrop(t *chatTurn) {
	turns.Lock()
	defer turns.Unlock()
	delete(turns.byRun, t.runAt())
	if turns.onKey[t.keyAt()] == t {
		delete(turns.onKey, t.keyAt())
	}
}

// chatHolds reports whether t still holds its session. A displaced turn does
// not: the claim passed to its replacement the moment the interrupt landed, and
// everything the displaced turn does afterwards is unwinding.
func chatHolds(t *chatTurn) bool {
	turns.Lock()
	defer turns.Unlock()
	return turns.onKey[t.keyAt()] == t
}

// chatFind resolves the turn an abort names: the one with that run id, or the
// one running on that session when no run id was given.
func chatFind(org, bot, run, key string) *chatTurn {
	turns.Lock()
	defer turns.Unlock()
	if run != "" {
		return turns.byRun[turnAt(org, bot, run)]
	}
	return turns.onKey[turnAt(org, bot, key)]
}

// chatStop ends the run a caller names and reports what it stopped. It is the
// one way work is stopped on this surface: chat.abort and sessions.abort both
// come through here, so a stop that answers "aborted" has cancelled a
// goroutine and not only rewritten a row.
//
// The turn publishes its own terminal event as it unwinds, so this does not
// announce an ending it has not seen.
func chatStop(org, bot, run, key string) (stopped string, ok bool) {
	t := chatFind(org, bot, run, key)
	if t == nil || (key != "" && t.key != key) || !t.halt() {
		return "", false
	}
	return t.run, true
}

// chatHaltBot stops every turn in flight on one bot. It is chatHaltAll's reason
// narrowed to one partition: a bot being forgotten is about to lose the file
// its turns write into and the connections they publish onto.
func chatHaltBot(org, bot string) {
	turns.Lock()
	all := []*chatTurn{}
	for _, t := range turns.byRun {
		if t.org == org && t.bot == bot {
			all = append(all, t)
		}
	}
	turns.Unlock()
	for _, t := range all {
		t.halt()
	}
}

// chatHaltAll stops every turn in flight. Shutdown calls it: a turn holds a
// store the surface is about to close and publishes onto connections it is
// about to end.
func chatHaltAll() {
	turns.Lock()
	all := make([]*chatTurn, 0, len(turns.byRun))
	for _, t := range turns.byRun {
		all = append(all, t)
	}
	turns.Unlock()
	for _, t := range all {
		t.halt()
	}
}

// chatRun is what is written down about a run, so a retry of a send that was
// already accepted finds the first run rather than starting a second one.
type chatRun struct {
	RunID   string `json:"runId"`
	Key     string `json:"sessionKey"`
	Agent   string `json:"agentId,omitempty"`
	State   string `json:"state"` // running | final | aborted | error
	Seq     int    `json:"messageSeq,omitempty"`
	Started int64  `json:"startedAt,omitempty"`
	Ended   int64  `json:"endedAt,omitempty"`
	Error   string `json:"error,omitempty"`
}

// chatAck is the answer to a send: the run it started and where that run is.
// The four statuses a client branches on are started, in_flight, ok and error
// (ui/src/pages/chat/chat-send-ack.ts).
func chatAck(r *chatRun) map[string]any {
	status := "in_flight"
	switch r.State {
	case "final", "aborted":
		status = "ok"
	case "error":
		status = "error"
	}
	out := map[string]any{"runId": r.RunID, "status": status}
	if r.Seq > 0 {
		out["messageSeq"] = r.Seq
	}
	return out
}

// say publishes one event of this turn. seq is the turn's own counter, which
// is what orders the events of a run; the frame's own sequence orders the
// connection and is stamped as the frame is written.
func (t *chatTurn) say(state string, extra map[string]any) {
	t.seq++
	ev := map[string]any{
		"runId":      t.run,
		"sessionKey": t.key,
		"agentId":    t.agent,
		"seq":        t.seq,
		"state":      state,
	}
	maps.Copy(ev, extra)
	Publish(t.org, t.bot, "", "chat", ev)
}

// settle records how a run ended, and tells the roster if this run is still the
// session's.
//
// The run document is always written: it is this run's own record, addressed by
// its own id, and how it ended is true whoever holds the session afterwards.
// The session row is the session's, and a turn writes it only while it still
// holds the claim — an interrupted turn unwinds after its replacement has taken
// the session, and stamping its own outcome there would report the conversation
// stopped while a model is being asked. The row is re-read for the same reason
// rather than written back from a copy taken before the turn.
func (t *chatTurn) settle(state, why string, at int64) {
	rec := chatRun{RunID: t.run, Key: t.key, Agent: t.agent, State: state, Ended: at, Error: why}
	if err := t.st.Put(t.ctxOrBackground(), runIn, t.run, &rec); err != nil {
		t.log.Error("bot: write run", "org", t.org, "run", t.run, "err", err)
	}
	if !chatHolds(t) {
		return
	}
	ctx := t.ctxOrBackground()
	var s session
	err := t.st.Do(ctx, func(st *Store) error {
		if err := st.Get(ctx, sessionsIn, t.key, &s); err != nil {
			return err
		}
		switch state {
		case "final":
			s.Status = "done"
		case "aborted":
			s.Status = "killed"
		default:
			s.Status = "failed"
		}
		s.LastRunID = t.run
		s.LastRunError = why
		s.LastActivityAt = at
		s.moved(at)
		return st.Put(ctx, sessionsIn, s.Key, &s)
	})
	if err != nil {
		if !errors.Is(err, ErrNoDoc) {
			t.log.Error("bot: write session", "org", t.org, "key", t.key, "err", err)
		}
		return
	}
	Publish(t.org, t.bot, "", "sessions.changed", sessionChanged{
		SessionKey: s.Key, SessionID: s.SessionID, AgentID: s.agent(), Reason: "run", TS: at,
	})
}

// ctxOrBackground is the context the bookkeeping writes use. The turn's own
// context is cancelled by an abort and expired by a timeout, and the record of
// how a run ended must still be written in both cases.
func (t *chatTurn) ctxOrBackground() context.Context { return context.WithoutCancel(t.ctx) }

// carry runs the turn: ask the model, then put the answer where the transcript
// and the client both find it. It is the only place a model is called.
func (t *chatTurn) carry(client cloud.AIClient, prompt string) {
	defer chatDrop(t)
	defer t.stop()

	t.say("status", map[string]any{"phase": "starting_model"})
	out, err := client.ChatCompletion(t.ctx, &cloud.ChatRequest{
		Model:      t.model,
		Prompt:     prompt,
		Org:        t.org,
		BillingOrg: t.bill,
		Project:    t.project,
	})
	at := time.Now().UnixMilli()
	switch {
	// A turn told to stop has stopped, whatever the model did in the
	// meantime. Checking the flag rather than only the context is what keeps
	// an answer that arrived in the cancellation window out of a transcript
	// the session has already moved on from.
	case t.told.Load(), errors.Is(err, context.Canceled):
		t.end("aborted", "", map[string]any{"stopReason": "aborted"}, at)
		return
	case err != nil:
		t.fail(err, at)
		return
	case out == nil || strings.TrimSpace(out.Content) == "":
		t.fail(errors.New("the model returned no content"), at)
		return
	}

	// The answer is finished before any of it is published. A published event
	// is written by the connection's own goroutine, so a message handed to one
	// and then changed would be marshalled while it was being changed.
	reply := chatSay("assistant", out.Content, t.run, "", at)
	reply.Model = t.model
	if out.PromptTokens > 0 || out.CompletionTokens > 0 {
		reply.Usage = &chatCost{Input: out.PromptTokens, Output: out.CompletionTokens}
	}
	// The position of the reply is taken, not assumed: a send may have landed
	// a question in this transcript while the model was being asked, and
	// writing at a position read before that would overwrite it.
	if err := chatAdd(t.ctxOrBackground(), t.st, t.key, &reply); err != nil {
		t.log.Error("bot: write message", "org", t.org, "key", t.key, "err", err)
		t.fail(err, at)
		return
	}

	// The delta rule with no previous text is the whole text, and the message
	// beside it is the full snapshot a client heals from.
	t.say("delta", map[string]any{"deltaText": out.Content, "message": reply})

	var s session
	if err := t.st.Get(t.ctxOrBackground(), sessionsIn, t.key, &s); err == nil {
		chatSaid(t.org, t.bot, &s, &reply, false)
	}
	t.end("final", "", map[string]any{"message": reply, "stopReason": "end_turn"}, at)
}

// end closes the turn: the outcome is written down, the session is released,
// and only then is the terminal event published. In that order a client that
// sends its next message the instant it sees the terminal finds the session
// free and its row already settled, rather than racing the turn it just
// watched finish.
func (t *chatTurn) end(state, why string, extra map[string]any, at int64) {
	t.settle(state, why, at)
	chatDrop(t)
	t.say(state, extra)
}

// fail ends a turn that could not produce an answer. The kind is what a client
// branches on to say whether waiting would help.
func (t *chatTurn) fail(err error, at int64) {
	kind := "unknown"
	switch {
	case errors.Is(err, types.ErrUpstreamBusy):
		kind = "rate_limit"
	case errors.Is(err, context.DeadlineExceeded):
		kind = "timeout"
	}
	why := err.Error()
	t.log.Error("bot: turn failed", "org", t.org, "key", t.key, "run", t.run, "err", err)
	t.end("error", why, map[string]any{"errorMessage": why, "errorKind": kind}, at)
}

// ---- chat.send ------------------------------------------------------------

// chatSendParams is ChatSendParamsSchema. Every field is declared because the
// schema is closed and a client that sends one this surface does not know has
// misunderstood the method. Resume is the one field outside that schema: the
// control UI marks a send it is replaying after a reconnect, and the gateway
// strips the mark before validating (src/gateway/server-methods/
// chat-server-timing.ts). A replay lands on its first run through the
// idempotency key, so the mark needs no second mechanism.
type chatSendParams struct {
	SessionKey  string            `json:"sessionKey"`
	AgentID     string            `json:"agentId"`
	SessionID   string            `json:"sessionId"`
	Message     string            `json:"message"`
	Mentions    json.RawMessage   `json:"mentions"`
	Intent      json.RawMessage   `json:"intent"`
	Thinking    string            `json:"thinking"`
	FastMode    json.RawMessage   `json:"fastMode"`
	FastAfter   int               `json:"fastAutoOnSeconds"`
	QueueMode   string            `json:"queueMode"`
	Deliver     *bool             `json:"deliver"`
	Channel     string            `json:"originatingChannel"`
	To          string            `json:"originatingTo"`
	Account     string            `json:"originatingAccountId"`
	Thread      string            `json:"originatingThreadId"`
	ReplyTo     string            `json:"replyToId"`
	Attachments []json.RawMessage `json:"attachments"`
	Bindings    json.RawMessage   `json:"toolBindings"`
	TimeoutMs   int               `json:"timeoutMs"`
	Provenance  json.RawMessage   `json:"systemInputProvenance"`
	Receipt     string            `json:"systemProvenanceReceipt"`
	NoCommands  *bool             `json:"suppressCommandInterpretation"`
	Leaf        json.RawMessage   `json:"expectedLeafEntryId"`
	Contract    string            `json:"expectedSessionRoutingContract"`
	Permission  json.RawMessage   `json:"expectedPermissionMode"`
	Tools       json.RawMessage   `json:"expectedToolOverrides"`
	Idem        string            `json:"idempotencyKey"`
	Resume      bool              `json:"__controlUiReconnectResume"`
}

// chatSend starts a turn and answers with the run it started. It does not wait
// for the model: the answer says the turn began, and the turn reports itself
// through `chat` events under the run id the caller minted.
//
// expectedLeafEntryId is accepted and not enforced. It is a compare-and-set on
// the branch of the transcript a client is displaying; a session here keeps one
// linear transcript, so there is no branch for a concurrent send to move out
// from under this one.
func chatSend(c *Call) (any, error) {
	var p chatSendParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(p.SessionKey)
	switch {
	case key == "":
		return nil, Invalid("sessionKey is required")
	case len(key) > sessionKeyMax:
		return nil, Invalid("sessionKey exceeds %d bytes", sessionKeyMax)
	}
	run := strings.TrimSpace(p.Idem)
	switch {
	case run == "":
		return nil, Invalid("idempotencyKey is required; it is the run id every event for this turn carries")
	case len(run) > runIDMax:
		return nil, Invalid("idempotencyKey exceeds %d bytes", runIDMax)
	}
	if strings.TrimSpace(p.Message) == "" {
		return nil, Invalid("message is empty")
	}
	if len(p.Message) > messageMax {
		return nil, Invalid("message exceeds %d bytes", messageMax)
	}
	if len(p.Attachments) > 0 {
		return nil, Invalid("this gateway sends text to the model; an attachment has no path through it")
	}
	if p.QueueMode != "" && p.QueueMode != chatQueue {
		return nil, Invalid("this gateway holds no message for a running turn; queueMode is %q or unset, not %q",
			chatQueue, p.QueueMode)
	}

	ai := c.svc.State.ai
	if ai == nil {
		return nil, Unavailable("no inference gateway is bound to this surface")
	}

	st, s, err := chatOpen(c, key, strings.TrimSpace(p.AgentID))
	if err != nil {
		return nil, err
	}

	// A send that was already accepted finds its first run. This is what makes
	// a retry after a lost answer replay rather than speak twice.
	var held chatRun
	if err := st.Get(c.Context(), runIn, run, &held); err == nil {
		return chatAck(&held), nil
	} else if !errors.Is(err, ErrNoDoc) {
		c.Log().Error("bot: read run", "org", c.Org(), "run", run, "err", err)
		return nil, Unavailable("the run could not be read")
	}

	model := chatPick(c, s)
	if model == "" {
		return nil, Unavailable("no model is configured for session %q", key)
	}

	// The turn outlives the request that started it, so it carries a context
	// of its own rather than the connection's or the frame's.
	limit := turnMost
	if p.TimeoutMs > 0 && time.Duration(p.TimeoutMs)*time.Millisecond < turnMost {
		limit = time.Duration(p.TimeoutMs) * time.Millisecond
	}
	ctx, stop := context.WithTimeout(context.Background(), limit)
	t := &chatTurn{
		org: c.Org(), bill: c.Billing(), project: c.Project(), bot: c.Bot(),
		key: key, agent: s.agent(), run: run, model: model,
		st: st, log: c.Log(), ctx: ctx, stop: stop,
	}

	// One turn at a time on a session, so two answers cannot interleave in one
	// transcript and two messages cannot claim one position. Taking the claim
	// is the reservation: everything below runs with the session held, and a
	// failure past this point releases it. An interrupt displaces what is
	// running and takes its place; anything else is told to come back, which is
	// the retry a client already knows how to do.
	prior, mine := chatClaim(t, p.QueueMode == chatQueue)
	if !mine {
		stop()
		return nil, Unavailable("a run is already active on session %q", key)
	}
	if prior != nil {
		prior.halt()
	}
	release := func() {
		chatDrop(t)
		stop()
	}

	at := time.Now().UnixMilli()
	asked := chatSay("user", p.Message, run, run, at)
	if err := chatAdd(c.Context(), st, key, &asked); err != nil {
		release()
		c.Log().Error("bot: write message", "org", c.Org(), "key", key, "err", err)
		return nil, Unavailable("the message could not be written")
	}
	rec := chatRun{RunID: run, Key: key, Agent: s.agent(), State: "running", Seq: asked.Mark.Seq, Started: at}
	if err := st.Put(c.Context(), runIn, run, &rec); err != nil {
		release()
		c.Log().Error("bot: write run", "org", c.Org(), "run", run, "err", err)
		return nil, Unavailable("the run could not be written")
	}
	// The row is read again and stamped inside one act rather than written
	// back from the copy chatOpen took: the turn that just ended settles the
	// same row, and replacing it from a copy read before that would put its
	// run state back and leave a finished conversation looking busy.
	if err := st.Do(c.Context(), func(st *Store) error {
		row, err := getSession(c, st, key)
		if err != nil {
			return err
		}
		row.Status = "running"
		row.LastRunID = run
		row.LastRunError = ""
		row.LastSpokenAt = at
		row.LastActivityAt = at
		s = row
		return saveSession(c, st, row, at)
	}); err != nil {
		c.Log().Error("bot: write session", "org", c.Org(), "key", key, "err", err)
	}
	chatSaid(c.Org(), c.Bot(), s, &asked, true)
	Publish(c.Org(), c.Bot(), "", "sessions.changed", sessionChanged{
		SessionKey: s.Key, SessionID: s.SessionID, AgentID: s.agent(), Reason: "send", TS: at,
	})

	tail, err := chatTail(c.Context(), st, key, promptScan)
	if err != nil {
		tail = []chatEntry{asked}
	}
	go t.carry(ai, chatPrompt(tail))

	return map[string]any{"runId": run, "status": "started", "messageSeq": asked.Mark.Seq}, nil
}

// chatOpen resolves the session a turn runs in, opening it through the family
// that owns sessions when the key names none. A first message to a key nobody
// has used yet is how a conversation starts, and creating the row here as well
// would give a session two ways to come into existence.
func chatOpen(c *Call, key, agent string) (*Store, *session, error) {
	st, err := c.Store()
	if err != nil {
		return nil, nil, err
	}
	var s session
	switch err := st.Get(c.Context(), sessionsIn, key, &s); {
	case err == nil:
	case errors.Is(err, ErrNoDoc):
		if !known("sessions.create") {
			return nil, nil, Unavailable("no sessions surface is mounted to open session %q", key)
		}
		ask := map[string]any{"key": key}
		if agent != "" {
			ask["agentId"] = agent
		}
		if _, err := relay(c, "sessions.create", ask); err != nil {
			return nil, nil, err
		}
		if err := st.Get(c.Context(), sessionsIn, key, &s); err != nil {
			c.Log().Error("bot: read session", "org", c.Org(), "key", key, "err", err)
			return nil, nil, Unavailable("the session could not be read")
		}
	default:
		c.Log().Error("bot: read session", "org", c.Org(), "key", key, "err", err)
		return nil, nil, Unavailable("the session could not be read")
	}
	if agent != "" && s.agent() != agent {
		return nil, nil, Invalid("session %q belongs to agent %q", s.Key, s.agent())
	}
	return st, &s, nil
}

// chatPick is the model a turn runs on: the session's own, then its agent's,
// then the deployment's. Routing between models is the inference gateway's
// work; what is chosen here is only which name to hand it.
func chatPick(c *Call, s *session) string {
	if s.Model != "" {
		return s.Model
	}
	if a, err := agentAt(c, s.agent()); err == nil && a != nil && a.Model != nil && a.Model.Primary != "" {
		return a.Model.Primary
	}
	return c.svc.State.model
}

// chatPrompt renders a conversation as the one prompt the completions client
// takes, most recent first within a character budget. The client's request
// carries a prompt rather than a message list, so the conversation is written
// into it; older turns fall off the front rather than the answer losing the
// question it belongs to.
func chatPrompt(history []chatEntry) string {
	kept := make([]string, 0, len(history))
	budget := promptBudget
	for i := len(history) - 1; i >= 0; i-- {
		said := history[i].said()
		if said == "" {
			continue
		}
		who := "User"
		if history[i].Role == "assistant" {
			who = "Assistant"
		}
		line := who + ": " + said
		if len(kept) > 0 && len(line) > budget {
			break
		}
		budget -= len(line)
		kept = append(kept, line)
	}
	slices.Reverse(kept)
	return strings.Join(kept, "\n\n")
}

// ---- chat.abort -----------------------------------------------------------

type chatAbortParams struct {
	SessionKey string `json:"sessionKey"`
	AgentID    string `json:"agentId"`
	RunID      string `json:"runId"`
	SideRuns   *bool  `json:"preserveSideRuns"`
}

// chatAbort stops a turn that is still running. The turn itself publishes the
// terminal event as it unwinds, so this method ends the work and does not
// announce an ending it has not seen.
func chatAbort(c *Call) (any, error) {
	var p chatAbortParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	key, run := strings.TrimSpace(p.SessionKey), strings.TrimSpace(p.RunID)
	if key == "" && run == "" {
		return nil, Invalid("an abort names a session or a run")
	}
	stopped, ok := chatStop(c.Org(), c.Bot(), run, key)
	if !ok {
		return sessionAbortResult{OK: true, Status: "no-active-run"}, nil
	}
	return sessionAbortResult{OK: true, Status: "aborted", Aborted: &stopped}, nil
}

// ---- chat.history and chat.startup ----------------------------------------

// chatHistoryParams is ChatHistoryParamsSchema.
type chatHistoryParams struct {
	SessionKey    string   `json:"sessionKey"`
	AgentID       string   `json:"agentId"`
	Cursor        string   `json:"cursor"`
	Limit         int      `json:"limit"`
	MaxBytes      int      `json:"maxBytes"`
	Offset        int      `json:"offset"`
	PendingBefore int      `json:"pendingBefore"`
	InputRunIDs   []string `json:"inputRunIds"`
	MessageID     string   `json:"messageId"`
	SessionID     string   `json:"sessionId"`
	MaxChars      int      `json:"maxChars"`
}

// chatView is one page of a transcript. offset counts back from the newest
// message and nextOffset is how deep into the transcript the client now holds,
// which is what a client uses to ask for the page before this one.
//
// InFlight is the turn still running when the page was read. A client that
// reloads mid-turn restores its stop control and its waiting state from it
// (ui/src/pages/chat/chat-history-snapshot.ts), and without it the page reads
// as a settled conversation while a model is still being asked.
type chatView struct {
	Messages   []chatEntry `json:"messages"`
	Offset     int         `json:"offset"`
	NextOffset int         `json:"nextOffset"`
	HasMore    bool        `json:"hasMore"`
	Total      int         `json:"totalMessages"`
	Complete   bool        `json:"completeSnapshot,omitempty"`
	SessionID  string      `json:"sessionId,omitempty"`
	Session    *session    `json:"sessionInfo,omitempty"`
	InFlight   *chatFlight `json:"inFlightRun,omitempty"`
}

// chatFlight is the running turn a page was read across.
type chatFlight struct {
	RunID     string `json:"runId"`
	StartedAt int64  `json:"startedAt,omitempty"`
	Abortable bool   `json:"sessionAbortable"`
}

func chatHistory(c *Call) (any, error) {
	var p chatHistoryParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	return chatLook(c, &p)
}

// chatStartupParams is ChatStartupParamsSchema: either the history parameters,
// or a short link to resolve into a session before reading the same page.
type chatStartupParams struct {
	chatHistoryParams
	ShortID  string `json:"shortId"`
	SlugHint string `json:"slugHint"`
}

// chatStartup opens a session a client reached by its short link and answers
// with its first page. Resolving the link is the sessions family's work, so it
// is asked to do it rather than a second resolver being written here.
func chatStartup(c *Call) (any, error) {
	var p chatStartupParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	short := strings.TrimSpace(p.ShortID)
	if short == "" {
		return chatLook(c, &p.chatHistoryParams)
	}
	if strings.TrimSpace(p.SessionKey) != "" {
		return nil, Invalid("a startup names a session key or a short id, not both")
	}
	if !known("sessions.resolve") {
		return nil, Unavailable("no sessions surface is mounted to resolve a short link")
	}
	ask := map[string]any{"shortId": short, "allowMissing": true}
	if p.SlugHint != "" {
		ask["slugHint"] = p.SlugHint
	}
	if p.AgentID != "" {
		ask["agentId"] = p.AgentID
	}
	var found sessionFound
	if err := relayInto(c, "sessions.resolve", ask, &found); err != nil {
		return nil, err
	}
	if !found.OK || found.Key == "" {
		return nil, Invalid("no session matches short id %q", short)
	}
	p.SessionKey = found.Key
	return chatLook(c, &p.chatHistoryParams)
}

// chatLook reads one page. A key that names no session answers an empty page
// rather than a refusal: a client opens a session before anything has been
// said in it, and a transcript that does not exist yet is empty, not missing.
func chatLook(c *Call, p *chatHistoryParams) (any, error) {
	key := strings.TrimSpace(p.SessionKey)
	if key == "" {
		return nil, Invalid("sessionKey is required")
	}
	if p.Limit < 0 || p.Limit > chatMost {
		return nil, Invalid("limit is between 1 and %d", chatMost)
	}
	if p.Offset < 0 {
		return nil, Invalid("offset cannot be negative")
	}
	// A cursor names a position in a stream of changes this surface does not
	// mint, so any cursor a client holds is one it cannot have got from here.
	// A discontinuity is the protocol's own answer to that, and it tells the
	// client to read a fresh tail.
	if strings.TrimSpace(p.Cursor) != "" {
		return map[string]any{"kind": "reset"}, nil
	}

	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	view := chatView{Messages: []chatEntry{}, Offset: p.Offset, NextOffset: p.Offset, Complete: true}

	var s session
	switch err := st.Get(c.Context(), sessionsIn, key, &s); {
	case err == nil:
		if p.AgentID != "" && s.agent() != p.AgentID {
			return view, nil
		}
		view.Session = &s
		view.SessionID = s.SessionID
	case errors.Is(err, ErrNoDoc):
		return view, nil
	default:
		c.Log().Error("bot: read session", "org", c.Org(), "key", key, "err", err)
		return nil, Unavailable("the session could not be read")
	}

	total, err := chatLen(c.Context(), st, key)
	if err != nil {
		c.Log().Error("bot: count transcript", "org", c.Org(), "key", key, "err", err)
		return nil, Unavailable("the transcript could not be read")
	}
	limit := p.Limit
	if limit == 0 {
		limit = chatPage
	}
	// The page is the newest `limit` messages, `offset` back from the end.
	end := max(total-p.Offset, 0)
	start := max(end-limit, 0)
	history, err := chatRead(c.Context(), st, key, end-start, start)
	if err != nil {
		c.Log().Error("bot: read transcript", "org", c.Org(), "key", key, "err", err)
		return nil, Unavailable("the transcript could not be read")
	}
	view.Total = total
	page := chatFit(history, p.MaxBytes, p.MaxChars)
	view.Messages = page
	view.NextOffset = p.Offset + len(page)
	view.HasMore = view.NextOffset < view.Total
	view.Complete = p.Offset == 0 && !view.HasMore
	if t := chatFind(c.Org(), c.Bot(), "", key); t != nil {
		view.InFlight = &chatFlight{RunID: t.run, Abortable: true}
	}
	return view, nil
}

// chatFit trims a page to the budgets a caller stated, dropping its oldest
// entries and keeping the newest one whatever they are: a page that carried
// nothing would tell a client the transcript is empty.
//
// The two budgets measure different things and each is held to its own.
// maxBytes bounds what the page weighs on the wire, so it is measured over the
// encoded message — the role, the stamps and the content envelope included.
// maxChars bounds what a reader is shown, so it is measured over the text.
// Comparing one against the other would hold a caller that asked for bytes to a
// count of characters, which is several times its stated budget.
func chatFit(page []chatEntry, maxBytes, maxChars int) []chatEntry {
	if len(page) == 0 || (maxBytes <= 0 && maxChars <= 0) {
		return page
	}
	bytes, chars := 0, 0
	weigh := func(e *chatEntry) (int, int) {
		b, err := json.Marshal(e)
		if err != nil {
			return len(e.said()), len(e.said())
		}
		return len(b), len(e.said())
	}
	for i := range page {
		b, ch := weigh(&page[i])
		bytes, chars = bytes+b, chars+ch
	}
	for len(page) > 1 && ((maxBytes > 0 && bytes > maxBytes) || (maxChars > 0 && chars > maxChars)) {
		b, ch := weigh(&page[0])
		bytes, chars = bytes-b, chars-ch
		page = page[1:]
	}
	return page
}

// ---- chat.message.get -----------------------------------------------------

type chatMessageParams struct {
	SessionKey string `json:"sessionKey"`
	AgentID    string `json:"agentId"`
	MessageID  string `json:"messageId"`
	MaxChars   int    `json:"maxChars"`
}

// chatMessageGet reads one message, which is how a client fetches something a
// page was too large to carry. The three reasons a message is not returned are
// the protocol's own, and each of them is a different thing to tell a reader.
func chatMessageGet(c *Call) (any, error) {
	var p chatMessageParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	key, id := strings.TrimSpace(p.SessionKey), strings.TrimSpace(p.MessageID)
	if key == "" || id == "" {
		return nil, Invalid("sessionKey and messageId are required")
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var s session
	if err := st.Get(c.Context(), sessionsIn, key, &s); err == nil {
		if p.AgentID != "" && s.agent() != p.AgentID {
			return map[string]any{"ok": false, "unavailableReason": "not_visible"}, nil
		}
	} else if !errors.Is(err, ErrNoDoc) {
		c.Log().Error("bot: read session", "org", c.Org(), "key", key, "err", err)
		return nil, Unavailable("the session could not be read")
	}
	// A message is addressed by an opaque id and the transcript is ordered by
	// position, so finding one means reading the conversation. That is the
	// method: it exists for the message a page was too large to carry, and is
	// asked for one message at a time rather than in a loop.
	history, err := chatRead(c.Context(), st, key, 0, 0)
	if err != nil {
		c.Log().Error("bot: read transcript", "org", c.Org(), "key", key, "err", err)
		return nil, Unavailable("the transcript could not be read")
	}
	for i := range history {
		if history[i].Mark.ID != id {
			continue
		}
		if p.MaxChars > 0 && len(history[i].said()) > p.MaxChars {
			return map[string]any{"ok": false, "unavailableReason": "oversized"}, nil
		}
		return map[string]any{"ok": true, "message": &history[i]}, nil
	}
	return map[string]any{"ok": false, "unavailableReason": "not_found"}, nil
}

// ---- chat.metadata --------------------------------------------------------

type chatMetadataParams struct {
	AgentID       string `json:"agentId"`
	AuthProfileID string `json:"authProfileId"`
	SessionKey    string `json:"sessionKey"`
}

// chatMetadata is what a client may type into a session: the commands it can
// name. The catalog belongs to the commands family, so it is asked for it; a
// gateway without one answers an empty catalog rather than a refusal, because
// a composer with no slash commands still takes a message.
//
// swarmEnabled reports whether one session may drive several agents at once.
// Nothing here does, so it is false.
func chatMetadata(c *Call) (any, error) {
	var p chatMetadataParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.SessionKey != "" && p.AuthProfileID != "" {
		return nil, Invalid("authProfileId previews a new chat and cannot be combined with sessionKey")
	}
	commands := []json.RawMessage{}
	if known("commands.list") {
		ask := map[string]any{"includeArgs": true, "scope": "text"}
		if p.AgentID != "" {
			ask["agentId"] = p.AgentID
		}
		if p.SessionKey != "" {
			ask["sessionKey"] = p.SessionKey
		}
		// The commands belong to whoever answered; they travel through
		// unopened, because the composer reads them and this method does not.
		var catalog struct {
			Commands []json.RawMessage `json:"commands"`
		}
		if err := relayInto(c, "commands.list", ask, &catalog); err != nil {
			return nil, err
		}
		if catalog.Commands != nil {
			commands = catalog.Commands
		}
	}
	return map[string]any{"commands": commands, "swarmEnabled": false}, nil
}

// ---- models ---------------------------------------------------------------

type modelListParams struct {
	AgentID       string `json:"agentId"`
	SessionKey    string `json:"sessionKey"`
	AuthProfileID string `json:"authProfileId"`
	Provider      string `json:"provider"`
	Details       *bool  `json:"includeDetails"`
	Capabilities  *bool  `json:"includeProviderCapabilities"`
	Prepared      *bool  `json:"preparedOnly"`
	Refresh       *bool  `json:"refresh"`
	View          string `json:"view"`
}

// modelChoice is ModelChoiceSchema narrowed to what this gateway knows about a
// model. A context window, a thinking level, a fast-mode capability and whether
// the model can be reached right now are facts the inference gateway holds and
// this surface does not, so they are absent rather than guessed. Every field
// but the id is optional in the schema, and a selector renders a plain row for
// a model that says only its name.
//
// Provider is required, and a qualified id (openai/gpt-…) says it outright. An
// unqualified one does not, and the honest answer is not silence: the schema
// refuses a row without a provider, so omitting it makes the whole catalog
// unreadable to a selector rather than making one row plainer. What routes an
// unqualified id is this gateway, so that is what the row says — a fact about
// where the model was reached, not a guess at whose model it is.
type modelChoice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// gatewayProvider names what routes a model whose id does not say. It is where
// the model was reached, which is the only provider this surface can state
// truthfully for an unqualified id.
const gatewayProvider = "hanzo"

// modelList is the catalog a selector is built from.
func modelList(c *Call) (any, error) {
	var p modelListParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.Prepared != nil && *p.Prepared && p.Refresh != nil && *p.Refresh {
		return nil, Invalid("preparedOnly reuses what is known and refresh replaces it; ask for one")
	}
	if p.SessionKey != "" && p.AuthProfileID != "" {
		return nil, Invalid("authProfileId previews a new chat and cannot be combined with sessionKey")
	}
	if p.View != "" && !slices.Contains(modelViews, p.View) {
		return nil, Invalid("view is one of %s", strings.Join(modelViews, ", "))
	}

	served, failed := modelCatalog(c)
	models := make([]modelChoice, 0, len(served))
	for _, id := range served {
		provider, name := gatewayProvider, id
		if at := strings.Index(id, "/"); at > 0 {
			provider, name = id[:at], id[at+1:]
		}
		if p.Provider != "" && provider != p.Provider {
			continue
		}
		models = append(models, modelChoice{ID: id, Name: name, Provider: provider})
	}
	out := map[string]any{
		"models":           models,
		"accountSelection": map[string]any{"kind": "automatic", "label": "Automatic"},
	}
	if failed {
		out["refreshFailed"] = true
	}
	return out, nil
}

// modelCatalog is what the inference gateway serves. A client that cannot
// enumerate its catalog — the RPC client, the fail-closed stub — leaves the
// configured default as the one model this deployment can reach, which is the
// model a turn would run on.
func modelCatalog(c *Call) (served []string, failed bool) {
	ai := c.svc.State.ai
	if ai == nil {
		return nil, false
	}
	if lister, ok := ai.(types.ModelLister); ok {
		known, err := lister.Models(c.Context())
		if err != nil {
			failed = true
		} else if len(known) > 0 {
			return known, false
		}
	}
	if m := c.svc.State.model; m != "" {
		return []string{m}, failed
	}
	return nil, failed
}

// models.authLogout is not registered. It forgets a model provider's saved
// credential, and this gateway saves none: inference is reached with the
// binary's own IAM identity, so there is nothing here to sign out of. A method
// that could only ever refuse would put a Sign out control on the model page
// that errors every time it is pressed; leaving it off the advertised list
// leaves the control off the page.

// ---- agents ---------------------------------------------------------------

// agentFace is how an agent presents itself.
type agentFace struct {
	Name   string `json:"name,omitempty"`
	Emoji  string `json:"emoji,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

// agentModel is which model an agent runs on. Fallbacks are the inference
// gateway's routing, not a list kept here.
type agentModel struct {
	Primary string `json:"primary,omitempty"`
}

// agentRow is one configured agent, in the field set AgentSummarySchema
// declares. That schema is closed, so what is here is what may be sent.
type agentRow struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind,omitempty"`
	CreatedVia string      `json:"createdVia,omitempty"`
	CreatedAt  int64       `json:"createdAt,omitempty"`
	Name       string      `json:"name,omitempty"`
	Identity   *agentFace  `json:"identity,omitempty"`
	Workspace  string      `json:"workspace,omitempty"`
	Model      *agentModel `json:"model,omitempty"`
}

// agentAt reads one agent, or nothing when it has never been configured.
func agentAt(c *Call, id string) (*agentRow, error) {
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var a agentRow
	if err := st.Get(c.Context(), agentIn, id, &a); errors.Is(err, ErrNoDoc) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &a, nil
}

// agentList is the roster a selector reads. The default agent is on it whether
// or not anyone has configured it: every session that names no agent belongs
// to that one, so it is a fact of this gateway rather than a row invented to
// fill a screen.
func agentList(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	docs, err := st.List(c.Context(), agentIn, agentMost, 0)
	if err != nil {
		c.Log().Error("bot: read agents", "org", c.Org(), "err", err)
		return nil, Unavailable("the agents could not be read")
	}
	rows := make([]agentRow, 0, len(docs)+1)
	for _, d := range docs {
		var a agentRow
		if err := json.Unmarshal(d.Doc, &a); err != nil {
			continue
		}
		rows = append(rows, a)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	if !slices.ContainsFunc(rows, func(a agentRow) bool { return a.ID == sessionAgent }) {
		rows = append([]agentRow{{ID: sessionAgent, Kind: "agent"}}, rows...)
	}
	return map[string]any{
		"defaultId": sessionAgent,
		"mainKey":   sessionAgent,
		"scope":     "per-sender",
		"agents":    rows,
	}, nil
}

type agentUpdateParams struct {
	AgentID   string          `json:"agentId"`
	Name      string          `json:"name"`
	Workspace string          `json:"workspace"`
	Model     json.RawMessage `json:"model"`
	Emoji     string          `json:"emoji"`
	Avatar    string          `json:"avatar"`
}

// agentUpdate changes what an agent is called, where it works and which model
// it runs on. A model of null clears the override and returns the agent to the
// deployment's default; a model left unsent leaves it as it was.
func agentUpdate(c *Call) (any, error) {
	sent, err := sentFields(c.Params())
	if err != nil {
		return nil, err
	}
	var p agentUpdateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(p.AgentID)
	if id == "" {
		return nil, Invalid("agentId is required")
	}
	if len(p.Name) > sessionLabel || len(p.Workspace) > sessionLabel {
		return nil, Invalid("a name or workspace exceeds %d bytes", sessionLabel)
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	// The row is read and written as one act: a patch names the fields it
	// changes, but the write stores the whole row, so two patches of one agent
	// outside an act keep only the second one's fields.
	if err := st.Do(c.Context(), func(st *Store) error {
		return agentApply(c, st, id, sent, &p)
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "agentId": id}, nil
}

// agentApply is one agents.update against the row as it now stands.
func agentApply(c *Call, st *Store, id string, sent map[string]bool, p *agentUpdateParams) error {
	a := agentRow{ID: id, Kind: "agent", CreatedVia: "operator", CreatedAt: time.Now().UnixMilli()}
	if err := st.Get(c.Context(), agentIn, id, &a); err != nil && !errors.Is(err, ErrNoDoc) {
		c.Log().Error("bot: read agent", "org", c.Org(), "agent", id, "err", err)
		return Unavailable("the agent could not be read")
	}
	if sent["name"] {
		a.Name = strings.TrimSpace(p.Name)
	}
	if sent["workspace"] {
		a.Workspace = strings.TrimSpace(p.Workspace)
	}
	if sent["emoji"] || sent["avatar"] || sent["name"] {
		face := agentFace{Name: a.Name}
		if a.Identity != nil {
			face = *a.Identity
			if sent["name"] {
				face.Name = a.Name
			}
		}
		if sent["emoji"] {
			face.Emoji = strings.TrimSpace(p.Emoji)
		}
		if sent["avatar"] {
			face.Avatar = strings.TrimSpace(p.Avatar)
		}
		a.Identity = &face
	}
	if sent["model"] {
		var name *string
		if err := json.Unmarshal(p.Model, &name); err != nil {
			return Invalid("model is a model id or null")
		}
		switch {
		case name == nil || strings.TrimSpace(*name) == "":
			a.Model = nil
		default:
			a.Model = &agentModel{Primary: strings.TrimSpace(*name)}
		}
	}
	if err := st.Put(c.Context(), agentIn, id, &a); err != nil {
		c.Log().Error("bot: write agent", "org", c.Org(), "agent", id, "err", err)
		return Unavailable("the agent could not be written")
	}
	return nil
}
