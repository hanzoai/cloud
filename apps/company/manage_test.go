package company

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// role is who a request comes from, as the identity boundary would mint it from a
// validated bearer: the org it acts in, whether the person administers that org,
// and whether they are a SuperAdmin.
type role struct {
	name     string
	org      string
	orgAdmin bool
	super    bool
	// headers are what else the request carries — a session cookie, what the
	// browser says about where it came from, a bearer.
	headers map[string]string
}

var (
	anonymous = role{name: "an anonymous caller"}
	member    = role{name: "a member who is not an admin", org: "acme"}
	orgAdmin  = role{name: "an admin of the org", org: "acme", orgAdmin: true}
	superUser = role{name: "a SuperAdmin acting in the org", org: "acme", super: true}
)

// send drives one request through the mounted routes as r.
func send(t *testing.T, app *zip.App, r role, method, path, contentType string, body []byte) (int, string) {
	t.Helper()
	rq := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		rq.Header.Set("Content-Type", contentType)
	}
	if r.org != "" {
		rq.Header.Set("X-Org-Id", r.org)
		rq.Header.Set("X-User-Id", "u_"+r.org)
	}
	if r.orgAdmin {
		rq.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if r.super {
		rq.Header.Set("X-User-IsAdmin", "true")
	}
	for k, v := range r.headers {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func sendJSON(t *testing.T, app *zip.App, r role, method, path string, body any) (int, string) {
	t.Helper()
	if body == nil {
		return send(t, app, r, method, path, "", nil)
	}
	raw, _ := json.Marshal(body)
	return send(t, app, r, method, path, "application/json", raw)
}

// TestOnlyAnAdminRunsTheCompany is the gate: every tenant route of the formation,
// read or write, refuses a member who is not an admin of the org, and admits the
// org's admin and a SuperAdmin acting in it. The formation writes the org's cap
// table and data room, so this is the same rule those surfaces keep.
func TestOnlyAnAdminRunsTheCompany(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, ct := mountFake(t)
		toCompany(t, app, "acme")
		seeded, imported := ct.seedCount, ct.imported

		routes := []struct {
			method, path, contentType string
			body                      []byte
		}{
			{http.MethodPost, "/v1/company", "application/json", []byte(`{"alreadyIncorporated":true}`)},
			{http.MethodGet, "/v1/company", "", nil},
			{http.MethodPut, "/v1/company/structure", "application/json", []byte(`{"structure":"c-corp","jurisdiction":"DE","name":"X"}`)},
			{http.MethodPost, "/v1/company/founders", "application/json", []byte(`{"founders":[{"name":"Eve","email":"eve@x.test","equityBps":10000}]}`)},
			{http.MethodPost, "/v1/company/kyc", "", nil},
			{http.MethodPost, "/v1/company/kyc/refresh", "", nil},
			{http.MethodPost, "/v1/company/payment", "", nil},
			{http.MethodPost, "/v1/company/documents", "", nil},
			{http.MethodPost, "/v1/company/esign", "", nil},
			{http.MethodPost, "/v1/company/esign/complete", "application/json", []byte(`{}`)},
			{http.MethodPost, "/v1/company/genesis", "", nil},
			{http.MethodPost, "/v1/company/advance", "application/json", []byte(`{"to":"company"}`)},
			{http.MethodPost, "/v1/company/skip", "", nil},
			{http.MethodPost, "/v1/company/import/documents", "application/json", []byte(`{"folderId":"F"}`)},
			{http.MethodPost, "/v1/company/import/captable", "application/json", []byte(`{"spreadsheetId":"S"}`)},
			{http.MethodPost, "/v1/company/fundraise/round", "application/json", []byte(`{"name":"Mallory round","roundType":"PRICED","targetAmount":1}`)},
			{http.MethodPost, "/v1/company/fundraise/safe", "application/json", []byte(`{}`)},
			{http.MethodPost, "/v1/company/fundraise/deck?name=deck", "application/pdf", []byte("%PDF-1.7\nDECK\n%%EOF")},
		}
		for _, rt := range routes {
			if code, b := send(t, app, anonymous, rt.method, rt.path, rt.contentType, rt.body); code != http.StatusUnauthorized {
				t.Errorf("%s %s as %s answered %d, want 401: %s", rt.method, rt.path, anonymous.name, code, b)
			}
			code, b := send(t, app, member, rt.method, rt.path, rt.contentType, rt.body)
			if code != http.StatusForbidden {
				t.Errorf("%s %s as %s answered %d, want 403: %s", rt.method, rt.path, member.name, code, b)
			} else if !strings.Contains(b, cloud.Admin.Refusal().Error()) {
				t.Errorf("%s %s refused a member without the admin refusal: %s", rt.method, rt.path, b)
			}
		}
		// Nothing a member sent reached the cap table.
		if ct.seedCount != seeded || ct.imported != imported || ct.roundName == "Mallory round" {
			t.Fatalf("a member's requests wrote the cap table: seeded %d→%d, imported %d→%d, round %q",
				seeded, ct.seedCount, imported, ct.imported, ct.roundName)
		}

		// Admitted means the formation answered: a step at the wrong stage is its own 409.
		for _, rt := range routes {
			for _, r := range []role{orgAdmin, superUser} {
				if code, b := send(t, app, r, rt.method, rt.path, rt.contentType, rt.body); code == http.StatusUnauthorized || code == http.StatusForbidden || code >= 500 {
					t.Errorf("%s %s as %s answered %d, want the formation's answer: %s", rt.method, rt.path, r.name, code, b)
				}
			}
		}
		if code, b := sendJSON(t, app, orgAdmin, http.MethodPost, "/v1/company/fundraise/round",
			map[string]any{"name": "Seed", "roundType": "PRICED", "targetAmount": 2000000}); code != http.StatusCreated || ct.roundName != "Seed" {
			t.Errorf("the admin's round: %d %s, recorded %q", code, b, ct.roundName)
		}
	})
}

