package patrol

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/zap-proto/zip"
)

// report_test.go reads what a filed account says. A report is composed once from
// one estate read and stored as text, so what it carries is what the operation
// stands behind a month later — and who may download one is decided by who it is
// about, not by who asked.

// letterhead writes the tenant document a report is headed with.
func letterhead(t *testing.T, app *zip.App) {
	t.Helper()
	if code, raw := call(t, app, http.MethodPut, "/v1/framework/"+DTTenant.String()+"/"+DTTenant.Name, org, boss,
		map[string]any{
			"company": map[string]any{
				"legalName": "Northstar Security Ltd", "address": "12 Quay St, Cork",
				"phone": "+353 21 000 0000", "licence": "PSA 12345", "registration": "IE 987654",
			},
			"operations": map[string]any{"verifyTargetSeconds": 45, "dispatchTargetSeconds": 120},
			"contacts":   map[string]any{"controllers": []string{controller}},
		}); code != http.StatusOK {
		t.Fatalf("write the tenant: %d (%s)", code, raw)
	}
}

// figure is one dotted count as a report writes it, and detail one dotted value,
// so a test names the label and the number rather than the dots between them.
func figure(label string, n int) string { return "  " + pad(label) + " " + strconv.Itoa(n) }

func detail(label, value string) string { return "  " + pad(label) + " " + value }

// checkpoint seeds one checkpoint already in a state. The confirmation route
// owns that field, so a round that has already been walked is written through
// the in-process API rather than through a surface that holds it read-only.
func checkpoint(t *testing.T, tour, site, state string) {
	t.Helper()
	if _, err := framework.Ingest(context.Background(), org, DTPoint, map[string]any{
		"tour": tour, "site": site, "tag": site + "-" + state, "state": state,
		"open": "2026-01-02 20:00:00", "close": "2026-01-02 22:00:00",
	}, ""); err != nil {
		t.Fatalf("seed a %s checkpoint at %s: %v", state, site, err)
	}
}

// TestAReportIsHeadedByTheCompanyItIsFiledBy proves the letterhead: the legal
// entity, where to find it, and what it is licensed as, all off the tenant
// document rather than off the request.
func TestAReportIsHeadedByTheCompanyItIsFiledBy(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	letterhead(t, app)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportShift})
	if code != http.StatusCreated {
		t.Fatalf("file a report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)
	for _, want := range []string{
		"NORTHSTAR SECURITY LTD — SHIFT REPORT", "12 Quay St, Cork · +353 21 000 0000",
		"PSA 12345 · IE 987654", "Taken ",
	} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("the letterhead is missing %q:\n%s", want, r.Body)
		}
	}
}

// TestAShiftReportCountsTheEstateAsItStands proves the figures a shift report
// turns on are read off the registers: how the estate stands and how the round
// went.
func TestAShiftReportCountsTheEstateAsItStands(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	seedSite(t, app, org, "yard", "1002", "")
	seedSite(t, app, org, "mill", "1003", "")
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "depot", "where": "safe"})
	for _, s := range []struct{ name, status string }{{"yard", SiteAlarm}, {"mill", SiteFault}} {
		if code, raw := call(t, app, http.MethodPut, "/v1/patrol/site/"+s.name, org, manager,
			map[string]any{"status": s.status}); code != http.StatusOK {
			t.Fatalf("set %s %s: %d (%s)", s.name, s.status, code, raw)
		}
	}
	tour := create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": "2026-01-02"})
	for _, st := range []string{"done", "late", PointPending} {
		checkpoint(t, tour["name"].(string), "depot", st)
	}

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportShift})
	if code != http.StatusCreated {
		t.Fatalf("file a shift report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)
	for _, want := range []string{
		"ESTATE STATUS", "PATROL TOUR COMPLIANCE",
		figure("Sites under contract", 3), figure("Secure", 1),
		figure("Device / camera faults", 1), figure("Active alarms", 1),
		figure("Client key sets held", 1), figure("Patrol units on duty", 1),
		figure("Checkpoints scheduled", 3), figure("Confirmed", 1),
		figure("Late", 1), figure("Remaining in window", 1),
	} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("the figures do not read %q:\n%s", want, r.Body)
		}
	}
}

