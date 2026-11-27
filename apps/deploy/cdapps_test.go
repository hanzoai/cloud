package deploy

// The documented list must return the applications that exist.
//
// GET /v1/deploy/applications projected `hanzo.ai/v1` App CRs, and the production
// cluster holds ZERO of them; the 328 applications actually deployed are
// `apps.hanzo.ai/v1alpha1` Applications, reachable only through /v1/deploy/gitops.
// The endpoint an operator reaches for answered `items: []` — a complete, correct,
// useless answer.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// listApplicationsAs drives GET /v1/deploy/applications with the given identity
// headers and returns status + decoded body.
func listApplicationsAs(t *testing.T, s *cloud.Service[state], headers map[string]string) (int, map[string]any) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	req := httptest.NewRequest("GET", "/v1/deploy/applications", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, body
}

func itemNames(body map[string]any) []string {
	items, _ := body["items"].([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		m, _ := it.(map[string]any)
		meta, _ := m["metadata"].(map[string]any)
		if n, ok := meta["name"].(string); ok {
			out = append(out, n)
		}
	}
	return out
}

// TestApplicationsListsTheCDPlaneForAnOperator pins the fix: an operator asking
// the documented endpoint gets the applications that exist, not an empty list of a
// kind nothing creates.
func TestApplicationsListsTheCDPlaneForAnOperator(t *testing.T) {
	s := fakeService(
		cdApp("iam", "https://git.hanzo.ai/hanzoai/universe", "abc123", "Synced", "Healthy", nil),
		cdApp("kms", "https://git.hanzo.ai/hanzoai/universe", "def456", "OutOfSync", "Degraded", nil),
	)

	status, body := listApplicationsAs(t, s, map[string]string{"X-User-IsAdmin": "true"})
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	names := itemNames(body)
	if len(names) != 2 {
		t.Fatalf("operator saw %d applications %v; want the 2 CD Applications that exist", len(names), names)
	}

	// The observed state must come through, not just the name — an operator reads
	// this to find what is unhealthy.
	items, _ := body["items"].([]any)
	first, _ := items[0].(map[string]any)
	st, _ := first["status"].(map[string]any)
	sync, _ := st["sync"].(map[string]any)
	health, _ := st["health"].(map[string]any)
	if sync["status"] == "" || health["status"] == "" {
		t.Errorf("CD application projected without sync/health: %v", st)
	}
	spec, _ := first["spec"].(map[string]any)
	src, _ := spec["source"].(map[string]any)
	if src["repoURL"] == "" {
		t.Errorf("CD application projected without its source: %v", spec)
	}
}

// TestApplicationsDoesNotWidenForATenant is the other half, and the one that keeps
// this a fix rather than a leak. The CD plane is fleet infrastructure; an org
// member must see exactly what it saw before — its own namespace's App CRs — and
// never another tenant's, nor the platform's.
func TestApplicationsDoesNotWidenForATenant(t *testing.T) {
	s := fakeService(
		cdApp("iam", "https://git.hanzo.ai/hanzoai/universe", "abc123", "Synced", "Healthy", nil),
		cdApp("kms", "https://git.hanzo.ai/hanzoai/universe", "def456", "Synced", "Healthy", nil),
	)

	status, body := listApplicationsAs(t, s, map[string]string{
		"X-Org-Id":  "acme",
		"X-User-Id": "u1",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if names := itemNames(body); len(names) != 0 {
		t.Fatalf("a tenant was shown the platform CD plane: %v", names)
	}
}

// TestApplicationsWithoutTheCDCRDIsNotAnError pins that a cluster with no Hanzo CD
// contributes nothing rather than failing the request — the same treatment
// /v1/deploy/gitops gives an absent CRD.
func TestApplicationsWithoutTheCDCRDIsNotAnError(t *testing.T) {
	s := fakeService() // no CD Applications at all
	status, body := listApplicationsAs(t, s, map[string]string{"X-User-IsAdmin": "true"})
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200", status)
	}
	if names := itemNames(body); len(names) != 0 {
		t.Fatalf("unexpected items: %v", names)
	}
}

// TestApplicationsAndGitOpsExposeOrgAuthorizedCDAppsWithStrictTenantIsolation
// pins that an org-authorized caller in "hanzo" sees "hanzo"'s CD Applications
// on /v1/deploy/applications, /v1/deploy/applications/{name}, and /v1/deploy/gitops
// without requiring X-User-IsAdmin, while another tenant ("acme") sees none of them.
func TestApplicationsAndGitOpsExposeOrgAuthorizedCDAppsWithStrictTenantIsolation(t *testing.T) {
	s := fakeService(
		cdApp("cloud", "https://git.hanzo.ai/hanzoai/universe", "3af69f9d", "Synced", "Healthy", nil),
		cdApp("iam", "https://git.hanzo.ai/hanzoai/universe", "def456", "Synced", "Healthy", nil),
	)

	// 1. Validated "hanzo" org caller sees "hanzo"'s CD applications without SuperAdmin.
	status, body := listApplicationsAs(t, s, map[string]string{
		"X-Org-Id":  "hanzo",
		"X-User-Id": "u-grok",
	})
	if status != http.StatusOK {
		t.Fatalf("hanzo list status = %d; want 200", status)
	}
	if names := itemNames(body); len(names) != 2 {
		t.Fatalf("hanzo org caller saw %v, want [cloud iam]", names)
	}

	// 2. Detail view /v1/deploy/applications/cloud works for "hanzo" org caller and 404s for "acme".
	resp := getAs(t, s, "/v1/deploy/applications/cloud", map[string]string{"X-Org-Id": "hanzo", "X-User-Id": "u-grok"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hanzo GET /v1/deploy/applications/cloud = %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	respAcme := getAs(t, s, "/v1/deploy/applications/cloud", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u-acme"})
	if respAcme.StatusCode != http.StatusNotFound {
		t.Fatalf("acme GET /v1/deploy/applications/cloud = %d, want 404 (strict tenant isolation)", respAcme.StatusCode)
	}
	_ = respAcme.Body.Close()

	// 3. /v1/deploy/gitops returns "hanzo"'s CD applications to "hanzo" and empty to "acme".
	gitopsHanzo := jsonBody(t, getAs(t, s, "/v1/deploy/gitops", map[string]string{"X-Org-Id": "hanzo", "X-User-Id": "u-grok"}))
	if apps, _ := gitopsHanzo["applications"].([]any); len(apps) != 2 {
		t.Fatalf("hanzo GET /v1/deploy/gitops applications = %v, want 2", apps)
	}
	gitopsAcme := jsonBody(t, getAs(t, s, "/v1/deploy/gitops", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u-acme"}))
	if apps, _ := gitopsAcme["applications"].([]any); len(apps) != 0 {
		t.Fatalf("acme GET /v1/deploy/gitops leaked applications = %v, want 0", apps)
	}
}
