package patrol

import (
	"net/http"
	"strings"
	"testing"
)

// perms_test.go holds the generic surface to the rules /v1/patrol enforces. Both
// serve the same store, and a right on a DocType is a right on every document of
// it, so a rule that lives in a route rather than in the model is a rule with a
// second way around it: an officer rewriting who crews a unit, a controller
// deleting an audit step, a filed report composed a second time.

// TestSnapshotHandsTheOfficerNoKeys holds /snapshot to the admission /key
// enforces. The key register says where a client's keys are kept and who has
// them; GET /key refuses the officer and the snapshot must refuse the same rows,
// or the refusal is a route rather than a rule.
func TestSnapshotHandsTheOfficerNoKeys(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTKey, map[string]any{
		"ref": "K-1", "site": "depot", "where": "cabinet 3, control room", "holder": "duty",
	})

	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/key", org, officer, nil); code != http.StatusForbidden {
		t.Fatalf("an officer reading the key register want 403, got %d", code)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/snapshot", org, officer, nil)
	if code != http.StatusOK {
		t.Fatalf("an officer's snapshot want 200, got %d (%s)", code, raw)
	}
	var got patrolEstate
	data(t, raw, &got)
	if len(got.Keys) != 0 {
		t.Fatalf("an officer refused GET /key read %d key sets off the snapshot: %+v", len(got.Keys), got.Keys)
	}
}

// TestOfficerCannotAdoptAnotherUnit proves the ownership check is a rule and not
// a route. POST /unit/{sign}/fix refuses an officer another unit, and the check
// is patrol.unit.officer — a field the same officer may write at
// /v1/framework/patrol.unit. If they can rewrite it they crew any unit: another
// guard's position trail, and every incident act ownUnit guards.
func TestOfficerCannotAdoptAnotherUnit(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	seedUnit(t, app, org, "gf-02", rival, UnitAvailable, 52.00, -8.00)

	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-02/fix", org, officer,
		map[string]any{"lat": 51.95, "lon": -8.50}); code != http.StatusForbidden {
		t.Fatalf("an officer reporting another unit want 403, got %d", code)
	}
	code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTUnit.String()+"/gf-02", org, officer,
		map[string]any{"sign": "gf-02", "officer": officer, "state": UnitAvailable, "lat": 52.00, "lon": -8.00})
	if code == http.StatusOK {
		again, body := call(t, app, http.MethodPost, "/v1/patrol/unit/gf-02/fix", org, officer,
			map[string]any{"lat": 51.95, "lon": -8.50})
		t.Fatalf("an officer rewrote who crews gf-02 through /v1/framework (200) and the route that "+
			"refused them then answered %d (%s)", again, body)
	}
	if code != http.StatusForbidden {
		t.Fatalf("an officer rewriting another unit's crew want 403, got %d (%s)", code, raw)
	}
}

// TestStepTrailIsAppendOnly holds patrol.step to what it is called. It is the
// whole audit — who moved an incident, from what, to what, when — and an officer
// holds Write on it through the generic surface, where the trail is a document
// like any other.
func TestStepTrailIsAppendOnly(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatalf("VERIFY want 200, got %d", code)
	}

	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTStep.String(), org, boss, nil)
	if code != http.StatusOK {
		t.Fatalf("read the trail want 200, got %d (%s)", code, raw)
	}
	var trail []struct {
		Name string `json:"name"`
		Act  string `json:"act"`
	}
	data(t, raw, &trail)
	var verify string
	for _, s := range trail {
		if s.Act == Verify {
			verify = s.Name
		}
	}
	if verify == "" {
		t.Fatalf("VERIFY left no step behind: %s", raw)
	}

	if code, body := call(t, app, http.MethodPut, "/v1/framework/"+DTStep.String()+"/"+verify, org, officer,
		map[string]any{"incident": name, "act": Verify, "from": Received, "to": Verified,
			"actor": "somebody else", "detail": "rewritten", "at": "1999-01-01 00:00:00"}); code == http.StatusOK {
		t.Errorf("an officer rewrote an audit step (200): %s", body)
	}
	if code, body := call(t, app, http.MethodDelete, "/v1/framework/"+DTStep.String()+"/"+verify, org, controller, nil); code < 400 {
		t.Errorf("a controller deleted an audit step (%d): %s", code, body)
	}
}

