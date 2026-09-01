package index

import (
	"context"
	"encoding/json"
	"errors"
)

// query.go is the lexical leg's in-process client: Query reads, Reconcile writes.
// /v1/search (clients/search) fuses Query with the vector leg, and both legs reach
// the SAME store the Meilisearch dialect serves — IN-PROCESS, no HTTP hop. That
// matters twice: the fused query is an agent tool call whose latency budget is a
// few hundred milliseconds, and a second network path to the same store would be a
// second way to do one thing.

// ErrNotMounted reports that the index subsystem is not mounted in this binary.
// The surface maps it to a DISABLED backend, never to a failed query — a
// deployment that does not run the index simply has no lexical leg.
var ErrNotMounted = errors.New("index: not mounted")

// Query runs the org-scoped lexical search over one index. org MUST come from a
// validated principal; the store pins every row to it, so a caller can never read
// another tenant's documents. An index that does not exist yields no rows rather
// than an error: "nothing indexed yet" is an empty result, not a failure.
//
// users bounds the rows by the `user` each was written with: nil reaches every
// row, and a list reaches only rows whose user is in it — "" being the rows
// written for everyone. A caller asking on a person's behalf passes {"", theirs}.
func Query(ctx context.Context, org, uid, q string, users []string, limit, offset int) ([]json.RawMessage, error) {
	if mounted == nil {
		return nil, ErrNotMounted
	}
	return mounted.State.store.Search(ctx, org, uid, q, users, limit, offset)
}

// Put writes one document into an index, creating the index on first use. It
// is Reconcile for a single document: the write a subsystem makes from its own
// save hook, so the lexical leg keeps step with the store without a full swap.
func Put(ctx context.Context, org, uid, primaryKey string, doc map[string]any) error {
	if mounted == nil {
		return ErrNotMounted
	}
	s := mounted.State.store
	if _, err := s.EnsureIndex(ctx, org, uid, primaryKey); err != nil {
		return err
	}
	return s.Upsert(ctx, org, uid, primaryKey, []map[string]any{doc})
}

// Remove deletes documents from an index by primary key. Keys that are not there
// are not an error: the caller's intent is "gone", and they are.
func Remove(ctx context.Context, org, uid string, pks ...string) error {
	if mounted == nil {
		return ErrNotMounted
	}
	return mounted.State.store.Delete(ctx, org, uid, pks)
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
