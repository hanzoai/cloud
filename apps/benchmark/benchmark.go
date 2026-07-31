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
	"context"
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

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

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

// ops binds the arena's state to the typed handlers: a TypedHandler takes only
// (context, *In), so the store arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	z := cloud.ZipApp(app)
	if z == nil {
		panic("benchmark.routes: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	zip.Get(z, "/v1/benchmark/catalog", o.catalog, opID("benchmarkCatalog"))             // the top-14 canonical set
	zip.Get(z, "/v1/benchmark/leaderboard", o.leaderboard, opID("benchmarkLeaderboard")) // per-model measured ∥ published
	zip.Get(z, "/v1/benchmark/compare", o.compare, opID("benchmarkCompare"))             // paired common-set (rescue/damage/McNemar)
	zip.Post(z, "/v1/benchmark/runs", o.queueRun, opID("benchmarkRun"), zip.WithStatus(http.StatusAccepted))
	presetRoutes(z, o) // design-your-own router blend (enso-<name>)
}

// opID is the per-route stable operation id every arena declaration carries — the
// name the OpenAPI document, the MCP tool and the CLI command all take. The summary
// is NOT set here: cmd/zipdoc lifts it from the handler's own doc comment.
func opID(id string) zip.OpOption { return zip.WithOperationID(id) }

// Catalog is the canonical benchmark set the arena runs.
type Catalog struct {
	// Data is every benchmark in the catalog, in registry order.
	Data []Benchmark `json:"data"`
	// Total is how many benchmarks the catalog holds.
	Total int `json:"total"`
}

// catalog returns the canonical public benchmark set the arena runs.
// That is the top-14 every major provider reports, under one standardized harness.
// Static registry data: no measurement is read.
//
// Response: {"data": [{"id": "gpqa_diamond", "title": "GPQA-Diamond", "axis": "science-reasoning", "items": 198, "native": true, "source": "hendrydong/gpqa_diamond_mc"}], "total": 14}
func (o ops) catalog(ctx context.Context, _ *struct{}) (*Catalog, error) {
	return &Catalog{Data: catalog, Total: len(catalog)}, nil
}

// LeaderRow layers the two planes for one model, coverage-aware, never blended.
type LeaderRow struct {
	// Model is the model id the row aggregates.
	Model string `json:"model"`
	// Measured is the hanzo-measured accuracy %, null when the model was not run.
	Measured *float64 `json:"measured"`
	// N is the coverage (items measured) — NEVER compare rows across different n.
	N int `json:"n"`
	// Published is the provider-claimed %, null when no claim is on file.
	Published *float64 `json:"published"`
	// Gap is published − measured, the arena signal. Null unless both planes exist.
	Gap *float64 `json:"gap"`
	// Protocol is how the provider scored their claim (single-attempt/pass@k/agentic).
	Protocol string `json:"protocol,omitempty"`
}

// BenchmarkQuery selects which benchmark a read aggregates.
type BenchmarkQuery struct {
	// Benchmark is a catalog benchmark id; empty means gpqa_diamond.
	Benchmark string `json:"benchmark"`
}

// Leaderboard is one benchmark's per-model board.
type Leaderboard struct {
	// Benchmark is the benchmark the rows were aggregated for.
	Benchmark string `json:"benchmark"`
	// Rows is one row per model, sorted by measured accuracy descending.
	Rows []LeaderRow `json:"rows"`
}

// leaderboard aggregates one benchmark's measured attempts per model and layers the
// provider-published claim beside them, coverage-aware and never blended: gap is
// published − measured, and a model with only one of the two planes shows null for
// the other.
//
// Example: {"benchmark": "gpqa_diamond"}
// Response: {"benchmark": "gpqa_diamond", "rows": [{"model": "grok-4.5", "measured": 88.4, "n": 198, "published": 94.3, "gap": 5.9, "protocol": "provider-reported"}]}
func (o ops) leaderboard(ctx context.Context, in *BenchmarkQuery) (*Leaderboard, error) {
	bench := strings.TrimSpace(in.Benchmark)
	if bench == "" {
		bench = "gpqa_diamond"
	}
	return &Leaderboard{Benchmark: bench, Rows: computeLeaderboard(o.s.State.store.Attempts(bench), bench)}, nil
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

// CompareQuery names the two arms to test against each other.
type CompareQuery struct {
	// Benchmark is a catalog benchmark id; empty means gpqa_diamond.
	Benchmark string `json:"benchmark"`
	// A is the first model id. Required.
	A string `json:"a" validate:"required"`
	// B is the second model id. Required.
	B string `json:"b" validate:"required"`
}

// Comparison is the paired common-set test between two arms.
type Comparison struct {
	// Benchmark is the benchmark both arms were measured on.
	Benchmark string `json:"benchmark"`
	// A and B are the two model ids compared.
	A string `json:"a"`
	B string `json:"b"`
	// NCommon is how many items BOTH arms completed — the only valid denominator.
	NCommon int `json:"n_common"`
	// ACorrect and BCorrect are each arm's correct count on the common set.
	ACorrect int `json:"a_correct"`
	BCorrect int `json:"b_correct"`
	// RescueAOverB is items A got right and B got wrong; RescueBOverA the reverse.
	RescueAOverB int `json:"rescue_a_over_b"`
	RescueBOverA int `json:"rescue_b_over_a"`
	// NetAMinusB is rescue_a_over_b − rescue_b_over_a.
	NetAMinusB int `json:"net_a_minus_b"`
	// McnemarP is the two-sided exact McNemar p over the discordant pairs.
	McnemarP float64 `json:"mcnemar_p"`
}

// compare runs the ONLY valid arm-vs-arm test, paired on a common item set. It
// reports each arm's rescues over the other on the items BOTH models completed, plus
// an exact two-sided McNemar p. Pairing is what prevents the subset artifact — one
// model's easy subset scored against another's full run.
//
// Example: {"benchmark": "gpqa_diamond", "a": "grok-4.5", "b": "gpt-5.6-sol"}
// Response: {"benchmark": "gpqa_diamond", "a": "grok-4.5", "b": "gpt-5.6-sol", "n_common": 196, "a_correct": 173, "b_correct": 168, "rescue_a_over_b": 14, "rescue_b_over_a": 9, "net_a_minus_b": 5, "mcnemar_p": 0.4049}
func (o ops) compare(ctx context.Context, in *CompareQuery) (*Comparison, error) {
	bench := strings.TrimSpace(in.Benchmark)
	if bench == "" {
		bench = "gpqa_diamond"
	}
	a, b := strings.TrimSpace(in.A), strings.TrimSpace(in.B)
	if a == "" || b == "" {
		return nil, zip.ErrBadRequest("compare needs ?a= and ?b= model ids")
	}
	out := computeCompare(o.s.State.store.Attempts(bench), bench, a, b)
	return &out, nil
}

// computeCompare is the pure paired common-set test (testable): rescue/damage on items
// BOTH arms completed + exact McNemar. The only valid arm-vs-arm comparison.
func computeCompare(attempts []attempt, bench, a, b string) Comparison {
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
	return Comparison{
		Benchmark: bench, A: a, B: b, NCommon: nCommon,
		ACorrect: aOK, BCorrect: bOK,
		RescueAOverB: aOnly, RescueBOverA: bOnly, NetAMinusB: aOnly - bOnly,
		McnemarP: mcnemarExact(aOnly, bOnly),
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
	// Benchmarks is the catalog benchmark ids to run. At least one is required.
	Benchmarks []string `json:"benchmarks"`
	// Model is the model id to measure. Required unless Endpoint is given.
	Model string `json:"model"`
	// Endpoint is a BYO OpenAI-compatible base URL to measure instead of a
	// catalog model.
	Endpoint string `json:"endpoint,omitempty"`
	// Attempts caps how many items to attempt; 0 means the whole benchmark.
	Attempts int `json:"attempts,omitempty"`
}

// RunQueued acknowledges an admitted run.
type RunQueued struct {
	// Status is "queued".
	Status string `json:"status"`
	// Model and Endpoint echo the admitted target.
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
	// Benchmarks echoes the admitted benchmark ids.
	Benchmarks []string `json:"benchmarks"`
	// Note states the caching rule the runner applies.
	Note string `json:"note"`
}

// queueRun admits and queues a measurement run, answering 202. The target is a
// catalog model or a BYO OpenAI-compatible endpoint. Every benchmark id is validated
// against the catalog first, so an unknown id is rejected rather than silently
// dropped. Execution is the async worker; nothing is measured on this call.
//
// Example: {"benchmarks": ["gpqa_diamond"], "model": "zen5-pro", "attempts": 198}
func (o ops) queueRun(ctx context.Context, in *RunRequest) (*RunQueued, error) {
	if in.Model == "" && in.Endpoint == "" {
		return nil, zip.ErrBadRequest("run needs a model id or a BYO endpoint")
	}
	if len(in.Benchmarks) == 0 {
		return nil, zip.ErrBadRequest("run needs at least one benchmark id")
	}
	// Validate benchmark ids against the catalog.
	valid := map[string]bool{}
	for _, b := range catalog {
		valid[b.ID] = true
	}
	var unknown []string
	for _, b := range in.Benchmarks {
		if !valid[b] {
			unknown = append(unknown, b)
		}
	}
	if len(unknown) > 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown benchmarks: %s", strings.Join(unknown, ", "))
	}
	return &RunQueued{
		Status: "queued", Model: in.Model, Endpoint: in.Endpoint,
		Benchmarks: in.Benchmarks,
		Note:       "cache-before-spend: already-attempted (item,model) pairs are skipped; results land in the leaderboard.",
	}, nil
}
