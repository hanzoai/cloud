package patrol

import (
	"net/http"
	"testing"
)

// reach_test.go finishes the sentence reachOf starts. "Every read of a row about
// a site passes through here and no route carries a filter of its own" is a
// claim about /v1/patrol. The same rows are served a second time at
// /v1/framework/patrol.*, where the only question asked is which ROLE the caller
// holds — and readPerms hands the customer Read on every site and every
// incident in the org. A second door on one room is the whole of tenancy here.

// TestClientReadsNoOtherCustomersSiteOnTheGenericSurface asks the generic plane
// what isolation_test.go asks the operational one. patrol.site is
// readPerms(officer, client), so a customer holds Read on the DocType, and a
// right on a DocType is a right on every row of it: the estate, the panel
// account, the coordinates, the standing hazards and the zone-by-zone detector
// layout of every other customer's premises.
func TestClientReadsNoOtherCustomersSiteOnTheGenericSurface(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)

	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTSite.String(), org, buyer, nil)
	if code == http.StatusOK {
		var sites []map[string]any
		data(t, raw, &sites)
		for _, s := range sites {
			if s["name"] == "rivals" {
				t.Errorf("a client listed another customer's site through /v1/framework: %v", s)
			}
		}
	}

	code, raw = call(t, app, http.MethodGet, "/v1/framework/"+DTSite.String()+"/rivals", org, buyer, nil)
	if code == http.StatusOK {
		t.Errorf("a client read another customer's site whole through /v1/framework (%d): %s",
			code, first(raw, 240))
	}
}

// TestClientReadsNoOtherCustomersIncidentOnTheGenericSurface is the same
// question about the incident: where a break-in happened, what the officer
// found, and what it came to. /v1/patrol/incident narrows; the generic plane
// serving the same rows does not.
func TestClientReadsNoOtherCustomersIncidentOnTheGenericSurface(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-1", "account": "1002", "zone": "Z9", "trigger": "Intruder — server room",
	}); code != http.StatusCreated {
		t.Fatalf("seed the rival's activation want 201, got %d (%s)", code, first(raw, 200))
	}

	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTIncident.String(), org, buyer, nil)
	if code != http.StatusOK {
		return // refused, which is the answer
	}
	var incs []map[string]any
	data(t, raw, &incs)
	for _, i := range incs {
		if i["site"] == "rivals" {
			t.Errorf("a client listed another customer's incident through /v1/framework: %v", i)
		}
	}
}

// TestClientReadsNoOtherCustomersReport holds the filed account to the rule
// ownReport applies at /v1/patrol/report/{name}. filedPerms names the client in
// `audience`, so the customer holds Read on patrol.report on the generic plane —
// where nothing asks whose incident the report is about, and a shift report,
// which is the whole estate in one body, is about no incident at all.
func TestClientReadsNoOtherCustomersReport(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-1", "account": "1002", "zone": "Z9", "trigger": "Intruder — server room",
	})
	if code != http.StatusCreated {
		t.Fatalf("seed the rival's activation want 201, got %d (%s)", code, first(raw, 200))
	}
	var made patrolActivation
	data(t, raw, &made)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportIncident, "incident": made.Incident.Name}); code != http.StatusCreated {
		t.Fatalf("file the rival's incident report want 201, got %d (%s)", code, first(raw, 200))
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportShift}); code != http.StatusCreated {
		t.Fatalf("file a shift report want 201, got %d (%s)", code, first(raw, 200))
	}

	code, raw = call(t, app, http.MethodGet, "/v1/framework/"+DTReport.String(), org, buyer, nil)
	if code != http.StatusOK {
		return // refused, which is the answer
	}
	var reports []map[string]any
	data(t, raw, &reports)
	if len(reports) > 0 {
		t.Errorf("a client read %d filed reports through /v1/framework, none of them theirs — "+
			"the first body begins %q", len(reports), firstText(reports[0]["body"], 160))
	}
}

