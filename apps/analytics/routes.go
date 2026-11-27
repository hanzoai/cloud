// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/datastore"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/zap-proto/zip"
)

// Config configures an analytics Service instance. Zero values pick production defaults.
type Config struct {
	// Backend is the read-only Datastore backend (defaults to DatastoreBackend).
	Backend Backend
	// KV is the Hanzo KV store (defaults to NewHanzoKV(KV_URL)).
	KV KVStore
	// MaxConcurrentQueries caps concurrent Datastore reads (defaults to MaxConcurrentQueries).
	MaxConcurrentQueries int
	// QueryTimeout bounds any single Datastore query (defaults to QueryTimeout).
	QueryTimeout time.Duration
	// CacheTTL is the fresh Hanzo KV TTL (defaults to DefaultCacheTTL = 15s).
	CacheTTL time.Duration
	// StaleTTL is the last-known snapshot TTL in KV (defaults to DefaultStaleTTL = 24h).
	StaleTTL time.Duration
	// StreamTick is the shared SSE hub refresh interval (defaults to DefaultStreamTick = 5s).
	StreamTick time.Duration
	// StreamHeartbeat is the SSE `: ping` interval (defaults to DefaultStreamHeartbeat = 25s).
	StreamHeartbeat time.Duration
	// Now overrides the clock for deterministic tests.
	Now func() time.Time
}

// Service owns the /v1/analytics/* handlers, guarded Datastore backend, Hanzo KV cache,
// and shared coalesced SSE hub.
type Service struct {
	backend *GuardedBackend
	cache   *Cache
	hub     *Hub
	now     func() time.Time
}

// NewService constructs a wired analytics Service from cfg.
func NewService(cfg Config) *Service {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	gb := NewGuardedBackend(cfg.Backend, cfg.MaxConcurrentQueries, cfg.QueryTimeout)
	cache := NewCache(cfg.KV, cfg.CacheTTL, cfg.StaleTTL, DefaultMaxRefreshes)
	cache.now = nowFn

	s := &Service{
		backend: gb,
		cache:   cache,
		now:     nowFn,
	}
	s.hub = NewHub(func(ctx context.Context, scope Scope) (AnalyticsOverviewData, error) {
		return s.fetchOverview(ctx, scope, "30d")
	}, cfg.StreamTick, cfg.StreamHeartbeat)
	return s
}

// Mount registers all /v1/analytics/* typed operations and the coalesced SSE stream on app.
func (s *Service) Mount(app cloud.Router) {
	z := app.Group("/v1/analytics")
	z.Get("/metrics", s.metrics, zip.WithOperationID("analyticsMetrics"))
	z.Post("/query", s.query, zip.WithOperationID("analyticsQuery"))
	z.Get("/overview", s.overview, zip.WithOperationID("analyticsOverview"))
	z.Get("/health", s.health, zip.WithOperationID("analyticsHealth"))
	z.Get("/freshness", s.freshness, zip.WithOperationID("analyticsFreshness"))
	z.Raw(http.MethodGet, "/stream", s.streamHandler)
}

// Use mounts the analytics plane as its own plugin. The address is owned by
// apps/analytics in manifest.Apps, not by whichever app used to mount it.
func Use(app cloud.Router, deps cloud.Deps) error {
	Routes(app, deps)
	return nil
}

// Routes mounts the default production /v1/analytics/* surface on app, connecting to
// Hanzo Datastore and Hanzo KV (when KV_URL is configured).
func Routes(app cloud.Router, deps cloud.Deps) *Service {
	datastore.Connect()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if datastore.Wait(ctx) == nil {
			if datastore.EnsureAnalyticsSchema(ctx) == nil {
				_ = datastore.SeedAnalyticsCatalog(ctx)
				datastore.StartAnalyticsSnapshotRefresher(context.Background())
			}
		}
	}()
	var kvStore KVStore
	if rawKV := env.Or("KV_URL", ""); rawKV != "" {
		if hkv := NewHanzoKV(rawKV); hkv != nil {
			kvStore = hkv
		}
	}
	s := NewService(Config{
		Backend: DatastoreBackend{},
		KV:      kvStore,
	})
	s.Mount(app)
	return s
}
