package tel

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// address and its shape; the shape's fields come from a doc comment on each one,
// which zipdoc lifts per field.
//
// Telephony is a plane where a bare string is a failed call. Every number here is
// E.164 — a leading + and digits, nothing else — and `id` is the carrier's handle
// for a number while `e164` is the number itself, so buying by the wrong one buys
// nothing. `monthly` is a rental in the MINOR unit of `currency`, which is why the
// two are useless apart, and it is a quoted price rather than a charge. `capable`
// decides what a number can carry at all: one without "sms" cannot send one however
// the API is called. And a message `status` of "sent" means the carrier took it
// while "delivered" means a handset got it — not every destination reports the
// second, so waiting for it can wait forever.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(newOpsApp(t), openapi.Info{Title: "tel", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("tel publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/tel describe",
			len(bare), strings.Join(bare, ", "))
	}
}
