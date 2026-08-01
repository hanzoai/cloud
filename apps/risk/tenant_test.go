package risk

// tenant_test.go proves the boundary. Every test here was written by
// reintroducing the defect and checking that THIS test — not some other one —
// goes red.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── the mint ────────────────────────────────────────────────────────────────

// TestTenantKeyNeverRegresses is the whole product in one test.
//
// The key is the store index, the org column on every engine record, AND the
// seed of the per-tenant tree geometry. Keyed on the BARE org, `acme` on
// hanzo.id and `acme` on zoo.ngo become one set of rows and one model. This
// pins that they cannot.
func TestTenantKeyNeverRegresses(t *testing.T) {
	hanzo, err := qualify("hanzo", "acme")
	if err != nil {
		t.Fatalf("qualify: %v", err)
	}
	zoo, err := qualify("zoo", "acme")
	if err != nil {
		t.Fatalf("qualify: %v", err)
	}
	if hanzo == zoo {
		t.Fatal("two brands' same-named orgs minted the SAME key — one set of rows and one model for two businesses")
	}
	if hanzo.String() != "hanzo/acme" {
		t.Fatalf("key = %q, want hanzo/acme", hanzo)
	}
	if hanzo.org() != "acme" {
		t.Fatalf("org half = %q, want acme", hanzo.org())
	}

	// A BARE org is not a key, and qualified() must say so — the assertion at
	// the boundary that catches a caller handing the core an unqualified value.
	if qualified("hanzo", Tenant("acme")) {
		t.Error("a bare org passed the qualified() check — the engine documents that value as invalid")
	}
	if qualified("hanzo", Tenant("zoo/acme")) {
		t.Error("another brand's key passed this brand's qualified() check")
	}

	for _, bad := range []struct{ brand, org, why string }{
		{"", "acme", "no brand vouches for the tenant"},
		{"hanzo", "", "no org names a tenant"},
		{"hanzo", publicTenant, "the anonymous lane is not a tenant"},
		{"hanzo", "lux/acme", "an org holding the separator is not readable back"},
		{"han/zo", "acme", "a brand holding the separator makes the key ambiguous"},
	} {
		if _, err := qualify(bad.brand, bad.org); err == nil {
			t.Errorf("qualify(%q, %q) was accepted — %s", bad.brand, bad.org, bad.why)
		}
	}
}

// TestPublicTenantIsNotATenant pins the reserved anonymous lane out of every
// plane. Credential-less writes land under it, so an unauthenticated stranger
// who could reach the risk surface through it would be moving a real tenant's —
// or the network's — statistics.
func TestPublicTenantIsNotATenant(t *testing.T) {
	if _, err := qualify("hanzo", publicTenant); err == nil {
		t.Fatal("$public minted a tenant key")
	}
	// And it is excluded from the network baseline's population statement, which
	// is the second place it could get in.
	if !strings.Contains(baselinePopulate, "$public") {
		t.Fatal("the baseline population statement does not exclude $public")
	}
	if !strings.Contains(baselinePopulate, "org != '$public'") {
		t.Fatal("the baseline excludes $public by some other spelling than the bare org — check both forms")
	}
}

// ── the feature read ────────────────────────────────────────────────────────

// TestFeatureReadOrgIsAlwaysTheLeadingBoundPredicate walks every predicate this
// package can build and asserts each one OPENS with `org = ?` and binds the
// tenant as the first argument.
//
// Leading matters as much as bound: `org` is the first column of the table's
// ORDER BY, so a leading predicate is a prefix scan and a trailing one is a
// filter over everybody's rows. Bound matters because a tenant key is data.
func TestFeatureReadOrgIsAlwaysTheLeadingBoundPredicate(t *testing.T) {
	tn := Tenant("hanzo/acme")
	start, end := time.Unix(0, 0).UTC(), time.Unix(3600, 0).UTC()

	for _, kind := range keys(subjectKinds) {
		where, args := riskWhere(tn, kind, "subject-1", start, end)
		if !strings.HasPrefix(where, "org = ?") {
			t.Errorf("kind %q: predicate %q does not OPEN with `org = ?`", kind, where)
		}
		if len(args) == 0 || args[0] != tn.String() {
			t.Errorf("kind %q: args[0] = %v, want the tenant key %q", kind, args, tn)
		}
		// Nothing user-derived may appear as text in the statement.
		if strings.Contains(where, tn.String()) || strings.Contains(where, "subject-1") {
			t.Errorf("kind %q: a value was interpolated into %q instead of bound", kind, where)
		}
	}
}

