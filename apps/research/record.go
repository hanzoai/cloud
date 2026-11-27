package research

// record.go is what a measurement has to name before a number from it can be
// believed or repeated: the TASK it was measured against, the EXECUTION that
// measured it, the QUESTION that execution was run to answer, and the WRITING
// that cites it.
//
// The plane already held Experiment — one measured number — and Attempt — one
// scored item. Neither is the execution. A number with no execution behind it
// cannot say which commit produced it, which prompt it read, how many questions
// it was supposed to answer, or whether it got to the end of them; and two such
// numbers cannot be told apart from two numbers of the same thing. Run is that
// missing middle, and Completion is the field it exists for.
//
// COMPLETION IS DERIVED, AND UNKNOWN IS A VALUE. A harness that checkpoints as
// it goes will report whatever has been answered so far, so a run read at
// question 57 of 282 looks exactly like a finished one unless the record carries
// the denominator. It does here, and `completion` reads it: `partial` for a
// record that says it covered less than the set, `complete` for one that says it
// covered all of it AND says when it ended, `inconsistent` where the counts
// cannot both be true, and `unknown` where the record does not say. Absence is
// never rounded down to zero — questions, answered, k, temperature and
// max_tokens are POINTERS, and a null is the record declining to answer.
//
// A RUN DOES NOT KNOW ABOUT PAPERS. Citation points one way: a Paper names the
// runs it cites, and a Run names at most the Study it belongs to. The reverse
// edge would make a run's record change when somebody else wrote something.
//
// BASELINES ARE RUNS. `baseline` is a mark on a run, not a kind of its own and
// not a category inside one: naive-rag is a system that was run, and its numbers
// come from an execution with a commit and a completion exactly like any other.
//
// Storage, versioning, idempotency, tenancy and consent are store.go's, unchanged
// and shared: these kinds ride the same per-org SQLite through the same orm
// records, the same content-hash identity, the same server-assigned append clock,
// and the same private-by-default grant. There is no second store here.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// The four values `completion` takes. Each says what the RECORD establishes,
// never what the run "probably" did — which is why `unknown` is one of them and
// why nothing here falls back to it silently.
const (
	// completionComplete: the record says the run answered every question it
	// set out to AND says when it ended.
	completionComplete = "complete"
	// completionPartial: the record says the run answered fewer questions than
	// it set out to. This is the checkpoint case — the one a published number
	// must never be taken from.
	completionPartial = "partial"
	// completionInconsistent: the record says the run answered MORE questions
	// than it set out to, so its own two counts disagree and neither can be
	// trusted as a denominator.
	completionInconsistent = "inconsistent"
	// completionUnknown: the record does not establish completion — a count is
	// absent, or the counts agree but nothing says the run ended.
	completionUnknown = "unknown"
)

// completions is the closed set a ?completion= filter is checked against, so a
// typo narrows to nothing loudly rather than silently listing everything.
var completions = map[string]bool{
	completionComplete: true, completionPartial: true,
	completionInconsistent: true, completionUnknown: true,
}

// Split is one named division of a benchmark's items, as the benchmark itself
// defines it — not as a run happened to slice its own results.
type Split struct {
	// Name is what the benchmark calls this division: dev, test, all.
	Name string `json:"name"`
	// Items is how many of the benchmark's items the split holds. 0 means the
	// definition does not state a size.
	Items int `json:"items"`
	// Definition is the split in the benchmark's own words — "conversations 0,
	// 1 and 2", "the 32k, 64k and 262k haystacks". It is what makes a split
	// reproducible by someone who has only the dataset, so a split named
	// without one is a label rather than a definition.
	Definition string `json:"definition"`
}

// ResearchBenchmark is the TASK: a standardized test, its dataset and the terms
// that dataset comes under, the divisions it declares, the metric its authors
// score it by, and how to cite it. It exists independently of anyone's run —
// nothing on it changes because somebody measured something — and it is what two
// runs must share before their numbers mean the same thing.
//
// The name carries its product because the published SCHEMA namespace is FLAT:
// `Benchmark` is claimed by apps/benchmark, the measurement ARENA, which means
// the catalogue of tests its harness can run. This is the cited definition a
// recorded run was measured against. Same convention, same reason, as
// ResearchArtifact and ResearchTotals.
type ResearchBenchmark struct {
	// Project is the sub-scope inside the org this definition is filed under.
	// On a write it is the SERVER's value — the caller's project scope — and a
	// project sent in the body is ignored.
	Project string `json:"project"`
	// ID is the benchmark's stable id, lowercase and terse: locomo, mab,
	// repobench-r. It is what a Run's `benchmark` names and what versions of
	// this definition accrue under. Required on a write.
	ID string `json:"id"`
	// Title is the benchmark's published name, as its authors write it.
	Title string `json:"title"`
	// Version is the benchmark's OWN revision — v6, 2025-03 — not the version
	// of anything that ran it. Empty means the benchmark publishes none.
	Version string `json:"version"`
	// Dataset is the items the benchmark draws on, named the way the reader
	// fetches them: a Hugging Face path, a repository, a file.
	Dataset string `json:"dataset"`
	// License is the terms the DATASET comes under — CC BY-NC 4.0, MIT. Empty
	// means the record does not say, which is not the same as permissive; a
	// benchmark whose licence is unrecorded is one nobody has checked.
	License string `json:"license"`
	// Origin is where the dataset was obtained: the URL or repository it was
	// fetched from. Provenance for the items themselves, distinct from
	// Citation, which is provenance for the task.
	Origin string `json:"origin"`
	// Splits are the divisions the benchmark declares, each with its own
	// definition. A run names one of these in its `split`.
	Splits []Split `json:"splits"`
	// Metric is the OFFICIAL metric — the one the benchmark's authors score it
	// by and the one a comparison against published numbers has to use.
	Metric string `json:"metric"`
	// Metrics are every quantity the benchmark's protocol reports, official
	// one included. A run may measure more; it may not measure fewer and still
	// claim the benchmark.
	Metrics []string `json:"metrics"`
	// Citation is how to cite the task: the paper, the arXiv id, the
	// repository. A benchmark without one is a number nobody can check back to
	// a definition.
	Citation string `json:"citation"`
	// Notes is anything about the task a reader needs and the fields above do
	// not carry — that the released items are a proxy for an unreleased set,
	// say.
	Notes string `json:"notes"`
	// Revision is this version's declared kind: original, corrected or
	// retracted. Anything else, including empty, reads as original. A retracted
	// latest version WITHDRAWS the id; every earlier version stays retained.
	Revision string `json:"revision"`
	// Canonical is DERIVED on read, never stored: a version is canonical when
	// it is the latest-appended non-retracted version of its id. A listing
	// returns only canonical versions, so it is true on every row it answers
	// with.
	Canonical bool `json:"canonical"`
	// When is when this definition was recorded, RFC3339. Empty means the
	// record does not say.
	When string `json:"when"`
}

