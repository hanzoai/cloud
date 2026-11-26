// SPDX-License-Identifier: Apache-2.0

package preble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func makeTokens(seed uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := 0; i < n; i++ {
		out[i] = seed*1_000_003 + uint64(i)*31 + 7
	}
	return out
}

func TestPrefixIndexCausalAndLRUBoundedMemory(t *testing.T) {
	idx := NewPrefixIndex(16)
	if err := idx.AddReplica("gpu-0", 128); err != nil { // 8 blocks max
		t.Fatalf("AddReplica: %v", err)
	}

	// Insert 64 tokens (4 blocks) for tenant-a.
	tokA := makeTokens(1, 64)
	blksA, err := HashTokenBlocks("tenant-a", "default", tokA, 16)
	if err != nil {
		t.Fatalf("HashTokenBlocks: %v", err)
	}
	pinned, matched, evicted, err := idx.ReserveAndPin("tenant-a", "default", "gpu-0", blksA)
	if err != nil {
		t.Fatalf("ReserveAndPin: %v", err)
	}
	if matched != 0 || evicted != 0 || len(pinned) != 4 {
		t.Fatalf("unexpected initial reserve: matched=%d evicted=%d pinned=%d", matched, evicted, len(pinned))
	}
	idx.Unpin("gpu-0", pinned)

	// Extend tokA with 32 new tokens -> should match all 64 prefix tokens.
	tokA2 := append(append([]uint64(nil), tokA...), makeTokens(99, 32)...)
	blksA2, _ := HashTokenBlocks("tenant-a", "default", tokA2, 16)
	m := idx.Match("tenant-a", "default", blksA2)
	if m.MatchedTokens["gpu-0"] != 64 || m.MatchedBlocks["gpu-0"] != 4 {
		t.Fatalf("expected 64 matched tokens (4 blocks), got %d (%d blocks)", m.MatchedTokens["gpu-0"], m.MatchedBlocks["gpu-0"])
	}

	// Flood 500 completely unique 64-token prompts (2,000 blocks > 8 block capacity).
	// Verify LRU evicts cleanly and radix tree NodeCount remains strictly <= MaxBlocks (8).
	for i := 0; i < 500; i++ {
		uTok := makeTokens(uint64(1000+i), 64)
		uBlks, _ := HashTokenBlocks("tenant-a", "default", uTok, 16)
		p, _, _, err := idx.ReserveAndPin("tenant-a", "default", "gpu-0", uBlks)
		if err != nil {
			t.Fatalf("flood reserve %d: %v", i, err)
		}
		idx.Unpin("gpu-0", p)
		if idx.NodeCount() > 8 {
			t.Fatalf("radix tree unbounded at iter %d: nodeCount=%d > maxBlocks=8", i, idx.NodeCount())
		}
	}

	snap, ok := idx.ReplicaSnapshot("gpu-0")
	if !ok {
		t.Fatal("missing gpu-0 snapshot")
	}
	if snap.UsedBlocks > snap.MaxBlocks {
		t.Fatalf("used blocks %d exceeded max blocks %d", snap.UsedBlocks, snap.MaxBlocks)
	}
	if snap.EvictedBlocks == 0 {
		t.Fatal("expected non-zero LRU evictions under flood")
	}
}

func TestPrefixIndexPinnedCapacityBypass(t *testing.T) {
	idx := NewPrefixIndex(16)
	if err := idx.AddReplica("gpu-0", 64); err != nil { // 4 blocks max
		t.Fatalf("AddReplica: %v", err)
	}
	// Pin all 4 blocks with request 1 (do not unpin).
	blks1, _ := HashTokenBlocks("org-a", "", makeTokens(10, 64), 16)
	p1, _, _, err := idx.ReserveAndPin("org-a", "", "gpu-0", blks1)
	if err != nil {
		t.Fatalf("reserve 1: %v", err)
	}
	if len(p1) != 4 {
		t.Fatalf("expected 4 pinned blocks, got %d", len(p1))
	}

	// Request 2 arrives with 4 different blocks while all 4 cache blocks are pinned:
	// must NOT evict pinned blocks or exceed MaxBlocks; must bypass cleanly.
	blks2, _ := HashTokenBlocks("org-a", "", makeTokens(20, 64), 16)
	p2, _, evicted, err := idx.ReserveAndPin("org-a", "", "gpu-0", blks2)
	if err != nil {
		t.Fatalf("reserve 2: %v", err)
	}
	if len(p2) != 0 || evicted != 0 {
		t.Fatalf("expected full pin bypass (0 pinned, 0 evicted), got pinned=%d evicted=%d", len(p2), evicted)
	}
	snap, _ := idx.ReplicaSnapshot("gpu-0")
	if snap.UsedBlocks != 4 || snap.CacheBypassed == 0 {
		t.Fatalf("expected UsedBlocks=4 and CacheBypassed>0, got used=%d bypassed=%d", snap.UsedBlocks, snap.CacheBypassed)
	}

	idx.Unpin("gpu-0", p1)
}

