package agents

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
)

// turnSession plants a live session that is a turn's view of a run.
func turnSession(t *testing.T, sto *Store, id, actor, run string) {
	t.Helper()
	now := time.Now().Unix()
	if err := sto.CreateSession(context.Background(), Session{
		ID: id, Org: "acme", Agent: "hanzo", Actor: actor, Status: StatusRunning,
		RootID: id, Run: run, StartedAt: now, CreatedAt: now, UpdatedAt: now, MeteredAt: now,
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

// A stop reaches the run executing the session: its context is cancelled with
// ErrStopped as the cause, and the session ends stopped rather than failed.
func TestStopCancelsTheRunAndEndsStopped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		st := storeOf(t, &mounted.State, "acme")
		turnSession(t, st, "sess_a", "acme/alice", "run_a")
		ctx, cancel := context.WithCancelCause(context.Background())
		hold("sess_a", cancel)
		t.Cleanup(func() { release("sess_a") })

		n, err := StopSessions(context.Background(), "acme", SessionMatch{Actor: "acme/alice", Session: "sess_a"})
		if err != nil || n != 1 {
			t.Fatalf("stop: n=%d err=%v", n, err)
		}
		if !errors.Is(context.Cause(ctx), ErrStopped) {
			t.Fatalf("run context cause is %v, want ErrStopped", context.Cause(ctx))
		}
		if s := statusOf(t, st, "acme", "sess_a"); s != StatusStopped {
			t.Fatalf("session is %q, want stopped", s)
		}

	})
}

// Naming a session narrows the match and never widens it: someone else's
// session is not stopped because its id was named.
func TestNamingASessionNeverWidensTheMatch(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		st := storeOf(t, &mounted.State, "acme")
		turnSession(t, st, "sess_bob", "acme/bob", "run_b")
		turnSession(t, st, "sess_alice", "acme/alice", "run_a")

		n, err := StopSessions(context.Background(), "acme", SessionMatch{Actor: "acme/alice", Session: "sess_bob"})
		if err != nil || n != 0 {
			t.Fatalf("stop named another's session: n=%d err=%v", n, err)
		}
		if s := statusOf(t, st, "acme", "sess_bob"); s != StatusRunning {
			t.Fatalf("bob's session is %q, want running", s)
		}
		if s := statusOf(t, st, "acme", "sess_alice"); s != StatusRunning {
			t.Fatalf("alice's other session is %q; naming one must not stop the rest", s)
		}

	})
}

// The steering API's stop is the same stop.
func TestSteeringStopIsTheSameStop(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		st := storeOf(t, &mounted.State, "acme")
		turnSession(t, st, "sess_s", "acme/alice", "run_s")
		ctx, cancel := context.WithCancelCause(context.Background())
		hold("sess_s", cancel)
		t.Cleanup(func() { release("sess_s") })

		x, _ := st.GetSession(context.Background(), "acme", "sess_s")
		if _, _, err := stop(context.Background(), st, x, reasonOf("")); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(context.Cause(ctx), ErrStopped) || statusOf(t, st, "acme", "sess_s") != StatusStopped {
			t.Fatalf("steering stop: cause=%v status=%q", context.Cause(ctx), statusOf(t, st, "acme", "sess_s"))
		}

	})
}

// A session ends once. A stop and a finishing run race; whichever lands first
// is the answer and the other changes nothing.
func TestASessionEndsOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, sto := meterStore(t)
		turnSession(t, sto, "sess_e", "acme/z", "run_e")
		ctx := context.Background()
		first, err := sto.End(ctx, "acme", "sess_e", StatusDone, time.Now().Unix())
		if err != nil || !first {
			t.Fatalf("first end: %v %v", first, err)
		}
		second, err := sto.End(ctx, "acme", "sess_e", StatusStopped, time.Now().Unix())
		if err != nil || second {
			t.Fatalf("second end changed a terminal session: %v %v", second, err)
		}
		if x := reload(t, sto, "sess_e"); x.Status != StatusDone {
			t.Fatalf("status %q, want done", x.Status)
		}
		if _, err := sto.End(ctx, "acme", "sess_e", StatusRunning, 1); err == nil {
			t.Fatal("End accepted a status that is not terminal")
		}

	})
}

