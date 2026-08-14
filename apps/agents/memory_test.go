package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
)

// The assistant answered every message as if it were the first one it had ever
// seen, and said so: it re-introduced itself each turn, and "try again" got back
// "what would you like me to help you with". Nothing carried the conversation.
// These are the four things that had to become true.

// A conversation is instructions, then what was said, then what is being asked —
// three separate USER/assistant turns. The order is the point: prior turns belong
// BETWEEN who the agent is and the newest question, and the string concatenation
// this replaces had no between.
//
// The roles are measured, not assumed. enso-flash through api.hanzo.ai IGNORES a
// system turn — asked in one for the single word PONG it answered "Hello! How can
// I help you today?", and answered "PONG" to the same bytes in a user turn — so
// instructions in a system turn reach nothing and the agent silently runs with no
// instructions at all. And they are a turn of their own, not a prefix on the
// newest message: prepended to "try again" the model answered the instructions
// ("Understood. How can I help you today?") instead of the question.
func TestTheConversationPutsHistoryBetweenTheAgentAndTheAsk(t *testing.T) {
	history := []types.ChatMessage{
		{Role: types.RoleUser, Content: "weather in Benicia"},
		{Role: types.RoleAssistant, Content: "It is 24 degrees and clear."},
	}
	msgs := conversation("You are Hanzo.", history, "try again")

	want := []types.ChatMessage{
		{Role: types.RoleUser, Content: "You are Hanzo."},
		{Role: types.RoleUser, Content: "weather in Benicia"},
		{Role: types.RoleAssistant, Content: "It is 24 degrees and clear."},
		{Role: types.RoleUser, Content: "try again"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("want %d turns, got %d: %+v", len(want), len(msgs), msgs)
	}
	for i := range want {
		if msgs[i].Role != want[i].Role || msgs[i].Content != want[i].Content {
			t.Errorf("turn %d: want %s %q, got %s %q",
				i, want[i].Role, want[i].Content, msgs[i].Role, msgs[i].Content)
		}
	}
}

// A run with nothing to answer — a scheduled agent — asks with its instructions,
// exactly as it did before.
func TestAStandingInstructionIsTheAskWhenThereIsNoQuestion(t *testing.T) {
	msgs := conversation("Post the daily summary.", nil, "")
	if len(msgs) != 1 || msgs[0].Role != types.RoleUser || msgs[0].Content != "Post the daily summary." {
		t.Fatalf("a scheduled run must ask as a user turn, got %+v", msgs)
	}
}

// The assistant's OWN replies must come back as assistant turns. Told they were
// the user's, the model reads its own words as instructions and agrees with
// itself; this is why the transcript has to carry which side said what, and why
// the inbox alone could never be the source (it records what arrives, and an
// answer leaves).
func TestTheAssistantsOwnTurnsComeBackAsItsOwn(t *testing.T) {
	msgs := transcript([]plane.Turn{
		{Sender: "U1", Text: "weather in Benicia"},
		{Self: true, Text: "It is 24 degrees and clear."},
		{Sender: "U1", Text: "   "}, // an event with no words is not a turn
		{Sender: "U2", Text: "thanks"},
	})
	want := []string{types.RoleUser, types.RoleAssistant, types.RoleUser}
	if len(msgs) != len(want) {
		t.Fatalf("want %d turns, got %d: %+v", len(want), len(msgs), msgs)
	}
	for i, role := range want {
		if msgs[i].Role != role {
			t.Errorf("turn %d: want %s, got %s (%q)", i, role, msgs[i].Role, msgs[i].Content)
		}
	}
	if transcript(nil) != nil {
		t.Error("no turns is a first message, which is a real answer and not a missing one")
	}
}

// The whole conversation must reach the MODEL, not merely exist. It was assembled
// by the bridge, put on the wire, and dropped on arrival — the run door took every
// other field of the turn and never read the history, so the fix looked shipped
// and changed nothing.
func TestTheHistoryReachesTheModel(t *testing.T) {
	plane := &fakePlane{} // no tools: the single-completion path
	withPlane(t, plane)
	ai := &scriptAI{replies: []types.ChatResponse{{Content: "24 degrees, still clear."}}}

	a := mk("maxpower", "greeter")
	a.Instructions = "You are Hanzo."
	history := []types.ChatMessage{
		{Role: types.RoleUser, Content: "weather in Benicia"},
		{Role: types.RoleAssistant, Content: "It is 24 degrees and clear."},
	}
	r := executeRun(context.Background(), ai, "maxpower", "maxpower/u1", a, "try again", history, "", "run_test")

	if r.Status != "ok" {
		t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
	}
	msgs := ai.seen[0].Messages
	if len(msgs) != 4 {
		t.Fatalf("want instructions + two prior turns + the ask, got %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Content != "weather in Benicia" || msgs[2].Content != "It is 24 degrees and clear." {
		t.Fatalf("the prior turns must be shown in the order they were said, got %+v", msgs)
	}
	if msgs[3].Content != "try again" {
		t.Fatalf("the ask must come last, got %q", msgs[3].Content)
	}
}

// WHAT THE ASSISTANT IS. Three things the default persona has to say, each of
// which was a real complaint about the deployed bot:
//
//	its NAME is Hanzo — it introduced itself as Enso, which is the model it runs
//	on. The correction is stated because the weights answer "Enso" on their own
//	and a prompt that merely asserts the right name leaves both true.
//
//	it GREETS ONCE — it opened every single message with an introduction.
//
//	it is GENERAL — "the assistant for the Hanzo cloud" was read as the subject of
//	every conversation, so any question came back as cloud support.
func TestTheAssistantKnowsWhatItIs(t *testing.T) {
	p := builtinAgentInstructions
	for _, phrase := range []string{
		"You are Hanzo",
		"never introduce",
		"Enso is the name of the model",
		"Introduce yourself at most once",
		"general assistant",
	} {
		if !strings.Contains(p, phrase) {
			t.Errorf("the persona must say %q", phrase)
		}
	}
	// The old opening sentence, which is the one that made it a support bot.
	if strings.Contains(p, "the assistant for the Hanzo cloud") {
		t.Error("the cloud is one of its abilities, never the subject of the conversation")
	}
}

// WHERE THE ISSUES ARE. Asked which issues someone had filed on our
// repositories, the deployed assistant searched the WEB for a GitHub profile,
// met the login wall every logged-out scrape meets, and reported that absence as
// a fact about the person — while holding `todo`, whose issues are GitHub's,
// mirrored in by the App.
//
// The tool was there; the sentence connecting the question to it was not, and no
// model can infer from "GitHub" that our copy of it is a subsystem here. So the
// persona states it, and this keeps it stated.
func TestTheAssistantKnowsWhereIssuesLive(t *testing.T) {
	p := builtinAgentInstructions
	for _, phrase := range []string{
		"todo",            // the subsystem that answers
		"get_todo_issues", // the op, named so it does not have to be found
		"mirrored in",     // why GitHub's issues are there at all
		"never",           // …and that a web search is not the way to them
	} {
		if !strings.Contains(p, phrase) {
			t.Errorf("the persona must say %q — without it the assistant web-searches for issues it holds a tool for", phrase)
		}
	}
}