// TestAClientReportIsTheEstateArrangedByCustomer proves the other question about
// the same registers: not how the operation did, but how it did for each
// customer — and a site under no contract is in nobody's account.
func TestAClientReportIsTheEstateArrangedByCustomer(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	seedSite(t, app, org, "unlet", "1003", "")
	create(t, app, org, DTKey, map[string]any{"ref": "K-1", "site": "theirs", "where": "safe"})
	create(t, app, org, DTKey, map[string]any{"ref": "K-2", "site": "unlet", "where": "safe"})
	seedUnit(t, app, org, "gf-01", officer, UnitAvailable, 51.90, -8.47)
	for _, s := range []struct{ name, status string }{{"rivals", SiteFault}, {"theirs", SiteAlarm}} {
		if code, raw := call(t, app, http.MethodPut, "/v1/patrol/site/"+s.name, org, manager,
			map[string]any{"status": s.status}); code != http.StatusOK {
			t.Fatalf("put %s into %s: %d (%s)", s.name, s.status, code, raw)
		}
	}
	tour := create(t, app, org, DTTour, map[string]any{"unit": "gf-01", "date": "2026-01-02"})
	for _, p := range []struct{ site, state string }{
		{"theirs", "done"}, {"theirs", "late"}, {"rivals", "done"}, {"unlet", "done"},
	} {
		checkpoint(t, tour["name"].(string), p.site, p.state)
	}

	// Every customer.
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportClient})
	if code != http.StatusCreated {
		t.Fatalf("file a client report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)
	if !strings.Contains(r.Body, "CLIENT ACCOUNTS") {
		t.Fatalf("a client report is arranged by customer:\n%s", r.Body)
	}
	for _, want := range []string{
		"  northside\n", "  southside\n",
		figure("  Sites under contract", 1), figure("  Key sets held", 1),
		figure("  Checkpoints scheduled", 2), figure("  Confirmed", 1),
		figure("  Late", 1), figure("  Device / camera faults", 1),
		figure("  Active alarms", 1), figure("  Secure", 0),
	} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("the accounts do not read %q:\n%s", want, r.Body)
		}
	}
	if strings.Contains(r.Body, "unlet") {
		t.Errorf("a site under no contract appeared in a customer's account:\n%s", r.Body)
	}

	// One customer.
	code, raw = call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportClient, "client": "northside"})
	if code != http.StatusCreated {
		t.Fatalf("file one customer's account want 201, got %d (%s)", code, raw)
	}
	data(t, raw, &r)
	if !strings.Contains(r.Body, "northside") || strings.Contains(r.Body, "southside") {
		t.Errorf("naming a customer must file that customer's account alone:\n%s", r.Body)
	}
}

// TestAClientReportOverNoContractSaysSo proves the empty case reads as an
// answer rather than as a blank page.
func TestAClientReportOverNoContractSaysSo(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportClient})
	if code != http.StatusCreated {
		t.Fatalf("file a client report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)
	if !strings.Contains(r.Body, "No estate under contract.") {
		t.Fatalf("an estate under no contract must say so:\n%s", r.Body)
	}
}

// TestAnIncidentReportIsTheAccountOfOneResponse proves what an incident report
// carries: the response, the clocks the acts settled against the tenant's own
// targets, and the whole trail with the officer named on it.
func TestAnIncidentReportIsTheAccountOfOneResponse(t *testing.T) {
	app := mount(t)
	name := opened(t, app)
	letterhead(t, app)
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
	if code, _ := stepped(t, app, name, supervisor,
		map[string]any{"act": Close, "outcome": "Nothing found", "false": true}); code != http.StatusOK {
		t.Fatalf("close want 200, got %d", code)
	}

	code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportIncident, "incident": name})
	if code != http.StatusCreated {
		t.Fatalf("file an incident report want 201, got %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)
	for _, want := range []string{
		"INCIDENT " + name,
		detail("Site", "depot"), detail("Zone", "Z3"), detail("Trigger", "Intruder"),
		detail("Tier", "gold"), detail("State", Closed), detail("Unit", "gf-01"),
		detail("Outcome", "Nothing found"), detail("False alarm", "yes"),
		"SLA CLOCKS", "Verify", "(target 45s)", "Dispatch", "(target 120s)",
		"On site", "(target 1200s)", "TRAIL", "OPEN", "VERIFY", "CLOSE", controller,
	} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("an incident report is missing %q:\n%s", want, r.Body)
		}
	}
}

