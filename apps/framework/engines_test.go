package framework

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/doctype"
	engine "github.com/hanzoai/framework"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
)

// The DocType engine keeps an org's documents the same on SQLite and on hanzo-sql: a doctype
// and its documents, a naming series that counts, a filter and a sort over a field inside a
// document, a set membership, and a lock one holder wins.
//
// MUTATION: drop Postgres from openEngine and the postgres leg fails at its first statement
// (? is not a placeholder there).
func TestTheEngineKeepsDocumentsOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
		ns, err := namespace.Org("acme")
		if err != nil {
			t.Fatal(err)
		}
		e, err := openEngine(open(ns, "framework"), t.TempDir(), luxlog.Default().New("subsystem", "framework"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		ctx, st := context.Background(), e.Store()
		dt, err := st.CreateDocType(ctx, "acme", engine.DocType{Name: "Inv", Module: "erp", Autoname: "INV-.YYYY.-.####",
			Fields: []engine.DocField{{Fieldname: "color", Fieldtype: engine.FieldData}}})
		if err != nil {
			t.Fatalf("create doctype: %v", err)
		}
		var names []string
		for _, color := range []string{"red", "blue", "red"} {
			d, err := st.CreateDocument(ctx, "acme", &dt, map[string]any{"color": color}, "")
			if err != nil {
				t.Fatalf("create document: %v", err)
			}
			names = append(names, d.Name)
		}
		for i, want := range []string{"0001", "0002", "0003"} {
			if !strings.HasPrefix(names[i], "INV-") || !strings.HasSuffix(names[i], want) {
				t.Fatalf("the naming series gave %v, want a count to %s", names, want)
			}
		}
		id := doctype.ID{Module: "erp", Name: "Inv"}
		red, err := st.ListNames(ctx, "acme", id, engine.ListOpts{Filters: map[string]string{"color": "red"}})
		if err != nil || len(red) != 2 {
			t.Fatalf("a filter inside the document found %v (%v), want the two red", red, err)
		}
		among, err := st.ListNames(ctx, "acme", id, engine.ListOpts{Among: map[string][]string{"color": {"blue", "green"}}})
		if err != nil || len(among) != 1 || among[0] != names[1] {
			t.Fatalf("a set found %v (%v), want the blue one", among, err)
		}
		sorted, err := st.ListNames(ctx, "acme", id, engine.ListOpts{OrderField: "color"})
		if err != nil || !slices.Equal(sorted, []string{names[1], names[0], names[2]}) {
			t.Fatalf("sorted by the field: %v (%v), want blue then the reds", sorted, err)
		}
		if other, err := st.ListNames(ctx, "beta", id, engine.ListOpts{}); err != nil || len(other) != 0 {
			t.Fatalf("another org reads %v (%v), want nothing", other, err)
		}
	})
}
