package auto

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// A targeted automation carries its granted folder into the Dev run it starts.
// Clearing dir keeps the target and returns to the machine's own choice: the
// absent field, on create and on patch, is the behaviour that came before it.
func TestAutomationDirReachesTheCodingRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		app := newApp(t)
		engineFor(ctx, t, "acme")
		dev := devPlane(t, client.TurnEvents{Next: 4, Outcome: client.TurnCompleted,
			Events: []client.TurnEvent{{Seq: 4, Kind: client.TurnAnswer, Text: "done"}}})

		const folder = "/Users/JamesBurns/CarbonDistrict-HQ"
		a := decode[Automation](t, ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
			"name": "Local work", "instructions": "Do the work.", "permissions": "auto",
			"target": "tgt_local", "dir": folder,
		}))
		if a.Target == nil || *a.Target != "tgt_local" || a.Dir == nil || *a.Dir != folder {
			t.Fatalf("created = %+v", a)
		}
		// dir lives in the flow's step, so the definition must still read back as
		// an automation after the round trip through the store.
		got := decode[Automation](t, req(t, app, http.MethodGet, "/v1/auto/automations/"+a.ID, "acme", nil))
		if got.Dir == nil || *got.Dir != folder {
			t.Fatalf("read back = %+v", got)
		}
		r := req(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil)
		until(t, "acme", decode[runStarted](t, r).Run.ID)

		dev.mu.Lock()
		if len(dev.starts) != 1 || dev.starts[0].TargetID != "tgt_local" || dev.starts[0].Dir != folder {
			t.Errorf("coding runs = %+v", dev.starts)
		}
		dev.mu.Unlock()

		p := ask(t, app, http.MethodPatch, "/v1/auto/automations/"+a.ID, "acme", map[string]any{"dir": nil})
		if p.Code != http.StatusOK {
			t.Fatalf("patch = %d %s", p.Code, p.Body)
		}
		cleared := decode[Automation](t, p)
		if cleared.Dir != nil || cleared.Target == nil || *cleared.Target != "tgt_local" {
			t.Fatalf("patched = %+v", cleared)
		}
		r = req(t, app, http.MethodPost, "/v1/auto/automations/"+a.ID+"/run", "acme", nil)
		until(t, "acme", decode[runStarted](t, r).Run.ID)

		dev.mu.Lock()
		if len(dev.starts) != 2 || dev.starts[1].TargetID != "tgt_local" || dev.starts[1].Dir != "" {
			t.Errorf("coding runs after clearing dir = %+v", dev.starts)
		}
		dev.mu.Unlock()
	})
}

// A folder lives on a machine: one named without a target cannot be honoured by
// the coding run, so it is refused at the boundary instead of at every run.
func TestAutomationDirNeedsATarget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		r := ask(t, app, http.MethodPost, "/v1/auto/automations", "acme", map[string]any{
			"name": "No machine", "instructions": "Do the work.", "dir": "/Users/JamesBurns/CarbonDistrict-HQ",
		})
		if r.Code != http.StatusBadRequest {
			t.Fatalf("create with dir and no target = %d %s", r.Code, r.Body)
		}
	})
}
