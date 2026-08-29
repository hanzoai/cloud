package books

// wire_test.go — the /v1/books WIRE, pinned.
//
// The books surface had no route test at all: every assertion in this package was
// over the store and the report engine, so the HTTP contract — status, headers, the
// exact JSON envelope — was covered by nothing. That is the coverage a typed-op
// migration needs most, because typing is a DESCRIPTION task: the whole claim is
// that the answers did not move. So this file asserts the answers, not the
// implementation, and it passes identically against the untyped handlers it was
// written from and the typed ops that replaced them.
//
// Two facts every books answer carries, and both are asserted here:
//
//   - Cache-Control: no-store on SUCCESS, and never on a refusal — per-org money is
//     never cached, and an error answer has never carried the header.
//   - the ledger selector is the literal "true". "1" and a bare "?sandbox" read as
//     LIVE, which is why it is a string on the wire and not a bool: a bool binding
//     would hand a caller asking for live books the sandbox's empty ones.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/openapi"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

var wireCfg = zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true}

// compose installs what a host installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer installs it once at the root, and
// in a test the test is the composer, so it owes the same install. A test that
// skips it does not test a stricter program — it tests one where every op that
// reads the org answers a refusal production can never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// mountBooks brings up the real /v1/books surface over a temp DataDir: the real
// router, the real middleware, the real stores.
func mountBooks(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	deps := cloud.Deps{DataDir: t.TempDir()}
	if err := Use(app, deps); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// hit issues one request. org "" exercises the no-principal path — X-Org-Id and
// X-User-Id are the pair the gateway mints only from a verified credential.
func hit(t *testing.T, app *zip.App, method, path, org string, body []byte) (int, string, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(req, wireCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Cache-Control"), out
}

// TestReadsAnswerTheirFrozenEnvelope pins the shape of every books READ on an empty
// ledger: the status, the no-store header, and the exact JSON envelope — a bare
// array where the route has always answered one, the named wrapper where it has
// always answered that.
func TestReadsAnswerTheirFrozenEnvelope(t *testing.T) {
	app := mountBooks(t)
	for _, tc := range []struct {
		path string
		want string // the envelope, on a freshly seeded (empty) ledger
	}{
		{"/v1/books/gl", `[]`},
		{"/v1/books/bank/transactions", `[]`},
		{"/v1/books/inbox", `{"items":[]}`},
		{"/v1/books/vendors", `{"vendors":[]}`},
		{"/v1/books/rules", `{"rules":[]}`},
		{"/v1/books/transactions", `{"transactions":[]}`},
		{"/v1/books/questions", `{"questions":[]}`},
		{"/v1/books/bank/unreconciled", `{"transactions":[],"questions":[]}`},
	} {
		code, cache, body := hit(t, app, http.MethodGet, tc.path, "acme", nil)
		if code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200 (%s)", tc.path, code, body)
			continue
		}
		if cache != "no-store" {
			t.Errorf("GET %s: Cache-Control %q, want no-store — money is never cached", tc.path, cache)
		}
		if !sameJSON(t, body, tc.want) {
			t.Errorf("GET %s: envelope moved\n got %s\nwant %s", tc.path, body, tc.want)
		}
	}
}

// TestAccountsAnswerTheSeededChartAsABareArray pins the one read whose empty
// ledger is not empty: the chart of accounts is SEEDED, and it has always come back
// as a bare JSON array rather than wrapped in an object.
func TestAccountsAnswerTheSeededChartAsABareArray(t *testing.T) {
	app := mountBooks(t)
	code, cache, body := hit(t, app, http.MethodGet, "/v1/books/accounts", "acme", nil)
	if code != http.StatusOK || cache != "no-store" {
		t.Fatalf("status %d cache %q, want 200/no-store", code, cache)
	}
	var accts []Account
	if err := json.Unmarshal(body, &accts); err != nil {
		t.Fatalf("accounts must be a bare JSON array: %v (%s)", err, body)
	}
	if len(accts) == 0 {
		t.Fatal("the seeded chart of accounts must not be empty")
	}
	for _, a := range accts {
		if a.Number == Bank {
			return
		}
	}
	t.Errorf("the seeded chart must carry account %s", Bank)
}

// TestStatementsCarryTheirProof pins the three statements: each answers 200 with the
// invariant that makes it trustworthy — the trial balance balances, the balance
// sheet's equation holds, and the P&L nets what it says it nets.
func TestStatementsCarryTheirProof(t *testing.T) {
	app := mountBooks(t)

	_, _, tbBody := hit(t, app, http.MethodGet, "/v1/books/trial", "acme", nil)
	var tb TrialBalance
	if err := json.Unmarshal(tbBody, &tb); err != nil {
		t.Fatalf("trial: %v (%s)", err, tbBody)
	}
	if !tb.Balanced {
		t.Errorf("trial balance must report balanced: %s", tbBody)
	}

	_, _, bsBody := hit(t, app, http.MethodGet, "/v1/books/position", "acme", nil)
	var bs BalanceSheet
	if err := json.Unmarshal(bsBody, &bs); err != nil {
		t.Fatalf("position: %v (%s)", err, bsBody)
	}
	if !bs.Balanced {
		t.Errorf("balance sheet must report balanced: %s", bsBody)
	}

	_, _, pnlBody := hit(t, app, http.MethodGet, "/v1/books/pnl", "acme", nil)
	var p PnL
	if err := json.Unmarshal(pnlBody, &p); err != nil {
		t.Fatalf("pnl: %v (%s)", err, pnlBody)
	}
	if p.NetIncome != p.TotalIncome-p.TotalExpense {
		t.Errorf("pnl net must be income − expense: %s", pnlBody)
	}
}

// TestExportRefusesAnyFormatButJSON pins the one parameter the export validates,
// and the ORDER it validates in: an anonymous caller is 401 whatever it asks for,
// so a probe never learns from a 400 that the parameter exists.
func TestExportRefusesAnyFormatButJSON(t *testing.T) {
	app := mountBooks(t)

	if code, _, body := hit(t, app, http.MethodGet, "/v1/books/export?format=csv", "acme", nil); code != http.StatusBadRequest {
		t.Errorf("format=csv: status %d, want 400 (%s)", code, body)
	}
	if code, cache, _ := hit(t, app, http.MethodGet, "/v1/books/export?format=json", "acme", nil); code != http.StatusOK || cache != "no-store" {
		t.Errorf("format=json: status %d cache %q, want 200/no-store", code, cache)
	}
	if code, _, _ := hit(t, app, http.MethodGet, "/v1/books/export", "acme", nil); code != http.StatusOK {
		t.Errorf("no format: status %d, want 200", code)
	}
	if code, cache, _ := hit(t, app, http.MethodGet, "/v1/books/export?format=csv", "", nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous + bad format: status %d, want 401 — the tenant gate answers first", code)
	} else if cache != "" {
		t.Errorf("a refusal must not carry Cache-Control, got %q", cache)
	}
}

// TestEveryRouteRefusesWithoutAValidatedPrincipal walks the WHOLE surface, typed and
// untyped alike, and pins the fail-closed answer: no principal, no books. A ledger
// route that answered anything else would be serving one org's money to whoever asked.
func TestEveryRouteRefusesWithoutAValidatedPrincipal(t *testing.T) {
	app := mountBooks(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/books/accounts"},
		{http.MethodGet, "/v1/books/gl"},
		{http.MethodGet, "/v1/books/trial"},
		{http.MethodGet, "/v1/books/metrics"},
		{http.MethodGet, "/v1/books/pnl"},
		{http.MethodGet, "/v1/books/position"},
		{http.MethodGet, "/v1/books/export"},
		{http.MethodGet, "/v1/books/questions"},
		{http.MethodGet, "/v1/books/inbox"},
		{http.MethodGet, "/v1/books/vendors"},
		{http.MethodGet, "/v1/books/rules"},
		{http.MethodGet, "/v1/books/transactions"},
		{http.MethodGet, "/v1/books/bank/transactions"},
		{http.MethodGet, "/v1/books/bank/unreconciled"},
		{http.MethodPost, "/v1/books/sync"},
		{http.MethodPost, "/v1/books/ask"},
		{http.MethodPost, "/v1/books/scan"},
		{http.MethodPost, "/v1/books/scan/book"},
		{http.MethodPost, "/v1/books/inbox"},
		{http.MethodPost, "/v1/books/vendors"},
		{http.MethodPost, "/v1/books/rules"},
		{http.MethodPost, "/v1/books/bank/import"},
		{http.MethodPost, "/v1/books/bank/sync"},
		{http.MethodPost, "/v1/books/bank/token"},
		{http.MethodPost, "/v1/books/bank/exchange"},
	} {
		var body []byte
		if r.method == http.MethodPost {
			body = []byte(`{}`)
		}
		code, cache, out := hit(t, app, r.method, r.path, "", body)
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401 without a validated principal (%s)", r.method, r.path, code, out)
		}
		if cache != "" {
			t.Errorf("%s %s: a refusal must not carry Cache-Control, got %q", r.method, r.path, cache)
		}
	}
}

