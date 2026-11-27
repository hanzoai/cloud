package tools

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"
)

// catalog_keep.go — the shelf keeps itself current.
//
// The catalog is filled by Sync. A SuperAdmin can ask for one at any time
// (POST /v1/tool/catalog/sync), and no deployment depends on anyone asking: the
// first read of a copy with no finished pass in catalogAge starts one in the
// background, and reads go on answering from the rows already held while it
// walks.
//
// It is started by a READ, not by a timer. The tool app is a lazy process, stopped
// after fifteen idle minutes (manifest idleAfter), so a timer would run only while
// someone happened to be asking anyway — and the read is the moment the shelf's
// age matters. A pass cut short (the upstream errored, the process was stopped)
// is not finished, so the next read after catalogRetry walks again; the rows it
// wrote stay, because Sync is idempotent and never deletes.

const (
	// catalogAge is how old a finished pass may be before a read starts another.
	catalogAge = 24 * time.Hour
	// catalogRetry spaces the starts, so an upstream that is down is asked once
	// per interval rather than once per read.
	catalogRetry = 10 * time.Minute
	// catalogPass bounds one walk: a few hundred pages of a hundred listings,
	// each one request, with room for a slow upstream.
	catalogPass = 30 * time.Minute
)

// keeper refreshes one catalog store in the background, one pass at a time.
type keeper struct {
	store   *CatalogStore
	log     luxlog.Logger
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool
	fresh   atomic.Int64 // Unix seconds of the last finished pass this process knows of
	started atomic.Int64 // Unix seconds of the last start
}

// newKeeper reads when the store last finished a pass, so a fresh copy is not
// walked again just because the process restarted.
func newKeeper(store *CatalogStore, log luxlog.Logger) *keeper {
	ctx, cancel := context.WithCancel(context.Background())
	k := &keeper{store: store, log: log, ctx: ctx, cancel: cancel}
	if at, err := store.Finished(ctx); err == nil {
		k.fresh.Store(at.Unix())
	}
	return k
}

// kick starts a pass when the copy is stale, none is running, and none started
// within catalogRetry. It never waits: the caller's answer is the rows held now.
func (k *keeper) kick() {
	now := time.Now()
	if now.Sub(time.Unix(k.fresh.Load(), 0)) < catalogAge {
		return
	}
	if now.Sub(time.Unix(k.started.Load(), 0)) < catalogRetry {
		return
	}
	if k.ctx.Err() != nil || !k.running.CompareAndSwap(false, true) {
		return
	}
	k.started.Store(now.Unix())
	k.wg.Add(1)
	go func() {
		defer k.wg.Done()
		defer k.running.Store(false)
		k.pass()
	}()
}

// pass walks the registry once, unless another writer finished one meanwhile —
// a SuperAdmin's sync records the same marker.
func (k *keeper) pass() {
	at, err := k.store.Finished(k.ctx)
	if err != nil {
		if k.ctx.Err() == nil {
			k.log.Warn("catalog: read the last pass", "err", err)
		}
		return
	}
	if time.Since(at) < catalogAge {
		k.fresh.Store(at.Unix())
		return
	}
	ctx, cancel := context.WithTimeout(k.ctx, catalogPass)
	defer cancel()
	began := time.Now()
	added, updated, err := k.store.Sync(ctx)
	if err != nil {
		if k.ctx.Err() == nil {
			k.log.Warn("catalog: sync failed", "added", added, "updated", updated, "registry", registryURL(), "err", err)
		}
		return
	}
	k.fresh.Store(time.Now().Unix())
	total, _ := k.store.Count(k.ctx)
	k.log.Info("catalog: synced", "added", added, "updated", updated, "total", total,
		"registry", registryURL(), "took", time.Since(began).Round(time.Second).String())
}

// stop cancels a pass in flight and waits for it to return. Idempotent.
func (k *keeper) stop() {
	k.cancel()
	k.wg.Wait()
}
