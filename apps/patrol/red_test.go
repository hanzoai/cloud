package patrol

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// red_test.go is the third adversarial pass. Every test here names a way the
// round-two repairs can still be walked around, and each one is written to FAIL
// while the way is open.

// ---- R1: the meta-permission is a writer after all ----

// TestManagerCannotRedefineAnImmutableTrail. filedPerms + Immutable is the claim
// that an append-only record "is refused by the record rather than by a row that
// could be added". The record is a row too: PUT /v1/framework/doctypes/patrol.step
// is Manager-only and replaces the definition wholesale, so the org's own System
// Manager drops Immutable, grants itself write, and rewrites the act trail an
// insurer and a court read.
func TestManagerCannotRedefineAnImmutableTrail(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatalf("VERIFY want 200, got %d", code)
	}
	verify := oneStep(t, app, Verify)

	// The org owner rewrites the schema: no Immutable, and a write grant.
	loose := map[string]any{
		"name":     DTStep.String(),
		"module":   Module,
		"autoname": "hash",
		"fields": []map[string]any{
			{"fieldname": "incident", "fieldtype": "Link", "options": DTIncident.String(), "reqd": true},
			{"fieldname": "act", "fieldtype": "Data"},
			{"fieldname": "from", "fieldtype": "Data"},
			{"fieldname": "to", "fieldtype": "Data"},
			{"fieldname": "actor", "fieldtype": "Data"},
			{"fieldname": "detail", "fieldtype": "Text"},
			{"fieldname": "at", "fieldtype": "Datetime"},
		},
		"permissions": []map[string]any{
			{"role": "System Manager", "read": true, "write": true, "create": true, "delete": true},
		},
	}
	code, body := call(t, app, http.MethodPut, "/v1/framework/doctypes/"+DTStep.String(), org, boss, loose)
	if code == http.StatusOK {
		t.Errorf("the org's System Manager dropped Immutable off the act trail (200): %s", first(body, 200))
	}
	code, body = call(t, app, http.MethodPut, "/v1/framework/"+DTStep.String()+"/"+verify, org, boss,
		map[string]any{"incident": name, "act": Verify, "from": Received, "to": Verified,
			"actor": "somebody else", "detail": "rewritten", "at": "1999-01-01 00:00:00"})
	if code == http.StatusOK {
		t.Errorf("an act on the append-only trail was rewritten after the schema was replaced (200): %s", first(body, 200))
	}
}

// TestManagerCannotEraseAnImmutableTrailWholesale. DELETE on the DEFINITION
// takes every document with it and asks only whether the caller is a manager.
// An Immutable DocType that can be deleted schema-and-all is an audit trail with
// a delete button on it.
func TestManagerCannotEraseAnImmutableTrailWholesale(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	if code, _ := stepped(t, app, name, controller, map[string]any{"act": Verify}); code != http.StatusOK {
		t.Fatalf("VERIFY want 200, got %d", code)
	}
	if oneStep(t, app, Verify) == "" {
		t.Fatal("no trail to erase")
	}
	code, body := call(t, app, http.MethodDelete, "/v1/framework/doctypes/"+DTStep.String(), org, boss, nil)
	if code < 400 {
		t.Errorf("the org's System Manager deleted the act trail, schema and all (%d): %s", code, first(body, 200))
	}
}

// oneStep answers the name of the newest step carrying `act`.
func oneStep(t *testing.T, app *zip.App, act string) string {
	t.Helper()
	code, raw := call(t, app, http.MethodGet, "/v1/framework/"+DTStep.String(), org, boss, nil)
	if code != http.StatusOK {
		t.Fatalf("read the trail want 200, got %d (%s)", code, raw)
	}
	var trail []struct {
		Name string `json:"name"`
		Act  string `json:"act"`
	}
	data(t, raw, &trail)
	for _, s := range trail {
		if s.Act == act {
			return s.Name
		}
	}
	return ""
}

// ---- R2: the round is forgeable on the generic plane ----

