// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"net/http"
	"testing"
)

// appBeaconBody is the EXACT payload the published-site page beacon posts
// (app/lib/publishing/wired-injection.ts:68-69): a {batch:[CaptureEvent]} envelope.
// The only change the app makes is repointing ANALYTICS_ENDPOINT from /v1/analytics
// to /v1/event — the body is unchanged and MUST land via the canonical door's
// site-host carve.
const appBeaconBody = `{"batch":[{"messageId":"m-abc123","type":"pageview","event":"$pageview",` +
	`"timestamp":"2026-07-22T12:00:00.000Z","distinctId":"anon-9","anonymousId":"anon-9",` +
	`"sessionId":"sess-1","url":"https://yadota.hanzo.app/pricing","path":"/pricing",` +
	`"referrer":"https://news.ycombinator.com/","properties":{"space":"yadota","title":"Pricing"},` +
	`"library":"@hanzo/capture-wired","libraryVersion":"0.1.1"}]}`

// TestMount_HostCarve_EventDoorForcesOrg is the /v1/event twin of
// TestMount_HostCarve_ForcesSiteOrg: a beacon POST to the CANONICAL door on a LIVE
// site host is ingested as the site's Org even though the request carries a forged
// org (body + X-Org-Id) and NO validated principal. 503-not-403 is the discriminator
// — the same body 403s a DIRECT /v1/event call (no brand fallback), but here it
// passes because the carve FORCES org from the host, stopping only at datastore-down.
func TestMount_HostCarve_EventDoorForcesOrg(t *testing.T) {
	app := carveApp(t, "hanzo")
	for _, body := range []string{
		`{"event":"signup_completed","distinctId":"d","org":"attacker"}`,       // bare Event
		`[{"event":"signup_completed","distinctId":"d"}]`,                      // bare [Event]
		`{"batch":[{"type":"event","event":"signup_completed"}],"org":"evil"}`, // {batch} envelope
	} {
		code := postHost(t, app, "yadota.hanzo.app", "/v1/event", body,
			map[string]string{"X-Org-Id": "attacker"})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("/v1/event beacon %q want 503 (ingested as site org), got %d", body, code)
		}
	}
}

// TestMount_HostCarve_AppBeaconExactBody confirms the CANONICAL door accepts the
// APP beacon's EXACT {batch:[ev]} body via the site-host carve — the acceptance test
// for repointing ANALYTICS_ENDPOINT to /v1/event. Admitted (503, datastore down),
// tenant forced to the site's Org regardless of the beacon's properties.space claim.
func TestMount_HostCarve_AppBeaconExactBody(t *testing.T) {
	app := carveApp(t, "hanzo")
	code := postHost(t, app, "yadota.hanzo.app", "/v1/event", appBeaconBody, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("exact app beacon on /v1/event want 503 (admitted via carve), got %d", code)
	}
}

// TestMount_HostCarve_EventEmptyBatchOK: an empty beacon batch on the canonical door
// is an honest 200 (zero counts) BEFORE the datastore is consulted — proving the
// carve decodes and funnels through the ONE write core with the host-forced org.
func TestMount_HostCarve_EventEmptyBatchOK(t *testing.T) {
	app := carveApp(t, "hanzo")
	if code := postHost(t, app, "yadota.hanzo.app", "/v1/event", `{"batch":[]}`, nil); code != http.StatusOK {
		t.Fatalf("empty beacon batch on /v1/event want 200, got %d", code)
	}
}

// TestMount_HostCarve_EventDirectNoHostStillFailsClosed pins that the forced-org
// carve is HOST-scoped: the SAME anonymous /v1/event body on a NON-site host runs the
// normal canonical gate (eventTenant, no brand fallback) and is refused 403 — the
// carve did not fire, so the strict door invariant is unweakened.
func TestMount_HostCarve_EventDirectNoHostStillFailsClosed(t *testing.T) {
	app := carveApp(t, "hanzo")
	code := postHost(t, app, "evil.example.com", "/v1/event",
		`{"event":"signup_completed","distinctId":"d"}`, map[string]string{"X-Org-Id": "attacker"})
	if code != http.StatusForbidden {
		t.Fatalf("anonymous /v1/event on a non-site host want 403 (no carve, no brand fallback), got %d", code)
	}
}
