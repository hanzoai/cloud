package agents

// follow_test.go — a turn answers for the coding run it starts.
//
// The bar is what a person reading the turn sees: the run's steps as the turn's
// progress, its buttons while it works, and its outcome — the pull request, or
// what failed — in the turn's answer. The run narrating into a message of its own
// is what this replaced, so every case is asserted on the TURN's session, which
// is all a surface reads.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	luxlog "github.com/luxfi/log"
)

// following is a turn over a real store, with a coding run planted beside it.
func following(t *testing.T) (*live, *Store) {
	t.Helper()
	st, sto := meterStore(t)
	s := &cloud.Service[state]{Log: luxlog.New("test"), State: *st}
	s.State.bus = newBus()
	turnSession(t, sto, "sess_turn", "acme/z", "run_turn")
	plant(t, sto, "sess_code", StatusRunning, time.Now().Unix())
	return &live{s: s, x: reload(t, sto, "sess_turn"), run: "run_turn"}, sto
}

// logged appends one of a run's own events, as apps/coding mirrors them.
func logged(t *testing.T, sto *Store, session, kind string, payload any) {
	t.Helper()
	appendEvent(t, sto, session, kind, payload)
}

// displayed is each event of one kind the turn has shown, in order, as a surface
// reads them.
func displayed(t *testing.T, sto *Store, kind string) []string {
	t.Helper()
	out, err := readTurn(context.Background(), sto, "acme", "sess_turn", 0)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range out.Events {
		if e.Kind == kind {
			texts = append(texts, e.Text)
		}
	}
	return texts
}

// concluding runs conclude while the test drives the run, and answers what the
// turn said once the run is over.
func concluding(t *testing.T, l *live, answer string, drive func()) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- l.conclude(context.Background(), answer) }()
	time.Sleep(30 * time.Millisecond)
	drive()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never stopped waiting for a run that ended")
		return ""
	}
}

// A coding call is followed whichever way it was spelled, and no other call is.
func TestEverySpellingOfACodingCallIsFollowed(t *testing.T) {
	const started = `{"sessionId":"sess_c","branch":"agent/c","repo":"api"}`
	for name, tc := range map[string]struct {
		tool, args string
		follows    bool
	}{
		"grouped — the shape every Slack turn takes": {"agent", `{"op":"create_agent_coding","input":{"repo":"api","prompt":"x"}}`, true},
		"grouped, by the operation's id":             {"agent", `{"op":"post_agent_coding","input":{"repo":"api","prompt":"x"}}`, true},
		"grouped, the envelope in another case":      {"agent", `{"Op":"create_agent_coding","Input":{"repo":"api"}}`, true},
		"flat, by its phrase":                        {"create_agent_coding", `{"repo":"api","prompt":"x"}`, true},
		"flat, by its id":                            {"post_agent_coding", `{"repo":"api","prompt":"x"}`, true},
		"another op in the same subsystem":           {"agent", `{"op":"list_agent_sessions","input":{}}`, false},
		"the code search a model reaches for first":  {"code", `{"op":"get_code_search","input":{"q":"Ship It"}}`, false},
		"arguments that will not parse":              {"agent", `{"op":`, false},
	} {
		t.Run(name, func(t *testing.T) {
			l := &live{}
			l.coded(tc.tool, tc.args, started, nil)
			if got := len(l.runs) == 1 && l.runs[0] == "sess_c"; got != tc.follows {
				t.Fatalf("followed %v, want %v (runs %v)", got, tc.follows, l.runs)
			}
		})
	}
}

