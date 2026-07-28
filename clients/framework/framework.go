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
//  3. binds each engine operation to a route and maps the error Code;
//  4. re-exports the engine vocabulary so the app lanes (cms, erp, help,
//     knowledge, content, guide) keep ONE import and compile unchanged.
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
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/principal"
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

	g := app.Group("/v1/framework")

	// STATIC routes register BEFORE the generic /:doctype routes so Fiber's
	// first-match scan resolves them unambiguously; their names are also
	// reserved DocType names, so no document route can shadow them.
	g.Get("/summary", cloud.Handle(s, summary))

	g.Get("/doctypes", cloud.Handle(s, listDocTypes))
	g.Post("/doctypes", cloud.Handle(s, createDocType))
	g.Get("/doctypes/:name", cloud.Handle(s, getDocType))
	g.Put("/doctypes/:name", cloud.Handle(s, replaceDocType))
	g.Delete("/doctypes/:name", cloud.Handle(s, deleteDocType))

	g.Get("/roles", cloud.Handle(s, listRoles))
	g.Post("/roles", cloud.Handle(s, assignRole))
	g.Delete("/roles/:user/:role", cloud.Handle(s, revokeRole))

	g.Get("/modules", cloud.Handle(s, listModules))
	g.Get("/modules/:module", cloud.Handle(s, getModule))
	g.Post("/modules/:module/install", cloud.Handle(s, installModule))

	// GENERIC metadata-driven document surface.
	g.Get("/:doctype", cloud.Handle(s, listDocuments))
	g.Post("/:doctype", cloud.Handle(s, createDocument))
	g.Get("/:doctype/:name", cloud.Handle(s, getDocument))
	g.Put("/:doctype/:name", cloud.Handle(s, updateDocument))
	g.Delete("/:doctype/:name", cloud.Handle(s, deleteDocument))
	g.Post("/:doctype/:name/submit", cloud.Handle(s, submitDocument))
	g.Post("/:doctype/:name/cancel", cloud.Handle(s, cancelDocument))

	log.Info("framework mounted", "brand", deps.Brand)
	return nil
}

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

// pathParam reads a URL path parameter and percent-decodes it, so a segment
// naming a record with reserved characters — a space ("Sales Invoice", "System
// Manager") arrives as %20 — is matched against its STORED value, not its raw
// encoding. The router (zip over fasthttp) runs with Fiber's default
// UnescapePath:false, so c.Param hands segments back verbatim. A malformed
// escape is left as-is: it simply won't match a stored name (an honest 404),
// never a panic. TrimSpace mirrors the engine's naming rules.
func pathParam(c *zip.Ctx, name string) string {
	raw := c.Param(name)
	if dec, err := url.PathUnescape(raw); err == nil {
		raw = dec
	}
	return strings.TrimSpace(raw)
}

// body binds a document request body through the engine's size guard, so every
// host enforces the same bound.
func body(c *zip.Ctx) (map[string]any, error) {
	m, err := engine.BindDocument(c.Body())
	if err != nil {
		return nil, fail(err, "")
	}
	return m, nil
}

// ---- DocType registry handlers ----

