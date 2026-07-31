package crawl

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestBothSpellingsReachTheOp pins the claim the mount makes: ONE registration at
// /v1/crawl serves the trailing-slash spelling too, because fiber is non-strict.
// Registering both would mint a second operation — and a second MCP tool — for
// one address, so the single registration is load-bearing and the alias has to be
// proven rather than assumed.
//
// Neither request carries a principal or the service key, so both must be refused
// by the gate (401, the key being unset here makes it 503) rather than 404: a 404
// is the router saying the address does not exist, which is exactly the regression
// this guards.
func TestBothSpellingsReachTheOp(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	for _, path := range []string{"/v1/crawl", "/v1/crawl/"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://x"+path, nil)
			resp, err := app.Fiber().Test(req)
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("%s is not routed (404) — the trailing-slash alias is gone", path)
			}
		})
	}
}
