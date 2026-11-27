package dataroom

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
func send(t *testing.T, app *zip.App, r role, method, path, contentType string, body []byte) (int, []byte) {
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
	resp, err := app.Test(rq, zip.TestConfig{Timeout: time.Minute})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func sendJSON(t *testing.T, app *zip.App, r role, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	ct := ""
	if body != nil {
		raw, _ = json.Marshal(body)
		ct = "application/json"
	}
	code, b := send(t, app, r, method, path, ct, raw)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return code, m
}

// TestOnlyAnAdminManagesTheDataRoom is the gate: every admin route, read or write,
// refuses a member who is not an admin of the org, and admits the org's admin and
// a SuperAdmin acting in it. Belonging to an org does not open its data room.
func TestOnlyAnAdminManagesTheDataRoom(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountFlowApp(t)

		// The org's admin builds a room, a document in it and a password link to it.
		code, m := sendJSON(t, app, orgAdmin, http.MethodPost, "/v1/dataroom/datarooms", map[string]any{"name": "Board"})
		if code != http.StatusOK {
			t.Fatalf("admin create room: %d %v", code, m)
		}
		roomID := str(m, "dataroom", "id")
		code, raw := send(t, app, orgAdmin, http.MethodPost, "/v1/dataroom/documents?name=deck.pdf", "application/pdf",
			[]byte("%PDF-1.7\nBOARD DECK\n%%EOF"))
		if code != http.StatusOK {
			t.Fatalf("admin upload: %d %s", code, raw)
		}
		var up map[string]any
		_ = json.Unmarshal(raw, &up)
		docID := str(up, "document", "id")
		if code, m = sendJSON(t, app, orgAdmin, http.MethodPost, "/v1/dataroom/datarooms/"+roomID+"/documents",
			map[string]any{"documentId": docID}); code != http.StatusOK {
			t.Fatalf("admin attach: %d %v", code, m)
		}
		code, m = sendJSON(t, app, orgAdmin, http.MethodPost, "/v1/dataroom/links",
			map[string]any{"dataroomId": roomID, "name": "Investors", "password": "a-long-shared-phrase", "emailProtected": false})
		if code != http.StatusOK {
			t.Fatalf("admin create link: %d %v", code, m)
		}
		linkID := str(m, "link", "id")

		routes := []struct {
			method, path, contentType string
			body                      []byte
		}{
			{http.MethodGet, "/v1/dataroom/documents", "", nil},
			{http.MethodGet, "/v1/dataroom/documents/" + docID, "", nil},
			{http.MethodGet, "/v1/dataroom/documents/" + docID + "/file", "", nil},
			{http.MethodPost, "/v1/dataroom/documents?name=x.pdf", "application/pdf", []byte("%PDF-1.7\nX\n%%EOF")},
			{http.MethodGet, "/v1/dataroom/datarooms", "", nil},
			{http.MethodPost, "/v1/dataroom/datarooms", "application/json", []byte(`{"name":"Mine"}`)},
			{http.MethodGet, "/v1/dataroom/datarooms/" + roomID, "", nil},
			{http.MethodPost, "/v1/dataroom/datarooms/" + roomID + "/documents", "application/json", []byte(`{"documentId":"` + docID + `"}`)},
			{http.MethodGet, "/v1/dataroom/links", "", nil},
			{http.MethodPost, "/v1/dataroom/links", "application/json", []byte(`{"dataroomId":"` + roomID + `","name":"Mine"}`)},
			{http.MethodGet, "/v1/dataroom/analytics/link/" + linkID, "", nil},
			{http.MethodGet, "/v1/dataroom/analytics/dataroom/" + roomID, "", nil},
			{http.MethodGet, "/v1/dataroom/trust", "", nil},
			{http.MethodPut, "/v1/dataroom/trust", "application/json", []byte(`{"name":"Acme"}`)},
		}
		for _, rt := range routes {
			if code, b := send(t, app, anonymous, rt.method, rt.path, rt.contentType, rt.body); code != http.StatusUnauthorized {
				t.Errorf("%s %s as %s answered %d, want 401: %s", rt.method, rt.path, anonymous.name, code, b)
			}
			code, b := send(t, app, member, rt.method, rt.path, rt.contentType, rt.body)
			if code != http.StatusForbidden {
				t.Errorf("%s %s as %s answered %d, want 403: %s", rt.method, rt.path, member.name, code, b)
			} else if !strings.Contains(string(b), cloud.Admin.Refusal().Error()) {
				t.Errorf("%s %s refused a member without the admin refusal: %s", rt.method, rt.path, b)
			}
			// Admitted means the room answered: a repeat attach is the bundle's own 409.
			for _, r := range []role{orgAdmin, superUser} {
				if code, b := send(t, app, r, rt.method, rt.path, rt.contentType, rt.body); code == http.StatusUnauthorized || code == http.StatusForbidden || code >= 500 {
					t.Errorf("%s %s as %s answered %d, want the room's answer: %s", rt.method, rt.path, r.name, code, b)
				}
			}
		}

		// The share link opens for whoever holds it and its password, with no account,
		// exactly as before.
		code, m = sendJSON(t, app, anonymous, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate",
			map[string]any{"password": "a-long-shared-phrase"})
		if code != http.StatusOK {
			t.Fatalf("visitor authenticate: %d %v", code, m)
		}
		viewID, _ := m["viewId"].(string)
		if viewID == "" {
			t.Fatalf("visitor got no viewing session: %v", m)
		}
		if code, b := send(t, app, anonymous, http.MethodGet,
			"/v1/dataroom/view/"+linkID+"/document/"+docID+"/file?viewId="+viewID, "", nil); code != http.StatusOK || !bytes.Contains(b, []byte("BOARD DECK")) {
			t.Errorf("visitor file read: %d %q", code, b)
		}
		if code, _ := sendJSON(t, app, anonymous, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate",
			map[string]any{"password": "wrong"}); code != http.StatusUnauthorized {
			t.Errorf("a wrong password answered %d, want 401", code)
		}
		if code, _ := send(t, app, anonymous, http.MethodGet, "/v1/dataroom/health", "", nil); code != http.StatusOK {
			t.Errorf("health answered %d, want 200", code)
		}
	})
}

