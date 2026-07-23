// Package content is the Hanzo agentic-marketing lane: the marketing content loop
// (generate → CMS → review → approve → publish → distribute) built natively on the
// framework DocType engine, the ONE Go-native replacement for the bespoke karma
// Python scripts, multi-tenant for ANY brand (org = tenant, project = brand/site
// sub-scope).
//
// It follows the knowledge lane's proven shape — a framework MODULE (DocType
// fixtures + lifecycle hook) PLUS a thin control-plane subsystem — NOT a second
// store:
//
//   - The content model IS framework DocTypes in module "marketing" (this file):
//     Campaign, SocialPost, Asset. A "collection" is a DocType, a content item is a
//     framework document, media is an Attach URL, and the editorial lifecycle is the
//     ONE `status` Select field (lifecycle.go). All CRUD, tenancy, permissions, and
//     install are the framework's already-live generic surface (/v1/framework/*) —
//     content adds NO parallel CRUD.
//   - The control-plane (content.go) mounts /v1/content/* for the three things the
//     generic engine cannot be: the cross-DocType board (aggregate read), the
//     side-effecting lifecycle transition, and the generate/publish orchestration
//     that reaches out to zen5 (deps.AI), studio, and hanzoai/social.
//
// Blog posts, pages, and articles remain the cms module's Page/Post/Article; phase 2
// widens their status field to this shared lifecycle so ONE state machine governs all
// publishable content. The marketing module owns the campaign/social/asset types the
// cms lane does not.
package content

import "github.com/hanzoai/cloud/clients/framework"

// Module is the framework module tag every marketing DocType carries. The console's
// marketing surface is the generic DocType renderer scoped to this module; installing
// it (POST /v1/framework/modules/marketing/install) ensures these DocTypes exist in
// the caller's org.
const Module = "marketing"

// RoleContentEditor is the editorial role the marketing DocTypes grant read/write/
// create/delete. The org owner (System Manager, seeded trust-on-first-use) assigns it
// via /v1/framework/roles; a role-less member stays denied (secure by default). It is
// the SAME role name the cms lane grants, so one grant covers both content lanes.
// Transitioning is a status write, so it needs no extra right.
const RoleContentEditor = "Content Editor"

// The marketing DocType names. Single-word (no spaces) so a name is a clean URL path
// segment under /v1/content/:doctype and a clean Link `options` target.
const (
	DocTypeCampaign   = "Campaign"
	DocTypeSocialPost = "SocialPost"
	DocTypeAsset      = "Asset"
)

// publishableDocTypes are the marketing DocTypes governed by the lifecycle: the hook
// enforces status edges on each (hooks.go) and the board aggregates over them
// (content.go). The ONE list both read.
var publishableDocTypes = []string{DocTypeCampaign, DocTypeSocialPost, DocTypeAsset}

// init registers the marketing content model with the framework engine. The lifecycle
// hooks are wired here too (hooks.go) so both the fixtures and their behavior are
// declared once at build time, exactly as the cms/kb lanes do.
//
// marketing is ALWAYS-ON: MarkAlwaysOn defaults its DocTypes on for every org, so the
// Business AI Guide's content steps (content_generate → Campaign/Asset) complete for a
// fresh org with no per-org install — the fix for the live "content: marketing module
// not installed for org" gap. It defaults only the SCHEMA on; every document stays
// physically org-scoped (framework.MarkAlwaysOn).
func init() {
	framework.RegisterModule(Module, DocTypes())
	framework.MarkAlwaysOn(Module)
	registerHooks()
}

// DocTypes returns the canonical marketing content model an org gets when it installs
// the "marketing" lane. Link targets are resolved at document write (not at define),
// so cross-references within the set are order-independent.
func DocTypes() []framework.DocType {
	return []framework.DocType{campaign(), socialPost(), asset()}
}

// ---- content DocTypes ----

// campaign is a marketing campaign: a brief + goal that groups content and drives
// generation, moving through the shared lifecycle. `product`/`design` are join keys
// to Hanzo Commerce (the product handle) and the studio design slug — commerce owns
// price/inventory, so a campaign references a product, it does not duplicate it.
func campaign() framework.DocType {
	return framework.DocType{
		Name: DocTypeCampaign, Module: Module, TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "slug", Fieldtype: framework.FieldData, Label: "Slug"},
			{Fieldname: "brief", Fieldtype: framework.FieldText, Label: "Brief"},
			{Fieldname: "goal", Fieldtype: framework.FieldSelect, Label: "Goal",
				Options: "awareness\nengagement\nconversion\nretention\nlaunch", Default: "awareness", InListView: true},
			{Fieldname: "teaser", Fieldtype: framework.FieldSmall, Label: "Teaser"},
			{Fieldname: "hero_media", Fieldtype: framework.FieldAttach, Label: "Hero Media"},
			{Fieldname: "product", Fieldtype: framework.FieldData, Label: "Product"}, // commerce product handle
			{Fieldname: "design", Fieldtype: framework.FieldData, Label: "Design"},   // studio design slug
			{Fieldname: "start_at", Fieldtype: framework.FieldDatetime, Label: "Start At"},
			{Fieldname: "end_at", Fieldtype: framework.FieldDatetime, Label: "End At"},
			statusField(), projectField(), tagsField(),
		},
		Perms: contentPerms(),
	}
}

