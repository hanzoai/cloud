package patrol

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
)

// register_test.go drives the reads that answer a register whole — the key
// register, the camera list, the note book — and the writes that move one row of
// it. Each one is read by the roles that hold it and by nobody else, and what
// comes back is the row as it stands.

// TestKeyRegisterReadsWhatItHolds proves the register answers the sets it holds,
// and that one set reads back by its own reference.
func TestKeyRegisterReadsWhatItHolds(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe", "holder": officer})
	create(t, app, org, DTKey, map[string]any{"ref": "K-2", "site": "depot", "where": "cabinet"})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/key", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("the control room reading the register want 200, got %d (%s)", code, raw)
	}
	var keys []patrolKey
	data(t, raw, &keys)
	if len(keys) != 2 {
		t.Fatalf("the register holds %d sets, want 2: %+v", len(keys), keys)
	}
	held := map[string]patrolKey{}
	for _, k := range keys {
		held[k.Name] = k
	}
	if held["K-1"].Site != "depot" || held["K-1"].Where != "safe" || held["K-1"].Holder != officer {
		t.Errorf("a register row must say where the set is and who has it: %+v", held["K-1"])
	}

	code, raw = call(t, app, http.MethodGet, "/v1/patrol/key/K-2", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read one key set want 200, got %d (%s)", code, raw)
	}
	var one patrolKey
	data(t, raw, &one)
	if one.Name != "K-2" || one.Where != "cabinet" || one.Holder != "" {
		t.Errorf("one set reads back as it stands: %+v", one)
	}
	// Neither the officer nor the customer reaches a single set either.
	for _, user := range []string{officer, buyer} {
		if code, _ := call(t, app, http.MethodGet, "/v1/patrol/key/K-2", org, user, nil); code != http.StatusForbidden {
			t.Errorf("%s reading a key set want 403, got %d", user, code)
		}
	}
}

// TestAKeyAuditMovesNobody proves the third act the register takes: counting a
// set records the movement and leaves the holder where it was, which is what
// separates an audit from a signing-out.
func TestAKeyAuditMovesNobody(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe"})

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/key/K-1/move", org, controller,
		map[string]any{"act": "out", "holder": officer}); code != http.StatusOK {
		t.Fatalf("sign the set out: %d (%s)", code, raw)
	}
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/key/K-1/move", org, controller,
		map[string]any{"act": "audit", "job": "quarterly count"})
	if code != http.StatusOK {
		t.Fatalf("count the set want 200, got %d (%s)", code, raw)
	}
	var k patrolKey
	data(t, raw, &k)
	if k.Holder != officer {
		t.Errorf("counting a set moved it from %q to %q", officer, k.Holder)
	}
	if k.Moved == "" {
		t.Error("counting a set must stamp when it was counted")
	}
}

// TestCameraListIsWhatTheCallerMayOpen proves the camera list is the shift's,
// and that it says where a camera is and never how to reach it.
func TestCameraListIsWhatTheCallerMayOpen(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedSite(t, app, org, "yard", "1002", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard gate", "kind": "dome", "online": true,
		"secret": kept(org, "cam-1"),
	})
	create(t, app, org, DTCamera, map[string]any{"name": "cam-2", "site": "yard", "label": "Loading bay"})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/camera", org, officer, nil)
	if code != http.StatusOK {
		t.Fatalf("an officer listing cameras want 200, got %d (%s)", code, raw)
	}
	var cams []patrolCamera
	data(t, raw, &cams)
	if len(cams) != 2 {
		t.Fatalf("the register holds %d cameras, want 2: %+v", len(cams), cams)
	}
	for _, c := range cams {
		if c.Name == "cam-1" && (c.Label != "Yard gate" || c.Kind != "dome" || !c.Online) {
			t.Errorf("a camera row must carry its label, kind and health: %+v", c)
		}
		if c.Name == "cam-2" && c.Online {
			t.Errorf("a camera that has not reported healthy reads online: %+v", c)
		}
	}
	// ?site= narrows it to one premises.
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/camera?site=yard", org, controller, nil)
	data(t, raw, &cams)
	if code != http.StatusOK || len(cams) != 1 || cams[0].Name != "cam-2" {
		t.Fatalf("?site= must narrow the list to that premises: %+v", cams)
	}
}

