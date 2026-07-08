package ingress

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"

	luxlog "github.com/luxfi/log"
	"github.com/vulcand/oxy/v2/forward"
	"github.com/vulcand/oxy/v2/roundrobin"
)

// Engine is the runtime edge router: a lock-free atomic snapshot of the compiled
// host table, hot-swapped on every config change. apply recompiles the whole
// table and atomically replaces the pointer, so a config change takes effect with
// no restart and no per-request lock. ServeHTTP is the edge data plane; TLSHost
// feeds the ACME HostPolicy. The Engine is transport-agnostic — the same handler
// serves the :443 TLS listener and the :80 HTTP listener.
type Engine struct {
	cur atomic.Pointer[compiled]
	log luxlog.Logger
}

// compiled is one immutable routing snapshot.
type compiled struct {
	byHost   map[string][]compiledRoute // host → routes, sorted by priority then prefix length
	tlsHosts map[string]struct{}        // hosts the edge terminates TLS for (ACME HostPolicy)
}

type compiledRoute struct {
	pathPrefix string
	priority   int
	handler    http.Handler
}

func newEngine(log luxlog.Logger) *Engine {
	e := &Engine{log: log}
	e.cur.Store(&compiled{byHost: map[string][]compiledRoute{}, tlsHosts: map[string]struct{}{}})
	return e
}

// apply recompiles the routing table from the current config and atomically swaps
// it in — the hot-reload seam. A route referencing an unknown/failed service or a
// bad middleware chain is SKIPPED (and counted), so one malformed object can never
// take the whole edge down. Returns (live, skipped) route counts.
func (e *Engine) apply(routes []Route, services map[string]Service, mws map[string]Middleware, tlsHosts map[string]struct{}) (live, skipped int) {
	built := make(map[string]http.Handler, len(services))
	c := &compiled{byHost: map[string][]compiledRoute{}, tlsHosts: tlsHosts}

	for _, r := range routes {
		svc, ok := services[r.Service]
		if !ok {
			skipped++
			e.log.Warn("ingress: route skipped (unknown service)", "route", r.ID, "service", r.Service)
			continue
		}
		h, ok := built[r.Service]
		if !ok {
			bh, err := buildService(svc)
			if err != nil {
				skipped++
				e.log.Warn("ingress: route skipped (service build failed)", "route", r.ID, "service", r.Service, "err", err)
				continue
			}
			built[r.Service] = bh
			h = bh
		}
		chain, ok := buildChain(h, r.Middlewares, mws)
		if !ok {
			skipped++
			e.log.Warn("ingress: route skipped (bad middleware chain)", "route", r.ID)
			continue
		}
		host := normalizeHost(r.Host)
		c.byHost[host] = append(c.byHost[host], compiledRoute{pathPrefix: r.PathPrefix, priority: r.Priority, handler: chain})
		live++
	}

	// Within a host, most specific wins: higher priority first, then longer path
	// prefix. Stable so equal keys keep insertion (store) order.
	for _, rs := range c.byHost {
		sort.SliceStable(rs, func(i, j int) bool {
			if rs[i].priority != rs[j].priority {
				return rs[i].priority > rs[j].priority
			}
			return len(rs[i].pathPrefix) > len(rs[j].pathPrefix)
		})
	}

	e.cur.Store(c)
	return live, skipped
}

// ServeHTTP is the edge: it routes by Host to the first matching route's handler
// chain (which reverse-proxies to the service's backend pool). No route → 404.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := e.cur.Load()
	host := normalizeHost(hostOnly(r.Host))
	for _, cr := range c.byHost[host] {
		if cr.pathPrefix == "" || strings.HasPrefix(r.URL.Path, cr.pathPrefix) {
			cr.handler.ServeHTTP(w, r)
			return
		}
	}
	http.Error(w, "ingress: no route for host "+host, http.StatusNotFound)
}

// TLSHost reports whether the edge terminates TLS (issues an ACME cert) for host.
// It is the autocert HostPolicy predicate — certs are issued ONLY for configured
// hosts, never for arbitrary SNI.
func (e *Engine) TLSHost(host string) bool {
	_, ok := e.cur.Load().tlsHosts[normalizeHost(hostOnly(host))]
	return ok
}

// counts reports the live host and TLS-host totals for /v1/ingress/status.
func (e *Engine) counts() (hosts, tlsHosts int) {
	c := e.cur.Load()
	return len(c.byHost), len(c.tlsHosts)
}

// buildService compiles a Service into an oxy/v2 weighted round-robin load
// balancer over its backends. forward.New returns a pre-configured
// httputil.ReverseProxy; roundrobin picks a backend per request and rewrites the
// outbound URL. This is the Traefik service/loadBalancer model, reused verbatim.
func buildService(svc Service) (http.Handler, error) {
	fwd := forward.New(svc.PassHostHeader)
	lb, err := roundrobin.New(fwd)
	if err != nil {
		return nil, err
	}
	for _, b := range svc.Backends {
		u, err := url.Parse(strings.TrimSpace(b.URL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("bad backend url %q", b.URL)
		}
		weight := b.Weight
		if weight <= 0 {
			weight = 1
		}
		if err := lb.UpsertServer(u, roundrobin.Weight(weight)); err != nil {
			return nil, fmt.Errorf("upsert backend %q: %w", b.URL, err)
		}
	}
	return lb, nil
}

// buildChain wraps the service handler in the route's ordered middleware chain
// (outermost = first listed). Returns ok=false if any middleware id is unknown or
// fails to build, so the caller skips the whole route rather than serving it with
// a partial chain.
func buildChain(h http.Handler, ids []string, mws map[string]Middleware) (http.Handler, bool) {
	for i := len(ids) - 1; i >= 0; i-- {
		mw, ok := mws[ids[i]]
		if !ok {
			return nil, false
		}
		wrap, err := buildMiddleware(mw)
		if err != nil {
			return nil, false
		}
		h = wrap(h)
	}
	return h, true
}