func TestStrictCrossTenantAndACLIsolation(t *testing.T) {
	sched, err := NewScheduler(SchedulerConfig{Policy: PolicyPrebleE2, BlockSize: 16})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := sched.AddReplica(ReplicaConfig{
			ID:             fmt.Sprintf("gpu-%d", i),
			KVBudgetTokens: 65536,
		}); err != nil {
			t.Fatalf("AddReplica: %v", err)
		}
	}

	sharedSecretPrefix := makeTokens(4242, 2048)

	// Tenant A warms the prefix.
	decA, err := sched.Place(PlacementRequest{
		RequestID:    "req-a-1",
		Tenant:       "org-alpha",
		ACLScope:     "repo-secret",
		TokenIDs:     sharedSecretPrefix,
		OutputTokens: 128,
	})
	if err != nil {
		t.Fatalf("Place A1: %v", err)
	}
	if decA.MatchedTokens != 0 {
		t.Fatalf("expected cold miss for A1, got %d", decA.MatchedTokens)
	}
	if err := sched.Complete("org-alpha", "req-a-1"); err != nil {
		t.Fatalf("Complete A1: %v", err)
	}

	// Tenant A follow-up in same ACL scope MUST hit cache on the exact warm replica.
	followA := append(append([]uint64(nil), sharedSecretPrefix...), makeTokens(77, 128)...)
	decA2, err := sched.Place(PlacementRequest{
		RequestID:    "req-a-2",
		Tenant:       "org-alpha",
		ACLScope:     "repo-secret",
		TokenIDs:     followA,
		OutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("Place A2: %v", err)
	}
	if decA2.ReplicaID != decA.ReplicaID || decA2.MatchedTokens != 2048 {
		t.Fatalf("expected A2 to reuse 2048 tokens on %s, got replica=%s matched=%d", decA.ReplicaID, decA2.ReplicaID, decA2.MatchedTokens)
	}

	// Tenant B sends the EXACT SAME token sequence + same ACLScope name: MUST have 0 matched tokens!
	decB, err := sched.Place(PlacementRequest{
		RequestID:    "req-b-1",
		Tenant:       "org-beta",
		ACLScope:     "repo-secret",
		TokenIDs:     followA,
		OutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("Place B1: %v", err)
	}
	if decB.MatchedTokens != 0 || decB.CacheReuseRatio != 0 {
		t.Fatalf("CROSS-TENANT LEAK: org-beta matched %d tokens from org-alpha!", decB.MatchedTokens)
	}

	// Tenant A in a DIFFERENT ACLScope sends exact same tokens: MUST have 0 matched tokens!
	decADiffACL, err := sched.Place(PlacementRequest{
		RequestID:    "req-a-other-acl",
		Tenant:       "org-alpha",
		ACLScope:     "repo-public",
		TokenIDs:     followA,
		OutputTokens: 64,
	})
	if err != nil {
		t.Fatalf("Place A diff ACL: %v", err)
	}
	if decADiffACL.MatchedTokens != 0 {
		t.Fatalf("CROSS-ACL LEAK: repo-public matched %d tokens from repo-secret!", decADiffACL.MatchedTokens)
	}

	// Tenant B MUST NOT be able to complete Tenant A's active request.
	if err := sched.Complete("org-beta", "req-a-2"); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("expected ErrRequestNotFound on cross-tenant Complete, got %v", err)
	}
	if err := sched.Complete("org-alpha", "req-a-2"); err != nil {
		t.Fatalf("owner Complete A2 failed: %v", err)
	}
}

func TestPrebleE2HotPrefixReplicationBeatsPrefixGreedy(t *testing.T) {
	// Build a skewed hot-prefix workload where 85% of requests hit 1 hot 4096-token prefix
	// arriving rapidly on 4 GPUs.
	hotPrefix := makeTokens(7, 4096)
	var reqs []TraceRequest
	for i := 0; i < 120; i++ {
		var toks []uint64
		if i%10 < 8 {
			toks = append(append([]uint64(nil), hotPrefix...), makeTokens(uint64(100+i), 384)...)
		} else {
			toks = append(makeTokens(uint64(5000+i%4), 3072), makeTokens(uint64(9000+i), 256)...)
		}
		reqs = append(reqs, TraceRequest{
			RequestID:    fmt.Sprintf("r-%d", i),
			ArrivalMs:    float64(i) * 14.0, // ~71 req/s burst
			Tenant:       "org-hanzo",
			ACLScope:     "coding",
			TokenIDs:     toks,
			OutputTokens: 64,
		})
	}
	var reps []ReplicaConfig
	for i := 0; i < 4; i++ {
		reps = append(reps, ReplicaConfig{
			ID:               fmt.Sprintf("gpu-%d", i),
			KVBudgetTokens:   65536,
			PrefillTokPerSec: 40000,
			DecodeTokPerSec:  1800,
		})
	}
	trace := WorkloadTrace{
		Name:            "unit-skew",
		InitialReplicas: reps,
		Requests:        reqs,
		BlockSize:       16,
	}

	greedyRes, err := RunSimulation(trace, PolicyPrefixGreedy, SchedulerConfig{})
	if err != nil {
		t.Fatalf("RunSimulation greedy: %v", err)
	}
	llRes, err := RunSimulation(trace, PolicyLeastLoaded, SchedulerConfig{})
	if err != nil {
		t.Fatalf("RunSimulation least_loaded: %v", err)
	}
	prebleRes, err := RunSimulation(trace, PolicyPrebleE2, SchedulerConfig{})
	if err != nil {
		t.Fatalf("RunSimulation preble_e2: %v", err)
	}

	if prebleRes.HotPrefixReplications == 0 {
		t.Fatalf("expected Preble E2 to dynamically replicate hot prefix under skew, got 0 replications")
	}
	if prebleRes.TTFTP99Ms >= greedyRes.TTFTP99Ms {
		t.Fatalf("expected Preble E2 p99 TTFT (%.2fms) < prefix_greedy (%.2fms)", prebleRes.TTFTP99Ms, greedyRes.TTFTP99Ms)
	}
	if prebleRes.TTFTP99Ms >= llRes.TTFTP99Ms {
		t.Fatalf("expected Preble E2 p99 TTFT (%.2fms) < least_loaded (%.2fms)", prebleRes.TTFTP99Ms, llRes.TTFTP99Ms)
	}
	if prebleRes.CacheReusePercent <= llRes.CacheReusePercent {
		t.Fatalf("expected Preble E2 cache reuse (%.2f%%) > least_loaded (%.2f%%)", prebleRes.CacheReusePercent, llRes.CacheReusePercent)
	}
}

