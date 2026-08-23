// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// stubKeys installs an in-process key resolver over a fixed table, and clears BOTH
// resolver slots so a leaked fallback cannot answer instead.
func stubKeys(t *testing.T, table map[string]Attribution) {
	t.Helper()
	keyMu.Lock()
	origR, origF := keyResolver, keyFallback
	keyMu.Unlock()
	SetKeyResolver(fixedKeys(table))
	SetFallbackKeyResolver(nil)
	t.Cleanup(func() {
		SetKeyResolver(origR)
		SetFallbackKeyResolver(origF)
	})
}

// bootKeys is the resolver a process holds before anything installs one — read at
// package init, so the answer is the package's own default and not whatever the
// test that ran last left behind.
var bootKeys = currentKeyResolver()

type fixedKeys map[string]Attribution

func (f fixedKeys) Resolve(_ context.Context, key string) (Attribution, bool, error) {
	at, ok := f[key]
	return at, ok, nil
}

// failingKeys is the owning app being unreachable — an error, never a miss.
type failingKeys struct{}

func (failingKeys) Resolve(context.Context, string) (Attribution, bool, error) {
	return Attribution{}, false, errors.New("projects unreachable")
}

const siteKey = "pk-sitekeysitekeysitekeysitekeysitekey00"

// TestAdmitAsksBothIssuers is the whole of what Admit is: TWO issuers, and a key
// from either one names its org. They are disjoint — a key minted by a project
// exists only in the project store, one issued by IAM only in IAM — so an
// endpoint that asks a single issuer refuses every key the other minted, which
// is an endpoint that refuses the key it tells a caller to create. Every
// endpoint that admits a key calls this, so the sequence is proved once, here.
func TestAdmitAsksBothIssuers(t *testing.T) {
	stubKeys(t, map[string]Attribution{"pk-project": {Org: "acme", Project: "shop"}})
	orig := resolveKeyOrg
	resolveKeyOrg = func(_ context.Context, key string) (string, bool) {
		return "beta", key == "pk-iam"
	}
	t.Cleanup(func() { resolveKeyOrg = orig })

	for _, tc := range []struct {
		name string
		key  string
		want Attribution
		ok   bool
	}{
		// The project store answers the narrower question, so it answers first: org
		// AND the site the key was minted with.
		{"a project key", "pk-project", Attribution{Org: "acme", Project: "shop"}, true},
		// IAM can only ever say org, having no project to scope to — and an empty
		// project honestly says this write names no site.
		{"an IAM key", "pk-iam", Attribution{Org: "beta"}, true},
		{"a key neither issued", "pk-forged", Attribution{}, false},
		{"no key at all", "", Attribution{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, ok := Admit(context.Background(), tc.key)
			if ok != tc.ok || at != tc.want {
				t.Fatalf("Admit(%q) = %+v, %v; want %+v, %v", tc.key, at, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestProjectKeyAttributesToItsSite is the design: the key names org AND site, so a
// beacon lands in the project's org tagged with the project — an attribution the
// server states rather than accepts.
func TestProjectKeyAttributesToItsSite(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{siteKey: {Org: "acme", Project: "shop"}})
	w := fakeWarehouse(t)
	app := mountApp(t)

	code := postKeyed(t, app, "/v1/event", "", `{"type":"pageview","event":"$pageview"}`,
		map[string]string{"Authorization": "Bearer " + siteKey})
	if code != http.StatusOK {
		t.Fatalf("keyed beacon = %d, want 200", code)
	}
	if got := w.tenants(t); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("tenant = %v, want [acme]", got)
	}
	if len(w.facts) != 1 || w.facts[0].product != "shop" {
		t.Fatalf("product = %q, want shop — the key must name the site", w.facts[0].product)
	}
}

// TestProjectKeyOverridesTheBodysProduct: `product` is client-supplied and therefore
// not evidence. When the key names a project the server's answer wins, so a page
// shipping one project's key cannot file its rows under another's name.
func TestProjectKeyOverridesTheBodysProduct(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{siteKey: {Org: "acme", Project: "shop"}})
	w := fakeWarehouse(t)
	app := mountApp(t)

	code := postKeyed(t, app, "/v1/event", "",
		`{"type":"pageview","event":"$pageview","product":"someone-elses-site"}`,
		map[string]string{"Authorization": "Bearer " + siteKey})
	if code != http.StatusOK {
		t.Fatalf("keyed beacon = %d, want 200", code)
	}
	if len(w.facts) != 1 || w.facts[0].product != "shop" {
		t.Fatalf("product = %q, want shop — a body claim reached the fact", w.facts[0].product)
	}
}

// TestKeyRidesEveryCarrier: the project key travels on all three ingest carriers, so
// a page can use whichever its transport allows. The query carrier is load-bearing:
// navigator.sendBeacon cannot set headers, and that is the transport a real page
// uses on unload.
func TestKeyRidesEveryCarrier(t *testing.T) {
	roomyRate(t)
	for _, tc := range []struct {
		name, path string
		hdr        map[string]string
	}{
		{"bearer", "/v1/event", map[string]string{"Authorization": "Bearer " + siteKey}},
		{"ingest header", "/v1/event", map[string]string{"x-hanzo-ingest-key": siteKey}},
		{"beacon query", "/v1/event?ingest_key=" + siteKey, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubKeys(t, map[string]Attribution{siteKey: {Org: "acme", Project: "shop"}})
			w := fakeWarehouse(t)
			app := mountApp(t)
			code := postKeyed(t, app, tc.path, "", `{"type":"pageview","event":"$pageview"}`, tc.hdr)
			if code != http.StatusOK {
				t.Fatalf("%s = %d, want 200", tc.name, code)
			}
			if got := w.tenants(t); len(got) != 1 || got[0] != "acme" {
				t.Fatalf("%s tenant = %v, want [acme]", tc.name, got)
			}
		})
	}
}

// TestUnknownKeyRefusedAndWritesNothing: a key that names no project is 403, never a
// downgrade. Filing it anywhere would hide the rows in a partition its owner cannot
// read — the silent failure this change exists to end.
func TestUnknownKeyRefusedAndWritesNothing(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{})
	stubResolver(t, func(string) (string, bool) { return "", false })
	w := fakeWarehouse(t)
	app := mountApp(t)

	code := postKeyed(t, app, "/v1/event", "", `{"type":"pageview","event":"$pageview"}`,
		map[string]string{"Authorization": "Bearer " + siteKey})
	if code != http.StatusForbidden {
		t.Fatalf("unknown key = %d, want 403", code)
	}
	if got := w.tenants(t); len(got) != 0 {
		t.Fatalf("an unresolvable key wrote %v", got)
	}
}

// TestDeletedSiteStopsRecordingAtTheDoor is the CTO's rule end to end: the same key
// that was landing rows stops landing them the moment its project is gone.
func TestDeletedSiteStopsRecordingAtTheDoor(t *testing.T) {
	roomyRate(t)
	live := map[string]Attribution{siteKey: {Org: "acme", Project: "shop"}}
	stubKeys(t, live)
	stubResolver(t, func(string) (string, bool) { return "", false })
	w := fakeWarehouse(t)
	app := mountApp(t)

	body := `{"type":"pageview","event":"$pageview"}`
	hdr := map[string]string{"Authorization": "Bearer " + siteKey}
	if code := postKeyed(t, app, "/v1/event", "", body, hdr); code != http.StatusOK {
		t.Fatalf("precondition: keyed beacon = %d, want 200", code)
	}
	before := len(w.facts)

	delete(live, siteKey) // the project is deleted; the key now names nothing
	if code := postKeyed(t, app, "/v1/event", "", body, hdr); code != http.StatusForbidden {
		t.Fatalf("after delete = %d, want 403", code)
	}
	if len(w.facts) != before {
		t.Fatalf("a deleted site still wrote %d fact(s)", len(w.facts)-before)
	}
}

// TestKeylessBeaconRefusedAndWritesNothing: the defect this change removes. A keyless
// beacon used to be accepted into a reserved tenant and answered {"accepted":1} — its
// owner could not read the partition, so it lost everything behind a 200.
func TestKeylessBeaconRefusedAndWritesNothing(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{})
	w := fakeWarehouse(t)
	app := mountApp(t)

	code, body := doHost(t, app, "/v1/event", "", "", "cloud.hanzo.ai",
		`{"type":"pageview","event":"$pageview"}`)
	if code != http.StatusUnauthorized {
		t.Fatalf("keyless beacon = %d (%s), want 401", code, body)
	}
	if !strings.Contains(string(body), "ingest_key_required") {
		t.Fatalf("body = %s, want the ingest_key_required code", body)
	}
	if got := w.tenants(t); len(got) != 0 {
		t.Fatalf("a keyless beacon wrote %v", got)
	}
}

