// Copyright © 2026 Hanzo AI. MIT License.

package analytics

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

const (
	// DefaultStreamTick is how often a scope hub with >= 1 active subscriber refreshes.
	DefaultStreamTick = 5 * time.Second
	// DefaultStreamHeartbeat is how often an idle SSE stream writes a `: ping` keepalive.
	DefaultStreamHeartbeat = 25 * time.Second
	// DefaultStreamBuffer caps queued updates per subscriber before a slow reader is dropped.
	DefaultStreamBuffer = 16
)

func init() {
	spec.Register("/v1/analytics/stream", http.MethodGet, nil, spec.Bytes{Type: "text/event-stream"})
	spec.Describe("/v1/analytics/stream", http.MethodGet,
		"Stream coalesced real-time analytics updates as Server-Sent Events",
		"Holds one text/event-stream open per viewer while coalescing all viewers on the same authorized "+
			"scope (`global` for SuperAdmin, `org:<slug>` for tenant callers) onto ONE shared backend refresh loop. "+
			"Publishes monotonic `version` integers: `event: snapshot` on connect, `event: delta` when the version "+
			"advances, and `: ping` every 25s. Pass `?since_version=<n>` on reconnect to receive `: up-to-date` "+
			"instead of resending an unchanged snapshot.")
}

// StreamPayload is the JSON body delivered inside `event: snapshot` and `event: delta` frames.
type StreamPayload struct {
	// Version is the monotonically increasing scope revision number.
	Version uint64 `json:"version"`
	// Scope is the canonical authorized scope ("global" or "org:<slug>").
	Scope string `json:"scope"`
	// Overview holds the latest constant-cost KPI snapshot for this scope.
	Overview AnalyticsOverviewData `json:"overview"`
	// PublishedAt is the RFC3339 UTC timestamp when this revision was emitted.
	PublishedAt string `json:"publishedAt"`
}

// streamFrame is one queued SSE event for a subscriber.
type streamFrame struct {
	event   string // "snapshot" or "delta"
	version uint64
	data    []byte
}

// StreamStatus reports live SSE hub utilization for /v1/analytics/health.
type StreamStatus struct {
	// ActiveStreams is the total number of currently connected SSE subscribers across all scopes.
	ActiveStreams int64 `json:"activeStreams"`
	// ActiveScopes is the number of scopes with an active background refresh ticker.
	ActiveScopes int `json:"activeScopes"`
}

// ScopeFetcher fetches the latest AnalyticsOverviewData for a given authorized scope.
type ScopeFetcher func(ctx context.Context, scope Scope) (AnalyticsOverviewData, error)

// Hub manages one shared backend refresh loop per authorized scope so N concurrent
// dashboard viewers share ONE backend ticker (0 ticks when 0 subscribers are connected).
type Hub struct {
	fetch     ScopeFetcher
	tick      time.Duration
	heartbeat time.Duration
	bufSize   int

	mu            sync.Mutex
	scopes        map[string]*scopeHub
	activeStreams atomic.Int64
	nextSubID     atomic.Uint64
}

type scopeHub struct {
	scope   Scope
	key     string
	subs    map[uint64]*subscriber
	cancel  context.CancelFunc
	running bool

	// Monotonic state preserved even across idle pauses so reconnects with
	// ?since_version=<n> never observe version regressions.
	version     uint64
	lastHash    string
	lastPayload []byte
	fetching    bool
	fetchDone   chan struct{}
}

type subscriber struct {
	id     uint64
	ch     chan streamFrame
	closed atomic.Bool
}

func (sub *subscriber) closeOnce() {
	if sub.closed.CompareAndSwap(false, true) {
		close(sub.ch)
	}
}

// NewHub creates a shared coalesced SSE hub.
func NewHub(fetch ScopeFetcher, tick, heartbeat time.Duration) *Hub {
	if tick <= 0 {
		tick = DefaultStreamTick
	}
	if heartbeat <= 0 {
		heartbeat = DefaultStreamHeartbeat
	}
	return &Hub{
		fetch:     fetch,
		tick:      tick,
		heartbeat: heartbeat,
		bufSize:   DefaultStreamBuffer,
		scopes:    make(map[string]*scopeHub),
	}
}

// Stats returns live active stream and active scope counts for /v1/analytics/health.
func (h *Hub) Stats() StreamStatus {
	if h == nil {
		return StreamStatus{}
	}
	h.mu.Lock()
	activeScopes := 0
	for _, sh := range h.scopes {
		if sh.running {
			activeScopes++
		}
	}
	h.mu.Unlock()
	return StreamStatus{
		ActiveStreams: h.activeStreams.Load(),
		ActiveScopes:  activeScopes,
	}
}

