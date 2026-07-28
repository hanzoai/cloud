// Package benchmark mounts the Hanzo Cloud /v1/benchmark/* surface: the native
// benchmark ARENA — run the top-N canonical public benchmarks against any model or
// endpoint, under ONE standardized harness, measure Hanzo's own models (enso, zen),
// and reconcile any external provider-reported claim against that measurement.
// Sibling to /v1/eval (eval = YOUR data + YOUR judge; benchmark =
// the canonical public tests, comparable + provenance-first + leaderboard).
//
// Provenance-first, never blended: a `published_claim` (what a vendor reports) and a
// `hanzo-measured` attempt (what OUR harness gets) are separate planes — the gap is
// the signal (some provider-reported claims run 3-13pp hot vs one standardized
// harness). The store is append-only; a re-scored label
// is a new score_event, never an overwrite.
//
// Mounted into the unified cloud binary via apps.go ({Name:"benchmark", Mount}); the
// Python enso-bench harness is the research prototype, THIS is the product surface.
package benchmark

import (
	"bufio"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Benchmark is one canonical, versioned public test. `Native` marks harness support
// today; the rest are adapter-pending on the same registry + provenance.
type Benchmark struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Axis   string `json:"axis"`
	Items  int    `json:"items,omitempty"`
	Native bool   `json:"native"`
	Source string `json:"source"`
}

// catalog is the top-14: the set every major provider reports, run under one
// standardized harness for apples-to-apples comparison. Mirrors registry/benchmarks.yaml.
var catalog = []Benchmark{
	{"gpqa_diamond", "GPQA-Diamond", "science-reasoning", 198, true, "hendrydong/gpqa_diamond_mc"},
	{"humanitys_last_exam", "Humanity's Last Exam", "frontier-reasoning", 500, true, "macabdul9/hle_text_only"},
	{"livecodebench", "LiveCodeBench (v6)", "competitive-coding", 0, true, "livecodebench/code_generation_lite test6"},
	{"livecodebench_pro", "LiveCodeBench Pro", "competitive-coding-hard", 0, false, "LiveCodeBench Pro"},
	{"swe_bench_verified", "SWE-Bench Verified", "software-engineering", 0, false, "SWE-bench Verified (bash-only)"},
	{"swe_bench_pro", "SWE-Bench Pro", "software-engineering-hard", 0, false, "scaleapi/SWE-bench_Pro-os"},
	{"terminal_bench_2_1", "Terminal Bench 2.1", "agentic-terminal", 0, false, "Terminal-Bench 2.1"},
	{"mmlu_pro", "MMLU-Pro", "broad-knowledge", 0, false, "TIGER-Lab/MMLU-Pro"},
	{"aime_2025", "AIME 2025 (MathArena)", "competition-math", 0, false, "MathArena AIME 2025"},
	{"mmmu_pro", "MMMU-Pro", "multimodal-reasoning", 0, false, "MMMU-Pro"},
	{"charxiv_reasoning", "CharXiv (reasoning)", "chart-vision-reasoning", 0, true, "CharXiv reasoning split"},
	{"tau2_bench", "τ²-Bench", "agentic-tool-use", 0, false, "tau2-bench"},
	{"scicode", "SciCode", "scientific-coding", 0, false, "SciCode"},
	{"mrcr_v2", "MRCRv2", "long-context", 0, false, "MRCRv2"},
}

// publishedClaim: a provider-reported aggregate, kept separate from measured. protocol
// records HOW they scored (single-attempt / pass@k / agentic) — apples-to-apples only
// on the default view; claims revealed on toggle.
type publishedClaim struct {
	Benchmark string  `json:"benchmark"`
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	Score     float64 `json:"score"`
	Protocol  string  `json:"protocol"`
	Source    string  `json:"source"`
}

// published holds external provider-reported claims as attributed DATA, each row
// carrying the source it was read from, reconciled against hanzo-measured attempts
// under one harness and never blended into them. Provider and model are data fields:
// they name the system a claim belongs to, which is what lets a reader check the
// citation and what joins a claim to the attempts measured for that same model.
var published = []publishedClaim{
	{"gpqa_diamond", "sakana", "fugu-ultra", 95.5, "agentic-orchestration", "Sakana Fugu Technical Report 2026"},
	{"swe_bench_pro", "sakana", "fugu-ultra", 73.7, "agentic", "Sakana Fugu Technical Report 2026"},
	{"terminal_bench_2_1", "sakana", "fugu-ultra", 82.1, "agentic", "Sakana Fugu Technical Report 2026"},
	{"livecodebench_pro", "sakana", "fugu-ultra", 90.8, "agentic", "Sakana Fugu Technical Report 2026"},
	{"gpqa_diamond", "xai", "grok-4.5", 94.3, "provider-reported", "provider card"},
	{"gpqa_diamond", "anthropic", "fable-5", 94.6, "provider-reported", "provider card"},
	{"gpqa_diamond", "openai", "gpt-5.5", 93.6, "provider-reported", "provider card"},
}

