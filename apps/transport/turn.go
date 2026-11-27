package transport

// turn.go — the agent answering on a transport.
//
// This is the half integrations used to carry beside the inbox: every adapter
// emitted an event here AND separately spawned an agent turn of its own, so one
// message drove two mechanisms and only the second one ever replied. The turn
// belongs on this side because everything it needs is already here — the policy
// gate that decides whether a sender may speak, the route that says where a
// reply goes, and the egress transports themselves.
//
// What is NOT here is custody. Which Hanzo account a chat user has linked is
// integrations' to answer (client.ChatIdentity) because the link lives in KMS
// under that subsystem; only the answer crosses, never a token.
//
// ── THE ANSWER IS ASYNCHRONOUS ────────────────────────────────────────────────
//
// A run is a model completion with a tool loop under it, and it takes as long as
// it takes. Bounding the REPLY by that made a slow model indistinguishable from
// a broken one: the deadline fired, and the only thing the person in the channel
// ever saw was a sentence saying the run had not finished. The model was working
// the whole time.
//
// So the run is not bounded by a reply deadline at all. A turn posts exactly one
// message and that message BECOMES the answer, however long it took. Nothing a
// person waits for is cut short, which is the whole point: the only way a turn
// can now produce a failure sentence is for something to have actually failed,
// and then it says what.
//
// While it runs, the wait is shown IN that message — "Working…" the moment the
// turn starts, then each line of progress, then the answer in their place
// (live.go). It used to be a Slack status set by the adapter, hung on the ask's
// own thread while the answer was posted wherever this file decided — so a
// question asked in the channel got a status in a thread and a message in the channel,
// two things for one turn, and a status nothing ever cleared. The place is
// decided once, below, and everything the turn shows is shown there.
//
// ── WHERE THE ANSWER GOES ─────────────────────────────────────────────────────
//
// A person answers where they were asked. This did not: every reply carried the
// thread of the message that triggered it, and a top-level @mention was reported
// as threaded under ITSELF, so an answer to something said out loud in the
// transport landed behind a "1 reply" nobody clicks. A colleague who did that to
// every remark would read as a bot, which is exactly how it read.
//
// So the place is decided ONCE, from the ask, before anything is said, and
// carried as the envelope every post of the turn is made from. Deciding once is
// forced by the race above: the acknowledgement is posted BEFORE the answer
// exists, so a rule that read the ANSWER's text would promise in one place and
// deliver in another. The ask is the better signal anyway — where a person chose
// to speak is a fact about the conversation, where their answer belongs is a
// guess about it.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// turnTracer is a var so a test can rebind it: a span that is created and never
// exported is exactly the failure worth catching.
var turnTracer = otel.Tracer("hanzo.transport")

const (
	// turnBudget stops a LEAKED GOROUTINE. It is not the work's deadline.
	//
	// That distinction is the whole of it. Bounding how long the agent may
	// think is the agent's own job and it has three bounds for it, sized
	// against what a model actually does; a second ceiling here, set from a
	// package that cannot see any of that, can only ever be wrong in one of
	// two directions. Too wide and it stops nothing the agent had not already
	// stopped. Too tight — which is what 5 minutes became once a single
	// completion was allowed four — and it cancels a live run from above and
	// describes it with a sentence that knows nothing, which is precisely the
	// silence this surface was reported for.
	//
	// So it is set far past any run that is still working — a turn now waits for
	// the coding run it starts, which agents bounds at under an hour (its
	// turnCeiling) — and nobody is waiting on it: the channel's message moves while
	// the run works. What is left for it to protect is a goroutine and its pool
	// slot, against an agent that answers neither way.
	turnBudget = time.Hour
	// turnsAtOnce caps simultaneous turns across ALL orgs, so no one tenant can
	// exhaust goroutines. turnsPerOrg is a fraction of it, so no one tenant can
	// starve the others. A turn holds its slot until the run returns, so these
	// bound concurrent RUNS and not merely concurrent replies.
	turnsAtOnce = 32
	turnsPerOrg = 8
)

