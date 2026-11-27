package auto

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// wantRoutedPlan is what a save refused for pairing a machine with ask says.
const wantRoutedPlan = `an automation that runs on a machine needs permissions "auto": "ask" runs a plan in Hanzo's sandbox, which cannot be routed to a machine`

// An automation saved before a machine target and ask were refused together
// still lists, so its owner can open it and fix it; only a save that keeps the
// pair is refused.
func TestStoredMachineTargetWithAskStillLists(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t)
		d := definition{Name: "Old", Instructions: "x", Target: "my-local-machine", Permissions: PermissionAsk}
		if err := d.check(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		f := Flow{ID: "flow_old", Org: "acme", Status: FlowEnabled, Created: 1, Updated: 1}
		if _, err := mounted.State.store.CreateAutomationFlow(ctx, f, maxAutomations); err != nil {
			t.Fatalf("seed flow: %v", err)
		}
		if err := mounted.State.store.SetFlowOwner(ctx, "acme", f.ID, Person{Sub: "u-acme"}); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
		if _, err := mounted.State.store.CreateVersion(ctx, FlowVersion{
			ID: "ver_old", Org: "acme", FlowID: f.ID, DisplayName: d.Name, Trigger: d.trigger(), Valid: true,
			State: VersionDraft, SchemaVersion: LatestFlowSchemaVersion, Created: 1, Updated: 1,
		}); err != nil {
			t.Fatalf("seed version: %v", err)
		}
		list := decode[automationPage](t, req(t, app, http.MethodGet, "/v1/auto/automations", "acme", nil))
		if len(list.Data) != 1 || list.Data[0].Target == nil || *list.Data[0].Target != "my-local-machine" || list.Data[0].Permissions != PermissionAsk {
			t.Fatalf("list = %+v", list.Data)
		}
		id := list.Data[0].ID
		if r := req(t, app, http.MethodPatch, "/v1/auto/automations/"+id, "acme", map[string]any{"enabled": false}); r.Code != http.StatusOK {
			t.Fatalf("switching it off = %d %s", r.Code, r.Body)
		}
		r := req(t, app, http.MethodPatch, "/v1/auto/automations/"+id, "acme", map[string]any{"name": "Still old"})
		var prob struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(r.Body, &prob)
		if r.Code != http.StatusBadRequest || !strings.Contains(prob.Detail, wantRoutedPlan) {
			t.Fatalf("renaming it kept the pair: %d %s", r.Code, r.Body)
		}
		if r := req(t, app, http.MethodPatch, "/v1/auto/automations/"+id, "acme", map[string]any{"permissions": "auto"}); r.Code != http.StatusOK {
			t.Fatalf("fixing it = %d %s", r.Code, r.Body)
		}
	})
}