// A run's session is billed through the run, never for its runtime: the meter
// does not see it, however long it is open.
func TestARunSessionIsNotMeteredForRuntime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		now := time.Now().Unix()
		if err := sto.CreateSession(context.Background(), Session{
			ID: "sess_run", Org: "acme", Agent: "hanzo", Actor: "acme/z", Status: StatusRunning,
			RootID: "sess_run", Run: "run_x", Payer: "runpayer",
			StartedAt: now, CreatedAt: now, UpdatedAt: now, MeteredAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		plant(t, sto, "sess_plain", StatusRunning, now)
		b := &books{}
		sweep(t, st, now+3*hour, cloud.RuntimeHourMicros, b)

		for _, p := range b.payer {
			if p == "runpayer" {
				t.Fatal("a run's session was billed for runtime")
			}
		}
		if b.len() == 0 {
			t.Fatal("the plain session was not billed; the meter did not run")
		}

	})
}

// A run's session whose run is not executing here is an orphan: its run died
// with an earlier process, so the sweep ends it rather than letting it sit live.
// One whose run is held here is left alone.
func TestTheSweepEndsOrphanedTurns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		turnSession(t, sto, "sess_dead", "acme/z", "run_dead")
		turnSession(t, sto, "sess_live", "acme/z", "run_live")
		_, cancel := context.WithCancelCause(context.Background())
		hold("sess_live", cancel)
		t.Cleanup(func() { release("sess_live") })

		reap(context.Background(), st, &record{}, testClocks)

		if x := reload(t, sto, "sess_dead"); x.Status != StatusError {
			t.Fatalf("orphan is %q, want error", x.Status)
		}
		if x := reload(t, sto, "sess_live"); x.Status != StatusRunning {
			t.Fatalf("a turn still running here was ended: %q", x.Status)
		}

	})
}

// append writes one event of kind with payload.
func appendEvent(t *testing.T, sto *Store, id, kind string, payload any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	if _, err := sto.AppendEvent(context.Background(), Event{
		ID: "evt_" + kind + time.Now().Format("150405.000000000"), SessionID: id, Org: "acme",
		Kind: kind, Payload: string(b), CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

// A surface reads progress lines and the answer, in order, and the outcome
// only once it has read everything.
func TestReadingATurn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, sto := meterStore(t)
		ctx := context.Background()
		turnSession(t, sto, "sess_r", "acme/z", "run_r")
		appendEvent(t, sto, "sess_r", KindProgress, map[string]string{"phase": "running", "activity": "Looking at the repository"})
		appendEvent(t, sto, "sess_r", KindToolCall, map[string]string{"args": "secret-ish"})

		out, err := readTurn(ctx, sto, "acme", "sess_r", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Events) != 1 || out.Events[0].Kind != client.TurnProgress || out.Events[0].Text != "Looking at the repository" {
			t.Fatalf("events %+v, want the one progress line and nothing from the tool call", out.Events)
		}
		if out.Outcome != "" {
			t.Fatalf("a running turn has outcome %q", out.Outcome)
		}

		appendEvent(t, sto, "sess_r", KindMessage, map[string]string{"text": "Done: 3 repos."})
		appendEvent(t, sto, "sess_r", KindLog, map[string]string{"error": ""})
		if _, err := sto.End(ctx, "acme", "sess_r", StatusDone, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		out, err = readTurn(ctx, sto, "acme", "sess_r", out.Next)
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Events) != 1 || out.Events[0].Kind != client.TurnAnswer || out.Events[0].Text != "Done: 3 repos." {
			t.Fatalf("events after the first read %+v, want just the answer", out.Events)
		}
		if out.Outcome != client.TurnCompleted {
			t.Fatalf("outcome %q, want completed", out.Outcome)
		}

	})
}

// A failed turn says why; a stopped one says stopped.
func TestTurnOutcomes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, sto := meterStore(t)
		ctx := context.Background()
		turnSession(t, sto, "sess_f", "acme/z", "run_f")
		appendEvent(t, sto, "sess_f", KindLog, map[string]string{"error": "model timed out"})
		_, _ = sto.End(ctx, "acme", "sess_f", StatusError, time.Now().Unix())
		out, _ := readTurn(ctx, sto, "acme", "sess_f", 0)
		if out.Outcome != client.TurnFailed || out.Error != "model timed out" {
			t.Fatalf("failed turn: %+v", out)
		}

		turnSession(t, sto, "sess_st", "acme/z", "run_st")
		_, _ = sto.End(ctx, "acme", "sess_st", StatusStopped, time.Now().Unix())
		out, _ = readTurn(ctx, sto, "acme", "sess_st", 0)
		if out.Outcome != client.TurnStopped || out.Error != "" {
			t.Fatalf("stopped turn: %+v", out)
		}

	})
}

