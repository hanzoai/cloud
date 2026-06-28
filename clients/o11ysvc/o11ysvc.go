// Package o11ysvc initializes the o11y subsystem's runtime handler in the
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
package o11ysvc

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/o11y"
	"github.com/hanzoai/zip"
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

// prefix is the public route surface hanzoai/o11y mounts; the o11y runtime's
// own controllers live under /api. This is the registered handler, so it owns
// the documented /v1/o11y/* -> /api/* rewrite (see o11y mount.go).
const prefix = "/v1/o11y"

// rewritePath maps a public /v1/o11y/* path to the runtime's /api/* path.
// /v1/o11y/v3/query_range -> /api/v3/query_range ; /v1/o11y -> /api.
func rewritePath(p string) string { return "/api" + strings.TrimPrefix(p, prefix) }

// newHandler builds the reverse-proxy handler targeting the o11y runtime. Pure
// (URL in, handler out) so it is unit-testable without a live upstream.
func newHandler(rawURL string) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)                            // sets scheme/host to target
		r.URL.Path = rewritePath(r.URL.Path) // /v1/o11y/* -> /api/*
		r.URL.RawPath = ""                 // force re-encode from Path
		r.Host = target.Host               // upstream vhost, not api.hanzo.ai
	}
	return proxy, nil
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
		o11y.SetHandler(h)
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
