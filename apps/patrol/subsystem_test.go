package patrol

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/zap-proto/zip"
)

// subsystem_test.go drives the routes: who is admitted, what each one answers,
// and the two paths through the activation intake.

// TestRolelessCallerIsRefusedOnEveryRoute proves a member of the org holding no
// patrol role reaches nothing but the anonymous tenant face.
func TestRolelessCallerIsRefusedOnEveryRoute(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	reads := []string{
		"/v1/patrol/snapshot", "/v1/patrol/site", "/v1/patrol/site/depot",
		"/v1/patrol/unit", "/v1/patrol/unit/near?site=depot", "/v1/patrol/tour",
		"/v1/patrol/key", "/v1/patrol/key/K-1", "/v1/patrol/camera",
		"/v1/patrol/camera/cam-1/stream", "/v1/patrol/event", "/v1/patrol/incident",
		"/v1/patrol/incident/inc-00001", "/v1/patrol/note", "/v1/patrol/report/r1",
	}
	for _, path := range reads {
		if code, raw := call(t, app, http.MethodGet, path, org, nobody, nil); code != http.StatusForbidden {
			t.Errorf("GET %s by a role-less caller want 403, got %d (%s)", path, code, raw)
		}
	}
	writes := []struct {
		path string
		body map[string]any
	}{
		{"/v1/patrol/unit/gf-01/fix", map[string]any{"lat": 51.9, "lon": -8.4}},
		{"/v1/patrol/unit/gf-01/state", map[string]any{"state": UnitAvailable}},
		{"/v1/patrol/point/p1/confirm", map[string]any{"method": "tag"}},
		{"/v1/patrol/key/K-1/move", map[string]any{"act": "out", "actor": "x"}},
		{"/v1/patrol/event", map[string]any{"level": LevelCyan, "title": "x"}},
		{"/v1/patrol/incident", map[string]any{"site": "depot"}},
		{"/v1/patrol/incident/inc-00001/step", map[string]any{"act": Verify}},
		{"/v1/patrol/note", map[string]any{"subject": "x"}},
		{"/v1/patrol/alarm", map[string]any{"ref": "a", "account": "1001"}},
		{"/v1/patrol/report", map[string]any{"kind": ReportShift}},
	}
	for _, w := range writes {
		if code, raw := call(t, app, http.MethodPost, w.path, org, nobody, w.body); code != http.StatusForbidden {
			t.Errorf("POST %s by a role-less caller want 403, got %d (%s)", w.path, code, raw)
		}
	}
	if code, raw := call(t, app, http.MethodPut, "/v1/patrol/site/depot", org, nobody,
		map[string]any{"town": "Cork"}); code != http.StatusForbidden {
		t.Errorf("PUT site by a role-less caller want 403, got %d (%s)", code, raw)
	}
	// And an unauthenticated caller reaches none of it either.
	if code, _ := hit(t, app, http.MethodGet, "/v1/patrol/snapshot", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("GET snapshot with no principal want 401, got %d", code)
	}
}

// TestTenantIsAnonymous proves the one public route: it answers with no
// principal at all, and it carries the public face and not the contact lists.
func TestTenantIsAnonymous(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	if code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTTenant.String()+"/"+DTTenant.Name, org, boss,
		map[string]any{
			"brand":       map[string]any{"name": "Northstar"},
			"company":     map[string]any{"legalName": "Northstar Security Ltd"},
			"contacts":    map[string]any{"controllers": []string{controller}},
			"operations":  map[string]any{"verifyTargetSeconds": 90},
			"features":    map[string]any{"patrolTours": true},
			"terminology": map[string]any{"siteSingular": "site"},
		}); code != http.StatusOK {
		t.Fatalf("write tenant want 200, got %d (%s)", code, raw)
	}
	code, raw := hit(t, app, http.MethodGet, "/v1/patrol/tenant/"+org, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("anonymous tenant read want 200, got %d (%s)", code, raw)
	}
	body := string(raw)
	if !strings.Contains(body, "Northstar") {
		t.Fatalf("the tenant face must carry the brand: %s", body)
	}
	if strings.Contains(body, "controllers") {
		t.Fatalf("the tenant face must not carry the contact lists: %s", body)
	}
	// An org that has written none is 404, not an empty object.
	if code, _ := hit(t, app, http.MethodGet, "/v1/patrol/tenant/nowhere", nil, nil); code != http.StatusNotFound {
		t.Fatalf("an org with no tenant document want 404, got %d", code)
	}
}

