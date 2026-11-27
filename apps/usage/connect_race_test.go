package usage

import (
	"context"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// connecting stands in for a warehouse whose first dial is still in flight: not
// ready until connect is called, and Wait blocks until then or until its context
// ends. Once connected it answers every read with row.
func connecting(t *testing.T, row map[string]any) (connect func()) {
	t.Helper()
	var up atomic.Bool
	ready := make(chan struct{})
	origReady, origWait, origExec, origQuery, origEnsure := warehouseReady, warehouseWait, warehouseExec, warehouseQuery, warehouseEnsure
	warehouseReady = up.Load
	warehouseWait = func(ctx context.Context) error {
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	warehouseExec = func(context.Context, string, ...any) error { return nil }
	warehouseQuery = func(context.Context, string, ...any) ([]map[string]any, error) {
		return []map[string]any{row}, nil
	}
	warehouseEnsure = func(context.Context) error { return nil }
	t.Cleanup(func() {
		warehouseReady, warehouseWait, warehouseExec, warehouseQuery, warehouseEnsure = origReady, origWait, origExec, origQuery, origEnsure
	})
	return func() {
		up.Store(true)
		close(ready)
	}
}

// noCommerce runs the test in a fleet with no commerce peer, so spend answers at
// once and the only wait a summary can have is the warehouse's.
func noCommerce(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
}

// The first summary after a start reads the warehouse. The process starts on the
// request that needs it and the dial begins at mount, so that request used to find
// the dial in flight and report the warehouse not connected.
func TestTheFirstSummaryAfterAStartReadsTheWarehouse(t *testing.T) {
	noCommerce(t)
	connect := connecting(t, map[string]any{
		"requests": uint64(7), "tokens": uint64(1200), "decisions": uint64(3), "provider": "anthropic",
	})
	app := mountApp(t)

	time.AfterFunc(50*time.Millisecond, connect)
	code, body := call(t, app, "/v1/usage/summary", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("summary: want 200, got %d (%s)", code, body)
	}
	s := decodeSummary(t, body)
	if !s.Sources.Warehouse || !s.LLM.Available || s.LLM.Requests != 7 || s.LLM.Tokens != 1200 {
		t.Fatalf("llm while the warehouse connects: warehouse=%v %+v, want the warehouse's totals", s.Sources.Warehouse, s.LLM)
	}
	if !s.Kai.Available || s.Kai.Decisions != 3 {
		t.Fatalf("kai while the warehouse connects: %+v, want the warehouse's totals", s.Kai)
	}
	if !s.Accounts.Account.Available || !s.Accounts.Hanzo.Available {
		t.Fatalf("account board while the warehouse connects: account=%v hanzo=%v, want both read",
			s.Accounts.Account.Available, s.Accounts.Hanzo.Available)
	}
}

// A warehouse that never connects still says so, and the summary answers within
// one warehouseGrace however many of its blocks read the warehouse.
func TestAWarehouseThatNeverConnectsSaysSoWithinTheGrace(t *testing.T) {
	noCommerce(t)
	connecting(t, nil) // never connects
	orig := warehouseGrace
	warehouseGrace = 500 * time.Millisecond
	t.Cleanup(func() { warehouseGrace = orig })

	start := time.Now()
	app := mountApp(t)
	code, body := call(t, app, "/v1/usage/summary", "maxpower/dave", "maxpower")
	took := time.Since(start)
	if code != 200 {
		t.Fatalf("summary: want 200, got %d (%s)", code, body)
	}
	s := decodeSummary(t, body)
	if s.Sources.Warehouse || s.LLM.Available || s.Kai.Available || s.Accounts.Account.Available || s.Accounts.Hanzo.Available {
		t.Fatalf("a warehouse that never connected read as connected: warehouse=%v llm=%v kai=%v account=%v hanzo=%v",
			s.Sources.Warehouse, s.LLM.Available, s.Kai.Available, s.Accounts.Account.Available, s.Accounts.Hanzo.Available)
	}
	if took < warehouseGrace {
		t.Fatalf("answered after %v, before the %v grace", took, warehouseGrace)
	}
	if took >= 2*warehouseGrace {
		t.Fatalf("answered after %v: the warehouse gates waited more than one %v grace between them", took, warehouseGrace)
	}
}

// The warehouse vars hold the real client in production. Asserted by code pointer:
// a var rebound at its declaration would serve fakes behind a 200.
func TestWarehouseClientsDefaultToTheRealThing(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want any
	}{
		{"warehouseReady", warehouseReady, datastore.Ready},
		{"warehouseWait", warehouseWait, datastore.Wait},
		{"warehouseExec", warehouseExec, datastore.Exec},
		{"warehouseQuery", warehouseQuery, datastore.Query},
		{"warehouseEnsure", warehouseEnsure, datastore.EnsureCloudUsage},
	} {
		if reflect.ValueOf(c.got).Pointer() != reflect.ValueOf(c.want).Pointer() {
			t.Errorf("%s does not default to the real warehouse client", c.name)
		}
	}
}
