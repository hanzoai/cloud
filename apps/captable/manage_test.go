package captable

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

	// The org's admin, signed in by cookie, as four different requests reach us.
	forged = role{name: "a page on another site carrying the admin's cookie", org: "acme", orgAdmin: true,
		headers: map[string]string{"Cookie": "hanzo_iam_token=v", "Sec-Fetch-Site": "cross-site"}}
	sibling = role{name: "a page on a sibling subdomain carrying the admin's cookie", org: "acme", orgAdmin: true,
		headers: map[string]string{"Cookie": "hanzo_iam_token=v", "Sec-Fetch-Site": "same-site"}}
	console = role{name: "the admin's own page", org: "acme", orgAdmin: true,
		headers: map[string]string{"Cookie": "hanzo_iam_token=v", "Sec-Fetch-Site": "same-origin"}}
	server = role{name: "a server calling with the admin's IAM bearer", org: "acme", orgAdmin: true,
		headers: map[string]string{"Authorization": "Bearer iam-access-token", "Cookie": "hanzo_iam_token=v"}}
)

// send drives one request through the mounted routes as r.
func send(t *testing.T, app *zip.App, r role, method, path string, body []byte) (int, []byte) {
	t.Helper()
	rq := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
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
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestOnlyAnAdminKeepsTheCapTable is the gate: every route, read or write, refuses
// a member who is not an admin of the org, and admits the org's admin and a
// SuperAdmin acting in it. A cap table is the company's to keep, and belonging to
// the org does not show it.
func TestOnlyAnAdminKeepsTheCapTable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		// The admin records a holder, so the member below is refused a table that has
		// something in it.
		if code, b := send(t, app, orgAdmin, http.MethodPost, "/v1/captable/stakeholders",
			[]byte(`{"name":"Ada Lovelace","email":"ada@example.com","stakeholderType":"INDIVIDUAL","currentRelationship":"FOUNDER"}`)); code != http.StatusCreated {
			t.Fatalf("admin add stakeholder: %d %s", code, b)
		}

		routes := []struct {
			method, path string
			body         []byte
		}{
			{http.MethodGet, "/v1/captable/company", nil},
			{http.MethodGet, "/v1/captable/stakeholders", nil},
			{http.MethodGet, "/v1/captable/classes", nil},
			{http.MethodGet, "/v1/captable/plans", nil},
			{http.MethodGet, "/v1/captable/shares", nil},
			{http.MethodGet, "/v1/captable/options", nil},
			{http.MethodGet, "/v1/captable/safes", nil},
			{http.MethodGet, "/v1/captable/convertibles", nil},
			{http.MethodGet, "/v1/captable/rounds", nil},
			{http.MethodGet, "/v1/captable/investments", nil},
			{http.MethodGet, "/v1/captable/summary", nil},
			{http.MethodGet, "/v1/captable/rounds/r1", nil},
			{http.MethodDelete, "/v1/captable/stakeholders/s1", nil},
			{http.MethodDelete, "/v1/captable/shares/s1", nil},
			{http.MethodDelete, "/v1/captable/options/o1", nil},
			{http.MethodDelete, "/v1/captable/safes/s1", nil},
			{http.MethodDelete, "/v1/captable/convertibles/c1", nil},
			{http.MethodPut, "/v1/captable/company", []byte(`{"name":"Acme"}`)},
			{http.MethodPatch, "/v1/captable/stakeholders/s1", []byte(`{"name":"X"}`)},
			{http.MethodPost, "/v1/captable/rounds/r1/close", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/stakeholders", []byte(`{"name":"Eve","email":"eve@example.com","stakeholderType":"INDIVIDUAL","currentRelationship":"INVESTOR"}`)},
			{http.MethodPost, "/v1/captable/classes", []byte(`{"name":"Common"}`)},
			{http.MethodPatch, "/v1/captable/classes/c1", []byte(`{"name":"Common"}`)},
			{http.MethodPost, "/v1/captable/plans", []byte(`{"name":"Plan"}`)},
			{http.MethodPost, "/v1/captable/shares", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/shares/transfer", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/options", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/safes", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/convertibles", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/rounds", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/rounds/r1/investments", []byte(`{}`)},
		}
		for _, rt := range routes {
			if code, b := send(t, app, anonymous, rt.method, rt.path, rt.body); code != http.StatusUnauthorized {
				t.Errorf("%s %s as %s answered %d, want 401: %s", rt.method, rt.path, anonymous.name, code, b)
			}
			code, b := send(t, app, member, rt.method, rt.path, rt.body)
			if code != http.StatusForbidden {
				t.Errorf("%s %s as %s answered %d, want 403: %s", rt.method, rt.path, member.name, code, b)
			} else if !strings.Contains(string(b), cloud.Admin.Refusal().Error()) {
				t.Errorf("%s %s refused a member without the admin refusal: %s", rt.method, rt.path, b)
			}
			// Admitted means the cap table answered — a bogus id is its own 404, a
			// partial body its own 400.
			for _, r := range []role{orgAdmin, superUser} {
				if code, b := send(t, app, r, rt.method, rt.path, rt.body); code == http.StatusUnauthorized || code == http.StatusForbidden || code >= 500 {
					t.Errorf("%s %s as %s answered %d, want the cap table's answer: %s", rt.method, rt.path, r.name, code, b)
				}
			}
		}

		// What the admin wrote is there for the admin and the SuperAdmin, and for them
		// only.
		for _, r := range []role{orgAdmin, superUser} {
			code, b := send(t, app, r, http.MethodGet, "/v1/captable/stakeholders", nil)
			var rows []map[string]any
			if code != http.StatusOK || json.Unmarshal(b, &rows) != nil || len(rows) == 0 {
				t.Errorf("%s reading the stakeholders: %d %s", r.name, code, b)
			}
		}
	})
}

