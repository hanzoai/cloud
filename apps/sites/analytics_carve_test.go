package sites

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// beaconBody is a Segment/PostHog-shaped beacon carrying a foreign org claim — the
// shape that used to be ingested here under a host-derived tenant.
const beaconBody = `{"batch":[{"type":"pageview"}],"org":"attacker-org","properties":{"space":"attacker-org"}}`

func beaconReq(host, path string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(beaconBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "attacker-org") // forged identity header
	return req
}

// TestSiteHostNeverIngests pins the invariant that replaced the analytics carve: a
// site host serves BYTES and is TERMINAL. A beacon POST to one is neither ingested
// here (there is no handler left to install — SetAnalyticsHost is gone, which the
// build proves) nor passed through to the API pipeline where the ingest endpoint
// lives.
//
// The carve was a SECOND attribution mechanism beside the project key, and the one
// that could not be checked: this middleware runs BEFORE the identity boundary, so
// the Host was the only thing it could trust. A site's beacon now carries the key
// minted with its project and posts it to api.hanzo.ai, where a credential can
// actually be read.
//
// X-Sentinel marks the API pipeline (newTestApp). Its ABSENCE is the assertion.
func TestSiteHostNeverIngests(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{
		Org: "hanzo", Slug: "yadota", Bucket: "b", Prefix: "hanzo/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(testServer())

	for _, path := range []string{"/v1/event", "/v1/insights/e", "/v1/analytics/overview"} {
		resp, err := app.Test(beaconReq("yadota.hanzo.app", path))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.Header.Get("X-Sentinel") != "" {
			t.Errorf("POST %s on a site host reached the API pipeline; a site host must be terminal", path)
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("POST %s on a site host answered 200; a beacon must not be accepted here", path)
		}
	}
}
