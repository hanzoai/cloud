package manifest

// The CLIENT ORACLE: does an address a client ASKS FOR reach an app that SERVES it?
//
// router_test.go asks the mirror-image question — every path an app publishes,
// does the fleet deliver it to that app — and it is complete on that side. Both
// sides being complete on their own is exactly how six instances of one defect
// shipped: the server renamed nine billing routes to drop compound words
// (payment-methods → methods, payment-config → settings, auto-recharge →
// recharge, spend-alerts → alerts, credit-grants → credits, and four more), both
// server repos converged, every published document agreed with itself, every
// server test passed — and not one client followed. The clients' OWN tests
// passed too, because a test that pins the URL its client sends is a test of
// nothing: it agrees with the client while the client is wrong.
//
// Nobody was asking the only question that spans the gap. This file asks it.
//
// Two things make an address work, and a client needs BOTH:
//
//   - ROUTED. manifest.Apps must hand the path to some app. A billing leaf no
//     row names deeper reaches no app at all (nothing claims the bare /v1/billing
//     remainder — apps.go says why), so it falls to the tail "/v1" row and
//     answers a 404 that looks exactly like a typo.
//   - SERVED. That app must actually register it. Routing a path to an app that
//     does not answer it just moves the 404.
//
// Neither side of this comparison is derived from the other. `calls` is
// hand-authored source, like Apps is; the router is built from Apps through the
// real zip.Load; and `served` reads each app's own subset, projected by that
// app's binary from that app's router and forced back to source by `make test`.
//
// Adding a client call means adding a line here. That is the point: the address
// a client uses is a fleet-wide commitment, and this is where the fleet finds
// out.

import (
	"strings"
	"testing"
)

// calls is every address a first-party client asks the fleet for.
//
// It is HAND-AUTHORED, and it is SOURCE — the same posture as Apps. It is not
// scraped from the clients, because a scrape would only tell us what they do
// today; this says what they are entitled to.
//
// `by` is the client, so a failure names who breaks. `path` is the address in
// OpenAPI template form ({id}, not :id) because that is the form both the router
// and the app subsets speak.
var calls = []struct{ by, path string }{
	// billing.hanzo.ai — lib/commerce-client.ts, against commerce-api.hanzo.ai
	// (the same fleet edge as api.hanzo.ai).
	{"billing.hanzo.ai", "/v1/billing/methods"},
	{"billing.hanzo.ai", "/v1/billing/alerts"},
	{"billing.hanzo.ai", "/v1/billing/alerts/{id}"},
	{"billing.hanzo.ai", "/v1/billing/credits"},
	{"billing.hanzo.ai", "/v1/billing/invoices"},
	{"billing.hanzo.ai", "/v1/billing/plans"},
	{"billing.hanzo.ai", "/v1/billing/subscriptions"},
	{"billing.hanzo.ai", "/v1/billing/subscribe/card"},
	{"billing.hanzo.ai", "/v1/billing/topup/token"},
	{"billing.hanzo.ai", "/v1/billing/usage"},
	{"billing.hanzo.ai", "/v1/billing/balance"},

	// console.hanzo.ai — src/lib/api/billing.ts, same-origin then proxied
	// verbatim, so the address it sends is the address the fleet must route.
	{"console.hanzo.ai", "/v1/billing/methods"},
	{"console.hanzo.ai", "/v1/billing/alerts"},
	{"console.hanzo.ai", "/v1/billing/settings"},
	{"console.hanzo.ai", "/v1/billing/usage"},

	// @hanzo/commerce — pkgs/commerce/client.ts, default base api.hanzo.ai.
	{"@hanzo/commerce", "/v1/billing/methods"},
	{"@hanzo/commerce", "/v1/billing/plans"},
	{"@hanzo/commerce", "/v1/billing/subscriptions"},
	{"@hanzo/commerce", "/v1/billing/balance"},

	// The platform auto-recharge sweep — universe/infra/k8s/commerce/
	// autorecharge-cron.yaml, in-cluster to cloud. Not a browser, and the one
	// caller here that nobody would notice failing: it charges saved cards for
	// orgs that dropped below their threshold, on a schedule, with no user
	// watching. It pointed at the pre-rename address for as long as the rename
	// has existed.
	{"autorecharge CronJob", "/v1/billing/recharge/run-all"},

	// The request-edge metering gate — cloud's own S2S read of the per-scope
	// spend cap, on every metered call.
	{"metering gate", "/v1/billing/alerts/authorize"},
}

