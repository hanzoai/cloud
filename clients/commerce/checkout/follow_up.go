// Deposit follow-up handlers: confirm, status, webhooks.
//
// Shape mirrors Deposits() — tenant resolved from Host, forwarded to
// the tenant's Backend.URL. Each handler pins its own upstream sub-path
// so the proxy layer is the single enforcement point for path
// validation. IDs are taken from gin route params (not the request
// body) and re-escaped for path safety.

package checkout

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// DepositConfirm handles POST /v1/commerce/deposits/:id/confirm. The SPA
// posts the provider-minted token (e.g. Square nonce) back here so BD
// can complete the pre-auth → capture flow. We never touch the provider
// directly from commerce — BD owns that call path and the audit record.
func DepositConfirm(r Resolver, fwd Forwarder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tenant, ok := resolveOr404(w, req, r)
		if !ok {
			return
		}
		if req.Header.Get("Authorization") == "" {
			writeJSONError(w, http.StatusUnauthorized, "authorization required")
			return
		}
		id, ok := routeParam(req, "id")
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "missing deposit id")
			return
		}
		proxyToBackend(w, req, tenant, fwd,
			http.MethodPost,
			"/v1/bd/deposits/"+url.PathEscape(id)+"/confirm",
		)
	})
}

// DepositStatus handles GET /v1/commerce/deposits/:id/status. Returns
// the BD-owned state machine (pending, processing, settled, failed). The
// SPA polls this until terminal or timeout.
func DepositStatus(r Resolver, fwd Forwarder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tenant, ok := resolveOr404(w, req, r)
		if !ok {
			return
		}
		if req.Header.Get("Authorization") == "" {
			writeJSONError(w, http.StatusUnauthorized, "authorization required")
			return
		}
		id, ok := routeParam(req, "id")
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "missing deposit id")
			return
		}
		proxyToBackend(w, req, tenant, fwd,
			http.MethodGet,
			"/v1/bd/deposits/"+url.PathEscape(id)+"/status",
		)
	})
}

// WebhookIntake handles POST /v1/commerce/webhooks/:provider. The
// provider (Square, Braintree, etc.) posts settlement/dispute events
// here. We DO NOT verify the provider's signature in commerce —
// signature keys live in BD + the tenant-scoped KMS secret that BD
// already owns, so we forward the payload + original signature headers
// verbatim so BD can verify with its own tenant-scoped key.
//
// Why not verify here: key rotation races. If commerce cached a stale
// signing key it would reject live webhooks. BD is the only source of
// truth for provider keys; having commerce also hold them would be two
// places to rotate, and two places to forget.
func WebhookIntake(r Resolver) http.Handler {
	fwd := NewHTTPForwarder()
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tenant, ok := resolveOr404(w, req, r)
		if !ok {
			return
		}
		provider, ok := routeParam(req, "provider")
		if !ok || !isKnownProvider(provider) {
			writeJSONError(w, http.StatusNotFound, "unknown provider")
			return
		}
		// Forward to BD's provider-specific webhook intake.
		// URL-escape the provider segment even though we validated it
		// against an allowlist — defense in depth.
		sub := "/v1/bd/webhooks/" + url.PathEscape(provider)
		proxyWebhook(w, req, tenant, fwd, sub)
	})
}

// ─── helpers ────────────────────────────────────────────────────────────

// resolveOr404 is the common tenant-resolve preamble. On failure it
// writes a 404 with no Host echo and returns ok=false.
func resolveOr404(w http.ResponseWriter, req *http.Request, r Resolver) (Tenant, bool) {
	t, err := r.Resolve(req.Host)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "unknown tenant")
		return Tenant{}, false
	}
	return t, true
}

// routeParam extracts a gin route parameter from the request context.
// gin stores params on the context — we read them back without needing
// the gin.Context type so our handlers stay net/http-native and easily
// unit-testable.
func routeParam(req *http.Request, name string) (string, bool) {
	ctx := req.Context()
	if v := ctx.Value(gin.ContextKey); v != nil {
		if gc, ok := v.(*gin.Context); ok {
			if val := gc.Param(name); val != "" {
				return val, true
			}
		}
	}
	// Fallback: many tests and non-gin callers set params via
	// request context value directly.
	if v := ctx.Value(paramsCtxKey(name)); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// paramsCtxKey is a test/helper seam for injecting route params without
// going through gin. Production traffic always reaches us with gin
// params in context.
type paramsCtxKey string

// proxyToBackend forwards req to tenant.Backend.URL + sub with
// Authorization + Content-Type preserved, body copied, and an
// X-Commerce-Tenant header set so BD logs can attribute correctly.
func proxyToBackend(
	w http.ResponseWriter,
	req *http.Request,
	tenant Tenant,
	fwd Forwarder,
	method, sub string,
) {
	if tenant.Backend.URL == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "tenant backend not configured")
		return
	}

	upstreamURL := strings.TrimRight(tenant.Backend.URL, "/") + sub

	var body io.Reader
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "read body")
			return
		}
		body = bytes.NewReader(b)
	}

	up, err := http.NewRequestWithContext(req.Context(), method, upstreamURL, body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "build upstream")
		return
	}
	up.Header.Set("Authorization", req.Header.Get("Authorization"))
	if ct := req.Header.Get("Content-Type"); ct != "" {
		up.Header.Set("Content-Type", ct)
	}
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
}

// proxyWebhook forwards a provider webhook to BD verbatim, preserving
// every header the provider sent (signatures live there). This is the
// one case where we pass headers through un-scrubbed because the
// upstream needs them all for signature verification.
func proxyWebhook(
	w http.ResponseWriter,
	req *http.Request,
	tenant Tenant,
	fwd Forwarder,
	sub string,
) {
	if tenant.Backend.URL == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "tenant backend not configured")
		return
	}

	upstreamURL := strings.TrimRight(tenant.Backend.URL, "/") + sub

	b, err := io.ReadAll(req.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "read body")
		return
	}
	up, err := http.NewRequestWithContext(req.Context(), http.MethodPost, upstreamURL, bytes.NewReader(b))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "build upstream")
		return
	}
	// Forward ALL original headers — provider signatures are in there.
	// The Authorization header (if any) is provider-specific, not a
	// user bearer, so forwarding it is correct.
	for k, vs := range req.Header {
		// Drop hop-by-hop headers that don't belong on the upstream
		// request (connection state, not application payload).
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "proxy-authenticate",
			"proxy-authorization", "te", "trailer", "transfer-encoding",
			"upgrade", "host", "content-length":
			continue
		}
		for _, v := range vs {
			up.Header.Add(k, v)
		}
	}
	up.Header.Set("X-Commerce-Tenant", tenant.Name)

	resp, err := fwd.Forward(up, tenant)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "upstream error")
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// isKnownProvider bounds the :provider URL param to an allowlist so
// path traversal, unicode chicanery, and provider-name typos return a
// clean 404 before we forward anywhere. Extend when a new provider is
// onboarded; an allowlist is the boring, auditable control.
func isKnownProvider(name string) bool {
	switch name {
	case "square", "braintree", "stripe", "paypal":
		return true
	}
	return false
}
