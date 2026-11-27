package host

// A KEY'S POLICY IS APPLIED WHERE THE KEY BECOMES A PRINCIPAL.
//
// Each API key is its own credential with its own policy: an expiry and a
// revocation IAM enforces when it resolves the key, and a grant — what the key may
// reach, and whether it may write — that this boundary applies to every request's
// address. These tests drive real keys through SanitizeIdentity against a stub IAM
// that answers per key, the way keys/principal does.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// iamPolicyServer answers keys/principal per presented key: a live key resolves
// to acme/alice with its scope, a dead one is refused with IAM's reason code.
func iamPolicyServer(t *testing.T, scopes map[string]string, dead map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.URL.Query().Get("accessKey")
		if why, ok := dead[key]; ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"` + why + `"}`))
			return
		}
		scope, ok := scopes[key]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist","code":"key_unknown"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"acme","name":"alice","org":"acme","scope":"` + scope + `","key":"` + key + `-row"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A key's own rate binds besides the org's rules, in a bucket of its own: the
// limited key is refused past its rate while a sibling key keeps going.
func TestAKeysRateBindsAndASiblingKeepsGoing(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error {
		key := c.Header("X-Test-Key")
		var g cloud.Grant
		if key == "slow" {
			g = cloud.Grant(nil).Policy(0, 0, 2)
		}
		principal.Mint(c, principal.Principal{Org: "acme", User: "acme/ann", Kind: principal.Key, Key: key, Limit: g})
		return c.Continue()
	}))
	app.Use(ScopeRateLimit(nil, nil))
	app.Raw(http.MethodGet, "/v1/train/jobs", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]bool{"ok": true}) })
	send := func(key string) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
		r.Header.Set("X-Org-Id", "acme")
		r.Header.Set("X-User-Id", "acme/ann")
		r.Header.Set("X-Test-Key", key)
		resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for i := range 2 {
		if code := send("slow"); code != http.StatusOK {
			t.Fatalf("request %d inside the key's rate: %d", i+1, code)
		}
	}
	if code := send("slow"); code != http.StatusTooManyRequests {
		t.Fatalf("the third request in a minute on a 2/minute key: want 429, got %d", code)
	}
	for range 5 {
		if code := send("fast"); code != http.StatusOK {
			t.Fatalf("the sibling key was limited: %d", code)
		}
	}
}
