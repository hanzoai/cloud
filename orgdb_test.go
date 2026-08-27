package cloud

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/namespace"
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

	// path resolves a validated (org, project) the way every store does: through
	// the ONE namespace constructor, then the ONE rendering of it.
	path := func(org, project, subsystem string) (string, error) {
		ns, err := OrgNamespace(org, project)
		if err != nil {
			return "", err
		}
		return namespace.Path(dir, ns, subsystem)
	}

	// org-scoped: {DataDir}/orgs/{org}/{subsystem}.db
	got, err := path("acme", "", "git")
	if err != nil {
		t.Fatalf("org-scoped path: %v", err)
	}
	if want := filepath.Join(dir, "orgs", "acme", "git.db"); got != want {
		t.Fatalf("org-scoped path = %q, want %q", got, want)
	}

	// project-scoped nests under projects/{project}
	got, err = path("acme", "web", "kms")
	if err != nil {
		t.Fatalf("project-scoped path: %v", err)
	}
	if want := filepath.Join(dir, "orgs", "acme", "projects", "web", "kms.db"); got != want {
		t.Fatalf("project-scoped path = %q, want %q", got, want)
	}

	// the default project is a real, nested segment (not folded into org scope)
	got, _ = path("acme", "default", "kms")
	if want := filepath.Join(dir, "orgs", "acme", "projects", "default", "kms.db"); got != want {
		t.Fatalf("default-project path = %q, want %q", got, want)
	}

	// the deployment's own partition is a different KIND of namespace, so it
	// cannot collide with a tenant's however the slugger changes.
	got, err = namespace.Path(dir, namespace.System(), "kms")
	if err != nil {
		t.Fatalf("platform path: %v", err)
	}
	if want := filepath.Join(dir, "orgs", "_platform", "kms.db"); got != want {
		t.Fatalf("platform path = %q, want %q", got, want)
	}

	// fail-closed: empty/unsafe org, unsafe project, empty subsystem all error —
	// NEVER a silent fall-through to some other org's file.
	if _, err := path("", "", "git"); err == nil {
		t.Fatal("empty org must error")
	}
	if _, err := path("bad org", "", "git"); err == nil {
		t.Fatal("org with a space (unsafe rune) must error")
	}
	if _, err := path("acme", "bad project", "kms"); err == nil {
		t.Fatal("project with a space (unsafe rune) must error")
	}
	if _, err := path("acme", "", ""); err == nil {
		t.Fatal("empty subsystem must error")
	}
	// the zero namespace names no database — the mistake Go's zero value would
	// otherwise turn into one file quietly shared by everyone who forgot to set one.
	if _, err := namespace.Path(dir, namespace.Namespace{}, "git"); err == nil {
		t.Fatal("the zero namespace must error")
	}
}

// TestTenantDBOrgIsolation proves two different orgs resolve to two different
// files with NO cross-read — the physical org boundary.
func TestTenantDBOrgIsolation(t *testing.T) {
	dir := t.TempDir()

	a, err := OrgDB(dir, MustOrgNamespace("orga", ""), "widget")
	if err != nil {
		t.Fatalf("open orgA: %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := OrgDB(dir, MustOrgNamespace("orgb", ""), "widget")
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

	// Two physically distinct stores exist (orga/, orgb/ are DNS-label identities).
	// Closed first: on the pure-Go codec the database is written back at close, so a
	// still-open store has no file to stat yet.
	_ = a.Close()
	_ = b.Close()
	fa := filepath.Join(dir, "orgs", "orga", "widget.db")
	fb := filepath.Join(dir, "orgs", "orgb", "widget.db")
	for _, f := range []string{fa, fb} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("store missing at %s: %v", f, err)
		}
	}
	if fa == fb {
		t.Fatal("orgA and orgB resolved to the SAME file")
	}
}

