package productcategory

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

func nsDB(parent context.Context, ns string) *datastore.Datastore {
	return datastore.New(nscontext.WithNamespace(parent, ns))
}

func TestCreateGetByIdRoundTrip(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(context.Background(), "acme")

	root := New(db)
	root.Name = "Apparel"
	root.Handle = "apparel"
	if err := root.Create(); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !root.IsRoot() {
		t.Fatal("root category IsRoot() = false")
	}

	got := New(db)
	if err := got.GetById(root.Id()); err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.Name != "Apparel" || got.Handle != "apparel" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !got.IsActive {
		t.Fatal("IsActive default should be true")
	}
}

func TestHierarchy_ChildrenQuery(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := nsDB(context.Background(), "acme")

	parent := New(db)
	parent.Name = "Shoes"
	parent.Handle = "shoes"
	if err := parent.Create(); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	for _, name := range []string{"Sneakers", "Boots"} {
		child := New(db)
		child.Name = name
		child.Handle = name
		child.ParentId = parent.Id()
		if err := child.Create(); err != nil {
			t.Fatalf("create child %s: %v", name, err)
		}
		if child.IsRoot() {
			t.Fatalf("child %s IsRoot() = true", name)
		}
	}

	children := make([]*ProductCategory, 0, 4)
	if _, err := Query(db).Filter("ParentId=", parent.Id()).GetAll(&children); err != nil {
		t.Fatalf("query children: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %d, want 2", len(children))
	}
}
