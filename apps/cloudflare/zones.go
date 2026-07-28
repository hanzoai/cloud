package cloudflare

// zones.go — Cloudflare Zones + Analytics, READ-ONLY. A zone list/get enumerates the
// zones the org's token can see (zone ids feed Workers routes and analytics); zone
// analytics reads a zone's traffic dashboard. Zone/record MANAGEMENT is NOT here — it
// stays on the Hanzo DNS plane (/v1/dns), so this plane never forks DNS; it only
// surfaces the Cloudflare zone objects the asset plane needs. All three are reads, so
// they gate on authClient (validated org) — never authWrite.

import (
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// maxPurgeFiles is Cloudflare's documented per-request URL cap for a files purge.
const maxPurgeFiles = 30

// zonesList relays GET /zones (the zones the token is scoped to), forwarding only an
// allowlisted set of pagination/filter params so a caller can page without arbitrary
// passthrough. Zones are token-scoped by Cloudflare, so no account resolution is
// needed. Read gate.
func zonesList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/zones"+query(c, "page", "per_page", "name", "status", "order", "direction"), nil)
}

// zoneGet relays GET /zones/{zone_id} for one zone the token can see.
func zoneGet(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	zone, err := pathSeg(c, "zone", idRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/zones/"+zone, nil)
}

// zoneAnalytics relays GET /zones/{zone_id}/analytics/dashboard — the zone traffic
// analytics read — forwarding the since/until/continuous window params. A zone whose
// plan does not serve this endpoint yields Cloudflare's OWN error (relayed via cfErr),
// never a fabricated success. Read gate.
func zoneAnalytics(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	zone, err := pathSeg(c, "zone", idRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/zones/"+zone+"/analytics/dashboard"+query(c, "since", "until", "continuous"), nil)
}

// PurgeCache is the body of a zone cache purge. Exactly one selector may be set:
// Everything drops the zone's entire edge cache; Files purges the listed URLs.
// Cloudflare also accepts tags/hosts/prefixes, which are Enterprise-only and are
// deliberately not modeled — an unmodeled field would silently no-op on our plan.
type PurgeCache struct {
	Everything bool     `json:"purge_everything,omitempty"`
	Files      []string `json:"files,omitempty"`
}

// zonePurge relays POST /zones/{zone_id}/purge_cache.
//
// Purging is the one zone-scoped WRITE this plane owns. It is not DNS — no record
// changes — so it does not belong on /v1/dns, and it is not a connection, so it does
// not belong on the integrations plane. It is a cache operation on a zone, which is
// what this asset plane is for.
//
// It gates on authWrite (org admin), because dropping a zone's cache sends every
// subsequent request to the origin: on a site fronting a small origin that is a
// self-inflicted load spike, so it is a change, not a look.
//
// Exactly one selector is required. Cloudflare treats a body with neither as a
// no-op and answers 200, which reads as "purged" to a caller that never purged
// anything — the failure we refuse to pass through.
func zonePurge(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authWrite(s, c)
	if err != nil {
		return err
	}
	zone, err := pathSeg(c, "zone", idRE)
	if err != nil {
		return err
	}
	var in PurgeCache
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	switch {
	case in.Everything && len(in.Files) > 0:
		return zip.ErrBadRequest("purge_everything and files are mutually exclusive")
	case !in.Everything && len(in.Files) == 0:
		return zip.ErrBadRequest("set purge_everything or a non-empty files list")
	case len(in.Files) > maxPurgeFiles:
		return zip.ErrBadRequest("files exceeds the per-request limit")
	}
	return cl.pass(c, http.MethodPost, "/zones/"+zone+"/purge_cache", in)
}
