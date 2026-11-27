package agents

// turn.go runs a chat turn as a live session.
//
// A chat surface used to call RunOnBehalf and wait: nothing to show until the
// answer arrived, and nothing to stop. A turn now opens a session first, runs
// the agent on a context this process owns, and reports into the session as it
// goes — what it is doing while it works, the answer when it is done, and a
// terminal status. A surface reads those events and renders them, and stopping
// the turn is stopping the session. Every surface reads the same stream, and the
// console already renders it.
//
// The run belongs to agents, not to the surface that asked. A surface that
// disconnects neither kills the run nor leaves it running unwatched: the run
// finishes, and its session says how.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/trace"
)

// turnCeiling bounds a turn's goroutine: the tool loop, then the coding runs it
// started (follow.go). Their own bounds end real work well before it; this stops
// a leak.
const turnCeiling = toolRunBudget + followWait + 5*time.Minute

// turnWait is how long a read of a turn's events waits for something new.
const turnWait = 20 * time.Second

func exposeTurns() {
	cloud.Plane().Post("/agents/turns/start", planeTurnStart,
		zip.WithOperationID(client.AgentsTurnStart),
		zip.WithSummary("Start a chat turn as a live session"))
	cloud.Plane().Post("/agents/turns/events", planeTurnEvents,
		zip.WithOperationID(client.AgentsTurnEvents),
		zip.WithSummary("Read what a turn has said since a sequence number"))
}

// planeTurnStart starts a bridge's turn and answers with its session.
//
// The org that pays is the one the wire proved, never the argument. The run
// detaches from this request and has to state its org again, so it states the
// one the caller was authenticated as; an argument naming another is refused.
func planeTurnStart(ctx context.Context, in *client.RunOnBehalfIn) (*client.TurnStarted, error) {
	if mounted == nil {
		return nil, fmt.Errorf("%w: agents", cloud.ErrNoPeer)
	}
	if strings.TrimSpace(in.Subject) == "" {
		return nil, fmt.Errorf("agents: a turn requires a linked subject")
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agents: a turn needs the org on the call")
	}
	if in.Org != "" && in.Org != org {
		return nil, zip.ErrForbidden("agents: the org named is not the caller's")
	}
	return startTurn(mounted, ctx, org, in)
}

// startTurn opens the session and starts the run behind it — or, when the turn
// asks to resume and the asker's last turn in this conversation was stopped or
// paused, starts that one again (resumeTurn).
func startTurn(s *cloud.Service[state], ctx context.Context, org string, in *client.RunOnBehalfIn) (*client.TurnStarted, error) {
	sto, err := s.State.storeFor(org)
	if err != nil {
		return nil, err
	}
	channel := channelOf(in.Reply)
	if in.Resume && channel != "" {
		x, err := sto.LastTurn(ctx, org, billingActor(org, in.Subject), channel)
		if err == nil && resumable(x) {
			t, err := termsOf(ctx, sto, x, in)
			if err != nil {
				return nil, err
			}
			return resumeTurn(s, ctx, sto, x, in, t)
		}
		if err != nil && !errors.Is(err, errSessionNotFound) {
			return nil, err
		}
	}
	now := time.Now().Unix()
	id := mint.ID("sess")
	x := Session{
		ID: id, Org: org, Agent: agentLabel(in.Ref), Actor: billingActor(org, in.Subject),
		Status: StatusRunning, RootID: id, Title: runTitle(in.Input), Channel: channel,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
		Payer: payerOf(ctx, org).Subject(), MeteredAt: now,
	}
	t, err := termsOf(ctx, sto, x, in)
	if err != nil {
		return nil, err
	}
	return runTurn(s, ctx, sto, x, in, sto.CreateSession, t)
}

// resumable reports whether x is a turn that can be picked up again: a run's
// session that was stopped or paused. A turn that finished or failed is over.
func resumable(x Session) bool {
	return x.Run != "" && (x.Status == StatusStopped || x.Status == StatusPaused)
}

