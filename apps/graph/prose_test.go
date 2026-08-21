package graph

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

func mountGraph(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// This surface carries three timestamps on one assertion and they do different
// jobs: `at` is when the thing was so, `seen` is when the filer says it became
// knowable, and only `knowable` — the later of `seen` and the server's clock,
// derived and never accepted from a caller — bounds an as-of read. A caller told
// only their types cannot tell which one an `as_of` question is answered against,
// nor that `id` and `by` are minted server-side while everything beside them is
// the caller's. `direction` is a closed vocabulary of out, in and both whose
// members are meaningless unqualified, and `contested` is not "there were
// conflicts": conflicts that all repeat the winner's value leave it false.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountGraph(t), openapi.Info{Title: "graph", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("graph publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/graph describe",
			len(bare), strings.Join(bare, ", "))
	}
}
