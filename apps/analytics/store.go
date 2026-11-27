// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/datastore"
)

const (
	// MaxConcurrentQueries bounds in-flight analytical Datastore queries so bursts
	// shed cleanly with 429/503 rather than cascading into ClickHouse saturation.
	MaxConcurrentQueries = 32

	// QueryTimeout is the per-query wall-clock budget enforced on every Datastore read.
	QueryTimeout = 5 * time.Second
)

var (
	// ErrOverloaded signals that all MaxConcurrentQueries slots are occupied.
	ErrOverloaded = errors.New("analytics: query concurrency limit reached")

	// ErrDatastoreUnavailable signals that Hanzo Datastore is disconnected or unreachable.
	ErrDatastoreUnavailable = errors.New("analytics: datastore unavailable")
)

// Backend is the read-only storage contract for /v1/analytics/*.
// Keeping this behind an interface isolates apps/analytics from datastore/ internals
// and enables deterministic unit tests without a live ClickHouse cluster.
type Backend interface {
	// Ready reports whether the backing analytical store is currently reachable.
	Ready() bool
	// Query executes a read-only SQL statement with positional ? arguments.
	Query(ctx context.Context, sql string, args ...any) ([]map[string]any, error)
}

// SemaphoreStatus reports real-time utilization of the bounded query semaphore.
type SemaphoreStatus struct {
	// InFlight is the number of Datastore queries currently executing.
	InFlight int64 `json:"inFlight"`
	// Capacity is the maximum allowed concurrent Datastore queries.
	Capacity int `json:"capacity"`
	// UtilizationPct is InFlight / Capacity expressed as a percentage (0–100).
	UtilizationPct float64 `json:"utilizationPct"`
	// Rejected is the cumulative count of queries rejected due to semaphore exhaustion.
	Rejected int64 `json:"rejected"`
}

// DatastoreBackend is the default Backend backed by the shared datastore.Query client.
type DatastoreBackend struct{}

// Ready delegates to datastore.Ready.
func (DatastoreBackend) Ready() bool { return datastore.Ready() }

// Query executes a read-only statement via datastore.Query with positional arguments.
func (DatastoreBackend) Query(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	if !datastore.Ready() {
		return nil, ErrDatastoreUnavailable
	}
	return datastore.Query(ctx, sql, args...)
}

// GuardedBackend wraps any Backend with a bounded concurrency semaphore and per-query timeout.
type GuardedBackend struct {
	inner    Backend
	sem      chan struct{}
	timeout  time.Duration
	inFlight atomic.Int64
	rejected atomic.Int64
}

// NewGuardedBackend wraps inner with maxConcurrent slots (default MaxConcurrentQueries)
// and per-query timeout (default QueryTimeout).
func NewGuardedBackend(inner Backend, maxConcurrent int, timeout time.Duration) *GuardedBackend {
	if inner == nil {
		inner = DatastoreBackend{}
	}
	if maxConcurrent <= 0 {
		maxConcurrent = MaxConcurrentQueries
	}
	if timeout <= 0 {
		timeout = QueryTimeout
	}
	return &GuardedBackend{
		inner:   inner,
		sem:     make(chan struct{}, maxConcurrent),
		timeout: timeout,
	}
}

// Ready reports whether the underlying Backend is reachable.
func (g *GuardedBackend) Ready() bool {
	return g.inner.Ready()
}

// Query acquires a semaphore slot without blocking indefinitely (shedding immediately
// with ErrOverloaded when saturated), applies the per-query timeout, and runs the read.
func (g *GuardedBackend) Query(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
	select {
	case g.sem <- struct{}{}:
		g.inFlight.Add(1)
		defer func() {
			g.inFlight.Add(-1)
			<-g.sem
		}()
	default:
		g.rejected.Add(1)
		return nil, ErrOverloaded
	}

	if !g.inner.Ready() {
		return nil, ErrDatastoreUnavailable
	}

	qctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	return g.inner.Query(qctx, sql, args...)
}

// SemaphoreStats returns live query concurrency saturation metrics for /v1/analytics/health.
func (g *GuardedBackend) SemaphoreStats() SemaphoreStatus {
	inFlight := g.inFlight.Load()
	capVal := cap(g.sem)
	pct := 0.0
	if capVal > 0 {
		pct = math.Round((float64(inFlight)/float64(capVal))*10000) / 100
	}
	return SemaphoreStatus{
		InFlight:       inFlight,
		Capacity:       capVal,
		UtilizationPct: pct,
		Rejected:       g.rejected.Load(),
	}
}

// ── Native Datastore row coercion helpers ────────────────────────────────────

func rowStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	default:
		return ""
	}
}

func rowInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int32:
		return int64(x)
	case int16:
		return int64(x)
	case int8:
		return int64(x)
	case int:
		return int64(x)
	case uint64:
		if x > math.MaxInt64 {
			return math.MaxInt64
		}
		return int64(x)
	case uint32:
		return int64(x)
	case uint16:
		return int64(x)
	case uint8:
		return int64(x)
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func rowFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0
		}
		return x
	case float32:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0
		}
		return f
	case int64:
		return float64(x)
	case int32:
		return float64(x)
	case int:
		return float64(x)
	case uint64:
		return float64(x)
	case uint32:
		return float64(x)
	case uint8:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0
		}
		return f
	default:
		return 0
	}
}

func rowTime(v any) time.Time {
	switch x := v.(type) {
	case time.Time:
		return x.UTC()
	case string:
		s := strings.TrimSpace(x)
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05.999",
			"2006-01-02 15:04:05",
			"2006-01-02",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
		return time.Time{}
	default:
		return time.Time{}
	}
}

// formatCHTime formats a UTC instant for positional ? binding in ClickHouse queries.
func formatCHTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}
