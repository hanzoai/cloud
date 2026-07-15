package content

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	"github.com/hanzoai/cloud/clients/metering"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// generate_test.go proves the REAL Generator end-to-end over the real framework store,
// with the two backends (AI plane + studio) httptest-stubbed:
//
//   - COPY: POST generate {SocialPost} drafts real-shaped brand copy, writes a draft doc,
//     and attributes the inference to the caller's org (the meter's key).
//   - ASSET: POST generate {Asset} submits the PROVEN Qwen-Image-Edit-2511 graph and
//     records an Asset doc with the render's provenance.
//   - Both fail-closed (503) when their backend is unconfigured/unreachable, never 5xx.
//   - Tenancy: the org is the validated principal; a studio-render billing denial is 402.

// ---- stubs ----

// recordingAI is a stub AIClient that records the last ChatRequest (so a test can assert
// the billing scope / model) and returns a canned copy reply.
type recordingAI struct {
	mu    sync.Mutex
	reply string
	last  *cloud.ChatRequest
	err   error
}

func (r *recordingAI) ChatCompletion(_ context.Context, req *cloud.ChatRequest) (*cloud.ChatResponse, error) {
	r.mu.Lock()
	r.last = req
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return &cloud.ChatResponse{Content: r.reply, PromptTokens: 20, CompletionTokens: 22, TotalTokens: 42}, nil
}

func (r *recordingAI) Embed(context.Context, *cloud.EmbedRequest) ([][]float32, error) {
	return nil, nil
}

func (r *recordingAI) lastReq() *cloud.ChatRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// studioStub is a fake ComfyUI: it captures the submitted graph and always completes a
// render with one output image.
type studioStub struct {
	mu    sync.Mutex
	graph map[string]any
}

func (s *studioStub) submitted() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.graph
}