func TestNegativeRegimeUniquePromptsNoDegradation(t *testing.T) {
	// 100% unique unshared prompts -> 0% cache reuse. Preble E2 must match least_loaded
	// without degrading p99 TTFT or GPU utilization.
	var reqs []TraceRequest
	for i := 0; i < 80; i++ {
		reqs = append(reqs, TraceRequest{
			RequestID:    fmt.Sprintf("uniq-%d", i),
			ArrivalMs:    float64(i) * 20.0,
			Tenant:       "org-hanzo",
			ACLScope:     "batch",
			TokenIDs:     makeTokens(uint64(50_000+i), 2048),
			OutputTokens: 64,
		})
	}
	var reps []ReplicaConfig
	for i := 0; i < 4; i++ {
		reps = append(reps, ReplicaConfig{
			ID:               fmt.Sprintf("gpu-%d", i),
			KVBudgetTokens:   32768,
			PrefillTokPerSec: 40000,
		})
	}
	trace := WorkloadTrace{
		Name:            "unit-unique",
		InitialReplicas: reps,
		Requests:        reqs,
		BlockSize:       16,
	}

	llRes, err := RunSimulation(trace, PolicyLeastLoaded, SchedulerConfig{})
	if err != nil {
		t.Fatalf("RunSimulation least_loaded: %v", err)
	}
	prebleRes, err := RunSimulation(trace, PolicyPrebleE2, SchedulerConfig{})
	if err != nil {
		t.Fatalf("RunSimulation preble_e2: %v", err)
	}

	if prebleRes.CacheReusePercent != 0 {
		t.Fatalf("expected 0%% cache reuse on unique prompts, got %.2f%%", prebleRes.CacheReusePercent)
	}
	// Preble E2 p99 TTFT must stay within 5% of least_loaded on 0%-reuse workload.
	if prebleRes.TTFTP99Ms > llRes.TTFTP99Ms*1.05+1.0 {
		t.Fatalf("Preble E2 degraded p99 TTFT on zero-reuse negative regime: preble=%.2fms vs least_loaded=%.2fms",
			prebleRes.TTFTP99Ms, llRes.TTFTP99Ms)
	}
}

func TestScaleOutColdStartAvoidance(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sched, err := NewScheduler(SchedulerConfig{Policy: PolicyPrebleE2})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := sched.AddReplicaAt(base, ReplicaConfig{ID: "gpu-warm", KVBudgetTokens: 65536}); err != nil {
		t.Fatalf("add warm: %v", err)
	}
	// Add a cold replica with 500ms warmup.
	if err := sched.AddReplicaAt(base, ReplicaConfig{
		ID:             "gpu-cold",
		KVBudgetTokens: 65536,
		WarmupDuration: 500 * time.Millisecond,
	}); err != nil {
		t.Fatalf("add cold: %v", err)
	}

	// At t=10ms, gpu-cold still has 490ms cold-start penalty -> must pick gpu-warm.
	dec1, err := sched.PlaceAt(base.Add(10*time.Millisecond), PlacementRequest{
		RequestID: "r1",
		Tenant:    "org-a",
		TokenIDs:  makeTokens(1, 512),
	})
	if err != nil {
		t.Fatalf("PlaceAt 10ms: %v", err)
	}
	if dec1.ReplicaID != "gpu-warm" {
		t.Fatalf("expected gpu-warm during cold start, got %s", dec1.ReplicaID)
	}

	// At t=600ms (after warmup), gpu-warm still has active r1 so warmed gpu-cold absorbs r2.
	dec2, err := sched.PlaceAt(base.Add(600*time.Millisecond), PlacementRequest{
		RequestID: "r2",
		Tenant:    "org-a",
		TokenIDs:  makeTokens(2, 8192),
	})
	if err != nil {
		t.Fatalf("PlaceAt 600ms: %v", err)
	}
	if dec2.ReplicaID != "gpu-cold" {
		t.Fatalf("expected warmed gpu-cold to absorb traffic after warmup, got %s", dec2.ReplicaID)
	}
	// While gpu-cold is busy prefilling 8192 tokens, r3 balances back onto gpu-warm.
	dec3, err := sched.PlaceAt(base.Add(601*time.Millisecond), PlacementRequest{
		RequestID: "r3",
		Tenant:    "org-a",
		TokenIDs:  makeTokens(3, 512),
	})
	if err != nil {
		t.Fatalf("PlaceAt 601ms: %v", err)
	}
	if dec3.ReplicaID != "gpu-warm" {
		t.Fatalf("expected r3 to balance back to gpu-warm while gpu-cold prefills 8192 tokens, got %s", dec3.ReplicaID)
	}
}

