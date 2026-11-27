package research

// corpus.go reads a measurement tree's OWN OUTPUT into records: the harness's
// `benchmarks.json` for the numbers it already normalized, and each run
// directory's `meta.json` and `metrics.json` for what that run was and whether it
// finished.
//
// Nothing here recomputes a metric. The harness normalizes its four benchmark
// families into one shape once, in `results.mjs`; a second normalization in Go
// would be a second answer to the same question, free to disagree with the one
// the published tables came from. So the numbers are read, not derived — and the
// fields the harness does not state are recorded as ABSENT rather than as zero.
// [Gap] counts those, per field, so "how much of this corpus is actually
// recorded" is a number rather than an impression.
//
// The benchmark DEFINITIONS below are reference data — LoCoMo's licence is the
// same for whoever runs it — so they ship, each field attributed to the line of
// the tree's own documentation it was read from. The RUNS are not: they are one
// org's measurements and they are loaded from a tree.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Corpus is what one measurement tree holds, in the record's own shapes, plus an
// honest account of what it does not hold.
type Corpus struct {
	// Benchmarks are the task definitions the tree's runs measured against.
	Benchmarks []ResearchBenchmark
	// Studies is the inquiry the tree is, when its documentation states one.
	Studies []Study
	// Runs are the executions, each with the measures the harness reported and
	// the completion counts it recorded — or did not.
	Runs []Run
	// Gaps is one entry per field some run leaves unstated, with how many runs
	// leave it unstated, worst first. This is the corpus's own incompleteness,
	// reported rather than filled in.
	Gaps []Gap
	// Skipped names the directories under runs/ that are not runs — a frozen
	// plan set, a directory with predictions and no metrics — and why. They
	// are named rather than silently dropped, because a run that failed to
	// load and a directory that was never a run are different problems.
	Skipped []string
}

// Gap is one field a set of runs leaves unstated, and how many of them do. It
// is the corpus's own incompleteness when a tree is loaded and the record's own
// when a listing is read, which is one question and so one type.
type Gap struct {
	// Field is the run field, by its wire name — so a reader goes straight
	// from this list to the field on the run that is missing.
	Field string `json:"field"`
	// Runs is how many of the runs do not state it.
	Runs int `json:"runs"`
}

// Load reads a measurement tree into records. fsys is rooted at the tree — the
// directory holding `benchmarks.json` and `runs/` (hanzoai/cloud `bench/brain`).
//
// A run's identity is its directory name, and the benchmark it measured is that
// name's first segment: the harness's own rule, `d.split('-')[0]` in
// results.mjs, which is also the layout METHOD.md declares
// (`runs/<bench>-<split>-<config>-<reader>/`). Reading the id rather than
// inventing a mapping is what keeps the record's benchmark and the tree's
// directories the same fact.
func Load(fsys fs.FS) (Corpus, error) {
	site, err := readSite(fsys)
	if err != nil {
		return Corpus{}, err
	}
	dirs, err := fs.ReadDir(fsys, "runs")
	if err != nil {
		return Corpus{}, fmt.Errorf("research corpus: %w", err)
	}

	ids := map[string]bool{}
	for id := range site {
		ids[id] = true
	}
	var skipped []string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		id := d.Name()
		hasMeta := exists(fsys, path.Join("runs", id, "meta.json"))
		hasMetrics := exists(fsys, path.Join("runs", id, "metrics.json"))
		switch {
		case hasMeta || hasMetrics || site[id] != nil:
			ids[id] = true
		default:
			skipped = append(skipped, id+" — no meta.json and no metrics.json; not a run")
		}
	}

	c := Corpus{Benchmarks: benchmarkDefs, Studies: studyDefs, Skipped: skipped}
	for _, id := range sortedSet(ids) {
		run, err := readRun(fsys, id, site[id])
		if err != nil {
			return Corpus{}, err
		}
		c.Runs = append(c.Runs, run)
	}
	c.Gaps = gapsIn(c.Runs)
	sort.Strings(c.Skipped)
	return c, nil
}