func newStudioServer(t *testing.T, stub *studioStub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt map[string]any `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stub.mu.Lock()
		stub.graph = body.Prompt
		stub.mu.Unlock()
		_, _ = w.Write([]byte(`{"prompt_id":"pid-1","number":1,"node_errors":{}}`))
	})
	mux.HandleFunc("/history/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		_, _ = w.Write([]byte(`{"` + id + `":{"status":{"completed":true,"status_str":"success"},` +
			`"outputs":{"15":{"images":[{"filename":"content_00001_.png","subfolder":"","type":"output"}]}}}}`))
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mountWith mounts framework + content with a caller-supplied content deps (AI, Metering,
// Env). Framework gets its own real store. Cleanup unmounts both.
func mountWith(t *testing.T, deps cloud.Deps) *zip.App {
	t.Helper()
	if deps.Logger == nil {
		deps.Logger = luxlog.New("test")
	}
	if deps.Domain == "" {
		deps.Domain = "api.test"
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := framework.Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("framework.Mount: %v", err)
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("content.Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(); _ = framework.Shutdown() })
	return app
}

func install(t *testing.T, app *zip.App, org string) {
	t.Helper()
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/marketing/install", org, nil); code != http.StatusOK {
		t.Fatalf("install marketing for %s: %d %s", org, code, b)
	}
}

// ---- COPY generation ----

func TestGenerateCopyWritesDraftAndMeters(t *testing.T) {
	const org = "acme"
	ai := &recordingAI{reply: `Here you go:
` + "```json" + `
{"title":"Velvet Season","caption":"Velvet is not a summer fabric. That was the whole point.",
"excerpt":"The winter capsule, in one line.","hashtags":["velvet","fw25","atelier"]}
` + "```"}
	app := mountWith(t, cloud.Deps{AI: ai, Env: "testnet"})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype":  DocTypeSocialPost,
		"brief":    "Launch the FW25 velvet capsule.",
		"project":  "karma",
		"channels": "instagram,x",
		"voice":    "quiet luxury, dry wit",
	})
	if code != http.StatusCreated {
		t.Fatalf("generate SocialPost: %d %s", code, b)
	}
	var res GenerateResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("decode result: %v (%s)", err, b)
	}
	if res.DocType != DocTypeSocialPost || res.Name == "" || res.Status != StatusDraft {
		t.Fatalf("unexpected result: %+v", res)
	}

	// The draft is a REAL framework document with real-shaped copy.
	doc, err := framework.Get(context.Background(), org, DocTypeSocialPost, res.Name)
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	caption, _ := doc.Data["caption"].(string)
	if !strings.Contains(caption, "Velvet is not a summer fabric") {
		t.Fatalf("caption not the model's copy: %q", caption)
	}
	if !strings.Contains(caption, "#velvet") {
		t.Fatalf("hashtags not folded into caption: %q", caption)
	}
	if title, _ := doc.Data["title"].(string); title != "Velvet Season" {
		t.Fatalf("title wrong: %q", title)
	}
	if excerpt, _ := doc.Data["excerpt"].(string); excerpt == "" {
		t.Fatalf("excerpt empty")
	}
	if ch, _ := doc.Data["channels"].(string); ch != "instagram,x" {
		t.Fatalf("channels wrong: %q", ch)
	}
	if st, _ := doc.Data["status"].(string); st != StatusDraft {
		t.Fatalf("draft not born as draft: %q", st)
	}

	// Metering attribution: the inference was scoped to the caller's org + project on the
	// ChatRequest — the exact key the ONE inference gate+meter (metered_ai.go) bills.
	last := ai.lastReq()
	if last == nil {
		t.Fatal("AI plane never called")
	}
	if last.Org != org {
		t.Fatalf("inference not billed to caller org: got %q want %q", last.Org, org)
	}
	if last.Project != "karma" {
		t.Fatalf("project attribution wrong: %q", last.Project)
	}
	if last.Model != defaultCopyModel {
		t.Fatalf("copy model wrong: got %q want %q", last.Model, defaultCopyModel)
	}
	// The engineered voice + brief reached the model.
	if !strings.Contains(last.Prompt, "senior brand copy director") || !strings.Contains(last.Prompt, "FW25 velvet") {
		t.Fatalf("prompt missing voice/brief: %q", last.Prompt)
	}
}

// TestGenerateCopyFailClosedUnconfigured proves copy generation is an honest 503 (never a
// 5xx crash) when the AI plane is not wired.
func TestGenerateCopyFailClosedUnconfigured(t *testing.T) {
	const org = "acme"
	app := mountWith(t, cloud.Deps{}) // no AI
	install(t, app, org)
	if code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeSocialPost, "brief": "x",
	}); code != http.StatusServiceUnavailable {
		t.Fatalf("copy w/o AI must be 503, got %d %s", code, b)
	}
}

// ---- ASSET generation ----

func TestGenerateAssetSubmitsGraphAndRecordsAsset(t *testing.T) {
	const org = "karmabikinis"
	stub := &studioStub{}
	srv := newStudioServer(t, stub)
	t.Setenv("CONTENT_STUDIO_URL", srv.URL)

	app := mountWith(t, cloud.Deps{Env: "testnet"})
	install(t, app, org)

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset,
		"design":  "festival_fuchsia",
		"kind":    "lifestyle",
		"project": "karma",
	})
	if code != http.StatusCreated {
		t.Fatalf("generate Asset: %d %s", code, b)
	}
	var res GenerateResult
	_ = json.Unmarshal(b, &res)
	if res.DocType != DocTypeAsset || res.Name == "" || res.Status != StatusDraft {
		t.Fatalf("unexpected result: %+v", res)
	}

	// The submitted graph is the verified single-ref Qwen-Image-Edit-2511 recipe.
	assertQwenGraph(t, stub.submitted(), "festival_fuchsia.png")

	// The Asset doc records the render's provenance.
	doc, err := framework.Get(context.Background(), org, DocTypeAsset, res.Name)
	if err != nil {
		t.Fatalf("read Asset: %v", err)
	}
	want := map[string]string{
		"design": "festival_fuchsia", "kind": "lifestyle",
		"generator": assetGenerator, "workflow": assetGenerator,
		"source_prompt_id": "pid-1", "project": "karma",
	}
	for k, v := range want {
		if got, _ := doc.Data[k].(string); got != v {
			t.Errorf("Asset.%s = %q, want %q", k, got, v)
		}
	}
	if file, _ := doc.Data["file"].(string); file == "" {
		t.Errorf("Asset.file empty (no render link recorded)")
	}
	if _, ok := doc.Data["render_params"]; !ok {
		t.Errorf("Asset.render_params missing")
	}
}

