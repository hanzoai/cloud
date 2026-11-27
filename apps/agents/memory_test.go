package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
)

// The assistant answered every message as if it were the first one it had ever
// seen, and said so: it re-introduced itself each turn, and "try again" got back
// "what would you like me to help you with". Nothing carried the conversation.
// These are the four things that had to become true.

// A conversation is instructions (system), then what was said (user/assistant),
// then what is being asked (user). Sending instructions as RoleSystem keeps them
// separate from user turns so openaiMessages never merges the system prompt into
// a user message (which caused models with identity prompts to reject the agent
// instructions as a user-turn persona override, hanzo-inc/cloud#937).
func TestTheConversationPutsHistoryBetweenTheAgentAndTheAsk(t *testing.T) {
	history := []infer.ChatMessage{
		{Role: infer.RoleUser, Content: "weather in Benicia"},
		{Role: infer.RoleAssistant, Content: "It is 24 degrees and clear."},
	}
	msgs := conversation("You are Hanzo.", history, "try again")

	want := []infer.ChatMessage{
		{Role: infer.RoleSystem, Content: "You are Hanzo."},
		{Role: infer.RoleUser, Content: "weather in Benicia"},
		{Role: infer.RoleAssistant, Content: "It is 24 degrees and clear."},
		{Role: infer.RoleUser, Content: "try again"},
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

// Regression test for hanzo-inc/cloud#937: a Slack thread where prior human
// messages precede "@Hanzo fix it" must keep the agent's instructions in
// RoleSystem rather than RoleUser so they are never merged into user input.
func TestSlackThreadInstructionsStaySystemRole(t *testing.T) {
	history := []infer.ChatMessage{
		{Role: infer.RoleUser, Content: "Denis: docs rendering bug on /docs/cli"},
		{Role: infer.RoleAssistant, Content: "I will investigate."},
	}
	msgs := conversation(builtinAgentInstructions, history, "fix it")
	want := []infer.ChatMessage{
		{Role: infer.RoleSystem, Content: builtinAgentInstructions},
		history[0],
		history[1],
		{Role: infer.RoleUser, Content: "fix it"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("want %d messages, got %d", len(want), len(msgs))
	}
	for i := range want {
		if msgs[i].Role != want[i].Role || msgs[i].Content != want[i].Content {
			t.Errorf("message %d: unexpected role or content; want exact separation of instructions, history and ask", i)
		}
	}
}

// A run with nothing to answer — a scheduled agent — asks with its instructions,
// exactly as it did before.
func TestAStandingInstructionIsTheAskWhenThereIsNoQuestion(t *testing.T) {
	msgs := conversation("Post the daily summary.", nil, "")
	if len(msgs) != 1 || msgs[0].Role != infer.RoleUser || msgs[0].Content != "Post the daily summary." {
		t.Fatalf("a scheduled run must ask as a user turn, got %+v", msgs)
	}
}

// The assistant's OWN replies must come back as assistant turns. Told they were
// the user's, the model reads its own words as instructions and agrees with
// itself; this is why the transcript has to carry which side said what, and why
// the inbox alone could never be the source (it records what arrives, and an
// answer leaves).
func TestTheAssistantsOwnTurnsComeBackAsItsOwn(t *testing.T) {
	msgs := transcript([]client.Turn{
		{Sender: "U1", Text: "weather in Benicia"},
		{Self: true, Text: "It is 24 degrees and clear."},
		{Sender: "U1", Text: "   "}, // an event with no words is not a turn
		{Sender: "U2", Text: "thanks"},
	})
	want := []string{infer.RoleUser, infer.RoleAssistant, infer.RoleUser}
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
// by the bridge, put on the wire, and dropped on arrival — the run handler took
// every other field of the turn and never read the history, so the fix looked
// shipped and changed nothing.
func TestTheHistoryReachesTheModel(t *testing.T) {
	plane := &fakePlane{} // no tools: the single-completion path
	withPlane(t, plane)
	ai := &scriptAI{replies: []infer.ChatResponse{{Content: "24 degrees, still clear."}}}

	a := mk("maxpower", "greeter")
	a.Instructions = "You are Hanzo."
	history := []infer.ChatMessage{
		{Role: infer.RoleUser, Content: "weather in Benicia"},
		{Role: infer.RoleAssistant, Content: "It is 24 degrees and clear."},
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
// a fact about the person — while holding `task`, whose issues are GitHub's,
// mirrored in by the App.
//
// The tool was there; the sentence connecting the question to it was not, and no
// model can infer from "GitHub" that our copy of it is a subsystem here. So the
// persona states it, and this keeps it stated.
func TestTheAssistantKnowsWhereIssuesLive(t *testing.T) {
	p := builtinAgentInstructions
	for _, phrase := range []string{
		"`task`",             // the subsystem that answers
		"`list_task_issues`", // the op, named so it does not have to be found
		"mirrored in",        // why GitHub's issues are there at all
		"never",              // …and that a web search is not the way to them
		"hanzoai/iam",        // the repo for Hanzo ID / IAM / auth issues
	} {
		if !strings.Contains(p, phrase) {
			t.Errorf("the persona must say %q — without it the assistant web-searches for issues it holds a tool for", phrase)
		}
	}
}

func TestTheAssistantKnowsProgressiveSkillAndHIPDiscovery(t *testing.T) {
	for name, p := range map[string]string{
		"builtin": builtinAgentInstructions,
		"talk":    talkInstructions,
	} {
		for _, phrase := range []string{
			"build, customize, and mod any part of Hanzo",
			"https://hanzoskills.com/llms.txt",
			"/categories/<slug>.md",
			"/skills/<id>/SKILL.md",
			"/v1/tool/detect",
			"https://hips.hanzo.ai/llms.txt",
		} {
			if !strings.Contains(p, phrase) {
				t.Errorf("%s instructions must include %q", name, phrase)
			}
		}
	}
	if len(progressiveCapabilityInstructions) > 640 {
		t.Errorf("progressive capability block should stay compact (<640 bytes), got %d", len(progressiveCapabilityInstructions))
	}
}

func TestChannelHistoryWeavesMissingAssistantReplies(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto := testStore(t)
		ctx := context.Background()
		now := time.Now().Unix()

		if err := sto.CreateSession(ctx, Session{
			ID: "sess_1", Org: "hanzo", Agent: "hanzo", Actor: "hanzo/u1",
			Status: StatusDone, RootID: "sess_1", Channel: "slack:D0BJK8J5GP4",
			Run: "run_1", StartedAt: now - 100, CreatedAt: now - 100, UpdatedAt: now - 90,
		}); err != nil {
			t.Fatal(err)
		}
		if err := sto.InsertRun(ctx, Run{
			ID: "run_1", Org: "hanzo", AgentName: "hanzo", Actor: "hanzo/u1",
			Status: "ok", Input: "can you fix this finally? working?",
			Output:    "Want me to keep going on the auth flow issue and the Slack post?",
			CreatedAt: now - 90,
		}); err != nil {
			t.Fatal(err)
		}

		// Simulate the bogus PONG run caused by an old probe when inbox lacked assistant replies
		if err := sto.CreateSession(ctx, Session{
			ID: "sess_2", Org: "hanzo", Agent: "hanzo", Actor: "hanzo/u1",
			Status: StatusDone, RootID: "sess_2", Channel: "slack:D0BJK8J5GP4",
			Run: "run_2", StartedAt: now - 50, CreatedAt: now - 50, UpdatedAt: now - 40,
		}); err != nil {
			t.Fatal(err)
		}
		if err := sto.InsertRun(ctx, Run{
			ID: "run_2", Org: "hanzo", AgentName: "hanzo", Actor: "hanzo/u1",
			Status: "ok", Input: "yes@",
			Output:    "PONG",
			CreatedAt: now - 40,
		}); err != nil {
			t.Fatal(err)
		}

		turns := []client.Turn{
			{Sender: "U1", Self: false, Text: "can you fix this finally? working?"},
			{Sender: "U1", Self: false, Text: "yes@"},
		}
		msgs := channelHistory(ctx, sto, "hanzo", "slack:D0BJK8J5GP4", turns)
		if len(msgs) != 2 {
			t.Fatalf("expected 2 messages (user + woven assistant, dropping bogus PONG turn), got %d: %+v", len(msgs), msgs)
		}
		if msgs[0].Role != infer.RoleUser || msgs[0].Content != "can you fix this finally? working?" {
			t.Fatalf("unexpected msgs[0]: %+v", msgs[0])
		}
		if msgs[1].Role != infer.RoleAssistant || msgs[1].Content != "Want me to keep going on the auth flow issue and the Slack post?" {
			t.Fatalf("unexpected msgs[1]: %+v", msgs[1])
		}

	})
}

func TestChannelHistoryIncludesRecordedToolSummariesAndCapsAt1536Bytes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto := testStore(t)
		ctx := context.Background()
		now := time.Now().Unix()

		l := &live{x: Session{ID: "sess_t1", Org: "hanzo"}, run: "run_t1"}
		l.recordTool("task", `{"op":"create_task_project_issue","input":{"key":"hanzoai/iam","title":"Fix OAuth redirect"}}`,
			`{"number":42,"url":"https://github.com/hanzoai/iam/issues/42"}`)
		for i := range 20 {
			l.recordTool("git", `{"op":"get_git_repo_blob","input":{"repo":"cloud","path":"apps/transport/live.go"}}`,
				strings.Repeat("x", 200)+string(rune('A'+i)))
		}
		summary := l.toolSummary()
		if !strings.Contains(summary, "task/create_task_project_issue") || !strings.Contains(summary, "https://github.com/hanzoai/iam/issues/42") {
			t.Fatalf("tool summary missing key args or URL: %q", summary)
		}
		if len(summary) > maxToolSummaryBytes {
			t.Fatalf("tool summary exceeded %d bytes: got %d", maxToolSummaryBytes, len(summary))
		}

		if err := sto.CreateSession(ctx, Session{
			ID: "sess_t1", Org: "hanzo", Agent: "hanzo", Actor: "hanzo/u1",
			Status: StatusDone, RootID: "sess_t1", Channel: "slack:C01",
			Run: "run_t1", StartedAt: now - 100, CreatedAt: now - 100, UpdatedAt: now - 90,
		}); err != nil {
			t.Fatal(err)
		}
		if err := sto.InsertRun(ctx, Run{
			ID: "run_t1", Org: "hanzo", AgentName: "hanzo", Actor: "hanzo/u1",
			Status: "ok", Input: "file an issue on hanzoai/iam",
			Output:    "Filed issue #42 on hanzoai/iam.",
			CreatedAt: now - 90,
		}); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]string{
			"text":  "Filed issue #42 on hanzoai/iam.",
			"tools": summary,
		})
		if _, err := sto.AppendEvent(ctx, Event{
			ID: "evt_msg1", SessionID: "sess_t1", Org: "hanzo", Kind: KindMessage,
			Payload: string(payload), CreatedAt: now - 90,
		}); err != nil {
			t.Fatal(err)
		}

		turns := []client.Turn{
			{Sender: "U1", Self: false, Text: "file an issue on hanzoai/iam"},
			{Sender: "assistant", Self: true, Text: "Filed issue #42 on hanzoai/iam."},
			{Sender: "U1", Self: false, Text: "assign it to me"},
		}
		msgs := channelHistory(ctx, sto, "hanzo", "slack:C01", turns)
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
		}
		if !strings.Contains(msgs[1].Content, "[Tools used]") || !strings.Contains(msgs[1].Content, "https://github.com/hanzoai/iam/issues/42") {
			t.Fatalf("expected assistant turn in channelHistory to include tool summary and issue URL, got %q", msgs[1].Content)
		}

	})
}

