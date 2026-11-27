package patrol

import "github.com/hanzoai/cloud/apps/framework"

// Module is the framework module tag every patrol DocType carries. The console's
// patrol surface is the generic DocType renderer scoped to this module.
const Module = "patrol"

// The six roles. Each is an IAM role name in the tenant's own org, and the same
// string appears in every DocPerm below and in every route's admission list.
const (
	// RoleOwner is the company principal.
	RoleOwner = "owner"
	// RoleManager runs the operation.
	RoleManager = "manager"
	// RoleSupervisor runs the shift.
	RoleSupervisor = "supervisor"
	// RoleController is the control room: verifies, dispatches, closes.
	RoleController = "controller"
	// RoleOfficer is on the ground: pings position, confirms points, works an
	// incident.
	RoleOfficer = "officer"
	// RoleClient is the customer, reading their own sites and reports.
	RoleClient = "client"
	// RoleCentre is the alarm receiving centre's own machine credential: it posts
	// activations and reads nothing. A centre is a third party whose whole
	// business with the operation is one POST, and the control room's role — the
	// other admission the intake takes — is the operation itself, which reads the
	// key register, opens any camera at any premises and works incidents.
	RoleCentre = "centre"
)

// The role sets, beside the roles they are made of. A route admits one of these
// and a DocType's permissions are built from the same values, so who may do a
// thing is answered from one place.
var (
	// everyone is the six — the admission of a read any signed-in member of the
	// operation may make.
	everyone = []string{RoleOwner, RoleManager, RoleSupervisor, RoleController, RoleOfficer, RoleClient}
	// staff is everyone but the customer — the admission of anything a shift does.
	staff = []string{RoleOwner, RoleManager, RoleSupervisor, RoleController, RoleOfficer}
	// control is the control room and above: the roles that verify, dispatch,
	// escalate and file.
	control = []string{RoleController, RoleSupervisor, RoleManager, RoleOwner}
	// crew is control plus the officer working the incident.
	crew = []string{RoleOfficer, RoleController, RoleSupervisor, RoleManager, RoleOwner}
	// shift is the supervisor and above — the roles that close.
	shift = []string{RoleSupervisor, RoleManager, RoleOwner}
	// intake is who may post an activation: the receiving centre, and the control
	// room keying one in by hand.
	intake = append([]string{RoleCentre}, control...)
	// audience is who may DOWNLOAD a filed report: the control room and above,
	// and the customer it is about, whose own reports ownReport narrows to. The
	// officer whose shift it describes reads the incident, not the account filed
	// of it. It is a route's admission and not a DocType's: on the generic
	// surface a role is a right on every row, and "theirs" is not a role.
	audience = []string{RoleOwner, RoleManager, RoleSupervisor, RoleController, RoleClient}
)

// The DocType set, by address. subsystem.go, incident.go, alarm.go and report.go
// all address documents through these, so the fixtures and the surface share one
// identifier set.
var (
	DTTenant   = framework.ID{Module: Module, Name: "tenant"}
	DTClient   = framework.ID{Module: Module, Name: "client"}
	DTZone     = framework.ID{Module: Module, Name: "zone"}
	DTHazard   = framework.ID{Module: Module, Name: "hazard"}
	DTUnit     = framework.ID{Module: Module, Name: "unit"}
	DTSite     = framework.ID{Module: Module, Name: "site"}
	DTFix      = framework.ID{Module: Module, Name: "fix"}
	DTTour     = framework.ID{Module: Module, Name: "tour"}
	DTPoint    = framework.ID{Module: Module, Name: "point"}
	DTKey      = framework.ID{Module: Module, Name: "key"}
	DTMovement = framework.ID{Module: Module, Name: "movement"}
	DTCamera   = framework.ID{Module: Module, Name: "camera"}
	DTIncident = framework.ID{Module: Module, Name: "incident"}
	DTAlarm    = framework.ID{Module: Module, Name: "alarm"}
	DTStep     = framework.ID{Module: Module, Name: "step"}
	DTEvent    = framework.ID{Module: Module, Name: "event"}
	DTNote     = framework.ID{Module: Module, Name: "note"}
	DTReport   = framework.ID{Module: Module, Name: "report"}
)

