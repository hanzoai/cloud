package transport

// turn_async_test.go pins what a channel sees of a turn: one message, progress in it
// as soon as the agent says anything, and the answer in its place at the end —
// in the thread the question was asked in.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// said is one send: a new message, or an edit of one, and the buttons it carries.
type said struct {
	text, channel, replyTo, edit, id string
	ctl                              client.Controls
}

// heard records what a turn sent, in order.
type heard struct {
	mu   sync.Mutex
	msgs []said
	n    int
}

func (h *heard) add(m Message) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := m.Edit
	if id == "" {
		h.n++
		id = fmt.Sprintf("m%d", h.n)
	}
	h.msgs = append(h.msgs, said{text: m.Text, channel: m.Channel.ID, replyTo: m.ReplyTo, edit: m.Edit, id: id, ctl: m.Controls})
	return id
}

func (h *heard) all() []said {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]said(nil), h.msgs...)
}

// posts counts the messages a person sees: sends that were not edits.
func (h *heard) posts() int {
	n := 0
	for _, s := range h.all() {
		if s.edit == "" {
			n++
		}
	}
	return n
}

// last is what the channel shows at the end.
func (h *heard) last() said {
	all := h.all()
	return all[len(all)-1]
}

// listening is a transport that records instead of reaching a platform.
func listening(h *heard, edit bool) transport {
	return transport{
		id:        "slack",
		caps:      capabilities{DM: true, Group: true, Thread: true, Edit: edit},
		normalize: func(client.TransportIngestIn) (Message, bool) { return Message{}, false },
		send: func(_ context.Context, _ *cloud.Service[state], _ string, m Message) (Delivery, error) {
			return Delivery{MessageID: h.add(m)}, nil
		},
	}
}

// linked answers the identity lookup with a linked subject.
func linked(t *testing.T) {
	t.Helper()
	prev := identify
	identify = func(context.Context, string, string, *client.ChatIdentityIn) (*client.ChatIdentityOut, error) {
		return &client.ChatIdentityOut{Subject: "u-acme"}, nil
	}
	t.Cleanup(func() { identify = prev })
}

// agent stands in for agents: each read returns the next scripted page, and once
// the script runs out it answers "nothing new" until the turn is stopped.
type agent struct {
	mu      sync.Mutex
	pages   []*client.TurnEvents
	read    int
	halted  []client.SessionMatchIn
	start   error
	started []client.RunOnBehalfIn
	// controlled is every steering command a surface sent, and coded every
	// instruction for a coding run; coding answers whether one was running.
	controlled []client.SessionControlIn
	coded      []client.CodingSteerIn
	coding     bool
	// owner is whose runs these are; a command naming anyone else is refused as
	// agents refuses it. was is the status a command finds, running by default.
	owner, was string
	// asking is a question open in the conversation, and answered every answer
	// agents was given.
	asking   bool
	answered []client.AnswerIn
}

func (a *agent) install(t *testing.T) {
	t.Helper()
	ps, pr, ph := startTurn, readTurn, haltTurn
	pc, psc, pa := controlTurn, steerCoding, answerQuestion
	answerQuestion = func(_ context.Context, _, _ string, in *client.AnswerIn) (*client.Answered, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.owner != "" && in.Subject != a.owner {
			return nil, zip.ErrForbidden("only the person this question is for can answer it")
		}
		if in.Question == "" && !a.asking {
			return nil, zip.ErrNotFound("no question of yours is open there")
		}
		a.answered = append(a.answered, *in)
		return &client.Answered{Question: in.Question, Text: in.Text}, nil
	}
	pe, prr := editEvery, readRetry
	controlTurn = func(_ context.Context, _, _ string, in *client.SessionControlIn) (*client.SessionControlled, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.owner != "" && in.Subject != a.owner {
			return nil, zip.ErrForbidden("only the person whose run this is can steer it")
		}
		a.controlled = append(a.controlled, *in)
		return &client.SessionControlled{Was: cmp.Or(a.was, "running"), Run: "run_2"}, nil
	}
	steerCoding = func(_ context.Context, _, _ string, in *client.CodingSteerIn) (*client.CodingAck, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.coded = append(a.coded, *in)
		if !a.coding {
			return nil, zip.ErrNotFound("nothing of yours is running there")
		}
		return &client.CodingAck{OK: true}, nil
	}
	editEvery, readRetry = 40*time.Millisecond, time.Millisecond
	startTurn = func(_ context.Context, _, _ string, in *client.RunOnBehalfIn) (*client.TurnStarted, error) {
		a.mu.Lock()
		a.started = append(a.started, *in)
		a.mu.Unlock()
		if a.start != nil {
			return nil, a.start
		}
		return &client.TurnStarted{SessionID: "sess_1", RunID: "run_1"}, nil
	}
	readTurn = func(ctx context.Context, _, _ string, in *client.TurnEventsIn) (*client.TurnEvents, error) {
		a.mu.Lock()
		if len(a.halted) > 0 {
			a.mu.Unlock()
			return &client.TurnEvents{Next: in.After, Outcome: client.TurnStopped}, nil
		}
		if a.read < len(a.pages) {
			p := a.pages[a.read]
			a.read++
			a.mu.Unlock()
			if p == nil {
				return nil, errors.New("agents unreachable")
			}
			return p, nil
		}
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		return &client.TurnEvents{Next: in.After}, nil
	}
	haltTurn = func(_ context.Context, _, _ string, in *client.SessionMatchIn) (*client.SessionCount, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.halted = append(a.halted, *in)
		return &client.SessionCount{Count: 1}, nil
	}
	t.Cleanup(func() {
		startTurn, readTurn, haltTurn = ps, pr, ph
		controlTurn, steerCoding, answerQuestion = pc, psc, pa
		editEvery, readRetry = pe, prr
	})
}

