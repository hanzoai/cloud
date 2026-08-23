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

// Fleet availability: what is up now, and what was up across a window.
//
// This is what is LEFT of the SuperAdmin VictoriaMetrics proxy, and it is here
// under its own name because the thing it replaced was named after a vendor.
// `GET /v1/o11y/vm/query` spelled VictoriaMetrics into the route table, admitted
// nineteen exact PromQL strings, and returned that store's Prometheus envelope
// byte-for-byte. The store is gone. A route named for it, speaking its wire, is
// a dependency's vocabulary outliving the dependency — the same thing
// metricsgauge.go refused when it declined to keep the envelope.
//
// So the surface is retired and the ONE question inside it that is still
// measured is asked plainly. Three of those nineteen queries — `up`, `sum(up)`,
// `count(up)` — were the platform-health board asking "how much of the fleet is
// up, now and lately". We still measure that. The other sixteen we do not, and
// the ledger below says so by name rather than answering them with an empty
// series.
//
// ── WHAT SURVIVED, AND HOW THE NUMBER CHANGED ────────────────────────────────
//
// `up` was the SCRAPE's own metric: one series per target, born from the act of
// visiting it. Nothing visits anything now. What we have instead is
// hanzo_service_up, which the fleet prober (probes.go) writes every 30s by
// knocking on each service's own health URL, and which metricspush.go carries
// into the store in-process.
//
// The two are not the same measurement and this endpoint does not pretend they
// are. `count(up)` counted every scrape target VictoriaMetrics federated —
// node-exporters, kube-state-metrics, and every pod any cluster's ServiceMonitor
// selected, in the hundreds. `total` here counts fleetTargets, which is the
// couple of dozen services we deliberately chose to knock on. The denominator
// DROPS, and it drops because the new number is a list somebody maintains rather
// than a census of whatever happened to be scraped. Per-replica identity is gone
// for the same reason status.go's inventory lost it: a Service address is not a
// pod, and inventing a pod name would be inventing a fact.
//
// ── WHAT DID NOT SURVIVE ─────────────────────────────────────────────────────
//
// Sixteen queries had exactly ONE producer each, every one of them a scrape, and
// not one of them writes to the store this process reads. They are not degraded
// here, they are ABSENT, and the boards that drew them must stop asking. Each
// entry below names its dead producer and what would have to be MEASURED to have
// the panel back — because "restore it" is never the answer to a signal whose
// collector is gone, and a panel is worth exactly the measurement under it:
//
//	node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes
//	    node-exporter, scraped per node on lux-k8s. To measure node memory again,
//	    something has to read /proc/meminfo on each node and push the pair.
//
//	container_memory_working_set_bytes
//	    the kubelet's cAdvisor, scraped per pod. To measure top-pod memory again,
//	    something has to read the kubelet summary API and push per-pod bytes.
//
//	kube_deployment_status_replicas_available / kube_deployment_spec_replicas
//	kube_statefulset_status_replicas_ready / kube_statefulset_replicas
//	    kube-state-metrics, scraped. These were never really metrics: they are the
//	    API server's own object status re-published as a time series, and the
//	    round trip through a metrics store only ever added staleness to a fact the
//	    API server answers directly. So the replica grid should not come back as a
//	    gauge at all — it should WATCH Deployments and StatefulSets and read
//	    status.availableReplicas against spec.replicas at the source.
//
//	lux_validator_up / _c_block_height / _peers / _bootstrapped
//	lux_network_validators_up / _validators_total
//	lux_network_c_tip_age_seconds / _block_height_spread
//	lux_network_tip_hash_variants / _ready_but_rpc_dead
//	    ghcr.io/luxfi/monitoring's /validator-exporter, which binds :9101 and
//	    renders a Prometheus exposition and does nothing else — no push, no OTLP,
//	    no egress. A lux-side vmagent scraped it and remote-wrote to a hub this
//	    process then queried. The measurements are still being TAKEN and the
//	    delivery was already fragile: that remote-write POSTed into a 404 for
//	    1369 consecutive retries in July, so the validator board had been dark for
//	    a while before anyone removed anything. To measure it again, that exporter
//	    pushes to the ZAP metric receiver the way this process does — no new
//	    store, no new reader, and gaugeSeries above then works on those names
//	    unchanged. (charts/app/values/hanzo/hanzo-chain-exporter.yaml is the same
//	    signal family for the hanzo brand, and it is the same scrape-only shape:
//	    it needs the same change.)
//
//	ALERTS{alertstate="firing", brand="lux"}
//	    vmalert's remote-write of its own firing set, and nothing else — the
//	    series existed only because a rule evaluator published its state as a
//	    metric. o11y's ruler keeps rule state in its SQL store, so the FACT still
//	    exists; it is rows, not a series. Getting the firing set back is a
//	    projection of the ruler's own state, not a metric to re-collect, and it
//	    belongs to an alerts endpoint rather than to a gauge read.
//
// Two of these were dead weight before they were dead: five of the eighteen
// queries the console declared (the four freeze-detection signals and the alert
// rollup) were allowlisted here and never fetched by any panel. Deleting them
// costs nothing that was on screen.
//
// ── WHO WAS READING IT ───────────────────────────────────────────────────────
//
// The console's telemetry client is bound to the Prometheus envelope: it reads
// data.result[].value and nothing else, and it never checks `status`. That is
// why this is a route DELETION and not a re-shaped 200 — a reshaped envelope
// parses to an empty array and paints a board of zeroes, whereas a route that is
// gone throws and renders the client's own error card. Three surfaces call it:
//
//	MetricsModule's InfraMetrics (/metrics, SuperAdmin) — up, sum(up), count(up).
//	    KEEPS its four tiles, its health donut, its "down now" list and both
//	    trend charts, by moving to /v1/o11y/availability. The numbers change:
//	    "scrape targets" becomes "probed services", which is dozens where it was
//	    hundreds, and the per-instance endpoint column has no measurement behind
//	    it any more.
//	StatusModule (/status, SuperAdmin) — up.
//	    KEEPS its three tiles and its service table, same move, minus the
//	    Endpoint column.
//	LuxNetworkModule (console.lux.cloud, SuperAdmin, brand lux) — the other 15.
//	    Goes DARK in full: validator tables, per-network up/total badges, node
//	    memory bars, top-pod memory, and the services replica grid. Its loader is
//	    a bare Promise.all, so it already failed whole rather than per-panel.
//	    Nothing here can serve it; see the ledger above for what each panel
//	    would have to measure.
//
// ── THE GATE ─────────────────────────────────────────────────────────────────
//
// Platform sudo, unchanged from the proxy this replaces. It is not tenant data:
// it is the whole fleet's inventory, so it takes the same predicate the infra-log
// god-view takes and every customer is 403. What DID change is that the handler
// is no longer an access boundary in front of an unauthenticated store — there
// is no second endpoint to leave open, because there is no second store.
//
// And an unreachable datastore is a 503 that says so. The failure this endpoint
// must never have is the quiet one: a 200 carrying an empty series renders as a
// board full of zeroes, which is indistinguishable from a fleet that is entirely
// down and is the single most expensive lie a status surface can tell.

