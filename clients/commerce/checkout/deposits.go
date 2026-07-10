// Deposits is the POST /v1/commerce/deposits handler. The checkout SPA
// POSTs here to create a deposit intent; we resolve the tenant from the
// Host header, then forward to the tenant's configured backend. For
// broker-dealer tenants, Backend.Kind="bd" and Backend.URL points at
// the BD service, which owns the deposit-intent lifecycle.
//
// Why a proxy and not a local implementation: commerce does not own
// deposit state for these tenants — the BD service does, and is the
// source of truth per the on-chain trading architecture. Re-implementing
// here would split the record and invite drift.

package checkout

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Forwarder wraps "given this request and this tenant, produce a
// response". The production Forwarder is a net/http.Client wired to the
// tenant's Backend.URL; tests substitute a ForwarderFunc.
type Forwarder interface {
	Forward(req *http.Request, tenant Tenant) (*http.Response, error)
}

// ForwarderFunc adapts a function to the Forwarder interface.
type ForwarderFunc func(req *http.Request, tenant Tenant) (*http.Response, error)

// Forward calls f.
func (f ForwarderFunc) Forward(req *http.Request, tenant Tenant) (*http.Response, error) {
	return f(req, tenant)
}

// Deposits handles POST /v1/commerce/deposits. Preconditions:
//  1. Host resolves to a known tenant (404 if not).
//  2. Authorization header is present (401 if not — IAM middleware at
//     the commerce router will re-validate the JWT; we only enforce
//     presence here to fail fast before forwarding anywhere).
//  3. Tenant.Backend.URL is configured (503 if not — fail closed, never
//     fall back to a default).
//
// On success the upstream response is streamed back to the client
// verbatim so the SPA can consume { id, provider, clientToken, ... }.
func Deposits(r Resolver, fwd Forwarder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tenant, err := r.Resolve(req.Host)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "unknown tenant")
			return
		}
		if req.Header.Get("Authorization") == "" {
			writeJSONError(w, http.StatusUnauthorized, "authorization required")
			return
		}
		if tenant.Backend.URL == "" {
			writeJSONError(w, http.StatusServiceUnavailable, "tenant backend not configured")
			return
		}

		// Build an upstream request. We force the URL to the tenant's
		// Backend.URL — never the Host header — so a spoofed Host cannot
		// redirect the forwarder to an attacker-controlled URL.
		upstreamURL := strings.TrimRight(tenant.Backend.URL, "/") + "/v1/bd/deposits"
		body, err := io.ReadAll(req.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "read body")
			return
		}
		up, err := http.NewRequestWithContext(req.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "build upstream")
			return
		}
		// Forward the Authorization header + content type; drop
		// everything else so cookies, custom headers, etc. do not leak
		// to the backend.
		up.Header.Set("Authorization", req.Header.Get("Authorization"))
		up.Header.Set("Content-Type", req.Header.Get("Content-Type"))
		up.Header.Set("X-Commerce-Tenant", tenant.Name)

		resp, err := fwd.Forward(up, tenant)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "upstream error")
			return
		}
		defer resp.Body.Close()

		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

// writeJSONError emits a compact JSON error. Intentionally does NOT echo
// the Host back so attackers cannot use error responses to fingerprint
// tenant existence.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