func progress(seq int64, text string) client.TurnEvent {
	return client.TurnEvent{Seq: seq, Kind: client.TurnProgress, Text: text}
}

func asked() Message {
	return Message{
		Transport: "slack",
		Account:   "t0fake",
		Channel:   Channel{ID: "C0FAKE", Kind: ChannelThread},
		ReplyTo:   "1699999999.000100",
		Sender:    Sender{ExternalID: "U0FAKE", Org: "acme"},
		Text:      "what broke last night",
	}
}

func service() *cloud.Service[state] {
	return &cloud.Service[state]{Log: luxlog.New("test"), State: state{}}
}

// "Working…" is posted the moment the turn starts, what the agent says edits it,
// and the answer replaces it in the same message. One message a person sees, in
// the thread the question was in.
func TestATurnIsOneMessageThatBecomesTheAnswer(t *testing.T) {
	recordTurns(t)
	linked(t)
	a := &agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{progress(1, "Looking at the repository")}, Next: 1},
		{Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "the database ran out of connections"}}, Next: 2, Outcome: client.TurnCompleted},
	}}
	a.install(t)

	h := &heard{}
	m := asked()
	turn(service(), listening(h, true), "acme", m)

	all := h.all()
	if h.posts() != 1 {
		t.Fatalf("a turn must be one message, saw %d posts: %+v", h.posts(), all)
	}
	if all[0].text != workingText || all[0].edit != "" {
		t.Fatalf("the first send must post %q before the agent says anything, got %+v", workingText, all[0])
	}
	for _, later := range all[1:] {
		if later.edit != all[0].id {
			t.Fatalf("every later send must edit the one message, got %+v", all)
		}
	}
	end := h.last()
	if end.text != "the database ran out of connections" || end.edit != all[0].id {
		t.Fatalf("the answer must replace the progress message, got %+v", end)
	}
	for i, s := range all {
		if s.channel != m.Channel.ID || s.replyTo != m.ReplyTo {
			t.Fatalf("send %d went to %s/%s, want %s/%s", i, s.channel, s.replyTo, m.Channel.ID, m.ReplyTo)
		}
	}
}

// A turn states the message it answers and whether its channel is public, so a fact
// filed in it cites that message and is filed only from a public channel.
func TestATurnStatesTheMessageItAnswers(t *testing.T) {
	recordTurns(t)
	linked(t)
	for _, public := range []bool{true, false} {
		a := &agent{pages: []*client.TurnEvents{
			{Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: "noted"}}, Next: 1, Outcome: client.TurnCompleted},
		}}
		a.install(t)
		m := asked()
		m.ID, m.Channel.Public = "1699999999.000200", public
		turn(service(), listening(&heard{}, true), "acme", m)
		if len(a.started) != 1 {
			t.Fatalf("want one turn started, got %d", len(a.started))
		}
		want := client.Origin{Account: "t0fake", Message: m.ID, Public: public}
		if got := a.started[0].Origin; got != want {
			t.Fatalf("the turn states %+v, want %+v", got, want)
		}
	}
}

