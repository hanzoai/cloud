package integrations

import (
	"context"
	"testing"
)

// TestConnected_OrgScopedBooleanNeverLeaksToken proves the observe/growth boolean
// (bound as guide's ConnectorPresent seam) is strictly org-scoped and fail-closed: it
// reports a connection ONLY for the org that owns it; a cross-tenant org, an unknown
// provider, and an invalid org all read false. It returns a boolean by construction —
// it never touches KMS and never surfaces the token.
func TestConnected_OrgScopedBooleanNeverLeaksToken(t *testing.T) {
	app := newApp(t, nil) // no KMS: Connected must never reach for a secret
	_ = app
	ctx := context.Background()

	// orgA connects github; orgB connects nothing.
	if err := mounted.State.store.Upsert(ctx, Connection{Org: "orgA", Provider: "github", ExternalID: "1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if !Connected(ctx, "orgA", "github") {
		t.Fatal("orgA connected github → Connected must be true")
	}
	if Connected(ctx, "orgB", "github") {
		t.Fatal("cross-tenant leak: orgB must NOT observe orgA's github connection")
	}
	if Connected(ctx, "orgA", "not-a-provider") {
		t.Fatal("an unknown provider must read false (fail-closed)")
	}
	if Connected(ctx, "", "github") {
		t.Fatal("an invalid org must read false (fail-closed)")
	}
}
