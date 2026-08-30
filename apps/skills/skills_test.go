package skills

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
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/brand"
	"github.com/zap-proto/zip"
)

func newApp(t *testing.T, deploymentBrand string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	if err := Use(app, cloud.Deps{Brand: deploymentBrand}); err != nil {
		t.Fatalf("Use:  %v", err)
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 0})
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
	MCP     struct {
		Description string `json:"description"`
		Method      string `json:"method"`
		URL         string `json:"url"`
	} `json:"mcp"`
	Skills []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Sha256 string `json:"sha256"`
	} `json:"skills"`
}

// The catalogue is deliberately the READ surface — one skill per GET, per
// product — so two halves of what an agent can do are absent from it by
// construction: every operation that is not a GET, and every tool that exists
// only for a caller (a connected connector, the org's own external MCP server,
// its functions, its agents), which is not a value any build-time projection
// can hold.
//
// Both are at one address, and an agent that read the catalogue and hit the
// read-only wall had nowhere to go. This asserts the index names it, per brand,
// on the brand's OWN host — a Hanzo URL in the Lux catalogue is the white-label
// leak [rebrand] exists to stop, and it would arrive here rather than in a
// skill's prose.
func TestTheIndexNamesTheAgentDoor(t *testing.T) {
	sub, err := fs.Sub(catalogFS, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	brands, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
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
		seen++
		if doc.MCP.URL != doc.BaseURL+"/v1/mcp" {
			t.Errorf("%s names the agent door at %q; want %q. The catalogue's read-only "+
				"surface has no onward pointer without it.", b.Name(), doc.MCP.URL, doc.BaseURL+"/v1/mcp")
		}
		if doc.MCP.Method != "POST" {
			t.Errorf("%s says the door takes %q", b.Name(), doc.MCP.Method)
		}
		// The sentence is the operation's own, lifted from the published
		// contract — so an empty one means the contract stopped describing it
		// and the generator wrote a door nobody can use.
		if strings.TrimSpace(doc.MCP.Description) == "" {
			t.Errorf("%s names the door and says nothing about it", b.Name())
		}
		if b.Name() != "hanzo" && strings.Contains(strings.ToLower(doc.MCP.Description), "hanzo") {
			t.Errorf("%s carries Hanzo prose on its agent door: %q", b.Name(), doc.MCP.Description)
		}
	}
	if seen == 0 {
		t.Fatal("no brand catalogue was read, so this test asserted nothing")
	}
}