// TestAnAgentActingForAMemberIsRefused holds the gate on the MCP transport, where a
// typed op is reached by name and no route middleware runs.
func TestAnAgentActingForAMemberIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountMCPApp(t)
		call := func(r role) (string, bool) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_dataroom_documents","arguments":{}}}`
			code, raw := send(t, app, r, http.MethodPost, "/mcp", "application/json", []byte(body))
			if code != http.StatusOK {
				t.Fatalf("tools/call as %s: %d %s", r.name, code, raw)
			}
			var env struct {
				Result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				} `json:"result"`
				Error *struct{ Message string } `json:"error"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || env.Error != nil || len(env.Result.Content) == 0 {
				t.Fatalf("tools/call as %s: no result: %s", r.name, raw)
			}
			return env.Result.Content[0].Text, env.Result.IsError
		}
		if text, isErr := call(member); !isErr || !strings.Contains(text, cloud.Admin.Refusal().Error()) {
			t.Errorf("a member's agent listed the documents: isError=%v %s", isErr, text)
		}
		if text, isErr := call(orgAdmin); isErr {
			t.Errorf("an admin's agent was refused: %s", text)
		}
	})
}

// TestAWriteIsTheAdminsOwnRequest is the anti-forgery rule on every data-room
// write: a page on another site, or on a sibling subdomain, that makes the admin's
// browser send a change with their cookie is refused, while the admin's own page
// and a server presenting the admin's bearer are not. Reads and the public viewer
// are not changes and are not asked.
func TestAWriteIsTheAdminsOwnRequest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountFlowApp(t)
		code, m := sendJSON(t, app, orgAdmin, http.MethodPost, "/v1/dataroom/datarooms", map[string]any{"name": "Board"})
		if code != http.StatusOK {
			t.Fatalf("admin create room: %d %v", code, m)
		}
		roomID := str(m, "dataroom", "id")
		code, m = sendJSON(t, app, orgAdmin, http.MethodPost, "/v1/dataroom/links",
			map[string]any{"dataroomId": roomID, "name": "Investors", "password": "a-long-shared-phrase", "emailProtected": false})
		if code != http.StatusOK {
			t.Fatalf("admin create link: %d %v", code, m)
		}
		linkID := str(m, "link", "id")

		writes := []struct {
			method, path, contentType string
			body                      []byte
		}{
			{http.MethodPost, "/v1/dataroom/datarooms", "application/json", []byte(`{"name":"Other"}`)},
			{http.MethodPost, "/v1/dataroom/documents?name=x.pdf", "application/pdf", []byte("%PDF-1.7\nX\n%%EOF")},
			{http.MethodPost, "/v1/dataroom/datarooms/" + roomID + "/documents", "application/json", []byte(`{"documentId":"nope"}`)},
			{http.MethodPost, "/v1/dataroom/links", "application/json", []byte(`{"dataroomId":"` + roomID + `","name":"Other"}`)},
			{http.MethodPut, "/v1/dataroom/trust", "application/json", []byte(`{"name":"Acme"}`)},
			{http.MethodPost, "/v1/dataroom/trust/artifacts", "application/json", []byte(`{"name":"x","kind":"policy","attester":"self"}`)},
			{http.MethodPatch, "/v1/dataroom/trust/artifacts/x", "application/json", []byte(`{"name":"x"}`)},
			{http.MethodPost, "/v1/dataroom/trust/requests/x/grant", "application/json", []byte(`{}`)},
			{http.MethodPost, "/v1/dataroom/trust/requests/x/refuse", "application/json", []byte(`{}`)},
		}
		for _, w := range writes {
			for _, r := range []role{forged, sibling} {
				code, b := send(t, app, r, w.method, w.path, w.contentType, w.body)
				if code != http.StatusForbidden || !strings.Contains(string(b), cloud.Unasked) {
					t.Errorf("%s %s from %s answered %d, want the anti-forgery 403: %s", w.method, w.path, r.name, code, b)
				}
			}
			for _, r := range []role{console, server} {
				if code, b := send(t, app, r, w.method, w.path, w.contentType, w.body); code == http.StatusForbidden || code == http.StatusUnauthorized || code >= 500 {
					t.Errorf("%s %s from %s answered %d, want the room's answer: %s", w.method, w.path, r.name, code, b)
				}
			}
		}

		if code, b := send(t, app, forged, http.MethodGet, "/v1/dataroom/datarooms", "", nil); code != http.StatusOK {
			t.Errorf("a read with the admin's cookie from another site answered %d, want 200: %s", code, b)
		}
		visitor := role{name: "a visitor whose browser carries some hanzo cookie",
			headers: map[string]string{"Cookie": "hanzo_iam_token=v", "Sec-Fetch-Site": "cross-site"}}
		if code, b := sendJSON(t, app, visitor, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate",
			map[string]any{"password": "a-long-shared-phrase"}); code != http.StatusOK {
			t.Errorf("the share link refused a visitor from another site: %d %s", code, b)
		}
	})
}
