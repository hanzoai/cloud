package gojabase

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
)

// TestTenantSegmentInjective is the C1 proof: the tenant→filename mapping is
// INJECTIVE (distinct orgs never share a file) and TRAVERSAL-SAFE (the segment is
// always a single [a-z2-7] path component that can never escape the data subtree).
// The previous slugify ToLower+folded — collapsing "Acme"/"acme" and "a b"/"a_b"
// onto shared SQLite files, a cross-tenant break. This must never regress.
func TestTenantSegmentInjective(t *testing.T) {
	// An adversarial set that specifically includes the collision pairs the old
	// slugify folded together, plus path-traversal payloads.
	inputs := []string{
		"Acme", "acme", "ACME", "aCmE",
		"a b", "a_b", "a-b", "a.b", "a/b",
		"../evil", "..", ".", "...", "/", "//", "\\",
		"a", "A", "1", "0",
		"tenant.with.dots", "tenant/with/slash", "space at end ",
		" space at start", "Œcafé", "café", "CAFÉ",
		"x", "xx", "xxx", "xxxx", "xxxxx", // base32 group boundaries (1..5 bytes)
	}

	// (1) INJECTIVITY: no two distinct inputs map to the same segment.
	segToInput := map[string]string{}
	for _, in := range inputs {
		seg := TenantSegment(in)
		if in == "" {
			continue
		}
		if prev, clash := segToInput[seg]; clash {
			t.Fatalf("NON-INJECTIVE: %q and %q both encode to %q (cross-tenant collision)", prev, in, seg)
		}
		segToInput[seg] = in
	}
	// Spell out the headline pairs so a regression names the exact break.
	for _, pair := range [][2]string{{"Acme", "acme"}, {"a b", "a_b"}, {"a.b", "a-b"}, {"ACME", "acme"}} {
		if TenantSegment(pair[0]) == TenantSegment(pair[1]) {
			t.Fatalf("case/separator-variant orgs collapsed: %q == %q", pair[0], pair[1])
		}
	}

	// (2) TRAVERSAL SAFETY: every non-empty segment is a single [a-z2-7] component
	// — no path separator, never "."/".." — so filepath.Join can't escape the dir.
	alphabet := regexp.MustCompile(`^[a-z2-7]+$`)
	const dataDir = "/var/lib/cloud/sign"
	for _, in := range inputs {
		seg := TenantSegment(in)
		if in == "" {
			if seg != "" {
				t.Fatalf("empty tenant must encode to empty, got %q", seg)
			}
			continue
		}
		if !alphabet.MatchString(seg) {
			t.Fatalf("segment %q for input %q has non-[a-z2-7] chars (path-unsafe)", seg, in)
		}
		if seg == "." || seg == ".." {
			t.Fatalf("segment for %q is %q (path-traversal)", in, seg)
		}
		// The joined path must stay physically under dataDir.
		joined := filepath.Join(dataDir, seg+".db")
		if filepath.Dir(joined) != dataDir {
			t.Fatalf("input %q escaped data dir: %q", in, joined)
		}
	}
}

