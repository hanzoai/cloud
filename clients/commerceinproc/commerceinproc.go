// Package commerceinproc is the ONE seam that lets every cloud subsystem that
// speaks the commerce billing S2S surface (clients/{billing,account,admin,
// referrals,authors,affiliates,usage} + the request-edge metering gate in build.go)
// reach the co-resident, in-process commerce handler with a DIRECT Go call instead
// of an HTTP hop to the standalone commerce pod (CLOUD_COMMERCE_HTTP_URL,
// commerce.hanzo.svc:8001). This is what lets the standalone be RETIRED (task #111):
// once commerce is folded in-process (#114), every /v1/billing read/write + the
// metering debit routes to the same in-tree gin engine that /v1/commerce already does.
//
// HOW. commerce.Mount registers the embedded commerce http.Handler here via
// SetHandler once, at boot. A subsystem builds its S2S HTTP request EXACTLY as
// before (same path, same `Authorization: Bearer <COMMERCE_SERVICE_TOKEN>`, same
// server-pinned `X-Org-Id`) and sends it through Transport(): when the handler is
// co-resident the request is dispatched to it in-process (httptest recorder — no
// socket, no network, no serialization change); when it is NOT (commerce staged /
// split-deploy) the request falls back to plain HTTP to its URL — the pre-#111
// behavior, unchanged. So the SAME request, headers, body, status and body bytes
// flow either way — the transport swap is behaviour-preserving, which is exactly
// what the money-parity gate proves.
//
// DE-COMPLECTED: subsystems keep their own request-building + subject-pinning
// (the tenant-isolation security boundary stays where it is); this package owns ONLY
// "where does the byte stream go", nothing about auth or scope.
package commerceinproc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"
)

// PlaceholderBase is the sentinel commerce base URL used when commerce is
// co-resident and CLOUD_COMMERCE_HTTP_URL is unset (the post-retirement steady
// state). The self-routing Transport ignores the host and dispatches by PATH, so
// the value only has to be a parseable absolute URL; it is never dialed.
const PlaceholderBase = "http://commerce.inproc"

// handler holds the embedded commerce http.Handler. atomic so SetHandler (boot) and
// RoundTrip (every request) race-free without a mutex on the hot path.
var handler atomic.Pointer[http.Handler]

// SetHandler publishes the in-process commerce handler. Called once by
// commerce.Mount after commerce.Embed returns its gin handler. Passing nil
// un-publishes (used by tests).
func SetHandler(h http.Handler) {
	if h == nil {
		handler.Store(nil)
		return
	}
	handler.Store(&h)
}

// Available reports whether commerce is co-resident (a handler is published). A
// subsystem uses this to keep its "configured" gate true even when
// CLOUD_COMMERCE_HTTP_URL is unset — the standalone is gone but commerce still
// answers, in-process.
func Available() bool { return handler.Load() != nil }

// BaseURL returns env when non-empty, else — when commerce is co-resident — the
// in-process PlaceholderBase, else "". This lets a subsystem drop its dependence on
// CLOUD_COMMERCE_HTTP_URL: once the standalone is retired and the env is removed,
// the base becomes the placeholder and requests still route in-process.
func BaseURL(env string) string {
	if e := strings.TrimSpace(env); e != "" {
		return e
	}
	if Available() {
		return PlaceholderBase
	}
	return ""
}

// roundTripper dispatches to the in-process commerce handler when one is published,
// else to fallback (plain HTTP). The gin engine routes on req.URL.Path, so the host
// in the (placeholder or real) base is irrelevant when co-resident.
type roundTripper struct{ fallback http.RoundTripper }

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if p := handler.Load(); p != nil {
		rec := httptest.NewRecorder()
		(*p).ServeHTTP(rec, req)
		resp := rec.Result()
		resp.Request = req
		return resp, nil
	}
	fb := rt.fallback
	if fb == nil {
		fb = http.DefaultTransport
	}
	return fb.RoundTrip(req)
}

// Transport returns the self-routing RoundTripper (in-process when co-resident,
// plain HTTP otherwise). Give it to any subsystem's *http.Client.Transport.
func Transport() http.RoundTripper { return roundTripper{fallback: http.DefaultTransport} }

// Client returns an *http.Client wired to the self-routing Transport with the given
// timeout (0 → no timeout; the in-process path is synchronous anyway). Drop-in for
// the &http.Client{Timeout: …} every commerce proxy builds today.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: Transport(), Timeout: timeout}
}
