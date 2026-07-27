// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// public.go — the ANONYMOUS lane of the ONE canonical event door.
//
// A logged-out visitor on a marketing surface carries no bearer and no key, so
// eventTenant resolves nothing. This file is the lane such a request falls through
// to, so a pageview or a browser error from a logged-out page lands in the warehouse
// instead of being refused.
//
// Anonymous input is attested by nobody. It is therefore admitted under a policy the
// vouched-for lane never applies, and the two lanes are SEPARATE FUNCTIONS rather
// than one function with a mode flag: eventTenant → ingestBody is untouched by
// anything in this file, and every restriction below is unreachable from it.
//
// The policy is in two pieces and no more: the request-scoped gates (publicIngest)
// and the pure decision (admitPublic).
//
//   - TENANT is publicTenant, a compile-time constant. admitPublic takes no request,
//     so no header, query, or body field can influence the tenant an anonymous row
//     carries: an anonymous write CANNOT land in a real org's partition, and there is
//     no input that makes it do so.
//   - KIND is an ALLOWLIST of two — pageview and error, what a marketing surface
//     emits. `identify` and `group` (which name a person and a group) and every
//     custom event are dropped, counted in the honest receipt, never stored.
//   - NAME is server-chosen FROM the kind ($pageview | $error), so the anonymous name
//     space is closed to two values: an anonymous caller can introduce neither a new
//     name into the read lenses nor unbounded cardinality into the table's ORDER BY
//     key.
//   - FIELDS are a PROJECTION, not a filter: admitPublic builds a fresh CaptureEvent
//     from the fields it names, so a field it does not name — personId, groupId,
//     revenue, productId, refCode, signupWeek, and the entire client property bag —
//     cannot reach the row. The only properties an anonymous row carries are the
//     server-folded $exception and the write core's $source.
//   - BYTES and COUNT are bounded first, and REFUSED rather than truncated.
//   - RATE is capped per client IP and, independently, per socket peer.
//   - DNT / Sec-GPC on the wire is honored: nothing is stored and the receipt says so.
//
// Everything admitted here flows through the SAME ONE write core (ingestEvents) into
// the SAME hanzo.events table. One write path; this file only decides what a caller
// nobody vouched for may put on it.
package analytics

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// publicTenant is the reserved tenant EVERY anonymous event is attributed to. The
// '$' prefix is load-bearing: an IAM org slug is lowercase ASCII alphanumerics and
// '-' (the IAM slugifier emits nothing else), so this value lies outside the org
// namespace and cannot collide with a real tenant. It is also the reason the anonymous
// stream is legible: a row's tenant_id alone says whether IAM vouched for it.
const publicTenant = "$public"

// maxPublicBytes / maxPublicBatch bound ONE anonymous request. @hanzo/event's default
// batchSize is 20 and it also drains the queue on page-unload, so 50 leaves real
// headroom; 64 KiB is the browser's own sendBeacon ceiling, which makes it the honest
// cap for the transport that reaches here. Over either bound is REFUSED, never
// truncated — a silent truncation would make the receipt a lie.
const (
	maxPublicBytes = 64 << 10
	maxPublicBatch = 50
)

// publicKinds is the ALLOWLIST of canonical kinds (canonicalType's closed set) an
// anonymous caller may store. A kind absent here is dropped: `identify` and `group`
// bind an event to a named person and a named group, and a bare `event` is the whole
// custom product/billing/metering surface — none of which a caller nobody vouched for
// may write. Adding a kind here is the ONLY way to widen the anonymous surface.
var publicKinds = map[string]bool{"pageview": true, "error": true}

// publicRateWindow, publicRateLimit and publicPeerRateLimit cap anonymous ingest.
// TWO independent buckets, because neither key alone suffices:
//
//   - the CLIENT IP (leftmost X-Forwarded-For, via cloud.ClientIP) is the real
//     per-visitor key at the edge, but it is a header, so a caller reaching the pod
//     directly can rotate it and reset its own bucket at will;
//   - the SOCKET PEER cannot be rotated, but at the edge it is the ingress for ALL
//     public traffic, so it can only carry a total-volume ceiling, never a per-visitor
//     cap.
//
// Together they bound both a single source and the aggregate. Sized so real marketing
// traffic never notices: a browser emits a handful of events per page load, so 300/min
// per client is orders of magnitude of headroom, while 6000/min is the pod's anonymous
// ingest ceiling. Per-pod and in-memory — a per-replica soft cap, which is what a
// flood control needs to be, not a distributed quota.
const (
	publicRateWindow    = time.Minute
	publicRateLimit     = 300
	publicPeerRateLimit = 6000
)

// counter is a fixed-window request counter keyed on a caller string, with
// opportunistic eviction so the map stays bounded at the edge's IP cardinality. This
// is deliberately a counter and not the zip token-bucket primitive for the same reason
// cloud's edge limiter is not: that primitive never evicts, which is fine for a
// bounded per-org keyspace and unbounded growth when keyed on raw IPs.
type counter struct {
	limit  int
	window time.Duration

	mu    sync.Mutex
	seen  map[string]*tally
	swept time.Time
}

// tally is one key's window: how many requests it has spent, and when it resets.
type tally struct {
	n     int
	reset time.Time
}

func newCounter(limit int, window time.Duration) *counter {
	return &counter{limit: limit, window: window, seen: map[string]*tally{}}
}

// ok charges one request to key and reports whether it is within the window's limit.
// A request over the limit is still charged, so a caller cannot ride a rejected
// request for free.
func (c *counter) ok(key string) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.swept) >= c.window {
		c.swept = now
		for k, t := range c.seen {
			if now.After(t.reset) {
				delete(c.seen, k)
			}
		}
	}
	t := c.seen[key]
	if t == nil || now.After(t.reset) {
		t = &tally{reset: now.Add(c.window)}
		c.seen[key] = t
	}
	t.n++
	return t.n <= c.limit
}

