package patrol

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// refusal_test.go is the other half of every route: what it answers when the
// document is not there, when the value is not one the model allows, and when
// the caller cannot be resolved at all. A refusal is the security surface, so it
// is measured rather than assumed — and every one of them says the same thing
// about the same condition, which is what fail exists to make true.

// TestFrameworkErrorsMapToOneStatusEach pins the mapping every route answers
// through. Absence is 404, a reference to a document that is not there is 400
// naming that, a value the model refuses is 400 carrying the model's own words,
// and anything else is 500 that says only what was being done.
func TestFrameworkErrorsMapToOneStatusEach(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	// A real refusal from the engine, so the classification is measured against
	// the value the engine actually produces.
	_, refused := framework.Ingest(context.Background(), org, DTUnit,
		map[string]any{"sign": "gf-09", "state": "banana"}, "")
	if !framework.IsValidationError(refused) {
		t.Fatalf("a state outside the seven must be a schema violation, got %v", refused)
	}

	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	for _, tc := range []struct {
		name   string
		err    error
		status int
		says   string
	}{
		{"a document that is not there", framework.ErrNotFound, http.StatusNotFound, "list keys: not found"},
		{"a reference to one that is not", framework.ErrBadRef, http.StatusBadRequest, "list keys: unknown reference"},
		{"a value the model refuses", refused, http.StatusBadRequest, refused.Error()},
		{"anything else", errors.New("disk on fire"), http.StatusInternalServerError, "list keys"},
	} {
		var got *zip.HTTPError
		if !errors.As(o.fail(tc.err, "list keys"), &got) {
			t.Fatalf("%s: fail did not answer an HTTP refusal", tc.name)
		}
		if got.Status != tc.status || got.Msg != tc.says {
			t.Errorf("%s: fail answered %d %q, want %d %q", tc.name, got.Status, got.Msg, tc.status, tc.says)
		}
	}
	// "disk on fire" is the operator's to read in the log and never the caller's.
	var leak *zip.HTTPError
	_ = errors.As(o.fail(errors.New("disk on fire"), "list keys"), &leak)
	if strings.Contains(leak.Msg, "disk on fire") {
		t.Errorf("a fault the caller cannot act on was handed to them: %q", leak.Msg)
	}
}

// TestAReferenceToADocumentThatIsNotThereIsRefused proves the mapping at a
// route: a feed row naming a site the register does not hold is 400 rather than
// a row filed against nothing.
func TestAReferenceToADocumentThatIsNotThereIsRefused(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/event", org, controller,
		map[string]any{"level": LevelRed, "title": "Intruder", "site": "no-such-site"})
	if code != http.StatusBadRequest {
		t.Fatalf("a row naming a site that is not registered want 400, got %d (%s)", code, raw)
	}
	if !strings.Contains(string(raw), "unknown reference") {
		t.Errorf("the refusal must say what was wrong with it: %s", raw)
	}
}

// TestAUnitStateOutsideTheSevenIsRefused proves the model's own vocabulary is
// what a route writes: a state the Select does not name is 400 carrying the
// engine's words, not a unit silently parked in a state no screen draws.
func TestAUnitStateOutsideTheSevenIsRefused(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-01/state", org, controller,
		map[string]any{"state": "banana"})
	if code != http.StatusBadRequest {
		t.Fatalf("a state outside the seven want 400, got %d (%s)", code, raw)
	}
	if !strings.Contains(string(raw), "not an allowed option") {
		t.Errorf("the refusal must name what the model refused: %s", raw)
	}
	// And the unit is where it was.
	code, raw = call(t, app, http.MethodGet, "/v1/patrol/unit", org, controller, nil)
	var units []patrolUnit
	data(t, raw, &units)
	if code != http.StatusOK || len(units) != 1 || units[0].State != UnitAvailable {
		t.Fatalf("the refused write moved the unit: %+v", units)
	}
}

// TestACallerWithNoSubjectIsRefused proves the role read is asked about
// somebody: a request carrying a tenant and no subject names nobody to ask IAM
// about, and a route that cannot resolve who is calling refuses.
func TestACallerWithNoSubjectIsRefused(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	if code, raw := hit(t, app, http.MethodGet, "/v1/patrol/site", nil,
		map[string]string{"X-Org-Id": org}); code != http.StatusUnauthorized {
		t.Fatalf("a request naming a tenant and no subject want 401, got %d (%s)", code, raw)
	}
}

// TestAnUnansweredRoleReadDenies proves the direction the role read fails in.
// The roles are asked of IAM on every request rather than read off a claim, so
// an IAM that does not answer is the one case where the honest reading is "we do
// not know" — and what a patrol route does with that is refuse.
func TestAnUnansweredRoleReadDenies(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/site", org, controller, nil); code != http.StatusOK {
		t.Fatalf("the estate reads while IAM answers, got %d", code)
	}

	// The peer is gone: the runtime directory holds no socket to reach it at.
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	for _, path := range []string{"/v1/patrol/site", "/v1/patrol/snapshot", "/v1/patrol/key", "/v1/patrol/note"} {
		if code, raw := call(t, app, http.MethodGet, path, org, controller, nil); code != http.StatusForbidden {
			t.Errorf("GET %s with IAM unreachable want 403, got %d (%s)", path, code, first(raw, 160))
		}
	}
}

