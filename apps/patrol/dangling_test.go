package patrol

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/zap-proto/zip"
)

// dangling_test.go asks what happens to a row that names a document somebody
// removed. The engine replaces a document's data on update, so every write sends
// the whole of it — and a reference that no longer resolves is refused. What
// matters is which side of the line each route falls on: a write a caller asked
// for is refused and says so, and a write an act set in motion is lost without
// unwinding the act.

// orphaned seeds an estate and then removes the customer record its site names,
// which is the one reference every register row here carries down to.
func orphaned(t *testing.T) *zip.App {
	t.Helper()
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	seedSite(t, app, org, "depot", "1001", "northside")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	if err := framework.Delete(context.Background(), org, DTClient, "northside"); err != nil {
		t.Fatalf("remove the customer record: %v", err)
	}
	return app
}

// TestAWriteOntoARowThatNamesNothingIsRefused proves the routes a caller drives:
// each one answers what was wrong with the write rather than reporting it done.
func TestAWriteOntoARowThatNamesNothingIsRefused(t *testing.T) {
	app := orphaned(t)
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe"})
	tour := create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": "2026-01-02"})
	point := create(t, app, org, DTPoint, map[string]any{
		"tour": tour["name"].(string), "site": "depot", "tag": "T1",
		"open": "2026-01-02 20:00:00", "close": "2099-01-02 22:00:00",
	})
	// The key and the checkpoint both name the site, and the site names the
	// customer that is gone, so the site is what cannot be written back.
	if err := framework.Delete(context.Background(), org, DTSite, "depot"); err != nil {
		t.Fatalf("remove the site: %v", err)
	}

	for _, tc := range []struct {
		name, method, path string
		user               string
		body               map[string]any
	}{
		{"a site edit", http.MethodPut, "/v1/patrol/site/depot", manager, map[string]any{"town": "Cork"}},
		{"a key movement", http.MethodPost, "/v1/patrol/key/K-1/move", controller,
			map[string]any{"act": "out", "holder": officer}},
		{"a checkpoint confirmation", http.MethodPost,
			"/v1/patrol/point/" + point["name"].(string) + "/confirm", officer, map[string]any{"method": "tag"}},
	} {
		code, raw := call(t, app, tc.method, tc.path, org, tc.user, tc.body)
		if tc.name == "a site edit" {
			// The site itself is gone, so this one is an absence and not a bad
			// reference — the route says so before it writes anything.
			if code != http.StatusNotFound {
				t.Errorf("%s onto a site that is gone want 404, got %d (%s)", tc.name, code, first(raw, 160))
			}
			continue
		}
		if code != http.StatusBadRequest {
			t.Errorf("%s naming a document that is gone want 400, got %d (%s)", tc.name, code, first(raw, 160))
		}
		if !strings.Contains(string(raw), "unknown reference") {
			t.Errorf("%s: the refusal must say what was wrong with it: %s", tc.name, first(raw, 160))
		}
	}
}

// TestASiteEditOntoAMissingCustomerIsRefused proves the same rule on the site
// itself: the register row carries the customer it belongs to, so a customer
// record that is gone is a site nobody can edit until the register is corrected.
func TestASiteEditOntoAMissingCustomerIsRefused(t *testing.T) {
	app := orphaned(t)
	code, raw := call(t, app, http.MethodPut, "/v1/patrol/site/depot", org, manager,
		map[string]any{"town": "Cork"})
	if code != http.StatusBadRequest {
		t.Fatalf("editing a site whose customer is gone want 400, got %d (%s)", code, first(raw, 160))
	}
	if !strings.Contains(string(raw), "unknown reference") {
		t.Errorf("the refusal must say what was wrong with it: %s", first(raw, 160))
	}
}

// TestAnActivationStillLandsWhenTheStatusWriteCannot proves the other side of
// the line. Putting the site into alarm is something the intake sets in motion,
// not something the centre asked for, so a site that cannot be written back
// costs that status and never the activation or the incident it opened.
func TestAnActivationStillLandsWhenTheStatusWriteCannot(t *testing.T) {
	app := orphaned(t)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller,
		map[string]any{"ref": "ARC-1", "account": "1001", "zone": "Z3", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("a matched activation want 201, got %d (%s)", code, first(raw, 200))
	}
	var got patrolActivation
	data(t, raw, &got)
	if got.Site != "depot" || got.Incident == nil || got.Incident.State != Received {
		t.Fatalf("the activation must open its incident: %+v", got)
	}
	st, err := framework.Get(context.Background(), org, DTSite, "depot")
	if err != nil {
		t.Fatalf("read the site back: %v", err)
	}
	if strField(st, "status") != SiteOK {
		t.Errorf("the status write landed after all: %q", strField(st, "status"))
	}
}

// TestAnOfficerMovesOnlyTheirOwnUnitsState proves the state route is held to the
// same rule as the position report: the control room moves any unit, an officer
// moves the one that names them.
func TestAnOfficerMovesOnlyTheirOwnUnitsState(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	seedUnit(t, app, org, "gf-02", "other@acme.test", UnitAvailable, 52.00, -8.00)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-02/state", org, officer,
		map[string]any{"state": "break"}); code != http.StatusForbidden {
		t.Fatalf("an officer restating another unit want 403, got %d (%s)", code, first(raw, 160))
	}
	u, err := framework.Get(context.Background(), org, DTUnit, "gf-02")
	if err != nil || strField(u, "state") != UnitAvailable {
		t.Errorf("the refused write moved the unit to %q", strField(u, "state"))
	}
}
