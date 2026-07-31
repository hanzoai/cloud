// Package framework mounts the Hanzo DocType engine into the cloud binary.
//
// The engine itself is github.com/hanzoai/framework, built on the metadata
// model github.com/hanzoai/doctype. Neither knows what HTTP is. This package is
// the ADAPTER — the "place" in the engine's layering:
//
//	doctype    the VALUE  — schema, coercion, naming, permission calculus.
//	framework  the ENGINE — store, hooks, leases, operations. No transport.
//	this pkg   the PLACE  — /v1/framework/* over zip, cek-encrypted storage,
//	                        principal-derived tenancy, engine Code → HTTP status.
//
// It does four things and nothing else:
//
//  1. opens the engine with cloud's storage policy (cek, encrypted at rest);
//  2. turns a validated request principal into an engine Caller;
//  3. binds each engine operation to a TYPED OP and maps the error Code;
//  4. re-exports the engine vocabulary so the app lanes (cms, erp, help,
//     knowledge, content, guide) keep ONE import and compile unchanged.
//
// (3) is a typed op — zip.Get[In, Out] and friends — for every route but the two
// document WRITES: one registry entry, and the REST route, the OpenAPI operation,
// the MCP tool, the CLI command and the generated SDK method all follow from it.
// The two exceptions are in Mount, with the reason.
//
// The engine enforces permissions itself, so there is no authorization logic
// here — a second copy would be a second answer.
package framework

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/cek"
	engine "github.com/hanzoai/framework"
	"github.com/zap-proto/zip"
)

// state is this subsystem's data: the engine it mounted.
type state struct{ eng *engine.Engine }

// mounted is the active service. It exists so Shutdown can close the engine and
// so the in-process API below (which the app lanes call with no request in
// hand) can reach it — the composition root is the right place for that global,
// not the engine.
var mounted *cloud.Service[state]

