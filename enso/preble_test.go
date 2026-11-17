package enso

import (
	"fmt"
	"testing"
)

func TestPrebleVsLeastLoaded(t *testing.T) {
	// Setup Replicas
	r1 := &Replica{ID: "gpu-1", MaxCapacity: 10, CachedPrefixes: make(map[string]bool)}
	r2 := &Replica{ID: "gpu-2", MaxCapacity: 10, CachedPrefixes: make(map[string]bool)}
	
	scheduler := NewPrebleScheduler()
	scheduler.AddReplica(r1)
	scheduler.AddReplica(r2)

	tenantA := "tenant-1"
	sharedPrefix := "system-prompt-v1"

	// Phase 1: Cold Start - Route to Least Loaded
	rep := scheduler.SelectReplica(tenantA, sharedPrefix)
	if rep == nil {
		t.Fatal("Failed to select replica")
	}
	
	// Simulate completion and caching on r1
	cacheKey := scheduler.getCacheKey(tenantA, sharedPrefix)
	scheduler.RecordCompletion(rep.ID, cacheKey)

	// Phase 2: Cache-Aware Routing
	// Request 2 with same prefix should hit the same replica even if loads are equal
	rep2 := scheduler.SelectReplica(tenantA, sharedPrefix)
	if rep2.ID != rep.ID {
		t.Errorf("Expected cache locality hit on %s, got %s", rep.ID, rep2.ID)
	}

	fmt.Printf("Locality Test Passed: Request routed to %s based on prefix cache.\n", rep2.ID)
}

func BenchmarkSchedulerSkew(b *testing.B) {
	scheduler := NewPrebleScheduler()
	for i := 0; i < 5; i++ {
		scheduler.AddReplica(&Replica{ID: fmt.Sprintf("gpu-%d", i), MaxCapacity: 100})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tenant := fmt.Sprintf("t-%d", i%2) // 2 tenants (high skew)
		prefix := "common-prefix"
		rep := scheduler.SelectReplica(tenant, prefix)
		if rep != nil {
			scheduler.RecordCompletion(rep.ID, scheduler.getCacheKey(tenant, prefix))
		}
	}
}