// resumeTurn starts a stopped or paused turn again: the SAME session, under a new
// run, told in.Input next. The run it starts holds the sandbox the session parked
// (withBox), so the first sandbox call gives that one a pod again and the model
// finds the tree it left.
//
// It is the agent the turn ran, as the person whose turn it was: the session says
// both, and a resume is not a way to run somebody else's agent or to run one as
// somebody else.
func resumeTurn(s *cloud.Service[state], ctx context.Context, sto *Store, x Session, in *client.RunOnBehalfIn, t terms) (*client.TurnStarted, error) {
	ask := *in
	ask.Ref = x.Agent
	return runTurn(s, ctx, sto, x, &ask, func(ctx context.Context, x Session) error {
		ok, err := sto.Reopen(ctx, x.Org, x.ID, x.Run, x.StartedAt)
		if err != nil {
			return err
		}
		if !ok {
			return zip.Errorf(http.StatusConflict, "agents: the turn is no longer stopped or paused")
		}
		return nil
	}, t)
}

// terms are what a turn runs on besides its session: whose balance it spends,
// the conversation before it, the person's words to record on the session before
// it starts, and what the app chose for its code (talk.go). A bridge records no
// words — its conversation is the channel's, which the bridge holds.
type terms struct {
	payer   account.Account
	history []infer.ChatMessage
	said    string
	ws      *workspace
	// base is what the run's context is detached from: for the app's conversation,
	// the asker as their request stated them and the payer it resolved
	// (cloud.Detach), so the sandbox and the tools the run reaches for are paid by
	// the one who asked. A bridge's turn has none, and runs as its org.
	base context.Context
}

// termsOf is the ONE place a turn's terms are decided, by what the session is. The
// app's conversation (talk.go) spends the asker's own balance and remembers itself:
// its turns are on its session. A bridge's turn spends the org's that connected the
// bridge, and its conversation is the one the bridge carried in.
func termsOf(ctx context.Context, sto *Store, x Session, in *client.RunOnBehalfIn) (terms, error) {
	if talks(x) {
		history, err := remembered(ctx, sto, x)
		if err != nil {
			return terms{}, err
		}
		t := terms{payer: cloud.PayerOf(ctx).Wallet, history: history, ws: workspaceOf(x)}
		if c, ok := cloud.Request(ctx); ok {
			// The asker and their payer, detached from the request — less their admin
			// bits: the turn's tools act as its person, not with their authority.
			base := cloud.Detach(context.Background(), c)
			who := zip.CallerOf(base)
			who.Admin, who.OrgAdmin = false, false
			t.base = zip.WithCaller(base, who)
		}
		return t, nil
	}
	return terms{payer: meter.PayerFor(x.Org, ""), history: channelHistory(ctx, sto, x.Org, x.Channel, in.History)}, nil
}

// runTurn starts the run behind x, once open has made x the session's row — a new
// row for a new turn, the stopped or paused one reopened for a resume.
func runTurn(s *cloud.Service[state], ctx context.Context, sto *Store, x Session, in *client.RunOnBehalfIn,
	open func(context.Context, Session) error, t terms) (*client.TurnStarted, error) {
	org := x.Org
	now := time.Now().Unix()
	x.Run, x.Status, x.StartedAt, x.EndedAt, x.UpdatedAt = mint.ID("run"), StatusRunning, now, 0, now

	// The run outlives this request. It keeps the org, where to narrate and the
	// trace, and nothing else of the request.
	base := context.Background()
	if t.base != nil {
		base = t.base
	}
	rctx := cloud.For(base, org)
	rctx = trace.ContextWithSpanContext(rctx, trace.SpanContextFromContext(ctx))
	rctx = withReply(rctx, in.Reply, in.Origin)
	rctx, cancel := context.WithCancelCause(rctx)
	rctx, cancelTimeout := context.WithTimeout(rctx, turnCeiling)

	// Held before the row says running, so the sweep never finds this session live
	// and unheld and ends it as an orphan. A session whose last run is still
	// unwinding here is refused rather than raced.
	if !hold(x.ID, cancel) {
		cancelTimeout()
		cancel(nil)
		return nil, zip.Errorf(http.StatusConflict, "agents: the turn's last run is still stopping; try again in a moment")
	}
	if err := open(ctx, x); err != nil {
		release(x.ID)
		cancelTimeout()
		cancel(nil)
		return nil, fmt.Errorf("agents: open turn: %w", err)
	}
	publishSession(s, x, 0, 0)

	l := &live{s: s, x: x, run: x.Run, ws: t.ws, free: sync.OnceFunc(func() { release(x.ID) })}
	// The person's words open the turn on its session, before anything the run
	// says, so the conversation reads in the order it was had.
	if t.said != "" {
		payload, _ := json.Marshal(map[string]string{"role": infer.RoleUser, "text": t.said})
		l.append(ctx, KindMessage, payload)
	}
	go func() {
		defer cancel(nil)
		defer cancelTimeout()
		defer l.letGo()
		defer func() {
			if r := recover(); r != nil {
				s.Log.Error("agents: turn panic (recovered)", "org", org, "session", x.ID, "err", r)
				ctx := context.WithoutCancel(rctx)
				l.log(ctx, Run{Status: "error"}, fmt.Sprint(r))
				// The app's conversation outlives a turn that failed (talk.go): it rests.
				if talks(l.x) {
					l.rest(ctx)
					return
				}
				l.end(ctx, StatusError)
			}
		}()
		r, err := runOnBehalfModel(s, withLive(rctx, l), org, in.Subject, in.Ref, in.Input, in.Model, t.history, t.payer)
		l.finish(rctx, r, err)
	}()
	return &client.TurnStarted{SessionID: x.ID, RunID: x.Run}, nil
}

