package index

import (
	"context"
	"encoding/json"
	"errors"
)

// query.go is the lexical leg's in-process seam: Query reads, Reconcile writes.
// Both reach the SAME store the Meilisearch dialect serves — IN-PROCESS, no HTTP
// hop. That matters twice: a fused query is an agent tool call whose latency
// budget is a few hundred milliseconds, and a second network path to the same
// store would be a second way to do one thing.

// ErrNotMounted reports that the index subsystem is not mounted in this binary.
// The surface maps it to a DISABLED backend, never to a failed query — a
// deployment that does not run the index simply has no lexical leg.
var ErrNotMounted = errors.New("index: not mounted")

// Query runs the org-scoped lexical search over one index. org MUST come from a
// validated principal; the store pins every row to it, so a caller can never read
// another tenant's documents. An index that does not exist yields no rows rather
// than an error: "nothing indexed yet" is an empty result, not a failure.
func Query(ctx context.Context, org, uid, q string, limit, offset int) ([]json.RawMessage, error) {
	if mounted == nil {
		return nil, ErrNotMounted
	}
	return mounted.State.store.Search(ctx, org, uid, q, nil, limit, offset)
}

// Reconcile REPLACES one index's whole corpus in a single idempotent call: every
// document is upserted and every key no longer present is deleted. It is Query's
// mirror — the write a subsystem that OWNS a corpus uses instead of POSTing its
// own documents back to itself through the Meilisearch dialect.
//
// A full swap rather than incremental writes because the corpus's truth lives
// UPSTREAM (a git forge, a sites table): re-running a sync must converge, and a
// repo deleted upstream must leave the index. Same prune-on-index contract the
// code index already keeps.
//
// org is the corpus's owner and is pinned into every row exactly as it is for a
// dialect write, so a corpus published under an org no principal can mint is
// readable by anyone allowed to query it and writable by nothing else.
func Reconcile(ctx context.Context, org, uid, primaryKey string, docs []map[string]any) (kept, removed int, err error) {
	if mounted == nil {
		return 0, 0, ErrNotMounted
	}
	s := mounted.State.store
	if _, err := s.EnsureIndex(ctx, org, uid, primaryKey); err != nil {
		return 0, 0, err
	}
	live := make(map[string]bool, len(docs))
	for _, d := range docs {
		if pk, _ := d[primaryKey].(string); pk != "" {
			live[pk] = true
		}
	}
	// Read the existing keys BEFORE the upsert: after it, a new document is
	// indistinguishable from one that was already there, and nothing is stale.
	before, err := s.PKs(ctx, org, uid)
	if err != nil {
		return 0, 0, err
	}
	if err := s.Upsert(ctx, org, uid, primaryKey, docs); err != nil {
		return 0, 0, err
	}
	stale := make([]string, 0, len(before))
	for _, pk := range before {
		if !live[pk] {
			stale = append(stale, pk)
		}
	}
	if len(stale) > 0 {
		if err := s.Delete(ctx, org, uid, stale); err != nil {
			return len(live), 0, err
		}
	}
	return len(live), len(stale), nil
}

// Ready reports whether the lexical leg can serve a query in this binary.
func Ready() bool { return mounted != nil }