// The plane calls a turn makes, held as values for the reason send.go gives for
// `ask`: a test swaps them to drive both sides of the ack race and to pin what a
// failed run actually says in the channel, neither of which is reachable through a
// live plane.
var (
	identify     = plane.Call[client.ChatIdentityIn, client.ChatIdentityOut]
	fetchHistory = plane.Call[client.ChatHistoryIn, client.ChatHistoryOut]
)

// historyTimeout bounds a best-effort platform transcript read so a slow
// upstream API never stalls an agent turn.
const historyTimeout = 8 * time.Second

// What a turn says when the words are ours rather than the agent's. Collected
// here because a person reads these far more often than anyone reading the code
// expects, and because two of them used to be written at the point of failure
// where nobody compared them.
const (
	identityUnreachable = "I could not reach your Hanzo account just now, so I have not run anything. This is on our side — please try again shortly."
	busyText            = "Too many requests are running for this workspace right now, so I have not started this one — please ask again in a minute."
	linkUndeliverable   = "I need you to connect your Hanzo account before I can run anything, and I could not send you the private connect link here. Send me a direct message and I will hand you one."
	policyUnreadable    = "I could not read who may use me here, so I have not answered. This is on our side — please try again shortly."
	emptyOutput         = "(the agent returned an empty response)"
	failedText          = "Hanzo couldn't finish that — please try again."
	notStartedText      = "Hanzo couldn't start that — please try again."
)

var (
	poolOnce sync.Once
	pool     *limiter
)

// limiter is a global cap with a per-org sub-cap. A turn holds one of each.
type limiter struct {
	all chan struct{}
	mu  sync.Mutex
	per map[string]int
	cap int
}

func newLimiter(all, per int) *limiter {
	return &limiter{all: make(chan struct{}, all), per: map[string]int{}, cap: per}
}

// take reports whether a slot was acquired. It never blocks: a full pool is an
// answer, not a queue, and the caller sheds rather than piling up goroutines
// holding untrusted input.
func (l *limiter) take(org string) bool {
	select {
	case l.all <- struct{}{}:
	default:
		return false
	}
	l.mu.Lock()
	if l.per[org] >= l.cap {
		l.mu.Unlock()
		<-l.all
		return false
	}
	l.per[org]++
	l.mu.Unlock()
	return true
}

func (l *limiter) done(org string) {
	l.mu.Lock()
	if l.per[org]--; l.per[org] <= 0 {
		delete(l.per, org)
	}
	l.mu.Unlock()
	<-l.all
}

// spawn runs a turn in a recovered goroutine, or reports that the pool is full.
//
// The recover is not optional. A turn walks a model completion and a platform
// post over UNTRUSTED input on a shared multi-tenant process, and middleware
// recovery wraps only the synchronous request goroutine — an unrecovered panic
// here would take every tenant down with it. The slot is released on every exit,
// panicking or not, because a pool that leaks slots stops answering.
func spawn(s *cloud.Service[state], org string, run func()) bool {
	poolOnce.Do(func() { pool = newLimiter(turnsAtOnce, turnsPerOrg) })
	if !pool.take(org) {
		s.Log.Warn("transport: turn shed, pool full", "org", org)
		return false
	}
	go func() {
		defer pool.done(org)
		defer func() {
			if r := recover(); r != nil {
				s.Log.Error("transport: turn panic (recovered)", "org", org, "err", r)
			}
		}()
		run()
	}()
	return true
}