// Measure is ONE reported number out of a run, at the grain a comparison needs:
// which slice of the items it covers, which quantity it is, its value, and the
// interval the harness put around it. A run's whole result is a list of these —
// one level, not a table of tables — so two runs are compared by joining on
// (category, metric) and nothing has to be reshaped first.
type Measure struct {
	// Category is the slice of the run's items this number covers: all, dev,
	// test, multi-hop, mh_32k. "all" is the whole run.
	Category string `json:"category"`
	// Metric is the quantity — f1, em, recall@10, substring_em. It is the only
	// place Value's unit is stated; nothing here normalizes, so one harness's
	// 0.45 and another's 45.1 are both stored as reported and are comparable
	// only within one (benchmark, metric).
	Metric string `json:"metric"`
	// Value is the number, in whatever unit Metric names.
	Value float64 `json:"value"`
	// Lo is the low end of the interval the harness reported around Value.
	// Null means it reported none — NOT an interval of zero width, which is
	// what a bare 0 would claim.
	Lo *float64 `json:"lo"`
	// Hi is the high end of that same interval. Null on the same terms as Lo.
	Hi *float64 `json:"hi"`
	// N is how many items this number was computed over. Null means the
	// harness did not say, which is the difference between a wide interval and
	// an unknown one.
	N *int `json:"n"`
	// Of is how many items the category HOLDS — the denominator N is drawn
	// from. Null means the harness did not say. N far below Of on a run whose
	// completion reads complete is the sign of a category the run skipped
	// rather than failed.
	Of *int `json:"of"`
}

// Run is ONE EXECUTION: what was measured, against which task and split, by
// which system, under which configuration, frozen at which commit, over which
// dataset — with the numbers it produced, when it happened, who ran it, and
// whether it reached the end.
//
// It is written to be re-runnable from itself. Every knob that changes a number
// is a field, and every digest that pins an input is a field, because a run
// whose record cannot be turned back into the command that produced it is an
// anecdote with a commit hash attached.
type Run struct {
	// Project is the sub-scope inside the org this run is filed under. On a
	// write it is the SERVER's value — the caller's project scope — and a
	// project sent in the body is ignored.
	Project string `json:"project"`
	// ID is the run's stable id — the run directory's name by convention,
	// `<benchmark>-<split>-<config>-<system>`, carrying no date. It is the key
	// versions accrue under: a correction re-sent with this id appends a new
	// version beside the old rather than replacing it, which is how a run
	// published from a checkpoint is fixed without erasing what was published.
	// Required on a write.
	ID string `json:"id"`
	// Benchmark is the ResearchBenchmark id this run measured against. Nothing
	// checks that the definition is recorded — a run may be filed before its
	// benchmark is — but a comparison across two different values here is
	// refused.
	Benchmark string `json:"benchmark"`
	// Split is which of the benchmark's declared splits the run covered.
	// Empty means the record does not say, and two runs cannot be compared
	// across differing splits.
	Split string `json:"split"`
	// System is what was under test — the model, the engine, the retrieval
	// policy. A baseline names its system here like any other run:
	// `naive-rag` is a system that was measured, never a category inside
	// somebody else's numbers. Empty means the record does not say, which a
	// comparison treats as a reason not to compare rather than as a match.
	System string `json:"system"`
	// Version is the system's own version at the time it was measured. Empty
	// means the record does not say, and two runs of one system at unrecorded
	// versions are not known to be two runs of the same thing.
	Version string `json:"version"`
	// Baseline marks a run as the reference the others are read against. It
	// changes nothing about how the run is stored or compared; it is how a
	// board knows which line to draw first.
	Baseline bool `json:"baseline"`
	// Study is the Study id this run belongs to, when it belongs to one. A run
	// names its study; it never names a paper.
	Study string `json:"study"`
	// Embedder is the embedding model the run retrieved with. Empty means the
	// record does not say, or the run retrieved nothing.
	Embedder string `json:"embedder"`
	// Reader is the model that produced the answers. Empty means the record
	// does not say; the literal `none` means the run measured retrieval and
	// read nothing.
	Reader string `json:"reader"`
	// K is the retrieval depth. Null means the record does not say — never 0,
	// which would claim the run retrieved nothing.
	K *int `json:"k"`
	// Temperature is the reader's sampling temperature. Null means the record
	// does not say — never 0, which is a deliberate and common setting and so
	// the most expensive possible default.
	Temperature *float64 `json:"temperature"`
	// MaxTokens is the reader's answer cap. Null means the record does not
	// say.
	MaxTokens *int `json:"max_tokens"`
	// Prompt is the prompt FILE the run read, by path. The file is what makes
	// a prompt reproducible; the digest below is what makes it checkable.
	Prompt string `json:"prompt"`
	// PromptDigest is the sha256 of that file's bytes as the harness recorded
	// it. Two runs whose prompt digests differ read different instructions,
	// whatever their prompt paths say.
	PromptDigest string `json:"prompt_digest"`
	// DatasetDigest is the sha256 of the dataset the run scored against. It is
	// the strongest single guard on comparability: two runs over different
	// dataset digests measured different items.
	DatasetDigest string `json:"dataset_digest"`
	// StoreDigest is the sha256 of the retrieval store the run queried —
	// the index, with whatever was in it.
	StoreDigest string `json:"store_digest"`
	// FactsDigest is the sha256 of the extracted-fact layer the run retrieved
	// over, for runs that have one.
	FactsDigest string `json:"facts_digest"`
	// Commit is the commit the harness was frozen at. Together with the
	// digests above and the knobs beside them it is what "re-runnable from its
	// own record" means.
	Commit string `json:"commit"`
	// Questions is how many questions the run set out to answer — the
	// DENOMINATOR. Null means the record does not say, and a run that does not
	// say cannot be shown as finished. This field and the next are why this
	// type exists.
	Questions *int `json:"questions"`
	// Answered is how many of them it actually answered. Null means the record
	// does not say. Below Questions the run is partial; above it the two
	// counts contradict each other and the run is inconsistent.
	Answered *int `json:"answered"`
	// When is when the run started, RFC3339. Empty means the record does not
	// say. It is what ?since= and ?until= bound a listing by.
	When string `json:"when"`
	// Ended is when it stopped, RFC3339. Empty means the record does not say
	// it ever did — which alone keeps completion at unknown, however well the
	// counts agree.
	Ended string `json:"ended"`
	// By names who ran it: the person, the job, the fleet. Empty means the
	// record does not say.
	By string `json:"by"`
	// Completion is DERIVED on read from Questions, Answered and Ended, and is
	// never stored or taken from a write: complete, partial, inconsistent or
	// unknown. It is the field a published number must be checked against.
	Completion string `json:"completion"`
	// Key is the measurement's identity, DERIVED on read and never taken from
	// a write: sha256 over what decides the number — benchmark, split, system,
	// version, k, temperature, max_tokens, embedder, reader and the four
	// digests — and over nothing about who ran it, when, at which commit or
	// what it found. Two runs with one key measured the same thing, so a
	// harness asks ?key= before it spends and measures again only when told to.
	Key string `json:"key"`
	// Measures are the run's numbers, one row per (category, metric), with the
	// intervals the harness reported.
	Measures []Measure `json:"measures"`
	// Notes is anything about this execution a reader needs that no field
	// above carries — a fallback that was hit, a category that was skipped.
	Notes string `json:"notes"`
	// Revision is this version's declared kind: original, corrected or
	// retracted. Anything else, including empty, reads as original. A
	// retracted latest version WITHDRAWS the id; every earlier version stays
	// retained.
	Revision string `json:"revision"`
	// Canonical is DERIVED on read, never stored: a version is canonical when
	// it is the latest-appended non-retracted version of its id.
	Canonical bool `json:"canonical"`
	// Visibility is private, org or public. A write forces private whatever the
	// body says; only a grant moves it.
	Visibility string `json:"visibility"`
	// Trainable is consent to train on this run. A write forces false; only a
	// grant sets it. Recording a run conveys no training right.
	Trainable bool `json:"trainable"`
	// Publishable is consent to publish this run to the commons. A write forces
	// false; only a grant sets it.
	Publishable bool `json:"publishable"`
}

