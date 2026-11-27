package patrol

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The users every test signs as. Each one holds exactly the role its name says,
// so a refusal in a test is a refusal about a role.
const (
	boss       = "boss@acme.test"       // System Manager, the org owner
	owner      = "owner@acme.test"      // owner
	manager    = "manager@acme.test"    // manager
	supervisor = "supervisor@acme.test" // supervisor
	controller = "controller@acme.test" // controller
	officer    = "officer@acme.test"    // officer
	buyer      = "buyer@acme.test"      // client
	nobody     = "nobody@acme.test"     // no role at all
	// The receiving centre's own machine accounts. A credential is admitted by the
	// role its tenant granted it, exactly as a person is, so the centre holds the
	// centre role — post an activation, read nothing — and the identity boundary's
	// machine label grants nothing on its own.
	centre  = "arc-receiver"
	centre2 = "arc@acme.test"
)

// grants is the role table the IAM peer answers from.
var grants = map[string][]string{
	boss:       {framework.RoleSystemManager},
	owner:      {RoleOwner},
	manager:    {RoleManager},
	supervisor: {RoleSupervisor},
	controller: {RoleController},
	officer:    {RoleOfficer},
	buyer:      {RoleClient},
	centre:     {RoleCentre},
	centre2:    {RoleCentre},
}

// TestFixturesValid proves every patrol DocType is a well-formed schema, that
// its Link and Table targets resolve within the lane, and that names are
// slug-clean.
func TestFixturesValid(t *testing.T) {
	dts := DocTypes()
	if len(dts) != 18 {
		t.Fatalf("patrol declares %d DocTypes, want 18", len(dts))
	}
	addrs := map[string]framework.DocType{}
	for _, dt := range dts {
		if err := dt.Validate(); err != nil {
			t.Fatalf("fixture %q invalid: %v", dt.Name, err)
		}
		if !slugClean(dt.Name) {
			t.Fatalf("DocType name %q is not slug-clean", dt.Name)
		}
		if dt.Module != Module {
			t.Fatalf("%s: module want %q, got %q", dt.Name, Module, dt.Module)
		}
		if dt.IsSubmittable {
			t.Fatalf("%s must not be submittable: an incident advances by a state write", dt.Name)
		}
		addrs[dt.ID().String()] = dt
	}
	for _, dt := range dts {
		for _, f := range dt.Fields {
			if f.Fieldtype != framework.FieldLink && f.Fieldtype != framework.FieldTable {
				continue
			}
			if _, ok := addrs[f.Options]; !ok {
				t.Fatalf("%s.%s targets %q, which is not a patrol DocType (patrol must be self-contained)",
					dt.Name, f.Fieldname, f.Options)
			}
		}
	}
}