func TestValidationAndOverflowBounds(t *testing.T) {
	sched, err := NewScheduler(SchedulerConfig{Policy: PolicyPrebleE2})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	badReplicas := []ReplicaConfig{
		{ID: ""},
		{ID: "bad-budget", KVBudgetTokens: -1},
		{ID: "huge-budget", KVBudgetTokens: MaxKVBudgetTokens + 1},
		{ID: "nan-prefill", PrefillTokPerSec: math.NaN()},
		{ID: "inf-decode", DecodeTokPerSec: math.Inf(1)},
		{ID: "neg-warmup", WarmupDuration: -time.Second},
	}
	for _, rc := range badReplicas {
		if err := sched.AddReplica(rc); err == nil {
			t.Errorf("expected error for invalid replica config %+v", rc)
		}
	}

	if _, err := sched.Place(PlacementRequest{Tenant: "org-a", TokenIDs: makeTokens(1, 32)}); !errors.Is(err, ErrNoReplicas) {
		t.Fatalf("expected ErrNoReplicas, got %v", err)
	}

	_ = sched.AddReplica(ReplicaConfig{ID: "gpu-0", KVBudgetTokens: 65536})
	if _, err := sched.Place(PlacementRequest{Tenant: "", TokenIDs: makeTokens(1, 32)}); err == nil {
		t.Error("expected error for empty tenant")
	}
	if _, err := sched.Place(PlacementRequest{Tenant: "org-a"}); err == nil {
		t.Error("expected error for empty prompt/tokens")
	}
	if _, err := sched.Place(PlacementRequest{Tenant: "org-a", TokenIDs: makeTokens(1, 32), OutputTokens: -5}); err == nil {
		t.Error("expected error for negative outputTokens")
	}
	if _, err := sched.Place(PlacementRequest{Tenant: "org-a", TokenIDs: makeTokens(1, 32), Policy: "unknown"}); err == nil {
		t.Error("expected error for unsupported policy")
	}
}

