// Package crm declares the sales model as DocType fixtures on the framework
// engine (apps/framework). A company, a contact, an opportunity and an inbound
// application are framework documents in module "crm"; CRUD, per-org isolation,
// permissions, install, list filtering and rendering are the engine's generic
// surface (/v1/framework/*). This package declares fixtures and nothing else.
package crm

import "github.com/hanzoai/cloud/apps/framework"

// Module is the framework module tag every CRM DocType carries. The console's CRM
// surface is the generic DocType renderer scoped to this module.
const Module = "crm"

// RoleCrmUser is the operational role the CRM DocTypes grant. The org owner
// (System Manager) assigns it via /v1/framework/roles.
const RoleCrmUser = "CRM User"

// DocType names are slug-style with a "crm-" prefix: no collision with the cms or
// erp lanes, and no space for the console's path filter to reject.
var (
	dtCompany     = framework.ID{Module: Module, Name: "company"}
	dtContact     = framework.ID{Module: Module, Name: "contact"}
	dtOpportunity = framework.ID{Module: Module, Name: "opportunity"}
	dtApplication = framework.ID{Module: Module, Name: "application"}
)

// init registers the CRM content model with the framework. Installing the module
// (POST /v1/framework/modules/crm/install) ensures these DocTypes exist in the
// caller's org.
func init() {
	framework.RegisterModule(Module, DocTypes())
}

// DocTypes is what an org gets when it installs the CRM lane. Order is masters
// first for readability; the engine resolves Link targets at write, so the set is
// order-independent.
func DocTypes() []framework.DocType {
	return []framework.DocType{company(), contact(), opportunity(), application()}
}

func crmPerms() []framework.DocPerm {
	return []framework.DocPerm{
		{Role: framework.RoleSystemManager, Read: true, Write: true, Create: true, Delete: true},
		{Role: RoleCrmUser, Read: true, Write: true, Create: true, Delete: true},
	}
}

// social is the pair of profile links a party carries.
func social() []framework.DocField {
	return []framework.DocField{
		{Fieldname: "linkedin", Fieldtype: framework.FieldData, Label: "LinkedIn"},
		{Fieldname: "x_link", Fieldtype: framework.FieldData, Label: "X"},
	}
}

func company() framework.DocType {
	return framework.DocType{
		Name: dtCompany.Name, Module: dtCompany.Module, Autoname: "field:company_name", TitleField: "company_name",
		Fields: append([]framework.DocField{
			{Fieldname: "company_name", Fieldtype: framework.FieldData, Label: "Name", Reqd: true, InListView: true},
			{Fieldname: "domain_name", Fieldtype: framework.FieldData, Label: "Domain", InListView: true},
			{Fieldname: "employees", Fieldtype: framework.FieldInt, Label: "Employees"},
			{Fieldname: "city", Fieldtype: framework.FieldData, Label: "City"},
			{Fieldname: "country", Fieldtype: framework.FieldData, Label: "Country"},
			{Fieldname: "arr", Fieldtype: framework.FieldCurrency, Label: "ARR", InListView: true},
			{Fieldname: "currency", Fieldtype: framework.FieldData, Label: "Currency"},
			{Fieldname: "icp", Fieldtype: framework.FieldCheck, Label: "Ideal Customer Profile"},
		}, social()...),
		Perms: crmPerms(),
	}
}

func contact() framework.DocType {
	return framework.DocType{
		Name: dtContact.Name, Module: dtContact.Module, Autoname: "field:email", TitleField: "email",
		Fields: append([]framework.DocField{
			{Fieldname: "first_name", Fieldtype: framework.FieldData, Label: "First Name", InListView: true},
			{Fieldname: "last_name", Fieldtype: framework.FieldData, Label: "Last Name", InListView: true},
			{Fieldname: "email", Fieldtype: framework.FieldData, Label: "Email", Reqd: true, InListView: true},
			{Fieldname: "phone", Fieldtype: framework.FieldData, Label: "Phone"},
			{Fieldname: "job_title", Fieldtype: framework.FieldData, Label: "Job Title"},
			{Fieldname: "city", Fieldtype: framework.FieldData, Label: "City"},
			{Fieldname: "company", Fieldtype: framework.FieldLink, Label: "Company", Options: dtCompany.String(), InListView: true},
		}, social()...),
		Perms: crmPerms(),
	}
}

func opportunity() framework.DocType {
	return framework.DocType{
		Name: dtOpportunity.Name, Module: dtOpportunity.Module, Autoname: "crm-opp-.#####", TitleField: "opportunity_name",
		Fields: []framework.DocField{
			{Fieldname: "opportunity_name", Fieldtype: framework.FieldData, Label: "Name", Reqd: true, InListView: true},
			{Fieldname: "amount", Fieldtype: framework.FieldCurrency, Label: "Amount", InListView: true},
			{Fieldname: "currency", Fieldtype: framework.FieldData, Label: "Currency"},
			{Fieldname: "stage", Fieldtype: framework.FieldSelect, Label: "Stage",
				Options: "NEW\nSCREENING\nMEETING\nPROPOSAL\nCUSTOMER", InListView: true},
			{Fieldname: "close_date", Fieldtype: framework.FieldDate, Label: "Close Date", InListView: true},
			{Fieldname: "company", Fieldtype: framework.FieldLink, Label: "Company", Options: dtCompany.String(), InListView: true},
			{Fieldname: "point_of_contact", Fieldtype: framework.FieldLink, Label: "Point of Contact", Options: dtContact.String()},
		},
		Perms: crmPerms(),
	}
}

// application is an inbound application to a programme, promoted on acceptance
// into a company and a contact.
func application() framework.DocType {
	return framework.DocType{
		Name: dtApplication.Name, Module: dtApplication.Module, Autoname: "crm-app-.#####", TitleField: "company",
		Fields: []framework.DocField{
			{Fieldname: "company", Fieldtype: framework.FieldData, Label: "Company", Reqd: true, InListView: true},
			{Fieldname: "website", Fieldtype: framework.FieldData, Label: "Website"},
			{Fieldname: "contact_name", Fieldtype: framework.FieldData, Label: "Contact Name", InListView: true},
			{Fieldname: "email", Fieldtype: framework.FieldData, Label: "Email", InListView: true},
			{Fieldname: "role", Fieldtype: framework.FieldData, Label: "Role"},
			{Fieldname: "stage", Fieldtype: framework.FieldSelect, Label: "Stage",
				Options: "applied\nscreening\naccepted\nrejected", InListView: true},
			{Fieldname: "tier1", Fieldtype: framework.FieldCheck, Label: "Tier 1"},
			{Fieldname: "reason", Fieldtype: framework.FieldText, Label: "Reason"},
			{Fieldname: "metadata", Fieldtype: framework.FieldJSON, Label: "Metadata"},
			{Fieldname: "screen", Fieldtype: framework.FieldJSON, Label: "Screen"},
			{Fieldname: "events", Fieldtype: framework.FieldJSON, Label: "Events"},
			// Set when an application is promoted.
			{Fieldname: "promoted_company", Fieldtype: framework.FieldLink, Label: "Company Record", Options: dtCompany.String()},
			{Fieldname: "promoted_contact", Fieldtype: framework.FieldLink, Label: "Contact Record", Options: dtContact.String()},
		},
		Perms: crmPerms(),
	}
}
