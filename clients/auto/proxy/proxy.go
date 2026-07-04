// Package proxy is the pure, dependency-free reverse-proxy mechanism behind the
// /v1/auto subsystem. It is split from the cloud-registration wrapper (clients/auto)
// so the tenant-boundary behavior — the validated-principal gate and the outbound
// identity re-stamping the header-trusting auto engine depends on — is unit-testable
// WITHOUT linking the cloud root package (which transitively pulls conflicting SQLite
// drivers into a test binary). Separation of concerns: this file is the security
// mechanism; clients/auto only wires it into cloud.Registry.
package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// EngineTrustHeaders are the identity headers the auto engine reads. The proxy
// re-derives them from the gate-validated inbound request so the engine sees only
// server-authoritative values.
var EngineTrustHeaders = []string{"X-Org-Id", "X-User-Id", "X-User-Email"}

// StrippedHeaders are identity aliases an attacker might smuggle that we delete on the
// outbound request (a superset beyond what the engine needs) so nothing identity-ish
// that cloud's SanitizeIdentity didn't set can reach the header-trusting engine.
var StrippedHeaders = []string{
	"X-Roles", "X-User-Permissions", "X-Phone-Number", "X-User-IsAdmin",
	"X-User-Role", "X-User-Roles", "X-User-Name", "X-Tenant-Id", "X-Tenant-ID", "X-Org",
}

// NewHandler builds the reverse-proxy handler targeting the auto engine at rawURL.
// Pure (URL in, handler out). The path is forwarded UNCHANGED: /v1/auto/* maps to
// /v1/auto/* on the engine. The Director re-stamps the outbound identity headers from
// the (gate-validated) inbound values, deleting every identity alias first, so the
// engine — which trusts X-Org-Id absolutely — only ever receives the validated tenant.
func NewHandler(rawURL string) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	base := rp.Director
	rp.Director = func(r *http.Request) {
		org := r.Header.Get("X-Org-Id")
		user := r.Header.Get("X-User-Id")
		email := r.Header.Get("X-User-Email")

		base(r)
		r.Host = target.Host

		for _, h := range EngineTrustHeaders {
			r.Header.Del(h)
		}
		for _, h := range StrippedHeaders {
			r.Header.Del(h)
		}
		r.Header.Set("X-Org-Id", org)
		r.Header.Set("X-User-Id", user)
		if email != "" {
			r.Header.Set("X-User-Email", email)
		}
	}
	return rp, nil
}

// Gate refuses any request with no validated principal (empty X-User-Id, the signal
// cloud's SanitizeIdentity sets only from a verified credential) before it reaches the
// header-trusting engine. This closes the anonymous-forge path (a client-restored
// X-Org-Id with no credential) that would otherwise drive a victim org's workflows.
func Gate(next http.Handler) http.Handler {
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
