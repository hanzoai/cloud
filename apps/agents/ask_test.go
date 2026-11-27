package agents

// ask_test.go — a run asks its person, the person answers once, and the run is
// handed exactly that answer; nobody else can answer, and silence is said.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/zap-proto/zip"
)

// slack is the conversation a test run is read in.
var slack = client.Reply{Provider: "slack", Channel: "C1", Thread: "T1", User: "UALICE"}

// shown records every message a question put in the conversation.
type shown struct {
	mu   sync.Mutex
	sent []client.ChatSendIn
}

func (s *shown) all() []client.ChatSendIn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]client.ChatSendIn(nil), s.sent...)
}

// asking stands in for the provider, and returns what it was sent.
func asking(t *testing.T) *shown {
	t.Helper()
	s := &shown{}
	prev, prevPoll := postQuestion, askPoll
	postQuestion = func(_ context.Context, _, _ string, in *client.ChatSendIn) (*client.ChatSendOut, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.sent = append(s.sent, *in)
		return &client.ChatSendOut{MessageID: "m1"}, nil
	}
	askPoll = 10 * time.Millisecond
	t.Cleanup(func() { postQuestion, askPoll = prev, prevPoll })
	return s
}

func options(labels ...string) []client.Option {
	out := make([]client.Option, 0, len(labels))
	for _, l := range labels {
		out = append(out, client.Option{Label: l})
	}
	return out
}

// posed waits for the question to be shown and returns its id.
func posed(t *testing.T, s *shown) string {
	t.Helper()
	if !waitFor(func() bool { return len(s.all()) > 0 }) {
		t.Fatal("the question was never shown")
	}
	return s.all()[0].Question.ID
}

// press answers as the named person, as channels does for a press.
func press(subject, question, label string) (*client.Answered, error) {
	return planeQuestionAnswer(cloud.For(context.Background(), "acme"), &client.AnswerIn{
		Subject: subject, Question: question, Text: label, Chosen: true, By: "UALICE",
	})
}

// The question is shown where the run is read, with its options and whose it is;
// its person's press is the run's answer, handed over once, and the message then
// says what was chosen and by whom, with no options left to press.
func TestARunIsHandedItsPersonsChoiceOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		sto := storeOf(t, &mounted.State, "acme")
		turnSession(t, sto, "sess_a", "acme/alice", "run_a")
		s := asking(t)

		got := make(chan string, 1)
		go func() {
			out, err := Ask(context.Background(), "acme", "sess_a", slack,
				client.AskIn{Question: "Which database?", Options: options("Postgres", "SQLite")}, 5*time.Second)
			if err != nil {
				out = "error: " + err.Error()
			}
			got <- out
		}()
		id := posed(t, s)
		first := s.all()[0]
		if first.Channel != "C1" || first.ReplyTo != "T1" || first.Question.Owner != "UALICE" || len(first.Question.Options) != 2 {
			t.Fatalf("the question must be shown in the run's conversation, for its person: %+v", first)
		}

		var he *zip.HTTPError
		if _, err := press("bob", id, "SQLite"); !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("another person answered the question: %v", err)
		}
		if _, err := press("alice", id, "MySQL"); !errors.As(err, &he) || he.Status != http.StatusBadRequest {
			t.Fatalf("a press on an option the question never offered was taken: %v", err)
		}
		if _, err := press("alice", id, "Postgres"); err != nil {
			t.Fatal(err)
		}
		if _, err := press("alice", id, "SQLite"); !errors.As(err, &he) || he.Status != http.StatusConflict {
			t.Fatalf("a question was answered twice: %v", err)
		}
		select {
		case out := <-got:
			if out != "The person chose: Postgres" {
				t.Fatalf("the run was handed %q", out)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the answer never reached the run")
		}
		if !waitFor(func() bool { return len(s.all()) == 2 }) {
			t.Fatalf("the question's message was not updated: %+v", s.all())
		}
		edit := s.all()[1]
		if edit.Edit != "m1" || edit.Question.ID != "" || edit.Text != "✓ Postgres — chosen by <@UALICE>" {
			t.Fatalf("the answered question must read as chosen, with no options: %+v", edit)
		}

	})
}

// A reply in the conversation, in the person's own words, answers their open
// question there; someone else's reply finds no question of theirs.
func TestAReplyInWordsAnswersTheOpenQuestion(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		sto := storeOf(t, &mounted.State, "acme")
		turnSession(t, sto, "sess_w", "acme/alice", "run_w")
		s := asking(t)
		got := make(chan string, 1)
		go func() {
			out, _ := Ask(context.Background(), "acme", "sess_w", slack,
				client.AskIn{Question: "Which region?", Options: options("us", "eu")}, 5*time.Second)
			got <- out
		}()
		posed(t, s)
		ctx := cloud.For(context.Background(), "acme")
		var he *zip.HTTPError
		if _, err := planeQuestionAnswer(ctx, &client.AnswerIn{Subject: "bob", Reply: slack, Text: "asia"}); !errors.As(err, &he) || he.Status != http.StatusNotFound {
			t.Fatalf("someone else's reply reached the question: %v", err)
		}
		if _, err := planeQuestionAnswer(ctx, &client.AnswerIn{Subject: "alice", Reply: slack, Text: "both, us first", By: "UALICE"}); err != nil {
			t.Fatal(err)
		}
		if out := <-got; out != "The person answered in their own words: both, us first" {
			t.Fatalf("the run was handed %q", out)
		}

	})
}

