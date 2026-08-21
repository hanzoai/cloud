package o11y

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of this package's published properties that carry NO
// description, and every one is a member of an ANONYMOUS struct.
//
// zipdoc files a field's prose under the type that DECLARES it, and an inline struct
// literal has no name to file under — `availabilityResponse.Range` is a `struct{...}`
// written in place, so `sinceSec` and `stepSec` have nowhere to be keyed. That two of
// them carry perfectly good doc comments in the source (availability.go) is the proof
// that this is a lookup gap rather than unwritten prose. The OUTER field is described in
// every case, so a reader is told what `range`, `series`, `usage` and `summary` are.
//
// Naming the types would fix the lookup and change the published document — an inline
// object becomes a $ref to a new component in every generated SDK — which is a shape
// change, not a description, and therefore a decision someone makes on purpose rather
// than a side effect of writing prose.
//
// Exact in BOTH directions: a bare property anywhere else in this package goes red, and
// an entry here that starts publishing prose goes red too.
var proseless = map[string]bool{
	"o11y.availabilityResponse.range.sinceSec": true,
	"o11y.availabilityResponse.range.stepSec":  true,
	"o11y.metricsResponse.range.sinceSec":      true,
	"o11y.metricsResponse.range.stepSec":       true,
	"o11y.metricsResponse.series.errors":       true,
	"o11y.metricsResponse.series.latencyP50Ms": true,
	"o11y.metricsResponse.series.latencyP95Ms": true,
	"o11y.metricsResponse.series.requests":     true,
	"o11y.metricsResponse.summary.errorRate":   true,
	"o11y.metricsResponse.summary.errors":      true,
	"o11y.metricsResponse.summary.p95Ms":       true,
	"o11y.metricsResponse.summary.requests":    true,
	"o11y.metricsResponse.usage.calls":         true,
	"o11y.metricsResponse.usage.costCents":     true,
	"o11y.metricsResponse.usage.series":        true,
	"o11y.metricsResponse.usage.tokens":        true,
}

// prose_test.go gates the FIELD half of the surface THIS PACKAGE writes.
// typed_wire_test.go proves every route is typed or named and every typed op is
// described; neither says whether the shapes those ops carry mean anything to a reader.
//
// The subject is narrower than the published subset, and the reason is the mount. This
// app is deliberately ONE product with ONE origin (Mount's own note): cloud's native
// reads and the o11y module's relay table are two halves of one route table, so zip
// qualifies every type either half reaches as `o11y.<Type>` and the 777 published
// components are indistinguishable by name, path or x-app. 769 of them are the
// module's, declared in another repository, and their prose is that repository's to
// write — a ledger of them here would be an exemption nobody could ever retire.
//
// So the gate quantifies over the components whose Go type is DECLARED IN THIS PACKAGE,
// read from this package's own source at test time. That set cannot go stale: add a
// published type here and it is covered without anyone remembering to list it.
//
// The facts at stake on the part we own are all about what a MISSING measurement means.
// A `deployment` absent from the inventory is one the prober is not reporting rather
// than one that is down. `latencyMs` is 0 when nothing answered, not because the service
// was fast. `source` says which of two signals decided `up`. Both maintenance lists are
// permanently empty because there is no scheduling plane — a true statement, not a
// placeholder. And `range` is the window actually used after clamping, which is why a
// caller reads it back instead of trusting what it asked for.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(surfaceApp(t), openapi.Info{Title: "o11y", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("o11y publishes no schemas at all — the gate would pass vacuously")
	}

	ours := map[string]any{}
	declared := typesDeclaredHere(t)
	for name, schema := range doc.Components.Schemas {
		if declared[name[strings.LastIndex(name, ".")+1:]] {
			ours[name] = schema
		}
	}
	if len(ours) == 0 {
		t.Fatal("none of the published components is declared in this package — " +
			"either the mount changed or the source walk did")
	}

	published, err := openapi.Bare(&openapi.Document{Components: &openapi.Components{Schemas: ours}})
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
			"the first of them alone — then run: make -C apps/o11y describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}

// typesDeclaredHere is the names of the struct types this package declares, read from
// the package's own source. Reading the source is what makes the gate's subject follow
// the code instead of a list someone has to remember to update.
func typesDeclaredHere(t *testing.T) map[string]bool {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse this package: %v", err)
	}
	declared := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, isStruct := ts.Type.(*ast.StructType); isStruct {
						declared[ts.Name.Name] = true
					}
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("the source walk found no struct types at all — it is not reading this package")
	}
	return declared
}
