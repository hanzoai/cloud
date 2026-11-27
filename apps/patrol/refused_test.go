package patrol

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
)

// refused_test.go proves what a route does when a write it depends on is turned
// down. The engine's lifecycle handlers are how a write is refused — a lane
// attaches one and a non-nil answer aborts the operation before any state
// changes — so this binary attaches one per DocType an act writes and a test
// arms the ones it is about.
//
// What is under test is the promise each route makes about the OTHER writes it
// had already made: an incident whose trail could not be written is not a
// record, an act whose step could not be written was not performed, and a report
// that could not be filed does not reopen an incident that has closed.

// turned is the set of DocTypes whose writes a running test has arranged to be
// turned down.
var turned struct {
	sync.Mutex
	down map[hooked]bool
}

// hooked is one DocType and the phase a write reaches it in.
type hooked struct {
	id     framework.ID
	action string
}

func init() {
	for _, id := range []framework.ID{DTIncident, DTStep, DTFix, DTUnit, DTMovement, DTAlarm, DTReport} {
		for _, action := range []string{framework.ActionBeforeInsert, framework.ActionBeforeSave, framework.ActionOnTrash} {
			framework.RegisterHook(id, action, turnDown(hooked{id: id, action: action}))
		}
	}
}

// turnDown answers whether this write is one a running test has arranged to
// refuse.
func turnDown(k hooked) framework.Hook {
	return func(context.Context, *framework.Event) error {
		turned.Lock()
		defer turned.Unlock()
		if turned.down[k] {
			return errors.New("this write was turned down")
		}
		return nil
	}
}

// refuse arranges for one kind of write to be turned down for the rest of this
// test.
func refuse(t *testing.T, id framework.ID, action string) {
	t.Helper()
	k := hooked{id: id, action: action}
	turned.Lock()
	if turned.down == nil {
		turned.down = map[hooked]bool{}
	}
	turned.down[k] = true
	turned.Unlock()
	t.Cleanup(func() {
		turned.Lock()
		defer turned.Unlock()
		delete(turned.down, k)
	})
}

// TestAnIncidentWhoseTrailCannotBeWrittenIsNotOpened proves the pair that lands
// together. An incident whose trail does not say it was opened is not a record
// of a response, so the incident is taken back with the step that failed.
func TestAnIncidentWhoseTrailCannotBeWrittenIsNotOpened(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	refuse(t, DTStep, framework.ActionBeforeInsert)

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "depot", "trigger": "Intruder"})
	if code < http.StatusBadRequest {
		t.Fatalf("an incident whose trail cannot be written want a refusal, got %d (%s)", code, first(raw, 200))
	}
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/incident", org, controller, nil)
	var incs []patrolIncident
	data(t, raw, &incs)
	if code != http.StatusOK || len(incs) != 0 {
		t.Fatalf("%d incidents were left with no trail behind them: %+v", len(incs), incs)
	}
}

// TestAnIncidentThatCannotBeTakenBackIsLeftWhereItIs proves the floor beneath
// that pair: when the step fails AND the incident cannot be removed, the route
// still refuses, and what is left behind is a record for somebody to find
// rather than a response the caller was told had opened.
func TestAnIncidentThatCannotBeTakenBackIsLeftWhereItIs(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	refuse(t, DTStep, framework.ActionBeforeInsert)
	refuse(t, DTIncident, framework.ActionOnTrash)

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "depot", "trigger": "Intruder"})
	if code < http.StatusBadRequest {
		t.Fatalf("an incident whose trail cannot be written want a refusal, got %d (%s)", code, first(raw, 200))
	}
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/incident", org, controller, nil)
	var incs []patrolIncident
	data(t, raw, &incs)
	if code != http.StatusOK || len(incs) != 1 {
		t.Fatalf("the incident that could not be taken back is not on record: %d %+v", len(incs), incs)
	}
}

// TestAnIncidentThatCannotBeRecordedOpensNothing proves the first write of the
// pair: with no incident there is nothing to leave a trail about.
func TestAnIncidentThatCannotBeRecordedOpensNothing(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	refuse(t, DTIncident, framework.ActionBeforeInsert)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "depot", "trigger": "Intruder"}); code < http.StatusBadRequest {
		t.Fatalf("an incident that cannot be written want a refusal, got %d (%s)", code, first(raw, 200))
	}
}

// TestAnActWhoseStepCannotBeWrittenIsNotPerformed proves the same pair on a
// transition: the trail is the whole audit, so a caller whose step could not be
// written performed no act and the incident has not moved.
func TestAnActWhoseStepCannotBeWrittenIsNotPerformed(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	refuse(t, DTStep, framework.ActionBeforeInsert)

	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code < http.StatusBadRequest {
		t.Fatalf("an act whose step cannot be written want a refusal, got %d", code)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
	var inc patrolIncident
	data(t, raw, &inc)
	if code != http.StatusOK || inc.State != Received {
		t.Fatalf("the incident moved to %s on an act that was never recorded", inc.State)
	}
	if inc.Verified != "" {
		t.Errorf("the act stamped %q on an incident it never landed on", inc.Verified)
	}
}

// TestAPositionThatCannotBeRecordedDoesNotMoveTheUnit proves the order the two
// writes of a position report are made in: the trail is the proof of where a
// unit was, so a fix that could not be written leaves the unit where it was.
func TestAPositionThatCannotBeRecordedDoesNotMoveTheUnit(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	refuse(t, DTFix, framework.ActionBeforeInsert)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/fix", org, officer,
		map[string]any{"lat": 52.50, "lon": -9.00}); code < http.StatusBadRequest {
		t.Fatalf("a position that cannot be recorded want a refusal, got %d (%s)", code, first(raw, 200))
	}
	u, err := framework.Get(context.Background(), org, DTUnit, "gf-01")
	if err != nil || floatField(u, "lat") != 51.90 {
		t.Fatalf("the unit moved to %v on a position that was never recorded", floatField(u, "lat"))
	}
}

