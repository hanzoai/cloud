// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cloud

import (
	"context"
	"maps"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"sync"

	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The DATA PLANE's own measurements — what this process ingested, wrote, bound
// and delivered.
//
// Everything the fleet could alert on until now was INFRASTRUCTURE: nodes,
// PVCs, pods, containers, chains. Thirty rules, and not one of them could see a
// single row of data move. The cost of that is measurable and was paid twice —
// span ingest was dead for four and a half months, and an 88% ingest loss ran
// under a green dashboard — because no rule could have noticed either. A plane
// that carries data and reports only on the machine underneath it is not
// observed; it is merely monitored.
//
// ZERO IS A MEASUREMENT — THE WHOLE FILE TURNS ON THIS.
//
// A counter that springs into existence on its first Add cannot express "this
// stopped". The series is simply absent, and absent is what a metric store
// shows for a name nobody ever wrote, a service that was never deployed, and a
// typo — so a rule written against it either never fires or fires everywhere.
// That is precisely why four and a half months of silence looked like health:
// there was nothing to compare against zero.
//
// So every counter here is SEEDED AT ZERO for its known label values at
// startup (seedCounters). From then on `increase(...[30m]) == 0` is a true
// sentence about a live process, and a restart — which resets the counter and
// leaves it at zero — reads as "still nothing", which is exactly right.
// Distinguishing "stopped" from "never started" is the entire job.
//
// Naming follows metrics_http.go: hanzo_-prefixed, in full Prometheus
// convention, because the exporter is installed with otlptranslator.NoTranslation
// and will not add a suffix or prefix for you. Instruments resolve LAZILY for
// the reason spelled out there — a meter taken at init binds to the no-op
// provider that exists before the composition root runs, and every measurement
// afterwards is discarded while the code looks perfectly instrumented.
//
// CARDINALITY. Every label value here comes from a finite, server-side set:
// warehouse table names, ingest endpoint names, egress names, the plane names this
// process itself served. None is client-chosen.

var (
	planeOnce sync.Once

	ingestItems metric.Int64Counter // hanzo_ingest_items_total{door,outcome}
	planeRows   metric.Int64Counter // hanzo_plane_rows_written_total{table} — table = the STREAM (see warehouseTables)
	alertEgress metric.Int64Counter // hanzo_alert_delivery_total{egress,outcome}
)

// warehouseTables are the event STREAMS this estate writes. Seeding them at zero
// is what makes "no span has landed in 30 minutes" a statement the metric store
// can answer — event.span is the series that was missing for four and a half
// months.
//
// A STREAM, not a table, since the occurrence tables merged into event.fact
// discriminated by `signal`. The distinction is the difference between a rule
// that works and one that cannot: five signals in one table report one series,
// so a table-keyed counter can only say "something still arrives" — never which
// signal stopped, which is the only question this exists to answer. Both writers
// therefore report the signal's own name (apps/analytics writes it as the
// signal's subject; the o11y plane sink's two tables ARE signals), so a row
// counts on one series whichever path carried it.
//
// event.event is gone from this list because the signal was renamed act — a
// namespace cannot also be a member of itself. Leaving it would seed a series
// that can only ever read zero, which is the same false "it stopped" this
// seeding exists to prevent, pointed the other way.
var warehouseTables = []string{"event.act", "event.clip", "event.error", "event.log", "event.span"}

// ingestDoors are the ingest endpoints whose admission outcome is counted. One
// endpoint today (POST /v1/event, the ONE event endpoint); the list exists so
// seeding stays honest when a second one is added.
var ingestDoors = []string{"event"}

// planeInstruments resolves the data-plane instruments and seeds them. Lazy:
// see the note above and metrics_http.go's instruments().
func planeInstruments() {
	planeOnce.Do(func() {
		m := otel.Meter(meterName)
		ingestItems, _ = m.Int64Counter("hanzo_ingest_items_total",
			metric.WithDescription("Items offered to an ingest endpoint, by door and admission outcome (accepted/dropped)."))
		planeRows, _ = m.Int64Counter("hanzo_plane_rows_written_total",
			metric.WithDescription("Rows written to an event-warehouse table."))
		alertEgress, _ = m.Int64Counter("hanzo_alert_delivery_total",
			metric.WithDescription("Alert batches carried out of the process, by egress and outcome (delivered/failed)."))

		registerPlaneGauges(m)
		seedCounters()
	})
}

// seedCounters writes 0 to every known label combination so the series EXISTS
// before anything happens. Adding zero is not a no-op to a metric store: it is
// the difference between a rule that can say "this stopped" and one that can
// only say nothing.
func seedCounters() {
	ctx := context.Background()
	for _, t := range warehouseTables {
		if planeRows != nil {
			planeRows.Add(ctx, 0, metric.WithAttributes(attribute.String("table", t)))
		}
	}
	for _, d := range ingestDoors {
		for _, outcome := range []string{"accepted", "dropped"} {
			if ingestItems != nil {
				ingestItems.Add(ctx, 0, metric.WithAttributes(
					attribute.String("door", d),
					attribute.String("outcome", outcome)))
			}
		}
	}
	// Alert egresses are seeded on the failing side only. "Delivered" appearing
	// for the first time is unambiguous; a `failed` series that does not exist
	// until the first failure would leave the meta-alert unable to distinguish
	// "no failures" from "nothing is even trying".
	for _, e := range []string{"slack", "webhook", "none"} {
		if alertEgress != nil {
			alertEgress.Add(ctx, 0, metric.WithAttributes(
				attribute.String("egress", e),
				attribute.String("outcome", "failed")))
		}
	}
}

// ObserveIngest records one ingest endpoint's admission outcome.
//
// accepted and dropped are reported TOGETHER because the useful question is a
// ratio, not a count: "88% of what was offered was dropped" is an outage,
// "8,000 items were dropped" is a number whose meaning depends on a second
// series nobody fetched.
func ObserveIngest(door string, accepted, dropped int) {
	planeInstruments()
	if ingestItems == nil || door == "" {
		return
	}
	ctx := context.Background()
	if accepted > 0 {
		ingestItems.Add(ctx, int64(accepted), metric.WithAttributes(
			attribute.String("door", door), attribute.String("outcome", "accepted")))
	}
	if dropped > 0 {
		ingestItems.Add(ctx, int64(dropped), metric.WithAttributes(
			attribute.String("door", door), attribute.String("outcome", "dropped")))
	}
}

// ObserveRows records rows landed in one event STREAM. Called from the two
// writers that put rows there — the o11y plane sink and the analytics bus drain
// — so `event.span` counts whichever one is carrying it.
//
// The argument is the stream (event.span, event.log, event.act …), not the
// physical table: since the occurrence tables merged into event.fact the table
// no longer identifies what stopped. See warehouseTables.
func ObserveRows(table string, n int) {
	planeInstruments()
	if planeRows == nil || table == "" || n <= 0 {
		return
	}
	planeRows.Add(context.Background(), int64(n),
		metric.WithAttributes(attribute.String("table", table)))
}

// ObserveAlertDelivery records one alert batch's egress outcome. This is the
// metric behind the meta-alert: paging that cannot reach a human is itself an
// incident, and the only reason it went unnoticed for months is that nothing
// counted it.
func ObserveAlertDelivery(egress, outcome string) {
	planeInstruments()
	if alertEgress == nil || egress == "" {
		return
	}
	alertEgress.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String("egress", egress), attribute.String("outcome", outcome)))
}