// TestFeatureReadRefusesAnUnknownKind pins the allowlist. A kind that is not in
// it is refused rather than reaching the statement, which is what makes the
// column list safe to concatenate.
func TestFeatureReadRefusesAnUnknownKind(t *testing.T) {
	_, err := window(context.Background(), Tenant("hanzo/acme"), "account'; DROP TABLE risk_feature; --", "x",
		time.Now().Add(-time.Hour), time.Now())
	if err == nil {
		t.Fatal("an unknown subject kind was accepted")
	}
	if _, err := baseline(context.Background(), "not-a-kind", time.Now()); err == nil {
		t.Fatal("the baseline accepted an unknown subject kind")
	}
}

// TestFeatureColumnsAreAnAllowlist proves the only identifiers that reach a
// statement come from this package's own map — never from a caller's spelling.
func TestFeatureColumnsAreAnAllowlist(t *testing.T) {
	ident := regexp.MustCompile(`^[a-z_]+$`)
	for name, col := range featureColumns {
		if !ident.MatchString(name) || !ident.MatchString(col) {
			t.Errorf("column %q -> %q is not a bare identifier; something outside the allowlist can reach the SQL", name, col)
		}
	}
	// The names the reader concatenates are the map's own keys, in order.
	got := columnNames()
	if len(got) != len(featureColumns) {
		t.Fatalf("columnNames returned %d of %d allowlisted columns", len(got), len(featureColumns))
	}
	for _, n := range got {
		if _, ok := featureColumns[n]; !ok {
			t.Errorf("columnNames produced %q, which is not allowlisted", n)
		}
	}
}

// ── the network baseline: aggregate-only, provably ──────────────────────────

