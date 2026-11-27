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