// turn answers one message and sends the answer back where it came from.
func turn(s *cloud.Service[state], tr transport, org string, m Message) {
	ctx, cancel := turnContext(org)
	defer cancel()

	// The turn's own span, and the one record that ties a CONVERSATION to a run.
	//
	// The webhook's request span ended when the adapter answered the platform 200,
	// and everything worth observing happens after that, here, on a detached
	// context. So "someone asked @hanzo something in this thread" and "a run
	// happened" were two facts with nothing in common.
	//
	// It carries the run id explicitly because the trace cannot be relied on to:
	// this context has no inbound request behind it, so on a split deployment the
	// run's spans sit in a trace of their own. The id is the join that holds
	// either way — thread → this span → run_id → the run row → its own spans.
	ctx, span := turnTracer.Start(ctx, "agent.turn "+m.Transport, trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	span.SetAttributes(
		// hanzo.org is the key the trace plane files a row under; without it this
		// span belongs to the platform rather than the tenant, and the tenant
		// cannot read its own conversation.
		attribute.String("hanzo.org", org),
		attribute.String("hanzo.chat.provider", m.Transport),
	)
	if m.Channel.ID != "" {
		span.SetAttributes(attribute.String("hanzo.chat.channel", m.Channel.ID))
	}
	if m.ReplyTo != "" {
		span.SetAttributes(attribute.String("hanzo.chat.thread", m.ReplyTo))
	}

	// WHERE THIS TURN ANSWERS, decided once and carried. Every post below is made
	// from this envelope, so the acknowledgement and the answer cannot disagree
	// about the place however long the run takes.
	reply := Message{Transport: m.Transport, Account: m.Account, Channel: m.Channel, ReplyTo: answerIn(m)}

	// WHO IS ASKING, first and synchronously. It is one lookup rather than a run,
	// so it is not what makes a turn slow — and resolving it before the race is
	// what keeps an ephemeral answer ephemeral: a sign-in prompt must never be
	// preceded by a public "working on it" about a run that is not happening.
	who, err := whoIsAsking(ctx, s, org, m)
	if err != nil {
		// LOUD, on the span too. A turn that failed and left an ok span is a turn
		// nobody finds: the trace is the one place a human looks after the fact, and
		// a green span is an assertion that nothing went wrong.
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		// SAID, as well as recorded. Someone asked a question; a bot that goes
		// quiet is indistinguishable from a broken one, and "it does nothing" is
		// the bug report that follows.
		say(ctx, s, tr, org, reply, identityUnreachable)
		return
	}
	if who.Say != "" {
		// No run happened, so there is nothing to attribute and nothing to bill.
		//
		// An ephemeral answer carries a sign-in URL bound to a nonce for THIS
		// person. Posted in a channel, a colleague could click it and bind their own
		// account to this chat user — account takeover, not an inconvenience. So it
		// goes back privately, to the one person who asked.
		//
		// Not every transport has a private reply. Where none exists the prompt
		// cannot be delivered at all and the person is left unlinked with no way to
		// know why, so that is an error and not a shrug.
		if who.Ephemeral {
			if _, perr := post(ctx, org, client.ChatSendIn{
				Provider: m.Transport, Channel: m.Channel.ID,
				User: m.Sender.ExternalID, Private: true, Text: who.Say,
			}); perr != nil {
				s.Log.Error("transport: sign-in prompt undeliverable, user left unlinked",
					"transport", m.Transport, "org", org, "channel", m.Channel.ID, "err", perr)
				span.RecordError(perr)
				span.SetStatus(codes.Error, perr.Error())
				// The LINK stays private; the fact that it could not be sent does not.
				// This sentence carries no URL, so the channel may read it.
				say(ctx, s, tr, org, reply, linkUndeliverable)
			}
			return
		}
		say(ctx, s, tr, org, reply, who.Say)
		return
	}
	if isStop(m.Text) {
		stopHere(ctx, s, tr, org, reply, who)
		return
	}
	if answers(ctx, org, m, reply, who) || steer(ctx, s, org, m, reply, who) {
		return
	}
	streamTurn(ctx, s, tr, org, m, reply, who, span)
}

// asideLines is how long an ask may run before its answer belongs in a thread
// rather than in the transport.
//
// Four lines is the length of a question. Past it a person is PASTING — a stack
// trace, a config, a list of requirements — and what comes back is long enough
// to bury the channel it lands in. A thread is the channel's own affordance for
// exactly that, and the only one worth spending it on.
const asideLines = 4

// codeFence is the other half of the same signal: a fenced block in the ask is
// someone showing code, and the answer to code is more code.
const codeFence = "```"

// aside reports whether an ask is a side discussion rather than a turn in the
// channel's conversation.
//
// It reads the ASK, never the answer, and the header above says why: the
// acknowledgement is posted before an answer exists, and a promise that lands in
// the transport while its answer lands in a thread is worse than either place
// alone.
func aside(text string) bool {
	return strings.Contains(text, codeFence) || strings.Count(text, "\n")+1 > asideLines
}

// answerIn is the ONE place this turn will speak, decided from the ask.
func answerIn(m Message) string {
	// IN a thread, answer in that thread — first, and whatever else is true. A
	// threaded exchange moved out into the transport publishes a side conversation
	// to everyone in the channel, which is a worse failure than burying one.
	if m.ReplyTo != "" {
		return m.ReplyTo
	}
	// A DM is already a private two-party channel. There is no transport to bury and
	// nobody a side discussion would spare, so a thread here only buries a
	// one-line answer behind a click.
	if m.Channel.Kind == ChannelDM {
		return ""
	}
	// A thread can only be opened where the transport reported the ask's own
	// address to root it at. Without one there is nothing to open, and the channel
	// is the honest answer rather than a guess.
	if m.ID != "" && aside(m.Text) {
		return m.ID
	}
	return ""
}

// say posts one message into the conversation, at the address this turn already
// chose.
//
// It takes the REPLY ENVELOPE rather than the ask, which is the whole of keeping
// the asynchronous turn honest: the acknowledgement and the answer are two posts
// of one turn, so the place is decided once in turn() and carried here rather
// than re-derived per message from a fact that may have stopped being true.
//
// A TURN OWES ONE REPLY, AND A SEND THAT FAILED ONCE HAS NOT PAID IT.
//
// By the time there is something to say the run has billed, so this post is the
// only record the asker will ever have of it. One failed send used to be logged
// and the answer dropped — a turn that ran, billed, and spoke into nothing, which
// reads in the channel as an agent that never answered.
//
// The send crosses a process boundary to a transport that may be waking, on a
// host that may be busy, so a first failure is more often a fact about the moment
// than about the message. It is offered again, with waits that grow so a transport
// coming back is not hammered, and a bounded number of times so one that stays
// down releases the pool slot. [plane.ErrNoPeer] is the one failure that is not
// about the moment: the transport's app is not part of this deployment, and no
// wait will deploy it.
//
// Delivery is therefore AT-LEAST-ONCE. A send carries no idempotency key, so a
// reply whose acknowledgement was lost may be said twice, and that is the better
// failure: a repeated answer can be read and a missing one cannot.
func say(ctx context.Context, s *cloud.Service[state], tr transport, org string, reply Message, text string) {
	reply.Text = text
	wait := replyWait
	for try := 1; ; try++ {
		_, err := tr.send(ctx, s, org, reply)
		if err == nil {
			return
		}
		s.Log.Warn("transport: reply", "transport", reply.Transport, "try", try, "err", err)
		if try == replyTries || errors.Is(err, plane.ErrNoPeer) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 4
	}
}

// How a turn offers its reply: replyTries sends, waiting replyWait and then four
// times as long after each failure. Values rather than constants for the reason
// the plane calls above are — a test shortens the wait to drive both outcomes.
var (
	replyTries = 4
	replyWait  = 2 * time.Second
)

// turnContext is the context ONE turn runs on: detached from any request,
// carrying the tenant it bills, bounded by the turn budget.
//
// Both halves are load-bearing. A run bills, and the balance gate takes the org
// from the CALLER's identity and never from an argument, so the org has to ride
// the caller. cloud.For states it — but a stated caller is read only where there
// is NO REQUEST behind the context, deliberately, so that stating an identity can
// never override an authenticated one. On a webhook's ctx it is a silent no-op.
//
// Detaching is independently required: the adapter answered the platform long
// ago, so on its context the completion would be cancelled immediately.
//
// It is not a laundering hole. For supplies a tenant where there is none and
// cannot override one, and this org came from OrgForExternalID on a
// signature-verified payload — never from a field a client chose.
func turnContext(org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cloud.For(context.Background(), org), turnBudget)
}