// init registers the model with the framework. Installing the module
// (POST /v1/framework/modules/patrol/install) creates these DocTypes in the
// caller's org. No lifecycle hooks: an incident advances by a state write that
// incident.go performs, not by a submit.
func init() { framework.RegisterModule(Module, DocTypes()) }

// DocTypes returns the model. Every Link and Table target is declared before the
// DocType that references it, and every target is in-lane, so patrol installs and
// runs without any other app.
func DocTypes() []framework.DocType {
	return []framework.DocType{
		tenant(), customer(), zone(), hazard(), unit(), site(),
		fix(), tour(), point(), key(), movement(), camera(),
		incident(), alarm(), step(), event(), note(), report(),
	}
}

// tenant is the one configuration document per org: what the product calls
// itself, who the company is, how the operation runs, which features are on,
// the words the screen uses, and the address lists notify sends to. Single, so
// there is one row and it is named for the DocType.
func tenant() framework.DocType {
	return framework.DocType{
		Name: DTTenant.Name, Module: Module, IsSingle: true,
		Fields: []framework.DocField{
			{Fieldname: "brand", Fieldtype: framework.FieldJSON, Label: "Brand"},
			{Fieldname: "company", Fieldtype: framework.FieldJSON, Label: "Company"},
			{Fieldname: "operations", Fieldtype: framework.FieldJSON, Label: "Operations"},
			{Fieldname: "features", Fieldtype: framework.FieldJSON, Label: "Features"},
			{Fieldname: "terminology", Fieldtype: framework.FieldJSON, Label: "Terminology"},
			{Fieldname: "contacts", Fieldtype: framework.FieldJSON, Label: "Contacts"},
		},
		Perms: opsPerms(),
	}
}

// customer is a client of the operation. Named by its name, so a site links it
// by the word a controller reads.
func customer() framework.DocType {
	return framework.DocType{
		Name: DTClient.Name, Module: Module, Autoname: "field:name", TitleField: "name",
		Fields: []framework.DocField{
			{Fieldname: "name", Fieldtype: framework.FieldData, Label: "Name", Reqd: true, InListView: true},
			{Fieldname: "contact", Fieldtype: framework.FieldData, Label: "Contact", InListView: true},
			{Fieldname: "phone", Fieldtype: framework.FieldData, Label: "Phone"},
			{Fieldname: "portal", Fieldtype: framework.FieldCheck, Label: "Portal", Default: "0"},
		},
		Perms: opsPerms(),
	}
}

