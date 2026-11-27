package benchmark

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// The measured plane's one way in.
//
// history and compare read per-item attempts, and nothing wrote any: this
// arena's own harness has not run, and every number on the board arrived as a
// claim. A claim is an aggregate, and an aggregate cannot be paired or followed
// over time — that needs the items.
//
// The items exist. A harness that measured a model wrote one verdict per item,
// and the research plane's Run records which execution that was: its commit, its
// digests, how many questions it set out to answer and how many it did. What was
// missing is a door, and the door is narrow on purpose, because a typed-in
// attempt is a fabricated measurement:
//
//   - Only a SuperAdmin acting in org admin imports. The measured plane is the
//     platform's, as the board is.
//   - An import is FOR A PUBLISHED CLAIM. The platform must already publish a
//     claim for the model on the benchmark whose source is the research run the
//     rows came from (…/v1/research/runs?…&id=<run>), and the rows must score
//     what the claim says to within half a tenth of a point. Items that do not
//     add up to the published number are refused, so the door can only ever
//     itemize a number already on the record — it cannot put a new one there.
//   - A run is imported once. The store is append-only, so a second import of a
//     run would double its items; a corrected file is a new run.
//   - Lower-is-better benchmarks are not imported. An attempt's verdict is
//     correct or not, and the board reads it as accuracy; a false-alarm rate is
//     not an accuracy, and storing one as if it were would invert it.

// maxItems bounds one import: the research plane's own batch bound, and more
// than any catalog benchmark holds.
const maxItems = 20000

// The bounds on one item: its id and the answer the harness extracted.
const (
	maxItemID     = 256
	maxItemAnswer = 1024
)

// tolerance is how far the rows' score may sit from the claim it itemizes, in
// points: half the last place of a score published to one decimal.
const tolerance = 0.05

// Item is one scored item of a run, as the harness that measured it wrote it.
type Item struct {
	// Item is the benchmark's own id for the question or case.
	Item string `json:"item"`
	// Correct is the harness's verdict on Answer.
	Correct bool `json:"correct"`
	// Answer is what the model answered, as the harness extracted it. Required:
	// an item with no answer was not attempted.
	Answer string `json:"answer"`
}

type importIn struct {
	// Benchmark is the catalog id the run measured. Required.
	Benchmark string `json:"benchmark" validate:"required"`
	// Model is the model the run measured, as the published claim names it.
	// Required.
	Model string `json:"model" validate:"required"`
	// Run is the research run the rows came from: the id the claim's source
	// names. Required.
	Run string `json:"run" validate:"required"`
	// At is when the run was measured, RFC3339. Empty is now. It orders the
	// model's runs in history and picks the latest for the board, so a backfill
	// names the date it measured.
	At string `json:"at,omitempty"`
	// Data is every item the run scored, at most 20000.
	Data []Item `json:"data" validate:"required"`
}

type importOut struct {
	// Imported is how many items were stored.
	Imported int `json:"imported"`
	// Benchmark is the catalog id they were stored under.
	Benchmark string `json:"benchmark"`
	// Model is the model they measured.
	Model string `json:"model"`
	// Run is the research run they came from.
	Run string `json:"run"`
	// Score is the rows' accuracy, in percent: what Measured now reads.
	Score float64 `json:"score"`
	// Claim is the published score the rows itemize.
	Claim float64 `json:"claim"`
	// Source is that claim's citation.
	Source string `json:"source"`
}

