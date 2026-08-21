// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The health probe used to be a closure marshalling a map; it is a typed op
// now, and the conversion is only safe if nothing a probe reads moved. The raw
// handler answered 200 with {"service":"commerce","status":"ok"} — a map's
// keys render sorted — so the typed answer is pinned to those exact bytes, not
// to a decoded equivalent: probes parse this body, and a reordered or renamed
// field is a behavior change even when the JSON is "equal".
func TestHealth_AnswerIsByteIdentical(t *testing.T) {
	app := zip.New(zip.Config{})
	zip.Get(app, "/v1/commerce/health", health)

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/commerce/health", nil))
	if err != nil {
		t.Fatalf("GET /v1/commerce/health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d (%s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("want an application/json answer, got %q", ct)
	}
	want := `{"service":"commerce","status":"ok"}`
	if string(body) != want {
		t.Fatalf("the probe body moved:\n  got  %s\n  want %s", body, want)
	}
}

// The conversion's point: the probe is a REGISTERED op now, so the document —
// and through it the MCP tool list, the CLI and every generated SDK — knows it
// exists. The raw route it replaced appeared in none of them.
func TestHealth_IsAPublishedOp(t *testing.T) {
	app := zip.New(zip.Config{})
	zip.Get(app, "/v1/commerce/health", health)

	spec, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	if _, ok := doc.Paths["/v1/commerce/health"]["get"]; !ok {
		t.Fatalf("GET /v1/commerce/health is not in the op registry — the probe went raw again; paths: %v", doc.Paths)
	}
}
