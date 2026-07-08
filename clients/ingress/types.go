package ingress

import (
	"fmt"
	"net/url"
	"strings"
)

// The config model speaks the Traefik dynamic-config vocabulary hanzoai/ingress
// (the Traefik-lineage fleet edge) uses — routers → services → middlewares —
// reduced to the honest minimum the cloud edge needs and made runtime-editable
// over /v1/ingress. A Route is a Traefik router (a Host rule → a Service through
// an ordered middleware chain, optionally terminating TLS); a Service is a
// weighted round-robin backend pool (oxy/v2); a Middleware is one edge transform.
// There is no static routes.yaml — the values below ARE the config, persisted
// per-tenant and hot-applied.

const (
	MaxHostLen     = 253 // RFC 1035 max FQDN length
	MaxIDLen       = 128
	MaxBackends    = 32
	MaxMiddlewares = 16
	MaxExtraHosts  = 256
)

// Kinds are the object namespaces in the store and the /v1/ingress path segments.
const (
	KindRoute      = "route"
	KindService    = "service"
	KindMiddleware = "middleware"
)

// Route is a Traefik-style router: an exact Host (and optional PathPrefix) rule
// that dispatches to Service through the ordered Middlewares chain, optionally
// terminating TLS (an ACME-managed certificate) for Host.
type Route struct {
	ID          string   `json:"id"`
	Host        string   `json:"host"`
	PathPrefix  string   `json:"pathPrefix,omitempty"`
	Service     string   `json:"service"`
	Middlewares []string `json:"middlewares,omitempty"`
	TLS         bool     `json:"tls,omitempty"`
	Priority    int      `json:"priority,omitempty"`
}

// Service is a Traefik-style load-balanced backend pool. Backends are weighted
// round-robin members (github.com/vulcand/oxy/v2 roundrobin).
type Service struct {
	ID             string    `json:"id"`
	Backends       []Backend `json:"backends"`
	PassHostHeader bool      `json:"passHostHeader,omitempty"`
}

// Backend is one upstream server URL with a round-robin weight (default 1). The
// URL can point at another cloud instance, an in-cluster service, or the local
// app's own listen address (loopback) — the edge just proxies to a URL.
type Backend struct {
	URL    string `json:"url"`
	Weight int    `json:"weight,omitempty"`
}

// Middleware is one edge transform applied before a route reaches its service.
type Middleware struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Config map[string]string `json:"config,omitempty"`
}

// Middleware types — the common edge middleware, each a small http.Handler wrap.
const (
	MWRedirectScheme = "redirectScheme" // config: scheme (default https), permanent (=true → 301)
	MWStripPrefix    = "stripPrefix"    // config: prefixes (comma-separated)
	MWAddPrefix      = "addPrefix"      // config: prefix
	MWHeaders        = "headers"        // config: header→value (set on the response)
)

// TLSConfig is a deployment's ACME intent, persisted per-org. ACMEEmail + Staging
// bind an ACME account for the lifetime of an edge process and are applied when
// the edge (re)starts; ExtraHosts feed the ACME HostPolicy and hot-apply on
// reload alongside the per-route TLS flags. This split is honest: an ACME account
// is not a per-request knob, but WHICH hosts get certs is.
type TLSConfig struct {
	ACMEEmail  string   `json:"acmeEmail,omitempty"`
	Staging    bool     `json:"staging,omitempty"`
	ExtraHosts []string `json:"extraHosts,omitempty"`
}

// ── validation (the ingest boundary) ─────────────────────────────────────────

// validID accepts a non-empty [a-z0-9-_.] identifier within MaxIDLen. It is the
// gate for every object id, service ref, and middleware ref, so an id can never
// smuggle path structure into a store key or a routing lookup.
func validID(s string) bool {
	if s == "" || len(s) > MaxIDLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// normalizeHost lowercases, trims, and drops a trailing dot so a host is a
// canonical routing key. It performs no validation — validHost does.
func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// hostOnly strips a :port suffix from a Host header value ("app.test:443" →
// "app.test"), leaving a bare host for the routing/TLS lookup.
func hostOnly(h string) string {
	h = strings.TrimSpace(h)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		return h[:i]
	}
	return h
}

// validHost reports whether h is a plausible DNS host: non-empty, within
// MaxHostLen, no scheme/path/port/space, dot-separated non-empty labels.
func validHost(h string) bool {
	if h == "" || len(h) > MaxHostLen {
		return false
	}
	if strings.ContainsAny(h, "/:\\ \t") || strings.Contains(h, "..") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" {
			return false
		}
	}
	return true
}

func (r *Route) validate() error {
	if !validID(r.ID) {
		return fmt.Errorf("route: invalid id %q", r.ID)
	}
	r.Host = normalizeHost(r.Host)
	if !validHost(r.Host) {
		return fmt.Errorf("route: invalid host %q", r.Host)
	}
	if !validID(r.Service) {
		return fmt.Errorf("route: invalid service ref %q", r.Service)
	}
	if r.PathPrefix != "" && !strings.HasPrefix(r.PathPrefix, "/") {
		return fmt.Errorf("route: pathPrefix must start with '/' (%q)", r.PathPrefix)
	}
	if len(r.Middlewares) > MaxMiddlewares {
		return fmt.Errorf("route: too many middlewares (%d > %d)", len(r.Middlewares), MaxMiddlewares)
	}
	for _, m := range r.Middlewares {
		if !validID(m) {
			return fmt.Errorf("route: invalid middleware ref %q", m)
		}
	}
	return nil
}

func (s *Service) validate() error {
	if !validID(s.ID) {
		return fmt.Errorf("service: invalid id %q", s.ID)
	}
	if len(s.Backends) == 0 || len(s.Backends) > MaxBackends {
		return fmt.Errorf("service: backends must be 1..%d (got %d)", MaxBackends, len(s.Backends))
	}
	for _, b := range s.Backends {
		u, err := url.Parse(strings.TrimSpace(b.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("service: backend url must be http(s)://host[:port] (%q)", b.URL)
		}
		if b.Weight < 0 {
			return fmt.Errorf("service: backend weight must be >= 0 (%d)", b.Weight)
		}
	}
	return nil
}

func (m *Middleware) validate() error {
	if !validID(m.ID) {
		return fmt.Errorf("middleware: invalid id %q", m.ID)
	}
	switch m.Type {
	case MWRedirectScheme, MWHeaders:
		// no required config (redirectScheme defaults to https; headers may be empty)
	case MWStripPrefix:
		if strings.TrimSpace(m.Config["prefixes"]) == "" {
			return fmt.Errorf("middleware %q: stripPrefix requires config.prefixes", m.ID)
		}
	case MWAddPrefix:
		if strings.TrimSpace(m.Config["prefix"]) == "" {
			return fmt.Errorf("middleware %q: addPrefix requires config.prefix", m.ID)
		}
	default:
		return fmt.Errorf("middleware %q: unknown type %q", m.ID, m.Type)
	}
	return nil
}

func (t *TLSConfig) normalize() error {
	if len(t.ExtraHosts) > MaxExtraHosts {
		return fmt.Errorf("tls: too many extraHosts (%d > %d)", len(t.ExtraHosts), MaxExtraHosts)
	}
	for i, h := range t.ExtraHosts {
		nh := normalizeHost(h)
		if !validHost(nh) {
			return fmt.Errorf("tls: invalid extraHost %q", h)
		}
		t.ExtraHosts[i] = nh
	}
	return nil
}
