package eval

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// TestTenantIgnoresClientProjectID pins the cross-tenant isolation invariant
// (RED MED-1): tenant() scopes EVERY metastore query + telemetry read, and MUST
// use ONLY the sanitized org (c.Org(), set by SanitizeIdentity from the validated
// bearer owner), NEVER the client-controllable X-Project-Id. A forged
// `X-Project-Id: victim-org` must not change the resolved tenant — otherwise a
// caller reads another org's eval datasets/scores.
func TestTenantIgnoresClientProjectID(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/echo-tenant", func(c *zip.Ctx) error {
		org, _ := tenant(c)
		return c.String(http.StatusOK, org)
	})

	// user is the VALIDATED principal signal (X-User-Id, set only by
	// SanitizeIdentity from a verified token). When empty, tenant() must fail
	// closed regardless of X-Org-Id (Red HIGH: the restored X-Org-Id is untrusted
	// without a validated principal).
	call := func(user, orgHeader, projectHeader string) string {
		req := httptest.NewRequest(http.MethodGet, "/echo-tenant", nil)
		if user != "" {
			req.Header.Set("X-User-Id", user) // in prod: minted by SanitizeIdentity from a verified token
		}
		if orgHeader != "" {
			req.Header.Set("X-Org-Id", orgHeader) // in prod: minted by SanitizeIdentity
		}
		if projectHeader != "" {
			req.Header.Set("X-Project-Id", projectHeader) // client-controllable sub-scope
		}
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	// A validated principal: forged X-Project-Id is ignored, org is authoritative.
	if got := call("u1", "maxpower", "victim-org"); got != "maxpower" {
		t.Fatalf("forged X-Project-Id leaked into tenant: got %q, want %q", got, "maxpower")
	}
	if got := call("u1", "maxpower", ""); got != "maxpower" {
		t.Fatalf("tenant without project: got %q, want %q", got, "maxpower")
	}
	// X-Project-Id alone (no org) never becomes the tenant.
	if got := call("u1", "", "victim-org"); got != "" {
		t.Fatalf("X-Project-Id alone became the tenant: got %q, want empty", got)
	}
	// Red HIGH: NO validated principal (empty X-User-Id) — even with a full
	// X-Org-Id — yields NO tenant. The bearer-less/opaque-key forged-org path is
	// closed at the tenant gate.
	if got := call("", "victim", "victim"); got != "" {
		t.Fatalf("no-principal forged X-Org-Id became the tenant: got %q, want empty", got)
	}
}

// ── pure helpers retained from the proxy (still used by the native runner) ────

func TestLoopbackBase(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://127.0.0.1:8080",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		"127.0.0.1:8000": "http://127.0.0.1:8000",
		"":               "http://127.0.0.1:8080",
		"garbage":        "http://127.0.0.1:8080",
	}
	for in, want := range cases {
		if got := loopbackBase(in); got != want {
			t.Errorf("loopbackBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildMessages(t *testing.T) {
	got := buildMessages("hello")
	if len(got) != 1 || got[0]["role"] != "user" || got[0]["content"] != "hello" {
		t.Fatalf("string input: got %+v", got)
	}
	in := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "s"},
		map[string]any{"role": "user", "content": "u"},
	}}
	got = buildMessages(in)
	if len(got) != 2 || got[0]["role"] != "system" || got[1]["content"] != "u" {
		t.Fatalf("messages passthrough: got %+v", got)
	}
	got = buildMessages(map[string]any{"prompt": "p"})
	if len(got) != 1 || got[0]["role"] != "user" || !strings.Contains(got[0]["content"].(string), "prompt") {
		t.Fatalf("object fallback: got %+v", got)
	}
}

func TestParseJudge(t *testing.T) {
	t.Run("clean json", func(t *testing.T) {
		v, r, err := parseJudge(`{"score": 0.8, "reasoning": "good"}`)
		if err != nil || v != 0.8 || r != "good" {
			t.Fatalf("got %v %q %v", v, r, err)
		}
	})
	t.Run("json embedded in prose", func(t *testing.T) {
		v, r, err := parseJudge("Here is my verdict: {\"score\": 1, \"reasoning\": \"x\"} done")
		if err != nil || v != 1 || r != "x" {
			t.Fatalf("got %v %q %v", v, r, err)
		}
	})
	t.Run("bare float", func(t *testing.T) {
		v, _, err := parseJudge("0.5")
		if err != nil || v != 0.5 {
			t.Fatalf("got %v %v", v, err)
		}
	})
	t.Run("clamped above 1", func(t *testing.T) {
		v, _, err := parseJudge(`{"score": 1.7}`)
		if err != nil || v != 1 {
			t.Fatalf("got %v %v", v, err)
		}
	})
	t.Run("clamped below 0", func(t *testing.T) {
		v, _, err := parseJudge(`{"score": -3}`)
		if err != nil || v != 0 {
			t.Fatalf("got %v %v", v, err)
		}
	})
	t.Run("non-finite is an error, never a fabricated score", func(t *testing.T) {
		for _, bad := range []string{`{"score": 1e400}`, `{"score": -1e400}`} {
			if _, _, err := parseJudge(bad); err == nil {
				t.Fatalf("expected error for non-finite judge reply %q", bad)
			}
		}
	})
	t.Run("unparseable is an error, not a fake score", func(t *testing.T) {
		if _, _, err := parseJudge("the model refused"); err == nil {
			t.Fatal("expected error for unparseable judge reply")
		}
	})
}

