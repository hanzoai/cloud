package graph

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// inProject files one assertion as the caller of `project`. An empty project is a
// caller that names none, which is the whole-org view.
func inProject(t *testing.T, app *zip.App, project, entity, relation, value string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"assertions": []map[string]any{{
		"entity": entity, "relation": relation, "value": value,
		"at": "2026-01-01T00:00:00Z", "seen": "2026-01-01T00:00:00Z",
		"source": "test", "evidence": "test://" + entity,
	}}})
	req, _ := http.NewRequest("POST", "http://cloud/v1/graph", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(zip.HeaderOrg, "acme")
	req.Header.Set(zip.HeaderUser, "acme/z@acme.test")
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("assert in %q: %v", project, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("assert in %q: status %d: %s", project, resp.StatusCode, b)
	}
}

// readProject is what that caller can see afterwards, as relation=value pairs.
func readProject(t *testing.T, app *zip.App, project, entity string) map[string]string {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://cloud/v1/graph?entity="+entity, nil)
	req.Header.Set(zip.HeaderOrg, "acme")
	req.Header.Set(zip.HeaderUser, "acme/z@acme.test")
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("read in %q: %v", project, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("read in %q: status %d: %s", project, resp.StatusCode, b)
	}
	var out struct {
		Assertions []struct{ Relation, Value string } `json:"assertions"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("read in %q answered something that is not the read shape: %v: %s", project, err, b)
	}
	got := map[string]string{}
	for _, a := range out.Assertions {
		got[a.Relation] = a.Value
	}
	return got
}

// TestAProjectIsAGraphDatabase is the whole feature. One organization keeps as
// many graphs as it has projects, and they are separate DATABASES rather than
// separate predicates over one — so a read cannot forget to narrow and a walk
// cannot cross.
func TestAProjectIsAGraphDatabase(t *testing.T) {
	app := mountGraph(t)

	inProject(t, app, "alpha", "acme/svc/api", "owner", "acme/team/core")
	inProject(t, app, "beta", "acme/svc/api", "owner", "acme/team/platform")

	if got := readProject(t, app, "alpha", "acme/svc/api")["owner"]; got != "acme/team/core" {
		t.Errorf("alpha reads owner = %q, want its own assertion", got)
	}
	if got := readProject(t, app, "beta", "acme/svc/api")["owner"]; got != "acme/team/platform" {
		t.Errorf("beta reads owner = %q, want its own assertion", got)
	}

	// The same key in both databases is two unrelated things, not a conflict:
	// contestation is a question inside ONE graph.
	if len(readProject(t, app, "alpha", "acme/svc/api")) != 1 {
		t.Error("alpha sees more than it filed — the databases are not separate")
	}
}

// TestAProjectSeesNothingOfAnother is the negative half, stated on a relation the
// other database never heard of.
func TestAProjectSeesNothingOfAnother(t *testing.T) {
	app := mountGraph(t)

	inProject(t, app, "alpha", "acme/svc/api", "secret", "alpha-only")

	if got := readProject(t, app, "beta", "acme/svc/api"); len(got) != 0 {
		t.Errorf("beta read %v from alpha's database", got)
	}
}

// TestTheDefaultProjectIsTheWholeOrgView pins the convention that keeps an org
// that never names a project reading exactly what it always read: no header and
// the literal "default" are ONE scope, and it is the file this plane opened
// before graphs were nameable.
func TestTheDefaultProjectIsTheWholeOrgView(t *testing.T) {
	app := mountGraph(t)

	inProject(t, app, "", "acme/svc/api", "owner", "acme/team/core")

	for _, project := range []string{"", "default"} {
		got := readProject(t, app, project, "acme/svc/api")
		if got["owner"] != "acme/team/core" {
			t.Errorf("project %q reads owner = %q; absent and %q are the same scope", project, got["owner"], "default")
		}
	}

	// And a named project is NOT that scope — it starts empty.
	if got := readProject(t, app, "alpha", "acme/svc/api"); len(got) != 0 {
		t.Errorf("a named project inherited the default project's assertions: %v", got)
	}
}