// Study is the QUESTION a set of runs was run to answer, and the answer it
// reached. It is what keeps a run from being read as a standalone score: a run
// belongs to an inquiry, and an inquiry can be wrong.
type Study struct {
	// Improvement enables automatic, deterministic review of this study's runs.
	// Every candidate is compared with the named baseline using these declared
	// metrics; results become private DRAFT research actions, never deployments.
	Improvement *ImprovementPolicy `json:"improvement,omitempty"`
	// Project is the sub-scope inside the org this study is filed under — the
	// SERVER's value on a write.
	Project string `json:"project"`
	// ID is the study's stable id, a slug. Runs name it in their `study`.
	// Required on a write.
	ID string `json:"id"`
	// Title is what the study is called.
	Title string `json:"title"`
	// Question is what it set out to answer, in a sentence. A study without
	// one is a folder.
	Question string `json:"question"`
	// Finding is what the runs showed, once they had. Empty while the study is
	// open — and a finding recorded before the runs completed is the failure
	// this whole record exists to make visible.
	Finding string `json:"finding"`
	// Revision is this version's declared kind: original, corrected or
	// retracted, on the same terms as every other record here.
	Revision string `json:"revision"`
	// Canonical is DERIVED on read: the latest-appended non-retracted version
	// of this id.
	Canonical bool `json:"canonical"`
	// When is when the study was opened, RFC3339. Empty means the record does
	// not say.
	When string `json:"when"`
}

// Paper is WRITING that cites runs. The edge points this way only: a paper
// names the runs it rests on, and no run is altered by being cited.
type Paper struct {
	// Project is the sub-scope inside the org this paper is filed under — the
	// SERVER's value on a write.
	Project string `json:"project"`
	// ID is the paper's stable id, a slug or a HIP number. Required on a write.
	ID string `json:"id"`
	// Title is the paper's title.
	Title string `json:"title"`
	// Authors are its authors, in the order they are credited.
	Authors []string `json:"authors"`
	// Venue is where it appeared, or is to appear.
	Venue string `json:"venue"`
	// DOI is its DOI, when it has one.
	DOI string `json:"doi"`
	// URL is where it can be read.
	URL string `json:"url"`
	// Status is where the writing has got to: draft, preprint, published or
	// withdrawn. It is free text beyond those — an unrecognized value is kept
	// verbatim rather than flattened, so a producer's own label is never shown
	// as something it did not say.
	Status string `json:"status"`
	// Cites are the Run ids the paper's claims rest on. This is the whole
	// point of the type: a paper that quotes a number names the execution it
	// came from, and that execution's completion can then be checked.
	Cites []string `json:"cites"`
	// Study is the Study id this paper reports, when it reports one.
	Study string `json:"study"`
	// Revision is this version's declared kind: original, corrected or
	// retracted, on the same terms as every other record here.
	Revision string `json:"revision"`
	// Canonical is DERIVED on read: the latest-appended non-retracted version
	// of this id.
	Canonical bool `json:"canonical"`
	// When is when the paper was recorded, RFC3339. Empty means the record does
	// not say.
	When string `json:"when"`
}

// Difference is one configuration field two runs disagree about, with both
// values written out. It is the explicit half of a comparison: a delta between
// two runs that differ in their reader is a fact about two readers, and the
// only way a reader notices is if the difference is named.
type Difference struct {
	// Field is the configuration field, by its wire name: reader, k,
	// dataset_digest.
	Field string `json:"field"`
	// A is the field's value on the first run, rendered as text. An empty
	// string is the record not saying; it is distinguished from a value of
	// "0" or "false".
	A string `json:"a"`
	// B is the same field's value on the second run.
	B string `json:"b"`
}

