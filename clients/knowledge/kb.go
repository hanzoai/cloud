// Package kb declares the Hanzo Knowledge Base + unified AI memory model as
// DocType fixtures on the framework engine (clients/framework), and wires the ONE
// per-org vector-indexing path (index.go) that turns every knowledge document
// into retrievable org memory. It is the FOURTH app lane after cms/erp/help and
// the one that makes "a Notion-like wiki + agent memory" just another module on
// Base — no new Base, no new database.
//
// Like cms/erp/help, the content model is fixtures: a wiki page IS a framework
// document (module "kb"), a memory IS a framework document, an ingested doc from
// a connector IS a framework document. CRUD, permissions, tenant isolation, and
// install are the framework's generic, already-live surface (/v1/framework/*) and
// the SAME generic @hanzo/ui DocType renderer that draws CMS and ERP.
//
// UNLIKE the pure-fixture lanes, kb attaches BEHAVIOR (hooks.go): on every save of
// a knowledge document the after_save hook upserts its text to the org's vector
// namespace, and on trash it removes it. Human wiki + AI memory therefore share
// ONE per-org knowledge store, indexed once. kb ALSO mounts a thin retrieval +
// ingestion control-plane subsystem (subsystem.go, /v1/kb/*): semantic search
// (the RAG entry point an agent/chat calls) and app connectors (Slack/GitHub/
// Google) that ingest external docs INTO the same store. Connectors are just
// producers of framework documents — they never fork the vector-write path.
//
// Names are slug-style with a "kb-" prefix so they never collide with the CMS
// (Author/Media/Page/…), ERP (erp-*), or Help (hd-*) lanes and never carry a
// space the console's /cloud path filter would reject.
package knowledge

import "github.com/hanzoai/cloud/clients/framework"

// Module is the framework module tag every KB DocType carries. The console's KB
// surface is the generic DocType renderer scoped to this module (a page-tree
// sidebar over kb-page.parent + the Lexical editor over kb-page.body).
const Module = "kb"

// RoleKBEditor is the authoring role the KB DocTypes grant read/write/create/
// delete. The org owner (System Manager, seeded trust-on-first-use) assigns it via
// /v1/framework/roles; a role-less member stays denied (secure by default). The
// engine seeds a System-Manager-only grant when perms are empty, so naming this
// role is an explicit widening, never a loosening.
const RoleKBEditor = "KB Editor"

// DocType names (slug, kb- prefixed). Exported so the hooks (hooks.go) and the
// retrieval subsystem (subsystem.go) reference the SAME identifiers as the
// fixtures — one source of truth for the doctype set.
const (
	DTPage      = "kb-page"      // a Notion-like wiki page (nested via `parent`)
	DTMemory    = "kb-memory"    // a unit of agent/AI memory (note/fact/observation)
	DTSource    = "kb-source"    // a document ingested from an app connector or upload
	DTConnector = "kb-connector" // an app-connector connection (metadata only; token in KMS)
	DTLink      = "kb-link"      // a wikilink edge extracted from a kb-page body
)

// indexedDocTypes is the set whose saves/trashes flow to the vector store — the
// org's retrievable KNOWLEDGE. kb-connector is metadata (connection state) and is
// NOT indexed; it never carries knowledge text. hooks.go registers the indexing
// hooks over exactly this set, and subsystem.go's search defaults to it.
var indexedDocTypes = []string{DTPage, DTMemory, DTSource}

// init registers the KB content model and its indexing hooks with the framework.
// Installing the "kb" module (POST /v1/framework/modules/kb/install) ensures these
// DocTypes exist in the caller's org; the hooks then index every knowledge write.
func init() {
	framework.RegisterModule(Module, DocTypes())
	registerHooks()
}

