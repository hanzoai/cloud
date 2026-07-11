package content

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
)

// generate.go is the agentic content-creation seam: given a brand brief + type, draft
// content into the CMS as a DRAFT document. The heart of the loop's first step.
//
// Decomplected into two concerns:
//
//   - Generator (the seam) turns a GenerateInput into the field data for a draft. The
//     REAL implementation (aiStudioGenerator, wired at Mount via newGenerator) has two
//     orthogonal modes behind the one seam:
//       * COPY (Campaign, SocialPost) — drafts brand copy on the platform AI plane
//         (deps.AI.ChatCompletion, model zen5). The billing scope (Org/Project) rides
//         the ChatRequest, so the ONE inference gate+meter (cloud/metered_ai.go) that
//         wraps deps.AI authorizes the brand's balance BEFORE the call and debits the
//         EXACT token cost AFTER — content never re-bills copy (double-billing the ONE
//         inference meter is a bug, and calling AI with an empty Org would be the very
//         "unattributed inference" that meter forbids). See draftCopy.
//       * ASSET — renders a studio image (the Qwen-Image-Edit-2511 ComfyUI graph) which
//         the AI plane never sees, so content is the SOLE meter: b.Bill.Gate before the
//         render (fail-closed 402) + b.Bill.MeterUsage after. See studio_render.go.
//   - Generate (this file) is the STABLE write path: it validates the type, requires the
//     marketing module be installed, forces status=draft, and persists via
//     framework.Ingest — the SAME validate + lifecycle-hook pipeline an HTTP create runs.
//     It never changes when the Generator's model or provider changes.
//
// When a mode's dependency is not configured (nil/disabled deps.AI for copy, empty or
// unreachable studio for assets), the Generator fail-closes with errNotConfigured — the
// handler maps it to an honest 503 instead of shipping fake copy or a fake render.

// GenerateInput is the request to draft a piece of content. DocType selects the target
// marketing type; the rest is generation context. It is the wire body of
// POST /v1/content/generate and the input of the content_generate automation action.
type GenerateInput struct {
	DocType  string `json:"doctype"`                // Campaign | SocialPost | Asset
	Title    string `json:"title,omitempty"`        // optional explicit title
	Brief    string `json:"brief,omitempty"`        // the brief/goal driving copy generation
	Product  string `json:"product,omitempty"`      // commerce product handle (copy context)
	Design   string `json:"design,omitempty"`       // studio design slug (asset source)
	Channels string `json:"channels,omitempty"`     // target channels (SocialPost)
	Project  string `json:"project,omitempty"`      // brand/site sub-scope (billing + tenancy axis)
	Voice    string `json:"voice,omitempty"`        // brand-voice guidance for the copy director
	Tone     string `json:"tone,omitempty"`         // tone override for a single draft
	Kind     string `json:"kind,omitempty"`         // asset kind: ecom|product|lifestyle|hover|hero
	Source   string `json:"source_media,omitempty"` // asset source image (design CAD/photo)
	Model    string `json:"model,omitempty"`        // optional zen model override (copy)
}

// GenerateResult is the created draft's identity.
type GenerateResult struct {
	DocType string `json:"doctype"`
	Name    string `json:"name"`
	Status  string `json:"status"`
}

// Generator drafts content field data for a marketing DocType. Implementations are
// the ONLY place a model/provider is chosen; everything else in the loop is provider-
// agnostic. A Generator returns the field data map for the draft (copy, media refs,
// derived fields); Generate stamps status=draft and persists it.
type Generator interface {
	Draft(ctx context.Context, org string, in GenerateInput) (map[string]any, error)
}

// notConfiguredGenerator is the fail-closed default when NO generation backend can be
// built at all. It never fabricates content — it returns an honest error the handler
// maps to 503, so an un-provisioned deployment degrades cleanly instead of shipping
// fake copy. (In practice newGenerator always returns the real aiStudioGenerator, which
// fail-closes per-mode; this remains the type-level zero value for the seam.)
type notConfiguredGenerator struct{}

func (notConfiguredGenerator) Draft(context.Context, string, GenerateInput) (map[string]any, error) {
	return nil, errNotConfigured
}

// defaultCopyModel is the platform AI-plane model the copy generator drafts with when a
// request pins none and no operator override is set. zen5 is the Hanzo AI plane's
// flagship editorial model (routed by the gateway; content never talks to a provider
// directly). Override per-deployment with CONTENT_COPY_MODEL, per-request with `model`.
const defaultCopyModel = "zen5"