// attempt is one measured model attempt on one item, append-only. correct is derived
// under a grader revision (a re-score adds a row, never mutates this one).
type attempt struct {
	Benchmark string `json:"benchmark"`
	ID        string `json:"id"`
	Model     string `json:"model"`
	Correct   bool   `json:"correct"`
	Answer    string `json:"answer"`
}

type state struct {
	store AttemptStore // the durability seam — fileStore (local dev) or cloud backend
}

// Mount is the subsystem entrypoint (registered in apps.go).
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "benchmark", build, routes)
}

func build(b cloud.Base) (state, error) {
	// Local-dev backend today; the cloud backend (relational + object store) swaps in
	// behind AttemptStore with no handler change (the architecture: prod is stateless,
	// never pod-local). Attempts import idempotently by stable id.
	store := newFileStore(b.DataDir)
	b.Log.Info("benchmark arena", "prefix", "/v1/benchmark", "benchmarks", len(catalog), "attempts", len(store.Attempts("")))
	return state{store: store}, nil
}

// loadAttempts reads the append-only measured plane (one JSONL per model). Best-effort:
// a missing dir yields an empty (honest) leaderboard, never an error.
func loadAttempts(dir string) []attempt {
	var out []attempt
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return out
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
		for sc.Scan() {
			var a attempt
			if json.Unmarshal(sc.Bytes(), &a) == nil && a.ID != "" && a.Model != "" {
				out = append(out, a)
			}
		}
		fh.Close()
	}
	return out
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/benchmark")
	g.Get("/catalog", cloud.Handle(s, getCatalog))         // the top-14 canonical set
	g.Get("/leaderboard", cloud.Handle(s, getLeaderboard)) // per-model measured ∥ published
	g.Get("/compare", cloud.Handle(s, getCompare))         // paired common-set (rescue/damage/McNemar)
	g.Post("/runs", cloud.Handle(s, postRun))              // run a benchmark against a model/endpoint
	presetRoutes(g, s)                                     // design-your-own router blend (enso-<name>)
}

func getCatalog(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{"data": catalog, "total": len(catalog)})
}

// LeaderRow layers the two planes for one model, coverage-aware, never blended.
type LeaderRow struct {
	Model     string   `json:"model"`
	Measured  *float64 `json:"measured"`  // hanzo-measured accuracy % (nil if unrun)
	N         int      `json:"n"`         // coverage — NEVER compare across different n
	Published *float64 `json:"published"` // provider-claimed % (nil if none)
	Gap       *float64 `json:"gap"`       // published − measured (the arena signal)
	Protocol  string   `json:"protocol,omitempty"`
}

func getLeaderboard(s *cloud.Service[state], c *zip.Ctx) error {
	bench := strings.TrimSpace(c.Query("benchmark"))
	if bench == "" {
		bench = "gpqa_diamond"
	}
	return c.JSON(http.StatusOK, map[string]any{"benchmark": bench, "rows": computeLeaderboard(s.State.store.Attempts(bench), bench)})
}

// computeLeaderboard is the pure aggregation (testable): per-model measured accuracy
// (coverage-aware) layered with the published claim, gap = published − measured. Never
// blended; a model with only a claim shows measured=nil, and vice versa.
func computeLeaderboard(attempts []attempt, bench string) []LeaderRow {
	type acc struct{ ok, n int }
	m := map[string]*acc{}
	for _, a := range attempts {
		if a.Benchmark != bench || a.Answer == "" {
			continue
		}
		if m[a.Model] == nil {
			m[a.Model] = &acc{}
		}
		m[a.Model].n++
		if a.Correct {
			m[a.Model].ok++
		}
	}
	claim := map[string]publishedClaim{}
	for _, p := range published {
		if p.Benchmark == bench {
			claim[p.Model] = p
		}
	}
	models := map[string]bool{}
	for k := range m {
		models[k] = true
	}
	for k := range claim {
		models[k] = true
	}
	var rows []LeaderRow
	for model := range models {
		r := LeaderRow{Model: model}
		if a := m[model]; a != nil && a.n > 0 {
			v := float64(a.ok) / float64(a.n) * 100
			r.Measured, r.N = &v, a.n
		}
		if p, ok := claim[model]; ok {
			v := p.Score
			r.Published, r.Protocol = &v, p.Protocol
		}
		if r.Measured != nil && r.Published != nil {
			g := *r.Published - *r.Measured
			r.Gap = &g
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		mi, mj := -1.0, -1.0
		if rows[i].Measured != nil {
			mi = *rows[i].Measured
		}
		if rows[j].Measured != nil {
			mj = *rows[j].Measured
		}
		return mi > mj
	})
	return rows
}

