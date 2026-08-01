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
	"time"
)

// anon_autocapture_test.go — the anonymous lane's SECOND naming family, and the wall
// beside it.
//
// A logged-out visitor's interactions are what a heatmap is made of, and they were all
// discarded behind a 200: @hanzo/observe emits every autocapture through capture(), so a
// $click is `type:"event"`, and the `event` KIND is the whole custom commerce/billing
// surface — refused, correctly, since the beginning. Admitting it by widening the kind
// would have opened that surface to anyone on the internet.
//
// So the lane admits a closed set of NAMES under that kind instead, resolved through a
// server-owned table (publicNames). Both halves are load-bearing and both are pinned
// here: the interaction lands (TestAnonAutocapture_*), and an arbitrary name still
// cannot (TestAnonEventName_*). The second is the security property — it is the reason
// the door was narrow, and narrowing is not what changed.
//
// Same observable as its neighbours: 503 ⇒ ADMITTED (reached the write core; no
// warehouse in the harness). 200 {accepted:0,dropped:N} ⇒ the projection refused it.

// The wire a logged-out page actually emits for an autocaptured click: @hanzo/event
// capture() ⇒ type "event" + the reserved name, with the url/path @hanzo/event 0.3.6
// stamps and the @hanzo/observe annotation in the bag.
const anonClick = `{"batch":[{"type":"event","event":"$click","distinctId":"anon-1",` +
	`"sessionId":"s1","product":"site","url":"https://hanzo.ai/pricing","path":"/pricing",` +
	`"library":"@hanzo/event","libraryVersion":"0.3.6",` +
	`"properties":{"$el":"navigation/Pricing/button[cta]","$role":"button","$testid":"cta",` +
	`"$name":"Get Started","$component":"PricingCTA","$path":["navigation","Pricing","button[cta]"]}}]}`

// ── the feature: an anonymous interaction lands ──────────────────────────────

// TestAnonAutocapture_ClickAdmittedThroughThePublicDoor is the headline. No bearer, no
// key, no team token — the exact shape hanzo.ai emits — and it reaches the write core.
func TestAnonAutocapture_ClickAdmittedThroughThePublicDoor(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	code, body := doHost(t, app, "/v1/event", "", "", "hanzo.ai", anonClick)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("anonymous $click = %d (%s), want 503 ADMITTED — a logged-out interaction is "+
			"the bulk of what a heatmap is drawn from, and it was being dropped behind a 200",
			code, body)
	}
}

// TestAnonAutocapture_ClickAdmittedOnThePostHogWire: the second wire the one door
// speaks (insights.hanzo.ai rewrites eight SDK spellings onto it). Its adapter maps only
// $pageview to a kind, so every other name arrives as `event` — exactly the shape the
// name table decides.
func TestAnonAutocapture_ClickAdmittedOnThePostHogWire(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	body := `{"event":"$click","distinct_id":"anon-1",` +
		`"properties":{"$current_url":"https://hanzo.ai/pricing","$pathname":"/pricing","$el":"nav/button[cta]"}}`
	if code, got := doHost(t, app, "/v1/event", "", "", "insights.hanzo.ai", body); code != http.StatusServiceUnavailable {
		t.Fatalf("anonymous $click on the PostHog wire = %d (%s), want 503 ADMITTED", code, got)
	}
}

// TestAnonAutocapture_StoresTheRealURL walks the pure projection into the REAL
// normalizer, because "accepted" is not the claim — a click that lands without the page
// it happened on is a counter, not a heatmap. The stored fact must carry the url, the
// path, and the element identity, under the public tenant.
func TestAnonAutocapture_StoresTheRealURL(t *testing.T) {
	evs, err := decodeEvent([]byte(anonClick))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, dropped := admitPublic(evs)
	if len(out) != 1 || dropped != 0 {
		t.Fatalf("admitPublic = %d admitted / %d dropped, want 1/0", len(out), dropped)
	}
	f, ok := normalize(publicTenant, time.Now(), out[0])
	if !ok {
		t.Fatal("the admitted click must be routable — an unnamed track is dropped by the write core")
	}
	if f.org != publicTenant {
		t.Fatalf("fact org = %q, want %q", f.org, publicTenant)
	}
	if f.name != "$click" {
		t.Fatalf("stored name = %q, want $click", f.name)
	}
	if f.url != "https://hanzo.ai/pricing" || f.path != "/pricing" {
		t.Fatalf("stored url/path = %q / %q, want the real page the click happened on", f.url, f.path)
	}
	if f.el.label != "navigation/Pricing/button[cta]" || f.el.role != "button" || f.el.testid != "cta" {
		t.Fatalf("element identity did not survive: %+v — a click with no element is a count, not a heatmap", f.el)
	}
	if len(f.el.path) != 3 {
		t.Fatalf("el.path = %v, want the three-node trail", f.el.path)
	}
}

