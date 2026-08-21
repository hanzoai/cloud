package projects

import (
	"context"
	"database/sql"
	"testing"
)

// starStore builds a Store over a PLAIN sqlite database holding only the stars
// schema.
//
// The package's own `newTestStore` opens the encrypted store, which fails closed
// on any host with no tmpfs — every test in this package that uses it is
// unrunnable on macOS, before and after this change. These four assert pure SQL
// (org isolation, per-user rows, idempotence), so they do not need the envelope
// to be honest about it, and skipping them on a developer machine would be a
// choice to test starring nowhere.
func starStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(starsDDL); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	return &Store{db: db}
}

// A star is the one PER-PERSON fact in a per-org store, and the two properties
// below are what make it safe to be one: two people disagree freely, and a star
// never crosses a tenant.

func TestStarIsPerPerson(t *testing.T) {
	ctx := context.Background()
	s := starStore(t)
	p := mkProject("acme", "site", "Site")

	if err := s.Star(ctx, "acme", "ann", p.ID); err != nil {
		t.Fatalf("star: %v", err)
	}

	ann, err := s.StarredBy(ctx, "acme", "ann")
	if err != nil {
		t.Fatalf("starredBy ann: %v", err)
	}
	if !ann[p.ID] {
		t.Fatal("ann starred it and does not see it starred")
	}

	// Bo is in the SAME org and sees the SAME project — and must not inherit
	// Ann's star. This is the whole reason stars are not a column on the row.
	bo, err := s.StarredBy(ctx, "acme", "bo")
	if err != nil {
		t.Fatalf("starredBy bo: %v", err)
	}
	if bo[p.ID] {
		t.Fatal("bo inherited ann's star — a star is not a property of the project")
	}
}

func TestStarNeverCrossesAnOrg(t *testing.T) {
	ctx := context.Background()
	s := starStore(t)
	mine := mkProject("acme", "site", "Site")

	if err := s.Star(ctx, "acme", "ann", mine.ID); err != nil {
		t.Fatalf("star: %v", err)
	}

	// The SAME user id in another tenant. Org is in the primary key precisely so
	// this cannot come back true: a shared user id must not carry state across
	// the boundary every other row in this store respects.
	across, err := s.StarredBy(ctx, "other", "ann")
	if err != nil {
		t.Fatalf("starredBy: %v", err)
	}
	if len(across) != 0 {
		t.Fatalf("a star leaked across orgs: %v", across)
	}
}

// Starring is a STATE, so saying it twice means what saying it once meant, and
// un-saying something never said is the state the caller asked for. Both halves
// matter for a double-clicked button and a retried request.
func TestStarIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := starStore(t)
	p := mkProject("acme", "site", "Site")

	for i := 0; i < 3; i++ {
		if err := s.Star(ctx, "acme", "ann", p.ID); err != nil {
			t.Fatalf("star %d: %v", i, err)
		}
	}
	got, _ := s.StarredBy(ctx, "acme", "ann")
	if len(got) != 1 {
		t.Fatalf("starring three times left %d stars, want 1", len(got))
	}

	for i := 0; i < 2; i++ {
		if err := s.Unstar(ctx, "acme", "ann", p.ID); err != nil {
			t.Fatalf("unstar %d: %v", i, err)
		}
	}
	got, _ = s.StarredBy(ctx, "acme", "ann")
	if len(got) != 0 {
		t.Fatalf("unstarring left %d stars, want 0", len(got))
	}
}

// No validated user means nobody to answer for. An empty set is the honest
// answer — the caller is about to render an unstarred list — and it must not be
// an error, because the list itself is still perfectly serveable.
func TestStarredByWithoutAUserIsEmptyNotAnError(t *testing.T) {
	ctx := context.Background()
	s := starStore(t)
	for _, user := range []string{"", " "} {
		got, err := s.StarredBy(ctx, "acme", user)
		if err != nil {
			t.Fatalf("user %q: %v", user, err)
		}
		if len(got) != 0 {
			t.Fatalf("user %q got %d stars", user, len(got))
		}
	}
	got, err := s.StarredBy(ctx, "", "ann")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty org: %v %v", got, err)
	}
}
