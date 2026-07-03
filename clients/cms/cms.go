// Package cms declares the Hanzo CMS content model as DocType fixtures on the
// framework engine (clients/framework). CMS is NOT a bespoke subsystem and mounts
// NO HTTP surface of its own: a "collection" IS a framework DocType in module
// "cms", a content entry IS a framework document, media IS an Attach-backed
// document, and publishing IS a status field. All CRUD, permissions, tenant
// isolation, and install are the framework's generic, already-live surface
// (/v1/framework/*). This package only DECLARES the fixtures and registers them
// with the engine at init — the ONE source of truth for the CMS content model,
// per-org on Base/SQLite.
//
// This is the first app lane on the framework and the template for the rest:
// ERPNext DocTypes and Helpdesk register their fixtures the same way
// (framework.RegisterModule) and are installed + rendered by the SAME generic
// install path and the SAME generic @hanzo/ui DocType renderer. One engine, one
// renderer, every business app.
package cms

import "github.com/hanzoai/cloud/clients/framework"

// Module is the framework module tag every CMS DocType carries. The console's CMS
// surface is the generic DocType renderer scoped to this module.
const Module = "cms"

// RoleContentEditor is the editorial role the CMS content DocTypes grant read/
// write/create/delete. The org owner (System Manager, seeded trust-on-first-use)
// assigns it to teammates via /v1/framework/roles; a role-less member stays
// denied (secure by default). Publishing is a status write, so it needs no extra
// right.
const RoleContentEditor = "Content Editor"

// init registers the CMS content model with the framework. Installing the "cms"
// module (POST /v1/framework/modules/cms/install) ensures these DocTypes exist in
// the caller's org.
func init() { framework.RegisterModule(Module, DocTypes()) }

// DocTypes returns the canonical CMS content model — the default collections an
// org gets when it installs the CMS lane. Order matters only for readability;
// Link targets are resolved at document write, not at define, so a DocType may
// reference another that is defined later in the set.
func DocTypes() []framework.DocType {
	return []framework.DocType{author(), media(), page(), post(), article(), navigation()}
}

// ---- content DocTypes ----

// author is a content author profile — the Link target for the author field on
// pages/posts/articles. Hash-named (an author needs no URL slug); the display
// name is the title.
func author() framework.DocType {
	return framework.DocType{
		Name: "Author", Module: Module, TitleField: "name",
		Fields: []framework.DocField{
			{Fieldname: "name", Fieldtype: framework.FieldData, Label: "Name", Reqd: true, InListView: true},
			{Fieldname: "email", Fieldtype: framework.FieldData, Label: "Email", InListView: true},
			{Fieldname: "bio", Fieldtype: framework.FieldText, Label: "Bio"},
			{Fieldname: "avatar", Fieldtype: framework.FieldAttach, Label: "Avatar"},
		},
		Perms: contentPerms(),
	}
}

// media is the DAM: an Attach-backed catalogue of uploaded assets (the Attach
// holds the object URL under the org's S3/SeaweedFS prefix). Hash-named.
func media() framework.DocType {
	return framework.DocType{
		Name: "Media", Module: Module, TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "file", Fieldtype: framework.FieldAttach, Label: "File", Reqd: true, InListView: true},
			{Fieldname: "alt", Fieldtype: framework.FieldData, Label: "Alt Text"},
			{Fieldname: "mime", Fieldtype: framework.FieldData, Label: "Type", InListView: true},
			{Fieldname: "size", Fieldtype: framework.FieldInt, Label: "Size"},
			{Fieldname: "width", Fieldtype: framework.FieldInt, Label: "Width"},
			{Fieldname: "height", Fieldtype: framework.FieldInt, Label: "Height"},
			{Fieldname: "folder", Fieldtype: framework.FieldData, Label: "Folder", InListView: true},
		},
		Perms: contentPerms(),
	}
}

