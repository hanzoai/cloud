package agentskills

// End-to-end serve tests: drive real requests through the zip/fiber router
// (app.Fiber().Test) exactly as production Serve wires it, so the assertions
// prove the real route + white-label + 404 behaviour, not a mock. Plus a golden
// integrity check over whatever catalogue is embedded (the tracked `ai` fallback
// in a plain checkout, or the full set the image build drops in) — so the sha256
// digests in index.json always match the served SKILL.md bytes.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"path"
	"testing"

	"github.com/zap-proto/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

func newApp(t *testing.T, deploymentBrand string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	if err := Mount(app, cloud.Deps{Brand: deploymentBrand}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func get(t *testing.T, app *zip.App, target, host string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("test %s: %v", target, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, resp.Header
}

type catalog struct {
	Schema  string `json:"schema"`
	Brand   string `json:"brand"`
	BaseURL string `json:"base_url"`
	Issuer  string `json:"issuer"`
	Scope   string `json:"scope"`
	Skills  []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Sha256 string `json:"sha256"`
	} `json:"skills"`
}

// TestServeMasterIndex proves the master catalogue is served with the right
// content-type, schema, and (by Host) brand.
func TestServeMasterIndex(t *testing.T) {
	app := newApp(t, "hanzo")
	status, body, hdr := get(t, app, "/.well-known/agent-skills/index.json", "api.hanzo.ai")
	if status != 200 {
		t.Fatalf("index status = %d", status)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("index content-type = %q", ct)
	}
	var doc catalog
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("index json: %v", err)
	}
	if doc.Schema != "hanzo.agent-skills/v1" || doc.Scope != "master" || doc.Brand != "hanzo" {
		t.Fatalf("index shape: %+v", doc)
	}
	if doc.BaseURL != "https://api.hanzo.ai" {
		t.Fatalf("index base_url = %q", doc.BaseURL)
	}
	if len(doc.Skills) == 0 {
		t.Fatal("index has no skills")
	}
}

// TestServeSkillDigest proves a skill document is served and its bytes match the
// sha256 the catalogue advertises (the integrity contract a consumer verifies).
func TestServeSkillDigest(t *testing.T) {
	app := newApp(t, "hanzo")
	_, body, _ := get(t, app, "/.well-known/agent-skills/index.json", "api.hanzo.ai")
	var doc catalog
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	sk := doc.Skills[0] // deterministic: skills are sorted by name
	status, md, hdr := get(t, app, "/.well-known/agent-skills/"+sk.Name+"/SKILL.md", "api.hanzo.ai")
	if status != 200 {
		t.Fatalf("skill status = %d", status)
	}
	if ct := hdr.Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Fatalf("skill content-type = %q", ct)
	}
	sum := sha256.Sum256(md)
	if got := hex.EncodeToString(sum[:]); got != sk.Sha256 {
		t.Fatalf("digest mismatch for %s: served %s, catalogue %s", sk.Name, got, sk.Sha256)
	}
}

// TestWhiteLabelByHost proves the SAME binary serves a Lux/Zoo brand by Host,
// with no cross-brand host/issuer leak in the served skill document.
func TestWhiteLabelByHost(t *testing.T) {
	app := newApp(t, "hanzo")
	cases := []struct{ host, brand, base, issuer string }{
		{"api.hanzo.ai", "hanzo", "https://api.hanzo.ai", "hanzo.id"},
		{"api.lux.network", "lux", "https://api.lux.network", "lux.id"},
		{"api.zoo.ngo", "zoo", "https://api.zoo.ngo", "zoo.id"},
	}
	others := map[string][]string{
		"hanzo": {"api.lux.network", "api.zoo.ngo", "lux.id", "zoo.id"},
		"lux":   {"api.hanzo.ai", "api.zoo.ngo", "hanzo.id", "zoo.id"},
		"zoo":   {"api.hanzo.ai", "api.lux.network", "hanzo.id", "lux.id"},
	}
	for _, tc := range cases {
		_, body, _ := get(t, app, "/.well-known/agent-skills/index.json", tc.host)
		var doc catalog
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: %v", tc.host, err)
		}
		if doc.Brand != tc.brand || doc.BaseURL != tc.base {
			t.Fatalf("%s: brand=%s base=%s want %s/%s", tc.host, doc.Brand, doc.BaseURL, tc.brand, tc.base)
		}
		_, md, _ := get(t, app, "/.well-known/agent-skills/"+doc.Skills[0].Name+"/SKILL.md", tc.host)
		text := string(md)
		if !contains(text, "api."+brandDomain(tc.brand)) {
			t.Fatalf("%s: skill missing own base host", tc.host)
		}
		for _, marker := range others[tc.brand] {
			if contains(text, marker) {
				t.Fatalf("%s: skill leaks cross-brand marker %q", tc.host, marker)
			}
		}
	}
}

// TestFallbackBrand proves an unrecognised Host degrades to the deployment brand.
func TestFallbackBrand(t *testing.T) {
	app := newApp(t, "zoo")
	_, body, _ := get(t, app, "/.well-known/agent-skills/index.json", "example.com")
	var doc catalog
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Brand != "zoo" {
		t.Fatalf("fallback brand = %s, want zoo (deployment brand)", doc.Brand)
	}
}

// TestUnknownAndTraversal proves a bogus skill id and a traversal attempt both
// 404 cleanly (never escape the embedded root).
func TestUnknownAndTraversal(t *testing.T) {
	app := newApp(t, "hanzo")
	if s, _, _ := get(t, app, "/.well-known/agent-skills/nope_nothere/SKILL.md", "api.hanzo.ai"); s != 404 {
		t.Fatalf("unknown skill status = %d, want 404", s)
	}
	// A dotted segment fails the skillID guard (fiber also never routes a slashed
	// param into `:skill`), so traversal can't reach the FS.
	if s, _, _ := get(t, app, "/.well-known/agent-skills/..%2f..%2findex.json/SKILL.md", "api.hanzo.ai"); s != 404 {
		t.Fatalf("traversal status = %d, want 404", s)
	}
}

// TestCatalogIntegrity is the golden check over the embedded bytes: every brand's
// index.json digest matches its SKILL.md, for whatever catalogue is embedded.
func TestCatalogIntegrity(t *testing.T) {
	sub, err := fs.Sub(catalogFS, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	brands, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, b := range brands {
		if !b.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(sub, path.Join(b.Name(), "index.json"))
		if err != nil {
			t.Fatalf("%s: index.json: %v", b.Name(), err)
		}
		var doc catalog
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", b.Name(), err)
		}
		if doc.Brand != b.Name() {
			t.Fatalf("%s: index brand = %s", b.Name(), doc.Brand)
		}
		for _, sk := range doc.Skills {
			md, err := fs.ReadFile(sub, path.Join(b.Name(), sk.Path))
			if err != nil {
				t.Fatalf("%s/%s: %v", b.Name(), sk.Path, err)
			}
			sum := sha256.Sum256(md)
			if got := hex.EncodeToString(sum[:]); got != sk.Sha256 {
				t.Fatalf("%s/%s digest mismatch", b.Name(), sk.Name)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no skills embedded")
	}
}

func brandDomain(brand string) string {
	switch brand {
	case "lux":
		return "lux.network"
	case "zoo":
		return "zoo.ngo"
	default:
		return "hanzo.ai"
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
