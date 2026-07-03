// Package framework is the Hanzo Framework: a metadata-driven DocType engine,
// native Go on Base/SQLite, mounted in the unified cloud binary at
// /v1/framework/*. It is the rebuilt-in-Go successor to Frappe's DocType/metadata
// core — the FOUNDATION on which CMS content-types, ERPNext DocTypes, and
// Helpdesk become "just DocTypes", so ONE engine + ONE generic UI renders every
// business app (maximal DRY). There is NO Frappe/Python runtime dependency; the
// engine itself is pure Go.
//
// # The model (faithful to Frappe, flattened onto SQLite)
//
//   - A DocType is a metadata definition: {name, module, fields[], permissions[],
//     isSingle, isSubmittable, autoname, titleField}. It is data, stored per-org.
//   - A DocField mirrors Frappe's DocField: {fieldname, fieldtype, label, reqd,
//     options, default, ...}. The fieldtype set is Data, Int, Float, Currency,
//     Check, Date, Datetime, Text, SmallText, LongText, Select, Link, Table,
//     Attach, JSON, Password.
//   - A Document is a schemaless record validated against its DocType at write and
//     persisted as a JSON blob keyed by (org, doctype, name). docstatus is the
//     Frappe lifecycle: 0=draft, 1=submitted, 2=cancelled.
//
// # Tenant isolation (the security boundary)
//
// Every request resolves its org through the ONE boundary — clients/principal
// .Tenant — which returns the org ONLY for a VALIDATED principal (a gateway- or
// BFF-minted X-User-Id from a verified IAM credential), verbatim and cloned. A
// forged X-Org-Id with no validated principal is refused 403 before any store
// access. Every doctype, document, series counter, and role row carries an `org`
// column and every query filters WHERE org=?, so org A's schema and data are
// physically invisible to org B. There is no second org derivation path anywhere
// in this package.
//
// # Surface (all org-scoped; /v1 only)
//
//	GET    /v1/framework/doctypes                DocType registry list        -> {data:[…]}
//	POST   /v1/framework/doctypes                define a DocType             -> DocType (201)
//	GET    /v1/framework/doctypes/:name          DocType definition           -> DocType
//	PUT    /v1/framework/doctypes/:name          replace a DocType            -> DocType
//	DELETE /v1/framework/doctypes/:name          delete a DocType (+ its docs)
//	GET    /v1/framework/roles                    per-org role assignments     -> {data:[…]}
//	POST   /v1/framework/roles                    assign (user,role)           -> Role (201)
//	DELETE /v1/framework/roles/:user/:role        revoke (user,role)
//	GET    /v1/framework/:doctype                 list documents               -> {data:[…]}
//	                                              ?filters=&fields=&limit=&order_by=
//	POST   /v1/framework/:doctype                 create a document            -> Document (201)
//	GET    /v1/framework/:doctype/:name           document detail              -> Document
//	PUT    /v1/framework/:doctype/:name           update a document            -> Document
//	DELETE /v1/framework/:doctype/:name           delete a document
//	POST   /v1/framework/:doctype/:name/submit    docstatus 0→1 (submittable)  -> Document
//	POST   /v1/framework/:doctype/:name/cancel    docstatus 1→2 (submittable)  -> Document
//
// serve.go auto-registers GET /v1/framework/health. Order 129 binds the surface
// before the AI subsystem's /v1/* catch-all (150); the static /doctypes + /roles
// routes register before the generic /:doctype routes so Fiber's first-match scan
// resolves them unambiguously (and those keywords are reserved DocType names).
package framework

import (
	"fmt"
	"regexp"
	"strings"
)

// Fieldtype constants mirror Frappe's DocField.fieldtype. This is the closed set
// the engine validates; a DocField with any other fieldtype is rejected at
// define time (fail closed — an unknown type has no validation and is a hole).
const (
	FieldData     = "Data"
	FieldInt      = "Int"
	FieldFloat    = "Float"
	FieldCurrency = "Currency"
	FieldCheck    = "Check"
	FieldDate     = "Date"
	FieldDatetime = "Datetime"
	FieldText     = "Text"
	FieldSmall    = "SmallText"
	FieldLong     = "LongText"
	FieldSelect   = "Select"
	FieldLink     = "Link"
	FieldTable    = "Table"
	FieldAttach   = "Attach"
	FieldJSON     = "JSON"
	FieldPassword = "Password"
)

// validFieldtypes is the membership set for the constants above.
var validFieldtypes = map[string]bool{
	FieldData: true, FieldInt: true, FieldFloat: true, FieldCurrency: true,
	FieldCheck: true, FieldDate: true, FieldDatetime: true, FieldText: true,
	FieldSmall: true, FieldLong: true, FieldSelect: true, FieldLink: true,
	FieldTable: true, FieldAttach: true, FieldJSON: true, FieldPassword: true,
}

