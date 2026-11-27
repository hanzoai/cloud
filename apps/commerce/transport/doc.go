// Package transport is the ONE client that lets every cloud subsystem that
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
// HOW. commerce.Use registers the embedded commerce http.Handler here via
// SetHandler once, at boot. A subsystem builds its HTTP request EXACTLY as
// before (same path, same server-pinned `X-Org-Id`; the identity is added here,
// by the carrier the root registers) and sends it through Transport(): when the handler is
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
