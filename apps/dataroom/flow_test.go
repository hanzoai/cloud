package dataroom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// memVFS is an in-memory types.VFSClient standing in for the S3/SeaweedFS data
// plane. The leaf reaches storage ONLY through the deps.VFS seam, so this drives
// the exact production path (Put on upload, Get on download) without a live S3.
type memVFS struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemVFS() *memVFS { return &memVFS{m: map[string][]byte{}} }
func (v *memVFS) Put(_ context.Context, key string, payload []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	v.m[key] = cp
	return nil
}
func (v *memVFS) Get(_ context.Context, key string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.m[key]
	if !ok {
		return nil, fmt.Errorf("memVFS: no such key %q", key)
	}
	return b, nil
}
func (v *memVFS) Delete(_ context.Context, key string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, key)
	return nil
}

func mountFlowApp(t *testing.T) (*zip.App, *memVFS) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	vfs := newMemVFS()
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: vfs}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app, vfs
}

// req drives one request through the mounted routes. org (when non-empty) sets a
// validated principal; contentType + raw control the body (JSON or file bytes).
func req(t *testing.T, app *zip.App, method, path, org, contentType string, raw []byte) (int, []byte) {
	t.Helper()
	var r io.Reader
	if raw != nil {
		r = bytes.NewReader(raw)
	}
	rq := httptest.NewRequest(method, path, r)
	if contentType != "" {
		rq.Header.Set("Content-Type", contentType)
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func jsonReq(t *testing.T, app *zip.App, method, path, org string, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	ct := ""
	if body != nil {
		raw, _ = json.Marshal(body)
		ct = "application/json"
	}
	code, b := req(t, app, method, path, org, ct, raw)
	var m map[string]any
	if len(b) > 0 {
		_ = json.Unmarshal(b, &m)
	}
	return code, m
}

func str(m map[string]any, keys ...string) string {
	cur := any(m)
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	s, _ := cur.(string)
	return s
}

// TestDataroomFullFlow proves the COMPLETE create-dataroom → upload-document →
// share-link → view-with-analytics flow, all Base-backed (per-tenant SQLite) with
// document bytes on the object-storage seam. The final assertion is the task's
// gate: a per-page view is recorded and surfaces in analytics.
func TestDataroomFullFlow(t *testing.T) {
	app, vfs := mountFlowApp(t)
	const org = "acme"

	// health is always on, no auth.
	if code, _ := req(t, app, http.MethodGet, "/v1/dataroom/health", "", "", nil); code != 200 {
		t.Fatalf("health want 200, got %d", code)
	}
	// admin routes require a validated principal.
	if code, _ := jsonReq(t, app, http.MethodGet, "/v1/dataroom/datarooms", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal list want 403, got %d", code)
	}

	// 1) create a data room.
	code, m := jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms", org,
		map[string]any{"name": "Series A Data Room", "description": "diligence"})
	if code != 200 {
		t.Fatalf("create dataroom want 200, got %d (%v)", code, m)
	}
	roomID := str(m, "dataroom", "id")
	if roomID == "" {
		t.Fatalf("no dataroom id: %v", m)
	}

	// 2) upload a document — raw bytes go through the object-storage seam.
	pdf := []byte("%PDF-1.7\nHANZO DATAROOM TEST DOCUMENT\n%%EOF")
	code, b := req(t, app, http.MethodPost, "/v1/dataroom/documents?name=deck.pdf&numPages=3", org, "application/pdf", pdf)
	if code != 200 {
		t.Fatalf("upload document want 200, got %d (%s)", code, b)
	}
	var up map[string]any
	_ = json.Unmarshal(b, &up)
	docID := str(up, "document", "id")
	fileKey := str(up, "document", "fileKey")
	if docID == "" || fileKey == "" {
		t.Fatalf("no document id/fileKey: %s", b)
	}
	if _, ok := vfs.m[fileKey]; !ok {
		t.Fatalf("document bytes not stored on VFS seam under %q", fileKey)
	}

	// 3) attach the document to the data room.
	if code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms/"+roomID+"/documents", org,
		map[string]any{"documentId": docID}); code != 200 {
		t.Fatalf("addDocument want 200, got %d (%v)", code, m)
	}

	// 4) create a shareable link: email-gated, allow-listed, password-protected.
	code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/links", org, map[string]any{
		"dataroomId":     roomID,
		"name":           "Investor link",
		"emailProtected": true,
		"allowList":      []string{"@acme.com"},
		"password":       "s3cret-pass",
		"allowDownload":  true,
	})
	if code != 200 {
		t.Fatalf("create link want 200, got %d (%v)", code, m)
	}
	linkID := str(m, "link", "id")
	if linkID == "" {
		t.Fatalf("no link id: %v", m)
	}
	if str(m, "link", "hasPassword") == "" && m["link"].(map[string]any)["hasPassword"] != true {
		t.Fatalf("link should report hasPassword=true, got %v", m["link"])
	}

	// 5) open the link as a public viewer (no principal) — see the gate metadata.
	code, m = jsonReq(t, app, http.MethodGet, "/v1/dataroom/view/"+linkID, "", nil)
	if code != 200 {
		t.Fatalf("view link want 200, got %d (%v)", code, m)
	}
	if m["link"].(map[string]any)["emailProtected"] != true || m["link"].(map[string]any)["hasPassword"] != true {
		t.Fatalf("viewer metadata should show email+password gates, got %v", m["link"])
	}

	// 5a) access controls are enforced: wrong password, disallowed email.
	if code, _ = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate", "",
		map[string]any{"email": "lp@acme.com", "password": "wrong"}); code != http.StatusUnauthorized {
		t.Fatalf("wrong password want 401, got %d", code)
	}
	if code, _ = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate", "",
		map[string]any{"email": "outsider@evil.com", "password": "s3cret-pass"}); code != http.StatusForbidden {
		t.Fatalf("disallowed email want 403, got %d", code)
	}

	// 6) authenticate as an allowed viewer → a View (analytics session) is created.
	code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate", "",
		map[string]any{"email": "lp@acme.com", "password": "s3cret-pass"})
	if code != 200 {
		t.Fatalf("authenticate want 200, got %d (%v)", code, m)
	}
	viewID, _ := m["viewId"].(string)
	if viewID == "" {
		t.Fatalf("no viewId: %v", m)
	}
	docs, _ := m["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("viewer should see 1 document, got %v", m["documents"])
	}
	viewDocID := str(docs[0].(map[string]any), "id")

	// 7) record per-page views (the page-by-page analytics): page 1 twice, page 2 once.
	for _, pv := range []map[string]any{
		{"viewId": viewID, "documentId": viewDocID, "pageNumber": 1, "duration": 4200},
		{"viewId": viewID, "documentId": viewDocID, "pageNumber": 1, "duration": 1800},
		{"viewId": viewID, "documentId": viewDocID, "pageNumber": 2, "duration": 900},
	} {
		if code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/pageview", "", pv); code != 200 {
			t.Fatalf("pageview want 200, got %d (%v)", code, m)
		}
	}

	// 8) analytics surfaces the per-page views — the task's completion gate.
	code, b = req(t, app, http.MethodGet, "/v1/dataroom/analytics/link/"+linkID, org, "", nil)
	if code != 200 {
		t.Fatalf("analytics want 200, got %d (%s)", code, b)
	}
	t.Logf("PER-PAGE ANALYTICS: %s", b)
	var an struct {
		TotalViews     int `json:"totalViews"`
		TotalPageViews int `json:"totalPageViews"`
		Pages          []struct {
			PageNumber    int `json:"pageNumber"`
			Views         int `json:"views"`
			TotalDuration int `json:"totalDuration"`
			AvgDuration   int `json:"avgDuration"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(b, &an); err != nil {
		t.Fatalf("analytics json: %v", err)
	}
	if an.TotalViews != 1 {
		t.Fatalf("want 1 view, got %d", an.TotalViews)
	}
	if an.TotalPageViews != 3 {
		t.Fatalf("want 3 page views, got %d", an.TotalPageViews)
	}
	if len(an.Pages) != 2 {
		t.Fatalf("want 2 distinct pages, got %d", len(an.Pages))
	}
	p1 := an.Pages[0]
	if p1.PageNumber != 1 || p1.Views != 2 || p1.TotalDuration != 6000 || p1.AvgDuration != 3000 {
		t.Fatalf("page 1 analytics wrong: %+v", p1)
	}

	// 9) the viewer downloads the document (allowDownload=true) — bytes round-trip.
	code, dl := req(t, app, http.MethodGet,
		"/v1/dataroom/view/"+linkID+"/document/"+viewDocID+"/file?viewId="+viewID+"&download=1", "", "", nil)
	if code != 200 {
		t.Fatalf("viewer download want 200, got %d (%s)", code, dl)
	}
	if !bytes.Equal(dl, pdf) {
		t.Fatalf("downloaded bytes differ from upload")
	}

	// 10) tenant isolation: another org sees none of acme's data.
	if code, m = jsonReq(t, app, http.MethodGet, "/v1/dataroom/datarooms", "other", nil); code != 200 {
		t.Fatalf("other-org list want 200, got %d", code)
	}
	if rooms, _ := m["datarooms"].([]any); len(rooms) != 0 {
		t.Fatalf("other org must see zero datarooms, got %v", m["datarooms"])
	}
}

// TestDataroomDenyListEnforced proves M6: a link's deny_list is ENFORCED and WINS
// over the allow_list — an address admitted by the allow list is still refused if
// it is on the deny list.
func TestDataroomDenyListEnforced(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	code, m := jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms", org, map[string]any{"name": "DR"})
	roomID := str(m, "dataroom", "id")
	if code != 200 || roomID == "" {
		t.Fatalf("create room: %d %v", code, m)
	}
	// Allow the whole @acme.com domain, but deny one specific address on it.
	code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/links", org, map[string]any{
		"dataroomId":     roomID,
		"emailProtected": true,
		"allowList":      []string{"@acme.com"},
		"denyList":       []string{"banned@acme.com"},
	})
	linkID := str(m, "link", "id")
	if code != 200 || linkID == "" {
		t.Fatalf("create link: %d %v", code, m)
	}
	// The deny list is surfaced back to the admin (write path is now readable).
	if dl, _ := m["link"].(map[string]any)["denyList"].([]any); len(dl) != 1 {
		t.Fatalf("denyList not surfaced on the link: %v", m["link"])
	}

	// An allowed, non-denied address authenticates.
	if code, _ = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate", "",
		map[string]any{"email": "ok@acme.com"}); code != 200 {
		t.Fatalf("allowed viewer want 200, got %d", code)
	}
	// The denied address is on the allow list (by domain) yet MUST be refused —
	// deny wins over allow.
	if code, _ = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate", "",
		map[string]any{"email": "banned@acme.com"}); code != http.StatusForbidden {
		t.Fatalf("denied viewer want 403 (deny wins), got %d", code)
	}
}
