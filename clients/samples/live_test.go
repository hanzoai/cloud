package samples

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/datastore"
)

// live_test.go proves the plane against a REAL datastore: the DDL actually parses
// and creates, the binds actually match the column types, the materialized view
// actually feeds its target, and the reads actually return what was written.
// Nothing else can prove those — a mock only ever re-asserts our own assumptions.
//
// It is OPT-IN (skipped unless DATASTORE_ADDR names one) so CI, which has no
// warehouse, stays green:
//
//	docker run -d --name ch -p 19000:9000 clickhouse/clickhouse-server:latest
//	DATASTORE_ADDR=127.0.0.1:19000 go test ./clients/samples -run TestLive -v
//
// It WRITES, so it refuses any non-loopback address: pointing it at a real
// warehouse must never inject test rows into a tenant's series.

func liveDatastore(t *testing.T) {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv("DATASTORE_ADDR"))
	if addr == "" {
		t.Skip("no DATASTORE_ADDR — set it to a LOCAL datastore to prove the DDL end to end")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("DATASTORE_ADDR %q is not host:port: %v", addr, err)
	}
	// Fail closed: this test inserts, so it only ever runs against loopback.
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		t.Fatalf("refusing to run a WRITING test against non-loopback %q", addr)
	}
	// Dials in the background and self-provisions the `hanzo` database; Wait
	// blocks until the connection latches.
	ready, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := datastore.Wait(ready); err != nil {
		t.Fatalf("datastore at %s never became ready: %v", addr, err)
	}
}

// TestLivePlaneRoundTrip is the whole contract against a real engine: the schema
// creates, a sample records, and both reads return it — org-scoped.
func TestLivePlaneRoundTrip(t *testing.T) {
	liveDatastore(t)
	ctx := context.Background()
	org := fmt.Sprintf("live-%d", time.Now().UnixNano())

	in := Sample{
		Org: org, Source: SourceAgent, Unit: "tgt-live", Host: "box.local",
		Kind: KindGPU, At: time.Now().UTC().Add(-time.Minute),
		CPUs: 20, Memory: 128 << 30, MemUsed: 64 << 30, MemFree: 64 << 30,
		Load1: 2.5, Load5: 2, Load15: 1.5, GPUUtil: 0.75,
		GPUs: 1, GPUModel: "GB10", CostCents: 0,
	}
	if err := Record(ctx, in); err != nil {
		t.Fatalf("Record against a live datastore: %v", err)
	}

	got, err := Series(ctx, Query{Org: org, Range: "1h"})
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 sample back, got %d", len(got))
	}
	s := got[0]
	if s.Org != org || s.Unit != "tgt-live" || s.Kind != KindGPU || s.Source != SourceAgent {
		t.Fatalf("identity did not round-trip: %+v", s)
	}
	if s.CPUs != 20 || s.GPUs != 1 || s.GPUModel != "GB10" || s.Memory != 128<<30 {
		t.Fatalf("spec did not round-trip: %+v", s)
	}
	if s.GPUUtil < 0.74 || s.GPUUtil > 0.76 || s.Load1 < 2.49 || s.Load1 > 2.51 {
		t.Fatalf("metrics did not round-trip through Float32: %+v", s)
	}
	if s.At.IsZero() || time.Since(s.At) > 2*time.Hour {
		t.Fatalf("ts did not round-trip: %v", s.At)
	}

	latest, err := Latest(ctx, org)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if _, ok := latest["tgt-live"]; !ok {
		t.Fatalf("the board overlay must carry the unit, got %v", latest)
	}
}