// newGenerator builds the REAL Generator from the shared deps at Mount. It captures the
// platform AI plane (deps.AI — already the metering-wrapped inference client), the blob
// plane (deps.VFS, for persisting rendered assets), the per-org resource meter (b.Bill,
// for studio render billing), and the studio endpoint (env, in-cluster default). It is
// the ONE place the copy model + studio URL policy is resolved.
func newGenerator(deps cloud.Deps, b cloud.Base) Generator {
	return &aiStudioGenerator{
		ai:     deps.AI,
		vfs:    deps.VFS,
		bill:   b.Bill,
		studio: newStudioClient(studioURLFromEnv()),
		model:  strings.TrimSpace(os.Getenv("CONTENT_COPY_MODEL")),
		env:    b.Env,
		log:    b.Log,
	}
}

// aiStudioGenerator is the real seam: zen5 copy for Campaign/SocialPost, studio assets
// for Asset. It holds only edges (no store of its own) — content stays a stateless
// orchestrator. Every dependency is optional at construction; a missing one fail-closes
// only the mode that needs it.
type aiStudioGenerator struct {
	ai     cloud.AIClient       // platform AI plane (metered) — copy
	vfs    cloud.VFSClient      // blob plane — persist rendered assets (optional)
	bill   *cloud.ResourceMeter // per-org meter — studio render billing
	studio *studioClient        // ComfyUI graph submit/poll/fetch — assets
	model  string               // operator copy-model override ("" → defaultCopyModel)
	env    string               // deployment env (attribution)
	log    luxlog.Logger
}

// Draft dispatches on the target DocType: Asset → studio render, everything else → zen5
// copy. The mode split is the ONE place the two generation planes diverge.
func (g *aiStudioGenerator) Draft(ctx context.Context, org string, in GenerateInput) (map[string]any, error) {
	if in.DocType == DocTypeAsset {
		return g.draftAsset(ctx, org, in) // studio_render.go
	}
	return g.draftCopy(ctx, org, in)
}

// draftCopy drafts editorial brand copy on the platform AI plane. The billing scope
// (Org/Project) rides the ChatRequest so the ONE inference gate+meter that wraps deps.AI
// authorizes the brand's balance and debits the exact token cost — this is the per-org
// metering for paid copy, done the single canonical way (no second meter, no bypass).
func (g *aiStudioGenerator) draftCopy(ctx context.Context, org string, in GenerateInput) (map[string]any, error) {
	if g.ai == nil {
		return nil, fmt.Errorf("content: AI plane not configured: %w", errNotConfigured)
	}
	spec, ok := copySpecs[in.DocType]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownDocType, in.DocType)
	}

	prompt := copySystemPrompt(in) + "\n\n" + copyUserPrompt(in, spec)
	resp, err := g.ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:   copyModel(in.Model, g.model),
		Prompt:  prompt,
		Org:     org,                           // BILL the brand's org (the meter reads this).
		Project: strings.TrimSpace(in.Project), // per-scope attribution.
	})
	if err != nil {
		// Disabled/unreachable AI plane, gateway outage, over-budget: honest degrade.
		// The org/balance detail is logged; the caller sees a clean 503, never a 5xx.
		g.log.Warn("content copy generation degraded", "doctype", in.DocType, "org", org, "err", err)
		return nil, fmt.Errorf("content: copy generation unavailable: %w", errNotConfigured)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		g.log.Warn("content copy generation returned empty", "doctype", in.DocType, "org", org)
		return nil, fmt.Errorf("content: empty copy from model: %w", errNotConfigured)
	}
	return spec.build(in, resp.Content), nil
}

// Generate drafts a piece of content into the CMS and returns its identity. org MUST
// be the caller's validated tenant (resolved once via principal.Org). It is the ONE
// generate path — the HTTP handler and the content_generate automation action both call
// it — so there is a single validated, metered, hook-running write for generated content.
func Generate(ctx context.Context, org string, in GenerateInput) (GenerateResult, error) {
	s := mounted
	if s == nil {
		return GenerateResult{}, errNotMounted
	}
	if !isPublishableDocType(in.DocType) {
		return GenerateResult{}, fmt.Errorf("%w: %q", errUnknownDocType, in.DocType)
	}
	if !framework.Installed(ctx, org, in.DocType) {
		return GenerateResult{}, errModuleNotInstalled
	}

	data, err := s.State.gen.Draft(ctx, org, in)
	if err != nil {
		return GenerateResult{}, err
	}
	if data == nil {
		data = map[string]any{}
	}
	// The lifecycle owns the initial state — a generated item is ALWAYS a draft, never
	// what a Generator might set. This is enforced again by the before_save hook.
	data[StatusField] = StatusDraft

	saved, err := framework.Ingest(ctx, org, in.DocType, data, "")
	if err != nil {
		return GenerateResult{}, err
	}
	return GenerateResult{DocType: saved.DocType, Name: saved.Name, Status: StatusDraft}, nil
}

