package agents

// resume_test.go — a stopped or paused turn keeps its sandbox, and resuming it
// runs the agent again in that same sandbox, with every file it had written.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/zap-proto/zip"
)

// heeding is a scripted model that answers nothing on a context that is done, as
// every real one does — which is what ends a run that was stopped mid-call.
type heeding struct{ *scriptAI }

func (h heeding) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return h.scriptAI.ChatCompletion(ctx, req)
}

// halfDone is a turn that writes a file and then works until it is stopped,
// followed by the turn that picks it up: it reads the file back and answers.
func halfDone() *scriptAI {
	return &scriptAI{replies: []infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{toolCall("c1", opWrite, `{"path":"notes.md","data":"half done"}`)}},
		{ToolCalls: []infer.ToolCall{toolCall("c2", opRun, `{"command":"make all"}`)}},
		{ToolCalls: []infer.ToolCall{toolCall("c3", opRead, `{"path":"notes.md"}`)}},
		{Content: "finished"},
	}}
}

// halfway mounts the agents app over a fake sandbox whose files live on its
// disk, with boxer as the agent a turn runs, and starts a turn as u-acme — the
// user do() calls as — in a Slack thread. It returns once the turn has written
// its file and is working.
func halfway(t *testing.T, ai *scriptAI) (*zip.App, *sandboxes, *Store, *client.TurnStarted) {
	t.Helper()
	sb := serveSandboxes(t)
	working := make(chan struct{}, 4)
	withPlane(t, sb.disk(working))
	app := mountApp(t, heeding{ai})
	sto := storeOf(t, &mounted.State, "acme")
	if err := sto.Create(context.Background(), boxer()); err != nil {
		t.Fatal(err)
	}
	st, err := startTurn(mounted, context.Background(), "acme", &client.RunOnBehalfIn{
		Subject: "u-acme", Ref: "boxer", Input: "build it",
		Reply: client.Reply{Provider: "slack", Channel: "C1", Thread: "T1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-working
	return app, sb, sto, st
}

// finished waits for the turn to end and says how.
func finished(t *testing.T, sto *Store, id string) string {
	t.Helper()
	waitFor(func() bool { return isTerminalStatus(statusOf(t, sto, "acme", id)) })
	return statusOf(t, sto, "acme", id)
}

// readBack is what the resumed run was handed for its read of notes.md, and the
// sandbox that read went to.
func readBack(t *testing.T, ai *scriptAI) string {
	t.Helper()
	if len(ai.seen) < 4 {
		t.Fatalf("the resumed run asked the model %d times, want 4", len(ai.seen))
	}
	for _, m := range ai.seen[3].Messages {
		if m.Role == infer.RoleTool && m.ToolCallID == "c3" {
			return m.Content
		}
	}
	t.Fatal("the resumed run never handed the model its read")
	return ""
}

// STOP, then RESUME: the stop parks the box and the session records it; the
// resume unparks that box, and the next sandbox call is made in it, where the file
// written before the stop still is. Completing then gives it back.
func TestResumingAStoppedTurnWorksInTheSameSandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := halfDone()
		app, sb, sto, st := halfway(t, ai)

		if code, b := do(t, app, http.MethodPost, "/v1/agent/sessions/"+st.SessionID+"/stop", "acme", nil); code != http.StatusOK {
			t.Fatalf("stop: %d %s", code, b)
		}
		if !waitFor(func() bool { return sb.isParked("m_1") }) {
			t.Fatal("the stop did not park the turn's sandbox")
		}
		if x := reload(t, sto, st.SessionID); x.Status != StatusStopped || x.Box != "m_1" {
			t.Fatalf("stopped session %+v, want stopped and holding m_1", x)
		}
		waitFor(func() bool { return !busy(st.SessionID) })

		code, b := do(t, app, http.MethodPost, "/v1/agent/sessions/"+st.SessionID+"/resume", "acme",
			map[string]any{"message": "carry on"})
		if code != http.StatusOK {
			t.Fatalf("resume: %d %s", code, b)
		}
		if got := finished(t, sto, st.SessionID); got != StatusDone {
			t.Fatalf("the resumed turn ended %q, want done", got)
		}
		if got := readBack(t, ai); got != "half done" {
			t.Fatalf("the resumed run read %q; the file written before the stop is gone", got)
		}
		sb.mu.Lock()
		minted, resumed := sb.minted, append([]string(nil), sb.resumed...)
		sb.mu.Unlock()
		if minted != 1 || len(resumed) != 1 || resumed[0] != "m_1" {
			t.Fatalf("minted %d and resumed %v: the resumed run must unpark m_1 and lease nothing new", minted, resumed)
		}
		if !waitFor(func() bool { return sb.ended("m_1") }) {
			t.Fatal("the resumed turn completed and kept its sandbox")
		}

	})
}

