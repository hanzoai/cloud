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
	"os"
	"strings"
	"time"

	"github.com/hanzoai/o11y/pkg/prober"

	"github.com/hanzoai/cloud"
)

// fleetTargets is the in-cluster surface cloud watches.
//
// The URL is each service's OWN health path, not a uniform guess: /healthz,
// /health, /ping and /api/public/health are all in use, and probing the wrong
// one reports a healthy service as down. These were carried over verbatim from
// the scrape config they replace.
var fleetTargets = []prober.Target{
	{Name: "bot-gateway", URL: "http://bot-gateway.hanzo.svc:80/health"},
	{Name: "commerce", URL: "http://commerce.hanzo.svc:8001/health"},
	{Name: "iam", URL: "http://iam.hanzo.svc:80/healthz"},
	{Name: "kms", URL: "http://kms.hanzo.svc:80/healthz"},
	{Name: "pricing", URL: "http://pricing.hanzo.svc:8080/health"},
	{Name: "chat", URL: "http://chat.hanzo.svc:80/api/public/health"},
	{Name: "search", URL: "http://search.hanzo.svc:7700/health"},
	{Name: "vector", URL: "http://vector.hanzo.svc:6333/healthz"},
	{Name: "models", URL: "http://models.hanzo.svc:80/health"},
	{Name: "s3", URL: "http://s3.hanzo.svc:9000/healthz"},
	{Name: "datastore", URL: "http://datastore.hanzo.svc:8123/ping"},
	{Name: "hanzo-mpc", URL: "http://hanzo-mpc.hanzo.svc:8080/health"},
	{Name: "billing", URL: "http://billing.hanzo.svc:80/health"},
	{Name: "flow", URL: "http://flow.hanzo.svc:80/health"},
	{Name: "cloud", URL: "http://cloud.hanzo.svc:8000/v1/health"},
	{Name: "visor", URL: "http://visor.hanzo.svc:19000/health"},
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

// Services folded into cloud are NOT probed separately.
//
// commerce and the o11y runtime have no operator CR: they are compiled into this
// binary and reached through api.hanzo.ai. Probing commerce.hanzo.svc would test
// a Service whose selector matches no pod — reporting down for something that is
// up, inside this very process. Probing cloud covers them, because they ARE
// cloud.
//
// iam and bot-gateway still carry their own CRs at replicas=1, so they are still
// separate processes and are still probed. When they fold in, their entries come
// out in the same change that removes their CR — not before, or the fold looks
// like an outage.

// fleetProber is pinned for the process life so Shutdown can stop it.
var fleetProber *prober.Prober

// mountProbes starts the fleet health probes.
//
// Opt-out rather than opt-in: this is the availability signal alerting depends
// on, and a signal that defaults to off is one that is off in the deployment
// that needed it. O11Y_PROBES=false disables it; O11Y_PROBE_INTERVAL tunes the
// period.
func mountProbes(deps cloud.Deps) error {
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

	p, err := prober.New(prober.Config{Targets: fleetTargets, Interval: interval})
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
