package esign

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
func send(t *testing.T, app *zip.App, r role, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, rd)
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
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestOnlyAnAdminSendsForSignature is the gate: every sender route, read or write,
// refuses a member who is not an admin of the org and admits the org's admin and a
// SuperAdmin acting in it, while a recipient's token still opens their document
// with no account at all.
func TestOnlyAnAdminSendsForSignature(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountApp(t)
		pdf, err := os.ReadFile("testdata/example.pdf")
		if err != nil {
			t.Fatalf("read testdata: %v", err)
		}
		upload := map[string]any{"title": "Side letter", "pdfBase64": base64.StdEncoding.EncodeToString(pdf)}

		// draft is a document the admin prepares for the investor: one signer, one
		// signature field.
		draft := func() (docID, recID, token string) {
			t.Helper()
			code, b := send(t, app, orgAdmin, http.MethodPost, "/v1/esign/documents", upload)
			if code != http.StatusCreated {
				t.Fatalf("admin create: %d %s", code, b)
			}
			docID, _ = decode(t, b)["id"].(string)
			code, b = send(t, app, orgAdmin, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients",
				map[string]any{"email": "investor@example.com", "name": "Ivy Investor", "role": "SIGNER"})
			if code != http.StatusCreated {
				t.Fatalf("admin add recipient: %d %s", code, b)
			}
			rec := decode(t, b)
			recID, _ = rec["id"].(string)
			token, _ = rec["token"].(string)
			if code, b = send(t, app, orgAdmin, http.MethodPost, "/v1/esign/documents/"+docID+"/fields",
				map[string]any{"recipientId": recID, "type": "SIGNATURE", "page": 1, "positionX": 10, "positionY": 70, "width": 30, "height": 8}); code != http.StatusCreated {
				t.Fatalf("admin add field: %d %s", code, b)
			}
			return docID, recID, token
		}
		docID, recID, _ := draft()

		routes := []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, "/v1/esign/documents", upload},
			{http.MethodGet, "/v1/esign/documents", nil},
			{http.MethodGet, "/v1/esign/documents/" + docID, nil},
			{http.MethodPost, "/v1/esign/documents/" + docID + "/recipients", map[string]any{"email": "b@example.com", "name": "B", "role": "SIGNER"}},
			{http.MethodPost, "/v1/esign/documents/" + docID + "/fields", map[string]any{"recipientId": recID, "type": "SIGNATURE", "page": 1, "positionX": 10, "positionY": 70, "width": 30, "height": 8}},
			{http.MethodGet, "/v1/esign/documents/" + docID + "/download", nil},
			{http.MethodGet, "/v1/esign/documents/" + docID + "/audit", nil},
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
			for _, r := range []role{orgAdmin, superUser} {
				if code, b := send(t, app, r, rt.method, rt.path, rt.body); code == http.StatusUnauthorized || code == http.StatusForbidden || code >= 500 {
					t.Errorf("%s %s as %s answered %d, want the document's answer: %s", rt.method, rt.path, r.name, code, b)
				}
			}
		}

		// Sending is a sender write like the rest; the member is refused it and the
		// admin sends.
		sent, _, token := draft()
		if code, b := send(t, app, member, http.MethodPost, "/v1/esign/documents/"+sent+"/send", map[string]any{}); code != http.StatusForbidden {
			t.Errorf("a member sent the document: %d %s", code, b)
		}
		if code, b := send(t, app, orgAdmin, http.MethodPost, "/v1/esign/documents/"+sent+"/send", map[string]any{}); code != http.StatusOK {
			t.Fatalf("admin send: %d %s", code, b)
		}

		// The recipient opens their document by token alone.
		if code, b := send(t, app, anonymous, http.MethodGet, "/v1/esign/o/acme/sign/"+token, nil); code != http.StatusOK {
			t.Errorf("the recipient's token view answered %d, want 200: %s", code, b)
		}
	})
}

// TestASendIsTheAdminsOwnRequest is the anti-forgery rule on every sender write: a
// page on another site, or on a sibling subdomain, that makes the admin's browser
// send a change with their cookie is refused, while the admin's own page and a
// server presenting the admin's bearer are not. Reads are not asked, and neither
// is a recipient, whose token is the whole credential.
func TestASendIsTheAdminsOwnRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountApp(t)
		pdf, err := os.ReadFile("testdata/example.pdf")
		if err != nil {
			t.Fatalf("read testdata: %v", err)
		}
		upload := map[string]any{"title": "Side letter", "pdfBase64": base64.StdEncoding.EncodeToString(pdf)}
		code, b := send(t, app, orgAdmin, http.MethodPost, "/v1/esign/documents", upload)
		if code != http.StatusCreated {
			t.Fatalf("admin create: %d %s", code, b)
		}
		docID, _ := decode(t, b)["id"].(string)
		code, b = send(t, app, orgAdmin, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients",
			map[string]any{"email": "investor@example.com", "name": "Ivy Investor", "role": "SIGNER"})
		if code != http.StatusCreated {
			t.Fatalf("admin add recipient: %d %s", code, b)
		}
		rec := decode(t, b)
		recID, _ := rec["id"].(string)
		token, _ := rec["token"].(string)

		writes := []struct {
			method, path string
			body         any
		}{
			{http.MethodPost, "/v1/esign/documents", upload},
			{http.MethodPost, "/v1/esign/documents/" + docID + "/recipients", map[string]any{"email": "b@example.com", "name": "B", "role": "SIGNER"}},
			{http.MethodPost, "/v1/esign/documents/" + docID + "/fields", map[string]any{"recipientId": recID, "type": "SIGNATURE", "page": 1, "positionX": 10, "positionY": 70, "width": 30, "height": 8}},
			{http.MethodPost, "/v1/esign/documents/" + docID + "/send", map[string]any{}},
		}
		for _, w := range writes {
			for _, r := range []role{forged, sibling} {
				code, b := send(t, app, r, w.method, w.path, w.body)
				if code != http.StatusForbidden || !strings.Contains(string(b), cloud.Unasked) {
					t.Errorf("%s %s from %s answered %d, want the anti-forgery 403: %s", w.method, w.path, r.name, code, b)
				}
			}
		}
		// Admitted means the document answered: a send with an unprepared recipient
		// is its own 400.
		for _, w := range writes {
			for _, r := range []role{console, server} {
				if code, b := send(t, app, r, w.method, w.path, w.body); code == http.StatusForbidden || code == http.StatusUnauthorized || code >= 500 {
					t.Errorf("%s %s from %s answered %d, want the document's answer: %s", w.method, w.path, r.name, code, b)
				}
			}
		}
		if code, b := send(t, app, forged, http.MethodGet, "/v1/esign/documents/"+docID, nil); code != http.StatusOK {
			t.Errorf("a read with the admin's cookie from another site answered %d, want 200: %s", code, b)
		}
		recipient := role{name: "a recipient whose browser carries some hanzo cookie",
			headers: map[string]string{"Cookie": "hanzo_iam_token=v", "Sec-Fetch-Site": "cross-site"}}
		if code, b := send(t, app, recipient, http.MethodGet, "/v1/esign/o/acme/sign/"+token, nil); code != http.StatusOK {
			t.Errorf("the recipient's token view answered %d, want 200: %s", code, b)
		}
	})
}