// TestAnIncidentWithNoOpeningMomentHasNoClocks proves the clocks are derived and
// never stored: they run from the moment the activation landed, so an incident
// carrying none has none to show.
func TestAnIncidentWithNoOpeningMomentHasNoClocks(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	made, err := framework.Ingest(context.Background(), org, DTIncident, map[string]any{
		"site": "depot", "trigger": "Intruder", "state": Received,
	}, "")
	if err != nil {
		t.Fatalf("seed an incident with no opening moment: %v", err)
	}
	code, raw := call(t, app, http.MethodGet, "/v1/patrol/incident/"+made.Name, org, controller, nil)
	if code != http.StatusOK {
		t.Fatalf("read the incident want 200, got %d (%s)", code, raw)
	}
	var got patrolIncident
	data(t, raw, &got)
	if len(got.Clocks) != 0 {
		t.Fatalf("an incident with no opening moment showed %d clocks: %+v", len(got.Clocks), got.Clocks)
	}
}

// TestTheOnSiteClockFallsBackWhenNoSLAIsContracted proves the third clock's
// default: a site under no contracted response time still has a target to be
// read against.
func TestTheOnSiteClockFallsBackWhenNoSLAIsContracted(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTSite, map[string]any{
		"name": "depot", "account": "1001", "lat": 51.90, "lon": -8.47, "status": SiteOK,
	})
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "depot", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("open an incident want 201, got %d (%s)", code, raw)
	}
	var inc patrolIncident
	data(t, raw, &inc)
	var onsite *patrolClock
	for i, c := range inc.Clocks {
		if c.Label == "On site" {
			onsite = &inc.Clocks[i]
		}
	}
	if onsite == nil || onsite.Target != 1800 {
		t.Fatalf("the on-site clock is %+v, want the default target", onsite)
	}
	if onsite.Settled != -1 {
		t.Errorf("a clock that has not settled reads %d, want -1", onsite.Settled)
	}
}

// TestACustomerDownloadsOnlyAnAccountAboutThem proves who a report is for. The
// control room reads every one; a customer reads an account of their own estate
// or of an incident at a site of theirs, and anything else is 404 rather than a
// refusal, so the route is no existence oracle.
func TestACustomerDownloadsOnlyAnAccountAboutThem(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)

	filed := func(body map[string]any) string {
		t.Helper()
		code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller, body)
		if code != http.StatusCreated {
			t.Fatalf("file %v want 201, got %d (%s)", body, code, raw)
		}
		var r patrolReport
		data(t, raw, &r)
		return r.Name
	}
	opens := func(site string) string {
		t.Helper()
		code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
			map[string]any{"site": site, "trigger": "Intruder"})
		if code != http.StatusCreated {
			t.Fatalf("open an incident at %s want 201, got %d (%s)", site, code, raw)
		}
		var inc patrolIncident
		data(t, raw, &inc)
		return inc.Name
	}

	for _, tc := range []struct {
		name   string
		report string
		want   int
	}{
		{"their own account", filed(map[string]any{"kind": ReportClient, "client": "northside"}), http.StatusOK},
		{"another customer's account", filed(map[string]any{"kind": ReportClient, "client": "southside"}), http.StatusNotFound},
		{"an incident at a site of theirs",
			filed(map[string]any{"kind": ReportIncident, "incident": opens("theirs")}), http.StatusOK},
		{"an incident at another customer's site",
			filed(map[string]any{"kind": ReportIncident, "incident": opens("rivals")}), http.StatusNotFound},
		{"an account of the whole operation", filed(map[string]any{"kind": ReportShift}), http.StatusNotFound},
	} {
		code, raw := call(t, app, http.MethodGet, "/v1/patrol/report/"+tc.report+"?format=text", org, buyer, nil)
		if code != tc.want {
			t.Errorf("a customer downloading %s want %d, got %d (%s)", tc.name, tc.want, code, first(raw, 160))
		}
		if code, _ := call(t, app, http.MethodGet, "/v1/patrol/report/"+tc.report+"?format=text", org, controller, nil); code != http.StatusOK {
			t.Errorf("the control room downloading %s want 200, got %d", tc.name, code)
		}
	}
}

// TestAReportAboutAnIncidentThatIsGoneIsNobodys proves the narrowing fails
// closed: a customer's right to an incident report is the site it names, so a
// report whose incident has been removed is nobody's to read.
func TestAReportAboutAnIncidentThatIsGoneIsNobodys(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "theirs", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("open an incident: %d (%s)", code, raw)
	}
	var inc patrolIncident
	data(t, raw, &inc)
	code, raw = call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportIncident, "incident": inc.Name})
	if code != http.StatusCreated {
		t.Fatalf("file the report: %d (%s)", code, raw)
	}
	var r patrolReport
	data(t, raw, &r)
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/report/"+r.Name+"?format=text", org, buyer, nil); code != http.StatusOK {
		t.Fatalf("the customer reads their own incident's account, got %d", code)
	}
	if err := framework.Delete(context.Background(), org, DTIncident, inc.Name); err != nil {
		t.Fatalf("remove the incident: %v", err)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/patrol/report/"+r.Name+"?format=text", org, buyer, nil); code != http.StatusNotFound {
		t.Fatalf("a report whose incident is gone want 404, got %d", code)
	}
}

