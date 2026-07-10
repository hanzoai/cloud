package content

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/framework"
)

// generate.go is the agentic content-creation seam: given a brand brief + type, draft
// content into the CMS as a DRAFT document. The heart of the loop's first step.
//
// Decomplected into two concerns:
//
//   - Generator (the seam) turns a GenerateInput into the field data for a draft. Its
//     real implementation (a follow-up, wired at Mount) calls:
//       * zen5 for COPY — deps.AI.ChatCompletion(ctx, &types.ChatRequest{Model, Prompt})
//         with the brand voice + brief, metered per-org via cloud.NewResourceMeter(
//         deps,"content").Gate/MeterUsage (org billing attribution — ChatRequest carries
//         no org, the meter does); and
//       * studio for ASSETS — the ComfyUI graph-submit protocol (POST /prompt with the
//         Qwen-Image-Edit-2511 node graph, poll GET /history/:id, GET /view) writing to
//         orgs/<brand>/output, recorded as an Asset with source_prompt_id.
//   - Generate (this file) is the STABLE write path: it validates the type, requires the
//     marketing module be installed, forces status=draft, and persists via
//     framework.Ingest — the SAME validate + lifecycle-hook pipeline an HTTP create runs.
//     It never changes when the Generator's model or provider changes.
//
// The scaffold ships the notConfigured Generator (honest fail-closed 503), so the write
// path is exercised and typed, and lighting up generation is a one-line Mount swap.

// GenerateInput is the request to draft a piece of content. DocType selects the target
// marketing type; the rest is generation context. It is the wire body of
// POST /v1/content/generate and the input of the content_generate automation action.
type GenerateInput struct {
	DocType  string `json:"doctype"`            // Campaign | SocialPost | Asset
	Title    string `json:"title,omitempty"`    // optional explicit title
	Brief    string `json:"brief,omitempty"`    // the brief/goal driving copy generation
	Product  string `json:"product,omitempty"`  // commerce product handle (copy context)
	Design   string `json:"design,omitempty"`   // studio design slug (asset source)
	Channels string `json:"channels,omitempty"` // target channels (SocialPost)
	Project  string `json:"project,omitempty"`  // brand/site sub-scope
	Model    string `json:"model,omitempty"`    // optional zen model override
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

// notConfiguredGenerator is the fail-closed default until a real Generator is wired at
// Mount. It never fabricates content — it returns an honest error the handler maps to
// 503, so an un-provisioned deployment degrades cleanly instead of shipping fake copy.
type notConfiguredGenerator struct{}

func (notConfiguredGenerator) Draft(context.Context, string, GenerateInput) (map[string]any, error) {
	return nil, errNotConfigured
}

// Generate drafts a piece of content into the CMS and returns its identity. org MUST
// be the caller's validated tenant (resolved once via principal.Tenant). It is the ONE
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