func TestSchedulerConcurrencyRaceFree(t *testing.T) {
	sched, err := NewScheduler(SchedulerConfig{Policy: PolicyPrebleE2})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := sched.AddReplica(ReplicaConfig{
			ID:             fmt.Sprintf("gpu-%d", i),
			KVBudgetTokens: 32768,
		}); err != nil {
			t.Fatalf("AddReplica: %v", err)
		}
	}

	sharedByTenant := map[string][]uint64{
		"org-1": makeTokens(101, 512),
		"org-2": makeTokens(101, 512), // identical tokens across different tenants!
		"org-3": makeTokens(303, 512),
		"org-4": makeTokens(404, 512),
	}

	var wg sync.WaitGroup
	tenants := []string{"org-1", "org-2", "org-3", "org-4"}
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			tenant := tenants[worker%len(tenants)]
			prefix := sharedByTenant[tenant]
			for i := 0; i < 40; i++ {
				toks := append(append([]uint64(nil), prefix...), makeTokens(uint64(worker*1000+i), 64)...)
				dec, err := sched.Place(PlacementRequest{
					Tenant:       tenant,
					ACLScope:     "prod",
					TokenIDs:     toks,
					OutputTokens: 32,
				})
				if err != nil {
					t.Errorf("worker %d iter %d Place: %v", worker, i, err)
					return
				}
				_ = sched.Replicas()
				_ = sched.TenantStats(tenant)
				if err := sched.Complete(tenant, dec.RequestID); err != nil {
					t.Errorf("worker %d iter %d Complete: %v", worker, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	for _, tenant := range tenants {
		st := sched.TenantStats(tenant)
		if st.TotalRequests != 80 {
			t.Fatalf("tenant %s expected 80 requests, got %d", tenant, st.TotalRequests)
		}
	}
}

func setupTestHTTPApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	err := Mount(app, Deps{
		Log: luxlog.NewNoOpLogger(),
		Org: func(ctx context.Context) (string, bool) {
			c := zip.CallerOf(ctx)
			if c.User == "" || c.Org == "" {
				return "", false
			}
			return c.Org, true
		},
		Acting: func(ctx context.Context) (string, error) {
			c := zip.CallerOf(ctx)
			if c.User == "" {
				return "", zip.ErrUnauthorized("a validated principal is required")
			}
			if c.Org == "" {
				return "", zip.ErrForbidden("X-Org-Id required")
			}
			return c.Org, nil
		},
		InitialReplicas: []ReplicaConfig{
			{ID: "gpu-0", KVBudgetTokens: 65536},
			{ID: "gpu-1", KVBudgetTokens: 65536},
		},
	})
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func doJSON(t *testing.T, app *zip.App, method, target, org string, body any) (int, []byte) {
	t.Helper()
	return doJSONWithAdmin(t, app, method, target, org, false, body)
}

func doJSONAsOperator(t *testing.T, app *zip.App, method, target, org string, body any) (int, []byte) {
	t.Helper()
	return doJSONWithAdmin(t, app, method, target, org, true, body)
}

func doJSONWithAdmin(t *testing.T, app *zip.App, method, target, org string, isAdmin bool, body any) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if isAdmin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("app.Test %s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

func TestEveryRouteIsDescribed(t *testing.T) {
	app := setupTestHTTPApp(t)
	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal OpenAPI spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal OpenAPI spec: %v", err)
	}
	count := 0
	for path, item := range doc.Paths {
		if len(path) < len("/v1/preble") || path[:len("/v1/preble")] != "/v1/preble" {
			continue
		}
		for method, op := range item {
			count++
			if d, _ := op["description"].(string); d == "" {
				t.Errorf("%s %s has empty OpenAPI description", method, path)
			}
		}
	}
	if count != 7 {
		t.Fatalf("expected 7 /v1/preble operations in OpenAPI ledger, got %d", count)
	}
}

func TestHTTPWireSecurityIsolationAndQueryHijackImmunity(t *testing.T) {
	app := setupTestHTTPApp(t)

	// 1. Public health endpoint works without auth and exposes no tenant data.
	code, body := doJSON(t, app, http.MethodGet, "/v1/preble/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /health status=%d body=%s", code, string(body))
	}
	var health healthOut
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.Status != "ok" || health.Replicas != 2 {
		t.Fatalf("unexpected health: %+v", health)
	}

	// 2. Unauthenticated write/place/metrics must fail closed with 401.
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/v1/preble/replicas", nil},
		{http.MethodGet, "/v1/preble/metrics", nil},
		{http.MethodPost, "/v1/preble/place", map[string]any{"prompt": "hello world"}},
		{http.MethodPost, "/v1/preble/complete", map[string]any{"requestId": "req-1"}},
		{http.MethodPost, "/v1/preble/replicas", map[string]any{"id": "gpu-2"}},
		{http.MethodDelete, "/v1/preble/replicas/gpu-0", nil},
	} {
		c, b := doJSON(t, app, tc.method, tc.path, "", tc.body)
		if c != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated: expected 401, got %d (%s)", tc.method, tc.path, c, string(b))
		}
	}

	// 3a. Non-operator authenticated tenant MUST be rejected with 403 on replica mutations.
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/replicas", "org-alpha", map[string]any{
		"id":             "gpu-2",
		"gpuModel":       "H200-141GB",
		"kvBudgetTokens": 131072,
	})
	if code != http.StatusForbidden {
		t.Fatalf("POST /replicas by non-operator: expected 403, got %d (%s)", code, string(body))
	}
	code, body = doJSON(t, app, http.MethodDelete, "/v1/preble/replicas/gpu-0", "org-beta", nil)
	if code != http.StatusForbidden {
		t.Fatalf("DELETE /replicas/gpu-0 by non-operator: expected 403, got %d (%s)", code, string(body))
	}

	// 3b. Platform operator registers replica gpu-2 via HTTP POST and verifies query-hijack immunity (url:"-").
	code, body = doJSONAsOperator(t, app, http.MethodPost, "/v1/preble/replicas?id=hijacked&kvBudgetTokens=16", "admin", map[string]any{
		"id":             "gpu-2",
		"gpuModel":       "H200-141GB",
		"kvBudgetTokens": 131072,
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /replicas status=%d body=%s", code, string(body))
	}
	var repStatus ReplicaStatus
	if err := json.Unmarshal(body, &repStatus); err != nil {
		t.Fatalf("decode replica: %v", err)
	}
	if repStatus.ID != "gpu-2" || repStatus.KVBudgetTokens != 131072 {
		t.Fatalf("url:\"-\" query hijack occurred! got %+v", repStatus)
	}

	// 4. Org Alpha places a shared coding prompt with hostile query params (?aclScope=hijacked&policy=round_robin).
	sharedPrompt := "You are Hanzo Dev Agent. Here is the full repository context and tool definitions for apps/preble/scheduler.go and apps/apps.go. " +
		"Always enforce strict tenant isolation and verify all KV prefix hashes with SHA-256 domain separation across every GPU replica."
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/place?aclScope=hijacked&policy=round_robin&prompt=hijacked", "org-alpha", map[string]any{
		"requestId":    "alpha-req-1",
		"aclScope":     "workspace-1",
		"prompt":       sharedPrompt,
		"outputTokens": 128,
		"policy":       "preble_e2",
	})
	if code != http.StatusOK {
		t.Fatalf("POST /place alpha-1 status=%d body=%s", code, string(body))
	}
	var decA1 PlacementDecision
	if err := json.Unmarshal(body, &decA1); err != nil {
		t.Fatalf("decode decA1: %v", err)
	}
	if decA1.Tenant != "org-alpha" || decA1.ACLScope != "workspace-1" || decA1.Policy != PolicyPrebleE2 {
		t.Fatalf("unexpected decA1 (query hijack or wrong org): %+v", decA1)
	}

	// 5. Org Beta places the EXACT same prompt + ACLScope -> MUST have matchedTokens == 0!
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/place", "org-beta", map[string]any{
		"requestId":    "beta-req-1",
		"aclScope":     "workspace-1",
		"prompt":       sharedPrompt,
		"outputTokens": 128,
	})
	if code != http.StatusOK {
		t.Fatalf("POST /place beta-1 status=%d body=%s", code, string(body))
	}
	var decB1 PlacementDecision
	_ = json.Unmarshal(body, &decB1)
	if decB1.Tenant != "org-beta" || decB1.MatchedTokens != 0 {
		t.Fatalf("CROSS-TENANT HTTP LEAK: org-beta got %+v", decB1)
	}

	// 6. Org Beta cannot complete Org Alpha's request -> 404.
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/complete", "org-beta", map[string]any{
		"requestId": "alpha-req-1",
	})
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 on cross-org complete, got %d (%s)", code, string(body))
	}

	// 7. Org Alpha completes its own request -> 200, then second turn hits cache!
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/complete", "org-alpha", map[string]any{
		"requestId": "alpha-req-1",
	})
	if code != http.StatusOK {
		t.Fatalf("owner complete failed: %d (%s)", code, string(body))
	}

	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/place", "org-alpha", map[string]any{
		"requestId":    "alpha-req-2",
		"aclScope":     "workspace-1",
		"prompt":       sharedPrompt + " Now write unit tests for prefix eviction.",
		"outputTokens": 64,
	})
	if code != http.StatusOK {
		t.Fatalf("POST /place alpha-2 status=%d body=%s", code, string(body))
	}
	var decA2 PlacementDecision
	_ = json.Unmarshal(body, &decA2)
	if decA2.MatchedTokens == 0 || decA2.ReplicaID != decA1.ReplicaID {
		t.Fatalf("expected alpha-2 cache hit on %s, got %+v", decA1.ReplicaID, decA2)
	}

	// 8. Org-scoped metrics & operator-only replica deletion.
	code, body = doJSON(t, app, http.MethodGet, "/v1/preble/metrics", "org-alpha", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /metrics status=%d", code)
	}
	var m TenantMetrics
	_ = json.Unmarshal(body, &m)
	if m.Tenant != "org-alpha" || m.TotalRequests != 2 || m.TotalMatchedTokens == 0 {
		t.Fatalf("unexpected alpha metrics: %+v", m)
	}

	// Non-operator org-alpha still cannot delete gpu-2.
	code, body = doJSON(t, app, http.MethodDelete, "/v1/preble/replicas/gpu-2", "org-alpha", nil)
	if code != http.StatusForbidden {
		t.Fatalf("DELETE /replicas/gpu-2 by non-operator: expected 403, got %d (%s)", code, string(body))
	}

	// Platform operator deletes gpu-2 cleanly.
	code, body = doJSONAsOperator(t, app, http.MethodDelete, "/v1/preble/replicas/gpu-2", "admin", nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE /replicas/gpu-2 by operator status=%d body=%s", code, string(body))
	}
}

