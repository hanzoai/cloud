// Package help declares the Hanzo Help Center (Frappe Helpdesk-core) model as
// DocType fixtures on the framework engine (clients/framework). Like clients/cms it
// is NOT a bespoke subsystem and mounts NO HTTP surface of its own: a ticket IS a
// framework document in module "help", its lifecycle IS a status field, an agent /
// team / SLA / canned response are framework documents, and CRUD + permissions +
// tenant isolation + install + rendering are the framework's generic, already-live
// surface (/v1/framework/*) and the SAME generic @hanzo/ui DocType renderer that
// draws CMS and ERP.
//
// Help is the THIRD app lane and the purest proof of the thesis: it is fixtures and
// nothing else — ZERO Go behavior (no hooks) and ZERO console UI (the generic
// renderer scoped to module=help). A whole support desk becomes "just DocTypes".
//
// Names are slug-style with an "hd-" prefix, so they never collide with the CMS
// (Author/Media/Page/…) or ERP (erp-*) lanes and never carry a space the console's
// `/cloud` path filter would reject. Tickets use a monotonic series name
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

// DocType names (slug, hd- prefixed).
const (
	dtTicket         = "hd-ticket"
	dtAgent          = "hd-agent"
	dtTeam           = "hd-team"
	dtCannedResponse = "hd-canned-response"
	dtSLA            = "hd-sla"
)

// init registers the Help model with the framework. Installing the "help" module
// (POST /v1/framework/modules/help/install) ensures these DocTypes exist in the
// caller's org. No hooks: Help is pure fixtures.
func init() { framework.RegisterModule(Module, DocTypes()) }

// DocTypes returns the canonical Help Center model. Masters (agent/team/SLA) come
// first for readability; the engine resolves Link targets at document write, so the
// ticket may reference an agent/team/SLA defined here in any order.
func DocTypes() []framework.DocType {
	return []framework.DocType{team(), agent(), sla(), cannedResponse(), ticket()}
}

// ticket is a support request. Its lifecycle is the status Select (Open → Pending →
// Resolved → Closed) — a status write, not a submit, so the ticket is NOT
// submittable. The customer is a free-text party reference (Data, not a cross-lane
// Link) so Help installs and runs WITHOUT the ERP lane; the assignee, team, and SLA
// are in-lane Links.
func ticket() framework.DocType {
	return framework.DocType{
		Name: dtTicket, Module: Module, Autoname: "hd-tkt-.#####", TitleField: "subject",
		Fields: []framework.DocField{
			{Fieldname: "subject", Fieldtype: framework.FieldData, Label: "Subject", Reqd: true, InListView: true},
			{Fieldname: "description", Fieldtype: framework.FieldText, Label: "Description"},
			{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: "Open\nPending\nResolved\nClosed", Default: "Open", InListView: true},
			{Fieldname: "priority", Fieldtype: framework.FieldSelect, Label: "Priority", Options: "Low\nMedium\nHigh\nUrgent", Default: "Medium", InListView: true},
			{Fieldname: "customer", Fieldtype: framework.FieldData, Label: "Customer", InListView: true},
			{Fieldname: "assigned_agent", Fieldtype: framework.FieldLink, Label: "Assigned Agent", Options: dtAgent, InListView: true},
			{Fieldname: "team", Fieldtype: framework.FieldLink, Label: "Team", Options: dtTeam},
			{Fieldname: "sla", Fieldtype: framework.FieldLink, Label: "SLA", Options: dtSLA},
			{Fieldname: "resolution", Fieldtype: framework.FieldText, Label: "Resolution"},
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
