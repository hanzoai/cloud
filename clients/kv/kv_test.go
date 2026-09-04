package kv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestMain hands cek a throwaway master key. The store opens through cek, which
// refuses to open a data plane unencrypted, so these tests run the REAL
// encrypted path — the same code a deployment runs, not a way around it.
func TestMain(m *testing.M) {
	_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	os.Exit(m.Run())
}

// mount builds the subsystem over a fresh directory exactly as a deployment
// does, so what the tests below drive is the mounted surface, not the handlers
// called out of band.
func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "kv-test", DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.NewWriter(io.Discard), DataDir: t.TempDir()}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// as issues one request carrying a validated caller in org. principal.Tenant
// reads BOTH headers: an org with no user is the anonymous forge and scopes to
// nothing, so a test that stated only the org would prove nothing about orgs.
func as(t *testing.T, app *zip.App, org, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u@"+org)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close() //nolint:errcheck
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return res.StatusCode, string(b)
}

// field reads one member of a JSON object response.
func field(t *testing.T, body, name string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("response is not a JSON object: %v (%s)", err, body)
	}
	return m[name]
}

// A key's life: written, read back as itself, replaced, listed, removed, and
// then gone. The revision is the part a caller can act on — it counts writes,
// so a reader can tell a value it has already seen from a new one.
func TestKeyLife(t *testing.T) {
	app := mount(t)

	code, body := as(t, app, "acme", "PUT", "/v1/kv/greeting", `{"value":{"hello":"world"}}`)
	if code != http.StatusOK {
		t.Fatalf("put = %d, want 200 (%s)", code, body)
	}
	if rev := field(t, body, "revision"); rev != float64(1) {
		t.Fatalf("first put revision = %v, want 1", rev)
	}

	code, body = as(t, app, "acme", "GET", "/v1/kv/greeting", "")
	if code != http.StatusOK {
		t.Fatalf("get = %d, want 200 (%s)", code, body)
	}
	if v, want := field(t, body, "value"), map[string]any{"hello": "world"}; !equal(v, want) {
		t.Fatalf("get value = %v, want %v", v, want)
	}

	if _, body = as(t, app, "acme", "PUT", "/v1/kv/greeting", `{"value":"again"}`); field(t, body, "revision") != float64(2) {
		t.Fatalf("second put revision = %v, want 2", field(t, body, "revision"))
	}

	code, body = as(t, app, "acme", "GET", "/v1/kv", "")
	if code != http.StatusOK || !strings.Contains(body, `"key":"greeting"`) {
		t.Fatalf("list = %d %s, want the key it holds", code, body)
	}

	if code, body = as(t, app, "acme", "DELETE", "/v1/kv/greeting", ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204 (%s)", code, body)
	}
	if code, _ = as(t, app, "acme", "GET", "/v1/kv/greeting", ""); code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", code)
	}
	if code, _ = as(t, app, "acme", "DELETE", "/v1/kv/greeting", ""); code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404: there was nothing left to remove", code)
	}
}

// One store holds every org's keys, so the org predicate on each statement is
// the whole of what keeps them apart. Same key, two orgs, no crossing.
func TestOrgsDoNotShareAKey(t *testing.T) {
	app := mount(t)

	as(t, app, "acme", "PUT", "/v1/kv/token", `{"value":"acme secret"}`)

	if code, body := as(t, app, "other", "GET", "/v1/kv/token", ""); code != http.StatusNotFound {
		t.Fatalf("other org read acme's key: %d %s", code, body)
	}
	if code, body := as(t, app, "other", "GET", "/v1/kv", ""); code != http.StatusOK || strings.Contains(body, "acme secret") {
		t.Fatalf("other org's list = %d %s, want none of acme's keys", code, body)
	}
	if code, _ := as(t, app, "other", "DELETE", "/v1/kv/token", ""); code != http.StatusNotFound {
		t.Fatalf("other org deleted acme's key: %d", code)
	}

	// acme's key is untouched by all of that, and still at its first revision.
	code, body := as(t, app, "acme", "GET", "/v1/kv/token", "")
	if code != http.StatusOK || field(t, body, "value") != "acme secret" {
		t.Fatalf("acme's own key = %d %s", code, body)
	}
	if rev := field(t, body, "revision"); rev != float64(1) {
		t.Fatalf("acme's key revision = %v, want 1", rev)
	}
}

// X-Org-Id alone is a claim, not a credential: off the gateway an anonymous
// caller can send any org it likes. The store answers a request that carries no
// validated principal with a refusal rather than with somebody's data.
func TestUnvalidatedCallerIsRefused(t *testing.T) {
	app := mount(t)

	as(t, app, "acme", "PUT", "/v1/kv/k", `{"value":"acme"}`)

	req := httptest.NewRequest("GET", "/v1/kv/k", nil)
	req.Header.Set("X-Org-Id", "acme") // stated, never validated: no X-User-Id
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("forged org read acme's key: %d, want 403", res.StatusCode)
	}
}

// A body that is not the {"value": …} envelope is still a value: the bytes the
// caller sent are what comes back.
func TestBareBodyIsTheValue(t *testing.T) {
	app := mount(t)

	if code, body := as(t, app, "acme", "PUT", "/v1/kv/raw", `[1,2,3]`); code != http.StatusOK {
		t.Fatalf("put = %d (%s)", code, body)
	}
	_, body := as(t, app, "acme", "GET", "/v1/kv/raw", "")
	if v, want := field(t, body, "value"), []any{float64(1), float64(2), float64(3)}; !equal(v, want) {
		t.Fatalf("value = %v, want %v", v, want)
	}
}

// equal compares two decoded JSON values.
func equal(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}
