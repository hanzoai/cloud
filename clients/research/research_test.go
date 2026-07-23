package research

// These tests prove the no-data-loss contract of the research plane WITHOUT a live
// datastore: the SQLite write plane is the source of truth (fully exercised), and the
// warehouse roll-up degrades fail-soft (rolled_up:false) when DATASTORE_ADDR is unset,
// exactly as it will when a real datastore is momentarily absent.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/hanzoai/cloud"
	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestMain injects a dev master key so the per-org SQLite files open under an
// encryption-capable build (sqlite v0.3.2's pure-Go codec always can encrypt) — the
// repo's shared OrgDB-test setup (clients/sync/main_test.go). 32 zero bytes, dev-only,
// temp dirs only.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	}
	os.Exit(m.Run())
}

const proj = "enso-bench"

// ── store plane: idempotency, latest-run-canonical, immutability, aggregates ──────

func newStore(t *testing.T) (*store, context.Context) {
	t.Helper()
	stores := cloud.NewOrgStore(t.TempDir(), "research", openStore)
	st, err := stores.For("acme", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = stores.CloseAll() })
	return st, context.Background()
}

// TestIngestIdempotentByStableID is the no-data-loss test: re-ingesting the SAME
// corpus adds zero rows and leaves the counts identical — running the backfill twice
// is a no-op, so no run is ever duplicated or lost.
func TestIngestIdempotentByStableID(t *testing.T) {
	st, ctx := newStore(t)
	exps := []Experiment{{
		ID: "benchmark:grok-4.5:gpqa_diamond", Kind: "benchmark", Subject: "grok-4.5",
		Task: "gpqa_diamond", Metric: "accuracy", Value: 94.3, N: 198, TS: 100,
	}}
	atts := []Attempt{
		{Benchmark: "gpqa_diamond", Item: "q1", Model: "grok-4.5", Answer: "A", Correct: true},
		{Benchmark: "gpqa_diamond", Item: "q2", Model: "grok-4.5", Answer: "B", Correct: false},
	}

	e1, a1, err := st.ingest(ctx, proj, exps, atts)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if e1 != 1 || a1 != 2 {
		t.Fatalf("first ingest added exp=%d att=%d, want 1,2", e1, a1)
	}
	ec1, ac1, _ := st.counts(ctx)

	// The double-backfill: identical batch, ZERO added, counts unchanged.
	e2, a2, err := st.ingest(ctx, proj, exps, atts)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if e2 != 0 || a2 != 0 {
		t.Fatalf("re-ingest added exp=%d att=%d, want 0,0 (idempotent)", e2, a2)
	}
	ec2, ac2, _ := st.counts(ctx)
	if ec1 != ec2 || ac1 != ac2 {
		t.Fatalf("counts drifted on re-ingest: before (%d,%d) after (%d,%d)", ec1, ac1, ec2, ac2)
	}
	if ec2 != 1 || ac2 != 2 {
		t.Fatalf("final counts exp=%d att=%d, want 1,2", ec2, ac2)
	}
}

