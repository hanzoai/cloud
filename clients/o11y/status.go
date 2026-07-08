package o11y

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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

	deps := vmUpInventory(ctx, svc)
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
		res.Source = "victoria-metrics"
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

// vmUpInventory reads `up{service="<product>"}` as an instant query and projects
// each series into a deployment row (instance + up). Any failure degrades to an
// empty inventory (honest), never a fabricated replica.
func vmUpInventory(ctx context.Context, svc service) []deployment {
	c := newVMClient()
	series, err := c.queryInstant(ctx, `up{service="`+promLabel(svc.PromService)+`"}`)
	if err != nil {
		return []deployment{}
	}
	out := make([]deployment, 0, len(series))
	for _, s := range series {
		inst := s.metric["instance"]
		if inst == "" {
			inst = s.metric["pod"]
		}
		out = append(out, deployment{Instance: inst, Up: s.value == 1})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}

// instantSeries is one VM instant-query series: its label set + scalar value.
type instantSeries struct {
	metric map[string]string
	value  float64
}

// vmInstantResponse is the Prometheus/VM query envelope (vector result).
type vmInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string  `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// queryInstant issues GET /api/v1/query with a SERVER-built PromQL (URL-encoded).
func (c *vmClient) queryInstant(ctx context.Context, query string) ([]instantSeries, error) {
	u := c.base + "/api/v1/query?query=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vm: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var vr vmInstantResponse
	if err := json.Unmarshal(body, &vr); err != nil {
		return nil, err
	}
	if vr.Status != "success" {
		return nil, fmt.Errorf("vm: non-success status")
	}
	out := make([]instantSeries, 0, len(vr.Data.Result))
	for _, r := range vr.Data.Result {
		var raw string
		if err := json.Unmarshal(r.Value[1], &raw); err != nil {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		out = append(out, instantSeries{metric: r.Metric, value: v})
	}
	return out, nil
}
