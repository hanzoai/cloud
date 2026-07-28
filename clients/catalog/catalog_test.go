package catalog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/index"
	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Same build-tag-agnostic harness as the index suite it borrows the store from.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	}
	os.Exit(m.Run())
}

// mount brings up the index (the store) and the catalog (the lens) on one app,
// exactly as apps.go orders them.
func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := index.Mount(app, deps); err != nil {
		t.Fatalf("index.Mount: %v", err)
	}
	t.Cleanup(func() { _ = index.Shutdown() })
	if err := Mount(app, deps); err != nil {
		t.Fatalf("catalog.Mount: %v", err)
	}
	return app
}

// do issues one request. hdr carries the SANITIZED principal headers the gateway
// mints; a test that set them on a real request would be testing the forgery the
// identity middleware strips, not this surface.
func do(t *testing.T, app *zip.App, method, url, body string, hdr map[string]string) (int, string) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, url, nil)
	} else {
		r = httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func decode(t *testing.T, body string) Response {
	t.Helper()
	var out Response
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

const admin = "X-User-IsAdmin"

// as mints the SANITIZED principal headers for one org. A validated principal is
// an org AND a user — principal.Org refuses an X-Org-Id with no X-User-Id behind
// it, which is exactly the forgery the identity boundary exists to stop.
func as(org string) map[string]string {
	return map[string]string{"X-Org-Id": org, "X-User-Id": "u@" + org}
}

// seed publishes the cross-org corpus as the platform SuperAdmin.
func seed(t *testing.T, app *zip.App) {
	t.Helper()
	code, body := do(t, app, http.MethodPut, "/v1/catalog", `{"entries":[
		{"id":"hanzo/console","org":"hanzo","name":"console","kind":"repo","archetype":"app","language":"TypeScript","description":"the operator console","forkable":true,"updated":"2026-07-01"},
		{"id":"lux/node","org":"lux","name":"node","kind":"repo","archetype":"infra","language":"Go","description":"lux blockchain node","updated":"2026-07-02"},
		{"id":"zoo/zips","org":"zoo","name":"zips","kind":"site","archetype":"site","language":"TypeScript","description":"zoo improvement proposals","url":"https://zips.zoo.ngo","updated":"2026-07-03"}
	]}`, map[string]string{admin: "true"})
	if code != http.StatusOK {
		t.Fatalf("publish: %d %s", code, body)
	}
}

// TestCrossOrgSearch is the whole point: ONE query reaches hanzo, lux and zoo.
func TestCrossOrgSearch(t *testing.T) {
	app := mount(t)
	seed(t, app)

	code, body := do(t, app, http.MethodGet, "/v1/catalog", "", nil)
	if code != http.StatusOK {
		t.Fatalf("browse: %d %s", code, body)
	}
	got := decode(t, body)
	if got.Total != 3 {
		t.Fatalf("want 3 entries across orgs, got %d: %s", got.Total, body)
	}
	for _, org := range []string{"hanzo", "lux", "zoo"} {
		if got.Facets["org"][org] != 1 {
			t.Errorf("org facet missing %q: %v", org, got.Facets["org"])
		}
	}

	// Free text is the index's job, not ours.
	if got := decode(t, mustGet(t, app, "/v1/catalog?q=blockchain")); got.Total != 1 || got.Data[0].Org != "lux" {
		t.Errorf("q=blockchain should find only the lux node, got %+v", got.Data)
	}
	// The browse axes are exact-match.
	if got := decode(t, mustGet(t, app, "/v1/catalog?language=Go")); got.Total != 1 || got.Data[0].ID != "lux/node" {
		t.Errorf("language=Go should find only lux/node, got %+v", got.Data)
	}
	if got := decode(t, mustGet(t, app, "/v1/catalog?forkable=true")); got.Total != 1 || got.Data[0].ID != "hanzo/console" {
		t.Errorf("forkable=true should find only hanzo/console, got %+v", got.Data)
	}
	if got := decode(t, mustGet(t, app, "/v1/catalog?org=zoo&archetype=site")); got.Total != 1 {
		t.Errorf("org+archetype should compose, got %+v", got.Data)
	}
}

// TestPrivateProjectNeverLeaks is the tenancy boundary. acme's own catalog row
// is visible to acme and to NOBODY else — not another tenant, not an anonymous
// caller — while the published corpus is visible to all three.
func TestPrivateProjectNeverLeaks(t *testing.T) {
	app := mount(t)
	seed(t, app)

	// acme writes its own row through the org-scoped dialect, as any tenant would.
	code, body := do(t, app, http.MethodPost, "/v1/index/indexes/catalog/documents",
		`[{"id":"acme/secret-crm","org":"acme","name":"secret-crm","kind":"site","archetype":"app","updated":"2026-07-04"}]`,
		as("acme"))
	if code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("acme write: %d %s", code, body)
	}

	for _, tc := range []struct {
		name string
		hdr  map[string]string
		want bool
	}{
		{"owner sees it", as("acme"), true},
		{"another tenant does not", as("globex"), false},
		{"anonymous does not", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decode(t, mustGetH(t, app, "/v1/catalog", tc.hdr))
			var found bool
			for _, e := range got.Data {
				if e.ID == "acme/secret-crm" {
					found = true
					if e.Scope != "org" {
						t.Errorf("a private row must be scoped %q, got %q", "org", e.Scope)
					}
				}
			}
			if found != tc.want {
				t.Fatalf("acme/secret-crm visible=%v, want %v: %v", found, tc.want, got.Data)
			}
			// Everyone still sees the published corpus.
			if got.Facets["org"]["lux"] != 1 {
				t.Errorf("published corpus missing for %s: %v", tc.name, got.Facets)
			}
		})
	}
}

