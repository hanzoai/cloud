// Copyright © 2026 Hanzo AI. MIT License.

package ai

// calls.go is the model API as an agent calls it: a search of the catalog, one
// completion, one decision — each answered with what was said, which model said
// it and who paid.
//
// /v1/models, /v1/chat/completions and /v1/decisions are hanzoai/ai's raw
// handlers, so no registry entry describes them and no MCP tool reaches them. And
// an MCP result has no headers, where the REST answer says which model served
// (X-Hanzo-Served) and who paid (X-Hanzo-Paid-By). These ops are those three
// routes for a transport with no headers, and nothing else: each replays the
// caller's own request — every header it arrived with, its credential included —
// against this process's own router (client.ServeHere). The call meets the same
// identity boundary, edge gates, zen claim and ai filters (auth, the usage policy,
// the wallet) as the REST route it names, as the same principal in the same org.
// There is no second billing path because there is no second path.
//
// THE ADDRESS. A typed op is one registry entry with four projections, so these
// answer over REST too. They live under /v1/ai/mcp, beside aiMCPTools: the
// agent's projection of the model API, whose answers carry in the body what the
// REST routes carry in headers. They are not a second door to /v1/models,
// /v1/chat/completions or /v1/decisions, which stay the one way to call those
// resources with their own wire.
//
// NO FIGURE CROSSES. A refusal answers its status, code, class, model, fallback,
// reset time and the actions that lift it. Its sentence crosses only when the
// refusal is not about money (402) or a rate (429), because those sentences can
// state an allowance.
// X-RateLimit-* and X-Spend-Warn are never read.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/settings"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// mountCalls registers the three ops on the app whose router answers them.
// Registered before ai's routes for the reading order; the static paths outrank
// them either way.
func mountCalls(app *zip.App) {
	o := callOps{serve: func(r *fasthttp.Request) (*fasthttp.Response, error) { return client.ServeHere(app, r) }}
	g := app.Group("/v1/ai")
	g.Get("/mcp/models", o.models, zip.WithOperationID("aiModels"))
	g.Post("/mcp/chat", o.chat, zip.WithOperationID("aiChat"))
	g.Post("/mcp/decisions", o.decide, zip.WithOperationID("aiDecide"))
	app.Post("/v1/frame", o.frameAlias, zip.WithOperationID("aiFrame"))
	app.Post("/v1/frame/generations", o.frameGenerate, zip.WithOperationID("aiFrameGenerate"))
	app.Post("/v1/frame/edits", o.frameEdit, zip.WithOperationID("aiFrameEdit"))
}

// callOps runs a request against a router: the process's own, the one REST
// reaches.
type callOps struct {
	serve func(*fasthttp.Request) (*fasthttp.Response, error)
}

// aiModelsQuery narrows the catalog. Every filter is optional; together they all
// apply.
type aiModelsQuery struct {
	// Q matches the model's id, name, description or owner, ignoring case.
	Q string `json:"q,omitempty" query:"q"`
	// Class keeps one class: premium (third-party frontier models), ours (Hanzo's
	// priced models) or free.
	Class string `json:"class,omitempty" query:"class"`
	// Family keeps one Hanzo family: enso, zen, kai or zoo.
	Family string `json:"family,omitempty" query:"family"`
	// Capability keeps the models that have it: tools, vision or reasoning, or a
	// modality they take or give (text, image, audio, file, video, decision).
	Capability string `json:"capability,omitempty" query:"capability"`
	// Free keeps the models a call to which bills nothing (bounded by the free
	// allowances instead of a balance).
	Free bool `json:"free,omitempty" query:"free"`
	// ZDR keeps only models eligible for Zero Data Retention (excludes free and
	// data-shared routes).
	ZDR bool `json:"zdr,omitempty" query:"zdr"`
	// Limit is the most models to answer, 1 to 500; 0 answers 50.
	Limit int `json:"limit,omitempty" query:"limit"`
}