// broken is the LEDGER: addresses a client still asks for that the fleet does
// not answer, and why they are not fixed here. Same contract as router_test.go's
// `unreachable` — these are DEFECTS, recorded so the next one cannot hide among
// them, and the list may only SHRINK. An entry that stops being true fails this
// test exactly as loudly as a new one.
var broken = map[string]string{
	// POST /v1/billing/payment — billing.hanzo.ai records a crypto or wire
	// payment here (recordCryptoPayment / recordWirePayment, both with live
	// callers). No app in the fleet has ever registered it, so both have always
	// failed; the client swallows the error, which is why nobody noticed. It is
	// NOT a rename — there is no short name to converge on — so fixing it means
	// deciding what records an off-rail payment, which is a product decision and
	// not a renaming one. Recorded rather than guessed at.
	"/v1/billing/payment": "no app has ever served it; needs a product decision, not a rename",

	// DELETE and PATCH /v1/billing/methods/{id} — removing or editing a saved
	// card. The billing app owns the /v1/billing/methods prefix and registers only
	// the COLLECTION (GET list, POST save), so the sub-resource misses on method:
	// the live edge answers 405 to DELETE today. A customer can add a card and
	// never remove one.
	//
	// The handlers exist in the vendored module (UpdatePaymentMethod,
	// DetachPaymentMethod). What does not exist is a safe way to reach them from
	// here: the billing app would have to proxy an address it owns itself, which
	// is the self-dispatch loop that produced the depth-8 502s on top-up (see
	// apps/commerce/mount.go). Fixing it is a plumbing decision on the money path,
	// so it is recorded, not guessed at.
	"/v1/billing/methods/{id}": "billing owns the prefix but registers only the collection; the sub-resource needs a proxy target that does not self-dispatch",

	// GET /v1/billing/portal/methods — the org's saved cards, masked. This is the
	// one that makes "card save is broken" true END TO END: cloud's billing app
	// serves GET /v1/billing/methods by proxying to this address, and NOTHING in
	// the fleet serves it. No manifest row claims /v1/billing/portal and no app
	// registers it, so the proxy forwards a 404 verbatim and the saved-card list
	// is empty no matter how many cards were vaulted. (Until this commit the proxy
	// also asked for the retired /v1/billing/portal/payment-methods, so it was
	// wrong twice; the name is fixed here, the missing owner is not.)
	//
	// Registering it is NOT a one-liner and must not be treated as one.
	// PortalPaymentMethods keys tenancy on a `customerId` QUERY PARAM, so exposing
	// it needs a chain that pins the subject. The console chain (IAMTokenRequired
	// + PinBillingSubject) cannot serve this caller — it arrives with the service
	// token, not an IAM JWT — and the S2S chain (TokenRequired) authenticates
	// without pinning, which would let any authenticated browser read another
	// tenant's cards by passing their customerId. That is an IDOR, and choosing
	// the gate is a security design decision, not a route.
	"/v1/billing/portal/methods": "nothing serves it; the handler keys tenancy on a query param, so it needs a subject-pinning gate that works for a service token — an IDOR control, not a route",
}

