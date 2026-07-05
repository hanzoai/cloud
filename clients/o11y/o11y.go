// Package o11y initializes the o11y subsystem's runtime handler in the
// unified cloud binary.
//
// hanzoai/o11y registers the /v1/o11y/* route surface (order 70) but delegates
// every request to a handler installed via o11y.SetHandler. The standalone o11y
// cmd/server constructs the full runtime in-process and installs its own
// PublicHandler. The cloud binary now does the SAME thing: it constructs that
// runtime IN-PROCESS (embed.go — telemetry stores over the ClickHouse datastore,
// sqlstore, querier, rule manager, dashboards, alerts) and installs
// server.PublicHandler, so /v1/o11y/* is served by THIS binary and the standalone
// o11y Deployment can retire.
//
// One way, two backings (this file's Register callback):
//   - PRIMARY: the in-process runtime (buildEmbeddedHandler), enabled by
//     O11Y_TELEMETRYSTORE_DATASTORE_DSN. Serves telemetry from cloud itself.
//   - FALLBACK: a reverse proxy to a still-running o11y Deployment, used only when
//     the embed is disabled (no DSN) or fails to init. Fail-soft, zero downtime.
//
// Path is preserved verbatim: /v1/o11y/* reaches the o11y runtime unchanged,
// which rewrites /v1/o11y/* -> /api/* internally (see o11y app.createPublicServer).
// The gateway terminates auth and propagates identity as X-* headers; the runtime
// (embedded) or the proxy (fallback) sees the same request.
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
	// strictly required (the handler is resolved per-request) but keeps the runtime
	// install adjacent to its routes. Runs during MountAll — before Listen — so the
	// handler is in place before the first request.
	//
	// ONE way, two backings: prefer the in-process runtime (embed.go) that serves
	// /v1/o11y/* from THIS binary against the ClickHouse datastore, so the standalone
	// o11y Deployment can retire. Fall back to reverse-proxying that Deployment when
	// the embed is disabled (no DSN) or fails to init — fail-soft, zero downtime.
	cloud.Register("o11y-runtime", 71, func(_ any, deps cloud.Deps) error {
		log := deps.Logger.New("subsystem", "o11y-runtime")

		if h, err := buildEmbeddedHandler(deps); err != nil {
			log.Warn("embedded o11y init failed; falling back to reverse proxy", "err", err)
		} else if h != nil {
			o11y.SetHandler(gate(h))
			log.Info("o11y runtime handler installed (in-process runtime)")
			return nil
		}

		// Fallback: reverse-proxy the standalone o11y Deployment.
		h, err := newHandler(upstream())
		if err != nil {
			return err
		}
		o11y.SetHandler(gate(h))
		log.Info("o11y runtime handler installed (reverse proxy fallback)", "upstream", upstream())
		return nil
	})
}

// Mount is a no-op kept for symmetry with other subsystems; the handler is
// installed in init via cloud.Register. It satisfies callers that look for a
// Mount(app, deps) entrypoint.
func Mount(_ *zip.App, _ cloud.Deps) error { return nil }
