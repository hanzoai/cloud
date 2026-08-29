// Package benchmark is one honest score for any model, on the tests everyone quotes.
//
// It is the native benchmark ARENA — run the top-N canonical public benchmarks
// against any model or endpoint, under ONE standardized harness, measure
// Hanzo's own models (enso, zen), and reconcile any external provider-reported
// claim against that measurement. Sibling to /v1/eval (evals = YOUR data +
// YOUR judge; benchmark = the canonical public tests, comparable +
// provenance-first + leaderboard).
//
// Provenance-first, never blended: a `published_claim` (what a vendor reports) and a
// `hanzo-measured` attempt (what OUR harness gets) are separate planes — the gap is
// the signal (some provider-reported claims run 3-13pp hot vs one standardized
// harness). The store is append-only; a re-scored label
// is a new score_event, never an overwrite.
//
// Its own binary (plugin/benchmark) states Name/Price/Mount; the host learns the
// prefix from its manifest row. The Python enso-bench harness is the research
// prototype, THIS is the product surface.
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
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Benchmark is one canonical, versioned public test. `Native` marks harness support
// today; the rest are adapter-pending on the same registry + provenance.
type Benchmark struct {
	ID     string `json:"id"`              // the id every other op on this surface takes
	Title  string `json:"title"`           // the benchmark's published name
	Axis   string `json:"axis"`            // what capability it measures
	Items  int    `json:"items,omitempty"` // how many items it holds, when the set is fixed
	Native bool   `json:"native"`          // whether the standardized harness runs it today
	Source string `json:"source"`          // where the items come from
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
	// Benchmark is the canonical test id the claim is about, from /catalog.
	Benchmark string `json:"benchmark"`
	// Provider is who the claim belongs to — the lab or leaderboard whose number
	// this is. It joins a claim to the attempts measured for that same model.
	Provider string `json:"provider"`
	// Model is the system the score is claimed for.
	Model string `json:"model"`
	// Score is the reported aggregate, as a percentage.
	Score float64 `json:"score"`
	// Protocol records HOW it was scored — provider-reported, agentic,
	// third-party-leaderboard — because a provider card and a third party running
	// its own harness are different kinds of number and must not be blended.
	Protocol string `json:"protocol"`
	// Source is the citation the row was read from. A claim without one is a
	// number nobody can check, so every write requires it.
	Source string `json:"source"`
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
	// Run is which measurement this attempt belongs to. Without it every attempt
	// ever made blends into one lifetime average, so a model that got BETTER
	// reads as a muddied middle rather than an improvement — and re-measuring a
	// model could only ever drag its own history along. A run is the unit that
	// makes "measured on this date, under this harness" a thing you can say.
	//
	// Empty on rows written before runs existed. Those are treated as one
	// implicit first run, which is what they were.
	Run string `json:"run,omitempty"`
	// At is when the attempt was recorded. Zero on pre-run rows.
	At time.Time `json:"at,omitempty"`
}

type state struct {
	store AttemptStore // the durability client — fileStore (local dev) or cloud backend
	// claims is the published plane's own store. Separate from `store` because
	// the two planes must never share a write path: an attempt is something our
	// harness did, a claim is a report of someone else's number, and one surface
	// that could write both is one mistake away from a typed-in measurement.
	claims ClaimStore
}

// Mount is the subsystem entrypoint (registered in apps.go).
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "benchmark", build, routes)
}

