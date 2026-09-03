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

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
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

// TestVaryIsAddedToWhatIsAlreadyThere. Vary is a LIST, and the edge names Origin
// on it before this subsystem is reached: every CORS answer depends on Origin,
// including the one that carries no CORS header at all. Assigning the header
// drops that, and then a shared cache may hand one origin the answer computed for
// another. The middleware here is exactly what the edge does (middleware_edge.go
// appends Origin through fiber's own Vary), so this measures the composition and
// not a stand-in.
func TestVaryIsAddedToWhatIsAlreadyThere(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// In FRONT of the subsystem, where the edge sits. fiber runs middleware in
	// registration order, so this has to be installed before Mount registers what
	// it must precede.
	app.Use(zip.H(func(c *zip.Ctx) error {
		c.Fiber().Vary("Origin")
		return c.Continue()
	}))
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := useWith(app, cloud.Deps{}, newMemVFS()); err != nil {
		t.Fatalf("Use:  %v", err)
	}

	// An answer this subsystem writes for itself, so held is on the path.
	rq := httptest.NewRequest(http.MethodPost, "/v1/dataroom/documents?name=deck.pdf&numPages=1",
		strings.NewReader("%PDF-1.7\nD\n%%EOF"))
	rq.Header.Set("Content-Type", "application/pdf")
	rq.Header.Set("X-Org-Id", "acme")
	rq.Header.Set("X-User-Id", "u_acme")
	rq.Header.Set("Origin", "https://console.hanzo.ai")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d, want 200 — the rows below would measure a refusal", resp.StatusCode)
	}
	v := resp.Header.Get("Vary")
	if !strings.Contains(v, "Origin") {
		t.Errorf("Vary = %q, want Origin still named — assigning the header drops what "+
			"the edge already put there", v)
	}
	if !strings.Contains(v, "Authorization") || !strings.Contains(v, "Cookie") {
		t.Errorf("Vary = %q, want the credentials this address answers differently under", v)
	}
	t.Logf("Vary composed with the edge: %q", v)
}

// TestATypedAnswerIsNotKeptEither. The byte streams and the untyped relay write
// their own response, so a header set on that path reaches them and nothing else
// — while the typed ops answer through ops.call and ops.run, which return an Out
// and touch no response at all. /links carries the link ids, which are the whole
// of a visitor's credential on the viewer surface, so that is the answer that
// must least be kept.
//
// PAIRED with the status on every row: an answer that 404'd would carry no
// headers and prove nothing.
func TestATypedAnswerIsNotKeptEither(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	for _, path := range []string{
		"/v1/dataroom/documents",
		"/v1/dataroom/datarooms",
		"/v1/dataroom/links",
		"/v1/dataroom/trust",
	} {
		st, h := headers(t, app, http.MethodGet, path, org, nil)
		if st != http.StatusOK {
			t.Fatalf("GET %s: %d, want 200 — the row below would measure a refusal", path, st)
		}
		notKept(t, "GET "+path, h)
		t.Logf("GET %s: Cache-Control=%q Vary=%q", path, h.Get("Cache-Control"), h.Get("Vary"))
	}
}

// TestThePlatformRosterIsNotKept. The one cross-tenant read sits on its own group
// at the operator's depth, so nothing installed on the subsystem's group reaches
// it — and it answers with every org's trust centre, which is the least storable
// answer here.
func TestThePlatformRosterIsNotKept(t *testing.T) {
	app, _ := mountFlowApp(t)
	rq := httptest.NewRequest(http.MethodGet, "/v1/admin/dataroom/trust", nil)
	rq.Header.Set("X-Org-Id", "admin")
	rq.Header.Set("X-User-Id", "u_admin")
	rq.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("roster: %d, want 200 — the row below would measure a refusal", resp.StatusCode)
	}
	notKept(t, "GET /v1/admin/dataroom/trust", resp.Header)
}
