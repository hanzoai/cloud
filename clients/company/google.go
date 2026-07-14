package company

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/integrations"
)

// google.go is the import bridge: it reads an org's Google Drive folder and Google
// Sheets using the OAuth access token custodied by clients/integrations (the
// "google" provider) — the SAME token the automations google connector uses — and
// feeds them into the import path (Drive files → data room, a cap-table Sheet →
// captable). The access token is fetched fresh per call via integrations.TokenFor,
// which resolves it from KMS and fails closed when Google is not connected for the
// org.

// googleProvider is the integrations provider id whose token these reads use. It
// MUST match the provider registered in clients/integrations/google.go and the
// automations google connector name.
const googleProvider = "google"

// DriveFile is one Google Drive file.
type DriveFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
}

// GoogleReader is the read seam over Google Drive + Sheets used by the import path.
// A real implementation authenticates with the org's custodied OAuth token; tests
// substitute a fake.
type GoogleReader interface {
	ListFolder(ctx context.Context, org, folderID string) ([]DriveFile, error)
	Download(ctx context.Context, org string, f DriveFile) (data []byte, contentType string, err error)
	SheetValues(ctx context.Context, org, spreadsheetID, rangeA1 string) ([][]string, error)
}

// googleTokenFor is the seam to the integrations token custody. It is a package var
// so a test can observe/inject the (org, provider) resolution without a live KMS.
var googleTokenFor = integrations.TokenFor

// httpGoogle is the production GoogleReader: bounded HTTP calls to the Drive + Sheets
// v3/v4 REST APIs with the org's Bearer token.
type httpGoogle struct {
	client   *http.Client
	driveAPI string
	sheetAPI string
}

func newHTTPGoogle() *httpGoogle {
	return &httpGoogle{
		client:   &http.Client{Timeout: 30 * time.Second},
		driveAPI: "https://www.googleapis.com/drive/v3",
		sheetAPI: "https://sheets.googleapis.com/v4",
	}
}

// token resolves the org's Google access token from integrations' KMS custody.
func (g *httpGoogle) token(ctx context.Context, org string) (string, error) {
	tok, err := googleTokenFor(ctx, org, googleProvider, "access_token")
	if err != nil {
		return "", fmt.Errorf("google not connected for org: %w", err)
	}
	return string(tok), nil
}

// ListFolder lists the (non-trashed) files directly under a Drive folder.
func (g *httpGoogle) ListFolder(ctx context.Context, org, folderID string) ([]DriveFile, error) {
	tok, err := g.token(ctx, org)
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
	u := g.driveAPI + "/files?q=" + url.QueryEscape(q) + "&fields=" + url.QueryEscape("files(id,name,mimeType)")
	var out struct {
		Files []DriveFile `json:"files"`
	}
	if err := g.getJSON(ctx, u, tok, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// Download fetches a Drive file's bytes. Google-native docs (Docs/Sheets/Slides) are
// exported to a portable format; binary files are downloaded directly.
func (g *httpGoogle) Download(ctx context.Context, org string, f DriveFile) ([]byte, string, error) {
	tok, err := g.token(ctx, org)
	if err != nil {
		return nil, "", err
	}
	var u, ct string
	if strings.HasPrefix(f.MimeType, "application/vnd.google-apps.") {
		export := exportMime(f.MimeType)
		u = fmt.Sprintf("%s/files/%s/export?mimeType=%s", g.driveAPI, url.PathEscape(f.ID), url.QueryEscape(export))
		ct = export
	} else {
		u = fmt.Sprintf("%s/files/%s?alt=media", g.driveAPI, url.PathEscape(f.ID))
		ct = f.MimeType
	}
	data, err := g.getBytes(ctx, u, tok)
	if err != nil {
		return nil, "", err
	}
	return data, ct, nil
}

// SheetValues reads a spreadsheet range as rows of string cells.
func (g *httpGoogle) SheetValues(ctx context.Context, org, spreadsheetID, rangeA1 string) ([][]string, error) {
	tok, err := g.token(ctx, org)
	if err != nil {
		return nil, err
	}
	if rangeA1 == "" {
		rangeA1 = "A1:Z1000"
	}
	u := fmt.Sprintf("%s/spreadsheets/%s/values/%s", g.sheetAPI, url.PathEscape(spreadsheetID), url.PathEscape(rangeA1))
	var out struct {
		Values [][]any `json:"values"`
	}
	if err := g.getJSON(ctx, u, tok, &out); err != nil {
		return nil, err
	}
	rows := make([][]string, 0, len(out.Values))
	for _, r := range out.Values {
		cells := make([]string, 0, len(r))
		for _, c := range r {
			cells = append(cells, fmt.Sprintf("%v", c))
		}
		rows = append(rows, cells)
	}
	return rows, nil
}

// exportMime maps a Google-native mime type to a portable export format.
func exportMime(googleMime string) string {
	switch googleMime {
	case "application/vnd.google-apps.document":
		return "application/pdf"
	case "application/vnd.google-apps.presentation":
		return "application/pdf"
	case "application/vnd.google-apps.spreadsheet":
		return "text/csv"
	default:
		return "application/pdf"
	}
}

func (g *httpGoogle) getJSON(ctx context.Context, u, tok string, v any) error {
	b, err := g.getBytes(ctx, u, tok)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("google: decode: %w", err)
	}
	return nil
}

func (g *httpGoogle) getBytes(ctx context.Context, u, tok string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxImportBytes))
	if err != nil {
		return nil, fmt.Errorf("google: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("google http %d: %s", resp.StatusCode, truncate(string(raw), 256))
	}
	return raw, nil
}

// maxImportBytes bounds a single Drive download / Sheets read.
const maxImportBytes = 64 << 20 // 64 MiB

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