// socialPost is a single social post targeting one or more channels (provider ids the
// hanzoai/social publish surface maps to per-brand integrations). `external_ids` holds
// the distributor's returned post ids for reconciliation (queued → published).
func socialPost() framework.DocType {
	return framework.DocType{
		Name: DocTypeSocialPost, Module: Module, TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "caption", Fieldtype: framework.FieldText, Label: "Caption"},
			{Fieldname: "excerpt", Fieldtype: framework.FieldSmall, Label: "Excerpt"},
			{Fieldname: "channels", Fieldtype: framework.FieldData, Label: "Channels"}, // "x,instagram,tiktok"
			{Fieldname: "media", Fieldtype: framework.FieldJSON, Label: "Media"},        // [{url,alt,mime}]
			{Fieldname: "campaign", Fieldtype: framework.FieldLink, Label: "Campaign", Options: DocTypeCampaign},
			{Fieldname: "asset", Fieldtype: framework.FieldLink, Label: "Asset", Options: DocTypeAsset},
			{Fieldname: "design", Fieldtype: framework.FieldData, Label: "Design"},
			{Fieldname: "scheduled_at", Fieldtype: framework.FieldDatetime, Label: "Scheduled At"},
			// published_at + external_ids are SERVER-MANAGED: written only by the trusted
			// Transition/Publish ops, never by a client. ReadOnly is the schema declaration
			// (console renders them read-only); the enforceServerOwned before_save hook is
			// what actually rejects a client write, since the store does not gate ReadOnly.
			{Fieldname: "published_at", Fieldtype: framework.FieldDatetime, Label: "Published At", ReadOnly: true},
			{Fieldname: "external_ids", Fieldtype: framework.FieldJSON, Label: "External IDs", ReadOnly: true},
			statusField(), projectField(), tagsField(),
		},
		Perms: contentPerms(),
	}
}

// asset is a studio-rendered marketing asset (the Qwen-Image-Edit-2511 output) plus
// its variants. `file`/`source_media` are Attach URLs under the org's studio output
// prefix (orgs/<brand>/output/...); `source_prompt_id` is the studio prompt id so a
// render can be traced/re-run.
func asset() framework.DocType {
	return framework.DocType{
		Name: DocTypeAsset, Module: Module, TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "design", Fieldtype: framework.FieldData, Label: "Design", InListView: true},
			{Fieldname: "kind", Fieldtype: framework.FieldSelect, Label: "Kind",
				Options: "ecom\nproduct\nlifestyle\nhover\nhero\nthumbnail", Default: "product", InListView: true},
			{Fieldname: "role", Fieldtype: framework.FieldData, Label: "Role"},
			{Fieldname: "prompt", Fieldtype: framework.FieldText, Label: "Prompt"},
			{Fieldname: "source_media", Fieldtype: framework.FieldAttach, Label: "Source Media"},
			{Fieldname: "file", Fieldtype: framework.FieldAttach, Label: "File"},
			{Fieldname: "variants", Fieldtype: framework.FieldJSON, Label: "Variants"},
			{Fieldname: "generator", Fieldtype: framework.FieldData, Label: "Generator", Default: "qwen-image-edit-2511"},
			{Fieldname: "workflow", Fieldtype: framework.FieldData, Label: "Workflow"},
			{Fieldname: "render_params", Fieldtype: framework.FieldJSON, Label: "Render Params"},
			{Fieldname: "source_prompt_id", Fieldtype: framework.FieldData, Label: "Source Prompt ID"},
			{Fieldname: "caption", Fieldtype: framework.FieldSmall, Label: "Caption"},
			{Fieldname: "campaign", Fieldtype: framework.FieldLink, Label: "Campaign", Options: DocTypeCampaign},
			statusField(), projectField(), tagsField(),
		},
		Perms: contentPerms(),
	}
}

// ---- shared field builders (DRY: every publishable DocType shares these) ----

// statusField is the ONE lifecycle column, shared by every publishable DocType. Its
// options and default come from lifecycle.go so the schema and the state machine can
// never disagree.
func statusField() framework.DocField {
	return framework.DocField{
		Fieldname: StatusField, Fieldtype: framework.FieldSelect, Label: "Status",
		Options: statusOptions(), Default: StatusDraft, InListView: true,
	}
}

// statusOptions renders the lifecycle states as a framework Select `options` string
// (newline-separated) in canonical order — derived from the state machine, not
// hand-copied.
func statusOptions() string {
	out := ""
	for i, s := range statuses {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}

// projectField is the org sub-scope: a brand/site key within the org (empty =
// org-level), mirroring the cms lane so one console org→project switcher filters both.
func projectField() framework.DocField {
	return framework.DocField{Fieldname: "project", Fieldtype: framework.FieldData, Label: "Project"}
}

func tagsField() framework.DocField {
	return framework.DocField{Fieldname: "tags", Fieldtype: framework.FieldData, Label: "Tags"}
}

// contentPerms is the shared permission set: the org owner (System Manager) has full
// rights; a granted Content Editor may read/write/create/delete. A role-less member is
// denied — secure by default (the engine seeds a System-Manager-only grant when perms
// are empty, so this is an explicit widening, never a loosening).
func contentPerms() []framework.DocPerm {
	return []framework.DocPerm{
		{Role: framework.RoleSystemManager, Read: true, Write: true, Create: true, Delete: true, Submit: true, Cancel: true},
		{Role: RoleContentEditor, Read: true, Write: true, Create: true, Delete: true},
	}
}
