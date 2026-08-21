package marketing

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// It matters here because this surface is money, audience and consent, and none
// of the three is legible from a field name. `budget` and `spend` are USD cents,
// and `spend` is a figure the CALLER writes — no send, ad buy or invoice moves
// it — so a reader who takes the roll-up for an observed total has it backwards.
// `channel` is two closed vocabularies wearing one name: a campaign's is
// email|sms|social|meta|google|tiktok, a calendar post's is
// x|facebook|instagram|linkedin|tiktok|youtube|threads, and only tiktok is in
// both. A promo quote's `listCents` is PER SEAT on team while `chargeCents`
// totals every seat, so subtracting one from the other is wrong past one seat.
// And an audience preview's `unmatched` is the whole explanation for a cohort of
// 500 that mails 3 — the number that decides whether the send is worth making.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app, _ := mountRoutes(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "marketing", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("marketing publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/marketing describe",
			len(bare), strings.Join(bare, ", "))
	}
}