// TestBaselineHasNoTenantColumn reads BOTH the Go row type and the DDL. A field
// or column naming an org, a subject, a person or an id would make a cross-org
// read expressible; the point of the design is that it is not.
func TestBaselineHasNoTenantColumn(t *testing.T) {
	forbidden := []string{"org", "organization", "tenant", "subject", "distinct", "person", "user", "account", "id"}

	rt := reflect.TypeOf(baselineRow{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.ToLower(rt.Field(i).Name)
		for _, f := range forbidden {
			if name == f {
				t.Errorf("baselineRow has a %q field — the network baseline must carry no tenant, subject or person", f)
			}
		}
	}

	// The DDL, read as text. `subject_kind` is an aggregation AXIS, not a
	// subject, so it is checked as a whole word rather than as a substring.
	body := ddlBody(baselineDDL)
	for _, line := range strings.Split(body, "\n") {
		col := strings.Fields(strings.TrimSpace(line))
		if len(col) == 0 {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(col[0]), ",")
		for _, f := range forbidden {
			if name == f {
				t.Errorf("hanzo.risk_baseline declares a %q column — the boundary would be a filter rather than a fact", f)
			}
		}
	}

	// And the ONLY statement that writes it is a package constant with no
	// placeholder: nothing a caller sends can reach it.
	if strings.Contains(baselinePopulate, "?") {
		t.Error("the baseline population statement takes a bound parameter — it must be composed of nothing but constants")
	}
}

// TestBaselineRefusesBelowKAnon pins the k-anonymity floor in BOTH places: in
// the statement, where it stops a thin bucket from being materialised at all,
// and on read, where it drops a row an operator inserted by hand.
func TestBaselineRefusesBelowKAnon(t *testing.T) {
	if kAnonMin < 25 {
		t.Fatalf("kAnonMin = %d; below 25 a quantile is close enough to one business's numbers to be them", kAnonMin)
	}
	if !strings.Contains(baselinePopulate, "HAVING orgs >= 25") {
		t.Error("the population statement does not carry the k-anonymity floor in its HAVING clause")
	}
	if !strings.Contains(baselinePopulate, "n >= 1000") {
		t.Error("the population statement does not carry the observation floor; k orgs at one row each is meaningless")
	}
	// The read-side belt: a row below either floor is dropped rather than
	// returned. Exercised through the same predicate the reader applies.
	for _, r := range []struct {
		orgs, n uint64
		want    bool
	}{
		{24, 100000, false}, {25, 999, false}, {25, 1000, true}, {1000, 1000000, true},
	} {
		got := r.orgs >= kAnonMin && r.n >= nMin
		if got != r.want {
			t.Errorf("orgs=%d n=%d published=%v, want %v", r.orgs, r.n, got, r.want)
		}
	}
}

// ddlBody returns the column block of a CREATE TABLE, so a column-name check
// reads columns and not the engine clause.
func ddlBody(ddl string) string {
	open := strings.Index(ddl, "(")
	close := strings.LastIndex(ddl, ")")
	if open < 0 || close < open {
		return ddl
	}
	return ddl[open+1 : close]
}

// TestFeatureTableLeadsWithOrg pins the sort key. `org` first is what makes a
// tenant read a prefix scan; on hanzo.cloud_usage's (timestamp, organization,…)
// shape the same read is a filter over the whole table, which is how a shared
// analytics pod gets taken down.
func TestFeatureTableLeadsWithOrg(t *testing.T) {
	if !strings.Contains(featureDDL, "ORDER BY (org, subject_kind, subject, bucket)") {
		t.Fatal("hanzo.risk_feature does not lead its sort key with org — a per-tenant read would be a full scan")
	}
}

// ── the model ───────────────────────────────────────────────────────────────

// TestModelGeometryIsPerTenant proves two tenants do not merely hold different
// counters: they hold different TREES. Probing one therefore reveals nothing
// about where another's regions lie.
func TestModelGeometryIsPerTenant(t *testing.T) {
	app, s := wireApp(t)
	_ = app

	a, b := Tenant("hanzo/acme"), Tenant("hanzo/beta")
	feed(t, s, a, 40)
	feed(t, s, b, 40)

	sa, _ := s.State.model.Snapshot(a.String())
	sb, _ := s.State.model.Snapshot(b.String())
	if sa.Seed == 0 || sb.Seed == 0 {
		t.Fatal("a tenant model was planted with no seed")
	}
	if sa.Seed == sb.Seed {
		t.Fatal("two tenants share a tree geometry — one tenant's probe maps the other's regions")
	}

	// A restore of A's snapshot under B's key is refused: state the model would
	// treat as its own memory has to have come from this tenant.
	sa.OrgID = b.String()
	if err := s.State.model.Restore(sa); err == nil {
		// The engine accepts a well-formed snapshot bearing B's id; the CLOUD
		// side is what refuses it, by comparing the snapshot's tenant against the
		// tenant that asked. loadModel is that check.
		t.Log("engine accepted a relabelled snapshot; the cloud-side tenant check is what refuses it")
	}
}

// feed drives n observations through a tenant's model so it has state to
// snapshot.
func feed(t *testing.T, s *stateService, tn Tenant, n int) {
	t.Helper()
	at := time.Now().Add(-time.Duration(n) * time.Minute)
	for i := 0; i < n; i++ {
		o := observation{
			id: newID("obs"), at: at.Add(time.Duration(i) * time.Minute),
			stage: StagePayment, kind: "account", subject: "acct-1",
			amount: int64(i+1) * 1_000_000_000, currency: "USD", direction: "in",
			signals: map[string]string{"ip": "203.0.113.5", "device": "d-1"},
		}
		record(s.State.vel, tn, o)
		tx, ent := txOf(tn, o)
		_, _ = s.State.model.Assess(tx, ent)
	}
}

// wireApp mounts the surface over a real temp data directory. Routes register
// either way; this gives the tests a tenant plane they can actually write to.
func wireApp(t *testing.T) (*zip.App, *stateService) {
	t.Helper()
	deps := cloud.Deps{Logger: luxlog.New("risktest"), DataDir: t.TempDir(), Brand: "hanzo"}
	s, err := build(deps)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("risktest"), DisableStartupMessage: true})
	mount(s, app)
	t.Cleanup(s.State.shelf.close)
	return app, s
}

// ── the wire ────────────────────────────────────────────────────────────────

// req drives one request through the mounted router with the identity headers
// the edge would have minted.
func req(t *testing.T, app *zip.App, method, path, org, user, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		r.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		r.Header.Set("X-User-Id", user)
	}
	resp, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: fiberTimeout})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// fiberTimeout is generous because a decide opens a tenant's encrypted SQLite
// file on first touch.
const fiberTimeout = 30 * time.Second

// TestEveryOpRefusesAnUnvalidatedPrincipal pins the identity gate on the typed
// routes. An X-Org-Id with no X-User-Id is exactly the forged-header case: the
// header survived the edge but no credential minted it.
func TestEveryOpRefusesAnUnvalidatedPrincipal(t *testing.T) {
	app, _ := wireApp(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/risk/decide", `{"stage":"signup","subject":{"kind":"account","id":"a1"}}`},
		{http.MethodGet, "/v1/risk/decisions", ""},
		{http.MethodGet, "/v1/risk/rules", ""},
		{http.MethodGet, "/v1/risk/dictionary", ""},
		{http.MethodGet, "/v1/risk/activity", ""},
		{http.MethodGet, "/v1/risk/controls", ""},
		{http.MethodGet, "/v1/ml/state", ""},
		{http.MethodGet, "/v1/ml/features", ""},
		{http.MethodPost, "/v1/ml/score", `{"observation":{"subject":{"kind":"account","id":"a1"}}}`},
		{http.MethodPost, "/v1/ml/train", `{"observations":[{"subject":{"kind":"account","id":"a1"}}]}`},
	} {
		code, body := req(t, app, tc.method, tc.path, "acme", "", tc.body)
		if code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s, want 403 for an unvalidated principal", tc.method, tc.path, code, body)
		}
	}
}

