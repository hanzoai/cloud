package ingress

import (
	"fmt"
	"net/http"
	"strings"
)

// buildMiddleware compiles one Middleware into an http.Handler wrapper. Each is
// the common edge transform, runtime-configurable — the orthogonal middleware
// primitives an edge needs, nothing more.
func buildMiddleware(m Middleware) (func(http.Handler) http.Handler, error) {
	switch m.Type {
	case MWRedirectScheme:
		return redirectSchemeMW(m.Config), nil
	case MWStripPrefix:
		return stripPrefixMW(m.Config)
	case MWAddPrefix:
		return addPrefixMW(m.Config)
	case MWHeaders:
		return headersMW(m.Config), nil
	default:
		return nil, fmt.Errorf("unknown middleware type %q", m.Type)
	}
}

// redirectSchemeMW redirects any request not already on the target scheme (default
// https) — the standard http→https edge redirect. permanent=true → 301, else 302.
func redirectSchemeMW(cfg map[string]string) func(http.Handler) http.Handler {
	scheme := strings.TrimSpace(cfg["scheme"])
	if scheme == "" {
		scheme = "https"
	}
	code := http.StatusFound
	if cfg["permanent"] == "true" {
		code = http.StatusMovedPermanently
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(requestScheme(r), scheme) {
				next.ServeHTTP(w, r)
				return
			}
			http.Redirect(w, r, scheme+"://"+r.Host+r.URL.RequestURI(), code)
		})
	}
}

// stripPrefixMW removes the first matching prefix from the request path before it
// reaches the backend (Traefik stripPrefix). It rewrites both URL.Path and
// RequestURI so oxy's forwarder — which reconstructs the outbound path from
// RequestURI — proxies the stripped path.
func stripPrefixMW(cfg map[string]string) (func(http.Handler) http.Handler, error) {
	prefixes := splitCSV(cfg["prefixes"])
	if len(prefixes) == 0 {
		return nil, fmt.Errorf("stripPrefix: config.prefixes required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range prefixes {
				if strings.HasPrefix(r.URL.Path, p) {
					rewritePath(r, ensureLeadingSlash(strings.TrimPrefix(r.URL.Path, p)))
					break
				}
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// addPrefixMW prepends a fixed prefix to the request path (Traefik addPrefix).
func addPrefixMW(cfg map[string]string) (func(http.Handler) http.Handler, error) {
	prefix := strings.TrimSpace(cfg["prefix"])
	if prefix == "" {
		return nil, fmt.Errorf("addPrefix: config.prefix required")
	}
	prefix = "/" + strings.Trim(prefix, "/")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rewritePath(r, prefix+ensureLeadingSlash(r.URL.Path))
			next.ServeHTTP(w, r)
		})
	}, nil
}

// headersMW sets edge response headers (e.g. HSTS, X-Frame-Options) from the
// config map. Backend-supplied values, if any, are appended after these.
func headersMW(cfg map[string]string) func(http.Handler) http.Handler {
	// Snapshot so a later config mutation can't race the compiled handler.
	hdrs := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if k != "" {
			hdrs[k] = v
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range hdrs {
				w.Header().Set(k, v)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requestScheme resolves the effective scheme of an inbound request: TLS
// termination → https, else the X-Forwarded-Proto hint, else http.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		return p
	}
	return "http"
}

// rewritePath sets the request path, keeping URL.Path and RequestURI in sync so
// the oxy forwarder proxies the rewritten path (it reconstructs the outbound
// target from RequestURI, which survives roundrobin's URL replacement).
func rewritePath(r *http.Request, newPath string) {
	r.URL.Path = newPath
	r.URL.RawPath = ""
	if r.URL.RawQuery != "" {
		r.RequestURI = newPath + "?" + r.URL.RawQuery
	} else {
		r.RequestURI = newPath
	}
}

func ensureLeadingSlash(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		return "/" + p
	}
	return p
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
