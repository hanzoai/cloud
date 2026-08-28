package framework

import (
	"github.com/hanzoai/doctype"
	engine "github.com/hanzoai/framework"
)

// alias.go re-exports the engine's vocabulary under this package's name.
//
// These are Go type ALIASES and value re-exports, not copies: framework.DocType
// here and doctype.DocType in the value module are the SAME type, so a value
// crosses between them with no conversion and there is still exactly one
// definition of each.
//
// They exist so an app lane (cms, crm, erp, help, knowledge, content, guide) can
// declare a content model as framework.DocType / framework.FieldData without
// caring that the engine lives in its own module. A lane that wants the engine
// directly imports github.com/hanzoai/framework; the types are interchangeable.

// The schema and document types.
type (
	// DocType is a metadata definition.
	DocType = doctype.DocType
	// DocField is one field in a DocType.
	DocField = doctype.DocField
	// DocPerm is a role's rights on a DocType.
	DocPerm = doctype.DocPerm
	// Document is a stored record: validated field data plus its lifecycle state.
	Document = engine.Document
	// Role is a (user, role) assignment within an org.
	Role = engine.Role
	// Store is the engine's per-org data access, reached by hooks via Event.Store.
	Store = engine.Store
	// Event is the value that flows through a lifecycle Hook.
	Event = engine.Event
	// Hook is a server-side lifecycle handler.
	Hook = engine.Hook
	// Ingested is the result of an in-process create.
	Ingested = engine.Ingested
	// Lease is an acquired exclusive claim on (org, key).
	Lease = engine.Lease
	// ListOpts is a parsed, validated list query.
	ListOpts = engine.ListOpts
)

// The closed fieldtype set.
const (
	FieldData     = doctype.FieldData
	FieldInt      = doctype.FieldInt
	FieldFloat    = doctype.FieldFloat
	FieldCurrency = doctype.FieldCurrency
	FieldCheck    = doctype.FieldCheck
	FieldDate     = doctype.FieldDate
	FieldDatetime = doctype.FieldDatetime
	FieldText     = doctype.FieldText
	FieldSmall    = doctype.FieldSmall
	FieldLong     = doctype.FieldLong
	FieldRichText = doctype.FieldRichText
	FieldSelect   = doctype.FieldSelect
	FieldLink     = doctype.FieldLink
	FieldTable    = doctype.FieldTable
	FieldAttach   = doctype.FieldAttach
	FieldJSON     = doctype.FieldJSON
	FieldPassword = doctype.FieldPassword
)

// Roles.
const (
	RoleSystemManager = doctype.RoleSystemManager
	RoleAll           = doctype.RoleAll
)

// Lifecycle hook actions.
const (
	ActionBeforeInsert = engine.ActionBeforeInsert
	ActionBeforeSave   = engine.ActionBeforeSave
	ActionAfterSave    = engine.ActionAfterSave
	ActionOnSubmit     = engine.ActionOnSubmit
	ActionOnCancel     = engine.ActionOnCancel
	ActionOnTrash      = engine.ActionOnTrash
)

// Error sentinels an in-process caller classifies with errors.Is.
var (
	ErrNotFound = engine.ErrNotFound
	ErrConflict = engine.ErrConflict
	ErrBadRef   = engine.ErrBadRef
)

// IsValidationError reports whether err is a document-schema violation.
func IsValidationError(err error) bool { return engine.IsValidationError(err) }

// Registration entry points. An app lane calls these from a package init() to
// declare its content model and attach behaviour — the ONE way a lane extends
// the engine.
var (
	// RegisterModule declares the DocType fixtures a module installs.
	RegisterModule = doctype.RegisterModule
	// MarkAlwaysOn makes a module's fixtures resolve for every org with no
	// per-org install step.
	MarkAlwaysOn = doctype.MarkAlwaysOn
	// RegisteredModules is the sorted set of registered module names.
	RegisteredModules = doctype.RegisteredModules
	// AlwaysOnModules is the sorted set of modules marked always-on.
	AlwaysOnModules = doctype.AlwaysOnModules
	// RegisterHook attaches a lifecycle handler to (doctype, action).
	RegisterHook = engine.RegisterHook
	// RegisteredHookCount is the number of (doctype, action) keys carrying a
	// hook — the composition root's link-guard asserts the lanes are compiled in.
	RegisteredHookCount = engine.RegisteredHookCount
)