// aiModels is a page of the catalog.
type aiModels struct {
	// Total is how many models matched, before Limit.
	Total int `json:"total"`
	// Models are the matches, in the catalog's order.
	Models []aiModel `json:"models"`
}

// aiModel is one model: what it is called, its class and family, its price and
// what it can do.
type aiModel struct {
	// ID is the model's id, the one a completion or a decision names.
	ID string `json:"id"`
	// Name is the model's display name.
	Name string `json:"name,omitempty"`
	// Class is premium, ours or free.
	Class string `json:"class"`
	// Family is the Hanzo family (enso, zen, kai, zoo), absent for a third-party
	// model.
	Family string `json:"family,omitempty"`
	// ContextWindow is the most tokens the model reads at once, absent when the
	// catalog does not say.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the most one answer may hold, absent when the catalog does
	// not say.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Free is whether a call bills nothing.
	Free bool `json:"free"`
	// ZDR is whether the model is eligible for Zero Data Retention (non-free,
	// non-data-shared commercial route).
	ZDR bool `json:"zdr"`
	// Expires is the day the model's vendor stops serving it (2006-01-02), absent
	// when it states none.
	Expires string `json:"expires,omitempty"`
	// Capabilities are tools, vision and reasoning, those the model supports.
	Capabilities []string `json:"capabilities,omitempty"`
	// Inputs are the modalities the model reads.
	Inputs []string `json:"inputs,omitempty"`
	// Outputs are the modalities the model writes.
	Outputs []string `json:"outputs,omitempty"`
	// Pricing is the model's list price.
	Pricing aiPrice `json:"pricing"`
}

// aiPrice is a model's list price, in USD.
type aiPrice struct {
	// Prompt is the price of one input token, a decimal string.
	Prompt string `json:"prompt"`
	// Completion is the price of one output token, a decimal string.
	Completion string `json:"completion"`
	// InputPerMillion is the price of a million input tokens.
	InputPerMillion float64 `json:"input_per_million"`
	// OutputPerMillion is the price of a million output tokens.
	OutputPerMillion float64 `json:"output_per_million"`
	// Variable is true for a router that bills each answer at the price of the
	// model that gave it; the figures above are then that router's ceiling.
	Variable bool `json:"variable,omitempty"`
	// Rates is every rate a resold model's vendor states, in the vendor's names
	// and units (USD per token for prompt, completion, input_cache_read,
	// input_cache_write, internal_reasoning; per request; per search for
	// web_search), as billed: the vendor's rate times our margin, exact.
	Rates map[string]string `json:"rates,omitempty"`
	// Overrides are the vendor's conditional rates as billed, each with its
	// condition: min_prompt_tokens, or utc_start, utc_end and utc_days.
	Overrides []map[string]any `json:"overrides,omitempty"`
}

// aiChatIn is one prompt for one model.
type aiChatIn struct {
	// Model is the model to answer, by id (zen5, enso, kai, anthropic/claude-sonnet-4.5
	// — aiModels lists them). Empty asks the deployment's default Hanzo model.
	Model string `json:"model,omitempty"`
	// Prompt is what to ask.
	Prompt string `json:"prompt"`
	// System is the system message ahead of the prompt, when set.
	System string `json:"system,omitempty"`
	// MaxTokens bounds the answer in tokens; 0 leaves it to the model.
	MaxTokens int `json:"max_tokens,omitempty"`
}

// aiChat is the reply and its receipt.
type aiChat struct {
	// Answer is the model's reply.
	Answer string `json:"answer"`
	// Model is the model asked: the caller's, or the default when it named none.
	Model string `json:"model"`
	// Receipt says which model served the reply and who paid for it.
	Receipt aiReceipt `json:"receipt"`
}

