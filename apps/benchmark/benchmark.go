package benchmark

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Benchmark is one canonical, versioned public test. `Native` marks harness support
// today; the rest are adapter-pending on the same registry + provenance.
//
// Every score on this surface is a percentage from 0 to 100, and Direction says
// which way it improves. There is no unit field because there is one unit: a
// latency, a throughput or a price is a fact about serving, which o11y measures
// per call and the research plane records per run with the device, the threads
// and the percentile beside it (GET /v1/research/runs?benchmark=decision.latency).
// A single number in milliseconds on a board drops exactly those conditions.
type Benchmark struct {
	ID     string `json:"id"`              // the id every other op on this surface takes
	Title  string `json:"title"`           // the benchmark's published name
	Axis   string `json:"axis"`            // what capability it measures
	Items  int    `json:"items,omitempty"` // how many items it holds, when the set is fixed
	Native bool   `json:"native"`          // whether the standardized harness runs it today
	Source string `json:"source"`          // where the items come from
	// Direction is "higher" when a higher score is better and "lower" when a
	// lower one is: a false-alarm rate, a share of answers that moved. The
	// leaderboard orders its rows by it.
	Direction string `json:"direction"`
}

// The two values a Benchmark's Direction takes.
const (
	Higher = "higher"
	Lower  = "lower"
)