// DocTypes returns the canonical KB + memory model. Masters have no cross-lane
// Links (kb runs standalone); kb-page's `parent` is a SELF Link (the framework
// resolves Link targets at document write, so a child may reference a parent
// defined in any order, and a page tree is just parent chains).
func DocTypes() []framework.DocType {
	return []framework.DocType{page(), memory(), source(), connector(), link()}
}

// ---- knowledge DocTypes ----

// page is a Notion-like wiki page. Slug-named (the document name IS the slug, so a
// page's stable key is unique per org) with a Lexical rich body. `parent` is a
// self-Link giving the hierarchy/tree (empty = a top-level page); the console
// renders the sidebar by walking parent chains. `project` is the optional scope
// tag (empty = org-level) matching the CMS convention, so the org→project switcher
// filters the same one engine. Backlinks are ordinary Link/Data references.
func page() framework.DocType {
	return framework.DocType{
		Name: DTPage, Module: Module, Autoname: "field:slug", TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "slug", Fieldtype: framework.FieldData, Label: "Slug", Reqd: true, InListView: true},
			{Fieldname: "body", Fieldtype: framework.FieldRichText, Label: "Body"},
			{Fieldname: "parent", Fieldtype: framework.FieldLink, Label: "Parent", Options: DTPage, InListView: true},
			{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: "Draft\nPublished", Default: "Draft", InListView: true},
			{Fieldname: "tags", Fieldtype: framework.FieldData, Label: "Tags"},
			{Fieldname: "project", Fieldtype: framework.FieldData, Label: "Project", InListView: true},
		},
		Perms: kbPerms(),
	}
}

// memory is a unit of unified AI/agent memory — the SAME store as the human wiki,
// per-org, so "agent memory" is not a separate system but knowledge documents in
// module kb. `content` holds the memory text (LongText, up to 100 KB); `kind`
// classifies it (note/fact/observation/summary); `source` records who wrote it
// (an agent id, a session, "user"); `project` scopes it. Hash-named (a memory
// needs no slug); the title is a short label for the console list.
func memory() framework.DocType {
	return framework.DocType{
		Name: DTMemory, Module: Module, TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", InListView: true},
			{Fieldname: "content", Fieldtype: framework.FieldLong, Label: "Content", Reqd: true},
			{Fieldname: "kind", Fieldtype: framework.FieldSelect, Label: "Kind", Options: "note\nfact\nobservation\nsummary", Default: "note", InListView: true},
			{Fieldname: "source", Fieldtype: framework.FieldData, Label: "Source", InListView: true},
			{Fieldname: "tags", Fieldtype: framework.FieldData, Label: "Tags"},
			{Fieldname: "project", Fieldtype: framework.FieldData, Label: "Project", InListView: true},
		},
		Perms: kbPerms(),
	}
}

// source is a document ingested from an app connector (Slack/GitHub/Google) or an
// upload — normalized to ONE shape so every source becomes retrievable org
// knowledge alongside manual pages and memories, in the SAME vector namespace.
// `provider` records the origin (github/slack/google/upload); `external_id`+`url`
// key it back to the source for incremental re-sync and citation; `body` (LongText)
// holds the normalized text the indexer embeds. Hash-named; the connector sync
// writes these and the after_save hook indexes them — one ingestion path.
func source() framework.DocType {
	return framework.DocType{
		Name: DTSource, Module: Module, TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "body", Fieldtype: framework.FieldLong, Label: "Body"},
			{Fieldname: "provider", Fieldtype: framework.FieldSelect, Label: "Provider", Options: "github\nslack\ngoogle\nupload", Default: "upload", InListView: true},
			{Fieldname: "external_id", Fieldtype: framework.FieldData, Label: "External ID"},
			{Fieldname: "url", Fieldtype: framework.FieldData, Label: "URL"},
			{Fieldname: "author", Fieldtype: framework.FieldData, Label: "Author"},
			{Fieldname: "source_ts", Fieldtype: framework.FieldDatetime, Label: "Source Time"},
			{Fieldname: "project", Fieldtype: framework.FieldData, Label: "Project", InListView: true},
		},
		Perms: kbPerms(),
	}
}

