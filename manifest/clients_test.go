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
	{"billing.hanzo.ai", "/v1/billing/methods/{id}"},
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

	// cloud's OWN billing app, reading and removing saved cards over the commerce
	// S2S seam (apps/billing/billing.go paymentMethods + deletePaymentMethod). It
	// is a first-party client like any other and belongs here for exactly the
	// reason the metering gate does: nobody watches an S2S call fail. This one
	// failed silently for as long as it has existed — the card list came back 404
	// and rendered as "no cards saved", which is what made "card save is broken"
	// true end to end.
	//
	// It cannot ask for /v1/billing/methods, the customer address it publishes
	// itself: that forward re-enters its own handler. The portal family is the
	// address commerce publishes for a host that fronts it.
	{"cloud billing app", "/v1/billing/portal/methods"},
}

// broken is the LEDGER: addresses a client still asks for that the fleet does
// not answer, and why they are not fixed here. Same contract as router_test.go's
// `unreachable` — these are DEFECTS, recorded so the next one cannot hide among
// them, and the list may only SHRINK. An entry that stops being true fails this
// test exactly as loudly as a new one.
// It is EMPTY, and that is a state it is allowed to be in — not a reason to
// delete the mechanism. The ledger's whole value is that the NEXT defect cannot
// hide among the recorded ones, and a list with nothing in it says that most
// clearly. All three entries it held were closed together:
//
//   - GET /v1/billing/portal/methods. Now claimed on commerce's manifest row and
//     registered co-resident (apps/commerce/mount.go). The gate the entry called
//     for is TokenRequired + PinBillingSubject: TokenRequired resolves the org
//     from the gateway-pinned X-Org-Id for BOTH principals that reach here (an
//     IAM member and the raw service token — IAMTokenRequired admits only the
//     first), and PinBillingSubject is the IDOR control the entry said was
//     missing, overwriting every billing-subject key with the validated caller's
//     own subject so a browser cannot name another tenant's customerId, passing
//     the query through only for a verified COMMERCE_SERVICE_TOKEN bearer, and
//     fail-closing anyone who is neither.
//
//   - DELETE /v1/billing/methods/{id}. billing registers the sub-resource now and
//     proxies it to commerce's DELETE /v1/billing/portal/methods/{id} — the
//     non-self-dispatching target the entry said did not exist, added in the
//     commerce repo beside the portal read it mirrors. (PATCH is still not
//     served: nothing asks for it. `calls` is what a client is entitled to, and
//     no client edits a card.)
//
//   - POST /v1/billing/payment. DELETED rather than served — see
//     apps/account/account.go, where the caller was. It is not a rename and there
//     was nothing to point it at: money-IN has one door (commerce's mint-gated
//     POST /v1/billing/deposit) and the fleet deliberately routes NO mint address
//     at the edge, so serving this would have opened the mint surface to a
//     client-supplied amount and a client-supplied subject — the exact shape the
//     mint gate exists to refuse.
var broken = map[string]string{}

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
