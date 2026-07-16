package projects

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
)

// fakeAI is a deterministic AIClient: it returns a canned manifest (or error) and
// records what it was called with, so generation is tested without a live gateway.
type fakeAI struct {
	content string
	err     error
	calls   int
	gotModel,
	gotPrompt string
}

func (f *fakeAI) ChatCompletion(_ context.Context, req *cloud.ChatRequest) (*cloud.ChatResponse, error) {
	f.calls++
	f.gotModel, f.gotPrompt = req.Model, req.Prompt
	if f.err != nil {
		return nil, f.err
	}
	return &cloud.ChatResponse{Content: f.content}, nil
}

// Embed satisfies the rest of the cloud.AIClient interface; site generation only
// uses ChatCompletion, so this is an unused stub.
func (f *fakeAI) Embed(context.Context, *cloud.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

// manifest is a tiny helper to build a JSON manifest string for a fake model.
func manifest(name string, files map[string]string) string {
	var b strings.Builder
	b.WriteString(`{"name":`)
	b.WriteString(jsonStr(name))
	b.WriteString(`,"files":[`)
	first := true
	for p, c := range files {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(`{"path":`)
		b.WriteString(jsonStr(p))
		b.WriteString(`,"content":`)
		b.WriteString(jsonStr(c))
		b.WriteString("}")
	}
	b.WriteString("]}")
	return b.String()
}

func jsonStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + s + `"`
}

const htmlNoViewport = `<!doctype html><html><head><title>Hi</title></head><body><h1>Hi</h1></body></html>`
const htmlWithViewport = `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><title>Hi</title></head><body>ok</body></html>`

// TestEnsureViewport: the responsive guarantee. A document missing the viewport
// meta gets it injected right after <head>; one that already declares it (single
// or double quoted) is returned byte-for-byte unchanged; a document with no <head>
// gets the tag prepended.
func TestEnsureViewport(t *testing.T) {
	got := ensureViewport(htmlNoViewport)
	if !strings.Contains(got, viewportMeta) {
		t.Fatalf("viewport not injected: %q", got)
	}
	if i := strings.Index(strings.ToLower(got), "<head>"); i < 0 || strings.Index(got, viewportMeta) < i {
		t.Fatalf("viewport must be injected AFTER <head>: %q", got)
	}
	if strings.Count(got, viewportMeta) != 1 {
		t.Fatalf("exactly one viewport tag expected, got %d", strings.Count(got, viewportMeta))
	}

	if out := ensureViewport(htmlWithViewport); out != htmlWithViewport {
		t.Fatalf("present viewport must be left unchanged; got %q", out)
	}
	// Single-quoted viewport is recognized (not duplicated).
	sq := `<head><meta name='viewport' content='width=device-width, initial-scale=1'></head>`
	if out := ensureViewport(sq); out != sq {
		t.Fatalf("single-quoted viewport must be recognized; got %q", out)
	}
	// No <head>: tag is prepended.
	nohead := `<div>bare</div>`
	if out := ensureViewport(nohead); !strings.HasPrefix(out, viewportMeta) {
		t.Fatalf("no-head doc must get a prepended viewport; got %q", out)
	}
}

// TestGenerateSiteResponsive: generateSite parses a manifest, requires index.html,
// and GUARANTEES the viewport tag even when the model omits it — the core
// responsive-by-default property.
func TestGenerateSiteResponsive(t *testing.T) {
	ai := &fakeAI{content: manifest("Landing", map[string]string{
		"index.html": htmlNoViewport, // model forgot the viewport tag
		"style.css":  "body{margin:0}",
	})}
	name, st, err := generateSite(context.Background(), ai, "zen-coder", "a landing page")
	if err != nil {
		t.Fatalf("generateSite: %v", err)
	}
	if name != "Landing" {
		t.Fatalf("name=%q want Landing", name)
	}
	if _, ok := st.files["index.html"]; !ok {
		t.Fatal("index.html must be present")
	}
	if !strings.Contains(string(st.files["index.html"]), viewportMeta) {
		t.Fatal("responsive guarantee failed: viewport meta was not injected")
	}
	// Non-HTML files are untouched.
	if string(st.files["style.css"]) != "body{margin:0}" {
		t.Fatal("css must not be rewritten")
	}
	// The brief + guidance reached the model.
	if !strings.Contains(ai.gotPrompt, "a landing page") || !strings.Contains(ai.gotPrompt, "mobile-responsive") {
		t.Fatalf("prompt missing guidance/brief: %q", ai.gotPrompt)
	}
	if ai.gotModel != "zen-coder" {
		t.Fatalf("model=%q want zen-coder", ai.gotModel)
	}

	// When the model DOES include the viewport, it is preserved (not duplicated).
	ai2 := &fakeAI{content: manifest("Ok", map[string]string{"index.html": htmlWithViewport})}
	_, st2, err := generateSite(context.Background(), ai2, "", "x")
	if err != nil {
		t.Fatalf("generateSite2: %v", err)
	}
	if n := strings.Count(string(st2.files["index.html"]), viewportMeta); n != 1 {
		t.Fatalf("viewport count = %d, want exactly 1 (no duplication)", n)
	}
}