// assertQwenGraph checks the API-format graph is the proven recipe: the key nodes, the
// EmptySD3LatentImage latent (NOT VAEEncode), single-reference TextEncode (image1 only),
// and the verified KSampler settings.
func assertQwenGraph(t *testing.T, g map[string]any, wantSource string) {
	t.Helper()
	if g == nil {
		t.Fatal("studio received no graph")
	}
	classes := map[string]bool{}
	for _, raw := range g {
		n, _ := raw.(map[string]any)
		if ct, _ := n["class_type"].(string); ct != "" {
			classes[ct] = true
		}
	}
	for _, need := range []string{
		"FluxKontextImageScale", "TextEncodeQwenImageEditPlus", "FluxKontextMultiReferenceLatentMethod",
		"EmptySD3LatentImage", "ModelSamplingAuraFlow", "CFGNorm", "KSampler", "VAEDecode", "SaveImage", "LoadImage",
	} {
		if !classes[need] {
			t.Errorf("graph missing node %s", need)
		}
	}
	if classes["VAEEncode"] {
		t.Error("graph must NOT VAEEncode the reference (repaints the CAD) — use EmptySD3LatentImage")
	}
	inputs := func(id string) map[string]any {
		n, _ := g[id].(map[string]any)
		in, _ := n["inputs"].(map[string]any)
		return in
	}
	// LoadImage loads the source; single-ref TextEncode has image1 and NOT image2/image3.
	if src, _ := inputs("6")["image"].(string); src != wantSource {
		t.Errorf("LoadImage.image = %q, want %q", src, wantSource)
	}
	pos := inputs("8")
	if _, ok := pos["image1"]; !ok {
		t.Error("positive TextEncode missing image1")
	}
	if _, ok := pos["image2"]; ok {
		t.Error("single-ref recipe must NOT set image2 (dual-ref collages)")
	}
	// KSampler settings: cfg 4.0, 30 steps, euler/simple.
	ks := inputs("13")
	if cfg, _ := ks["cfg"].(float64); cfg != renderCFG {
		t.Errorf("KSampler cfg = %v, want %v", ks["cfg"], renderCFG)
	}
	if steps, _ := ks["steps"].(float64); int(steps) != renderSteps {
		t.Errorf("KSampler steps = %v, want %d", ks["steps"], renderSteps)
	}
	if s, _ := ks["sampler_name"].(string); s != renderSampler {
		t.Errorf("KSampler sampler = %q, want %q", s, renderSampler)
	}
	if s, _ := ks["scheduler"].(string); s != renderSched {
		t.Errorf("KSampler scheduler = %q, want %q", s, renderSched)
	}
	// EmptySD3LatentImage dimensions.
	lat := inputs("12")
	if w, _ := lat["width"].(float64); int(w) != renderWidth {
		t.Errorf("latent width = %v, want %d", lat["width"], renderWidth)
	}
	if h, _ := lat["height"].(float64); int(h) != renderHeight {
		t.Errorf("latent height = %v, want %d", lat["height"], renderHeight)
	}
	// FluxKontextMultiReferenceLatentMethod uses index_timestep_zero on both conditionings.
	if m, _ := inputs("10")["reference_latents_method"].(string); m != renderMethod {
		t.Errorf("pos reference method = %q, want %q", m, renderMethod)
	}
	if m, _ := inputs("11")["reference_latents_method"].(string); m != renderMethod {
		t.Errorf("neg reference method = %q, want %q", m, renderMethod)
	}
}