// catalog is the arena's vocabulary, in three parts.
//
// The first 14 are the set every major provider reports, run under one
// standardized harness for apples-to-apples comparison; they mirror
// registry/benchmarks.yaml.
//
// The next 4 are the tests coding agents are judged on: DeepSWE, Terminal-Bench
// 4.0, SWE-Atlas QnA and SWE-Bench Pro v2. No harness here runs them, and none is
// in registry/benchmarks.yaml; their numbers reach this surface as claims, each
// citing where it was read. Each is a test of its own: terminal_bench_4_0 is not
// terminal_bench_2_1, and swe_bench_pro_v2 is not swe_bench_pro.
//
// The rest are the Decision Suite: the typed-decision tests Kai is measured on —
// the frozen harness's rows (hanzoai/benchmarks decision/, seed 13), choice among
// many options, stability, false alarms on real benign traffic and capability
// isolation. Their items are scored by Kai's own harness and reach this surface as
// claims citing the research run each number came from; this arena's harness runs
// none of them.
//
// The catalog is compiled, not written through the API: an id here is a promise
// that every claim, attempt and read naming it means the same test, and that
// promise is reviewed with the code that makes it.
var catalog = []Benchmark{
	{"gpqa_diamond", "GPQA-Diamond", "science-reasoning", 198, true, "hendrydong/gpqa_diamond_mc", Higher},
	{"humanitys_last_exam", "Humanity's Last Exam", "frontier-reasoning", 500, true, "macabdul9/hle_text_only", Higher},
	{"livecodebench", "LiveCodeBench (v6)", "competitive-coding", 0, true, "livecodebench/code_generation_lite test6", Higher},
	{"livecodebench_pro", "LiveCodeBench Pro", "competitive-coding-hard", 0, false, "LiveCodeBench Pro", Higher},
	{"swe_bench_verified", "SWE-Bench Verified", "software-engineering", 0, false, "SWE-bench Verified (bash-only)", Higher},
	{"swe_bench_pro", "SWE-Bench Pro", "software-engineering-hard", 0, false, "scaleapi/SWE-bench_Pro-os", Higher},
	{"terminal_bench_2_1", "Terminal Bench 2.1", "agentic-terminal", 0, false, "Terminal-Bench 2.1", Higher},
	{"mmlu_pro", "MMLU-Pro", "broad-knowledge", 0, false, "TIGER-Lab/MMLU-Pro", Higher},
	{"aime_2025", "AIME 2025 (MathArena)", "competition-math", 0, false, "MathArena AIME 2025", Higher},
	{"mmmu_pro", "MMMU-Pro", "multimodal-reasoning", 0, false, "MMMU-Pro", Higher},
	{"charxiv_reasoning", "CharXiv (reasoning)", "chart-vision-reasoning", 0, true, "CharXiv reasoning split", Higher},
	{"tau2_bench", "τ²-Bench", "agentic-tool-use", 0, false, "tau2-bench", Higher},
	{"scicode", "SciCode", "scientific-coding", 0, false, "SciCode", Higher},
	{"mrcr_v2", "MRCRv2", "long-context", 0, false, "MRCRv2", Higher},

	{"deepswe", "DeepSWE v1.1", "software-engineering", 0, false, "DeepSWE v1.1 leaderboard trials.json", Higher},
	{"terminal_bench_4_0", "Terminal-Bench 4.0", "agentic-terminal", 0, false, "Terminal-Bench 4.0", Higher},
	{"swe_atlas_qna", "SWE-Atlas QnA", "codebase-question-answering", 0, false, "SWE-Atlas QnA", Higher},
	{"swe_bench_pro_v2", "SWE-Bench Pro v2", "software-engineering-hard", 642, false, "Scale SWE-Bench Pro v2, released 2026-09-22", Higher},

	{"jev.ag_news", "AG News", "topic-classification", 400, false, harness, Higher},
	{"jev.emotion", "DAIR Emotion", "emotion-classification", 400, false, harness, Higher},
	{"jev.banking77_full", "Banking77", "intent-classification", 400, false, harness, Higher},
	{"app.support_triage", "Support triage", "ticket-routing", 400, false, harness, Higher},
	{"app.email_spam", "Email spam", "spam-detection", 400, false, harness, Higher},
	{"app.phishing", "Phishing", "phishing-detection", 400, false, harness, Higher},
	{"app.moderation_toxicity", "Toxicity", "moderation", 400, false, harness, Higher},
	{"app.rag_relevance", "RAG relevance", "retrieval-relevance", 400, false, harness, Higher},
	{"app.model_routing_domain", "Model routing", "request-routing", 399, false, harness, Higher},
	{"typed_decisions", "Typed decisions", "typed-decisions", 2000, false, harness, Higher},
	{"massive", "MASSIVE, 51 languages (macro accuracy)", "multilingual-intent", 5100, false, harness + "; 100 utterances a language, scored as the mean of the 51 languages' accuracies", Higher},
	{"cap.cardinality", "Choice among many options", "option-cardinality", 400, false, "hanzoai/benchmarks decision/capability cardinality: 400 questions at each of 4, 16, 77 and 150 options; a claim names its size in its model, @k<options>", Higher},
	{"decision.stability", "Same request three times (% identical)", "determinism", 4200, false, "hanzo-inc/kai bench/hijack: 4,200 requests, each sent three times; the share whose three answers agree on the decision and its probabilities", Higher},
	{"jailbreak.wild", "False alarms on real benign prompts (% flagged)", "jailbreak-false-alarms", 632, false, "hanzo-inc/kai bench/jailbreak-suite in-the-wild set, Kai-clean: 600 regular prompts and 32 attacks; the score is the share of the benign prompts flagged", Lower},
	{"decision.isolation", "Capability isolation (% moved)", "capability-isolation", 0, false, "hanzo-inc/kai: every request a routed capability does not claim, answered with and without the capability installed; the share whose answer moved by any byte", Lower},
}

// harness is where the frozen Decision Suite rows come from.
const harness = "hanzoai/benchmarks decision/ frozen harness (builders 0.3.20, seed 13)"

// lookup is the catalog row for id.
func lookup(id string) (Benchmark, bool) {
	for _, b := range catalog {
		if b.ID == id {
			return b, true
		}
	}
	return Benchmark{}, false
}

// known reports whether id is in the catalog.
func known(id string) bool {
	_, ok := lookup(id)
	return ok
}

// direction is the way id's score improves: the catalog's, else Higher.
func direction(id string) string {
	if b, ok := lookup(id); ok {
		return b.Direction
	}
	return Higher
}

// better reports whether score a beats score b in direction dir.
func better(a, b float64, dir string) bool {
	if dir == Lower {
		return a < b
	}
	return a > b
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
	// store is the arena's one database: the measured plane (attempts), the published
	// plane (claims) and each org's stored router blends. The planes share a database and
	// never a write path: an attempt is something our harness did, a claim is a report of
	// someone else's number, and one surface that could write both is one mistake away
	// from a typed-in measurement. The only way in to attempts is importAttempts.
	store *Store
	// audit takes the record every claims write and attempts import owes before
	// it lands. Nil only where the deployment disabled the trail, and then
	// neither is written.
	audit *audit.Recorder
}