// A turn that says nothing before answering is still one message: "Working…",
// then the answer in its place.
func TestAQuietTurnIsStillOneMessage(t *testing.T) {
	recordTurns(t)
	linked(t)
	(&agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: "nothing broke"}}, Next: 1, Outcome: client.TurnCompleted},
	}}).install(t)

	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	all := h.all()
	if h.posts() != 1 || all[0].text != workingText {
		t.Fatalf("want one post of %q, got %+v", workingText, all)
	}
	if end := h.last(); end.text != "nothing broke" || end.edit != all[0].id {
		t.Fatalf("the answer must replace %q, got %+v", workingText, all)
	}
}

// Progress that arrives faster than a channel can read is coalesced: the newest
// line wins and the ones in between are never sent.
func TestProgressIsCoalesced(t *testing.T) {
	recordTurns(t)
	linked(t)
	burst := make([]client.TurnEvent, 0, 20)
	for i := range 20 {
		burst = append(burst, progress(int64(i+1), fmt.Sprintf("step %d", i)))
	}
	(&agent{pages: []*client.TurnEvents{
		{Events: burst, Next: 20},
		{Events: []client.TurnEvent{{Seq: 21, Kind: client.TurnAnswer, Text: "done"}}, Next: 21, Outcome: client.TurnCompleted},
	}}).install(t)

	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	all := h.all()
	if len(all) > 3 {
		t.Fatalf("20 progress lines produced %d sends; they were not coalesced: %+v", len(all), all)
	}
	if h.posts() != 1 || h.last().text != "done" {
		t.Fatalf("want one message ending in the answer, got %+v", all)
	}
}

// A transport that cannot edit shows no progress: its one message is the answer.
func TestATransportThatCannotEditShowsOnlyTheAnswer(t *testing.T) {
	recordTurns(t)
	linked(t)
	(&agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{progress(1, "Working")}, Next: 1},
		{Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "the answer"}}, Next: 2, Outcome: client.TurnCompleted},
	}}).install(t)

	h := &heard{}
	turn(service(), listening(h, false), "acme", asked())
	if all := h.all(); len(all) != 1 || all[0].text != "the answer" {
		t.Fatalf("want exactly the answer, got %+v", all)
	}
}

// A failed turn says so in Hanzo's words and names the run, in the message it
// already had. The run's own error — model names, upstream addresses and codes —
// never reaches the channel.
func TestAFailedTurnPostsOnlyAHanzoReason(t *testing.T) {
	recordTurns(t)
	linked(t)
	const why = `cloud: chat completion (model "qwen3-235b-a22b:free"): Post "http://127.0.0.1:8000/v1/chat/completions": upstream 429 from openrouter.ai: context deadline exceeded`
	(&agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{progress(1, "Thinking")}, Next: 1},
		{Next: 2, Outcome: client.TurnFailed, Error: why},
	}}).install(t)

	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	end := h.last()
	if h.posts() != 1 {
		t.Fatalf("a failure must stay one message, saw %d", h.posts())
	}
	if end.text != runFailure("run_1") || !strings.Contains(end.text, "run_1") {
		t.Fatalf("the failure must be Hanzo's sentence with the run, got %q", end.text)
	}
	for _, s := range h.all() {
		for _, leak := range []string{"http://", "https://", "127.0.0.1", "qwen", "openrouter", "429", "chat completion", "deadline", "model"} {
			if strings.Contains(strings.ToLower(s.text), leak) {
				t.Fatalf("a posted failure carries %q: %q", leak, s.text)
			}
		}
	}
}

// An answer the model degenerated into is never posted: the channel is told the
// model returned an unusable answer, with the run that finds it. A failed turn's
// garbage is dropped the same way, and its failure line stands alone.
func TestADegenerateAnswerIsNeverPosted(t *testing.T) {
	garbage := "We have a user asking: what broke? The context says we are Enso. " + strings.Repeat("<unk>", 2000)
	for _, outcome := range []string{client.TurnCompleted, client.TurnFailed} {
		recordTurns(t)
		linked(t)
		(&agent{pages: []*client.TurnEvents{
			{Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: garbage}}, Next: 1, Outcome: outcome, Error: "model gave out"},
		}}).install(t)

		h := &heard{}
		turn(service(), listening(h, true), "acme", asked())
		for _, s := range h.all() {
			if strings.Contains(s.text, "<unk>") {
				t.Fatalf("%s: a degenerate answer reached the channel: %.80q", outcome, s.text)
			}
		}
		end := h.last().text
		want := unusable("run_1")
		if outcome == client.TurnFailed {
			want = runFailure("run_1")
		}
		if end != want {
			t.Fatalf("%s: the turn ended %q, want %q", outcome, end, want)
		}
	}
}

