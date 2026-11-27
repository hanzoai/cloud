package transport

// live.go renders an agent turn into a chat, and nothing else.
//
// The turn itself runs in agents as a session (apps/agents/turn.go). This file
// starts it, reads what it says, and keeps ONE message in the conversation up to
// date: "Working…" is posted the moment the turn starts, every line of progress
// edits that message — the coding run the turn starts included, which agents
// follows into the same stream — and the answer replaces it. Stopping a turn is
// stopping its session, and "continue" in the same conversation resumes it — the
// same session, in the same sandbox, which agents kept for it. Every transport
// renders the same stream; one that cannot edit shows only the answer.
//
// While the turn works its message carries its controls — Stop and Pause, and
// Continue and Stop once it is paused (act.go) — and the answer carries none.
// Once it is following a coding run the buttons are that run's, because the run
// is the work being watched; a run that ends paused leaves its Continue and Stop.
// What its asker says in the conversation meanwhile is the turn's next input
// rather than a second turn beside it (steer).
//
// The run's lifetime belongs to agents. If this process goes away mid-turn the
// run still finishes and its session records how; only the rendering is lost.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// The agents ops a turn uses, as values so a test can stand in for agents.
var (
	startTurn      = plane.Call[client.RunOnBehalfIn, client.TurnStarted]
	readTurn       = plane.Call[client.TurnEventsIn, client.TurnEvents]
	haltTurn       = plane.Call[client.SessionMatchIn, client.SessionCount]
	controlTurn    = plane.Call[client.SessionControlIn, client.SessionControlled]
	steerCoding    = plane.Call[client.CodingSteerIn, client.CodingAck]
	answerQuestion = plane.Call[client.AnswerIn, client.Answered]
)

// DegenerateReason reports why an LLM answer is degenerate and should be
// withheld, or "" when the answer is usable. Private distributions wire their
// classifier here at plugin boot.
var DegenerateReason = func(text string, _ bool) string {
	if strings.Count(text, "<unk>") >= 3 || strings.Contains(text, "<|endoftext|>") || strings.Contains(text, "<|im_end|>") {
		return "special token degeneration"
	}
	return ""
}

// editEvery is the least time between two edits of a turn's message. Progress
// can arrive many times a second; Slack takes about one update a second per
// transport, and a person cannot read faster than that anyway.
var editEvery = 1500 * time.Millisecond

// readRetry is how long to wait before reading a turn again after a failed read,
// and readTries how many failed reads in a row end the rendering. The run is not
// affected either way.
var (
	readRetry = time.Second
	readTries = 10
)

const (
	// workingText is the turn's message before the agent has said anything. It is
	// posted at once, because a model's first words take seconds and an empty channel
	// reads as a bot that is not there — and it is the SAME message every later
	// line edits, so there is never a status beside the answer to skip past.
	workingText   = "Working…"
	stoppedText   = "Stopped."
	pausedText    = "Paused."
	nothingToStop = "Nothing of yours is running here."
	// heardText is the turn's message the moment its asker says something into
	// it: one line, in the message they are already watching, rather than a reply
	// of its own.
	heardText = "Got it — working that in…"
)

// streamTurn starts the turn in agents and renders it until it ends.
func streamTurn(ctx context.Context, s *cloud.Service[state], tr transport, org string, m, reply Message, who *client.ChatIdentityOut, span trace.Span) {
	st, err := startTurn(ctx, "agent", client.AgentsTurnStart, &client.RunOnBehalfIn{
		Org: org, Subject: who.Subject, Ref: agentFor(ctx, s.State.store, org, m),
		Input: m.Text, Model: who.Model, History: turnHistory(ctx, s, org, m),
		// Where a coding run this turn starts should narrate, and the message a
		// fact filed in this turn cites: facts only this bridge holds.
		Reply:  replyOf(m, reply),
		Origin: client.Origin{Account: m.Account, Message: m.ID, Public: m.Channel.Public},
		// "continue" asks for the asker's stopped or paused turn in this
		// conversation back; agents answers with a new turn when there is none.
		Resume: isResume(m.Text),
	})
	if err != nil || st == nil {
		if err == nil {
			err = errors.New("agents started no turn")
		}
		s.Log.Error("transport: start turn", "transport", m.Transport, "org", org, "err", err)
		fail(span, err)
		say(ctx, s, tr, org, reply, notStarted())
		return
	}
	span.SetAttributes(
		attribute.String("hanzo.agent.run_id", st.RunID),
		attribute.String("hanzo.agent.session_id", st.SessionID),
	)

	r := rendering{
		org: org, session: st.SessionID, run: st.RunID, subject: who.Subject,
		owner: m.Sender.ExternalID, reply: reply, started: time.Now().Unix(),
	}
	// Written BEFORE the first read, so there is no moment a turn is running with
	// nothing on record to find its message by.
	s.State.store.openTurn(ctx, s, r)
	render(ctx, s, tr, r, span)
}