// Regression test for Codex Finding #1: normal org B must not be able to delete or add
// shared replicas while org A has active placements.
func TestReplicaMutationRequiresOperatorAuthorization(t *testing.T) {
	app := setupTestHTTPApp(t)

	// Org A places an active request onto one of the shared GPUs.
	code, body := doJSON(t, app, http.MethodPost, "/v1/preble/place", "org-a", map[string]any{
		"requestId":    "org-a-active",
		"prompt":       "active multi-turn prompt for org-a on shared gpu",
		"outputTokens": 64,
	})
	if code != http.StatusOK {
		t.Fatalf("org-a place failed: %d (%s)", code, string(body))
	}
	var dec PlacementDecision
	if err := json.Unmarshal(body, &dec); err != nil {
		t.Fatalf("decode decision: %v", err)
	}

	// Org B lists replicas and tries to delete the replica holding Org A's active request.
	code, body = doJSON(t, app, http.MethodDelete, "/v1/preble/replicas/"+dec.ReplicaID, "org-b", nil)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for org-b deleting shared replica %s, got %d (%s)", dec.ReplicaID, code, string(body))
	}

	// Org B also tries to register a rogue replica.
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/replicas", "org-b", map[string]any{
		"id": "rogue-gpu",
	})
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for org-b adding shared replica, got %d (%s)", code, string(body))
	}

	// Org A's active request MUST still exist and complete cleanly.
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/complete", "org-a", map[string]any{
		"requestId": "org-a-active",
	})
	if code != http.StatusOK {
		t.Fatalf("org-a active placement was disrupted: complete status=%d (%s)", code, string(body))
	}
}