// aiDecideIn is one decision, as POST /v1/decisions takes it.
type aiDecideIn struct {
	// Model is kai, Kai's versioned id kai-<12 hex>, or Jev by its vendor id
	// (typesafe/jev-1.13, ~typesafe/jev-latest).
	Model string `json:"model"`
	// State is what the questions are about: a string, an object or an array, kept
	// as written. Not sent with a Handle.
	State json.RawMessage `json:"state,omitempty"`
	// Questions are 1 to 100 named questions, each {"type": "choice"|"noul"|"score",
	// "instructions"?, "criteria"?}; a choice names at least 2 labels and a score at
	// least 1 level. Not sent with a Handle.
	Questions map[string]json.RawMessage `json:"questions,omitempty"`
	// Observe holds the state under this id (1 to 128 of A-Z a-z 0-9 . _ -), so a
	// later decision can name it as its Handle.
	Observe string `json:"observe,omitempty"`
	// Handle decides over a state this org observed earlier, in place of State and
	// Questions.
	Handle string `json:"handle,omitempty"`
}

// aiDecision is the decision and its receipt.
type aiDecision struct {
	// Decision is POST /v1/decisions' own answer: id, model, provider, answers,
	// usage (the billed input tokens), routing, state_hash and latency_ms.
	Decision json.RawMessage `json:"decision"`
	// Receipt says which model served the decision and who paid for it.
	Receipt aiReceipt `json:"receipt"`
}

// aiFrameBox is a percentage bounding rectangle (0..100) where a layer sits on the frame stage.
type aiFrameBox struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// aiFrameTerminal describes a terminal/diff overlay layer inside a /v1/frame shot.
type aiFrameTerminal struct {
	Title   string   `json:"title,omitempty"`
	Command string   `json:"command,omitempty"`
	Header  string   `json:"header,omitempty"`
	File    string   `json:"file,omitempty"`
	Diff    []string `json:"diff,omitempty"`
	Status  string   `json:"status,omitempty"`
	Badge   string   `json:"badge,omitempty"`
}

// aiFrameLayer is one composable layer in a /v1/frame/generations or /v1/frame/edits stack.
type aiFrameLayer struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	ZIndex   int              `json:"z_index,omitempty"`
	Visible  *bool            `json:"visible,omitempty"`
	Box      *aiFrameBox      `json:"box,omitempty"`
	Layout   string           `json:"layout,omitempty"`
	Theme    string           `json:"theme,omitempty"`
	Model    string           `json:"model,omitempty"`
	Prompt   string           `json:"prompt,omitempty"`
	Eyebrow  string           `json:"eyebrow,omitempty"`
	Headline string           `json:"headline,omitempty"`
	Accent   string           `json:"accent,omitempty"`
	Subhead  string           `json:"subhead,omitempty"`
	Pills    []string         `json:"pills,omitempty"`
	Terminal *aiFrameTerminal `json:"terminal,omitempty"`
	HTML     string           `json:"html,omitempty"`
	CSS      string           `json:"css,omitempty"`
	Color    string           `json:"color,omitempty"`
	FontSize float64          `json:"font_size,omitempty"`
	Font     string           `json:"font,omitempty"`
}