// TestAFiledClientReportTellsTheCustomer proves the one message filing sends: a
// customer's account reaches the customer it is about, at the contact on their
// own record.
func TestAFiledClientReportTellsTheCustomer(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	create(t, app, org, DTClient, map[string]any{"name": "northside", "contact": buyer})
	seedSite(t, app, org, "theirs", "1001", "northside")
	sent := mailbox(t)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportClient, "client": "northside"}); code != http.StatusCreated {
		t.Fatalf("file the account: %d (%s)", code, raw)
	}
	if !sent.to(buyer) {
		t.Errorf("the customer's own account was filed and they were not told: %v", sent.all())
	}
}

// TestAnAccountAboutAnIncidentReachesThatSitesCustomer proves the other way the
// customer behind a filed account is resolved: through the incident it is about,
// to the site that incident happened at, to the customer under contract for it.
func TestAnAccountAboutAnIncidentReachesThatSitesCustomer(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	sent := mailbox(t)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "theirs", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("open an incident: %d (%s)", code, raw)
	}
	var inc patrolIncident
	data(t, raw, &inc)

	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportClient, "incident": inc.Name}); code != http.StatusCreated {
		t.Fatalf("file the account: %d (%s)", code, raw)
	}
	if !sent.to(buyer) {
		t.Errorf("the customer whose site the incident happened at was not told: %v", sent.all())
	}
	if sent.to(rival) {
		t.Errorf("another customer was told about it: %v", sent.all())
	}
}

// TestAnAccountAboutASiteUnderNoContractTellsNobody proves the floor: an account
// filed about an incident at a site no customer is under contract for names
// nobody to tell, and sends nothing.
func TestAnAccountAboutASiteUnderNoContractTellsNobody(t *testing.T) {
	app := mount(t)
	install(t, app, org)
	seedSite(t, app, org, "depot", "1001", "")
	sent := mailbox(t)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "depot", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("open an incident: %d (%s)", code, raw)
	}
	var inc patrolIncident
	data(t, raw, &inc)
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportClient, "incident": inc.Name}); code != http.StatusCreated {
		t.Fatalf("file the account: %d (%s)", code, raw)
	}
	if got := sent.all(); len(got) != 0 {
		t.Fatalf("%d messages went out about a site under no contract: %+v", len(got), got)
	}
}

// TestAnAccountAboutAnIncidentWhoseSiteIsGoneTellsNobody proves the last step of
// resolving who a filed account is for: the incident names the site and the site
// names the customer, so a site the register no longer holds names nobody.
func TestAnAccountAboutAnIncidentWhoseSiteIsGoneTellsNobody(t *testing.T) {
	app := mount(t)
	twoCustomers(t, app)
	sent := mailbox(t)
	code, raw := call(t, app, http.MethodPost, "/v1/patrol/incident", org, controller,
		map[string]any{"site": "theirs", "trigger": "Intruder"})
	if code != http.StatusCreated {
		t.Fatalf("open an incident: %d (%s)", code, raw)
	}
	var inc patrolIncident
	data(t, raw, &inc)
	if err := framework.Delete(context.Background(), org, DTSite, "theirs"); err != nil {
		t.Fatalf("remove the site: %v", err)
	}
	if code, raw := call(t, app, http.MethodPost, "/v1/patrol/report", org, controller,
		map[string]any{"kind": ReportClient, "incident": inc.Name}); code != http.StatusCreated {
		t.Fatalf("file the account: %d (%s)", code, raw)
	}
	if got := sent.all(); len(got) != 0 {
		t.Fatalf("%d messages went out about a site that is gone: %+v", len(got), got)
	}
}

// TestAPaddedLabelStopsAtTheColumn proves the dotting: a label shorter than the
// column is dotted out to it so the figures line up, and one at or past it is
// left alone rather than truncated.
func TestAPaddedLabelStopsAtTheColumn(t *testing.T) {
	short := pad("Secure")
	if len(short) != 34 || !strings.HasPrefix(short, "Secure ") || !strings.HasSuffix(short, ".") {
		t.Errorf("a short label dots out to the column, got %q", short)
	}
	long := strings.Repeat("x", 40)
	if pad(long) != long {
		t.Errorf("a label past the column was rewritten to %q", pad(long))
	}
}

