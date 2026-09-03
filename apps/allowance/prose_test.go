package allowance

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// ADDRESS and its SHAPE; the shape's FIELDS come from a different place — a doc
// comment on each one, which zipdoc lifts one at a time — so a fully typed read can
// still publish a document a caller has to guess at.
//
// It matters here because the whole answer is four numbers whose meaning is nowhere
// in their types. `limit` and `used` are COUNTS OF CALLS and never money — the two
// ceilings in this estate are a wallet and this, and reading one as the other is how
// a free tier gets billed. 0 for `limit` means UNBOUNDED rather than "nothing
// allowed", which is the opposite of what the number reads as. `used` counts only
// SERVED calls, so a refusal and an outage leave it alone. And `resets` is unix
// SECONDS at the next UTC midnight, not a duration and not milliseconds.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// mount brings the surface up on one app, as the plugin's composition root does.
func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("allowance.Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	return app
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mount(t), openapi.Info{Title: "allowance", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("allowance publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/allowance describe",
			len(bare), strings.Join(bare, ", "))
	}
}