// readRun assembles one run from the three files that describe it. Precedence is
// always MOST SPECIFIC FIRST — the run's own meta, then its metrics, then the
// table the harness built — and an absent value stays absent at every step.
func readRun(fsys fs.FS, id string, s *section) (Run, error) {
	var m meta
	if err := readJSON(fsys, path.Join("runs", id, "meta.json"), &m); err != nil {
		return Run{}, err
	}
	var mx metrics
	if err := readJSON(fsys, path.Join("runs", id, "metrics.json"), &mx); err != nil {
		return Run{}, err
	}
	bench, _, _ := strings.Cut(id, "-")

	r := Run{
		ID:        id,
		Benchmark: bench,
		// The harness calls a table's configuration its `row`; a LoCoMo answer
		// run calls it the `policy`; the LoCoMo-Conv lane calls it the `label`.
		// Three names for the system that was measured, so all three are read —
		// and where none is stated the system stays UNRECORDED rather than being
		// guessed out of the directory name.
		System:        first(m.Row, m.Policy, mx.Row, mx.Label, sectionRow(s)),
		Split:         first(m.Split, sectionSplit(s)),
		Embedder:      first(m.Embedding, m.Embed),
		Reader:        first(m.Reader, sectionReader(s)),
		K:             firstInt(m.K, sectionK(s)),
		Temperature:   m.Temperature,
		MaxTokens:     m.MaxTokens,
		Prompt:        m.Prompt,
		PromptDigest:  m.PromptSHA,
		DatasetDigest: m.DatasetSHA,
		StoreDigest:   first(m.StoreSHA, m.Store),
		FactsDigest:   m.FactsSHA,
		Commit:        first(m.Commit, sectionCommit(s)),
		Questions:     m.Questions,
		Answered:      m.Answered,
		When:          first(m.Started, m.When),
		Ended:         m.Finished,
		Notes:         strings.TrimSpace(m.Bench + " " + m.Protocol),
		Study:         corpusStudy,
		Measures:      measuresOf(s),
	}
	r.Completion = completionOf(r.Questions, r.Answered, r.Ended)
	return r, nil
}

// measuresOf flattens the harness's per-category table into the record's one
// grain: a row per (category, metric), carrying the interval where the harness
// reported one and carrying none where it did not.
func measuresOf(s *section) []Measure {
	if s == nil {
		return nil
	}
	var out []Measure
	for _, cat := range s.Categories {
		for name, raw := range cat.Metrics {
			if len(raw) == 0 || string(raw) == "null" {
				continue // the harness reported nothing here; a zero would invent one
			}
			m := Measure{Category: cat.Name, Metric: name, N: cat.N}
			var iv interval
			if err := json.Unmarshal(raw, &iv); err == nil {
				m.Value, m.Lo, m.Hi = iv.Mean, &iv.Lo, &iv.Hi
			} else if err := json.Unmarshal(raw, &m.Value); err != nil {
				continue // a shape neither a number nor an interval: not a measure
			}
			// `of` is the category's own size, not a metric of it: it belongs
			// on every measure of that category as the denominator, which is
			// what tells a skipped category from a failed one.
			if name == "of" {
				continue
			}
			out = append(out, m)
		}
	}
	den := denominator(s)
	for i := range out {
		out[i].Of = den[out[i].Category]
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Metric < out[j].Metric
	})
	return out
}

// denominator reads each category's `of` — how many items the category holds —
// out of the metrics the harness put it among.
func denominator(s *section) map[string]*int {
	den := map[string]*int{}
	for _, cat := range s.Categories {
		raw, ok := cat.Metrics["of"]
		if !ok {
			continue
		}
		var n int
		if json.Unmarshal(raw, &n) == nil {
			v := n
			den[cat.Name] = &v
		}
	}
	return den
}

