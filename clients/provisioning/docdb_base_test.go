package provisioning

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tests"
)

// TestDocdbProvisioner_CreateYieldsBaseCollection proves the re-based docdb
// product provisions a real Hanzo Base document collection (SQLite + realtime),
// with ZERO MongoDB: Create builds a base-type Collection with a json `data`
// field and authenticated access rules, returns the /v1/base collection-records
// endpoint, is idempotent (409 on repeat), and Drop removes it.
func TestDocdbProvisioner_CreateYieldsBaseCollection(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("new base test app: %v", err)
	}
	defer app.Cleanup()

	p := &docdbProvisioner{
		publicURL: "https://api.hanzo.ai",
		host:      "api.hanzo.ai",
		port:      443,
		appFn:     func() core.App { return app },
	}
	const physical = "otesthash_mydb"
	ctx := context.Background()

	cs, host, port, db, err := p.Create(ctx, physical, "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if db != physical {
		t.Errorf("db = %q, want %q", db, physical)
	}
	if host != "api.hanzo.ai" || port != 443 {
		t.Errorf("endpoint = %s:%d, want api.hanzo.ai:443", host, port)
	}
	if want := "https://api.hanzo.ai/v1/base/collections/" + physical + "/records"; cs != want {
		t.Errorf("connString = %q, want %q", cs, want)
	}

	// The Base collection must exist: base type, a json `data` document field,
	// and non-nil access rules (authenticated CRUD + realtime).
	col, err := app.FindCollectionByNameOrId(physical)
	if err != nil || col == nil {
		t.Fatalf("collection %q not created: %v", physical, err)
	}
	if col.Type != core.CollectionTypeBase {
		t.Errorf("collection type = %q, want %q", col.Type, core.CollectionTypeBase)
	}
	if f := col.Fields.GetByName("data"); f == nil {
		t.Error("collection missing `data` document field")
	} else if f.Type() != core.FieldTypeJSON {
		t.Errorf("`data` field type = %q, want %q", f.Type(), core.FieldTypeJSON)
	}
	if col.ListRule == nil || col.CreateRule == nil {
		t.Error("collection rules nil — expected authenticated CRUD rules")
	}

	// Idempotent: a second Create of the same physical name reports conflict.
	if _, _, _, _, err := p.Create(ctx, physical, "", ""); !errors.Is(err, errAlreadyExists) {
		t.Errorf("second create err = %v, want errAlreadyExists", err)
	}

	// Drop removes the collection; a second Drop is a no-op (idempotent).
	if err := p.Drop(ctx, physical, ""); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if col, _ := app.FindCollectionByNameOrId(physical); col != nil {
		t.Error("collection still present after drop")
	}
	if err := p.Drop(ctx, physical, ""); err != nil {
		t.Errorf("second drop should be a no-op, got: %v", err)
	}
}
