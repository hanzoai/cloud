package dns

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/environ"
)

// The Hanzo DNS control plane (hanzoai/dns, plugin/hanzodns) — the plane this
// surface was written against, and the one a deployment gets when it names none.
//
// Its API IS this surface's contract: the addresses under /v1/dns are the plane's
// own addresses, so every operation here is the SAME call the caller made, issued
// upstream under the caller's own identity. That is why this adapter declares Relay:
// an address the four operations do not name is still an address the plane has.
//
// A SECOND plane is a second file next to this one — not a change to this one, to
// provider.go, or to the route.
func init() { register("hanzo", func() Provider { return newHanzoPlane() }) }

// endpoint is the in-cluster DNS control-plane API — the operator's DNSConnector
// default (the API listens on :8443). Used when HANZO_DNS_URL is unset so a standard
// cluster deployment forwards without extra config.
const endpoint = "http://coredns-hanzodns.dns-system.svc:8443"

// maxBody bounds the upstream response read: zone/record listings are small JSON, so
// this caps a hostile or runaway upstream body.
const maxBody = 4 << 20 // 4 MiB

// callTimeout bounds a single upstream call so a hung plane cannot wedge a console
// request. The caller's context deadline (if tighter) still wins.
const callTimeout = 15 * time.Second

// hanzoPlane relays to hanzoai/dns. It holds no credential of its own: the plane is
// OIDC-gated and keys every zone per-org, so each call travels under the CALLER'S own
// validated bearer and the server-validated org, and cloud substitutes no service
// credential (which would collapse tenants).
type hanzoPlane struct {
	base string
	http *http.Client
}

func newHanzoPlane() *hanzoPlane {
	return &hanzoPlane{
		base: strings.TrimRight(environ.Or("HANZO_DNS_URL", endpoint), "/"),
		http: &http.Client{
			Timeout: callTimeout,
			// Do NOT follow upstream 3xx. Relay the redirect verbatim (status +
			// Location) so responses pass through as claimed and a redirect can never
			// silently re-target the request onto another host or path under this
			// head's own (fresh-request, bearer-relay) identity.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (h *hanzoPlane) ID() string { return "hanzo" }

// The four operations, and the plane's own addresses beyond them, are ONE call:
// this plane's API is this surface's contract, so what the caller asked for is what
// goes upstream.
func (h *hanzoPlane) ListZones(ctx context.Context, c Call) (Answer, error) { return h.send(ctx, c) }
func (h *hanzoPlane) ListRecords(ctx context.Context, c Call) (Answer, error) {
	return h.send(ctx, c)
}
func (h *hanzoPlane) UpsertRecord(ctx context.Context, c Call) (Answer, error) {
	return h.send(ctx, c)
}
func (h *hanzoPlane) DeleteRecord(ctx context.Context, c Call) (Answer, error) {
	return h.send(ctx, c)
}
func (h *hanzoPlane) Relay(ctx context.Context, c Call) (Answer, error) { return h.send(ctx, c) }

// send issues one call to the plane and returns its answer verbatim: its status code,
// its Content-Type, and its Location on a redirect it never follows.
//
// It builds a FRESH request and sets only the headers it means to send, so no inbound
// header (a stray cookie, a forged X-*, an injected Authorization copy) is blindly
// relayed. The host comes only from deployment config, never from the call, so no
// path can re-target another host.
func (h *hanzoPlane) send(ctx context.Context, c Call) (Answer, error) {
	if h.base == "" {
		return Answer{}, ErrUnconfigured
	}
	target := h.base + c.Path
	if c.Query != "" {
		target += "?" + c.Query
	}

	var body io.Reader
	if len(c.Body) > 0 {
		body = strings.NewReader(string(c.Body))
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, target, body)
	if err != nil {
		return Answer{}, ErrUnreachable
	}
	// BEARER RELAY — the caller's OWN validated bearer, unchanged. The plane
	// re-validates it and derives the org from the `owner` claim; X-Org-Id carries the
	// server-validated org for a trusted-proxy mode. Both are the caller's own
	// identity, so org A can never reach org B.
	req.Header.Set("Authorization", "Bearer "+c.Bearer)
	req.Header.Set("X-Org-Id", c.Org)
	if c.ContentType != "" {
		req.Header.Set("Content-Type", c.ContentType)
	}
	req.Header.Set("Accept", "application/json")

	res, err := h.http.Do(req)
	if err != nil {
		return Answer{}, ErrUnreachable
	}
	defer func() { _ = res.Body.Close() }()

	out, _ := io.ReadAll(io.LimitReader(res.Body, maxBody))
	return Answer{
		Status:      res.StatusCode,
		ContentType: res.Header.Get("Content-Type"),
		Location:    res.Header.Get("Location"),
		Body:        out,
	}, nil
}
