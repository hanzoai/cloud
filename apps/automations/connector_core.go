package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// corePiece is the built-in connector: control-flow + generic I/O steps that need
// NO external credentials. It is the connector the durable-run green test exercises
// end-to-end, so it must run without integrations mounted.
const corePiece = "core"

func init() {
	register(&Connector{
		Name:        corePiece,
		DisplayName: "Core",
		AuthType:    "none",
		AuthReq:     false,
		Actions: map[string]*Action{
			"http_request": {
				Name:        "http_request",
				DisplayName: "HTTP Request",
				Description: "Issue an HTTP GET/POST and return {status, headers, body}.",
				Props: []PropSpec{
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
				Props: []PropSpec{
					{Name: "seconds", Type: "number", Required: true, Description: "Seconds to wait (capped)."},
				},
				Run: runDelay,
			},
			"code": {
				Name:        "code",
				DisplayName: "Transform",
				Description: "Return the (reference-resolved) input map — a Go data-mapper transform.",
				Props: []PropSpec{
					{Name: "input", Type: "object", Description: "Values, optionally {{step.path}} references to prior outputs."},
				},
				Run: runCode,
			},
			"wait_for_approval": {
				Name:        "wait_for_approval",
				DisplayName: "Wait for Approval",
				Description: "A manual waitpoint. Inside a flow it blocks until a resume signal; invoked directly it approves.",
				Props: []PropSpec{
					{Name: "reason", Type: "string", Description: "Why approval is requested."},
				},
				Run: runWaitForApproval,
			},
		},
		Triggers: map[string]*Trigger{
			"schedule": {
				Name:        "schedule",
				DisplayName: "Schedule",
				Description: "Start the flow on a cron schedule.",
				Strategy:    StrategyPolling,
				Props: []PropSpec{
					{Name: "cron", Type: "string", Required: true, Description: "Cron expression, e.g. \"0 3 * * *\"."},
				},
			},
			"manual": {
				Name:        "manual",
				DisplayName: "Manual",
				Description: "Start the flow only on an explicit /run.",
				Strategy:    StrategyManual,
			},
		},
	})
}

// ── core.http_request ────────────────────────────────────────────────────────

const (
	// maxHTTPBody bounds a fetched response so a hostile/huge upstream can't exhaust
	// memory. 5 MiB is generous for an automation step.
	maxHTTPBody = 5 << 20
	// httpAllowPrivateEnv, when "1", lifts the SSRF guard (private/loopback targets
	// allowed). Off by default and intended ONLY for tests that hit a loopback stub;
	// production leaves it unset, so a flow can never reach cluster-internal or
	// cloud-metadata addresses.
	httpAllowPrivateEnv = "AUTOMATIONS_HTTP_ALLOW_PRIVATE"
)

// ssrfDialer is a net.Dialer whose Control hook runs AFTER DNS resolution, on the
// concrete IP:port about to be connected — so it closes the DNS-rebinding TOCTOU
// window a resolve-then-check would leave open. A non-public target is refused
// unless httpAllowPrivateEnv=="1".
var ssrfDialer = &net.Dialer{
	Timeout: 10 * time.Second,
	Control: func(network, address string, _ syscall.RawConn) error {
		if os.Getenv(httpAllowPrivateEnv) == "1" {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("ssrf: bad address %q", address)
		}
		ip := net.ParseIP(host)
		if ip == nil || !isPublicIP(ip) {
			return fmt.Errorf("ssrf: refusing non-public address %s", host)
		}
		return nil
	},
}

// httpClient is the ONE bounded client core.http_request uses: a real timeout
// (fail-secure — never an infinite hang), the SSRF dialer, and a capped redirect
// chain (each hop is re-checked by the dialer, so a redirect to a private host is
// also refused).
var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:           ssrfDialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DisableKeepAlives:     true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

// specialUseCIDRs are the IANA special-use ranges Go's net helpers do NOT cover.
// Go's IsPrivate is only RFC1918 + ULA; IsLinkLocal* covers 169.254/16 + fe80::/10.
// These add the ranges an SSRF attacker reaches for — notably CGNAT 100.64/10
// (Alibaba's 100.100.100.200 metadata endpoint) and the reserved/benchmark/test
// blocks — so the blocklist is honest about what it stops. Parsed once at init.
var specialUseCIDRs = mustCIDRs(
	"0.0.0.0/8",       // "this host on this network" (RFC 1122)
	"100.64.0.0/10",   // CGNAT (RFC 6598) — Alibaba metadata 100.100.100.200
	"192.0.0.0/24",    // IETF protocol assignments (RFC 6890)
	"192.0.2.0/24",    // TEST-NET-1 (RFC 5737)
	"192.88.99.0/24",  // 6to4 relay anycast (RFC 7526)
	"198.18.0.0/15",   // benchmarking (RFC 2544)
	"198.51.100.0/24", // TEST-NET-2 (RFC 5737)
	"203.0.113.0/24",  // TEST-NET-3 (RFC 5737)
	"240.0.0.0/4",     // reserved / former class E (RFC 1112)
	"64:ff9b::/96",    // NAT64 well-known prefix (RFC 6052)
)

// isPublicIP reports whether ip is a routable PUBLIC address. It rejects loopback,
// private (RFC1918/ULA), link-local, unspecified, multicast (Go's net helpers) PLUS
// the IANA special-use ranges above. This is NOT a claim of a complete cloud-metadata
// blocklist — 169.254.169.254 (AWS/GCP/Azure) is covered as link-local and
// 100.100.100.200 (Alibaba) as CGNAT, but the guard is a general special-use/private
// blocklist, not a per-cloud allowlist. Called by the dialer Control hook on the
// concrete resolved IP, so it also closes the DNS-rebind TOCTOU window.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// Normalize an IPv4-mapped IPv6 (::ffff:a.b.c.d) to its v4 form so a mapped
	// private/special address can't slip past the v4 CIDR checks.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range specialUseCIDRs {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// mustCIDRs parses CIDR literals at init; a bad literal is a programming error.
func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("automations: bad special-use CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}

func runHTTPRequest(ctx context.Context, rc RunContext) (any, error) {
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

func runDelay(ctx context.Context, rc RunContext) (any, error) {
	secs := numInput(rc.Input, "seconds")
	if secs < 0 {
		secs = 0
	}
	d := time.Duration(secs * float64(time.Second))
	if d > maxDelay {
		d = maxDelay
	}
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
func runCode(_ context.Context, rc RunContext) (any, error) {
	if rc.Input == nil {
		return map[string]any{}, nil
	}
	return rc.Input, nil
}

// ── core.wait_for_approval ───────────────────────────────────────────────────

// runWaitForApproval is the DIRECT-invocation body (MCP tools/call). Inside a flow
// the workflow special-cases this step onto a durable "resume" signal and never
// calls Run — so this path represents an immediate, explicit approval.
func runWaitForApproval(_ context.Context, rc RunContext) (any, error) {
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