// connector is an app-connector CONNECTION — metadata ONLY. It holds NO OAuth
// token: the retrievable secret lives in KMS (kms_ref names its path, never its
// value), because a token that must be presented to the provider's API has to be
// retrievable and therefore belongs in KMS, not a Password field (the framework's
// own guidance). `provider` is the connector kind; `status` its lifecycle;
// `account` the connected identity (a GitHub org/user, a Slack team); `cursor` the
// incremental-sync token; `error` the last failure. Named by provider (one
// connection per provider per org) so re-connecting is idempotent.
func connector() framework.DocType {
	return framework.DocType{
		Name: DTConnector, Module: Module, Autoname: "field:provider", TitleField: "provider",
		Fields: []framework.DocField{
			{Fieldname: "provider", Fieldtype: framework.FieldSelect, Label: "Provider", Options: "github\nslack\ngoogle", Reqd: true, InListView: true},
			{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: "disconnected\nconnected\nsyncing\nerror", Default: "disconnected", InListView: true},
			{Fieldname: "account", Fieldtype: framework.FieldData, Label: "Account", InListView: true},
			{Fieldname: "scope", Fieldtype: framework.FieldData, Label: "Scope"},
			// kms_ref is the KMS secret PATH holding the OAuth token — never the token
			// itself. The value is fetched from KMS at sync time and never logged.
			{Fieldname: "kms_ref", Fieldtype: framework.FieldData, Label: "KMS Ref", ReadOnly: true},
			{Fieldname: "cursor", Fieldtype: framework.FieldData, Label: "Sync Cursor", ReadOnly: true},
			{Fieldname: "last_sync", Fieldtype: framework.FieldDatetime, Label: "Last Sync", ReadOnly: true},
			{Fieldname: "doc_count", Fieldtype: framework.FieldInt, Label: "Ingested Docs", ReadOnly: true},
			{Fieldname: "error", Fieldtype: framework.FieldSmall, Label: "Last Error", ReadOnly: true},
		},
		Perms: kbPerms(),
	}
}

// link is a wikilink EDGE extracted from a kb-page body: the persisted form of a
// "[[Page Title]]" reference. It is the ONE store for backlinks (kb.go's "Backlinks
// are ordinary Link/Data references"): `source` is a Link to the page that contains
// the wikilink (always a real page — the edge is written by that page's after_save
// hook), and `target_title` is the raw link text as authored, a Data value. The
// target is resolved to a page by VALUE at graph-read time (matching a page title
// or slug), not stored as a foreign key — so a dangling link is representable, and a
// rename or trash of the target needs no edge rewrite (values, not places). Edges
// carry NO knowledge text, so kb-link is not in indexedDocTypes and never touches
// the vector store. Hash-named: an edge needs no stable slug.
func link() framework.DocType {
	return framework.DocType{
		Name: DTLink, Module: Module,
		Fields: []framework.DocField{
			{Fieldname: "source", Fieldtype: framework.FieldLink, Label: "Source", Options: DTPage, Reqd: true, InListView: true},
			{Fieldname: "target_title", Fieldtype: framework.FieldData, Label: "Target", Reqd: true, InListView: true},
		},
		Perms: kbPerms(),
	}
}

// kbPerms is the shared permission set: the org owner (System Manager) has full
// rights; a granted KB Editor may read/write/create/delete. A role-less member is
// denied — secure by default. KB documents are never submittable (a wiki page /
// memory has no submit lifecycle; publish is a status write), so no submit/cancel.
func kbPerms() []framework.DocPerm {
	return []framework.DocPerm{
		{Role: framework.RoleSystemManager, Read: true, Write: true, Create: true, Delete: true},
		{Role: RoleKBEditor, Read: true, Write: true, Create: true, Delete: true},
	}
}
