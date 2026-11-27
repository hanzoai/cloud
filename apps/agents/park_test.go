package agents

// park_test.go — a coding run waiting for a limit to clear is a session that is paused,
// ended as far as every sweep goes, holding no sandbox, and saying why. One update parks
// it and one update puts it back, each won by exactly one caller, on both engines.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// parkedSession opens a running coding session holding a sandbox, the shape a run has
// when its model API refuses it.
func parkedSession(t *testing.T, id string) (*Store, Session) {
	t.Helper()
	mountInproc(t)
	sto := storeOf(t, &mounted.State, "acme")
	x := mkSession("acme", id, "", id)
	x.Kind, x.Box, x.Branch, x.MeteredAt = "coding", "sbx_1", "agent/abc", x.StartedAt
	if err := sto.CreateSession(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	return sto, x
}

// A run parks in ONE update: paused, its end written so no sweep, meter or reaper counts
// it live, no sandbox, and the reason and time it waits on. It reads parked everywhere a
// session is read: the view says why and when, and its progress is the run's own word,
// which no model estimates.
//
// MUTATION: park without writing its end and it is live to the sweeps (billed, reaped at
// the day's ceiling); park without clearing the sandbox and it names a box that is gone.
func TestAParkedRunIsPausedEndedAndSaysWhy(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto, x := parkedSession(t, "sess_p1")
		ctx := context.Background()
		at := time.Now().Add(6 * time.Hour).Unix()

		ok, err := ParkSession(ctx, "acme", x.ID, ParkLimit, at)
		if err != nil || !ok {
			t.Fatalf("ParkSession = (%v, %v), want it parked", ok, err)
		}
		got, _ := sto.GetSession(ctx, "acme", x.ID)
		if got.Status != StatusPaused || got.Park != ParkLimit || got.ParkAt != at || got.EndedAt == 0 || got.Box != "" || !got.Parked() {
			t.Fatalf("parked session = %+v, want paused, ended, parked for the limit at %d, with no sandbox", got, at)
		}
		live, err := sto.Live(ctx, "acme")
		if err != nil || len(live) != 0 {
			t.Fatalf("Live = %d sessions (%v), want none: nothing runs in a parked session", len(live), err)
		}
		if quiet, err := sto.Quiet(ctx, "acme"); err != nil || !quiet {
			t.Fatalf("Quiet = %v (%v), want an org with only parked runs to be quiet", quiet, err)
		}
		if over, _ := newClocks().over(got, time.Now().Add(72*time.Hour)); over {
			t.Fatal("the reaper's ceiling ends a parked run, which has no run to bound")
		}

		v := toSessionView(got, 0, 0)
		if v.Park == nil || v.Park.Reason != ParkLimit || v.Park.ResumeAt != time.Unix(at, 0).UTC().Format(time.RFC3339) ||
			!strings.HasPrefix(v.Park.Message, "parked: usage limit, goes on at ") ||
			!strings.Contains(v.Park.Message, " or when credits are added; after 24 hours, on Continue") {
			t.Fatalf("the view says %+v", v.Park)
		}
		if v.Progress.Phase != phaseBlocked || v.Progress.Activity != v.Park.Message || v.Progress.Estimated {
			t.Fatalf("progress = %+v, want blocked, the run's own sentence, not estimated", v.Progress)
		}
		if newEstimator(&progressAI{}).stale(got, time.Now().Unix()+3600) {
			t.Fatal("a model is asked to estimate a parked run")
		}

		// A payer's refusal names no time.
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkPayment, 0); err != nil || !ok {
			t.Fatalf("a parked run parks again with another reason: (%v, %v)", ok, err)
		}
		again, _ := sto.GetSession(ctx, "acme", x.ID)
		if again.Park != ParkPayment || again.ParkAt != 0 || again.EndedAt != got.EndedAt {
			t.Fatalf("re-parked = %+v, want payment, no time, the first end kept", again)
		}
		if w := toSessionView(again, 0, 0).Park; w == nil || w.ResumeAt != "" || w.Message != "parked: payment refused, goes on when credits are added; after 24 hours, on Continue" {
			t.Fatalf("a payer's refusal reads %+v", w)
		}
	})
}