// TestThePlatformBookStaysTheSuperAdmins holds the routes that were already
// SuperAdmin-only where they were: an org's own admin is refused the register and
// a founder KYC decision, and the SuperAdmin reaches both.
func TestThePlatformBookStaysTheSuperAdmins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _, _ := mountFake(t)
		toCompany(t, app, "acme")
		for _, path := range []string{"/v1/company/register", "/v1/company/register/summary", "/v1/company/review"} {
			for _, r := range []role{member, orgAdmin} {
				if code, b := send(t, app, r, http.MethodGet, path, "", nil); code != http.StatusForbidden {
					t.Errorf("GET %s as %s answered %d, want 403: %s", path, r.name, code, b)
				}
			}
			if code, b := send(t, app, superUser, http.MethodGet, path, "", nil); code != http.StatusOK {
				t.Errorf("GET %s as %s answered %d, want 200: %s", path, superUser.name, code, b)
			}
		}
		decision := map[string]any{"email": "nobody@acme.test", "status": "reviewer_confirmed"}
		for _, r := range []role{member, orgAdmin} {
			if code, b := sendJSON(t, app, r, http.MethodPost, "/v1/company/kyc/decision", decision); code != http.StatusForbidden || !strings.Contains(b, "platform reviewer") {
				t.Errorf("a KYC decision as %s answered %d, want the platform reviewer's 403: %s", r.name, code, b)
			}
		}
		if code, b := sendJSON(t, app, superUser, http.MethodPost, "/v1/company/kyc/decision", decision); code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Errorf("a KYC decision as %s answered %d: %s", superUser.name, code, b)
		}
		for _, path := range []string{"/v1/company/tariff", "/v1/company/ein"} {
			if code, b := sendJSON(t, app, anonymous, http.MethodPost, path, map[string]any{}); code == http.StatusUnauthorized || code == http.StatusForbidden {
				t.Errorf("POST %s, a public quote, answered %d: %s", path, code, b)
			}
		}
	})
}
