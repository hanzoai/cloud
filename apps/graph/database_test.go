package graph

import (
	"testing"

	"github.com/zap-proto/zip"
)

// graphOf is what the caller of one project can read about an entity.
func graphOf(t *testing.T, app *zip.App, project, entity string) []string {
	t.Helper()
	return valuesOf(answered(t, app, project, "/v1/graph?entity="+entity))
}

// TestAProjectIsAGraphDatabase is the whole feature. One organization keeps as
// many graphs as it has projects, and they are separate DATABASES rather than
// separate predicates over one — so a read cannot forget to narrow and a walk
// cannot cross.
func TestAProjectIsAGraphDatabase(t *testing.T) {
	app := mountGraph(t)

	assertFact(t, app, "alpha", "acme/svc/api", "owner", "acme/team/core", true)
	assertFact(t, app, "beta", "acme/svc/api", "owner", "acme/team/platform", true)

	if got := graphOf(t, app, "alpha", "acme/svc/api"); len(got) != 1 || got[0] != "acme/team/core" {
		t.Errorf("alpha reads %v, want only its own assertion", got)
	}
	if got := graphOf(t, app, "beta", "acme/svc/api"); len(got) != 1 || got[0] != "acme/team/platform" {
		t.Errorf("beta reads %v, want only its own assertion", got)
	}
}

// TestAProjectSeesNothingOfAnother is the negative half, stated on a relation the
// other database never heard of.
func TestAProjectSeesNothingOfAnother(t *testing.T) {
	app := mountGraph(t)

	assertFact(t, app, "alpha", "acme/svc/api", "secret", "alpha-only", false)

	if got := graphOf(t, app, "beta", "acme/svc/api"); len(got) != 0 {
		t.Errorf("beta read %v from alpha's database", got)
	}
}

// TestTheDefaultProjectIsTheWholeOrgView pins the convention that keeps an org
// that never names a project reading exactly what it always read: no header and
// the literal "default" are ONE scope, and it is the file this plane opened
// before graphs were nameable.
func TestTheDefaultProjectIsTheWholeOrgView(t *testing.T) {
	app := mountGraph(t)

	assertFact(t, app, "", "acme/svc/api", "owner", "acme/team/core", true)

	for _, project := range []string{"", "default"} {
		got := graphOf(t, app, project, "acme/svc/api")
		if len(got) != 1 || got[0] != "acme/team/core" {
			t.Errorf("project %q reads %v; absent and \"default\" are the same scope", project, got)
		}
	}

	// And a named project is NOT that scope — it starts empty.
	if got := graphOf(t, app, "alpha", "acme/svc/api"); len(got) != 0 {
		t.Errorf("a named project inherited the default project's assertions: %v", got)
	}
}
