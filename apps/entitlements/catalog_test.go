package entitlements

// catalog_test.go is the GATE across the two lists that decide whether a paying org
// may open a console app, and it exists because NOTHING compared them before: cloud's
// appProducts (require.go) says which products the paywall ASKS about, and @hanzo/plans
// `entitlements["licensing.product_ids"]` says which products a tier can GRANT. Both
// are "the products"; neither ever read the other; they drifted until they shared one
// element out of eight.
//
// A drift here is never cosmetic, because the two failures it produces are opposite and
// both cost money:
//
//   - ASKED but never GRANTABLE — CheckEntitlement resolves cleanly and answers
//     Active:false for EVERY org on EVERY tier, so the licence leg of RequireProduct is
//     dead and the product is CLOSED to everyone the wallet leg does not carry. A
//     customer who bought the top tier is refused what they bought.
//   - GRANTABLE but never ASKED — a tier sells a product no gate consults, so the grant
//     buys the customer nothing and is billed for anyway.
//
// The gate is deliberately a COMPARISON, not a pin. Pinning today's five strings would
// have passed every day of the drift it is here to catch: both lists were internally
// consistent the whole time and wrong only about each other. So it re-derives BOTH
// sides on every run — appProducts from this package, the grants from the embedded
// catalog the binary actually links — and asserts they describe the same vocabulary.
//
// A THIRD list exists and this gate cannot reach it, so it is recorded here instead:
// @hanzogui/shell's APP_ENTITLEMENTS (hanzo-registry.ts) maps studio/bot/world/platform
// to a minimum tier of "pro" and gates on it CLIENT-SIDE, against a different endpoint
// (GET /v1/billing/subscriptions). That map is the only place the product intent — these
// apps are paid, Pro and above — is actually written down, and being client-side it is
// advice rather than enforcement. Its `world: 'pro'` even names the mechanism the catalog
// really used: "bundled via world-pro on pro/plus/max, world-team on team", i.e. the
// tier-level `bundles` field, which @hanzo/plans carried at v1.4.4 and DELETED at v1.4.11
// with nothing put in its place. So the honest count is three vocabularies, not two, and
// the server has never held the one that decides. Whoever resolves the failures below
// should collapse all three, not just the two this file can see.

import (
	"maps"
	"slices"
	"sort"
	"strings"
	"testing"

	hplans "github.com/hanzoai/plans"
)

// grants reports every product id the LINKED @hanzo/plans catalog can license, mapped
// to the tiers that license it. It walks the whole embedded catalog rather than
// reaching into subscription.json by name, so a tier that moves between catalog files
// keeps its grant visible to this gate instead of silently reading as ungranted.
//
// The key is NESTED — entitlements["licensing.product_ids"], never a top-level
// "product_ids" — and reading the wrong depth is not a near miss: it returns an empty
// set for every plan, which is a clean, confident, WRONG answer that makes this whole
// gate pass vacuously. Hence the positive control below: a probe that finds NO grants
// anywhere has broken its own query, and says so, instead of reporting a well-formed
// catalog as empty.
func grants(t *testing.T) map[string][]string {
	t.Helper()

	data, err := hplans.Data()
	if err != nil {
		t.Fatalf("read the embedded @hanzo/plans catalog: %v", err)
	}

	out := map[string][]string{}
	walk(data, func(m map[string]any) {
		ent, ok := m["entitlements"].(map[string]any)
		if !ok {
			return
		}
		ids, ok := ent["licensing.product_ids"].([]any)
		if !ok {
			return
		}
		tier, _ := m["id"].(string)
		if tier == "" {
			tier = "(unnamed tier)"
		}
		for _, id := range ids {
			if p, ok := id.(string); ok && p != "" {
				out[p] = append(out[p], tier)
			}
		}
	})

	// POSITIVE CONTROL. Not defensive noise — the one failure mode that would make
	// every assertion below succeed while proving nothing.
	if len(out) == 0 {
		t.Fatalf("catalog probe found NO licensing.product_ids anywhere in @hanzo/plans.\n" +
			"That is this gate's own query failing, not an empty catalog: the key is nested under\n" +
			"each tier's \"entitlements\" object. Fix the probe before trusting any result here.")
	}
	return out
}

// walk visits every JSON object in a decoded catalog value.
func walk(v any, visit func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		visit(x)
		for _, e := range x {
			walk(e, visit)
		}
	case []any:
		for _, e := range x {
			walk(e, visit)
		}
	}
}

