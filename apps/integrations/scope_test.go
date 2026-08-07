package integrations

// Scope is who owns a connection, and it is part of the key rather than a column
// beside it: user="" is the org's, anything else is that person's. These pin the
// consequences, because the old shape could not express any of them — a provider
// declared its plane, so Google was org-only forever.

import (
	"context"
	"testing"
)

// The ask, stated as a test: one provider connected both ways at once.
func TestGoogleForTheOrgAndForMeAreDifferentConnections(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Upsert(ctx, Connection{Org: "hanzo", Provider: "google", ExternalID: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, Connection{Org: "hanzo", User: "z", Provider: "google", ExternalID: "mine"}); err != nil {
		t.Fatal(err)
	}

	org, ok, err := s.Get(ctx, "hanzo", "", "google", "")
	if err != nil || !ok || org.ExternalID != "shared" {
		t.Fatalf("the org's google = %+v ok=%v err=%v", org, ok, err)
	}
	mine, ok, err := s.Get(ctx, "hanzo", "z", "google", "")
	if err != nil || !ok || mine.ExternalID != "mine" {
		t.Fatalf("my google = %+v ok=%v err=%v", mine, ok, err)
	}
	if all, err := s.ListFor(ctx, "hanzo", "google"); err != nil || len(all) != 2 {
		t.Fatalf("google is connected twice, so the provider holds 2 rows, got %d (%v)", len(all), err)
	}
}

// The isolation that makes a per-user connection worth having.
func TestMyConnectionIsNotMyColleagues(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Upsert(ctx, Connection{Org: "hanzo", User: "z", Provider: "google", ExternalID: "z"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(ctx, "hanzo", "kai", "google", ""); err != nil || ok {
		t.Fatalf("kai resolved z's connection: ok=%v err=%v", ok, err)
	}
	list, err := s.List(ctx, "hanzo", "kai")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.User == "z" {
			t.Fatalf("kai's list carries z's connection: %+v", c)
		}
	}
}

// What List means: everything this principal may USE — the org's, plus their own,
// and nobody else's.
func TestListIsWhatThisPrincipalMayUse(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	for _, c := range []Connection{
		{Org: "hanzo", Provider: "slack"},               // the org's
		{Org: "hanzo", User: "z", Provider: "google"},   // mine
		{Org: "hanzo", User: "kai", Provider: "linear"}, // a colleague's
	} {
		if err := s.Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	list, err := s.List(ctx, "hanzo", "z")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range list {
		seen[c.Provider] = true
	}
	if !seen["slack"] || !seen["google"] {
		t.Errorf("z should see the org's slack and their own google, got %v", seen)
	}
	if seen["linear"] {
		t.Error("z sees kai's linear, which belongs to kai alone")
	}
}

// Disconnecting takes every account of one provider at that scope, and leaves the
// other scope alone — the org disconnecting Google must not sign me out of mine.
func TestDisconnectTakesOneScopeNotBoth(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Upsert(ctx, Connection{Org: "hanzo", Provider: "google"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, Connection{Org: "hanzo", User: "z", Provider: "google"}); err != nil {
		t.Fatal(err)
	}
	if gone, err := s.Disconnect(ctx, "hanzo", "", "google"); err != nil || !gone {
		t.Fatalf("disconnect the org's google: gone=%v err=%v", gone, err)
	}
	if _, ok, err := s.Get(ctx, "hanzo", "z", "google", ""); err != nil || !ok {
		t.Fatalf("my google went with the org's: ok=%v err=%v", ok, err)
	}
}