// TestAReportWithNoBodyStillRenders proves the PDF is produced from whatever the
// document holds: a report whose text is empty renders a page rather than
// failing the download.
func TestAReportWithNoBodyStillRenders(t *testing.T) {
	for _, body := range []string{"", "\n\n\n"} {
		pdf, err := render(body)
		if err != nil {
			t.Fatalf("render %q: %v", body, err)
		}
		if len(pdf) == 0 {
			t.Fatalf("%q rendered no bytes", body)
		}
	}
}

// TestAReportLongerThanAPageIsPaged proves the page shape: a body past
// linesPerPage is set across more than one page rather than running off the
// first.
func TestAReportLongerThanAPageIsPaged(t *testing.T) {
	one, err := render(strings.Repeat("a line\n", linesPerPage))
	if err != nil {
		t.Fatalf("render one page: %v", err)
	}
	two, err := render(strings.Repeat("a line\n", linesPerPage*3))
	if err != nil {
		t.Fatalf("render three pages: %v", err)
	}
	if len(two) <= len(one) {
		t.Errorf("three pages rendered %d bytes and one rendered %d", len(two), len(one))
	}
}

// TestAStoredFieldReadsBackAsTheNumberItIs proves the field readers: a stored
// number reaches Go as whatever the store's JSON made of it, and a field reads
// the same value through every one of those forms.
func TestAStoredFieldReadsBackAsTheNumberItIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		held any
		want int64
	}{
		{"a number that came back through JSON", float64(20), 20},
		{"a number written as Go's own", int64(20), 20},
		{"a number written as an int", int(20), 20},
		{"a check that is set", true, 1},
		{"a check that is not", false, 0},
		{"a field that is not there", nil, 0},
		{"a field holding something else", "twenty", 0},
	} {
		d := framework.Document{Data: map[string]any{"sla": tc.held}}
		if got := intField(d, "sla"); got != tc.want {
			t.Errorf("%s: intField = %d, want %d", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		held any
		want float64
	}{
		{"a coordinate that came back through JSON", float64(51.9), 51.9},
		{"a coordinate written as Go's own", int64(51), 51},
		{"a coordinate written as an int", int(51), 51},
		{"a field that is not there", nil, 0},
		{"a field holding something else", "north", 0},
	} {
		d := framework.Document{Data: map[string]any{"lat": tc.held}}
		if got := floatField(d, "lat"); got != tc.want {
			t.Errorf("%s: floatField = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestAJSONFieldIsHandedBackRatherThanReshaped proves what a JSON field answers:
// the bytes as they stand, so a caller re-marshals rather than re-shapes; and
// the JSON null for a field there is nothing of, or one nothing can be made of.
func TestAJSONFieldIsHandedBackRatherThanReshaped(t *testing.T) {
	for _, tc := range []struct {
		name string
		held any
		want string
	}{
		{"bytes already in hand", json.RawMessage(`{"name":"Northstar"}`), `{"name":"Northstar"}`},
		{"a value to render", map[string]any{"name": "Northstar"}, `{"name":"Northstar"}`},
		{"a field that is not there", nil, "null"},
		{"a value nothing can be made of", math.NaN(), "null"},
	} {
		d := framework.Document{Data: map[string]any{"brand": tc.held}}
		if got := string(jsonField(d, "brand")); got != tc.want {
			t.Errorf("%s: jsonField = %s, want %s", tc.name, got, tc.want)
		}
	}
	if got := string(jsonField(framework.Document{}, "brand")); got != "null" {
		t.Errorf("a document with no data answered %s", got)
	}
}

// TestAChildTableReadsItsRowsAndSkipsWhatIsNotOne proves the child-table reader:
// it answers the rows a table holds and passes over anything in it that is not a
// row, so a malformed document reads as the rows it does have.
func TestAChildTableReadsItsRowsAndSkipsWhatIsNotOne(t *testing.T) {
	d := framework.Document{Data: map[string]any{"zones": []any{
		map[string]any{"ref": "Z1"}, "Z2", nil, map[string]any{"ref": "Z3"},
	}}}
	rows := children(d, "zones")
	if len(rows) != 2 || str(rows[0]["ref"]) != "Z1" || str(rows[1]["ref"]) != "Z3" {
		t.Fatalf("a child table read %d rows: %+v", len(rows), rows)
	}
	if len(children(d, "hazards")) != 0 {
		t.Error("a table that is not there read rows")
	}
}
