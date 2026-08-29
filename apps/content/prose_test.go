package content

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// mountAlone mounts ONLY this subsystem, which is what the published document is:
// plugin/content links content and nothing else, so its openapi.json carries these
// schemas and no others. mountContent brings the framework up alongside, because the
// behaviour tests drive real documents through it — a fine app to make requests
// against and the wrong one to read a subset off, since it would fold another app's
// shapes into this app's gate.
func mountAlone(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level gate
// cannot see. Typing a route documents its ADDRESS and its SHAPE; the shape's FIELDS
// come from a different place — a doc comment on each one, which zipdoc lifts one at
// a time.
//
// It matters here because `status` is THREE closed vocabularies wearing one name, and
// they answer different questions. The lifecycle one (draft, in_review, approved,
// queued, published, archived) decides what a reader may see: the public site pulls
// exactly `published`, so that value is a visibility fact and not a workflow label. A
// publish's is distributed|scheduled|failed|in_progress|not_configured — where
// `in_progress` means nothing was posted and `failed` means nothing is on record at
// all. A storefront's is a third, narrower set. Beside them `externalIds` is not a
// report but the idempotency ledger every later publish of the item skips against,
// and an absent `distribution` or `storefront` means the side effect was never
// attempted rather than that it failed quietly.
//
// Presence is all a gate can check. A description restating the field's name is worse
// than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountAlone(t), openapi.Info{Title: "content", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("content publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/content describe",
			len(bare), strings.Join(bare, ", "))
	}
}