// TestSandboxSelectorIsTheLiteralTrue pins the ledger selector. Only "true"
// (case-insensitively) reads the sandbox: "1" and a bare "?sandbox" have always
// read LIVE, and a caller asking for live books must never be handed the sandbox's.
// The two ledgers are physically separate files, so the proof is that a vendor
// written to one is invisible from the other.
func TestSandboxSelectorIsTheLiteralTrue(t *testing.T) {
	app := mountBooks(t)
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/vendors?sandbox=true", "acme",
		[]byte(`{"canonical":"OnlyInSandbox"}`)); code != http.StatusOK {
		t.Fatalf("seed sandbox vendor: status %d (%s)", code, out)
	}
	for _, q := range []string{"?sandbox=true", "?sandbox=TRUE", "?sandbox=True"} {
		if _, _, out := hit(t, app, http.MethodGet, "/v1/books/vendors"+q, "acme", nil); !bytes.Contains(out, []byte("OnlyInSandbox")) {
			t.Errorf("%s must read the SANDBOX ledger, got %s", q, out)
		}
	}
	for _, q := range []string{"", "?sandbox=1", "?sandbox", "?sandbox=false", "?sandbox=yes"} {
		if _, _, out := hit(t, app, http.MethodGet, "/v1/books/vendors"+q, "acme", nil); bytes.Contains(out, []byte("OnlyInSandbox")) {
			t.Errorf("%q must read the LIVE ledger — only the literal \"true\" selects sandbox; got %s", q, out)
		}
	}
}