// Use is the subsystem entrypoint (registered in apps.go).
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "benchmark", func(b cloud.Base) (state, error) {
		store, err := openStore(b.DataDir)
		if err != nil {
			return state{}, fmt.Errorf("benchmark: open the store: %w", err)
		}
		// FAIL-SECURE: what the file stores recorded is carried before any route is
		// served, and an error fails the mount rather than serving an arena that has
		// forgotten it.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		carried, err := store.importLegacy(ctx, b.DataDir)
		if err != nil {
			_ = store.Close()
			return state{}, err
		}
		for _, l := range carried {
			b.Log.Info("benchmark: carried what the file store recorded", "plane", l.Plane, "rows", l.Carried, "skipped", l.Skipped)
		}
		b.Log.Info("benchmark arena", "prefix", "/v1/benchmark", "benchmarks", len(catalog), "audit", deps.Audit != nil)
		return state{store: store, audit: deps.Audit}, nil
	}, routes)
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
	g.Get("/catalog", o.catalog)         // the canonical set: the public 14, the coding 4, then the Decision Suite
	g.Get("/leaderboard", o.leaderboard) // per-model measured ∥ published
	g.Get("/compare", o.compare)         // paired common-set (rescue/damage/McNemar)
	g.Post("/runs", o.run)
	// The measured plane's one way in: a published run's per-item rows.
	g.Post("/attempts", o.importAttempts)

	// Design-your-own router blend (enso-<name>). The handlers live in presets.go
	// for cohesion; the ADDRESSES live here, because one surface has one route
	// table and zipdoc files an op's prose under the group it can see.
	// The published plane, managed. A claim is other people's data and it changes
	// when they do, so it is read and written here rather than recompiled.
	// A score is a fact about a model ON A DAY, so the runs behind it are
	// readable: the board shows the latest, this shows the arc.
	g.Get("/history", o.history)

	g.Get("/claims", o.claims)
	g.Post("/claims", o.putClaims)
	// Publication: a SuperAdmin copies an org's public claims onto the board.
	g.Post("/claims/promote", o.promote)

	g.Get("/presets", o.presets)
	g.Post("/presets", o.compose)
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

// catalog is the canonical benchmarks this arena reads — the id, title, axis, item
// count, upstream source and direction of each, with native marking the ones the
// standardized harness runs today. The first 14 are the public set every major
// provider reports; the next 4 are the tests coding agents are judged on —
// DeepSWE, Terminal-Bench 4.0, SWE-Atlas QnA and SWE-Bench Pro v2 — published as
// claims; the rest are the Decision Suite, scored by Kai's own harness and
// published as claims citing the research run each number came from.
//
// direction is "higher" or "lower": which way a score improves, and so which way
// the leaderboard orders its rows. Every score is a percentage; a latency is a
// fact about serving and lives in the research plane's runs, not here.
//
// These ids are the vocabulary the rest of the surface takes: a run and a claim
// name them, and the leaderboard and compare read them from ?benchmark=. The
// catalog is deployment-wide and identical for every caller — there is no tenant
// in it.
func (o ops) catalog(ctx context.Context, _ *noIn) (*benchmarkCatalog, error) {
	return &benchmarkCatalog{Data: catalog, Total: len(catalog)}, nil
}

