package dataroom

// What a shared cache may keep.
//
// The viewer routes are reached with NO principal: a link id is the whole of the
// caller's identity, so the URL alone is enough for a cache to key on and hand to
// the next visitor. Everything that authorises one of these answers moves — a link
// is revoked, its password changed, a viewing session closed — so a stored copy
// goes on answering under a permission that has already been taken back.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// headers drives one request and answers with its status and its response headers.
func headers(t *testing.T, app *zip.App, method, path, org string, body io.Reader) (int, http.Header) {
	t.Helper()
	rq := httptest.NewRequest(method, path, body)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header
}

// notKept asserts the answer refuses to be stored and names what the same address
// answers differently under.
func notKept(t *testing.T, what string, h http.Header) {
	t.Helper()
	cc := h.Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("%s: Cache-Control = %q, want no-store — a shared cache may hold this "+
			"and serve it after the link that authorised it is gone", what, cc)
	}
	if !strings.Contains(cc, "private") {
		t.Errorf("%s: Cache-Control = %q, want private", what, cc)
	}
	if v := h.Get("Vary"); !strings.Contains(v, "Authorization") || !strings.Contains(v, "Cookie") {
		t.Errorf("%s: Vary = %q, want the credentials this address answers differently under", what, v)
	}
}

// TestALinkVisitorsAnswerIsNotKept walks the visitor's whole path — the link's own
// metadata, the viewing session, and the document bytes — and requires every answer
// to refuse storage. PAIRED with the status: an answer that 404'd would carry no
// headers and prove nothing, so each row asserts it got through first.
func TestALinkVisitorsAnswerIsNotKept(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	code, m := jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms", org,
		map[string]any{"name": "Diligence", "description": "d"})
	if code != 200 {
		t.Fatalf("create dataroom: %d %v", code, m)
	}
	roomID := str(m, "dataroom", "id")

	code, b := req(t, app, http.MethodPost, "/v1/dataroom/documents?name=deck.pdf&numPages=1",
		org, "application/pdf", []byte("%PDF-1.7\nD\n%%EOF"))
	if code != 200 {
		t.Fatalf("upload: %d %s", code, b)
	}
	var up map[string]any
	_ = json.Unmarshal(b, &up)
	docID := str(up, "document", "id")

	if code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms/"+roomID+"/documents", org,
		map[string]any{"documentId": docID}); code != 200 {
		t.Fatalf("attach: %d %v", code, m)
	}
	code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/links", org, map[string]any{
		"dataroomId": roomID, "name": "Investor link", "allowDownload": true,
	})
	if code != 200 {
		t.Fatalf("create link: %d %v", code, m)
	}
	linkID := str(m, "link", "id")

	// The link's own metadata, read by whoever holds the id.
	st, h := headers(t, app, http.MethodGet, "/v1/dataroom/view/"+linkID, "", nil)
	if st != http.StatusOK {
		t.Fatalf("view link: %d, want 200 — the rows below would measure a refusal", st)
	}
	notKept(t, "GET /view/:linkId", h)

	// The viewing session, and then the document's bytes under it.
	code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate", "",
		map[string]any{"email": "lp@acme.com"})
	if code != 200 {
		t.Fatalf("authenticate: %d %v", code, m)
	}
	viewID, _ := m["viewId"].(string)
	docs, _ := m["documents"].([]any)
	if viewID == "" || len(docs) != 1 {
		t.Fatalf("no viewing session: %v", m)
	}
	viewDocID := str(docs[0].(map[string]any), "id")

	st, h = headers(t, app, http.MethodGet,
		"/v1/dataroom/view/"+linkID+"/document/"+viewDocID+"/file?viewId="+viewID+"&download=1", "", nil)
	if st != http.StatusOK {
		t.Fatalf("viewer download: %d, want 200 — the row below would measure a refusal", st)
	}
	notKept(t, "GET /view/:linkId/document/:documentId/file", h)

	// The owner's own read of the same document is the same material under a
	// credential, and a cache must not cross the two.
	st, h = headers(t, app, http.MethodGet, "/v1/dataroom/documents/"+docID+"/file", org, nil)
	if st != http.StatusOK {
		t.Fatalf("owner download: %d, want 200", st)
	}
	notKept(t, "GET /documents/:id/file", h)
}