// TestAlarmMatchedOpensAnIncident proves the intake's first path: an activation
// carrying a panel account that a site claims opens an incident in received,
// puts the site into alarm, and answers 201.
func TestAlarmMatchedOpensAnIncident(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-7", "account": "1001", "zone": "Z3",
		"trigger": "Intruder — rear door", "source": "dc09",
		"raw": map[string]any{"line": "1001 E130 Z3"},
	})
	if code != http.StatusCreated {
		t.Fatalf("a matched activation want 201, got %d (%s)", code, raw)
	}
	var got patrolActivation
	data(t, raw, &got)
	if !got.Matched || got.Site != "depot" || got.Incident == nil {
		t.Fatalf("a matched activation must name its site and its incident: %+v", got)
	}
	if got.Incident.State != Received {
		t.Fatalf("a new incident is %s, want %s", got.Incident.State, Received)
	}
	if got.Incident.Tier != "gold" || got.Incident.SLA != 20 {
		t.Fatalf("the incident must carry the site's tier and SLA: %+v", got.Incident)
	}

	// The site is in alarm, and the feed carries a red row.
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/site/depot", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read site want 200, got %d", code)
	}
	var st patrolSite
	data(t, raw, &st)
	if st.Status != SiteAlarm {
		t.Fatalf("the site is %s after an activation, want %s", st.Status, SiteAlarm)
	}
	if !hasEvent(t, app, LevelRed) {
		t.Fatal("a matched activation must raise a red feed row")
	}
}

// TestAlarmUnmatchedIsStillRecorded proves the intake's second path: an
// activation no site claims is recorded anyway, raises an amber row, opens no
// incident, and answers 202. An activation is never dropped.
func TestAlarmUnmatchedIsStillRecorded(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-8", "account": "9999", "zone": "Z1", "trigger": "Panic",
	})
	if code != http.StatusAccepted {
		t.Fatalf("an unmatched activation want 202, got %d (%s)", code, raw)
	}
	var got patrolActivation
	data(t, raw, &got)
	if got.Matched || got.Incident != nil || got.Site != "" {
		t.Fatalf("an unmatched activation must claim no site and no incident: %+v", got)
	}
	if !hasEvent(t, app, LevelAmber) {
		t.Fatal("an unmatched activation must raise an amber feed row")
	}
	// It is on record.
	code, raw = call(t, app, http.MethodGet, "/v1/framework/"+DTAlarm.String(), org, boss, nil)
	if code != http.StatusOK || !strings.Contains(string(raw), "ARC-8") {
		t.Fatalf("the activation must be stored whether or not it matched: %d %s", code, raw)
	}
	// And no incident was opened.
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/incident", org, controller, nil)
	var incs []patrolIncident
	data(t, raw, &incs)
	if code != http.StatusOK || len(incs) != 0 {
		t.Fatalf("an unmatched activation opened %d incidents, want 0", len(incs))
	}
}

// TestAlarmAdmitsTheMachineCredential proves the receiving centre's own identity
// reaches the intake, and that a client never does.
func TestAlarmAdmitsTheMachineCredential(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := hit(t, app, http.MethodPost, "/v1/patrol/alarm",
		map[string]any{"ref": "ARC-9", "account": "1001", "trigger": "Fire"},
		map[string]string{"X-Org-Id": org, "X-User-Id": "arc-receiver", "X-User-IsApp": "true"})
	if code != http.StatusCreated {
		t.Fatalf("the org's machine credential want 201, got %d (%s)", code, raw)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, buyer,
		map[string]any{"ref": "ARC-10", "account": "1001"}); code != http.StatusForbidden {
		t.Fatalf("a client posting an activation want 403, got %d", code)
	}
}

// TestSnapshotAnswersTheWholeScreen proves the one read that replaces five.
func TestSnapshotAnswersTheWholeScreen(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe"})
	call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller,
		map[string]any{"ref": "ARC-1", "account": "1001", "trigger": "Intruder"})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/snapshot", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("snapshot want 200, got %d (%s)", code, raw)
	}
	var got patrolEstate
	data(t, raw, &got)
	if len(got.Sites) != 1 || len(got.Units) != 1 || len(got.Keys) != 1 {
		t.Fatalf("snapshot must carry the estate, the fleet and the key register: %+v", got)
	}
	if len(got.Events) == 0 {
		t.Fatal("snapshot must carry the feed")
	}
	if got.Incident == nil || got.Incident.State != Received {
		t.Fatalf("snapshot must carry the running incident: %+v", got.Incident)
	}
}

