// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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

// Fleet health probes.
//
// These targets were blackbox scrape jobs in a separate metrics stack, which
// existed for one reason: o11y received pushed telemetry and could not ask a
// question. o11y/pkg/prober closed that gap, so the probes live here now,
// emitting hanzo_service_up through the same meter as every other metric.
//
// Availability is the one signal that cannot be pushed. A crashed service sends
// nothing, and nothing reads exactly like idle — so somebody has to knock.

package o11y

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/o11y/pkg/prober"
	"github.com/luxfi/metric"

	"github.com/hanzoai/cloud"
)

// fleetTargets is the ONE registry of where a Hanzo service answers, and both
// availability reads take their address from it: the fleet prober below, whose
// gauge becomes the public status document, and the scoped per-product read
// (status.go, through productmap.go). It used to be only the first of those. The
// second derived its own address by convention — `<product>.hanzo.svc.cluster.local`
// on port 80, trying /health then /healthz — which is a second answer to the
// question this table already answers, and it was wrong for nineteen of the
// twenty-seven products it covered, because most services here serve neither
// port 80 nor either of those paths. Two tables for one fact is how one of them
// goes stale unnoticed.
//
// The URL is each service's OWN address, not a uniform guess: ports differ,
// and /healthz, /health, /ping, /metrics and /api/public/health are all in use.
// Probing the wrong one reports a healthy service as down, and because the
// public status page publishes exactly this gauge, a wrong entry here is
// published to customers as an outage.
//
// Every entry is MEASURED from inside the cluster before it lands here. That is
// not ceremony: three of these were carried over verbatim from an old scrape
// config, drifted as the fleet moved underneath them, and stood as ongoing
// incidents against services that were up the whole time.
var fleetTargets = []prober.Target{
	{Name: "bot-gateway", URL: "http://bot-gateway.hanzo.svc:80/health"},
	{Name: "commerce", URL: "http://commerce.hanzo.svc:8001/health"},

	// iam answers on port 80 (→ 8000), but /healthz is not a route it serves
	// there: it is measured 404, which the prober correctly scored as down and
	// the status page published as an outage of a healthy identity provider.
	// The address below is the OIDC discovery document — what iam's own
	// readiness probe reads, and the first thing every client fetches before it
	// can sign anybody in. If it answers, a customer can log in; if it does not,
	// no customer can. That is the dependency worth measuring, and unlike a
	// health path it cannot drift away from the contract, because it IS the
	// contract.
	{Name: "iam", URL: "http://iam.hanzo.svc:80/.well-known/openid-configuration"},

	// kms is served by THIS binary. The standalone kms Deployment is scaled to
	// zero and its Service has no endpoints, so the old kms.hanzo.svc address
	// refused every connection and stood as a permanent false outage; what a
	// customer of kms.hanzo.ai reaches is cloud's embedded KMS. The path is
	// cloud's own KMS probe, which answers 503 when the master key is absent
	// (apps/kms/mount.go), so this stays a signal distinct from the cloud entry
	// below rather than a duplicate of it: the API can be up while KMS is
	// fail-closed, and a customer feels that as KMS being down.
	{Name: "kms", URL: "http://cloud.hanzo.svc:8000/v1/kms/health"},

	{Name: "pricing", URL: "http://pricing.hanzo.svc:8080/health"},
	{Name: "chat", URL: "http://chat.hanzo.svc:80/api/public/health"},
	{Name: "search", URL: "http://search.hanzo.svc:7700/health"},
	{Name: "vector", URL: "http://vector.hanzo.svc:6333/healthz"},
	{Name: "models", URL: "http://models.hanzo.svc:80/health"},
	{Name: "s3", URL: "http://s3.hanzo.svc:9000/healthz"},
	{Name: "datastore", URL: "http://datastore.hanzo.svc:8123/ping"},
	{Name: "billing", URL: "http://billing.hanzo.svc:80/health"},
	{Name: "flow", URL: "http://flow.hanzo.svc:80/health"},
	{Name: "cloud", URL: "http://cloud.hanzo.svc:8000/v1/health"},
	{Name: "visor", URL: "http://visor.hanzo.svc:19000/v1/health"},
	{Name: "base", URL: "http://base.hanzo.svc:80/healthz"},
	{Name: "console", URL: "http://console.hanzo.svc:4000/api/public/health"},
	{Name: "studio", URL: "http://studio.hanzo.svc:80/metrics"},
	{Name: "hanzo-playground", URL: "http://hanzo-playground.hanzo.svc:80/metrics"},

	// The edge. Nothing else being reachable matters if this is not, and it was
	// absent from the scrape config this list came from — a gap inherited, not
	// chosen. It is deployed raw (infra/k8s/ingress), not via an operator CR,
	// which is likely why it was missed.
	{Name: "ingress", URL: "http://ingress.hanzo.svc:80/ping"},
}