// TestServeIndex proves the catalogue is served with the right
// content-type, schema, and (by Host) brand.
func TestServeIndex(t *testing.T) {
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
	if doc.Schema != "hanzo.agent-skills/v1" || doc.Brand != "hanzo" {
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
// with no cross-brand host/issuer leak in the served skill document — and that
// the issuer it advertises is the one the token validator actually checks.
//
// EVERY EXPECTATION IS READ FROM THE REGISTRY, never restated here. The table
// this file used to carry said zoo's issuer was zoo.id while brand.go said
// zoolabs.id, and both halves of that copy failed silently: the `issuer` field
// was declared and never asserted, and the cross-brand marker "zoo.id" was a
// string the registry emits nowhere, so it could not fire. A catalogue is the
// document an agent reads to learn where to get a token; pointing it at a host
// with no DNS record strands every Zoo agent, and pointing it at ANOTHER
// brand's IdP is the cross-brand confusion this test exists to refuse. Deriving
// from brand.* makes a disagreement unrepresentable rather than untested.
func TestWhiteLabelByHost(t *testing.T) {
	app := newApp(t, "hanzo")

	// The brands with an embedded catalogue — cloud/brand holds more (pars,
	// bootnode) than the generator emits for, and a brand with none falls back
	// at serve time rather than being handed an empty document.
	sub, err := fs.Sub(catalogFS, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatal(err)
	}
	var served []string
	for _, e := range entries {
		if e.IsDir() && brand.Registered(e.Name()) {
			served = append(served, e.Name())
		}
	}
	if len(served) == 0 {
		t.Fatal("no brand catalogue embedded")
	}

	host := func(b string) string { return brand.APIHost(b) }
	issuerHost := func(b string) string {
		return strings.TrimPrefix(brand.IssuerFor(b), "https://")
	}

	for _, b := range served {
		_, body, _ := get(t, app, "/.well-known/agent-skills/index.json", host(b))
		var doc catalog
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: %v", host(b), err)
		}
		if doc.Brand != b {
			t.Fatalf("%s: brand = %s, want %s", host(b), doc.Brand, b)
		}
		if want := "https://" + host(b); doc.BaseURL != want {
			t.Fatalf("%s: base_url = %s, want %s", host(b), doc.BaseURL, want)
		}
		// The advertised issuer IS the validated issuer, or the catalogue is
		// telling agents to authenticate somewhere the API will not accept.
		if want := brand.IssuerFor(b); doc.Issuer != want {
			t.Fatalf("%s: issuer = %s, want %s (brand.IssuerFor)", host(b), doc.Issuer, want)
		}

		_, md, _ := get(t, app, "/.well-known/agent-skills/"+doc.Skills[0].Name+"/SKILL.md", host(b))
		text := string(md)
		if !strings.Contains(text, host(b)) {
			t.Fatalf("%s: skill missing own base host", host(b))
		}
		if !strings.Contains(text, issuerHost(b)) {
			t.Fatalf("%s: skill missing own issuer %s", host(b), issuerHost(b))
		}
		for _, other := range served {
			if other == b {
				continue
			}
			for _, marker := range []string{host(other), issuerHost(other)} {
				if strings.Contains(text, marker) {
					t.Fatalf("%s: skill leaks cross-brand marker %q", host(b), marker)
				}
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

// floorSkills is the RATCHET: the catalogue may grow and may not quietly shrink.
//
// TestCatalogIntegrity above cannot see a shrink, and that is not an oversight in
// it — it quantifies over the INDEX, so when the index and the files shrink
// together every surviving entry still names a readable file with a matching
// digest and the whole suite stays green. The only lower bound was "not empty".
//
// It was 542 while the catalogue came from hanzoai/openapi's skills.py, and that
// number is the reason this ratchet exists: the generator read ANOTHER repo's
// specs, so the output was a function of a checkout this one does not pin, and a
// run against a branched one emitted 357 per brand — a deletion that arrived
// looking exactly like a routine regeneration.
//
// It is 675, and it was 696 one rebase earlier. Both numbers come from
// plugin/gen-skills reading THIS repo's own per-app specs; the drop is 92 sibling
// commits folding prefixes (HIP-0139 §7, one capability one prefix), which took
// the product count 127 -> 115. Coverage is the invariant, not the total, and it
// held across the fold: every product carrying a GET has at least one skill,
// before and after.
// The number went UP rather than down: the old input was a hand-kept spec tree
// that had drifted both ways, advertising skills for /v1/balancers and
// /v1/builds (which production 404s) while missing products this fleet serves.
// The old input was a hand-kept spec tree that had drifted both ways,
// advertising skills for /v1/balancers and /v1/builds (which production 404s)
// while missing products this fleet serves.
//
// A deliberate deletion lowers this by hand in the same commit, where a reviewer
// sees the number go down next to the reason — the same rule openapi/floor.json
// carries for the document.
//
// 675 -> 669: iam now addresses a record by its own path, and the verb spellings
// it replaced (iam_get-user, iam_get-organizations and the rest) are no longer
// routes. A skill for an address nothing serves is one an agent can pick and
// never reach, so those six went with them. Every operation they described is
// still reachable under its noun.
const floorSkills = 669

func TestTheCatalogMayNotQuietlyShrink(t *testing.T) {
	sub, err := fs.Sub(catalogFS, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	brands, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatal(err)
	}
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
		if len(doc.Skills) < floorSkills {
			t.Errorf("%s publishes %d skills, below the floor of %d. A regeneration "+
				"that loses products deletes "+
				"skills silently, because the index and the files shrink together and "+
				"every integrity check still passes. Regenerate with `make skills` and, "+
				"if the deletion is deliberate, lower floorSkills in this commit and say why.",
				b.Name(), len(doc.Skills), floorSkills)
		}
	}
}