// aiFrameIn is a Hyperframe composite generation or layer-edit request.
type aiFrameIn struct {
	// Model is the image SKU for the text-free diffusion background (default "iro";
	// also accepts "qwen-image", "qwen-image-edit", "z-image-turbo", "flux-3").
	Model string `json:"model,omitempty"`
	// Prompt describes the visual scene. Any quoted copy is extracted onto the copy
	// layer and stripped from the diffusion pass so the background never contains
	// diffusion-rendered text.
	Prompt string `json:"prompt,omitempty"`
	// Instruction is an optional natural-language edit directive on /v1/frame/edits
	// (e.g. `Redesign text to "Zero-config Cloud." with glass theme and split-right layout`).
	Instruction string `json:"instruction,omitempty"`
	// Size is the pixel dimension string, e.g. "1344x768" (default) or "1024x1024".
	Size string `json:"size,omitempty"`
	// Layout selects the copy layer placement: "split-left" (default), "split-right", "center", or "bottom-left".
	Layout string `json:"layout,omitempty"`
	// Theme selects the copy layer styling: "light" (default), "glass", or "dark".
	Theme string `json:"theme,omitempty"`
	// Eyebrow is the mono eyebrow label above the headline (default "HANZO DEV").
	Eyebrow string `json:"eyebrow,omitempty"`
	// Headline is the primary @hanzo/font Zen headline copy.
	Headline string `json:"headline,omitempty"`
	// Accent is the secondary muted headline line.
	Accent string `json:"accent,omitempty"`
	// Subhead is the supporting paragraph copy below the headline.
	Subhead string `json:"subhead,omitempty"`
	// Pills are the mono badge pills rendered beneath the subhead.
	Pills []string `json:"pills,omitempty"`
	// Terminal is the optional terminal/diff overlay seated inside the detected dark surface.
	Terminal *aiFrameTerminal `json:"terminal,omitempty"`
	// TerminalBox overrides the auto-detected percentage bounding box of the dark terminal surface.
	TerminalBox *aiFrameBox `json:"terminal_box,omitempty"`
	// CopyBox overrides the percentage bounding box of the copy layer.
	CopyBox *aiFrameBox `json:"copy_box,omitempty"`
	// BackgroundB64 is an existing text-free diffusion background (base64 PNG/JPEG)
	// passed to /v1/frame/edits to re-composite text/layers without re-running diffusion.
	BackgroundB64 string `json:"background_b64,omitempty"`
	// EditBackground forces /v1/frame/edits to regenerate or edit the diffusion background
	// via Model while preserving all text and UI layers.
	EditBackground bool `json:"edit_background,omitempty"`
	// Layers is the full composable layer stack from a prior frame response.
	Layers []aiFrameLayer `json:"layers,omitempty"`
	// LayerPatches applies partial updates (by layer id) or appends custom text/card/html layers.
	LayerPatches []aiFrameLayer `json:"layer_patches,omitempty"`
}

// aiFrameItem is one rendered frame composite and its editable layers.
type aiFrameItem struct {
	B64JSON       string         `json:"b64_json"`
	BackgroundB64 string         `json:"background_b64"`
	HTML          string         `json:"html"`
	Layout        string         `json:"layout,omitempty"`
	Theme         string         `json:"theme,omitempty"`
	TerminalBox   aiFrameBox     `json:"terminal_box"`
	Layers        []aiFrameLayer `json:"layers"`
}

// aiFrameOut is the response from POST /v1/frame/generations and POST /v1/frame/edits.
type aiFrameOut struct {
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Mode    string        `json:"mode,omitempty"`
	Data    []aiFrameItem `json:"data"`
	Receipt aiReceipt     `json:"receipt,omitempty"`
}

// aiReceipt is what the model API's response headers say about an answer: names
// and states, never a figure.
type aiReceipt struct {
	// Served is the model that answered (X-Hanzo-Served).
	Served string `json:"served,omitempty"`
	// Routed is the model `auto` resolved to (X-Routed-Model).
	Routed string `json:"routed,omitempty"`
	// Fallback is the model that answered in place of the one asked, in limited
	// mode (X-Hanzo-Fallback).
	Fallback string `json:"fallback,omitempty"`
	// Reason is the refusal code that sent the request to the fallback
	// (X-Hanzo-Usage-Reason).
	Reason string `json:"reason,omitempty"`
	// PaidBy is plan, credits or free (X-Hanzo-Paid-By); absent when the answer
	// named no payer, as a free model's does.
	PaidBy string `json:"paid_by,omitempty"`
	// Class is the model's class as billed: premium, ours or free
	// (X-Hanzo-Usage-Class).
	Class string `json:"class,omitempty"`
	// Usage is where that class stands for the payer: ok, near or limited
	// (X-Hanzo-Usage).
	Usage string `json:"usage,omitempty"`
}