// whoIsAsking resolves the asker's linked Hanzo identity, or reports that it
// could not be reached. An identity endpoint that answers nothing is a failure
// and says so, rather than being read as "this person is unlinked".
func whoIsAsking(ctx context.Context, s *cloud.Service[state], org string, m Message) (*client.ChatIdentityOut, error) {
	who, err := identify(ctx, manifest.Provider, client.ChatIdentity,
		&client.ChatIdentityIn{Org: org, Provider: m.Transport, ExternalID: m.Account, User: m.Sender.ExternalID})
	if err == nil && who == nil {
		err = errors.New("identity endpoint returned nothing")
	}
	if err != nil {
		// The person cannot fix an unreachable identity endpoint, but they can stop
		// waiting, and we can be found in the logs and the trace.
		s.Log.Error("transport: identity unreachable", "transport", m.Transport, "org", org, "err", err)
		return nil, err
	}
	return who, nil
}

// runFailure is what a failed run says in the channel: a Hanzo-level sentence and
// the run id that finds the rest. The run's own account of its failure — model
// names, upstream addresses and codes — goes to the log and the trace only
// (live.go), never into a channel a workspace reads.
func runFailure(id string) string {
	if id == "" {
		return failedText
	}
	return strings.TrimSuffix(failedText, ".") + " (run " + id + ")."
}