// Delta is one metric the two runs BOTH measured on one category they both
// covered, and the change from the first to the second.
type Delta struct {
	// Category is the slice both runs measured this metric on.
	Category string `json:"category"`
	// Metric is the quantity, in whatever unit both runs reported it.
	Metric string `json:"metric"`
	// A is the first run's value.
	A float64 `json:"a"`
	// B is the second run's value.
	B float64 `json:"b"`
	// Change is B minus A, in the metric's own unit. Nothing is normalized and
	// nothing is turned into a percentage, so a change is only ever as
	// meaningful as the two values it came from.
	Change float64 `json:"change"`
	// Overlap says whether the two reported intervals overlap. Null means at
	// least one run reported no interval, so nothing is claimed. True means the
	// change is inside the noise BOTH runs already declared, which is the
	// answer most published deltas actually have.
	Overlap *bool `json:"overlap"`
}

// Comparison is two runs read side by side, with the reasons they may not be
// compared stated FIRST. It is the operation this record exists to serve: a
// board that shows two numbers next to each other has already made the claim
// that they are numbers of the same thing, and that claim is exactly what goes
// wrong.
type Comparison struct {
	// A is the first run, canonical version, as a read returns it.
	A *Run `json:"a"`
	// B is the second run.
	B *Run `json:"b"`
	// Comparable is true only when Blocks is empty. It is the single bit a
	// caller should branch on before rendering a delta.
	Comparable bool `json:"comparable"`
	// Blocks are the reasons these two runs are not comparable, each in a
	// sentence — a different benchmark, a different split, different dataset
	// bytes, or either run not established as complete. Empty means none was
	// found, which is not a guarantee of comparability: it is the absence of
	// the failures this record can see.
	Blocks []string `json:"blocks"`
	// Config is every configuration field the two runs disagree about, whether
	// or not they are comparable. A comparable pair with a long Config list is
	// the normal case — it is what the pair is FOR — and reading a delta
	// without reading this is how a prompt change gets published as a model
	// improvement.
	Config []Difference `json:"config"`
	// Deltas are the per-(category, metric) changes over what both runs
	// measured, ordered by category then metric. A metric only one of them
	// reported is left out rather than compared against nothing.
	Deltas []Delta `json:"deltas"`
}

// completionOf derives a run's completion from what the record actually says.
//
// The rule is deliberately hard to satisfy, because the failure it guards
// against is a number published from a run that had not finished — and a
// harness that checkpoints as it goes produces exactly the record a finished run
// produces, minus the denominator. So a count that is absent is never assumed,
// and counts that agree are not enough on their own: something has to say the
// run ended.
func completionOf(questions, answered *int, ended string) string {
	if questions == nil || answered == nil {
		return completionUnknown
	}
	switch {
	case *answered > *questions:
		return completionInconsistent
	case *answered < *questions:
		return completionPartial
	case strings.TrimSpace(ended) == "":
		return completionUnknown
	default:
		return completionComplete
	}
}

// keyOf is a run's measurement identity: the hex sha256 of the identity fields
// in this order, each written as text and followed by one NUL byte — benchmark,
// split, system, version, k, temperature, max_tokens, embedder, reader,
// prompt_digest, dataset_digest, store_digest, facts_digest. An absent number is
// the empty string; k and max_tokens are decimal integers; temperature is the
// shortest decimal that reads back as the same float64, never in exponent form
// (0.7, 0, 0.0000001). The hanzo-research SDK writes the same bytes and both
// sides test the same vector (TestKeyVector), so a key a client computes before
// it spends is the key this listing filters on.
//
// Commit is left out on purpose: a re-run of an unchanged configuration at a
// later commit of the harness is the spend this key exists to avoid. A change
// that moves a number belongs in a field the key covers — a digest or the
// version — and then it is a new key.
func keyOf(r Run) string {
	temperature := ""
	if r.Temperature != nil {
		temperature = strconv.FormatFloat(*r.Temperature, 'f', -1, 64)
	}
	return hashJoin(r.Benchmark, r.Split, r.System, r.Version,
		itoa(r.K), temperature, itoa(r.MaxTokens), r.Embedder, r.Reader,
		r.PromptDigest, r.DatasetDigest, r.StoreDigest, r.FactsDigest)
}

// setup is the configuration a comparison diffs: the fields that change a
// number, each rendered as text so an absent value ("") is distinguishable from
// a present zero ("0"). One list, in one place, so "what counts as
// configuration" has one answer — and so adding a knob to Run and forgetting to
// compare it is one edit, not two.
func (r Run) setup() []Difference {
	return []Difference{
		{Field: "benchmark", A: r.Benchmark},
		{Field: "split", A: r.Split},
		{Field: "system", A: r.System},
		{Field: "version", A: r.Version},
		{Field: "embedder", A: r.Embedder},
		{Field: "reader", A: r.Reader},
		{Field: "k", A: itoa(r.K)},
		{Field: "temperature", A: ftoa(r.Temperature)},
		{Field: "max_tokens", A: itoa(r.MaxTokens)},
		{Field: "prompt", A: r.Prompt},
		{Field: "prompt_digest", A: r.PromptDigest},
		{Field: "dataset_digest", A: r.DatasetDigest},
		{Field: "store_digest", A: r.StoreDigest},
		{Field: "facts_digest", A: r.FactsDigest},
		{Field: "commit", A: r.Commit},
		{Field: "baseline", A: strconv.FormatBool(r.Baseline)},
	}
}

