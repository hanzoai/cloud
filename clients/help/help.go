// Package help is the Hanzo Support product: the Frappe-Helpdesk model rebuilt as
// DocType fixtures on the native framework engine (clients/framework), plus a thin
// /v1/help subsystem (subsystem.go) for the one plane the generic, secure-by-default
// framework surface deliberately cannot serve — the PUBLIC help center.
//
// # Two planes, one engine
//
// A ticket IS a framework document in module "help"; its lifecycle IS a status
// field; an agent / team / SLA / canned response / KB article / conversation
// message are framework documents. So the AGENT plane — triage a ticket, author an
// article, read or post a conversation thread — is the framework's generic,
// already-live, role-gated surface (/v1/framework/hd-*) and the SAME generic
// @hanzo/ui DocType renderer that draws CMS and ERP. That plane needs ZERO code
// here beyond the fixtures.
//
// What the generic surface CANNOT do is serve help.hanzo.ai's PUBLIC face: framework
// is secure-by-default (every read/write needs a validated principal AND a role), so
// there is no anonymous "read the public knowledge base" or "a customer files a
// ticket" path. subsystem.go adds exactly that — the public plane — the same way the
// knowledge lane adds /v1/kb for retrieval the generic surface lacks. It owns NO
// store: every read/write delegates to the framework in-process API (Ingest/Get/
// Search), so there is ONE storage engine and no duplicated CRUD.
//
// # Migration
//
// This is the native-Go endgame for the Frappe Helpdesk (Vue frontend + Python
// Frappe backend on a Werkzeug dev server): the model moves onto the native DocType
// engine (no Frappe, no Python, no Werkzeug), the product surface is native Go, and
// the whole desk ships in the ONE cloud binary on Base.
//
// Names are slug-style with an "hd-" prefix, so they never collide with the CMS
// (Author/Media/Page/…), ERP (erp-*), or KB (kb-*) lanes and never carry a space the
// console's `/cloud` path filter would reject. Tickets use a monotonic series name
// ("hd-tkt-.#####"); the masters use a field autoname the console slugifies on write.
package help

import "github.com/hanzoai/cloud/clients/framework"

// Module is the framework module tag every Help DocType carries. The console's Help
// surface is the generic DocType renderer scoped to this module.
const Module = "help"

// RoleHelpAgent is the support role the Help DocTypes grant read/write/create/
// delete. The org owner (System Manager, seeded trust-on-first-use) assigns it via
// /v1/framework/roles; a role-less member stays denied (secure by default).
const RoleHelpAgent = "Help Agent"

// DocType names (slug, hd- prefixed). Exported names are referenced by subsystem.go
// (the public plane) so the fixtures and the surface share ONE identifier set.
const (
	DTTicket        = "hd-ticket"
	DTCommunication = "hd-communication"
	DTArticle       = "hd-article"
	DTCategory      = "hd-article-category"

	dtAgent          = "hd-agent"
	dtTeam           = "hd-team"
	dtCannedResponse = "hd-canned-response"
	dtSLA            = "hd-sla"
)

// init registers the Help model with the framework. Installing the "help" module
// (POST /v1/framework/modules/help/install) ensures these DocTypes exist in the
// caller's org. No lifecycle hooks: the support workflow is a status write, not a
// submit, so the model is pure data.
func init() { framework.RegisterModule(Module, DocTypes()) }

// DocTypes returns the canonical Hanzo Support model. Every Link target is defined
// before the DocType that references it (the engine resolves Links at document
// write, so this ordering is for readability, not correctness) and every target is
// in-lane, so Help installs and runs WITHOUT any other app lane.
func DocTypes() []framework.DocType {
	return []framework.DocType{
		team(), agent(), sla(), cannedResponse(),
		category(), article(),
		ticket(), communication(),
	}
}

