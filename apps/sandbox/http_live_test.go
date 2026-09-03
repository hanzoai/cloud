package sandbox

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The live proof in live_test.go calls the runtime directly — r.exec, r.stop,
// r.purge. It proves a POD runs code. It cannot fail if every route in Routes()
// were deleted, because it never issues a request. That is the gap this file
// closes: the SAME proof, driven only through the seven registered routes, so
// what is verified is the address a client actually calls.
//
// Guarded by SANDBOX_LIVE like its sibling: it needs a cluster.
//
//	SANDBOX_LIVE=1 SANDBOX_NAMESPACE=hanzo-sandboxes \
//	  go test ./apps/sandbox/ -run TestLiveHTTP -v
func mountHTTP(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

func req(t *testing.T, app *zip.App, method, path, org, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if org != "" {
		r.Header.Set("X-Org-Id", org)
		r.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 180 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestLiveHTTPSandboxEditsRealCode drives create → write → exec → read → delete
// over HTTP against a real cluster. Every assertion is a status code and a body
// from a route, never a direct call into the runtime.
func TestLiveHTTPSandboxEditsRealCode(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") == "" {
		t.Skip("SANDBOX_LIVE unset")
	}
	app := mountHTTP(t)
	const org = "hanzo"

	// The org gate is a ROUTE fact, not a handler courtesy: no principal, no
	// sandbox. Checked first so a later 201 cannot be explained by an ungated route.
	if code, b := req(t, app, http.MethodGet, "/v1/sandbox", "", ""); code != http.StatusForbidden {
		t.Fatalf("unauthenticated list: want 403, got %d %s", code, b)
	}

	code, b := req(t, app, http.MethodPost, "/v1/sandbox", org,
		`{"class":"exec","image":"node:22","ttlSec":600}`)
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/sandbox: want 201, got %d %s", code, b)
	}
	var m Sandbox
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode create: %v (%s)", err, b)
	}
	if m.ID == "" {
		t.Fatalf("create returned no id: %s", b)
	}
	t.Logf("CREATED over HTTP: id=%s class=%s status=%s", m.ID, m.Class, m.Status)
	defer func() {
		c, _ := req(t, app, http.MethodDelete, "/v1/sandbox/"+m.ID+"?purge=1", org, "")
		t.Logf("DELETE /v1/sandbox/%s -> %d", m.ID, c)
	}()

	base := "/v1/sandbox/" + m.ID

	// GET /:id — the member route answers the row it just minted.
	if code, b = req(t, app, http.MethodGet, base, org, ""); code != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d %s", base, code, b)
	}
	t.Logf("GET MEMBER over HTTP: %s", firstLine(b))

	// POST /:id/fs — raw body is the file. This is the agent writing code.
	src := "export const answer = 42; // written over /v1/sandbox/:id/fs\n"
	if code, b = req(t, app, http.MethodPost, base+"/fs?path=answer.js", org, src); code != http.StatusOK {
		t.Fatalf("POST %s/fs: want 200, got %d %s", base, code, b)
	}
	t.Logf("WROTE FILE over HTTP: %s", firstLine(b))

	// GET /:id/fs — a SEPARATE request reads it back. Proves the edit is in the
	// pod, not in this process's memory.
	if code, b = req(t, app, http.MethodGet, base+"/fs?path=answer.js", org, ""); code != http.StatusOK {
		t.Fatalf("GET %s/fs: want 200, got %d %s", base, code, b)
	}
	if string(b) != src {
		t.Fatalf("read back differs:\n got: %q\nwant: %q", b, src)
	}
	t.Logf("EDIT PERSISTS over HTTP: %s", strings.TrimSpace(string(b)))

	// POST /:id/exec — the code that was written over HTTP is now RUN over HTTP.
	if code, b = req(t, app, http.MethodPost, base+"/exec", org,
		`{"command":"node -e \"import('./answer.js').then(m=>console.log('ANSWER='+m.answer))\""}`); code != http.StatusOK {
		t.Fatalf("POST %s/exec: want 200, got %d %s", base, code, b)
	}
	var run struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exitCode"`
	}
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatalf("decode exec: %v (%s)", err, b)
	}
	if !strings.Contains(run.Stdout, "ANSWER=42") {
		t.Fatalf("edited code did not run: exit=%d stdout=%q stderr=%q", run.ExitCode, run.Stdout, run.Stderr)
	}
	t.Logf("EDITED CODE RUNS over HTTP: %s", strings.TrimSpace(run.Stdout))

	// A non-zero exit is DATA on this route, not a 500 — an agent has to be able
	// to read a failing build.
	if code, b = req(t, app, http.MethodPost, base+"/exec", org, `{"command":"exit 3"}`); code != http.StatusOK {
		t.Fatalf("failing exec: want 200 (failure is data), got %d %s", code, b)
	}
	run.ExitCode = 0
	_ = json.Unmarshal(b, &run)
	if run.ExitCode != 3 {
		t.Fatalf("failing exec: want exitCode 3, got %d (%s)", run.ExitCode, b)
	}
	t.Logf("FAILURE IS DATA over HTTP: exitCode=%d", run.ExitCode)

	// Another org may not see, read or exec in this sandbox. Isolation is the
	// property most easily left declared-but-not-real.
	if code, _ = req(t, app, http.MethodGet, base, "other-org", ""); code == http.StatusOK {
		t.Fatalf("cross-org GET %s returned 200 — org isolation is not enforced", base)
	}
	t.Logf("CROSS-ORG REFUSED over HTTP: GET as other-org -> %d", code)

	// The collection lists what it leased.
	if code, b = req(t, app, http.MethodGet, "/v1/sandbox", org, ""); code != http.StatusOK {
		t.Fatalf("GET /v1/sandbox: want 200, got %d %s", code, b)
	}
	if !strings.Contains(string(b), m.ID) {
		t.Fatalf("list does not contain the sandbox it just leased: %s", firstLine(b))
	}
	t.Logf("LIST CONTAINS IT over HTTP")
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}