// TestOfficerCannotForgeAnotherRoundsCheckpoint. /v1/patrol/point/:name/confirm
// narrows to the officer's own unit and stamps the actor from the request. The
// SAME row is a document on the generic plane, where patrol.point is
// fieldPerms() — the officer holds Write on every row of it — and neither
// Immutable nor a read-only actor stands in the way. Proof of presence is the
// product; a round anybody on the roster can complete for somebody else is not
// proof of anything.
func TestOfficerCannotForgeAnotherRoundsCheckpoint(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedUnit(t, app, org, "gf-01", "someone.else@acme.test", UnitAvailable, 51.90, -8.47)
	round := create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": "2026-01-02"})
	point := create(t, app, org, DTPoint, map[string]any{
		"tour": round["name"], "site": "depot", "tag": "T1",
		"open": "2026-01-02 20:00:00", "close": "2026-01-02 22:00:00",
	})
	pname, _ := point["name"].(string)

	// The operational route refuses: the round is not this officer's.
	if code, _ := call(t, app, http.MethodPost, "/v1/patrol/point/"+pname+"/confirm", org, officer,
		map[string]any{"method": "tag"}); code < 400 {
		t.Fatalf("/v1/patrol confirm on another officer's round want a refusal, got %d", code)
	}
	// The generic plane serves the same row.
	code, body := call(t, app, http.MethodPut, "/v1/framework/"+DTPoint.String()+"/"+pname, org, officer,
		map[string]any{"state": "done", "at": "2026-01-02 21:00:00", "method": "tag",
			"actor": "someone.else@acme.test", "lat": 51.90, "lon": -8.47})
	if code == http.StatusOK {
		t.Errorf("an officer completed another officer's checkpoint, in that officer's name, on the generic plane (200): %s",
			first(body, 240))
	}
}

// TestOfficerCannotRewriteAnotherUnitsTrail. A position report is stamped by the
// server and held to the officer's own unit at /v1/patrol/unit/:name/fix. The
// stored row is patrol.fix, fieldPerms(), not Immutable: the officer edits any
// unit's recorded position to any place at any time.
func TestOfficerCannotRewriteAnotherUnitsTrail(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedUnit(t, app, org, "gf-02", "someone.else@acme.test", UnitAvailable, 51.90, -8.47)
	fix := create(t, app, org, DTFix, map[string]any{
		"unit": "gf-02", "lat": 51.90, "lon": -8.47, "at": "2026-01-02 21:00:00"})
	fname, _ := fix["name"].(string)

	code, body := call(t, app, http.MethodPut, "/v1/framework/"+DTFix.String()+"/"+fname, org, officer,
		map[string]any{"unit": "gf-02", "lat": 0.0, "lon": 0.0, "at": "1999-01-01 00:00:00"})
	if code == http.StatusOK {
		t.Errorf("an officer moved another unit's recorded position (200): %s", first(body, 240))
	}
}

// ---- R3: a Single's read-only fields are not held ----

// TestSingleHoldsItsReadOnlyFieldOnUpdate. UpdateDocument branches to
// writeSingle BEFORE fixed(carried(...)) runs, so a Single DocType's read-only
// fields are held at creation and free thereafter. patrol.tenant declares none
// today; the engine is the fleet's, and this is the rule it states.
func TestSingleHoldsItsReadOnlyFieldOnUpdate(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	single := map[string]any{
		"name": "Meter", "module": "ledger", "isSingle": true,
		"fields": []map[string]any{
			{"fieldname": "memo", "fieldtype": "Data"},
			{"fieldname": "state", "fieldtype": "Select", "options": "open\nclosed", "default": "open", "readOnly": true},
		},
		"permissions": []map[string]any{
			{"role": "System Manager", "read": true, "write": true, "create": true, "delete": true},
		},
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/framework/doctypes", org, boss, single); code != http.StatusCreated {
		t.Fatalf("define the Single want 201, got %d (%s)", code, raw)
	}
	// Creation holds it: the DocType says a meter is born open.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/ledger.Meter", org, boss,
		map[string]any{"memo": "one", "state": "closed"}); code == http.StatusCreated {
		t.Errorf("a create landed a read-only field off its declared birth value on a Single (201)")
	}
	if code, raw := call(t, app, http.MethodPut, "/v1/framework/ledger.Meter/ledger.Meter", org, boss,
		map[string]any{"memo": "one", "state": "closed"}); code == http.StatusOK {
		t.Errorf("a PUT moved a Single's read-only field (200): %s", first(raw, 200))
	}
}

// ---- R4: the ticket verifier without a KMS ----

// TestTicketRouteSurvivesNoKMS. stream() asks whether o.s.KMS is nil before it
// touches it; the ticket verifier reaches the same vault and must ask the same
// question. A deployment with no KMS has no key to verify against and no
// credential to hand back, so both routes answer the absence — and neither
// dereferences a vault that is not there.
//
// The presented ticket is well formed, so the route does not stop at its shape:
// it names a tenant and carries a tag, which is what takes it as far as the
// vault.
func TestTicketRouteSurvivesNoKMS(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	create(t, app, org, DTCamera, map[string]any{"name": "cam-1", "site": "depot", "label": "Yard"})

	code, body := call(t, app, http.MethodGet,
		"/v1/patrol/camera/cam-1/stream/"+org+".deadbeef?expires=4102444800", org, controller, nil)
	if code != http.StatusNotFound {
		t.Errorf("a ticket presented to a deployment with no KMS want 404, got %d: %s", code, first(body, 200))
	}
	if code, body := call(t, app, http.MethodGet,
		"/v1/patrol/camera/cam-1/stream", org, controller, nil); code != http.StatusNotFound {
		t.Errorf("minting with no KMS want 404, got %d: %s", code, first(body, 200))
	}
}

