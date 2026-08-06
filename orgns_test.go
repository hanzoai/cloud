package cloud

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/namespace"
)

// TestOnlyOrgnsBuildsANamespace is the structural half of the isolation
// argument, for the whole repository.
//
// Isolation is the FILE an entity's records live in, so the entire question is
// "what names the file". A namespace is unforgeable by construction — there is
// no route to one that skips a constructor — but that only bounds what a name
// can BE, not what it can be built FROM. If every constructor call lives in
// orgns.go, then reading orgns.go is enough to know what a database can be named
// after anywhere in cloud, and the audit is a handful of calls to OrgNamespace
// sitting next to the principals they read rather than every handler in the tree.
//
// Without this, a future handler could write namespace.MustOrg(c.Query("org"))
// and get a perfectly legal namespace naming somebody else's database — the same
// bug in a new costume, and one that compiles.
//
// It is a source assertion because that is the only kind that survives code
// nobody has written yet.
func TestOnlyOrgnsBuildsANamespace(t *testing.T) {
	// Every constructor that turns a VALUE INTO A NAME: the entity ones, Parse, Of
	// (from a billing subject) and OrgProject (from an org and a project, which is
	// what OrgNamespace below IS). OrgProject and MustOrgProject are on this list
	// precisely BECAUSE they are the friendly ones — they fold a hostile name into
	// a safe segment, which makes namespace.MustOrgProject(c.Query("org")) compile,
	// run, and quietly name somebody else's database.
	//
	// namespace.System is deliberately absent, and it is the one exception the
	// argument survives: it takes NO INPUT. There is one deployment and one system
	// namespace, it is a different KIND from every entity namespace, and no string
	// anywhere can be folded into it — so a store naming the deployment's own
	// partition is naming a constant, not an entity, and nothing about "what can a
	// database be named after" is at stake. Every platform store says so where it
	// opens, which is where a reader asks.
	//
	// namespace.Sanitize is absent for a different reason, and NewGroup and
	// MustGroup with it: none of them returns a Namespace. A slug is a string until
	// a constructor accepts it, and a group is CODE a package declares once rather
	// than data that arrives with a request.
	build := regexp.MustCompile(`\bnamespace\.(Org|User|Repo|MustOrg|MustUser|MustRepo|Parse|Of|OrgProject|MustOrgProject)\(`)

	// The doors. orgns.go is cloud's, and holds the argument. finance.go is the
	// SECOND and last, and it is one because cloud imports it: a package below
	// cloud cannot call OrgNamespace, so before this it carried its own org→slug
	// regexp instead — a second injective slugger the gate could not see, which is
	// strictly worse than the call it now makes. Its org is principal.Org, the same
	// validated claim every OrgNamespace caller passes. A THIRD door is not a thing
	// to add; move the caller above cloud, or take the namespace as a parameter.
	doors := map[string]bool{
		"orgns.go":                true,
		"apps/finance/finance.go": true,
	}

	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A dot-directory is not this module's source. It is .git, or a
			// worktree an agent parked under .claude — a whole second copy of
			// this repository, whose orgns.go and apps/finance/finance.go are
			// the SAME two doors reported at paths that exist for nobody else.
			// This gate read one and went red while nothing in the module had
			// changed. Every other source-walking gate here already states the
			// rule (typed_request_gate_test.go, iamurl_test.go,
			// cmd/cloud/mount_test.go); this was the one that did not.
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "node_modules", "webui", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if rel, _ := filepath.Rel(root, path); doors[filepath.ToSlash(rel)] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if loc := build.FindIndex(b); loc != nil {
			rel, _ := filepath.Rel(root, path)
			line := 1 + strings.Count(string(b[:loc[0]]), "\n")
			t.Errorf("%s:%d builds a namespace outside orgns.go — every namespace must come "+
				"from OrgNamespace so the entity naming the file is "+
				"provably the validated one", rel, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestOrgNamespaceRefusesWhatSanitizeRefuses pins the door shut on the inputs
// that must never become a database name. Each of these would, if admitted, be a
// file two distinct orgs could share or a segment that escapes the data dir.
func TestOrgNamespaceRefusesWhatSanitizeRefuses(t *testing.T) {
	for _, tc := range []struct{ org, project, why string }{
		{"", "", "an empty org names no entity"},
		{"acme ", "", "a trailing space folds onto acme after any TrimSpace"},
		{"ac​me", "", "a zero-width rune is invisible in an identifier"},
		{"acme", " ", "a whitespace-only project names no group"},
		{"!!!", "", "an org that folds to nothing would be named by its hash alone"},
	} {
		if ns, err := OrgNamespace(tc.org, tc.project); err == nil {
			t.Errorf("OrgNamespace(%q, %q) = %q, want an error: %s", tc.org, tc.project, ns, tc.why)
		}
	}
}

// TestOrgNamespaceNeutralisesTraversal pins what happens to an org that looks
// like a path. It is not refused — namespace.Sanitize folds every separator and dot to
// "-" and then disambiguates with a hash of the raw owner, so "../etc" becomes
// one safe segment that is still distinct from an org actually named "etc". The
// property that matters is not "rejected" but "cannot leave its directory", and
// asserting the wrong one of those would be a test that passes for the wrong
// reason.
func TestOrgNamespaceNeutralisesTraversal(t *testing.T) {
	hostile := []string{"../etc", "..", ".", "a/b", "..%2f..%2fvictim", `a\b`}
	for _, org := range hostile {
		ns, err := OrgNamespace(org, "")
		if err != nil {
			continue // refused outright is also fine
		}
		id := ns.ID()
		if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
			t.Errorf("OrgNamespace(%q) = %q, whose id %q can leave its directory", org, ns, id)
		}
		key, err := namespace.Key(ns, "widget")
		if err != nil {
			t.Fatalf("namespace.Key(%q): %v", ns, err)
		}
		if want := path.Join(orgsRoot, id, "widget.db"); key != want {
			t.Errorf("OrgNamespace(%q) rendered to %q, want %q", org, key, want)
		}
	}
	// The same for a hostile project, which lands in the group slot.
	for _, project := range hostile {
		ns, err := OrgNamespace("acme", project)
		if err != nil {
			continue
		}
		if g := ns.Group().String(); g == "." || g == ".." || strings.ContainsAny(g, `/\`) {
			t.Errorf("project %q became group %q, which can leave its directory", project, g)
		}
		if ns.ID() != "acme" {
			t.Errorf("project %q changed the org to %q", project, ns.ID())
		}
	}
}

// TestOrgNamespaceIsInjective proves the property the whole boundary rests on:
// two distinct orgs never name one database. namespace.Sanitize carries it (every fold
// is disambiguated by a hash of the raw owner) and namespace.Org's case fold
// cannot undo it, because namespace.Sanitize has already lowercased everything it emits.
func TestOrgNamespaceIsInjective(t *testing.T) {
	seen := map[string]string{}
	for _, org := range []string{
		"acme", "ACME", "Acme", "acme-corp", "acme_corp", "acme.corp",
		"team-a", "team.a", "team_a", "platform", "system", "org",
	} {
		ns, err := OrgNamespace(org, "")
		if err != nil {
			t.Fatalf("OrgNamespace(%q): %v", org, err)
		}
		if prev, dup := seen[ns.String()]; dup {
			t.Fatalf("orgs %q and %q name the SAME database %q", prev, org, ns)
		}
		seen[ns.String()] = org
	}
	// And no org can reach the deployment's own partition, whatever it is called.
	for _, org := range []string{"_platform", "platform", "system", "_system"} {
		ns, err := OrgNamespace(org, "")
		if err != nil {
			continue
		}
		if ns == namespace.System() {
			t.Fatalf("org %q named the platform partition", org)
		}
		if k, err := namespace.Key(ns, "kms"); err == nil {
			if p, _ := namespace.Key(namespace.System(), "kms"); k == p {
				t.Fatalf("org %q rendered to the platform partition's file %q", org, k)
			}
		}
	}
}
