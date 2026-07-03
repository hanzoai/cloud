package security

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/security/detect"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

var testCfg = fiber.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Domain: "api.hanzo.test"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// do runs a JSON request through the Fiber test harness. A non-empty org sets
// BOTH the tenant header and the validated-principal header (principal.Tenant
// gates on X-User-Id), mirroring clients/git's test helper.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestSubmitScanFindsAndRedacts proves a scan detects a planted secret,
// persists a finding, and the stored/returned finding never carries the secret.
func TestSubmitScanFindsAndRedacts(t *testing.T) {
	app := mountApp(t)
	secret := "AKIAIOSFODNN7EXAMPLE"

	code, body := do(t, app, http.MethodPost, "/v1/security/scans", "acme", submitReq{
		Files: []fileInput{{Path: "config.py", Content: "aws_key = \"" + secret + "\"\nok = 1"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("submit want 201, got %d (%s)", code, body)
	}
	var sv scanView
	if err := json.Unmarshal(body, &sv); err != nil {
		t.Fatalf("scan json: %v (%s)", err, body)
	}
	if sv.Findings < 1 || sv.Critical < 1 {
		t.Fatalf("expected >=1 critical finding, got %+v", sv)
	}

	// The scan detail must carry the finding, redacted.
	code, body = do(t, app, http.MethodGet, "/v1/security/scans/"+sv.ID, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get scan want 200, got %d (%s)", code, body)
	}
	if bytes.Contains(body, []byte(secret)) {
		t.Fatalf("scan detail leaked the raw secret: %s", body)
	}
	var detail struct {
		Findings []findingView `json:"findings"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("detail json: %v", err)
	}
	if len(detail.Findings) == 0 {
		t.Fatal("scan detail has no findings")
	}
	f := detail.Findings[0]
	if f.Fingerprint != detect.Fingerprint(secret) {
		t.Fatalf("fingerprint mismatch: %q", f.Fingerprint)
	}
	if strings.Contains(f.Preview, secret) {
		t.Fatalf("preview leaked secret: %q", f.Preview)
	}
}

// TestTenantIsolation proves one org can never see another's scans or findings.
func TestTenantIsolation(t *testing.T) {
	app := mountApp(t)

	code, body := do(t, app, http.MethodPost, "/v1/security/scans", "acme", submitReq{
		Files: []fileInput{{Path: "a.py", Content: `k = "AKIAIOSFODNN7EXAMPLE"`}},
	})
	if code != http.StatusCreated {
		t.Fatalf("acme submit want 201, got %d (%s)", code, body)
	}
	var sv scanView
	_ = json.Unmarshal(body, &sv)

	// evil cannot GET acme's scan by id.
	if code, _ := do(t, app, http.MethodGet, "/v1/security/scans/"+sv.ID, "evil", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant scan get want 404, got %d", code)
	}
	// evil's scan list is empty.
	code, body = do(t, app, http.MethodGet, "/v1/security/scans", "evil", nil)
	if code != http.StatusOK {
		t.Fatalf("evil list want 200, got %d", code)
	}
	var listed struct {
		Data []scanView `json:"data"`
	}
	_ = json.Unmarshal(body, &listed)
	if len(listed.Data) != 0 {
		t.Fatalf("evil should see 0 scans, saw %d", len(listed.Data))
	}
	// evil's findings are empty too.
	code, body = do(t, app, http.MethodGet, "/v1/security/findings", "evil", nil)
	var fl struct {
		Data []findingView `json:"data"`
	}
	_ = json.Unmarshal(body, &fl)
	if code != http.StatusOK || len(fl.Data) != 0 {
		t.Fatalf("evil findings want 200/empty, got %d/%d", code, len(fl.Data))
	}
}

// TestNoPrincipalIsForbidden proves every tenant-scoped route 403s without a
// validated principal (an anonymous X-Org-Id is untrusted).
func TestNoPrincipalIsForbidden(t *testing.T) {
	app := mountApp(t)
	for _, p := range []string{"/v1/security/scans", "/v1/security/findings"} {
		if code, _ := do(t, app, http.MethodGet, p, "", nil); code != http.StatusForbidden {
			t.Fatalf("%s no-principal want 403, got %d", p, code)
		}
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/security/scans", "",
		submitReq{Files: []fileInput{{Path: "x", Content: "y"}}}); code != http.StatusForbidden {
		t.Fatalf("submit no-principal want 403, got %d", code)
	}
}

// TestFindingsSeverityFilter proves minSeverity drops lower-ranked findings.
func TestFindingsSeverityFilter(t *testing.T) {
	app := mountApp(t)
	// A critical (aws key) and a medium (jwt) in one scan.
	content := "k = \"AKIAIOSFODNN7EXAMPLE\"\nt = eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhYmMifQ.SGVsbG9TaWduYXR1cmU"
	if code, body := do(t, app, http.MethodPost, "/v1/security/scans", "acme",
		submitReq{Files: []fileInput{{Path: "m.txt", Content: content}}}); code != http.StatusCreated {
		t.Fatalf("submit want 201, got %d (%s)", code, body)
	}

	code, body := do(t, app, http.MethodGet, "/v1/security/findings?minSeverity=critical", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("filtered list want 200, got %d", code)
	}
	var fl struct {
		Data []findingView `json:"data"`
	}
	_ = json.Unmarshal(body, &fl)
	if len(fl.Data) == 0 {
		t.Fatal("expected at least the critical finding")
	}
	for _, f := range fl.Data {
		if f.Severity != detect.SeverityCritical {
			t.Fatalf("minSeverity=critical returned a %s finding", f.Severity)
		}
	}

	// An invalid minSeverity is a 400.
	if code, _ := do(t, app, http.MethodGet, "/v1/security/findings?minSeverity=nope", "acme", nil); code != http.StatusBadRequest {
		t.Fatalf("bad minSeverity want 400, got %d", code)
	}
}

// TestValidation proves the submit guards: empty files, and health/rules are
// open (no principal needed).
func TestValidationAndOpenRoutes(t *testing.T) {
	app := mountApp(t)

	if code, _ := do(t, app, http.MethodPost, "/v1/security/scans", "acme",
		submitReq{Files: nil}); code != http.StatusBadRequest {
		t.Fatalf("empty files want 400, got %d", code)
	}
	// health + rules need no principal.
	if code, body := do(t, app, http.MethodGet, "/v1/security/health", "", nil); code != http.StatusOK {
		t.Fatalf("health want 200, got %d (%s)", code, body)
	}
	code, body := do(t, app, http.MethodGet, "/v1/security/rules", "", nil)
	if code != http.StatusOK {
		t.Fatalf("rules want 200, got %d", code)
	}
	var rl struct {
		Data []detect.RuleView `json:"data"`
	}
	_ = json.Unmarshal(body, &rl)
	if len(rl.Data) == 0 {
		t.Fatal("rules catalog empty over HTTP")
	}
}

// TestCleanScanZeroFindings proves clean source yields a persisted scan with
// zero findings (a real "we looked and found nothing" record, not an error).
func TestCleanScanZeroFindings(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/security/scans", "acme",
		submitReq{Files: []fileInput{{Path: "clean.go", Content: "package main\nfunc main(){}"}}})
	if code != http.StatusCreated {
		t.Fatalf("clean submit want 201, got %d (%s)", code, body)
	}
	var sv scanView
	_ = json.Unmarshal(body, &sv)
	if sv.Findings != 0 {
		t.Fatalf("clean scan should have 0 findings, got %d", sv.Findings)
	}
	if sv.Files != 1 {
		t.Fatalf("scan should record 1 file, got %d", sv.Files)
	}
}