// A parked run goes back to running in ONE update that only one of two tries wins, and
// the wait it spent is neither a life nor a charge: its clock and its meter start again.
//
// MUTATION: Unpark without the status guard and both tries launch the run.
func TestAParkedRunIsUnparkedOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto, x := parkedSession(t, "sess_p2")
		ctx := context.Background()
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkLimit, 0); err != nil || !ok {
			t.Fatal(ok, err)
		}
		// The session was parked an hour ago, as far as its clocks go.
		if _, err := sto.db.Exec(`UPDATE agent_sessions SET started_at = started_at - 3600, ended_at = ended_at - 3600, metered_at = metered_at - 3600 WHERE id = $1`, x.ID); err != nil {
			t.Fatal(err)
		}
		before, _ := sto.GetSession(ctx, "acme", x.ID)

		var won atomic.Int32
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if ok, err := UnparkSession(ctx, "acme", x.ID); err != nil {
					t.Error(err)
				} else if ok {
					won.Add(1)
				}
			}()
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d tries unparked the run, want exactly one", won.Load())
		}
		got, _ := sto.GetSession(ctx, "acme", x.ID)
		if got.Status != StatusRunning || got.Park != "" || got.ParkAt != 0 || got.EndedAt != 0 || got.Parked() {
			t.Fatalf("unparked session = %+v, want running, not parked, live", got)
		}
		if got.StartedAt <= before.StartedAt || got.MeteredAt <= before.MeteredAt {
			t.Fatalf("started_at %d→%d, metered_at %d→%d: the wait is a life and a charge", before.StartedAt, got.StartedAt, before.MeteredAt, got.MeteredAt)
		}
		if ok, _ := UnparkSession(ctx, "acme", x.ID); ok {
			t.Fatal("a run that is running was unparked")
		}
	})
}

// A stop reaches a parked run as it reaches a paused one: it ends the session, clears the
// park, tells the hook that cancels its wake, and nothing can park or unpark it after. A
// session that ended is never parked, so a run that was stopped as it parked leaves no
// parked session behind.
//
// MUTATION: End without clearing the park and a stopped session still reads parked;
// stop without telling Stopped and the wake stays set.
func TestStoppingAParkedRunCancelsItsWake(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto, x := parkedSession(t, "sess_p3")
		ctx := context.Background()
		var told []string
		was := Stopped
		Stopped = func(org, session string) { told = append(told, org+"/"+session) }
		t.Cleanup(func() { Stopped = was })

		if ok, err := ParkSession(ctx, "acme", x.ID, ParkLimit, time.Now().Add(time.Hour).Unix()); err != nil || !ok {
			t.Fatal(ok, err)
		}
		parked, _ := sto.GetSession(ctx, "acme", x.ID)
		if _, _, err := stop(ctx, sto, parked, "stopped by its owner"); err != nil {
			t.Fatal(err)
		}
		got, _ := sto.GetSession(ctx, "acme", x.ID)
		if got.Status != StatusStopped || got.Park != "" || got.ParkAt != 0 || got.Parked() {
			t.Fatalf("stopped session = %+v, want stopped and not parked", got)
		}
		if len(told) != 1 || told[0] != "acme/"+x.ID {
			t.Fatalf("Stopped was told %v, want the stopped session once", told)
		}
		if ok, err := UnparkSession(ctx, "acme", x.ID); err != nil || ok {
			t.Fatalf("a stopped run was unparked: (%v, %v)", ok, err)
		}
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkLimit, 0); err != nil || ok {
			t.Fatalf("a stopped run was parked: (%v, %v)", ok, err)
		}

		// A run that finished before it could park is not parked either.
		y := mkSession("acme", "sess_p4", "", "sess_p4")
		if err := sto.CreateSession(ctx, y); err != nil {
			t.Fatal(err)
		}
		if err := CloseSession(ctx, "acme", y.ID, StatusDone); err != nil {
			t.Fatal(err)
		}
		if ok, err := ParkSession(ctx, "acme", y.ID, ParkLimit, 0); err != nil || ok {
			t.Fatalf("a finished run was parked: (%v, %v)", ok, err)
		}
	})
}

// A cap raised on a session resumes work that stopped at the cap; it does not resume a
// parked run, which has no run for a cap to resume and waits for its own reasons.
//
// MUTATION: drop the Parked guard in the budget write and the POST flips a parked
// session to running with nothing running in it.
func TestARaisedBudgetDoesNotUnparkARun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &tokenAI{prompt: 1, completion: 1})
		code, out := do(t, app, http.MethodPost, "/v1/agent/sessions", "acme",
			map[string]any{"agent": "worker", "title": "t", "budget_micro_usd": 10})
		if code != http.StatusCreated {
			t.Fatalf("register want 201, got %d (%s)", code, out)
		}
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(out, &v)
		sto, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if ok, err := ParkSession(ctx, "acme", v.ID, ParkPayment, 0); err != nil || !ok {
			t.Fatal(ok, err)
		}
		code, out = do(t, app, http.MethodPost, "/v1/agent/sessions/"+v.ID+"/budget", "acme", map[string]any{"budget_micro_usd": 100})
		if code != http.StatusOK || !strings.Contains(string(out), `"status":"paused"`) {
			t.Fatalf("a raised cap on a parked run answered %d (%s), want it still paused", code, out)
		}
		got, _ := sto.GetSession(ctx, "acme", v.ID)
		if !got.Parked() {
			t.Fatalf("a raised cap unparked the run: %+v", got)
		}
	})
}