func build(b cloud.Base) (state, error) {
	// Local-dev backend today; the cloud backend (relational + object store) swaps in
	// behind AttemptStore with no handler change (the architecture: prod is stateless,
	// never pod-local). Attempts import idempotently by stable id.
	store := newFileStore(b.DataDir)
	claims := newClaimStore(b.DataDir)
	b.Log.Info("benchmark arena", "prefix", "/v1/benchmark", "benchmarks", len(catalog), "attempts", len(store.Attempts("")))
	return state{store: store, claims: claims}, nil
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

// routes registers the arena. Every op is TYPED — its input and its answer are Go
// types — so the schema, the prose, the MCP tool, the CLI command and every
// generated SDK method are projections of the handler itself. That matters more
// here than almost anywhere: the arena's whole value is knowing WHAT a number is,
// and an operation that cannot say measured-versus-claimed is worse than useless.
//
// zipdoc lifts the doc comments into zipdoc_gen.go, which is the only way prose
// reaches the published registry: Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/benchmark")

	zip.Get(g, "/catalog", o.catalog)         // the top-14 canonical set
	zip.Get(g, "/leaderboard", o.leaderboard) // per-model measured ∥ published
	zip.Get(g, "/compare", o.compare)         // paired common-set (rescue/damage/McNemar)
	zip.Post(g, "/runs", o.run, zip.WithStatus(http.StatusAccepted))

	// Design-your-own router blend (enso-<name>). The handlers live in presets.go
	// for cohesion; the ADDRESSES live here, because one surface has one route
	// table and zipdoc files an op's prose under the group it can see.
	// The published plane, managed. A claim is other people's data and it changes
	// when they do, so it is read and written here rather than recompiled.
	// A score is a fact about a model ON A DAY, so the runs behind it are
	// readable: the board shows the latest, this shows the arc.
	zip.Get(g, "/history", o.history)

	zip.Get(g, "/claims", o.claims)
	zip.Post(g, "/claims", o.putClaims)

	zip.Get(g, "/presets", o.presets)
	zip.Post(g, "/presets", o.compose, zip.WithStatus(http.StatusAccepted))
}

// ops binds the service to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query.
type noIn struct{}

// benchmarkCatalog is the set of canonical public tests this arena runs.
type benchmarkCatalog struct {
	// Data is one row per benchmark, in the catalog's own order.
	Data []Benchmark `json:"data"`
	// Total is how many rows Data holds.
	Total int `json:"total"`
}

// catalog is the canonical public benchmarks this arena runs — the id, title, axis,
// item count and upstream source of each, with native marking the ones the
// standardized harness runs today; the rest are registered and adapter-pending.
//
// These ids are the vocabulary the rest of the surface takes: a run names them, and
// the leaderboard and compare read them from ?benchmark=. The catalog is
// deployment-wide and identical for every caller — there is no tenant in it.
func (o ops) catalog(ctx context.Context, _ *noIn) (*benchmarkCatalog, error) {
	return &benchmarkCatalog{Data: catalog, Total: len(catalog)}, nil
}

// LeaderRow layers the two planes for one model, coverage-aware, never blended.
type LeaderRow struct {
	Model     string   `json:"model"`              // the model this row scores
	Measured  *float64 `json:"measured"`           // hanzo-measured accuracy % (nil if unrun)
	N         int      `json:"n"`                  // coverage — NEVER compare across different n
	Published *float64 `json:"published"`          // provider-claimed % (nil if none)
	Gap       *float64 `json:"gap"`                // published − measured (the arena signal)
	Protocol  string   `json:"protocol,omitempty"` // how the vendor scored their claim: single-attempt, pass@k or agentic
	// Claims is how many independent claims exist for this model on this
	// benchmark. More than one means several sources reported it.
	Claims int `json:"claims,omitempty"`
	// Spread is the distance between the highest and lowest of them, nil when
	// there is only one. It is the disagreement AMONG sources, which a single
	// Published number cannot show — signal in the same way the
	// published-minus-measured gap is.
	Spread *float64 `json:"spread,omitempty"`
	// Mean is the unweighted average of every claim, which answers a different
	// question from Published: what the field says on average, rather than what
	// the vendor says about itself. With one claim the two are equal.
	Mean *float64 `json:"mean,omitempty"`
	// Run names the measurement Measured came from, and MeasuredAt is when it
	// ran. A score with no date is not a fact about a model, it is a fact about
	// a model on a day — and models change, so the date is what makes the number
	// checkable rather than merely quoted.
	Run string `json:"run,omitempty"`
	// MeasuredAt is when the run behind Measured was recorded.
	MeasuredAt *time.Time `json:"measuredAt,omitempty"`
	// CILow and CIHigh are the 95% Wilson interval on Measured, in percent. They
	// are what makes the score comparable: at n=198 a 98% carries roughly ±2
	// points, so most differences at the top of a board are not distinguishable
	// and a bare number implies a precision it does not have. Absent when there
	// is no measurement.
	CILow *float64 `json:"ciLow,omitempty"`
	// CIHigh is the upper bound of that interval. Wilson rather than the normal
	// approximation because the normal one produces bounds past 100 exactly where
	// benchmark scores live — at 194/198 that is the top of the board, not a
	// corner case.
	CIHigh *float64 `json:"ciHigh,omitempty"`
}