// subscription holds one live viewer's handle into a shared scopeHub.
type subscription struct {
	hub        *Hub
	scopeKey   string
	sub        *subscriber
	initialVer uint64
	initialRaw []byte
	upToDate   bool
}

// Subscribe attaches a viewer to scope's shared hub.
// All concurrent subscribers arriving for the same scope coalesce onto ONE initial
// backend fetch and ONE periodic refresh loop.
func (h *Hub) Subscribe(ctx context.Context, scope Scope, sinceVersion uint64) (*subscription, error) {
	key := scope.Key()
	sub := &subscriber{
		id: h.nextSubID.Add(1),
		ch: make(chan streamFrame, h.bufSize),
	}

	h.mu.Lock()
	sh, ok := h.scopes[key]
	if !ok {
		sh = &scopeHub{
			scope: scope,
			key:   key,
			subs:  make(map[uint64]*subscriber),
		}
		h.scopes[key] = sh
	}

	// Register subscriber first so the scope cannot go idle while initial fetch runs.
	sh.subs[sub.id] = sub
	h.activeStreams.Add(1)

	needInitialFetch := sh.version == 0
	var waitCh chan struct{}
	doFetch := false
	if needInitialFetch {
		if sh.fetching {
			waitCh = sh.fetchDone
		} else {
			sh.fetching = true
			sh.fetchDone = make(chan struct{})
			waitCh = sh.fetchDone
			doFetch = true
		}
	}

	// Start the ONE shared ticker goroutine when transitioning from 0 -> 1 active loop.
	if !sh.running {
		loopCtx, cancel := context.WithCancel(context.Background())
		sh.cancel = cancel
		sh.running = true
		go h.runScopeLoop(loopCtx, sh)
	}
	h.mu.Unlock()

	// Coalesce initial snapshot fetch across all concurrently arriving subscribers.
	if doFetch {
		fetchErr := h.refreshScope(ctx, sh, false)
		h.mu.Lock()
		sh.fetching = false
		close(sh.fetchDone)
		sh.fetchDone = nil
		h.mu.Unlock()
		if fetchErr != nil {
			h.unsubscribe(key, sub)
			return nil, fetchErr
		}
	} else if waitCh != nil {
		select {
		case <-waitCh:
		case <-ctx.Done():
			h.unsubscribe(key, sub)
			return nil, ctx.Err()
		}
	}

	h.mu.Lock()
	ver := sh.version
	raw := sh.lastPayload
	h.mu.Unlock()

	if ver == 0 || len(raw) == 0 {
		// Initial fetch failed for the coalesced leader.
		h.unsubscribe(key, sub)
		return nil, ErrDatastoreUnavailable
	}

	return &subscription{
		hub:        h,
		scopeKey:   key,
		sub:        sub,
		initialVer: ver,
		initialRaw: raw,
		upToDate:   sinceVersion > 0 && sinceVersion == ver,
	}, nil
}

// Unsubscribe detaches the viewer and stops the scope's refresh ticker if it was the last viewer.
func (s *subscription) Unsubscribe() {
	if s == nil || s.hub == nil || s.sub == nil {
		return
	}
	s.hub.unsubscribe(s.scopeKey, s.sub)
}

func (h *Hub) unsubscribe(scopeKey string, sub *subscriber) {
	h.mu.Lock()
	sh, ok := h.scopes[scopeKey]
	if ok {
		if _, exists := sh.subs[sub.id]; exists {
			delete(sh.subs, sub.id)
			h.activeStreams.Add(-1)
		}
		if len(sh.subs) == 0 && sh.running {
			sh.cancel()
			sh.cancel = nil
			sh.running = false
		}
	}
	h.mu.Unlock()
	sub.closeOnce()
}

// runScopeLoop is the ONE background refresh goroutine for an active scope.
// It exits immediately when the last subscriber disconnects (ctx cancelled).
func (h *Hub) runScopeLoop(ctx context.Context, sh *scopeHub) {
	ticker := time.NewTicker(h.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rctx, cancel := context.WithTimeout(ctx, QueryTimeout)
			_ = h.refreshScope(rctx, sh, true)
			cancel()
		}
	}
}