// channelHistory converts in.History to model messages, weaving in any missing
// assistant replies recorded on completed runs in the same channel (for turns
// that ran before transport recorded outbound replies into its inbox).
func channelHistory(ctx context.Context, sto *Store, org, channel string, turns []client.Turn) []infer.ChatMessage {
	if len(turns) == 0 {
		return nil
	}
	var runs []ChannelRun
	if sto != nil && channel != "" {
		runs, _ = sto.ChannelRuns(ctx, org, channel, 20)
	}
	used := make([]bool, len(runs))
	multiParty := isMultiPartyChannel(channel, turns)
	msgs := make([]infer.ChatMessage, 0, len(turns)*2)
	for i, t := range turns {
		text := strings.TrimSpace(t.Text)
		if text == "" {
			continue
		}
		if t.Self {
			if len(runs) > 0 && !strings.Contains(text, "[Tools used]") && i > 0 && !turns[i-1].Self {
				prevUser := strings.TrimSpace(turns[i-1].Text)
				for j, cr := range runs {
					if !used[j] && strings.TrimSpace(cr.Input) == prevUser {
						used[j] = true
						text = WithToolSummary(text, cr.Tools)
						break
					}
				}
			}
			msgs = append(msgs, infer.ChatMessage{Role: infer.RoleAssistant, Content: text})
			continue
		}
		userContent := formatChannelUserTurn(multiParty, t.Sender, text)
		hasNextSelf := i+1 < len(turns) && turns[i+1].Self
		if !hasNextSelf && len(runs) > 0 {
			matched := -1
			for j, cr := range runs {
				if !used[j] && strings.TrimSpace(cr.Input) == text {
					matched = j
					break
				}
			}
			if matched >= 0 {
				used[matched] = true
				out := strings.TrimSpace(runs[matched].Output)
				if out == "PONG" && !strings.Contains(strings.ToUpper(text), "PONG") {
					continue
				}
				out = WithToolSummary(out, runs[matched].Tools)
				msgs = append(msgs,
					infer.ChatMessage{Role: infer.RoleUser, Content: userContent},
					infer.ChatMessage{Role: infer.RoleAssistant, Content: out},
				)
				continue
			}
		}
		msgs = append(msgs, infer.ChatMessage{Role: infer.RoleUser, Content: userContent})
	}
	return msgs
}

// isMultiPartyChannel reports whether turns belong to a shared room/thread
// where speaker attribution prevents consecutive human/bot messages from
// blurring together when merged by openaiMessages.
func isMultiPartyChannel(channel string, turns []client.Turn) bool {
	if channel != "" && !strings.HasPrefix(channel, "slack:D") {
		return true
	}
	firstSender := ""
	for _, t := range turns {
		if t.Self {
			continue
		}
		s := strings.TrimSpace(t.Sender)
		if s == "" {
			continue
		}
		if firstSender == "" {
			firstSender = s
		} else if s != firstSender {
			return true
		}
	}
	return false
}