// TestClientReadsOnlyTheirOwnEstate proves the narrowing is taken from the
// caller's role, never from a query parameter, and that the key register is
// empty for them.
func TestClientReadsOnlyTheirOwnEstate(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	create(t, app, org, DTClient, map[string]any{"name": "southside", "contact": "other@acme.test"})
	seedSite(t, app, org, "theirs", "1001", "northside")
	seedSite(t, app, org, "not-theirs", "1002", "southside")
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "theirs", "where": "safe"})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/site", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("a client listing sites want 200, got %d (%s)", code, raw)
	}
	var sites []patrolSite
	data(t, raw, &sites)
	if len(sites) != 1 || sites[0].Name != "theirs" {
		t.Fatalf("a client sees %d sites, want only their own: %+v", len(sites), sites)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/site/not-theirs", org, buyer, nil); code != http.StatusNotFound {
		t.Fatalf("a client reading another customer's site want 404, got %d", code)
	}
	// The key register never reaches them.
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/key", org, buyer, nil); code != http.StatusForbidden {
		t.Fatalf("a client reading the key register want 403, got %d", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/key", org, officer, nil); code != http.StatusForbidden {
		t.Fatalf("an officer reading the key register want 403, got %d", code)
	}
	// The snapshot narrows the same way and hands them no keys.
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/snapshot", org, buyer, nil)
	var got patrolEstate
	data(t, raw, &got)
	if code != http.StatusOK || len(got.Sites) != 1 || len(got.Keys) != 0 {
		t.Fatalf("a client's snapshot carries %d sites and %d keys, want 1 and 0", len(got.Sites), len(got.Keys))
	}
}

// TestFixMovesTheUnitAndItsTrail proves a position report writes the trail, moves
// the unit, and that an officer may report only their own.
func TestFixMovesTheUnitAndItsTrail(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	seedUnit(t, app, org, "gf-02", "other@acme.test", UnitAvailable, 52.00, -8.00)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/fix", org, officer,
		map[string]any{"lat": 51.95, "lon": -8.50}); code != http.StatusOK {
		t.Fatalf("an officer reporting their own unit want 200, got %d (%s)", code, raw)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-02/fix", org, officer,
		map[string]any{"lat": 51.95, "lon": -8.50}); code != http.StatusForbidden {
		t.Fatalf("an officer reporting another unit want 403, got %d", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-02/fix", org, controller,
		map[string]any{"lat": 51.95, "lon": -8.50}); code != http.StatusOK {
		t.Fatalf("the control room may report any unit, got %d", code)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/unit", org, controller, nil)
	var units []patrolUnit
	data(t, raw, &units)
	if code != http.StatusOK {
		t.Fatalf("list units want 200, got %d", code)
	}
	for _, u := range units {
		if u.Name != "gf-01" {
			continue
		}
		if u.Lat != 51.95 || u.Seen == "" {
			t.Fatalf("the reported unit did not move: %+v", u)
		}
		if len(u.Trail) != 1 {
			t.Fatalf("the unit's trail has %d fixes, want 1", len(u.Trail))
		}
	}
}

// TestNearRoute proves the nearest-unit read over the wire, with three units at
// one site.
func TestNearRoute(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitOnsite, 51.8990, -8.4760)
	seedUnit(t, app, org, "gf-02", "b@acme.test", UnitAvailable, 51.9200, -8.5000)
	seedUnit(t, app, org, "gf-03", "c@acme.test", UnitAvailable, 52.2000, -8.9000)

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/unit/near?site=depot&limit=3", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("nearest want 200, got %d (%s)", code, raw)
	}
	var got []patrolNearest
	data(t, raw, &got)
	if len(got) != 3 || got[0].Unit != "gf-02" {
		t.Fatalf("nearest answered %+v, want the nearest FREE unit first", got)
	}
	if got[0].Km <= 0 || got[0].Eta <= 0 {
		t.Fatalf("nearest must carry a distance and an eta: %+v", got[0])
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/unit/near?site=depot", org, officer, nil); code != http.StatusForbidden {
		t.Fatalf("an officer asking who is nearest want 403, got %d", code)
	}
}

