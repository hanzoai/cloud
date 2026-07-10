package cloud

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// createT + insert/count helpers exercise a resolved *sql.DB as a real org
// file so the isolation proofs are behavioral, not just path-string checks.
func createMarkerTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE marks (v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

func insertMarker(t *testing.T, db *sql.DB, v string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO marks (v) VALUES (?)`, v); err != nil {
		t.Fatalf("insert %q: %v", v, err)
	}
}

func countMarker(t *testing.T, db *sql.DB, v string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM marks WHERE v=?`, v).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", v, err)
	}
	return n
}

// TestTenantDBPathConvention pins the two-shape path convention and the
// fail-closed behavior on an invalid org/project — the whole contract of the
// resolver, asserted without touching the filesystem.
func TestTenantDBPathConvention(t *testing.T) {
	dir := "/data"

	// org-scoped: {DataDir}/orgs/{org}/{subsystem}.db
	got, err := orgDBPath(dir, "acme", "", "git")
	if err != nil {
		t.Fatalf("org-scoped path: %v", err)
	}
	if want := filepath.Join(dir, "orgs", "acme", "git.db"); got != want {
		t.Fatalf("org-scoped path = %q, want %q", got, want)
	}

	// project-scoped nests under projects/{project}
	got, err = orgDBPath(dir, "acme", "web", "tracker")
	if err != nil {
		t.Fatalf("project-scoped path: %v", err)
	}
	if want := filepath.Join(dir, "orgs", "acme", "projects", "web", "tracker.db"); got != want {
		t.Fatalf("project-scoped path = %q, want %q", got, want)
	}

	// the default project is a real, nested segment (not folded into org scope)
	got, _ = orgDBPath(dir, "acme", "default", "tracker")
	if want := filepath.Join(dir, "orgs", "acme", "projects", "default", "tracker.db"); got != want {
		t.Fatalf("default-project path = %q, want %q", got, want)
	}

	// fail-closed: empty/unsafe org, unsafe project, empty subsystem all error —
	// NEVER a silent fall-through to some other org's file.
	if _, err := orgDBPath(dir, "", "", "git"); err == nil {
		t.Fatal("empty org must error")
	}
	if _, err := orgDBPath(dir, "bad org", "", "git"); err == nil {
		t.Fatal("org with a space (unsafe rune) must error")
	}
	if _, err := orgDBPath(dir, "acme", "bad project", "tracker"); err == nil {
		t.Fatal("project with a space (unsafe rune) must error")
	}
	if _, err := orgDBPath(dir, "acme", "", ""); err == nil {
		t.Fatal("empty subsystem must error")
	}
}

// TestTenantDBOrgIsolation proves two different orgs resolve to two different
// files with NO cross-read — the physical org boundary.
func TestTenantDBOrgIsolation(t *testing.T) {
	dir := t.TempDir()

	a, err := OrgDB(dir, "orga", "", "widget")
	if err != nil {
		t.Fatalf("open orgA: %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := OrgDB(dir, "orgb", "", "widget")
	if err != nil {
		t.Fatalf("open orgB: %v", err)
	}
	defer func() { _ = b.Close() }()

	createMarkerTable(t, a)
	createMarkerTable(t, b)
	insertMarker(t, a, "secretA")

	if n := countMarker(t, b, "secretA"); n != 0 {
		t.Fatalf("orgB saw orgA's row (cross-org leak): count=%d", n)
	}
	if n := countMarker(t, a, "secretA"); n != 1 {
		t.Fatalf("orgA cannot see its own row: count=%d", n)
	}

	// Two physically distinct files exist (orga/, orgb/ are DNS-label identities).
	fa := filepath.Join(dir, "orgs", "orga", "widget.db")
	fb := filepath.Join(dir, "orgs", "orgb", "widget.db")
	if _, err := os.Stat(fa); err != nil {
		t.Fatalf("orgA file missing at %s: %v", fa, err)
	}
	if _, err := os.Stat(fb); err != nil {
		t.Fatalf("orgB file missing at %s: %v", fb, err)
	}
	if fa == fb {
		t.Fatal("orgA and orgB resolved to the SAME file")
	}
}

// TestTenantDBProjectIsolation proves two projects under ONE org resolve to two
// nested files with no cross-read — the project-scoped boundary.
func TestTenantDBProjectIsolation(t *testing.T) {
	dir := t.TempDir()

	alpha, err := OrgDB(dir, "acme", "alpha", "tracker")
	if err != nil {
		t.Fatalf("open alpha: %v", err)
	}
	defer func() { _ = alpha.Close() }()
	beta, err := OrgDB(dir, "acme", "beta", "tracker")
	if err != nil {
		t.Fatalf("open beta: %v", err)
	}
	defer func() { _ = beta.Close() }()

	createMarkerTable(t, alpha)
	createMarkerTable(t, beta)
	insertMarker(t, alpha, "issue-1")

	if n := countMarker(t, beta, "issue-1"); n != 0 {
		t.Fatalf("project beta saw project alpha's row (cross-project leak): count=%d", n)
	}

	fAlpha := filepath.Join(dir, "orgs", "acme", "projects", "alpha", "tracker.db")
	fBeta := filepath.Join(dir, "orgs", "acme", "projects", "beta", "tracker.db")
	for _, f := range []string{fAlpha, fBeta} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("expected nested project file at %s: %v", f, err)
		}
	}
	if fAlpha == fBeta {
		t.Fatal("two projects resolved to the SAME file")
	}
}