// render shows one turn in its one message until the turn ends. It is the WHOLE
// of rendering, for a turn this process started and for one it inherited
// (resume.go) alike — a second loop for the inherited case is how the two would
// come to disagree about what a channel is shown.
//
// r.message is the message a previous process already posted, or "" when there
// is none yet. Reading starts from the beginning either way: agents keeps every
// event, progress is coalesced to the newest line, and starting anywhere later
// could skip the ANSWER a previous reader had already consumed — which would
// leave an inherited turn concluding with nothing to say.
func render(ctx context.Context, s *cloud.Service[state], tr transport, r rendering, span trace.Span) {
	v := &view{s: s, tr: tr, org: r.org, reply: r.reply, id: r.message,
		ctl: client.Controls{Session: r.session, Kind: client.ControlsTurn, Owner: r.owner}}
	v.posted = func(id string) {
		s.State.store.markTurn(context.WithoutCancel(ctx), s, r.org, r.session, id)
	}
	key := conversation(r.org, r.reply)
	track(key, active{session: r.session, subject: r.subject, view: v})
	defer untrack(key, r.session)
	// The row goes on EVERY way out of here, because every way out has shown the
	// channel a conclusion. The one exit that leaves it is the process dying, which
	// is exactly the exit the row exists for.
	defer s.State.store.closeTurn(context.WithoutCancel(ctx), s, r.org, r.session)
	// The row is renewed while the turn is shown, which is what tells another process
	// this one is alive. If another takes it all the same (this one stalled for longer
	// than a lease), the turn is that process's to show: this one stops, saying nothing
	// and forgetting nothing.
	hold := s.State.store.keep(s, r.org, r.session)
	defer hold.stop()

	if r.message == "" {
		v.progress(ctx, workingText)
	}
	answer, tools, after, misses := "", "", int64(0), 0
	for {
		if hold.lost() {
			return
		}
		ev, err := readTurn(ctx, "agent", client.AgentsTurnEvents,
			&client.TurnEventsIn{SessionID: r.session, After: after})
		if err != nil || ev == nil {
			misses++
			// ErrNoPeer is not about the moment: agents is not part of this
			// deployment, and no wait will deploy it.
			if ctx.Err() != nil || misses >= readTries || errors.Is(err, plane.ErrNoPeer) {
				if err == nil {
					err = errors.New("agents answered nothing")
				}
				s.Log.Error("transport: read turn", "org", r.org, "session", r.session, "err", err)
				fail(span, err)
				if hold.lost() {
					return
				}
				v.final(context.WithoutCancel(ctx), lostTrack(r.run), client.Controls{})
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(readRetry):
			}
			continue
		}
		misses = 0
		for _, e := range ev.Events {
			switch e.Kind {
			case client.TurnProgress:
				v.progress(ctx, e.Text+"…")
			case client.TurnAnswer:
				answer = e.Text
			case client.TurnRun:
				v.follow(ctx, e.Text, false)
			case client.TurnRunPaused:
				v.follow(ctx, e.Text, true)
			}
		}
		if ev.Tools != "" {
			tools = ev.Tools
		}
		after = ev.Next
		if ev.Outcome == "" {
			continue
		}
		if hold.lost() {
			return
		}
		v.final(ctx, conclusion(ev, answer, r.run), v.after(ev.Outcome))
		if a := strings.TrimSpace(answer); s != nil && s.State.store != nil && (a != "" || strings.TrimSpace(tools) != "") && ev.Outcome != client.TurnStopped && ev.Outcome != client.TurnPaused && DegenerateReason(a, false) == "" {
			if ierr := s.State.store.insertInbox(context.WithoutCancel(ctx), inboxRow{
				Org:         r.org,
				Transport:   r.reply.Transport,
				Account:     r.reply.Account,
				ChannelID:   r.reply.Channel.ID,
				ChannelKind: r.reply.Channel.Kind,
				Sender:      "bot",
				SenderUser:  "assistant",
				Text:        withToolSummary(a, tools),
				ReplyTo:     r.reply.ReplyTo,
				EventKey:    "reply:" + r.run,
				CreatedAt:   time.Now().Unix(),
			}); ierr != nil {
				s.Log.Warn("transport: reply inbox insert", "transport", r.reply.Transport, "err", ierr)
			}
		}
		if ev.Outcome == client.TurnFailed {
			s.Log.Warn("transport: run failed", "org", r.org, "run", r.run, "err", ev.Error)
			fail(span, errString(ev.Error))
		}
		if why := DegenerateReason(answer, false); why != "" {
			fail(span, errString("unusable answer withheld: "+why))
		}
		return
	}
}