// Searches the model catalog — the one GET /v1/models lists, every OpenRouter model
// among it as last synced — by text, class, family, capability or free, and answers
// each match with its class, family, whether it is free, its list price (per token
// and per million; variable for a router billed at the answering model's price; a
// resold model's every vendor rate and conditional rate as billed), context window,
// output bound, expiry and capabilities.
func (o callOps) models(ctx context.Context, in *aiModelsQuery) (*aiModels, error) {
	a, err := o.ask(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if a.status != http.StatusOK {
		return nil, a.refusal()
	}
	var list struct {
		Data []catalogRow `json:"data"`
	}
	if err := json.Unmarshal(a.body, &list); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the model catalog answered something that is not a list")
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = 50
	case limit > 500:
		limit = 500
	}
	zdrRequired := in.ZDR || zdrEnforced(ctx)
	out := &aiModels{Models: []aiModel{}}
	for _, r := range list.Data {
		m := r.model()
		if zdrRequired && !m.ZDR {
			continue
		}
		if !matches(r, m, in) {
			continue
		}
		out.Total++
		if len(out.Models) < limit {
			out.Models = append(out.Models, m)
		}
	}
	return out, nil
}

// zdrEnforced reports whether the caller's request or org settings require
// Zero Data Retention (excluding free and data-shared models).
func zdrEnforced(ctx context.Context) bool {
	if c, ok := cloud.Request(ctx); ok {
		h := strings.ToLower(strings.TrimSpace(c.Header("X-Hanzo-ZDR")))
		if h == "true" || h == "1" {
			return true
		}
	}
	if org, ok := principal.OrgFrom(ctx); ok && org != "" {
		return settings.ZDREnabled(ctx, org)
	}
	return false
}

// Asks one model one prompt and answers the reply, which model served it and who
// paid. The call is the caller's own POST /v1/chat/completions, run as the caller,
// so it is gated, billed and refused exactly as that route is; a refusal names its
// code and what lifts it, never an amount. An empty model asks the deployment's
// default Hanzo model.
func (o callOps) chat(ctx context.Context, in *aiChatIn) (*aiChat, error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return nil, zip.ErrBadRequest("prompt is required")
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = aimodel.Model(ctx, aimodel.RoleDefault, aimodel.DefaultModel)
	}
	if zdrEnforced(ctx) && (strings.EqualFold(model, "free") || strings.HasSuffix(strings.ToLower(model), ":free")) {
		return nil, zip.ErrForbidden("model is not eligible under your organization's Zero Data Retention (ZDR) policy; free and data-shared models are disabled")
	}
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	msgs := make([]message, 0, 2)
	if in.System != "" {
		msgs = append(msgs, message{"system", in.System})
	}
	msgs = append(msgs, message{"user", in.Prompt})
	body, err := json.Marshal(struct {
		Model     string    `json:"model"`
		Messages  []message `json:"messages"`
		MaxTokens int       `json:"max_tokens,omitempty"`
		Stream    bool      `json:"stream"`
	}{model, msgs, in.MaxTokens, false})
	if err != nil {
		return nil, zip.ErrBadRequest("the prompt does not encode: " + err.Error())
	}
	a, err := o.ask(ctx, http.MethodPost, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}
	if a.status != http.StatusOK {
		return nil, a.refusal()
	}
	var done struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(a.body, &done); err != nil || len(done.Choices) == 0 {
		return nil, zip.Errorf(http.StatusBadGateway, "the model API answered something that is not a completion")
	}
	if a.receipt.Served == "" {
		a.receipt.Served = done.Model
	}
	return &aiChat{Answer: done.Choices[0].Message.Content, Model: model, Receipt: a.receipt}, nil
}

// Runs one decision — the caller's own POST /v1/decisions, gated and billed
// exactly as that route is — and answers the decision, which model served it and
// who paid. A refusal names its code and what lifts it, never an amount.
func (o callOps) decide(ctx context.Context, in *aiDecideIn) (*aiDecision, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, zip.ErrBadRequest("the decision does not encode: " + err.Error())
	}
	a, err := o.ask(ctx, http.MethodPost, "/v1/decisions", body)
	if err != nil {
		return nil, err
	}
	if a.status != http.StatusOK {
		return nil, a.refusal()
	}
	if !json.Valid(a.body) {
		return nil, zip.Errorf(http.StatusBadGateway, "the decision service answered something that is not JSON")
	}
	if a.receipt.Served == "" {
		var d struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(a.body, &d)
		a.receipt.Served = d.Model
	}
	return &aiDecision{Decision: a.body, Receipt: a.receipt}, nil
}