// needsOptions is the set of fieldtypes whose `options` names a target: Select
// (newline-separated choices), Link (target DocType), Table (child DocType).
var needsOptions = map[string]bool{FieldSelect: true, FieldLink: true, FieldTable: true}

// reservedDocTypeNames are names a DocType may NOT take because they collide with
// a static route segment under /v1/framework/. Rejecting them at define time is
// why the static routes can be registered before the generic /:doctype routes
// without ambiguity.
var reservedDocTypeNames = map[string]bool{
	"doctypes": true, "roles": true, "health": true, "summary": true, "modules": true,
}

// Limits. maxField bounds a single scalar text value; maxDocBytes bounds a whole
// document body so an unbounded blob can't amplify the shared SQLite file;
// maxFields / maxChildRows bound a schema and a child table.
const (
	maxField     = 100_000 // 100 KB per scalar (LongText/JSON); Data fields are far smaller
	maxNameLen   = 255
	maxDocTypeLn = 140
	maxDocBytes  = 4 << 20 // 4 MB per document body
	maxFields    = 512
	maxChildRows = 5_000
	defaultLimit = 100
	maxLimit     = 1000
)

// fieldnameRe is the safe identifier a fieldname must match. Fieldnames are used
// verbatim in SQLite json paths ('$.'||fieldname is BOUND, not interpolated, but
// this is defense-in-depth) and as JSON keys, so they are strict snake tokens.
var fieldnameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// docTypeNameRe is the safe identifier a DocType or document name must match. It
// permits the Frappe-ish label set (letters, digits, space, dash, dot, underscore)
// so "Sales Invoice" or "INV-2026-0001" are legal, while excluding path/quote/
// control characters. Names are always BOUND parameters in SQL — never
// interpolated — so this is a well-formedness gate, not the injection defense.
var docTypeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]*$`)

// DocField is one field in a DocType, faithful to Frappe's DocField. JSON tags
// are the wire contract the sibling app lanes (CMS/ERP/CRM) and the generic
// @hanzo/ui DocType renderer build to.
type DocField struct {
	Fieldname string `json:"fieldname"`
	Fieldtype string `json:"fieldtype"`
	Label     string `json:"label,omitempty"`
	Reqd      bool   `json:"reqd,omitempty"`
	// Options is fieldtype-dependent: Select → newline-separated choices; Link →
	// target DocType name; Table → child DocType name. Empty otherwise.
	Options string `json:"options,omitempty"`
	// Default is applied when a create omits the field (before validation).
	Default  string `json:"default,omitempty"`
	Unique   bool   `json:"unique,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	Hidden   bool   `json:"hidden,omitempty"`
	// InListView flags the field for the generic list UI (metadata only).
	InListView bool `json:"inListView,omitempty"`
	// FetchFrom auto-populates this field from a linked document, in the form
	// "link_fieldname.source_fieldname": on save the engine loads the doc named by
	// the Link field `link_fieldname` and copies its `source_fieldname` here.
	FetchFrom string `json:"fetchFrom,omitempty"`
}

// DocPerm is a role's rights on a DocType, faithful to Frappe's DocPerm. The
// engine enforces these per-org against the caller's resolved roles.
type DocPerm struct {
	Role   string `json:"role"`
	Read   bool   `json:"read,omitempty"`
	Write  bool   `json:"write,omitempty"`
	Create bool   `json:"create,omitempty"`
	Delete bool   `json:"delete,omitempty"`
	Submit bool   `json:"submit,omitempty"`
	Cancel bool   `json:"cancel,omitempty"`
}

// DocType is a metadata definition. It is per-org data: the same DocType `name`
// may exist independently in many orgs with different fields, and one org's
// definition is invisible to another.
type DocType struct {
	Name          string `json:"name"`
	Module        string `json:"module,omitempty"`
	IsSingle      bool   `json:"isSingle,omitempty"`
	IsSubmittable bool   `json:"isSubmittable,omitempty"`
	// Autoname is the naming rule (see naming.go): "" or "hash" → random id;
	// "field:fieldname" → value of that field; "prompt" → client supplies name;
	// any other value is a series pattern, e.g. "INV-.YYYY.-.#####".
	Autoname   string     `json:"autoname,omitempty"`
	TitleField string     `json:"titleField,omitempty"`
	Fields     []DocField `json:"fields"`
	Perms      []DocPerm  `json:"permissions,omitempty"`
	CreatedAt  int64      `json:"createdAt,omitempty"`
	UpdatedAt  int64      `json:"updatedAt,omitempty"`
}