// TestIncidentStateMovesOnlyThroughTheMachine proves the transition table is the
// only way an incident's state changes. patrol.incident is a document, an
// officer holds Write on it, and a PUT that lands `closed` on a `received`
// incident skips every act, every role, every stamp and the whole trail.
func TestIncidentStateMovesOnlyThroughTheMachine(t *testing.T) {
	app := mount(t)
	name := opened(t, app)

	// The machine refuses this caller and this jump outright.
	if code, _ := stepped(t, app, name, officer, map[string]any{"act": Close}); code == http.StatusOK {
		t.Fatalf("an officer CLOSEd a received incident through the machine")
	}
	code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTIncident.String()+"/"+name, org, officer,
		map[string]any{"site": "depot", "state": Closed, "outcome": "nothing found", "false": 1,
			"closed": now(), "secured": now(), "filed": now()})
	if code == http.StatusOK {
		var got patrolIncident
		read, body := call(t, app, http.MethodGet, "/v1/patrol/incident/"+name, org, controller, nil)
		if read == http.StatusOK {
			data(t, body, &got)
		}
		t.Fatalf("an officer wrote state=%s straight onto the incident through /v1/framework, "+
			"leaving %d steps on the trail", got.State, len(got.Steps))
	}
	if code != http.StatusForbidden {
		t.Fatalf("an officer writing an incident's state want 403, got %d (%s)", code, raw)
	}
}

// TestCameraSecretNeverReachesAReader holds the camera credential's path to the
// claim made for it. There is no field-level permission, so every role that
// reads patrol.camera at all reads `secret` — and the customer is one of them.
func TestCameraSecretNeverReachesAReader(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{
		"name": "cam-1", "site": "depot", "label": "Yard", "online": true,
		"secret": "acme/patrol/camera/cam-1",
	})

	for _, who := range []string{buyer, officer} {
		code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTCamera.String()+"/cam-1", org, who, nil)
		if code == http.StatusOK && strings.Contains(string(raw), "acme/patrol/camera/cam-1") {
			t.Errorf("%s read the camera's KMS path off the document: %s", who, raw)
		}
	}
}

// TestSitePanelAccountIsNotTheOfficersToMove holds the one field an activation
// is matched on. patrol.site.account routes every alarm the receiving centre
// sends; an officer holds Write on patrol.site through the generic surface, so
// they can point another site's activations at a site of their choosing — or at
// nothing. The same PUT replaces the document, so every field the body leaves
// out is gone: coordinates, zones, hazards, the keyholder.
func TestSitePanelAccountIsNotTheOfficersToMove(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTSite.String()+"/depot", org, officer,
		map[string]any{"name": "depot", "account": "6666", "status": SiteOK})
	if code != http.StatusForbidden {
		t.Errorf("an officer moving a site's panel account want 403, got %d (%s)", code, first(raw, 200))
	}

	code, raw = call(t, app, http.MethodGet, "/v1/patrol/site/depot", org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read the site want 200, got %d (%s)", code, raw)
	}
	var st patrolSite
	data(t, raw, &st)
	if st.Account != "1001" {
		t.Errorf("the site's panel account is now %q: activations for 1001 no longer reach it", st.Account)
	}
	if st.Lat == 0 || st.Tier == "" {
		t.Errorf("a partial PUT erased the register facts a body left out: %+v", st)
	}
}

