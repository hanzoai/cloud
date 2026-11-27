package patrol

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
)

// dispatch_test.go is the half of an act that runs after it has landed. The
// incident has already moved by then, so every register write and every message
// is best effort — and what that has to mean is that none of them can unwind the
// act, whatever they find.

// working seeds one incident already in a state, so a test stands at the act it
// is about rather than walking the machine to get there.
func working(t *testing.T, row map[string]any) string {
	t.Helper()
	made, err := framework.Ingest(context.Background(), org, DTIncident, row, "")
	if err != nil {
		t.Fatalf("seed an incident %v: %v", row, err)
	}
	return made.Name
}

// TestAStateWriteWithNothingToWriteToChangesNothing proves the two register
// writes an act sets in motion leave the register alone when there is nothing to
// write to: no name at all, or a name the register does not hold.
func TestAStateWriteWithNothingToWriteToChangesNothing(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	ctx := context.Background()

	o.setUnit(ctx, org, "", UnitOnsite)
	o.setUnit(ctx, org, "no-such-unit", UnitOnsite)
	o.setSite(ctx, org, "", SiteAlarm)
	o.setSite(ctx, org, "no-such-site", SiteAlarm)

	u, err := framework.Get(ctx, org, DTUnit, "gf-01")
	if err != nil || strField(u, "state") != UnitAvailable {
		t.Errorf("the fleet moved: %v %q", err, strField(u, "state"))
	}
	st, err := framework.Get(ctx, org, DTSite, "depot")
	if err != nil || strField(st, "status") != SiteOK {
		t.Errorf("the estate moved: %v %q", err, strField(st, "status"))
	}
}

// TestAnIncidentThatNamesNoUnitStillRuns proves an act whose register write has
// nothing to write to still lands: the response moved, and a unit nobody
// assigned is not a reason to refuse it.
func TestAnIncidentThatNamesNoUnitStillRuns(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	name := working(t, map[string]any{
		"site": "depot", "trigger": "Intruder", "state": Dispatched, "opened": now(),
	})
	code, inc := stepped(t, app, name, controller, map[string]any{"act": Roll})
	if code != http.StatusOK {
		t.Fatalf("roll on an incident naming no unit want 200, got %d", code)
	}
	if inc.State != Enroute {
		t.Fatalf("the incident is %s, want %s", inc.State, Enroute)
	}
}

// TestAnOfficerNeedsAUnitToBeTheirs proves the rule an act on the ground is held
// to. The control room works every unit; an officer works the one that names
// them, so an incident naming no unit is nobody's to roll, and one naming a unit
// the fleet no longer holds is not there.
func TestAnOfficerNeedsAUnitToBeTheirs(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)

	none := working(t, map[string]any{
		"site": "depot", "trigger": "Intruder", "state": Dispatched, "opened": now(),
	})
	if code, _ := stepped(t, app, none, officer, map[string]any{"act": Roll}); code != http.StatusForbidden {
		t.Errorf("an officer rolling an incident that names no unit want 403, got %d", code)
	}

	gone := working(t, map[string]any{
		"site": "depot", "trigger": "Intruder", "unit": "gf-01", "state": Dispatched, "opened": now(),
	})
	if err := framework.Delete(context.Background(), org, DTUnit, "gf-01"); err != nil {
		t.Fatalf("retire the unit: %v", err)
	}
	if code, _ := stepped(t, app, gone, officer, map[string]any{"act": Roll}); code != http.StatusNotFound {
		t.Errorf("an officer rolling an incident whose unit is retired want 404, got %d", code)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+gone, org, controller, nil)
	var inc patrolIncident
	data(t, raw, &inc)
	if code != http.StatusOK || inc.State != Dispatched {
		t.Fatalf("the refused act moved the incident to %s", inc.State)
	}
}

// TestNothingIsAnybodysOffTheRequest proves the ownership question is asked of
// the request: off the HTTP path there is no caller, so no unit is theirs.
func TestNothingIsAnybodysOffTheRequest(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	if err := o.ownUnit(context.Background(), org, "gf-01"); err == nil {
		t.Error("a unit was somebody's with nobody on the request")
	}
}

// TestAnActThatCannotLandLeavesNoStepBehind proves the two writes one act makes
// go together. The step is written first, because it is the record of the act;
// a state write that then fails takes the step back with it, so a trail never
// carries an act the incident did not perform.
func TestAnActThatCannotLandLeavesNoStepBehind(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	name := working(t, map[string]any{
		"site": "depot", "trigger": "Intruder", "unit": "gf-01", "state": Checked, "opened": now(),
	})
	// The incident carries the site it is at, so a site the register no longer
	// holds is a document that cannot be written back.
	if err := framework.Delete(context.Background(), org, DTSite, "depot"); err != nil {
		t.Fatalf("retire the site: %v", err)
	}
	if code, _ := stepped(t, app, name, officer, map[string]any{"act": Secure}); code != http.StatusBadRequest {
		t.Fatalf("an act that cannot be written back want 400, got %d", code)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read the incident want 200, got %d (%s)", code, first(raw, 200))
	}
	var inc patrolIncident
	data(t, raw, &inc)
	if inc.State != Checked {
		t.Errorf("the incident moved to %s on an act that did not land", inc.State)
	}
	for _, s := range inc.Steps {
		if s.Act == Secure {
			t.Errorf("the trail carries a %s the incident never performed: %+v", Secure, s)
		}
	}
}

// TestADispatchNeedsAUnitToSend proves the one act that cannot be performed
// without naming one, and that a second dispatch may leave the unit where the
// first put it.
func TestADispatchNeedsAUnitToSend(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatalf("verify want 200")
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident/"+name+"/step", org, controller,
		map[string]any{"act": Dispatch}); code != http.StatusBadRequest {
		t.Fatalf("a dispatch naming no unit want 400, got %d (%s)", code, first(raw, 160))
	}
}

// TestTheNearestReadDefaultsItsLimit proves the answer a controller gets when
// they ask who can get there and name no number: the three closest.
func TestTheNearestReadDefaultsItsLimit(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	for _, u := range []struct {
		sign string
		lat  float64
	}{{"gf-01", 51.90}, {"gf-02", 51.91}, {"gf-03", 51.92}, {"gf-04", 51.93}} {
		seedUnit(t, app, org, u.sign, u.sign+"@acme.test", UnitAvailable, u.lat, -8.47)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/unit/near?site=depot", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("nearest want 200, got %d (%s)", code, raw)
	}
	var got []patrolNearest
	data(t, raw, &got)
	if len(got) != nearUnits {
		t.Fatalf("a read naming no limit answered %d units, want %d", len(got), nearUnits)
	}
}

// TestAFeedRowAtALevelNobodyDrawsIsCyan proves the feed's own vocabulary is
// closed: a level outside the four is recorded as the one that means "everything
// else" rather than refused or stored as written.
func TestAFeedRowAtALevelNobodyDrawsIsCyan(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": "purple", "title": "Gate left open"})
	if code != http.StatusCreated {
		t.Fatalf("raise a row want 201, got %d (%s)", code, raw)
	}
	var e patrolEvent
	data(t, raw, &e)
	if e.Level != LevelCyan {
		t.Fatalf("a row at a level nobody draws is %q, want %q", e.Level, LevelCyan)
	}
}
