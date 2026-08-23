package graph

import (
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"

	// cek reads no environment, so a test binary keys ITSELF: this mints a
	// deterministic master through the same encrypted path production uses.
	// Without it the per-org store refuses to open and every request through the
	// harness is a 500 about a missing key.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

func mountGraph(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	// The composer owes this. Serve installs cloud.Bridge binary-wide and a
	// plugin program's constructor does the same, so a bare test app that skips
	// it parks no validated principal and every org-scoped op answers 403 for a
	// reason production cannot produce. It went unnoticed here because the only
	// test that used this harness read the SPEC and made no request.
	app.Use(cloud.Bridge())
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
	var undescribed []string
	for _, b := range bare {
		if !proseless[b] {
			undescribed = append(undescribed, b)
		}
	}
	if len(undescribed) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/graph describe",
			len(undescribed), strings.Join(undescribed, ", "))
	}
	// The ledger may only SHRINK. An entry that starts publishing prose is one
	// zip learned to lift, and leaving it named here would outlive the gap.
	for name := range proseless {
		if !slices.Contains(bare, name) {
			t.Errorf("%s publishes prose now — delete it from proseless, in this commit", name)
		}
	}
}

// proseless is the REFLECTION CLIENT, and it is the whole cost of the one route
// here that cannot be a typed op.
//
// POST /v1/graph/graphql answers a shape the CALLER chose — its selection set —
// so it declares one In and no single Out and cannot be typed. Its bodies reach
// the document through openapi.Register (graphql.go's init) instead, and Register
// derives a schema by REFLECTION: Go drops comments at compile time, and zipdoc,
// which lifts field prose, walks zip's TYPED registrations and can never reach a
// type that arrives this way.
//
// Every one of these seven CARRIES a doc comment in graphql.go. Reflection cannot
// see it. So they are named here rather than answered with a schema hand-written
// beside the struct, which is the drift Register exists to prevent — and the
// operation's own prose is declared where it can be (openapi.Describe, same init).
//
// The day zip lifts comments for Register, this empties instead of outliving it.
var proseless = map[string]bool{
	"graphQLIn.query":         true,
	"graphQLIn.variables":     true,
	"graphQLIn.operationName": true,
	"graphQLOut.data":         true,
	"graphQLOut.errors":       true,
	"graphQLError.message":    true,
	"graphQLError.path":       true,
}