func createDocType(s *cloud.Service[state], c *zip.Ctx) error {
	var dt DocType
	if err := c.Bind(&dt); err != nil {
		return err
	}
	saved, err := s.State.eng.DefineDocType(c.Context(), caller(c), dt)
	if err != nil {
		return fail(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func listDocTypes(s *cloud.Service[state], c *zip.Ctx) error {
	rows, err := s.State.eng.ListDocTypes(c.Context(), caller(c))
	if err != nil {
		return fail(err, "")
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getDocType(s *cloud.Service[state], c *zip.Ctx) error {
	dt, err := s.State.eng.DocTypeOf(c.Context(), caller(c), pathParam(c, "name"))
	if err != nil {
		return fail(err, "doctype not found")
	}
	return c.JSON(http.StatusOK, dt)
}

func replaceDocType(s *cloud.Service[state], c *zip.Ctx) error {
	var dt DocType
	if err := c.Bind(&dt); err != nil {
		return err
	}
	saved, err := s.State.eng.ReplaceDocType(c.Context(), caller(c), pathParam(c, "name"), dt)
	if err != nil {
		return fail(err, "doctype not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func deleteDocType(s *cloud.Service[state], c *zip.Ctx) error {
	if err := s.State.eng.DeleteDocType(c.Context(), caller(c), pathParam(c, "name")); err != nil {
		return fail(err, "doctype not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- Role handlers ----

func listRoles(s *cloud.Service[state], c *zip.Ctx) error {
	rows, err := s.State.eng.ListRoles(c.Context(), caller(c))
	if err != nil {
		return fail(err, "")
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func assignRole(s *cloud.Service[state], c *zip.Ctx) error {
	var in Role
	if err := c.Bind(&in); err != nil {
		return err
	}
	saved, err := s.State.eng.AssignRole(c.Context(), caller(c), in.User, in.Role)
	if err != nil {
		return fail(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func revokeRole(s *cloud.Service[state], c *zip.Ctx) error {
	err := s.State.eng.RevokeRole(c.Context(), caller(c), pathParam(c, "user"), pathParam(c, "role"))
	if err != nil {
		return fail(err, "role assignment not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- Module (app-lane fixture) handlers ----

func listModules(s *cloud.Service[state], c *zip.Ctx) error {
	mods, err := s.State.eng.Modules(c.Context(), caller(c))
	if err != nil {
		return fail(err, "")
	}
	out := make([]map[string]any, 0, len(mods))
	for _, m := range mods {
		out = append(out, map[string]any{"module": m.Module, "doctypes": m.DocTypes})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func getModule(s *cloud.Service[state], c *zip.Ctx) error {
	st, err := s.State.eng.ModuleOf(c.Context(), caller(c), c.Param("module"))
	if err != nil {
		return fail(err, "unknown module: "+c.Param("module"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"module": st.Module, "doctypes": st.DocTypes, "installed": st.Installed,
	})
}

func installModule(s *cloud.Service[state], c *zip.Ctx) error {
	res, err := s.State.eng.InstallModule(c.Context(), caller(c), c.Param("module"))
	if err != nil {
		return fail(err, "unknown module: "+c.Param("module"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"module": res.Module, "created": res.Created, "existing": res.Existing,
	})
}

// ---- Document handlers ----

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

func listDocuments(s *cloud.Service[state], c *zip.Ctx) error {
	cl := caller(c)
	dtName := pathParam(c, "doctype")
	// The list query is validated against the schema, so the DocType is resolved
	// first — through the SAME permission gate the list itself uses.
	dt, err := s.State.eng.DocTypeOf(c.Context(), cl, dtName)
	if err != nil {
		return fail(err, "doctype not found")
	}
	opts, fields, err := engine.ParseListQuery(&dt, engine.ListQuery{
		Filters: c.Query("filters"),
		Fields:  c.Query("fields"),
		OrderBy: c.Query("order_by"),
		Limit:   c.Query("limit"),
	})
	if err != nil {
		return fail(err, "")
	}
	docs, err := s.State.eng.ListDocuments(c.Context(), cl, dtName, opts)
	if err != nil {
		return fail(err, "doctype not found")
	}
	out := make([]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Wire(fields))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

func getDocument(s *cloud.Service[state], c *zip.Ctx) error {
	doc, err := s.State.eng.GetDocument(c.Context(), caller(c), pathParam(c, "doctype"), pathParam(c, "name"))
	if err != nil {
		return fail(err, "document not found")
	}
	return c.JSON(http.StatusOK, doc.Wire(nil))
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

func deleteDocument(s *cloud.Service[state], c *zip.Ctx) error {
	err := s.State.eng.DeleteDocument(c.Context(), caller(c), pathParam(c, "doctype"), pathParam(c, "name"))
	if err != nil {
		return fail(err, "document not found")
	}
	return c.NoContent(http.StatusNoContent)
}

func submitDocument(s *cloud.Service[state], c *zip.Ctx) error {
	doc, err := s.State.eng.Submit(c.Context(), caller(c), pathParam(c, "doctype"), pathParam(c, "name"))
	if err != nil {
		return fail(err, "document not found")
	}
	return c.JSON(http.StatusOK, doc.Wire(nil))
}

func cancelDocument(s *cloud.Service[state], c *zip.Ctx) error {
	doc, err := s.State.eng.Cancel(c.Context(), caller(c), pathParam(c, "doctype"), pathParam(c, "name"))
	if err != nil {
		return fail(err, "document not found")
	}
	return c.JSON(http.StatusOK, doc.Wire(nil))
}

func summary(s *cloud.Service[state], c *zip.Ctx) error {
	sum, err := s.State.eng.Summary(c.Context(), caller(c))
	if err != nil {
		return fail(err, "")
	}
	return c.JSON(http.StatusOK, map[string]any{"doctypes": sum.DocTypes, "documents": sum.Documents})
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