// TestAUnitThatCannotBeMovedRefusesThePosition proves the second write of that
// pair is not optional either: a report the caller was told landed must have
// moved the unit the map draws.
func TestAUnitThatCannotBeMovedRefusesThePosition(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	refuse(t, DTUnit, framework.ActionBeforeSave)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/fix", org, officer,
		map[string]any{"lat": 52.50, "lon": -9.00}); code < http.StatusBadRequest {
		t.Fatalf("a position that cannot move the unit want a refusal, got %d (%s)", code, first(raw, 200))
	}
}

// TestADispatchLandsEvenWhenTheFleetCannotBeRestated proves the other side of
// the line on the same register. Moving the unit to assigned is something the
// act sets in motion, not something the controller asked for, so a fleet that
// cannot be written costs that state and never the dispatch.
func TestADispatchLandsEvenWhenTheFleetCannotBeRestated(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatalf("verify want 200, got %d", code)
	}
	refuse(t, DTUnit, framework.ActionBeforeSave)

	code, inc := stepped(t, app, name, controller, map[string]any{"act": Dispatch, "unit": "gf-01"})
	if code != http.StatusOK || inc.State != Dispatched {
		t.Fatalf("a dispatch whose fleet write fails want 200 and %s, got %d %s", Dispatched, code, inc.State)
	}
	u, err := framework.Get(context.Background(), org, DTUnit, "gf-01")
	if err != nil || strField(u, "state") != UnitAvailable {
		t.Errorf("the fleet moved after all: %q", strField(u, "state"))
	}
}

// TestAKeyMovementThatCannotBeRecordedDoesNotMoveTheRegister proves the chain of
// custody is written before the register that summarises it: the register says
// where a set is now and the movements say how it got there, so a register that
// moved with no movement behind it is a custody record with a hole in it.
func TestAKeyMovementThatCannotBeRecordedDoesNotMoveTheRegister(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe"})
	refuse(t, DTMovement, framework.ActionBeforeInsert)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/key/K-1/move", org, controller,
		map[string]any{"act": "out", "holder": officer}); code < http.StatusBadRequest {
		t.Fatalf("a movement that cannot be recorded want a refusal, got %d (%s)", code, first(raw, 200))
	}
	k, err := framework.Get(context.Background(), org, DTKey, "K-1")
	if err != nil || strField(k, "holder") != "" {
		t.Fatalf("the register handed the set to %q with no movement behind it", strField(k, "holder"))
	}
}

// TestAnIncidentClosesEvenWhenItsReportCannotBeFiled proves the last act's side
// effect is best effort: the incident has already closed, and a report that
// could not be written must not reopen it.
func TestAnIncidentClosesEvenWhenItsReportCannotBeFiled(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	for _, act := range []struct{ act, user string }{
		{Verify, controller}, {Dispatch, controller}, {Roll, officer}, {Arrive, officer},
		{Check, officer}, {Secure, officer},
	} {
		body := map[string]any{"act": act.act}
		if act.act == Dispatch {
			body["unit"] = "gf-01"
		}
		if code, _ := stepped(t, app, name, act.user, body); code != http.StatusOK {
			t.Fatalf("%s want 200, got %d", act.act, code)
		}
	}
	refuse(t, DTReport, framework.ActionBeforeInsert)

	code, inc := stepped(t, app, name, supervisor, map[string]any{"act": Close, "outcome": "Nothing found"})
	if code != http.StatusOK || inc.State != Closed {
		t.Fatalf("a close whose report cannot be filed want 200 and %s, got %d %s", Closed, code, inc.State)
	}
}

// TestAnActivationThatCannotBeRecordedIsRefused proves the intake's own floor.
// An activation is never dropped, so one that could not be written is answered
// as a refusal — the centre sends it again — and never as a 202 that says it is
// on record.
func TestAnActivationThatCannotBeRecordedIsRefused(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	refuse(t, DTAlarm, framework.ActionBeforeInsert)

	for _, tc := range []struct {
		name    string
		account string
	}{
		{"one no site claims", "9999"},
		{"one a site claims", "1001"},
	} {
		code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller,
			map[string]any{"ref": "ARC-" + tc.account, "account": tc.account, "trigger": "Intruder"})
		if code < http.StatusBadRequest {
			t.Errorf("%s that cannot be recorded want a refusal, got %d (%s)", tc.name, code, first(raw, 200))
		}
	}
}

// TestAnActivationWhoseIncidentCannotOpenIsRefused proves the intake answers for
// the whole of what it promised: a matched activation opens an incident, so one
// that could not open it has not been taken.
func TestAnActivationWhoseIncidentCannotOpenIsRefused(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	refuse(t, DTIncident, framework.ActionBeforeInsert)

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller,
		map[string]any{"ref": "ARC-1", "account": "1001", "trigger": "Intruder"})
	if code < http.StatusBadRequest {
		t.Fatalf("an activation whose incident cannot open want a refusal, got %d (%s)", code, first(raw, 200))
	}
}