// TestBearerStillAttributes: console.hanzo.ai deliberately carries NO key — it is one
// brand-agnostic image, so a baked-in key would pin lux/zoo white-labels onto hanzo —
// and attributes through its IAM bearer instead. That path must keep working.
//
// A bearer names an ORG and no site, so the fact carries no product. That is the
// honest answer: `product` on the canonical wire is not a caller field at all, and the
// only thing that can state one is a key minted with a project.
func TestBearerStillAttributes(t *testing.T) {
	roomyRate(t)
	stubKeys(t, map[string]Attribution{})
	w := fakeWarehouse(t)
	app := mountApp(t)

	code, body := doHost(t, app, "/v1/event", "u_console", "hanzo", "console.hanzo.ai",
		`{"type":"pageview","event":"$pageview"}`)
	if code != http.StatusOK {
		t.Fatalf("bearer beacon = %d (%s), want 200", code, body)
	}
	if got := w.tenants(t); len(got) != 1 || got[0] != "hanzo" {
		t.Fatalf("tenant = %v, want [hanzo]", got)
	}
	if w.facts[0].product != "" {
		t.Fatalf("product = %q — a bearer names no site", w.facts[0].product)
	}
}

// TestResolverFailureIsNotAMiss: the owning app being unreachable must not read as
// "this site does not exist". Both refuse, but only one is the caller's to fix, and a
// transient failure must never be reported as a deleted project.
func TestResolverFailureIsNotAMiss(t *testing.T) {
	at, ok := resolveAttribution(context.Background(), siteKey)
	_ = at
	if ok {
		t.Fatal("precondition")
	}
	keyMu.Lock()
	origR, origF := keyResolver, keyFallback
	keyMu.Unlock()
	SetKeyResolver(failingKeys{})
	SetFallbackKeyResolver(nil)
	// RESTORE, not clear: clearing left the package's plane default nil for every
	// test that ran after this one, so what those tests measured depended on the
	// order they ran in.
	t.Cleanup(func() { SetKeyResolver(origR); SetFallbackKeyResolver(origF) })
	if _, ok := resolveAttribution(context.Background(), siteKey); ok {
		t.Fatal("a failing resolver must not attribute")
	}
}

