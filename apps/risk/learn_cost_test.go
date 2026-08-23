package risk

// learn_cost_test.go — what the learning path COSTS, per event, measured.
//
// WHY THIS EXISTS AS A BENCHMARK AND NOT A COMMENT. The learning path used to run
// the model TWICE over every event it learned from: Inspect to build the verdict
// the response carried, then Assess to move the counters. Both enter the engine's
// `judge`, so both project the point — three aggregate reads each — and both walk
// the forest; and above the cut both run the counterfactual attribution, which is
// one further forest walk per dimension.
//
// A claim like that is either measured or it is decoration, and measuring it is
// what keeps it honest in BOTH directions. Removing the second pass took 12% off
// the operation per event and 8% of its allocations — not the half that counting
// model calls alone would predict, because the durable record and the aggregates
// are the larger part of what a caller waits for, and because the attribution is
// only reached by the share of the stream the appetite admits.
//
//	                    before            after
//	batch 8      31.5 µs/event      27.8 µs/event    -12%
//	batch 128    20.0 µs/event      17.5 µs/event    -12%
//	batch 128     6717 allocs       6169 allocs       -8%
//
// Re-measure with:
//
//	go test -tags "sqlite_purego sqlite_math_functions sqlite_fts5" \
//	  -run XXX -bench BenchmarkLearn -benchtime 200x -count 3 ./apps/risk/

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// benchStride is the id stride between iterations. Larger than any batch this
// file measures, so no two iterations — and no warm-up event — can mint the same
// id, which would take the inert-duplicate path and measure the wrong thing.
const benchStride = 1024

// benchBatchOf is one iteration's events, all with distinct ids.
func benchBatchOf(tb testing.TB, size, iter int, at time.Time) []observation {
	tb.Helper()
	out := make([]observation, 0, size)
	for j := range size {
		n := iter*benchStride + j
		o, err := observe("b_"+strconv.Itoa(n), actor{
			Kind:    kindAccount,
			Subject: "u_" + strconv.Itoa(n%64),
			Peer:    "p_" + strconv.Itoa(n%13),
			Device:  "d_" + strconv.Itoa(n%7),
		}, float64(100+(n*37)%9000), at.Add(time.Duration(n)*time.Millisecond))
		if err != nil {
			tb.Fatalf("observe: %v", err)
		}
		out = append(out, o)
	}
	return out
}

// warmForBench builds a plane and learns one tenant past the warm floor, so the
// measurement is taken on a model that actually consults its threshold and
// therefore actually runs the attribution.
func warmForBench(tb testing.TB) (*plane, tenant) {
	tb.Helper()
	probe.reset(true)
	p, err := newPlane(cloud.NewBase(cloud.Deps{Brand: brandA, DataDir: tb.TempDir()}, "risk"))
	if err != nil {
		tb.Fatalf("newPlane: %v", err)
	}
	tb.Cleanup(func() { _ = p.close(context.Background()) })
	for i := range maxFolds {
		select {
		case p.folds <- struct{}{}:
		default:
			tb.Fatalf("only %d of %d fold tickets were free", i, maxFolds)
		}
	}
	k, err := qualify(brandA, orgA)
	if err != nil {
		tb.Fatalf("qualify: %v", err)
	}
	at := time.Now().UTC().Add(-2 * time.Hour)
	// Past the engine's warm floor (8 windows of 256), so the threshold is in force
	// and the attribution actually runs. Negative iterations, so no warm-up id can
	// collide with a measured one.
	for i := range 18 {
		if _, err := p.learn(k, benchBatchOf(tb, 128, -i-1, at)...); err != nil {
			tb.Fatalf("warm: %v", err)
		}
	}
	st, _, err := p.state(k)
	if err != nil {
		tb.Fatalf("state: %v", err)
	}
	if !st.Warm {
		tb.Fatalf("the model is not warm after %d events, so this benchmark measures the warming branch", st.Learned)
	}
	return p, k
}