// benchmarkQuery names the benchmark a read is about.
type benchmarkQuery struct {
	// Benchmark is the catalog id to read, defaulting to gpqa_diamond.
	Benchmark string `json:"benchmark"`
}

// leaderboard is the answer to a per-model score read.
type leaderboard struct {
	// Benchmark is the catalog id these rows are about.
	Benchmark string `json:"benchmark"`
	// Rows is one per model, ordered by measured accuracy descending.
	Rows []LeaderRow `json:"rows"`
}

// leaderboard answers one row per model for the benchmark named — what our own
// harness measured, beside what the vendor claims, and the gap between them.
//
// The gap is the point of the arena; provider-reported claims have run materially
// hot against one standardized harness.
//
// The two planes are NEVER blended, and that is the rule to read the rows by: a
// model we have measured but no vendor has claimed for shows published null, a
// model with only a claim shows measured null, and gap exists only where both do.
//
// n is coverage and is not decoration: two measured numbers taken over different
// item counts are not comparable, so read the row's n before reading its accuracy.
func (o ops) leaderboard(ctx context.Context, in *benchmarkQuery) (*leaderboard, error) {
	bench := strings.TrimSpace(in.Benchmark)
	if bench == "" {
		bench = "gpqa_diamond"
	}
	return &leaderboard{Benchmark: bench, Rows: computeLeaderboard(o.s.State.store.Attempts(bench), bench, claimsFor(o.s.State.claims, bench))}, nil
}