// TestGenerateParseRobustness: tolerant of markdown fences and prose; rejects a
// manifest without index.html and one with a traversal path; a nil AI is a clean
// (non-panicking) error.
func TestGenerateParseRobustness(t *testing.T) {
	// Markdown-fenced JSON.
	fenced := "```json\n" + manifest("F", map[string]string{"index.html": "<h1>x</h1>"}) + "\n```"
	if _, _, err := generateSite(context.Background(), &fakeAI{content: fenced}, "", "b"); err != nil {
		t.Fatalf("fenced JSON must parse: %v", err)
	}
	// Prose around the JSON, plus a '}' inside a string value (brace-balance guard).
	prose := "Sure! Here you go:\n" + manifest("P", map[string]string{"index.html": "<p>a } brace</p>"}) + "\nHope that helps."
	if _, _, err := generateSite(context.Background(), &fakeAI{content: prose}, "", "b"); err != nil {
		t.Fatalf("prose-wrapped JSON must parse: %v", err)
	}
	// Missing index.html at root.
	if _, _, err := generateSite(context.Background(), &fakeAI{content: manifest("N", map[string]string{"about.html": "x"})}, "", "b"); err == nil {
		t.Fatal("manifest without index.html must be rejected")
	}
	// Path traversal.
	if _, _, err := generateSite(context.Background(), &fakeAI{content: manifest("T", map[string]string{"index.html": "x", "../evil": "y"})}, "", "b"); err == nil {
		t.Fatal("traversal path must be rejected")
	}
	// Empty response.
	if _, _, err := generateSite(context.Background(), &fakeAI{content: "   "}, "", "b"); err == nil {
		t.Fatal("empty model response must be an error")
	}
	// Nil AI.
	if _, _, err := generateSite(context.Background(), nil, "", "b"); err == nil {
		t.Fatal("nil AI must be a clean error")
	}
}

// TestSiteFromFiles: the shared validator applied to a raw file manifest — index
// required, viewport injected into html, non-html untouched, guards enforced.
func TestSiteFromFiles(t *testing.T) {
	st, err := siteFromFiles([]genFile{
		{Path: "index.html", Content: htmlNoViewport},
		{Path: "app.js", Content: "1"},
	})
	if err != nil {
		t.Fatalf("siteFromFiles: %v", err)
	}
	if !strings.Contains(string(st.files["index.html"]), viewportMeta) {
		t.Fatal("viewport must be injected on the html file")
	}
	if string(st.files["app.js"]) != "1" {
		t.Fatal("js must be untouched")
	}
	if st.bytes != int64(len(st.files["index.html"])+1) {
		t.Fatalf("bytes miscount: %d", st.bytes)
	}

	if _, err := siteFromFiles(nil); err == nil {
		t.Fatal("empty file list must error")
	}
	if _, err := siteFromFiles([]genFile{{Path: "about.html", Content: "x"}}); err == nil {
		t.Fatal("missing index.html must error")
	}
	if _, err := siteFromFiles([]genFile{{Path: "index.html", Content: "x"}, {Path: "/etc/passwd", Content: "y"}}); err == nil {
		t.Fatal("absolute path must error")
	}
}

// TestExtractJSONObject: fenced/prose extraction, brace-balance across strings,
// and the unbalanced/absent error paths.
func TestExtractJSONObject(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                 `{"a":1}`,
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		`prefix {"a":"}"} suffix`: `{"a":"}"}`,
		`{"a":{"b":2}} trailing`:  `{"a":{"b":2}}`,
		"```\n{\"x\":\"y\"}\n```": `{"x":"y"}`,
	}
	for in, want := range cases {
		got, err := extractJSONObject(in)
		if err != nil || got != want {
			t.Fatalf("extractJSONObject(%q)=(%q,%v) want %q", in, got, err, want)
		}
	}
	if _, err := extractJSONObject("no json here"); err == nil {
		t.Fatal("absent object must error")
	}
	if _, err := extractJSONObject(`{"a":1`); err == nil {
		t.Fatal("unbalanced object must error")
	}
}

// TestResolveSlug: explicit valid accepted; explicit reserved/invalid rejected;
// empty derives from name; a name that slugifies to a reserved/empty label mints
// a fresh "site-…" slug that is always valid.
func TestResolveSlug(t *testing.T) {
	if got, err := resolveSlug("My-Site", "ignored"); err != nil || got != "my-site" {
		t.Fatalf("explicit slug: got=%q err=%v", got, err)
	}
	if _, err := resolveSlug("api", "x"); err == nil {
		t.Fatal("reserved slug must be rejected")
	}
	if _, err := resolveSlug("Bad Slug!", "x"); err == nil {
		t.Fatal("invalid explicit slug must be rejected")
	}
	if got, err := resolveSlug("", "Cool Landing Page"); err != nil || got != "cool-landing-page" {
		t.Fatalf("name-derived slug: got=%q err=%v", got, err)
	}
	// Name slugifies to a reserved label -> must mint instead of returning "api".
	got, err := resolveSlug("", "API")
	if err != nil {
		t.Fatalf("mint on reserved-name: %v", err)
	}
	if got == "api" || sites.IsReserved(got) {
		t.Fatalf("must not return a reserved slug, got %q", got)
	}
	if !strings.HasPrefix(got, "site-") || !slugRE.MatchString(got) {
		t.Fatalf("minted slug %q must be a valid site-… slug", got)
	}
	// Empty name -> minted.
	got2, err := resolveSlug("", "")
	if err != nil || !strings.HasPrefix(got2, "site-") || !slugRE.MatchString(got2) {
		t.Fatalf("empty name must mint a valid slug, got=%q err=%v", got2, err)
	}
}
