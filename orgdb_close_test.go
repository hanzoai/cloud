package cloud

// orgdb_close_test.go — CloseAll is TERMINAL.
//
// The defect these hold shut is that CloseAll used to be a RESET rather than a
// close: it closed every handle and then installed fresh empty maps, so the very
// next For() opened the file again. Every caller of CloseAll is a Shutdown path,
// and a request still in flight during a rollout reaches For() after it — so the
// store came back to life in a process that is on its way out, re-hydrating and
// re-claiming the fence lease the NEW pod is claiming at that moment. Two live
// writers for one org, from a handle nobody thought was reachable.

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/hanzoai/namespace"
)

// TestOrgStoreCloseAllIsTerminal proves a closed store STAYS closed: the one
// spelling of "this store is done" is refused, not silently re-opened.
func TestOrgStoreCloseAllIsTerminal(t *testing.T) {
	dir := t.TempDir()
	opens := 0
	cache := NewOrgStore(Base{DataDir: dir}, "widget", func(db *sql.DB) (*sql.DB, error) {
		opens++
		return db, nil
	})
	ns := MustOrgNamespace("orga", "")
	if _, err := cache.For(ns); err != nil {
		t.Fatalf("For before close: %v", err)
	}
	if opens != 1 {
		t.Fatalf("want 1 open, got %d", opens)
	}
	if err := cache.CloseAll(); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}

	// THE DEFECT. A request that was in flight when Shutdown ran reaches For()
	// here. It must be REFUSED. Re-opening resurrects the file — and on a durable
	// deployment re-hydrates it and re-claims the lease the successor pod holds.
	if _, err := cache.For(ns); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("For after CloseAll must refuse with ErrStoreClosed, got err=%v", err)
	}
	if opens != 1 {
		t.Fatalf("For after CloseAll re-opened the org file: opens=%d, want 1 — the store came back to life after shutdown", opens)
	}

	// The SAME refusal on every door that can open a file, so there is no second
	// spelling of the reset that survives.
	if _, err := cache.For(MustOrgNamespace("neverseen", "")); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("For(new org) after CloseAll must refuse with ErrStoreClosed, got %v", err)
	}
	if err := cache.Each(func(namespace.Namespace, *sql.DB, error) {
		t.Fatal("Each after CloseAll must enumerate nothing")
	}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("Each after CloseAll must refuse with ErrStoreClosed, got %v", err)
	}
	if opens != 1 {
		t.Fatalf("a door other than For re-opened an org file after close: opens=%d want 1", opens)
	}

	// CloseAll stays idempotent — a Shutdown that runs twice is not an error.
	if err := cache.CloseAll(); err != nil {
		t.Fatalf("CloseAll must be idempotent, second call: %v", err)
	}
}
