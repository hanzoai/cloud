package cloud

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/zip"
)

// mountIAMProxy mounts a same-origin reverse proxy for the identity plane at
// /v1/iam/* → {IAMBase}/v1/iam/* in the single binary.
//
// WHY. IAM (hanzoai/iam) is deliberately NOT fused into the cloud binary — it is
// an isolated control plane (blast-radius isolation; see subsystems). But the
// embedded console is served SAME-ORIGIN, and its auth + identity calls target
// /v1/iam/* on its OWN origin: get-account (session bootstrap), signin/signout,
// the OAuth/OIDC endpoints, and IAM management (get-users / get-organizations /
// get-organization-projects). In production an edge (gateway/Traefik) routes
// /v1/iam to IAM; the single binary has no edge, so it routes /v1/iam itself.
// This is what makes `./cloud` a COMPLETE local cloud — the console can bootstrap
// its session and manage identity against a real IAM, whole surface on one origin.
//
// Target resolution: CLOUD_IAM_URL wins (a local `hanzo iam`, or any IAM); else
// cfg.IAMIssuer (the brand OIDC issuer, e.g. https://hanzo.id — the SAME host the
// binary already validates JWTs against). Empty ⇒ the proxy is not mounted: the
// console still renders, /v1/iam is simply absent (get-account resolves anonymous
// client-side, so the login screen shows). Never fails boot.
//
// FIRST-PARTY SESSION COOKIE. IAM sets its session cookie scoped to ITS host. For a
// same-origin proxy the browser must store that cookie against the CLOUD origin and
// re-send it to /v1 on the same origin — so ModifyResponse strips the Set-Cookie
// `Domain` attribute, turning it into a host-only cookie (STRICTLY more scoped than
// a domain cookie). Secure / HttpOnly / SameSite are preserved untouched. This is
// the one rewrite that makes same-origin auth work without weakening the cookie.
//
// FAIL SECURE. A bounded transport timeout (never infinite); upstream/transport
// errors return a plain 502 and are logged server-side, never leaking IAM internals
// to the client.
func mountIAMProxy(app *zip.App, cfg *Config, log luxlog.Logger) error {
	base := strings.TrimSpace(iamProxyTarget(cfg))
	if base == "" {
		if log != nil {
			log.Info("iam proxy disabled (no CLOUD_IAM_URL / IAM issuer resolved)")
		}
		return nil
	}
	proxy, err := newIAMProxy(base, log)
	if err != nil {
		return fmt.Errorf("iam proxy: %w", err)
	}
	h := zip.AdaptNetHTTP(proxy)
	// Exact head + subtree. Registered before mountConsole so /v1/iam/* wins over
	// the SPA catch-all; iam is not a Registry subsystem, so nothing else claims it.
	app.All("/v1/iam", h)
	app.All("/v1/iam/*", h)
	if log != nil {
		log.Info("iam proxy mounted", "path", "/v1/iam/*", "upstream", base)
	}
	return nil
}

// iamProxyTarget resolves the IAM base URL: CLOUD_IAM_URL wins, else the brand
// OIDC issuer (cfg.IAMIssuer). Returns "" when neither is set.
func iamProxyTarget(cfg *Config) string {
	if v := strings.TrimSpace(getenv("CLOUD_IAM_URL", "")); v != "" {
		return v
	}
	return strings.TrimSpace(cfg.IAMIssuer)
}

// newIAMProxy builds the reverse-proxy handler for the identity plane. Pure (URL
// in, handler out) so it is unit-testable without a live IAM. The upstream path is
// preserved verbatim (/v1/iam/... → {base}/v1/iam/...); only scheme+host+cookie
// domain are rewritten.
func newIAMProxy(rawURL string, log luxlog.Logger) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("invalid IAM url %q: need scheme and host", rawURL)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)              // sets scheme/host to target; path preserved
		r.Host = target.Host // upstream vhost (IAM), not the cloud origin
		// Never forward a client-forged forwarding header; set our own truthfully.
		r.Header.Set("X-Forwarded-Host", r.Header.Get("Host"))
	}

	// Bounded timeouts — fail secure, never hang a request on a slow/absent IAM.
	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	}

	// Make the IAM session cookie first-party to the cloud origin (host-only).
	proxy.ModifyResponse = func(resp *http.Response) error {
		rewriteSetCookieHostOnly(resp.Header)
		return nil
	}

	// Redact upstream/transport errors — a client never sees IAM internals.
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		if log != nil {
			log.Error("iam proxy upstream error", "err", err)
		}
		http.Error(w, "identity service unavailable", http.StatusBadGateway)
	}
	return proxy, nil
}

// rewriteSetCookieHostOnly strips the `Domain=` attribute from every Set-Cookie on
// the response so the cookie binds to the requesting host (the cloud origin) rather
// than IAM's domain. Host-only is strictly more restrictive; Secure / HttpOnly /
// SameSite and all other attributes are left untouched.
func rewriteSetCookieHostOnly(h http.Header) {
	cookies := h.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}
	rewritten := make([]string, 0, len(cookies))
	for _, c := range cookies {
		rewritten = append(rewritten, stripCookieDomain(c))
	}
	h.Del("Set-Cookie")
	for _, c := range rewritten {
		h.Add("Set-Cookie", c)
	}
}

// stripCookieDomain removes a `Domain=...` attribute (case-insensitive) from a
// single Set-Cookie header value, preserving every other attribute and order.
func stripCookieDomain(setCookie string) string {
	parts := strings.Split(setCookie, ";")
	kept := parts[:0]
	for _, p := range parts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(p)), "domain=") {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, ";")
}
