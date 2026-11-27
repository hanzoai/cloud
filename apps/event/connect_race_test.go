package event

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// connecting stands in for a warehouse whose dial is still in flight: not ready
// until connect is called, and Wait blocks until then or until its context ends.
func connecting(t *testing.T) (connect func()) {
	t.Helper()
	var up atomic.Bool
	ready := make(chan struct{})
	origReady, origWait := warehouseReady, warehouseWait
	warehouseReady = up.Load
	warehouseWait = func(ctx context.Context) error {
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { warehouseReady, warehouseWait = origReady, origWait })
	return func() {
		up.Store(true)
		close(ready)
	}
}

// The first event after a start lands. The warehouse dial begins at mount and
// takes milliseconds, and the gate used to refuse whatever arrived inside them:
// every cloud start answered its first beacon with a 503.
func TestTheFirstEventAfterAStartLands(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{siteKey: {Org: "acme", Project: "shop"}})
	w := fakeWarehouse(t)
	connect := connecting(t)
	app := mountApp(t)

	time.AfterFunc(50*time.Millisecond, connect)
	code := postKeyed(t, app, "/v1/event", "", `{"type":"pageview","event":"$pageview","properties":{"consent":"analytics"}}`,
		map[string]string{"Authorization": "Bearer " + siteKey})
	if code != http.StatusOK {
		t.Fatalf("first beacon while the warehouse connects = %d, want 200", code)
	}
	if len(w.facts) != 1 {
		t.Fatalf("landed %d facts, want 1", len(w.facts))
	}
}

// A warehouse that never connects still answers 503, bounded by warehouseGrace.
func TestAWarehouseThatNeverConnectsStillAnswers503(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{siteKey: {Org: "acme", Project: "shop"}})
	w := fakeWarehouse(t)
	connecting(t) // never connects
	orig := warehouseGrace
	warehouseGrace = 100 * time.Millisecond
	t.Cleanup(func() { warehouseGrace = orig })
	app := mountApp(t)

	start := time.Now()
	code := postKeyed(t, app, "/v1/event", "", `{"type":"pageview","event":"$pageview","properties":{"consent":"analytics"}}`,
		map[string]string{"Authorization": "Bearer " + siteKey})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("beacon with no warehouse = %d, want 503", code)
	}
	if took := time.Since(start); took < warehouseGrace {
		t.Fatalf("answered after %v, before the %v grace", took, warehouseGrace)
	}
	if len(w.facts) != 0 {
		t.Fatalf("landed %d facts with no warehouse", len(w.facts))
	}
}
