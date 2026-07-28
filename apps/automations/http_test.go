package automations

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// TestOrgGating403 proves every data endpoint refuses a request with NO validated
// principal (the anonymous-forge path). A data plane never serves an unauthenticated
// principal.
func TestOrgGating403(t *testing.T) {
	app := newApp(t)
	gated := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/automations/connectors"},
		{http.MethodGet, "/v1/automations/flows"},
		{http.MethodPost, "/v1/automations/flows"},
		{http.MethodGet, "/v1/automations/flows/x"},
		{http.MethodGet, "/v1/automations/runs"},
		{http.MethodPost, "/v1/automations/flows/x/run"},
		{http.MethodPost, "/v1/automations/mcp"},
	}
	for _, g := range gated {
		if r := req(t, app, g.method, g.path, "", nil); r.Code != http.StatusForbidden {
			t.Fatalf("%s %s without principal want 403, got %d (%s)", g.method, g.path, r.Code, r.Body)
		}
	}
}

// TestConnectorsCatalog: the /connectors endpoint returns the full connector
// catalogue and, within it, the Tier-A executable connectors (core/slack/github/
// google_*). The count is not pinned to the seed — the full catalogue is embedded
// — so the invariant is ConnectorCount==len(Connectors) and every Tier-A connector
// is present. The retired /pieces path is proven a byte-identical alias.
func TestConnectorsCatalog(t *testing.T) {
	app := newApp(t)
	r := req(t, app, http.MethodGet, "/v1/automations/connectors", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("connectors want 200, got %d (%s)", r.Code, r.Body)
	}
	var cat Catalog
	if err := json.Unmarshal(r.Body, &cat); err != nil {
		t.Fatalf("connectors body: %v (%s)", err, r.Body)
	}
	if cat.ConnectorCount != len(cat.Connectors) || len(cat.Connectors) < 5 {
		t.Fatalf("catalogue inconsistent/too small: count=%d len=%d", cat.ConnectorCount, len(cat.Connectors))
	}
	byName := map[string]ConnectorMetadata{}
	for _, p := range cat.Connectors {
		byName[p.Name] = p
	}
	for _, want := range []string{"core", "slack", "github", "google"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("catalogue missing %q: %+v", want, cat.Connectors)
		}
	}
	if byName["slack"].Auth.Type != "bot_token" || !byName["slack"].Auth.Required {
		t.Fatalf("slack auth descriptor wrong: %+v", byName["slack"].Auth)
	}

	// Back-compat: the retired /pieces path is a pure alias — same 200, same body.
	alias := req(t, app, http.MethodGet, "/v1/automations/pieces", "acme", nil)
	if alias.Code != http.StatusOK || !bytes.Equal(alias.Body, r.Body) {
		t.Fatalf("/pieces alias must mirror /connectors: code=%d bodyEqual=%v", alias.Code, bytes.Equal(alias.Body, r.Body))
	}
}

