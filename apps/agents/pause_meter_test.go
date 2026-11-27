package agents

// A PAUSE ENDS THE RUN'S SPAN (#768). A paused session has nothing running in it,
// so its end is written when it pauses — the rule Rest and Park already keep — and
// the runtime meter bills it to that moment and no further. A resume opens it
// again: the clock and the meter start over from the resume, and the time it sat
// paused is neither a life nor a charge.
//
// The leak these tests pin: `hanzo dev` (hanzo-gen) records a clean exit of an
// interactive session as `paused`, so a session with only its opening event and
// no process behind it billed every fifteen minutes until the reaper's day-long
// ceiling, because the idle clock reads only `running`.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// TestACLIExitThatPausesStopsTheMeter is the report, over HTTP, in the order the
// CLI writes it: register running, one opening event, PATCH paused on a clean
// exit. Long after, the meter has billed nothing past the pause, the session is
// still paused (resumable), and a PATCH back to running opens it again.
//
// MUTATION: drop the pause branch from UpdateSession's ended_at and the sweep ten
// hours later bills ten hours for a session nothing was running in.
func TestACLIExitThatPausesStopsTheMeter(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		v := register(t, app, "acme", map[string]any{
			"agent": "dev", "title": "hanzo dev", "host": "james-mac", "cwd": "/Users/j/app", "status": "running",
		})
		if code, b := do(t, app, http.MethodPost, "/v1/agent/sessions/"+v.ID+"/events", "acme",
			map[string]any{"kind": "status", "payload": map[string]any{"type": "context", "host": "james-mac"}}); code != http.StatusCreated {
			t.Fatalf("opening event: %d %s", code, b)
		}
		if code, b := do(t, app, http.MethodPatch, "/v1/agent/sessions/"+v.ID, "acme",
			map[string]any{"status": "paused"}); code != http.StatusOK {
			t.Fatalf("pause on exit: %d %s", code, b)
		}

		st := &mounted.State
		sto := storeOf(t, st, "acme")
		x := reload(t, sto, v.ID)
		if x.Status != StatusPaused {
			t.Fatalf("a clean exit left the session %q, want paused", x.Status)
		}
		if x.EndedAt == 0 {
			t.Fatalf("a paused session kept ended_at=0, so the meter goes on billing it")
		}

		b := &books{}
		sweep(t, st, x.EndedAt+10*hour, cloud.RuntimeHourMicros, b)
		sweep(t, st, x.EndedAt+20*hour, cloud.RuntimeHourMicros, b)
		if most := cloud.RuntimeCost(cloud.RuntimeHourMicros, x.EndedAt-x.StartedAt); b.micros() > most {
			t.Fatalf("a session paused after %ds billed %d µ$ over twenty hours, want at most %d",
				x.EndedAt-x.StartedAt, b.micros(), most)
		}

		// `hanzo dev --resume` reuses a paused id: it PATCHes running.
		if code, b := do(t, app, http.MethodPatch, "/v1/agent/sessions/"+v.ID, "acme",
			map[string]any{"status": "running"}); code != http.StatusOK {
			t.Fatalf("resume: %d %s", code, b)
		}
		x = reload(t, sto, v.ID)
		if x.Status != StatusRunning || x.EndedAt != 0 {
			t.Fatalf("a resumed session is status=%q endedAt=%d, want running and open", x.Status, x.EndedAt)
		}
		live, err := sto.Live(context.Background(), "acme")
		if err != nil || len(live) != 1 || live[0].ID != v.ID {
			t.Fatalf("a resumed session is not live (%v, %v), so the reaper could never end it", idsOf(live), err)
		}
	})
}

// pauseAt writes the pause the way PATCH and the budget cap do: through
// UpdateSession, with the moment as updated_at.
func pauseAt(t *testing.T, sto *Store, id string, at int64) {
	t.Helper()
	x := reload(t, sto, id)
	x.Status, x.UpdatedAt = StatusPaused, at
	if err := sto.UpdateSession(context.Background(), x); err != nil {
		t.Fatalf("pause %s: %v", id, err)
	}
}

// resumeAt writes the resume the way PATCH and a raised budget do.
func resumeAt(t *testing.T, sto *Store, id string, at int64) {
	t.Helper()
	x := reload(t, sto, id)
	x.Status, x.UpdatedAt = StatusRunning, at
	if err := sto.UpdateSession(context.Background(), x); err != nil {
		t.Fatalf("resume %s: %v", id, err)
	}
}