// isPublishableDocType reports whether name is one of the marketing lifecycle DocTypes.
func isPublishableDocType(name string) bool {
	for _, dt := range publishableDocTypes {
		if dt == name {
			return true
		}
	}
	return false
}

// ---- copy generation: prompt engineering + per-DocType shaping ----

// copySpec is a copy DocType's generation recipe: the JSON schema the model must return
// and the build func that maps that JSON onto the DocType's field data. One spec per
// copy DocType — the ONE place a DocType's copy shape lives.
type copySpec struct {
	kind     string // human label for the prompt ("marketing campaign", "social post")
	guidance string // the exact JSON keys + per-key direction the model must honor
	build    func(in GenerateInput, content string) map[string]any
}

// copySpecs is the copy registry, keyed by DocType. Asset is deliberately absent — it is
// a studio render, not a language-model copy draft (draftAsset handles it).
var copySpecs = map[string]copySpec{
	DocTypeCampaign: {
		kind: "marketing campaign concept",
		guidance: `Return ONLY a JSON object, no markdown fences, with EXACTLY these keys:
  "title":  a real headline, <= 60 characters, no trailing punctuation.
  "brief":  2-3 sentences of campaign narrative — the idea and its point of view.
  "teaser": one line, <= 120 characters, the hook a reader stops on.`,
		build: buildCampaign,
	},
	DocTypeSocialPost: {
		kind: "social post",
		guidance: `Return ONLY a JSON object, no markdown fences, with EXACTLY these keys:
  "title":    a short internal label, <= 60 characters.
  "caption":  the post copy — a hook first line, then 1-2 short lines. No emoji.
  "excerpt":  one-line teaser, <= 140 characters.
  "hashtags": an array of 3-8 lowercase tags, no spaces, no leading '#'.`,
		build: buildSocialPost,
	},
}

// copySystemPrompt is the brand-voice system prompt: the editorial bar every draft is
// held to. It deliberately does NOT inject the deployment brand (hanzo/lux/zoo) into the
// copy — the tenant's brand voice comes from the request (Voice) + brief, never the
// platform's own name. The karma line is the calibration target.
func copySystemPrompt(in GenerateInput) string {
	var b strings.Builder
	b.WriteString("You are the senior brand copy director for a fashion label. ")
	b.WriteString("You write the way the best houses write: confident, spare, editorial, with a point of view. ")
	b.WriteString("Short declarative sentences. Make one real claim and turn it. ")
	b.WriteString(`The bar is a line like "Velvet is not a summer fabric. That was the whole point." — an assertion, then a reveal. `)
	b.WriteString("Never use marketing filler (elevate, unleash, game-changing, must-have, curated, effortless, level up). ")
	b.WriteString("Never use emoji. Never shout. Write copy a discerning reader actually stops on.")
	if v := strings.TrimSpace(in.Voice); v != "" {
		b.WriteString("\nBrand voice: ")
		b.WriteString(v)
	}
	if t := strings.TrimSpace(in.Tone); t != "" {
		b.WriteString("\nTone for this piece: ")
		b.WriteString(t)
	}
	return b.String()
}

// copyUserPrompt is the task prompt: the brief + context + the strict-JSON contract the
// build func parses. It never invents facts — it hands the model the brief and asks for
// copy, so an empty brief yields a tasteful concept, not fabricated product claims.
func copyUserPrompt(in GenerateInput, spec copySpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TASK: Draft a %s.\n", spec.kind)
	if brief := strings.TrimSpace(in.Brief); brief != "" {
		fmt.Fprintf(&b, "Brief: %s\n", brief)
	} else {
		b.WriteString("Brief: (none given — infer a tasteful concept from the product/design)\n")
	}
	if p := strings.TrimSpace(in.Product); p != "" {
		fmt.Fprintf(&b, "Product: %s\n", p)
	}
	if d := strings.TrimSpace(in.Design); d != "" {
		fmt.Fprintf(&b, "Design: %s\n", d)
	}
	if in.DocType == DocTypeSocialPost {
		if ch := strings.TrimSpace(in.Channels); ch != "" {
			fmt.Fprintf(&b, "Channels: %s\n", ch)
		}
	}
	b.WriteString(spec.guidance)
	return b.String()
}