// The tenancy boundary against a REAL engine, not just a built statement: org B
// reading with A's unit id gets nothing.
func TestLiveCrossTenantIsolation(t *testing.T) {
	liveDatastore(t)
	ctx := context.Background()
	a := fmt.Sprintf("live-a-%d", time.Now().UnixNano())
	b := fmt.Sprintf("live-b-%d", time.Now().UnixNano())

	secret := Sample{Org: a, Source: SourceAgent, Unit: "tgt-secret", Kind: KindGPU,
		At: time.Now().UTC(), GPUUtil: 0.99, Load1: 9}
	if err := Record(ctx, secret); err != nil {
		t.Fatalf("Record A: %v", err)
	}

	// B asks for A's exact unit id.
	got, err := Series(ctx, Query{Org: b, Unit: "tgt-secret", Range: "1h"})
	if err != nil {
		t.Fatalf("Series B: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("CROSS-TENANT LEAK: org %s read %d of org %s's samples: %+v", b, len(got), a, got)
	}
	board, err := Latest(ctx, b)
	if err != nil {
		t.Fatalf("Latest B: %v", err)
	}
	if len(board) != 0 {
		t.Fatalf("CROSS-TENANT LEAK: org %s's board carried %v", b, board)
	}
	// A still sees its own.
	own, err := Series(ctx, Query{Org: a, Unit: "tgt-secret", Range: "1h"})
	if err != nil || len(own) != 1 {
		t.Fatalf("org A must read its own sample, got %d / %v", len(own), err)
	}
}

// A hostile sample must be storable-or-rejected, never a corrupt row: the clamps
// are what let the binds meet UInt16/UInt8/Float32 without the engine erroring.
func TestLiveHostileSampleIsClampedNotRejectedByEngine(t *testing.T) {
	liveDatastore(t)
	ctx := context.Background()
	org := fmt.Sprintf("live-hostile-%d", time.Now().UnixNano())

	err := Record(ctx, Sample{
		Org: org, Source: SourceAgent, Unit: "tgt-hostile", Kind: KindLaptop,
		At: time.Now().UTC(), CPUs: 1 << 20, GPUs: 99999,
		Load1: 1e300, Load5: -1, GPUUtil: 42, Memory: -1,
		Host: strings.Repeat("h", 500), GPUModel: strings.Repeat("m", 500),
	})
	if err != nil {
		t.Fatalf("a clamped hostile sample must still be a legal row: %v", err)
	}
	got, err := Series(ctx, Query{Org: org, Range: "1h"})
	if err != nil || len(got) != 1 {
		t.Fatalf("want the clamped row back, got %d / %v", len(got), err)
	}
	s := got[0]
	if s.CPUs != maxCPUs || s.GPUs != maxGPUs || s.GPUUtil != 1 || s.Load1 != maxLoad {
		t.Fatalf("the engine stored an unclamped value: %+v", s)
	}
	if s.Load5 != 0 || s.Memory != 0 {
		t.Fatalf("negatives must have floored to 0: %+v", s)
	}
}

// An untenanted row must never reach a real table, even when the datastore IS
// connected — this is the fail-closed half of Record that the no-datastore unit
// test cannot reach.
func TestLiveRecordFailsClosedOnBadTenant(t *testing.T) {
	liveDatastore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		s    Sample
		want error
	}{
		{"blank org", Sample{Source: SourceAgent, Unit: "u", Kind: KindGPU}, errOrg},
		{"blank unit", Sample{Org: "acme", Source: SourceAgent, Kind: KindGPU}, errUnit},
		{"unknown source", Sample{Org: "acme", Source: "evil", Unit: "u", Kind: KindGPU}, errSource},
		{"unknown kind", Sample{Org: "acme", Source: SourceAgent, Unit: "u", Kind: "evil"}, errKind},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Record(ctx, tc.s); err != tc.want {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// The hourly rollup is fed by the view on insert and is itself org-scoped — the
// trend plane must not become a cross-tenant back door around the raw table.
func TestLiveRollupIsFedAndOrgScoped(t *testing.T) {
	liveDatastore(t)
	ctx := context.Background()
	org := fmt.Sprintf("live-mv-%d", time.Now().UnixNano())

	at := time.Now().UTC()
	for i, util := range []float64{0.2, 0.9, 0.5} {
		if err := Record(ctx, Sample{
			Org: org, Source: SourceAgent, Unit: "tgt-mv", Kind: KindGPU,
			At: at.Add(time.Duration(i) * time.Second), GPUUtil: util, Load1: 1, MemUsed: 1 << 30,
		}); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	rows, err := datastore.Query(ctx,
		`SELECT unit, maxMerge(gpu_util_max) AS mx, avgMerge(gpu_util_avg) AS av
		   FROM hanzo.compute_samples_hourly WHERE org = ? GROUP BY unit`, org)
	if err != nil {
		t.Fatalf("the rollup must be queryable: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the view must have fed exactly one unit's rollup, got %d rows", len(rows))
	}
	if got := f64(rows[0]["mx"]); got < 0.89 || got > 0.91 {
		t.Fatalf("gpu_util_max want ~0.9, got %v", got)
	}
	if got := f64(rows[0]["av"]); got < 0.52 || got > 0.55 {
		t.Fatalf("gpu_util_avg want ~0.533, got %v", got)
	}

	// Another tenant's rollup read finds nothing of this org's.
	other, err := datastore.Query(ctx,
		`SELECT count() AS n FROM hanzo.compute_samples_hourly WHERE org = ?`, org+"-other")
	if err != nil {
		t.Fatalf("rollup read: %v", err)
	}
	if n := i64(other[0]["n"]); n != 0 {
		t.Fatalf("CROSS-TENANT LEAK in the rollup: %d rows", n)
	}
}

// Latest must mean LATEST. `LIMIT 1 BY unit` only picks the newest row if the
// ordering is right, and a board showing a stale sample as current utilization
// would be a silent lie — so the ordering is proven against the engine, not read.
func TestLiveLatestPicksTheNewestPerUnit(t *testing.T) {
	liveDatastore(t)
	ctx := context.Background()
	org := fmt.Sprintf("live-latest-%d", time.Now().UnixNano())
	at := time.Now().UTC().Add(-10 * time.Minute)

	// Deliberately NOT in time order, and the newest is not the largest value —
	// so neither insert order nor max() could pass this by accident.
	for _, s := range []struct {
		off  time.Duration
		util float64
	}{{2 * time.Minute, 0.5}, {4 * time.Minute, 0.1}, {time.Minute, 0.9}} {
		if err := Record(ctx, Sample{
			Org: org, Source: SourceAgent, Unit: "tgt-latest", Kind: KindGPU,
			At: at.Add(s.off), GPUUtil: s.util,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	// A second unit proves the "per unit" half.
	if err := Record(ctx, Sample{Org: org, Source: SourceAgent, Unit: "tgt-other",
		Kind: KindLaptop, At: at, GPUUtil: 0.3}); err != nil {
		t.Fatalf("Record other: %v", err)
	}

	board, err := Latest(ctx, org)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(board) != 2 {
		t.Fatalf("want one row per unit (2), got %d: %v", len(board), board)
	}
	got := board["tgt-latest"]
	if got.GPUUtil < 0.09 || got.GPUUtil > 0.11 {
		t.Fatalf("Latest must be the NEWEST sample (util 0.1 @ +4m), got %v", got.GPUUtil)
	}
	if !got.At.Equal(at.Add(4 * time.Minute).Truncate(time.Millisecond)) {
		t.Fatalf("Latest picked the wrong instant: %v", got.At)
	}
	if board["tgt-other"].Kind != KindLaptop {
		t.Fatalf("each unit keeps its own latest: %+v", board["tgt-other"])
	}
}

// The shipped DDL must be idempotent: ensure() runs on every cold write path, so a
// second pass over an existing table/view can never error.
func TestLiveSchemaIsIdempotent(t *testing.T) {
	liveDatastore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		for _, ddl := range schema {
			if err := datastore.Exec(ctx, ddl); err != nil {
				t.Fatalf("pass %d: DDL is not idempotent: %v", i, err)
			}
		}
	}
}