// Silence is said: a question nobody answers in time is closed, the run is told
// no answer came, the message says so with no options, and a late press finds it
// closed. A run stopped while it waits stops waiting the same way.
func TestAnUnansweredQuestionSaysNoneCame(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		sto := storeOf(t, &mounted.State, "acme")
		turnSession(t, sto, "sess_t", "acme/alice", "run_t")
		s := asking(t)
		out, err := Ask(context.Background(), "acme", "sess_t", slack,
			client.AskIn{Question: "Ship it?", Options: options("yes", "no")}, 50*time.Millisecond)
		if err != nil || !strings.HasPrefix(out, "No answer came") {
			t.Fatalf("the run was told %q (%v), want that no answer came", out, err)
		}
		sent := s.all()
		if last := sent[len(sent)-1]; last.Edit != "m1" || last.Question.ID != "" || !strings.Contains(last.Text, "No answer came") {
			t.Fatalf("the closed question's message must say so, with no options: %+v", last)
		}
		var he *zip.HTTPError
		if _, err := press("alice", sent[0].Question.ID, "yes"); !errors.As(err, &he) || he.Status != http.StatusConflict {
			t.Fatalf("a late press answered a closed question: %v", err)
		}

		ctx, stop := context.WithCancelCause(context.Background())
		stop(ErrPaused)
		if out, _ := Ask(ctx, "acme", "sess_t", slack, client.AskIn{Question: "Now?", Options: options("a", "b")}, time.Minute); !strings.Contains(out, "paused") {
			t.Fatalf("a paused run was told %q", out)
		}

	})
}

// A question a person cannot answer from their screen is refused before it is
// asked, and so is a run with nowhere to ask it.
func TestAQuestionMustBeAnswerable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		long := strings.Repeat("x", maxLabel+1)
		for _, in := range []client.AskIn{
			{Question: "", Options: options("a", "b")},
			{Question: "one?", Options: options("a")},
			{Question: "nine?", Options: options("1", "2", "3", "4", "5", "6", "7", "8", "9")},
			{Question: "long?", Options: options("a", long)},
			{Question: "twice?", Options: options("a", "a")},
		} {
			if checkAsk(in) == nil {
				t.Errorf("%+v was accepted", in)
			}
		}
		mountInproc(t)
		sto := storeOf(t, &mounted.State, "acme")
		turnSession(t, sto, "sess_n", "acme/alice", "run_n")
		if _, err := Ask(context.Background(), "acme", "sess_n", client.Reply{}, client.AskIn{Question: "q?", Options: options("a", "b")}, time.Second); err == nil {
			t.Fatal("a run with no conversation asked anyway")
		}

	})
}

// A chat turn is offered ask_user, and a call to it waits on the person — not on
// the tool plane — and hands the model their choice as the tool's result.
func TestATurnAsksItsPersonThroughItsLoop(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		sto := storeOf(t, &mounted.State, "acme")
		turnSession(t, sto, "sess_l", "acme/alice", "run_l")
		s := asking(t)
		withPlane(t, &fakePlane{})
		l := &live{s: mounted, x: reload(t, sto, "sess_l"), run: "run_l"}
		ai := &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{{ID: "q1", Name: AskTool.Name, Arguments: `{"question":"Which one?","options":[{"label":"A"},{"label":"B"}]}`}}},
			{Content: "Going with B."},
		}}
		pressed := make(chan struct{})
		go func() {
			defer close(pressed)
			id := posed(t, s)
			_, _ = press("alice", id, "B")
		}()
		defer func() { <-pressed }()
		ctx := withLive(withReply(context.Background(), slack, client.Origin{}), l)
		resp, _, err, _ := completeWithTools(ctx, ai, "acme", "acme/alice", conversation("", nil, "pick"), "m", "", nil, "run_l")
		if err != nil || resp.Content != "Going with B." {
			t.Fatalf("the turn ended %+v (%v)", resp, err)
		}
		if offered := ai.seen[0].Tools; len(offered) != 1 || offered[0].Name != AskTool.Name {
			t.Fatalf("a turn must be offered ask_user, got %+v", offered)
		}
		var result string
		for _, m := range ai.seen[1].Messages {
			if m.Role == infer.RoleTool && m.ToolCallID == "q1" {
				result = m.Content
			}
		}
		if result != "The person chose: B" {
			t.Fatalf("the model was handed %q", result)
		}

	})
}
