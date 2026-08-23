package destination

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// report_test.go drives the inbound plane end to end against httptest platforms.
// No test reaches a real API and no test stands a warehouse up: the platform is a
// mock server pointed at through the same package vars the outbound adapters use,
// and the warehouse is the two func vars record writes through.

// ── clients ────────────────────────────────────────────────────────────────────

// warehouse captures what record wrote, so a test asserts the STATEMENT and the
// bound values rather than a row it would need a columnar store to read back.
type warehouse struct {
	stmts []string
	args  [][]any
}

func fakeWarehouse(t *testing.T, ready bool) *warehouse {
	t.Helper()
	w := &warehouse{}
	oldReady, oldExec := warehouseReady, warehouseExec
	warehouseReady = func() bool { return ready }
	warehouseExec = func(_ context.Context, stmt string, args ...any) error {
		w.stmts = append(w.stmts, stmt)
		w.args = append(w.args, args)
		return nil
	}
	reportProvisioned.Store(false)
	t.Cleanup(func() {
		warehouseReady, warehouseExec = oldReady, oldExec
		reportProvisioned.Store(false)
	})
	return w
}

// fakeReporter records what the driver handed it.
type fakeReporter struct {
	ms     []Metric
	secret string
	window Window
}

func (f *fakeReporter) ID() string { return "fake" }
func (f *fakeReporter) Report(_ context.Context, _ Config, secret string, w Window) ([]Metric, error) {
	f.secret, f.window = secret, w
	return f.ms, nil
}

// withReporter registers r for the duration of one test.
func withReporter(t *testing.T, r Reporter) {
	t.Helper()
	reporters[r.ID()] = r
	t.Cleanup(func() { delete(reporters, r.ID()) })
}

func window(start, end string) Window {
	s, _ := time.Parse("2006-01-02", start)
	e, _ := time.Parse("2006-01-02", end)
	return Window{Start: s, End: e}
}

// ── the shared-custody invariant ─────────────────────────────────────────────

// TestReporterSharesDestinationIdentity is the load-bearing structural test: a
// Reporter is keyed by the SAME slug as a registered Destination. That identity
// is what makes the inbound pull read the org's existing row and existing KMS
// secret; a Reporter under a slug nothing forwards to would silently never run,
// because the driver iterates the org's CONNECTED destinations.
func TestReporterSharesDestinationIdentity(t *testing.T) {
	if len(reporters) == 0 {
		t.Fatal("no reporters registered")
	}
	for id, r := range reporters {
		if r.ID() != id {
			t.Fatalf("reporter keyed %q reports id %q", id, r.ID())
		}
		if _, ok := registry[id]; !ok {
			t.Fatalf("reporter %q has no destination of the same slug", id)
		}
	}
	for _, want := range []string{metaID, googleadsID} {
		if _, ok := reporters[want]; !ok {
			t.Fatalf("%s has no reporter", want)
		}
	}
}

func TestWindowSnapsToDaysAndRefusesNonsense(t *testing.T) {
	start := time.Date(2026, 3, 1, 17, 42, 9, 0, time.UTC)
	got := Window{Start: start, End: start.AddDate(0, 0, 2)}.days()
	if !got.Start.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("start not snapped to UTC midnight: %s", got.Start)
	}
	if err := got.check(); err != nil {
		t.Fatalf("a two-day window was refused: %v", err)
	}
	if err := (Window{}).check(); err == nil {
		t.Fatal("a zero window was accepted")
	}
	if err := window("2026-03-05", "2026-03-01").check(); err == nil {
		t.Fatal("a backwards window was accepted")
	}
	if err := window("2020-01-01", "2026-01-01").check(); err == nil {
		t.Fatal("a six-year window was accepted; it would truncate in silence")
	}
}

// ── Meta ─────────────────────────────────────────────────────────────────────

const metaInsightsBody = `{"data":[
  {"campaign_id":"c1","campaign_name":"Spring","date_start":"2026-03-01",
   "spend":"120.50","impressions":"10000","clicks":"250","account_currency":"USD",
   "actions":[{"action_type":"offsite_conversion.fb_pixel_purchase","value":"9"},
              {"action_type":"omni_purchase","value":"7"}],
   "action_values":[{"action_type":"offsite_conversion.fb_pixel_purchase","value":"900"},
                    {"action_type":"omni_purchase","value":"700"}]},
  {"campaign_id":"c2","campaign_name":"Always On","date_start":"2026-03-02",
   "spend":"8","impressions":"400","clicks":"3","account_currency":"USD"}
]}`