// gapsIn counts, per field, how many runs do not state it — the corpus's own
// incompleteness, worst first. Fields are named exactly as they are on the wire
// so a reader can go straight from this list to the record.
func gapsIn(runs []Run) []Gap {
	count := map[string]int{}
	miss := func(field string, absent bool) {
		if absent {
			count[field]++
		}
	}
	for _, r := range runs {
		miss("system", r.System == "")
		miss("version", r.Version == "")
		miss("split", r.Split == "")
		miss("embedder", r.Embedder == "")
		miss("reader", r.Reader == "")
		miss("k", r.K == nil)
		miss("temperature", r.Temperature == nil)
		miss("max_tokens", r.MaxTokens == nil)
		miss("prompt", r.Prompt == "")
		miss("prompt_digest", r.PromptDigest == "")
		miss("dataset_digest", r.DatasetDigest == "")
		miss("store_digest", r.StoreDigest == "")
		miss("facts_digest", r.FactsDigest == "")
		miss("commit", r.Commit == "")
		miss("questions", r.Questions == nil)
		miss("answered", r.Answered == nil)
		miss("when", r.When == "")
		miss("ended", r.Ended == "")
		miss("by", r.By == "")
		miss("measures", len(r.Measures) == 0)
	}
	out := make([]Gap, 0, len(count))
	for f, n := range count {
		out = append(out, Gap{Field: f, Runs: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Runs != out[j].Runs {
			return out[i].Runs > out[j].Runs
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// ── the tree's own files ──────────────────────────────────────────────────────

// section is one run's row in the harness's generated table, which is where the
// NUMBERS come from: `results.mjs` normalizes four benchmark families into this
// one shape, and hanzo.ai/benchmarks renders exactly this file.
type section struct {
	Bench      string     `json:"bench"`
	Run        string     `json:"run"`
	Row        *string    `json:"row"`
	Split      *string    `json:"split"`
	K          *int       `json:"k"`
	Reader     *string    `json:"reader"`
	Commit     *string    `json:"commit"`
	Categories []category `json:"categories"`
}

type category struct {
	Name    string                     `json:"name"`
	N       *int                       `json:"n"`
	Metrics map[string]json.RawMessage `json:"metrics"`
}

// interval is the harness's shape for a number it bootstrapped a 95% CI for.
// A metric that is a bare number reported no interval and gets none here.
type interval struct {
	Mean float64 `json:"mean"`
	Lo   float64 `json:"lo"`
	Hi   float64 `json:"hi"`
}

// meta is a run directory's meta.json — the fields the harness writes about the
// run ITSELF. The families write overlapping subsets of it (a retrieval run
// records `store`, an answer run records `store_sha256`), which is why the
// assembly above reads several names for one fact and none of them is required.
type meta struct {
	Bench       string   `json:"bench"`
	Policy      string   `json:"policy"`
	Row         string   `json:"row"`
	Split       string   `json:"split"`
	K           *int     `json:"k"`
	Reader      string   `json:"reader"`
	Embedding   string   `json:"embedding"`
	Embed       string   `json:"embed"`
	Prompt      string   `json:"prompt"`
	PromptSHA   string   `json:"prompt_sha256"`
	DatasetSHA  string   `json:"dataset_sha256"`
	StoreSHA    string   `json:"store_sha256"`
	Store       string   `json:"store"`
	FactsSHA    string   `json:"facts_sha256"`
	Commit      string   `json:"commit"`
	Temperature *float64 `json:"temperature"`
	MaxTokens   *int     `json:"max_tokens"`
	Questions   *int     `json:"questions"`
	Answered    *int     `json:"answered"`
	Started     string   `json:"started"`
	Finished    string   `json:"finished"`
	When        string   `json:"when"`
	Protocol    string   `json:"protocol"`
}

// metrics reads only what metrics.json says about the run's IDENTITY. Its
// numbers are deliberately not read: the harness already normalized them into
// benchmarks.json, and reading them twice would be two answers to one question.
type metrics struct {
	Row   string `json:"row"`
	Label string `json:"label"`
}

// readSite indexes the harness's generated table by run id.
func readSite(fsys fs.FS) (map[string]*section, error) {
	var doc struct {
		Sections []section `json:"sections"`
	}
	if err := readJSON(fsys, "benchmarks.json", &doc); err != nil {
		return nil, err
	}
	out := make(map[string]*section, len(doc.Sections))
	for i := range doc.Sections {
		out[doc.Sections[i].Run] = &doc.Sections[i]
	}
	return out, nil
}

// readJSON decodes one file, treating ABSENT as empty rather than as an error:
// a run directory that has no meta.json has said nothing about itself, which is
// a gap to count and not a tree to reject.
func readJSON(fsys fs.FS, name string, v any) error {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("research corpus %s: %w", name, err)
	}
	return nil
}

func exists(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)
	return err == nil
}

// first returns the first value that says something; all-empty stays empty.
func first(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// firstInt returns the first count that is present; all-absent stays absent,
// never 0.
func firstInt(vs ...*int) *int {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

func sectionRow(s *section) string    { return deref(s, func(s *section) *string { return s.Row }) }
func sectionSplit(s *section) string  { return deref(s, func(s *section) *string { return s.Split }) }
func sectionReader(s *section) string { return deref(s, func(s *section) *string { return s.Reader }) }
func sectionCommit(s *section) string { return deref(s, func(s *section) *string { return s.Commit }) }

func sectionK(s *section) *int {
	if s == nil {
		return nil
	}
	return s.K
}

func deref(s *section, pick func(*section) *string) string {
	if s == nil {
		return ""
	}
	if v := pick(s); v != nil {
		return *v
	}
	return ""
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── reference data ────────────────────────────────────────────────────────────

// corpusStudy is the id of the inquiry the measurement tree is. Every run in the
// tree belongs to it: README.md opens by saying what the whole directory is for,
// and a run read outside that question is a score with nobody asking anything.
const corpusStudy = "memory-retrieval"

// codeStudy is the second inquiry the bench tree holds, one directory over:
// `bench/code` runs the same engine against code, where the typed links are
// imports and definitions rather than extracted facts. Its runs are recorded by
// the harness (bench/brain/record.mjs walks both lanes); the definition lives
// here beside the other reference data, for the same reason repobench-r's
// benchmark definition does.
const codeStudy = "code-retrieval"

var studyDefs = []Study{{
	ID:       corpusStudy,
	Title:    "Hanzo Context — memory retrieval, measured",
	Question: "Similarity finds what sounds related; memory needs what is connected. How much does the difference measure, on public benchmarks under a declared protocol?",
}, {
	ID:       codeStudy,
	Title:    "Hanzo Context on code — typed links instead of facts",
	Question: "Similarity is one candidate generator among several and the rest follow typed links. Does that hold in a second domain, where a completion point needs one snippet from another file and the file's imports, definitions and identifiers say which?",
}}

// benchmarkDefs are the task definitions the tree's runs measured against, each
// field read from the tree's own documentation: the dataset paths and licences
// from README.md's "Data" section, the split definitions and metrics from
// METHOD.md's "Splits" and "Metrics", and LoCoMo-Conv's protocol citation from
// the `protocol` field its own runs record.
//
// A licence absent from that documentation is recorded absent. Two of these
// datasets are non-commercial and one is non-derivative, which is the kind of
// fact a leaderboard is built on top of without ever writing down.
var benchmarkDefs = []ResearchBenchmark{
	{
		ID:      "locomo",
		Title:   "LoCoMo",
		Dataset: "snap-research/locomo",
		License: "CC BY-NC 4.0",
		Origin:  "https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json",
		Splits: []Split{
			{Name: "dev", Definition: "conversations 0, 1 and 2"},
			{Name: "test", Definition: "conversations 3 through 9"},
			{Name: "all", Definition: "all ten conversations; the split numbers quoted before dev/test were declared"},
		},
		Metric: "f1",
		Metrics: []string{"f1", "em", "exact", "supported", "answered", "recall@5", "recall@10",
			"recall@20", "all@5", "all@10", "all@20", "any@5", "any@10", "any@20",
			"mrr", "ndcg@5", "ndcg@10", "ndcg@20", "examined", "tokens", "p50_ms", "p95_ms"},
		Citation: "snap-research/locomo",
		Notes:    "Answer scores use LoCoMo normalisation; every headline carries a bootstrap 95% CI over questions, 1000 resamples.",
	},
	{
		ID:      "mab",
		Title:   "MemoryAgentBench — Conflict Resolution (FactConsolidation)",
		Dataset: "ai-hyz/MemoryAgentBench",
		License: "MIT",
		Origin:  "https://huggingface.co/datasets/ai-hyz/MemoryAgentBench",
		Splits: []Split{
			{Name: "dev", Definition: "the two 6k haystacks, single-hop and multi-hop"},
			{Name: "test", Definition: "the 32k, 64k and 262k haystacks"},
		},
		Metric:   "substring_em",
		Metrics:  []string{"substring_em"},
		Citation: "ai-hyz/MemoryAgentBench",
		Notes:    "substring_exact_match is the benchmark's own metric.",
	},
	{
		ID:      "conv",
		Title:   "LoCoMo-Conv",
		Dataset: "snap-research/locomo (proxy: the original LoCoMo QA pool)",
		License: "CC BY-NC 4.0",
		Origin:  "https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json",
		Splits: []Split{
			{Name: "dev", Definition: "the first two conversations' items"},
			{Name: "test", Definition: "the rest"},
		},
		Metric:   "recall",
		Metrics:  []string{"recall", "all", "any", "tokens", "units_per_conv"},
		Citation: "arXiv 2609.03467",
		Notes: "The paper's own conversation styles are unreleased, so these runs score a PROXY over the " +
			"original LoCoMo QA pool under the paper's protocol (raw turns, top-k, " +
			"recall=|ret∩gold|/|gold| with verbatim containment). Numbers from it are not comparable to " +
			"the paper's without that caveat.",
	},
	{
		ID:       "repobench-r",
		Title:    "RepoBench-R",
		Dataset:  "tianyang/repobench-r",
		License:  "CC BY-NC-ND 4.0",
		Origin:   "https://huggingface.co/datasets/tianyang/repobench-r",
		Splits:   []Split{{Name: "dev"}, {Name: "test"}},
		Metric:   "recall@1",
		Metrics:  []string{"recall@1", "recall@3", "recall@5", "mrr", "ndcg@5", "examined", "p50"},
		Citation: "tianyang/repobench-r",
		Notes:    "Run under two settings, cross-file-first and cross-file-random, which are settings of the task rather than splits of its items.",
	},
	{
		ID:       "longmemeval",
		Title:    "LongMemEval",
		Dataset:  "xiaowu0162/longmemeval",
		License:  "MIT",
		Origin:   "https://huggingface.co/datasets/xiaowu0162/longmemeval",
		Metric:   "mrr",
		Metrics:  []string{"mrr", "all@5", "all@10", "any@5", "any@10"},
		Citation: "xiaowu0162/longmemeval",
		Notes:    "The tree's documentation declares no split for this benchmark, so none is recorded.",
	},
}
