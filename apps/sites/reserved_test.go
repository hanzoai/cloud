package sites

import "testing"

// sharedAppRedirectLabels is the audited set of `<label>.hanzo.app` subdomains that
// the SHARED `hanzo-app` IAM client registers as OAuth redirect URIs (universe
// init_data.json, application "hanzo-app"). It is maintained BY HAND in lock-step
// with that client: whenever a `<label>.hanzo.app/...` redirect is added to (or
// removed from) the shared client, this set — and reserved.go — must change with it.
//
// As of the audit the shared client's `*.hanzo.app` redirects are:
//
//	https://www.hanzo.app/auth/callback   → www
//	https://stg.hanzo.app/callback        → stg
//
// (Bare `hanzo.app/...` entries are the apex, which is never a publishable
// `<slug>.hanzo.app` label; `*.hanzo.ai` entries are a different apex the sites edge
// never serves.)
var sharedAppRedirectLabels = []string{"www", "stg"}

// TestReservedCoversSharedAppRedirects pins the account-takeover invariant: every
// label the shared brand client can be redirected to under the published apex MUST
// be reserved, so an attacker can never first-come-claim `<label>.hanzo.app`, serve
// their own page, and harvest a victim's code minted for the shared client_id.
func TestReservedCoversSharedAppRedirects(t *testing.T) {
	for _, label := range sharedAppRedirectLabels {
		if !IsReserved(label) {
			t.Errorf("label %q is a shared hanzo-app OAuth redirect host but is NOT reserved — "+
				"an attacker could claim %q.hanzo.app and harvest codes for client_id=hanzo-app "+
				"(account takeover). Add it to baseReserved in reserved.go.", label, label)
		}
	}
}

// TestReservedStgIsTheClosedGap is the regression guard for the specific HIGH RED
// found: `stg` was a shared-client redirect (https://stg.hanzo.app/callback) that
// was NOT reserved. It must stay reserved.
func TestReservedStgIsTheClosedGap(t *testing.T) {
	if !IsReserved("stg") {
		t.Fatal("stg must be reserved (shared hanzo-app client registers stg.hanzo.app/callback)")
	}
	// Case-insensitive, like every reserved check.
	if !IsReserved("STG") {
		t.Fatal("reserved check must be case-insensitive for stg")
	}
}
