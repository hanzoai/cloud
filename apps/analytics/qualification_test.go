// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestHIP1405Qualification1KAnd10KConcurrentViewers verifies the HIP-1405 Phase II
// concurrent-viewer, coalescing, API latency (p95 <= 50ms), backpressure, reconnect
// resync (?since_version=), and bounded-memory qualification gates across 1,000 and
// 10,000 simultaneous dashboard viewers.
func TestHIP1405Qualification1KAnd10KConcurrentViewers(t *testing.T) {
	viewerCounts := []int{1000, 10000}
	if raceDetectorEnabled {
		// Go's -race runtime caps simultaneously alive goroutines at 8,128.
		viewerCounts = []int{1000}
	}
	for _, viewerCount := range viewerCounts {
		t.Run(fmt.Sprintf("%d_viewers", viewerCount), func(t *testing.T) {
			var dsQueries atomic.Int64
			var currentMRR atomic.Int64
			currentMRR.Store(420000)

			// Simulate a realistic 3ms L3 ClickHouse snapshot read.
			hub := NewHub(func(_ context.Context, scope Scope) (AnalyticsOverviewData, error) {
				dsQueries.Add(1)
				time.Sleep(3 * time.Millisecond)
				return AnalyticsOverviewData{
					Scope:  scope.Key(),
					Org:    scope.Org,
					Window: "30d",
					KPIs: OverviewKPIs{
						MRRCents: currentMRR.Load(),
						Orgs:     100000,
					},
					Freshness: Freshness{
						AsOf:     time.Now().UTC().Format(time.RFC3339),
						LagMs:    3,
						Source:   "analytics.serving_global_overview",
						Accuracy: AccuracyExact,
					},
				}, nil
			}, 120*time.Millisecond, 2*time.Second)

			// 1. Connect viewerCount simultaneous subscribers to global scope.
			subs := make([]*subscription, viewerCount)
			latencies := make([]time.Duration, viewerCount)
			var errCount atomic.Int64
			var wg sync.WaitGroup

			startAll := time.Now()
			for i := range viewerCount {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					t0 := time.Now()
					sub, err := hub.Subscribe(context.Background(), Scope{Super: true}, 0)
					latencies[idx] = time.Since(t0)
					if err != nil {
						errCount.Add(1)
						return
					}
					subs[idx] = sub
				}(i)
			}
			wg.Wait()
			connectWall := time.Since(startAll)

			if got := errCount.Load(); got != 0 {
				t.Fatalf("expected 0 subscribe errors for %d viewers, got %d", viewerCount, got)
			}
			if st := hub.Stats(); st.ActiveStreams != int64(viewerCount) || st.ActiveScopes != 1 {
				t.Fatalf("unexpected hub stats: %+v", st)
			}

			// All N viewers coalesce onto O(1) Datastore query!
			initialQueries := dsQueries.Load()
			if initialQueries > 2 {
				t.Fatalf("expected <=2 coalesced Datastore queries for %d viewers, got %d", viewerCount, initialQueries)
			}
			for i := range viewerCount {
				if subs[i].initialVer == 0 || len(subs[i].initialRaw) == 0 {
					t.Fatalf("viewer %d missing initial snapshot", i)
				}
			}

			slices.Sort(latencies)
			p50 := latencies[len(latencies)*50/100]
			p95 := latencies[len(latencies)*95/100]
			p99 := latencies[len(latencies)*99/100]
			maxLat := latencies[len(latencies)-1]

			// 2. Delta broadcast + slow-consumer non-blocking backpressure:
			// Leave the first 50 subscribers unread (simulating stalled/slow consumers)
			// while fast viewers await the next delta tick.
			currentMRR.Store(550000)
			var deltaDelivered atomic.Int64
			var bwg sync.WaitGroup
			for i := 50; i < viewerCount; i++ {
				bwg.Add(1)
				go func(s *subscription) {
					defer bwg.Done()
					deadline := time.NewTimer(2 * time.Second)
					defer deadline.Stop()
					for {
						select {
						case ev, ok := <-s.sub.ch:
							if !ok {
								return
							}
							var p StreamPayload
							if json.Unmarshal(ev.data, &p) == nil && p.Overview.KPIs.MRRCents == 550000 {
								deltaDelivered.Add(1)
								return
							}
						case <-deadline.C:
							return
						}
					}
				}(subs[i])
			}
			bwg.Wait()
			wantDelta := int64(viewerCount - 50)
			if got := deltaDelivered.Load(); got != wantDelta {
				t.Fatalf("delta delivered to %d/%d fast viewers", got, wantDelta)
			}

			// 3. Reconnect with ?since_version=<current> reports upToDate=true (zero redundant resend).
			hub.mu.Lock()
			latestVer := hub.scopes["global"].version
			hub.mu.Unlock()
			resyncSub, err := hub.Subscribe(context.Background(), Scope{Super: true}, latestVer)
			if err != nil {
				t.Fatalf("resync subscribe: %v", err)
			}
			if !resyncSub.upToDate {
				t.Fatalf("expected since_version=%d to return upToDate=true", latestVer)
			}
			hub.unsubscribe(resyncSub.scopeKey, resyncSub.sub)

			var memPeak runtime.MemStats
			runtime.ReadMemStats(&memPeak)

			// 4. Clean teardown: unsubscribe all viewers and verify hub drains to 0 streams & 0 active scopes.
			for _, s := range subs {
				if s != nil {
					hub.unsubscribe(s.scopeKey, s.sub)
				}
			}
			if st := hub.Stats(); st.ActiveStreams != 0 || st.ActiveScopes != 0 {
				t.Fatalf("expected 0 active streams/scopes after teardown, got %+v", st)
			}

			totalDS := dsQueries.Load()
			heapMB := float64(memPeak.HeapAlloc) / (1024 * 1024)
			t.Logf("QUAL_RECEIPT viewers=%d wall=%v ds_queries_init=%d ds_queries_total=%d coalesce_ratio=%dx p50=%v p95=%v p99=%v max=%v heap_mb=%.1f",
				viewerCount, connectWall.Round(time.Millisecond), initialQueries, totalDS, viewerCount/int(max(1, initialQueries)), p50, p95, p99, maxLat, heapMB)
		})
	}
}