// catalog renders the grant side for a failure message: which tier licenses what.
func catalog(g map[string][]string) string {
	var b strings.Builder
	for _, p := range slices.Sorted(maps.Keys(g)) {
		tiers := g[p]
		sort.Strings(tiers)
		b.WriteString("\n    " + p + " <- " + strings.Join(tiers, ", "))
	}
	return b.String()
}

// TestEveryProductCloudAsksAboutCanBeGranted is the CLOSED-to-everyone direction: a
// product the paywall gates on that no tier licenses can never answer Active:true, so
// its licence leg is dead on arrival and the gate degenerates to the wallet leg alone.
func TestEveryProductCloudAsksAboutCanBeGranted(t *testing.T) {
	g := grants(t)

	var dead []string
	for _, p := range appProducts {
		if len(g[p]) == 0 {
			dead = append(dead, p)
		}
	}
	if len(dead) == 0 {
		return
	}
	t.Fatalf("cloud gates on %d product(s) NO plan can grant: %s\n\n"+
		"  ASKED   apps/entitlements/require.go appProducts = %v\n"+
		"  GRANTED @hanzo/plans entitlements[\"licensing.product_ids\"]:%s\n\n"+
		"For each dead product CheckEntitlement resolves cleanly and answers Active:false for\n"+
		"EVERY org on EVERY tier, enterprise included. RequireProduct then admits only on the\n"+
		"wallet leg (cloud.Stand: LicenceNone + readable zero balance = Unpaid = 402), and\n"+
		"GET /v1/entitlements reports the app LOCKED to every customer unconditionally — the\n"+
		"projection consults no kill switch.\n\n"+
		"Resolve it on ONE side, never by widening this gate: either the catalog grants these\n"+
		"products to the tiers that sell them, or cloud stops asking the licence authority\n"+
		"about products it does not license. Before choosing, read @hanzogui/shell's\n"+
		"APP_ENTITLEMENTS — it already answers this question (studio/bot/world/platform = pro)\n"+
		"client-side, and the server has never agreed with it.",
		len(dead), strings.Join(dead, ", "), appProducts, catalog(g))
}

// engineLicence is the catalog vocabulary that is NOT the console's.
//
// TWO AUTHORITIES LICENSE THINGS HERE, and conflating them is what made this gate
// wrong rather than strict. The console gate (appProducts, apps/entitlements) asks
// commerce whether an ORG may open an APP. The engine licence is a different
// artifact entirely: apps/plan/licence.go stamps these ids into a SIGNED licence
// ("licensing.product:"+id) that a customer's own engine deployment verifies
// offline, and apps/commerce/client.go relays it. No console surface reads them and
// none should — an engine licence is not a door in this product.
//
// So an id here being absent from appProducts is the DESIGN, not a defect, and the
// test below must not demand the console consult it. Anything NOT in this set is
// still held to the paid-for-nothing rule.
var engineLicence = map[string]bool{"engine": true, "engine-rocm": true}

// TestEveryProductThePlansGrantIsAskedAbout is the paid-for-nothing direction: a tier
// that licenses a product no gate consults bills the customer for a grant that opens
// no door.
//
// Scoped to the console's own vocabulary: ids belonging to the engine-licence
// authority above are excluded, because the consumer that reads them is a signed
// licence rather than a gate, and requiring the console to consult them asserted a
// coupling this repo deliberately does not have.
func TestEveryProductThePlansGrantIsAskedAbout(t *testing.T) {
	g := grants(t)

	asked := make(map[string]bool, len(appProducts))
	for _, p := range appProducts {
		asked[p] = true
	}
	var unread []string
	for _, p := range slices.Sorted(maps.Keys(g)) {
		if !asked[p] && !engineLicence[p] {
			unread = append(unread, p)
		}
	}
	if len(unread) == 0 {
		return
	}
	t.Fatalf("%d product(s) are licensed by a plan and consulted by NOTHING: %s\n\n"+
		"  GRANTED @hanzo/plans entitlements[\"licensing.product_ids\"]:%s\n"+
		"  ASKED   apps/entitlements/require.go appProducts = %v\n\n"+
		"A grant no gate reads is a line the customer pays for that opens no door. Either the\n"+
		"consumer that should read it is missing, or the grant belongs in a vocabulary this one\n"+
		"is not — apps/plan/licence.go relays these ids verbatim into a signed engine licence\n"+
		"(Tokens: \"licensing.product:\"+p), which is a different authority from the console\n"+
		"paywall that reads the SAME list here.\n\n"+
		"One vocabulary must end up describing products. Resolve it in the catalog or in the\n"+
		"consumer — never by exempting an id from this gate.",
		len(unread), strings.Join(unread, ", "), catalog(g), appProducts)
}
