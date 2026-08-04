// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package analytics

import (
	"fmt"
	"net/http"
	"strings"
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
	code, body := postAnon(t, app, "/v1/event", anonClick, nil)
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
	if code, got := postAnon(t, app, "/v1/event", body, nil); code != http.StatusServiceUnavailable {
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
	f, ok := normalize("acme", time.Now(), out[0])
	if !ok {
		t.Fatal("the admitted click must be routable — an unnamed track is dropped by the write core")
	}
	if f.org != "acme" {
		t.Fatalf("fact org = %q, want %q", f.org, "acme")
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
		refusedAnon(t, "anonymous event "+name, code, got)
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
		refusedAnon(t, "anonymous "+kind+" named $click", code, got)
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
		f, ok := normalize("acme", time.Now(), out[0])
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
		f, ok := normalize("acme", time.Now(), out[0])
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
	f, ok := normalize("acme", time.Now(), out[0])
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
	f, ok := normalize("acme", time.Now(), out[0])
	if !ok || f.name != "page_viewed" {
		t.Fatalf("stored pageview name = %q (routable=%v), want the route's own page_viewed", f.name, ok)
	}
}

// ── the hole Red found: an anonymous error named itself ──────────────────────

// TestAnonError_NameIsNeverTheCallersExceptionClass is the HIGH-1 regression, and it is
// the sharpest test in this file because the bug it pins was invisible from the door:
// the request 200s either way, and the caller's bytes only appear once the row is
// normalized.
//
// The kind family used to be admitted with an EMPTY name so resolveName would supply the
// route's default. That is true for a pageview and was FALSE for an error: resolveName
// is shared with the credentialed lane, where naming a row after its exception class is
// correct, so `{"type":"error","error":{"type":"…"}}` walked 60 KiB of caller-chosen
// bytes into `name` — fifty distinct per request, and on a published-site host into a
// REAL org's partition.
func TestAnonError_NameIsNeverTheCallersExceptionClass(t *testing.T) {
	for _, class := range []string{
		"TypeError",                // the honest one: still must not name the row
		strings.Repeat("N", 3000),  // cardinality by length
		strings.Repeat("N", 60000), // the demonstrated 60 KiB
		"order_completed",          // poisons the commerce lenses through the error door
		"$click",                   // an admitted autocapture name, via the wrong family
	} {
		e := foldException(CaptureEvent{Type: "error", Error: &Exception{Type: class, Message: "boom"}})
		out, dropped := admitPublic([]CaptureEvent{e})
		if len(out) != 1 || dropped != 0 {
			t.Fatalf("class %.20q: admitted %d dropped %d, want 1/0 — an anonymous error still lands",
				class, len(out), dropped)
		}
		f, ok := normalize("acme", time.Now(), out[0])
		if !ok {
			t.Fatalf("class %.20q: admitted error must stay routable", class)
		}
		if f.name != nameError {
			t.Fatalf("class %.20q: stored name = %.40q (len %d), want the server's %q — an unattested "+
				"caller must not name a row", class, f.name, len(f.name), nameError)
		}
	}
}

// TestAnonError_ClassStillGroupsTheIssue guards the OTHER side of that fix. Closing the
// name must not cost the error stream its grouping: the class still reaches the fault
// body and still feeds the fingerprint, so two anonymous TypeErrors remain one issue and
// a TypeError and a RangeError remain two. A fix that made every anonymous error one
// undifferentiated blob would have been a regression wearing a security badge.
func TestAnonError_ClassStillGroupsTheIssue(t *testing.T) {
	fact := func(class, msg string) fact {
		e := foldException(CaptureEvent{Type: "error", Error: &Exception{Type: class, Message: msg}})
		out, _ := admitPublic([]CaptureEvent{e})
		f, ok := normalize("acme", time.Now(), out[0])
		if !ok {
			t.Fatalf("class %q must stay routable", class)
		}
		return f
	}
	// Same failure twice, then the SAME message under a different class — which isolates
	// the class as the grouping input, since the message is held constant.
	a, b, c := fact("TypeError", "cannot read x"), fact("TypeError", "cannot read x"), fact("RangeError", "cannot read x")
	if a.class != "TypeError" {
		t.Fatalf("class did not survive onto the fact: %+v — grouping is built on it", a)
	}
	if a.issue != b.issue {
		t.Error("the same failure got two groups — grouping is not deterministic")
	}
	if a.issue == c.issue {
		t.Error("two classes share a group — the fingerprint stopped reading the class, which is " +
			"the fact `name` no longer carries")
	}
}

// TestAnonError_OversizeClassIsDropped: the same hole one column over, closed with the
// name. `class` is not free text — it leads the fingerprint and so the error table's
// ORDER BY — so an anonymous caller may not spend a whole request on it. Over the bound
// the class is dropped and the error still lands, falling back to the message-shape
// grouping the fingerprint already implements.
func TestAnonError_OversizeClassIsDropped(t *testing.T) {
	huge := strings.Repeat("N", maxClass+1)
	e := foldException(CaptureEvent{Type: "error", Error: &Exception{Type: huge, Message: "boom"}})
	out, dropped := admitPublic([]CaptureEvent{e})
	if len(out) != 1 || dropped != 0 {
		t.Fatalf("admitted %d dropped %d, want the error to still land", len(out), dropped)
	}
	f, ok := normalize("acme", time.Now(), out[0])
	if !ok {
		t.Fatal("want routable")
	}
	if f.class != "" {
		t.Fatalf("stored class len %d — over %d must be dropped, never clipped", len(f.class), maxClass)
	}
	if f.issue == "" {
		t.Error("dropping the class must not cost the row its group — the message-shape fallback exists for this")
	}
	// A class at exactly the bound is a real class and must survive.
	ok2 := foldException(CaptureEvent{Type: "error", Error: &Exception{Type: strings.Repeat("N", maxClass)}})
	got, _ := admitPublic([]CaptureEvent{ok2})
	if got[0].Error.Type == "" {
		t.Error("a class AT the bound was dropped — the bound is inclusive")
	}
}

// ── the annotation's own bound (Red MEDIUM-2) ────────────────────────────────

// TestAnonAnnotation_OversizeIsDropped pins the bound the request cap does not give.
// maxPublicBytes bounds a REQUEST; it does not bound one stored VALUE, and inside 64 KiB
// a caller can spend nearly all of it on a single $el or a very wide $path. Those land in
// the `el` tuple, and this package is forbidden from declaring the DDL that would narrow
// that column (capture_test.go: cloud writes the plane, o11y owns its schema) — so the
// writer bounds the value instead.
//
// The event still lands: the annotation is enrichment, so an out-of-bounds one costs
// context, never the interaction.
func TestAnonAnnotation_OversizeIsDropped(t *testing.T) {
	wide := make([]any, 6000)
	for i := range wide {
		wide[i] = "step"
	}
	for _, tc := range []struct {
		what  string
		props map[string]any
	}{
		{"a 40 KiB label", map[string]any{"$el": strings.Repeat("A", 40000)}},
		{"a label one byte over", map[string]any{"$el": strings.Repeat("A", maxAnnotation+1)}},
		{"a 6000-element trail", map[string]any{"$path": wide}},
		{"a trail one step over", map[string]any{"$path": make([]any, maxAnnotationPath+1)}},
		{"a huge step inside a short trail", map[string]any{"$path": []any{"ok", strings.Repeat("A", 40000)}}},
		{"an object where a label belongs", map[string]any{"$el": map[string]any{"nested": "x"}}},
		{"a number where a label belongs", map[string]any{"$el": 12345}},
	} {
		out, dropped := admitPublic([]CaptureEvent{{Type: "event", Event: "$click", Properties: tc.props}})
		if len(out) != 1 || dropped != 0 {
			t.Fatalf("%s: admitted %d dropped %d — the click must still land", tc.what, len(out), dropped)
		}
		if out[0].Properties != nil {
			t.Errorf("%s: reached the projection as %+v — an out-of-bounds annotation is not carried",
				tc.what, out[0].Properties)
		}
		f, ok := normalize("acme", time.Now(), out[0])
		if !ok {
			t.Fatalf("%s: want routable", tc.what)
		}
		if !f.el.empty() {
			t.Errorf("%s: reached the el tuple: %+v", tc.what, f.el)
		}
		for k := range f.attributes {
			if k != "$source" {
				t.Errorf("%s: caller key %q reached the attributes dictionary", tc.what, k)
			}
		}
	}
}

// TestAnonAnnotation_RealClientOutputFits is the other half, and the reason the bound is
// READ OFF THE CLIENT rather than invented. @hanzo/observe walks at most maxDepth=12
// ancestors and clips every accessible name to MAX_NAME=80, so its worst honest $el is
// around 1.4 KiB. A bound that clipped real output would silently degrade every heatmap
// on a deep page — so the worst thing the shipped client can emit must pass untouched.
func TestAnonAnnotation_RealClientOutputFits(t *testing.T) {
	// Twelve steps of `role[name]` at the client's own 80-char name ceiling.
	step := "navigation[" + strings.Repeat("N", 81) + "]"
	label := step
	trail := []any{step}
	for i := 1; i < 12; i++ {
		label += "/" + step
		trail = append(trail, step)
	}
	if len(label) > maxAnnotation {
		t.Fatalf("the client's own worst label is %d bytes, over the server bound %d — the bound is "+
			"too tight and would clip real output", len(label), maxAnnotation)
	}
	out, _ := admitPublic([]CaptureEvent{{
		Type: "event", Event: "$click",
		Properties: map[string]any{"$el": label, "$path": trail, "$role": "button"},
	}})
	f, ok := normalize("acme", time.Now(), out[0])
	if !ok {
		t.Fatal("want routable")
	}
	if f.el.label != label {
		t.Fatalf("the client's worst honest label did not survive (stored %d of %d bytes)",
			len(f.el.label), len(label))
	}
	if len(f.el.path) != 12 {
		t.Fatalf("el.path = %d steps, want the client's full 12", len(f.el.path))
	}
}

// TestAnonAnnotation_BoundsAreDerivedNotInvented is the drift gate for the numbers
// themselves. They are the client's, scaled: 2 KiB clears @hanzo/observe's ~1.4 KiB worst
// label, and 32 steps is 2.6x its maxDepth=12. If someone tightens them below what the
// client emits, the test above fails; if someone loosens them without a reason, this one
// makes it a deliberate edit rather than a drift.
func TestAnonAnnotation_BoundsAreDerivedNotInvented(t *testing.T) {
	if maxAnnotation != 2<<10 {
		t.Errorf("maxAnnotation = %d, want 2 KiB — the bound clears the client's ~1.4 KiB worst label", maxAnnotation)
	}
	if maxAnnotationPath != 32 {
		t.Errorf("maxAnnotationPath = %d, want 32 — 2.6x @hanzo/observe's maxDepth=12", maxAnnotationPath)
	}
	if maxClass != 256 {
		t.Errorf("maxClass = %d, want 256 — far past any runtime's exception class", maxClass)
	}
}

// ownerOrg is a REAL org — the projected lane files into one (a team guest writes
// into the org that invited it), which is what makes these rules load-bearing.
const ownerOrg = "hanzo"

// TestAnonError_RealOrgNeverTakesACallerChosenName is Red's probe, kept: the projected
// lane files into a REAL org (a team guest writes into the org that invited it), so a
// caller-chosen error class would mint cardinality in that org's ORDER BY key. Fifty
// distinct classes in ONE request — the batch ceiling — must produce fifty rows all
// named `error`.
//
// Driven at the projection, which is where the rule lives: admitPublic decides what is
// admitted, normalize stamps the tenant and the name.
func TestAnonError_RealOrgNeverTakesACallerChosenName(t *testing.T) {
	evs := make([]CaptureEvent, 0, maxPublicBatch)
	for i := 0; i < maxPublicBatch; i++ {
		// Each one distinct, and long enough that a survivor is unmistakable.
		evs = append(evs, CaptureEvent{
			Type: "error", Path: "/pricing",
			Error: &Exception{Type: fmt.Sprintf("RED-%d-%s", i, strings.Repeat("N", 200)), Message: "boom"},
		})
	}
	admitted, dropped := admitPublic(evs)
	if len(admitted) != maxPublicBatch || dropped != 0 {
		t.Fatalf("admitted %d dropped %d, want %d/0 — the errors must still land",
			len(admitted), dropped, maxPublicBatch)
	}
	names := map[string]int{}
	for _, e := range admitted {
		f, ok := normalize(ownerOrg, time.Now(), foldException(e))
		if !ok {
			t.Fatal("want routable")
		}
		names[f.name]++
		if f.org != ownerOrg {
			t.Fatalf("fact landed in %q, want the real org %q", f.org, ownerOrg)
		}
		if strings.Contains(f.name, "RED-") || len(f.name) > 64 {
			t.Fatalf("caller bytes reached `name`: %.60q (len %d)", f.name, len(f.name))
		}
	}
	if len(names) != 1 || names[nameError] != maxPublicBatch {
		t.Fatalf("fifty caller-chosen classes produced %d distinct names (%v) — an unattested caller "+
			"must not mint cardinality in a real org's ORDER BY key", len(names), names)
	}
}

// ── an exception is the error family's field (Red MEDIUM-2) ──────────────────

// TestAnonAutocapture_CarriesNoException is the MEDIUM-2 regression, and it is written
// through the REAL PIPELINE ORDER because the ordering IS the bug: publicIngest projects
// (admitPublic) and only then does ingestDecoded fold (foldException). A test that folded
// first — as the error tests above legitimately do, to build a realistic error — would
// have the projection see an already-stamped bag and would MISS this entirely.
//
// foldException stamps attributes['$exception'] onto whatever it is handed, so while the
// projection carried Error onto every admitted kind, an anonymous `$click` shipped the
// caller's whole exception — message and stack included — into the attributes dictionary.
// Measured at 32 KiB from a single request. The class bound does not reach it: the bytes
// are in Message and Stack, which are free text on purpose.
func TestAnonAutocapture_CarriesNoException(t *testing.T) {
	huge := &Exception{
		Type:    "TypeError",
		Message: strings.Repeat("M", 22000),
		Stack:   strings.Repeat("S", 10000),
	}
	for _, tc := range []struct{ kind, event string }{
		{"event", "$click"},
		{"event", "$input"},
		{"event", "$submit"},
		{"pageview", ""}, // the other kind whose row is not a fault
	} {
		out, dropped := admitPublic([]CaptureEvent{{
			Type: tc.kind, Event: tc.event, Path: "/pricing", Error: huge,
		}})
		if len(out) != 1 || dropped != 0 {
			t.Fatalf("%s/%s: admitted %d dropped %d — the row must still land", tc.kind, tc.event, len(out), dropped)
		}
		if out[0].Error != nil {
			t.Errorf("%s/%s: the projection carried an exception onto a row that is not a fault", tc.kind, tc.event)
		}
		// The fold runs AFTER the projection, exactly as ingestDecoded runs it.
		f, ok := normalize("acme", time.Now(), foldException(out[0]))
		if !ok {
			t.Fatalf("%s/%s: want routable", tc.kind, tc.event)
		}
		if v, ok := f.attributes["$exception"]; ok {
			t.Errorf("%s/%s: %d caller bytes reached attributes['$exception'] — an interaction is not a fault",
				tc.kind, tc.event, len(v))
		}
		if faulted(f) {
			t.Errorf("%s/%s: a non-error row grew error columns", tc.kind, tc.event)
		}
	}
}

// TestAnonError_StillCarriesItsException is the other half, and it is what keeps the fix
// from being an over-correction. Dropping the exception everywhere would have been easy
// and would have silently emptied the anonymous error stream — which is a real product,
// not a tolerated side effect. On the ONE kind whose row IS a fault, the exception must
// survive the projection intact: the class into `class`, the message into the fold's
// attribute, and a group so the issue still stitches.
func TestAnonError_StillCarriesItsException(t *testing.T) {
	out, dropped := admitPublic([]CaptureEvent{{
		Type: "error", Path: "/pricing",
		Error: &Exception{Type: "TypeError", Message: "cannot read x", Stack: "at f (a.js:1:1)"},
	}})
	if len(out) != 1 || dropped != 0 {
		t.Fatalf("admitted %d dropped %d", len(out), dropped)
	}
	if out[0].Error == nil {
		t.Fatal("the projection dropped the exception from an ERROR — the fix over-reached and the " +
			"anonymous error stream is now empty")
	}
	f, ok := normalize("acme", time.Now(), foldException(out[0]))
	if !ok {
		t.Fatal("want routable")
	}
	if f.name != nameError {
		t.Fatalf("stored name = %q, want the server's %q", f.name, nameError)
	}
	if f.signal != signalError || f.class != "TypeError" {
		t.Fatalf("the class did not reach the error columns: %+v", f)
	}
	if f.issue == "" {
		t.Error("an anonymous error must still group into an issue")
	}
	if f.attributes["$exception"] == "" {
		t.Error("the folded $exception is the error lens's own read path and must still be stamped")
	}
}

// TestAnonAutocapture_NoExceptionReachesARealOrg is the same closure asserted where the
// attack actually lands. On a published-site host the anonymous projection files rows
// under the SITE'S REAL ORG, so an unbounded attribute there is not a $public-partition
// nuisance — it is caller-controlled bytes in a customer's own dictionary, written by
// someone holding no credential at all.
//
// It asserts on the FACTS the write path emitted rather than on the status code, because
// the door answered 200 before the fix and answers 200 after: the whole bug lived past
// the receipt.
func TestAnonAutocapture_NoExceptionReachesARealOrg(t *testing.T) {
	admitted, dropped := admitPublic([]CaptureEvent{{
		Type: "event", Event: "$click",
		URL: "https://yadota.hanzo.ai/pricing", Path: "/pricing",
		Properties: map[string]any{"$el": "nav/button[cta]"},
		Error: &Exception{
			Type:    "TypeError",
			Message: strings.Repeat("M", 22000),
			Stack:   strings.Repeat("S", 10000),
		},
	}})
	if len(admitted) != 1 || dropped != 0 {
		t.Fatalf("admitted %d dropped %d, want 1/0 — the click is admitted, it just carries no fault",
			len(admitted), dropped)
	}
	// The REAL pipeline order: the projection runs first, foldException second.
	f, ok := normalize(ownerOrg, time.Now(), foldException(admitted[0]))
	if !ok {
		t.Fatal("want routable")
	}
	if f.org != ownerOrg {
		t.Fatalf("fact landed in %q, want the site's real org %q", f.org, ownerOrg)
	}
	if f.name != "$click" {
		t.Fatalf("stored name = %q, want $click", f.name)
	}
	if v, ok := f.attributes["$exception"]; ok {
		t.Fatalf("%d caller bytes reached a REAL org's attributes dictionary on an interaction row", len(v))
	}
	if faulted(f) {
		t.Error("an autocapture row grew error columns in a real org")
	}
	// The interaction itself must survive — the point of the lane is the heatmap.
	if f.el.label != "nav/button[cta]" {
		t.Errorf("el.label = %q, want the annotation to still land", f.el.label)
	}
}

// ── the lens names an anonymous caller must never be able to mint (Red CRITICAL-1) ──

// lensNames are the event names the READ side counts as money and reach. They are
// copied from the lenses themselves — campaign.go's impressions/clicks/conversions and
// analytics.go's orders — because that is what makes this test a statement about
// consequence rather than about strings:
//
//	countIf(name = 'order_completed')                                        → orders
//	countIf(name = 'order_completed' OR 'signup_completed' OR 'conversion')  → conversions
//	countIf(name = 'click' OR 'ad_click')                                    → clicks
//	countIf(name = 'impression' OR 'ad_impression')                          → impressions
//
// `name` is the WHOLE predicate for each — there is no second column narrowing it to a
// vouched-for writer — so a row an anonymous caller named is a row that counts. That is
// what made the naming hole a forgery primitive and not a cardinality nuisance.
var lensNames = []string{
	"order_completed", "signup_completed", "conversion",
	"click", "ad_click", "impression", "ad_impression",
}

// TestAnonLane_CannotMintALensName is CRITICAL-1 stated as its consequence. The earlier
// tests assert the mechanism (the stored name is a server constant); this one asserts
// the DAMAGE is unreachable, through every wire field on this lane that has ever fed
// resolveName — the event name, the exception class, and the metric name.
//
// It is the test that would have caught the original bug from the outside: it never
// mentions resolveName, so it survives any future refactor of how the name is chosen.
func TestAnonLane_CannotMintALensName(t *testing.T) {
	for _, want := range lensNames {
		for _, tc := range []struct {
			how string
			ev  CaptureEvent
		}{
			{"as a custom event name", CaptureEvent{Type: "event", Event: want}},
			{"as an exception class", CaptureEvent{Type: "error", Error: &Exception{Type: want, Message: "boom"}}},
			{"as an exception class on a pageview", CaptureEvent{Type: "pageview", Error: &Exception{Type: want}}},
			{"as a metric name", CaptureEvent{Type: "metric", Event: "$click", Metric: &MetricBody{Name: want, Value: 1}}},
			{"as a metric name under an error", CaptureEvent{Type: "error", Metric: &MetricBody{Name: want, Value: 1}}},
			{"as an autocapture name", CaptureEvent{Type: "event", Event: "$click", Properties: map[string]any{"$el": want}}},
		} {
			out, _ := admitPublic([]CaptureEvent{tc.ev})
			for _, adm := range out {
				f, ok := normalize("acme", time.Now(), foldException(adm))
				if !ok {
					continue // unroutable is a drop, which is a pass
				}
				if f.name == want {
					t.Errorf("%q %s: reached `name` — an unattested caller just moved a money lens",
						want, tc.how)
				}
			}
		}
	}
}

// TestAnonAutocapture_IsNotTheAdLensClick is the near-miss that makes the closure fragile
// on purpose, and it is the reason lensNames carries the bare `click` and `impression`.
//
// The heatmap vocabulary is `$click`; the campaign lens counts `click`. One '$' is the
// entire distance between a heatmap and a forged ad click, and nothing in the type system
// keeps them apart — a future tidy-up that "normalized" the reserved prefix away, or a
// publicNames entry written without it, would silently turn the anonymous lane into a
// billing-metric writer. This pins the prefix as load-bearing rather than cosmetic.
func TestAnonAutocapture_IsNotTheAdLensClick(t *testing.T) {
	for _, n := range []string{"$click", "$view"} {
		out, _ := admitPublic([]CaptureEvent{{Type: "event", Event: n, Path: "/pricing"}})
		if len(out) != 1 {
			t.Fatalf("%s must be admitted — it is the heatmap", n)
		}
		f, ok := normalize("acme", time.Now(), out[0])
		if !ok {
			t.Fatalf("%s must be routable", n)
		}
		if !strings.HasPrefix(f.name, "$") {
			t.Fatalf("stored name %q lost its reserved prefix — it is now in the lens namespace", f.name)
		}
		for _, lens := range lensNames {
			if f.name == lens {
				t.Fatalf("the autocapture vocabulary collided with the lens name %q", lens)
			}
		}
	}
	// And the bare lens spellings are not in the table at all.
	for _, n := range []string{"click", "impression", "conversion"} {
		if _, ok := publicNames[n]; ok {
			t.Errorf("publicNames admits %q — that is a lens name, not an interaction", n)
		}
	}
}

// faulted reports whether a fact carries any of the ERROR columns. With one table the
// old question — "did this row grow a fault body?" — is answered by the columns rather
// than by a pointer, which is strictly the stronger assertion: a body could be present
// and empty, a column cannot.
func faulted(f fact) bool {
	return f.signal == signalError || f.class != "" || f.issue != "" || len(f.frames) > 0
}