// field returns the named DocField and whether it exists.
func (d *DocType) field(name string) (DocField, bool) {
	for _, f := range d.Fields {
		if f.Fieldname == name {
			return f, true
		}
	}
	return DocField{}, false
}

// selectChoices splits a Select field's newline-separated options into the
// allowed values (Frappe semantics: a leading blank line means "" is allowed).
func (f DocField) selectChoices() []string {
	raw := strings.Split(f.Options, "\n")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		out = append(out, strings.TrimRight(s, "\r"))
	}
	return out
}

// Validate checks a DocType definition is well-formed. A malformed schema is
// rejected at define time (400) so the document validator can trust it later —
// validation is done once, at the boundary, not re-litigated per document.
func (d *DocType) Validate() error {
	name := strings.TrimSpace(d.Name)
	if name == "" {
		return fmt.Errorf("doctype name is required")
	}
	if len(name) > maxDocTypeLn {
		return fmt.Errorf("doctype name too long (max %d)", maxDocTypeLn)
	}
	if !docTypeNameRe.MatchString(name) {
		return fmt.Errorf("doctype name %q has invalid characters", name)
	}
	if reservedDocTypeNames[strings.ToLower(name)] {
		return fmt.Errorf("doctype name %q is reserved", name)
	}
	if len(d.Fields) == 0 {
		return fmt.Errorf("doctype must declare at least one field")
	}
	if len(d.Fields) > maxFields {
		return fmt.Errorf("too many fields (max %d)", maxFields)
	}
	seen := make(map[string]bool, len(d.Fields))
	for i, f := range d.Fields {
		if !fieldnameRe.MatchString(f.Fieldname) {
			return fmt.Errorf("field %d: fieldname %q must match %s", i, f.Fieldname, fieldnameRe)
		}
		if seen[f.Fieldname] {
			return fmt.Errorf("duplicate fieldname %q", f.Fieldname)
		}
		seen[f.Fieldname] = true
		if !validFieldtypes[f.Fieldtype] {
			return fmt.Errorf("field %q: unknown fieldtype %q", f.Fieldname, f.Fieldtype)
		}
		if needsOptions[f.Fieldtype] && strings.TrimSpace(f.Options) == "" {
			return fmt.Errorf("field %q: fieldtype %s requires options", f.Fieldname, f.Fieldtype)
		}
		if f.FetchFrom != "" {
			src, _, ok := strings.Cut(f.FetchFrom, ".")
			if !ok || strings.TrimSpace(src) == "" {
				return fmt.Errorf("field %q: fetchFrom must be \"link_field.source_field\"", f.Fieldname)
			}
		}
	}
	// FetchFrom + autoname field: references must point at declared fields.
	if src := autonameField(d.Autoname); src != "" {
		if _, ok := d.field(src); !ok {
			return fmt.Errorf("autoname field:%s references unknown field", src)
		}
	}
	if d.TitleField != "" {
		if _, ok := d.field(d.TitleField); !ok {
			return fmt.Errorf("titleField %q is not a declared field", d.TitleField)
		}
	}
	for _, f := range d.Fields {
		if f.FetchFrom == "" {
			continue
		}
		link, _, _ := strings.Cut(f.FetchFrom, ".")
		lf, ok := d.field(link)
		if !ok || lf.Fieldtype != FieldLink {
			return fmt.Errorf("field %q: fetchFrom source %q must be a Link field", f.Fieldname, link)
		}
	}
	for _, p := range d.Perms {
		if strings.TrimSpace(p.Role) == "" {
			return fmt.Errorf("permission with empty role")
		}
	}
	return nil
}

// normalize trims the DocType's identifying strings in place and defaults empty
// slices so the stored/returned shape is canonical.
//
// SECURE BY DEFAULT: when no permissions are declared, seed the System Manager
// grant so a doctype is NEVER silently permless. A permless doctype is thus
// governed by the org owner (a System Manager) and closed to everyone else — the
// generic UI and audit always see an explicit permission set, and the enforcement
// (permission.can) is default-deny for a role-less member.
func (d *DocType) normalize() {
	d.Name = strings.TrimSpace(d.Name)
	d.Module = strings.TrimSpace(d.Module)
	d.Autoname = strings.TrimSpace(d.Autoname)
	if d.Fields == nil {
		d.Fields = []DocField{}
	}
	if len(d.Perms) == 0 {
		d.Perms = []DocPerm{{
			Role: RoleSystemManager,
			Read: true, Write: true, Create: true, Delete: true, Submit: true, Cancel: true,
		}}
	}
}