// frameRequestError refuses a frame request that has nothing to render, before any
// model is asked (#773): `{}` used to be forwarded with the default model and
// answered 200 with a generated, billed frame. A generation needs a prompt; an edit
// needs the composite it edits (background_b64 or layers) and something to change.
func frameRequestError(targetPath string, in *aiFrameIn) error {
	isEdit := strings.HasSuffix(targetPath, "/edits") || strings.HasSuffix(targetPath, "/edit")
	if !isEdit {
		if strings.TrimSpace(in.Prompt) == "" {
			return zip.ErrBadRequest("prompt is required: describe the scene the frame renders")
		}
		return nil
	}
	if strings.TrimSpace(in.BackgroundB64) == "" && len(in.Layers) == 0 {
		return zip.ErrBadRequest("background_b64 or layers is required: pass the composite a prior frame response returned")
	}
	changes := in.EditBackground || len(in.LayerPatches) > 0 || in.Terminal != nil || in.TerminalBox != nil || in.CopyBox != nil || len(in.Pills) > 0
	for _, f := range []string{in.Instruction, in.Prompt, in.Headline, in.Accent, in.Subhead, in.Eyebrow, in.Layout, in.Theme, in.Size} {
		changes = changes || strings.TrimSpace(f) != ""
	}
	if !changes && len(in.Layers) == 0 {
		return zip.ErrBadRequest("nothing to change: pass instruction, layer_patches, layers, copy fields or edit_background")
	}
	return nil
}

// Alias for POST /v1/frame/generations: generates a layered Hyperframe graphic
// composite (strictly text-free diffusion background plus pixel-perfect
// @hanzo/font typography and HTML/UI layers). Prefer POST /v1/frame/generations
// to create composites and POST /v1/frame/edits for ~15ms zero-diffusion layer
// edits. For layered HyperFrames motion/video compositions (MP4/WebM), use
// POST /v1/studio/runs with render.composition, video.edit, or video.generate.
// prompt is required: a request without one is a 400 and no model is asked.
func (o callOps) frameAlias(ctx context.Context, in *aiFrameIn) (*aiFrameOut, error) {
	return o.runFrame(ctx, "/v1/frames/generations", in)
}

// Generates a Hyperframe graphic composite (POST /v1/frame/generations): strips
// all copy and text triggers from the diffusion prompt so the image model (iro,
// qwen-image, z-image-turbo, flux-3) renders a strictly text-free background,
// auto-detects the dark hardware/terminal surface bounding box, and layers
// pixel-perfect @hanzo/font (Zen + Zen Mono) typography and interactive HTML/UI
// elements on top. Returns the composited PNG (b64_json), the clean diffusion
// background (background_b64), self-contained Hyperframe HTML (html), and the
// editable layers stack (layers). prompt is required: a request without one is a
// 400 and no model is asked.
func (o callOps) frameGenerate(ctx context.Context, in *aiFrameIn) (*aiFrameOut, error) {
	return o.runFrame(ctx, "/v1/frames/generations", in)
}

// Edits an existing Hyperframe composite or its layer stack (POST /v1/frame/edits):
// pass background_b64 and layers (or headline, layout, theme, terminal, instruction,
// or layer_patches) to redesign text, reposition panels, swap themes (light, glass,
// dark), or add custom card/HTML layers in ~15ms with zero diffusion cost; or set
// edit_background=true (with qwen-image-edit or iro) to redo the visual background
// while keeping all typography and UI layers intact. background_b64 or layers is
// required, plus something to change: a request without them is a 400 and no model
// is asked.
func (o callOps) frameEdit(ctx context.Context, in *aiFrameIn) (*aiFrameOut, error) {
	return o.runFrame(ctx, "/v1/frames/edits", in)
}

