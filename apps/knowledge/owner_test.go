package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
)

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
