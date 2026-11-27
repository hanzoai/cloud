package transport

// steer_test.go — what the asker says while their run is working reaches that
// run, and a run's buttons act for its asker and nobody else.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// working starts a turn that says one line and then works until stopped, and
// returns once its message is up.
func working(t *testing.T, a *agent, tr transport, h *heard) (done chan struct{}) {
	t.Helper()
	a.pages = []*client.TurnEvents{{Events: []client.TurnEvent{progress(1, "Cloning the repository")}, Next: 1}}
	a.install(t)
	done = make(chan struct{})
	go func() { turn(service(), tr, "acme", asked()); close(done) }()
	waitFor(t, func() bool { return h.posts() == 1 })
	return done
}

func stopIt(tr transport, done chan struct{}) {
	stop := asked()
	stop.Text = "stop"
	turn(service(), tr, "acme", stop)
	<-done
}

// Said into the conversation while the asker's turn works, a message is that
// turn's next input: it reaches agents for exactly that session as its asker, the
// turn's own message says it was heard, and no second turn starts.
func TestAMessageToARunningTurnSteersIt(t *testing.T) {
	recordTurns(t)
	linked(t)
	a, h := &agent{}, &heard{}
	tr := listening(h, true)
	done := working(t, a, tr, h)

	more := asked()
	more.Text = "use the staging database instead"
	turn(service(), tr, "acme", more)

	a.mu.Lock()
	controlled, started := append([]client.SessionControlIn(nil), a.controlled...), len(a.started)
	a.mu.Unlock()
	if len(controlled) != 1 || controlled[0] != (client.SessionControlIn{
		Subject: "u-acme", SessionID: "sess_1", Command: client.SessionMessage, Message: more.Text,
	}) {
		t.Fatalf("the message must reach the running turn as its asker, got %+v", controlled)
	}
	if started != 1 {
		t.Fatalf("a message to a running turn started %d turns, want the one", started)
	}
	waitFor(t, func() bool { return h.last().text == heardText })
	if h.posts() != 1 {
		t.Fatalf("the acknowledgement must be an edit of the turn's message, saw %d posts", h.posts())
	}
	stopIt(tr, done)
}

// With nothing of theirs running, a message is a new turn — after asking whether
// a coding run of theirs is going in this conversation.
func TestAMessageWithNothingRunningIsANewTurn(t *testing.T) {
	recordTurns(t)
	linked(t)
	a := &agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: "hello"}}, Next: 1, Outcome: client.TurnCompleted},
	}}
	a.install(t)
	turn(service(), listening(&heard{}, true), "acme", asked())
	if len(a.started) != 1 || len(a.controlled) != 0 {
		t.Fatalf("want one new turn and nothing steered, got %d turns and %+v", len(a.started), a.controlled)
	}
	m := asked()
	if len(a.coded) != 1 || a.coded[0].Reply != (client.Reply{Provider: "slack", Channel: m.Channel.ID, Thread: m.ReplyTo, User: "U0FAKE"}) {
		t.Fatalf("the conversation's coding run must be asked for first, got %+v", a.coded)
	}
}

// With a coding run of theirs going in the conversation and no turn, the message
// is that run's new instruction, and no turn starts.
func TestAMessageToARunningCodingRunFollowsItUp(t *testing.T) {
	recordTurns(t)
	linked(t)
	a := &agent{coding: true}
	a.install(t)
	m := asked()
	m.Text = "also add a test for it"
	turn(service(), listening(&heard{}, true), "acme", m)
	if len(a.started) != 0 || len(a.coded) != 1 || a.coded[0].Text != m.Text || a.coded[0].Subject != "u-acme" {
		t.Fatalf("want the coding run followed up as its asker and no turn, got %d turns, %+v", len(a.started), a.coded)
	}
}

// A running turn that does not take the message — it has already answered, or
// agents refused — leaves it a turn of its own, never nothing.
func TestAMessageARunningTurnRefusesIsANewTurn(t *testing.T) {
	recordTurns(t)
	linked(t)
	a, h := &agent{owner: "somebody-else"}, &heard{}
	tr := listening(h, true)
	done := working(t, a, tr, h)
	more := asked()
	more.Text = "and another thing"
	second := make(chan struct{})
	go func() { turn(service(), tr, "acme", more); close(second) }()
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.started) == 2
	})
	stopIt(tr, done)
	<-second
	assertNothingTracked(t)
}