// page is site structure (marketing/product pages). Slug-named: the document name
// IS the slug, so a page's stable URL key is unique per org.
func page() framework.DocType {
	return framework.DocType{
		Name: "Page", Module: Module, Autoname: "field:slug", TitleField: "title",
		Fields: append(baseContentFields(),
			seoTitle(), seoDescription()),
		Perms: contentPerms(),
	}
}

// post is a blog/changelog entry: the base content plus a category and an explicit
// publish timestamp.
func post() framework.DocType {
	return framework.DocType{
		Name: "Post", Module: Module, Autoname: "field:slug", TitleField: "title",
		Fields: append(baseContentFields(),
			category(),
			framework.DocField{Fieldname: "published_at", Fieldtype: framework.FieldDatetime, Label: "Published At"}),
		Perms: contentPerms(),
	}
}

// article is a knowledge-base / docs article: the base content plus a category.
func article() framework.DocType {
	return framework.DocType{
		Name: "Article", Module: Module, Autoname: "field:slug", TitleField: "title",
		Fields: append(baseContentFields(), category()),
		Perms:  contentPerms(),
	}
}

// navigation is a site menu (header/footer/sidebar): an ordered, optionally nested
// list of links held as JSON ([{label,url,children}]).
func navigation() framework.DocType {
	return framework.DocType{
		Name: "Navigation", Module: Module, Autoname: "field:slug", TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "slug", Fieldtype: framework.FieldData, Label: "Slug", Reqd: true, InListView: true},
			{Fieldname: "location", Fieldtype: framework.FieldSelect, Label: "Location", Options: "header\nfooter\nsidebar", Default: "header", InListView: true},
			{Fieldname: "items", Fieldtype: framework.FieldJSON, Label: "Items"},
		},
		Perms: contentPerms(),
	}
}

// ---- shared field builders (DRY: page/post/article share these) ----

// baseContentFields is the field set every publishable content DocType shares:
// title, URL slug, rich body, excerpt, the publish status, a linked author, a
// featured image, and tags. Returned fresh each call (append-safe).
func baseContentFields() []framework.DocField {
	return []framework.DocField{
		{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
		{Fieldname: "slug", Fieldtype: framework.FieldData, Label: "Slug", Reqd: true, InListView: true},
		{Fieldname: "body", Fieldtype: framework.FieldText, Label: "Body"},
		{Fieldname: "excerpt", Fieldtype: framework.FieldSmall, Label: "Excerpt"},
		{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: "Draft\nPublished", Default: "Draft", InListView: true},
		{Fieldname: "author", Fieldtype: framework.FieldLink, Label: "Author", Options: "Author"},
		{Fieldname: "featured_image", Fieldtype: framework.FieldAttach, Label: "Featured Image"},
		{Fieldname: "tags", Fieldtype: framework.FieldData, Label: "Tags"},
	}
}

func category() framework.DocField {
	return framework.DocField{Fieldname: "category", Fieldtype: framework.FieldData, Label: "Category", InListView: true}
}
func seoTitle() framework.DocField {
	return framework.DocField{Fieldname: "seo_title", Fieldtype: framework.FieldData, Label: "SEO Title"}
}
func seoDescription() framework.DocField {
	return framework.DocField{Fieldname: "seo_description", Fieldtype: framework.FieldSmall, Label: "SEO Description"}
}

// contentPerms is the shared permission set: the org owner (System Manager) has
// full rights; a granted Content Editor may read/write/create/delete. A role-less
// member is denied — secure by default (the engine seeds a System-Manager-only
// grant when perms are empty, so this is an explicit widening, never a loosening).
func contentPerms() []framework.DocPerm {
	return []framework.DocPerm{
		{Role: framework.RoleSystemManager, Read: true, Write: true, Create: true, Delete: true, Submit: true, Cancel: true},
		{Role: RoleContentEditor, Read: true, Write: true, Create: true, Delete: true},
	}
}