// TestAPausedSpanIsBilledToThePauseAndNoFurther — one hour running, nine paused:
// one hour billed, once, and never again however many ticks follow.
//
// MUTATION: write ended_at=0 on a pause and the two late ticks bill nine hours.
func TestAPausedSpanIsBilledToThePauseAndNoFurther(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		b := &books{}
		const start = 1_800_000_000
		open(t, sto, "sess_1", "acme", start)

		pauseAt(t, sto, "sess_1", start+hour)
		if x := reload(t, sto, "sess_1"); x.Status != StatusPaused || x.EndedAt != start+hour {
			t.Fatalf("a pause left status=%q endedAt=%d, want paused at %d", x.Status, x.EndedAt, start+hour)
		}
		sweep(t, st, start+5*hour, cloud.RuntimeHourMicros, b)
		sweep(t, st, start+10*hour, cloud.RuntimeHourMicros, b)

		if b.len() != 1 || b.micros() != cloud.RuntimeHourMicros {
			t.Fatalf("one running hour billed %d debits, %d µ$; want one debit of %d", b.len(), b.micros(), cloud.RuntimeHourMicros)
		}
	})
}

// TestAResumeBillsOnlyTheTimeItRan — an hour, four paused, an hour: two hours,
// whether or not a sweep landed while it was paused. The resume restarts the
// session's life (the ceiling reads started_at) as Reopen and Unpark do.
//
// MUTATION: reset metered_at to the resume instead of shifting it by the pause,
// and the "no sweep while paused" case bills one hour: the hour before the pause
// is lost. Leave metered_at alone and it bills five: the pause is charged.
func TestAResumeBillsOnlyTheTimeItRan(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sweptPaused bool
	}{{"no sweep while paused", false}, {"a sweep while paused", true}} {
		t.Run(tc.name, func(t *testing.T) {
			sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
				st, sto := meterStore(t)
				b := &books{}
				const start = 1_800_000_000
				open(t, sto, "sess_1", "acme", start)

				pauseAt(t, sto, "sess_1", start+hour)
				if tc.sweptPaused {
					sweep(t, st, start+3*hour, cloud.RuntimeHourMicros, b)
				}
				resumeAt(t, sto, "sess_1", start+5*hour)

				x := reload(t, sto, "sess_1")
				if x.Status != StatusRunning || x.EndedAt != 0 || x.StartedAt != start+5*hour {
					t.Fatalf("resumed: status=%q endedAt=%d startedAt=%d, want running, open, started at the resume",
						x.Status, x.EndedAt, x.StartedAt)
				}
				sweep(t, st, start+6*hour, cloud.RuntimeHourMicros, b)

				if want := 2 * cloud.RuntimeHourMicros; b.micros() != want {
					t.Fatalf("two running hours around a four-hour pause billed %d µ$, want %d", b.micros(), want)
				}
			})
		})
	}
}

// TestTheControlPauseWritesItsEnd — POST .../pause reaches Store.Pause, which is
// the third writer of `paused`, and keeps the same rule.
//
// MUTATION: drop ended_at from Store.Pause and the row stays open.
func TestTheControlPauseWritesItsEnd(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, sto := meterStore(t)
		const start = 1_800_000_000
		open(t, sto, "sess_1", "acme", start)
		if ok, err := sto.Pause(context.Background(), "acme", "sess_1", start+hour); err != nil || !ok {
			t.Fatalf("pause: %v %v", ok, err)
		}
		if x := reload(t, sto, "sess_1"); x.EndedAt != start+hour {
			t.Fatalf("a control pause left endedAt=%d, want %d", x.EndedAt, start+hour)
		}
	})
}

