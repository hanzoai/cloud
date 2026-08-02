package risk

// bound_test.go is the regression suite for the two defects that held this app:
// a process-wide velocity store whose eviction crossed tenants, and a per-key
// cost of ~22 KB against no per-tenant ceiling at all.
//
// Each test was written by reintroducing the defect and checking that THIS test
// goes red.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestOneTenantCannotEvictAnothersAggregates is the load-bearing one.
//
// THE DEFECT: one velocity.Store for the whole process, MaxKeys 100,000 over 64
// shards, keys hashed across those shards without regard to tenant. Tenant A
// pushing distinct subjects evicted tenant B's counters, so B's velocity rules
// read zero, fired on nothing, and reported success. No error, no log, no alert.
//
// THE TEST: give a tenant the smallest bound the knob allows, drive A far past
// it — so A's own store DEMONSTRABLY evicts — and require that B's single key
// still reads every observation B made. With a shared store B's key is gone.
func TestOneTenantCannotEvictAnothersAggregates(t *testing.T) {
	t.Setenv(envVelBytes, "1048576") // 1 MiB: the floor, so the bound is reachable in a test
	_, s := wireApp(t)

	a, b := Tenant("hanzo/acme"), Tenant("hanzo/beta")
	bees := resOf(t, s, b)
	bvel, _, _ := bees.arms()

	// B records five observations on ONE subject.
	const bCount = 5
	for i := 0; i < bCount; i++ {
		bees.record(observation{
			at: time.Now(), kind: "account", subject: "b-account",
			amount: 1_000_000_000, signals: map[string]string{"ip": "198.51.100.7"},
		})
	}

	// A floods, well past its OWN bound.
	ay := resOf(t, s, a)
	avel, _, _ := ay.arms()
	flood := maxKeys() * 3
	for i := 0; i < flood; i++ {
		ay.record(observation{
			at: time.Now(), kind: "account", subject: fmt.Sprintf("a-account-%d", i),
			amount: 1_000_000_000,
		})
	}

	// A really did hit its own bound — otherwise this test proves nothing,
	// because nothing was ever under pressure. And it hit it AT the bound: the
	// gate refuses, the engine never evicts.
	if got := avel.keys(); got != maxKeys() {
		t.Fatalf("the flooding tenant holds %d keys against a bound of %d — the gate is not what bound it", got, maxKeys())
	}
	if !ay.strained() {
		t.Fatal("the flooding tenant is past its own bound and does not say so")
	}

	// B is untouched. This is the property.
	obs := bvel.observe(b, "account", "b-account")
	var got int
	for _, o := range obs {
		if o.Window == "24h" {
			got = o.Count
		}
	}
	if got != bCount {
		t.Fatalf("B's 24h count is %d, want %d — another tenant's volume evicted B's counters, and B's velocity rules now fire on nothing", got, bCount)
	}
	if avel == bvel {
		t.Fatal("two tenants share one velocity store — the eviction boundary is a hash, not a tenant")
	}
}