// TestTenantDBProjectIsolation proves two projects under ONE org resolve to two
// nested files with no cross-read — the project-scoped boundary.
func TestTenantDBProjectIsolation(t *testing.T) {
	dir := t.TempDir()

	alpha, err := OrgDB(dir, MustOrgNamespace("acme", "alpha"), "kms")
	if err != nil {
		t.Fatalf("open alpha: %v", err)
	}
	defer func() { _ = alpha.Close() }()
	beta, err := OrgDB(dir, MustOrgNamespace("acme", "beta"), "kms")
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

	_ = alpha.Close()
	_ = beta.Close()
	fAlpha := filepath.Join(dir, "orgs", "acme", "projects", "alpha", "kms.db")
	fBeta := filepath.Join(dir, "orgs", "acme", "projects", "beta", "kms.db")
	for _, f := range []string{fAlpha, fBeta} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("expected a nested project store at %s: %v", f, err)
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
	cache := NewOrgStore(Base{DataDir: dir}, "widget", func(db *sql.DB) (*sql.DB, error) {
		opened++
		return db, nil
	})
	t.Cleanup(func() { _ = cache.CloseAll() })

	a1, err := cache.For(MustOrgNamespace("orga", ""))
	if err != nil {
		t.Fatalf("For orgA: %v", err)
	}
	a2, err := cache.For(MustOrgNamespace("orga", ""))
	if err != nil {
		t.Fatalf("For orgA (2): %v", err)
	}
	if a1 != a2 {
		t.Fatal("same org must return the SAME cached handle")
	}
	if opened != 1 {
		t.Fatalf("open called %d times for one org, want 1", opened)
	}

	b, err := cache.For(MustOrgNamespace("orgb", ""))
	if err != nil {
		t.Fatalf("For orgB: %v", err)
	}
	if b == a1 {
		t.Fatal("distinct orgs must get distinct handles")
	}

	// A project scope under the same org is a DISTINCT file/handle.
	pa, err := cache.For(MustOrgNamespace("orga", "alpha"))
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

// TestOrgStoreEach proves the cross-org sweep primitive: it enumerates exactly the
// orgs that have THIS subsystem's file (skipping the reserved _* partitions and orgs
// with only other subsystems' files) and hands back the SAME cached handle For
// returns — never a second open of the file (the property a reconciler relies on,
// since the at-rest layer does not support a concurrent second open).
func TestOrgStoreEach(t *testing.T) {
	dir := t.TempDir()
	opened := 0
	cache := NewOrgStore(Base{DataDir: dir}, "widget", func(db *sql.DB) (*sql.DB, error) { opened++; return db, nil })
	t.Cleanup(func() { _ = cache.CloseAll() })

	// Two real orgs (For creates + caches their widget.db) ...
	a, err := cache.For(MustOrgNamespace("orga", ""))
	if err != nil {
		t.Fatalf("For orga: %v", err)
	}
	b, err := cache.For(MustOrgNamespace("orgb", ""))
	if err != nil {
		t.Fatalf("For orgb: %v", err)
	}
	if opened != 2 {
		t.Fatalf("want 2 opens after two For, got %d", opened)
	}
	// ... a reserved platform partition (must be skipped) ...
	p, err := OrgDB(dir, namespace.System(), "widget")
	if err != nil {
		t.Fatalf("platform partition: %v", err)
	}
	defer func() { _ = p.Close() }()
	// ... and an org dir carrying only a DIFFERENT subsystem's file (no widget.db → skipped).
	other := filepath.Join(dir, "orgs", "orgc")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "gadget.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	seen := map[namespace.Namespace]*sql.DB{}
	if err := cache.Each(func(ns namespace.Namespace, st *sql.DB, e error) {
		if e != nil {
			t.Fatalf("Each open %s: %v", ns, e)
		}
		seen[ns] = st
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}

	// Exactly the two real orgs — _platform skipped, orgc (no widget.db) skipped.
	nsA, nsB := MustOrgNamespace("orga", ""), MustOrgNamespace("orgb", "")
	if len(seen) != 2 || seen[nsA] == nil || seen[nsB] == nil {
		t.Fatalf("Each enumerated %d namespaces %v, want exactly {orga,orgb}", len(seen), seen)
	}
	// The handles are the SAME cached ones For returned — no second open.
	if seen[nsA] != a || seen[nsB] != b {
		t.Fatal("Each must return the cached handle, not a fresh open")
	}
	if opened != 2 {
		t.Fatalf("Each must not re-open already-cached orgs: opens=%d want 2", opened)
	}

	// A missing orgs root is an empty enumeration, not an error.
	empty := NewOrgStore(Base{DataDir: filepath.Join(dir, "nope")}, "widget", func(db *sql.DB) (*sql.DB, error) { return db, nil })
	if err := empty.Each(func(namespace.Namespace, *sql.DB, error) { t.Fatal("no orgs → fn must not be called") }); err != nil {
		t.Fatalf("missing root want nil error, got %v", err)
	}
}

// TestSanitizeInjectiveAndSafe locks the properties OrgDB relies on: a
// clean DNS label is the identity, case-only siblings do NOT fold onto one slug
// (a case-insensitive-filesystem cross-org break), and unsafe-rune orgs are
// refused.
func TestSanitizeInjectiveAndSafe(t *testing.T) {
	if got := namespace.Sanitize("acme"); got != "acme" {
		t.Fatalf("clean DNS label should be identity, got %q", got)
	}
	if namespace.Sanitize("") != "" {
		t.Fatal("empty org must be refused")
	}
	if namespace.Sanitize("acme ") != "" { // trailing space is an unsafe (trimmable) rune
		t.Fatal("whitespace-bearing org must be refused")
	}
	// "Acme" and "acme" are DISTINCT owners and must NOT share a slug.
	if namespace.Sanitize("Acme") == namespace.Sanitize("acme") {
		t.Fatal("case-only siblings folded onto one slug (cross-org break)")
	}
	// Folded (non-identity) output carries the disambiguation suffix.
	if got := namespace.Sanitize("Acme"); got == "acme" || got == "Acme" {
		t.Fatalf("non-identity owner must be re-suffixed, got %q", got)
	}
}

// TestOwnedOnTheLocalOnlyPath — who owns an org when there is no fence to ask.
//
// It is the one question Sync cannot answer. Sync acks trivially without a
// Durability, because a write is then already as durable as the deployment is
// configured to be. Ownership has no such trivial answer: one writer owns
// everything and several writers can prove nothing about which of them owns what.
//
// MUTATION: return true unconditionally when dur is nil and the multi-writer row
// goes green — which is two pods each billing every org, the whole C-1 defect,
// reintroduced under the configuration where the object store is down.
func TestOwnedOnTheLocalOnlyPath(t *testing.T) {
	ns := MustOrgNamespace("acme", "")
	for _, c := range []struct {
		name  string
		peers bool
		want  bool
	}{
		{"a lone writer owns everything it holds", false, true},
		{"several writers with no fence own nothing", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := NewOrgStore(Base{DataDir: t.TempDir(), Peers: c.peers}, "probe",
				func(db *sql.DB) (*sql.DB, error) { return db, nil })
			t.Cleanup(func() { _ = st.CloseAll() })
			if got := st.Owned(ns); got != c.want {
				t.Fatalf("Owned = %v, want %v", got, c.want)
			}
			// Sync is unchanged either way: it is not the gate being asked here, and a
			// test that let the two blur would not notice one standing in for the other.
			if acked, err := st.Sync(ns); !acked || err != nil {
				t.Fatalf("Sync on a local-only store = (%v, %v), want (true, nil)", acked, err)
			}
		})
	}
}

// TestPeersIsEitherSignal. A production deployment declares its writers one of two
// ways, and reading only the static list is a hole in the live one: an in-cluster
// deployment names CLOUD_PEER_SELECTOR and routinely names no CLOUD_PEERS at all.
//
// MUTATION: drop the PeerSelector clause from build.go and the last row here goes
// green — two pods that each believe they are alone.
func TestPeersIsEitherSignal(t *testing.T) {
	for _, c := range []struct {
		name     string
		peers    string
		selector string
		want     bool
	}{
		{"a lone pod, no selector", "", "", false},
		{"one named peer is this pod itself", "a@a:1", "", false},
		{"two named peers", "a@a:1,b@b:1", "", true},
		{"a selector names writers a list does not", "", "app=cloud", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := peered(&Config{ShardPeers: c.peers, PeerSelector: c.selector})
			if got != c.want {
				t.Fatalf("peers(%q, %q) = %v, want %v", c.peers, c.selector, got, c.want)
			}
		})
	}
}