// TestAttributionRequiresAnOrg: a resolver that answers found with no org is refused.
// An empty org would be a write with no tenant at all.
func TestAttributionRequiresAnOrg(t *testing.T) {
	stubKeys(t, map[string]Attribution{siteKey: {Org: "", Project: "shop"}})
	if _, ok := resolveAttribution(context.Background(), siteKey); ok {
		t.Fatal("an attribution with no org must be refused")
	}
}

// TestAttributeProjectIsPureAndTotal: the stamp reaches every event in a batch, and
// an empty project leaves the caller's value alone (a bearer names no site).
func TestAttributeProjectIsPureAndTotal(t *testing.T) {
	evs := []CaptureEvent{{Product: "a"}, {Product: "b"}, {}}
	out := attributeProject(evs, "shop")
	for i, e := range out {
		if e.Product != "shop" {
			t.Fatalf("event %d product = %q, want shop", i, e.Product)
		}
	}
	back := attributeProject([]CaptureEvent{{Product: "console"}}, "")
	if back[0].Product != "console" {
		t.Fatalf("empty project overwrote %q", back[0].Product)
	}
}

// TestTheKeyDoorNeedsNoMount: the cross-process resolver is the package DEFAULT, so
// every binary that links this package resolves a project key — the one that mounts
// analytics and the one that only calls Admit (apps/integrations serves the
// OpenRouter webhook). While a Mount installed it, a key resolved in one process and
// named nothing in the next, and no behavioural test inside this package could see
// that, because the package is correct either way. So the default itself is asserted.
func TestTheKeyDoorNeedsNoMount(t *testing.T) {
	if _, ok := bootKeys.(planeKeys); !ok {
		t.Fatalf("a process that mounts nothing resolves keys through %T, not the plane; "+
			"every project key it is handed would name no org", bootKeys)
	}
}