// PAUSE ends the run here as a stop does and parks the box the same way, but the
// session stays live, paused, and a surface is told so. RESUME goes on in the box.
func TestPausingATurnParksItsSandboxAndResumeGoesOn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := halfDone()
		app, sb, sto, st := halfway(t, ai)

		if code, b := do(t, app, http.MethodPost, "/v1/agent/sessions/"+st.SessionID+"/pause", "acme", nil); code != http.StatusOK {
			t.Fatalf("pause: %d %s", code, b)
		}
		if !waitFor(func() bool { return sb.isParked("m_1") }) {
			t.Fatal("the pause did not park the turn's sandbox")
		}
		waitFor(func() bool { return !busy(st.SessionID) })
		if x := reload(t, sto, st.SessionID); x.Status != StatusPaused || x.Box != "m_1" {
			t.Fatalf("paused session %+v, want paused and holding m_1", x)
		}
		out, err := readTurn(context.Background(), sto, "acme", st.SessionID, 0)
		if err != nil || out.Outcome != client.TurnPaused {
			t.Fatalf("a surface reading the paused turn got %+v %v, want outcome paused", out, err)
		}

		if code, b := do(t, app, http.MethodPost, "/v1/agent/sessions/"+st.SessionID+"/resume", "acme", nil); code != http.StatusOK {
			t.Fatalf("resume: %d %s", code, b)
		}
		if got := finished(t, sto, st.SessionID); got != StatusDone {
			t.Fatalf("the resumed turn ended %q, want done", got)
		}
		if got := readBack(t, ai); got != "half done" {
			t.Fatalf("the resumed run read %q; the file written before the pause is gone", got)
		}

	})
}

