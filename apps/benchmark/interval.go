package benchmark

import "math"

// How many runs, and which one to report.
//
// The tempting answers are best-of-N and the average of N, and both are wrong in
// ways that matter more here than almost anywhere, because this arena's entire
// claim is that its numbers are honest.
//
// BEST OF N IS BIASED AND GAMEABLE. The maximum of N samples is not an estimate
// of a model's accuracy; it is an estimate of its accuracy plus a bonus that
// GROWS WITH N. Report best-of-3 and you have published a number the model
// reaches about a third of the time. Worse, anyone can improve it by running
// more — which makes scores incomparable between models that were run different
// numbers of times, and is one of the mechanisms that produces exactly the
// provider claims running 3-13pp hot that this surface exists to catch. We do
// not get to use it and then complain about it.
//
// AVERAGING ACROSS RUNS conflates measurements of different things. A model
// scoring 65 in March and 90 in August has not scored 77.5; it has improved, and
// 77.5 describes a model that never existed. Runs are separated for that reason
// and the leaderboard reads the latest.
//
// SO: repeat WITHIN a run, report the mean over its items, and carry the
// interval. Repetition inside one run is measuring one quantity more precisely,
// which is the only case where pooling is sound. Across runs, the latest is the
// answer and history is the trend.
//
// The interval is what makes the number usable by either audience. A scientist
// needs to know whether a difference is real; a consumer needs to know whether
// the model at the top is meaningfully better than the one below it. At n=198 —
// the GPQA-Diamond set — a score of 98% carries a 95% interval of roughly ±2
// points, which means most differences at the top of a leaderboard are not
// distinguishable and a board that prints bare numbers implies a precision it
// does not have.
//
// For two models head to head the interval is not the right test at all, because
// the runs share items: /compare already does exact McNemar on the paired common
// set, which is stronger than comparing two intervals and is the correct answer
// for "is A better than B".

// wilson returns the 95% Wilson score interval for k successes in n trials, as
// percentages. Wilson rather than the textbook normal approximation because the
// normal one is wrong exactly where benchmark scores live: near 0 and near 100
// it produces bounds beyond the possible range, and at 194/198 that is not a
// corner case, it is the top of the board.
//
// Returns (0,0) for n == 0 — no trials is no interval, not a wide one.
func wilson(k, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 0
	}
	const z = 1.959963985 // 95%
	p := float64(k) / float64(n)
	fn := float64(n)
	den := 1 + z*z/fn
	centre := (p + z*z/(2*fn)) / den
	half := z * math.Sqrt(p*(1-p)/fn+z*z/(4*fn*fn)) / den
	lo, hi = (centre-half)*100, (centre+half)*100
	if lo < 0 {
		lo = 0
	}
	if hi > 100 {
		hi = 100
	}
	return lo, hi
}