// TestFlowCRUDHTTP exercises the flow lifecycle over HTTP with a validated principal,
// and proves cross-tenant invisibility at the HTTP boundary.
func TestFlowCRUDHTTP(t *testing.T) {
	app := newApp(t)

	// Create a flow with an initial draft version.
	create := req(t, app, http.MethodPost, "/v1/automations/flows", "acme", map[string]any{
		"displayName": "Nightly Sync",
		"trigger": map[string]any{
			"name": "trigger", "type": TriggerTypePiece, "displayName": "Start",
			"strategy": string(StrategyManual),
			"settings": map[string]any{"pieceName": "core", "triggerName": "manual"},
		},
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create flow want 201, got %d (%s)", create.Code, create.Body)
	}
	var pf populatedFlow
	if err := json.Unmarshal(create.Body, &pf); err != nil {
		t.Fatalf("create body: %v (%s)", err, create.Body)
	}
	if pf.Org != "acme" {
		t.Fatalf("flow projectId must be the org, got %q", pf.Org)
	}
	if pf.Version == nil || pf.Version.DisplayName != "Nightly Sync" {
		t.Fatalf("create must return the initial version: %+v", pf.Version)
	}
	flowID := pf.ID

	// GET returns the flow + latest version.
	get := req(t, app, http.MethodGet, "/v1/automations/flows/"+flowID, "acme", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get flow want 200, got %d (%s)", get.Code, get.Body)
	}

	// A different org cannot see it.
	if r := req(t, app, http.MethodGet, "/v1/automations/flows/"+flowID, "globex", nil); r.Code != http.StatusNotFound {
		t.Fatalf("globex GET acme flow want 404, got %d", r.Code)
	}
	// A different org's list is empty.
	rl := req(t, app, http.MethodGet, "/v1/automations/flows", "globex", nil)
	var listOut struct {
		Data []Flow `json:"data"`
	}
	_ = json.Unmarshal(rl.Body, &listOut)
	if len(listOut.Data) != 0 {
		t.Fatalf("globex must see zero flows, got %d", len(listOut.Data))
	}

	// DELETE.
	if r := req(t, app, http.MethodDelete, "/v1/automations/flows/"+flowID, "acme", nil); r.Code != http.StatusNoContent {
		t.Fatalf("delete flow want 204, got %d (%s)", r.Code, r.Body)
	}
	if r := req(t, app, http.MethodGet, "/v1/automations/flows/"+flowID, "acme", nil); r.Code != http.StatusNotFound {
		t.Fatalf("deleted flow GET want 404, got %d", r.Code)
	}
}

// TestOperationsApply proves the FlowOperation apply mutates the flow's version tree
// (ADD_ACTION into the linear chain) and honestly rejects an unsupported tree op.
func TestOperationsApply(t *testing.T) {
	app := newApp(t)

	create := req(t, app, http.MethodPost, "/v1/automations/flows", "acme", map[string]any{
		"displayName": "Builder Flow",
		"trigger": map[string]any{
			"name": "trigger", "type": TriggerTypePiece, "displayName": "Start",
			"strategy": string(StrategyManual),
			"settings": map[string]any{"pieceName": "core", "triggerName": "manual"},
		},
	})
	var pf populatedFlow
	_ = json.Unmarshal(create.Body, &pf)
	flowID := pf.ID

	// ADD_ACTION: append a core.http_request step after the trigger.
	addReq := map[string]any{
		"type": string(OpAddAction),
		"request": map[string]any{
			"parentStep": "",
			"action": map[string]any{
				"name": "http1", "type": ActionTypePiece, "displayName": "Fetch",
				"settings": map[string]any{"pieceName": "core", "actionName": "http_request", "input": map[string]any{"url": "https://example.com"}},
			},
		},
	}
	r := req(t, app, http.MethodPost, "/v1/automations/flows/"+flowID+"/operations", "acme", addReq)
	if r.Code != http.StatusOK {
		t.Fatalf("ADD_ACTION want 200, got %d (%s)", r.Code, r.Body)
	}
	var v FlowVersion
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("operation body: %v (%s)", err, r.Body)
	}
	if v.Trigger == nil || v.Trigger.NextAction == nil || v.Trigger.NextAction.Name != "http1" {
		t.Fatalf("ADD_ACTION must insert http1 into the chain: %+v", v.Trigger)
	}

	// CHANGE_NAME.
	nameReq := map[string]any{"type": string(OpChangeName), "request": map[string]any{"displayName": "Renamed"}}
	rn := req(t, app, http.MethodPost, "/v1/automations/flows/"+flowID+"/operations", "acme", nameReq)
	var v2 FlowVersion
	_ = json.Unmarshal(rn.Body, &v2)
	if v2.DisplayName != "Renamed" {
		t.Fatalf("CHANGE_NAME must rename, got %q", v2.DisplayName)
	}

	// An ADD_ACTION planting a ROUTER (tree op) is honestly rejected 422.
	badReq := map[string]any{
		"type": string(OpAddAction),
		"request": map[string]any{
			"parentStep": "http1",
			"action":     map[string]any{"name": "router1", "type": ActionTypeRouter, "displayName": "Route"},
		},
	}
	if rb := req(t, app, http.MethodPost, "/v1/automations/flows/"+flowID+"/operations", "acme", badReq); rb.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ROUTER add want 422 (unsupported), got %d (%s)", rb.Code, rb.Body)
	}
}
