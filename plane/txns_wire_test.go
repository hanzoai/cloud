// Copyright © 2026 Hanzo AI. MIT License.

package plane

import (
	"reflect"
	"testing"
)

// The layout of a plane type IS its wire: zapenc gives fields slots in
// DECLARATION ORDER and no name travels, so a field is its offset. That makes
// the compatibility rule structural — append at the end, and only at the end —
// and it makes an edit that looks harmless in a diff a silent re-interpretation
// of every peer's bytes.
//
// TxnsIn is the one that just grew: Subject went on the END, after Test and
// Limit, so a caller that never sets it sends the same bytes it always sent and
// the ledger answers for the whole org exactly as before. Reordering these three
// — putting Subject first, where a reader might feel it belongs beside the
// tenant — would hand the far end a page size where it expects a wallet, with
// nothing failing to say so.
//
// So the order is written down here, positionally, rather than trusted to
// review.
func TestTxnsInKeepsItsWireOrder(t *testing.T) {
	want := []string{"Test", "Limit", "Subject"}
	typ := reflect.TypeFor[TxnsIn]()
	if typ.NumField() != len(want) {
		t.Fatalf("TxnsIn has %d fields, want %d — a field was added somewhere other "+
			"than the end, or removed", typ.NumField(), len(want))
	}
	for i, name := range want {
		if got := typ.Field(i).Name; got != name {
			t.Errorf("slot %d is %s, want %s — the wire moved under every peer", i, got, name)
		}
	}
}

// The subject is a WALLET, never a tenant, and the difference is the whole
// reason it may ride the argument at all: the org rides the caller, so a field
// that could name one would let a caller read another tenant's books. This is
// the same rule TestNoPlaneInputCanNameAnOrg states for the plane's inputs, held
// here beside the type it was just added to.
func TestTxnsInNamesNoTenant(t *testing.T) {
	typ := reflect.TypeFor[TxnsIn]()
	for field := range typ.Fields() {
		if name := field.Name; name == "Org" || name == "Owner" {
			t.Errorf("TxnsIn.%s: the org rides the caller — a ledger read that can name "+
				"its own tenant reads somebody else's", name)
		}
	}
}