// importAttempts stores the per-item rows of a published run, so the board's
// measured column, history and compare read the items behind a number the
// platform already publishes.
//
// Only a SuperAdmin acting in org admin may import; anyone else is refused 403,
// and the refusal is audited.
//
// The rows are FOR A CLAIM: the platform must publish a claim for the model on
// the benchmark whose source names the run (a research run URL whose id is run),
// and the rows must score that claim to within 0.05 points, or the import is
// refused 422 saying which. A run is imported once (409 after), every item id is
// unique and carries an answer, and a lower-is-better benchmark is refused 422:
// its score is a rate the board does not read as accuracy.
//
// The trail takes the import's intent, naming the run and its counts, before the
// first row lands.
//
// Example: {"benchmark": "gpqa_diamond", "model": "enso", "run": "enso.gpqa_diamond-test-frontier-enso", "data": [{"item": "rec0", "correct": true, "answer": "B"}]}
func (o ops) importAttempts(ctx context.Context, in *importIn) (*importOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	by := strings.TrimSpace(cloud.Who(ctx).User)
	super := principal.SuperFrom(ctx)
	summary := map[string]any{"benchmark": in.Benchmark, "model": in.Model, "run": in.Run, "items": len(in.Data)}
	if org != curator || !super {
		_ = o.audit(ctx, importAction, org, by, super, audit.Outcome{Result: "deny", Status: http.StatusForbidden, Reason: "SuperAdmin acting in org admin required"}, summary)
		return nil, cloud.Super.Refusal()
	}
	st := o.s.State
	if st.store == nil || st.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "benchmark: attempt store, claims store or audit trail unavailable")
	}

	bench, model, run := strings.TrimSpace(in.Benchmark), strings.TrimSpace(in.Model), strings.TrimSpace(in.Run)
	b, ok := lookup(bench)
	switch {
	case !ok:
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "unknown benchmark %q", bench)
	case b.Direction != Higher:
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "%s improves downward: its score is a rate, not an accuracy, and is published as claims only", bench)
	case model == "" || run == "" || overlong(model, run):
		return nil, zip.ErrBadRequest(fmt.Sprintf("an import names its model and run, each at most %d characters", maxClaimName))
	case len(in.Data) == 0:
		return nil, zip.ErrBadRequest("an import carries the run's items")
	case len(in.Data) > maxItems:
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "benchmark: at most %d items per import", maxItems)
	}
	at := time.Now().UTC()
	if s := strings.TrimSpace(in.At); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil || t.After(at.Add(time.Minute)) {
			return nil, zip.ErrBadRequest("at is when the run was measured, RFC3339 and not in the future")
		}
		at = t.UTC()
	}

	seen := map[string]bool{}
	correct := 0
	rows := make([]attempt, 0, len(in.Data))
	for i, it := range in.Data {
		id := strings.TrimSpace(it.Item)
		switch {
		case id == "" || len(id) > maxItemID:
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "item %d: an id of 1 to %d bytes", i, maxItemID)
		case seen[id]:
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "item %d: %q appears twice", i, id)
		case it.Answer == "" || utf8.RuneCountInString(it.Answer) > maxItemAnswer:
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "item %d: an answer of 1 to %d characters", i, maxItemAnswer)
		}
		seen[id] = true
		if it.Correct {
			correct++
		}
		rows = append(rows, attempt{Benchmark: bench, ID: id, Model: model, Correct: it.Correct, Answer: it.Answer, Run: run, At: at})
	}
	score := float64(correct) / float64(len(rows)) * 100

	claim, ok, err := publishedFor(ctx, st.store, bench, model, run)
	if err != nil {
		return nil, o.gap(err)
	}
	if !ok {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "no published claim for %s on %s cites run %s: publish the claim first", model, bench, run)
	}
	if math.Abs(score-claim.Score) > tolerance {
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "the items score %.2f (%d of %d); the published claim is %.2f", score, correct, len(rows), claim.Score)
	}

	summary["correct"], summary["source"] = correct, claim.Source
	// A run is imported once: the check and the rows are one transaction under the store's
	// write lock, so two replicas handed one run cannot both import it.
	var intended bool
	err = st.store.write(ctx, func(b book) error {
		again, err := b.imported(ctx, bench, model, run)
		if err != nil {
			return err
		}
		if again {
			return zip.Errorf(http.StatusConflict, "run %s is imported for %s on %s already", run, model, bench)
		}
		if err := o.audit(ctx, importAction, org, by, super, audit.Outcome{Result: "intent"}, summary); err != nil {
			return err
		}
		intended = true
		return b.Append(ctx, rows...)
	})
	switch {
	case err == nil:
	case !intended || isRefusal(err):
		return nil, o.refused(err)
	default:
		o.s.Log.Error("benchmark: attempts not stored", "benchmark", bench, "model", model, "run", run, "err", err)
		_ = o.audit(ctx, importAction, org, by, super, audit.Outcome{Result: "error", Status: http.StatusInternalServerError, Reason: "not stored"}, summary)
		return nil, zip.Errorf(http.StatusInternalServerError, "benchmark: the items were not stored")
	}
	_ = o.audit(ctx, importAction, org, by, super, audit.Outcome{Result: "success", Status: http.StatusOK}, summary)
	return &importOut{Imported: len(rows), Benchmark: bench, Model: model, Run: run, Score: score, Claim: claim.Score, Source: claim.Source}, nil
}

// publishedFor is the platform's public claim for model on bench whose source is
// the research run named run.
func publishedFor(ctx context.Context, store claimReader, bench, model, run string) (storedClaim, bool, error) {
	claims, err := claimsFor(ctx, store, bench)
	if err != nil {
		return storedClaim{}, false, err
	}
	for _, c := range claims[model] {
		if citesRun(c.Source, run) {
			return c, true, nil
		}
	}
	return storedClaim{}, false, nil
}

// citesRun reports whether source is a research run address naming run: a URL
// whose path ends in /v1/research/runs and whose id parameter is run.
func citesRun(source, run string) bool {
	u, err := url.Parse(strings.TrimSpace(source))
	if err != nil || !strings.HasSuffix(u.Path, "/v1/research/runs") {
		return false
	}
	return u.Query().Get("id") == run
}
