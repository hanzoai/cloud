package patrol

import (
	"encoding/json"
	"net/http"
	"testing"
)

// surface_test.go asks the generic plane the questions the operational plane
// already answers. /v1/framework/patrol.* serves the SAME documents through the
// engine's role calculus, and a right on a DocType is a right on every row and
// on every field of it — so a record the machine owns is only as append-only as
// that plane leaves it.

// wire decodes one unenveloped document response — what createDocument and
// getDocument answer with.
func wire(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode document: %v (%s)", err, first(raw, 200))
	}
	return m
}

// named is the document name off such a response.
func named(t *testing.T, raw []byte) string {
	t.Helper()
	s, _ := wire(t, raw)["name"].(string)
	if s == "" {
		t.Fatalf("no document name in %s", first(raw, 200))
	}
	return s
}

// TestCreateHoldsAReadOnlyFieldToo holds a CREATE to the rule fixed() holds a
// PUT to. An incident's state and its stamps are the machine's: incident.go
// moves them one act at a time, through the transition table, and each move
// leaves a step. A create that lands them directly asserts the same fact a
// second way — an incident closed inside its SLA with no act, no role and no
// trail behind it, which is the record an insurer and a court read off it.
func TestCreateHoldsAReadOnlyFieldToo(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := call(t, app, http.MethodPost, "/v1/framework/"+DTIncident.String(), org, controller,
		map[string]any{
			"site": "depot", "ref": "ARC-FORGED", "trigger": "Intruder",
			"state":   Closed,
			"opened":  "2026-09-16 02:00:00",
			"onsite":  "2026-09-16 02:03:00",
			"secured": "2026-09-16 02:09:00",
			"closed":  "2026-09-16 02:10:00",
			"outcome": "attended, secured, three minute response",
		})
	if code != http.StatusCreated {
		return // refused, which is the answer
	}
	name := named(t, raw)

	got, body := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
	if got != http.StatusOK {
		t.Fatalf("read the created incident want 200, got %d (%s)", got, first(body, 200))
	}
	var inc patrolIncident
	data(t, body, &inc)
	t.Errorf("a controller created incident %s at state %q, closed %q, with %d acts on its trail, "+
		"through POST /v1/framework/%s: fixed() gates the PUT and nothing gates the create, so the "+
		"state machine is a suggestion", name, inc.State, inc.Closed, len(inc.Steps), DTIncident)
}

// TestAFiledAccountHasNoWriter holds patrol.step and patrol.report to what
// filedPerms says about them. The engine's Access.Can answers true for EVERY
// right when the caller is a manager, and the org owner is a System Manager — so
// naming System Manager with Read alone grants nothing and withholds nothing.
// The trail is the whole audit: a right to write one is a right to forge it, and
// a right to delete one is a right to unfile a response that happened.
func TestAFiledAccountHasNoWriter(t *testing.T) {
	app := mount(t)
	name := opened(t, app)

	if code, raw := call(t, app, http.MethodPost, "/v1/framework/"+DTStep.String(), org, boss,
		map[string]any{
			"incident": name, "act": "ARRIVE", "from": Dispatched, "to": Onsite,
			"actor": org + "/" + officer, "detail": "attended", "at": "2026-09-16 02:03:00",
		}); code == http.StatusCreated {
		t.Errorf("the org owner wrote a patrol.step by hand (%d, %s): an act nobody performed is on "+
			"the trail, signed with an officer who was never there", code, first(raw, 160))
	}

	// And the step the incident already carries — the record of what did happen.
	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTStep.String(), org, boss, nil)
	if code != http.StatusOK {
		t.Fatalf("list steps want 200, got %d (%s)", code, first(raw, 200))
	}
	var steps []map[string]any
	data(t, raw, &steps)
	if len(steps) == 0 {
		t.Fatalf("the seeded incident carries no trail to erase")
	}
	victim, _ := steps[0]["name"].(string)
	if code, raw := call(t, app, http.MethodDelete,
		"/v1/framework/"+DTStep.String()+"/"+victim, org, boss, nil); code < 400 {
		t.Errorf("the org owner deleted step %s off an incident's trail (%d, %s): an append-only "+
			"record that can be shortened is not one", victim, code, first(raw, 160))
	}
}

// TestTheResponseRecordOutlivesItsControlRoom asks whether the control room can
// erase the response it ran. opsPerms names Delete on patrol.incident, on
// patrol.alarm — the activation exactly as received — and on patrol.movement,
// which the fixture calls append-only and which is the chain of custody for a
// customer's own keys.
func TestTheResponseRecordOutlivesItsControlRoom(t *testing.T) {
	app := mount(t)
	name := opened(t, app)

	if code, raw := call(t, app, http.MethodDelete,
		"/v1/framework/"+DTIncident.String()+"/"+name, org, controller, nil); code < 400 {
		t.Errorf("a controller deleted incident %s (%d, %s): the response is gone and the steps "+
			"left behind name an incident that no longer exists", name, code, first(raw, 160))
	}

	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "cabinet 3"})
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/key/K-1/move", org, controller,
		map[string]any{"act": "out", "actor": officer, "job": "tour"}); code != http.StatusOK {
		t.Fatalf("sign a key set out want 200, got %d (%s)", code, first(raw, 200))
	}
	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTMovement.String(), org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("list movements want 200, got %d (%s)", code, first(raw, 200))
	}
	var moves []map[string]any
	data(t, raw, &moves)
	if len(moves) == 0 {
		t.Fatalf("no movement was recorded to delete")
	}
	move, _ := moves[0]["name"].(string)
	if code, raw := call(t, app, http.MethodDelete,
		"/v1/framework/"+DTMovement.String()+"/"+move, org, controller, nil); code < 400 {
		t.Errorf("a controller deleted the movement that says who took a customer's keys (%d, %s)",
			code, first(raw, 160))
	}
}

// TestReplacingASiteKeepsTheFactsItWasNotAsked measures PUT's replace semantics
// on the register. validateDoc builds the stored document from the BODY alone,
// and fixed() restores only a READ-ONLY field the body left out. patrol.site
// declares none, so every fact an incomplete body omits is dropped. The panel
// account is what an activation matches on: a site that loses it stops opening
// incidents and is filed 202 from then on, silently.
func TestReplacingASiteKeepsTheFactsItWasNotAsked(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	seedSite(t, app, org, "depot", "1001", "northside")

	if code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTSite.String()+"/depot", org, manager,
		map[string]any{"name": "depot", "town": "Cork"}); code != http.StatusOK {
		t.Fatalf("a manager naming the town want 200, got %d (%s)", code, first(raw, 200))
	}

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/site/depot", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read the site want 200, got %d (%s)", code, first(raw, 200))
	}
	var st patrolSite
	data(t, raw, &st)
	if st.Account != "1001" {
		t.Errorf("a PUT that named only the town erased the panel account (%q): every activation "+
			"from this site now matches nothing and is filed 202", st.Account)
	}
	if st.Client != "northside" {
		t.Errorf("a PUT that named only the town erased the customer (%q): the site belongs to "+
			"nobody and its owner's portal no longer reaches it", st.Client)
	}
	if st.Lat == 0 || st.SLA == 0 {
		t.Errorf("a PUT that named only the town erased the coordinates and the SLA (lat=%v sla=%d): "+
			"nearest-unit now measures from null island", st.Lat, st.SLA)
	}
}
