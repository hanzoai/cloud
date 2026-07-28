package content

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// studio_render.go is the ASSET half of the Generator seam: it turns a design + kind into
// a real studio-rendered image via the PROVEN Qwen-Image-Edit-2511 ComfyUI graph, then
// records it as an Asset document. It is the ONE place the ComfyUI submit→poll→fetch
// protocol and the render graph live.
//
// The graph is the karma-verified recipe (single reference + prose, NOT dual-ref), learned
// the hard way (proofs v1→v5):
//
//	LoadImage(source) → FluxKontextImageScale          (raw ref repaints/collages; must scale)
//	UNETLoader → ModelSamplingAuraFlow(shift 3.1) → CFGNorm → KSampler.model
//	TextEncodeQwenImageEditPlus(image1=scaled ref) ×2  (positive prose, negative)
//	  each → FluxKontextMultiReferenceLatentMethod(index_timestep_zero)  (CONDITIONING patch)
//	EmptySD3LatentImage 1024x1536 → KSampler.latent    (NOT VAEEncode of the ref — that
//	                                                     repaints the CAD; empty latent
//	                                                     lets the prose direct the scene)
//	KSampler cfg 4.0 / 30 steps / euler / simple → VAEDecode → SaveImage
//
// Fail-closed: an unset studio URL, an unreachable studio, a graph the studio rejects, or
// a render that does not return in time all degrade to errNotConfigured (an honest 503),
// never a 5xx and never a fabricated Asset.

// Studio model filenames — the proven Qwen-Image-Edit-2511 stack in Studio format. These
// are the studio's on-disk names (Comfy-Org split_files); pinned to the verified recipe,
// overridable per-deployment for a model bump.
const (
	studioUNet = "qwen_image_edit_2511_bf16.safetensors"
	studioCLIP = "qwen_2.5_vl_7b_fp8_scaled.safetensors"
	studioVAE  = "qwen_image_vae.safetensors"
)

// Render constants — the verified KSampler + latent settings (karma proofs). 1024x1536 is
// the portrait fashion frame; shift 3.1 + CFGNorm + cfg 4.0/30/euler/simple is the recipe.
const (
	renderWidth    = 1024
	renderHeight   = 1536
	renderSteps    = 30
	renderCFG      = 4.0
	renderShift    = 3.1
	renderSampler  = "euler"
	renderSched    = "simple"
	renderMethod   = "index_timestep_zero"
	assetGenerator = "qwen-image-edit-2511"
)

// assetNegative kills the two things the reference must NOT leak into the render: any text
// (CAD annotations / labels / watermarks) and the CAD construction lines (art direction:
// panels flatten to one fabric color, never reproduced as visible seams).
const assetNegative = "text, watermark, logo, label, caption, typography, " +
	"cad lines, construction lines, seam lines, panel lines, sketch outline, " +
	"extra garments, deformed, distorted, low quality, blurry"

// assetScenes is the per-kind scene prose. Each is a complete photographic direction; the
// reference image carries the exact garment, the prose carries the scene. ONE map — the
// single place a kind's look is defined.
var assetScenes = map[string]string{
	"product":   "Studio product photograph on a clean white seamless background, soft even lighting, centered composition, ecommerce catalog, sharp focus, true-to-life color.",
	"ecom":      "Ecommerce catalog photograph on a clean white seamless background, soft even lighting, centered, sharp focus, true-to-life color, no props.",
	"lifestyle": "Editorial lifestyle photograph, worn by a model in natural daylight, candid elegant pose, shallow depth of field, magazine fashion photography.",
	"hover":     "Alternate ecommerce hover shot, three-quarter angle on a clean seamless background, soft studio light, catalog-consistent framing.",
	"hero":      "Cinematic hero photograph, dramatic directional light, bold editorial composition, campaign key visual, rich saturated color.",
	"thumbnail": "Clean cropped thumbnail on a neutral background, product centered, high clarity.",
}

// defaultStudioURL is the in-cluster ComfyUI service (studio listens on 8188). An operator
// overrides it with CONTENT_STUDIO_URL / STUDIO_URL; unreachable at runtime → fail-closed.
const defaultStudioURL = "http://studio:8188"

