package observe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// status.go serves GET /v1/o11y/status — a REAL, live health signal for a product:
// an in-cluster health probe (measured latency) corroborated by the VictoriaMetrics
// up{service=<product>} scrape gauge. Not a hardcoded green dot.
//
// Status is INFRA-level (a product is up or down regardless of tenant), so there is
// no per-org data to isolate here — but the endpoint still requires a validated
// principal (no anonymous probing of the internal service mesh) and confines the
// probe target to a validated product slug, so it can never be pointed at an
// arbitrary in-cluster host.

const (
	statusProbeTimeout = 2 * time.Second
	vmQueryTimeout     = 2 * time.Second
)

type statusResponse struct {
	Product   string  `json:"product"`
	Up        bool    `json:"up"`
	LatencyMs float64 `json:"latencyMs"` // health-probe round trip; -1 when the probe did not complete
	HTTPCode  int     `json:"httpCode"`  // health-probe status code (0 = no response)
	ScrapeUp  *int    `json:"scrapeUp"`  // VictoriaMetrics up{service}: 1/0, or null when no scrape target
	ProbeURL  string  `json:"probeUrl"`
	Source    string  `json:"source"` // which signal set Up: "probe" | "scrape" | "none"
	CheckedAt string  `json:"checkedAt"`
}

func (s *service) getStatus(c *zip.Ctx) error {
	if _, ok := s.tenant(c); !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	product, err := requireProductQuery(c)
	if err != nil {
		return err
	}
	service := serviceFor(product)

	resp := statusResponse{Product: product, LatencyMs: -1, CheckedAt: time.Now().UTC().Format(time.RFC3339)}

	// 1) Live health probe (measured latency), confined to the validated product's
	//    in-cluster Service host.
	probeURL, code, latMs, probedOK := s.probeHealth(c.Context(), service)
	resp.ProbeURL = probeURL
	resp.HTTPCode = code
	if probedOK {
		resp.LatencyMs = latMs
	}

	// 2) VictoriaMetrics up{service} scrape gauge (authoritative scrape state).
	if up, ok := s.scrapeUp(c.Context(), service); ok {
		resp.ScrapeUp = &up
	}

	switch {
	case probedOK:
		resp.Up = true
		resp.Source = "probe"
	case resp.ScrapeUp != nil && *resp.ScrapeUp == 1:
		resp.Up = true
		resp.Source = "scrape"
	default:
		resp.Up = false
		resp.Source = "none"
	}
	return c.JSON(http.StatusOK, resp)
}

// probeHealth does a time-boxed GET against the product's in-cluster health
// endpoint (/health, then /healthz), returning the URL tried, the status code, the
// latency in ms, and whether a 2xx/3xx came back. The host is <service><dnsSuffix>
// with service validated by productRE, so no attacker-chosen host is reachable.
func (s *service) probeHealth(parent context.Context, service string) (probeURL string, code int, latMs float64, ok bool) {
	host := service + s.dnsSuffix
	client := &http.Client{Timeout: statusProbeTimeout}
	for _, path := range []string{"/health", "/healthz"} {
		u := "http://" + host + path
		ctx, cancel := context.WithTimeout(parent, statusProbeTimeout)
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			continue
		}
		res, err := client.Do(req)
		lat := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			cancel()
			probeURL = u
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		cancel()
		probeURL = u
		code = res.StatusCode
		latMs = round2(lat)
		if res.StatusCode >= 200 && res.StatusCode < 400 {
			return u, code, latMs, true
		}
	}
	return probeURL, code, latMs, false
}

// scrapeUp reads up{service="<service>"} from VictoriaMetrics. Returns (value, true)
// when a scrape target exists, else (0, false). service is validated by productRE
// (no quote/injection chars) before it reaches the PromQL string.
func (s *service) scrapeUp(parent context.Context, service string) (int, bool) {
	ctx, cancel := context.WithTimeout(parent, vmQueryTimeout)
	defer cancel()
	q := `up{service="` + service + `"}`
	u := strings.TrimRight(s.vmURL, "/") + "/api/v1/query?query=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false
	}
	res, err := (&http.Client{Timeout: vmQueryTimeout}).Do(req)
	if err != nil {
		return 0, false
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, false
	}
	var parsed struct {
		Data struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Data.Result) == 0 {
		return 0, false
	}
	// value is [ <ts float>, "<sample string>" ]; take the max across targets so a
	// product with several pods reads up if ANY is up.
	up := 0
	for _, r := range parsed.Data.Result {
		if len(r.Value) == 2 {
			if sv, okk := r.Value[1].(string); okk {
				if f, perr := strconv.ParseFloat(sv, 64); perr == nil && f >= 1 {
					up = 1
				}
			}
		}
	}
	return up, true
}
