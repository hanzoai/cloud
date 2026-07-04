package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Google connectors (Sheets + Drive). Google is NOT yet a provider in
// clients/integrations, so RunContext.Token — bound to (in.Owner, "google_sheets"
// / "google_drive") — resolves an UNKNOWN provider and fails closed. Every action
// therefore returns an honest "google not connected" until a google provider lands
// in integrations. The API code below is complete; it simply never runs in Phase 1.

const googleTokenSecret = "access_token"

var (
	googleSheetsAPIBase = "https://sheets.googleapis.com/v4"
	googleDriveAPIBase  = "https://www.googleapis.com/drive/v3"
)

var googleClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Connector{
		Name:        "google_sheets",
		DisplayName: "Google Sheets",
		AuthType:    "oauth2",
		AuthReq:     true,
		Actions: map[string]*Action{
			"append_row": {
				Name:        "append_row",
				DisplayName: "Append Row",
				Description: "Append a row of values to a spreadsheet range.",
				Props: []PropSpec{
					{Name: "spreadsheetId", Type: "string", Required: true, Description: "Target spreadsheet id."},
					{Name: "range", Type: "string", Required: true, Description: "A1 range, e.g. Sheet1!A:C."},
					{Name: "values", Type: "array", Required: true, Description: "Row cell values."},
				},
				Run: runSheetsAppendRow,
			},
		},
	})
	register(&Connector{
		Name:        "google_drive",
		DisplayName: "Google Drive",
		AuthType:    "oauth2",
		AuthReq:     true,
		Actions: map[string]*Action{
			"list_files": {
				Name:        "list_files",
				DisplayName: "List Files",
				Description: "List files matching an optional query.",
				Props: []PropSpec{
					{Name: "query", Type: "string", Description: "Drive query, e.g. name contains 'report'."},
				},
				Run: runDriveListFiles,
			},
		},
	})
}

func runSheetsAppendRow(ctx context.Context, rc RunContext) (any, error) {
	tok, err := rc.Token(googleTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("google not connected: %w", err)
	}
	sheet := strInput(rc.Input, "spreadsheetId")
	rng := strInput(rc.Input, "range")
	if sheet == "" || rng == "" {
		return nil, fmt.Errorf("sheets append_row: spreadsheetId and range are required")
	}
	values, ok := rc.Input["values"].([]any)
	if !ok {
		return nil, fmt.Errorf("sheets append_row: values must be an array")
	}
	payload, err := jsonBytes(map[string]any{"values": [][]any{values}})
	if err != nil {
		return nil, fmt.Errorf("sheets append_row: encode: %w", err)
	}
	u := fmt.Sprintf("%s/spreadsheets/%s/values/%s:append?valueInputOption=USER_ENTERED",
		googleSheetsAPIBase, url.PathEscape(sheet), url.PathEscape(rng))
	return googleDo(ctx, http.MethodPost, u, string(tok), payload)
}

func runDriveListFiles(ctx context.Context, rc RunContext) (any, error) {
	tok, err := rc.Token(googleTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("google not connected: %w", err)
	}
	u := googleDriveAPIBase + "/files"
	if q := strInput(rc.Input, "query"); q != "" {
		u += "?q=" + url.QueryEscape(q)
	}
	return googleDo(ctx, http.MethodGet, u, string(tok), nil)
}

// googleDo issues one Google API call. Bounded read, explicit non-2xx handling.
func googleDo(ctx context.Context, method, u, token string, body io.Reader) (any, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := googleClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("google: read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("google http %d", resp.StatusCode)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("google: decode: %w", err)
	}
	return out, nil
}