func formatChannelUserTurn(multiParty bool, sender, text string) string {
	sender = strings.TrimSpace(sender)
	if !multiParty || sender == "" {
		return text
	}
	const preThreadPrefix = "[channel context before thread] "
	if rest, ok := strings.CutPrefix(text, preThreadPrefix); ok {
		if strings.HasPrefix(rest, sender+": ") {
			return text
		}
		return preThreadPrefix + sender + ": " + rest
	}
	if strings.HasPrefix(text, sender+": ") {
		return text
	}
	return sender + ": " + text
}

// agentLabel is the session's agent name: the ref asked for, or the default.
func agentLabel(ref string) string {
	if r := strings.TrimSpace(ref); r != "" && len(r) <= maxAgentLabel {
		return r
	}
	return "hanzo"
}

// channelOf is the conversation a turn answers in, as a label.
func channelOf(r client.Reply) string {
	if r.Channel == "" {
		return ""
	}
	channel := r.Provider + ":" + r.Channel
	if r.Thread != "" {
		channel += "/" + r.Thread
	}
	return channel
}

// live is the session a turn's run reports into. It rides the run's context,
// so the tool loop can say what it is doing without being handed a session.
//
// It holds NO STORE, and that is the fix for a turn that answered and still read
// `running`. A turn runs for minutes, and cloud.OrgStore does not promise a
// handle for that long: an idle store is evicted and a promoted one replaced,
// each by closing the handle (orgdb.go reclaim, promote), because no REQUEST
// holds one that long. A turn did. Every write it made after that — its
// progress, its answer, its end — went into a closed database, each failure
// swallowed, and the row stayed `running` until something else ended it. So the
// store is resolved for each write, the way a request resolves its own.
type live struct {
	s   *cloud.Service[state]
	x   Session
	run string
	ws  *workspace // what the app chose for this turn's code (talk.go); nil off the app
	// quiet reports no step as progress: a conversation's follower (talk.go follow).
	quiet bool
	free  func() // lets the session's hold go, once (letGo)

	mu         sync.Mutex
	last       string          // the last line reported, so a repeat is not reported twice
	runs       []string        // the coding runs this turn started, by session (follow.go)
	refused    string          // why its last coding call was refused, until a later one starts a run
	reads      map[string]bool // the tools offered to this turn that change nothing (talk.go furnish)
	tools      []string        // one-line summaries per tool call, capped at maxToolSummaryBytes
	toolsBytes int
}

const (
	// maxToolSummaryBytes caps the total tool-call summary stored with a turn's
	// reply (~1.5 KB) so subsequent turns know what tools were called and what
	// URLs/IDs they returned without bloating the history budget.
	maxToolSummaryBytes = 1536
	maxToolLineBytes    = 280
)

var toolURLRE = regexp.MustCompile(`https?://[^\s"'<>\]\)]+`)

// WithToolSummary appends a turn's tool summary block to reply when tools is
// non-empty and not already present.
func WithToolSummary(reply, tools string) string {
	reply = strings.TrimSpace(reply)
	tools = strings.TrimSpace(tools)
	if tools == "" || strings.Contains(reply, "[Tools used]") {
		return reply
	}
	if len(tools) > maxToolSummaryBytes {
		tools = text.Cut(tools, maxToolSummaryBytes)
	}
	if reply == "" {
		return "[Tools used]\n" + tools
	}
	return reply + "\n\n[Tools used]\n" + tools
}

// recordTool appends a concise one-line summary of one tool call (name, key
// args, short result such as an issue/PR URL), capped at maxToolSummaryBytes.
func (l *live) recordTool(name, args, out string) {
	if l == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" || name == loadTools.Name || name == client.Describe {
		return
	}
	line := summarizeToolCall(name, args, out)
	if line == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sep := 0
	if len(l.tools) > 0 {
		sep = 1
	}
	rem := maxToolSummaryBytes - l.toolsBytes - sep
	if rem <= 16 {
		return
	}
	if len(line) > rem {
		line = text.Cut(line, rem-len("…")) + "…"
	}
	l.tools = append(l.tools, line)
	l.toolsBytes += sep + len(line)
}

