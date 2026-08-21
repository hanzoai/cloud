package link

// prose_test.go gates the FIELD half of this surface. typed_compat_test.go proves every
// operation reaches the document, the MCP tool list and the CLI under one registry
// entry; it says nothing about whether the SHAPES those projections carry mean anything
// to a reader, and those come from a different place — a doc comment on each field,
// which zipdoc lifts one at a time.
//
// It matters here because this surface publishes two ledgers that must never be added
// together, and only prose says so. `costCents` is int64 CENTS as the PROVIDER's own
// meter stated it — a plan's spend, not a Hanzo charge — while `usedPct` is 0..100 of
// an allowance and is not money at all; a row's `source` and `scope` are what separate
// them, so a caller who cannot read those two fields will sum a percentage into a bill.
// `available:false` is the other one: it means the ledger did not answer, which is a
// different claim from zero usage, and a dashboard that reads it as 0 reports an outage
// as thrift.
//
// The gate checks presence, not meaning. A description that restates the field's name
// is worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of published properties that carry NO description
// because the SEAM they arrived through cannot carry one — not because nobody wrote
// one. Every field below IS described in the Go source; the two ends key it on
// different names.
//
// EMBEDDED STRUCT. ingestReq accepts one sample or many (usage.go): `samples` is the
// batch, and a single sample rides the SAME fields inline, which it gets by embedding
// readingReq. encoding/json promotes those fields onto the outer object and zip's
// schema builder follows (openapi.go wireFields), so they publish as ingestReq's own
// properties and are looked up under `ingestReq.<name>`. zipdoc files a field's prose
// under the type whose declaration IS the struct literal, so all 21 comments are filed
// under `readingReq.<name>`. Both halves are right and the keys do not meet.
//
// The prose is not missing from the DOCUMENT: readingReq is published too, as the item
// type of `samples`, and carries all 21 descriptions. Unrolling the embedding into a
// second copy of the 21 fields would close this by replacing one true statement with
// two that can drift — and the one-or-many body is exactly the shape that would drift.
//
// Exact in BOTH directions: a bare property anywhere else goes red, and an entry here
// that starts publishing prose goes red too, which is the day zipdoc learns to follow a
// promoted field to its declaring type.
var proseless = map[string]bool{
	"ingestReq.account":           true,
	"ingestReq.cachedInputTokens": true,
	"ingestReq.confidence":        true,
	"ingestReq.costCents":         true,
	"ingestReq.costLimitCents":    true,
	"ingestReq.currency":          true,
	"ingestReq.inputTokens":       true,
	"ingestReq.kind":              true,
	"ingestReq.lane":              true,
	"ingestReq.machine":           true,
	"ingestReq.outputTokens":      true,
	"ingestReq.plan":              true,
	"ingestReq.provider":          true,
	"ingestReq.requests":          true,
	"ingestReq.resetsAt":          true,
	"ingestReq.synthetic":         true,
	"ingestReq.totalTokens":       true,
	"ingestReq.usedPct":           true,
	"ingestReq.window":            true,
	"ingestReq.windowMinutes":     true,
	"ingestReq.windowStart":       true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountLink(t), openapi.Info{Title: "link", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("link publishes no schemas at all — the gate would pass vacuously")
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
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/links describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