// studioURLFromEnv resolves the studio endpoint, most-specific first, defaulting to the
// in-cluster service so an in-cluster deployment renders with zero config.
func studioURLFromEnv() string {
	for _, k := range []string{"CONTENT_STUDIO_URL", "STUDIO_URL"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return defaultStudioURL
}

// studioClient speaks the ComfyUI HTTP protocol: POST /prompt, GET /history/:id, GET /view.
type studioClient struct {
	base    string
	http    *http.Client
	timeout time.Duration // total render budget (submit→completed)
	poll    time.Duration // history poll interval
}

// newStudioClient builds a client for base. A short per-call HTTP timeout bounds each
// request; the render budget (studio renders ~4 min) is bounded separately by timeout.
func newStudioClient(base string) *studioClient {
	return &studioClient{
		base:    strings.TrimRight(strings.TrimSpace(base), "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		timeout: envDurationSec("CONTENT_STUDIO_TIMEOUT_SEC", 300*time.Second),
		poll:    envDurationSec("CONTENT_STUDIO_POLL_SEC", time.Second),
	}
}

// draftAsset renders an Asset via studio and returns its field data. It BILLS the render
// to the brand's org up-front (b.Bill.Gate — the AI plane never sees a studio render, so
// content is the sole meter) and, on success, records the debit. A denial surfaces as the
// raw metering error the handler renders 402/503; a studio failure fail-closes to 503.
func (g *aiStudioGenerator) draftAsset(ctx context.Context, org string, in GenerateInput) (map[string]any, error) {
	if g.studio == nil || g.studio.base == "" {
		return nil, fmt.Errorf("content: studio not configured: %w", errNotConfigured)
	}
	source := assetSource(in)
	if source == "" {
		return nil, fmt.Errorf("content: asset needs a design or source_media: %w", errNotConfigured)
	}
	// SSRF/traversal gate: the resolved source is loaded verbatim by the studio's
	// LoadImage node, so it must be either a safe studio-local/S3-key reference or an
	// https URL on the asset-host allowlist. A hostile value is a 400 HERE — before the
	// billing gate and before the studio is ever contacted. (This also gates the
	// design-convention path <design>.png, so a traversal design slug is refused too.)
	if err := validateSource(source); err != nil {
		return nil, err
	}
	kind := assetKind(in)
	project := strings.TrimSpace(in.Project)

	// Gate the render to the brand's org BEFORE the compute (fail-closed 402 out of funds).
	// project rides the request body (in.Project), not a server-minted identity claim,
	// so it is unvalidated → a project-scoped cap stays soft (anti project-spoof).
	fee := cloud.ResourceFeeCents("CONTENT_STUDIO_FEE_CENTS", kind)
	if err := g.bill.Gate(ctx, org, project, false, "asset", fee); err != nil {
		return nil, err
	}

	prompt := assetPrompt(kind, in)
	seed := randSeed()
	graph := buildQwenEditGraph(source, prompt, assetNegative, seed, in.Design)

	promptID, err := g.studio.submit(ctx, graph)
	if err != nil {
		g.log.Warn("studio submit degraded", "org", org, "design", in.Design, "err", err)
		return nil, fmt.Errorf("content: studio submit failed: %w", errNotConfigured)
	}
	img, err := g.studio.await(ctx, promptID)
	if err != nil {
		g.log.Warn("studio render degraded", "org", org, "prompt_id", promptID, "err", err)
		return nil, fmt.Errorf("content: studio render failed: %w", errNotConfigured)
	}

	// The render happened — record the debit to the brand's org (fire-and-forget).
	// METER-ON-RENDER-SUCCESS is intentional (RED-3): the billable event is the consumed
	// GPU compute, not the CMS Asset row. persistOrLink (below) writes the rendered bytes
	// to the blob plane BEFORE Generate's framework.Ingest, so even if that later CMS
	// write fails, the paid artifact survives on the blob plane (only the bookkeeping row
	// is missing) — the org is never charged for a lost render.
	g.bill.Meter(org, project, kind, fee, "", "")

	file := g.persistOrLink(ctx, org, in.Design, img)
	data := map[string]any{
		"title":            assetTitle(in, kind),
		"kind":             kind,
		"prompt":           prompt,
		"file":             file,
		"generator":        assetGenerator,
		"workflow":         assetGenerator,
		"source_prompt_id": promptID,
		"render_params": map[string]any{
			"width": renderWidth, "height": renderHeight, "steps": renderSteps,
			"cfg": renderCFG, "shift": renderShift, "sampler": renderSampler,
			"scheduler": renderSched, "reference_method": renderMethod, "seed": seed,
		},
	}
	if d := strings.TrimSpace(in.Design); d != "" {
		data["design"] = d
	}
	if project != "" {
		data["project"] = project
	}
	if src := strings.TrimSpace(in.Source); src != "" {
		data["source_media"] = src
	}
	return data, nil
}

// persistOrLink stores the rendered bytes on the blob plane (orgs/<org>/output/...) and
// returns that key as the Attach value; when VFS is unconfigured it falls back to the
// studio /view URL so the Asset is still traceable. Never fatal — a storage miss degrades
// to the link, it does not fail the render.
func (g *aiStudioGenerator) persistOrLink(ctx context.Context, org, design string, img studioImage) string {
	viewURL := g.studio.viewURL(img)
	if g.vfs == nil || len(img.bytes) == 0 {
		return viewURL
	}
	key := fmt.Sprintf("orgs/%s/output/%s/%s", pathSeg(org), pathSeg(firstNonEmpty(design, "asset")), pathSeg(img.Filename))
	if err := g.vfs.Put(ctx, key, img.bytes); err != nil {
		g.log.Warn("asset blob store failed (linking studio view instead)", "org", org, "key", key, "err", err)
		return viewURL
	}
	return key
}

// ---- ComfyUI protocol ----

// studioImage identifies (and optionally carries the bytes of) a rendered output.
type studioImage struct {
	Filename  string
	Subfolder string
	Type      string
	bytes     []byte
}

// submit POSTs the graph to /prompt and returns the assigned prompt id. A non-2xx, a
// transport error, or per-node validation errors are all errors (fail-closed).
func (c *studioClient) submit(ctx context.Context, graph map[string]any) (string, error) {
	body, err := json.Marshal(map[string]any{"prompt": graph, "client_id": clientID()})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/prompt", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("studio /prompt: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var pr struct {
		PromptID   string         `json:"prompt_id"`
		NodeErrors map[string]any `json:"node_errors"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return "", fmt.Errorf("studio /prompt: decode: %w", err)
	}
	if len(pr.NodeErrors) > 0 {
		return "", fmt.Errorf("studio /prompt: node errors: %v", pr.NodeErrors)
	}
	if strings.TrimSpace(pr.PromptID) == "" {
		return "", fmt.Errorf("studio /prompt: no prompt_id")
	}
	return pr.PromptID, nil
}

// await polls /history/:id until the render completes (an output image appears) or the
// render budget / caller context expires. It honors ctx (a client disconnect cancels).
func (c *studioClient) await(ctx context.Context, promptID string) (studioImage, error) {
	deadline := time.Now().Add(c.timeout)
	for {
		img, done, err := c.history(ctx, promptID)
		if err != nil {
			return studioImage{}, err
		}
		if done {
			img.bytes, _ = c.view(ctx, img) // best effort; the ref (filename) is enough to record.
			return img, nil
		}
		if time.Now().After(deadline) {
			return studioImage{}, fmt.Errorf("studio render timed out after %s", c.timeout)
		}
		select {
		case <-ctx.Done():
			return studioImage{}, ctx.Err()
		case <-time.After(c.poll):
		}
	}
}

// history reads /history/:id. done is true once an output image is present. A completed
// history with no image is an error (the render finished but produced nothing).
func (c *studioClient) history(ctx context.Context, promptID string) (studioImage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return studioImage{}, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return studioImage{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return studioImage{}, false, fmt.Errorf("studio /history: status %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	var hist map[string]struct {
		Status  map[string]any `json:"status"`
		Outputs map[string]struct {
			Images []struct {
				Filename  string `json:"filename"`
				Subfolder string `json:"subfolder"`
				Type      string `json:"type"`
			} `json:"images"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &hist); err != nil {
		return studioImage{}, false, fmt.Errorf("studio /history: decode: %w", err)
	}
	entry, ok := hist[promptID]
	if !ok {
		return studioImage{}, false, nil // not queued into history yet — keep polling.
	}
	for _, out := range entry.Outputs {
		for _, im := range out.Images {
			if strings.TrimSpace(im.Filename) == "" {
				continue
			}
			t := im.Type
			if t == "" {
				t = "output"
			}
			return studioImage{Filename: im.Filename, Subfolder: im.Subfolder, Type: t}, true, nil
		}
	}
	// History present but no image: done iff the run reported completion, else still running.
	if statusDone(entry.Status) {
		return studioImage{}, false, fmt.Errorf("studio render produced no image")
	}
	return studioImage{}, false, nil
}

// view fetches the rendered bytes from /view.
func (c *studioClient) view(ctx context.Context, img studioImage) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.viewURL(img), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("studio /view: status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// viewURL is the canonical /view URL for a rendered output.
func (c *studioClient) viewURL(img studioImage) string {
	q := url.Values{"filename": {img.Filename}, "type": {firstNonEmpty(img.Type, "output")}}
	if img.Subfolder != "" {
		q.Set("subfolder", img.Subfolder)
	}
	return c.base + "/view?" + q.Encode()
}

// statusDone reports whether a ComfyUI history status marks the run complete.
func statusDone(status map[string]any) bool {
	if status == nil {
		return false
	}
	if v, ok := status["completed"].(bool); ok {
		return v
	}
	if s, ok := status["status_str"].(string); ok {
		return s == "success" || s == "error"
	}
	return false
}

// ---- graph builder ----

// buildQwenEditGraph assembles the /prompt API-format graph (flat node map keyed by id;
// each input is a literal widget value or a [nodeID, outputSlot] link). It is the verified
// single-reference recipe: EmptySD3LatentImage (NOT VAEEncode) so the prose directs the
// scene while the scaled reference carries the exact garment.
func buildQwenEditGraph(source, positive, negative string, seed int64, filenamePrefix string) map[string]any {
	node := func(class string, inputs map[string]any) map[string]any {
		return map[string]any{"class_type": class, "inputs": inputs}
	}
	// filename_prefix is written by the studio's SaveImage node — sanitize the design
	// slug through the same last-line guard as blob keys so a traversal slug (when an
	// explicit source_media bypasses the source-path validation) cannot escape the
	// studio's output dir.
	filenamePrefix = pathSeg(firstNonEmpty(strings.TrimSpace(filenamePrefix), "content"))
	return map[string]any{
		"1": node("CLIPLoader", map[string]any{"clip_name": studioCLIP, "type": "qwen_image", "device": "default"}),
		"2": node("VAELoader", map[string]any{"vae_name": studioVAE}),
		"3": node("UNETLoader", map[string]any{"unet_name": studioUNet, "weight_dtype": "default"}),
		"4": node("ModelSamplingAuraFlow", map[string]any{"model": link("3"), "shift": renderShift}),
		"5": node("CFGNorm", map[string]any{"model": link("4"), "strength": 1.0}),
		"6": node("LoadImage", map[string]any{"image": source}),
		"7": node("FluxKontextImageScale", map[string]any{"image": link("6")}),
		"8": node("TextEncodeQwenImageEditPlus", map[string]any{
			"clip": link("1"), "vae": link("2"), "image1": link("7"), "prompt": positive}),
		"9": node("TextEncodeQwenImageEditPlus", map[string]any{
			"clip": link("1"), "vae": link("2"), "image1": link("7"), "prompt": negative}),
		"10": node("FluxKontextMultiReferenceLatentMethod", map[string]any{
			"conditioning": link("8"), "reference_latents_method": renderMethod}),
		"11": node("FluxKontextMultiReferenceLatentMethod", map[string]any{
			"conditioning": link("9"), "reference_latents_method": renderMethod}),
		"12": node("EmptySD3LatentImage", map[string]any{
			"width": renderWidth, "height": renderHeight, "batch_size": 1}),
		"13": node("KSampler", map[string]any{
			"model": link("5"), "positive": link("10"), "negative": link("11"), "latent_image": link("12"),
			"seed": seed, "steps": renderSteps, "cfg": renderCFG,
			"sampler_name": renderSampler, "scheduler": renderSched, "denoise": 1.0}),
		"14": node("VAEDecode", map[string]any{"samples": link("13"), "vae": link("2")}),
		"15": node("SaveImage", map[string]any{"images": link("14"), "filename_prefix": filenamePrefix}),
	}
}

// link is a ComfyUI graph edge to a node's first output.
func link(nodeID string) []any { return []any{nodeID, 0} }

// ---- asset input shaping ----

// assetKind normalizes the requested kind to a valid Asset Select option, defaulting to
// "product" (the DocType default). An unknown kind falls back to product, never errors.
func assetKind(in GenerateInput) string {
	k := strings.ToLower(strings.TrimSpace(in.Kind))
	if _, ok := assetScenes[k]; ok {
		return k
	}
	return "product"
}

// assetSource resolves the reference image the graph loads: the explicit source_media,
// else the design's CAD by convention (<design>.png in the studio input dir).
func assetSource(in GenerateInput) string {
	if s := strings.TrimSpace(in.Source); s != "" {
		return s
	}
	if d := strings.TrimSpace(in.Design); d != "" {
		return d + ".png"
	}
	return ""
}

// srcHostEnv lets an operator widen the source_media URL allowlist (comma-separated
// hosts) with the brand's own asset host, on top of the built-in platform stores.
const srcHostEnv = "CONTENT_SOURCE_HOSTS"

// defaultSourceHosts are the platform object-store hosts a source_media URL may point
// at — the branded S3 fleet where studio outputs live. Everything else is rejected.
// These are the ONLY hostnames a URL source may name (an operator adds a brand host via
// CONTENT_SOURCE_HOSTS). Nothing internal, nothing by IP.
var defaultSourceHosts = []string{"s3.hanzo.ai", "s3.lux.cloud", "s3.zoo.network"}

// validateSource is the ONE source_media gate. The resolved source is loaded verbatim by
// the studio's LoadImage node, so anything hostile must die here (→ errInvalidSource →
// 400) before it reaches the studio pod. Two shapes are allowed, everything else denied:
//
//   - a studio-local / S3-key REFERENCE (no URL scheme): must not be absolute and must
//     carry no ".." traversal segment (an absolute or traversing path can escape the
//     studio input dir / read arbitrary files via LoadImage);
//   - an https URL whose host is a NAME on the allowlist (defaultSourceHosts +
//     CONTENT_SOURCE_HOSTS): scheme must be https (no file/gopher/http), the host must
//     not be an IP literal (blocks 169.254.x, 127.x, 10.x, 172.16-31.x, 192.168.x and
//     every IPv6 form incl. ::1 / link-local / ULA), and must exactly match an allowed
//     host (blocks cluster-internal names like *.svc.cluster.local).
func validateSource(src string) error {
	s := strings.TrimSpace(src)
	if s == "" {
		return fmt.Errorf("%w: empty", errInvalidSource)
	}
	// Backslash is never legitimate in an S3 key, a studio filename, or an https URL — its
	// only use here is parser confusion. Go's url.Parse REJECTS `\` in the authority, but a
	// downstream WHATWG/Python fetcher (the studio's URL loader) treats `\` as `/` or splits
	// userinfo on it, resolving a DIFFERENT host than we validated. Reject it up front with
	// control/whitespace so `https://s3.hanzo.ai\@evil.com/x` can never reach the studio.
	if strings.ContainsAny(s, "\x00\n\r\t \\") {
		return fmt.Errorf("%w: contains whitespace, control, or backslash characters", errInvalidSource)
	}
	// FAIL CLOSED on an unparseable source. A parse error (e.g. a bad %-escape like
	// `https://evil.com%/x`) must NOT be silently reclassified as a harmless local
	// filename — that downgrade skipped the host allowlist + IP-literal block entirely
	// (SSRF fail-open). An input the URL parser rejects is denied outright.
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("%w: unparseable source", errInvalidSource)
	}
	// A parseable scheme (http/https/file/gopher/…, and Windows-drive "C:") routes to the
	// URL rules; a bare/relative path routes to the local-reference rules.
	if u.Scheme != "" {
		return validateSourceURL(u)
	}
	return validateLocalRef(s)
}

// validateSourceURL enforces https + non-IP + allowlisted-host on a URL source.
func validateSourceURL(u *url.URL) error {
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: scheme %q not allowed (https only)", errInvalidSource, u.Scheme)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return fmt.Errorf("%w: url has no host", errInvalidSource)
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("%w: host must be a name, not an IP address", errInvalidSource)
	}
	if !sourceHostAllowed(host) {
		return fmt.Errorf("%w: host %q not on the asset-host allowlist", errInvalidSource, host)
	}
	return nil
}

// validateLocalRef enforces no-absolute + no-traversal on a scheme-less reference (a
// studio-local filename or an S3-style key such as orgs/<brand>/output/hero.png).
func validateLocalRef(s string) error {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "\\") {
		return fmt.Errorf("%w: absolute path not allowed", errInvalidSource)
	}
	for _, seg := range strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return fmt.Errorf("%w: path traversal not allowed", errInvalidSource)
		}
	}
	return nil
}