// LeaderRow layers the two planes for one model, coverage-aware, never blended.
type LeaderRow struct {
	Model     string   `json:"model"`              // the model this row scores
	Measured  *float64 `json:"measured"`           // hanzo-measured accuracy % (nil if unrun)
	N         int      `json:"n"`                  // coverage — NEVER compare across different n
	Published *float64 `json:"published"`          // the platform's selected claim % (nil if none)
	Gap       *float64 `json:"gap"`                // published − measured (the arena signal)
	Protocol  string   `json:"protocol,omitempty"` // how the vendor scored their claim: single-attempt, pass@k or agentic
	// From is the org whose claim Published was promoted from, when a SuperAdmin
	// published another org's claim rather than writing one under org admin:
	// "hanzo" on a row Hanzo measured and the platform published.
	From string `json:"from,omitempty"`
	// Claims is how many independent claims the platform holds for this model on
	// this benchmark. More than one means several sources reported it.
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
	// Runs are the research run ids the Published claim rests on
	// (GET /v1/research/runs?id=).
	Runs []string `json:"runs,omitempty"`
	// Evidence are the evidence uris the Published claim rests on
	// (GET /v1/research/evidence?uri=).
	Evidence []string `json:"evidence,omitempty"`
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
	// Direction is "higher" or "lower": which way this benchmark's score
	// improves, and so which way Rows is ordered.
	Direction string `json:"direction"`
	// Rows is one per model, best first in Direction: measured rows by their
	// measurement, then claim-only rows by their published score, ties by model.
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
// Published, Claims, Spread and Mean read the platform's own public claims (org
// "admin") and nothing else, whoever asks. Other orgs' claims are at
// /v1/benchmark/claims, labelled, and reach this board only when a SuperAdmin
// publishes them: writing under org admin, or promoting an org's public claims
// with POST /v1/benchmark/claims/promote, which keeps the org they came from in
// the row's from.
//
// Rows are best first. A benchmark whose score improves downward — a false-alarm
// rate, a share of answers that moved — is ordered lowest first, and the answer
// says which with direction.
//
// n is coverage and is not decoration: two measured numbers taken over different
// item counts are not comparable, so read the row's n before reading its accuracy.
func (o ops) leaderboard(ctx context.Context, in *benchmarkQuery) (*leaderboard, error) {
	bench := strings.TrimSpace(in.Benchmark)
	if bench == "" {
		bench = "gpqa_diamond"
	}
	dir := direction(bench)
	as, err := o.s.State.store.Attempts(ctx, bench)
	if err != nil {
		return nil, o.gap(err)
	}
	claims, err := claimsFor(ctx, o.s.State.store, bench)
	if err != nil {
		return nil, o.gap(err)
	}
	return &leaderboard{Benchmark: bench, Direction: dir, Rows: computeLeaderboard(as, bench, dir, claims)}, nil
}

// computeLeaderboard is the pure aggregation (testable): per-model measured accuracy
// (coverage-aware) layered with the published claim, gap = published − measured. Never
// blended; a model with only a claim shows measured=nil, and vice versa. dir orders
// the rows and picks among a model's claims.
func computeLeaderboard(attempts []attempt, bench, dir string, claim map[string][]storedClaim) []LeaderRow {
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
			p, _ := selectClaim(cs, dir)
			v := p.Score
			r.Published, r.Protocol, r.From, r.Runs, r.Evidence = &v, p.Protocol, p.From, p.Runs, p.Evidence
			r.Claims, r.Spread, r.Mean = len(cs), claimSpread(cs), claimMean(cs)
		}
		if r.Measured != nil && r.Published != nil {
			g := *r.Published - *r.Measured
			r.Gap = &g
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if c := first(a.Measured, b.Measured, dir); c != 0 {
			return c < 0
		}
		if c := first(a.Published, b.Published, dir); c != 0 {
			return c < 0
		}
		return a.Model < b.Model
	})
	return rows
}