// TestNobodyIsNamedOffTheRequest proves the one place patrol reads who is
// calling: it reads the request, so a context that carries none names nobody,
// and nothing resolves a customer from nobody.
func TestNobodyIsNamedOffTheRequest(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	ctx := context.Background()
	if got := who(ctx); got != "" {
		t.Errorf("who off the request names %q", got)
	}
	if got := actor(ctx); got != "" {
		t.Errorf("actor off the request names %q", got)
	}
	o := ops{s: &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}}
	if got := o.customer(ctx, org); got != "" {
		t.Errorf("a caller nobody names resolved the customer %q", got)
	}
	if got, err := o.roles(ctx, org); err == nil {
		t.Errorf("a caller nobody names held the roles %v", got)
	}
	if got := o.reachOf(ctx, org); got.all || got.client != "" || len(got.sites) != 0 {
		t.Errorf("a caller nobody names reached %+v", got)
	}
}

// TestEveryReadOfADocumentThatIsNotThereIs404 walks the single-document reads
// and the writes addressed by name. Each one answers 404 and none of them
// faults, so a name a caller invented tells them only that it is not there.
func TestEveryReadOfADocumentThatIsNotThereIs404(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)

	for _, tc := range []struct {
		name, method, path string
		user               string
		body               map[string]any
	}{
		{"a site", http.MethodGet, "/v1/patrol/site/ghost", controller, nil},
		{"a site to edit", http.MethodPut, "/v1/patrol/site/ghost", manager, map[string]any{"town": "Cork"}},
		{"a unit to move", http.MethodPost, "/v1/patrol/unit/ghost/fix", controller, map[string]any{"lat": 1.0, "lon": 2.0}},
		{"a unit to restate", http.MethodPost, "/v1/patrol/unit/ghost/state", controller, map[string]any{"state": UnitAvailable}},
		{"a site to measure from", http.MethodGet, "/v1/patrol/unit/near?site=ghost", controller, nil},
		{"a checkpoint", http.MethodPost, "/v1/patrol/point/ghost/confirm", officer, map[string]any{"method": "tag"}},
		{"a key set", http.MethodGet, "/v1/patrol/key/ghost", controller, nil},
		{"a key set to move", http.MethodPost, "/v1/patrol/key/ghost/move", controller, map[string]any{"act": "out"}},
		{"an incident", http.MethodGet, "/v1/patrol/incident/ghost", controller, nil},
		{"a site to open an incident at", http.MethodPost, "/v1/patrol/incident", controller, map[string]any{"site": "ghost"}},
		{"an incident to act on", http.MethodPost, "/v1/patrol/incident/ghost/step", controller, map[string]any{"act": Verify}},
		{"a customer to report on", http.MethodPost, "/v1/patrol/report", controller, map[string]any{"kind": ReportClient, "client": "ghost"}},
		{"an incident to report on", http.MethodPost, "/v1/patrol/report", controller, map[string]any{"kind": ReportIncident, "incident": "ghost"}},
		{"a report to download", http.MethodGet, "/v1/patrol/report/ghost", controller, nil},
		{"a camera to open", http.MethodGet, "/v1/patrol/camera/ghost/stream", controller, nil},
	} {
		if code, raw := call(t, app, tc.method, tc.path, org, tc.user, tc.body); code != http.StatusNotFound {
			t.Errorf("%s that is not there: %s %s want 404, got %d (%s)",
				tc.name, tc.method, tc.path, code, first(raw, 160))
		}
	}
}

// TestACheckpointWhoseRoundIsGoneIsNotThere proves the confirmation resolves the
// round before it admits anybody: whose round it is is what decides who may
// confirm it, so a checkpoint whose tour has been removed is 404 rather than one
// anybody may complete.
func TestACheckpointWhoseRoundIsGoneIsNotThere(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	tour := create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": "2026-01-02"})
	point := create(t, app, org, DTPoint, map[string]any{
		"tour": tour["name"].(string), "site": "depot", "tag": "T1",
		"open": "2026-01-02 20:00:00", "close": "2099-01-02 22:00:00",
	})
	if err := framework.Delete(context.Background(), org, DTTour, tour["name"].(string)); err != nil {
		t.Fatalf("remove the round: %v", err)
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/point/"+point["name"].(string)+"/confirm",
		org, officer, map[string]any{"method": "tag"}); code != http.StatusNotFound {
		t.Fatalf("a checkpoint whose round is gone want 404, got %d (%s)", code, raw)
	}
}

// TestAnInstalledTenantWithNoConfigurationIs404 proves the public face is a
// document and not a default: an org that installed patrol and wrote no tenant
// document has no face to draw, and the route says so rather than answering an
// empty one.
func TestAnInstalledTenantWithNoConfigurationIs404(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	if code, raw := hit(t, app, http.MethodGet, "/v1/patrol/tenant/"+org, nil, nil); code != http.StatusNotFound {
		t.Fatalf("an installed org with no tenant document want 404, got %d (%s)", code, raw)
	}
}

// TestAnActivationLargerThanTheIntakeTakesIsRefused proves the intake's own
// bound: the panel's payload is kept verbatim up to 16 KiB, and one over that is
// refused whole rather than stored truncated.
func TestAnActivationLargerThanTheIntakeTakesIsRefused(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, controller, map[string]any{
		"ref": "ARC-BIG", "account": "1001", "trigger": "Intruder",
		"raw": map[string]any{"line": strings.Repeat("9", maxRaw+1)},
	})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an activation over %d bytes want 413, got %d (%s)", maxRaw, code, first(raw, 160))
	}
}

// TestAnIntakeWithNoModelInstalledIsUnavailable proves the one thing the intake
// cannot work around. An activation is never dropped, but an org whose store
// holds no patrol model has nowhere to drop it, so the centre is told to send it
// again rather than told it was taken.
func TestAnIntakeWithNoModelInstalledIsUnavailable(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/alarm", "unfitted", controller,
		map[string]any{"ref": "ARC-1", "account": "1001", "trigger": "Intruder"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("an activation into an org with no patrol model want 503, got %d (%s)", code, first(raw, 160))
	}
}