// refreshScope executes ONE backend fetch for sh. If the data changed (or this is the
// initial snapshot), it increments the monotonic version and fans out `event: delta`
// without blocking on slow subscribers.
func (h *Hub) refreshScope(ctx context.Context, sh *scopeHub, broadcast bool) error {
	overview, err := h.fetch(ctx, sh.scope)
	if err != nil {
		return err
	}
	hash := overviewContentHash(overview)

	h.mu.Lock()
	if sh.version > 0 && sh.lastHash == hash {
		h.mu.Unlock()
		return nil
	}
	sh.version++
	ver := sh.version
	payload := StreamPayload{
		Version:     ver,
		Scope:       sh.key,
		Overview:    overview,
		PublishedAt: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	sh.lastHash = hash
	sh.lastPayload = raw

	var slow []*subscriber
	if broadcast {
		frame := streamFrame{event: "delta", version: ver, data: raw}
		for _, sub := range sh.subs {
			if sub.closed.Load() {
				continue
			}
			select {
			case sub.ch <- frame:
			default:
				// Drop slow subscriber cleanly without ever blocking the hub.
				slow = append(slow, sub)
			}
		}
		for _, sub := range slow {
			delete(sh.subs, sub.id)
			h.activeStreams.Add(-1)
		}
		if len(sh.subs) == 0 && sh.running {
			sh.cancel()
			sh.cancel = nil
			sh.running = false
		}
	}
	h.mu.Unlock()

	for _, sub := range slow {
		sub.closeOnce()
	}
	return nil
}

// overviewContentHash computes a stable digest of KPI values + stale status so unchanged
// periodic refreshes do not spuriously advance the monotonic stream version.
func overviewContentHash(ov AnalyticsOverviewData) string {
	type stableView struct {
		Scope  string       `json:"scope"`
		Org    string       `json:"org"`
		Window string       `json:"window"`
		KPIs   OverviewKPIs `json:"kpis"`
		Stale  bool         `json:"stale"`
	}
	b, _ := json.Marshal(stableView{
		Scope:  ov.Scope,
		Org:    ov.Org,
		Window: ov.Window,
		KPIs:   ov.KPIs,
		Stale:  ov.Freshness.Stale,
	})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// pumpSSE streams frames to w until ctx ends, the subscription channel closes, or write fails.
func pumpSSE(ctx context.Context, w *bufio.Writer, sub *subscription, heartbeat time.Duration) {
	if sub.upToDate {
		if !writeSSE(w, ": up-to-date\n\n") {
			return
		}
	} else {
		if !writeSSE(w, fmt.Sprintf("id: %d\nevent: snapshot\ndata: %s\n\n", sub.initialVer, sub.initialRaw)) {
			return
		}
	}

	hb := time.NewTicker(heartbeat)
	defer hb.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if !writeSSE(w, ": ping\n\n") {
				return
			}
		case frame, open := <-sub.sub.ch:
			if !open {
				return
			}
			if !writeSSE(w, fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", frame.version, frame.event, frame.data)) {
				return
			}
		}
	}
}

func writeSSE(w *bufio.Writer, s string) bool {
	if _, err := w.WriteString(s); err != nil {
		return false
	}
	return w.Flush() == nil
}

// streamHandler serves GET /v1/analytics/stream.
func (s *Service) streamHandler(c *zip.Ctx) error {
	baseScope, err := ResolveScopeCtx(c)
	if err != nil {
		return err
	}

	reqScope := strings.TrimSpace(c.Query("scope"))
	reqOrg := strings.TrimSpace(c.Query("org"))
	if !baseScope.Super && strings.EqualFold(reqScope, "global") {
		return zip.ErrForbidden("global analytics stream requires SuperAdmin")
	}
	effScope, err := EnforceOrgParam(baseScope, reqOrg)
	if err != nil {
		return PlanErrorToZip(err)
	}

	var sinceVersion uint64
	if rawSince := strings.TrimSpace(c.Query("since_version")); rawSince != "" {
		v, perr := strconv.ParseUint(rawSince, 10, 64)
		if perr != nil {
			return zip.ErrBadRequest("invalid since_version: must be a non-negative integer")
		}
		sinceVersion = v
	}

	// Fail fast when neither Datastore nor a cached KV snapshot is available so callers
	// back off cleanly instead of holding open dead SSE sockets.
	cacheKey := CacheKey(effScope.Key(), "overview:30d")
	if !s.backend.Ready() && !s.cache.HasStaleSnapshot(c.Context(), cacheKey) {
		return zip.Errorf(http.StatusServiceUnavailable, "analytics stream unavailable: datastore not connected")
	}

	sub, err := s.hub.Subscribe(c.Context(), effScope, sinceVersion)
	if err != nil {
		return PlanErrorToZip(err)
	}

	ctx := context.WithoutCancel(c.Context())
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer sub.Unsubscribe()
		pumpSSE(ctx, w, sub, s.hub.heartbeat)
	})
}
