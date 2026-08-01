package git

import (
	"os"
	"strings"
	"testing"
)

// Every subsystem runs as its own process, so a seam registered in one is nil in
// the others. The app that DECIDES to import (integrations, holding the provider
// credential) is never the app that owns the repos, which is why an import
// answered "git importer not registered" while both apps were healthy.

// The import must be reachable across that boundary, not only in-process.
func TestImportIsPublishedOnThePlane(t *testing.T) {
	src, err := os.ReadFile("import_plane.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(src), `"/git/import"`) {
		t.Error("no plane route: an import from another process cannot arrive")
	}
	// Wired at Mount, beside the other cross-app seams — a published op nothing
	// calls exposeImport for is unreachable.
	mount, err := os.ReadFile("git.go")
	if err != nil {
		t.Fatalf("read git.go: %v", err)
	}
	if !strings.Contains(string(mount), "exposeImport()") {
		t.Error("exposeImport is never called, so the route is never registered")
	}
}

// The tenant comes from the CALLER's plane identity. Reading it off the argument
// would let an app acting for one org create a repo in another's namespace.
func TestImportTakesTheOrgFromTheCaller(t *testing.T) {
	src, _ := os.ReadFile("import_plane.go")
	s := string(src)
	if !strings.Contains(s, "cloud.Who(ctx)") {
		t.Error("the org must come from the plane identity")
	}
	if !strings.Contains(s, "Org:       who.Org") {
		t.Error("the import must be performed for the CALLER's org")
	}
	// The payload carries no org at all, so there is nothing to widen.
	if strings.Contains(s, "in.Org") {
		t.Error("the argument must not name an org")
	}
}

// An anonymous call is refused rather than defaulted: a request arriving with no
// principal must fail, not create a repo somewhere.
func TestAnonymousImportIsRefused(t *testing.T) {
	src, _ := os.ReadFile("import_plane.go")
	if !strings.Contains(string(src), `zip.ErrForbidden("git import: org required")`) {
		t.Error("an anonymous import must be refused")
	}
}

// A push advancing a branch crosses the same boundary an import does: the app
// that RECEIVES the webhook (or runs the scheduled reconcile) is not the app that
// holds the repos.
func TestInboundIsPublishedOnThePlane(t *testing.T) {
	src, _ := os.ReadFile("import_plane.go")
	s := string(src)
	if !strings.Contains(s, `"/git/inbound"`) {
		t.Error("no plane route: a push from another process cannot advance a ref")
	}
	if !strings.Contains(s, `zip.ErrForbidden("git inbound: org required")`) {
		t.Error("an anonymous push must be refused, not applied somewhere")
	}
	if !strings.Contains(s, "Org: who.Org") {
		t.Error("the ref must be advanced for the CALLER's org")
	}
}

// A divergence is an ANSWER, not an error: native is canonical and was left
// untouched, and the caller needs to know that rather than retry into an
// overwrite. Conflict must survive the trip as a field.
func TestADivergenceCrossesThePlaneAsAnAnswer(t *testing.T) {
	src, _ := os.ReadFile("import_plane.go")
	s := string(src)
	if !strings.Contains(s, "Conflict: res.Conflict") {
		t.Error("a conflict must come back as a value; as an error it reads as a retryable failure")
	}
	for _, f := range []string{"Applied:", "NoOp:", "Before:", "After:"} {
		if !strings.Contains(s, f) {
			t.Errorf("the reply drops %s, so the caller cannot tell what happened", f)
		}
	}
}