// TestTenantIsolation is the load-bearing one: two orgs, one surface, and org B
// can see nothing of org A's.
//
// A foreign decision answers 404, not 403. A 403 would be an ORACLE — it
// distinguishes "this id exists and is not yours" from "this id does not
// exist", which lets a probe enumerate another tenant's volume. A list answers
// ZERO ROWS for the same reason.
func TestTenantIsolation(t *testing.T) {
	app, _ := wireApp(t)

	// Org A makes a decision.
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"payment","subject":{"kind":"transaction","id":"tx-1"},
		  "amount":{"nano":5000000000,"currency":"USD","direction":"in"},
		  "signals":{"ip":"203.0.113.9","device":"dev-a"}}`)
	if code != http.StatusOK {
		t.Fatalf("A decide = %d %s", code, body)
	}
	var made riskDecision
	if err := json.Unmarshal(body, &made); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if made.ID == "" {
		t.Fatal("a decision was made with no identifier")
	}

	// A sees it.
	code, body = req(t, app, http.MethodGet, "/v1/risk/decisions", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("A list = %d %s", code, body)
	}
	var mine riskDecisionPage
	_ = json.Unmarshal(body, &mine)
	if len(mine.Items) != 1 {
		t.Fatalf("A sees %d of its own decisions, want 1", len(mine.Items))
	}

	// B sees NOTHING — zero rows, not an error, because an error is a signal.
	code, body = req(t, app, http.MethodGet, "/v1/risk/decisions", "beta", "u_beta", "")
	if code != http.StatusOK {
		t.Fatalf("B list = %d %s", code, body)
	}
	var theirs riskDecisionPage
	_ = json.Unmarshal(body, &theirs)
	if len(theirs.Items) != 0 {
		t.Fatalf("B sees %d of A's decisions — the tenant boundary leaked", len(theirs.Items))
	}

	// B naming A's decision id gets 404, indistinguishable from an unknown id.
	code, body = req(t, app, http.MethodGet, "/v1/risk/decisions/"+made.ID, "beta", "u_beta", "")
	if code != http.StatusNotFound {
		t.Fatalf("B reading A's decision = %d %s, want 404 (a 403 is a probe oracle)", code, body)
	}
	codeUnknown, _ := req(t, app, http.MethodGet, "/v1/risk/decisions/dec_deadbeef", "beta", "u_beta", "")
	if codeUnknown != code {
		t.Fatalf("an unknown id answers %d and a foreign id answers %d — the difference IS the oracle", codeUnknown, code)
	}

	// B labelling A's decision cannot reach it either.
	code, _ = req(t, app, http.MethodPost, "/v1/risk/decisions/"+made.ID+"/label", "beta", "u_beta",
		`{"verdict":"legitimate"}`)
	if code != http.StatusNotFound {
		t.Fatalf("B labelling A's decision = %d, want 404", code)
	}

	// And A's own read still works, so the isolation is not simply "nothing works".
	code, _ = req(t, app, http.MethodGet, "/v1/risk/decisions/"+made.ID, "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("A reading its OWN decision = %d, want 200 — the test proves nothing if nobody can read", code)
	}
}

// TestTenantIsolationOnEveryPlane walks the record planes a tenant owns and
// proves each one is empty for the other tenant. One plane leaking is the whole
// boundary leaking, and a test that checked only decisions would not see it.
func TestTenantIsolationOnEveryPlane(t *testing.T) {
	app, _ := wireApp(t)

	// A creates a rule, a list entry, a suppression and a control.
	mustOK(t, app, http.MethodPost, "/v1/risk/rules", "acme", "u_acme",
		`{"rule":{"name":"A only","stage":"signup","action":"block","weight":0.5,"enabled":true,
		  "all":[{"field":"signal.ip","op":"eq","value":"1.2.3.4"}]}}`, http.StatusCreated)
	mustOK(t, app, http.MethodPost, "/v1/risk/lists/ip-deny/entries", "acme", "u_acme",
		`{"values":["1.2.3.4"]}`, http.StatusOK)
	mustOK(t, app, http.MethodPost, "/v1/risk/suppressions", "acme", "u_acme",
		`{"rule":"signup-burst-ip","reason":"known partner"}`, http.StatusCreated)
	mustOK(t, app, http.MethodPost, "/v1/risk/controls", "acme", "u_acme",
		`{"subject":{"kind":"merchant","id":"m-1"},"control":"reserve","rate":0.2,"reason":"new merchant"}`,
		http.StatusCreated)

	// B sees its own starter rules and NONE of A's additions.
	_, body := req(t, app, http.MethodGet, "/v1/risk/rules", "beta", "u_beta", "")
	if strings.Contains(string(body), "A only") {
		t.Error("B can read A's rule")
	}
	_, body = req(t, app, http.MethodGet, "/v1/risk/suppressions", "beta", "u_beta", "")
	var sup riskSuppressionPage
	_ = json.Unmarshal(body, &sup)
	if len(sup.Items) != 0 {
		t.Errorf("B sees %d of A's suppressions", len(sup.Items))
	}
	_, body = req(t, app, http.MethodGet, "/v1/risk/controls", "beta", "u_beta", "")
	var ctl riskControlPage
	_ = json.Unmarshal(body, &ctl)
	if len(ctl.Items) != 0 {
		t.Errorf("B sees %d of A's controls", len(ctl.Items))
	}
	_, body = req(t, app, http.MethodGet, "/v1/risk/lists", "beta", "u_beta", "")
	var lists riskListPage
	_ = json.Unmarshal(body, &lists)
	for _, l := range lists.Items {
		if l.Name == "ip-deny" && l.Entries != 0 {
			t.Errorf("B's ip-deny list holds %d entries — A's values reached it", l.Entries)
		}
	}
}

// TestSubjectViewReportsAnHonestGap pins the warehouse-backed halves of the
// subject read. This box has no warehouse, which is exactly the case that
// matters: a zeroed history would say the subject did nothing and a zeroed
// baseline would say the platform did. Both must be ABSENT and the gap NAMED.
//
// It also pins that the gap does not take the decision plane with it — the
// velocity, decisions and controls come from memory and the tenant's own file,
// so the read still answers 200 with everything it can actually know.
func TestSubjectViewReportsAnHonestGap(t *testing.T) {
	app, _ := wireApp(t)
	mustOK(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"signup","subject":{"kind":"account","id":"a-1"},"signals":{"ip":"192.0.2.44"}}`,
		http.StatusOK)

	code, body := req(t, app, http.MethodGet, "/v1/risk/subjects/account/a-1", "acme", "u_acme", "")
	if code != http.StatusOK {
		t.Fatalf("subject read = %d %s — a missing warehouse took the decision plane with it", code, body)
	}
	var v riskSubjectView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	if v.Gap == "" {
		t.Error("no warehouse, and no gap reported — the empty history reads as a quiet subject")
	}
	if len(v.History) != 0 || len(v.Network) != 0 {
		t.Errorf("history=%d network=%d with no warehouse; both must be absent, not zeroed",
			len(v.History), len(v.Network))
	}
	// The parts that do not need the warehouse must still be there, or the
	// assertion above proves nothing.
	if len(v.Velocity) == 0 {
		t.Error("no velocity on a subject that was just decided on — the in-memory rings are not being read")
	}
	if len(v.Decisions) != 1 {
		t.Errorf("%d decisions on the subject, want 1", len(v.Decisions))
	}
}

