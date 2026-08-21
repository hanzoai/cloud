package visor

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// ADDRESS and its SHAPE; the shape's FIELDS come from a different place — a doc
// comment on each one, which zipdoc lifts one at a time — so a fully typed plane can
// still publish a document nobody can read.
//
// This is the compute plane, so the unreadable fields are the ones that cost money or
// decide whether work can run. Every memory figure here is BYTES except machineView's
// `mem`, which is a rendered display string, and gpuView's `memory`, which is VRAM in
// whatever units the card's own tooling used; `gpuUtil` is a fraction of 1 and never a
// percentage; `costCents` is whole US cents where 0 means UNPRICED rather than free.
// A GPU count is per unit — for a cluster the vendor totals across every node — and a
// cluster's `nvidiaGpu`/`amdGpu` are an inventory taken once at attach, not live
// capacity. The status words are three vocabularies this surface deliberately does
// not merge: the provider's own machine states, passed through unmapped; vm's
// reconciled binding states, capitalized on its own terms; and the queue's closed
// lifecycle plus `stalled`, which this surface alone mints for a job whose worker
// died holding the lease.
//
// The gate checks PRESENCE, not meaning. A description that restates the field's name
// is worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of published properties carrying NO description
// because the SEAM they arrive through cannot carry one — not because nobody wrote
// it. Every field named here HAS its doc comment in the source; the generator cannot
// reach it from where the schema is built.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day zipdoc
// learns, and the ledger must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// EMBEDDED STRUCT. botView embeds machineView and clusterDetailView embeds
	// clusterView, and zip's schema builder INLINES an embedded struct's fields into
	// the outer component (openapi.go wireFields) while zipdoc files a field's prose
	// under the type that DECLARES it (internal/zipdoc extract.go structFields). So
	// the prose for these lands on machineView.* and clusterView.* — where it is
	// published, and where these rows repeat it verbatim — and nothing can key it to
	// the outer name. The two workarounds both cost more than the gap: unrolling the
	// embedding into copies, or writing a schema by hand beside the struct, each
	// replace one true statement with two that can drift.
	"botView.createdTime":             true,
	"botView.gpu":                     true,
	"botView.id":                      true,
	"botView.image":                   true,
	"botView.mem":                     true,
	"botView.name":                    true,
	"botView.os":                      true,
	"botView.privateIp":               true,
	"botView.provider":                true,
	"botView.publicIp":                true,
	"botView.region":                  true,
	"botView.status":                  true,
	"botView.type":                    true,
	"botView.vcpu":                    true,
	"clusterDetailView.amdGpu":        true,
	"clusterDetailView.createdAt":     true,
	"clusterDetailView.doClusterId":   true,
	"clusterDetailView.doksClusterId": true,
	"clusterDetailView.kind":          true,
	"clusterDetailView.name":          true,
	"clusterDetailView.nodeCount":     true,
	"clusterDetailView.nodePools":     true,
	"clusterDetailView.nodeSize":      true,
	"clusterDetailView.nvidiaGpu":     true,
	"clusterDetailView.region":        true,
	"clusterDetailView.status":        true,

	// ANONYMOUS STRUCT. `nodePool` on the cluster-create body is an inline struct
	// literal, and a field's prose is filed under the name of the type that declares
	// it — which an anonymous struct does not have. The property itself IS described
	// (the comment on the NodePool field lands); its three members cannot be. Naming
	// the type would fix it and would ALSO change the published document, turning an
	// inline object into a $ref to a new component in all eight SDKs — a shape
	// change, decided on purpose, not a side effect of writing prose.
	"createClusterReq.nodePool.count": true,
	"createClusterReq.nodePool.name":  true,
	"createClusterReq.nodePool.size":  true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(projectionApp(t), openapi.Info{Title: "visor", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("visor publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/visor describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