// TestPublishIsPlatformOnly proves a tenant cannot promote itself into the
// cross-org catalog: the write is SuperAdmin, and the org it writes is a name no
// principal can hold.
func TestPublishIsPlatformOnly(t *testing.T) {
	app := mount(t)
	for _, hdr := range []map[string]string{nil, as("acme"), as(PublicOrg)} {
		if code, _ := do(t, app, http.MethodPut, "/v1/catalog",
			`{"entries":[{"id":"acme/ad","org":"hanzo","name":"ad"}]}`, hdr); code != http.StatusForbidden {
			t.Fatalf("publish as %v: got %d, want 403", hdr, code)
		}
	}
	if got := decode(t, mustGet(t, app, "/v1/catalog")); got.Total != 0 {
		t.Fatalf("nothing should have been published: %+v", got.Data)
	}
}

// TestPublishPrunes proves the corpus is a full swap, not an append: a project
// deleted upstream leaves the catalog on the next sync.
func TestPublishPrunes(t *testing.T) {
	app := mount(t)
	seed(t, app)
	code, body := do(t, app, http.MethodPut, "/v1/catalog",
		`{"entries":[{"id":"lux/node","org":"lux","name":"node","kind":"repo","language":"Go","updated":"2026-07-09"}]}`,
		map[string]string{admin: "true"})
	if code != http.StatusOK {
		t.Fatalf("republish: %d %s", code, body)
	}
	if !strings.Contains(body, `"pruned":2`) {
		t.Errorf("want 2 pruned, got %s", body)
	}
	got := decode(t, mustGet(t, app, "/v1/catalog"))
	if got.Total != 1 || got.Data[0].ID != "lux/node" || got.Data[0].Updated != "2026-07-09" {
		t.Fatalf("swap did not converge: %+v", got.Data)
	}
}

func mustGet(t *testing.T, app *zip.App, url string) string { return mustGetH(t, app, url, nil) }

func mustGetH(t *testing.T, app *zip.App, url string, hdr map[string]string) string {
	t.Helper()
	code, body := do(t, app, http.MethodGet, url, "", hdr)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, code, body)
	}
	return body
}