// first orders two optional scores in dir: -1 when a goes first, 1 when b does,
// 0 when neither decides. A score goes before no score.
func first(a, b *float64, dir string) int {
	switch {
	case a == nil && b == nil:
		return 0
	case b == nil:
		return -1
	case a == nil:
		return 1
	case better(*a, *b, dir):
		return -1
	case better(*b, *a, dir):
		return 1
	}
	return 0
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
	as, err := o.s.State.store.Attempts(ctx, bench)
	if err != nil {
		return nil, o.gap(err)
	}
	return computeCompare(as, bench, a, b), nil
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

// suite names a model and the catalog benchmarks to ask about.
type suite struct {
	// Benchmarks are the catalog ids to ask about. At least one is required, and
	// every id must be in the catalog.
	Benchmarks []string `json:"benchmarks" validate:"required"`
	// Model is the model to ask about, by the id its attempts are stored under.
	// Required.
	Model string `json:"model"`
}

// measurement is one benchmark's answer: the measurement already held for the
// model, or why there is none and none is coming.
type measurement struct {
	// Benchmark is the catalog id this answer is about.
	Benchmark string `json:"benchmark"`
	// Latest is the model's latest run on this benchmark — its run id, when it
	// was recorded, its accuracy and how many items it covered — read from the
	// attempts the store already holds. Null when the store holds none.
	Latest *RunPoint `json:"latest"`
	// Harness is the harness that measures this benchmark for the arena, by
	// repository. Empty when none does, and then a measurement arrives only as a
	// published run's items imported through POST /v1/benchmark/attempts.
	Harness string `json:"harness,omitempty"`
	// Note says in a sentence what the answer means: a held measurement served
	// rather than spent again, or nothing queued and why.
	Note string `json:"note"`
}

// measurements is what a run request answers: one entry per benchmark asked
// about, in the order asked.
type measurements struct {
	// Org is the caller's verified org.
	Org string `json:"org"`
	// By is the verified user who asked.
	By string `json:"by"`
	// Model is the model asked about.
	Model string `json:"model"`
	// Data is one answer per benchmark, in the order the request named them.
	Data []measurement `json:"data"`
}

// measurer is what measures the catalog's native benchmarks. The arena reads the
// attempts it records and runs none itself.
const measurer = "hanzoai/enso-bench"

// run answers what the arena already holds for a model on each benchmark asked
// about, and spends nothing.
//
// For a benchmark the store holds attempts for, the answer is the model's latest
// run — its id, when it was recorded, its accuracy and how many items it covered
// — served from the attempts already recorded. A measurement held is never run
// again from here.
//
// For a benchmark it holds none for, nothing is queued, because this arena has
// no executor: the answer says so and names the harness that measures the
// benchmark, or, where none here does, the import a published run's items take.
// There is no flag to force a run, because nothing here would act on it.
//
// Any signed-in caller may ask; a caller with no verified principal is refused
// 401. The answer names the caller's verified org and user — nothing in the
// body names either — and stores nothing.
//
// Validation is up front and total — a request with no model is a 400, one with
// no benchmarks is a 400, and any benchmark id outside the catalog is a 422
// naming exactly which ids were unknown.
//
// Example: {"benchmarks": ["gpqa_diamond"], "model": "grok-4.5"}
func (o ops) run(ctx context.Context, in *suite) (*measurements, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if in.Model == "" {
		return nil, zip.ErrBadRequest("run needs a model id")
	}
	if len(in.Benchmarks) == 0 {
		return nil, zip.ErrBadRequest("run needs at least one benchmark id")
	}
	var unknown []string
	for _, b := range in.Benchmarks {
		if !known(b) {
			unknown = append(unknown, b)
		}
	}
	if len(unknown) > 0 {
		return nil, zip.Errorf(http.StatusUnprocessableEntity,
			"unknown benchmarks: %s", strings.Join(unknown, ", "))
	}
	out := &measurements{Org: org, By: strings.TrimSpace(cloud.Who(ctx).User), Model: in.Model}
	for _, id := range in.Benchmarks {
		m := measurement{Benchmark: id}
		as, err := o.s.State.store.Attempts(ctx, id)
		if err != nil {
			return nil, o.gap(err)
		}
		if h := computeHistory(as, id, in.Model); len(h) > 0 {
			latest := h[0].Points[len(h[0].Points)-1]
			latest.Delta = nil
			m.Latest = &latest
		}
		b, _ := lookup(id)
		if b.Native {
			m.Harness = measurer
		}
		switch {
		case m.Latest != nil:
			m.Note = "measured: served from the attempts already recorded, nothing spent again"
		case b.Native:
			m.Note = "not measured, and nothing was queued: this arena has no executor; " + measurer + " measures this benchmark"
		default:
			m.Note = "not measured, and nothing was queued: this arena has no executor; a published run's items reach it through POST /v1/benchmark/attempts"
		}
		out.Data = append(out.Data, m)
	}
	return out, nil
}