// "continue" in the conversation a stopped turn was in resumes THAT turn — the
// same session, in the same sandbox — and anywhere else it is a new turn.
func TestContinueInTheConversationResumesTheStoppedTurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := halfDone()
		_, sb, sto, st := halfway(t, ai)
		if n, err := StopSessions(context.Background(), "acme", SessionMatch{Actor: billingActor("acme", "u-acme"), Session: st.SessionID}); err != nil || n != 1 {
			t.Fatalf("stop: n=%d err=%v", n, err)
		}
		if !waitFor(func() bool { return sb.isParked("m_1") }) {
			t.Fatal("the stop did not park the turn's sandbox")
		}
		waitFor(func() bool { return !busy(st.SessionID) })

		again, err := startTurn(mounted, context.Background(), "acme", &client.RunOnBehalfIn{
			Subject: "u-acme", Ref: "boxer", Input: "continue", Resume: true,
			Reply: client.Reply{Provider: "slack", Channel: "C1", Thread: "T1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if again.SessionID != st.SessionID || again.RunID == st.RunID {
			t.Fatalf("continue started %+v; want the stopped session %s under a new run", again, st.SessionID)
		}
		if got := finished(t, sto, st.SessionID); got != StatusDone {
			t.Fatalf("the resumed turn ended %q, want done", got)
		}
		if got := readBack(t, ai); got != "half done" {
			t.Fatalf("the resumed run read %q; the file written before the stop is gone", got)
		}

		// Another thread has nothing stopped in it: "continue" there is a new turn.
		ai.replies = append(ai.replies, infer.ChatResponse{Content: "hello"})
		other, err := startTurn(mounted, context.Background(), "acme", &client.RunOnBehalfIn{
			Subject: "u-acme", Ref: "boxer", Input: "continue", Resume: true,
			Reply: client.Reply{Provider: "slack", Channel: "C1", Thread: "T2"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if other.SessionID == st.SessionID {
			t.Fatal("continue in another thread resumed this one")
		}
		finished(t, sto, other.SessionID)

	})
}

// asUser calls the app as one user of one org.
func asUser(t *testing.T, app *zip.App, path, org, user string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", user)
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// A turn is resumed by the person whose turn it is, in the org it ran in. Another
// org does not see the session at all, and neither does another member of the org
// (visible), so neither may run the agent as its owner — and neither refusal
// touches the parked sandbox.
func TestOnlyItsOwnerResumesATurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, sb, sto, st := halfway(t, halfDone())
		if n, err := StopSessions(context.Background(), "acme", SessionMatch{Actor: billingActor("acme", "u-acme"), Session: st.SessionID}); err != nil || n != 1 {
			t.Fatalf("stop: n=%d err=%v", n, err)
		}
		waitFor(func() bool { return sb.isParked("m_1") && !busy(st.SessionID) })

		path := "/v1/agent/sessions/" + st.SessionID + "/resume"
		if code := asUser(t, app, path, "evil", "u-evil"); code != http.StatusNotFound {
			t.Fatalf("another org's resume answered %d, want 404", code)
		}
		if code := asUser(t, app, path, "acme", "u-other"); code != http.StatusNotFound {
			t.Fatalf("another member's resume answered %d, want 404", code)
		}
		if x := reload(t, sto, st.SessionID); x.Status != StatusStopped {
			t.Fatalf("a refused resume moved the session to %q", x.Status)
		}
		sb.mu.Lock()
		resumed := len(sb.resumed)
		sb.mu.Unlock()
		if resumed != 0 || !sb.isParked("m_1") {
			t.Fatal("a refused resume unparked the sandbox")
		}

	})
}

// A paused turn has no run anywhere, by design — that is what paused means — so
// the sweep that ends a run's session whose run is not here must leave it alone.
func TestAPausedTurnIsNotAnOrphan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		turnSession(t, sto, "sess_p", "acme/z", "run_p")
		if ok, err := sto.Pause(context.Background(), "acme", "sess_p", 1); err != nil || !ok {
			t.Fatalf("pause: %v %v", ok, err)
		}
		reap(context.Background(), st, &record{}, testClocks)
		if x := reload(t, sto, "sess_p"); x.Status != StatusPaused {
			t.Fatalf("the sweep ended a paused turn as an orphan: %q", x.Status)
		}

	})
}

// busy reports whether a run of session id is still executing here.
func busy(id string) bool {
	running.Lock()
	defer running.Unlock()
	_, ok := running.m[id]
	return ok
}

// A pause that reaches a run which had already answered does not lose the answer:
// the run finished, and it ends done. One that stopped it mid-work leaves it live.
func TestAPauseDoesNotEatAFinishedAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		s := runService(t, nil)
		s.State = *st
		for _, c := range []struct {
			id   string
			r    Run
			err  error
			want string
		}{
			{"sess_done", Run{Status: "ok", Output: "the answer"}, nil, StatusDone},
			{"sess_mid", Run{Status: "error", Error: "paused"}, nil, StatusRunning},
		} {
			turnSession(t, sto, c.id, "acme/z", "run_"+c.id)
			l := &live{s: s, x: reload(t, sto, c.id), run: "run_" + c.id}
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(ErrPaused)
			l.finish(ctx, c.r, c.err)
			if got := reload(t, sto, c.id).Status; got != c.want {
				t.Fatalf("%s: a paused run that ended %q left its session %q, want %q", c.id, c.r.Status, got, c.want)
			}
		}

	})
}