// TestAWriteIsTheAdminsOwnRequest is the anti-forgery rule on every cap-table
// write: a page on another site, or on a sibling subdomain, that makes the admin's
// browser send a change with their cookie is refused, while the admin's own page
// and a server presenting the admin's IAM bearer — captable.hanzo.ai calls
// /v1/captable server-side that way — are not. Reads are not changes and are not
// asked.
func TestAWriteIsTheAdminsOwnRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		writes := []struct {
			method, path string
			body         []byte
		}{
			{http.MethodPut, "/v1/captable/company", []byte(`{"name":"Acme"}`)},
			{http.MethodPatch, "/v1/captable/stakeholders/s1", []byte(`{"name":"X"}`)},
			{http.MethodPost, "/v1/captable/rounds/r1/close", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/stakeholders", []byte(`{"name":"Eve","email":"eve@example.com","stakeholderType":"INDIVIDUAL","currentRelationship":"INVESTOR"}`)},
			{http.MethodPost, "/v1/captable/classes", []byte(`{"name":"Common"}`)},
			{http.MethodPatch, "/v1/captable/classes/c1", []byte(`{"name":"Common"}`)},
			{http.MethodPost, "/v1/captable/plans", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/shares", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/shares/transfer", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/options", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/safes", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/convertibles", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/rounds", []byte(`{}`)},
			{http.MethodPost, "/v1/captable/rounds/r1/investments", []byte(`{}`)},
			{http.MethodDelete, "/v1/captable/stakeholders/s1", nil},
			{http.MethodDelete, "/v1/captable/shares/s1", nil},
			{http.MethodDelete, "/v1/captable/options/o1", nil},
			{http.MethodDelete, "/v1/captable/safes/s1", nil},
			{http.MethodDelete, "/v1/captable/convertibles/c1", nil},
		}
		for _, w := range writes {
			for _, r := range []role{forged, sibling} {
				code, b := send(t, app, r, w.method, w.path, w.body)
				if code != http.StatusForbidden || !strings.Contains(string(b), cloud.Unasked) {
					t.Errorf("%s %s from %s answered %d, want the anti-forgery 403: %s", w.method, w.path, r.name, code, b)
				}
			}
			for _, r := range []role{console, server} {
				if code, b := send(t, app, r, w.method, w.path, w.body); code == http.StatusForbidden || code == http.StatusUnauthorized || code >= 500 {
					t.Errorf("%s %s from %s answered %d, want the cap table's answer: %s", w.method, w.path, r.name, code, b)
				}
			}
		}
		if code, b := send(t, app, forged, http.MethodGet, "/v1/captable/summary", nil); code != http.StatusOK {
			t.Errorf("a read with the admin's cookie from another site answered %d, want 200: %s", code, b)
		}
	})
}
