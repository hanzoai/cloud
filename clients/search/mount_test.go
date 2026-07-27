package search

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestMountedRoutesAreReachable proves every route this subsystem declares is
// actually registered. The Meilisearch client probes /health before it will use
// a server at all, so a route that mounts but does not answer makes the whole
// surface look absent — which is exactly what shipped: /v1/search/indexes
// answered in production while /v1/search/health and /v1/search/version 404'd.
func TestMountedRoutesAreReachable(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/search/health"},
		{http.MethodGet, "/v1/search/version"},
		{http.MethodPost, "/v1/search/indexes"},
		{http.MethodGet, "/v1/search/indexes/x"},
		{http.MethodGet, "/v1/search/indexes/x/settings"},
		{http.MethodPatch, "/v1/search/indexes/x/settings"},
		{http.MethodPost, "/v1/search/indexes/x/search"},
		{http.MethodPost, "/v1/search/indexes/x/documents"},
		{http.MethodPut, "/v1/search/indexes/x/documents"},
		{http.MethodGet, "/v1/search/indexes/x/documents"},
		{http.MethodPost, "/v1/search/indexes/x/documents/delete-batch"},
		{http.MethodGet, "/v1/search/indexes/x/documents/1"},
		{http.MethodDelete, "/v1/search/indexes/x/documents/1"},
		{http.MethodGet, "/v1/search/tasks/1"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, "http://x"+tc.path, nil)
			resp, err := app.Fiber().Test(req)
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			// A missing route and a real "that index does not exist" are BOTH
			// 404, so the status alone cannot tell them apart. A registered
			// route answers in the Meilisearch error shape; the router's own
			// not-found does not carry a `code`.
			if resp.StatusCode != http.StatusNotFound {
				return
			}
			var body struct {
				Code string `json:"code"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if body.Code == "" {
				t.Errorf("%s %s is not registered (router 404, no handler answered)", tc.method, tc.path)
			}
		})
	}
}
