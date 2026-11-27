package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/sdk"
)

// build.go fills an org's passage store from the org's documents. It is how a
// store that has never been built becomes whole: an org whose knowledge predates
// its passage store — every org, on the deploy that replaced one vector per
// document with passages — starts with an empty file, and nothing else would
// ever embed a document nobody saves again.
//
// A store is BUILT once a build has read every document the org holds and synced
// each; the mark is the file's own PRAGMA user_version (store.go), so it travels
// with the file wherever the org's store is shipped. A document that failed to
// embed during a build falls under the ordinary failure contract (index.go): it
// has no passages until it is saved again or the org is reindexed. Only a build
// that could not list the documents leaves the store unbuilt, and the next use
// starts another.
//
// The first use of an unbuilt store in this process starts its build, once, in
// the background, bound to the mount and not to the request that happened to
// open the store. Writes do not wait for it: each takes its document's lock and
// reads the store, as the build does, so a save and a build converge on one
// document in either order. A search waits for it up to buildWait, then answers
// errUnbuilt, which both surfaces report as a degraded vector leg rather than as
// an empty one.

// buildWait is how long a search waits for an org's build before answering that
// its passages are not built. It covers the build of an org with a handful of
// documents, which is every new org, and keeps a caller with a latency budget
// from waiting on a large org's first build.
const buildWait = 2 * time.Second

// errUnbuilt refuses a vector read of a store whose build has not ended.
var errUnbuilt = errors.New("kb: the org's passages are still being built")

// building starts the org's build, or finds the one running, and answers the
// channel that closes when it ends.
func (x *indexer) building(org string, st *store) <-chan struct{} {
	x.mu.Lock()
	defer x.mu.Unlock()
	if ch, ok := x.builds[org]; ok {
		return ch
	}
	ch := make(chan struct{})
	if st.built.Load() { // a build ended between the caller's look and this lock
		close(ch)
		return ch
	}
	x.builds[org] = ch
	x.wg.Add(1)
	go func() {
		defer x.wg.Done()
		if out, err := x.rebuild(cloud.For(x.ctx, org), org); err != nil {
			x.log.Warn("kb build failed", "org", org, "err", err)
		} else {
			x.log.Info("kb built", "org", org, "vectors", out.Vectors, "failed", out.Failed, "lexical", out.Lexical)
		}
		x.mu.Lock()
		delete(x.builds, org)
		x.mu.Unlock()
		close(ch)
	}()
	return ch
}

// await holds a search until the org's build ends, up to buildWait, and answers
// errUnbuilt when the store is still not built: the build is running yet, or it
// could not list the org's documents.
func await(ctx context.Context, st *store, pending <-chan struct{}) error {
	if pending == nil {
		return nil
	}
	t := time.NewTimer(buildWait)
	defer t.Stop()
	select {
	case <-pending:
	case <-t.C:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", errUnbuilt, ctx.Err())
	}
	if !st.built.Load() {
		return errUnbuilt
	}
	return nil
}

// rebuild syncs every document the org holds and marks its store built. The
// lexical index is reconciled to the same documents first, which is what takes
// out a row no document backs; each sync then writes its document's row from a
// fresher read than the listing. A lexical failure does not hold up the
// passages: it is returned after them.
func (x *indexer) rebuild(ctx context.Context, org string) (*reindexOut, error) {
	type ref struct {
		dt   framework.ID
		name string
	}
	var (
		refs []ref
		rows []map[string]any
	)
	for _, dt := range indexedDocTypes {
		docs, err := framework.Search(ctx, org, dt, nil, reindexLimit)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", dt, err)
		}
		for _, d := range docs {
			refs = append(refs, ref{dt, d.Name})
			rows = append(rows, lexicalRow(dt, d.Name, d.Data))
		}
	}
	out := &reindexOut{}
	kept, removed, lexErr := lexical.Reconcile(ctx, org, lexicalIndex, lexicalKey, rows)
	switch {
	case lexErr == nil:
		out.Lexical, out.Removed = kept, removed
	case noLexical(lexErr):
		lexErr = nil
	default:
		lexErr = fmt.Errorf("lexical: %w", lexErr)
	}
	for _, r := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := x.sync(ctx, org, r.dt, r.name, false); err != nil {
			out.Failed++
			x.log.Warn("kb build: document not indexed", "org", org, "doctype", r.dt, "name", r.name, "err", err)
			continue
		}
		out.Vectors++
	}
	st, err := x.storeFor(org)
	if err != nil {
		return nil, err
	}
	if err := st.markBuilt(ctx); err != nil {
		return nil, err
	}
	return out, lexErr
}