// ---- R5: the anonymous tenant read mints a store per name ----

// TestAnonymousTenantDoesNotMintAStorePerName. GET /v1/patrol/tenant/:org is the
// one anonymous route and it names the org from the path. The read resolves an
// engine for that name, and an engine is a file: an unauthenticated caller
// naming a fresh org on every request writes a database per request.
func TestAnonymousTenantDoesNotMintAStorePerName(t *testing.T) {
	dir := t.TempDir()
	app := mountIn(t, dir)
	install(t, app, org)

	before := countOrgDirs(t, dir)
	for i := range 40 {
		hit(t, app, http.MethodGet, "/v1/patrol/tenant/ghost"+itoa(i), nil, map[string]string{})
	}
	after := countOrgDirs(t, dir)
	if after > before {
		t.Errorf("an anonymous caller minted %d per-org stores by naming them (%d → %d)", after-before, before, after)
	}
}

// ---- R6: the customer's open-incident list fans out per site per state ----

// TestOpenIncidentListDoesNotFanOutPerSitePerState. seen() asks the store once
// per site of the caller's estate, and openIncidents() asks seen() once per open
// state — and incidents() computes a full narrowed window first and then throws
// it away when ?open=1. A customer with a large estate turns one GET into
// sites×(states+1) queries against a single-connection SQLite file.
func TestOpenIncidentListDoesNotFanOutPerSitePerState(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	const estate = 120
	for i := range estate {
		seedSite(t, app, org, "site"+itoa(i), "acc"+itoa(i), "northside")
	}

	start := time.Now()
	if code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident?open=1", org, buyer, nil); code != http.StatusOK {
		t.Fatalf("a client listing open incidents want 200, got %d (%s)", code, raw)
	}
	client := time.Since(start)

	start = time.Now()
	if code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident?open=1", org, controller, nil); code != http.StatusOK {
		t.Fatalf("the control room listing open incidents want 200, got %d (%s)", code, raw)
	}
	room := time.Since(start)

	if client > 20*room {
		t.Errorf("one customer GET cost %v against the control room's %v — %d sites × the open states, per request",
			client, room, estate)
	}
}

// ---- R7: a client report is the whole operation ----

// TestClientReportIsAboutOneClient. kind=client takes no client, and compose()
// sends it down the same estateBody as kind=shift: the figures are the whole
// operation's — every site under contract, every key set held, every checkpoint
// — filed under a name that says it is the customer's.
func TestClientReportIsAboutOneClient(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	create(t, app, org, DTClient, map[string]any{"name": "southside", "contact": rival})
	seedSite(t, app, org, "theirs", "1001", "northside")
	seedSite(t, app, org, "rivals", "1002", "southside")

	shift := filed(t, app, ReportShift)
	client := filed(t, app, ReportClient)
	if stripHead(shift) == stripHead(client) {
		t.Errorf("a client report is the shift report with another word on it:\n%s", first([]byte(client), 400))
	}
}

// filed composes a report of one kind and answers its stored text.
func filed(t *testing.T, app *zip.App, kind string) string {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller, map[string]any{"kind": kind})
	if code != http.StatusCreated {
		t.Fatalf("file a %s report want 201, got %d (%s)", kind, code, raw)
	}
	var v patrolReport
	data(t, raw, &v)
	return v.Body
}

// stripHead drops the letterhead, which is the one line that differs by kind.
func stripHead(body string) string {
	if i := indexOf(body, "\n\n"); i >= 0 {
		return body[i:]
	}
	return body
}

// ---- helpers ----

func itoa(n int) string { return strconv.Itoa(n) }

func indexOf(s, sub string) int { return strings.Index(s, sub) }

// mountIn is mount() with the data directory named, so a test can count the
// stores a route opened.
func mountIn(t *testing.T, dir string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", dir)
	planetest.Roled(t, func(_, user string) []string { return grants[user] })
	planetest.Entitled(t, func(_, product string) bool { return product == Module })
	deps := cloud.Deps{}
	if err := framework.Use(app, deps); err != nil {
		t.Fatalf("mount framework: %v", err)
	}
	if err := Use(app, deps); err != nil {
		t.Fatalf("mount patrol: %v", err)
	}
	t.Cleanup(func() { _ = framework.Shutdown() })
	return app
}

// countOrgDirs counts the per-org stores under a data directory.
func countOrgDirs(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n++
		}
		return nil
	})
	return n
}