// buildCampaign maps the model's JSON onto Campaign field data. Title is mandatory on
// the DocType, so it is always non-empty (derived from the brief when the model omits
// it). goal is left to the DocType default (awareness).
func buildCampaign(in GenerateInput, content string) map[string]any {
	p := parseCopyJSON(content)
	brief := firstNonEmpty(cstr(p, "brief"), strings.TrimSpace(in.Brief), plainText(content))
	title := firstNonEmpty(cstr(p, "title"), strings.TrimSpace(in.Title), firstLine(brief, 60))
	data := map[string]any{"title": title}
	if brief != "" {
		data["brief"] = brief
	}
	if teaser := cstr(p, "teaser"); teaser != "" {
		data["teaser"] = teaser
	}
	addContext(data, in)
	return data
}

// buildSocialPost maps the model's JSON onto SocialPost field data: caption carries the
// copy plus the hashtags; excerpt is the one-line teaser; channels come from the request.
func buildSocialPost(in GenerateInput, content string) map[string]any {
	p := parseCopyJSON(content)
	caption := firstNonEmpty(cstr(p, "caption"), plainText(content))
	if tags := hashtagLine(cstrs(p, "hashtags")); tags != "" {
		caption = strings.TrimSpace(caption + "\n\n" + tags)
	}
	title := firstNonEmpty(cstr(p, "title"), strings.TrimSpace(in.Title), firstLine(caption, 60))
	data := map[string]any{"title": title}
	if caption != "" {
		data["caption"] = caption
	}
	if excerpt := firstNonEmpty(cstr(p, "excerpt"), firstLine(caption, 140)); excerpt != "" {
		data["excerpt"] = excerpt
	}
	if ch := strings.TrimSpace(in.Channels); ch != "" {
		data["channels"] = ch
	}
	addContext(data, in)
	return data
}

// addContext stamps the shared join keys (project/design) every copy DocType carries.
func addContext(data map[string]any, in GenerateInput) {
	if p := strings.TrimSpace(in.Project); p != "" {
		data["project"] = p
	}
	if d := strings.TrimSpace(in.Design); d != "" {
		data["design"] = d
	}
	if in.DocType == DocTypeCampaign {
		if pr := strings.TrimSpace(in.Product); pr != "" {
			data["product"] = pr
		}
	}
}

// ---- copy parsing helpers (defensive — a model that ignores the JSON contract still
// yields real copy from its raw words, never template mush) ----

// copyModel resolves the copy model most-specific first: request override, operator
// override (CONTENT_COPY_MODEL), then the zen5 default.
func copyModel(reqModel, envModel string) string {
	return firstNonEmpty(strings.TrimSpace(reqModel), envModel, defaultCopyModel)
}

// parseCopyJSON best-effort decodes the model's reply into a field map. It tolerates
// ```json fences / leading prose by extracting the outermost {...}. A reply with no JSON
// object returns an empty map, so callers fall back to the raw text.
func parseCopyJSON(s string) map[string]any {
	obj := extractJSONObject(s)
	if obj == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(obj), &m); err != nil {
		return map[string]any{}
	}
	return m
}

// extractJSONObject returns the substring from the first '{' to the matching final '}'
// (by depth), or "" when there is no balanced object.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// skip
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// cstr reads a trimmed string field from a parsed copy map.
func cstr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// cstrs reads a []string field, tolerating a single comma/space string the model may
// have returned instead of an array.
func cstrs(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case string:
		return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' })
	default:
		return nil
	}
}

// hashtagLine renders tags as a normalized "#a #b #c" line (leading '#', no spaces),
// or "" for none.
func hashtagLine(tags []string) string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#"))
		t = strings.ReplaceAll(t, " ", "")
		if t != "" {
			out = append(out, "#"+t)
		}
	}
	return strings.Join(out, " ")
}

// firstNonEmpty returns the first trimmed non-empty argument, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// firstLine returns the first line of s clipped to max runes (with an ellipsis when
// clipped), used to derive a title/excerpt from a body when the model omitted one.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// plainText strips ```json fences and returns the model reply as prose fallback copy —
// used only when the JSON contract was not honored, so the draft still carries the
// model's real words rather than an empty field.
func plainText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