// ticket is a support request. Its lifecycle is the status Select (Open → Pending →
// Resolved → Closed) — a status write, not a submit, so the ticket is NOT
// submittable. The customer is a free-text party reference (Data, not a cross-lane
// Link) so Help installs and runs WITHOUT the ERP lane; the assignee, team, and SLA
// are in-lane Links. `source` records the intake channel (portal/email/zendesk/…) so
// the inbound support connectors (Zendesk/Intercom/Re:amaze) can stamp provenance
// when they file a ticket via framework.Ingest — the ONE inbound seam. `public_ref`
// is the opaque, random customer-facing reference the public intake returns in place
// of the monotonic name, so an anonymous submitter never learns the ticket volume
// (the sequential name stays internal to the agent plane).
func ticket() framework.DocType {
	return framework.DocType{
		Name: DTTicket, Module: Module, Autoname: "hd-tkt-.#####", TitleField: "subject",
		Fields: []framework.DocField{
			{Fieldname: "subject", Fieldtype: framework.FieldData, Label: "Subject", Reqd: true, InListView: true},
			{Fieldname: "description", Fieldtype: framework.FieldText, Label: "Description"},
			{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: "Open\nPending\nResolved\nClosed", Default: "Open", InListView: true},
			{Fieldname: "priority", Fieldtype: framework.FieldSelect, Label: "Priority", Options: "Low\nMedium\nHigh\nUrgent", Default: "Medium", InListView: true},
			{Fieldname: "customer", Fieldtype: framework.FieldData, Label: "Customer", InListView: true},
			{Fieldname: "source", Fieldtype: framework.FieldData, Label: "Source", Default: "portal", InListView: true},
			{Fieldname: "public_ref", Fieldtype: framework.FieldData, Label: "Public Ref", Unique: true, ReadOnly: true},
			{Fieldname: "assigned_agent", Fieldtype: framework.FieldLink, Label: "Assigned Agent", Options: dtAgent, InListView: true},
			{Fieldname: "team", Fieldtype: framework.FieldLink, Label: "Team", Options: dtTeam},
			{Fieldname: "sla", Fieldtype: framework.FieldLink, Label: "SLA", Options: dtSLA},
			{Fieldname: "resolution", Fieldtype: framework.FieldText, Label: "Resolution"},
		},
		Perms: helpPerms(),
	}
}

// communication is ONE message in a ticket's conversation thread — the Frappe
// Communication, in-lane. `ticket` Links the thread it belongs to; `sender_type`
// distinguishes a customer message from an agent reply; `channel` records how it
// arrived (portal/email/…). Hash-named (a message needs no slug). Agents read and
// post the thread via the generic surface (GET/POST /v1/framework/hd-communication);
// the customer's opening message is written by the public intake (subsystem.go).
func communication() framework.DocType {
	return framework.DocType{
		Name: DTCommunication, Module: Module, TitleField: "sender",
		Fields: []framework.DocField{
			{Fieldname: "ticket", Fieldtype: framework.FieldLink, Label: "Ticket", Options: DTTicket, Reqd: true, InListView: true},
			{Fieldname: "sender", Fieldtype: framework.FieldData, Label: "Sender", Reqd: true, InListView: true},
			{Fieldname: "sender_type", Fieldtype: framework.FieldSelect, Label: "Sender Type", Options: "customer\nagent", Default: "customer", InListView: true},
			{Fieldname: "body", Fieldtype: framework.FieldText, Label: "Message", Reqd: true},
			{Fieldname: "channel", Fieldtype: framework.FieldData, Label: "Channel", Default: "portal", InListView: true},
		},
		Perms: helpPerms(),
	}
}

// category is a knowledge-base section (the KB taxonomy). Named by category_name
// (the console slugifies on write) so a category has a stable, unique key per org.
func category() framework.DocType {
	return framework.DocType{
		Name: DTCategory, Module: Module, Autoname: "field:category_name", TitleField: "category_name",
		Fields: []framework.DocField{
			{Fieldname: "category_name", Fieldtype: framework.FieldData, Label: "Category", Reqd: true, InListView: true},
			{Fieldname: "description", Fieldtype: framework.FieldSmall, Label: "Description"},
		},
		Perms: helpPerms(),
	}
}