// TestKeyMovementWritesTheRegister proves a key leaving and returning moves the
// register row and leaves a movement behind it.
func TestKeyMovementWritesTheRegister(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe"})

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/key/K-1/move", org, controller,
		map[string]any{"act": "out", "holder": officer, "job": "callout"})
	if code != http.StatusOK {
		t.Fatalf("sign a key out want 200, got %d (%s)", code, raw)
	}
	var k patrolKey
	data(t, raw, &k)
	if k.Holder != officer || k.Moved == "" {
		t.Fatalf("a key signed out must name its holder and when: %+v", k)
	}
	code, raw = call(t, app, http.MethodPost, "/v1/patrol/key/K-1/move", org, controller,
		map[string]any{"act": "in", "holder": officer})
	data(t, raw, &k)
	if code != http.StatusOK || k.Holder != "" {
		t.Fatalf("a key signed back in holds nobody, got %d %+v", code, k)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/key/K-1/move", org, controller,
		map[string]any{"act": "borrow"}); code != http.StatusBadRequest {
		t.Fatalf("an act outside out/in/audit want 400, got %d", code)
	}
	code, raw = call(t, app, http.MethodGet, "/v1/framework/"+DTMovement.String(), org, boss, nil)
	if code != http.StatusOK || strings.Count(string(raw), `"key":"K-1"`) != 2 {
		t.Fatalf("two movements must be on record: %s", raw)
	}
}

// TestCheckpointConfirmation proves presence is recorded, and that a
// confirmation after the window shuts is recorded late rather than refused.
func TestCheckpointConfirmation(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	tour := create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": "2026-01-02"})
	tourName, _ := tour["name"].(string)
	inTime := create(t, app, org, DTPoint, map[string]any{
		"tour": tourName, "site": "depot", "tag": "T1",
		"open": "2026-01-02 20:00:00", "close": "2099-01-02 22:00:00",
	})
	shut := create(t, app, org, DTPoint, map[string]any{
		"tour": tourName, "site": "depot", "tag": "T2",
		"open": "2026-01-02 20:00:00", "close": "2026-01-02 22:00:00",
	})

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/point/"+inTime["name"].(string)+"/confirm", org, officer,
		map[string]any{"method": "tag", "lat": 51.90, "lon": -8.47})
	if code != http.StatusOK {
		t.Fatalf("confirm a checkpoint want 200, got %d (%s)", code, raw)
	}
	var p patrolCheckpoint
	data(t, raw, &p)
	if p.State != "done" || p.At == "" || p.Method != "tag" {
		t.Fatalf("a confirmation in the window is done: %+v", p)
	}
	code, raw = call(t, app, http.MethodPost, "/v1/patrol/point/"+shut["name"].(string)+"/confirm", org, officer,
		map[string]any{"method": "manual"})
	data(t, raw, &p)
	if code != http.StatusOK || p.State != "late" {
		t.Fatalf("a confirmation after the window shuts is recorded late, got %d %+v", code, p)
	}
	// The tour read carries both.
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/tour?date=2026-01-02", org, controller, nil)
	var tours []patrolTour
	data(t, raw, &tours)
	if code != http.StatusOK || len(tours) != 1 || len(tours[0].Points) != 2 {
		t.Fatalf("the round must carry its checkpoints: %+v", tours)
	}
}

// TestReportFiledAndDownloaded proves a report is composed from the estate,
// stored as text, and answered as a PDF — with ?format=text handing back the
// body verbatim.
func TestReportFiledAndDownloaded(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportShift})
	if code != http.StatusCreated {
		t.Fatalf("file a report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)
	if r.Kind != ReportShift || !strings.Contains(r.Body, "ESTATE STATUS") {
		t.Fatalf("a shift report must carry the estate figures: %+v", r)
	}

	code, text := call(t, app, http.MethodGet, "/v1/patrol/report/"+r.Name+"?format=text", org, controller, nil)
	if code != http.StatusOK || string(text) != r.Body {
		t.Fatalf("?format=text must hand back the body verbatim: %d", code)
	}
	code, pdf := call(t, app, http.MethodGet, "/v1/patrol/report/"+r.Name, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("download a report want 200, got %d", code)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("a report downloads as a PDF, got %q", first(pdf, 16))
	}
	// An incident report needs an incident.
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportIncident}); code != http.StatusBadRequest {
		t.Fatalf("an incident report with no incident want 400, got %d", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": "annual"}); code != http.StatusBadRequest {
		t.Fatalf("a kind outside shift/incident/client want 400, got %d", code)
	}
}

// TestEventRoute proves a controller may write one feed row by hand and read it
// back, and that an officer may not write one.
func TestEventRoute(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	seedSite(t, app, org, "depot", "1001", "northside")

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelAmber, "title": "Gate left open", "site": "depot"})
	if code != http.StatusCreated {
		t.Fatalf("raise an event want 201, got %d (%s)", code, raw)
	}
	var e patrolEvent
	data(t, raw, &e)
	if e.Level != LevelAmber || e.Title != "Gate left open" || e.At == "" {
		t.Fatalf("the row must carry its level, title and time: %+v", e)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelRed}); code != http.StatusBadRequest {
		t.Fatalf("a row with no title want 400, got %d", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/event", org, officer,
		map[string]any{"level": LevelRed, "title": "x"}); code != http.StatusForbidden {
		t.Fatalf("an officer raising a row want 403, got %d", code)
	}
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/event?level="+LevelAmber, org, buyer, nil)
	var rows []patrolEvent
	data(t, raw, &rows)
	if code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("the amber rows are %d, want 1", len(rows))
	}
}