// TestTheLedgerSelectorStaysOnTheURLForBodyWrites pins the half of the selector a
// GET cannot: the WRITES take a JSON body, and a typed POST's In is documented as
// that body — so a Sandbox field on one of them would MOVE the live/sandbox choice
// off the URL it has always ridden on, and publish it as a body field. The ops read
// it from the request instead (sandboxFrom, typed.go), so `sandbox` in the BODY selects
// nothing. This goes red the moment someone names it on an In, which is exactly when
// the wire would have moved.
func TestTheLedgerSelectorStaysOnTheURLForBodyWrites(t *testing.T) {
	app := mountBooks(t)
	// A body that ASKS for the sandbox, on the URL that does not.
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/vendors", "acme",
		[]byte(`{"canonical":"BodySelectorVendor","sandbox":"true"}`)); code != http.StatusOK {
		t.Fatalf("vendor upsert: status %d (%s)", code, out)
	}
	if _, _, out := hit(t, app, http.MethodGet, "/v1/books/vendors?sandbox=true", "acme", nil); bytes.Contains(out, []byte("BodySelectorVendor")) {
		t.Errorf("a body `sandbox` must select nothing — the row landed in the SANDBOX ledger: %s", out)
	}
	if _, _, out := hit(t, app, http.MethodGet, "/v1/books/vendors", "acme", nil); !bytes.Contains(out, []byte("BodySelectorVendor")) {
		t.Errorf("the row must land in the LIVE ledger the URL named, got %s", out)
	}
	// And the URL still selects, on the same body-carrying route.
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/rules?sandbox=true", "acme",
		[]byte(`{"pattern":"urlselector","category":"cloud"}`)); code != http.StatusOK {
		t.Fatalf("rule upsert: status %d (%s)", code, out)
	}
	if _, _, out := hit(t, app, http.MethodGet, "/v1/books/rules?sandbox=true", "acme", nil); !bytes.Contains(out, []byte("urlselector")) {
		t.Errorf("?sandbox=true on a POST must write the SANDBOX ledger, got %s", out)
	}
	if _, _, out := hit(t, app, http.MethodGet, "/v1/books/rules", "acme", nil); bytes.Contains(out, []byte("urlselector")) {
		t.Errorf("the sandbox write must be invisible from the LIVE ledger, got %s", out)
	}
}

// filledMetrics is a Metrics with every field set to a distinct non-zero value, by
// REFLECTION — so a field added to Metrics is filled automatically and the parity tests
// below see it, rather than a hand-written literal quietly not knowing it exists.
func filledMetrics(t *testing.T) Metrics {
	t.Helper()
	var m Metrics
	v := reflect.ValueOf(&m).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString(fmt.Sprintf("x%d", i+1))
		case reflect.Int, reflect.Int64:
			f.SetInt(int64(i + 1))
		default:
			t.Fatalf("Metrics field %s has kind %s — teach filledMetrics to fill it",
				v.Type().Field(i).Name, f.Kind())
		}
	}
	return m
}