func TestNormalizeJudge(t *testing.T) {
	d := normalizeJudge(nil, "gpt-4o-mini")
	if d.Model != "gpt-4o-mini" || d.Name != "llm-judge" || d.Criteria == "" {
		t.Fatalf("defaults: %+v", d)
	}
	o := normalizeJudge(&judgeSpec{Name: "acc", Criteria: "match exactly"}, "m")
	if o.Model != "m" || o.Name != "acc" || o.Criteria != "match exactly" {
		t.Fatalf("overrides: %+v", o)
	}
	// A judge name becomes a stored score name — a bad name is rejected and the
	// safe default kept, never persisted as-is.
	bad := normalizeJudge(&judgeSpec{Name: "../etc/passwd"}, "m")
	if bad.Name != "llm-judge" {
		t.Fatalf("bad judge name must fall back to default, got %q", bad.Name)
	}
}

func TestClampAndAsText(t *testing.T) {
	if clamp01(0.3) != 0.3 || clamp01(-1) != 0 || clamp01(9) != 1 {
		t.Fatal("clamp01 wrong")
	}
	if asText("s") != "s" {
		t.Fatal("asText string")
	}
	if asText(map[string]any{"a": 1}) != `{"a":1}` {
		t.Fatalf("asText json = %q", asText(map[string]any{"a": 1}))
	}
	if asText(nil) != "" {
		t.Fatal("asText nil")
	}
}

func TestGenUUIDShape(t *testing.T) {
	id := genUUID()
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("uuid shape: %q", id)
	}
	if id[14] != '4' {
		t.Fatalf("uuid version: %q", id)
	}
	if id == genUUID() {
		t.Fatal("uuid not unique")
	}
}

func TestNameValidation(t *testing.T) {
	for _, bad := range []string{"", "../etc", "a b", "name!", strings.Repeat("x", 65), "/abs"} {
		if nameRE.MatchString(bad) {
			t.Fatalf("nameRE should reject %q", bad)
		}
	}
	for _, good := range []string{"greeting", "greet.v2", "a_b-c", "X1", "acc"} {
		if !nameRE.MatchString(good) {
			t.Fatalf("nameRE should accept %q", good)
		}
	}
}

// ── metastore ────────────────────────────────────────────────────────────────

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "evals.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkDataset(org, name string) Dataset {
	return Dataset{ID: org + "-" + name + "-id", Org: org, Name: name, Metadata: "{}", UpdatedAt: time.Now().Unix()}
}

func TestDatasetUpsertAndGet(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d, err := s.UpsertDataset(ctx, mkDataset("maxpower", "qa"))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if d.CreatedAt == 0 {
		t.Fatal("createdAt not stamped")
	}
	d2 := mkDataset("maxpower", "qa")
	d2.Description = "updated"
	d2.UpdatedAt = d.UpdatedAt + 10
	got, err := s.UpsertDataset(ctx, d2)
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got.CreatedAt != d.CreatedAt {
		t.Fatalf("createdAt must be stable: %d vs %d", got.CreatedAt, d.CreatedAt)
	}
	if got.Description != "updated" {
		t.Fatalf("description not updated: %q", got.Description)
	}
}

// TestMetastoreTenantIsolation is the security invariant: one org can never read,
// list, or delete another org's datasets/items, even with an identical name.
func TestMetastoreTenantIsolation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if _, err := s.UpsertDataset(ctx, mkDataset("maxpower", "shared")); err != nil {
		t.Fatalf("seed maxpower: %v", err)
	}
	if _, err := s.UpsertDataset(ctx, mkDataset("acme", "shared")); err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	mp, _ := s.ListDatasets(ctx, "maxpower", 100)
	ac, _ := s.ListDatasets(ctx, "acme", 100)
	if len(mp) != 1 || len(ac) != 1 {
		t.Fatalf("each org must see exactly its own: mp=%d ac=%d", len(mp), len(ac))
	}
	now := time.Now().Unix()
	if _, err := s.PutItem(ctx, DatasetItem{ID: "i1", Org: "maxpower", Dataset: "shared", Input: `"mp"`, Metadata: "{}", Status: "ACTIVE", UpdatedAt: now}); err != nil {
		t.Fatalf("mp item: %v", err)
	}
	if _, err := s.PutItem(ctx, DatasetItem{ID: "i1", Org: "acme", Dataset: "shared", Input: `"ac"`, Metadata: "{}", Status: "ACTIVE", UpdatedAt: now}); err != nil {
		t.Fatalf("ac item: %v", err)
	}
	mpItems, _ := s.ListItems(ctx, "maxpower", "shared", true, 100)
	if len(mpItems) != 1 || mpItems[0].Input != `"mp"` {
		t.Fatalf("maxpower items leaked or missing: %+v", mpItems)
	}
	acItems, _ := s.ListItems(ctx, "acme", "shared", true, 100)
	if len(acItems) != 1 || acItems[0].Input != `"ac"` {
		t.Fatalf("acme items leaked or missing: %+v", acItems)
	}
	// Reading acme's item id under maxpower's org returns maxpower's row, never acme's.
	got, err := s.GetItem(ctx, "maxpower", "i1")
	if err != nil || got.Input != `"mp"` {
		t.Fatalf("cross-org item read: got %q err %v", got.Input, err)
	}
	deleted, err := s.DeleteDataset(ctx, "acme", "shared")
	if err != nil || !deleted {
		t.Fatalf("acme delete own: %v deleted=%v", err, deleted)
	}
	if _, err := s.GetDataset(ctx, "maxpower", "shared"); err != nil {
		t.Fatalf("maxpower dataset must survive acme's delete: %v", err)
	}
	if mpItems, _ := s.ListItems(ctx, "maxpower", "shared", false, 100); len(mpItems) != 1 {
		t.Fatalf("maxpower items must survive acme's delete: %+v", mpItems)
	}
}