// A turn that followed a coding run reads its parking as the run's last word for the turn,
// in the run's own sentence and with where its work is, and puts the run's buttons on the
// turn's message, as a paused run's are: Continue now, or Stop. The run goes on by itself
// when its time or its credits come, with no turn following it.
//
// MUTATION: leave "parked" unread and the turn waits on a run that is asleep until its own
// deadline.
func TestATurnReadsAParkedRunAsItsLastWord(t *testing.T) {
	e := Event{Kind: KindStatus, Payload: `{"status":"parked","reason":"limit","message":"` + ParkSaid(ParkLimit, 1794700800) +
		`","changed":true,"branch":"agent/abc"}`}
	got := narrate(e)
	want := "The coding run is " + ParkSaid(ParkLimit, 1794700800) + ". Work so far is on `agent/abc`."
	if got.end != want || !got.paused {
		t.Fatalf("narrate = %+v, want the turn to end on %q with the run's buttons", got, want)
	}
}

// A sign-out or a link revoke tears down every live session of the person, and a parked run is
// live: it goes on by itself, as that person, days later. The sweep reaches it though its end
// is written, ends it, and tells whoever holds its wake (Stopped), so the park goes with it.
//
// MUTATION: select only sessions that have not ended (ended_at=0, as the sweep did) and the
// parked run survives the revoke and goes on as the person who signed out.
func TestARevokeEndsAParkedRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto, x := parkedSession(t, "sess_rv1")
		ctx := context.Background()
		var mu sync.Mutex
		var released []string
		was := Stopped
		Stopped = func(org, id string) { mu.Lock(); released = append(released, org+"/"+id); mu.Unlock() }
		t.Cleanup(func() { Stopped = was })
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkLimit, time.Now().Add(time.Hour).Unix()); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if n, err := CountActiveSessions(ctx, "acme", SessionMatch{Actor: x.Actor}); err != nil || n != 1 {
			t.Fatalf("CountActiveSessions = (%d, %v), want the parked run counted: a revoke will end it", n, err)
		}
		n, err := StopSessions(ctx, "acme", SessionMatch{Actor: x.Actor})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := sto.GetSession(ctx, "acme", x.ID)
		if n != 1 || got.Status != StatusStopped || got.Park != "" || got.ParkAt != 0 {
			t.Fatalf("StopSessions(actor=%q) stopped %d; the session reads %s/park=%q, want it stopped and no longer parked", x.Actor, n, got.Status, got.Park)
		}
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(released, []string{"acme/" + x.ID}) {
			t.Fatalf("Stopped was told %v, want the parked run's, so its park is removed", released)
		}
	})
}

// The platform ends a parked run by name and reason, as it ends one whose person may no longer
// act in its org: stopped, with the reason on its stream, its wake told, and a second end of the
// same session, or one of a session that is not there, is no stop.
//
// MUTATION: leave Stopped untold and the row of an ended run waits for a try to find it gone.
func TestThePlatformEndsAParkedRunByName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto, x := parkedSession(t, "sess_en1")
		ctx := context.Background()
		var released atomic.Int32
		was := Stopped
		Stopped = func(string, string) { released.Add(1) }
		t.Cleanup(func() { Stopped = was })
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkPayment, 0); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if ok, err := StopSession(ctx, "acme", x.ID, "access removed"); err != nil || !ok {
			t.Fatalf("StopSession = (%v, %v), want the parked run ended", ok, err)
		}
		got, _ := sto.GetSession(ctx, "acme", x.ID)
		if got.Status != StatusStopped || got.Parked() || released.Load() != 1 {
			t.Fatalf("the session reads %+v after an end, told %d", got, released.Load())
		}
		evs, _ := sto.ListEvents(ctx, "acme", x.ID, 0, 50)
		said := false
		for _, e := range evs {
			if e.Kind == KindControl && strings.Contains(e.Payload, "access removed") {
				said = true
			}
		}
		if !said {
			t.Fatalf("the stream does not say why: %v", evs)
		}
		if ok, err := StopSession(ctx, "acme", x.ID, "again"); err != nil || ok {
			t.Fatalf("a second end = (%v, %v), want none", ok, err)
		}
		if ok, err := StopSession(ctx, "acme", "sess_nobody", "x"); err != nil || ok {
			t.Fatalf("an end of a session that is not there = (%v, %v), want none", ok, err)
		}
	})
}