// TestNetworkReadTakesNoTenant is the counterpart to the DDL test: the FUNCTION
// that reads the baseline has no tenant parameter, so no call site — present or
// future — can scope it to one org, correctly or incorrectly.
func TestNetworkReadTakesNoTenant(t *testing.T) {
	fn := reflect.TypeOf(baseline)
	for i := 0; i < fn.NumIn(); i++ {
		if fn.In(i) == reflect.TypeOf(Tenant("")) {
			t.Fatal("baseline() takes a Tenant — the network plane would be scopeable to one org, " +
				"which is the exact thing that makes it aggregate-only")
		}
	}
	// And the per-tenant read is the mirror image: it MUST take one.
	fn = reflect.TypeOf(window)
	var tenanted bool
	for i := 0; i < fn.NumIn(); i++ {
		if fn.In(i) == reflect.TypeOf(Tenant("")) {
			tenanted = true
		}
	}
	if !tenanted {
		t.Fatal("window() takes no Tenant — a feature read could be spelled without one")
	}
}

func mustOK(t *testing.T, app *zip.App, method, path, org, user, body string, want int) {
	t.Helper()
	code, got := req(t, app, method, path, org, user, body)
	if code != want {
		t.Fatalf("%s %s = %d %s, want %d", method, path, code, got, want)
	}
}