// While a turn works its message carries Stop and Pause for its asker; paused,
// Continue and Stop; answered, nothing.
func TestATurnsMessageCarriesItsControls(t *testing.T) {
	recordTurns(t)
	linked(t)
	want := client.Controls{Session: "sess_1", Kind: client.ControlsTurn, Owner: "U0FAKE"}
	for _, c := range []struct {
		outcome string
		end     client.Controls
	}{
		{client.TurnPaused, client.Controls{Session: "sess_1", Kind: client.ControlsTurn, Owner: "U0FAKE", Paused: true}},
		{client.TurnCompleted, client.Controls{}},
	} {
		(&agent{pages: []*client.TurnEvents{
			{Events: []client.TurnEvent{progress(1, "Working")}, Next: 1},
			{Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "done"}}, Next: 2, Outcome: c.outcome},
		}}).install(t)
		h := &heard{}
		turn(service(), listening(h, true), "acme", asked())
		if first := h.all()[0]; first.ctl != want {
			t.Fatalf("a working turn's message carries %+v, want %+v", first.ctl, want)
		}
		if end := h.last(); end.ctl != c.end {
			t.Fatalf("%s: the message ends carrying %+v, want %+v", c.outcome, end.ctl, c.end)
		}
	}
}

// pressing mounts transports over a recording transport and resolves each platform
// user to their own person, as chat_identity does.
func pressing(t *testing.T, a *agent) (*heard, context.Context) {
	t.Helper()
	recordTurns(t)
	prevID := identify
	identify = func(_ context.Context, _, _ string, in *client.ChatIdentityIn) (*client.ChatIdentityOut, error) {
		switch in.User {
		case "U0FAKE":
			return &client.ChatIdentityOut{Subject: "u-acme"}, nil
		case "UOTHER":
			return &client.ChatIdentityOut{Subject: "u-other"}, nil
		}
		return &client.ChatIdentityOut{Say: "link your account"}, nil
	}
	a.owner = "u-acme"
	a.install(t)
	h := &heard{}
	prevT, prevM := transports, mounted.Load()
	transports = []transport{listening(h, true)}
	mounted.Store(service())
	t.Cleanup(func() { identify, transports = prevID, prevT; mounted.Store(prevM) })
	return h, cloud.For(context.Background(), "acme")
}

func press(user, command string, ctl client.Controls) *client.TransportActIn {
	return &client.TransportActIn{
		Provider: "slack", Account: "t0fake", Channel: "C0FAKE", Thread: "1699999999.000100",
		Message: "m-pressed", User: user, Command: command, Controls: ctl,
	}
}

// A press acts as the person who PRESSED, resolved from the platform — never as
// the run's owner named on the button. Anyone else is refused 403 and agents is
// never asked; nobody linked is refused the same way.
func TestAPressActsOnlyForTheRunsPerson(t *testing.T) {
	a := &agent{}
	_, ctx := pressing(t, a)
	ctl := client.Controls{Session: "sess_1", Kind: client.ControlsTurn, Owner: "U0FAKE"}

	for _, who := range []string{"UOTHER", "UNOBODY"} {
		_, err := planeAct(ctx, press(who, client.SessionStop, ctl))
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("%s pressed Stop on another's run: %v", who, err)
		}
	}
	if len(a.controlled) != 0 {
		t.Fatalf("a refused press reached a run: %+v", a.controlled)
	}
	if _, err := planeAct(ctx, press("U0FAKE", client.SessionPause, ctl)); err != nil {
		t.Fatal(err)
	}
	if len(a.controlled) != 1 || a.controlled[0].Subject != "u-acme" || a.controlled[0].Command != client.SessionPause {
		t.Fatalf("the owner's press must reach the run as them, got %+v", a.controlled)
	}
}

