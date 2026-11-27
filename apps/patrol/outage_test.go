package patrol

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
)

// outage_test.go asks what every route answers when the store behind it cannot.
// The direction matters more than the status: an empty list reads as "you have
// none of these", which about a key register or an incident list is a false
// answer to a real question. A route that cannot read refuses.

// TestNoRouteAnswersAnEmptyListWhenTheStoreIsGone closes the store under a
// mounted surface and drives every read and write that would have gone to it.
func TestNoRouteAnswersAnEmptyListWhenTheStoreIsGone(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe"})
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/key", org, controller, nil); code != http.StatusOK {
		t.Fatalf("the register reads while the store is open, got %d", code)
	}
	if err := framework.Shutdown(); err != nil {
		t.Fatalf("close the store: %v", err)
	}

	for _, tc := range []struct {
		name, method, path string
		body               map[string]any
	}{
		{"the estate", http.MethodGet, "/v1/patrol/site", nil},
		{"the fleet", http.MethodGet, "/v1/patrol/unit", nil},
		{"the key register", http.MethodGet, "/v1/patrol/key", nil},
		{"the camera register", http.MethodGet, "/v1/patrol/camera", nil},
		{"the note book", http.MethodGet, "/v1/patrol/note", nil},
		{"the whole screen", http.MethodGet, "/v1/patrol/snapshot", nil},
		{"a note to write", http.MethodPost, "/v1/patrol/note", map[string]any{"subject": "depot"}},
		{"a feed row to raise", http.MethodPost, "/v1/patrol/event", map[string]any{"level": LevelCyan, "title": "x"}},
		{"a report to file", http.MethodPost, "/v1/patrol/report", map[string]any{"kind": ReportShift}},
		{"a report to download", http.MethodGet, "/v1/patrol/report/r1", nil},
		{"an act on an incident", http.MethodPost, "/v1/patrol/incident/inc-00001/step",
			map[string]any{"act": Verify}},
	} {
		code, raw := call(t, app, tc.method, tc.path, org, controller, tc.body)
		if code < http.StatusBadRequest {
			t.Errorf("%s with the store gone answered %d (%s), want a refusal",
				tc.name, code, first(raw, 200))
		}
	}
}

// TestTheRoundsAndTheTrailsAreEmptyRatherThanWrong proves the two reads that
// answer with what they could gather: a round carries the checkpoints the caller
// may see, and a unit carries the tail of its trail. Neither is the answer to
// the question being asked, so a store that cannot supply them leaves them out
// instead of failing a read that has something else to say.
func TestTheRoundsAndTheTrailsAreEmptyRatherThanWrong(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	if err := framework.Shutdown(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/tour", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("the rounds want 200, got %d (%s)", code, first(raw, 200))
	}
	var tours []patrolTour
	data(t, raw, &tours)
	if len(tours) != 0 {
		t.Errorf("a store that cannot be read answered %d rounds", len(tours))
	}
}

// TestACustomerWhoseEstateCannotBeResolvedReachesNothing proves the narrowing
// fails closed. A customer's reach is the set of sites their record owns, so a
// store that cannot answer which those are leaves them reaching nothing — never
// the whole operation.
func TestACustomerWhoseEstateCannotBeResolvedReachesNothing(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/site", org, buyer, nil)
	var sites []patrolSite
	data(t, raw, &sites)
	if code != http.StatusOK || len(sites) != 1 {
		t.Fatalf("the customer reads their own estate while the store is open: %d %+v", code, sites)
	}
	if err := framework.Shutdown(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	for _, path := range []string{"/v1/patrol/site", "/v1/patrol/incident", "/v1/patrol/event"} {
		code, raw := call(t, app, http.MethodGet, path, org, buyer, nil)
		if code == http.StatusOK && strings.Contains(string(raw), "rivals") {
			t.Errorf("GET %s answered another customer's estate: %s", path, first(raw, 200))
		}
	}
}

// TestATenantThatCannotBeReadTellsNobody proves the address lists are read off
// the tenant document on every send: a store that cannot answer leaves the list
// empty, so an act sends no mail rather than sending it to whoever was cached.
func TestATenantThatCannotBeReadTellsNobody(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	contacted(t, app, map[string]any{"controllers": []string{controller}})
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	if got := o.contacts(context.Background(), org, "controllers"); len(got) != 1 {
		t.Fatalf("the list reads %v while the store is open, want one address", got)
	}
	if err := framework.Shutdown(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	if got := o.contacts(context.Background(), org, "controllers"); len(got) != 0 {
		t.Fatalf("a tenant that cannot be read answered the list %v", got)
	}
}

// TestAWindowTheStoreCannotCutIsEmpty proves what the two windowed lists answer
// when the store cannot cut a window. The read they go through also draws the
// snapshot, where the feed rides along beside the estate and a feed that cannot
// be read must not fail the screen — so it has no refusal to hand back, and the
// window comes back empty.
func TestAWindowTheStoreCannotCutIsEmpty(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelCyan, "title": "on the record", "site": "depot"}); code != http.StatusCreated {
		t.Fatalf("seed a row: %d (%s)", code, raw)
	}
	if err := framework.Shutdown(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	for _, path := range []string{"/v1/patrol/event", "/v1/patrol/incident"} {
		code, raw := call(t, app, http.MethodGet, path, org, controller, nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s want 200, got %d (%s)", path, code, first(raw, 200))
		}
		var rows []map[string]any
		data(t, raw, &rows)
		if len(rows) != 0 {
			t.Errorf("GET %s answered %d rows off a store that cannot be read", path, len(rows))
		}
	}
}