// ── the wall: an arbitrary name is still refused ─────────────────────────────

// TestAnonEventName_ArbitraryRefused is THE security property, and the reason this door
// admits a table rather than a kind. Every name here is a real attack on the warehouse
// or on a read lens: the commerce lenses COUNT names (`countIf(event = 'order_completed')`),
// and an unattested caller minting names is unbounded cardinality in a key space the
// server is supposed to own.
func TestAnonEventName_ArbitraryRefused(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, name := range []string{
		"order_completed",     // poisons the revenue/commerce lenses
		"signup_completed",    // poisons the GTM funnel
		"$autocapture",        // a NEIGHBOURING reserved name we do not mint
		"$pageview",           // a real name, but it is a KIND here — one way to say it
		"$click_",             // one byte off an admitted name
		"$click evil",         // an admitted name with a payload appended
		"$$$$$$$$$$$$$$$$$$$", // pure cardinality
		"",                    // the unnamed track the write core drops anyway
	} {
		body := `{"batch":[{"type":"event","event":"` + name + `","distinctId":"attacker","path":"/pricing"}]}`
		code, got := doHost(t, app, "/v1/event", "", "", "hanzo.ai", body)
		if code == http.StatusServiceUnavailable {
			t.Errorf("anonymous event %q reached the write core — the lane admits a CLOSED set of "+
				"names, never a caller-chosen one", name)
			continue
		}
		if code != http.StatusOK {
			t.Errorf("anonymous event %q = %d (%s), want 200 all-dropped", name, code, got)
			continue
		}
		if r := receipt(t, got); r.Accepted != 0 || r.Dropped != 1 {
			t.Errorf("anonymous event %q receipt = %+v, want accepted:0 dropped:1", name, r)
		}
	}
}

// TestAnonEventName_CannotBuyAKind: the two allowlists are ANDed, never ORed. An
// admitted name carried by a person- or group-binding kind is still refused — the name
// table widens the `event` kind alone, and `identify`/`group` mean something a caller
// nobody vouched for may not say at all.
func TestAnonEventName_CannotBuyAKind(t *testing.T) {
	roomyRate(t)
	app := mountApp(t)
	for _, kind := range []string{"identify", "group"} {
		body := `{"batch":[{"type":"` + kind + `","event":"$click","distinctId":"victim","personId":"victim-person"}]}`
		code, got := doHost(t, app, "/v1/event", "", "", "hanzo.ai", body)
		if code == http.StatusServiceUnavailable {
			t.Errorf("anonymous %s named $click reached the write core — an admitted NAME must not "+
				"admit a refused KIND", kind)
			continue
		}
		if r := receipt(t, got); r.Accepted != 0 || r.Dropped != 1 {
			t.Errorf("anonymous %s named $click receipt = %+v, want accepted:0 dropped:1", kind, r)
		}
	}
}

// TestAnonAutocapture_NameIsTheServersNotTheCallers is the property stated exactly: the
// bytes stored in `name` come from publicNames, never from the request. A caller that
// spells an admitted name differently gets the CANONICAL one — so a variant spelling is
// one name in the read lenses, not a second, and the lookup's tolerance cannot become a
// cardinality hole.
func TestAnonAutocapture_NameIsTheServersNotTheCallers(t *testing.T) {
	for _, wire := range []string{"$click", "$CLICK", " $Click ", "$cLiCk"} {
		out, dropped := admitPublic([]CaptureEvent{{Type: "event", Event: wire}})
		if len(out) != 1 || dropped != 0 {
			t.Fatalf("spelling %q: admitted %d dropped %d, want 1/0", wire, len(out), dropped)
		}
		if out[0].Event != "$click" {
			t.Fatalf("spelling %q stored as %q — the stored name must be the table's value, "+
				"never the caller's bytes", wire, out[0].Event)
		}
		f, ok := normalize(publicTenant, time.Now(), out[0])
		if !ok || f.name != "$click" {
			t.Fatalf("spelling %q normalized to %q (routable=%v), want $click", wire, f.name, ok)
		}
	}
}

