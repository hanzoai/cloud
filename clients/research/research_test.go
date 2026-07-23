package research

// These tests prove the versioned append-only contract WITHOUT a live datastore: the
// SQLite plane is exercised fully; the warehouse roll-up degrades fail-soft
// (rolled_up:false) when DATASTORE_ADDR is unset, as it will when a datastore is absent.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// TestIngestIdempotentByContent: re-ingesting byte-identical content appends ZERO
// versions and leaves retained + canonical counts identical — the backfill is a no-op on
// the second run (no run duplicated, none lost).
func TestIngestIdempotentByContent(t *testing.T) {
	st, ctx := newStore(t)
	exps := []Experiment{{ID: "benchmark:grok-4.5:gpqa_diamond", Kind: "benchmark", Subject: "grok-4.5", Task: "gpqa_diamond", Metric: "accuracy", Value: 94.3, N: 198}}
	atts := []Attempt{
		{Benchmark: "gpqa_diamond", Item: "q1", Model: "grok-4.5", Answer: "A", Correct: true},
		{Benchmark: "gpqa_diamond", Item: "q2", Model: "grok-4.5", Answer: "B", Correct: false},
	}
	e1, a1, err := st.ingest(ctx, proj, exps, atts)
	if err != nil || e1 != 1 || a1 != 2 {
		t.Fatalf("first ingest added exp=%d att=%d err=%v, want 1,2,nil", e1, a1, err)
	}
	c1, _ := st.counts(ctx)

	e2, a2, err := st.ingest(ctx, proj, exps, atts)
	if err != nil || e2 != 0 || a2 != 0 {
		t.Fatalf("re-ingest added exp=%d att=%d err=%v, want 0,0,nil (idempotent)", e2, a2, err)
	}
	c2, _ := st.counts(ctx)
	if c1 != c2 {
		t.Fatalf("counts drifted on re-ingest: %+v -> %+v", c1, c2)
	}
	if c2.AttemptsRetained != 2 || c2.AttemptsCanonical != 2 {
		t.Fatalf("counts wrong: %+v, want retained=2 canonical=2", c2)
	}
}

