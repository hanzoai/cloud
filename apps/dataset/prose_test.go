package dataset

// prose_test.go gates the FIELD half of this plane. wire_test.go proves every route
// is a typed op, which documents its ADDRESS and its SHAPE; the shape's FIELDS come
// from a different place — a doc comment on each one, which zipdoc lifts one at a
// time — so a fully typed plane can still publish a wholly unreadable document.
//
// It matters here because this plane's numbers are claims about a training set, and
// several of them read as their opposite when bare. `to` is EXCLUSIVE, and the
// lineage's `to` is not the spec's — it is the spec's end pulled back by the maturity
// horizon, so a caller reproducing a version by re-asking the spec's window gets
// different rows. `subjects` and not `rows` is the real sample size, because every row
// of one subject lands in one split. `train`/`val`/`test` are temporal slices in that
// order, not a random partition. And `judged` at 0 means nothing has been labelled,
// which a reader who takes it as "no positives" turns into a tenant with no fraud.
//
// The gate checks presence, not meaning. A description that restates the field's name
// is worse than none, and only a reader catches that.

import (
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description. There is no ledger: every shape on this plane arrives
// through a typed op, so zipdoc can reach all of it.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("datasettest"), DisableStartupMessage: true})
	compose(app)
	if err := mount(newPlane(&fake{}), app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	doc, err := openapi.Spec(app, openapi.Info{Title: "dataset", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("dataset publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/dataset describe",
			len(bare), strings.Join(bare, ", "))
	}
}