// BenchmarkLearn is one batch of the learning path, end to end and through the
// production entry point: the durable record, the tenant's own aggregates, and the
// model.
//
// It runs over a WARM model, because that is the only state where the cost is real
// — a warming model returns before the threshold is consulted, so it never reaches
// the attribution that dominates the alerting path.
//
// IT IS SIZED, and the sizes are the point. One batch is ONE record transaction
// whatever it holds, so at a small batch that transaction dominates and the model's
// share of the total is small; as the batch grows the transaction amortises and what
// is left is the per-event model cost. Reading the two together is what separates
// "the op got faster" from "the op does half the model work it used to", and only
// the second is a claim about this change.
func BenchmarkLearn(b *testing.B) {
	for _, size := range []int{8, 128} {
		b.Run("batch"+strconv.Itoa(size), func(b *testing.B) {
			p, k := warmForBench(b)
			at := time.Now().UTC().Add(-time.Hour)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Fresh ids per iteration: a duplicate is inert, which is a different path
				// and would measure the wrong thing.
				batch := benchBatchOf(b, size, i, at)
				if _, err := p.learn(k, batch...); err != nil {
					b.Fatalf("learn: %v", err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/event")
		})
	}
}

// BenchmarkPrior — what the AGGREGATE READING costs, against a record filled to
// its own retention bound.
//
// WHY IT IS MEASURED AND NOT ASSUMED. [plane.prior] is on the decide path, and
// the credit endpoint that calls it decides inside a 150ms budget. The pace half
// is three ring reads and is a fixed number of adds; the fan-out half is TWO SQL
// COUNTS, and the row a `device` predicate selects on is not in the record's
// covering index — so the read is a range scan of the tenant's own recent rows,
// and how many that is depends on how full the record is.
//
// So the case measured is the WORST one the bound admits: a record at
// [recordRows], which is [recordBudget] of observations, all inside the retention
// window, with the queried device present on none of them — the non-firing case,
// which is both the common one and the one that cannot stop early.
//
// A LIMIT does not help there: DISTINCT under a LIMIT stops as soon as the bound
// is reached, and a device nobody shares never reaches it. That is the honest
// shape of the cost and it is why the figure below is the one to watch.
//
//	28,556 rows (the ceiling)    6.3 ms/op    2,976 B    58 allocs
//
// Six milliseconds is 4% of the endpoint's budget for the WORST record the bound
// admits, and it is why there is no second index: [maxRowBytes] is measured
// against a real row ([TestBounds_ArePublishedInTheDimensionThatBinds]) and
// covers ONE index, so buying this read a covering index would spend the record's
// published per-tenant ceiling to save four per cent of a budget. If that trade
// ever changes, this benchmark is the number that says so.
//
// Re-measure with:
//
//	go test -tags "sqlite_purego sqlite_math_functions sqlite_fts5" \
//	  -run XXX -bench BenchmarkPrior -benchtime 50x -count 3 ./apps/risk/
func BenchmarkPrior(b *testing.B) {
	p, k := warmForBench(b)
	at := time.Now().UTC().Add(-time.Hour)
	// Fill the record to its own ceiling, every row carrying a device and a
	// counterparty so both queries have rows to walk past.
	for held := recorded(b, p, k); held < recordRows; held = recorded(b, p, k) {
		batch := make([]observation, 0, 512)
		for j := range 512 {
			n := held + j
			o, err := observe("fill_"+strconv.Itoa(n), actor{
				Kind: kindAccount, Subject: "u_" + strconv.Itoa(n%1024),
				Peer: "p_" + strconv.Itoa(n%512), Device: "d_" + strconv.Itoa(n%512),
			}, 1, at.Add(time.Duration(n)*time.Millisecond))
			if err != nil {
				b.Fatalf("observe: %v", err)
			}
			batch = append(batch, o)
		}
		if _, err := p.learn(k, batch...); err != nil {
			b.Fatalf("fill: %v", err)
		}
	}
	rows := recorded(b, p, k)
	// The event judged names a device and a counterparty NOBODY shares, so neither
	// count can stop early: the whole retained range is walked.
	one, err := observe("probe", actor{
		Kind: kindAccount, Subject: "u_probe", Peer: "p_probe", Device: "d_probe",
	}, 1, at)
	if err != nil {
		b.Fatalf("observe: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		seen, err := p.prior(k, one)
		if err != nil {
			b.Fatalf("prior: %v", err)
		}
		if len(seen.Pace) == 0 || len(seen.Shared) != 2 {
			b.Fatalf("the reading is %+v — the benchmark is measuring the wrong path", seen)
		}
	}
	b.ReportMetric(float64(rows), "rows")
}