// notStarted is what a turn says when agents would not start it. Why is logged
// where it happened.
func notStarted() string { return notStartedText }

// lostTrack is what a turn says when agents stopped answering while it ran. The
// run may still finish; only its rendering is over, and the run id finds it.
func lostTrack(runID string) string {
	return "Hanzo lost track of that run (run " + runID + ") — please ask again."
}

// unusable is what a turn says in place of an answer its model degenerated into:
// a run of special tokens, a loop, its reasoning where the answer belongs. The
// garbage is never posted; the run id finds it.
func unusable(runID string) string {
	return "The model returned an unusable answer (run " + runID + "). This is on our side — please ask again."
}

// maxHistoryGap is the 48h session boundary for unthreaded conversations. What a
// conversation may hold is client.Fit's budget, the one every surface shares.
const maxHistoryGap = 48 * 3600

// withToolSummary attaches a capped tool-call summary block to a reply so it
// is saved in the inbox and included in loadHistory.
func withToolSummary(reply, tools string) string {
	reply = strings.TrimSpace(reply)
	tools = strings.TrimSpace(tools)
	if tools == "" || strings.Contains(reply, "[Tools used]") {
		return reply
	}
	if len(tools) > client.ToolsBytes {
		tools = tools[:client.ToolsBytes]
	}
	if reply == "" {
		return "[Tools used]\n" + tools
	}
	return reply + client.ToolsMarker + tools
}

// loadHistory fetches recent conversation turns in this channel or thread, oldest
// first, excluding the current message m itself so the agent receives the full
// dialogue context.
func loadHistory(ctx context.Context, st *store, org string, m Message) []client.Turn {
	if st == nil || m.Transport == "" || m.Channel.ID == "" {
		return nil
	}
	rows, err := st.recentChannel(ctx, org, m.Transport, m.Account, m.Channel.ID, recentLimit)
	if err != nil || len(rows) == 0 {
		return nil
	}
	var currentAt int64
	filtered := make([]inboxRow, 0, len(rows))
	skippedCurrent := false
	for _, r := range rows {
		if !skippedCurrent {
			if m.Idempotency != "" && r.EventKey == m.Idempotency {
				skippedCurrent = true
				currentAt = r.CreatedAt
				continue
			}
			if m.Idempotency == "" && r.Text == m.Text && r.Sender == m.Sender.ExternalID {
				skippedCurrent = true
				currentAt = r.CreatedAt
				continue
			}
		}
		if m.ReplyTo != "" && m.Channel.Kind != ChannelDM {
			isRoot := r.ReplyTo == "" && strings.HasSuffix(r.EventKey, ":"+m.ReplyTo)
			if r.ReplyTo != m.ReplyTo && !isRoot {
				continue
			}
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		return nil
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })

	// Stop at multi-day gaps in unthreaded rooms/DMs so weeks-old probes or
	// pastes do not bleed in; what is left is held to the shared budget (Fit).
	startIdx := 0
	nextAt := currentAt
	for i := len(filtered) - 1; i >= 0; i-- {
		if m.ReplyTo == "" && nextAt > 0 && filtered[i].CreatedAt > 0 && nextAt-filtered[i].CreatedAt > maxHistoryGap {
			startIdx = i + 1
			break
		}
		if filtered[i].CreatedAt > 0 {
			nextAt = filtered[i].CreatedAt
		}
	}
	kept := filtered[startIdx:]

	turns := make([]client.Turn, 0, len(kept))
	for _, r := range kept {
		text := strings.TrimSpace(r.Text)
		if text == "" {
			continue
		}
		self := r.SenderUser == "assistant" || r.Sender == "bot"
		sender := r.SenderUser
		if sender == "" {
			sender = r.Sender
		}
		turns = append(turns, client.Turn{
			Sender: sender,
			Self:   self,
			Text:   text,
			At:     r.CreatedAt,
		})
	}
	return client.Fit(turns)
}

