// Copyright © 2026 Hanzo AI. MIT License.

// Per-connection memory profile for hanzoai/commerce — canonical
// regression test per SCALE_STANDARD.md §3 / §8.
//
// We measure zip-on-fasthttp's per-conn overhead with a commerce-shaped
// health route registered, NOT the full Gin handler chain. The
// fasthttp accept-loop and zip Ctx pool are the same regardless of
// which subsystem mounts on top, so this test catches the same
// regressions across services. Once commerce migrates its HTTP layer
// from gin → zip per SCALE_STANDARD.md §1 / §9 step 1, the same test
// also covers the real request path.
//
// Run with:
//   go test -mod=mod -run=TestConnMemory -v -conn-count=10000

package commerce_test

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"
)

var connCount = flag.Int("conn-count", 1000, "concurrent connections to hold")

const (
	maxPerConnHeapBytes   = 12 * 1024
	goroutinesPerConnLow  = 0.95
	goroutinesPerConnHigh = 1.05
)

func TestConnMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory profile in -short mode")
	}
	n := *connCount

	app := zip.New(zip.Config{
		Logger:                luxlog.New("test", "commerce-conn-memory"),
		DisableStartupMessage: true,
		AppName:               "commerce",
	})

	app.Get("/healthz", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{
			"status":  "ok",
			"service": "commerce",
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var holding atomic.Int64
	app.Get("/hold", func(c *zip.Ctx) error {
		holding.Add(1)
		defer holding.Add(-1)
		<-ctx.Done()
		return nil
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	go func() { _ = app.Fiber().Listener(ln) }()
	defer func() { _ = app.Shutdown() }()

	time.Sleep(200 * time.Millisecond)
	var baseline, peak runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&baseline)
	// Snapshot the goroutine count BEFORE opening connections so the per-conn
	// metric below is a DELTA (goroutines the held connections ADD), exactly like
	// the heap metric (peak-baseline). An absolute NumGoroutine()/n is polluted
	// by goroutines unrelated to connections — GC workers, the test framework,
	// and background goroutines other tests in this package leaked before this
	// one runs — which made the metric flake near the 1.0 boundary. The delta
	// isolates the accept-loop's true per-conn cost.
	baselineGoroutines := runtime.NumGoroutine()

	req := []byte("GET /hold HTTP/1.1\r\nHost: x\r\n\r\n")
	conns := make([]net.Conn, 0, n)
	var connsMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				return
			}
			if _, err := c.Write(req); err != nil {
				_ = c.Close()
				return
			}
			connsMu.Lock()
			conns = append(conns, c)
			connsMu.Unlock()
		}()
	}
	wg.Wait()
	defer func() {
		connsMu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		connsMu.Unlock()
	}()

	deadline := time.Now().Add(30 * time.Second)
	for holding.Load() < int64(n) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if got := holding.Load(); got < int64(n) {
		t.Logf("only %d/%d conns accepted before deadline", got, n)
		n = int(got)
	}
	if n == 0 {
		t.Fatal("no conns accepted")
	}

	runtime.GC()
	runtime.ReadMemStats(&peak)
	peakGoroutines := runtime.NumGoroutine()

	delta := int64(peak.HeapAlloc) - int64(baseline.HeapAlloc)
	perConn := float64(delta) / float64(n)
	totalGoroutines := peakGoroutines
	// DELTA per conn — goroutines the held connections added over the pre-conn
	// baseline (one per held /hold handler), immune to unrelated background
	// goroutines. Matches the heap delta methodology above.
	goroutinesPerConn := float64(peakGoroutines-baselineGoroutines) / float64(n)

	fmt.Printf("\n=== Per-connection memory profile (hanzoai/commerce) ===\n")
	fmt.Printf("conns held       : %d\n", n)
	fmt.Printf("baseline heap    : %s\n", humanBytes(int64(baseline.HeapAlloc)))
	fmt.Printf("peak heap        : %s\n", humanBytes(int64(peak.HeapAlloc)))
	fmt.Printf("delta            : %s\n", humanBytes(delta))
	fmt.Printf("per-conn heap    : %.0f B (%.2f KiB)\n", perConn, perConn/1024)
	fmt.Printf("goroutines total : %d (baseline %d, +%d for %d conns)\n", totalGoroutines, baselineGoroutines, peakGoroutines-baselineGoroutines, n)
	fmt.Printf("goroutines / conn: %.2f (delta)\n", goroutinesPerConn)
	fmt.Printf("=======================================================\n\n")

	if perConn > maxPerConnHeapBytes {
		t.Errorf("per-conn heap %.0f B exceeds budget %d B (SCALE_STANDARD.md §3)",
			perConn, maxPerConnHeapBytes)
	}
	if goroutinesPerConn < goroutinesPerConnLow || goroutinesPerConn > goroutinesPerConnHigh {
		t.Errorf("goroutines/conn %.2f outside [%.2f, %.2f] (SCALE_STANDARD.md §3)",
			goroutinesPerConn, goroutinesPerConnLow, goroutinesPerConnHigh)
	}

	cancel()
	wg.Wait()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < 0 {
		return "-" + humanBytes(-n)
	}
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