// getCompare: the ONLY valid arm-vs-arm test — paired on items BOTH completed, with
// rescue/damage and an exact-McNemar p. Prevents the subset-artifact (comparing a
// model's easy subset against another's full run).
func getCompare(s *cloud.Service[state], c *zip.Ctx) error {
	bench := strings.TrimSpace(c.Query("benchmark"))
	if bench == "" {
		bench = "gpqa_diamond"
	}
	a, b := strings.TrimSpace(c.Query("a")), strings.TrimSpace(c.Query("b"))
	if a == "" || b == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "compare needs ?a= and ?b= model ids"})
	}
	return c.JSON(http.StatusOK, computeCompare(s.State.store.Attempts(bench), bench, a, b))
}

// computeCompare is the pure paired common-set test (testable): rescue/damage on items
// BOTH arms completed + exact McNemar. The only valid arm-vs-arm comparison.
func computeCompare(attempts []attempt, bench, a, b string) map[string]any {
	ao, bo := map[string]bool{}, map[string]bool{}
	for _, at := range attempts {
		if at.Benchmark != bench || at.Answer == "" {
			continue
		}
		switch at.Model {
		case a:
			ao[at.ID] = at.Correct
		case b:
			bo[at.ID] = at.Correct
		}
	}
	var nCommon, aOK, bOK, aOnly, bOnly int
	for id, ac := range ao {
		bc, ok := bo[id]
		if !ok {
			continue
		}
		nCommon++
		if ac {
			aOK++
		}
		if bc {
			bOK++
		}
		if ac && !bc {
			aOnly++
		}
		if bc && !ac {
			bOnly++
		}
	}
	return map[string]any{
		"benchmark": bench, "a": a, "b": b, "n_common": nCommon,
		"a_correct": aOK, "b_correct": bOK,
		"rescue_a_over_b": aOnly, "rescue_b_over_a": bOnly, "net_a_minus_b": aOnly - bOnly,
		"mcnemar_p": mcnemarExact(aOnly, bOnly),
	}
}

// mcnemarExact: two-sided exact binomial p on the discordant pairs (b,c).
func mcnemarExact(b, cc int) float64 {
	n := b + cc
	if n == 0 {
		return 1.0
	}
	lo := b
	if cc < lo {
		lo = cc
	}
	var tail float64
	for k := 0; k <= lo; k++ {
		tail += binom(n, k)
	}
	p := tail / math.Pow(2, float64(n)) * 2
	if p > 1 {
		p = 1
	}
	return math.Round(p*10000) / 10000
}

func binom(n, k int) float64 {
	if k < 0 || k > n {
		return 0
	}
	res := 1.0
	for i := 0; i < k; i++ {
		res = res * float64(n-i) / float64(i+1)
	}
	return res
}

// RunRequest: run a benchmark against a model/endpoint. target is a catalog model id
// OR a BYO OpenAI-compatible endpoint+key (the cloud offering: benchmark YOUR model).
// The runner caches before spend (skip any (item, model) already attempted) and
// records provenance. Execution is the async worker (follow-on); this admits + queues.
type RunRequest struct {
	Benchmarks []string `json:"benchmarks"`
	Model      string   `json:"model"`
	Endpoint   string   `json:"endpoint,omitempty"`
	Attempts   int      `json:"attempts,omitempty"`
}

func postRun(s *cloud.Service[state], c *zip.Ctx) error {
	var req RunRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid run request"})
	}
	if req.Model == "" && req.Endpoint == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "run needs a model id or a BYO endpoint"})
	}
	if len(req.Benchmarks) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "run needs at least one benchmark id"})
	}
	// Validate benchmark ids against the catalog.
	valid := map[string]bool{}
	for _, b := range catalog {
		valid[b.ID] = true
	}
	var unknown []string
	for _, b := range req.Benchmarks {
		if !valid[b] {
			unknown = append(unknown, b)
		}
	}
	if len(unknown) > 0 {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{"error": "unknown benchmarks", "unknown": unknown})
	}
	return c.JSON(http.StatusAccepted, map[string]any{
		"status": "queued", "model": req.Model, "endpoint": req.Endpoint,
		"benchmarks": req.Benchmarks,
		"note":       "cache-before-spend: already-attempted (item,model) pairs are skipped; results land in the leaderboard.",
	})
}
