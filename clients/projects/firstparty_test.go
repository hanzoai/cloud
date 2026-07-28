package projects

import (
	"context"
	"testing"
)

// The declaration itself must be well-formed, or the badge is applied to nothing
// (or, with an empty org, to everything).
func TestFirstPartyManifestIsValid(t *testing.T) {
	f, err := loadFirstParty()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Org != "hanzo" {
		t.Fatalf("platform org want hanzo, got %q", f.Org)
	}
	seen := map[string]bool{}
	for _, s := range f.Slugs {
		if seen[s] {
			t.Fatalf("duplicate slug %q — a set, not a list", s)
		}
		seen[s] = true
	}
	if len(f.Slugs) < 74 {
		t.Fatalf("want the whole example catalogue (>=74), got %d", len(f.Slugs))
	}
}

// The point of the whole file: a project the platform declares as its own comes
// back official WITHOUT any caller asking for it — and one it does not declare
// stays exactly as it was, in BOTH directions (an unofficial row is untouched,
// and a badge a real SuperAdmin set by hand is never revoked).
func TestBackfillOfficialRaisesOnlyDeclaredRows(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	f, err := loadFirstParty()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	declared := f.Slugs[0]

	must := func(p Project) {
		t.Helper()
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatalf("create %s/%s: %v", p.Org, p.Slug, err)
		}
	}
	must(Project{ID: "p1", Org: f.Org, Slug: declared, Name: "declared"})
	must(Project{ID: "p2", Org: f.Org, Slug: "not-in-the-manifest", Name: "undeclared"})
	must(Project{ID: "p3", Org: "acme", Slug: declared, Name: "same slug, other org"})
	must(Project{ID: "p4", Org: "acme", Slug: "hand-badged", Name: "superadmin set this", Official: true})

	n, err := backfillOfficial(ctx, st.db)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if n != 1 {
		t.Fatalf("want exactly 1 badge raised, got %d", n)
	}
	for _, tc := range []struct {
		org, slug string
		want      bool
	}{
		{f.Org, declared, true},               // declared, in the platform org
		{f.Org, "not-in-the-manifest", false}, // platform org is NOT enough on its own
		{"acme", declared, false},             // a tenant cannot inherit the badge by slug
		{"acme", "hand-badged", true},         // raise-only: never revokes
	} {
		p, err := st.GetProject(ctx, tc.org, tc.slug)
		if err != nil {
			t.Fatalf("get %s/%s: %v", tc.org, tc.slug, err)
		}
		if p.Official != tc.want {
			t.Fatalf("%s/%s official = %v, want %v", tc.org, tc.slug, p.Official, tc.want)
		}
	}

	// Converged: a second run changes nothing, so this is a projection of the
	// declaration and not a rewrite on every boot.
	if n, err := backfillOfficial(ctx, st.db); err != nil || n != 0 {
		t.Fatalf("second run: n=%d err=%v — want 0, nil", n, err)
	}
}