// TestTenantStoreCachesAndIsolates proves the shared cache opens each org
// file exactly once and keys distinct (org, project) scopes to distinct handles.
func TestTenantStoreCachesAndIsolates(t *testing.T) {
	dir := t.TempDir()
	opened := 0
	cache := NewOrgStore(dir, "widget", func(db *sql.DB) (*sql.DB, error) {
		opened++
		return db, nil
	})
	t.Cleanup(func() { _ = cache.CloseAll() })

	a1, err := cache.For("orga", "")
	if err != nil {
		t.Fatalf("For orgA: %v", err)
	}
	a2, err := cache.For("orga", "")
	if err != nil {
		t.Fatalf("For orgA (2): %v", err)
	}
	if a1 != a2 {
		t.Fatal("same org must return the SAME cached handle")
	}
	if opened != 1 {
		t.Fatalf("open called %d times for one org, want 1", opened)
	}

	b, err := cache.For("orgb", "")
	if err != nil {
		t.Fatalf("For orgB: %v", err)
	}
	if b == a1 {
		t.Fatal("distinct orgs must get distinct handles")
	}

	// A project scope under the same org is a DISTINCT file/handle.
	pa, err := cache.For("orga", "alpha")
	if err != nil {
		t.Fatalf("For orgA/alpha: %v", err)
	}
	if pa == a1 {
		t.Fatal("project scope must resolve to a distinct handle from org scope")
	}
	if opened != 3 {
		t.Fatalf("open called %d times for 3 distinct orgs, want 3", opened)
	}
}

// TestSanitizeOrgInjectiveAndSafe locks the properties OrgDB relies on: a
// clean DNS label is the identity, case-only siblings do NOT fold onto one slug
// (a case-insensitive-filesystem cross-org break), and unsafe-rune orgs are
// refused.
func TestSanitizeOrgInjectiveAndSafe(t *testing.T) {
	if got := SanitizeOrg("acme"); got != "acme" {
		t.Fatalf("clean DNS label should be identity, got %q", got)
	}
	if SanitizeOrg("") != "" {
		t.Fatal("empty org must be refused")
	}
	if SanitizeOrg("acme ") != "" { // trailing space is an unsafe (trimmable) rune
		t.Fatal("whitespace-bearing org must be refused")
	}
	// "Acme" and "acme" are DISTINCT owners and must NOT share a slug.
	if SanitizeOrg("Acme") == SanitizeOrg("acme") {
		t.Fatal("case-only siblings folded onto one slug (cross-org break)")
	}
	// Folded (non-identity) output carries the disambiguation suffix.
	if got := SanitizeOrg("Acme"); got == "acme" || got == "Acme" {
		t.Fatalf("non-identity owner must be re-suffixed, got %q", got)
	}
}
