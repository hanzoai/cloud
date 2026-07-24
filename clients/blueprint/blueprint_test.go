// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package blueprint

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestEmbeddedBlueprintsValidateAndPrice: every shipped blueprint parses, yields a
// non-empty SBOM, and prices to a positive rate. This is the invariant build
// enforces at mount, asserted directly on the embed FS.
func TestEmbeddedBlueprintsValidateAndPrice(t *testing.T) {
	ids, err := catalogIDs()
	if err != nil {
		t.Fatalf("catalogIDs: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("no embedded blueprints")
	}
	for _, id := range ids {
		est, ok := EstimateTemplate(id)
		if !ok {
			t.Fatalf("blueprint %q did not estimate", id)
		}
		if len(est.SBOM) == 0 {
			t.Errorf("blueprint %q has empty SBOM", id)
		}
		if est.MicroUSDPerHour <= 0 || est.CentsPerMonth <= 0 {
			t.Errorf("blueprint %q priced non-positive: %+v", id, est)
		}
		for _, s := range est.SBOM {
			if s.Image == "" {
				t.Errorf("blueprint %q service %q has no image (SBOM entry must name an image)", id, s.Name)
			}
		}
	}
}

// TestEmbeddedExactCosts pins the hand-computed cost of each shipped blueprint, so
// a change to a fixture's sizing or the rate card is a deliberate, visible edit.
func TestEmbeddedExactCosts(t *testing.T) {
	want := map[string]int64{ // µ$/hr
		"postgres":  24000, // db declared 1.0/2.0
		"redis":     12000, // cache default 0.5/1.0
		"ghost":     33000, // web default 0.5/0.5 + db default 1.0/2.0
		"n8n":       48000, // worker default 0.5/1.0 ×2 + db default 1.0/2.0
		"wordpress": 27000, // web legacy 0.5/0.5 + db mem-only 1.0/1.0
	}
	for id, micro := range want {
		est, ok := EstimateTemplate(id)
		if !ok {
			t.Fatalf("blueprint %q missing", id)
		}
		if est.MicroUSDPerHour != micro {
			t.Errorf("%s microUSDPerHour = %d, want %d (sbom %+v)", id, est.MicroUSDPerHour, micro, est.SBOM)
		}
	}
}

// TestSeamEstimateTemplate proves the in-process seam the deploy/metering path
// uses: a known id prices; an unknown id and a traversal attempt both fail closed.
func TestSeamEstimateTemplate(t *testing.T) {
	if _, ok := EstimateTemplate("postgres"); !ok {
		t.Fatal("known blueprint must estimate")
	}
	if _, ok := EstimateTemplate("does-not-exist"); ok {
		t.Fatal("unknown blueprint must not estimate")
	}
	if _, ok := EstimateTemplate("../estimate"); ok {
		t.Fatal("path traversal id must be rejected")
	}
}

// ── HTTP surface (mirrors clients/sbom's in-process zip harness) ─────────────

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func get(t *testing.T, app *zip.App, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestHTTPSurface(t *testing.T) {
	app := mountApp(t)

	if code, _ := get(t, app, "/v1/blueprint/health"); code != http.StatusOK {
		t.Fatalf("health want 200, got %d", code)
	}

	// index
	code, body := get(t, app, "/v1/blueprint")
	if code != http.StatusOK {
		t.Fatalf("index want 200, got %d", code)
	}
	var index struct {
		Data []struct {
			TemplateID    string `json:"templateId"`
			Services      int    `json:"services"`
			CentsPerMonth int64  `json:"estCentsPerMonth"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		t.Fatalf("index decode: %v", err)
	}
	ids, _ := catalogIDs()
	if len(index.Data) != len(ids) {
		t.Fatalf("index len = %d, want %d", len(index.Data), len(ids))
	}

	// one template
	code, body = get(t, app, "/v1/blueprint/sbom?template=postgres")
	if code != http.StatusOK {
		t.Fatalf("sbom?template=postgres want 200, got %d", code)
	}
	var est Estimate
	if err := json.Unmarshal(body, &est); err != nil {
		t.Fatalf("estimate decode: %v", err)
	}
	if est.TemplateID != "postgres" || est.MicroUSDPerHour != 24000 || len(est.SBOM) != 1 {
		t.Fatalf("postgres estimate wrong: %+v", est)
	}
	if est.SBOM[0].Image != "postgres:16-alpine" {
		t.Fatalf("postgres SBOM image = %q", est.SBOM[0].Image)
	}

	// batch
	code, body = get(t, app, "/v1/blueprint/sbom")
	if code != http.StatusOK {
		t.Fatalf("batch want 200, got %d", code)
	}
	var batch struct {
		Data []Estimate `json:"data"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("batch decode: %v", err)
	}
	if len(batch.Data) != len(ids) {
		t.Fatalf("batch len = %d, want %d", len(batch.Data), len(ids))
	}

	// unknown → 404
	if code, _ := get(t, app, "/v1/blueprint/sbom?template=nope"); code != http.StatusNotFound {
		t.Fatalf("unknown template want 404, got %d", code)
	}
}
