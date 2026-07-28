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

// TestMount_HostCarve_EventDoorIngestsForSiteOrg is the /v1/event twin of
// TestMount_HostCarve_IngestsForSiteOrg: a beacon POST to the CANONICAL door on a LIVE
// site host is ingested for the site's Org in EVERY wire shape the tolerant decoder
// accepts, even though the request carries a forged org (body + X-Org-Id) and NO
// validated principal. 503 is the discriminator: it passed the door and stopped only at
// datastore-down, so the org came from the host.
//
// The kind is pageview because a site host is anonymous by construction (it runs before
// the identity boundary) and the anonymous lane admits pageview and error. A custom
// event on the same door is dropped — TestMount_HostCarve_AnonymousCapabilityOnly.
func TestMount_HostCarve_EventDoorIngestsForSiteOrg(t *testing.T) {
	tightenPublicRate(t, 1_000_000, 1_000_000)
	app := carveApp(t, "hanzo")
	for _, body := range []string{
		`{"batch":[{"type":"pageview"}],"org":"evil"}`,         // {batch} envelope
		`{"events":[{"type":"pageview"}],"tenant_id":"evil"}`,  // {events} alias
		`{"batch":[{"type":"error","error":{"message":"x"}}]}`, // the other admitted kind
	} {
		code := postHost(t, app, "yadota.hanzo.app", "/v1/event", body,
			map[string]string{"X-Org-Id": "attacker"})
		if code != http.StatusServiceUnavailable {
			t.Fatalf("/v1/event beacon %q want 503 (ingested for the site org), got %d", body, code)
		}
	}

	// The BARE canonical Event wire ({event,distinctId,time,properties}) carries no
	// `type` field at all, so canonicalType folds it to "event" — a kind the anonymous
	// allowlist does not admit. On a site host, where nothing can be vouched for, the
	// bare wire is therefore always dropped; a beacon that wants to record a pageview
	// sends the {batch:[…]} envelope, which is exactly what the app's wired injection
	// emits (appBeaconBody below).
	for _, body := range []string{
		`{"event":"signup_completed","distinctId":"d","org":"attacker"}`,
		`[{"event":"signup_completed","distinctId":"d"}]`,
	} {
		if code := postHost(t, app, "yadota.hanzo.app", "/v1/event", body,
			map[string]string{"X-Org-Id": "attacker"}); code != http.StatusOK {
			t.Fatalf("/v1/event bare-Event beacon %q want 200 (kind not anonymously admitted), got %d", body, code)
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

// TestMount_HostCarve_EventDirectNoHostGetsNoOrg pins that the forced-org carve is
// HOST-scoped: the SAME body on a NON-site host does not get a site org. The carve did
// not fire, so the request runs the normal canonical gate — no principal and no key, so
// it takes the ANONYMOUS lane, where the forged X-Org-Id and the custom event kind both
// buy nothing: 200 with an all-dropped receipt, no row under `attacker`.
func TestMount_HostCarve_EventDirectNoHostGetsNoOrg(t *testing.T) {
	app := carveApp(t, "hanzo")
	code := postHost(t, app, "evil.example.com", "/v1/event",
		`{"event":"signup_completed","distinctId":"d"}`, map[string]string{"X-Org-Id": "attacker"})
	if code != http.StatusOK {
		t.Fatalf("anonymous /v1/event on a non-site host want 200 (anonymous lane, kind dropped), got %d", code)
	}
}