// A coding run says how it ended in its last status, so a caller following it on
// this feed — an automation's agent step — ends with its answer, a plan run's
// plan, or its failure.
func TestReadingACodingRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, sto := meterStore(t)
		ctx := context.Background()
		for _, c := range []struct {
			id, status string
			payload    map[string]any
			answer     string
			outcome    string
			why        string
		}{
			{"sess_cd", StatusDone, map[string]any{"status": "done", "answer": "Three things need you today."}, "Three things need you today.", client.TurnCompleted, ""},
			{"sess_cp", StatusDone, map[string]any{"status": "done", "plan": "1. Draft the reply."}, "1. Draft the reply.", client.TurnCompleted, ""},
			{"sess_ce", StatusError, map[string]any{"status": "error", "error": "coding: the sandbox did not start"}, "", client.TurnFailed, "coding: the sandbox did not start"},
		} {
			turnSession(t, sto, c.id, "acme/z", "run_"+c.id)
			appendEvent(t, sto, c.id, KindStatus, map[string]any{"status": "started", "repo": "site"})
			appendEvent(t, sto, c.id, KindStatus, c.payload)
			if _, err := sto.End(ctx, "acme", c.id, c.status, time.Now().Unix()); err != nil {
				t.Fatal(err)
			}
			out, err := readTurn(ctx, sto, "acme", c.id, 0)
			if err != nil {
				t.Fatal(err)
			}
			answer := ""
			for _, e := range out.Events {
				if e.Kind == client.TurnAnswer {
					answer = e.Text
				}
			}
			if answer != c.answer || out.Outcome != c.outcome || out.Error != c.why {
				t.Errorf("%s: answer %q outcome %q error %q", c.id, answer, out.Outcome, out.Error)
			}
		}

		// Trailing non-error KindLog events must not wipe a prior error, and reading
		// from an `after` cursor past the error event must fall back to the session's
		// stored error instead of returning an empty error string.
		turnSession(t, sto, "sess_trail", "acme/z", "run_trail")
		appendEvent(t, sto, "sess_trail", KindStatus, map[string]any{"status": "error", "error": "coding: run: exec: context deadline exceeded"})
		appendEvent(t, sto, "sess_trail", KindLog, map[string]any{"step": "kept", "message": "sandbox kept"})
		if _, err := sto.End(ctx, "acme", "sess_trail", StatusError, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		out, err := readTurn(ctx, sto, "acme", "sess_trail", 0)
		if err != nil {
			t.Fatal(err)
		}
		if out.Outcome != client.TurnFailed || out.Error != "coding: run: exec: context deadline exceeded" {
			t.Fatalf("trailing log wiped error: %+v", out)
		}
		outAfter, err := readTurn(ctx, sto, "acme", "sess_trail", out.Next)
		if err != nil {
			t.Fatal(err)
		}
		if outAfter.Outcome != client.TurnFailed || outAfter.Error != "coding: run: exec: context deadline exceeded" {
			t.Fatalf("cursor past error event lost session error: %+v", outAfter)
		}

	})
}

// Waiting on a turn returns as soon as it says something, not at the timeout.
func TestWaitingReturnsWhenTheTurnSpeaks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		s := &cloud.Service[state]{Log: luxlog.New("test"), State: *st}
		s.State.bus = newBus()
		turnSession(t, sto, "sess_w", "acme/z", "run_w")
		hold("sess_w", func(error) {}) // a turn's run is held by the process running it
		t.Cleanup(func() { release("sess_w") })
		l := &live{s: s, x: reload(t, sto, "sess_w"), run: "run_w"}

		go func() {
			time.Sleep(100 * time.Millisecond)
			l.doing(context.Background(), "git")
		}()
		start := time.Now()
		out, err := waitTurn(context.Background(), s, sto, "acme", "sess_w", 0)
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(start) > 5*time.Second || len(out.Events) != 1 {
			t.Fatalf("waited %v for %d events", time.Since(start), len(out.Events))
		}

	})
}