// computeLeaderboard is the pure aggregation (testable): per-model measured accuracy
// (coverage-aware) layered with the published claim, gap = published − measured. Never
// blended; a model with only a claim shows measured=nil, and vice versa.
func computeLeaderboard(attempts []attempt, bench string, claim map[string][]publishedClaim) []LeaderRow {
	// The LATEST run per model, not every attempt ever made. A model is
	// re-measured when the harness improves or the model does, and blending a
	// new run into an old one reports neither: a model that went from 88 to 94
	// would show something in between forever, which is the opposite of what a
	// re-measurement is for. History is not discarded — it is in the store, and
	// /runs reads it — but the leaderboard answers "how good is it now".
	latest := map[string]string{}
	when := map[string]time.Time{}
	for _, a := range attempts {
		if a.Benchmark != bench || a.Answer == "" {
			continue
		}
		if t, ok := when[a.Model]; !ok || a.At.After(t) {
			when[a.Model], latest[a.Model] = a.At, a.Run
		}
	}

	type acc struct{ ok, n int }
	m := map[string]*acc{}
	for _, a := range attempts {
		if a.Benchmark != bench || a.Answer == "" {
			continue
		}
		if a.Run != latest[a.Model] {
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
		if r.Measured != nil {
			lo, hi := wilson(m[model].ok, m[model].n)
			r.CILow, r.CIHigh = &lo, &hi
			r.Run = latest[model]
			if t, ok := when[model]; ok && !t.IsZero() {
				at := t
				r.MeasuredAt = &at
			}
		}
		if cs, ok := claim[model]; ok && len(cs) > 0 {
			// One column, every claim counted. selectClaim states which reading
			// the column shows; Claims and Spread say how many others there were
			// and how far apart, so a single number never hides a disagreement.
			p, _ := selectClaim(cs)
			v := p.Score
			r.Published, r.Protocol = &v, p.Protocol
			r.Claims, r.Spread, r.Mean = len(cs), claimSpread(cs), claimMean(cs)
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

// compareQuery names the benchmark and the two arms to test against each other.
type compareQuery struct {
	// Benchmark is the catalog id to compare on, defaulting to gpqa_diamond.
	Benchmark string `json:"benchmark"`
	// A is the first model id. It is required.
	A string `json:"a" validate:"required"`
	// B is the second model id. It is required.
	B string `json:"b" validate:"required"`
}

// pairing is the ONLY valid arm-vs-arm test: paired on the items BOTH arms
// completed, with rescue and damage counts and an exact-McNemar p.
type pairing struct {
	// Benchmark is the catalog id the two arms were compared on.
	Benchmark string `json:"benchmark"`
	// A is the first model id.
	A string `json:"a"`
	// B is the second model id.
	B string `json:"b"`
	// NCommon is how many items BOTH arms completed. It is the denominator, and
	// the reason this comparison is valid where a raw accuracy difference is not.
	NCommon int `json:"n_common"`
	// ACorrect is how many of those common items A got right.
	ACorrect int `json:"a_correct"`
	// BCorrect is how many of those common items B got right.
	BCorrect int `json:"b_correct"`
	// RescueAOverB is how many items A got right and B got wrong.
	RescueAOverB int `json:"rescue_a_over_b"`
	// RescueBOverA is how many items B got right and A got wrong.
	RescueBOverA int `json:"rescue_b_over_a"`
	// NetAMinusB is the two rescue counts subtracted — A's advantage in items.
	NetAMinusB int `json:"net_a_minus_b"`
	// McnemarP is the two-sided exact binomial p on the discordant pairs. It is 1
	// when nothing is discordant, which is "no evidence of a difference", not an
	// error.
	McnemarP float64 `json:"mcnemar_p"`
}

// compare is the ONLY valid arm-vs-arm test: it pairs the two models on the items
// BOTH completed, and answers rescue and damage counts with an exact-McNemar p.
//
// Pairing is what prevents the subset artifact — comparing one model's easy subset
// against another's full run — so n_common, not either arm's own coverage, is the
// number to read this by.
//
// Both a and b are required. The benchmark defaults to gpqa_diamond.
func (o ops) compare(ctx context.Context, in *compareQuery) (*pairing, error) {
	bench := strings.TrimSpace(in.Benchmark)
	if bench == "" {
		bench = "gpqa_diamond"
	}
	a, b := strings.TrimSpace(in.A), strings.TrimSpace(in.B)
	if a == "" || b == "" {
		return nil, zip.ErrBadRequest("compare needs ?a= and ?b= model ids")
	}
	return computeCompare(o.s.State.store.Attempts(bench), bench, a, b), nil
}

// computeCompare is the pure paired common-set test (testable): rescue/damage on items
// BOTH arms completed + exact McNemar. The only valid arm-vs-arm comparison.
func computeCompare(attempts []attempt, bench, a, b string) *pairing {
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
	return &pairing{
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
	lo := min(cc, b)
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
	for i := range k {
		res = res * float64(n-i) / float64(i+1)
	}
	return res
}

// suite runs a benchmark against a model or endpoint. The target is a catalog
// model id OR your own chat-completions endpoint and key — the cloud offering:
// benchmark YOUR model under the same standardized harness.
type suite struct {
	// Benchmarks are the catalog ids to run. At least one is required, and every id
	// must be in the catalog.
	Benchmarks []string `json:"benchmarks" validate:"required"`
	// Model is the catalog model id to run. Either this or endpoint is required.
	Model string `json:"model"`
	// Endpoint is your own chat-completions URL, for benchmarking a model this arena
	// does not host. Either this or model is required.
	Endpoint string `json:"endpoint,omitempty"`
	// Attempts is how many times to try each item; the harness's default applies
	// when it is omitted.
	Attempts int `json:"attempts,omitempty"`
}

// admission is the receipt a queued run answers with.
type admission struct {
	// Status is "queued": the run is admitted, not finished.
	Status string `json:"status"`
	// Model is the catalog model the run targets.
	Model string `json:"model,omitempty"`
	// Endpoint is the caller's own endpoint the run targets.
	Endpoint string `json:"endpoint,omitempty"`
	// Benchmarks are the catalog ids admitted.
	Benchmarks []string `json:"benchmarks"`
	// Note explains what admission does and does not promise.
	Note string `json:"note"`
}

// run admits and queues a benchmark run against a model or your own endpoint, and
// answers 202 with the receipt.
//
// It is an ADMISSION, not a result: the work is done by the harness afterwards and
// the numbers appear on the leaderboard as it completes them.
//
// Cost is bounded by the store rather than by a quota: attempts are append-only and
// keyed by (benchmark, item, model), so an (item, model) pair already attempted is
// skipped instead of re-spent, and re-queuing the same run is close to free.
//
// Validation is up front and total — a request with neither model nor endpoint is a
// 400, one with no benchmarks is a 400, and any benchmark id outside the catalog is
// a 422 naming exactly which ids were unknown, so a typo never silently queues a
// partial run.
func (o ops) run(ctx context.Context, in *suite) (*admission, error) {
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
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"unknown benchmarks: %s", strings.Join(unknown, ", "))
	}
	return &admission{
		Status: "queued", Model: in.Model, Endpoint: in.Endpoint, Benchmarks: in.Benchmarks,
		Note: "cache-before-spend: already-attempted (item,model) pairs are skipped; results land in the leaderboard.",
	}, nil
}