// A refused call is said when no run started, and forgotten once one does: a
// model that read the candidates and asked again started the run it meant.
func TestARefusalIsSaidUntilARunStarts(t *testing.T) {
	const call = `{"op":"create_agent_coding","input":{"repo":"hanzo","prompt":"x"}}`
	refusal := errors.New(`coding: hanzoai has no repository named or serving "hanzo"; did you mean hanzo.ai?`)

	l := &live{}
	l.coded("agent", call, "", refusal)
	got := l.conclude(context.Background(), "I tried.")
	if !strings.HasPrefix(got, "I tried.\n\n") || !strings.Contains(got, "did not start") || !strings.Contains(got, "did you mean hanzo.ai") {
		t.Fatalf("the refusal was not said after the answer: %q", got)
	}

	l.coded("agent", call, "no json here", nil)
	if !strings.Contains(l.conclude(context.Background(), ""), "without naming its session") {
		t.Fatal("a 202 with no session is a refusal too, and was not said")
	}
}

// The run's steps are the turn's progress; its sandbox's chatter is not; its
// buttons are the message's while the turn follows it; its pull request is the
// last paragraph of the answer.
func TestTheTurnAnswersWithItsRunsPullRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		l, sto := following(t)
		l.runs = []string{"sess_code"}
		logged(t, sto, "sess_code", KindStatus, map[string]any{"status": "started", "repo": "hanzo.ai", "branch": "agent/abc"})
		logged(t, sto, "sess_code", KindToolCall, map[string]any{"step": "lease", "message": "leasing a dev sandbox", "status": "running"})
		logged(t, sto, "sess_code", KindLog, map[string]any{"message": "npm WARN deprecated"})
		logged(t, sto, "sess_code", KindToolCall, map[string]any{"step": "exit", "message": "exit 0"})
		logged(t, sto, "sess_code", KindStatus, map[string]any{"status": "asked", "question": "q1", "text": "Which hero?"})
		logged(t, sto, "sess_code", KindStatus, map[string]any{"status": "answered", "question": "q1", "answer": "Home"})
		logged(t, sto, "sess_code", KindToolCall, map[string]any{"step": "push", "message": "pushing agent/abc", "status": "running"})

		answer := concluding(t, l, "On it — started a run on hanzo.ai.", func() {
			logged(t, sto, "sess_code", KindStatus, map[string]any{"status": "done", "changed": true, "branch": "agent/abc",
				"url": "https://git.hanzo.ai/hanzoai/hanzo.ai/pulls/7"})
			if _, err := sto.End(context.Background(), "acme", "sess_code", StatusDone, time.Now().Unix()); err != nil {
				t.Fatal(err)
			}
		})

		if want := "On it — started a run on hanzo.ai.\n\nOpened a pull request for `agent/abc`: https://git.hanzo.ai/hanzoai/hanzo.ai/pulls/7"; answer != want {
			t.Fatalf("answer %q, want %q", answer, want)
		}
		lines := displayed(t, sto, client.TurnProgress)
		want := []string{"Working on hanzo.ai in agent/abc", "Leasing a dev sandbox",
			"Waiting for your answer", "Got your answer; carrying on", "Pushing agent/abc"}
		if strings.Join(lines, "|") != strings.Join(want, "|") {
			t.Fatalf("the turn reported %q, want the run's own steps %q", lines, want)
		}
		if runs := displayed(t, sto, client.TurnRun); len(runs) != 1 || runs[0] != "sess_code" {
			t.Fatalf("the turn named the run it follows as %v, want [sess_code]", runs)
		}

	})
}