func (l *live) toolSummary() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.tools, "\n")
}

func summarizeToolCall(name, args, out string) string {
	label := name
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		if op, ok := m["op"].(string); ok && strings.TrimSpace(op) != "" {
			label = name + "/" + strings.TrimSpace(op)
		}
		if nested, ok := m["input"].(map[string]any); ok {
			for k, v := range nested {
				m[k] = v
			}
		}
	}
	var kv []string
	for _, k := range []string{"key", "repo", "owner", "project", "num", "number", "title", "assignee", "status", "kind", "channel", "session", "id", "path", "q", "query", "url", "prompt"} {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		s := collapse(audit.RedactText(fmt.Sprint(v)))
		if s == "" || s == "<nil>" {
			continue
		}
		if len(s) > 48 {
			s = text.Cut(s, 47) + "…"
		}
		kv = append(kv, k+"="+s)
		if len(kv) >= 4 {
			break
		}
	}
	callPart := label
	if len(kv) > 0 {
		callPart += "(" + strings.Join(kv, ", ") + ")"
	}
	resPart := summarizeToolResult(out)
	line := "- " + callPart
	if resPart != "" {
		line += " -> " + resPart
	}
	if len(line) > maxToolLineBytes {
		line = text.Cut(line, maxToolLineBytes-1) + "…"
	}
	return line
}

func summarizeToolResult(out string) string {
	out = strings.TrimSpace(audit.RedactText(out))
	if out == "" {
		return "ok"
	}
	var parts []string
	var rm map[string]any
	if json.Unmarshal([]byte(out), &rm) == nil {
		for _, k := range []string{"identifier", "id", "sessionId", "number", "status", "count", "error"} {
			if v, ok := rm[k]; ok && v != nil {
				s := collapse(fmt.Sprint(v))
				if s != "" {
					parts = append(parts, k+"="+text.Cut(s, 48))
				}
			}
		}
		for _, k := range []string{"url", "html_url"} {
			if v, ok := rm[k].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, strings.TrimSpace(v))
				break
			}
		}
	}
	if u := toolURLRE.FindString(out); u != "" {
		hasURL := false
		for _, p := range parts {
			if strings.Contains(p, u) {
				hasURL = true
				break
			}
		}
		if !hasURL {
			parts = append(parts, u)
		}
	}
	if len(parts) > 0 {
		return text.Cut(strings.Join(parts, " "), 140)
	}
	first, _, _ := strings.Cut(out, "\n")
	return text.Cut(collapse(first), 120)
}

type liveKey struct{}

func withLive(ctx context.Context, l *live) context.Context {
	return context.WithValue(ctx, liveKey{}, l)
}

func liveFrom(ctx context.Context) *live {
	l, _ := ctx.Value(liveKey{}).(*live)
	return l
}

// markup is the shape of model text that is not prose meant for a person:
// JSON, a code fence, or a tool-call tag the model wrote instead of calling.
var markup = regexp.MustCompile("^\\s*[\\[{<]|```")

// said reports the model's own words before it acts — the plan, in the model's
// voice. The first paragraph only, and nothing that is not prose.
func (l *live) said(ctx context.Context, text string) {
	if l == nil || markup.MatchString(text) {
		return
	}
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n\n")
	l.report(ctx, first)
}

// doing reports that the run is using a tool, by name only. What it was asked
// and what it answered stay out of the public line.
func (l *live) doing(ctx context.Context, tool string) {
	if l == nil {
		return
	}
	l.report(ctx, toolLine(tool))
}

// toolName is a tool name safe to show: a plain identifier.
var toolName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// toolLine is the progress line for using a tool.
func toolLine(tool string) string {
	if line, ok := toolLines[tool]; ok {
		return line
	}
	if toolName.MatchString(tool) {
		return "Using " + strings.ReplaceAll(tool, "_", " ")
	}
	return "Working"
}

// toolLines names the tools a person asks about most, in their words.
var toolLines = map[string]string{
	"git":      "Looking at the repository",
	"agent":    "Starting a coding agent",
	"web":      "Searching the web",
	"fetch":    "Reading a page",
	"search":   "Searching",
	"todo":     "Updating the board",
	"ask_user": "Waiting for your answer",
}

