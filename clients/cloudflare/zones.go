package cloudflare

// zones.go — Cloudflare Zones + Analytics, READ-ONLY. A zone list/get enumerates the
// zones the org's token can see (zone ids feed Workers routes and analytics); zone
// analytics reads a zone's traffic dashboard. Zone/record MANAGEMENT is NOT here — it
// stays on the Hanzo DNS plane (/v1/dns), so this plane never forks DNS; it only
// surfaces the Cloudflare zone objects the asset plane needs. All three are reads, so
// they gate on authClient (validated org) — never authWrite.

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

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