// Every ending a run can have is said in words, and a run that ended without
// saying how is not reported as a success.
func TestEveryEndingIsSaid(t *testing.T) {
	for name, tc := range map[string]struct {
		last   any
		status string
		want   string
	}{
		"it failed": {map[string]any{"status": "error", "error": "coding: push: exit 1: remote: refusing to update refs/heads/agent/abc\nmore"},
			StatusError, "The coding run failed: push: exit 1: remote: refusing to update refs/heads/agent/abc"},
		"nothing to change": {map[string]any{"status": "done", "changed": false},
			StatusDone, "The coding run finished: nothing needed to change."},
		"pushed, no pull request": {map[string]any{"status": "done", "changed": true, "branch": "agent/abc", "prError": "forge: 422"},
			StatusDone, "Pushed `agent/abc`, but the pull request was not opened: forge: 422"},
		"stopped with its work kept": {map[string]any{"status": "stopped", "changed": true, "branch": "agent/abc"},
			StatusStopped, "The coding run stopped. Work so far is on `agent/abc`."},
		"ended without a word": {nil, StatusDone, "The coding run ended (done) without saying how (session sess_code)."},
	} {
		t.Run(name, func(t *testing.T) {
			sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
				l, sto := following(t)
				l.runs = []string{"sess_code"}
				if tc.last != nil {
					logged(t, sto, "sess_code", KindStatus, tc.last)
				}
				if _, err := sto.End(context.Background(), "acme", "sess_code", tc.status, time.Now().Unix()); err != nil {
					t.Fatal(err)
				}
				if got := l.conclude(context.Background(), ""); got != tc.want {
					t.Fatalf("said %q, want %q", got, tc.want)
				}

			})
		})
	}
}

// A run that pauses ends the turn with the run's Continue and Stop, and says
// where its work is.
func TestAPausedRunLeavesItsButtons(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		l, sto := following(t)
		l.runs = []string{"sess_code"}
		logged(t, sto, "sess_code", KindStatus, map[string]any{"status": "paused", "changed": true, "branch": "agent/abc"})
		if got := l.conclude(context.Background(), ""); got != "The coding run is paused. Work so far is on `agent/abc`." {
			t.Fatalf("a paused run was said as %q", got)
		}
		if paused := displayed(t, sto, client.TurnRunPaused); len(paused) != 1 || paused[0] != "sess_code" {
			t.Fatalf("the turn did not hand its message the paused run's buttons: %v", paused)
		}

	})
}

// A run redirected by a new instruction hands over to its follow-up, and the
// turn follows the follow-up to ITS end — still one message.
func TestTheTurnFollowsAFollowUp(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		l, sto := following(t)
		l.runs = []string{"sess_code"}
		plant(t, sto, "sess_next", StatusRunning, time.Now().Unix())
		logged(t, sto, "sess_code", KindStatus, map[string]any{"status": "stopped", "followed": true, "changed": true, "branch": "agent/abc"})
		if _, err := sto.End(context.Background(), "acme", "sess_code", StatusStopped, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}

		answer := concluding(t, l, "", func() {
			logged(t, sto, "sess_code", KindStatus, map[string]any{"status": "followed", "next": "sess_next"})
			logged(t, sto, "sess_next", KindStatus, map[string]any{"status": "done", "changed": true, "branch": "agent/def",
				"url": "https://git.example/pulls/8"})
			if _, err := sto.End(context.Background(), "acme", "sess_next", StatusDone, time.Now().Unix()); err != nil {
				t.Fatal(err)
			}
		})

		if !strings.Contains(answer, "https://git.example/pulls/8") {
			t.Fatalf("the turn did not follow the follow-up to its end: %q", answer)
		}
		if runs := displayed(t, sto, client.TurnRun); strings.Join(runs, ",") != "sess_code,sess_next" {
			t.Fatalf("the turn's buttons followed %v, want the run and then its follow-up", runs)
		}

	})
}

// A turn that is stopped stops the run it is following, which keeps its work:
// the run is what the person was watching.
func TestAStoppedTurnStopsItsRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		l, sto := following(t)
		l.runs = []string{"sess_code"}
		run, cancel := context.WithCancelCause(context.Background())
		done, ok := Hold("sess_code", cancel)
		if !ok {
			t.Fatal("could not hold the run")
		}
		t.Cleanup(done)

		ctx, stopTurn := context.WithCancelCause(context.Background())
		go func() { time.Sleep(30 * time.Millisecond); stopTurn(fmt.Errorf("%w: by its asker", ErrStopped)) }()
		got := l.conclude(ctx, "")

		if !strings.Contains(got, "stopped with the turn") {
			t.Fatalf("the turn said %q", got)
		}
		if !errors.Is(context.Cause(run), ErrStopped) {
			t.Fatalf("the run was not stopped with the turn: its cause is %v", context.Cause(run))
		}
		if s := statusOf(t, sto, "acme", "sess_code"); s != StatusStopped {
			t.Fatalf("the run's session is %q, want stopped", s)
		}

	})
}

