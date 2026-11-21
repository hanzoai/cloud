// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package preblelocality

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPrefixCacheSaltIsolation(t *testing.T) {
	c := NewPrefixCache(1000)
	a := CacheKey{Tenant: "tenant-a", Session: "s1", Prefix: 42}
	b := CacheKey{Tenant: "tenant-b", Session: "s1", Prefix: 42} // same session+hash, different salt
	c.Publish(a, 128)

	if got, ok := c.Lookup(b); ok {
		t.Fatalf("cross-tenant lookup must miss (HIP-1345 §1.1), got %d tokens", got)
	}
	if got, ok := c.Lookup(a); !ok || got != 128 {
		t.Fatalf("same-tenant lookup must hit with 128 tokens, got %d ok=%v", got, ok)
	}
}

func TestPrefixCacheSaltIsolationEvenSamePrefixHash(t *testing.T) {
	c := NewPrefixCache(1000)
	// Two tenants with byte-identical prompts still never share entries.
	a := CacheKey{Tenant: "a", Session: "s", Prefix: 7}
	b := CacheKey{Tenant: "b", Session: "s", Prefix: 7}
	c.Publish(a, 64)
	c.Publish(b, 64)
	if c.Len() != 2 {
		t.Fatalf("expected 2 distinct salted entries, got %d", c.Len())
	}
}

func TestPrefixCacheLRUEviction(t *testing.T) {
	c := NewPrefixCache(10)
	k1 := CacheKey{Tenant: "t", Session: "s", Prefix: 1}
	k2 := CacheKey{Tenant: "t", Session: "s", Prefix: 2}
	k3 := CacheKey{Tenant: "t", Session: "s", Prefix: 3}
	c.Publish(k1, 4)
	c.Publish(k2, 4)
	c.Lookup(k1)     // promote k1; k2 is now LRU
	c.Publish(k3, 4) // must evict k2 (budget 10, k1+k3 = 8)
	if _, ok := c.Peek(k2); ok {
		t.Fatal("k2 (LRU) should have been evicted")
	}
	if _, ok := c.Peek(k1); !ok {
		t.Fatal("k1 (recently used) must survive")
	}
	if _, ok := c.Peek(k3); !ok {
		t.Fatal("k3 must be resident")
	}
	if c.Evictions != 1 {
		t.Fatalf("expected exactly 1 eviction, got %d", c.Evictions)
	}
	if c.Used() > 10 {
		t.Fatalf("token budget exceeded: %d > 10", c.Used())
	}
}

func TestPrefixCacheRefusesOversizeAndEmpty(t *testing.T) {
	c := NewPrefixCache(10)
	empty := CacheKey{Tenant: "t", Session: "s", Prefix: 1}
	huge := CacheKey{Tenant: "t", Session: "s", Prefix: 2}
	c.Publish(empty, 0) // fail-closed: never publish empty blocks
	if c.Len() != 0 {
		t.Fatalf("empty publish must be ignored, len=%d", c.Len())
	}
	c.Publish(huge, 11) // exceeds whole budget: refuse, never thrash
	if c.Len() != 0 || c.Refused != 1 {
		t.Fatalf("oversize publish must be refused, len=%d refused=%d", c.Len(), c.Refused)
	}
}

func TestPrefixCacheConcurrent(t *testing.T) {
	c := NewPrefixCache(4096)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				key := CacheKey{Tenant: fmt.Sprintf("t%d", g), Session: "s", Prefix: uint64(i % 100)}
				c.Publish(key, 32)
				c.Lookup(key)
				c.Peek(key)
			}
		}(g)
	}
	wg.Wait()
	if c.Used() > 4096 {
		t.Fatalf("token budget exceeded under concurrency: %d", c.Used())
	}
}

