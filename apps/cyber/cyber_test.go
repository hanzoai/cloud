// Copyright © 2026 Hanzo AI. MIT License.

package cyber

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

var testCfg = zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true}

func mountApp(t *testing.T, check alphaChecker) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_DOMAIN", "api.hanzo.test")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if check != nil {
		mounted.State.alphaCheck = check
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

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
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAlphaAuthAndTenantIsolation(t *testing.T) {
	grants := map[string]bool{"partner": true}
	app := mountApp(t, func(ctx context.Context, org string) (bool, error) {
		if hanzoInternalOrgs[strings.ToLower(org)] {
			return true, nil
		}
		return grants[org], nil
	})

	// 1. Health is public.
	code, _ := do(t, app, http.MethodGet, "/v1/cyber/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("health = %d, want 200", code)
	}

	// 2. Unauthenticated caller is refused (401).
	code, _ = do(t, app, http.MethodGet, "/v1/cyber/dashboard", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dashboard = %d, want 401", code)
	}

	// 3. External org without beta.cyber grant is refused (403).
	code, _ = do(t, app, http.MethodGet, "/v1/cyber/dashboard", "random-external", nil)
	if code != http.StatusForbidden {
		t.Fatalf("ungranted external dashboard = %d, want 403", code)
	}

	// 4. Hanzo org is auto-enabled by default.
	code, body := do(t, app, http.MethodPost, "/v1/cyber/engagements/seed-demo", "hanzo", nil)
	if code != http.StatusCreated {
		t.Fatalf("hanzo seed-demo = %d (%s), want 201", code, body)
	}

	// 5. Granted partner org cannot read Hanzo org's seeded engagements or findings.
	code, body = do(t, app, http.MethodGet, "/v1/cyber/engagements", "partner", nil)
	if code != http.StatusOK {
		t.Fatalf("partner list engagements = %d, want 200", code)
	}
	var partnerEngs engagementList
	if err := json.Unmarshal(body, &partnerEngs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(partnerEngs.Data) != 0 {
		t.Fatalf("partner saw %d hanzo engagements, want 0", len(partnerEngs.Data))
	}

	// 6. Cross-tenant task graph lookup returns 404.
	code, _ = do(t, app, http.MethodGet, "/v1/cyber/tasks/tsk_mdtf_audit_hanzo/graph", "partner", nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant task graph = %d, want 404", code)
	}
}

func TestEndToEndVerbatimProofAndKaiGovernance(t *testing.T) {
	app := mountApp(t, nil)

	// Create engagement.
	code, body := do(t, app, http.MethodPost, "/v1/cyber/engagements", "hanzo", createEngagementReq{
		Name: "Enclave Alpha", Target: "api.alpha.test", Scope: []string{"api.alpha.test"}, Mode: "enclave",
	})
	if code != http.StatusCreated {
		t.Fatalf("create engagement = %d (%s)", code, body)
	}
	var eng engagementView
	_ = json.Unmarshal(body, &eng)

	// Record captured HTTP exchange.
	code, body = do(t, app, http.MethodPost, "/v1/cyber/traffic", "hanzo", recordTrafficReq{
		EngagementID: eng.ID, Method: "GET", URL: "https://api.alpha.test/debug",
		StatusCode: 200, ResponseBody: `{"debug":true,"role":"admin_bypass"}`,
	})
	if code != http.StatusCreated {
		t.Fatalf("record traffic = %d (%s)", code, body)
	}
	var tr trafficView
	_ = json.Unmarshal(body, &tr)

	// Create finding bound to traffic.
	code, body = do(t, app, http.MethodPost, "/v1/cyber/findings", "hanzo", createFindingReq{
		EngagementID: eng.ID, Title: "Debug Bypass Exposed", Severity: "critical",
		Category: "Broken Access Control", CWE: "CWE-284", MitreTechnique: "T1190", MitreTactic: "Initial Access",
		Target: "https://api.alpha.test/debug", EvidenceQuote: `"role":"admin_bypass"`, TrafficID: tr.ID,
	})
	if code != http.StatusCreated {
		t.Fatalf("create finding = %d (%s)", code, body)
	}
	var f findingView
	_ = json.Unmarshal(body, &f)
	if f.Verified {
		t.Fatal("finding should start unverified before explicit verify call")
	}

	// Deterministic verbatim proof verification succeeds on exact substring.
	code, body = do(t, app, http.MethodPost, "/v1/cyber/findings/"+f.ID+"/verify", "hanzo", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("verify finding = %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &f)
	if !f.Verified || f.Status != "verified" {
		t.Fatalf("expected verified finding, got verified=%v status=%q note=%q", f.Verified, f.Status, f.VerificationNote)
	}

	// Non-matching quote must fail verification cleanly (verified=false).
	code, body = do(t, app, http.MethodPost, "/v1/cyber/findings/"+f.ID+"/verify", "hanzo", map[string]any{
		"evidenceQuote": "NOT_IN_RESPONSE_BODY",
	})
	if code != http.StatusOK {
		t.Fatalf("verify mismatch = %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &f)
	if f.Verified {
		t.Fatal("mismatched quote must never verify")
	}

	// Kai risk scoring & human decision.
	code, body = do(t, app, http.MethodPost, "/v1/cyber/approvals", "hanzo", submitApprovalReq{
		EngagementID: eng.ID, Action: "Run active exploit payload injection test",
		Target: "api.alpha.test", Tool: "opencyber-mcp:http_exploit",
	})
	if code != http.StatusCreated {
		t.Fatalf("submit approval = %d (%s)", code, body)
	}
	var apr approvalView
	_ = json.Unmarshal(body, &apr)
	if apr.RiskScore < 70 || (apr.RiskLevel != "high" && apr.RiskLevel != "critical") {
		t.Fatalf("expected high/critical Kai risk score, got %d (%s)", apr.RiskScore, apr.RiskLevel)
	}

	code, body = do(t, app, http.MethodPost, "/v1/cyber/approvals/"+apr.ID+"/decide", "hanzo", map[string]any{
		"decision": "always_allow",
	})
	if code != http.StatusOK {
		t.Fatalf("decide approval = %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &apr)
	if apr.Status != "always_allow" || apr.DecidedBy == "" {
		t.Fatalf("expected always_allow + decidedBy, got %+v", apr)
	}

	// Verify ATT&CK, CoA, and Hardening projections.
	for _, ep := range []string{"/v1/cyber/attack", "/v1/cyber/coa", "/v1/cyber/hardening", "/v1/cyber/dashboard"} {
		code, body = do(t, app, http.MethodGet, ep+"?engagementId="+eng.ID, "hanzo", nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d (%s)", ep, code, body)
		}
	}
}

func TestEveryRouteIsTypedAndDescribed(t *testing.T) {
	app := mountApp(t, nil)
	doc, err := spec.Of(app, spec.Info{Title: "cyber", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/cyber") }
	served, typed := map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	if len(served) != 24 {
		t.Errorf("cyber serves %d operations, expected 24", len(served))
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped ops: %s", strings.Join(untyped, ", "))
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/cyber/...", key)
		}
	}
}