// compare reads two runs side by side: what stops them being comparable, what
// they were configured differently for, and what changed where both measured
// the same thing.
func compare(a, b Run) Comparison {
	c := Comparison{A: &a, B: &b}

	// Blocks first, and stated as facts about the pair rather than as a code.
	// Each names BOTH values, because "different split" without the two splits
	// sends the reader back to the two records to find out which.
	if a.Benchmark != b.Benchmark {
		c.Blocks = append(c.Blocks, "different benchmarks: "+quote(a.Benchmark)+" and "+quote(b.Benchmark))
	}
	if a.Split != b.Split {
		c.Blocks = append(c.Blocks, "different splits: "+quote(a.Split)+" and "+quote(b.Split))
	}
	if a.DatasetDigest != b.DatasetDigest {
		c.Blocks = append(c.Blocks, "different dataset digests: "+quote(a.DatasetDigest)+" and "+quote(b.DatasetDigest)+" — the two runs scored different items")
	}
	for _, r := range []Run{a, b} {
		if r.Completion != completionComplete {
			c.Blocks = append(c.Blocks, "run "+quote(r.ID)+" is not established as complete (completion "+r.Completion+")")
		}
		// Two runs whose system is unrecorded produce a delta in something, and
		// nothing says in what. That is not a missing label; it is the absence
		// of the fact a comparison is about.
		if r.System == "" {
			c.Blocks = append(c.Blocks, "run "+quote(r.ID)+" does not name the system under test")
		}
	}
	c.Comparable = len(c.Blocks) == 0

	// Configuration difference, always — an incomparable pair is the pair whose
	// differences most need naming.
	as, bs := a.setup(), b.setup()
	for i := range as {
		if as[i].A != bs[i].A {
			c.Config = append(c.Config, Difference{Field: as[i].Field, A: as[i].A, B: bs[i].A})
		}
	}

	// Deltas over the intersection. A metric only one side reported is left out:
	// comparing it against nothing would put a number where an absence belongs.
	index := map[string]Measure{}
	for _, m := range b.Measures {
		index[m.Category+"\x00"+m.Metric] = m
	}
	for _, m := range a.Measures {
		n, ok := index[m.Category+"\x00"+m.Metric]
		if !ok {
			continue
		}
		c.Deltas = append(c.Deltas, Delta{
			Category: m.Category, Metric: m.Metric,
			A: m.Value, B: n.Value, Change: n.Value - m.Value,
			Overlap: overlap(m, n),
		})
	}
	sort.Slice(c.Deltas, func(i, j int) bool {
		if c.Deltas[i].Category != c.Deltas[j].Category {
			return c.Deltas[i].Category < c.Deltas[j].Category
		}
		return c.Deltas[i].Metric < c.Deltas[j].Metric
	})
	return c
}

// overlap reports whether two measures' reported intervals intersect, or nil
// when either reported none. A change inside two overlapping intervals is
// inside noise both runs already declared — the answer most published deltas
// have, and the one a bare subtraction hides.
func overlap(m, n Measure) *bool {
	if m.Lo == nil || m.Hi == nil || n.Lo == nil || n.Hi == nil {
		return nil
	}
	v := math.Max(*m.Lo, *n.Lo) <= math.Min(*m.Hi, *n.Hi)
	return &v
}

// quote renders a value for a block sentence, so an EMPTY value reads as empty
// rather than vanishing into the prose around it.
func quote(s string) string {
	if s == "" {
		return "(unrecorded)"
	}
	return strconv.Quote(s)
}

// itoa and ftoa render an optional number for the configuration diff: absent is
// "" and present is the number, so a null and a zero are never the same text.
func itoa(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func ftoa(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

// within reports whether an RFC3339 instant falls inside [since, until], where
// either bound may be empty (unbounded). A record with no instant, or one that
// does not parse, never satisfies a BOUNDED filter — an unknown date is not
// evidence of falling in a range.
func within(when, since, until string) bool {
	if since == "" && until == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		return false
	}
	if since != "" {
		s, err := time.Parse(time.RFC3339, since)
		if err != nil || t.Before(s) {
			return false
		}
	}
	if until != "" {
		u, err := time.Parse(time.RFC3339, until)
		if err != nil || t.After(u) {
			return false
		}
	}
	return true
}

// ── the surface ───────────────────────────────────────────────────────────────
//
// Nine addresses, in one shape: a POST that records a batch of one kind, a GET
// that lists that kind narrowed by the filters that matter, and one GET that
// reads two runs against each other. A list narrowed by `?id=` IS the read of a
// single record, so there is one address per direction per kind and no second
// way to fetch one thing. The run listing is also the one read open to a caller
// with no org: the runs the org `?org=` names granted public.

// recordBatch is the answer every record-a-batch call gives: what this call
// added and what the store now holds, so a caller sees the versioned truth
// rather than a dedup that reads as loss.
type recordBatch struct {
	// Project is the project the batch was filed under — the SERVER's value,
	// never the body's.
	Project string `json:"project"`
	// Recorded is how many NEW versions this call appended. Zero on a
	// byte-identical re-send, which is what makes a backfill replayable.
	Recorded int `json:"recorded"`
	// Canonical is how many distinct records of this kind the store now holds
	// — the deduped view, retractions withdrawn.
	Canonical int `json:"canonical"`
}

// benchmarkBatch is one upload of benchmark definitions.
type benchmarkBatch struct {
	// Benchmarks are the definitions to record. Each needs an id. At most
	// 20000 in one call.
	Benchmarks []ResearchBenchmark `json:"benchmarks"`
}

// RecordResearchBenchmarks records benchmark definitions — the TASK a run is
// measured against, with its dataset, that dataset's licence and origin, its
// declared splits, its official metric and how to cite it. A definition is
// independent of anyone's run and is recorded on its own: re-sending identical
// content appends nothing, and changing any of it appends a new version beside
// the old one rather than replacing it.
//
// Example: {"benchmarks": [{"id": "locomo", "title": "LoCoMo", "dataset": "snap-research/locomo", "license": "CC BY-NC 4.0", "metric": "f1"}]}
func (o ops) postBenchmarks(ctx context.Context, in *benchmarkBatch) (*recordBatch, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := project(ctx)
	if err := bounded(len(in.Benchmarks), in.Benchmarks); err != nil {
		return nil, err
	}
	for _, b := range in.Benchmarks {
		if b.ID == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "benchmark needs an id")
		}
	}
	added, err := st.putBenchmarks(ctx, proj, in.Benchmarks)
	if err != nil {
		o.s.Log.Error("research benchmark write failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research benchmark write failed")
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	all, err := st.benchmarks(ctx, "", "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "research benchmark count failed")
	}
	return &recordBatch{Project: proj, Recorded: added, Canonical: len(all)}, nil
}

// benchmarksIn narrows a benchmark listing. Both are query parameters.
type benchmarksIn struct {
	// Project narrows to one project. Empty reads the org's whole set.
	Project string `json:"project"`
	// ID narrows to exactly one benchmark, which is how a single definition is
	// read.
	ID string `json:"id"`
}

