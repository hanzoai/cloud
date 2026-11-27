package transport

// resume_turn_test.go — "continue" in a thread picks up the turn stopped there.

import (
	"testing"

	"github.com/hanzoai/cloud/client"
)

// "continue" said in the thread a turn was stopped in asks agents to pick THAT
// turn up — the same conversation, as the same person — and its answer lands in
// the thread like any turn's.
func TestContinueInTheThreadResumesTheStoppedTurn(t *testing.T) {
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

	a.mu.Lock()
	a.halted = nil
	a.pages = append(a.pages, &client.TurnEvents{
		Events: []client.TurnEvent{{Seq: 2, Kind: client.TurnAnswer, Text: "Built."}},
		Next:   2, Outcome: client.TurnCompleted,
	})
	a.mu.Unlock()
	more := asked()
	more.Text = "Continue."
	turn(service(), tr, "acme", more)

	a.mu.Lock()
	started := append([]client.RunOnBehalfIn(nil), a.started...)
	a.mu.Unlock()
	if len(started) != 2 {
		t.Fatalf("want the question and the continue to reach agents, got %d starts", len(started))
	}
	first, again := started[0], started[1]
	if first.Resume {
		t.Fatal("a question was sent as a resume")
	}
	if !again.Resume || again.Reply != first.Reply || again.Subject != first.Subject || again.Input != "Continue." {
		t.Fatalf("continue must ask to resume the turn in this thread as its asker, got %+v after %+v", again, first)
	}
	if end := h.last(); end.text != "Built." {
		t.Fatalf("the resumed turn's answer must land in the thread, got %+v", end)
	}
	assertNothingTracked(t)
}

// Only the word itself asks to resume, the way only "stop" stops: a sentence that
// begins with it is a request like any other.
func TestWhatReadsAsAResume(t *testing.T) {
	for text, want := range map[string]bool{
		"continue": true, "Continue.": true, " resume! ": true, "RESUME": true,
		"continue the story": false, "stop": false, "": false,
	} {
		if got := isResume(text); got != want {
			t.Errorf("isResume(%q) = %v, want %v", text, got, want)
		}
	}
}

// A turn paused elsewhere — the console, the API — says so in its message, and
// the rendering ends: nothing is running to show.
func TestAPausedTurnSaysSo(t *testing.T) {
	recordTurns(t)
	linked(t)
	(&agent{pages: []*client.TurnEvents{
		{Events: []client.TurnEvent{progress(1, "Working")}, Next: 1},
		{Next: 1, Outcome: client.TurnPaused},
	}}).install(t)
	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	if end := h.last(); end.text != pausedText || end.edit == "" {
		t.Fatalf("the turn's message must become %q, got %+v", pausedText, end)
	}
	assertNothingTracked(t)
}