// WHAT IS NOT HERE, AND WHY.
//
// hanzo-mpc is GONE from this list. There is no hanzo-mpc Deployment: the
// Service of that name in the hanzo namespace has no endpoints and selects
// nothing, so the probe refused on every cycle and published a standing incident
// for a workload that had already left. What serves mpc.hanzo.ai is a separate
// ring in its own namespace, and its API answers 307 to every path including
// ones that do not exist — a probe that can only say yes is not a measurement,
// so pointing at it would trade a false outage for a false all-clear. Nothing in
// this fleet measures MPC, and the list says so by not naming it.
//
// Services folded into cloud keep their NAME here but take cloud's address, as
// kms does above. The name is what a customer knows (kms.hanzo.ai), so dropping
// it would silently stop reporting on something people depend on; the address is
// where it actually answers. commerce is the same case reached from the other
// side — its Service selects cloud's pods, so commerce.hanzo.svc is already
// cloud, and the entry is left as it stands because it is measured to answer.
//
// iam and bot-gateway still carry their own CRs at replicas=1, so they are still
// separate processes at their own addresses. When they fold in, the address
// moves to cloud's in the same change that removes their CR — not before, or the
// fold looks like an outage.

// address returns where the named service answers and whether this fleet watches
// it at all. It is the only way to learn a service's address: a caller that
// cannot find one here must probe NOTHING, because an address nobody measured is
// how a healthy service is reported down.
//
// A linear scan over a couple of dozen entries costs less than the map that
// would have to be kept in step with the list beside it.
func address(name string) (string, bool) {
	for _, t := range fleetTargets {
		if t.Name == name {
			return t.URL, true
		}
	}
	return "", false
}

// answered is the verdict both availability reads apply: did a server reply?
// 2xx is a yes. 3xx is also a yes, because a redirect comes from a process that
// is running and routing. Anything else is a no, INCLUDING a 404 — a 404 means
// the server is alive but this address is not the contract we think it is, and
// scoring that up would hide the exact defect that put three healthy services on
// the public status page as outages. It stays a no, and the reporter below names
// it so the address gets fixed in minutes rather than months.
//
// hanzoai/o11y's prober applies this same rule internally and status.go calls
// this function, so the two paths cannot disagree about what up means for one
// address — and two answers to that question, read side by side, are
// indistinguishable from an outage.
func answered(code int) bool { return code >= 200 && code < 400 }

// journal is the part of the logger this file uses. Naming the two methods
// instead of taking the whole Logger is what lets a test read what was written
// without standing up a logging stack.
type journal interface {
	Info(msg string, ctx ...interface{})
	Warn(msg string, ctx ...interface{})
}

// reporter is the probe client's transport, and it exists because the prober
// records a number and a number does not say why. A target that reads down
// because its Service has no endpoints and a target that reads down because it
// answers 404 on a path it never served are the same 0 in the gauge and
// completely different repairs; telling those two apart cost a live
// investigation. So every change of verdict is written out with the target, the
// exact address dialled, and the reason.
//
// It reports on CHANGE only. A target that has been down for a day would
// otherwise write the same line every 30 seconds and bury the one line that
// mattered.
type reporter struct {
	next http.RoundTripper
	// up is hanzo_service_up in the program's own registry — the same series,
	// name and label the meter pipeline has always written, produced here a
	// second time so the framework's export carries it natively. Two producers,
	// one truth: both record the same probe's verdict, so the series is
	// uninterrupted whichever pipeline a reader drinks from, and the old one can
	// be retired without a gap once every reader is confirmed on this road.
	// Nil when no registry was offered; recording is then skipped, never faked.
	up metric.GaugeVec
	// name maps a target's exact address to its name. A redirect lands on a
	// different address, is absent from this map, and passes through unreported:
	// only a probe's own first request is a verdict.
	name map[string]string
	log  journal

	mu sync.Mutex
	// last is the reason most recently reported per target. Empty means the
	// target answered.
	last map[string]string
}