// TestAnonAutocapture_VocabularyIsClosed is the DRIFT GATE. Every test above derives its
// bodies from the table, so they would stay green while the table grew — which is the
// one change that widens the anonymous surface. This is the assertion that has to be
// edited deliberately.
//
// The set is the client's, not an invention: @hanzo/observe names an interaction from
// its kind (observer.ts NAME) and emits exactly these five through capture(). Its sixth,
// nav, calls pageview() and arrives as the pageview KIND — so $pageview belongs in
// publicKinds and must NOT appear here.
func TestAnonAutocapture_VocabularyIsClosed(t *testing.T) {
	want := map[string]string{
		"$click":  "$click",
		"$input":  "$input",
		"$change": "$change",
		"$submit": "$submit",
		"$view":   "$view",
	}
	if len(publicNames) != len(want) {
		t.Fatalf("publicNames = %v, want %v — adding a name is a public surface change", publicNames, want)
	}
	for k, v := range want {
		if publicNames[k] != v {
			t.Fatalf("publicNames[%q] = %q, want %q", k, publicNames[k], v)
		}
	}
	// Every entry is admitted end to end, and stores its own name: a table entry that
	// the pipeline drops downstream would be a promise this file cannot keep.
	for wire, stored := range publicNames {
		out, dropped := admitPublic([]CaptureEvent{{Type: "event", Event: wire, Path: "/pricing"}})
		if len(out) != 1 || dropped != 0 {
			t.Fatalf("%q: admitted %d dropped %d, want 1/0", wire, len(out), dropped)
		}
		f, ok := normalize(publicTenant, time.Now(), out[0])
		if !ok || f.name != stored {
			t.Fatalf("%q normalized to %q (routable=%v), want %q", wire, f.name, ok, stored)
		}
	}
}

// ── the property bag ────────────────────────────────────────────────────────

// TestAnonAutocapture_OnlyTheAnnotationCrosses: the interaction carries the element
// identity a heatmap needs and NOTHING else. The annotation is the one property family
// that widens nothing — annotationOf lifts those keys into the `el` tuple and
// attributesOf skips the same set — so every other key the caller sent, including the
// $-prefixed ones @hanzo/observe itself emits, is dropped at the projection.
func TestAnonAutocapture_OnlyTheAnnotationCrosses(t *testing.T) {
	out, _ := admitPublic([]CaptureEvent{{
		Type:  "event",
		Event: "$input",
		Properties: map[string]any{
			"$el":       "form/input[email]",
			"$role":     "textbox",
			"$kind":     "email",                                       // @hanzo/observe emits it; not an annotation key
			"$value":    map[string]any{"redacted": true, "length": 9}, // the redacted field state
			"tenant_id": "maxpower",                                    // the classic attribution forgery
			"password":  "hunter2",
		},
	}})
	if len(out) != 1 {
		t.Fatal("want 1 admitted event")
	}
	got := out[0].Properties
	if len(got) != 2 || got["$el"] != "form/input[email]" || got["$role"] != "textbox" {
		t.Fatalf("projected properties = %v, want exactly the annotation keys the caller sent", got)
	}
	// Through the real normalizer nothing the caller chose reaches the attributes map —
	// the dictionary an unbounded anonymous bag would attack.
	f, ok := normalize(publicTenant, time.Now(), out[0])
	if !ok {
		t.Fatal("want routable")
	}
	for _, k := range []string{"$kind", "$value", "tenant_id", "password"} {
		if _, bad := f.attributes[k]; bad {
			t.Fatalf("caller key %q reached the attributes dictionary: %v", k, f.attributes)
		}
	}
	if f.el.label != "form/input[email]" || f.el.role != "textbox" {
		t.Fatalf("annotation did not reach the el tuple: %+v", f.el)
	}
}

// TestAnonPageview_StillCarriesNoCallerName: the older family is untouched. A pageview's
// name is the ROUTE's, so a caller naming one cannot smuggle a name in through the kind
// that ignores it — the projection drops `event` on the way past, exactly as before.
func TestAnonPageview_StillCarriesNoCallerName(t *testing.T) {
	out, _ := admitPublic([]CaptureEvent{{Type: "pageview", Event: "order_completed", Path: "/pricing"}})
	if len(out) != 1 {
		t.Fatal("want 1 admitted pageview")
	}
	if out[0].Event != "" {
		t.Fatalf("projected pageview carries name %q — the kind family must drop the caller's name "+
			"and let resolveName supply it", out[0].Event)
	}
	f, ok := normalize(publicTenant, time.Now(), out[0])
	if !ok || f.name != "page_viewed" {
		t.Fatalf("stored pageview name = %q (routable=%v), want the route's own page_viewed", f.name, ok)
	}
}