// zone is one detection zone on a panel. A child table of site, so it rides
// inside the site document and one fetch answers the site view.
func zone() framework.DocType {
	return framework.DocType{
		Name: DTZone.Name, Module: Module, TitleField: "ref",
		Fields: []framework.DocField{
			{Fieldname: "ref", Fieldtype: framework.FieldData, Label: "Ref", InListView: true},
			{Fieldname: "location", Fieldtype: framework.FieldData, Label: "Location", InListView: true},
			{Fieldname: "device", Fieldtype: framework.FieldData, Label: "Device"},
			{Fieldname: "state", Fieldtype: framework.FieldData, Label: "State"},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// hazard is one standing warning about a site. A child table of site.
func hazard() framework.DocType {
	return framework.DocType{
		Name: DTHazard.Name, Module: Module, TitleField: "hazard",
		Fields: []framework.DocField{
			{Fieldname: "hazard", Fieldtype: framework.FieldData, Label: "Hazard", InListView: true},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// unit is a patrol vehicle or a foot officer. Named by its call sign, so
// /v1/patrol/unit/{name}/fix addresses the sign a controller says out loud. due
// is the lone-worker deadline.
func unit() framework.DocType {
	return framework.DocType{
		Name: DTUnit.Name, Module: Module, Autoname: "field:sign", TitleField: "sign",
		Fields: []framework.DocField{
			{Fieldname: "sign", Fieldtype: framework.FieldData, Label: "Sign", Reqd: true, Unique: true, InListView: true},
			{Fieldname: "officer", Fieldtype: framework.FieldData, Label: "Officer", InListView: true},
			{Fieldname: "sector", Fieldtype: framework.FieldData, Label: "Sector", InListView: true},
			{Fieldname: "state", Fieldtype: framework.FieldSelect, Label: "State", Options: unitStates, Default: UnitAvailable, InListView: true},
			{Fieldname: "lat", Fieldtype: framework.FieldFloat, Label: "Latitude"},
			{Fieldname: "lon", Fieldtype: framework.FieldFloat, Label: "Longitude"},
			{Fieldname: "seen", Fieldtype: framework.FieldDatetime, Label: "Seen"},
			{Fieldname: "due", Fieldtype: framework.FieldDatetime, Label: "Due"},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// site is one address under contract. account is the alarm-panel account number
// the receiving centre sends, and it is what an activation matches on. Zones and
// hazards ride inside the document.
func site() framework.DocType {
	return framework.DocType{
		Name: DTSite.Name, Module: Module, Autoname: "field:name", TitleField: "name",
		Fields: []framework.DocField{
			{Fieldname: "ref", Fieldtype: framework.FieldInt, Label: "Ref", InListView: true},
			{Fieldname: "name", Fieldtype: framework.FieldData, Label: "Name", Reqd: true, InListView: true},
			{Fieldname: "account", Fieldtype: framework.FieldData, Label: "Account", Unique: true, InListView: true},
			{Fieldname: "category", Fieldtype: framework.FieldData, Label: "Category"},
			{Fieldname: "town", Fieldtype: framework.FieldData, Label: "Town", InListView: true},
			{Fieldname: "eircode", Fieldtype: framework.FieldData, Label: "Eircode"},
			{Fieldname: "lat", Fieldtype: framework.FieldFloat, Label: "Latitude"},
			{Fieldname: "lon", Fieldtype: framework.FieldFloat, Label: "Longitude"},
			{Fieldname: "risk", Fieldtype: framework.FieldInt, Label: "Risk"},
			{Fieldname: "tier", Fieldtype: framework.FieldData, Label: "Tier"},
			{Fieldname: "sla", Fieldtype: framework.FieldInt, Label: "SLA Minutes"},
			{Fieldname: "sector", Fieldtype: framework.FieldData, Label: "Sector"},
			{Fieldname: "unit", Fieldtype: framework.FieldLink, Label: "Unit", Options: DTUnit.String()},
			{Fieldname: "keys", Fieldtype: framework.FieldCheck, Label: "Keys Held", Default: "0"},
			{Fieldname: "cameras", Fieldtype: framework.FieldInt, Label: "Cameras"},
			{Fieldname: "visits", Fieldtype: framework.FieldInt, Label: "Visits"},
			{Fieldname: "contact", Fieldtype: framework.FieldData, Label: "Contact"},
			{Fieldname: "status", Fieldtype: framework.FieldSelect, Label: "Status", Options: siteStates, Default: SiteOK, InListView: true},
			{Fieldname: "client", Fieldtype: framework.FieldLink, Label: "Client", Options: DTClient.String(), InListView: true},
			{Fieldname: "zones", Fieldtype: framework.FieldTable, Label: "Zones", Options: DTZone.String()},
			{Fieldname: "hazards", Fieldtype: framework.FieldTable, Label: "Hazards", Options: DTHazard.String()},
			{Fieldname: "notes", Fieldtype: framework.FieldText, Label: "Notes"},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// fix is one recorded position of one unit, written by POST /unit/{sign}/fix and
// by nothing else. Where a unit was is what a lone-worker alarm, an SLA and a
// court all read, so it is Immutable: the route stamps it once and every role
// reads. Write-mostly besides — the map trail draws the last few dozen and
// nothing reads further back.
func fix() framework.DocType {
	return framework.DocType{
		Name: DTFix.Name, Module: Module, Autoname: "hash", TitleField: "unit", Immutable: true,
		Fields: []framework.DocField{
			{Fieldname: "unit", Fieldtype: framework.FieldLink, Label: "Unit", Options: DTUnit.String(), Reqd: true, InListView: true},
			{Fieldname: "lat", Fieldtype: framework.FieldFloat, Label: "Latitude"},
			{Fieldname: "lon", Fieldtype: framework.FieldFloat, Label: "Longitude"},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At", InListView: true},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// tour is one unit's round for one date. The operation plans it and the officer
// reads it: a round an officer could re-crew is a round they could complete for
// somebody else, through the route that asks whose unit it is.
func tour() framework.DocType {
	return framework.DocType{
		Name: DTTour.Name, Module: Module, Autoname: "hash", TitleField: "unit",
		Fields: []framework.DocField{
			{Fieldname: "unit", Fieldtype: framework.FieldLink, Label: "Unit", Options: DTUnit.String(), Reqd: true, InListView: true},
			{Fieldname: "date", Fieldtype: framework.FieldDate, Label: "Date", InListView: true},
			{Fieldname: "start", Fieldtype: framework.FieldDatetime, Label: "Start"},
			{Fieldname: "end", Fieldtype: framework.FieldDatetime, Label: "End"},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// point is one checkpoint on a tour. open and close bound the window it may be
// confirmed in; photo holds an object key, never bytes.
//
// The round is planned by the operation and confirmed by
// POST /point/{name}/confirm, so everything the confirmation establishes — that
// it happened, when, by whom, how, where, and the photo taken there — is
// ReadOnly and belongs to that route. Proof of presence is the product, and a
// checkpoint anybody on the roster could complete in another officer's name is
// proof of nothing.
func point() framework.DocType {
	return framework.DocType{
		Name: DTPoint.Name, Module: Module, Autoname: "hash", TitleField: "tag",
		Fields: []framework.DocField{
			{Fieldname: "tour", Fieldtype: framework.FieldLink, Label: "Tour", Options: DTTour.String(), Reqd: true, InListView: true},
			{Fieldname: "site", Fieldtype: framework.FieldLink, Label: "Site", Options: DTSite.String(), InListView: true},
			{Fieldname: "tag", Fieldtype: framework.FieldData, Label: "Tag"},
			{Fieldname: "method", Fieldtype: framework.FieldSelect, Label: "Method", Options: pointMethods, ReadOnly: true},
			{Fieldname: "open", Fieldtype: framework.FieldDatetime, Label: "Opens"},
			{Fieldname: "close", Fieldtype: framework.FieldDatetime, Label: "Closes"},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At", ReadOnly: true},
			{Fieldname: "lat", Fieldtype: framework.FieldFloat, Label: "Latitude", ReadOnly: true},
			{Fieldname: "lon", Fieldtype: framework.FieldFloat, Label: "Longitude", ReadOnly: true},
			{Fieldname: "photo", Fieldtype: framework.FieldData, Label: "Photo", ReadOnly: true},
			{Fieldname: "actor", Fieldtype: framework.FieldData, Label: "Actor", ReadOnly: true, InListView: true},
			{Fieldname: "state", Fieldtype: framework.FieldSelect, Label: "State", Options: pointStates, Default: PointPending, ReadOnly: true, InListView: true},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// key is one client key set. Its own DocType, and its Perms name neither officer
// nor client, so there is no key row for either of them to read.
func key() framework.DocType {
	return framework.DocType{
		Name: DTKey.Name, Module: Module, Autoname: "field:ref", TitleField: "ref",
		Fields: []framework.DocField{
			{Fieldname: "ref", Fieldtype: framework.FieldData, Label: "Ref", Reqd: true, Unique: true, InListView: true},
			{Fieldname: "site", Fieldtype: framework.FieldLink, Label: "Site", Options: DTSite.String(), InListView: true},
			{Fieldname: "where", Fieldtype: framework.FieldData, Label: "Where", InListView: true},
			{Fieldname: "holder", Fieldtype: framework.FieldData, Label: "Holder", InListView: true},
			{Fieldname: "moved", Fieldtype: framework.FieldDatetime, Label: "Moved"},
		},
		Perms: opsPerms(),
	}
}

// movement is one key set leaving, returning, or being counted: who signed for
// it, who took it, and when. It is the chain of custody for a customer's own
// keys, so it is written once by the route that moves the register and read by
// the roles key names.
func movement() framework.DocType {
	return framework.DocType{
		Name: DTMovement.Name, Module: Module, Autoname: "hash", TitleField: "key", Immutable: true,
		Fields: []framework.DocField{
			{Fieldname: "key", Fieldtype: framework.FieldLink, Label: "Key", Options: DTKey.String(), Reqd: true, InListView: true},
			{Fieldname: "act", Fieldtype: framework.FieldSelect, Label: "Act", Options: keyActs, InListView: true},
			{Fieldname: "actor", Fieldtype: framework.FieldData, Label: "Actor", InListView: true},
			{Fieldname: "holder", Fieldtype: framework.FieldData, Label: "Holder", InListView: true},
			{Fieldname: "job", Fieldtype: framework.FieldData, Label: "Job"},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At"},
		},
		Perms: filedPerms(control...),
	}
}

// camera is one stream at a site. secret holds a KMS path, so the document store
// never carries a stream credential and a client reading their own camera row
// learns nothing they could connect with.
func camera() framework.DocType {
	return framework.DocType{
		Name: DTCamera.Name, Module: Module, Autoname: "field:name", TitleField: "label",
		Fields: []framework.DocField{
			{Fieldname: "name", Fieldtype: framework.FieldData, Label: "Name", Reqd: true, InListView: true},
			{Fieldname: "site", Fieldtype: framework.FieldLink, Label: "Site", Options: DTSite.String(), InListView: true},
			{Fieldname: "label", Fieldtype: framework.FieldData, Label: "Label", InListView: true},
			{Fieldname: "kind", Fieldtype: framework.FieldData, Label: "Kind"},
			{Fieldname: "online", Fieldtype: framework.FieldCheck, Label: "Online", Default: "0", InListView: true},
			{Fieldname: "secret", Fieldtype: framework.FieldData, Label: "Secret", ReadOnly: true},
		},
		Perms: opsPerms(),
	}
}

// incident is one response, from activation to close. It is the machine's
// document: incident.go opens it and moves it one act at a time through the
// transition table, and each move leaves a step on the trail. So it is immutable
// and nobody holds create — a state landed directly, and an incident with no
// trail behind it, are the same assertion of a response that never happened —
// and the stamps are ReadOnly besides, which is what holds a create through the
// in-process API to the state an incident is born in. The SLA clocks are derived
// from the stamps and never stored.
func incident() framework.DocType {
	return framework.DocType{
		Name: DTIncident.Name, Module: Module, Autoname: "inc-.#####", TitleField: "ref", Immutable: true,
		Fields: []framework.DocField{
			{Fieldname: "site", Fieldtype: framework.FieldLink, Label: "Site", Options: DTSite.String(), Reqd: true, InListView: true},
			{Fieldname: "zone", Fieldtype: framework.FieldData, Label: "Zone"},
			{Fieldname: "ref", Fieldtype: framework.FieldData, Label: "Ref", InListView: true},
			{Fieldname: "trigger", Fieldtype: framework.FieldData, Label: "Trigger", InListView: true},
			{Fieldname: "tier", Fieldtype: framework.FieldData, Label: "Tier"},
			{Fieldname: "sla", Fieldtype: framework.FieldInt, Label: "SLA Minutes"},
			{Fieldname: "state", Fieldtype: framework.FieldSelect, Label: "State", Options: incidentStates, Default: Received, ReadOnly: true, InListView: true},
			{Fieldname: "unit", Fieldtype: framework.FieldLink, Label: "Unit", Options: DTUnit.String(), InListView: true},
			{Fieldname: "opened", Fieldtype: framework.FieldDatetime, Label: "Opened", ReadOnly: true},
			{Fieldname: "verified", Fieldtype: framework.FieldDatetime, Label: "Verified", ReadOnly: true},
			{Fieldname: "dispatched", Fieldtype: framework.FieldDatetime, Label: "Dispatched", ReadOnly: true},
			{Fieldname: "enroute", Fieldtype: framework.FieldDatetime, Label: "En Route", ReadOnly: true},
			{Fieldname: "onsite", Fieldtype: framework.FieldDatetime, Label: "On Site", ReadOnly: true},
			{Fieldname: "checked", Fieldtype: framework.FieldDatetime, Label: "Checked", ReadOnly: true},
			{Fieldname: "secured", Fieldtype: framework.FieldDatetime, Label: "Secured", ReadOnly: true},
			{Fieldname: "filed", Fieldtype: framework.FieldDatetime, Label: "Filed", ReadOnly: true},
			{Fieldname: "closed", Fieldtype: framework.FieldDatetime, Label: "Closed", ReadOnly: true},
			{Fieldname: "escalated", Fieldtype: framework.FieldDatetime, Label: "Escalated", ReadOnly: true},
			{Fieldname: "outcome", Fieldtype: framework.FieldText, Label: "Outcome"},
			{Fieldname: "false", Fieldtype: framework.FieldCheck, Label: "False Alarm", Default: "0"},
		},
		Perms: filedPerms(staff...),
	}
}

// alarm is one activation exactly as received, kept whether or not a site
// matched it. "Exactly as received" is the whole of it, so the intake writes it
// and nobody edits it.
func alarm() framework.DocType {
	return framework.DocType{
		Name: DTAlarm.Name, Module: Module, Autoname: "hash", TitleField: "ref", Immutable: true,
		Fields: []framework.DocField{
			{Fieldname: "ref", Fieldtype: framework.FieldData, Label: "Ref", InListView: true},
			{Fieldname: "account", Fieldtype: framework.FieldData, Label: "Account", InListView: true},
			{Fieldname: "site", Fieldtype: framework.FieldLink, Label: "Site", Options: DTSite.String(), InListView: true},
			{Fieldname: "zone", Fieldtype: framework.FieldData, Label: "Zone"},
			{Fieldname: "trigger", Fieldtype: framework.FieldData, Label: "Trigger", InListView: true},
			{Fieldname: "source", Fieldtype: framework.FieldData, Label: "Source"},
			{Fieldname: "raw", Fieldtype: framework.FieldJSON, Label: "Raw"},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At"},
			{Fieldname: "incident", Fieldtype: framework.FieldLink, Label: "Incident", Options: DTIncident.String()},
		},
		Perms: filedPerms(control...),
	}
}

// step is one act performed on one incident, and the whole trail.
func step() framework.DocType {
	return framework.DocType{
		Name: DTStep.Name, Module: Module, Autoname: "hash", TitleField: "act", Immutable: true,
		Fields: []framework.DocField{
			{Fieldname: "incident", Fieldtype: framework.FieldLink, Label: "Incident", Options: DTIncident.String(), Reqd: true, InListView: true},
			{Fieldname: "act", Fieldtype: framework.FieldData, Label: "Act", InListView: true},
			{Fieldname: "from", Fieldtype: framework.FieldData, Label: "From"},
			{Fieldname: "to", Fieldtype: framework.FieldData, Label: "To"},
			{Fieldname: "actor", Fieldtype: framework.FieldData, Label: "Actor", InListView: true},
			{Fieldname: "detail", Fieldtype: framework.FieldText, Label: "Detail"},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At"},
		},
		Perms: filedPerms(staff...),
	}
}

// event is one row of the live feed: what happened, when, and where. Written
// once by the code that raised it and read by everybody, so it is Immutable.
func event() framework.DocType {
	return framework.DocType{
		Name: DTEvent.Name, Module: Module, Autoname: "hash", TitleField: "title", Immutable: true,
		Fields: []framework.DocField{
			{Fieldname: "title", Fieldtype: framework.FieldData, Label: "Title", Reqd: true, InListView: true},
			{Fieldname: "level", Fieldtype: framework.FieldSelect, Label: "Level", Options: eventLevels, Default: LevelCyan, InListView: true},
			{Fieldname: "site", Fieldtype: framework.FieldLink, Label: "Site", Options: DTSite.String(), InListView: true},
			{Fieldname: "unit", Fieldtype: framework.FieldLink, Label: "Unit", Options: DTUnit.String(), InListView: true},
			{Fieldname: "detail", Fieldtype: framework.FieldText, Label: "Detail"},
			{Fieldname: "source", Fieldtype: framework.FieldData, Label: "Source"},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At"},
		},
		Perms: readPerms(RoleOfficer),
	}
}

// note is an internal remark. Its own DocType, and its Perms do not name the
// client, so there is no note row a customer can read.
func note() framework.DocType {
	return framework.DocType{
		Name: DTNote.Name, Module: Module, Autoname: "hash", TitleField: "subject",
		Fields: []framework.DocField{
			{Fieldname: "subject", Fieldtype: framework.FieldData, Label: "Subject", Reqd: true, InListView: true},
			{Fieldname: "body", Fieldtype: framework.FieldText, Label: "Body"},
			{Fieldname: "actor", Fieldtype: framework.FieldData, Label: "Actor", InListView: true},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At"},
		},
		// The officer writes notes, and this is the one register the shift writes
		// through the generic surface. What the shift PRODUCES — a position, a
		// round, a checkpoint, a feed row — is the operation's to plan and the
		// route's to stamp.
		Perms: append(opsPerms(), framework.DocPerm{Role: RoleOfficer, Read: true, Write: true}),
	}
}

// report is one rendered account of a shift, an incident, or a client's estate.
// body holds the text; the PDF is produced on read. Composed once, so it reads
// the same a month later as it did the minute it was filed.
//
// incident and client are who it is ABOUT, and they are what decides who may
// download it: a customer reads an account of their own estate or of an incident
// at a site of theirs, and an account of the whole operation names neither.
func report() framework.DocType {
	return framework.DocType{
		Name: DTReport.Name, Module: Module, Autoname: "hash", TitleField: "kind", Immutable: true,
		Fields: []framework.DocField{
			{Fieldname: "kind", Fieldtype: framework.FieldSelect, Label: "Kind", Options: reportKinds, Default: ReportShift, InListView: true},
			{Fieldname: "from", Fieldtype: framework.FieldDatetime, Label: "From"},
			{Fieldname: "to", Fieldtype: framework.FieldDatetime, Label: "To"},
			{Fieldname: "incident", Fieldtype: framework.FieldLink, Label: "Incident", Options: DTIncident.String()},
			{Fieldname: "client", Fieldtype: framework.FieldLink, Label: "Client", Options: DTClient.String(), InListView: true},
			{Fieldname: "body", Fieldtype: framework.FieldLong, Label: "Body"},
			{Fieldname: "at", Fieldtype: framework.FieldDatetime, Label: "At", InListView: true},
		},
		Perms: filedPerms(control...),
	}
}

// The Select vocabularies, newline-separated as the engine reads them. Every
// value a route writes comes from one of these.
const (
	siteStates     = SiteOK + "\n" + SiteFault + "\n" + SiteAlarm + "\n" + SiteOffline
	unitStates     = UnitAvailable + "\nassigned\n" + UnitEnroute + "\n" + UnitOnsite + "\noffline\nbreak\nemergency"
	pointMethods   = "tag\nscan\nfence\nmanual\nphoto"
	pointStates    = PointPending + "\ndone\nlate\nskipped"
	keyActs        = "out\nin\naudit"
	eventLevels    = LevelRed + "\n" + LevelAmber + "\n" + LevelGreen + "\n" + LevelCyan
	incidentStates = Received + "\n" + Verified + "\n" + Dispatched + "\n" + Enroute + "\n" + Onsite + "\n" + Checked + "\n" + Secured + "\n" + Filed + "\n" + Closed
	reportKinds    = ReportShift + "\n" + ReportIncident + "\n" + ReportClient
)

// Site status values.
const (
	SiteOK      = "ok"
	SiteFault   = "fault"
	SiteAlarm   = "alarm"
	SiteOffline = "offline"
)

// Unit states a route writes. The remaining three (offline, break, emergency)
// are written only through the generic surface.
const (
	UnitAvailable = "available"
	UnitAssigned  = "assigned"
	UnitEnroute   = "enroute"
	UnitOnsite    = "onsite"
)

// PointPending is a checkpoint that has not been confirmed.
const PointPending = "pending"

// Feed levels: red is an activation or an escalation, amber an activation with
// no matching site, green a resolution, cyan everything else.
const (
	LevelRed   = "red"
	LevelAmber = "amber"
	LevelGreen = "green"
	LevelCyan  = "cyan"
)

// Report kinds.
const (
	ReportShift    = "shift"
	ReportIncident = "incident"
	ReportClient   = "client"
)

// These three sets are every permission patrol declares. A right on a DocType is
// a right on every document of it and on every field, so a role that may edit a
// register may edit any row of it, and "their own" is not a role — which is why
// the customer is named on no DocType at all. The portal is served through
// /v1/patrol, where reach narrows every read to the estate the caller's own
// customer record owns; a role on the generic surface would be that same estate
// plus everybody else's.
//
// It is also why a document the machine writes names no writer, why the DocType
// carrying it is Immutable, and why a field the machine maintains is ReadOnly
// besides.
//
// opsPerms is the control room and above: read, write, create and delete on
// everything it names. System Manager rides with owner so the org owner always
// resolves.
func opsPerms() []framework.DocPerm {
	return []framework.DocPerm{
		{Role: framework.RoleSystemManager, Read: true, Write: true, Create: true, Delete: true},
		{Role: RoleOwner, Read: true, Write: true, Create: true, Delete: true},
		{Role: RoleManager, Read: true, Write: true, Create: true, Delete: true},
		{Role: RoleSupervisor, Read: true, Write: true, Create: true, Delete: true},
		{Role: RoleController, Read: true, Write: true, Create: true, Delete: true},
	}
}

// readPerms is the control room writing a register the named roles read. The
// register's own facts — which customer a site belongs to, its panel account,
// which officer crews a unit — are the operation's to set, and reading one is
// not leave to move it.
//
// The customer is never among those roles: a register row is a row about an
// estate, and the estate it is about is the one thing a DocType permission
// cannot say.
func readPerms(roles ...string) []framework.DocPerm {
	return append(opsPerms(), reading(roles)...)
}

// filedPerms is a document the machine writes and people read: nobody holds
// write, create or delete on it, and its DocType is Immutable besides, so a
// caller no permission row binds is refused by the record rather than by a row
// that could be added. An act's trail and a filed account are composed once, by
// the code that earned them, and a right to rewrite one is a right to forge it.
func filedPerms(roles ...string) []framework.DocPerm {
	return append([]framework.DocPerm{{Role: framework.RoleSystemManager, Read: true}}, reading(roles)...)
}

// reading turns role names into read-only grants.
func reading(roles []string) []framework.DocPerm {
	out := make([]framework.DocPerm, 0, len(roles))
	for _, r := range roles {
		out = append(out, framework.DocPerm{Role: r, Read: true})
	}
	return out
}
