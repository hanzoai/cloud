// Package claim is the algebra of "somebody asserted something about a
// thing, at a moment, from evidence".
//
// It is a VALUE package: no I/O, no store, no clock, no vocabulary. Everything
// here is a pure function of its arguments, which is what lets the precedence
// rule and the leakage guard be tested exhaustively — and what lets two
// capabilities share them instead of each keeping a copy that drifts.
//
// It owns four things and deliberately nothing else:
//
//   - KNOWABLE, the derivation that makes the leakage guard a guard rather than
//     a courtesy (Knowable).
//   - DIGEST, the content address that makes redelivery idempotent (Digest).
//   - THE WINDOW an observation is made under (Window).
//   - THE TOTAL ORDER that resolves a conflict without discarding the losers
//     (Resolve).
//
// What it does NOT own is the vocabulary. A caller declares its own relations,
// its own values and its own ranking of sources, because those are claims about
// a domain and this package makes none. The ranking is the single parameter the
// order takes; every other term of it is general.
package claim
