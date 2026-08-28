package admin

// prose_test.go gates the FIELD half of this surface. projection_test.go proves every
// route reaches the document, the tool list and the CLI under one operation id; it says
// nothing about whether the SHAPES those projections carry mean anything to a reader.
//
// On the operator's board they carry the whole product, and most of them are money or
// measurement. `costUsd` on the gen_ai half of the AI board is DOLLARS while `costCents`
// on the ledger half is CENTS, and the two sit in one payload. A volume's `usedGiB` and
// `pct` are null meaning UNMEASURED rather than 0, and `complete: false` means the fleet
// numbers are unknown rather than zero — which is the only thing separating an outage
// from an empty account. `outstandingCents` is a liability, not granted minus consumed;
// `runwayDays` is null rather than infinite; `drift` is the operator's own verdict rather
// than a re-derivation of it. None of that is legible from a field name.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// proseless is the CLOSED list of published properties that carry NO description. Every
// entry is one generator limitation with one cause, and the prose it names is WRITTEN —
// on the type that declares the field, where the document already publishes it.
//
// Both groups are fields PROMOTED through an embedded struct. `MetricsData` embeds
// `SaaSMetrics` and `ServiceView` embeds `ServiceRow`; encoding/json inlines the
// embedded fields onto the outer object and zip's schema builder follows, looking each
// description up under the OUTER type's name — while zipdoc files a field's prose under
// the type that DECLARES it. Both halves are right and the two keys never meet. The
// generated files show it: `SaaSMetrics.asOf` and `ServiceRow.service` are emitted,
// `MetricsData.asOf` and `ServiceView.service` are what the lookup asks for.
//
// The two available workarounds each replace one true statement with two that can drift:
// unrolling the embedding changes the Go shape, and hand-writing a schema beside the
// struct is the drift reflection exists to prevent. So the limitation is recorded here
// instead, exactly, in BOTH directions — a bare property anywhere else goes red, and an
// entry that starts publishing prose goes red too. That is the day zipdoc follows an
// embedding, and this ledger empties rather than outliving the gap.
var proseless = map[string]bool{
	"MetricsData.asOf":          true,
	"MetricsData.currency":      true,
	"MetricsData.customers":     true,
	"MetricsData.gaps":          true,
	"MetricsData.orgs":          true,
	"MetricsData.revenue":       true,
	"MetricsData.subscriptions": true,
	"MetricsData.usage":         true,
	"MetricsData.window":        true,
	"ServiceView.createdAt":     true,
	"ServiceView.description":   true,
	"ServiceView.displayName":   true,
	"ServiceView.hosts":         true,
	"ServiceView.service":       true,
	"ServiceView.updatedAt":     true,
	"ServiceView.updatedBy":     true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema that
// carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, &cloud.Service[core.State]{State: core.State{}})

	doc, err := openapi.Spec(app, openapi.Info{Title: "admin", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("admin publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/admin describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