func TestItemCannotMoveDataset(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	if _, err := s.PutItem(ctx, DatasetItem{ID: "x", Org: "o", Dataset: "d1", Input: `"a"`, Metadata: "{}", Status: "ACTIVE", UpdatedAt: now}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.PutItem(ctx, DatasetItem{ID: "x", Org: "o", Dataset: "d2", Input: `"b"`, Metadata: "{}", Status: "ACTIVE", UpdatedAt: now + 1}); err != errConflict {
		t.Fatalf("re-home item want errConflict, got %v", err)
	}
}

func TestScoreConfigRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	lo, hi := 0.0, 10.0
	cfg, err := s.UpsertScoreConfig(ctx, ScoreConfig{
		ID: "c1", Org: "o", Name: "quality", DataType: "NUMERIC",
		MinValue: &lo, MaxValue: &hi, UpdatedAt: time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if cfg.MinValue == nil || *cfg.MinValue != 0 || cfg.MaxValue == nil || *cfg.MaxValue != 10 {
		t.Fatalf("min/max not persisted: %+v", cfg)
	}
	got, err := s.GetScoreConfig(ctx, "o", "quality")
	if err != nil || got.MaxValue == nil || *got.MaxValue != 10 {
		t.Fatalf("get: %+v err %v", got, err)
	}
	cat, err := s.UpsertScoreConfig(ctx, ScoreConfig{
		ID: "c2", Org: "o", Name: "tone", DataType: "CATEGORICAL",
		Categories: []string{"good", "bad"}, UpdatedAt: time.Now().Unix(),
	})
	if err != nil || len(cat.Categories) != 2 {
		t.Fatalf("categorical: %+v err %v", cat, err)
	}
}

// TestRunConcurrencyCap pins the per-org run limiter (Red MED): an org can hold
// at most maxConcurrentRunsPerOrg slots; the next acquire is refused (→ 429 at the
// HTTP layer); releasing frees a slot. Uses a dedicated org so it can't be
// perturbed by other tests sharing the global limiter.
func TestRunConcurrencyCap(t *testing.T) {
	const org = "concurrency-cap-probe"
	acquired := 0
	defer func() {
		for i := 0; i < acquired; i++ {
			releaseRunSlot(org)
		}
	}()
	for i := 0; i < maxConcurrentRunsPerOrg; i++ {
		if !acquireRunSlot(org) {
			t.Fatalf("slot %d within cap should acquire", i)
		}
		acquired++
	}
	if acquireRunSlot(org) {
		acquired++
		t.Fatalf("acquiring beyond the cap (%d) must be refused", maxConcurrentRunsPerOrg)
	}
	// Freeing one slot makes exactly one available again.
	releaseRunSlot(org)
	acquired--
	if !acquireRunSlot(org) {
		t.Fatal("after a release, a slot must be available")
	}
	acquired++
}

func TestRunRecordRollup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r, err := s.UpsertRun(ctx, DatasetRun{ID: "r1", Org: "o", Dataset: "d", Name: "run-1", Model: "m", Items: 5, Scored: 4, AvgScore: 0.75, UpdatedAt: time.Now().Unix()})
	if err != nil {
		t.Fatalf("upsert run: %v", err)
	}
	if r.AvgScore != 0.75 {
		t.Fatalf("avg not stored: %v", r.AvgScore)
	}
	runs, err := s.ListRuns(ctx, "o", "d", 100)
	if err != nil || len(runs) != 1 || runs[0].Name != "run-1" {
		t.Fatalf("list runs: %+v err %v", runs, err)
	}
	other, _ := s.ListRuns(ctx, "other", "", 100)
	if len(other) != 0 {
		t.Fatalf("run leaked across org: %+v", other)
	}
}