// servedPlanes is the set of plane names THIS process bound, recorded by
// ServePlane. It is the "should be bound" set, and it is authoritative because
// it is written by the act of binding rather than by a list somebody maintains:
// an app that this process was asked to serve belongs here, and nothing else
// does. Walking the whole 112-row manifest instead would dial sockets for apps
// that were never meant to be here and call their absence a fault.
var (
	servedMu     sync.Mutex
	servedPlanes = map[string]bool{}
)

// planeServed notes that name's socket was bound here, so the gauge below can
// notice when it stops answering.
func planeServed(name string) {
	servedMu.Lock()
	defer servedMu.Unlock()
	servedPlanes[name] = true
}

// servedPlaneNames returns the expected set, sorted for a stable scrape.
func servedPlaneNames() []string {
	servedMu.Lock()
	defer servedMu.Unlock()
	return slices.Sorted(maps.Keys(servedPlanes))
}

// registerPlaneGauges installs the observable gauges: facts that are true at
// collection time rather than events to be tracked.
func registerPlaneGauges(m metric.Meter) {
	bound, err := m.Int64ObservableGauge("hanzo_plane_peer_bound",
		metric.WithDescription("1 when a plane socket this process served still accepts connections, 0 when it does not."))
	if err == nil {
		_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			for _, name := range servedPlaneNames() {
				up := int64(0)
				// listening CONNECTS rather than stats: on a volume-backed run
				// directory a socket file outlives the process that bound it, so
				// the file proves nothing. A present-but-unusable socket returns
				// an error and is reported as down, which is what it is.
				if ok, probeErr := listening(zip.SocketPath(name)); ok && probeErr == nil {
					up = 1
				}
				o.ObserveInt64(bound, up, metric.WithAttributes(attribute.String("peer", name)))
			}
			return nil
		}, bound)
	}

	// Process memory against the limit the runtime actually enforces.
	//
	// ContainerOOMKilled is a POST-MORTEM: the kernel has already killed the
	// process, the request in flight is gone, and cloud is a single writer, so
	// by the time that alert fires the API has had an outage. GOMEMLIMIT is the
	// ceiling the Go runtime governs itself against, and watching the approach
	// to it is the only version of this signal that arrives while there is
	// still something to do about it.
	used, uerr := m.Int64ObservableGauge("hanzo_process_memory_bytes",
		metric.WithDescription("Memory the Go runtime governs against GOMEMLIMIT (total mapped, less released)."))
	limit, lerr := m.Int64ObservableGauge("hanzo_process_memory_limit_bytes",
		metric.WithDescription("The effective GOMEMLIMIT this process runs under."))
	if uerr == nil && lerr == nil {
		// SetMemoryLimit(-1) READS the limit without setting it — the documented
		// way to ask. This process must never change its own ceiling; the
		// deployment owns that.
		samples := []metrics.Sample{
			{Name: "/memory/classes/total:bytes"},
			{Name: "/memory/classes/heap/released:bytes"},
		}
		_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
			metrics.Read(samples)
			total := int64(samples[0].Value.Uint64())
			released := int64(samples[1].Value.Uint64())
			o.ObserveInt64(used, total-released)
			o.ObserveInt64(limit, debug.SetMemoryLimit(-1))
			return nil
		}, used, limit)
	}
}