// TestTheWorstCaseIsArithmeticAndConservative pins the memory bound as something
// computed rather than hoped for, and proves the computation is an OVER-estimate
// by measuring the real thing.
//
// A ceiling that under-states is not a ceiling. The formula must be at least as
// large as the bytes a key actually costs, or the per-tenant budget buys more
// keys than it can hold.
func TestTheWorstCaseIsArithmeticAndConservative(t *testing.T) {
	// The formula is the buckets actually configured, not a constant left behind
	// by an earlier window set.
	buckets := 0
	for _, w := range windows() {
		buckets += w.Buckets
	}
	if want := buckets*bucketBytes + keyOverhead + 3*textMax; bytesPerKey() != want {
		t.Fatalf("bytesPerKey() = %d, want %d — the estimate has drifted from the windows and the text cap it is computed over", bytesPerKey(), want)
	}
	// And the budget really does bound the key count.
	if maxKeys()*bytesPerKey() > velBytes() {
		t.Fatalf("%d keys x %d B = %d B exceeds the %d B budget", maxKeys(), bytesPerKey(), maxKeys()*bytesPerKey(), velBytes())
	}

	// MEASURED. Fill one tenant's rings with distinct keys and weigh it. An
	// ORDINARY identifier here; the WORST case the door admits is measured by
	// TestThePublishedCeilingHoldsForTheLongestValueTheDoorAccepts, which is the
	// one that matters and the one this test used to be missing.
	tn := Tenant("hanzo/acme")
	n := maxKeys()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	vel := aggregates()
	at := time.Now()
	for i := 0; i < n; i++ {
		vel.record(tn, observation{at: at, kind: "account", subject: fmt.Sprintf("s-%d", i), amount: 1_000_000_000}, nil)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(vel)

	measured := int(after.HeapAlloc-before.HeapAlloc) / n
	if measured > bytesPerKey() {
		t.Fatalf("a key measures %d B against a published ceiling of %d B — the per-tenant budget buys more keys than it can hold", measured, bytesPerKey())
	}
	t.Logf("per key: measured %d B, published ceiling %d B; per tenant %d keys in %d B; node budget %d B",
		measured, bytesPerKey(), maxKeys(), velBytes(), memBytes())
}

// TestTheDefaultCeilingIsWhatIsDocumented pins the published numbers. The whole
// point of a computable worst case is that it is written down somewhere an
// operator reads, so a change to a default has to be a change to the document.
func TestTheDefaultCeilingIsWhatIsDocumented(t *testing.T) {
	for _, k := range []string{envVelBytes, envMemory, envIdle} {
		t.Setenv(k, "")
	}
	if got := velBytes(); got != 8<<20 {
		t.Errorf("default per-tenant aggregate budget = %d, want 8 MiB", got)
	}
	if got := memBytes(); got != 512<<20 {
		t.Errorf("default node budget = %d, want 512 MiB", got)
	}
	if got := idleReclaim(); got != 6*time.Hour {
		t.Errorf("default reclaim idleness = %s, want 6h", got)
	}
	// The per-tenant CEILING, stated, and the node budget it is charged against.
	// If either number moves, the comment at the top of bound.go must move with
	// it. Note what this is NOT: a count of tenants. A tenant is charged for what
	// it holds, so the node serves memBytes/cellBytes ordinary tenants and
	// memBytes/perTenant simultaneously at their ceiling.
	perTenant := cellBytes + velBytes() + governMemo
	if perTenant > 16<<20 {
		t.Fatalf("one tenant may hold %d B (%d MiB) — beyond what a shared pod may promise any single org", perTenant, perTenant>>20)
	}
	if memBytes() < 8*perTenant {
		t.Fatalf("the node budget (%d B) is under eight tenants at their ceiling (%d B) — a node that cannot hold a handful of busy orgs is not an operating point", memBytes(), perTenant)
	}
	t.Logf("per tenant ceiling %d B; node %d B = %d ordinary cells or %d at their ceiling",
		perTenant, memBytes(), memBytes()/cellBytes, memBytes()/perTenant)
}

// TestARetirementCannotRaceAWorker pins the invariant that lets a retire CLOSE a
// tenant's file without a reference count on every op.
//
// Retirement is safe only because a cell reaches it having served no request for
// idleReclaim, and nothing in this process can hold a tenant's handle that long:
// the longest is a search worker, bounded by searchBudget. If someone lowers the
// floor under that budget — or raises the budget over the floor — the close
// starts racing a live worker, and the failure is a write error on a durable
// report rather than anything this suite would otherwise notice.
func TestARetirementCannotRaceAWorker(t *testing.T) {
	if idleFloor <= searchBudget {
		t.Fatalf("the retire floor (%s) is not above the longest a worker holds a tenant's file (%s) — closing it races that worker", idleFloor, searchBudget)
	}
	// And the floor really is a floor: no environment value can go under it.
	for _, v := range []string{"60", "1", "0", "-5", "600"} {
		t.Setenv(envIdle, v)
		if got := idleReclaim(); got < idleFloor {
			t.Fatalf("%s=%s yields %s, under the %s floor", envIdle, v, got, idleFloor)
		}
	}
}

// TestOnlyOneConstructorIsBounded is the structural half: it is not enough that
// today's call sites are bounded, it must be hard to add an unbounded one.
//
// velocity.New with a zero Config is a 100,000-key store, and anomaly.New with a
// zero Config holds 256 tenants under a global LRU. Both are exactly the shape
// the defect had. This asserts that neither is spelled anywhere in the package
// except inside the two constructors that force the bound.
func TestOnlyOneConstructorIsBounded(t *testing.T) {
	for _, call := range []struct{ pkg, fn, allowedIn string }{
		{"velocity", "New", "bound.go"},
		{"anomaly", "New", "bound.go"},
	} {
		for file, n := range callsIn(t, call.pkg, call.fn) {
			if file != call.allowedIn {
				t.Errorf("%s calls %s.%s %d time(s); the only bounded constructor is in %s — an unbounded store shared across tenants is the defect this package was held for",
					file, call.pkg, call.fn, n, call.allowedIn)
			}
		}
	}
}

// callsIn counts <pkg>.<fn>( per file across the package's non-test sources.
func callsIn(t *testing.T, pkg, fn string) map[string]int {
	t.Helper()
	out := map[string]int{}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, p := range pkgs {
		for name, f := range p.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != fn {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || id.Name != pkg {
					return true
				}
				out[filepath.Base(name)]++
				return true
			})
		}
	}
	return out
}

// TestAStrainedTenantSaysSo pins the loud half of the cardinality bound. A
// tenant that has hit its own bound is reading partial rings, and a partial ring
// under-counts, which is the failure mode that reads as a clean result.
func TestAStrainedTenantSaysSo(t *testing.T) {
	t.Setenv(envVelBytes, "1048576")
	app, s := wireApp(t)

	// Not strained to begin with, or the assertion below proves nothing.
	code, body := req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"signup","subject":{"kind":"account","id":"first"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	var first riskDecision
	_ = json.Unmarshal(body, &first)
	if first.Strained {
		t.Fatal("a fresh tenant reports its aggregates as strained")
	}
	if first.Since == "" {
		t.Fatal("a decision does not publish the period its aggregates cover — a 30-day count over ten minutes of rings reads as a 30-day fact")
	}

	// Fill this tenant's own store past its own bound.
	acme := resOf(t, s, Tenant("hanzo/acme"))
	for i := 0; i < maxKeys()*2; i++ {
		acme.record(observation{
			at: time.Now(), kind: "account", subject: fmt.Sprintf("filler-%d", i), amount: 1,
		})
	}

	code, body = req(t, app, http.MethodPost, "/v1/risk/decide", "acme", "u_acme",
		`{"stage":"signup","subject":{"kind":"account","id":"later"}}`)
	if code != http.StatusOK {
		t.Fatalf("decide = %d %s", code, body)
	}
	var later riskDecision
	_ = json.Unmarshal(body, &later)
	if !later.Strained {
		t.Fatal("a tenant at its own cardinality bound does not say so, so an under-counted velocity reads as a clean one")
	}
}