// TestEveryAddressAClientCallsIsRoutedAndServed is the gate.
//
// It fails on the ROUTING and on the app's OWN projection, so no committed
// document and no client-side test can talk it out of firing.
func TestEveryAddressAClientCallsIsRoutedAndServed(t *testing.T) {
	fleet := router(t)

	// One read per app, not one per call: several clients ask for the same
	// address, and `served` shells out to a file each time.
	surface := map[string]map[string]bool{}
	for _, a := range Apps {
		set := map[string]bool{}
		for _, p := range served(t, a.Name) {
			set[p] = true
		}
		surface[a.Name] = set
	}

	seen := map[string]bool{}
	checked := 0
	for _, c := range calls {
		if why, ok := broken[c.path]; ok {
			t.Errorf("%s calls %s, which is in the broken ledger (%s) — a recorded defect may not also be claimed as a working call", c.by, c.path, why)
			continue
		}
		checked++
		seen[c.path] = true

		to := destination(t, fleet, c.path)
		if to == "nothing" {
			t.Errorf("NOT ROUTED: %s calls %s and manifest.Apps hands it to no app.\n"+
				"  It falls past every prefix to the tail \"/v1\" row, which does not serve it — a 404 that reads like a typo.\n"+
				"  Fix: name it on the row of the app that serves it, in manifest/apps.go.", c.by, c.path)
			continue
		}
		if !surface[to][c.path] {
			t.Errorf("ROUTED BUT NOT SERVED: %s calls %s; the fleet delivers it to %q, which does not register it.\n"+
				"  plugin/%s/openapi.json is projected from that app's own router, and %s is absent.\n"+
				"  Either the app must register it, or the client is asking for the wrong name.", c.by, c.path, to, to, c.path)
		}
	}

	// A gate that examined nothing passes. Refuse the vacuous run: every way this
	// could check zero addresses — an empty ledger, subsets that decoded to
	// nothing — is a defect elsewhere arriving here as a green tick.
	if checked == 0 {
		t.Fatal("no client addresses were examined — this gate proved nothing")
	}

	// A ledger entry that has stopped being true is a fix nobody recorded. Same
	// rule as router_test.go: the list may only shrink, and shrinking it is a
	// deliberate line-edit, never a silent drift.
	for path, why := range broken {
		if seen[path] {
			continue
		}
		if to := destination(t, fleet, path); to != "nothing" && surface[to][path] {
			t.Errorf("STALE LEDGER: %s is recorded broken (%s) but the fleet now routes it to %q and %q serves it.\n"+
				"  Delete the entry and add the call to `calls`.", path, why, to, to)
		}
	}

	t.Logf("%d client addresses routed and served across %d apps", checked, len(surface))
}

// TestNoClientAsksForACompoundName is the SPECIFIC shape this class took, kept
// as its own gate because the general one above can only catch a compound name
// after someone writes it down — and the whole failure was that nobody did.
//
// The billing namespace already says "billing". A route under it that repeats
// the noun (billing/payment-methods, billing/spend-alerts) is a name that has
// not earned its place, and every one of them was renamed away. This refuses the
// re-introduction by NAME rather than by reachability, so it fires even while a
// stale row still happens to route the old address somewhere.
func TestNoClientAsksForACompoundName(t *testing.T) {
	// Each retired name and what replaced it, so a failure is a fix and not a
	// puzzle. These are the exact renames of "no compound words in the billing
	// surface"; both server repos have converged on the right-hand side.
	retired := map[string]string{
		"/v1/billing/payment-methods": "/v1/billing/methods",
		"/v1/billing/payment-config":  "/v1/billing/settings",
		"/v1/billing/auto-recharge":   "/v1/billing/recharge",
		"/v1/billing/spend-alerts":    "/v1/billing/alerts",
		"/v1/billing/credit-grants":   "/v1/billing/credits",
		"/v1/billing/usage-rollup":    "/v1/billing/usage/rollup",
		"/v1/billing/gpu-charge":      "/v1/billing/gpu/charge",
		"/v1/billing/gpu-eligibility": "/v1/billing/gpu/eligibility",
		"/v1/billing/test-mode":       "/v1/billing/mode",
	}

	for _, c := range calls {
		for old, now := range retired {
			if c.path == old || strings.HasPrefix(c.path, old+"/") {
				t.Errorf("%s calls the retired name %s — use %s (the namespace already says billing)", c.by, c.path, now)
			}
		}
	}

	// The manifest must not name a retired address either: a row that still
	// claims one routes a dead name to a live app, which is how a client keeps
	// getting a plausible-looking answer from the wrong door.
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			if now, ok := retired[p]; ok {
				t.Errorf("manifest row %q claims the retired prefix %s — use %s", a.Name, p, now)
			}
		}
	}
}