// Mount wires the framework surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("framework.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("framework.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("framework.Mount: empty DataDir")
	}
	log := deps.Logger.New("subsystem", "framework")

	// cek is CLOUD's storage policy — encrypted at rest under a KMS-held master
	// key. The engine takes it as an opener rather than importing it, so the
	// same engine runs unencrypted in a test or a standalone app.
	// The engine opens the deployment's own DocType stores under deps.DataDir, not a
	// tenant's, so they key under the platform principal. A per-org DocType store would
	// come through OrgDB, which names its owner.
	eng, err := engine.Open(engine.Config{
		Dir:    deps.DataDir,
		OpenDB: func(path string) (*sql.DB, error) { return cek.Open(cek.Global, path) },
		Logger: log,
	})
	if err != nil {
		return fmt.Errorf("framework.Mount: %w", err)
	}

	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "framework"), State: state{eng: eng}}
	mounted = s

	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("framework.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}

	g := app.Group("/v1/framework")

	// The bridges, FIRST: a typed op receives only a context, so every request
	// fact its signature drops has to be parked there — the validated org by
	// cloud.Bridge, the two header-only identity facts by bridgeFacts. fiber runs
	// middleware in registration order, so one installed after its leaves never
	// runs; and the group bounds them to the subtree this subsystem serves.
	g.Use(cloud.Bridge(), bridgeFacts)

	o := ops{s: s}

	// The typed ops register on the APP with the full path spelled, never on the
	// group: cmd/zipdoc keys its extraction on the path LITERAL at the call site
	// while the registry keys on the group-joined one, so a group-relative
	// declaration lifts prose under a key no operation ever looks up — the doc
	// comments are written, generated, and silently never rendered.
	//
	// STATIC routes register BEFORE the generic /:doctype routes so Fiber's
	// first-match scan resolves them unambiguously; their names are also
	// reserved DocType names, so no document route can shadow them.
	zip.Get(zapp, "/v1/framework/summary", o.summary)

	zip.Get(zapp, "/v1/framework/doctypes", o.listDocTypes)
	zip.Post(zapp, "/v1/framework/doctypes", o.createDocType, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/framework/doctypes/:name", o.getDocType)
	zip.Put(zapp, "/v1/framework/doctypes/:name", o.replaceDocType)
	zip.Delete(zapp, "/v1/framework/doctypes/:name", o.deleteDocType)

	zip.Get(zapp, "/v1/framework/roles", o.listRoles)
	zip.Post(zapp, "/v1/framework/roles", o.assignRole, zip.WithStatus(http.StatusCreated))
	zip.Delete(zapp, "/v1/framework/roles/:user/:role", o.revokeRole)

	zip.Get(zapp, "/v1/framework/modules", o.listModules)
	zip.Get(zapp, "/v1/framework/modules/:module", o.getModule)
	zip.Post(zapp, "/v1/framework/modules/:module/install", o.installModule)

	// GENERIC metadata-driven document surface.
	//
	// The two WRITES stay raw handlers, and deliberately: their request body IS
	// the document's own field data — a flat JSON object whose properties the
	// DocType defines at run time. A typed op's schema is REFLECTED off its In
	// type, and no Go struct both accepts that body verbatim and describes it, so
	// typing them would publish a request schema naming the two path segments and
	// nothing else — an SDK method that cannot send a document. zip needs an
	// open-object input (`additionalProperties: true`) before these convert; a
	// schema that lies is worse than the route-only entry they carry today.
	zip.Get(zapp, "/v1/framework/:doctype", o.listDocuments)
	g.Post("/:doctype", cloud.Handle(s, createDocument))
	zip.Get(zapp, "/v1/framework/:doctype/:name", o.getDocument)
	g.Put("/:doctype/:name", cloud.Handle(s, updateDocument))
	zip.Delete(zapp, "/v1/framework/:doctype/:name", o.deleteDocument)
	zip.Post(zapp, "/v1/framework/:doctype/:name/submit", o.submitDocument)
	zip.Post(zapp, "/v1/framework/:doctype/:name/cancel", o.cancelDocument)

	log.Info("framework mounted", "brand", deps.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Shutdown closes the engine. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.eng.Close()
	mounted = nil
	return err
}

// ---- the two boundaries ----

// caller turns a validated request principal into an engine Caller.
//
// This is the ONE tenant-derivation path. principal.Org returns an org only for
// a VALIDATED principal (a gateway/BFF-minted X-User-Id from a verified IAM
// credential); a forged X-Org-Id with no validated principal yields nothing, so
// the engine is handed an empty Caller and refuses 403 before touching a store.
func caller(c *zip.Ctx) engine.Caller {
	org, ok := principal.Org(c)
	if !ok {
		return engine.Caller{}
	}
	return engine.Caller{Org: org, User: c.User(), IsAdmin: c.IsAdmin()}
}

// facts are the identity values a TYPED op needs that cloud.Bridge does not
// carry: the validated user id and platform admin-ness, both header-only. The
// ORG is deliberately absent — cloud.Bridge already parks it and one fact with
// two carriers is one carrier too many.
type facts struct {
	// user is the validated principal's id. CLONED at the bridge: c.User() is a
	// zero-copy view into the reused fasthttp request buffer, and the engine
	// writes this value into fw_roles when it seeds an org's first manager, so it
	// outlives the request.
	user string
	// admin is c.IsAdmin() — the PLATFORM SuperAdmin bit, which the engine's
	// permission calculus lets bypass per-DocType rights.
	admin bool
}

// factsKey names the request-scoped slot bridgeFacts parks facts under.
// Unexported zero-size type: unforgeable from another package.
type factsKey struct{}

// bridgeFacts carries the header-only halves of the engine Caller onto the
// request context — the twin of the org cloud.Bridge parks. A request that never
// passed the bridge reads back the zero facts, so absence is "no user, not an
// admin", which the engine refuses before touching a store.
func bridgeFacts(c *zip.Ctx) error {
	c.SetContext(context.WithValue(c.Context(), factsKey{}, facts{
		user:  strings.Clone(c.User()),
		admin: c.IsAdmin(),
	}))
	return c.Continue()
}

// callerOf is caller() across the typed-op seam, and the SAME decision: the org
// comes from principal.OrgFrom (what principal.Org decided, parked by
// cloud.Bridge) and the rest from bridgeFacts. No validated org means the ZERO
// Caller — exactly what caller() returns for an unvalidated principal — which
// the engine refuses 403 before touching a store.
//
// FAIL CLOSED OFF THE HTTP PATH. An MCP tools/call and a CLI LocalInvoke pass no
// bridge, so both reads come back empty and every op here refuses. That is the
// handler's own gate, with no second gate to keep in sync.
func callerOf(ctx context.Context) engine.Caller {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return engine.Caller{}
	}
	f, _ := ctx.Value(factsKey{}).(facts)
	return engine.Caller{Org: org, User: f.user, IsAdmin: f.admin}
}

// fail maps an engine error to the HTTP status its Code means. The engine
// decides WHAT a failure is; this decides how THIS transport says it. Every
// handler funnels through here, so no route can answer a different status for
// the same condition.
func fail(err error, notFound string) error {
	switch engine.Classify(err) {
	case engine.CodeForbidden:
		return zip.ErrForbidden(strings.TrimPrefix(err.Error(), "framework: "))
	case engine.CodeNotFound:
		if notFound == "" {
			notFound = "not found"
		}
		return zip.ErrNotFound(notFound)
	case engine.CodeConflict:
		return zip.Errorf(http.StatusConflict, "%s", err.Error())
	case engine.CodeInvalid:
		return zip.ErrBadRequest(err.Error())
	case engine.CodeRejected:
		return zip.Errorf(http.StatusUnprocessableEntity, "%s", err.Error())
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// decodeSeg percent-decodes a URL path segment, so a segment naming a record
// with reserved characters — a space ("Sales Invoice", "System Manager") arrives
// as %20 — is matched against its STORED value, not its raw encoding. The router
// (zip over fasthttp) runs with Fiber's default UnescapePath:false, so both
// readers below hand segments back verbatim. A malformed escape is left as-is:
// it simply won't match a stored name (an honest 404), never a panic. TrimSpace
// mirrors the engine's naming rules.
//
// It is a function of the SEGMENT, not of the request, because two readers need
// it: pathParam for the raw handlers and the typed ops for the value zip's
// bindURL wrote onto their In. One rule, two readers — never two rules.
func decodeSeg(raw string) string {
	if dec, err := url.PathUnescape(raw); err == nil {
		raw = dec
	}
	return strings.TrimSpace(raw)
}

// pathParam reads a URL path parameter and decodes it.
func pathParam(c *zip.Ctx, name string) string { return decodeSeg(c.Param(name)) }

// body binds a document request body through the engine's size guard, so every
// host enforces the same bound.
func body(c *zip.Ctx) (map[string]any, error) {
	m, err := engine.BindDocument(c.Body())
	if err != nil {
		return nil, fail(err, "")
	}
	return m, nil
}

// ---- the typed ops ----
//
// ops binds the mounted service so every op can be a METHOD VALUE: a
// TypedHandler is func(context.Context, *In) (*Out, error) — no parameter for
// the service — and a method value is also the only bound form cmd/zipdoc can
// lift prose from. It carries STATE and no logic: each op resolves its Caller
// and calls the one engine operation it names, exactly as the raw handler did.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name, so a defined type here would publish
// "200 with a body" about a route that answers 204 with none.
type noContent = struct{}

// ---- DocType registry ----

// docTypeRef addresses one DocType by the name in the URL.
type docTypeRef struct {
	// Name is the DocType's name, from the path. A name containing a space
	// ("Sales Invoice") arrives percent-encoded and is decoded before it is
	// matched against the stored one.
	Name string `json:"name"`
}

// docTypeList is a page of DocType definitions.
type docTypeList struct {
	// Data is every DocType defined in the caller's org.
	Data []DocType `json:"data"`
}

// createDocType defines a DocType in the caller's org: the metadata that gives a
// document surface its fields, its naming rule, whether it has a submit/cancel
// lifecycle, and which role may do what to it. Manager-only — on a fresh org the
// first caller to administer it is seeded as its System Manager, after which
// only a System Manager (or a platform admin) may define. Answers 201.
//
// Example: {"name": "Task", "autoname": "TASK-.#####", "fields": [{"fieldname": "subject", "fieldtype": "Data", "reqd": true}]}
func (o ops) createDocType(ctx context.Context, in *DocType) (*DocType, error) {
	saved, err := o.s.State.eng.DefineDocType(ctx, callerOf(ctx), *in)
	if err != nil {
		return nil, fail(err, "")
	}
	return &saved, nil
}

// listDocTypes returns every DocType defined in the caller's org. Another
// tenant's definitions are never included: the org is part of the store key.
func (o ops) listDocTypes(ctx context.Context, _ *noInput) (*docTypeList, error) {
	rows, err := o.s.State.eng.ListDocTypes(ctx, callerOf(ctx))
	if err != nil {
		return nil, fail(err, "")
	}
	return &docTypeList{Data: rows}, nil
}

// getDocType returns one DocType definition — its fields, naming rule,
// permissions and lifecycle flags. Scoped to the caller's org, so another
// tenant's DocType of the same name is simply not found.
//
// Example: {"name": "Task"}
func (o ops) getDocType(ctx context.Context, in *docTypeRef) (*DocType, error) {
	dt, err := o.s.State.eng.DocTypeOf(ctx, callerOf(ctx), decodeSeg(in.Name))
	if err != nil {
		return nil, fail(err, "doctype not found")
	}
	return &dt, nil
}

// replaceDocType replaces a DocType definition wholesale (PUT semantics): the
// stored definition becomes the body. The name in the URL is authoritative over
// the body's, and documents already stored under the DocType are left intact.
// Manager-only.
//
// Example: {"name": "Task", "fields": [{"fieldname": "subject", "fieldtype": "Data"}]}
func (o ops) replaceDocType(ctx context.Context, in *DocType) (*DocType, error) {
	saved, err := o.s.State.eng.ReplaceDocType(ctx, callerOf(ctx), decodeSeg(in.Name), *in)
	if err != nil {
		return nil, fail(err, "doctype not found")
	}
	return &saved, nil
}

// deleteDocType removes a DocType and every document stored under it. The
// definition and its data go together — a document with no schema can be neither
// validated nor read back — so there is no undo. Manager-only. Answers 204.
//
// Example: {"name": "Task"}
func (o ops) deleteDocType(ctx context.Context, in *docTypeRef) (*noContent, error) {
	if err := o.s.State.eng.DeleteDocType(ctx, callerOf(ctx), decodeSeg(in.Name)); err != nil {
		return nil, fail(err, "doctype not found")
	}
	return nil, nil
}

// ---- Roles ----

// roleRef addresses one role assignment by the (user, role) pair in the URL.
type roleRef struct {
	// User is the assignee whose grant is being revoked, from the path.
	User string `json:"user"`
	// Role is the role to revoke, from the path. A role name containing a space
	// ("System Manager") arrives percent-encoded and is decoded before it is
	// matched against the stored assignment.
	Role string `json:"role"`
}

// roleList is a page of role assignments.
type roleList struct {
	// Data is every (user, role) assignment in the caller's org.
	Data []Role `json:"data"`
}

// listRoles returns every (user, role) assignment in the caller's org. Roles are
// what DocType permissions are written against, so this is the grant table the
// permission calculus resolves a member's rights from.
func (o ops) listRoles(ctx context.Context, _ *noInput) (*roleList, error) {
	rows, err := o.s.State.eng.ListRoles(ctx, callerOf(ctx))
	if err != nil {
		return nil, fail(err, "")
	}
	return &roleList{Data: rows}, nil
}

// assignRole grants one user one role in the caller's org — how a member gains
// rights on a DocType, since permissions name roles and never users.
// Manager-only. Answers 201.
//
// Example: {"user": "u_alice", "role": "System Manager"}
func (o ops) assignRole(ctx context.Context, in *Role) (*Role, error) {
	saved, err := o.s.State.eng.AssignRole(ctx, callerOf(ctx), in.User, in.Role)
	if err != nil {
		return nil, fail(err, "")
	}
	return &saved, nil
}

// revokeRole removes one (user, role) grant in the caller's org. Manager-only.
// Answers 204; a grant that does not exist is not found.
//
// Example: {"user": "u_alice", "role": "System Manager"}
func (o ops) revokeRole(ctx context.Context, in *roleRef) (*noContent, error) {
	err := o.s.State.eng.RevokeRole(ctx, callerOf(ctx), decodeSeg(in.User), decodeSeg(in.Role))
	if err != nil {
		return nil, fail(err, "role assignment not found")
	}
	return nil, nil
}

// ---- Modules (app-lane fixtures) ----

// moduleRef addresses one app lane by the module name in the URL.
type moduleRef struct {
	// Module is the lane's registered name ("cms", "erp"), from the path.
	Module string `json:"module"`
}

// moduleList is a page of the app lanes this deployment carries.
type moduleList struct {
	// Data is every module compiled into this binary, with the DocTypes it installs.
	Data []engine.ModuleInfo `json:"data"`
}

// listModules returns every app lane compiled into this deployment and the
// DocTypes each one installs. It describes the BINARY, not the org: what a given
// org has actually installed is the per-module state below.
func (o ops) listModules(ctx context.Context, _ *noInput) (*moduleList, error) {
	mods, err := o.s.State.eng.Modules(ctx, callerOf(ctx))
	if err != nil {
		return nil, fail(err, "")
	}
	return &moduleList{Data: mods}, nil
}

// getModule returns one app lane's install state for the caller's org: the
// DocTypes the lane declares, and which of them already exist in the org. That
// is the honest "set up" versus "installed" answer a console renders.
//
// Example: {"module": "cms"}
func (o ops) getModule(ctx context.Context, in *moduleRef) (*engine.ModuleState, error) {
	st, err := o.s.State.eng.ModuleOf(ctx, callerOf(ctx), in.Module)
	if err != nil {
		return nil, fail(err, "unknown module: "+in.Module)
	}
	return &st, nil
}

// installModule creates an app lane's DocTypes in the caller's org. Idempotent
// and create-if-absent: a DocType the org already has is reported as existing
// and never replaced, so re-installing cannot clobber a definition the org has
// since edited. Manager-only.
//
// Example: {"module": "cms"}
func (o ops) installModule(ctx context.Context, in *moduleRef) (*engine.Install, error) {
	res, err := o.s.State.eng.InstallModule(ctx, callerOf(ctx), in.Module)
	if err != nil {
		return nil, fail(err, "unknown module: "+in.Module)
	}
	return &res, nil
}

// ---- Documents ----

// docView is a document as it goes over the wire: its own field data overlaid
// with the managed envelope keys (name, doctype, docstatus, createdAt,
// updatedAt), Password fields replaced by a fixed redaction marker. It is an
// OPEN object because the field set is metadata — the DocType defines it at run
// time — so there is no fixed list of properties to declare.
type docView map[string]any

// documentList is a page of one DocType's documents.
type documentList struct {
	// Data is the matching documents, newest-updated first unless order_by said
	// otherwise, each projected to the requested fields plus the envelope keys.
	Data []docView `json:"data"`
}

// docRef addresses one document by the (doctype, name) pair in the URL.
type docRef struct {
	// DocType is the document's DocType, from the path.
	DocType string `json:"doctype"`
	// Name is the document's name — its key within the DocType — from the path.
	// A name containing a space arrives percent-encoded and is decoded before it
	// is matched against the stored one.
	Name string `json:"name"`
}

// listDocumentsIn lists one DocType's documents. Every filter rides in the URL.
type listDocumentsIn struct {
	// DocType is the DocType to list, from the path.
	DocType string `json:"doctype"`
	// Filters is a JSON object of equality matches, e.g. {"priority":"High"}.
	// Every key must be a field the DocType declares (or the managed name /
	// docstatus); an undeclared one is refused rather than silently ignored.
	Filters string `json:"filters"`
	// Fields projects the response to a subset — a JSON array ["a","b"] or a
	// comma list "a,b". The envelope keys are always returned.
	Fields string `json:"fields"`
	// OrderBy is "<field> [asc|desc]". Empty means most-recently-updated first.
	OrderBy string `json:"order_by"`
	// Limit caps the rows returned. Anything that is not a positive integer
	// leaves the engine's default in place.
	Limit string `json:"limit"`
}

// listDocuments returns the caller org's documents of one DocType, filtered,
// ordered and projected by the query. The DocType is resolved FIRST — through
// the same permission gate the list itself uses — because the query is validated
// against its schema: a filter, sort or field name the DocType does not declare
// is refused rather than reaching the store.
//
// Example: {"doctype": "Task", "filters": "{\"priority\":\"High\"}", "order_by": "estimate asc", "limit": "20"}
func (o ops) listDocuments(ctx context.Context, in *listDocumentsIn) (*documentList, error) {
	cl := callerOf(ctx)
	dtName := decodeSeg(in.DocType)
	dt, err := o.s.State.eng.DocTypeOf(ctx, cl, dtName)
	if err != nil {
		return nil, fail(err, "doctype not found")
	}
	opts, fields, err := engine.ParseListQuery(&dt, engine.ListQuery{
		Filters: in.Filters,
		Fields:  in.Fields,
		OrderBy: in.OrderBy,
		Limit:   in.Limit,
	})
	if err != nil {
		return nil, fail(err, "")
	}
	docs, err := o.s.State.eng.ListDocuments(ctx, cl, dtName, opts)
	if err != nil {
		return nil, fail(err, "doctype not found")
	}
	out := make([]docView, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Wire(fields))
	}
	return &documentList{Data: out}, nil
}

// getDocument returns one document by name, with Password fields redacted.
//
// Example: {"doctype": "Task", "name": "TASK-00001"}
func (o ops) getDocument(ctx context.Context, in *docRef) (*docView, error) {
	doc, err := o.s.State.eng.GetDocument(ctx, callerOf(ctx), decodeSeg(in.DocType), decodeSeg(in.Name))
	if err != nil {
		return nil, fail(err, "document not found")
	}
	v := docView(doc.Wire(nil))
	return &v, nil
}

// deleteDocument removes one document, after its on_trash hooks agree. A
// SUBMITTED document cannot be deleted — cancel it first. Answers 204.
//
// Example: {"doctype": "Task", "name": "TASK-00001"}
func (o ops) deleteDocument(ctx context.Context, in *docRef) (*noContent, error) {
	err := o.s.State.eng.DeleteDocument(ctx, callerOf(ctx), decodeSeg(in.DocType), decodeSeg(in.Name))
	if err != nil {
		return nil, fail(err, "document not found")
	}
	return nil, nil
}

// submitDocument moves a draft to submitted (docstatus 0 → 1) after its
// on_submit hooks agree. A submitted document is IMMUTABLE: further writes and
// deletes are refused until it is cancelled. Only a submittable DocType has this
// lifecycle; any other docstatus is an illegal transition.
//
// Example: {"doctype": "Task", "name": "TASK-00001"}
func (o ops) submitDocument(ctx context.Context, in *docRef) (*docView, error) {
	doc, err := o.s.State.eng.Submit(ctx, callerOf(ctx), decodeSeg(in.DocType), decodeSeg(in.Name))
	if err != nil {
		return nil, fail(err, "document not found")
	}
	v := docView(doc.Wire(nil))
	return &v, nil
}

// cancelDocument moves a submitted document to cancelled (docstatus 1 → 2) after
// its on_cancel hooks agree. Cancelling is terminal — a cancelled document
// cannot be re-submitted — but it CAN then be deleted.
//
// Example: {"doctype": "Task", "name": "TASK-00001"}
func (o ops) cancelDocument(ctx context.Context, in *docRef) (*docView, error) {
	doc, err := o.s.State.eng.Cancel(ctx, callerOf(ctx), decodeSeg(in.DocType), decodeSeg(in.Name))
	if err != nil {
		return nil, fail(err, "document not found")
	}
	v := docView(doc.Wire(nil))
	return &v, nil
}

// summaryView is how much of the DocType surface one org uses. It restates
// engine.Summary rather than exporting it because a SCHEMA NAME is fleet-global:
// "Summary" is already apps/marketing's, and one name with two shapes binds
// whichever an SDK generator read last (openapi/weave_test.go refuses it).
type summaryView struct {
	// DocTypes is how many DocTypes the org has defined.
	DocTypes int `json:"doctypes"`
	// Documents is how many documents exist across them.
	Documents int `json:"documents"`
}

// summary reports how much of the DocType surface the caller's org uses.
// It counts the DocTypes the org has defined and the documents that exist across them.
func (o ops) summary(ctx context.Context, _ *noInput) (*summaryView, error) {
	sum, err := o.s.State.eng.Summary(ctx, callerOf(ctx))
	if err != nil {
		return nil, fail(err, "")
	}
	return &summaryView{DocTypes: sum.DocTypes, Documents: sum.Documents}, nil
}

// ---- the two raw document writes ----
//
// Their body is the document's own field data, whose shape is METADATA rather
// than a Go type — see the note in Mount for why they are not ops.

func createDocument(s *cloud.Service[state], c *zip.Ctx) error {
	in, err := body(c)
	if err != nil {
		return err
	}
	doc, err := s.State.eng.CreateDocument(c.Context(), caller(c), pathParam(c, "doctype"), in)
	if err != nil {
		return fail(err, "doctype not found")
	}
	return c.JSON(http.StatusCreated, doc.Wire(nil))
}

func updateDocument(s *cloud.Service[state], c *zip.Ctx) error {
	in, err := body(c)
	if err != nil {
		return err
	}
	doc, err := s.State.eng.UpdateDocument(c.Context(), caller(c), pathParam(c, "doctype"), pathParam(c, "name"), in)
	if err != nil {
		return fail(err, "document not found")
	}
	return c.JSON(http.StatusOK, doc.Wire(nil))
}

// ---- in-process API for first-party producers ----
//
// The app lanes (knowledge's connector sync, content's generator, help's ticket
// intake) create documents off-request, inside the trust boundary. They call
// these with an org they have ALREADY resolved via principal.Org and vouch, as
// first-party Go, that the write belongs to that tenant. Every store operation
// is still physically scoped by that org.
//
// They are thin forwards to the mounted engine so a lane needs no engine handle
// and no second import. The pipeline is the engine's, so an off-request create
// runs the SAME validation and lifecycle hooks an HTTP create does.

func engineOf() (*engine.Engine, error) {
	if mounted == nil || mounted.State.eng == nil {
		return nil, fmt.Errorf("framework: not mounted")
	}
	return mounted.State.eng, nil
}

// Ingest creates a document from already-trusted field data, running the full
// validate + lifecycle-hook pipeline.
func Ingest(ctx context.Context, org, doctype string, data map[string]any, requestedName string) (Ingested, error) {
	e, err := engineOf()
	if err != nil {
		return Ingested{}, err
	}
	return e.Ingest(ctx, org, doctype, data, requestedName)
}

// UpdateData replaces an existing draft document's data, running before_save +
// after_save — the in-process twin of the HTTP PUT.
func UpdateData(ctx context.Context, org, doctype, name string, data map[string]any) error {
	e, err := engineOf()
	if err != nil {
		return err
	}
	return e.UpdateData(ctx, org, doctype, name, data)
}

// Delete removes a document after running the on_trash gate hooks.
func Delete(ctx context.Context, org, doctype, name string) error {
	e, err := engineOf()
	if err != nil {
		return err
	}
	return e.Delete(ctx, org, doctype, name)
}

// Get returns one document by name in (org, doctype).
func Get(ctx context.Context, org, doctype, name string) (Document, error) {
	e, err := engineOf()
	if err != nil {
		return Document{}, err
	}
	return e.Get(ctx, org, doctype, name)
}

// Search is the in-process, org-scoped document list.
func Search(ctx context.Context, org, doctype string, filters map[string]string, limit int) ([]Document, error) {
	e, err := engineOf()
	if err != nil {
		return nil, err
	}
	return e.Search(ctx, org, doctype, filters, limit)
}

// FindByField returns the name of the first document whose `field` equals
// `value`, or "" if none.
func FindByField(ctx context.Context, org, doctype, field, value string) (string, error) {
	e, err := engineOf()
	if err != nil {
		return "", err
	}
	return e.FindByField(ctx, org, doctype, field, value)
}

// Installed reports whether `doctype` exists in `org`.
func Installed(ctx context.Context, org, doctype string) bool {
	e, err := engineOf()
	if err != nil {
		return false
	}
	return e.Installed(ctx, org, doctype)
}

// ModuleInstalled reports whether a module's content model resolves for `org`.
func ModuleInstalled(ctx context.Context, org, module string) bool {
	e, err := engineOf()
	if err != nil {
		return false
	}
	return e.ModuleInstalled(ctx, org, module)
}

// AcquireLease takes an exclusive, TTL-bounded lease on (org, key) — the
// cross-process interlock a non-idempotent side effect (the content lane's
// channel fan-out) serializes on.
func AcquireLease(ctx context.Context, org, key string, ttl, wait time.Duration) (*Lease, bool, error) {
	e, err := engineOf()
	if err != nil {
		return nil, false, err
	}
	return e.AcquireLease(ctx, org, key, ttl, wait)
}
