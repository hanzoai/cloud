package agents

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// engines_test.go holds what the port to hanzo-sql changed that no behaviour test
// above reaches by itself: the statements whose answer the engines do not agree on
// unless the store asks for it, run on both.

// A due minute of a schedule is claimed once, by whichever caller asks first, and a
// claim older than the retention goes with the next one.
func TestAFiringMinuteIsClaimedOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto := testStore(t)
		ctx := context.Background()
		at := time.Now().Unix()

		first, err := sto.ClaimFiring(ctx, "agent_a", at)
		if err != nil || !first {
			t.Fatalf("the first claim of a minute = %v, %v; want it taken", first, err)
		}
		second, err := sto.ClaimFiring(ctx, "agent_a", at)
		if err != nil || second {
			t.Fatalf("the second claim of the same minute = %v, %v; want it refused", second, err)
		}
		if other, err := sto.ClaimFiring(ctx, "agent_b", at); err != nil || !other {
			t.Fatalf("another agent's claim of that minute = %v, %v; want it taken", other, err)
		}

		old := at - int64(firingKept/time.Second) - 60
		if _, err := sto.db.Exec(`INSERT INTO agent_firings (agent, at) VALUES ($1, $2)`, "agent_a", old); err != nil {
			t.Fatal(err)
		}
		if _, err := sto.ClaimFiring(ctx, "agent_a", at+60); err != nil {
			t.Fatal(err)
		}
		var kept int
		if err := sto.db.QueryRow(`SELECT COUNT(*) FROM agent_firings WHERE agent = $1 AND at = $2`, "agent_a", old).Scan(&kept); err != nil || kept != 0 {
			t.Fatalf("a claim past its retention is still there: %d, %v", kept, err)
		}
	})
}

// A flag is a BIGINT column that a Go bool reaches through bit(): written true it is
// read true, and written false it is read false, on either engine.
func TestAFlagColumnRoundTrips(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto := testStore(t)
		ctx := context.Background()
		x := mkSession("acme", "sess_flags", "", "sess_flags")
		x.Published, x.ProgressEstimated, x.BudgetRemoved = true, true, true
		x.ProgressPhase, x.ProgressAt = "building", 5
		if err := sto.CreateSession(ctx, x); err != nil {
			t.Fatal(err)
		}
		got, err := sto.GetSession(ctx, "acme", "sess_flags")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Published || !got.ProgressEstimated || !got.BudgetRemoved {
			t.Fatalf("written true, read published=%v estimated=%v removed=%v", got.Published, got.ProgressEstimated, got.BudgetRemoved)
		}

		got.Published = false
		if err := sto.UpdateSession(ctx, got); err != nil {
			t.Fatal(err)
		}
		if err := sto.SetProgress(ctx, "acme", "sess_flags", Progress{Pct: 10, Phase: "building", At: 6, Estimated: false}); err != nil {
			t.Fatal(err)
		}
		if err := sto.SetSessionBudget(ctx, "acme", "sess_flags", 0, false); err != nil {
			t.Fatal(err)
		}
		got, err = sto.GetSession(ctx, "acme", "sess_flags")
		if err != nil {
			t.Fatal(err)
		}
		if got.Published || got.ProgressEstimated || got.BudgetRemoved {
			t.Fatalf("written false, read published=%v estimated=%v removed=%v", got.Published, got.ProgressEstimated, got.BudgetRemoved)
		}
		listed, err := sto.ListSessions(ctx, "acme", SessionFilter{Published: true})
		if err != nil || len(listed) != 0 {
			t.Fatalf("a session no longer published is listed as one: %v, %v", listed, err)
		}
	})
}

// Text one engine would keep and the other refuse is refused before either is asked:
// the store answers dialect.ErrText, and the door answers 400, not a 500 from the
// driver.
func TestTextAnEngineCannotKeepIsRefusedOnBoth(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto := testStore(t)
		ctx := context.Background()

		a := mk("acme", "nul")
		a.Instructions = "before\x00after"
		if err := sto.Create(ctx, a); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("Create with a NUL = %v, want ErrText", err)
		}
		a.Instructions = "ok"
		a.Description = "not \xff UTF-8"
		if err := sto.Create(ctx, a); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("Create with invalid UTF-8 = %v, want ErrText", err)
		}
		if err := sto.InsertRun(ctx, Run{ID: "run_x", Org: "acme", AgentName: "nul", Status: "ok", Output: "a\x00b", CreatedAt: 1}); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("InsertRun with a NUL = %v, want ErrText", err)
		}
		if _, err := sto.AppendEvent(ctx, Event{ID: "evt_x", SessionID: "sess_x", Org: "acme", Kind: "log\x00", Payload: "{}", CreatedAt: 1}); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("AppendEvent with a NUL = %v, want ErrText", err)
		}
		// A payload is kept as plain text (plainPayload): a NUL in one is escaped before it is stored.
		if _, err := sto.AppendEvent(ctx, Event{ID: "evt_y", SessionID: "sess_x", Org: "acme", Kind: KindLog, Payload: "\"a\x00b\"", CreatedAt: 1}); err != nil {
			t.Fatalf("AppendEvent with a NUL in its payload = %v, want it escaped and kept", err)
		}
		if _, err := sto.Get(ctx, "acme", "nul"); !errors.Is(err, errNotFound) {
			t.Fatalf("a refused agent was stored: %v", err)
		}

		app := mountApp(t, &fakeAI{content: "x"})
		code, body := do(t, app, http.MethodPost, "/v1/agent", "acme", map[string]any{
			"name": "nul", "instructions": "before\u0000after",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("POST an agent whose instructions hold a NUL = %d %s, want 400", code, body)
		}
	})
}
