package company

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHTTPGoogleReader drives the production Google reader against mock Drive +
// Sheets endpoints, with the access token injected through the same seam that
// resolves it from integrations' KMS custody. It proves folder listing, native-doc
// export, binary download, and sheet-value parsing.
func TestHTTPGoogleReader(t *testing.T) {
	drive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/files") && r.URL.Query().Get("q") != "":
			_, _ = w.Write([]byte(`{"files":[{"id":"a","name":"deck.pdf","mimeType":"application/pdf"},{"id":"b","name":"notes","mimeType":"application/vnd.google-apps.document"}]}`))
		case strings.Contains(r.URL.Path, "/files/b/export"):
			if r.URL.Query().Get("mimeType") != "application/pdf" {
				t.Errorf("native doc must export as pdf, got %s", r.URL.Query().Get("mimeType"))
			}
			_, _ = w.Write([]byte("%PDF exported"))
		case strings.Contains(r.URL.Path, "/files/a") && r.URL.Query().Get("alt") == "media":
			_, _ = w.Write([]byte("%PDF binary"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer drive.Close()

	sheets := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"values":[["Name","Email"],["Ada","ada@acme.com"],["Bo","bo@acme.com"]]}`))
	}))
	defer sheets.Close()

	g := newHTTPGoogle()
	g.driveAPI, g.sheetAPI = drive.URL, sheets.URL

	// Inject the token the way integrations.TokenFor would resolve it.
	old := googleTokenFor
	googleTokenFor = func(_ context.Context, org, provider, name string) ([]byte, error) {
		if provider != "google" || name != "access_token" {
			t.Errorf("token lookup must be (google, access_token), got (%s, %s)", provider, name)
		}
		return []byte("tok-123"), nil
	}
	defer func() { googleTokenFor = old }()

	ctx := context.Background()

	files, err := g.ListFolder(ctx, "acme", "FOLDER")
	if err != nil || len(files) != 2 {
		t.Fatalf("ListFolder: %v files=%v", err, files)
	}

	// Native Google doc → exported to PDF.
	data, ct, err := g.Download(ctx, "acme", files[1])
	if err != nil || ct != "application/pdf" || !strings.Contains(string(data), "exported") {
		t.Fatalf("Download native: ct=%q data=%q err=%v", ct, data, err)
	}
	// Binary file → direct media download.
	data, _, err = g.Download(ctx, "acme", files[0])
	if err != nil || !strings.Contains(string(data), "binary") {
		t.Fatalf("Download binary: data=%q err=%v", data, err)
	}

	rows, err := g.SheetValues(ctx, "acme", "SHEET", "A1:B10")
	if err != nil || len(rows) != 3 || rows[1][0] != "Ada" {
		t.Fatalf("SheetValues: %v rows=%v", err, rows)
	}
}

// TestHTTPGoogleReaderNotConnected proves the reader surfaces an honest "not
// connected" error when the token cannot be resolved.
func TestHTTPGoogleReaderNotConnected(t *testing.T) {
	g := newHTTPGoogle()
	old := googleTokenFor
	googleTokenFor = func(context.Context, string, string, string) ([]byte, error) {
		return nil, context.Canceled
	}
	defer func() { googleTokenFor = old }()

	if _, err := g.ListFolder(context.Background(), "acme", "F"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("want not-connected error, got %v", err)
	}
}