// turnHistory returns the conversation context for a turn, oldest first.
//
// For Slack, the platform itself is asked first (via provider's ChatHistory
// plane op) because transport's local inbox only records messages previously
// addressed to @hanzo — any prior human discussion, stack trace, bot alert, or
// thread parent posted before @hanzo was mentioned only exists in Slack. Any
// [Tools used] summaries stored on local assistant turns are merged back onto
// matching Slack assistant replies so tool memory is never lost. If the Slack
// read fails (e.g. missing_scope, not_in_channel, or peer unavailable), the
// turn degrades gracefully to the local inbox history rather than failing.
func turnHistory(ctx context.Context, s *cloud.Service[state], org string, m Message) []client.Turn {
	var st *store
	if s != nil {
		st = s.State.store
	}
	local := loadHistory(ctx, st, org, m)
	if m.Transport != "slack" || strings.TrimSpace(m.Channel.ID) == "" {
		return local
	}
	hctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	out, err := fetchHistory(hctx, manifest.Provider, client.ChatHistory, &client.ChatHistoryIn{
		Org:        org,
		Provider:   m.Transport,
		ExternalID: m.Account,
		Channel:    m.Channel.ID,
		ThreadID:   m.ReplyTo,
		MessageID:  m.ID,
		Text:       m.Text,
	})
	if err != nil || out == nil {
		if err != nil && !errors.Is(err, plane.ErrNoPeer) && s != nil && s.Log != nil {
			s.Log.Warn("transport: platform history unavailable, falling back to inbox",
				"transport", m.Transport, "org", org, "channel", m.Channel.ID, "thread", m.ReplyTo, "err", err)
		}
		return local
	}
	if len(out.Turns) == 0 && len(local) > 0 {
		return local
	}
	return client.Fit(mergeToolSummaries(out.Turns, local))
}

// mergeToolSummaries carries any [Tools used] block stored on local inbox
// assistant turns onto the matching platform assistant replies.
func mergeToolSummaries(platform, local []client.Turn) []client.Turn {
	if len(platform) == 0 || len(local) == 0 {
		return platform
	}
	type savedTools struct {
		prose string
		tools string
		used  bool
	}
	var saved []savedTools
	for _, t := range local {
		if !t.Self {
			continue
		}
		if idx := strings.Index(t.Text, client.ToolsMarker); idx >= 0 {
			saved = append(saved, savedTools{
				prose: strings.TrimSpace(t.Text[:idx]),
				tools: strings.TrimSpace(t.Text[idx+len(client.ToolsMarker):]),
			})
		}
	}
	if len(saved) == 0 {
		return platform
	}
	out := append([]client.Turn(nil), platform...)
	for i := range out {
		if !out[i].Self || strings.Contains(out[i].Text, "[Tools used]") {
			continue
		}
		prose := strings.TrimSpace(out[i].Text)
		for j := range saved {
			if !saved[j].used && saved[j].prose != "" && saved[j].prose == prose {
				saved[j].used = true
				out[i].Text = withToolSummary(out[i].Text, saved[j].tools)
				break
			}
		}
	}
	return out
}
