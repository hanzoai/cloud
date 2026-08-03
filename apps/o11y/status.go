package o11y

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// The scoped status read: a LIVE in-cluster health probe of the product's service
// (time-boxed, latency-measured) FUSED with the VM `up{service=…}` inventory
// (each series = one replica/instance with its up value). The health probe target
// is ONLY ever an allowlisted service host (resolveService), so a crafted product
// can never turn this into an SSRF of an arbitrary host — the SSRF boundary.
//
// Status is infra health, not tenant data (a service is up or down for everyone),
// so it is principal-gated (any validated caller) but NOT org-partitioned; the org
// is still resolved server-side and the endpoint refuses an unvalidated caller.

const healthProbeTimeout = 2 * time.Second

// deployment is one live replica of a product's service from VM `up`.
type deployment struct {
	Instance string `json:"instance"`
	Up       bool   `json:"up"`
}

// statusResult is the scoped status response.
type statusResult struct {
	Product     string       `json:"product"`
	Up          bool         `json:"up"`
	LatencyMs   int64        `json:"latencyMs"`
	Source      string       `json:"source"`
	Deployments []deployment `json:"deployments"`
	CheckedAt   string       `json:"checkedAt"`
}

// probeStatus builds the live status for an allowlisted service: it probes the
// in-cluster health endpoint (measuring latency) and reads the VM up-series
// inventory. `up` is true if the probe succeeded OR any VM replica reports up.
func probeStatus(ctx context.Context, svc service) statusResult {
	res := statusResult{
		Product:   svc.ID,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	probeUp, latency := probeHealth(ctx, svc.HealthHost)
	res.LatencyMs = latency.Milliseconds()

	deps := upInventory(ctx, svc)
	res.Deployments = deps

	anyVMUp := false
	for _, d := range deps {
		if d.Up {
			anyVMUp = true
			break
		}
	}
	switch {
	case probeUp:
		res.Up = true
		res.Source = "probe"
	case len(deps) > 0:
		res.Up = anyVMUp
		res.Source = "datastore"
	default:
		res.Up = false
		res.Source = "unreachable"
	}
	return res
}

// unknownStatus is the honest response for a well-formed but unbacked product:
// down, no probe (no SSRF of an arbitrary host), source "unknown-service".
func unknownStatus(product string) statusResult {
	return statusResult{
		Product:     product,
		Up:          false,
		Source:      "unknown-service",
		Deployments: []deployment{},
		CheckedAt:   time.Now().UTC().Format(time.RFC3339),
	}
}

// probeHealth hits the service's in-cluster health endpoint, trying /health then
// /healthz, time-boxed. Returns up + the measured latency. Only an allowlisted
// host reaches here (the caller resolved it), so this is not an SSRF vector.
func probeHealth(ctx context.Context, host string) (bool, time.Duration) {
	client := &http.Client{Timeout: healthProbeTimeout}
	for _, path := range []string{"/health", "/healthz"} {
		start := time.Now()
		pctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, "http://"+host+path, nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		latency := time.Since(start)
		cancel()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return true, latency
		}
	}
	return false, 0
}

// upInventory reads the fleet prober's own gauge and projects it into
// deployment rows. Any failure degrades to an empty inventory (honest), never a
// fabricated replica.
//
// ⚠️ PER-REPLICA IDENTITY IS GONE, AND THAT IS NOT A REGRESSION TO PAPER OVER.
// This used to read `up{service=…}`, which is the SCRAPE's own metric: one
// series per target, so `instance`/`pod` came free because something had
// visited each pod to produce it. Nothing scrapes anything now. What remains is
// hanzo_service_up, which the prober records per SERVICE — it asks a Service
// address whether the service answered, and a Service address is not a replica.
//
// So the inventory is one row per service, keyed by the thing actually
// measured. Synthesising a pod name here would be inventing a fact no
// measurement supports, which is the failure mode this whole file exists to
// avoid. If per-replica health is wanted back it has to be MEASURED — the
// prober would have to resolve endpoints and probe each — not derived.
func upInventory(ctx context.Context, svc service) []deployment {
	name := strings.TrimSpace(svc.PromService)
	if name == "" {
		return []deployment{}
	}
	byService, err := latestGaugeBy(ctx, upMetric, "service")
	if err != nil {
		return []deployment{}
	}
	v, ok := byService[name]
	if !ok {
		return []deployment{}
	}
	return []deployment{{Instance: name, Up: v == 1}}
}