// "stop" in the conversation stops the asker's turn there: the stop reaches
// agents for exactly that session as that person, the turn's message becomes
// "Stopped.", and nothing is left registered.
func TestStopInTheThreadStopsTheTurn(t *testing.T) {
	recordTurns(t)
	linked(t)
	a := &agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{progress(1, "Cloning the repository")}, Next: 1},
	}}
	a.install(t)

	h := &heard{}
	tr := listening(h, true)
	done := make(chan struct{})
	go func() { turn(service(), tr, "acme", asked()); close(done) }()

	waitFor(t, func() bool { return h.posts() == 1 })
	stop := asked()
	stop.Text = "stop"
	turn(service(), tr, "acme", stop)
	<-done

	if len(a.halted) != 1 || a.halted[0].SessionID != "sess_1" || a.halted[0].Subject != "u-acme" {
		t.Fatalf("the stop must name the session as its asker, got %+v", a.halted)
	}
	if end := h.last(); end.text != stoppedText || end.edit == "" {
		t.Fatalf("the turn's message must become %q, got %+v", stoppedText, end)
	}
	if h.posts() != 1 {
		t.Fatalf("stopping must not add a message, saw %d posts", h.posts())
	}
	assertNothingTracked(t)
}

// A stop with nothing running says so, rather than going silent.
func TestAStopWithNothingRunningSaysSo(t *testing.T) {
	recordTurns(t)
	linked(t)
	(&agent{}).install(t)
	h := &heard{}
	m := asked()
	m.Text = "stop"
	turn(service(), listening(h, true), "acme", m)
	if all := h.all(); len(all) != 1 || all[0].text != nothingToStop {
		t.Fatalf("want %q, got %+v", nothingToStop, all)
	}
}

// A turn that cannot start says so once, in Hanzo's words, and registers nothing.
// Why stays in the log.
func TestATurnThatCannotStartSaysSo(t *testing.T) {
	recordTurns(t)
	linked(t)
	(&agent{start: errors.New("agents down")}).install(t)
	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	if all := h.all(); len(all) != 1 || all[0].text != notStarted() ||
		strings.Contains(all[0].text, "agents down") {
		t.Fatalf("want one Hanzo-level message and no internal reason, got %+v", all)
	}
	assertNothingTracked(t)
}

// Losing agents mid-turn ends the rendering with one honest line in the same
// message, and the registry is empty afterwards.
func TestLosingAgentsMidTurnEndsCleanly(t *testing.T) {
	recordTurns(t)
	linked(t)
	pages := []*client.TurnEvents{{Events: []client.TurnEvent{progress(1, "Working")}, Next: 1}}
	for range readTries {
		pages = append(pages, nil)
	}
	(&agent{pages: pages}).install(t)
	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	if end := h.last(); h.posts() != 1 || end.text != lostTrack("run_1") ||
		strings.Contains(end.text, "agents unreachable") || end.edit == "" {
		t.Fatalf("want one message ending in Hanzo's line and the run, got %+v", h.all())
	}
	assertNothingTracked(t)
}

// A failed turn that started a coding run still reports the run: its outcome
// follows the failure in the same message.
func TestAFailedTurnStillReportsItsRun(t *testing.T) {
	recordTurns(t)
	linked(t)
	(&agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: "Opened a pull request for `agent/abc`: https://git.example/pulls/7"}},
			Next: 1, Outcome: client.TurnFailed, Error: "model timed out"},
	}}).install(t)

	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	end := h.last()
	if h.posts() != 1 || !strings.HasPrefix(end.text, runFailure("run_1")) || strings.Contains(end.text, "model timed out") ||
		!strings.Contains(end.text, "https://git.example/pulls/7") {
		t.Fatalf("want one message with the failure and the run's outcome, got %+v", h.all())
	}
}

func assertNothingTracked(t *testing.T) {
	t.Helper()
	turns.Lock()
	defer turns.Unlock()
	if len(turns.m) != 0 {
		t.Fatalf("turns still registered after the turn ended: %+v", turns.m)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