// TestFiledReportIsNotRewritable holds the filed account to what it is for. A
// report is composed once so it reads the same a month later — it is what a
// client is shown, what an insurer is sent, and what a court reads. patrol.report
// carries clientPerms, which reaches the officer through fieldPerms, so the
// officer whose shift the report describes may rewrite it.
func TestFiledReportIsNotRewritable(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportShift})
	if code != http.StatusCreated {
		t.Fatalf("file a report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)

	const forged = "FORGED: nothing happened tonight."
	if code, body := call(t, app, http.MethodPut, "/v1/framework/"+DTReport.String()+"/"+r.Name, org, officer,
		map[string]any{"kind": ReportShift, "body": forged, "at": now()}); code != http.StatusForbidden {
		t.Errorf("an officer rewriting a filed report want 403, got %d (%s)", code, first(body, 200))
	}
	code, body := call(t, app, http.MethodGet, "/v1/patrol/report/"+r.Name+"?format=text", org, controller, nil)
	if code == http.StatusOK && strings.Contains(string(body), forged) {
		t.Errorf("the download hands back the rewritten text as the filed report: %s", first(body, 80))
	}
}

// TestReportDownloadAdmitsOnlyTheTable holds /report/{name} to §3. The table
// names owner, manager, supervisor, controller and the client; the officer is
// not on it, and a filed report carries the estate — every site under contract,
// every key set held, every checkpoint of the round.
func TestReportDownloadAdmitsOnlyTheTable(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportShift})
	if code != http.StatusCreated {
		t.Fatalf("file a report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)

	if code, body := call(t, app, http.MethodGet, "/v1/patrol/report/"+r.Name+"?format=text", org, officer, nil); code != http.StatusForbidden {
		t.Fatalf("an officer downloading a shift report want 403, got %d (%s)", code, first(body, 120))
	}
}

// TestAnonymousReachesNeitherRawHandler covers the two routes that are not typed
// ops. They resolve the principal themselves rather than through the op
// machinery, so the refusal every typed op gets for free is theirs to get right.
func TestAnonymousReachesNeitherRawHandler(t *testing.T) {
	app := mount(t)
	install(t, app, org)

	for _, path := range []string{"/v1/patrol/feed", "/v1/patrol/feed?since=2026-01-01+00%3A00%3A00", "/v1/patrol/report/r1"} {
		if code, raw := hit(t, app, http.MethodGet, path, nil, nil); code != http.StatusUnauthorized {
			t.Errorf("GET %s with no principal want 401, got %d (%s)", path, code, raw)
		}
		if code, raw := hit(t, app, http.MethodGet, path, nil,
			map[string]string{"X-Org-Id": org}); code != http.StatusUnauthorized {
			t.Errorf("GET %s naming an org with no user want 401, got %d (%s)", path, code, raw)
		}
	}
}

// TestAlarmIntakeDoesNotTrustARawHeader holds the intake's machine admission to
// a minted fact. principal.IsApp is one line — `c.Header("X-User-IsApp") ==
// "true"` — composed with nothing, and admitAlarm short-circuits the whole role
// check on it. The identity boundary mints that header only for a principal with
// no subject org, so it can never name a human; patrol reads the header rather
// than the pair, so it cannot tell. Patrol ships as plugin/patrol, its own
// process, and principal.go says in as many words that a check reading a header
// in a process without the boundary is reading whatever the client typed.
func TestAlarmIntakeDoesNotTrustARawHeader(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/alarm", org, buyer,
		map[string]any{"ref": "A", "account": "1001", "trigger": "Intruder"}); code != http.StatusForbidden {
		t.Fatalf("a client posting an activation want 403, got %d", code)
	}
	// The same caller, one header later. buyer holds the client role in this org,
	// so the boundary would never mint a machine principal under that subject.
	code, raw := hit(t, app, http.MethodPost, "/v1/patrol/alarm",
		map[string]any{"ref": "B", "account": "1001", "trigger": "Intruder"},
		map[string]string{"X-Org-Id": org, "X-User-Id": buyer, "X-User-IsApp": "true"})
	if code != http.StatusForbidden {
		t.Fatalf("a caller holding a human role in the org was admitted as a machine: %d (%s)",
			code, first(raw, 160))
	}
}