// Continue on a paused turn runs it again in the pressed message, never a new
// one; Stop on a paused coding run, which nothing running can show, edits it.
func TestAPressShowsItsResultInThePressedMessage(t *testing.T) {
	a := &agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: "Built."}}, Next: 1, Outcome: client.TurnCompleted},
	}}
	h, ctx := pressing(t, a)
	if _, err := planeAct(ctx, press("U0FAKE", client.SessionResume,
		client.Controls{Session: "sess_1", Kind: client.ControlsTurn, Owner: "U0FAKE", Paused: true})); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return h.posts() == 0 && len(h.all()) > 0 && h.last().text == "Built." })
	if end := h.last(); end.edit != "m-pressed" {
		t.Fatalf("a continued turn must answer in the pressed message, got %+v", end)
	}
	assertNothingTracked(t)

	a.mu.Lock()
	a.coding = true
	a.mu.Unlock()
	coding := client.Controls{Session: "sess_c", Kind: client.ControlsCoding, Owner: "U0FAKE", Paused: true}
	if _, err := planeAct(ctx, press("U0FAKE", client.SessionResume, coding)); err != nil {
		t.Fatal(err)
	}
	if len(a.coded) != 1 || a.coded[0].SessionID != "sess_c" || a.coded[0].Update != "m-pressed" || a.coded[0].Subject != "u-acme" {
		t.Fatalf("Continue on a coding run must follow it up in the pressed message, got %+v", a.coded)
	}

	a.mu.Lock()
	a.was = "paused"
	a.mu.Unlock()
	if _, err := planeAct(ctx, press("U0FAKE", client.SessionStop, coding)); err != nil {
		t.Fatal(err)
	}
	if end := h.last(); end.text != codingStoppedText || end.edit != "m-pressed" || end.ctl != (client.Controls{}) {
		t.Fatalf("a stopped paused run's message must say so, with no buttons, got %+v", end)
	}
}

// Only a thread redirects a coding run: what its person says next at the top of
// the channel is a turn of its own.
func TestAMessageAtTheTopOfTheChannelLeavesACodingRunAlone(t *testing.T) {
	recordTurns(t)
	linked(t)
	a := &agent{coding: true, pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{{Seq: 1, Kind: client.TurnAnswer, Text: "hi"}}, Next: 1, Outcome: client.TurnCompleted},
	}}
	a.install(t)
	m := asked()
	m.Channel.Kind, m.ReplyTo = ChannelGroup, ""
	turn(service(), listening(&heard{}, true), "acme", m)
	if len(a.coded) != 0 || len(a.started) != 1 {
		t.Fatalf("a top-level message redirected a coding run: %+v, %d turns", a.coded, len(a.started))
	}
}

// While its person has a question open in the conversation, what they say there
// is the answer: agents is given it as their words, and nothing is steered and
// no turn starts.
func TestAReplyWhileAQuestionIsOpenAnswersIt(t *testing.T) {
	recordTurns(t)
	linked(t)
	a := &agent{asking: true, coding: true}
	a.install(t)
	m := asked()
	m.Text = "the second one"
	turn(service(), listening(&heard{}, true), "acme", m)
	if len(a.answered) != 1 || len(a.started) != 0 || len(a.coded) != 0 || len(a.controlled) != 0 {
		t.Fatalf("want the reply taken as the answer and nothing else, got %+v, %d turns, %+v, %+v",
			a.answered, len(a.started), a.coded, a.controlled)
	}
	got := a.answered[0]
	if got.Subject != "u-acme" || got.Text != m.Text || got.Chosen || got.By != "U0FAKE" ||
		got.Reply != (client.Reply{Provider: "slack", Channel: m.Channel.ID, Thread: m.ReplyTo, User: "U0FAKE"}) {
		t.Fatalf("the answer must be the sender's own words in this conversation, got %+v", got)
	}
}

// An option pressed on a question is its answer, as the PRESSER: someone else is
// refused 403 and nothing is answered; the question's person answers it.
func TestAPressedOptionAnswersAsItsPresser(t *testing.T) {
	a := &agent{}
	_, ctx := pressing(t, a)
	press := func(user string) *client.TransportActIn {
		return &client.TransportActIn{Provider: "slack", Account: "t0fake", Channel: "C0FAKE", Thread: "1699999999.000100",
			Message: "m-q", User: user, Command: client.SessionAnswer, Question: "qst_1", Answer: "Postgres",
			Controls: client.Controls{Owner: "U0FAKE"}}
	}
	_, err := planeAct(ctx, press("UOTHER"))
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusForbidden || len(a.answered) != 0 {
		t.Fatalf("another person answered the question: %v %+v", err, a.answered)
	}
	if _, err := planeAct(ctx, press("U0FAKE")); err != nil {
		t.Fatal(err)
	}
	if len(a.answered) != 1 || a.answered[0] != (client.AnswerIn{Subject: "u-acme", Question: "qst_1", Text: "Postgres", Chosen: true, By: "U0FAKE"}) {
		t.Fatalf("the press must answer as its presser, got %+v", a.answered)
	}
}