// A payload the turn cannot read changes nothing: the run has pushed a branch and
// must not be disturbed by a line this cannot parse.
func TestNarrateSurvivesAHostilePayload(t *testing.T) {
	for _, payload := range []string{"", "not json", "[]", `{"step":123}`, `{`} {
		for _, kind := range []string{KindStatus, KindToolCall, KindLog} {
			if got := narrate(Event{Kind: kind, Payload: payload}); got != (told{}) {
				t.Errorf("%s %q rendered %+v; it must change nothing", kind, payload, got)
			}
		}
	}
}

// The seam, through the real tool loop: the model asks for a coding run inside a
// turn, the call names the turn so the run posts nothing of its own, and the turn
// is now following the run the plane admitted.
func TestADispatchedRunIsFollowed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		plane := &fakePlane{offer: []infer.ToolDef{toolDef("agent")}, out: `{"sessionId":"sess_1","branch":"agent/abc","repo":"api"}`}
		withPlane(t, plane)
		ai := &scriptAI{replies: []infer.ChatResponse{
			{ToolCalls: []infer.ToolCall{{ID: "c1", Name: "agent",
				Arguments: `{"op":"create_agent_coding","input":{"repo":"api","prompt":"fix the auth test"}}`}},
				FinishReason: "tool_calls"},
			{Content: "Started."},
		}}
		a := mk("acme", "hanzo")
		a.Tools = []string{"agent"}
		l, _ := following(t)
		ctx := withReply(withLive(context.Background(), l), thread, client.Origin{})
		r := executeRun(ctx, ai, "acme", "acme/u1", a, "fix the auth test in api", nil, "", "run_test")
		if r.Status != "ok" {
			t.Fatalf("want ok, got %q err=%q", r.Status, r.Error)
		}
		if len(l.runs) != 1 || l.runs[0] != "sess_1" {
			t.Fatalf("the turn follows %v, want the run the plane admitted", l.runs)
		}
		var env struct {
			Input client.CodingStartIn `json:"input"`
		}
		if err := json.Unmarshal([]byte(plane.calls[0].args), &env); err != nil {
			t.Fatal(err)
		}
		if env.Input.Reply.Turn != "sess_turn" || env.Input.Repo != "api" {
			t.Fatalf("the run was dispatched as %+v; it must name the turn that follows it", env.Input)
		}

	})
}

// A turn that followed a coding run ended because its harness cannot renew its model credential says
// that, and not that the time ran out: the run had hours left and the credential did not.
//
// MUTATION: leave "unrenewable" unread and the turn tells its person the run used its time.
func TestATurnReadsARunThatCouldNotRenewItsCredentialAsThatAndNotAsTime(t *testing.T) {
	got := narrate(Event{Kind: KindStatus, Payload: `{"status":"stopped","unrenewable":true,"timeUp":false,"changed":true,"branch":"agent/abc"}`})
	want := "The coding run ended when its model credential was about to: its harness cannot renew it. Work so far is on `agent/abc`. Start a new run with `after` set to this session to carry on."
	if got.end != want {
		t.Fatalf("narrate = %q, want %q", got.end, want)
	}
	if time := narrate(Event{Kind: KindStatus, Payload: `{"status":"stopped","timeUp":true,"changed":true,"branch":"agent/abc"}`}); !strings.Contains(time.end, "used its time") {
		t.Fatalf("a run out of time reads %q", time.end)
	}
}