// TestCorrectionAppendsVersion: a correction (new content for the same stable id) APPENDS
// a version; the prior is RETAINED (superseded), the canonical view shows the corrected
// value — the HIP's stale-91.4→69.7 example, as supersession not overwrite.
func TestCorrectionAppendsVersion(t *testing.T) {
	st, ctx := newStore(t)
	id := "benchmark:enso:livecodebench"
	mk := func(v float64, rev string, ts int64) Experiment {
		return Experiment{ID: id, Kind: "benchmark", Subject: "enso", Task: "livecodebench", Metric: "accuracy", Value: v, N: 200, Revision: rev, TS: ts}
	}
	if _, _, err := st.ingest(ctx, proj, []Experiment{mk(91.4, "original", 100)}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ingest(ctx, proj, []Experiment{mk(69.7, "corrected", 200)}, nil); err != nil {
		t.Fatal(err)
	}
	c, _ := st.counts(ctx)
	if c.ExperimentsRetained != 2 || c.ExperimentsCanonical != 1 {
		t.Fatalf("counts=%+v, want retained=2 canonical=1", c)
	}
	exps, _ := st.listExperiments(ctx, "", "")
	if len(exps) != 1 || exps[0].Value != 69.7 || exps[0].Revision != "corrected" || !exps[0].Canonical {
		t.Fatalf("canonical view wrong: %+v (want value 69.7, revision corrected, canonical)", exps)
	}
}

// TestRetractionWithdraws proves a retraction is HONORED, not a silent no-op (H1): it
// appends a distinct version (revision is part of content identity), withdraws the id
// from canonical (the latest version is a retraction ⇒ no canonical), retains both, and a
// later correction restores canonical.
func TestRetractionWithdraws(t *testing.T) {
	st, ctx := newStore(t)
	id := "benchmark:m:gpqa_diamond"
	orig := Experiment{ID: id, Kind: "benchmark", Subject: "m", Task: "gpqa_diamond", Metric: "accuracy", Value: 80, N: 10, TS: 100}
	if _, _, err := st.ingest(ctx, proj, []Experiment{orig}, nil); err != nil {
		t.Fatal(err)
	}
	if exps, _ := st.listExperiments(ctx, "", ""); len(exps) != 1 {
		t.Fatalf("pre-retraction canonical=%d, want 1", len(exps))
	}
	// Retract: same id + content, revision retracted, newer ts. revision is in the hash,
	// so this is a DISTINCT version (added=1), not an INSERT-OR-IGNORE no-op.
	retr := orig
	retr.Revision = "retracted"
	retr.TS = 200
	added, _, err := st.ingest(ctx, proj, []Experiment{retr}, nil)
	if err != nil || added != 1 {
		t.Fatalf("retraction added=%d err=%v, want 1 (not a silent no-op)", added, err)
	}
	if exps, _ := st.listExperiments(ctx, "", ""); len(exps) != 0 {
		t.Fatalf("post-retraction canonical=%d, want 0 (withdrawn)", len(exps))
	}
	c, _ := st.counts(ctx)
	if c.ExperimentsRetained != 2 || c.ExperimentsCanonical != 0 {
		t.Fatalf("counts=%+v, want retained=2 canonical=0 (retained keeps it, withdrawn from canonical)", c)
	}
	// A later corrected run supersedes the retraction → canonical again.
	corr := orig
	corr.Revision = "corrected"
	corr.Value = 75
	corr.TS = 300
	if _, _, err := st.ingest(ctx, proj, []Experiment{corr}, nil); err != nil {
		t.Fatal(err)
	}
	exps, _ := st.listExperiments(ctx, "", "")
	if len(exps) != 1 || exps[0].Value != 75 {
		t.Fatalf("post-correction canonical=%v, want value 75 (restored)", exps)
	}
}

// TestSupersessionIsSeqNotClientTS is the N2 cross-tool case: supersession is by the
// server's append clock (seq), NOT client ts. A backfilled experiment carries a big
// wall-clock ts (~1.7e9); an SDK live correction/retraction sends NONE (ts=0). Ordering by
// ts would sort the ts=0 record BEFORE the ts=big backfill so it would never win — the
// exact 91.4→69.7 stale-correction bug in the intended backfill+live-SDK workflow. Under
// seq (latest-append-wins) the later record supersedes regardless of ts.
func TestSupersessionIsSeqNotClientTS(t *testing.T) {
	st, ctx := newStore(t)
	big := int64(1_700_000_000) // an uploader's int(time.time())
	// Experiment: ts=big backfill, then ts=0 SDK correction → correction must win.
	eid := "benchmark:enso:livecodebench"
	mkE := func(v float64, rev string, ts int64) Experiment {
		return Experiment{ID: eid, Kind: "benchmark", Subject: "enso", Task: "livecodebench", Metric: "accuracy", Value: v, N: 200, Revision: rev, TS: ts}
	}
	if _, _, err := st.ingest(ctx, proj, []Experiment{mkE(91.4, "original", big)}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ingest(ctx, proj, []Experiment{mkE(69.7, "corrected", 0)}, nil); err != nil {
		t.Fatal(err)
	}
	if exps, _ := st.listExperiments(ctx, "", ""); len(exps) != 1 || exps[0].Value != 69.7 {
		t.Fatalf("ts=0 correction of ts=big backfill: canonical=%v, want 69.7 (seq-authoritative)", exps)
	}
	// A ts=0 SDK retraction of the ts=big-lineage id must WITHDRAW it.
	if _, _, err := st.ingest(ctx, proj, []Experiment{mkE(69.7, "retracted", 0)}, nil); err != nil {
		t.Fatal(err)
	}
	if exps, _ := st.listExperiments(ctx, "", ""); len(exps) != 0 {
		t.Fatalf("ts=0 retraction of ts=big lineage: canonical=%d, want 0 (withdrawn)", len(exps))
	}

	// Attempt: same cross-tool case — ts=big backfill answer, ts=0 SDK correction wins.
	mkA := func(ans string, rev string, ts int64) Attempt {
		return Attempt{Benchmark: "livecodebench", Item: "q1", Model: "enso", Answer: ans, Correct: ans == "A", Revision: rev, TS: ts}
	}
	if _, _, err := st.ingest(ctx, proj, nil, []Attempt{mkA("B", "original", big)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ingest(ctx, proj, nil, []Attempt{mkA("A", "corrected", 0)}); err != nil {
		t.Fatal(err)
	}
	var ans string
	if err := st.db.QueryRowContext(ctx, `SELECT a.answer FROM attempt a WHERE `+canonicalAttWhere+` AND a.benchmark='livecodebench'`).Scan(&ans); err != nil {
		t.Fatal(err)
	}
	if ans != "A" {
		t.Fatalf("ts=0 attempt correction of ts=big backfill: canonical answer=%q, want A", ans)
	}
	c, _ := st.counts(ctx)
	if c.ExperimentsRetained != 3 || c.AttemptsRetained != 2 {
		t.Fatalf("retained counts=%+v, want exp=3 att=2 (every version retained)", c)
	}
}

// TestProvenanceDistinctVersions: the SAME number on a DIFFERENT git sha / lib version is
// a distinct RETAINED version — the longitudinal "which lib version regressed this"
// record — and provenance is returned queryable on read.
func TestProvenanceDistinctVersions(t *testing.T) {
	st, ctx := newStore(t)
	id := "kernel-perf:hanzo-engine:prefill"
	mk := func(sha, libs string, ts int64) Experiment {
		return Experiment{ID: id, Kind: "kernel-perf", Subject: "hanzo-engine", Task: "prefill", Metric: "tok/s",
			Value: 94.4, GitSHA: sha, GitBranch: "main", LibVersions: json.RawMessage(libs), TS: ts}
	}
	if _, _, err := st.ingest(ctx, proj, []Experiment{mk("aaaa111", `{"hanzo-kernel":"0.2.39"}`, 10)}, nil); err != nil {
		t.Fatal(err)
	}
	// Same 94.4 tok/s but a newer kernel — a distinct data point, retained.
	if _, _, err := st.ingest(ctx, proj, []Experiment{mk("bbbb222", `{"hanzo-kernel":"0.2.40"}`, 20)}, nil); err != nil {
		t.Fatal(err)
	}
	c, _ := st.counts(ctx)
	if c.ExperimentsRetained != 2 || c.ExperimentsCanonical != 1 {
		t.Fatalf("counts=%+v, want retained=2 canonical=1 (provenance-distinct runs retained)", c)
	}
	exps, _ := st.listExperiments(ctx, "", "")
	if exps[0].GitSHA != "bbbb222" || string(exps[0].LibVersions) != `{"hanzo-kernel":"0.2.40"}` {
		t.Fatalf("canonical provenance wrong: sha=%q libs=%s", exps[0].GitSHA, exps[0].LibVersions)
	}
	// The superseded provenance is retained + queryable by git_sha (the board's query).
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM experiment WHERE git_sha=?`, "aaaa111").Scan(&n); err != nil || n != 1 {
		t.Fatalf("retained provenance query n=%d err=%v, want 1", n, err)
	}
}

// TestFaultedRetained: a faulted attempt (negative result) is RETAINED, and a later
// successful version supersedes it — canonical shows the success, retained keeps the fault.
func TestFaultedRetained(t *testing.T) {
	st, ctx := newStore(t)
	key := func(status, answer string, ts int64) Attempt {
		return Attempt{Benchmark: "gpqa_diamond", Item: "q1", Model: "m", Status: status, Answer: answer, Correct: answer == "A", TS: ts}
	}
	if _, a, err := st.ingest(ctx, proj, nil, []Attempt{key("faulted", "", 10)}); err != nil || a != 1 {
		t.Fatalf("faulted ingest add=%d err=%v", a, err)
	}
	if _, a, err := st.ingest(ctx, proj, nil, []Attempt{key("complete", "A", 20)}); err != nil || a != 1 {
		t.Fatalf("retry ingest add=%d err=%v", a, err)
	}
	c, _ := st.counts(ctx)
	if c.AttemptsRetained != 2 || c.AttemptsCanonical != 1 {
		t.Fatalf("counts=%+v, want retained=2 canonical=1 (fault retained, success canonical)", c)
	}
	var status, answer string
	if err := st.db.QueryRowContext(ctx, `SELECT a.status, a.answer FROM attempt a WHERE `+canonicalAttWhere).Scan(&status, &answer); err != nil {
		t.Fatal(err)
	}
	if status != "complete" || answer != "A" {
		t.Fatalf("canonical attempt = (%s,%s), want (complete,A)", status, answer)
	}
}

// TestFaultedOnlyRetainedNotCanonical is the 9306-vs-9190 gap in miniature: a
// faulted-only key (never answered) is RETAINED but is NOT counted as an answered
// canonical result — retained is the truth, canonical the answered view.
func TestFaultedOnlyRetainedNotCanonical(t *testing.T) {
	st, ctx := newStore(t)
	// One answered attempt + one faulted-only attempt (distinct keys).
	_, _, err := st.ingest(ctx, proj, nil, []Attempt{
		{Benchmark: "gpqa_diamond", Item: "ok", Model: "m", Answer: "A", Correct: true, Status: "complete"},
		{Benchmark: "gpqa_diamond", Item: "fault", Model: "m", Answer: "", Correct: false, Status: "faulted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := st.counts(ctx)
	if c.AttemptsRetained != 2 || c.AttemptsCanonical != 1 {
		t.Fatalf("counts=%+v, want retained=2 canonical=1 (faulted-only retained, not canonical)", c)
	}
}

// TestPrivateByDefaultAndGrant: an upload is private and grants no training/commons
// rights even if the payload asks; the SEPARATE grant is the only path that elevates.
func TestPrivateByDefaultAndGrant(t *testing.T) {
	st, ctx := newStore(t)
	e := Experiment{ID: "benchmark:m:gpqa_diamond", Kind: "benchmark", Subject: "m", Task: "gpqa_diamond", Metric: "accuracy", Value: 50, Visibility: "public", Trainable: true, Publishable: true}
	if _, _, err := st.ingest(ctx, proj, []Experiment{e}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := st.listExperiments(ctx, "", "")
	if got[0].Visibility != "private" || got[0].Trainable || got[0].Publishable {
		t.Fatalf("upload self-authorized: %+v (want private, no consent)", got[0])
	}
	pub := "public"
	yes := true
	if n, err := st.setGrant(ctx, proj, e.ID, &pub, &yes, nil); err != nil || n == 0 {
		t.Fatalf("grant n=%d err=%v", n, err)
	}
	got, _ = st.listExperiments(ctx, "", "")
	if got[0].Visibility != "public" || !got[0].Trainable || got[0].Publishable {
		t.Fatalf("grant wrong: %+v (want public, trainable, NOT publishable)", got[0])
	}
}

// TestProjectSummariesAndTotals: the ops-board aggregates report canonical + retained
// deterministically across an org's projects.
func TestProjectSummariesAndTotals(t *testing.T) {
	st, ctx := newStore(t)
	_, _, err := st.ingest(ctx, "enso-bench",
		[]Experiment{{ID: "benchmark:m1:gpqa_diamond", Kind: "benchmark", Subject: "m1", Task: "gpqa_diamond", Metric: "accuracy", Value: 80, N: 2, CostUSD: 1.5}},
		[]Attempt{
			{Benchmark: "gpqa_diamond", Item: "q1", Model: "m1", Correct: false, Answer: "B", TS: 1},
			{Benchmark: "gpqa_diamond", Item: "q1", Model: "m2", Correct: true, Answer: "A", TS: 1},
		})
	if err != nil {
		t.Fatal(err)
	}
	// Correct m1/q1 (new version) → retained > canonical.
	if _, _, err := st.ingest(ctx, "enso-bench", nil, []Attempt{{Benchmark: "gpqa_diamond", Item: "q1", Model: "m1", Correct: true, Answer: "A", Revision: "corrected", TS: 2}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ingest(ctx, "hanzo-engine", []Experiment{{ID: "kernel-perf:hanzo-engine:prefill", Kind: "kernel-perf", Subject: "hanzo-engine", Task: "prefill", Metric: "tok/s", Value: 94.4}}, nil); err != nil {
		t.Fatal(err)
	}

	ps, err := st.projectSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ProjectSummary{}
	for _, p := range ps {
		byName[p.Project] = p
	}
	if e := byName["enso-bench"]; e.Experiments != 1 || e.Attempts != 2 || e.AttemptsRetained != 3 || e.Models != 2 || e.Benchmarks != 1 || e.CostUSD != 1.5 {
		t.Fatalf("enso-bench summary wrong: %+v (want canonical att=2 retained=3)", e)
	}
	if k := byName["hanzo-engine"]; k.Experiments != 1 || len(k.Kinds) != 1 || k.Kinds[0] != "kernel-perf" {
		t.Fatalf("hanzo-engine summary wrong: %+v", k)
	}

	tot, err := st.totals(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if tot.Projects != 2 || tot.Experiments != 2 || tot.Attempts != 2 || tot.AttemptsRetained != 3 || tot.Models != 2 || tot.CostUSD != 1.5 || len(tot.ByKind) != 2 {
		t.Fatalf("org totals wrong: %+v", tot)
	}
}

// TestEmptyStoreTotals guards the empty-table aggregate (SUM over zero rows is NULL —
// must COALESCE to 0, not fail to scan).
func TestEmptyStoreTotals(t *testing.T) {
	st, ctx := newStore(t)
	tot, err := st.totals(ctx, "")
	if err != nil {
		t.Fatalf("empty totals: %v", err)
	}
	if tot.Experiments != 0 || tot.Attempts != 0 || tot.Projects != 0 {
		t.Fatalf("empty totals = %+v, want zeros", tot)
	}
	c, err := st.counts(ctx)
	if err != nil || (c != Counts{}) {
		t.Fatalf("empty counts=%+v err=%v", c, err)
	}
	ps, err := st.projectSummaries(ctx)
	if err != nil || len(ps) != 0 {
		t.Fatalf("empty projects=%v err=%v", ps, err)
	}
}

// ── SSRF gate (HIP-0512 §5) ───────────────────────────────────────────────────

func TestSSRFGate(t *testing.T) {
	blocked := []string{
		"https://127.0.0.1/v1", "https://[::1]/v1", "https://10.1.2.3/v1", "https://172.16.0.1/v1",
		"https://192.168.0.1/v1", "https://169.254.169.254/latest/meta-data", "https://[fd00:ec2::254]/",
		"https://0.0.0.0/v1", "http://1.1.1.1/v1", "ftp://1.1.1.1/v1", "not-a-url",
		"https://100.100.100.200/latest/meta-data", // Alibaba IMDS (CGNAT 100.64/10)
		"https://100.64.0.1/v1",                    // CGNAT
		"https://192.0.0.192/opc/v1/instance/",     // Oracle IMDS (IANA protocol assignments)
		"https://0.1.2.3/v1",                       // 0.0.0.0/8 this-network
		"https://255.255.255.255/v1",               // 240/4 reserved / broadcast
		"https://198.18.0.1/v1",                    // benchmarking range
	}
	for _, u := range blocked {
		if err := ssrfSafe(u); err == nil {
			t.Errorf("ssrfSafe(%q) = nil, want refusal", u)
		}
	}
	for _, u := range []string{"https://1.1.1.1/v1", "https://8.8.8.8/chat/completions"} {
		if err := ssrfSafe(u); err != nil {
			t.Errorf("ssrfSafe(%q) = %v, want nil (public https)", u, err)
		}
	}
}

// ── HTTP surface ──────────────────────────────────────────────────────────────

func mountResearch(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

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
		Experiments: []Experiment{{ID: "benchmark:grok-4.5:gpqa_diamond", Kind: "benchmark", Subject: "grok-4.5", Task: "gpqa_diamond", Metric: "accuracy", Value: 94.3, N: 198,
			GitSHA: "deadbeef", GitBranch: "main", LibVersions: json.RawMessage(`{"harness":"0.1.0"}`)}},
		Attempts: []Attempt{{Benchmark: "gpqa_diamond", Item: "q1", Model: "grok-4.5", Answer: "A", Correct: true}},
	}
	code, m := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	if code != http.StatusOK {
		t.Fatalf("POST status=%d body=%v", code, m)
	}
	if m["attempts_ingested"].(float64) != 1 || m["canonical_attempts"].(float64) != 1 || m["attempts_retained"].(float64) != 1 {
		t.Fatalf("ingest counts wrong: %v", m)
	}
	if m["project"] != "enso-bench" || m["rolled_up"].(bool) != false {
		t.Fatalf("project/rolled_up wrong: %v", m)
	}

	code, m = do(t, app, http.MethodGet, "/v1/research/experiments", "acme", "", nil)
	if code != http.StatusOK || m["total"].(float64) != 1 {
		t.Fatalf("GET acme total=%v", m["total"])
	}
	got := m["data"].([]any)[0].(map[string]any)
	if got["id"] != "benchmark:grok-4.5:gpqa_diamond" || got["value"].(float64) != 94.3 {
		t.Fatalf("round-trip mismatch: %v", got)
	}
	if got["project"] != "enso-bench" || got["status"] != "complete" || got["visibility"] != "private" || got["canonical"] != true {
		t.Fatalf("project/status/visibility/canonical wrong: %v", got)
	}
	if got["git_sha"] != "deadbeef" || got["lib_versions"].(map[string]any)["harness"] != "0.1.0" {
		t.Fatalf("provenance not round-tripped: %v", got)
	}

	code, m = do(t, app, http.MethodGet, "/v1/research/experiments", "other", "", nil)
	if code != http.StatusOK || m["total"].(float64) != 0 {
		t.Fatalf("tenant isolation broken: other org total=%v", m["total"])
	}
	code, _ = do(t, app, http.MethodGet, "/v1/research/experiments", "", "", nil)
	if code != http.StatusForbidden {
		t.Fatalf("no-principal GET status=%d, want 403", code)
	}
}

func TestHTTPProjectsAndTotals(t *testing.T) {
	app := mountResearch(t)
	do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", IngestRequest{
		Experiments: []Experiment{{ID: "benchmark:m1:hle", Kind: "benchmark", Subject: "m1", Task: "hle", Metric: "accuracy", Value: 70, N: 1, CostUSD: 2}},
		Attempts:    []Attempt{{Benchmark: "hle", Item: "q1", Model: "m1", Correct: true, Answer: "A"}},
	})
	do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "hanzo-engine", IngestRequest{
		Experiments: []Experiment{{ID: "kernel-perf:hanzo-engine:prefill", Kind: "kernel-perf", Subject: "hanzo-engine", Task: "prefill", Metric: "tok/s", Value: 94.4}},
	})
	code, m := do(t, app, http.MethodGet, "/v1/research/projects", "acme", "", nil)
	if code != http.StatusOK || m["total"].(float64) != 2 {
		t.Fatalf("/projects total=%v", m["total"])
	}
	code, m = do(t, app, http.MethodGet, "/v1/research/totals", "acme", "", nil)
	if code != http.StatusOK || m["projects"].(float64) != 2 || m["experiments"].(float64) != 2 || m["attempts"].(float64) != 1 || m["cost_usd"].(float64) != 2 || len(m["by_kind"].([]any)) != 2 {
		t.Fatalf("/totals wrong: %v", m)
	}
}

// TestHTTPIdempotentDoubleIngest: two identical POSTs leave retained + canonical identical.
func TestHTTPIdempotentDoubleIngest(t *testing.T) {
	app := mountResearch(t)
	batch := IngestRequest{Attempts: []Attempt{
		{Benchmark: "hle", Item: "i1", Model: "m", Answer: "x", Correct: true},
		{Benchmark: "hle", Item: "i2", Model: "m", Answer: "y", Correct: false},
	}}
	_, m1 := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	_, m2 := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	if m2["attempts_ingested"].(float64) != 0 {
		t.Fatalf("second POST ingested %v, want 0", m2["attempts_ingested"])
	}
	if m1["attempts_retained"] != m2["attempts_retained"] || m1["canonical_attempts"] != m2["canonical_attempts"] {
		t.Fatalf("counts drifted: %v vs %v", m1, m2)
	}
}

func TestHTTPSSRFRejected(t *testing.T) {
	app := mountResearch(t)
	batch := IngestRequest{Experiments: []Experiment{{ID: "kernel-perf:hanzo-engine:prefill", Kind: "kernel-perf", Subject: "hanzo-engine", Task: "prefill", Metric: "tok/s", Value: 94.4, Endpoint: "https://169.254.169.254/latest/meta-data"}}}
	code, m := do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", batch)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("SSRF status=%d body=%v, want 422", code, m)
	}
	_, g := do(t, app, http.MethodGet, "/v1/research/experiments", "acme", "", nil)
	if g["total"].(float64) != 0 {
		t.Fatalf("SSRF-rejected batch still wrote rows: total=%v", g["total"])
	}
}

// TestHTTPGrantSeparateFromUpload: upload is private; a distinct /grants call elevates.
func TestHTTPGrantSeparateFromUpload(t *testing.T) {
	app := mountResearch(t)
	do(t, app, http.MethodPost, "/v1/research/experiments", "acme", "enso-bench", IngestRequest{
		Experiments: []Experiment{{ID: "benchmark:m:hle", Kind: "benchmark", Subject: "m", Task: "hle", Metric: "accuracy", Value: 70}},
	})
	_, m := do(t, app, http.MethodGet, "/v1/research/experiments", "acme", "enso-bench", nil)
	if m["data"].([]any)[0].(map[string]any)["visibility"] != "private" {
		t.Fatalf("upload not private: %v", m["data"])
	}
	vis := "public"
	code, g := do(t, app, http.MethodPost, "/v1/research/grants", "acme", "enso-bench", GrantRequest{Project: "enso-bench", ID: "benchmark:m:hle", Visibility: &vis})
	if code != http.StatusOK || g["updated"].(float64) < 1 {
		t.Fatalf("grant status=%d body=%v", code, g)
	}
	_, m = do(t, app, http.MethodGet, "/v1/research/experiments", "acme", "enso-bench", nil)
	if m["data"].([]any)[0].(map[string]any)["visibility"] != "public" {
		t.Fatalf("grant did not take: %v", m["data"])
	}
}

// doRaw issues a request and returns status + raw body (for non-JSON responses like the
// hash-addressed blob).
func doRaw(t *testing.T, app *zip.App, method, path, org, project string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
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
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestHTTPArtifactsDiary: the research-diary surface — CONTENT-ADDRESSED (the server
// hashes the submitted bytes, a client-asserted mismatch is rejected, poisoning needs a
// preimage), idempotent, newest-first feed, private-by-default, byte retrieval by hash,
// and the separate visibility grant by sha256.
func TestHTTPArtifactsDiary(t *testing.T) {
	app := mountResearch(t)
	png := []byte("fake-png-bytes-of-the-board")
	b64 := base64.StdEncoding.EncodeToString(png)
	want := fmt.Sprintf("%x", sha256.Sum256(png))

	// POST the bytes (no client sha256) → server derives the identity + ref.
	code, m := do(t, app, http.MethodPost, "/v1/research/artifacts", "acme", "enso-bench",
		Artifact{Content: b64, Kind: "snapshot", RunID: "benchmark:m:gpqa_diamond", GitSHA: "deadbeef"})
	if code != http.StatusOK || m["created"] != true || m["sha256"] != want || m["ref"] != "sha256:"+want {
		t.Fatalf("POST artifact: %v (want server sha256 %s)", m, want)
	}
	// Re-POST identical bytes → no-op (hash-addressed idempotency).
	_, m2 := do(t, app, http.MethodPost, "/v1/research/artifacts", "acme", "enso-bench",
		Artifact{Content: b64, Kind: "snapshot", RunID: "benchmark:m:gpqa_diamond"})
	if m2["created"] != false {
		t.Fatalf("re-POST created=%v, want false", m2["created"])
	}
	// POISONING: a client-asserted sha256 that does NOT match the bytes is refused.
	poison, _ := do(t, app, http.MethodPost, "/v1/research/artifacts", "acme", "enso-bench",
		Artifact{Content: b64, SHA256: fmt.Sprintf("%064d", 0), Kind: "snapshot"})
	if poison != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched sha256 status=%d, want 422 (un-poisonable)", poison)
	}
	// Content is required — a manifest with no bytes cannot be content-addressed.
	nc, _ := do(t, app, http.MethodPost, "/v1/research/artifacts", "acme", "enso-bench", Artifact{Kind: "snapshot"})
	if nc != http.StatusUnprocessableEntity {
		t.Fatalf("no-content status=%d, want 422", nc)
	}
	// A newer report.
	rpt := []byte("# report\nauto-generated")
	do(t, app, http.MethodPost, "/v1/research/artifacts", "acme", "enso-bench",
		Artifact{Content: base64.StdEncoding.EncodeToString(rpt), Kind: "report", RunID: "benchmark:m:gpqa_diamond", TS: 200})

	// Diary feed newest-first, private, server-derived ref.
	code, g := do(t, app, http.MethodGet, "/v1/research/artifacts", "acme", "enso-bench", nil)
	if code != http.StatusOK || g["total"].(float64) != 2 {
		t.Fatalf("diary total=%v", g["total"])
	}
	head := g["data"].([]any)[0].(map[string]any)
	if head["visibility"] != "private" || head["retention_class"] != "raw-artifact" || !bytesHasPrefix(head["ref"], "sha256:") {
		t.Fatalf("diary head wrong: %v", head)
	}

	// Retrieve the snapshot bytes BY HASH — hash-addressed retrieval round-trips.
	rc, body := doRaw(t, app, http.MethodGet, "/v1/research/artifacts/"+want, "acme", "enso-bench")
	if rc != http.StatusOK || !bytes.Equal(body, png) {
		t.Fatalf("blob retrieval status=%d bytes-match=%v", rc, bytes.Equal(body, png))
	}
	// A DIFFERENT org cannot retrieve it — tenant isolation on the blob too.
	oc, _ := doRaw(t, app, http.MethodGet, "/v1/research/artifacts/"+want, "other", "enso-bench")
	if oc != http.StatusNotFound {
		t.Fatalf("cross-org blob status=%d, want 404", oc)
	}

	// Kind validation.
	bad, _ := do(t, app, http.MethodPost, "/v1/research/artifacts", "acme", "enso-bench", Artifact{Content: b64, Kind: "malware"})
	if bad != http.StatusUnprocessableEntity {
		t.Fatalf("bad kind status=%d, want 422", bad)
	}
	// Separate visibility grant by sha256.
	vis := "public"
	code, gr := do(t, app, http.MethodPost, "/v1/research/grants", "acme", "enso-bench", GrantRequest{Project: "enso-bench", SHA256: want, Visibility: &vis})
	if code != http.StatusOK || gr["updated"].(float64) != 1 {
		t.Fatalf("artifact grant status=%d body=%v", code, gr)
	}
}

func bytesHasPrefix(v any, p string) bool {
	s, _ := v.(string)
	return len(s) >= len(p) && s[:len(p)] == p
}