// A progress line never carries a tool's arguments: it names the tool, in words
// where there are some, and model text that is not prose is not shown at all.
func TestProgressShowsNoPayload(t *testing.T) {
	if got := toolLine("git"); got != "Looking at the repository" {
		t.Fatalf("git: %q", got)
	}
	if got := toolLine("list_git_repos"); got != "Using list git repos" {
		t.Fatalf("plain name: %q", got)
	}
	if got := toolLine(`{"op":"x"}`); got != "Working" {
		t.Fatalf("a name that is not an identifier was shown: %q", got)
	}
	for _, s := range []string{`{"op":"list"}`, "```bash\nrm -rf\n```", `<tool_call>{}</tool_call>`, `[1,2]`} {
		if !markup.MatchString(s) {
			t.Fatalf("model markup treated as prose: %q", s)
		}
	}
	if markup.MatchString("I'll check the repository, then open a PR.") {
		t.Fatal("prose treated as markup")
	}
}

// The same line twice is reported once.
func TestARepeatedLineIsReportedOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		s := &cloud.Service[state]{Log: luxlog.New("test"), State: *st}
		turnSession(t, sto, "sess_d", "acme/z", "run_d")
		l := &live{s: s, x: reload(t, sto, "sess_d"), run: "run_d"}
		for range 3 {
			l.doing(context.Background(), "git")
		}
		out, _ := readTurn(context.Background(), sto, "acme", "sess_d", 0)
		if len(out.Events) != 1 {
			t.Fatalf("%d events for one repeated line", len(out.Events))
		}

	})
}

// A turn outlives the store handle it started with: an idle store is evicted and
// a promoted one replaced, each by closing the handle. The turn's answer and its
// end still land, because each write resolves the store as it is now.
func TestATurnEndsThroughAStoreReplacedUnderIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		stores := func() *cloud.OrgStore[*Store] {
			c := cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)
			t.Cleanup(func() { _ = c.CloseAll() })
			return c
		}
		s := &cloud.Service[state]{Log: luxlog.New("test"), State: state{stores: stores()}}
		sto := storeOf(t, &s.State, "acme")
		turnSession(t, sto, "sess_e", "acme/z", "run_e")
		l := &live{s: s, x: reload(t, sto, "sess_e"), run: "run_e"}

		_ = s.State.stores.CloseAll() // the handle the turn started with is gone
		s.State.stores = stores()

		l.finish(context.Background(), Run{Status: "ok", Output: "the answer"}, nil)
		now := storeOf(t, &s.State, "acme")
		if got := reload(t, now, "sess_e").Status; got != StatusDone {
			t.Fatalf("the turn answered and reads %q, want done", got)
		}
		out, _ := readTurn(context.Background(), now, "acme", "sess_e", 0)
		if len(out.Events) != 1 || out.Events[0].Text != "the answer" || out.Outcome != client.TurnCompleted {
			t.Fatalf("the answer did not land: %+v", out)
		}

	})
}

// A turn whose run is not executing anywhere — its process went away mid-run —
// is ended the moment its surface asks, with the reason, rather than reading as
// working until the sweep.
func TestAnOrphanedTurnEndsWhenItsSurfaceAsks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		s := &cloud.Service[state]{Log: luxlog.New("test"), State: *st}
		turnSession(t, sto, "sess_o", "acme/z", "run_o")

		out, err := waitTurn(context.Background(), s, sto, "acme", "sess_o", 0)
		if err != nil {
			t.Fatal(err)
		}
		if out.Outcome != client.TurnFailed || out.Error != orphanReason {
			t.Fatalf("an orphaned turn read %+v, want failed with the reason", out)
		}
		if got := reload(t, sto, "sess_o").Status; got != StatusError {
			t.Fatalf("the orphan reads %q, want error", got)
		}

		// A turn whose run IS executing here is left alone.
		turnSession(t, sto, "sess_h", "acme/z", "run_h")
		hold("sess_h", func(error) {})
		t.Cleanup(func() { release("sess_h") })
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, _ = waitTurn(ctx, s, sto, "acme", "sess_h", 0)
		if got := reload(t, sto, "sess_h").Status; got != StatusRunning {
			t.Fatalf("a held turn reads %q, want running", got)
		}

	})
}