func TestLeastLoadedRespectsSlotCap(t *testing.T) {
	replicas := []*Replica{
		NewReplica(0, 1, 100, 20000, 100, 2.5),
		NewReplica(1, 1, 100, 20000, 100, 2.5),
	}
	cs := &ClusterState{Replicas: replicas}
	replicas[0].active = 1
	replicas[1].active = 1
	req := &Request{Tenant: "t", Session: "s", PrefixLen: 8, SuffixLen: 4}
	if got := (LeastLoadedPolicy{}).Select(req, cs); got != -1 {
		t.Fatalf("all replicas at cap must return -1, got %d", got)
	}
}

func TestLeastLoadedTieBreakLowestID(t *testing.T) {
	replicas := []*Replica{
		NewReplica(0, 4, 100, 20000, 100, 2.5),
		NewReplica(1, 4, 100, 20000, 100, 2.5),
		NewReplica(2, 4, 100, 20000, 100, 2.5),
	}
	cs := &ClusterState{Replicas: replicas}
	replicas[0].active = 2
	replicas[1].active = 1
	replicas[2].active = 1
	req := &Request{Tenant: "t", Session: "s"}
	if got := (LeastLoadedPolicy{}).Select(req, cs); got != 1 {
		t.Fatalf("tie between replicas 1 and 2 must pick lowest ID (1), got %d", got)
	}
}

func TestCacheAwarePrefersResidency(t *testing.T) {
	replicas := []*Replica{
		NewReplica(0, 4, 100000, 20000, 100, 2.5),
		NewReplica(1, 4, 100000, 20000, 100, 2.5),
	}
	cs := &ClusterState{Replicas: replicas}
	req := &Request{ID: 1, Tenant: "t", Session: "s", PrefixLen: 4096, SuffixLen: 256, PrefixHash: 99}
	replicas[1].Cache.Publish(CacheKeyOf(req), req.PrefixLen)
	replicas[0].MarkWarm(req.Tenant)
	replicas[1].MarkWarm(req.Tenant)
	if got := (NewCacheAwarePolicy(1.0)).Select(req, cs); got != 1 {
		t.Fatalf("cache-aware must route to the replica holding the resident prefix, got %d", got)
	}
}

func TestCacheAwareNeverOverloadsCap(t *testing.T) {
	replicas := []*Replica{
		NewReplica(0, 2, 100000, 20000, 100, 2.5),
		NewReplica(1, 2, 100000, 20000, 100, 2.5),
	}
	cs := &ClusterState{Replicas: replicas}
	replicas[0].active = 2
	replicas[1].active = 2
	req := &Request{Tenant: "t", Session: "s"}
	if got := (NewCacheAwarePolicy(1.0)).Select(req, cs); got != -1 {
		t.Fatalf("cache-aware must return -1 at cap, got %d", got)
	}
}

func TestCacheAwareAvoidsColdReplica(t *testing.T) {
	replicas := []*Replica{
		NewReplica(0, 4, 100000, 20000, 100, 2.5), // cold for tenant
		NewReplica(1, 4, 100000, 20000, 100, 2.5), // warm for tenant, no residency
	}
	cs := &ClusterState{Replicas: replicas}
	replicas[1].MarkWarm("t")
	req := &Request{Tenant: "t", Session: "s"}
	if got := (NewCacheAwarePolicy(1.0)).Select(req, cs); got != 1 {
		t.Fatalf("cache-aware must avoid the cold replica when scores are otherwise equal, got %d", got)
	}
}

func TestSimCompletesAllRequestsAndRespectsSlots(t *testing.T) {
	for _, policy := range Policies(MatrixPolicyWeight) {
		cfg := ScenarioPresets()[0]
		m := RunSim(cfg, policy)
		if m.RequestsDropped != 0 {
			t.Fatalf("%s: dropped %d requests", policy.Name(), m.RequestsDropped)
		}
		if m.RequestsDone != m.RequestsArrived {
			t.Fatalf("%s: done=%d arrived=%d", policy.Name(), m.RequestsDone, m.RequestsArrived)
		}
		for i, ma := range m.MaxActiveByReplica {
			if ma > cfg.Slots {
				t.Fatalf("%s: replica %d exceeded slot cap: %d > %d", policy.Name(), i, ma, cfg.Slots)
			}
		}
	}
}