// article is a knowledge-base article. Slug-named (the document name IS the URL
// slug, unique per org). `status` (Draft/Published) is the editorial lifecycle and
// `is_public` gates public-center visibility: the public plane (subsystem.go) serves
// ONLY status=Published AND is_public=1, so a Draft or an internal (agent-only)
// article never leaks. Agents author every article via the generic surface.
func article() framework.DocType {
	return framework.DocType{
		Name: DTArticle, Module: Module, Autoname: "field:slug", TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "slug", Fieldtype: framework.FieldData, Label: "Slug", Reqd: true, InListView: true},
			{Fieldname: "category", Fieldtype: framework.FieldLink, Label: "Category", Options: DTCategory, InListView: true},
			{Fieldname: "body", Fieldtype: framework.FieldRichText, Label: "Body"},
			{Fieldname: "excerpt", Fieldtype: framework.FieldSmall, Label: "Excerpt"},
			{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: "Draft\nPublished", Default: "Draft", InListView: true},
			{Fieldname: "is_public", Fieldtype: framework.FieldCheck, Label: "Public", Default: "0", InListView: true},
		},
		Perms: helpPerms(),
	}
}

// agent is a support agent. Named by agent_name (the console slugifies on write).
func agent() framework.DocType {
	return framework.DocType{
		Name: dtAgent, Module: Module, Autoname: "field:agent_name", TitleField: "agent_name",
		Fields: []framework.DocField{
			{Fieldname: "agent_name", Fieldtype: framework.FieldData, Label: "Agent Name", Reqd: true, InListView: true},
			{Fieldname: "email", Fieldtype: framework.FieldData, Label: "Email", Reqd: true, InListView: true},
			{Fieldname: "team", Fieldtype: framework.FieldLink, Label: "Team", Options: dtTeam, InListView: true},
			{Fieldname: "is_active", Fieldtype: framework.FieldCheck, Label: "Active", Default: "1", InListView: true},
		},
		Perms: helpPerms(),
	}
}

// team is a support team (the assignment/queue grouping).
func team() framework.DocType {
	return framework.DocType{
		Name: dtTeam, Module: Module, Autoname: "field:team_name", TitleField: "team_name",
		Fields: []framework.DocField{
			{Fieldname: "team_name", Fieldtype: framework.FieldData, Label: "Team Name", Reqd: true, InListView: true},
			{Fieldname: "description", Fieldtype: framework.FieldSmall, Label: "Description"},
		},
		Perms: helpPerms(),
	}
}

// cannedResponse is a reusable reply template.
func cannedResponse() framework.DocType {
	return framework.DocType{
		Name: dtCannedResponse, Module: Module, Autoname: "field:title", TitleField: "title",
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "message", Fieldtype: framework.FieldText, Label: "Message", Reqd: true},
		},
		Perms: helpPerms(),
	}
}

// sla is a service-level target keyed by priority (response/resolution hours).
func sla() framework.DocType {
	return framework.DocType{
		Name: dtSLA, Module: Module, Autoname: "field:sla_name", TitleField: "sla_name",
		Fields: []framework.DocField{
			{Fieldname: "sla_name", Fieldtype: framework.FieldData, Label: "SLA Name", Reqd: true, InListView: true},
			{Fieldname: "priority", Fieldtype: framework.FieldSelect, Label: "Priority", Options: "Low\nMedium\nHigh\nUrgent", InListView: true},
			{Fieldname: "response_time_hours", Fieldtype: framework.FieldInt, Label: "Response (hrs)", InListView: true},
			{Fieldname: "resolution_time_hours", Fieldtype: framework.FieldInt, Label: "Resolution (hrs)", InListView: true},
		},
		Perms: helpPerms(),
	}
}

// helpPerms is the shared permission set: the org owner (System Manager) has full
// rights; a granted Help Agent may read/write/create/delete. A role-less member is
// denied — secure by default (the engine's define-time seed is a System-Manager
// grant, so this is an explicit widening, never a loosening).
func helpPerms() []framework.DocPerm {
	return []framework.DocPerm{
		{Role: framework.RoleSystemManager, Read: true, Write: true, Create: true, Delete: true},
		{Role: RoleHelpAgent, Read: true, Write: true, Create: true, Delete: true},
	}
}