// A client that waits on a session is told a parked run has not ended: no outcome, and why it
// stands still and when it goes on, so it keeps waiting for as long as it will and may say so.
// A run a person paused still answers paused; one that ended answers its end.
//
// MUTATION: answer a parked run the outcome of a paused one and an automation's agent step ends
// "paused in Dev" on a run that would have gone on by itself in the morning.
func TestAWaiterIsToldAParkedRunHasNotEnded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto, x := parkedSession(t, "sess_wt1")
		ctx := context.Background()
		at := time.Now().Add(3 * time.Hour).Unix()
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkLimit, at); err != nil || !ok {
			t.Fatal(ok, err)
		}
		got, err := readTurn(ctx, sto, "acme", x.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got.Outcome != "" || got.Park == nil || got.Park.Reason != ParkLimit || got.Park.ResumeAt != at {
			t.Fatalf("a parked run answers %+v (park %+v), want no outcome, and why and when", got, got.Park)
		}
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkPayment, 0); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if got, _ = readTurn(ctx, sto, "acme", x.ID, 0); got.Park == nil || got.Park.Reason != ParkPayment || got.Park.ResumeAt != 0 {
			t.Fatalf("a run parked for payment answers %+v", got.Park)
		}
		if ok, err := sto.Unpark(ctx, "acme", x.ID, "", time.Now().Unix()); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if _, err := sto.Pause(ctx, "acme", x.ID, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		if got, _ = readTurn(ctx, sto, "acme", x.ID, 0); got.Outcome != client.TurnPaused || got.Park != nil {
			t.Fatalf("a paused run answers %+v, want paused and not parked", got)
		}
	})
}

// A parked run says in one sentence, for a progress line and a card, why it waits, when it goes on
// by itself, and that after the age a session may live it goes on only when a person presses
// Continue; and the sentence fits a progress line whatever that age is set to.
//
// MUTATION: leave the bound out of the sentence and a run parked for a day reads as one that
// will go on by itself forever; widen it and the progress line is refused whole.
func TestAParkedRunSaysItWaitsForContinueAfterTheDay(t *testing.T) {
	at := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC).Unix()
	for _, life := range []string{"", "172800", "129600", "600", "3600"} {
		t.Setenv("AGENTS_MAX_LIFE_SEC", life)
		for _, reason := range []string{ParkLimit, ParkPayment} {
			for _, when := range []int64{0, at} {
				say := ParkSaid(reason, when)
				if len(say) > 120 {
					t.Errorf("AGENTS_MAX_LIFE_SEC=%q: %q is %d bytes, over the 120 a progress line takes", life, say, len(say))
				}
				if !strings.HasSuffix(say, ", on Continue") || !strings.Contains(say, "; after ") {
					t.Errorf("%q does not say it waits for Continue", say)
				}
			}
		}
	}
	t.Setenv("AGENTS_MAX_LIFE_SEC", "")
	if got := ParkSaid(ParkLimit, at); got != "parked: usage limit, goes on at 2026-11-15 00:00 UTC or when credits are added; after 24 hours, on Continue" {
		t.Errorf("ParkSaid = %q", got)
	}
}

// A run that goes on is paid as its person resolves today, not as it was stamped when it began:
// the runtime meter bills the session's stored payer, so unparking re-stamps it from the caller
// the try states. A try that states no wallet leaves the stamp.
//
// MUTATION: leave the payer out of the unpark and a person who moved from a personal wallet to
// the org's pool is billed their sandbox time on the wallet they left.
func TestAnUnparkedRunIsBilledToTheWalletItsPersonIsPaidFromToday(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountInproc(t)
		sto := storeOf(t, &mounted.State, "acme")
		ctx := context.Background()
		x := mkSession("acme", "sess_py1", "", "sess_py1")
		x.Kind, x.Box, x.Branch, x.MeteredAt, x.Payer = "coding", "sbx_1", "agent/abc", x.StartedAt, "acme/carol"
		if err := sto.CreateSession(ctx, x); err != nil {
			t.Fatal(err)
		}
		if ok, err := ParkSession(ctx, "acme", x.ID, ParkLimit, 0); err != nil || !ok {
			t.Fatal(ok, err)
		}
		as := zip.WithCaller(cloud.For(context.Background(), "acme"), zip.Caller{Org: "acme", User: "u-carol", Name: "carol"})
		if ok, err := UnparkSession(as, "acme", x.ID); err != nil || !ok {
			t.Fatalf("UnparkSession = (%v, %v)", ok, err)
		}
		got, _ := sto.GetSession(ctx, "acme", x.ID)
		if got.Payer != "acme" {
			t.Fatalf("the session is billed to %q, want the org's pool, which is what this person is paid from today", got.Payer)
		}
		if r := sessionRunning(got, 1); r.Payer != "acme" {
			t.Fatalf("the meter bills %q", r.Payer)
		}
	})
}
