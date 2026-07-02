package crm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
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
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestHTTPRoundTripAndIsolation is the end-to-end wire proof: create→list per
// org, cross-tenant reads blocked, no-org rejected, validation enforced. This is
// the exact behavior the live curl proof exercises.
func TestHTTPRoundTripAndIsolation(t *testing.T) {
	app := mountApp(t)

	// No org → 403 on every collection.
	for _, p := range []string{"/v1/crm/companies", "/v1/crm/contacts", "/v1/crm/opportunities", "/v1/crm/summary"} {
		if code, _ := do(t, app, http.MethodGet, p, "", nil); code != http.StatusForbidden {
			t.Fatalf("no-org GET %s want 403, got %d", p, code)
		}
	}

	// maxpower creates a company.
	code, body := do(t, app, http.MethodPost, "/v1/crm/companies", "maxpower",
		map[string]any{"name": "MaxPower Inc", "domainName": "maxpower.ai", "employees": 42, "idealCustomerProfile": true})
	if code != http.StatusCreated {
		t.Fatalf("create company want 201, got %d (%s)", code, body)
	}
	var comp Company
	if err := json.Unmarshal(body, &comp); err != nil || comp.ID == "" {
		t.Fatalf("create company json: %v (%s)", err, body)
	}
	if comp.Name != "MaxPower Inc" || comp.Employees != 42 || !comp.ICP {
		t.Fatalf("create company round-trip mismatch: %+v", comp)
	}

	// maxpower creates a contact linked to that company.
	code, body = do(t, app, http.MethodPost, "/v1/crm/contacts", "maxpower",
		map[string]any{"firstName": "Dave", "lastName": "Lorenzini", "email": "dave@maxpower.ai", "jobTitle": "CEO", "companyId": comp.ID})
	if code != http.StatusCreated {
		t.Fatalf("create contact want 201, got %d (%s)", code, body)
	}
	var contact Contact
	_ = json.Unmarshal(body, &contact)

	// maxpower creates an opportunity referencing both.
	code, body = do(t, app, http.MethodPost, "/v1/crm/opportunities", "maxpower",
		map[string]any{"name": "Enterprise Deal", "amount": 5000000, "stage": "proposal", "companyId": comp.ID, "pointOfContactId": contact.ID})
	if code != http.StatusCreated {
		t.Fatalf("create opp want 201, got %d (%s)", code, body)
	}
	var opp Opportunity
	_ = json.Unmarshal(body, &opp)
	if opp.Stage != "PROPOSAL" { // lower-case input normalized
		t.Fatalf("stage normalize want PROPOSAL, got %q", opp.Stage)
	}

	// maxpower lists → sees its rows.
	code, body = do(t, app, http.MethodGet, "/v1/crm/companies", "maxpower", nil)
	var listed struct {
		Data []Company `json:"data"`
	}
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 1 || listed.Data[0].Name != "MaxPower Inc" {
		t.Fatalf("maxpower list want [MaxPower Inc], got %d %+v", code, listed.Data)
	}

	// summary reflects real counts.
	code, body = do(t, app, http.MethodGet, "/v1/crm/summary", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"companies":1`)) || !bytes.Contains(body, []byte(`"opportunities":1`)) {
		t.Fatalf("summary want 1/1/1, got %d %s", code, body)
	}

	// acme sees NOTHING and cannot read maxpower's company by id.
	code, body = do(t, app, http.MethodGet, "/v1/crm/companies", "acme", nil)
	listed.Data = nil
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("acme must see zero companies, got %d %+v", code, listed.Data)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/crm/companies/"+comp.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme GET maxpower company want 404, got %d", code)
	}
	// acme cannot delete maxpower's company either.
	if code, _ := do(t, app, http.MethodDelete, "/v1/crm/companies/"+comp.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme DELETE maxpower company want 404, got %d", code)
	}
}

// TestHTTPValidation covers the boundary rejections the FE relies on.
func TestHTTPValidation(t *testing.T) {
	app := mountApp(t)

	// company without name → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/companies", "o", map[string]any{"domainName": "x.com"}); code != http.StatusBadRequest {
		t.Fatalf("company no-name want 400, got %d", code)
	}
	// contact with no identity fields → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/contacts", "o", map[string]any{"jobTitle": "Nobody"}); code != http.StatusBadRequest {
		t.Fatalf("contact empty want 400, got %d", code)
	}
	// opportunity with bad stage → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/opportunities", "o", map[string]any{"name": "D", "stage": "BOGUS"}); code != http.StatusBadRequest {
		t.Fatalf("opp bad stage want 400, got %d", code)
	}
	// opportunity referencing a missing company → 422.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/opportunities", "o", map[string]any{"name": "D", "companyId": "comp_ghost"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("opp bad ref want 422, got %d", code)
	}
}