// TestAStopAfterAPauseKeepsThePausesEnd — ending a paused (or parked) session
// does not move its end to the stop: nothing ran in between, and an end moved
// forward is a span the meter bills. Two writers end a session: Store.End (a stop,
// a reap) and UpdateSession with a terminal status (a PATCH, CloseSession, which
// sets EndedAt to now on the struct it writes).
//
// MUTATION: write ended_at=$at unconditionally in Store.End, or let a terminal
// UpdateSession take the struct's end over the row's, and the close eight hours
// after the pause bills those eight hours.
func TestAStopAfterAPauseKeepsThePausesEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, sto *Store, at int64) string
	}{
		{"stop", func(t *testing.T, sto *Store, at int64) string {
			if ok, err := sto.End(context.Background(), "acme", "sess_1", StatusStopped, at); err != nil || !ok {
				t.Fatalf("stop: %v %v", ok, err)
			}
			return StatusStopped
		}},
		{"close", func(t *testing.T, sto *Store, at int64) string {
			x := reload(t, sto, "sess_1")
			x.Status, x.EndedAt, x.UpdatedAt = StatusDone, at, at
			if err := sto.UpdateSession(context.Background(), x); err != nil {
				t.Fatalf("close: %v", err)
			}
			return StatusDone
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
				st, sto := meterStore(t)
				b := &books{}
				const start = 1_800_000_000
				open(t, sto, "sess_1", "acme", start)
				pauseAt(t, sto, "sess_1", start+hour)
				sweep(t, st, start+2*hour, cloud.RuntimeHourMicros, b)

				want := tc.end(t, sto, start+9*hour)
				x := reload(t, sto, "sess_1")
				if x.Status != want || x.EndedAt != start+hour {
					t.Fatalf("ended after a pause: status=%q endedAt=%d, want %s, ended at the pause %d", x.Status, x.EndedAt, want, start+hour)
				}
				sweep(t, st, start+10*hour, cloud.RuntimeHourMicros, b)
				if b.micros() != cloud.RuntimeHourMicros {
					t.Fatalf("one running hour, paused, then ended billed %d µ$, want %d", b.micros(), cloud.RuntimeHourMicros)
				}
			})
		})
	}
}

// TestTheReaperClosesAPauseNobodyEnded — a row paused before this rule (or by a
// writer that forgets it) is open with status paused. The reaper writes its end at
// its last activity, leaves it paused (a person may still resume it), and the
// meter bills it to there.
//
// MUTATION: delete the paused branch in reap and the sweep bills the six hours
// since it was opened, the four paused among them.
func TestTheReaperClosesAPauseNobodyEnded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		b := &books{}
		now := time.Now().Unix()
		start, paused := now-6*hour, now-4*hour
		if err := sto.CreateSession(context.Background(), Session{ID: "sess_old", Org: "acme", Agent: "dev",
			Actor: "acme/z", Status: StatusPaused, RootID: "sess_old", StartedAt: start, CreatedAt: start,
			UpdatedAt: paused, Payer: "acme", MeteredAt: start}); err != nil {
			t.Fatalf("plant: %v", err)
		}

		reap(context.Background(), st, &record{}, testClocks)

		x := reload(t, sto, "sess_old")
		if x.Status != StatusPaused {
			t.Fatalf("the reaper turned a paused session %q; a pause is not an end of the session", x.Status)
		}
		if x.EndedAt != paused {
			t.Fatalf("the reaper left a paused session's end at %d, want its last activity %d", x.EndedAt, paused)
		}
		sweep(t, st, now, cloud.RuntimeHourMicros, b)
		sweep(t, st, now+5*hour, cloud.RuntimeHourMicros, b)
		if want := 2 * cloud.RuntimeHourMicros; b.micros() != want {
			t.Fatalf("two running hours then a pause billed %d µ$, want %d", b.micros(), want)
		}
	})
}

// TestAQuietQueuedSessionIsReaped — `queued` is a claim of work to come, not a
// state a person chose, and the meter bills it. Silent past the idle allowance it
// is an abandoned row like a silent `running` one.
//
// MUTATION: restore `x.Status != StatusRunning` in over and the queued row lives
// until the day-long ceiling, billing all of it.
func TestAQuietQueuedSessionIsReaped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, sto := meterStore(t)
		now := time.Now().Unix()
		plant(t, sto, "sess_queued", StatusQueued, now-3*hour)
		plant(t, sto, "sess_waiting", StatusQueued, now-60)

		reap(context.Background(), st, &record{}, testClocks)

		if x := reload(t, sto, "sess_queued"); !isTerminalStatus(x.Status) || x.EndedAt == 0 {
			t.Fatalf("a queued session silent for three hours is %q endedAt=%d, want reaped", x.Status, x.EndedAt)
		}
		if x := reload(t, sto, "sess_waiting"); x.Status != StatusQueued || x.EndedAt != 0 {
			t.Fatalf("a queued session a minute old was touched: %q endedAt=%d", x.Status, x.EndedAt)
		}
	})
}
