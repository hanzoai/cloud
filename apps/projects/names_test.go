package projects

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// ONE NAMESPACE: <name>.hanzo.app is held by exactly one site or app across
// every org, first come first served. A site holds its name from create; an app
// claims its own over the plane; a name anyone else holds — or the platform
// reserves — is refused, and a release by anyone but the holder frees nothing.
func TestNamesAreOneNamespace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		claim := func(org, name, holder string) bool {
			t.Helper()
			out, err := planeClaim(as(org), &client.NameIn{Name: name, Holder: holder})
			if err != nil {
				t.Fatalf("claim %s/%s as %s: %v", org, name, holder, err)
			}
			return out.Taken
		}
		create := func(org, name string, want int) {
			t.Helper()
			if code, body := do(t, app, http.MethodPost, "/v1/project", org, map[string]any{"name": name}); code != want {
				t.Fatalf("%s creating %q answered %d (%s), want %d", org, name, code, body, want)
			}
		}

		create("acme", "Shop", http.StatusCreated)
		if !claim("globex", "shop", "app:a1") {
			t.Fatal("an app took another org's site name")
		}
		if !claim("acme", "shop", "app:a1") {
			t.Fatal("an app took its own org's site name")
		}
		if claim("globex", "bakery", "app:a1") || claim("globex", "bakery", "app:a1") {
			t.Fatal("a free name, or its holder's own, was refused")
		}
		if !claim("globex", "bakery", "app:a2") {
			t.Fatal("a second app of the org took a held name")
		}
		create("acme", "Bakery", http.StatusConflict)
		create("globex", "Bakery", http.StatusConflict)
		if !claim("globex", "api", "app:a3") {
			t.Fatal("an app took a name the platform reserves")
		}

		if _, err := planeRelease(as("acme"), &client.NameIn{Name: "bakery", Holder: "app:a1"}); err != nil {
			t.Fatal(err)
		}
		create("acme", "Bakery", http.StatusConflict)
		if _, err := planeRelease(as("globex"), &client.NameIn{Name: "bakery", Holder: "app:a1"}); err != nil {
			t.Fatal(err)
		}
		create("acme", "Bakery", http.StatusCreated)

		for _, in := range []client.NameIn{{Name: "x", Holder: "x"}, {Name: "x", Holder: "app:"}, {Name: "x.y", Holder: "app:a"}} {
			if _, err := planeClaim(as("acme"), &in); err == nil {
				t.Errorf("claim %+v was taken", in)
			}
		}
	})
}
