package ingress

import (
	"fmt"
	"net/url"
	"slices"
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
	// ID identifies the route within the org: [A-Za-z0-9-_.], at most 128 chars.
	// A create that omits it gets a generated one.
	ID string `json:"id"`
	// Host is the exact hostname this route matches, lowercased with any trailing
	// dot stripped. It is a GLOBALLY unique claim — one route across the whole
	// edge may hold a host, so no tenant can hijack another's.
	Host string `json:"host"`
	// PathPrefix narrows the match to requests under this path; it must start
	// with "/". Empty matches every path on the host.
	PathPrefix string `json:"pathPrefix,omitempty"`
	// Service is the id of the backend pool this route dispatches to. A route
	// naming a service that does not exist is skipped at compile, not served.
	Service string `json:"service"`
	// Middlewares are the ids of the edge transforms to apply, in this order,
	// before the request reaches the service. At most 16.
	Middlewares []string `json:"middlewares,omitempty"`
	// TLS asks the edge to terminate TLS for Host with an ACME-managed certificate.
	TLS bool `json:"tls,omitempty"`
	// Priority orders routes that share a host: higher wins, and equal priorities
	// fall back to the longer PathPrefix.
	Priority int `json:"priority,omitempty"`
}

// Upstream is a Traefik-style load-balanced backend pool. Backends are weighted
// round-robin members (github.com/vulcand/oxy/v2 roundrobin).
type Upstream struct {
	// ID identifies the pool within the org: [A-Za-z0-9-_.], at most 128 chars.
	// A create that omits it gets a generated one. Routes reference it by this id.
	ID string `json:"id"`
	// Backends are the upstream servers to balance across: 1..32 of them.
	Backends []Backend `json:"backends"`
	// PassHostHeader forwards the client's original Host header upstream instead
	// of rewriting it to the backend's.
	PassHostHeader bool `json:"passHostHeader,omitempty"`
}

// Backend is one upstream server URL with a round-robin weight (default 1). The
// URL can point at another cloud instance, an in-cluster service, or the local
// app's own listen address (loopback) — the edge just proxies to a URL.
type Backend struct {
	// URL is the upstream server, http(s)://host[:port].
	URL string `json:"url"`
	// Weight is this member's share of the round-robin; must be >= 0.
	Weight int `json:"weight,omitempty"`
}

// Middleware is one edge transform applied before a route reaches its service.
type Middleware struct {
	// ID identifies the transform within the org: [A-Za-z0-9-_.], at most 128
	// chars. A create that omits it gets a generated one. Routes reference it by
	// this id.
	ID string `json:"id"`
	// Type is the transform: redirectScheme, stripPrefix, addPrefix or headers.
	Type string `json:"type"`
	// Config is the transform's parameters: redirectScheme takes scheme (default
	// https) and permanent ("true" ⇒ 301, else 302); stripPrefix REQUIRES
	// prefixes (comma-separated, first match wins); addPrefix REQUIRES prefix;
	// headers is a header→value map set on the response.
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
	// ACMEEmail is the ACME account email. It binds an account for the lifetime
	// of an edge process, so it applies only when the edge (re)starts.
	ACMEEmail string `json:"acmeEmail,omitempty"`
	// Staging issues from Let's Encrypt's staging directory (untrusted certs, high
	// rate limits). Like ACMEEmail it applies only when the edge (re)starts.
	Staging bool `json:"staging,omitempty"`
	// ExtraHosts get certificates without owning a route — at most 256. They feed
	// the ACME HostPolicy and hot-apply on the next reload.
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
	return !slices.Contains(strings.Split(h, "."), "")
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

func (s *Upstream) validate() error {
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