func (o callOps) runFrame(ctx context.Context, targetPath string, in *aiFrameIn) (*aiFrameOut, error) {
	if in == nil {
		in = &aiFrameIn{}
	}
	if err := frameRequestError(targetPath, in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Model) == "" {
		in.Model = "iro"
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, zip.ErrBadRequest("the frame request does not encode: " + err.Error())
	}
	a, err := o.ask(ctx, http.MethodPost, targetPath, body)
	if err != nil {
		return nil, err
	}
	if a.status != http.StatusOK {
		return nil, a.refusal()
	}
	var out aiFrameOut
	if err := json.Unmarshal(a.body, &out); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the frame service answered something that is not a frame response")
	}
	if a.receipt.Served == "" {
		a.receipt.Served = out.Model
	}
	out.Receipt = a.receipt
	return &out, nil
}

// answer is what the route answered: its status, its body and its receipt.
type answer struct {
	status  int
	body    []byte
	receipt aiReceipt
}

// ask runs the caller's own request, retargeted at method and path with body,
// against this process's router, and reads the answer.
//
// Every header the caller's request carried rides along — the credential, the org
// it selected, X-Hanzo-Fallback — because the route must see the caller exactly
// as REST does: the identity boundary re-derives the principal from them, and a
// header dropped here is a gate answered for somebody else. Only the encoding
// headers are not carried, since the body is a new one.
func (o callOps) ask(ctx context.Context, method, path string, body []byte) (*answer, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		// Off the HTTP path there is no caller to run the route as.
		return nil, zip.ErrUnauthorized("sign in to call the model API")
	}
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	c.Fiber().Request().CopyTo(req)
	req.Header.SetMethod(method)
	req.SetRequestURI(path)
	req.Header.Del(fasthttp.HeaderContentEncoding)
	req.Header.Del(fasthttp.HeaderAcceptEncoding)
	req.Header.Set(fasthttp.HeaderAccept, "application/json")
	req.ResetBody()
	if body == nil {
		req.Header.Del(fasthttp.HeaderContentType)
	} else {
		req.Header.SetContentType("application/json")
		req.SetBody(body)
	}
	resp, err := o.serve(req)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the model API did not answer: %v", err)
	}
	defer fasthttp.ReleaseResponse(resp)
	raw, err := resp.BodyUncompressed()
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "the model API's answer does not decode: %v", err)
	}
	h := &resp.Header
	return &answer{
		status: resp.StatusCode(),
		body:   bytes.Clone(raw),
		receipt: aiReceipt{
			Served:   string(h.Peek("X-Hanzo-Served")),
			Routed:   string(h.Peek("X-Routed-Model")),
			Fallback: string(h.Peek("X-Hanzo-Fallback")),
			Reason:   string(h.Peek("X-Hanzo-Usage-Reason")),
			PaidBy:   string(h.Peek("X-Hanzo-Paid-By")),
			Class:    string(h.Peek("X-Hanzo-Usage-Class")),
			Usage:    string(h.Peek("X-Hanzo-Usage")),
		},
	}, nil
}

// aiRefused is a refusal as a tool reads it: the route's own refusal with every
// figure left out.
type aiRefused struct {
	Status     int        `json:"status"`
	Code       string     `json:"code,omitempty"`
	Type       string     `json:"type,omitempty"`
	Message    string     `json:"message,omitempty"`
	Class      string     `json:"class,omitempty"`
	Model      string     `json:"model,omitempty"`
	Fallback   string     `json:"fallback,omitempty"`
	ResetsAt   string     `json:"resets_at,omitempty"`
	UpgradeURL string     `json:"upgrade_url,omitempty"`
	Actions    []aiRemedy `json:"actions,omitempty"`
}

// aiRemedy is one way past a refusal: upgrade, switch model, continue with
// credits, add credit.
type aiRemedy struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	URL   string `json:"url,omitempty"`
	Plan  string `json:"plan,omitempty"`
	Model string `json:"model,omitempty"`
}

