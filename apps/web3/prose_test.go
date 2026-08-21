package web3

// prose_test.go gates the FIELD half of this surface. Typing a route documents
// its address and its shape; the shape's fields come from a different place — a
// doc comment on each one, which zipdoc lifts per field — so a fully typed plane
// can still publish a wholly unreadable document.
//
// It matters here because every value on this surface is a chain's, and a chain's
// values do not mean what a reader assumes. `id` is this deployment's OWN
// registry key (the `:chain` segment), while `chainId` is the EIP-155 number a
// wallet signs against — two different identities of one chain, and confusing
// them signs a transaction for the wrong network. A balance is a 0x-QUANTITY
// string rather than a number because wei does not survive float64. And an rpc
// answer is JSON-RPC's own envelope: a failed call is a 200 carrying `error`,
// never an HTTP status, so a reader who watches the status code sees every
// failure as a success.
//
// The gate checks presence, not meaning. A description that restates the field's
// name is worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// proseless is the CLOSED list of published properties that carry NO description
// because the SEAM they arrived through cannot carry one — not because nobody
// wrote it. It is exact in BOTH directions: a bare property anywhere else goes
// red, and an entry here that starts publishing prose goes red too, which is the
// day the generator learns and this ledger must shrink rather than outlive the
// gap.
var proseless = map[string]bool{
	// EMBEDDED STRUCT. chainStatus embeds Chain (web3.go) to say "this chain, plus
	// whether it is answering right now". zipdoc files a field's prose under the
	// type that DECLARES it, so the three lifted keys are Chain.id, Chain.name and
	// Chain.chainId — and the schema generator inlines the promotion, publishing
	// chainStatus.id / .name / .chainId, which no key matches. All three ARE
	// described on Chain and reach the document through the chains listing.
	//
	// Not worked around. Unrolling the embedding into three copies replaces one
	// true statement with two that can drift, and naming an intermediate type
	// changes the published shape rather than its prose.
	"chainStatus.chainId": true,
	"chainStatus.id":      true,
	"chainStatus.name":    true,
}

// mountApp mounts the chain surface on a fresh in-memory app, composed as the
// unified binary is. The registry is EMPTY on purpose: routes and published
// shapes are a function of Mount alone, never of which chains an operator
// declared.
func mountApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(envChains, "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published
// schema that carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "web3", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("web3 publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/web3 describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