// sourceHostAllowed reports whether host exactly matches an allowed asset host.
func sourceHostAllowed(host string) bool {
	for _, h := range defaultSourceHosts {
		if host == h {
			return true
		}
	}
	if extra := strings.TrimSpace(os.Getenv(srcHostEnv)); extra != "" {
		for _, h := range strings.Split(extra, ",") {
			if host == strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(h), "."))) {
				return true
			}
		}
	}
	return false
}

// assetPrompt is the positive prose: the per-kind scene, plus the design-fidelity + art
// direction (keep the exact garment, flatten construction lines to one color), plus any
// caller brief.
func assetPrompt(kind string, in GenerateInput) string {
	scene := assetScenes[kind]
	if scene == "" {
		scene = assetScenes["product"]
	}
	var b strings.Builder
	b.WriteString(scene)
	b.WriteString(" Preserve the exact garment design, cut, and color from the reference. ")
	b.WriteString("Render every panel as one continuous fabric color — no construction lines, no seams, no text.")
	if brief := strings.TrimSpace(in.Brief); brief != "" {
		b.WriteString(" ")
		b.WriteString(brief)
	}
	return b.String()
}

// assetTitle derives the Asset title (mandatory field) from the request.
func assetTitle(in GenerateInput, kind string) string {
	if t := strings.TrimSpace(in.Title); t != "" {
		return t
	}
	if d := strings.TrimSpace(in.Design); d != "" {
		return d + " — " + kind
	}
	return "Asset — " + kind
}

// ---- small utilities ----

// clientID is a random ComfyUI client id (correlates a submit with its ws stream; we poll
// history, so it is informational). Non-fatal on RNG error → a fixed marker.
func clientID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "hanzo-content"
	}
	return "hanzo-content-" + hex.EncodeToString(b[:])
}

// randSeed is a non-negative render seed. On RNG error → 0 (deterministic, still valid).
func randSeed() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	var n int64
	for _, x := range b {
		n = n<<8 | int64(x)
	}
	if n < 0 {
		n = -n
	}
	return n
}

// pathSeg sanitizes one blob-key path segment: keep [A-Za-z0-9._-], collapse everything
// else to '_', and never allow a traversal component. The last-line guard on org/design.
func pathSeg(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// envDurationSec reads an integer-seconds env var into a Duration, defaulting to dflt.
func envDurationSec(key string, dflt time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return dflt
	}
	var secs int
	if _, err := fmt.Sscanf(v, "%d", &secs); err != nil || secs <= 0 {
		return dflt
	}
	return time.Duration(secs) * time.Second
}