// refusal turns a non-200 answer into the op's error: the same status, and a
// message that is the refusal as JSON — {"error":{status, code, ...}} — so an MCP
// client reads the code and the remedies out of isError content and a REST client
// gets the status it would have had from the route.
func (a *answer) refusal() error {
	var env struct {
		Error json.RawMessage `json:"error"`
		Msg   string          `json:"msg"`
	}
	_ = json.Unmarshal(a.body, &env)
	r := aiRefused{}
	switch {
	case len(env.Error) > 0 && env.Error[0] == '{':
		_ = json.Unmarshal(env.Error, &r)
	case len(env.Error) > 0:
		_ = json.Unmarshal(env.Error, &r.Message)
	}
	r.Status = a.status
	if r.Message == "" {
		r.Message = env.Msg
	}
	if a.status == http.StatusPaymentRequired || a.status == http.StatusTooManyRequests {
		r.Message = ""
	}
	if r.Code == "" && r.Message == "" {
		r.Message = http.StatusText(a.status)
	}
	text, _ := json.Marshal(map[string]aiRefused{"error": r})
	status := a.status
	if status < 400 {
		status = http.StatusBadGateway
	}
	return &zip.HTTPError{Status: status, Code: r.Code, Msg: string(text)}
}

// catalogRow is one row of GET /v1/models, the fields this projection reads.
type catalogRow struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	OwnedBy           string   `json:"owned_by"`
	Class             string   `json:"class"`
	Family            string   `json:"family"`
	ContextWindow     int      `json:"context_window"`
	MaxOutputTokens   int      `json:"max_output_tokens"`
	Free              bool     `json:"free"`
	Expires           string   `json:"expires"`
	Inputs            []string `json:"inputs"`
	Outputs           []string `json:"outputs"`
	SupportsTools     bool     `json:"supports_tools"`
	SupportsVision    bool     `json:"supports_vision"`
	SupportsReasoning bool     `json:"supports_reasoning"`
	Pricing           aiPrice  `json:"pricing"`
}

func (r catalogRow) model() aiModel {
	isFreeOrShared := r.Free || strings.EqualFold(r.Class, "free") || strings.HasSuffix(strings.ToLower(r.ID), ":free")
	m := aiModel{ID: r.ID, Name: r.Name, Class: r.Class, Family: r.Family, ContextWindow: r.ContextWindow,
		MaxOutputTokens: r.MaxOutputTokens, Free: r.Free, ZDR: !isFreeOrShared, Expires: r.Expires,
		Inputs: r.Inputs, Outputs: r.Outputs, Pricing: r.Pricing}
	if r.SupportsTools {
		m.Capabilities = append(m.Capabilities, "tools")
	}
	if r.SupportsVision {
		m.Capabilities = append(m.Capabilities, "vision")
	}
	if r.SupportsReasoning {
		m.Capabilities = append(m.Capabilities, "reasoning")
	}
	return m
}

// matches applies every filter the query set.
func matches(r catalogRow, m aiModel, in *aiModelsQuery) bool {
	if in.Class != "" && !strings.EqualFold(r.Class, in.Class) {
		return false
	}
	if in.Family != "" && !strings.EqualFold(r.Family, in.Family) {
		return false
	}
	if in.Free && !r.Free {
		return false
	}
	if in.ZDR && !m.ZDR {
		return false
	}
	if c := strings.ToLower(strings.TrimSpace(in.Capability)); c != "" {
		has := false
		for _, set := range [][]string{m.Capabilities, r.Inputs, r.Outputs} {
			for _, v := range set {
				has = has || strings.EqualFold(v, c)
			}
		}
		if !has {
			return false
		}
	}
	if q := strings.ToLower(strings.TrimSpace(in.Q)); q != "" {
		text := strings.ToLower(r.ID + "\n" + r.Name + "\n" + r.Description + "\n" + r.OwnedBy)
		if !strings.Contains(text, q) {
			return false
		}
	}
	return true
}
