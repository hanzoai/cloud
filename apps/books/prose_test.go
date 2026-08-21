package books

// prose_test.go gates the half of the surface projection_test.go cannot see. That file
// proves every /v1/books route reaches OpenAPI, MCP and the CLI under one operation id
// — it says nothing about whether the SHAPES those projections carry mean anything to
// a reader.
//
// On this surface they carry almost the whole product. Every amount here is int64
// CENTS, and a bare integer named `debit` or `amount` cannot tell an SDK user which of
// three different conventions it is under: the trial balance places a net on the column
// its SIGN chose, the P&L flips income once for display so both halves read positive,
// and a bank row is always positive with `direction` carrying the sign. Three sign
// rules, one Go type, and only prose separates them. `totalIncome` being ACCRUAL —
// recognized, so a prepaid top-up is not in it — is likewise a fact about the number
// that no schema can hold.
//
// The gate checks presence, not meaning: a description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of published properties that carry NO description,
// and it is a property of the SEAM they arrived through rather than of anyone's
// diligence.
//
// ScanDraft reaches the document only through openapi.Register (scan.go), because
// POST /v1/books/scan takes a PDF as its raw body and so cannot be a typed op. Register
// derives a schema by REFLECTION from the Go type, and Go drops comments at compile
// time — zipdoc, the pass that lifts field prose, walks zip's TYPED registrations and
// can therefore never reach a type that arrives this way. Its fields carry doc comments
// in the Go source like every other type in this package; reflection simply cannot see
// them. The choice at that route was a bare shape or NO shape, and a bare shape is what
// lets an SDK offer a receipt upload with a return type at all.
//
// Note what is NOT here: Extracted, LineItem and Question are nested inside ScanDraft
// AND reachable from a typed op (InboxItem carries an *Extracted, questions has its own
// read), so the typed side describes them and the document publishes one described copy.
//
// The list is exact in BOTH directions. A bare property anywhere else is a typed op's,
// which zipdoc can describe, and goes red. An entry here that starts publishing prose
// also goes red — that is the day zip learns to lift comments through Register, and
// this ledger must shrink then rather than quietly outlive the limitation.
var proseless = map[string]bool{
	"ScanDraft.balanced":   true,
	"ScanDraft.category":   true,
	"ScanDraft.confidence": true,
	"ScanDraft.extracted":  true,
	"ScanDraft.questions":  true,
	"ScanDraft.scanId":     true,
	"ScanDraft.vendor":     true,
	"ScanDraft.voucher":    true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema that
// carries no description and is not in the ledger above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountBooks(t), openapi.Info{Title: "books", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("books publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted "+
			"onto the first of them alone — then run: make -C apps/books describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
