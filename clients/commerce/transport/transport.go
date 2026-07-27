// Package transport is the ONE seam that lets every cloud subsystem that
// speaks the commerce billing S2S surface (clients/{billing,account,admin,
// referrals,authors,affiliates,usage} + the request-edge metering gate in build.go)
// reach the co-resident, in-process commerce handler with a DIRECT Go call instead
// of an HTTP hop to the standalone commerce pod (CLOUD_COMMERCE_HTTP_URL,
// commerce.hanzo.svc:8001). This is what lets the standalone be RETIRED (task #111):
// once commerce is folded in-process (#114), every /v1/billing read/write + the
// metering debit routes to the same in-tree gin engine that /v1/commerce already does.
//
// It is a SIBLING of clients/commerce, never a part of it: this package owns the
// BYTE STREAM (an http.RoundTripper), clients/commerce owns the DOMAIN reads
// (entitlement, active plan, balance) as typed Go calls on the embedded datastore.
// Two concerns, two packages — and the split is load-bearing, not cosmetic: the
// domain client resolves plan tiers through clients/plan, which imports the root
// cloud package, while build.go (in that same root package) needs this transport.
// One package would close that loop into an import cycle.
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
package transport

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"
)

// PlaceholderBase is the sentinel commerce base URL used when commerce is
// co-resident and CLOUD_COMMERCE_HTTP_URL is unset (the post-retirement steady
// state). The self-routing Transport ignores the host and dispatches by PATH, so
// the value only has to be a parseable absolute URL; it is never dialed.
const PlaceholderBase = "http://commerce.inproc"

// handler holds the embedded commerce http.Handler. atomic so SetHandler (boot) and
// RoundTrip (every request) race-free without a mutex on the hot path.
var handler atomic.Pointer[http.Handler]

// SetApp publishes the co-resident zip app commerce's routes live on (the
// SharedApp contract). The S2S dispatch enters the app's fasthttp pipeline
// directly; the http.Handler shape survives only inside this seam for the
// bridge-building subsystems that still speak *http.Request. Passing nil
// un-publishes.
func SetApp(app *zip.App) {
	if app == nil {
		SetHandler(nil)
		return
	}
	SetHandler(adaptor.FiberApp(app.Fiber()))
}

// SetHandler publishes the in-process commerce handler. SetApp is the
// production path; this remains the seam tests stub. Passing nil
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

// maxDepth caps how many co-resident dispatches may be NESTED on one goroutine.
// The in-process handler is the WHOLE shared app (SetApp publishes
// adaptor.FiberApp), so a dispatch re-runs every edge middleware — and any of
// those middlewares (e.g. the per-org scope rate-limiter reading its rules from
// commerce, or the ai per-tier gate) itself issues a commerce read, which
// dispatches the whole app AGAIN. On a cold config/tier cache that read never
// resolves before it re-enters, so it recurses without bound: synchronously it
// overflows the goroutine stack (a fatal ~1e9-byte "stack overflow"); each level
// also leaks the completion's net/http cancel watchdog, so under load the writer
// accumulates tens of thousands of goroutines parked in setRequestCancel and OOMs.
// A legitimate flow nests at most a couple of reads (a debit that first checks a
// balance), so a small cap admits every real path while turning the runaway into a
// bounded, fail-safe refusal at the seam. Callers of the co-resident reads treat
// the refusal as any transport error and fall safe (the tier gate ALLOWS, the
// scope-rule fetch fails OPEN); the prepaid balance read is a direct in-process
// ledger call, never this transport, so fail-closed billing is unaffected.
const maxDepth = 8

// depthByGoroutine tracks the current nested-dispatch depth per goroutine. The
// dispatch is synchronous (ServeHTTP runs on the caller's goroutine), so a
// goroutine-keyed counter measures exactly the nesting of the recursion; distinct
// concurrent requests run on distinct goroutines and never share a count.
var depthByGoroutine sync.Map // goid -> int

// goroutineID returns the current goroutine's numeric id. It is used ONLY for
// re-entrancy accounting (never identity or security); the runtime prints it at
// the head of the per-goroutine stack, which is the one portable way to read it.
func goroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// "goroutine <id> [<state>]:"
	s := string(buf[:n])
	s = strings.TrimPrefix(s, "goroutine ")
	if i := strings.IndexByte(s, ' '); i > 0 {
		if id, err := strconv.ParseInt(s[:i], 10, 64); err == nil {
			return id
		}
	}
	return 0
}

// roundTripper dispatches to the in-process commerce handler when one is published,
// else to fallback (plain HTTP). The gin engine routes on req.URL.Path, so the host
// in the (placeholder or real) base is irrelevant when co-resident.
type roundTripper struct{ fallback http.RoundTripper }

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if p := handler.Load(); p != nil {
		// Re-entrancy guard: bound how deep co-resident dispatches may nest on
		// this goroutine (see maxDepth). Without it a middleware that reads
		// commerce while serving a commerce read re-runs the whole app forever.
		id := goroutineID()
		depth := 0
		if v, ok := depthByGoroutine.Load(id); ok {
			depth = v.(int)
		}
		if depth >= maxDepth {
			return nil, fmt.Errorf("commerce transport: in-process dispatch depth %d exceeded (re-entrant commerce read refused: %s %s)", maxDepth, req.Method, req.URL.Path)
		}
		depthByGoroutine.Store(id, depth+1)
		defer func() {
			if depth == 0 {
				depthByGoroutine.Delete(id)
			} else {
				depthByGoroutine.Store(id, depth)
			}
		}()

		// Callers build CLIENT-style requests (http.NewRequest → empty
		// RequestURI); the in-process dispatch is SERVER-side, and the fiber
		// pipeline routes on RequestURI. Normalize here — the one seam.
		if req.RequestURI == "" {
			req.RequestURI = req.URL.RequestURI()
		}
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
