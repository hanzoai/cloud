// Copyright © 2026 Hanzo AI. MIT License.

package transport

import (
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/zap-proto/zip"
)

// TestRoundTripReentrancyBounded reproduces the production crash shape — a handler
// that reads the co-resident commerce app WHILE serving a commerce read, so the
// dispatch re-runs the whole app and re-enters itself — and proves the depth guard
// turns the unbounded recursion (stack overflow + setRequestCancel pileup) into a
// bounded, fail-safe refusal. Without the guard this test recurses until the
// goroutine stack overflows and the process dies; with it the outer request returns
// and the nesting never exceeds maxDepth.
func TestRoundTripReentrancyBounded(t *testing.T) {
	var live, peak int64
	client := Client(0)

	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.All("/loop", func(c *zip.Ctx) error {
		d := atomic.AddInt64(&live, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if d <= p || atomic.CompareAndSwapInt64(&peak, p, d) {
				break
			}
		}
		defer atomic.AddInt64(&live, -1)
		// Self-read: dispatch the SAME co-resident app again (the scope-rate-limiter
		// reading its rules from commerce / the per-tier gate reading the tier — a
		// commerce read issued from inside a commerce read).
		req, _ := http.NewRequest(http.MethodGet, PlaceholderBase+"/loop", nil)
		if resp, err := client.Do(req); err == nil { // an err here is the guard's refusal — fail safe
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return c.Bytes(http.StatusOK, []byte("ok"))
	})
	SetApp(app)
	defer SetHandler(nil)

	// Enter THROUGH the transport so the outer request is dispatch depth 1.
	req, _ := http.NewRequest(http.MethodGet, PlaceholderBase+"/loop", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("outer request errored (guard must not refuse the first dispatch): %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	t.Logf("peak nesting reached = %d (cap maxDepth=%d)", peak, maxDepth)
	if peak < 2 {
		t.Fatalf("handler never re-entered (peak nesting %d) — the test did not exercise the recursion", peak)
	}
	if peak > maxDepth {
		t.Fatalf("nesting reached %d, exceeds the cap %d — the guard did not bound the self-dispatch", peak, maxDepth)
	}
}

// TestRoundTripSingleDispatchOK proves the guard is invisible to the normal path: a
// single (non-nested) co-resident dispatch succeeds and leaves no depth residue, so
// a later dispatch on the same goroutine starts fresh.
func TestRoundTripSingleDispatchOK(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.All("/ok", func(c *zip.Ctx) error { return c.Bytes(http.StatusOK, []byte("ok")) })
	SetApp(app)
	defer SetHandler(nil)

	client := Client(0)
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, PlaceholderBase+"/ok", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("dispatch %d errored: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("dispatch %d: status=%d body=%q, want 200 \"ok\"", i, resp.StatusCode, body)
		}
	}
	if _, ok := depthByGoroutine.Load(goroutineID()); ok {
		t.Fatalf("depth counter leaked after balanced dispatches")
	}
}