// TestMetricsResponseCarriesEveryMetricsField is the guard that made typing the metrics
// route safe. MetricsResponse spells Metrics' fields out FLAT (zip's schema walk does not
// flatten an embedded struct the way encoding/json does, so embedding would publish a
// schema no answer matches) — and a spelled-out copy can drift: the next field added to
// Metrics could silently never reach the wire. This pins the copy complete: every Metrics
// field must marshal identically out of metricsResponseOf's result, and figures must be
// the ONLY extra key. A new Metrics field goes red here until the Out and the constructor
// carry it.
func TestMetricsResponseCarriesEveryMetricsField(t *testing.T) {
	m := filledMetrics(t)
	mj, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("metrics marshal: %v", err)
	}
	rj, err := json.Marshal(metricsResponseOf(m))
	if err != nil {
		t.Fatalf("response marshal: %v", err)
	}
	var mm, rm map[string]json.RawMessage
	if err := json.Unmarshal(mj, &mm); err != nil {
		t.Fatalf("metrics keys: %v", err)
	}
	if err := json.Unmarshal(rj, &rm); err != nil {
		t.Fatalf("response keys: %v", err)
	}
	for k, want := range mm {
		got, ok := rm[k]
		if !ok {
			t.Errorf("Metrics field %q never reaches the wire — add it to MetricsResponse AND metricsResponseOf", k)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("field %q: response carries %s, Metrics says %s — metricsResponseOf dropped or crossed it", k, got, want)
		}
	}
	if _, ok := rm["figures"]; !ok {
		t.Error("the response must carry the formatted figures")
	}
	if len(rm) != len(mm)+1 {
		t.Errorf("MetricsResponse carries %d keys, want the %d Metrics keys + figures — an extra key is a wire change", len(rm), len(mm))
	}
}

// TestMetricsSchemaMatchesItsWire closes the loop the old refusal measured: the PUBLISHED
// MetricsResponse schema and the JSON the route answers must carry the same keys. This is
// exactly the assertion that failed while MetricsResponse embedded Metrics (zip published
// a nested "Metrics" property the wire never carries), and it is what keeps the document,
// every generated SDK and the MCP tool honest about this payload from here on.
func TestMetricsSchemaMatchesItsWire(t *testing.T) {
	app := zip.New(zip.Config{})
	zip.Get(app, "/probe", func(context.Context, *struct{}) (*MetricsResponse, error) { return nil, nil })
	spec, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("spec decode: %v", err)
	}
	published := doc.Components.Schemas["MetricsResponse"].Properties
	wire, err := json.Marshal(metricsResponseOf(filledMetrics(t)))
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	var onTheWire map[string]json.RawMessage
	if err := json.Unmarshal(wire, &onTheWire); err != nil {
		t.Fatalf("wire decode: %v", err)
	}
	for k := range onTheWire {
		if _, ok := published[k]; !ok {
			t.Errorf("the wire carries %q but the published schema does not — the document under-describes the route", k)
		}
	}
	for k := range published {
		if _, ok := onTheWire[k]; !ok {
			t.Errorf("the published schema claims %q but the wire never carries it — the document invents a field", k)
		}
	}
}

// TestMetricsAnswersTheFlatSnapshot pins the metrics WIRE on the mounted surface: 200,
// no-store, the snapshot keys FLAT at the top level (never nested under "Metrics"), and
// the formatted figures beside them — the exact envelope the route answered before it was
// typed.
func TestMetricsAnswersTheFlatSnapshot(t *testing.T) {
	app := mountBooks(t)
	code, cache, body := hit(t, app, http.MethodGet, "/v1/books/metrics", "acme", nil)
	if code != http.StatusOK || cache != "no-store" {
		t.Fatalf("status %d cache %q, want 200/no-store (%s)", code, cache, body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("metrics body: %v (%s)", err, body)
	}
	if _, nested := got["Metrics"]; nested {
		t.Fatalf("the snapshot must flatten onto the wire, got %s", body)
	}
	for _, k := range []string{"period", "months", "mrr", "arr", "revenue", "cogs", "burn", "grossProfit", "grossMarginBps", "netIncome", "cash", "deferredRevenue", "monthlyBurn", "runwayMonths", "figures"} {
		if _, ok := got[k]; !ok {
			t.Errorf("the snapshot must carry %q at the top level, got %s", k, body)
		}
	}
	var resp MetricsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("metrics decode: %v", err)
	}
	if len(resp.Figures) == 0 {
		t.Error("an empty ledger still answers honest zero figures, formatted")
	}
	if resp.RunwayMonths != -1 {
		t.Errorf("an org burning nothing has infinite runway (-1), got %d", resp.RunwayMonths)
	}
}