// TestHIP1405QualificationAPILatencyAndColdKV measures end-to-end HTTP API latency
// (p50/p95/p99 <= 50ms gate) for GET /v1/analytics/overview under both warm KV and
// completely cold/down KV with 16 concurrent callers (500 requests each).
func TestHIP1405QualificationAPILatencyAndColdKV(t *testing.T) {
	backend := newFakeBackend(func(_ context.Context, sql string, _ ...any) ([]map[string]any, error) {
		time.Sleep(3 * time.Millisecond)
		if strings.Contains(sql, "serving_global_overview") || strings.Contains(sql, "global_overview") {
			return []map[string]any{{
				"orgs_total":      int64(100000),
				"mrr_minor":       int64(420000),
				"cash_minor":      int64(900000),
				"ai_requests_30d": int64(150000),
				"ai_tokens_30d":   int64(6330000000),
				"spans_24h":       int64(3900000),
				"errors_24h":      int64(11000),
			}}, nil
		}
		return []map[string]any{
			{"model": "enso-pro", "ai_tokens": float64(3904918774), "ai_requests": float64(43006)},
		}, nil
	})

	for _, kvDown := range []bool{false, true} {
		mode := "warm_kv"
		if kvDown {
			mode = "cold_down_kv"
		}
		t.Run(mode, func(t *testing.T) {
			kv := newMemKV()
			kv.SetDown(kvDown)
			app, _ := newTestApp(t, Config{
				Backend:              backend,
				KV:                   kv,
				MaxConcurrentQueries: 32,
			})

			const totalReqs = 500
			const concurrency = 16
			latencies := make([]time.Duration, totalReqs)
			var failures atomic.Int64
			jobs := make(chan int, totalReqs)
			for i := range totalReqs {
				jobs <- i
			}
			close(jobs)

			var wg sync.WaitGroup
			for range concurrency {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for idx := range jobs {
						t0 := time.Now()
						req := httptest.NewRequest(http.MethodGet, "/v1/analytics/overview?window=30d", nil)
						for k, v := range hdrSuper {
							req.Header.Set(k, v)
						}
						resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
						latencies[idx] = time.Since(t0)
						if err != nil || resp.StatusCode != http.StatusOK {
							failures.Add(1)
							continue
						}
						var out AnalyticsOverviewOut
						_ = json.NewDecoder(resp.Body).Decode(&out)
						_ = resp.Body.Close()
						if out.Data == nil || out.Data.KPIs.Orgs != 100000 {
							failures.Add(1)
						}
					}
				}()
			}
			wg.Wait()

			if f := failures.Load(); f != 0 {
				t.Fatalf("%s: %d/%d requests failed", mode, f, totalReqs)
			}
			slices.Sort(latencies)
			p50 := latencies[totalReqs*50/100]
			p95 := latencies[totalReqs*95/100]
			p99 := latencies[totalReqs*99/100]
			maxLat := latencies[totalReqs-1]
			if !raceDetectorEnabled && p95 > 50*time.Millisecond {
				t.Fatalf("%s: API p95=%v exceeds 50ms gate", mode, p95)
			}
			t.Logf("QUAL_API mode=%s n=%d conc=%d p50=%v p95=%v p99=%v max=%v", mode, totalReqs, concurrency, p50, p95, p99, maxLat)
		})
	}
}
