package sites

import (
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
)

// unseed returns the policy to its pre-publication state so a test can observe what
// a FRESH PROCESS reads. Every setter marks the policy seeded, and the package
// globals outlive a test, so without this a test that runs after any other would be
// reading the previous test's publication rather than the derivation under test.
func unseed(t *testing.T) {
	t.Helper()
	reset := func() {
		policyMu.Lock()
		extraReserved, selfDomains, seeded = map[string]bool{}, nil, false
		policyMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// TestPolicyDerivesFromEnvWithoutAServer is the PROCESS-TOPOLOGY regression: the
// self/reserved policy must be readable by whichever process asks, not only by the
// one that constructs a Server.
//
// cloud runs ONE PROCESS PER APP. plugin/projects links this package for exactly
// these two predicates — it is where the custom-domain CLAIM gate and the
// site_hosts storage invariant both live — and it never calls New, because the
// edge is not in that process. So the set New publishes was EMPTY there:
// IsSelfHost answered false for everything, `api.hanzo.ai` read as not-ours, and
// the claim gate that TestSelfHostsAreNotClaimable proves correct was inert in the
// only process that runs it. The tests could not see it — they publish the set
// themselves, in one binary, which is the same blind spot the risk scorer client has.
//
// The fix is to derive from config wherever the question is asked. ConfigFromEnv is
// the ONE reader of that config and every process has the same environment, so the
// answers cannot drift the way a hand-off between processes does. Note the
// production default needs no deployment change: hanzo.ai is DERIVED from
// CLOUD_DOMAIN's registrable domain, not configured.
func TestPolicyDerivesFromEnvWithoutAServer(t *testing.T) {
	prodEnv(t)
	unseed(t) // as a process that never constructs a Server starts

	for _, h := range []string{"hanzo.ai", "api.hanzo.ai", "login.hanzo.ai", "hanzo.app", "x.hanzo.app"} {
		if !IsSelfHost(h) {
			t.Errorf("IsSelfHost(%q) = false with no Server in this process — the claim gate "+
				"and the host table both run HERE, and they would let a tenant take the name", h)
		}
	}
	// The operator's extra labels arrive by the same route.
	if !IsReserved("stg") { // prodEnv sets CLOUD_SITES_RESERVED=stg
		t.Error("IsReserved(stg) = false — operator extras never reached a Server-less process")
	}
	// And it stays narrow: a real customer domain is not ours.
	if IsSelfHost("yadota.tech") || Ours("www.example.com") {
		t.Error("the derived set claimed a customer domain")
	}
}

// The derivation is reached from request goroutines, so first touch is concurrent
// by construction — this is the shape a process actually starts in. Run under -race.
func TestSeedIsConcurrencySafe(t *testing.T) {
	prodEnv(t)
	unseed(t)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			if !Ours("api.hanzo.ai") || !Ours("api") || Ours("www.example.com") {
				t.Error("the policy answered wrong under a concurrent first touch")
			}
		})
	}
	wg.Wait()
}

// TestOursSplitsOnShape pins the one predicate over the two shapes site_hosts
// stores. A bare LABEL is the reserved-subdomain policy (it would publish as
// <label>.<apex>); a HOSTNAME is the self-domain set.
//
// The storage gate used to ask IsReserved with whichever it was given. A whole FQDN
// against a set of bare labels matches nothing, so `login.hanzo.ai` passed the only
// guard behind the claim gate. The naive repair — compare the first label of an
// FQDN — is worse than the hole: `www` and `login` are reserved labels AND the two
// most common custom domains a customer brings, so it would refuse the ordinary
// case. Splitting on shape is what answers both without either error.
func TestOursSplitsOnShape(t *testing.T) {
	prodEnv(t)
	unseed(t)
	New(ConfigFromEnv(""), luxlog.New("test"))

	for _, name := range []string{
		"api", "login", "www", "", // bare labels: the reserved policy
		"hanzo.ai", "login.hanzo.ai", "x.hanzo.app", // hostnames: the self-domain set
	} {
		if !Ours(name) {
			t.Errorf("Ours(%q) = false — a name the platform holds is claimable", name)
		}
	}
	for _, name := range []string{
		"yadota", "myblog", // ordinary site slugs
		"www.example.com", "login.example-bank.com", "api.yadota.tech", // customer hostnames
		"hanzo.ai.evil.test", // a suffix that only LOOKS like ours
	} {
		if Ours(name) {
			t.Errorf("Ours(%q) = true — a customer's own name was refused", name)
		}
	}
}

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