func (r *reporter) RoundTrip(req *http.Request) (*http.Response, error) {
	name, watched := r.name[req.URL.String()]
	resp, err := r.next.RoundTrip(req)
	if !watched {
		return resp, err
	}
	verdict := 0.0
	switch {
	case err != nil:
		r.note(name, req.URL.String(), err.Error())
	case answered(resp.StatusCode):
		verdict = 1.0
		r.note(name, req.URL.String(), "")
	default:
		r.note(name, req.URL.String(), "HTTP "+strconv.Itoa(resp.StatusCode))
	}
	if r.up != nil {
		r.up.WithLabelValues(name).Set(verdict)
	}
	return resp, err
}

// note writes a line only when this target's verdict changed. A different reason
// for the same target is a different fact and is reported again — a target that
// went from refusing connections to answering 404 has moved, and the person
// reading needs to know which one they are chasing.
func (r *reporter) note(name, url, reason string) {
	r.mu.Lock()
	was, seen := r.last[name]
	if seen && was == reason {
		r.mu.Unlock()
		return
	}
	r.last[name] = reason
	r.mu.Unlock()

	if reason == "" {
		// A first sighting that is healthy is not news. Only a recovery is, and
		// only after something was reported broken.
		if seen {
			r.log.Info("fleet target recovered", "target", name, "url", url)
		}
		return
	}
	r.log.Warn("fleet target did not answer", "target", name, "url", url, "reason", reason)
}

// probeClient is the client the prober uses, carrying the reporter. It sets no
// Timeout of its own: the prober bounds every probe with a context deadline, and
// a second bound would be a second number to keep in step with the first.
func probeClient(log journal, up metric.GaugeVec) *http.Client {
	name := make(map[string]string, len(fleetTargets))
	for _, t := range fleetTargets {
		name[t.URL] = t.Name
	}
	return &http.Client{Transport: &reporter{
		next: http.DefaultTransport,
		name: name,
		log:  log,
		up:   up,
		last: make(map[string]string, len(fleetTargets)),
	}}
}

// upgauge mints hanzo_service_up in the program's registry — the exact
// series name and label the meter pipeline has always published, so every rule
// and reader sees one uninterrupted series whichever producer wrote the sample.
// A nil registry yields a nil gauge and recording is skipped: a test that
// mounts probes without a registry measures the probing, not the export.
func upgauge(r metric.Registerer) metric.GaugeVec {
	if r == nil {
		return nil
	}
	return r.NewGaugeVec("hanzo_service_up",
		"1 when the service answered its probe, 0 when it did not", []string{"service"})
}

// fleetProber is pinned for the process life so Shutdown can stop it.
var fleetProber *prober.Prober

// mountProbes starts the fleet health probes.
//
// Opt-out rather than opt-in: this is the availability signal alerting depends
// on, and a signal that defaults to off is one that is off in the deployment
// that needed it. O11Y_PROBES=false disables it; O11Y_PROBE_INTERVAL tunes the
// period.
func mountProbes(deps cloud.Deps, instruments metric.Registerer) error {
	log := deps.Logger.New("subsystem", "o11y-probes")

	if strings.EqualFold(strings.TrimSpace(os.Getenv("O11Y_PROBES")), "false") {
		log.Info("fleet health probes disabled (O11Y_PROBES=false)")
		return nil
	}

	interval := 30 * time.Second
	if v := strings.TrimSpace(os.Getenv("O11Y_PROBE_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		} else {
			log.Warn("ignoring unparseable O11Y_PROBE_INTERVAL", "value", v)
		}
	}

	p, err := prober.New(prober.Config{
		Targets:  fleetTargets,
		Interval: interval,
		Client:   probeClient(log, upgauge(instruments)),
	})
	if err != nil {
		// Non-fatal: losing probes should not take the API plane down with them.
		log.Warn("fleet health probes not started", "err", err)
		return nil
	}
	p.Start(context.Background())
	fleetProber = p

	log.Info("fleet health probes running",
		"targets", len(fleetTargets), "interval", interval, "metric", "hanzo_service_up")
	return nil
}

// stopProbes is called from the subsystem's Shutdown.
func stopProbes() {
	if fleetProber != nil {
		fleetProber.Stop()
		fleetProber = nil
	}
}