// letGo lets the turn's hold on its session go, once: when the turn has rested
// (talk.go rest), or when its goroutine ends, whichever is first.
func (l *live) letGo() {
	if l.free != nil {
		l.free()
	}
}

// report appends one progress line and moves the session's progress with it.
func (l *live) report(ctx context.Context, line string) {
	if l.quiet {
		return
	}
	line = collapse(line)
	if line == "" {
		return
	}
	if len(line) > progressLine {
		line = text.Cut(line, progressLine-1) + "…"
	}
	l.mu.Lock()
	if line == l.last {
		l.mu.Unlock()
		return
	}
	l.last = line
	l.mu.Unlock()

	payload, _ := json.Marshal(map[string]string{"phase": phaseRunning, "activity": line})
	if guardEvent(string(payload)) != nil {
		return // a line that looks like a credential is not worth showing
	}
	e, ok := l.append(ctx, KindProgress, payload)
	if !ok {
		return
	}
	if sto, err := l.store(); err == nil {
		_ = sto.SetProgress(ctx, l.x.Org, l.x.ID, Progress{
			Pct: pctUnknown, Phase: phaseRunning, Activity: line, At: e.CreatedAt,
		})
	}
}

// store is the org's store as it is now; see live.
func (l *live) store() (*Store, error) { return l.s.State.storeFor(l.x.Org) }

