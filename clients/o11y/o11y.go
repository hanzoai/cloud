// Package o11y initializes the o11y subsystem's runtime handler in the
// unified cloud binary.
//
// hanzoai/o11y registers the /v1/o11y/* route surface (order 70) but delegates
// every request to a handler installed via o11y.SetHandler. The standalone
// o11y cmd/server constructs the full runtime in-process and installs its own
// PublicHandler. The cloud binary does NOT construct that heavy runtime
// (telemetry stores, rule manager, opamp, websockets) a second time — a
// dedicated o11y Deployment already runs it. Instead, cloud installs a reverse
// proxy to that deployment as the handler, so /v1/o11y/* serves real telemetry
// instead of the "o11y runtime not initialized" 503.
//
// Path is preserved verbatim: /v1/o11y/* is forwarded unchanged to the o11y
// runtime, which rewrites /v1/o11y/* -> /api/* internally (see o11y
// app.createPublicServer). The gateway terminates auth and propagates identity
// as X-* headers, which the proxy forwards.
package o11y

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/o11y"
	"github.com/zap-proto/zip"
)

// defaultUpstream is the in-cluster address of the o11y runtime Deployment's
// Service (port 80 -> container 8080). Overridable via O11Y_UPSTREAM.
const defaultUpstream = "http://o11y.hanzo.svc.cluster.local:80"

func upstream() string {
	if v := strings.TrimSpace(os.Getenv("O11Y_UPSTREAM")); v != "" {
		return v
	}
	return defaultUpstream
}

// newHandler builds the reverse-proxy handler targeting the o11y runtime. Pure
// (URL in, handler out) so it is unit-testable without a live upstream.
//
// The path is forwarded UNCHANGED: the o11y runtime registers its routes at
// their exact public path (/v1/o11y/<version>/<path>) — no /api/, no rewrite.
// One and one way: the route IS the path, on both sides of this proxy.
func newHandler(rawURL string) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)              // sets scheme/host to target; path unchanged
		r.Host = target.Host // upstream vhost, not api.hanzo.ai
	}
	return proxy, nil
}

// gate refuses any request that carries no validated principal before it reaches
// the o11y runtime. The bare reverse proxy forwards ALL inbound headers upstream,
// including a client-forged X-Org-Id restored on the bearer-less path; without
// this an off-gateway caller reads another tenant's telemetry/logs. X-User-Id is
// set ONLY by the identity middleware from a verified credential (the same signal
// principal.Validated uses), so its presence is the authoritative principal gate.
// Every legitimate /v1/o11y/* caller arrives through the console BFF with a
// user-bound bearer, so this refuses only the anonymous-forge path.
func gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-User-Id")) == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"error","msg":"no validated principal"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func init() {
	// Order 71: after o11y.Mount (70) installs the route surface. Ordering is not
	// strictly required (the handler is resolved per-request) but keeps the
	// runtime install adjacent to its routes.
	cloud.Register("o11y-runtime", 71, func(_ any, deps cloud.Deps) error {
		h, err := newHandler(upstream())
		if err != nil {
			return err
		}
		o11y.SetHandler(gate(h))
		if deps.Logger != nil {
			deps.Logger.New("subsystem", "o11y-runtime").
				Info("o11y runtime handler installed (reverse proxy)", "upstream", upstream())
		}
		return nil
	})
}

// Mount is a no-op kept for symmetry with other subsystems; the handler is
// installed in init via cloud.Register. It satisfies callers that look for a
// Mount(app, deps) entrypoint.
func Mount(_ *zip.App, _ cloud.Deps) error { return nil }