// publicRate / publicPeerRate are the two anonymous-ingest buckets. Package-level
// because the cap is a property of the pod, which outlives any request. Vars, not
// consts, so a test can install a tighter pair.
var (
	publicRate     = newCounter(publicRateLimit, publicRateWindow)
	publicPeerRate = newCounter(publicPeerRateLimit, publicRateWindow)
)

// publicRateOK charges one anonymous request against BOTH buckets and reports whether
// it may proceed. An unkeyable caller shares one bucket rather than being exempt, so
// an absent header never buys an uncapped lane. Both buckets are always charged (no
// short-circuit) so a flood keeps counting against the ceiling even while its own
// per-client bucket is already over.
func publicRateOK(c *zip.Ctx) bool {
	client := cloud.ClientIP(c)
	if client == "" {
		client = "-"
	}
	within := publicRate.ok(client)
	return publicPeerRate.ok(peerIP(c)) && within
}

// peerIP is the L4 socket peer — the one address a caller cannot set. Through the
// ingress this is the proxy, so it keys the aggregate ceiling rather than a visitor.
func peerIP(c *zip.Ctx) string {
	ip := c.Fiber().IP()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if ip == "" {
		return "-"
	}
	return ip
}

// optedOut reports whether the visitor signalled Do-Not-Track or Global Privacy
// Control on the wire.
//
// This is a SECOND, independent line rather than a duplicate of the client's. On the
// client, consent is the host app's `enabled` config (@hanzo/event turns the whole
// client off when it is false, so an opted-out visitor normally emits no request at
// all) — which means the app owns that decision and a request can still arrive
// carrying the header: a surface that has not wired `enabled` to DNT/GPC, a browser
// or extension that sets the header itself, or a privacy proxy that adds one. When it
// does, the server stores nothing.
func optedOut(c *zip.Ctx) bool {
	if strings.TrimSpace(c.Header("DNT")) == "1" {
		return true
	}
	return strings.TrimSpace(c.Header("Sec-GPC")) == "1"
}

// admitPublic is the anonymous door's WHOLE attribution decision, and it is PURE over
// the decoded batch: it returns the tenant the rows will carry, the events that may be
// stored, and how many were dropped. It takes no *zip.Ctx — there is no header, query,
// or body field it could read a tenant from, so the returned org is always
// publicTenant. That is the isolation proof: not a check that can be bypassed, but an
// argument list that cannot express the alternative.
//
// Each admitted event is REBUILT from the allowlisted fields rather than edited, so a
// field this function does not name cannot reach the row. The stored name comes from
// the kind (resolveEventName maps the empty name to $pageview / $error). Properties are
// left nil: the shared tail (ingestDecoded) folds the typed error into
// properties.$exception and the write core stamps $source, so an anonymous row's
// properties hold exactly what the SERVER put there and nothing the caller sent.
func admitPublic(evs []CaptureEvent) (string, []CaptureEvent, int) {
	out := make([]CaptureEvent, 0, len(evs))
	dropped := 0
	for _, e := range evs {
		kind := canonicalType(e.Type)
		if !publicKinds[kind] {
			dropped++
			continue
		}
		out = append(out, CaptureEvent{
			MessageID:   e.MessageID,
			Type:        kind,
			Timestamp:   e.Timestamp,
			DistinctID:  e.DistinctID,
			AnonymousID: e.AnonymousID,
			SessionID:   e.SessionID,
			Product:     e.Product,
			URL:         e.URL,
			Path:        e.Path,
			Referrer:    e.Referrer,
			UTM:         e.UTM,
			Library:     e.Library,
			LibraryVer:  e.LibraryVer,
			Error:       e.Error,
		})
	}
	return publicTenant, out, dropped
}

// publicIngest answers an ANONYMOUS POST on the canonical door: the request-scoped
// gates (capture flag, rate, size, opt-out) then the pure decision (admitPublic) then
// the ONE write core. source stays the front door's origin tag — the TENANT, not the
// tag, is what records that a row arrived unattested.
//
// It is reached only from eventHandle, and only when the caller presented no
// credential at all: a presented-but-unresolvable key is refused there rather than
// downgraded to anonymous.
func publicIngest(c *zip.Ctx, source string) error {
	// CLOUD_ANALYTICS_PUBLIC_CAPTURE is the ONE existing anonymous-capture switch
	// (it also gates the site-host carve). Off ⇒ the canonical door keeps its
	// strict, principal-only contract.
	if !publicCaptureEnabled() {
		return zip.ErrForbidden("valid bearer or a resolvable ingest key required")
	}
	if !publicRateOK(c) {
		return zip.Errorf(http.StatusTooManyRequests, "rate limit exceeded")
	}
	body := c.Body()
	if len(body) > maxPublicBytes {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "event payload too large")
	}
	evs, err := decodeIngest(body)
	if err != nil {
		return zip.ErrBadRequest("malformed event payload")
	}
	if len(evs) > maxPublicBatch {
		return zip.ErrBadRequest("batch too large")
	}
	if optedOut(c) {
		return c.JSON(http.StatusOK, CaptureResult{Dropped: len(evs)})
	}
	// Rejoin the ONE pipeline: admission decided the tenant and the projection, and
	// ingestDecoded (event.go) does the rest exactly as it does for a bearer.
	org, admitted, dropped := admitPublic(evs)
	return ingestDecoded(c, org, source, admitted, dropped)
}