// append records one event of the run on its session and publishes it.
func (l *live) append(ctx context.Context, kind string, payload []byte) (Event, bool) {
	sto, err := l.store()
	if err != nil {
		l.s.Log.Warn("agents: the turn's store is unreachable", "org", l.x.Org, "session", l.x.ID, "err", err)
		return Event{}, false
	}
	e, err := sto.AppendEvent(ctx, Event{
		ID: mint.ID("evt"), SessionID: l.x.ID, Org: l.x.Org, Kind: kind,
		Actor: l.x.Actor, Payload: string(payload), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		l.s.Log.Warn("agents: a turn's event was not recorded", "org", l.x.Org, "session", l.x.ID, "kind", kind, "err", err)
		return Event{}, false
	}
	publishEvent(l.s, l.x, e)
	return e, true
}

// finish records how the run ended. It writes on a context that survives the
// run's cancellation, because a stopped run's record still has to be written.
//
// The coding runs the turn started are part of its answer, whether or not the
// model's own reply succeeded: finish waits for them first (follow.go), on the
// turn's own context, so a stop ends the wait and not the record.
//
// A PAUSED run is recorded and not ended: its session stays live, paused, for a
// resume to pick up. The pause already said so on the row. A run that finished
// its answer before the pause reached it finished: the answer is kept.
func (l *live) finish(ctx context.Context, r Run, err error) {
	paused := errors.Is(context.Cause(ctx), ErrPaused) && (err != nil || r.Status != "ok")
	ok := err == nil && r.Status == "ok"
	answer := ""
	if ok {
		answer = r.Output
	}
	// A turn of the app's conversation ends when its answer does; the runs it
	// started are followed after it rests (talk.go follow).
	talking := talks(l.x)
	switch {
	case talking:
		answer = l.refusal(answer)
	case !paused:
		answer = l.conclude(ctx, answer)
	}
	ctx = context.WithoutCancel(ctx)
	why := r.Error
	if err != nil {
		why = err.Error()
	}
	l.log(ctx, r, why)
	if paused {
		return
	}
	tools := l.toolSummary()
	if text := strings.TrimSpace(answer); text != "" || tools != "" {
		msg := map[string]string{"text": text}
		if tools != "" {
			msg["tools"] = tools
		}
		payload, _ := json.Marshal(msg)
		l.append(ctx, KindMessage, payload)
	}
	// The app's conversation is not over when a turn is: it rests, waiting for the
	// person's next words (talk.go rest), whether this turn answered or failed, and
	// what it started is followed after.
	if talking {
		runs := l.started()
		for _, id := range runs {
			l.spawned(ctx, id, false)
		}
		l.rest(ctx)
		l.follow(runs)
		return
	}
	if ok {
		l.end(ctx, StatusDone)
		return
	}
	l.end(ctx, StatusError, why)
}

// log records the run on its session, as a run's session always has.
func (l *live) log(ctx context.Context, r Run, why string) {
	payload, _ := json.Marshal(map[string]any{
		"runId": l.run, "status": r.Status, "model": r.Model,
		"durationMs": r.DurationMs, "error": why,
	})
	l.append(ctx, KindLog, payload)
}

// keep records the sandbox the run works in on its session, so a resume finds it
// (box.go).
func (l *live) keep(box string) {
	sto, err := l.store()
	if err == nil {
		err = sto.SetBox(context.Background(), l.x.Org, l.x.ID, box)
	}
	if err != nil {
		l.s.Log.Warn("agents: the turn's sandbox was not recorded, so a resume starts afresh",
			"org", l.x.Org, "session", l.x.ID, "sandbox", box, "err", err)
	}
}

// end moves the session to status, unless something ended it first — a stop
// that landed while the run was finishing wins, and this is a no-op.
//
// A failure here is an ERROR, not a warning: it is the one write that says the
// turn is over, and a turn it misses reads as working until the reaper finds it.
func (l *live) end(ctx context.Context, status string, errStr ...string) {
	now := time.Now().Unix()
	sto, err := l.store()
	ended := false
	if err == nil {
		ended, err = sto.End(context.WithoutCancel(ctx), l.x.Org, l.x.ID, status, now, errStr...)
	}
	if err != nil {
		l.s.Log.Error("agents: a finished turn could not be ended", "org", l.x.Org, "session", l.x.ID, "status", status, "err", err)
		return
	}
	if !ended {
		return
	}
	x := l.x
	x.Status, x.EndedAt, x.UpdatedAt = status, now, now
	publishSession(l.s, x, 0, 0)
}

// planeTurnEvents answers what a turn has said after in.After, waiting up to
// turnWait for something new. The org is the caller's.
func planeTurnEvents(ctx context.Context, in *client.TurnEventsIn) (*client.TurnEvents, error) {
	if mounted == nil {
		return nil, fmt.Errorf("%w: agents", cloud.ErrNoPeer)
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agents: reading a turn needs the org on the call")
	}
	sto, err := mounted.State.storeFor(org)
	if err != nil {
		return nil, err
	}
	return waitTurn(ctx, mounted, sto, org, in.SessionID, in.After)
}

// waitTurn reads a turn, and if it has nothing new and is still running, waits
// for the bus to say something changed — or for turnWait to pass. A turn whose
// run is gone is ended first (reap.go orphaned), so the one waiting on it is told.
func waitTurn(ctx context.Context, s *cloud.Service[state], sto *Store, org, id string, after int64) (*client.TurnEvents, error) {
	if s != nil {
		s.State.orphaned(ctx, sto, org, id)
	}
	deadline := time.NewTimer(turnWait)
	defer deadline.Stop()
	var updates <-chan streamUpdate
	for {
		out, err := readTurn(ctx, sto, org, id, after)
		if err != nil {
			return nil, err
		}
		if len(out.Events) > 0 || out.Outcome != "" {
			return out, nil
		}
		if updates == nil {
			if s == nil || s.State.bus == nil {
				updates = tick()
			} else {
				ch, unsub := s.State.bus.subscribe(org)
				defer unsub()
				updates = ch
			}
			continue // read once more now that nothing can be missed
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-deadline.C:
			return out, nil
		case _, ok := <-updates:
			if !ok {
				updates = tick() // dropped by the bus; fall back to polling
			}
		}
	}
}

// tick is a slow stand-in for the bus.
func tick() <-chan streamUpdate {
	ch := make(chan streamUpdate, 1)
	time.AfterFunc(500*time.Millisecond, func() { ch <- streamUpdate{}; close(ch) })
	return ch
}

// turnPage is how many events one read returns.
const turnPage = 200

// readTurn is what a turn has said after `after`, and its outcome once it has
// said everything.
func readTurn(ctx context.Context, sto *Store, org, id string, after int64) (*client.TurnEvents, error) {
	x, err := sto.GetSession(ctx, org, id)
	if errors.Is(err, errSessionNotFound) {
		return nil, zip.ErrNotFound("turn not found")
	}
	if err != nil {
		return nil, err
	}
	evs, err := sto.ListEvents(ctx, org, id, after, turnPage)
	if err != nil {
		return nil, err
	}
	out := &client.TurnEvents{Events: []client.TurnEvent{}, Next: after}
	for _, e := range evs {
		out.Next = e.Seq
		switch e.Kind {
		case KindProgress:
			var p struct {
				Activity string `json:"activity"`
			}
			if json.Unmarshal([]byte(e.Payload), &p) == nil && p.Activity != "" {
				out.Events = append(out.Events, client.TurnEvent{Seq: e.Seq, Kind: client.TurnProgress, Text: p.Activity})
			}
		case KindMessage:
			var m struct {
				Role  string `json:"role"`
				Text  string `json:"text"`
				Tools string `json:"tools"`
			}
			// The person's own words (talk.go) are what was asked, not an answer.
			if json.Unmarshal([]byte(e.Payload), &m) == nil && m.Role != infer.RoleUser {
				if m.Text != "" {
					out.Events = append(out.Events, client.TurnEvent{Seq: e.Seq, Kind: client.TurnAnswer, Text: m.Text})
				}
				if m.Tools != "" {
					out.Tools = m.Tools
				}
			}
		case KindSpawn:
			// The coding run the turn is following (follow.go): its buttons are
			// the ones the turn's message carries, and its pause outlasts the turn.
			var r struct {
				Session string `json:"session"`
				Paused  bool   `json:"paused"`
			}
			if json.Unmarshal([]byte(e.Payload), &r) == nil && r.Session != "" {
				kind := client.TurnRun
				if r.Paused {
					kind = client.TurnRunPaused
				}
				out.Events = append(out.Events, client.TurnEvent{Seq: e.Seq, Kind: kind, Text: r.Session})
			}
		case KindLog:
			var r struct {
				Error string `json:"error"`
			}
			if json.Unmarshal([]byte(e.Payload), &r) == nil && r.Error != "" {
				out.Error = r.Error
			}
		case KindStatus:
			// A coding run says how it ended in its last status (apps/coding
			// finished): what its harness answered, or a plan's plan, or why it
			// failed. That is a coding session's answer, read here so a caller
			// following the run on this feed — an automation's agent step —
			// ends with it.
			var r struct {
				Status string `json:"status"`
				Answer string `json:"answer"`
				Plan   string `json:"plan"`
				Error  string `json:"error"`
			}
			if json.Unmarshal([]byte(e.Payload), &r) != nil {
				break
			}
			switch {
			case r.Status == "done" && cmp.Or(r.Answer, r.Plan) != "":
				out.Events = append(out.Events, client.TurnEvent{Seq: e.Seq, Kind: client.TurnAnswer, Text: cmp.Or(r.Answer, r.Plan)})
			case r.Status == "error" && r.Error != "":
				out.Error = r.Error
			}
		}
	}
	// Terminal only once every event has been read, so the outcome never
	// arrives ahead of the answer it concludes. A paused turn has stopped working
	// too, and a surface showing it says so rather than waiting on it.
	//
	// A PARKED run is neither: it has not stopped working, it waits for a refusal to
	// clear and goes on by itself. It answers no outcome, so whoever waits keeps
	// waiting, and says why it stands still and when it goes on.
	switch {
	case x.Parked():
		out.Park = &client.TurnPark{Reason: x.Park, ResumeAt: x.ParkAt}
	case (isTerminalStatus(x.Status) || x.Status == StatusPaused) && len(evs) < turnPage:
		out.Outcome = outcomeOf(x.Status)
		if out.Outcome != client.TurnFailed {
			out.Error = ""
		} else if out.Error == "" {
			out.Error = cmp.Or(x.Error, sto.endedWhy(ctx, org, id))
		}
	}
	return out, nil
}

// outcomeOf is a terminal status in the words a surface uses.
func outcomeOf(status string) string {
	switch status {
	case StatusDone:
		return client.TurnCompleted
	case StatusStopped:
		return client.TurnStopped
	case StatusPaused:
		return client.TurnPaused
	}
	return client.TurnFailed
}