// TestNotesReadBackToTheShift proves the note book: what the shift writes is
// what it reads, and ?subject= answers the remarks about one thing.
func TestNotesReadBackToTheShift(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	for _, n := range []map[string]any{
		{"subject": "depot", "body": "dog on site after 8"},
		{"subject": "depot", "body": "gate code changed"},
		{"subject": "yard", "body": "keys with the caretaker"},
	} {
		if code, raw := call(t, app, http.MethodPost, "/v1/patrol/note", org, officer, n); code != http.StatusCreated {
			t.Fatalf("write a note want 201, got %d (%s)", code, raw)
		}
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/note", org, supervisor, nil)
	if code != http.StatusOK {
		t.Fatalf("list notes want 200, got %d (%s)", code, raw)
	}
	var notes []patrolNote
	data(t, raw, &notes)
	if len(notes) != 3 {
		t.Fatalf("the book holds %d remarks, want 3", len(notes))
	}
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/note?subject=yard", org, officer, nil)
	data(t, raw, &notes)
	if code != http.StatusOK || len(notes) != 1 || notes[0].Body != "keys with the caretaker" {
		t.Fatalf("?subject= must answer the remarks about one thing: %+v", notes)
	}
	if notes[0].Actor == "" || notes[0].At == "" {
		t.Errorf("a remark must say who wrote it and when: %+v", notes[0])
	}
}

// TestUnitStateMovesTheUnit proves the state route: a unit goes on a break and
// comes back, the row that answers says so, and it carries no trail — the state
// screen asks where a unit is, not where it has been.
func TestUnitStateMovesTheUnit(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/fix", org, officer,
		map[string]any{"lat": 51.95, "lon": -8.50}); code != http.StatusOK {
		t.Fatalf("seed a position: %d (%s)", code, raw)
	}

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/state", org, officer,
		map[string]any{"state": "break"})
	if code != http.StatusOK {
		t.Fatalf("an officer putting their own unit on a break want 200, got %d (%s)", code, raw)
	}
	var u patrolUnit
	data(t, raw, &u)
	if u.State != "break" || u.Name != "gf-01" {
		t.Fatalf("the unit answered %+v, want it on a break", u)
	}
	if len(u.Trail) != 0 {
		t.Errorf("the state answer carries %d positions; the trail is the map's read", len(u.Trail))
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/state", org, controller,
		map[string]any{"state": UnitAvailable}); code != http.StatusOK {
		t.Fatalf("the control room may move any unit, got %d (%s)", code, raw)
	}
}

// TestAUnitsTrailIsTheTailOfIt proves the bound on the map read: a unit that has
// reported all shift is drawn from the last trailFixes positions, oldest first,
// and not from every fix it ever wrote.
func TestAUnitsTrailIsTheTailOfIt(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	const reported = trailFixes + 5
	for i := range reported {
		if _, err := framework.Ingest(context.Background(), org, DTFix, map[string]any{
			"unit": "gf-01", "lat": 51.90, "lon": -8.47,
			"at": fmt.Sprintf("2026-01-02 %02d:%02d:00", 20+i/60, i%60),
		}, ""); err != nil {
			t.Fatalf("seed position %d: %v", i, err)
		}
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/unit", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("list units want 200, got %d (%s)", code, raw)
	}
	var units []patrolUnit
	data(t, raw, &units)
	if len(units) != 1 {
		t.Fatalf("the fleet has %d units, want 1", len(units))
	}
	if len(units[0].Trail) != trailFixes {
		t.Fatalf("a unit that reported %d times draws %d positions, want the last %d",
			reported, len(units[0].Trail), trailFixes)
	}
}

// TestSiteReadCarriesTheZonesAndHazards proves what rides inside a site
// document: the panel's zones and the standing warnings come back on a single
// read and are left out of a list, which is the difference the two shapes exist
// for.
func TestSiteReadCarriesTheZonesAndHazards(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTSite, map[string]any{
		"name": "depot", "account": "1001", "tier": "gold", "sla": 20,
		"lat": 51.90, "lon": -8.47, "status": SiteOK,
		"zones": []map[string]any{
			{"ref": "Z1", "location": "Rear door", "device": "PIR", "state": "sealed"},
			{"ref": "Z3", "location": "Office", "device": "contact", "state": "sealed"},
		},
		"hazards": []map[string]any{{"hazard": "Dog on site after 20:00"}},
	})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/site/depot", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read a site want 200, got %d (%s)", code, raw)
	}
	var st patrolSite
	data(t, raw, &st)
	if len(st.Zones) != 2 || st.Zones[0].Ref != "Z1" || st.Zones[0].Location != "Rear door" ||
		st.Zones[0].Device != "PIR" || st.Zones[0].State != "sealed" {
		t.Fatalf("a site read whole must carry the panel's zones: %+v", st.Zones)
	}
	if len(st.Hazards) != 1 || st.Hazards[0] != "Dog on site after 20:00" {
		t.Fatalf("a site read whole must carry the standing warnings: %+v", st.Hazards)
	}
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/site", org, controller, nil)
	var list []patrolSite
	data(t, raw, &list)
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("list sites want one site, got %d (%s)", code, raw)
	}
	if len(list[0].Zones) != 0 || len(list[0].Hazards) != 0 {
		t.Errorf("a list carries what rides inside the document: %+v", list[0])
	}
}

// TestSiteListNarrowsByStatus proves ?status= asks the store rather than the
// answer: the estate in alarm is the one a controller opens the screen on.
func TestSiteListNarrowsByStatus(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedSite(t, app, org, "yard", "1002", "")
	if code, raw := call(t, app, http.MethodPut, "/v1/patrol/site/yard", org, manager,
		map[string]any{"status": SiteAlarm}); code != http.StatusOK {
		t.Fatalf("put a site into alarm: %d (%s)", code, raw)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/site?status="+SiteAlarm, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("list the estate in alarm want 200, got %d (%s)", code, raw)
	}
	var sites []patrolSite
	data(t, raw, &sites)
	if len(sites) != 1 || sites[0].Name != "yard" {
		t.Fatalf("?status=%s answered %+v, want the one site in alarm", SiteAlarm, sites)
	}
}

// TestToursToday proves ?date=today is read as the day it is, so the screen that
// asks for the round in progress does not ask for a date it computed itself.
func TestToursToday(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	today := create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": now()[:10]})
	create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": "2026-01-02"})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/tour?date=today", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("today's rounds want 200, got %d (%s)", code, raw)
	}
	var tours []patrolTour
	data(t, raw, &tours)
	if len(tours) != 1 || tours[0].Name != today["name"].(string) {
		t.Fatalf("?date=today answered %+v, want the round for the day it is", tours)
	}
}
