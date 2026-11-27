package graph

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"

	// Without it the per-org store refuses to open and every request through the
	// cek reads no environment, so a test binary keys ITSELF: this mints a
	// deterministic master through the same encrypted path production uses.
	// harness is a 500 about a missing key.
	"github.com/hanzoai/cloud/internal/sqltest"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

func mountGraph(t *testing.T) *zip.App {
	t.Helper()
	return mount(t, cloud.Deps{})
}

// mount is mountGraph with the deps a test needs to vary — the audit trail an
// erasure is written to, the model plane an answer completes through — which is
// the same seam production fills.
func mount(t *testing.T, deps cloud.Deps) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	// The composer owes this. Serve installs cloud.Bridge binary-wide and a
	// plugin program's constructor does the same, so a bare test app that skips
	// it parks no validated principal and every org-scoped op answers 401 for a
	// reason production cannot produce. It went unnoticed here because the only
	// test that used this harness read the SPEC and made no request.
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, deps); err != nil {
		t.Fatalf("Use:  %v", err)
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		doc, err := spec.Of(mountGraph(t), spec.Info{Title: "graph", Version: "v1"})
		if err != nil {
			t.Fatalf("spec: %v", err)
		}
		if doc.Components == nil || len(doc.Components.Schemas) == 0 {
			t.Fatal("graph publishes no schemas at all — the gate would pass vacuously")
		}
		bare, err := spec.Bare(doc)
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
	})
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

// TestEveryOpSaysWhatAndWhen holds each typed op's prose to one shape: its first
// sentence is the summary, it says how the op reads as_of and as_known (even when
// it reads neither), and an op that takes input carries an example of it. The
// catalog, the MCP tools, the CLI help and every generated SDK read this prose
// and nothing else.
func TestEveryOpSaysWhatAndWhen(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		reg, err := spec.Typed(mountGraph(t))
		if err != nil {
			t.Fatalf("typed: %v", err)
		}
		if len(reg.Ops) == 0 {
			t.Fatal("graph registers no typed op — the gate would pass vacuously")
		}
		for key, op := range reg.Ops {
			prose := strings.Join(strings.Fields(op.Description), " ")
			if op.Summary == "" || !strings.HasPrefix(prose, op.Summary) {
				t.Errorf("%s: summary %q is not the description's first sentence", key, op.Summary)
			}
			if !strings.Contains(op.Description, "\n\nTime: ") ||
				!strings.Contains(op.Description, "as_of") || !strings.Contains(op.Description, "as_known") {
				t.Errorf("%s: description has no Time paragraph naming as_of and as_known", key)
			}
			body, _ := json.Marshal(op.RequestBody)
			example := strings.Contains(string(body), `"example"`)
			for _, p := range op.Parameters {
				example = example || len(p.Example) > 0
			}
			if (op.RequestBody != nil || len(op.Parameters) > 0) && !example {
				t.Errorf("%s: takes input and carries no example of it", key)
			}
		}
	})
}