// TestModelSpec locks the contract the routes and the front end both read: the
// nine incident states, the site and unit vocabularies, the Single tenant, and
// the names a route addresses a document by.
func TestModelSpec(t *testing.T) {
	byID := map[framework.ID]framework.DocType{}
	for _, dt := range DocTypes() {
		byID[dt.ID()] = dt
	}
	for _, want := range []framework.ID{
		DTTenant, DTClient, DTZone, DTHazard, DTUnit, DTSite, DTFix, DTTour, DTPoint,
		DTKey, DTMovement, DTCamera, DTIncident, DTAlarm, DTStep, DTEvent, DTNote, DTReport,
	} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("missing patrol DocType %s", want)
		}
	}

	if !byID[DTTenant].IsSingle {
		t.Fatalf("%s must be a Single: one configuration document per org", DTTenant)
	}

	inc := byID[DTIncident]
	st, ok := fieldOf(inc, "state")
	if !ok || st.Fieldtype != framework.FieldSelect || st.Default != Received {
		t.Fatalf("%s.state must be a Select defaulting to %s, got %+v", DTIncident, Received, st)
	}
	states := strings.Split(st.Options, "\n")
	want := []string{Received, Verified, Dispatched, Enroute, Onsite, Checked, Secured, Filed, Closed}
	if len(states) != len(want) {
		t.Fatalf("%s.state has %d states, want the nine", DTIncident, len(states))
	}
	for i, s := range want {
		if states[i] != s {
			t.Fatalf("%s.state position %d is %q, want %q", DTIncident, i, states[i], s)
		}
	}
	if inc.Autoname != "inc-.#####" {
		t.Fatalf("%s must carry a series name, got %q", DTIncident, inc.Autoname)
	}
	// Every act's stamp is a field on the incident, so a transition has somewhere
	// to record itself.
	for a, m := range acts {
		f, ok := fieldOf(inc, m.stamp)
		if !ok || f.Fieldtype != framework.FieldDatetime {
			t.Fatalf("act %s stamps %q, which is not a Datetime field on %s", a, m.stamp, DTIncident)
		}
	}
	if f, ok := fieldOf(inc, "opened"); !ok || f.Fieldtype != framework.FieldDatetime {
		t.Fatalf("%s.opened must be a Datetime: it is what every SLA clock runs from", DTIncident)
	}

	site := byID[DTSite]
	if a, ok := fieldOf(site, "account"); !ok || !a.Unique {
		t.Fatalf("%s.account must be Unique: an activation matches a site on it, got %+v", DTSite, a)
	}
	if s, ok := fieldOf(site, "status"); !ok || s.Options != siteStates || s.Default != SiteOK {
		t.Fatalf("%s.status must be the four-state Select defaulting ok, got %+v", DTSite, s)
	}
	if site.Autoname != "field:name" || site.TitleField != "name" {
		t.Fatalf("%s must be named by its name, got autoname %q title %q", DTSite, site.Autoname, site.TitleField)
	}
	if z, ok := fieldOf(site, "zones"); !ok || z.Fieldtype != framework.FieldTable || z.Options != DTZone.String() {
		t.Fatalf("%s.zones must be a child table of %s so one fetch answers the site", DTSite, DTZone)
	}

	unit := byID[DTUnit]
	if unit.Autoname != "field:sign" {
		t.Fatalf("%s must be named by its call sign, got %q", DTUnit, unit.Autoname)
	}
	if s, ok := fieldOf(unit, "state"); !ok || s.Options != unitStates || s.Default != UnitAvailable {
		t.Fatalf("%s.state must be the seven-state Select defaulting available, got %+v", DTUnit, s)
	}
	for _, dt := range []framework.ID{DTFix, DTMovement, DTStep, DTEvent} {
		if byID[dt].Autoname != "hash" {
			t.Fatalf("%s is an append-only trail and must be hash-named, got %q", dt, byID[dt].Autoname)
		}
	}
	if sec, ok := fieldOf(byID[DTCamera], "secret"); !ok || !sec.ReadOnly {
		t.Fatalf("%s.secret must be read-only: it is a KMS path, not a credential", DTCamera)
	}
}

// TestGatedDocTypes proves the structural gating: there is no key row an officer
// or a client could read, and no note row a client could read, because their
// permissions never name them.
func TestGatedDocTypes(t *testing.T) {
	byID := map[framework.ID][]framework.DocPerm{}
	for _, dt := range DocTypes() {
		byID[dt.ID()] = dt.Perms
	}
	for _, gated := range []framework.ID{DTKey, DTMovement} {
		if names(byID[gated], RoleOfficer) || names(byID[gated], RoleClient) {
			t.Errorf("%s must name neither officer nor client: %v", gated, byID[gated])
		}
	}
	if names(byID[DTNote], RoleClient) {
		t.Errorf("%s must not name the client: %v", DTNote, byID[DTNote])
	}
	if !names(byID[DTNote], RoleOfficer) {
		t.Errorf("%s must name the officer, who writes them: %v", DTNote, byID[DTNote])
	}
	// The client reads and never writes, wherever it is named at all.
	for _, dt := range DocTypes() {
		for _, p := range dt.Perms {
			if p.Role == RoleClient && (p.Write || p.Create || p.Delete) {
				t.Errorf("%s grants the client more than read: %+v", dt.Name, p)
			}
		}
	}
	// Every DocType names System Manager, so the org owner always resolves.
	for _, dt := range DocTypes() {
		if !names(dt.Perms, framework.RoleSystemManager) {
			t.Errorf("%s does not name System Manager", dt.Name)
		}
	}
}