// TestLRUEvictionReopen proves M4: the per-tenant DB pool is bounded (never grows
// past the cap) and an evicted tenant's data SURVIVES eviction (reopen is lossless).
func TestLRUEvictionReopen(t *testing.T) {
	t.Setenv("CLOUD_GOJABASE_MAX_DBS", "2")
	t.Setenv("CLOUD_GOJABASE_IDLE_TTL_SEC", "0") // disable idle sweep; test the cap alone
	h := newTinyHost(t, nil)
	ctx := context.Background()

	orgs := []string{"one", "two", "three", "four", "five"}
	for _, org := range orgs {
		resp, err := h.Dispatch(ctx, org, Request{Route: "put", Body: map[string]any{"k": "name", "v": org + "-val"}})
		if err != nil {
			t.Fatalf("put %s: %v", org, err)
		}
		if resp.Status != 201 {
			t.Fatalf("put %s status=%d", org, resp.Status)
		}
		// Pool never exceeds the cap (writes are sequential; nothing is pinned here).
		h.stores.mu.Lock()
		open := len(h.stores.m)
		h.stores.mu.Unlock()
		if open > 2 {
			t.Fatalf("pool grew past cap: %d open after %s", open, org)
		}
	}
	// Every tenant — including the ones long since evicted — still reads back its
	// own committed row (data persisted to disk; reopen re-migrated + re-read).
	for _, org := range orgs {
		resp, err := h.Dispatch(ctx, org, Request{Route: "get", Params: map[string]string{"k": "name"}})
		if err != nil {
			t.Fatalf("get %s: %v", org, err)
		}
		if resp.Status != 200 {
			t.Fatalf("evicted tenant %s lost its data: status=%d", org, resp.Status)
		}
		var row struct{ V string }
		if err := json.Unmarshal(resp.Body, &row); err != nil {
			t.Fatalf("decode %s: %v (%s)", org, err, resp.Body)
		}
		if row.V != org+"-val" {
			t.Fatalf("tenant %s read wrong value %q", org, row.V)
		}
	}
}

// TestConcurrentMultiTenantDispatch is the M10 -race proof: many goroutines
// dispatch concurrently across many tenants (INCLUDING case/separator-variant orgs
// that MUST stay isolated) under a tiny pool cap that forces constant eviction —
// exercising the pool, the per-tenant DB cache, pin/release, and eviction together.
// Run with `go test -race`. It asserts race-freedom AND per-tenant data integrity:
// every write survives (eviction never yanks a live DB), and no tenant sees
// another's rows (injective isolation holds under concurrency).
func TestConcurrentMultiTenantDispatch(t *testing.T) {
	t.Setenv("CLOUD_GOJABASE_MAX_DBS", "3") // < number of tenants → heavy eviction churn
	h := newTinyHost(t, nil)
	ctx := context.Background()

	// Distinct tenants, deliberately including collision-prone pairs the old
	// slugify folded ("Acme"/"acme", "a b"/"a_b") so isolation is proven, not argued.
	tenants := []string{
		"Acme", "acme", "a b", "a_b", "globex", "GLOBEX",
		"initech", "umbrella", "stark", "wayne", "wonka", "hooli",
	}
	const perTenant = 25

	var wg sync.WaitGroup
	errCh := make(chan error, len(tenants)*perTenant)
	for _, org := range tenants {
		for i := 0; i < perTenant; i++ {
			wg.Add(1)
			go func(org string, i int) {
				defer wg.Done()
				k := "k" + strconv.Itoa(i)
				resp, err := h.Dispatch(ctx, org, Request{
					Route: "put",
					Body:  map[string]any{"k": k, "v": org + "|" + k},
				})
				if err != nil {
					errCh <- fmt.Errorf("put %s/%s: %w", org, k, err)
					return
				}
				if resp.Status != 201 {
					errCh <- fmt.Errorf("put %s/%s status=%d", org, k, resp.Status)
				}
			}(org, i)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	// Integrity: each tenant holds EXACTLY its own perTenant rows, values intact,
	// none leaked from a sibling (esp. the case/separator-variant pairs).
	for _, org := range tenants {
		resp, err := h.Dispatch(ctx, org, Request{Route: "list"})
		if err != nil {
			t.Fatalf("list %s: %v", org, err)
		}
		if resp.Status != 200 {
			t.Fatalf("list %s status=%d", org, resp.Status)
		}
		var rows []struct{ K, V string }
		if err := json.Unmarshal(resp.Body, &rows); err != nil {
			t.Fatalf("decode list %s: %v (%s)", org, err, resp.Body)
		}
		if len(rows) != perTenant {
			t.Fatalf("tenant %q has %d rows, want %d (cross-tenant leak or lost write)", org, len(rows), perTenant)
		}
		for _, r := range rows {
			if want := org + "|" + r.K; r.V != want {
				t.Fatalf("tenant %q row %q value %q, want %q", org, r.K, r.V, want)
			}
		}
	}
}