// benchmarksOut is a canonical benchmark listing.
type benchmarksOut struct {
	// Data are the canonical benchmark definitions, ordered by project then id.
	Data []ResearchBenchmark `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListResearchBenchmarks returns the caller org's benchmark definitions — what
// each task is, which dataset it draws on under which licence, the splits it
// declares and the metric its authors score it by. ?id= narrows to one, which is
// how one definition is read; ?project= narrows to one project.
func (o ops) listBenchmarks(ctx context.Context, in *benchmarksIn) (*benchmarksOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	bs, err := st.benchmarks(ctx, in.Project, in.ID)
	if err != nil {
		o.s.Log.Error("research benchmark list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research benchmark list failed")
	}
	return &benchmarksOut{Data: bs, Total: len(bs)}, nil
}

// runBatch is one upload of run records.
type runBatch struct {
	// Runs are the executions to record. Each needs an id and a benchmark. A
	// system is not required: a tree that does not name what was under test
	// cannot be made to, and demanding the field is how one gets invented. At
	// most 20000 in one call.
	Runs []Run `json:"runs"`
}

// RecordResearchRuns records executions: what ran, against which task and split,
// under which configuration, frozen at which commit, over which dataset bytes,
// with the numbers it produced and — the field the rest is in service of —
// whether it reached the end of the questions it set out to answer.
//
// Completion is DERIVED from `questions`, `answered` and `ended`, never taken
// from the body. A record that omits a count leaves completion `unknown` rather
// than reading as finished, which is the whole difference between this and a
// harness checkpoint published as a result. Consent is forced private: recording
// a run grants no training or publication right, which is a separate call.
//
// Example: {"runs": [{"id": "locomo-all-context-k20-enso-flash", "benchmark": "locomo", "split": "all", "system": "enso-flash", "questions": 282, "answered": 282, "ended": "2026-09-10T21:24:01Z"}]}
func (o ops) postRuns(ctx context.Context, in *runBatch) (*recordBatch, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := project(ctx)
	if err := bounded(len(in.Runs), in.Runs); err != nil {
		return nil, err
	}
	for _, r := range in.Runs {
		if r.ID == "" || r.Benchmark == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "run needs an id and a benchmark")
		}
	}
	added, err := st.putRuns(ctx, proj, in.Runs)
	if err != nil {
		o.s.Log.Error("research run write failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research run write failed")
	}
	if err := o.reviewImprovements(ctx, st, org, proj); err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	if err := o.deliverImprovementEvents(ctx, st, org); err != nil {
		return nil, err
	}
	// Roll up best-effort, exactly as the experiment ingest does: the SQLite
	// write above is the source of truth, so a datastore outage degrades the
	// cross-project view and never fails a recorded run.
	if err := o.s.State.wh.rollUpRuns(ctx, org, proj, in.Runs, time.Now()); err != nil {
		o.s.Log.Warn("research run roll-up skipped; SQLite retains the write", "org", org, "err", err)
	}
	all, err := st.runs(ctx, runFilter{})
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "research run count failed")
	}
	return &recordBatch{Project: proj, Recorded: added, Canonical: len(all)}, nil
}

// runsIn narrows a run listing. All are query parameters.
type runsIn struct {
	// Project narrows to one project. Empty reads the org's whole set.
	Project string `json:"project"`
	// ID narrows to the runs with these ids: one, which is how a single run is
	// read, or several separated by commas, any of which a run may carry.
	ID string `json:"id"`
	// Key narrows to the runs of one measurement identity — the derived `key`
	// a run carries, which a client computes the same way before it spends.
	// A complete run here is a measurement already paid for.
	Key string `json:"key"`
	// Benchmark narrows to the runs measured against these tasks: one, or
	// several separated by commas, any of which a run may have measured.
	Benchmark string `json:"benchmark"`
	// System narrows to the runs of one system under test — which is also how
	// a baseline's runs are listed, since a baseline is a system that was run.
	System string `json:"system"`
	// Study narrows to the runs belonging to one study.
	Study string `json:"study"`
	// Completion narrows by the DERIVED completion: complete, partial,
	// inconsistent or unknown. `partial` is the query that finds runs whose
	// numbers were published before they finished. Anything outside those four
	// is refused rather than quietly matching nothing.
	Completion string `json:"completion"`
	// Since bounds the listing to runs that STARTED at or after this RFC3339
	// instant. A run whose start is unrecorded never satisfies a bounded
	// filter — an unknown date is not evidence of falling in a range.
	Since string `json:"since"`
	// Until bounds the listing to runs that started at or before this RFC3339
	// instant, on the same terms as Since.
	Until string `json:"until"`
	// Org is the org whose runs a caller with no org of its own reads, and then
	// only the runs that org granted public. A caller with an org reads its own,
	// and this is not consulted.
	Org string `json:"org"`
	// Category keeps, of each run's measures, those whose category is one of
	// these (comma-separated) or lies under one: `vs-claude-code` keeps
	// `vs-claude-code/search`. It narrows a run's measures and never drops the
	// run.
	Category string `json:"category"`
	// Metric keeps, of each run's measures, those whose metric is one of these
	// (comma-separated). It narrows a run's measures and never drops the run.
	Metric string `json:"metric"`
	// View is how much of each run the answer carries. Empty is the full record.
	// `summary` is what a page draws: each run without its key, digests, prompt,
	// embedder, reader, k, temperature, max_tokens, when, by, trainable or
	// publishable, each measure without the interval and counts it does not
	// state, and the listing without gaps. Anything else is refused.
	View string `json:"view"`
	// Limit is how many runs one page carries, 1 to 200. Absent or 0, it is
	// 100: a listing longer than that is read by its `next`.
	Limit int `json:"limit"`
	// Cursor continues a listing: pass back the previous page's `next`, with the
	// same query. Omit it for the first page.
	Cursor string `json:"cursor"`
}

// Census is a set of runs counted by their derived completion. The four fields
// are the whole closed set, so they sum to the listing's total and a reader
// never has to work out what a missing one meant.
type Census struct {
	// Complete: the run says it answered every question it set out to and says
	// when it ended.
	Complete int `json:"complete"`
	// Partial: the run says it answered fewer. A number taken from one of
	// these was taken from a checkpoint.
	Partial int `json:"partial"`
	// Inconsistent: the run's own two counts cannot both be true.
	Inconsistent int `json:"inconsistent"`
	// Unknown: the run does not establish completion. This is the common case
	// for anything recorded before a lane wrote its counts, and it is reported
	// rather than rounded toward either of the other three.
	Unknown int `json:"unknown"`
}

// runsOut is a canonical run listing, with the two facts that say how much of it
// is actually recorded.
type runsOut struct {
	// Data are the canonical runs, each with its derived completion, ordered
	// by project then id: one page of them.
	Data []Run `json:"data"`
	// Total is how many runs the query matched, across every page.
	Total int `json:"total"`
	// Next continues the listing: pass it back as `cursor`. Absent on the last
	// page.
	Next string `json:"next,omitempty"`
	// Completion counts the matched runs, across every page, by completion.
	// Read it before reading any aggregate over Data: a set whose Unknown or
	// Partial is a large share of Total is a set whose average is over runs
	// that may not have finished.
	Completion Census `json:"completion"`
	// Gaps is one field per entry that some matched run leaves unstated, with
	// how many leave it unstated, worst first. It is the record's account of
	// its own incompleteness, and it is answered on every listing rather than
	// discovered by reading the rows: a field nobody records is a comparison
	// nobody can make.
	Gaps []Gap `json:"gaps"`
}

// censusOf counts a set of runs by their derived completion.
func censusOf(rs []Run) Census {
	var t Census
	for _, r := range rs {
		switch r.Completion {
		case completionComplete:
			t.Complete++
		case completionPartial:
			t.Partial++
		case completionInconsistent:
			t.Inconsistent++
		default:
			t.Unknown++
		}
	}
	return t
}

// ListResearchRuns returns the caller org's recorded executions with their
// derived completion. ?id= narrows to one, which is how a single run is read;
// ?benchmark=, ?system=, ?study=, ?since= and ?until= narrow the rest; and
// ?completion=partial is the query this record was built to answer — which runs
// were reported before they finished.
//
// ?key= is the read a harness makes before it spends: every run carries `key`,
// the sha256 of what decides its number (benchmark, split, system, version, k,
// temperature, max_tokens, embedder, reader and the four digests), and a
// complete run under the key it is about to measure is a measurement already
// held. With ?completion=complete beside it the answer is empty or the hit.
//
// Every listing also answers how much of itself is recorded: `completion` counts
// the matched runs by the four completion values, and `gaps` names each field
// some matched run leaves unstated with how many leave it unstated. Both are
// computed over the runs the filter matched, so narrowing the listing narrows
// the account of it.
//
// ?id= and ?benchmark= each take a comma-separated list, any of which a run may
// match. ?category= and ?metric= narrow each run's measures to the slices and
// quantities named, and drop no run. ?view=summary serves each run with what a
// page draws and nothing a harness re-runs it from. The listing comes in pages
// of ?limit= runs (1 to 200, 100 when it names none) in its order, project then
// id: `total` counts every matched run, and `next`, passed back as ?cursor= with
// the same query, reads the page after; the last page carries none.
//
// A caller with no org of its own, no credential at all, reads the runs the org
// `?org=` names granted public (POST /v1/research/grants) and no others. Until
// that org has published a run the answer is the 404 an address nobody serves
// gets, so the read tells nobody which orgs use research; after, a private run is
// absent from the listing exactly as a run never recorded is. That read serves an
// Enso or Zen run without its version, notes, prompt, embedder or reader, an
// enso.* benchmark's runs without notes, and no run of the base models those
// families and Kai are built from; its `key` is derived over what it serves. The
// org's own read is the full record.
func (o ops) listRuns(ctx context.Context, in *runsIn) (*zip.Or[runsOut, runsSummary], error) {
	if _, ok := principal.OrgFrom(ctx); !ok && in.Org != "" {
		return o.public(ctx, in)
	}
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	q, err := in.query()
	if err != nil {
		return nil, err
	}
	rs, err := st.runs(ctx, q.filter)
	if err != nil {
		o.s.Log.Error("research run list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research run list failed")
	}
	return q.answer(rs), nil
}

// public is the listing for a caller with no org of its own: the runs in.Org
// granted public, each as [served] serves it, narrowed as any listing is over
// what is served. The filter is judged only once that org has published, because
// a 422 before then would tell a stranger it holds research at all.
func (o ops) public(ctx context.Context, in *runsIn) (*zip.Or[runsOut, runsSummary], error) {
	st, ok, err := published(o.s.State.stores, in.Org)
	if err != nil {
		o.s.Log.Error("research store open failed", "org", in.Org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research store unavailable")
	}
	if !ok {
		return nil, missing()
	}
	all, err := st.runs(ctx, runFilter{})
	if err != nil {
		o.s.Log.Error("research run list failed", "org", in.Org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research run list failed")
	}
	var pub []Run
	p := tierPrices(ctx)
	for _, r := range all {
		if r.Visibility != "public" {
			continue
		}
		if s, ok := served(r, p); ok {
			pub = append(pub, s)
		}
	}
	if len(pub) == 0 {
		return nil, missing()
	}
	q, err := in.query()
	if err != nil {
		return nil, err
	}
	var rs []Run
	for _, r := range pub {
		if q.filter.keeps(r) {
			rs = append(rs, r)
		}
	}
	return q.answer(rs), nil
}

// missing is the answer an address nobody serves gets, the bytes cloud.Stage
// refuses with, so an org that published nothing reads as one outside research.
func missing() error { return zip.ErrNotFound(http.StatusText(http.StatusNotFound)) }

// studyBatch is one upload of studies.
type studyBatch struct {
	// Studies are the inquiries to record. Each needs an id. At most 20000 in
	// one call.
	Studies []Study `json:"studies"`
}

// RecordResearchStudies records the questions runs were run to answer, and the
// findings they reached. A study is what keeps a run from being read as a
// standalone score.
//
// Example: {"studies": [{"id": "memory-retrieval-2026", "question": "does graph-shaped retrieval beat dense-only on long conversations"}]}
func (o ops) postStudies(ctx context.Context, in *studyBatch) (*recordBatch, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := project(ctx)
	if err := bounded(len(in.Studies), in.Studies); err != nil {
		return nil, err
	}
	for _, s := range in.Studies {
		if s.ID == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "study needs an id")
		}
		if err := validateImprovement(s.Improvement); err != nil {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "study %s: %v", s.ID, err)
		}
	}
	added, err := st.putStudies(ctx, proj, in.Studies)
	if err != nil {
		o.s.Log.Error("research study write failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research study write failed")
	}
	if err := o.reviewImprovements(ctx, st, org, proj); err != nil {
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	if err := o.deliverImprovementEvents(ctx, st, org); err != nil {
		return nil, err
	}
	all, err := st.studies(ctx, "", "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "research study count failed")
	}
	return &recordBatch{Project: proj, Recorded: added, Canonical: len(all)}, nil
}

// studiesIn narrows a study listing. Both are query parameters.
type studiesIn struct {
	// Project narrows to one project. Empty reads the org's whole set.
	Project string `json:"project"`
	// ID narrows to exactly one study, which is how a single study is read.
	ID string `json:"id"`
}

// studiesOut is a canonical study listing.
type studiesOut struct {
	// Data are the canonical studies, ordered by project then id.
	Data []Study `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListResearchStudies returns the caller org's studies — the question each set
// of runs was run to answer and what it found. ?id= narrows to one.
func (o ops) listStudies(ctx context.Context, in *studiesIn) (*studiesOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	ss, err := st.studies(ctx, in.Project, in.ID)
	if err != nil {
		o.s.Log.Error("research study list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research study list failed")
	}
	return &studiesOut{Data: ss, Total: len(ss)}, nil
}

// paperBatch is one upload of papers.
type paperBatch struct {
	// Papers are the writings to record. Each needs an id. At most 20000 in
	// one call.
	Papers []Paper `json:"papers"`
}

// RecordResearchPapers records writing that CITES runs. The edge points one way:
// a paper names the run ids its claims rest on, and no run record is altered by
// being cited — so a paper's numbers can be checked against the completion of
// the executions they came from.
//
// Example: {"papers": [{"id": "hip-1145", "title": "Research — The Experiment Record", "status": "published", "cites": ["locomo-all-context-k20-enso-flash"]}]}
func (o ops) postPapers(ctx context.Context, in *paperBatch) (*recordBatch, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := project(ctx)
	if err := bounded(len(in.Papers), in.Papers); err != nil {
		return nil, err
	}
	for _, p := range in.Papers {
		if p.ID == "" {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "paper needs an id")
		}
	}
	added, err := st.putPapers(ctx, proj, in.Papers)
	if err != nil {
		o.s.Log.Error("research paper write failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research paper write failed")
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	all, err := st.papers(ctx, "", "", "", "")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "research paper count failed")
	}
	return &recordBatch{Project: proj, Recorded: added, Canonical: len(all)}, nil
}

