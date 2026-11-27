package auto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/provider"
	"github.com/hanzoai/outbound"
)

// coreProvider is the built-in connector: control-flow + generic I/O steps that need
// NO external credentials. It is the connector the durable-run green test exercises
// end-to-end, so it must run without integrations mounted.
const coreProvider = "core"

func init() {
	provider.RegisterActions(coreProvider, "Core", map[string]*provider.Action{
		"http_request": {
			Name:        "http_request",
			DisplayName: "HTTP Request",
			Description: "Issue an HTTP GET/POST and return {status, headers, body}.",
			Props: []provider.PropSpec{
				{Name: "url", Type: "string", Required: true, Description: "Absolute http(s) URL."},
				{Name: "method", Type: "string", Description: "GET (default) or POST."},
				{Name: "body", Type: "string", Description: "Request body for POST."},
				{Name: "headers", Type: "object", Description: "Optional request headers."},
			},
			Run: runHTTPRequest,
		},
		"delay": {
			Name:        "delay",
			DisplayName: "Delay",
			Description: "Pause the flow for a bounded number of seconds.",
			Props: []provider.PropSpec{
				{Name: "seconds", Type: "number", Required: true, Description: "Seconds to wait (capped)."},
			},
			Run: runDelay,
		},
		"code": {
			Name:        "code",
			DisplayName: "Transform",
			Description: "Return the (reference-resolved) input map — a Go data-mapper transform.",
			Props: []provider.PropSpec{
				{Name: "input", Type: "object", Description: "Values, optionally {{step.path}} references to prior outputs."},
			},
			Run: runCode,
		},
		"wait_for_approval": {
			Name:        "wait_for_approval",
			DisplayName: "Wait for Approval",
			Description: "A manual waitpoint. Inside a flow it blocks until a resume signal; invoked directly it approves.",
			Props: []provider.PropSpec{
				{Name: "reason", Type: "string", Description: "Why approval is requested."},
			},
			Run: runWaitForApproval,
		},
	}, map[string]*provider.Trigger{
		"schedule": {
			Name:        "schedule",
			DisplayName: "Schedule",
			Description: "Start the flow on a cron schedule.",
			Strategy:    provider.StrategyPolling,
			Props: []provider.PropSpec{
				{Name: "cron", Type: "string", Required: true, Description: "Cron expression, e.g. \"0 3 * * *\"."},
			},
		},
		"manual": {
			Name:        "manual",
			DisplayName: "Manual",
			Description: "Start the flow only on an explicit /run.",
			Strategy:    provider.StrategyManual,
		},
	})
}

// ── core.http_request ────────────────────────────────────────────────────────

// maxHTTPBody bounds a fetched response so a hostile/huge upstream can't exhaust
// memory. 5 MiB is generous for an automation step.
const maxHTTPBody = 5 << 20

// httpClient is the ONE bounded client core.http_request uses: a real timeout
// (fail-secure — never an infinite hang) and the outbound guard, which judges the
// address of every dial — each redirect hop included — after resolution, so a flow
// can never reach cluster-internal or cloud-metadata addresses, and drops the
// credential headers a flow set once a redirect leaves the origin.
var httpClient = outbound.Client(outbound.Options{Timeout: 15 * time.Second, Redirects: 5})

func runHTTPRequest(ctx context.Context, rc provider.RunContext) (any, error) {
	rawURL := strInput(rc.Input, "url")
	if rawURL == "" {
		return nil, fmt.Errorf("http_request: url is required")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("http_request: url must be absolute http(s)")
	}
	method := strings.ToUpper(strInput(rc.Input, "method"))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost {
		return nil, fmt.Errorf("http_request: method must be GET or POST")
	}

	var body io.Reader
	if b := strInput(rc.Input, "body"); b != "" {
		body = strings.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("http_request: %w", err)
	}
	if hdrs, ok := rc.Input["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if sv, ok := v.(string); ok {
				req.Header.Set(k, sv)
			}
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBody))
	if err != nil {
		return nil, fmt.Errorf("http_request: read: %w", err)
	}
	return map[string]any{
		"status": resp.StatusCode,
		"body":   string(raw),
	}, nil
}

// ── core.delay ───────────────────────────────────────────────────────────────

// maxDelay caps a single delay step so a flow can't pin a worker slot indefinitely.
const maxDelay = 60 * time.Second

func runDelay(ctx context.Context, rc provider.RunContext) (any, error) {
	secs := numInput(rc.Input, "seconds")
	if secs < 0 {
		secs = 0
	}
	d := min(time.Duration(secs*float64(time.Second)), maxDelay)
	select {
	case <-time.After(d):
		return map[string]any{"delayed": d.Seconds()}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ── core.code ────────────────────────────────────────────────────────────────

// runCode is the data-mapper transform: it returns its (already
// reference-resolved, see resolveRefs) input map. No JS engine — a Go value map,
// so later steps can read {{thisStep.field}}.
func runCode(_ context.Context, rc provider.RunContext) (any, error) {
	if rc.Input == nil {
		return map[string]any{}, nil
	}
	return rc.Input, nil
}

// ── core.wait_for_approval ───────────────────────────────────────────────────

// runWaitForApproval is the DIRECT-invocation body (MCP tools/call). Inside a flow
// the workflow special-cases this step onto a durable "resume" signal and never
// calls Run — so this path represents an immediate, explicit approval.
func runWaitForApproval(_ context.Context, rc provider.RunContext) (any, error) {
	return map[string]any{"approved": true, "reason": strInput(rc.Input, "reason")}, nil
}

// ── input helpers ────────────────────────────────────────────────────────────

func strInput(in map[string]any, key string) string {
	if in == nil {
		return ""
	}
	if v, ok := in[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func numInput(in map[string]any, key string) float64 {
	if in == nil {
		return 0
	}
	switch v := in[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

// jsonBytes is a small helper for connectors that POST a JSON body.
func jsonBytes(v any) (*bytes.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}