// TestExperimentLatestRunCanonical proves a model's number is the LATEST complete run,
// read mechanically — the HIP's exact example (a stale enso LiveCodeBench 91.4 that
// regressed to 69.7 must read 69.7), and an out-of-order replay of the OLD run must
// not regress the newer number.
func TestExperimentLatestRunCanonical(t *testing.T) {
	st, ctx := newStore(t)
	id := "benchmark:enso:livecodebench"
	mk := func(v float64, ts int64) Experiment {
		return Experiment{ID: id, Kind: "benchmark", Subject: "enso", Task: "livecodebench", Metric: "accuracy", Value: v, N: 200, TS: ts}
	}
	if _, _, err := st.ingest(ctx, proj, []Experiment{mk(91.4, 100)}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ingest(ctx, proj, []Experiment{mk(69.7, 200)}, nil); err != nil { // newer run
		t.Fatal(err)
	}
	if got := experimentValue(t, st, id); got != 69.7 {
		t.Fatalf("after newer run value=%v, want 69.7 (latest-run-canonical)", got)
	}
	// An older run replayed late must NOT overwrite the newer number.
	if _, _, err := st.ingest(ctx, proj, []Experiment{mk(91.4, 50)}, nil); err != nil {
		t.Fatal(err)
	}
	if got := experimentValue(t, st, id); got != 69.7 {
		t.Fatalf("after stale replay value=%v, want 69.7 (no regress on older ts)", got)
	}
}

// TestAttemptImmutableFirstWriteWins proves the raw response is immutable: a re-ingest
// of the same (project, benchmark, item, model) keeps the ORIGINAL response and adds
// no row.
func TestAttemptImmutableFirstWriteWins(t *testing.T) {
	st, ctx := newStore(t)
	key := Attempt{Benchmark: "gpqa_diamond", Item: "q1", Model: "m", Answer: "A", Correct: true, Response: "ORIGINAL"}
	if _, a, err := st.ingest(ctx, proj, nil, []Attempt{key}); err != nil || a != 1 {
		t.Fatalf("first attempt add=%d err=%v, want 1,nil", a, err)
	}
	mutated := key
	mutated.Response = "TAMPERED"
	mutated.Correct = false
	if _, a, err := st.ingest(ctx, proj, nil, []Attempt{mutated}); err != nil || a != 0 {
		t.Fatalf("re-ingest add=%d err=%v, want 0,nil (immutable)", a, err)
	}
	var resp string
	var correct int
	if err := st.db.QueryRowContext(ctx,
		`SELECT response, correct FROM attempt WHERE project=? AND benchmark=? AND item=? AND model=?`,
		proj, key.Benchmark, key.Item, key.Model).Scan(&resp, &correct); err != nil {
		t.Fatal(err)
	}
	if resp != "ORIGINAL" || correct != 1 {
		t.Fatalf("attempt mutated: response=%q correct=%d, want ORIGINAL,1", resp, correct)
	}
}

// TestProjectSummariesAndTotals proves the ops-board aggregates report REAL totals
// deterministically across an org's projects, and that a default status is `complete`.
func TestProjectSummariesAndTotals(t *testing.T) {
	st, ctx := newStore(t)
	// enso-bench: a benchmark experiment + two attempts (two models, one benchmark).
	_, _, err := st.ingest(ctx, "enso-bench",
		[]Experiment{{ID: "benchmark:m1:gpqa_diamond", Kind: "benchmark", Subject: "m1", Task: "gpqa_diamond", Metric: "accuracy", Value: 80, N: 2, CostUSD: 1.5}},
		[]Attempt{
			{Benchmark: "gpqa_diamond", Item: "q1", Model: "m1", Correct: true, Answer: "A"},
			{Benchmark: "gpqa_diamond", Item: "q1", Model: "m2", Correct: false, Answer: "B"},
		})
	if err != nil {
		t.Fatal(err)
	}
	// hanzo-engine: a kernel-perf experiment, no attempts.
	if _, _, err := st.ingest(ctx, "hanzo-engine",
		[]Experiment{{ID: "kernel-perf:hanzo-engine:prefill", Kind: "kernel-perf", Subject: "hanzo-engine", Task: "prefill", Metric: "tok/s", Value: 94.4, CostUSD: 0}}, nil); err != nil {
		t.Fatal(err)
	}

	ps, err := st.projectSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("projects=%d, want 2", len(ps))
	}
	byName := map[string]ProjectSummary{}
	for _, p := range ps {
		byName[p.Project] = p
	}
	if e := byName["enso-bench"]; e.Experiments != 1 || e.Attempts != 2 || e.Models != 2 || e.Benchmarks != 1 || e.CostUSD != 1.5 {
		t.Fatalf("enso-bench summary wrong: %+v", e)
	}
	if k := byName["hanzo-engine"]; k.Experiments != 1 || k.Attempts != 0 || len(k.Kinds) != 1 || k.Kinds[0] != "kernel-perf" {
		t.Fatalf("hanzo-engine summary wrong: %+v", k)
	}

	// Org-wide totals.
	tot, err := st.totals(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if tot.Projects != 2 || tot.Experiments != 2 || tot.Attempts != 2 || tot.Models != 2 || tot.Benchmarks != 1 || tot.CostUSD != 1.5 {
		t.Fatalf("org totals wrong: %+v", tot)
	}
	if len(tot.ByKind) != 2 {
		t.Fatalf("by_kind=%d, want 2 (benchmark, kernel-perf)", len(tot.ByKind))
	}
	// Project-scoped totals.
	et, err := st.totals(ctx, "enso-bench")
	if err != nil {
		t.Fatal(err)
	}
	if et.Experiments != 1 || et.Attempts != 2 || et.Projects != 1 {
		t.Fatalf("enso-bench totals wrong: %+v", et)
	}

	// Default status is complete.
	exps, _ := st.listExperiments(ctx, "enso-bench", "")
	if len(exps) != 1 || exps[0].Status != "complete" {
		t.Fatalf("default status wrong: %+v", exps)
	}
}

// experimentValue reads one experiment's canonical value from the store.
func experimentValue(t *testing.T, st *store, id string) float64 {
	t.Helper()
	exps, err := st.listExperiments(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range exps {
		if e.ID == id {
			return e.Value
		}
	}
	t.Fatalf("experiment %q not found", id)
	return 0
}

// ── SSRF gate (HIP-0512 §5) ───────────────────────────────────────────────────

// TestSSRFGate proves the BYO-endpoint denylist refuses every address class an
// experiment's endpoint must never target, and admits a public https URL. Literal IPs
// keep the test hermetic (no DNS).
func TestSSRFGate(t *testing.T) {
	blocked := []string{
		"https://127.0.0.1/v1",                     // loopback
		"https://[::1]/v1",                         // loopback v6
		"https://10.1.2.3/v1",                      // private 10/8
		"https://172.16.0.1/v1",                    // private 172.16/12
		"https://192.168.0.1/v1",                   // private 192.168/16
		"https://169.254.169.254/latest/meta-data", // cloud metadata (link-local)
		"https://[fd00:ec2::254]/",                 // ULA / v6 metadata
		"https://0.0.0.0/v1",                       // unspecified
		"http://1.1.1.1/v1",                        // non-https scheme
		"ftp://1.1.1.1/v1",                         // non-http scheme
		"not-a-url",                                // unparseable / no host
	}
	for _, u := range blocked {
		if err := ssrfSafe(u); err == nil {
			t.Errorf("ssrfSafe(%q) = nil, want refusal", u)
		}
	}
	allowed := []string{"https://1.1.1.1/v1", "https://8.8.8.8/chat/completions"}
	for _, u := range allowed {
		if err := ssrfSafe(u); err != nil {
			t.Errorf("ssrfSafe(%q) = %v, want nil (public https)", u, err)
		}
	}
}

// ── HTTP surface: round-trip, tenant isolation, fail-soft, SSRF, board reads ──────

func mountResearch(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// do issues a request with a validated principal (X-Org-Id + X-User-Id, the pair the
// gateway sets only from a verified credential); org=="" exercises the no-principal
// path. project, when set, rides as X-Project-Id (principal.Project). Returns status +
// decoded body.
func do(t *testing.T, app *zip.App, method, path, org, project string, body any) (int, map[string]any) {
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
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func TestIngestRoundTripAndTenantIsolation(t *testing.T) {
	app := mountResearch(t)
	batch := IngestRequest{
		Experiments: []Experiment{{
			ID: "benchmark:grok-4.5:gpqa_diamond", Kind: "benchmark", Subject: "grok-4.5",
			Task: "gpqa_diamond", Metric: "accuracy", Value: 94.3, N: 198, TS: 100,
		}},
		Attempts: []Attempt{{Benchmark: "gpqa_diamond", Item: "q1", Model: "grok-4.5", Answer: "A", Correct: true}},
	}

	// POST as acme / project enso-bench.
	code, m := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	if code != http.StatusOK {
		t.Fatalf("POST status=%d body=%v", code, m)
	}
	if m["experiments_ingested"].(float64) != 1 || m["attempts_ingested"].(float64) != 1 {
		t.Fatalf("ingested counts wrong: %v", m)
	}
	if m["project"] != "enso-bench" {
		t.Fatalf("project not stamped server-side: %v", m["project"])
	}
	// No live datastore in tests → fail-soft roll-up, SQLite retains the write.
	if m["rolled_up"].(bool) != false {
		t.Fatalf("rolled_up=%v, want false (no datastore in test)", m["rolled_up"])
	}

	// GET as acme returns exactly what we posted, project-stamped, status=complete.
	code, m = do(t, app, http.MethodGet, "/v1/research/experiments", "acme", "", nil)
	if code != http.StatusOK || m["total"].(float64) != 1 {
		t.Fatalf("GET acme status=%d total=%v", code, m["total"])
	}
	got := m["data"].([]any)[0].(map[string]any)
	if got["id"] != "benchmark:grok-4.5:gpqa_diamond" || got["value"].(float64) != 94.3 {
		t.Fatalf("round-trip mismatch: %v", got)
	}
	if got["project"] != "enso-bench" || got["status"] != "complete" {
		t.Fatalf("project/status wrong on read: %v", got)
	}

	// GET as a DIFFERENT org sees nothing — physical per-org file isolation.
	code, m = do(t, app, http.MethodGet, "/v1/research/experiments", "other", "", nil)
	if code != http.StatusOK || m["total"].(float64) != 0 {
		t.Fatalf("tenant isolation broken: other org sees total=%v", m["total"])
	}

	// No validated principal → 403, nothing reachable.
	code, _ = do(t, app, http.MethodGet, "/v1/research/experiments", "", "", nil)
	if code != http.StatusForbidden {
		t.Fatalf("no-principal GET status=%d, want 403", code)
	}
}

// TestHTTPProjectsAndTotals proves the ops-board reads: /projects lists every project
// with real totals, /totals is the headline aggregate + per-kind breakdown.
func TestHTTPProjectsAndTotals(t *testing.T) {
	app := mountResearch(t)
	// Two projects under one org.
	do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", IngestRequest{
		Experiments: []Experiment{{ID: "benchmark:m1:hle", Kind: "benchmark", Subject: "m1", Task: "hle", Metric: "accuracy", Value: 70, N: 1, CostUSD: 2}},
		Attempts:    []Attempt{{Benchmark: "hle", Item: "q1", Model: "m1", Correct: true, Answer: "A"}},
	})
	do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "hanzo-engine", IngestRequest{
		Experiments: []Experiment{{ID: "kernel-perf:hanzo-engine:prefill", Kind: "kernel-perf", Subject: "hanzo-engine", Task: "prefill", Metric: "tok/s", Value: 94.4}},
	})

	code, m := do(t, app, http.MethodGet, "/v1/research/projects", "acme", "", nil)
	if code != http.StatusOK || m["total"].(float64) != 2 {
		t.Fatalf("/projects status=%d total=%v", code, m["total"])
	}

	code, m = do(t, app, http.MethodGet, "/v1/research/totals", "acme", "", nil)
	if code != http.StatusOK {
		t.Fatalf("/totals status=%d", code)
	}
	if m["projects"].(float64) != 2 || m["experiments"].(float64) != 2 || m["attempts"].(float64) != 1 || m["cost_usd"].(float64) != 2 {
		t.Fatalf("/totals aggregate wrong: %v", m)
	}
	if len(m["by_kind"].([]any)) != 2 {
		t.Fatalf("/totals by_kind wrong: %v", m["by_kind"])
	}
}

// TestHTTPIdempotentDoubleIngest proves the no-data-loss guarantee over the wire: two
// identical POSTs leave the totals identical.
func TestHTTPIdempotentDoubleIngest(t *testing.T) {
	app := mountResearch(t)
	batch := IngestRequest{
		Attempts: []Attempt{
			{Benchmark: "hle", Item: "i1", Model: "m", Answer: "x", Correct: true},
			{Benchmark: "hle", Item: "i2", Model: "m", Answer: "y", Correct: false},
		},
	}
	_, m1 := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	_, m2 := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	if m2["attempts_ingested"].(float64) != 0 {
		t.Fatalf("second POST ingested %v attempts, want 0", m2["attempts_ingested"])
	}
	if m1["attempts_total"].(float64) != m2["attempts_total"].(float64) {
		t.Fatalf("totals drifted across double-ingest: %v vs %v", m1["attempts_total"], m2["attempts_total"])
	}
}

// TestHTTPSSRFRejected proves an experiment carrying a loopback/metadata endpoint is
// refused at ingest and writes nothing.
func TestHTTPSSRFRejected(t *testing.T) {
	app := mountResearch(t)
	batch := IngestRequest{Experiments: []Experiment{{
		ID: "kernel-perf:hanzo-engine:prefill", Kind: "kernel-perf", Subject: "hanzo-engine",
		Task: "prefill", Metric: "tok/s", Value: 94.4, Endpoint: "https://169.254.169.254/latest/meta-data",
	}}}
	code, m := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("SSRF endpoint status=%d body=%v, want 422", code, m)
	}
	// Nothing was written.
	_, g := do(t, app, http.MethodGet, "/v1/research/experiments", "acme", "", nil)
	if g["total"].(float64) != 0 {
		t.Fatalf("SSRF-rejected batch still wrote rows: total=%v", g["total"])
	}
}