// papersIn narrows a paper listing. All are query parameters.
type papersIn struct {
	// Project narrows to one project. Empty reads the org's whole set.
	Project string `json:"project"`
	// ID narrows to exactly one paper, which is how a single paper is read.
	ID string `json:"id"`
	// Study narrows to the papers reporting one study.
	Study string `json:"study"`
	// Cites narrows to the papers that cite one run id — the read that answers
	// "what did we publish off this run", and the one to run before correcting
	// a number.
	Cites string `json:"cites"`
}

// papersOut is a canonical paper listing.
type papersOut struct {
	// Data are the canonical papers, ordered by project then id.
	Data []Paper `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ListResearchPapers returns the caller org's papers and the runs each cites.
// ?id= narrows to one, ?study= to the papers reporting one study, and ?cites= to
// the papers resting on one run — which is how a correction finds everything it
// invalidates.
func (o ops) listPapers(ctx context.Context, in *papersIn) (*papersOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := st.papers(ctx, in.Project, in.ID, in.Study, in.Cites)
	if err != nil {
		o.s.Log.Error("research paper list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research paper list failed")
	}
	return &papersOut{Data: ps, Total: len(ps)}, nil
}

// compareIn names the two runs to read against each other. All are query
// parameters.
type compareIn struct {
	// A is the first run's id — the reference, when one of the two is a
	// baseline.
	A string `json:"a"`
	// B is the second run's id. Deltas read B minus A.
	B string `json:"b"`
	// Project locates both runs. Empty takes the caller's project scope; both
	// runs are read from the same project, because two runs in different
	// projects were not filed as the same body of work.
	Project string `json:"project"`
}

// CompareResearchRuns reads two runs side by side and states FIRST the reasons
// they may not be compared — a different benchmark, a different split, different
// dataset bytes, or either run not established as complete. It then names every
// configuration field they disagree about, and only then the per-metric change
// over what both of them actually measured, with whether the reported intervals
// overlap.
//
// This is the operation the record exists to serve. Putting two numbers next to
// each other is already the claim that they are numbers of the same thing, and
// that claim is what goes wrong: a delta between a run frozen at one commit and
// a run frozen at another, or between a finished run and a checkpoint, is a
// difference in something other than the system under test. The answer here
// cannot be read without seeing which.
func (o ops) compareRuns(ctx context.Context, in *compareIn) (*Comparison, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if in.A == "" || in.B == "" {
		return nil, zip.ErrBadRequest("compare needs two run ids, a and b")
	}
	proj := in.Project
	if proj == "" {
		proj = project(ctx)
	}
	a, ok, err := st.run(ctx, proj, in.A)
	if err != nil {
		o.s.Log.Error("research compare failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research compare failed")
	}
	if !ok {
		return nil, zip.ErrNotFound("no canonical run " + strconv.Quote(in.A))
	}
	b, ok, err := st.run(ctx, proj, in.B)
	if err != nil {
		o.s.Log.Error("research compare failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "research compare failed")
	}
	if !ok {
		return nil, zip.ErrNotFound("no canonical run " + strconv.Quote(in.B))
	}
	c := compare(a, b)
	return &c, nil
}

// bounded is the per-batch guard every record-a-batch call leads with, so one
// request cannot pin the writer with an unbounded batch. The same bound the
// experiment ingest keeps, for the same reason. The items it is given are held to
// what a JSON column holds (holdable).
func bounded(n int, items ...any) error {
	if n == 0 {
		return zip.ErrBadRequest("nothing to record")
	}
	if n > maxBatchItems {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "batch too large; split the upload")
	}
	for _, v := range items {
		if err := holdable(v); err != nil {
			return err
		}
	}
	return nil
}

// holdable refuses a record the store's JSON column would: a string that holds a NUL.
// SQLite keeps it; PostgreSQL's jsonb refuses it at the statement, and a caller would
// see a 500 that names nothing. The check is the same on both engines, so a record
// that is recorded on one is recorded on the other, and a refusal says why. (A byte
// that is not UTF-8 never reaches here: decoding the request already replaced it.)
func holdable(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return zip.ErrBadRequest("record is not JSON")
	}
	if err := dialect.Document(raw); err != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%s", err)
	}
	return nil
}