package o11y

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/hanzoai/cloud/apps/datastore"
	"github.com/zap-proto/zip"
)

// availabilityIn bounds the trend window. There is no query field and there is
// no service field: the caller picks a WINDOW, never what is measured or how.
type availabilityIn struct {
	// Range is the trend window in seconds. Default 3600, capped at 604800 (7d).
	Range int `json:"range"`
	// StepSec is the bucket width in seconds, clamped to [30, 3600]. Absent
	// picks ~60 buckets across the range.
	StepSec int `json:"stepSec"`
}

// availabilityPoint is one bucket of the fleet trend.
type availabilityPoint struct {
	// T is the bucket start, RFC3339 in UTC.
	T string `json:"t"`
	// Up is how many services were up at the end of the bucket.
	Up int `json:"up"`
	// Total is how many services reported at all inside the bucket. It can be
	// lower than the current total: a target added last week reported nothing
	// the week before, and saying so is the point.
	Total int `json:"total"`
}

// availabilityResponse is the whole platform-health board in one read — the
// instant inventory and the trend that used to cost three separate PromQL
// round-trips.
type availabilityResponse struct {
	// Range is the window and bucket width actually used, after clamping — not what
	// was asked for, which is why a caller reads it back rather than assuming.
	Range struct {
		// SinceSec is the window actually used, after clamping.
		SinceSec int `json:"sinceSec"`
		// StepSec is the bucket width actually used, after clamping.
		StepSec int `json:"stepSec"`
	} `json:"range"`
	// Up is how many services are up right now.
	Up int `json:"up"`
	// Total is how many services the prober currently watches.
	Total int `json:"total"`
	// Services is the current inventory, sorted by name so two reads of an
	// unchanged fleet are byte-identical.
	Services []serviceUp `json:"services"`
	// Series is the trend, oldest bucket first.
	Series []availabilityPoint `json:"series"`
}

// GetO11yAvailability reports how much of the Hanzo fleet is up — the current
// per-service inventory plus an up-versus-reporting trend across the window.
// Both come from the fleet prober's own measurements: every service is asked its
// health URL every 30 seconds, so a service is listed as down because it did not
// answer, never because something failed to collect it. PLATFORM SUDO ONLY —
// this is the whole fleet's inventory, not tenant data, so every customer is
// 403. An unreachable telemetry store answers 503 rather than an empty trend,
// because a board of zeroes and a fleet that is down look identical.
//
// Example: {"range": 3600}
func handleAvailability(ctx context.Context, in *availabilityIn) (*availabilityResponse, error) {
	if !callerIsAdmin(ctx) {
		return nil, zip.ErrForbidden("fleet availability is restricted to platform administrators")
	}
	if !datastore.Ready() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "o11y availability: datastore not connected")
	}
	rangeSec := boundRangeSec(in.Range)
	stepSec := stepFor(rangeSec, in.StepSec)

	var resp availabilityResponse
	resp.Range.SinceSec = rangeSec
	resp.Range.StepSec = stepSec
	resp.Services = []serviceUp{}
	resp.Series = []availabilityPoint{}

	byService, err := latestGaugeBy(ctx, upMetric, "service")
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "o11y availability: %v", err)
	}
	for name, v := range byService {
		up := v == 1
		resp.Services = append(resp.Services, serviceUp{Name: name, Up: up})
		if up {
			resp.Up++
		}
	}
	resp.Total = len(resp.Services)
	sort.Slice(resp.Services, func(i, j int) bool { return resp.Services[i].Name < resp.Services[j].Name })

	buckets, err := gaugeSeries(ctx, upMetric, rangeSec, stepSec)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "o11y availability: %v", err)
	}
	for _, b := range buckets {
		resp.Series = append(resp.Series, availabilityPoint{
			T:     time.UnixMilli(b.StartMilli).UTC().Format(time.RFC3339),
			Up:    int(b.Sum + 0.5),
			Total: b.Series,
		})
	}
	return &resp, nil
}