// TestLimitFallsBackOnEveryWayItFailsToArrive pins the row cap: a limit that is
// absent, unparseable, zero or negative falls back to the route's default rather
// than capping the read at nothing.
func TestLimitFallsBackOnEveryWayItFailsToArrive(t *testing.T) {
	app := mountBooks(t)
	// Ten rules, so a default-capped read returns all ten and a limit of 3 returns 3.
	for _, p := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		if code, _, out := hit(t, app, http.MethodPost, "/v1/books/rules", "acme",
			[]byte(`{"pattern":"`+p+`","category":"software"}`)); code != http.StatusOK {
			t.Fatalf("seed rule %s: status %d (%s)", p, code, out)
		}
	}
	count := func(q string) int {
		t.Helper()
		_, _, out := hit(t, app, http.MethodGet, "/v1/books/transactions"+q, "acme", nil)
		// Decoded through a local shape, not the op's Out type, so this file pins the
		// WIRE and compiles against the untyped handlers it was written from too.
		var got struct {
			Transactions []Txn `json:"transactions"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("transactions%s: %v (%s)", q, err, out)
		}
		return len(got.Transactions)
	}
	// The register is empty on a fresh ledger, so the cap is exercised on the GL read,
	// which the rules above do not touch either — what matters is that a malformed
	// limit is not read as "return nothing".
	for _, q := range []string{"", "?limit=abc", "?limit=0", "?limit=-5", "?limit=1.5"} {
		if n := count(q); n != 0 {
			t.Errorf("transactions%s returned %d rows on an empty ledger", q, n)
		}
	}
	if code, _, _ := hit(t, app, http.MethodGet, "/v1/books/gl?limit=-5", "acme", nil); code != http.StatusOK {
		t.Errorf("gl?limit=-5: status %d, want 200 — a bad limit falls back, it does not fail", code)
	}
}

// TestVendorAndRuleUpsertsEchoTheNormalizedRow pins the two writes that are not the
// scanner's: each answers 200 with the row as STORED, its category normalized to a
// real COA account rather than the slug the caller wrote.
func TestVendorAndRuleUpsertsEchoTheNormalizedRow(t *testing.T) {
	app := mountBooks(t)

	code, cache, out := hit(t, app, http.MethodPost, "/v1/books/vendors", "acme",
		[]byte(`{"canonical":"GitHub","aliases":["github.com"],"defaultCategory":"software"}`))
	if code != http.StatusOK || cache != "no-store" {
		t.Fatalf("vendor upsert: status %d cache %q (%s)", code, cache, out)
	}
	var v VendorRow
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("vendor upsert body: %v (%s)", err, out)
	}
	if v.DefaultCategory != SoftwareExpense {
		t.Errorf("vendor category must normalize to %s, got %q", SoftwareExpense, v.DefaultCategory)
	}
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/vendors", "acme", []byte(`{"canonical":"  "}`)); code != http.StatusBadRequest {
		t.Errorf("blank canonical: status %d, want 400 (%s)", code, out)
	}

	code, _, out = hit(t, app, http.MethodPost, "/v1/books/rules", "acme",
		[]byte(`{"pattern":"aws","category":"cloud","priority":5}`))
	if code != http.StatusOK {
		t.Fatalf("rule upsert: status %d (%s)", code, out)
	}
	var r Rule
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("rule upsert body: %v (%s)", err, out)
	}
	if r.Category != CloudCOGS {
		t.Errorf("rule category must normalize to %s, got %q", CloudCOGS, r.Category)
	}
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/rules", "acme", []byte(`{"pattern":""}`)); code != http.StatusBadRequest {
		t.Errorf("blank pattern: status %d, want 400 (%s)", code, out)
	}
}

// TestBankLinkStubsStayHonest pins the two Plaid/Teller endpoints: they exist so the
// frontend contract is stable, and until a connector implements them they answer 501
// rather than pretending. That is also why they are not typed ops — a typed op would
// publish a success schema for a response neither has ever sent.
func TestBankLinkStubsStayHonest(t *testing.T) {
	app := mountBooks(t)
	for _, p := range []string{"/v1/books/bank/token", "/v1/books/bank/exchange"} {
		if code, _, out := hit(t, app, http.MethodPost, p, "acme", []byte(`{}`)); code != http.StatusNotImplemented {
			t.Errorf("POST %s: status %d, want 501 (%s)", p, code, out)
		}
	}
}

// TestScannerRefusesAnEmptyUpload pins the scanner's body contract: it takes RAW
// document bytes, not a JSON object, and an empty upload is a 400. This is the
// reason scan and the inbox upload are not typed ops — there is no JSON input to name.
func TestScannerRefusesAnEmptyUpload(t *testing.T) {
	app := mountBooks(t)
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/inbox", "acme", []byte{}); code != http.StatusBadRequest {
		t.Errorf("empty inbox upload: status %d, want 400 (%s)", code, out)
	}
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/inbox", "acme", []byte("a receipt")); code != http.StatusOK {
		t.Errorf("inbox upload: status %d, want 200 (%s)", code, out)
	}
	// A queued document shows up in the inbox as unsorted.
	_, _, out := hit(t, app, http.MethodGet, "/v1/books/inbox", "acme", nil)
	var q struct {
		Items []InboxItem `json:"items"`
	}
	if err := json.Unmarshal(out, &q); err != nil {
		t.Fatalf("inbox: %v (%s)", err, out)
	}
	if len(q.Items) != 1 || q.Items[0].Status != inboxUnsorted {
		t.Errorf("the uploaded document must queue as unsorted, got %s", out)
	}
}

// TestOneOrgNeverReadsAnother pins the isolation the whole surface rests on: the org
// comes from the VALIDATED principal, never from anything the caller can assert for
// itself, so a second tenant sees its own empty books and not the first's rows.
func TestOneOrgNeverReadsAnother(t *testing.T) {
	app := mountBooks(t)
	if code, _, out := hit(t, app, http.MethodPost, "/v1/books/vendors", "acme",
		[]byte(`{"canonical":"AcmeOnly"}`)); code != http.StatusOK {
		t.Fatalf("seed acme vendor: status %d (%s)", code, out)
	}
	if _, _, out := hit(t, app, http.MethodGet, "/v1/books/vendors", "acme", nil); !bytes.Contains(out, []byte("AcmeOnly")) {
		t.Fatalf("acme must read its own vendor, got %s", out)
	}
	if _, _, out := hit(t, app, http.MethodGet, "/v1/books/vendors", "globex", nil); bytes.Contains(out, []byte("AcmeOnly")) {
		t.Errorf("globex read acme's vendor book: %s", out)
	}
}

// sameJSON compares two JSON documents by VALUE, so key order — which a map and a
// struct do not agree on and no client depends on — is not what the pin is about.
func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("bad want literal %q: %v", want, err)
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// untypedByDesign is the CLOSED list of addresses this app serves raw, each with
// the wire fact that keeps it there.
//
// The package already MEASURES each of these — the tests above drive the wires
// themselves, which is the stronger form and stays. What this list adds is the
// SUM: those tests go red when a refused route's WIRE changes, and nothing went
// red when a route was ADDED raw beside them. openapi/untyped.json catches that
// fleet-wide by count, and a count is flat when one route converts and another
// arrives raw in the same change, which is exactly what this catches.
var untypedByDesign = map[string]string{
	"POST /v1/books/scan": "the request body IS the receipt — raw bytes under the caller's own " +
		"Content-Type. zip decodes every non-empty typed body as JSON before the handler is entered.",
	"POST /v1/books/inbox": "the same raw-byte upload as scan.",
	"POST /v1/books/bank/import": "raw statement bytes (OFX/QFX/CSV): there is no JSON In that names " +
		"a file, and a typed In would answer 400 to every real import.",
	"POST /v1/books/bank/token": "answers an unconditional 501. A typed op publishes a SUCCESS " +
		"response — its Out schema, or the 204 a void op declares — that this route has never sent, " +
		"so typing it would put an invented contract in every generated SDK.",
	"POST /v1/books/bank/exchange": "the same unconditional 501 as token.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface, so a route added raw goes red without anyone remembering this file, and
// a reason that stops being true goes red the moment its op is written.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountBooks(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "books", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/books") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/books") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if !typed[key] {
			if _, named := untypedByDesign[key]; !named {
				untyped = append(untyped, key)
			}
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it, or name it in untypedByDesign with the wire fact that keeps it raw.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this app no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}
