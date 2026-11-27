package patrol

import (
	"net/http"
	"strings"
	"testing"
)

// red4_test.go — what a customer is handed once reach lets them through.

// TestACustomersIncidentCarriesNoInternalTrail. reach decides WHICH incident a
// customer reads; it says nothing about WHAT comes back. viewIncident attaches
// the whole act trail, so the customer is handed every controller's and every
// officer's identity and the free text they typed on each act — the same
// category of remark patrol.note is its own DocType to keep from them.
func TestACustomersIncidentCarriesNoInternalTrail(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-5", "account": "1001", "zone": "Z3", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("seed the activation want 201, got %d (%s)", code, first(raw, 200))
	}
	var made patrolActivation
	data(t, raw, &made)
	name := made.Incident.Name

	if code, _ := stepped(t, app, name, controller, map[string]any{
		"act": Verify, "detail": "keyholder is unreliable, do not call — police only"}); code != http.StatusOK {
		t.Fatalf("VERIFY want 200, got %d", code)
	}

	code, raw = call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("the customer reads their own incident want 200, got %d (%s)", code, first(raw, 200))
	}
	var v patrolIncident
	data(t, raw, &v)
	for _, s := range v.Steps {
		if strings.Contains(s.Detail, "keyholder is unreliable") {
			t.Errorf("the customer was handed the control room's own remark on the act trail: %q", s.Detail)
		}
		if strings.Contains(s.Actor, controller) {
			t.Errorf("the customer was handed the identity of the controller who worked it: %q", s.Actor)
		}
	}
}
