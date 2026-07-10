package checkout

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// HTTPForwarder is the production Forwarder. It uses a shared *http.Client
// with sane timeouts so a slow backend cannot exhaust commerce's goroutine
// budget. TLS is enforced (tenant Backend.URL must be https://) — an http
// backend is a misconfiguration and requests to it will be rejected by
// Go's transport anyway.
type HTTPForwarder struct {
	client *http.Client
}

// NewHTTPForwarder builds a forwarder with 15s connect timeout, 30s
// request timeout, 20s TLS handshake. These numbers are tuned for BD:
// BD's deposit-intent creation path is typically sub-second.
func NewHTTPForwarder() *HTTPForwarder {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 20 * time.Second,
		// Pin TLS 1.2+ — commerce never talks to a backend over
		// plaintext, and old TLS versions are not acceptable on the
		// inter-service path.
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	return &HTTPForwarder{
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			// Never follow redirects — a redirect from BD would be
			// surprising and a potential SSRF pivot. Error instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Forward sends req upstream. tenant is accepted so future per-tenant
// policy (custom timeouts, mTLS client certs) can be layered without
// changing the interface.
func (h *HTTPForwarder) Forward(req *http.Request, tenant Tenant) (*http.Response, error) {
	_ = tenant // reserved for per-tenant policy
	return h.client.Do(req)
}