// TestRolelessCallerIsDeniedOnEveryDocType drives the real engine: a member of
// the org holding no patrol role reads nothing and writes nothing, on every one
// of the eighteen. Default is deny, and this measures it rather than asserting
// it.
func TestRolelessCallerIsDeniedOnEveryDocType(t *testing.T) {
	app := mount(t)
	install(t, app, org)

	for _, dt := range DocTypes() {
		addr := dt.ID().String()
		if code, body := call(t, app, http.MethodGet, "/v1/framework/"+addr, org, nobody, nil); code == http.StatusOK {
			t.Errorf("%s: a role-less caller READ it (200): %s", addr, body)
		}
		if code, body := call(t, app, http.MethodPost, "/v1/framework/"+addr, org, nobody,
			map[string]any{"name": "x"}); code == http.StatusCreated {
			t.Errorf("%s: a role-less caller CREATED one (201): %s", addr, body)
		}
	}
}

// ---- harness ----

// org is the tenant every test acts in.
const org = "acme"

// mount builds the engine and the patrol surface on one app, with an IAM peer
// answering the role table above. Roles are what patrol admits on, so the peer
// is the whole fixture.
func mount(t *testing.T) *zip.App {
	t.Helper()
	return mountWith(t, func() { planetest.Roled(t, func(_, user string) []string { return grants[user] }) })
}

// mountWith is mount with the IAM role peer the test publishes.
func mountWith(t *testing.T, peer func()) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	peer()
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

// install creates the patrol DocTypes in one org, as the org owner.
func install(t *testing.T, app *zip.App, in string) {
	t.Helper()
	if code, body := call(t, app, http.MethodPost, "/v1/framework/modules/"+Module+"/install", in, boss, nil); code != http.StatusOK {
		t.Fatalf("install patrol want 200, got %d (%s)", code, body)
	}
}

// create writes one document through the generic surface, as the org owner.
func create(t *testing.T, app *zip.App, in string, id framework.ID, body map[string]any) map[string]any {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/framework/"+id.String(), in, boss, body)
	if code != http.StatusCreated {
		t.Fatalf("create %s want 201, got %d (%s)", id, code, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// seedSite creates one site with a panel account and an optional customer.
func seedSite(t *testing.T, app *zip.App, in, name, account, client string) {
	t.Helper()
	body := map[string]any{
		"name": name, "account": account, "tier": "gold", "sla": 20,
		"lat": 51.90, "lon": -8.47, "status": SiteOK,
	}
	if client != "" {
		body["client"] = client
	}
	create(t, app, in, DTSite, body)
}

// seedUnit creates one unit at a position.
func seedUnit(t *testing.T, app *zip.App, in, sign, who, state string, lat, lon float64) {
	t.Helper()
	create(t, app, in, DTUnit, map[string]any{
		"sign": sign, "officer": who, "state": state, "lat": lat, "lon": lon,
	})
}

// call issues one request as a named user in a named org.
func call(t *testing.T, app *zip.App, method, path, in, user string, body any) (int, []byte) {
	t.Helper()
	return hit(t, app, method, path, body, map[string]string{"X-Org-Id": in, "X-User-Id": user})
}

// hit issues one request with exactly the headers given, so an anonymous or a
// machine caller is expressible.
func hit(t *testing.T, app *zip.App, method, path string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// A budget, not zip's 1s default: a loaded CI runner takes longer than that.
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// waits issues one request that may sit behind something slower than the test
// client's default patience — a claim another caller holds, a bus that has to
// time out — so the wait the route makes is not read as a timeout.
func waits(t *testing.T, app *zip.App, method, path, in, user string, body any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Org-Id", in)
	req.Header.Set("X-User-Id", user)
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// data decodes a {"data": …} envelope into v.
func data(t *testing.T, raw []byte, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, raw)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode data: %v (%s)", err, env.Data)
	}
}

func fieldOf(dt framework.DocType, name string) (framework.DocField, bool) {
	for _, f := range dt.Fields {
		if f.Fieldname == name {
			return f, true
		}
	}
	return framework.DocField{}, false
}

func names(perms []framework.DocPerm, role string) bool {
	for _, p := range perms {
		if p.Role == role {
			return true
		}
	}
	return false
}

func slugClean(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return s != ""
}
