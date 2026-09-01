package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
)

// TestOwnerFilter: the org itself reaches only unowned documents; a person
// reaches those and their own, expressed as the store's own filter forms.
func TestOwnerFilter(t *testing.T) {
	must := []map[string]any{{"key": "org", "match": map[string]any{"value": "acme"}}}
	org, _ := json.Marshal(ownerFilter(must, ""))
	if string(org) != `{"must":[{"key":"org","match":{"value":"acme"}},{"is_empty":{"key":"owner"}}]}` {
		t.Fatalf("org filter: %s", org)
	}
	me, _ := json.Marshal(ownerFilter(must, "u-7"))
	if string(me) != `{"must":[{"key":"org","match":{"value":"acme"}}],"should":[{"is_empty":{"key":"owner"}},{"key":"owner","match":{"value":"u-7"}}]}` {
		t.Fatalf("person filter: %s", me)
	}
	if len(must) != 1 {
		t.Fatal("the caller's must clauses were written through")
	}
}

// TestOwnerOnSave: with nobody behind the call, a document may be the org's and
// nothing else; once owned, the owner does not move.
func TestOwnerOnSave(t *testing.T) {
	ctx := context.Background()
	doc := func(owner string) *framework.Document {
		return &framework.Document{Data: map[string]any{"title": "t", "owner": owner}}
	}
	if err := ownerOnSave(ctx, &framework.Event{Doc: doc("")}); err != nil {
		t.Fatalf("the org's own document: %v", err)
	}
	if err := ownerOnSave(ctx, &framework.Event{Doc: doc("u-7")}); !errors.Is(err, errOwnerNotYou) {
		t.Fatalf("claiming for a person with nobody behind the call: %v", err)
	}
	if err := ownerOnSave(ctx, &framework.Event{Doc: doc("u-7"), Prev: doc("u-7")}); err != nil {
		t.Fatalf("an owner kept on update: %v", err)
	}
	if err := ownerOnSave(ctx, &framework.Event{Doc: doc(""), Prev: doc("u-7")}); !errors.Is(err, errOwnerFixed) {
		t.Fatalf("an owner dropped on update: %v", err)
	}
	if err := ownerOnSave(ctx, &framework.Event{Doc: doc("u-8"), Prev: doc("u-7")}); !errors.Is(err, errOwnerFixed) {
		t.Fatalf("an owner moved on update: %v", err)
	}
}