// TestNoteRoute proves an internal remark is written and read by the shift, and
// never by the customer.
func TestNoteRoute(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/note", org, officer,
		map[string]any{"subject": "depot", "body": "dog on site after 8"})
	if code != http.StatusCreated {
		t.Fatalf("write a note want 201, got %d (%s)", code, raw)
	}
	var n patrolNote
	data(t, raw, &n)
	if n.Actor == "" {
		t.Fatalf("a note must record who wrote it: %+v", n)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/note", org, buyer, nil); code != http.StatusForbidden {
		t.Fatalf("a client reading notes want 403, got %d", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/note", org, officer,
		map[string]any{"body": "no subject"}); code != http.StatusBadRequest {
		t.Fatalf("a note with no subject want 400, got %d", code)
	}
}

// TestEditSite proves a manager may change the contract fields and that the
// register facts are left alone.
func TestEditSite(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := call(t, app, http.MethodPut, "/v1/patrol/site/depot", org, manager,
		map[string]any{"town": "Cork", "sla": 15, "notes": "gate code 4412"})
	if code != http.StatusOK {
		t.Fatalf("edit a site want 200, got %d (%s)", code, raw)
	}
	var st patrolSite
	data(t, raw, &st)
	if st.Town != "Cork" || st.SLA != 15 || st.Notes != "gate code 4412" {
		t.Fatalf("the edit did not land: %+v", st)
	}
	if st.Account != "1001" || st.Lat == 0 {
		t.Fatalf("the register facts must survive an edit: %+v", st)
	}
	if code, _ := call(t, app, http.MethodPut, "/v1/patrol/site/depot", org, controller,
		map[string]any{"town": "Mallow"}); code != http.StatusForbidden {
		t.Fatalf("a controller editing a site want 403, got %d", code)
	}
}

// TestCameraNeverCarriesItsCredential proves the stream credential is not part
// of a camera row, and that a camera with none registered mints nothing.
func TestCameraNeverCarriesItsCredential(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true,
	})
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera?site=depot", org, buyer, nil)
	if code != http.StatusOK {
		t.Fatalf("list cameras want 200, got %d (%s)", code, raw)
	}
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("a camera row must never carry its credential: %s", raw)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, officer, nil); code != http.StatusNotFound {
		t.Fatalf("a camera with no credential mints nothing, got %d", code)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/camera/cam-1/stream", org, buyer, nil); code != http.StatusForbidden {
		t.Fatalf("a client opening a stream want 403, got %d", code)
	}
}

// TestFeedFrames proves the wire a browser reads over fetch: each frame is one
// `data:` record terminated by a blank line, and the stream opens and beats with
// comments a reader ignores.
func TestFeedFrames(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if !open(w) || !send(w, frame{Kind: "fix", Unit: "gf-01", Lat: 51.9, Lon: -8.4, At: "2026-01-02 20:00:00"}) || !ping(w) {
		t.Fatal("writing to a live reader must not report it gone")
	}
	got := buf.String()
	want := ": stream open\n\n" +
		`data: {"kind":"fix","unit":"gf-01","lat":51.9,"lon":-8.4,"at":"2026-01-02 20:00:00"}` + "\n\n" +
		": ping\n\n"
	if got != want {
		t.Fatalf("the feed wire is\n%q\nwant\n%q", got, want)
	}
}

// TestFeedSubjectIsTheOrgsOwn proves a frame is published on one org's subject
// and no other, so a subscriber reaches only its own tenant.
func TestFeedSubjectIsTheOrgsOwn(t *testing.T) {
	if got, other := bus.Phys(org, Module), bus.Phys("other", Module); got == other {
		t.Fatalf("two orgs share the subject %q", got)
	}
	if got := bus.Phys(org, Module); !strings.Contains(got, org) || !strings.HasSuffix(got, Module) {
		t.Fatalf("the patrol subject for %s is %q", org, got)
	}
}

// hasEvent reports whether the feed carries a row at that level.
func hasEvent(t *testing.T, app *zip.App, level string) bool {
	t.Helper()
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/event?level="+level, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("list events want 200, got %d (%s)", code, raw)
	}
	var rows []patrolEvent
	data(t, raw, &rows)
	return len(rows) > 0
}

// first returns at most n bytes, for an error message about a body.
func first(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}