// Regression test for Codex Finding #2: active requestIds must be namespaced by (tenant, requestId)
// so two orgs can concurrently use identical requestIds without collision or existence probing.
func TestActiveRequestIDsNamespacedByTenant(t *testing.T) {
	app := setupTestHTTPApp(t)

	// Org A places with predictable requestId "job-1".
	code, body := doJSON(t, app, http.MethodPost, "/v1/preble/place", "org-a", map[string]any{
		"requestId":    "job-1",
		"prompt":       "org-a prompt content",
		"outputTokens": 32,
	})
	if code != http.StatusOK {
		t.Fatalf("org-a place job-1 failed: %d (%s)", code, string(body))
	}

	// Org B simultaneously places with the EXACT SAME requestId "job-1" -> MUST succeed!
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/place", "org-b", map[string]any{
		"requestId":    "job-1",
		"prompt":       "org-b prompt content",
		"outputTokens": 32,
	})
	if code != http.StatusOK {
		t.Fatalf("org-b place with same requestId 'job-1' failed: %d (%s)", code, string(body))
	}

	// Org A submitting duplicate "job-1" within its own namespace MUST still be rejected (400).
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/place", "org-a", map[string]any{
		"requestId":    "job-1",
		"prompt":       "org-a duplicate",
		"outputTokens": 32,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 on same-tenant duplicate active requestId, got %d (%s)", code, string(body))
	}

	// Org B completes its "job-1" -> MUST NOT affect Org A's "job-1".
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/complete", "org-b", map[string]any{
		"requestId": "job-1",
	})
	if code != http.StatusOK {
		t.Fatalf("org-b complete job-1 failed: %d (%s)", code, string(body))
	}

	// Org B completing "job-1" a second time -> 404.
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/complete", "org-b", map[string]any{
		"requestId": "job-1",
	})
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 on second org-b complete, got %d (%s)", code, string(body))
	}

	// Org A's "job-1" is still active and completes cleanly.
	code, body = doJSON(t, app, http.MethodPost, "/v1/preble/complete", "org-a", map[string]any{
		"requestId": "job-1",
	})
	if code != http.StatusOK {
		t.Fatalf("org-a complete job-1 failed after org-b completed its own job-1: %d (%s)", code, string(body))
	}
}

// Regression test for Codex Finding #3: when a full cache evicts the last block of the
// same (tenant, aclScope) during ReserveAndPin, the namespace root must stay registered
// in idx.roots so newly attached blocks are reachable by Match.
func TestReserveAndPinReRegistersPrunedNamespaceRoot(t *testing.T) {
	idx := NewPrefixIndex(16)
	// Two-block cache: holds exactly 2 blocks total.
	if err := idx.AddReplica("gpu-0", 32); err != nil {
		t.Fatalf("AddReplica: %v", err)
	}

	// Insert 1 unpinned block for tenant A (becomes LRU).
	blkA1, err := HashTokenBlocks("tenant-a", "default", makeTokens(1, 16), 16)
	if err != nil {
		t.Fatalf("HashTokenBlocks A1: %v", err)
	}
	pA1, _, _, err := idx.ReserveAndPin("tenant-a", "default", "gpu-0", blkA1)
	if err != nil {
		t.Fatalf("ReserveAndPin A1: %v", err)
	}
	idx.Unpin("gpu-0", pA1)

	// Insert 1 unpinned block for tenant B (becomes MRU; cache is now full: 2/2 blocks).
	blkB1, err := HashTokenBlocks("tenant-b", "default", makeTokens(2, 16), 16)
	if err != nil {
		t.Fatalf("HashTokenBlocks B1: %v", err)
	}
	pB1, _, _, err := idx.ReserveAndPin("tenant-b", "default", "gpu-0", blkB1)
	if err != nil {
		t.Fatalf("ReserveAndPin B1: %v", err)
	}
	idx.Unpin("gpu-0", pB1)

	// Tenant A inserts a different 1-block prefix A2.
	// This evicts A1 (the LRU block, which is the ONLY block under tenant-a's root).
	blkA2, err := HashTokenBlocks("tenant-a", "default", makeTokens(3, 16), 16)
	if err != nil {
		t.Fatalf("HashTokenBlocks A2: %v", err)
	}
	pA2, matched, evicted, err := idx.ReserveAndPin("tenant-a", "default", "gpu-0", blkA2)
	if err != nil {
		t.Fatalf("ReserveAndPin A2: %v", err)
	}
	if matched != 0 || evicted != 1 || len(pA2) != 1 {
		t.Fatalf("unexpected A2 reserve: matched=%d evicted=%d pinned=%d", matched, evicted, len(pA2))
	}
	idx.Unpin("gpu-0", pA2)

	// Match MUST find A2 (16 tokens) and B1 (16 tokens) cleanly.
	mA2 := idx.Match("tenant-a", "default", blkA2)
	if mA2.MatchedTokens["gpu-0"] != 16 || mA2.MatchedBlocks["gpu-0"] != 1 {
		t.Fatalf("detached namespace root: Match(A2) returned %d tokens (%d blocks), expected 16 (1 block)",
			mA2.MatchedTokens["gpu-0"], mA2.MatchedBlocks["gpu-0"])
	}
	mB1 := idx.Match("tenant-b", "default", blkB1)
	if mB1.MatchedTokens["gpu-0"] != 16 {
		t.Fatalf("Match(B1) returned %d tokens, expected 16", mB1.MatchedTokens["gpu-0"])
	}
	if idx.TenantCount() != 2 || idx.NodeCount() != 2 {
		t.Fatalf("expected TenantCount=2 NodeCount=2, got tenants=%d nodes=%d", idx.TenantCount(), idx.NodeCount())
	}
}

