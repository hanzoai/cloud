package cloudflare

// zones.go — Cloudflare Zones + Analytics, READ-ONLY. A zone list/get enumerates the
// zones the org's token can see (zone ids feed Workers routes and analytics); zone
// analytics reads a zone's traffic dashboard. Zone/record MANAGEMENT is NOT here — it
// stays on the Hanzo DNS plane (/v1/dns), so this plane never forks DNS; it only
// surfaces the Cloudflare zone objects the asset plane needs. All three are reads, so
// they gate on authClient (validated org) — never authWrite.

import (
	"context"
	"net/http"

	"github.com/zap-proto/zip"
)

// maxPurgeFiles is Cloudflare's documented per-request URL cap for a files purge.
const maxPurgeFiles = 30

// zonesIn pages and filters the zone list. Every field is optional and rides the
// query string; each is forwarded to Cloudflare under the same name.
type zonesIn struct {
	// Page is the 1-based page of zones to return.
	Page string `json:"page"`
	// PerPage is how many zones one page holds.
	PerPage string `json:"per_page"`
	// Name filters to the zone with this domain name.
	Name string `json:"name"`
	// Status filters by zone status (active, pending, initializing, …).
	Status string `json:"status"`
	// Order names the field to sort by, and Direction sorts asc or desc.
	Order     string `json:"order"`
	Direction string `json:"direction"`
}

// ZonesList lists the Cloudflare zones the org's connected API token can see,
// paged and filtered by the query parameters Cloudflare itself accepts. Zones are
// token-scoped by Cloudflare, so no account is resolved. Any org member may read.
//
// Zone and DNS-record MANAGEMENT is not here: it stays on the Hanzo DNS plane
// (/v1/dns). This only surfaces the Cloudflare zone objects the asset plane needs
// — a zone id is what addresses a Worker route or an analytics read.
func (o ops) zonesList(ctx context.Context, in *zonesIn) (*cfResult, error) {
	cl, _, err := o.authClient(ctx)
	if err != nil {
		return nil, err
	}
	q := forward(map[string]string{
		"page": in.Page, "per_page": in.PerPage, "name": in.Name,
		"status": in.Status, "order": in.Order, "direction": in.Direction,
	})
	return cl.relay(ctx, http.MethodGet, "/zones"+q, nil)
}

// zoneRef addresses one zone. The id is the path segment: the URL is the addressing
// authority, so it binds from there whatever a body says.
type zoneRef struct {
	// Zone is the 32-hex Cloudflare zone id.
	Zone string `json:"zone"`
}

// ZoneGet reads one Cloudflare zone the org's token can see. Any org member may
// read. A zone id the token cannot see is Cloudflare's own not-found, relayed.
//
// Example: {"zone": "0123456789abcdef0123456789abcdef"}
func (o ops) zoneGet(ctx context.Context, in *zoneRef) (*cfResult, error) {
	cl, _, err := o.authClient(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := seg("zone", in.Zone, idRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodGet, "/zones/"+zone, nil)
}

// analyticsIn reads a zone's traffic dashboard over a window.
type analyticsIn struct {
	// Zone is the 32-hex Cloudflare zone id.
	Zone string `json:"zone"`
	// Since and Until bound the window, in the form Cloudflare accepts — an RFC 3339
	// time or a negative number of minutes from now ("-1440" is the last day).
	Since string `json:"since"`
	Until string `json:"until"`
	// Continuous asks Cloudflare for only fully-aggregated buckets.
	Continuous string `json:"continuous"`
}

// ZoneAnalytics reads a zone's Cloudflare traffic dashboard — requests, bandwidth,
// threats and pageviews over the since/until window. Any org member may read.
//
// A zone whose Cloudflare plan does not serve this endpoint yields Cloudflare's
// OWN error, never a fabricated success.
//
// Example: {"zone": "0123456789abcdef0123456789abcdef", "since": "-1440", "until": "0"}
func (o ops) zoneAnalytics(ctx context.Context, in *analyticsIn) (*cfResult, error) {
	cl, _, err := o.authClient(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := seg("zone", in.Zone, idRE)
	if err != nil {
		return nil, err
	}
	q := forward(map[string]string{"since": in.Since, "until": in.Until, "continuous": in.Continuous})
	return cl.relay(ctx, http.MethodGet, "/zones/"+zone+"/analytics/dashboard"+q, nil)
}

// PurgeCache is the body of a zone cache purge. Exactly one selector may be set:
// Everything drops the zone's entire edge cache; Files purges the listed URLs.
// Cloudflare also accepts tags/hosts/prefixes, which are Enterprise-only and are
// deliberately not modeled — an unmodeled field would silently no-op on our plan.
type PurgeCache struct {
	Everything bool     `json:"purge_everything,omitempty"`
	Files      []string `json:"files,omitempty"`
}

// purgeIn is the purge request: the zone from the path, plus the one selector.
// PurgeCache is the shape sent UPSTREAM; this is the shape the caller sends, and
// the two are spelled out separately because the zone is ours and never Cloudflare's.
type purgeIn struct {
	// Zone is the 32-hex Cloudflare zone id, from the path.
	Zone string `json:"zone"`
	// Everything drops the zone's entire edge cache.
	Everything bool `json:"purge_everything"`
	// Files purges exactly the listed URLs — at most 30, Cloudflare's per-request cap.
	Files []string `json:"files"`
}

// ZonePurge drops a zone's Cloudflare edge cache — either the whole zone
// (purge_everything) or exactly the listed file URLs. Requires org admin.
//
// Purging is the one zone-scoped WRITE this plane owns. It is not DNS — no record
// changes — so it does not belong on /v1/dns, and it is not a connection, so it does
// not belong on the integrations plane. It is a cache operation on a zone, which is
// what this asset plane is for. It takes the admin gate because dropping a zone's
// cache sends every subsequent request to the origin: on a site fronting a small
// origin that is a self-inflicted load spike, so it is a change, not a look.
//
// Exactly one selector is required. Cloudflare treats a body with neither as a
// no-op and answers 200, which reads as "purged" to a caller that never purged
// anything — the failure we refuse to pass through.
//
// Example: {"zone": "0123456789abcdef0123456789abcdef", "purge_everything": true}
func (o ops) zonePurge(ctx context.Context, in *purgeIn) (*cfResult, error) {
	cl, _, err := o.authWrite(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := seg("zone", in.Zone, idRE)
	if err != nil {
		return nil, err
	}
	switch {
	case in.Everything && len(in.Files) > 0:
		return nil, zip.ErrBadRequest("purge_everything and files are mutually exclusive")
	case !in.Everything && len(in.Files) == 0:
		return nil, zip.ErrBadRequest("set purge_everything or a non-empty files list")
	case len(in.Files) > maxPurgeFiles:
		return nil, zip.ErrBadRequest("files exceeds the per-request limit")
	}
	return cl.relay(ctx, http.MethodPost, "/zones/"+zone+"/purge_cache",
		PurgeCache{Everything: in.Everything, Files: in.Files})
}