// TestClientReadsNoFleet holds the unit list to the rule the live feed already
// keeps. reach.carries drops a `fix` frame for a customer, because a unit's
// position names no site and belongs to the operation — and then GET
// /v1/patrol/unit admits all six and answers with every unit, the officer
// crewing it, and the tail of its position trail.
func TestClientReadsNoFleet(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/fix", org, officer,
		map[string]any{"lat": 51.95, "lon": -8.41}); code != http.StatusOK {
		t.Fatalf("seed a position want 200, got %d (%s)", code, first(raw, 200))
	}

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/unit", org, buyer, nil)
	if code != http.StatusOK {
		return // refused, which is the answer
	}
	var units []patrolUnit
	data(t, raw, &units)
	for _, u := range units {
		if u.Officer != "" || len(u.Trail) > 0 || u.Lat != 0 {
			t.Errorf("a client read the fleet: unit %q crewed by %q at %v,%v with %d positions on "+
				"its trail — a customer holding the live movements of another company's staff",
				u.Name, u.Officer, u.Lat, u.Lon, len(u.Trail))
		}
	}
}

// TestClientReadsNoOtherCustomersTour holds the proof-of-presence record to the
// same rule. A checkpoint names the site to attend, so a round is a row about a
// site; /tour and the tours riding inside /snapshot pass through no narrowing at
// all.
func TestClientReadsNoOtherCustomersTour(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	seedUnit(t, app, org, "gf-02", "other@acme.test", UnitAvailable, 51.90, -8.47)
	tour := create(t, app, org, DTTour, map[string]any{"unit": "gf-02", "date": "2026-09-16"})
	create(t, app, org, DTPoint, map[string]any{
		"tour": tour["name"], "site": "rivals", "tag": "REAR-DOOR", "state": PointPending,
	})

	code, raw := call(t, app, http.MethodGet, "/v1/patrol/tour", org, buyer, nil)
	if code != http.StatusOK {
		return // refused, which is the answer
	}
	var tours []patrolTour
	data(t, raw, &tours)
	for _, tr := range tours {
		for _, p := range tr.Points {
			if p.Site == "rivals" {
				t.Errorf("a client read another customer's round: checkpoint %q at %q on unit %q's "+
					"tour", p.Tag, p.Site, tr.Unit)
			}
		}
	}
}

// TestAnOfficerConfirmsOnlyTheirOwnRound asks who may write the proof of
// presence. ownUnit holds a position report and every incident act to the unit
// whose officer the caller is; confirm asks nothing at all, and patrol.point
// carries no actor — so a confirmation says a checkpoint was attended and
// cannot say by whom, and any officer in the org can write one for any round.
func TestAnOfficerConfirmsOnlyTheirOwnRound(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	seedUnit(t, app, org, "gf-02", "other@acme.test", UnitAvailable, 53.34, -6.26)
	tour := create(t, app, org, DTTour, map[string]any{"unit": "gf-02", "date": "2026-09-16"})
	point := create(t, app, org, DTPoint, map[string]any{
		"tour": tour["name"], "site": "depot", "tag": "REAR-DOOR", "state": PointPending,
	})

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/point/"+point["name"].(string)+"/confirm",
		org, officer, map[string]any{"method": "manual", "lat": 51.90, "lon": -8.47})
	if code == http.StatusOK {
		var p patrolCheckpoint
		data(t, raw, &p)
		t.Errorf("an officer crewing gf-01 confirmed a checkpoint on gf-02's round from 250 km away "+
			"(state %q, method %q, at %q): the record carries no actor, so it cannot even say who "+
			"claimed it", p.State, p.Method, p.At)
	}
}

// firstText renders the head of a stored body for a failure message.
func firstText(v any, n int) string {
	s, _ := v.(string)
	if len(s) > n {
		return s[:n]
	}
	return s
}