func TestSimDeterminism(t *testing.T) {
	cfg := ScenarioPresets()[1] // zipf scenario
	m1 := RunSim(cfg, NewCacheAwarePolicy(MatrixPolicyWeight))
	m2 := RunSim(cfg, NewCacheAwarePolicy(MatrixPolicyWeight))
	a, _ := json.Marshal(m1)
	b, _ := json.Marshal(m2)
	if string(a) != string(b) {
		t.Fatalf("same seed must reproduce identical metrics:\n%s\n%s", a, b)
	}
}

func TestSimMetricsInvariants(t *testing.T) {
	for _, cfg := range ScenarioPresets() {
		for _, policy := range Policies(MatrixPolicyWeight) {
			m := RunSim(cfg, policy)
			label := cfg.Name + "/" + policy.Name()
			if m.ReuseRatio < 0 || m.ReuseRatio > 1 {
				t.Fatalf("%s: reuse ratio out of range: %f", label, m.ReuseRatio)
			}
			if m.GPUUtil <= 0 || m.GPUUtil > 1 {
				t.Fatalf("%s: gpu util out of range: %f", label, m.GPUUtil)
			}
			if m.TTFTP99 < m.TTFTP50 {
				t.Fatalf("%s: p99 < p50", label)
			}
			if m.TTFTMax < m.TTFTP99 {
				t.Fatalf("%s: max < p99", label)
			}
			if m.GoodputTPS <= 0 {
				t.Fatalf("%s: non-positive goodput", label)
			}
		}
	}
}

func TestSimZipfSkew(t *testing.T) {
	cfg := ScenarioPresets()[1]
	m := RunSim(cfg, LeastLoadedPolicy{})
	total := 0
	maxPerTenant := 0
	for _, n := range m.TenantRequests {
		total += n
		if n > maxPerTenant {
			maxPerTenant = n
		}
	}
	if total == 0 {
		t.Fatal("no requests recorded")
	}
	uniformShare := float64(total) / float64(cfg.Tenants)
	if float64(maxPerTenant) < 10*uniformShare {
		t.Fatalf("zipf skew not visible: max tenant %d vs uniform share %.1f", maxPerTenant, uniformShare)
	}
}

func TestScenarioMatrixCompletes(t *testing.T) {
	results := RunMatrix()
	if len(results) != len(ScenarioPresets())*2*len(MatrixSeeds) {
		t.Fatalf("unexpected matrix size %d", len(results))
	}
	for _, m := range results {
		if m.RequestsDropped != 0 {
			t.Fatalf("%s/%s seed %d: dropped %d", m.Scenario, m.Policy, m.Seed, m.RequestsDropped)
		}
	}
}