func TestChannelHistoryAttributesSpeakersInChannelsAndThreads(t *testing.T) {
	turns := []client.Turn{
		{Sender: "Alice", Text: "[channel context before thread] Deployed auth v2"},
		{Sender: "Sentry", Text: "[Attachment] 500 on /v1/auth/callback"},
		{Sender: "hanzo", Self: true, Text: "Investigating now."},
		{Sender: "Bob", Text: "Happening in prod too"},
	}
	msgs := channelHistory(context.Background(), nil, "hanzo", "slack:C0DEV/1700000000.000100", turns)
	if len(msgs) != 4 {
		t.Fatalf("want 4 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "[channel context before thread] Alice: Deployed auth v2" {
		t.Errorf("unexpected pre-thread speaker formatting: %q", msgs[0].Content)
	}
	if msgs[1].Content != "Sentry: [Attachment] 500 on /v1/auth/callback" {
		t.Errorf("unexpected bot alert speaker formatting: %q", msgs[1].Content)
	}
	if msgs[2].Role != infer.RoleAssistant || msgs[2].Content != "Investigating now." {
		t.Errorf("assistant message must stay unprefixed RoleAssistant, got %+v", msgs[2])
	}
	if msgs[3].Content != "Bob: Happening in prod too" {
		t.Errorf("unexpected human speaker formatting: %q", msgs[3].Content)
	}
}

func TestSlackTurnReceivesSystemInstructionsToolsAndContextGuidance(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var captured *infer.ChatRequest
		ai := &groupMockAI{
			fn: func(req *infer.ChatRequest) (*infer.ChatResponse, error) {
				cp := *req
				cp.Messages = append([]infer.ChatMessage(nil), req.Messages...)
				captured = &cp
				return &infer.ChatResponse{Content: "Checked the trace and started a fix."}, nil
			},
		}
		app := mountApp(t, ai)
		_ = app

		ctx := withReply(context.Background(),
			client.Reply{Provider: "slack", Channel: "C0DEV", Thread: "1700000000.000100"},
			client.Origin{Account: "t1", Message: "1700000005.000200", Public: true},
		)
		history := []infer.ChatMessage{
			{Role: infer.RoleUser, Content: "Alice: 500 on /v1/auth/callback in hanzoai/iam"},
		}
		run, err := runOnBehalfModel(mounted, ctx, "acme", "u-alice", "hanzo", "fix this", "", history, meter.PayerFor("acme", ""))
		if err != nil {
			t.Fatalf("runOnBehalfModel: %v", err)
		}
		if run.Status != "ok" {
			t.Fatalf("expected ok run, got %+v", run)
		}
		if captured == nil || len(captured.Messages) < 3 {
			t.Fatalf("expected system + history + user messages, got %+v", captured)
		}
		sys := captured.Messages[0]
		if sys.Role != infer.RoleSystem {
			t.Fatalf("expected RoleSystem instructions, got %q", sys.Role)
		}
		for _, want := range []string{
			"ALWAYS read the provided thread and recent channel history FIRST",
			"Never act tabula rasa",
			"create_agent_coding",
			"run_in_sandbox",
			"https://hanzoskills.com/llms.txt",
			"https://hips.hanzo.ai/llms.txt",
			"Slack channel C0DEV (thread 1700000000.000100)",
		} {
			if !strings.Contains(sys.Content, want) {
				t.Errorf("Slack system prompt missing %q", want)
			}
		}

		// Even a persisted primary hanzo row created before newer instructions
		// must receive Slack context guidance and progressive capability discovery.
		if code, body := do(t, app, http.MethodPost, "/v1/agent", "legacyorg", map[string]any{
			"name":               "hanzo",
			"isPrimary":          true,
			"instructions":       "Legacy primary instructions.",
			"tools":              []string{"*"},
			"cap_micro_usd":      5000000,
			"max_task_micro_usd": 1000000,
			"period":             "week",
		}); code != http.StatusCreated {
			t.Fatalf("create legacy primary: %d %s", code, body)
		}
		captured = nil
		run, err = runOnBehalfModel(mounted, ctx, "legacyorg", "u-alice", "hanzo", "fix this", "", history, meter.PayerFor("legacyorg", ""))
		if err != nil || run.Status != "ok" {
			t.Fatalf("legacy primary run failed: err=%v run=%+v", err, run)
		}
		if captured == nil || !strings.Contains(captured.Messages[0].Content, "Slack channel C0DEV") || !strings.Contains(captured.Messages[0].Content, "https://hanzoskills.com/llms.txt") {
			t.Fatalf("legacy primary Slack turn missing context or progressive discovery guidance: %+v", captured)
		}
	})
}