func TestMetaReport(t *testing.T) {
	var sawAuth, sawRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "s3cr3t") {
			t.Errorf("the access token reached the URL: %s", r.URL.RawQuery)
		}
		sawAuth = r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/me/adaccounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"act_123"}]}`))
		case r.URL.Path == "/act_123/insights":
			sawRange = r.URL.Query().Get("time_range")
			_, _ = w.Write([]byte(metaInsightsBody))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	old := metaGraph
	metaGraph = srv.URL
	defer func() { metaGraph = old }()

	ms, err := metaReport{}.Report(context.Background(), Config{}, "s3cr3t", window("2026-03-01", "2026-03-02"))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if sawAuth != "Bearer s3cr3t" {
		t.Fatalf("token did not ride the Authorization header: %q", sawAuth)
	}
	if sawRange != `{"since":"2026-03-01","until":"2026-03-02"}` {
		t.Fatalf("time_range was %q", sawRange)
	}
	if len(ms) != 2 {
		t.Fatalf("got %d metrics, want 2", len(ms))
	}
	got := ms[0]
	if !got.Day.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("day %s", got.Day)
	}
	if got.Campaign != "c1" || got.Name != "Spring" || got.Currency != "USD" {
		t.Fatalf("dimensions wrong: %+v", got)
	}
	if got.Spend != 120.50 || got.Impressions != 10000 || got.Clicks != 250 {
		t.Fatalf("spend/impressions/clicks wrong: %+v", got)
	}
	// omni_purchase is Meta's own dedup and is listed SECOND in the fixture, so
	// picking it proves the preference beats array order — and proves the two
	// purchase types are never summed (9+7 and 900+700 would both be wrong).
	if got.Conversions != 7 || got.Revenue != 700 {
		t.Fatalf("purchase preference wrong: conversions=%v revenue=%v, want 7 and 700", got.Conversions, got.Revenue)
	}
	// A campaign that reported no actions reports zero, not a failed day.
	if ms[1].Conversions != 0 || ms[1].Revenue != 0 || ms[1].Spend != 8 {
		t.Fatalf("actionless campaign wrong: %+v", ms[1])
	}
}

// TestMetaReportWithoutAdAccount pins the honest failure: a token that reads no
// ad account cannot report spend, and saying so beats answering with nothing and
// letting a dashboard read it as zero spend.
func TestMetaReportWithoutAdAccount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	old := metaGraph
	metaGraph = srv.URL
	defer func() { metaGraph = old }()

	if _, err := (metaReport{}).Report(context.Background(), Config{}, "s3cr3t", window("2026-03-01", "2026-03-02")); err == nil {
		t.Fatal("a credential that reads no ad account reported success")
	}
}

// ── Google Ads ───────────────────────────────────────────────────────────────

func TestGoogleadsReport(t *testing.T) {
	var sawQuery, sawDevToken string
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"at-1"}`))
		case "/customers/123/googleAds:search":
			buf, _ := io.ReadAll(r.Body)
			sawQuery = string(buf)
			sawDevToken = r.Header.Get("developer-token")
			page++
			if page == 1 {
				_, _ = w.Write([]byte(`{"results":[{"campaign":{"id":"7","name":"Brand"},
					"customer":{"currencyCode":"EUR"},"segments":{"date":"2026-03-02"},
					"metrics":{"costMicros":"12340000","impressions":"5000","clicks":"120",
					"conversions":4.5,"conversionsValue":333.25}}],"nextPageToken":"p2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"campaign":{"id":"8","name":"Generic"},
				"customer":{"currencyCode":"EUR"},"segments":{"date":"2026-03-02"},
				"metrics":{"costMicros":"500000","impressions":"10","clicks":"1",
				"conversions":0,"conversionsValue":0}}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	oldAPI, oldOAuth := googleAdsAPI, googleOAuthURL
	googleAdsAPI, googleOAuthURL = srv.URL, srv.URL+"/token"
	defer func() { googleAdsAPI, googleOAuthURL = oldAPI, oldOAuth }()

	creds := `{"developer_token":"dev","client_id":"ci","client_secret":"cs","refresh_token":"rt"}`
	ms, err := googleadsReport{}.Report(context.Background(),
		Config{"customerId": "123"}, creds, window("2026-03-01", "2026-03-02"))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if sawDevToken != "dev" {
		t.Fatalf("developer-token header was %q", sawDevToken)
	}
	if !strings.Contains(sawQuery, "segments.date BETWEEN '2026-03-01' AND '2026-03-02'") {
		t.Fatalf("window did not reach the query: %s", sawQuery)
	}
	// The second page proves the report is not silently truncated at one page.
	if len(ms) != 2 {
		t.Fatalf("got %d metrics, want 2 across two pages", len(ms))
	}
	got := ms[0]
	if got.Campaign != "7" || got.Name != "Brand" || got.Currency != "EUR" {
		t.Fatalf("dimensions wrong: %+v", got)
	}
	if got.Spend != 12.34 {
		t.Fatalf("cost_micros not converted to currency: %v, want 12.34", got.Spend)
	}
	if got.Impressions != 5000 || got.Clicks != 120 || got.Conversions != 4.5 || got.Revenue != 333.25 {
		t.Fatalf("metrics wrong: %+v", got)
	}
}

// ── the warehouse write ──────────────────────────────────────────────────────

func TestRecordProvisionsThenBindsEveryValue(t *testing.T) {
	wh := fakeWarehouse(t, true)
	day1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ms := []Metric{
		{Day: day1, Campaign: "c1", Name: "Spring", Currency: "USD", Spend: 120.5, Impressions: 10000, Clicks: 250, Conversions: 7, Revenue: 700},
		{Day: day1, Campaign: "c2", Name: "Always On", Currency: "USD", Spend: 8},
	}
	if err := record(context.Background(), "acme", metaID, ms); err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(wh.stmts) != 3 {
		t.Fatalf("got %d statements, want database + table + insert", len(wh.stmts))
	}
	if !strings.Contains(wh.stmts[1], "ReplacingMergeTree(pulled_at)") {
		t.Fatalf("the table is not replacing; a re-pull would accumulate restatements:\n%s", wh.stmts[1])
	}
	insert := wh.stmts[2]
	if strings.Contains(insert, "acme") || strings.Contains(insert, "Spring") {
		t.Fatalf("a value was rendered into statement text: %s", insert)
	}
	// Assert the SHAPE, not just the count: a tuple missing its parenthesis
	// carries the right number of placeholders and is still not an insert.
	const tuple = "(?,?,?,?,?,?,?,?,?,?,?,?)"
	if !strings.HasSuffix(insert, "VALUES "+tuple+","+tuple) {
		t.Fatalf("insert does not end in two bound 12-column tuples:\n%s", insert)
	}
	args := wh.args[2]
	if len(args) != 24 {
		t.Fatalf("bound %d values, want 24", len(args))
	}
	want := []any{"acme", metaID, "c1", day1, "Spring", "USD", 120.5, int64(10000), int64(250), float64(7), float64(700)}
	for i, w := range want {
		if args[i] != w {
			t.Fatalf("arg %d is %#v, want %#v", i, args[i], w)
		}
	}
	if _, ok := args[11].(time.Time); !ok {
		t.Fatalf("pulled_at is %#v, want a time", args[11])
	}

	// Provisioning latches: a second record inserts and nothing more.
	if err := record(context.Background(), "acme", metaID, ms[:1]); err != nil {
		t.Fatalf("second record: %v", err)
	}
	if len(wh.stmts) != 4 {
		t.Fatalf("the table was re-provisioned; got %d statements", len(wh.stmts))
	}
}

// TestRecordNeedsAWarehouse pins the honest gap: an unreachable warehouse fails
// the write and does NOT latch, so the next pull retries rather than believing a
// table it never created exists.
func TestRecordNeedsAWarehouse(t *testing.T) {
	wh := fakeWarehouse(t, false)
	err := record(context.Background(), "acme", metaID, []Metric{{Campaign: "c1"}})
	if err == nil {
		t.Fatal("a write to an unreachable warehouse reported success")
	}
	if len(wh.stmts) != 0 {
		t.Fatalf("statements ran against an unreachable warehouse: %v", wh.stmts)
	}
	if reportProvisioned.Load() {
		t.Fatal("a failed provision latched; the table would never be created")
	}
}

// ── the driver ───────────────────────────────────────────────────────────────

// TestPullRecordsWhatThePlatformReported is the end-to-end driver test: a
// connected, enabled destination whose credential is KMS-sealed is pulled with
// THAT credential and the snapped window, and its rows land in the warehouse
// under the caller's org.
func TestPullRecordsWhatThePlatformReported(t *testing.T) {
	ctx := context.Background()
	wh := fakeWarehouse(t, true)
	kc := newKMS(t)
	s := testService(t, kc, map[string]Destination{"fake": &fakeDest{}})
	rep := &fakeReporter{ms: []Metric{
		{Day: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Campaign: "c1", Spend: 10, Impressions: 100, Clicks: 5},
	}}
	withReporter(t, rep)

	if err := kc.Put(kmsPath("acme", "fake"), "access_token", kmsEnv, []byte("s3cr3t")); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := s.State.store.Upsert(ctx, Row{Org: "acme", Platform: "fake", Enabled: true, Config: Config{"pixelId": "px"}}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	got, err := pull(s, ctx, "acme", window("2026-03-01", "2026-03-02"))
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if got["fake"] != 1 {
		t.Fatalf("pull reported %v, want one row for fake", got)
	}
	if rep.secret != "s3cr3t" {
		t.Fatalf("reporter got secret %q, want the one Send resolves", rep.secret)
	}
	if !rep.window.Start.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("reporter got window %+v", rep.window)
	}
	if len(wh.stmts) != 3 || !strings.HasPrefix(wh.stmts[2], "INSERT INTO "+reportTable) {
		t.Fatalf("rows did not reach %s: %v", reportTable, wh.stmts)
	}
	if wh.args[2][0] != "acme" || wh.args[2][1] != "fake" {
		t.Fatalf("rows landed under the wrong tenant/platform: %v", wh.args[2][:2])
	}
}

// TestPullSkipsWhatCannotReport covers the three ways a connected destination
// contributes nothing, none of which is an error: it is disabled, it has no
// Reporter, or it belongs to another org.
func TestPullSkipsWhatCannotReport(t *testing.T) {
	ctx := context.Background()
	fakeWarehouse(t, true)
	kc := newKMS(t)
	s := testService(t, kc, map[string]Destination{"fake": &fakeDest{}})
	withReporter(t, &fakeReporter{ms: []Metric{{Campaign: "c1"}}})
	if err := kc.Put(kmsPath("acme", "fake"), "access_token", kmsEnv, []byte("s3cr3t")); err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Disabled: connected, but the org turned it off.
	_ = s.State.store.Upsert(ctx, Row{Org: "acme", Platform: "fake", Enabled: false, Config: Config{"pixelId": "px"}})
	if got, err := pull(s, ctx, "acme", window("2026-03-01", "2026-03-02")); err != nil || len(got) != 0 {
		t.Fatalf("a disabled destination was pulled: %v %v", got, err)
	}

	// No reporter: ga4 forwards fine and simply has nothing to pull back.
	_ = s.State.store.Upsert(ctx, Row{Org: "acme", Platform: "ga4", Enabled: true})
	if got, err := pull(s, ctx, "acme", window("2026-03-01", "2026-03-02")); err != nil || len(got) != 0 {
		t.Fatalf("a destination with no reporter was pulled: %v %v", got, err)
	}

	// Another org's connection is not this org's to pull.
	if got, err := pull(s, ctx, "other", window("2026-03-01", "2026-03-02")); err != nil || len(got) != 0 {
		t.Fatalf("pull crossed a tenant boundary: %v %v", got, err)
	}
}

func TestPullRefusesABadOrgOrWindow(t *testing.T) {
	s := testService(t, newKMS(t), map[string]Destination{})
	if _, err := pull(s, context.Background(), "Not A Slug", window("2026-03-01", "2026-03-02")); err == nil {
		t.Fatal("pull accepted an org that is not a DNS-1123 label")
	}
	if _, err := pull(s, context.Background(), "acme", window("2026-03-05", "2026-03-01")); err == nil {
		t.Fatal("pull accepted a backwards window")
	}
}