// Regression test for Codex Finding #4: simulator must share a replica's aggregate
// DecodeTokPerSec budget across simultaneous decode streams and reschedule completions
// as the batch changes.
func TestSimulationEnforcesAggregateDecodeBudget(t *testing.T) {
	// 1 default replica (DecodeTokPerSec = 1800) with 64 simultaneous requests arriving
	// at t=0, each requiring 64 decode tokens = 4,096 aggregate decode tokens.
	// At 1,800 tok/s aggregate rate, decode alone requires >= 4096/1800 = 2275.56 ms.
	reqs := make([]TraceRequest, 64)
	for i := 0; i < 64; i++ {
		reqs[i] = TraceRequest{
			RequestID:    fmt.Sprintf("sim-dec-%d", i),
			ArrivalMs:    0,
			Tenant:       "org-bench",
			ACLScope:     "default",
			TokenIDs:     makeTokens(uint64(100+i), 64),
			OutputTokens: 64,
		}
	}
	trace := WorkloadTrace{
		Name: "aggregate-decode-budget",
		InitialReplicas: []ReplicaConfig{
			{
				ID:               "gpu-0",
				KVBudgetTokens:   65536,
				PrefillTokPerSec: 40000,
				DecodeTokPerSec:  1800,
				MaxConcurrent:    64,
			},
		},
		Requests:  reqs,
		BlockSize: 16,
	}

	res, err := RunSimulation(trace, PolicyPrebleE2, SchedulerConfig{})
	if err != nil {
		t.Fatalf("RunSimulation: %v", err)
	}
	minDecodeMs := (4096.0 / 1800.0) * 1000.0 // 2275.555... ms
	if res.MakespanMs < minDecodeMs {
		t.Fatalf("aggregate DecodeTokPerSec violated: 4096 decode tokens finished in %.2f ms, below physical minimum %.2f ms",
			res.MakespanMs, minDecodeMs)
	}

	// Single stream must still be capped at per-stream speed (80 tok/s => 64/80*1000 = 800ms decode).
	singleTrace := WorkloadTrace{
		Name:            "single-stream-decode",
		InitialReplicas: trace.InitialReplicas,
		Requests:        reqs[:1],
		BlockSize:       16,
	}
	singleRes, err := RunSimulation(singleTrace, PolicyPrebleE2, SchedulerConfig{})
	if err != nil {
		t.Fatalf("RunSimulation single: %v", err)
	}
	if singleRes.MakespanMs < 800.0 || singleRes.MakespanMs > 820.0 {
		t.Fatalf("expected single-stream makespan ~801.6ms (64/40000s prefill + 64/80s decode), got %.2f ms", singleRes.MakespanMs)
	}
}

// Regression test for Codex Finding #5: raw Prompt tokenization must preserve whitespace
// (indentation, spaces, tabs, newlines) so distinct prompts never collide in KV cache identity.
func TestTokenizeTextPreservesWhitespaceIdentity(t *testing.T) {
	// Python indentation example: second() inside vs outside an if block.
	insideIf := "if cond:\n    first()\n    second()\n"
	outsideIf := "if cond:\n    first()\nsecond()\n"

	tokIn := TokenizeText(insideIf)
	tokOut := TokenizeText(outsideIf)
	if len(tokIn) == 0 || len(tokOut) == 0 {
		t.Fatal("expected non-empty token sequences")
	}
	same := len(tokIn) == len(tokOut)
	if same {
		for i := range tokIn {
			if tokIn[i] != tokOut[i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Fatalf("whitespace-distinct Python prompts produced identical tokens: %v", tokIn)
	}

	// Verify end-to-end on Scheduler.Place raw Prompt path: warming insideIf must NOT
	// fabricate full-prompt KV reuse for outsideIf (blockSize=16 covers the whole prompt).
	sched, err := NewScheduler(SchedulerConfig{Policy: PolicyPrebleE2, BlockSize: 16})
	if err != nil {
		t.Fatalf("NewScheduler: %v", err)
	}
	if err := sched.AddReplica(ReplicaConfig{ID: "gpu-0", KVBudgetTokens: 65536}); err != nil {
		t.Fatalf("AddReplica: %v", err)
	}
	dec1, err := sched.Place(PlacementRequest{
		RequestID:    "py-1",
		Tenant:       "org-py",
		Prompt:       insideIf,
		OutputTokens: 16,
	})
	if err != nil {
		t.Fatalf("Place py-1: %v", err)
	}
	_ = sched.Complete("org-py", dec1.RequestID)

	dec2, err := sched.Place(PlacementRequest{
		RequestID:    "py-2",
		Tenant:       "org-py",
		Prompt:       outsideIf,
		OutputTokens: 16,
	})
	if err != nil {
		t.Fatalf("Place py-2: %v", err)
	}
	// They differ in block 0 (at token 10, indentation before second()), so block 0 hash differs => 0 reuse!
	if dec2.MatchedTokens != 0 {
		t.Fatalf("expected 0 reused tokens between differently indented Python prompts, got %d/%d",
			dec2.MatchedTokens, dec2.PromptTokens)
	}

	// Additional whitespace pairs that must all yield distinct token streams.
	variants := []string{
		"abcd efgh",
		"abcdefgh",
		"abcd  efgh",
		"abcd\tefgh",
		"abcd\nefgh",
		"abcd\r\nefgh",
		" abcd efgh",
		"abcd efgh ",
	}
	seen := make(map[string]string, len(variants))
	for _, v := range variants {
		blks, err := HashTokenBlocks("org-ws", "", TokenizeText(v), 16)
		if err != nil {
			t.Fatalf("HashTokenBlocks(%q): %v", v, err)
		}
		key := fmt.Sprintf("%x", blks[len(blks)-1].Hash)
		if prev, dup := seen[key]; dup {
			t.Fatalf("whitespace collision between %q and %q", prev, v)
		}
		seen[key] = v
	}
}