// TestHypothesisChecks encodes the falsifiable hypotheses of cloud#35 as
// regression guards over the deterministic matrix. Each check reflects the
// measured outcome recorded in README §6; if a future change to the model
// flips a verdict, these tests fail and the README must be updated.
func TestHypothesisChecks(t *testing.T) {
	type key struct{ scen, policy string }
	agg := map[key][]Metrics{}
	for _, m := range RunMatrix() {
		agg[key{m.Scenario, m.Policy}] = append(agg[key{m.Scenario, m.Policy}], m)
	}
	mean := func(ms []Metrics, f func(Metrics) float64) float64 {
		var s float64
		for _, m := range ms {
			s += f(m)
		}
		return s / float64(len(ms))
	}
	p99 := func(k key) float64 { return mean(agg[k], func(m Metrics) float64 { return m.TTFTP99 }) }
	p50 := func(k key) float64 { return mean(agg[k], func(m Metrics) float64 { return m.TTFTP50 }) }
	reuse := func(k key) float64 { return mean(agg[k], func(m Metrics) float64 { return m.ReuseRatio }) }

	// H1: zipf skew — reuse multiplier ≥ 1.5×; p50 improves ≥ 20%
	// (measured: reuse 5.0x, p50 −29%; p99 is set by the max-prefix
	// prefill ceiling in this scenario, see README §6).
	if rLL, rCA := reuse(key{"tenant_skew_zipf", "least-loaded"}), reuse(key{"tenant_skew_zipf", "cache-aware"}); rCA < 1.5*rLL {
		t.Fatalf("H1 reuse: cache-aware %.3f < 1.5 x least-loaded %.3f", rCA, rLL)
	}
	if pLL, pCA := p50(key{"tenant_skew_zipf", "least-loaded"}), p50(key{"tenant_skew_zipf", "cache-aware"}); pCA > 0.8*pLL {
		t.Fatalf("H1 p50: cache-aware %.2f did not improve ≥20%% over %.2f", pCA, pLL)
	}

	// H2: saturation — graceful degradation, no hot-spotting collapse
	// (measured: p99 ratio 0.96, utilization equal).
	if r := p99(key{"saturation_high", "cache-aware"}) / p99(key{"saturation_high", "least-loaded"}); r > 1.05 {
		t.Fatalf("H2: saturation p99 ratio %.2f exceeds graceful bound 1.05", r)
	}

	// H3: zero reuse — parity within 5% (measured: ≤0.3%).
	if d := p99(key{"zero_reuse_negative", "cache-aware"}) / p99(key{"zero_reuse_negative", "least-loaded"}); d > 1.05 || d < 0.95 {
		t.Fatalf("H3: zero-reuse p99 ratio %.3f outside parity band", d)
	}

	// H4: eviction pressure — cache-aware retains ≥ least-loaded p99
	// (measured: equal; no thrash penalty) and reuse ≥ 3×
	// (measured: 5.7x).
	if r := p99(key{"eviction_pressure", "cache-aware"}) / p99(key{"eviction_pressure", "least-loaded"}); r > 1.05 {
		t.Fatalf("H4: eviction p99 ratio %.2f shows thrash penalty", r)
	}
	if rLL, rCA := reuse(key{"eviction_pressure", "least-loaded"}), reuse(key{"eviction_pressure", "cache-aware"}); rCA < 3*rLL {
		t.Fatalf("H4 reuse: cache-aware %.3f < 3 x least-loaded %.3f", rCA, rLL)
	}

	// H5: cold-start burst — residency-aware placement cuts p99 ≥ 30%
	// and cold starts ≥ 40% (measured: p99 −52%, cold −58%).
	if r := p99(key{"cold_start_burst", "cache-aware"}) / p99(key{"cold_start_burst", "least-loaded"}); r > 0.70 {
		t.Fatalf("H5: burst p99 ratio %.2f above reduction bound 0.70", r)
	}
	coldLeast := 0.0
	coldCache := 0.0
	for _, m := range agg[key{"cold_start_burst", "least-loaded"}] {
		coldLeast += float64(m.ColdStarts)
	}
	for _, m := range agg[key{"cold_start_burst", "cache-aware"}] {
		coldCache += float64(m.ColdStarts)
	}
	if coldCache > 0.6*coldLeast {
		t.Fatalf("H5: burst cold starts %.0f not ≥40%% below %.0f", coldCache, coldLeast)
	}
}

// TestWriteMatrixResults regenerates the committed raw results. It runs the
// full matrix (36 deterministic runs) and rewrites testdata/results.json so
// that a reviewer can reproduce the committed bytes with:
//
//	go test ./research/preblelocality/ -run TestWriteMatrixResults
func TestWriteMatrixResults(t *testing.T) {
	b, err := MatrixJSON(RunMatrix())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "results.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
