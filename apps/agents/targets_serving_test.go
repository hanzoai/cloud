package agents

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// A machine reports whether a run sent to it now would be taken, by the same
// rule dispatch applies: online, and a runner polled for work within servingTTL.
// A beating machine with no runner lists as online and not serving, so a picker
// never offers it as a place to run.
func TestTargetServingFollowsTheDispatchRule(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		old := claimLongPoll
		claimLongPoll = 100 * time.Millisecond
		defer func() { claimLongPoll = old }()
		app := mountApp(t, &fakeAI{content: "x"})

		served := func(id string) bool {
			t.Helper()
			code, b := do(t, app, http.MethodGet, "/v1/agent/targets", "acme", nil)
			if code != http.StatusOK {
				t.Fatalf("list: %d %s", code, b)
			}
			var l struct {
				Targets []map[string]any `json:"targets"`
			}
			if err := json.Unmarshal(b, &l); err != nil {
				t.Fatal(err)
			}
			for _, row := range l.Targets {
				if row["id"] == id {
					v, ok := row["serving"].(bool)
					if !ok {
						t.Fatalf("the list names no serving for %s: %v", id, row)
					}
					return v
				}
			}
			t.Fatalf("%s is not listed", id)
			return false
		}

		id, key := registerAndMint(t, app, "acme", "evo")
		if served(id) {
			t.Fatal("a machine whose runner never polled reads serving")
		}
		code, _ := do(t, app, http.MethodGet, "/v1/agent/targets/"+id, "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("get: %d", code)
		}

		// A claim poll is the runner saying it listens; it answers empty after its wait.
		if code, b := doKey(t, app, http.MethodPost, "/v1/agent/targets/"+id+"/claim", "acme", key); code != http.StatusOK && code != http.StatusNoContent {
			t.Fatalf("claim poll: %d %s", code, b)
		}
		if !served(id) {
			t.Fatal("a machine whose runner just polled reads not serving")
		}
		// A colleague sees the machine listed and never as a place to send a run.
		code, b0 := reqAs(t, app, http.MethodGet, "/v1/agent/targets", "acme", "mallory", false, "", nil)
		var theirs struct {
			Targets []map[string]any `json:"targets"`
		}
		if err := json.Unmarshal(b0, &theirs); code != http.StatusOK || err != nil || len(theirs.Targets) != 1 || theirs.Targets[0]["serving"] != false {
			t.Fatalf("a colleague reads %d %s, want the machine listed and not serving", code, b0)
		}
		// An org admin may send runs to any of the org's machines.
		code, b0 = reqAs(t, app, http.MethodGet, "/v1/agent/targets", "acme", "boss", true, "", nil)
		if err := json.Unmarshal(b0, &theirs); code != http.StatusOK || err != nil || len(theirs.Targets) != 1 || theirs.Targets[0]["serving"] != true {
			t.Fatalf("an org admin reads %d %s, want it serving", code, b0)
		}
		code, b := do(t, app, http.MethodGet, "/v1/agent/targets/"+id, "acme", nil)
		var one map[string]any
		_ = json.Unmarshal(b, &one)
		if code != http.StatusOK || one["serving"] != true {
			t.Fatalf("one machine's read disagrees with the list: %d %s", code, b)
		}

		// Drained is intent: it takes nothing new, so it does not serve.
		if code, b := do(t, app, http.MethodPatch, "/v1/agent/targets/"+id, "acme", map[string]any{"status": TargetDraining}); code != http.StatusOK {
			t.Fatalf("drain: %d %s", code, b)
		} else {
			var p map[string]any
			_ = json.Unmarshal(b, &p)
			if p["serving"] != false {
				t.Fatalf("a drained machine reads serving: %s", b)
			}
		}
		if served(id) {
			t.Fatal("a drained machine lists as serving")
		}

	})
}
