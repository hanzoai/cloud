package tools

import (
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/internal/sqltest"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// catalog_keep_test.go pins how the shelf fills itself: a read of a copy no pass
// has finished starts one, a fresh copy is never walked again, and a walk cut
// short is not mistaken for a finished one.
//
// The names stay short on purpose: the first test to mount fixes the runtime
// socket directory under its own TempDir, and darwin caps a unix socket path at
// 104 bytes.

// twoPages is a two-page registry that counts its requests and can be told to
// fail its second page.
func twoPages(t *testing.T, failSecond bool) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body := map[string]any{
			"servers":  []map[string]any{entry("com.stripe/mcp", "payments", remote("https://mcp.stripe.com"), nil)},
			"metadata": map[string]any{"nextCursor": "p1"},
		}
		if r.URL.Query().Get("cursor") == "p1" {
			if failSecond {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			body = map[string]any{
				"servers":  []map[string]any{entry("com.linear/mcp", "issues", remote("https://mcp.linear.app"), nil)},
				"metadata": map[string]any{},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

// settle waits for the keeper's pass to return.
func settle(t *testing.T) {
	t.Helper()
	k := mounted.State.keeper
	for deadline := time.Now().Add(5 * time.Second); k.running.Load(); {
		if time.Now().After(deadline) {
			t.Fatal("the pass did not return")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestShelfFills: nobody synced, a tenant opens the registry, and the
// copy fills without a SuperAdmin — the read answers at once, the pass behind it.
func TestShelfFills(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ts, hits := twoPages(t, false)
		app := newApp(t, nil)
		t.Setenv("CLOUD_TOOLS_REGISTRY", ts.URL)

		_ = catalogIDs(t, app, "acme", false, "")
		settle(t)
		if got := catalogIDs(t, app, "acme", false, ""); !got["com.stripe_mcp"] || !got["com.linear_mcp"] {
			t.Fatalf("after the pass the shelf holds %v, want both listings", got)
		}
		at, err := mounted.State.catalog.Finished(context.Background())
		if err != nil || at.IsZero() {
			t.Fatalf("a pass that reached the last page must be recorded finished, got %v, %v", at, err)
		}
		before := hits.Load()
		_ = catalogIDs(t, app, "acme", false, "")
		settle(t)
		if hits.Load() != before {
			t.Fatalf("a fresh shelf was walked again: %d more requests", hits.Load()-before)
		}
	})
}

// TestShelfFresh: a SuperAdmin's sync records the same finish, so
// the next read does not repeat it — in this process or the next one.
func TestShelfFresh(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ts, hits := twoPages(t, false)
		app := newApp(t, nil)
		t.Setenv("CLOUD_TOOLS_REGISTRY", ts.URL)
		if r := send(t, app, http.MethodPost, "/v1/tool/catalog/sync", "admin", nil, true); r.Code != 200 {
			t.Fatalf("sync: %d (%s)", r.Code, r.Body)
		}
		synced := hits.Load()
		_ = catalogIDs(t, app, "acme", false, "")
		settle(t)
		if hits.Load() != synced {
			t.Fatalf("a read after a finished sync walked the registry again: %d more requests", hits.Load()-synced)
		}
		if k := newKeeper(mounted.State.catalog, mounted.Log); time.Since(time.Unix(k.fresh.Load(), 0)) > time.Minute {
			t.Fatal("a restarted process must read the finished pass from the store, not start stale")
		}
	})
}

// TestShelfCutShort: an upstream that fails mid-walk leaves the rows
// it gave, and the copy stale, so a later read walks again — but not the very
// next one, which would ask a registry that is down once per read.
func TestShelfCutShort(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ts, hits := twoPages(t, true)
		app := newApp(t, nil)
		t.Setenv("CLOUD_TOOLS_REGISTRY", ts.URL)

		_ = catalogIDs(t, app, "acme", false, "")
		settle(t)
		failed := hits.Load()
		if got := catalogIDs(t, app, "acme", false, ""); !got["com.stripe_mcp"] {
			t.Fatalf("the page read before the failure must be kept, got %v", got)
		}
		settle(t)
		if hits.Load() != failed {
			t.Fatal("a read right after a failed pass asked the registry again")
		}
		at, err := mounted.State.catalog.Finished(context.Background())
		if err != nil || !at.IsZero() {
			t.Fatalf("a walk that did not reach the last page must not be recorded finished, got %v, %v", at, err)
		}
		mounted.State.keeper.started.Store(time.Now().Add(-catalogRetry - time.Second).Unix())
		_ = catalogIDs(t, app, "acme", false, "")
		settle(t)
		if hits.Load() == failed {
			t.Fatalf("a stale shelf past the retry interval must be walked again (%d requests)", hits.Load())
		}
	})
}