// conclusion is what the turn's message says once the turn is over. An answer
// the model degenerated into (zen/degenerate — the predicate zen fails an arm
// on) is never posted: the channel is told so, with the run id that finds it.
func conclusion(ev *client.TurnEvents, answer, runID string) string {
	usable := DegenerateReason(answer, false) == ""
	switch ev.Outcome {
	case client.TurnStopped:
		return stoppedText
	case client.TurnPaused:
		return pausedText
	case client.TurnFailed:
		// A turn that failed can still have something to say — the outcome of a
		// coding run it started before its model gave out — and it is said after
		// the failure, in the same message.
		if a := strings.TrimSpace(answer); a != "" && usable {
			return runFailure(runID) + "\n\n" + a
		}
		return runFailure(runID)
	}
	switch {
	case strings.TrimSpace(answer) == "":
		return emptyOutput
	case !usable:
		return unusable(runID)
	}
	return answer
}

func fail(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

type errString string

func (e errString) Error() string { return string(e) }

// view is a turn's one message.
type view struct {
	s     *cloud.Service[state]
	tr    transport
	org   string
	reply Message
	// ctl are the controls the message carries while the turn works; run are the
	// coding run's, once the turn is following one; parked a followed run's that
	// ended paused, which the conclusion keeps.
	ctl, run, parked client.Controls

	// posted is told the message's id the first time there is one, so it can be
	// found again by a process that did not post it. Optional.
	posted func(id string)

	mu    sync.Mutex
	id    string    // the message, once posted
	shown string    // what it says
	want  string    // the newest progress line
	at    time.Time // when it last changed
	timer *time.Timer
	done  bool
}

// progress shows a progress line: at once if nothing is posted yet, otherwise
// no sooner than editEvery after the last edit, and only the newest line — the
// ones in between were already out of date.
func (v *view) progress(ctx context.Context, line string) {
	if !v.tr.caps.Edit {
		return // one message: a transport that cannot edit waits for the answer
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.done {
		return
	}
	v.want = line
	if v.id == "" {
		v.show(ctx)
		return
	}
	if wait := editEvery - time.Since(v.at); wait > 0 {
		if v.timer == nil {
			v.timer = time.AfterFunc(wait, func() {
				v.mu.Lock()
				defer v.mu.Unlock()
				v.timer = nil
				if !v.done {
					v.show(ctx)
				}
			})
		}
		return
	}
	v.show(ctx)
}

// show sends the newest line. The caller holds mu, so edits never overlap.
func (v *view) show(ctx context.Context) {
	if v.want == v.shown {
		return
	}
	out := v.reply
	out.Text, out.Edit, out.Controls = v.want, v.id, v.ctl
	if v.run.Session != "" {
		out.Controls = v.run
	}
	d, err := v.tr.send(ctx, v.s, v.org, out)
	if err != nil {
		v.s.Log.Warn("transport: progress", "transport", v.reply.Transport, "err", err)
		return
	}
	if v.id == "" && d.MessageID != "" {
		v.id = d.MessageID
		if v.posted != nil {
			v.posted(v.id)
		}
	}
	v.shown, v.at = v.want, time.Now()
}

// follow puts the coding run the turn is following on the message's buttons:
// Stop and Pause act on that run while it works, which is the work the person is
// watching. The message is edited at once, outside the progress throttle — it
// happens once per run, and a press must never reach a run that is no longer the
// one being shown. A run that paused keeps its Continue and Stop past the turn's
// end.
func (v *view) follow(ctx context.Context, session string, paused bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c := client.Controls{Session: session, Kind: client.ControlsCoding, Owner: v.ctl.Owner}
	if paused {
		c.Paused = true
		v.parked = c
		return
	}
	v.run = c
	if v.id != "" && !v.done && v.tr.caps.Edit {
		v.shown = ""
		v.show(ctx)
	}
}

// after is the controls a turn's conclusion carries: Continue and Stop for a
// paused turn, or for the paused run it was following, and none for one that is
// over.
func (v *view) after(outcome string) client.Controls {
	v.mu.Lock()
	defer v.mu.Unlock()
	switch {
	case outcome == client.TurnPaused:
		c := v.ctl
		c.Paused = true
		return c
	case outcome != client.TurnStopped && v.parked.Session != "":
		return v.parked
	}
	return client.Controls{}
}

// final replaces whatever the message shows with the turn's conclusion. It is
// never throttled, and it always lands: if the edit fails the conclusion is
// posted on its own, because a turn owes its answer.
func (v *view) final(ctx context.Context, text string, ctl client.Controls) {
	v.mu.Lock()
	v.done = true
	if v.timer != nil {
		v.timer.Stop()
		v.timer = nil
	}
	id := v.id
	v.mu.Unlock()
	out := v.reply
	out.Controls = ctl
	if id != "" {
		out.Text, out.Edit = text, id
		if _, err := v.tr.send(ctx, v.s, v.org, out); err == nil {
			return
		}
		out.Edit = ""
	}
	say(ctx, v.s, v.tr, v.org, out, text)
}

// active is a turn this process is rendering.
type active struct {
	session string
	subject string // who asked, which is who may stop it
	view    *view  // its message, where what it is told is acknowledged
}

// turns maps a conversation to the turns rendering in it, so a stop said there
// can find them. An entry lives exactly as long as its turn is rendered.
var turns = struct {
	sync.Mutex
	m map[string][]active
}{m: map[string][]active{}}

// conversation keys where a turn answers; channelKey is the prefix a channel-wide stop
// matches on.
func conversation(org string, reply Message) string {
	return channelKey(org, reply) + "/" + reply.ReplyTo
}

func channelKey(org string, reply Message) string {
	return org + "/" + reply.Transport + "/" + reply.Account + "/" + reply.Channel.ID
}

func track(key string, a active) {
	turns.Lock()
	turns.m[key] = append(turns.m[key], a)
	turns.Unlock()
}

func untrack(key, session string) {
	turns.Lock()
	defer turns.Unlock()
	list := turns.m[key]
	for i, a := range list {
		if a.session == session {
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(turns.m, key)
	} else {
		turns.m[key] = list
	}
}

// running lists the turns subject is running where it may stop them: in the
// same conversation, or anywhere in the channel when channelWide.
func running(key, prefix, subject string, channelWide bool) []active {
	turns.Lock()
	defer turns.Unlock()
	var out []active
	for k, list := range turns.m {
		if k != key && !(channelWide && strings.HasPrefix(k, prefix+"/")) {
			continue
		}
		for _, a := range list {
			if a.subject == subject {
				out = append(out, a)
			}
		}
	}
	return out
}

// isStop reports whether a message asks to stop the turn in progress.
func isStop(text string) bool {
	return strings.EqualFold(strings.Trim(text, " \t\n.!"), "stop")
}

// isResume reports whether a message asks to pick up the turn that was stopped
// or paused — read the way isStop reads "stop": the word, and nothing else.
func isResume(text string) bool {
	w := strings.Trim(text, " \t\n.!")
	return strings.EqualFold(w, "continue") || strings.EqualFold(w, "resume")
}

// halt stops the given turns as subject, and reports how many it stopped.
// Agents checks each one is subject's own, so naming another's stops nothing.
// The org rides the caller, as it does for every stop.
func halt(ctx context.Context, s *cloud.Service[state], subject string, turns []active) int {
	n := 0
	for _, a := range turns {
		out, err := haltTurn(ctx, "agent", client.AgentsSessionsStop,
			&client.SessionMatchIn{Subject: subject, SessionID: a.session})
		if err != nil {
			s.Log.Warn("transport: stop", "session", a.session, "err", err)
			continue
		}
		if out != nil {
			n += out.Count
		}
	}
	return n
}

// steer hands a message to what its sender already has running in this
// conversation, and reports whether anything took it.
//
// A running TURN takes it as its next input: agents hands it to the model at the
// next step, and the turn's own message says it was heard. With no turn, a
// running CODING RUN in the same thread takes it as a new instruction: the run is
// stopped with its work kept on its branch and followed up from there
// (AgentsCodingSteer), and its message says so. A turn that has already answered
// takes nothing more (409), and neither does a failure — so a message nothing
// took becomes a turn of its own, never nothing.
//
// Only a THREAD redirects a coding run. A run lasts many minutes, and what its
// person says next at the top of a channel or in a DM is as likely to be about
// something else; "continue" is not an instruction at all.
func steer(ctx context.Context, s *cloud.Service[state], org string, m, reply Message, who *client.ChatIdentityOut) bool {
	for _, a := range running(conversation(org, reply), channelKey(org, reply), who.Subject, false) {
		if _, err := controlTurn(ctx, "agent", client.AgentsSessionControl, &client.SessionControlIn{
			Subject: who.Subject, SessionID: a.session, Command: client.SessionMessage, Message: m.Text,
		}); err != nil {
			s.Log.Info("transport: a running turn did not take a message; it starts a new one",
				"org", org, "session", a.session, "err", err)
			continue
		}
		a.view.progress(ctx, heardText)
		return true
	}
	if reply.ReplyTo == "" || isResume(m.Text) {
		return false
	}
	_, err := steerCoding(ctx, "agent", client.AgentsCodingSteer, &client.CodingSteerIn{
		Subject: who.Subject, Reply: replyOf(m, reply), Text: m.Text,
	})
	return err == nil
}

// answers hands a message to the question its sender has open in this
// conversation (apps/agents/ask.go), and reports whether one took it. A run that
// asked is waiting on exactly this, so an answer is neither a steer nor a turn:
// what the person says while their question is open IS the answer.
func answers(ctx context.Context, org string, m, reply Message, who *client.ChatIdentityOut) bool {
	_, err := answerQuestion(ctx, "agent", client.AgentsQuestionAnswer, &client.AnswerIn{
		Subject: who.Subject, Reply: replyOf(m, reply), Text: m.Text, By: m.Sender.ExternalID,
	})
	return err == nil
}

// replyOf is the conversation a turn answers in, as the address a run it starts
// narrates at — and whose run it is.
func replyOf(m, reply Message) client.Reply {
	return client.Reply{Provider: m.Transport, Channel: m.Channel.ID, Thread: reply.ReplyTo, User: m.Sender.ExternalID}
}

// stopHere answers a "stop" said in a conversation: it stops the asker's turn
// there. The turn's own message becomes "Stopped." when its stream ends, so
// there is nothing more to say unless there was nothing to stop.
func stopHere(ctx context.Context, s *cloud.Service[state], tr transport, org string, reply Message, who *client.ChatIdentityOut) {
	key := conversation(org, reply)
	sessions := running(key, channelKey(org, reply), who.Subject, false)
	if len(sessions) == 0 {
		// A stop said at the top of a channel reaches the asker's turns in its
		// threads too; one said in a thread reaches only that thread.
		if reply.ReplyTo == "" {
			sessions = running(key, channelKey(org, reply), who.Subject, true)
		}
	}
	if halt(ctx, s, who.Subject, sessions) == 0 {
		say(ctx, s, tr, org, reply, nothingToStop)
	}
}

// exposeStop publishes the stop a slash command reaches. Mount calls it.
func exposeStop(s *cloud.Service[state]) {
	cloud.Plane().Post("/transport/stop", func(ctx context.Context, in *client.TransportStopIn) (*client.SessionCount, error) {
		org := cloud.Who(ctx).Org
		if org == "" {
			return nil, zip.ErrForbidden("transport: a stop needs the org on the call")
		}
		// WHO IS ASKING, resolved here rather than taken from the wire: a caller
		// that could name the subject could stop somebody else's turn.
		who, err := whoIsAsking(ctx, s, org, Message{
			Transport: in.Provider, Account: in.Account, Sender: Sender{ExternalID: in.User},
		})
		if err != nil {
			return nil, err
		}
		if who.Subject == "" {
			return &client.SessionCount{}, nil // unlinked: nothing of theirs is running
		}
		reply := Message{Transport: in.Provider, Account: in.Account, Channel: Channel{ID: in.Channel}}
		key := conversation(org, reply)
		n := halt(ctx, s, who.Subject, running(key, channelKey(org, reply), who.Subject, true))
		return &client.SessionCount{Count: n}, nil
	}, zip.WithOperationID(client.TransportStop),
		zip.WithSummary("Stop the turns the asker has running in one channel"))
}
