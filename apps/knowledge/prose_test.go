package knowledge

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time. Retrieval is where that costs the most here: a
// hit's `score` is a cosine similarity with no absolute cutoff, so it orders one
// answer and means nothing across two, and `doctype` is the closed set
// kb-page/kb-memory/kb-source that `doctypes` filters on. The catalog's
// `configured` is about the DEPLOYMENT's OAuth credentials, not about whether
// the caller's org has connected anything, and a graph edge's `from`/`to` are
// always node ids — a wikilink matching no page points at a synthetic
// "unresolved:" node instead of dangling.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	// Mounted exactly as plugin/knowledge mounts it — this subsystem alone. The
	// other test helper adds framework, whose CRUD surface this app does not
	// publish and whose fields are another package's to write.
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{Domain: "api.test"}); err != nil {
		t.Fatalf("knowledge.Use:  %v", err)
	}

	doc, err := openapi.Spec(app, openapi.Info{Title: "knowledge", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("knowledge publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/knowledge describe",
			len(bare), strings.Join(bare, ", "))
	}
}
