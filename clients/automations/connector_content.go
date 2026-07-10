package automations

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/content"
)

// connector_content.go registers the "content" connector: the marketing content loop
// (clients/content) exposed as automation ACTIONS and MCP TOOLS, so the loop runs
// AUTONOMOUSLY. Each action is both a flow step and an MCP tool named
// "content_<action>" at POST /v1/automations/mcp — the surface a scheduled flow, an
// /v1/agents tool call, or a headless hanzo-bot drives.
//
// Why a first-class connector and not core.http_request → /v1/content/*: core.http_request
// has an SSRF guard that blocks cluster-internal targets in prod, so a flow cannot reach
// the cloud's own /v1/content/* over HTTP. This connector calls the content ops
// IN-PROCESS (content.Generate/Transition/Publish), org-scoped by rc.Org (the VALIDATED
// tenant), needing no network hop and no credential (AuthType "none") — the loop's copy,
// asset, and social credentials live inside the content subsystem's own edges (deps.AI +
// KMS), never here.
//
// The canonical autonomous loop is a flow with a POLLING (cron) trigger:
//
//	content_generate → core.wait_for_approval → content_transition → content_publish
//
// enabled via POST /v1/automations/flows/:id/enable. wait_for_approval is the review
// gate; content_transition advances the lifecycle; content_publish (or the transition's
// own fan-out) distributes. Tracking each run as a hanzo.team task is a separate flow
// step (a future "tracker" connector over the native /v1/tracker REST), keeping content
// orthogonal to tracking.
func init() {
	register(&Connector{
		Name:        "content",
		DisplayName: "Content",
		AuthType:    "none",
		AuthReq:     false,
		Actions: map[string]*Action{
			"generate": {
				Name:        "generate",
				DisplayName: "Generate Content",
				Description: "Draft a marketing content item (Campaign|SocialPost|Asset) into the CMS as a draft.",
				Props: []PropSpec{
					{Name: "doctype", Type: "string", Required: true, Description: "Campaign | SocialPost | Asset."},
					{Name: "brief", Type: "string", Description: "Brief/goal driving copy generation."},
					{Name: "title", Type: "string", Description: "Optional explicit title."},
					{Name: "product", Type: "string", Description: "Commerce product handle (copy context)."},
					{Name: "design", Type: "string", Description: "Studio design slug (asset source)."},
					{Name: "channels", Type: "string", Description: "Target channels for a SocialPost (comma list)."},
					{Name: "project", Type: "string", Description: "Brand/site sub-scope."},
				},
				Run: runContentGenerate,
			},
			"transition": {
				Name:        "transition",
				DisplayName: "Transition Content",
				Description: "Advance a content item along the lifecycle (draft→in_review→approved→queued→published→archived); queued/published fan out to channels.",
				Props: []PropSpec{
					{Name: "doctype", Type: "string", Required: true, Description: "Campaign | SocialPost | Asset."},
					{Name: "name", Type: "string", Required: true, Description: "The content item's document name."},
					{Name: "to", Type: "string", Required: true, Description: "Target lifecycle state."},
					{Name: "scheduleAt", Type: "string", Description: "ISO-8601 time for a scheduled (queued) publish."},
				},
				Run: runContentTransition,
			},
			"publish": {
				Name:        "publish",
				DisplayName: "Publish Content",
				Description: "Distribute a content item to its channels via hanzoai/social.",
				Props: []PropSpec{
					{Name: "doctype", Type: "string", Required: true, Description: "Campaign | SocialPost | Asset."},
					{Name: "name", Type: "string", Required: true, Description: "The content item's document name."},
					{Name: "scheduleAt", Type: "string", Description: "ISO-8601 time to schedule; empty = now."},
				},
				Run: runContentPublish,
			},
		},
	})
}

// runContentGenerate drafts content in the caller's VALIDATED org (rc.Org). It delegates
// to content.Generate — the SAME op the /v1/content/generate handler calls — so the
// automation and the HTTP surface share one implementation.
func runContentGenerate(ctx context.Context, rc RunContext) (any, error) {
	in := content.GenerateInput{
		DocType:  strInput(rc.Input, "doctype"),
		Title:    strInput(rc.Input, "title"),
		Brief:    strInput(rc.Input, "brief"),
		Product:  strInput(rc.Input, "product"),
		Design:   strInput(rc.Input, "design"),
		Channels: strInput(rc.Input, "channels"),
		Project:  strInput(rc.Input, "project"),
		Model:    strInput(rc.Input, "model"),
	}
	if in.DocType == "" {
		return nil, fmt.Errorf("content_generate: doctype is required")
	}
	return content.Generate(ctx, rc.Org, in)
}

// runContentTransition advances a content item's lifecycle in rc.Org, firing the
// distribution fan-out when it enters queued/published (best effort inside content).
func runContentTransition(ctx context.Context, rc RunContext) (any, error) {
	doctype := strInput(rc.Input, "doctype")
	name := strInput(rc.Input, "name")
	to := strInput(rc.Input, "to")
	if doctype == "" || name == "" || to == "" {
		return nil, fmt.Errorf("content_transition: doctype, name and to are required")
	}
	return content.Transition(ctx, rc.Org, doctype, name, to, strInput(rc.Input, "scheduleAt"))
}

// runContentPublish distributes a content item to its channels in rc.Org.
func runContentPublish(ctx context.Context, rc RunContext) (any, error) {
	in := content.PublishInput{
		DocType:    strInput(rc.Input, "doctype"),
		Name:       strInput(rc.Input, "name"),
		ScheduleAt: strInput(rc.Input, "scheduleAt"),
	}
	if in.DocType == "" || in.Name == "" {
		return nil, fmt.Errorf("content_publish: doctype and name are required")
	}
	return content.Publish(ctx, rc.Org, in)
}
