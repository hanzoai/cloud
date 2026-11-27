package provisioning

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// A provisioned database, cache or bucket is the org's infrastructure: a plain
// member neither creates nor drops one, and a drop a member asks for leaves the
// resource standing. (Regression: any member could drop the org's databases; in
// hanzo, every self-serve customer.)
func TestAMemberNeitherProvisionsNorDrops(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		orch := newFakeOrch()
		s := newDedicatedService(t, orch)
		if resp := postCreate(t, s, "datastore", "acme", "analytics"); resp.StatusCode != http.StatusCreated {
			t.Fatalf("an admin provisions: %d", resp.StatusCode)
		}
		member := func(method, path, body string, mount func(*zip.App)) int {
			t.Helper()
			app := zip.New(zip.Config{DisableStartupMessage: true})
			app.Use(cloud.Bridge())
			mount(app)
			req, _ := http.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Org-Id", "acme")
			req.Header.Set("X-User-Id", "u-member")
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			return resp.StatusCode
		}
		if code := member(http.MethodPost, "/v1/datastore", `{"name":"mine"}`, func(a *zip.App) { mountCreate(a, s, "datastore") }); code != http.StatusForbidden {
			t.Fatalf("a member provisions: %d", code)
		}
		if code := member(http.MethodDelete, "/v1/datastore/analytics", "", mountDelete("/v1/datastore/:name", ops{s}.dropDatastore)); code != http.StatusForbidden {
			t.Fatalf("a member drops the org's datastore: %d", code)
		}
		resp := doReq(t, mountGet("/v1/datastore/:name", ops{s}.getDatastore), http.MethodGet, "/v1/datastore/analytics", "acme", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the datastore did not survive a member's drop: %d", resp.StatusCode)
		}
	})
}