// TestGenerateAssetFailClosedStudioDown proves an unreachable/erroring studio degrades to
// an honest 503, never a 5xx, and records no fake Asset.
func TestGenerateAssetFailClosedStudioDown(t *testing.T) {
	const org = "acme"
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "studio down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	t.Setenv("CONTENT_STUDIO_URL", down.URL)

	app := mountWith(t, cloud.Deps{})
	install(t, app, org)
	if code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "x",
	}); code != http.StatusServiceUnavailable {
		t.Fatalf("asset w/ studio down must be 503, got %d %s", code, b)
	}
}

// TestGenerateAssetBillingGate proves the studio render is gated to the brand's org: an
// out-of-funds org is refused 402 BEFORE any render (the AI plane never sees a studio
// render, so content is the sole meter).
func TestGenerateAssetBillingGate(t *testing.T) {
	const org = "brokebrand"
	bal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/billing/balance") {
			_, _ = w.Write([]byte(`{"user":"` + org + `","available":0}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(bal.Close)
	mc, err := metering.New(metering.Config{BaseURL: bal.URL})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}

	app := mountWith(t, cloud.Deps{Metering: mc})
	install(t, app, org)
	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "x", "kind": "product",
	})
	if code != http.StatusPaymentRequired {
		t.Fatalf("out-of-funds asset render must be 402, got %d %s", code, b)
	}
}

// ---- unit: copy shaping is defensive (real copy even off-contract) ----

func TestCopyShapingFallbacks(t *testing.T) {
	// Strict JSON → parsed fields, hashtags folded, leading '#' normalized.
	d := buildSocialPost(GenerateInput{DocType: DocTypeSocialPost, Channels: "x"},
		`{"title":"T","caption":"Hook line.","excerpt":"E","hashtags":["#Alpha","beta gamma"]}`)
	cap, _ := d["caption"].(string)
	if !strings.Contains(cap, "Hook line.") || !strings.Contains(cap, "#Alpha") || !strings.Contains(cap, "#betagamma") {
		t.Fatalf("caption shaping wrong: %q", cap)
	}
	if d["title"] != "T" || d["excerpt"] != "E" || d["channels"] != "x" {
		t.Fatalf("fields wrong: %+v", d)
	}

	// Non-JSON prose → the model's words still become real copy (never empty/template).
	d2 := buildSocialPost(GenerateInput{DocType: DocTypeSocialPost},
		"Just a bare sentence with no JSON at all.")
	if cap, _ := d2["caption"].(string); !strings.Contains(cap, "bare sentence") {
		t.Fatalf("prose fallback lost the copy: %q", cap)
	}
	if title, _ := d2["title"].(string); title == "" {
		t.Fatal("title must be derived when absent (it is a required field)")
	}

	// Campaign requires a title; it is derived from the brief when the model omits it.
	d3 := buildCampaign(GenerateInput{DocType: DocTypeCampaign, Brief: "A quiet winter drop."}, `{"brief":"A quiet winter drop."}`)
	if title, _ := d3["title"].(string); title == "" {
		t.Fatal("campaign title must never be empty")
	}
}

// TestExtractJSONObject covers the defensive extractor directly.
func TestExtractJSONObject(t *testing.T) {
	cases := map[string]string{
		`prefix {"a":1} suffix`:           `{"a":1}`,
		"```json\n{\"a\":{\"b\":2}}\n```": `{"a":{"b":2}}`,
		`{"s":"a } not close","x":1}`:     `{"s":"a } not close","x":1}`,
		`no object here`:                  ``,
	}
	for in, want := range cases {
		if got := extractJSONObject(in); got != want {
			t.Errorf("extractJSONObject(%q) = %q, want %q", in, got, want)
		}
	}
}
