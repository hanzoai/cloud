package dataroom

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/hanzoai/cloud/internal/devmaster"
	"github.com/zap-proto/zip"
)

// head drives one request and returns the status, the response headers and the
// body. The flow helpers drop headers, and the headers ARE the property under test
// here: what a stored file is served AS decides whether it runs.
func head(t *testing.T, app *zip.App, method, path, org string) (int, http.Header, []byte) {
	t.Helper()
	rq := httptest.NewRequest(method, path, nil)
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// TestStoredFileIsServedFromItsBytes is the property: the type a document is served
// under comes from the document, not from whoever uploaded it. api.hanzo.ai is the
// same origin as every console, so a file served as markup would run beside them.
func TestStoredFileIsServedFromItsBytes(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	for _, tc := range []struct {
		name       string
		uploadAs   string // the type the uploader claims
		body       string
		wantType   string
		wantAttach bool
	}{
		{"markup claiming markup", "text/html", "<script>alert(document.cookie)</script>", "application/octet-stream", true},
		{"markup claiming pdf", "application/pdf", "<html><body><script>alert(1)</script></body></html>", "application/octet-stream", true},
		{"svg", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, "application/octet-stream", true},
		{"xhtml", "application/xhtml+xml", `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"/>`, "application/octet-stream", true},
		{"javascript", "text/javascript", "alert(1)", "application/octet-stream", true},
		{"pdf", "application/pdf", "%PDF-1.7\n1 0 obj\n%%EOF", "application/pdf", false},
		{"pdf carrying markup", "text/html", "%PDF-1.7\n<script>alert(1)</script>\n%%EOF", "application/pdf", false},
		{"png claiming markup", "text/html", "\x89PNG\r\n\x1a\n\x00\x00\x00\x0d", "image/png", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := upload(t, app, org, "x", tc.uploadAs, []byte(tc.body))
			code, h, got := head(t, app, http.MethodGet, "/v1/dataroom/documents/"+id+"/file", org)
			if code != http.StatusOK {
				t.Fatalf("download want 200, got %d (%s)", code, got)
			}
			if ct := h.Get("Content-Type"); ct != tc.wantType {
				t.Fatalf("Content-Type = %q, want %q", ct, tc.wantType)
			}
			if h.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", h.Get("X-Content-Type-Options"))
			}
			cd := h.Get("Content-Disposition")
			if tc.wantAttach && !strings.HasPrefix(cd, "attachment") {
				t.Fatalf("Content-Disposition = %q, want an attachment", cd)
			}
			if !tc.wantAttach && cd != "" {
				t.Fatalf("Content-Disposition = %q, want none for an inline type", cd)
			}
			if string(got) != tc.body {
				t.Fatalf("bytes changed on the way out")
			}
		})
	}
}

// TestViewerFileIsServedFromItsBytes proves the public viewer path — the one a
// stranger reaches with only a link id — carries the same property. It shares one
// function with the owner path, and this holds that sharing to its promise.
func TestViewerFileIsServedFromItsBytes(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	code, m := jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms", org, map[string]any{"name": "DR"})
	roomID := str(m, "dataroom", "id")
	if code != http.StatusOK || roomID == "" {
		t.Fatalf("create room: %d %v", code, m)
	}
	docID := upload(t, app, org, "page.html", "text/html", []byte("<script>alert(document.cookie)</script>"))
	if code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/datarooms/"+roomID+"/documents", org,
		map[string]any{"documentId": docID}); code != http.StatusOK {
		t.Fatalf("addDocument: %d %v", code, m)
	}
	code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/links", org,
		map[string]any{"dataroomId": roomID, "name": "l", "allowDownload": true})
	linkID := str(m, "link", "id")
	if code != http.StatusOK || linkID == "" {
		t.Fatalf("create link: %d %v", code, m)
	}
	code, m = jsonReq(t, app, http.MethodPost, "/v1/dataroom/view/"+linkID+"/authenticate", "", map[string]any{"email": "v@acme.com"})
	viewID := str(m, "viewId")
	if code != http.StatusOK || viewID == "" {
		t.Fatalf("authenticate: %d %v", code, m)
	}

	code, h, b := head(t, app, http.MethodGet,
		"/v1/dataroom/view/"+linkID+"/document/"+docID+"/file?viewId="+viewID, "")
	if code != http.StatusOK {
		t.Fatalf("viewer file want 200, got %d (%s)", code, b)
	}
	if ct := h.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("viewer Content-Type = %q, want application/octet-stream", ct)
	}
	if !strings.HasPrefix(h.Get("Content-Disposition"), "attachment") {
		t.Fatalf("viewer Content-Disposition = %q, want an attachment", h.Get("Content-Disposition"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("viewer nosniff missing")
	}
}

// TestDispositionCarriesNoSecondHeader proves a document name cannot become a
// header of its own, cannot walk out of the download directory, and survives being
// a name a person would actually type.
func TestDispositionCarriesNoSecondHeader(t *testing.T) {
	for name, want := range map[string]string{
		"deck.pdf":         "attachment; filename=deck.pdf",
		"a\r\nX-Evil: 1":   `attachment; filename*=utf-8''a%0D%0AX-Evil%3A%201`,
		"../../etc/passwd": "attachment; filename=passwd",
		"":                 "attachment",
		"/":                "attachment",
		"naïve déck.pdf":   `attachment; filename*=utf-8''na%C3%AFve%20d%C3%A9ck.pdf`,
	} {
		t.Run(name, func(t *testing.T) {
			got := disposition(name)
			if got != want {
				t.Fatalf("disposition(%q) = %q, want %q", name, got, want)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("disposition(%q) put a line break on the wire: %q", name, got)
			}
		})
	}
}
