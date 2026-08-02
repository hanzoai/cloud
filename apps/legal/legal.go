package legal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// maxBody bounds a legal request body. Templates + merge maps are small; a custom
// template override is capped generously.
const maxBody = 1 << 20 // 1 MiB

// routePrefix is the subtree this subsystem owns, named once so the mount and the
// body cap cannot disagree about which routes are legal's.
const routePrefix = "/v1/legal"

// state is legal's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *Store
	esign Esign
	filer Filer
	audit *audit.Recorder
}

// mounted is the process-wide handle so Shutdown can close the store.
var mounted *cloud.Service[state]

// Mount wires /v1/legal/* and opens the sealed store under {DataDir}/legal.db. The
// e-sign and filing seams default to the honest stubs; a real provider is a
// config-driven swap (the seams are provider-agnostic).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("legal.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("legal.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("legal.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("legal.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "legal.db"))
	if err != nil {
		return fmt.Errorf("legal.Mount: open store: %w", err)
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "legal"),
		State: state{store: store, esign: stubEsign{}, filer: stubFiler{}, audit: deps.Audit},
	}
	mounted = s
	routes(app, s)
	s.Log.Info("legal mounted", "brand", deps.Brand, "templates", len(Builtins()), "audit", deps.Audit != nil)
	return nil
}

// routes registers the legal surface. Everything but the signature COMPLETION is a
// typed op (typed.go) — one registry entry carrying the schema, the prose, an MCP
// tool, a CLI command and an SDK method.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/legal")
	// Bridge FIRST: a typed op receives only a context, so the validated org
	// reaches it by being parked there — never as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. fiber runs middleware in registration order, so this must precede
	// every leaf below; nesting under Serve's own Bridge is harmless (the inner
	// one is what the handler sees).
	g.Use(cloud.Bridge())
	// The 1 MiB body gate, in front of the typed ops. A typed op receives its
	// DECODED In, so a size check inside one would run after the parse it exists to
	// precede — zip refuses an oversized non-JSON body with 400 before the handler
	// is ever called. Registered after Bridge and before every leaf, because fiber
	// runs middleware in registration order.
	g.Use(bodyCap())

	zip.Get(g, "/health", o.health)
	zip.Get(g, "/templates", o.listTemplates)
	zip.Get(g, "/templates/:id", o.getTemplate)
	zip.Put(g, "/templates/:id", o.overrideTemplate)

	zip.Post(g, "/documents", o.generateDocument, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/documents", o.listDocuments)
	zip.Get(g, "/documents/:id", o.getDocument)
	zip.Post(g, "/documents/:id/sign", o.requestSign)
	// UNTYPED BY DESIGN — it DISCARDS its decode error (`_ = decode(…)` below), so
	// a caller may post an unparseable body and still drive the provider-reported
	// completion. zip's invoke refuses such a body before the handler runs, so
	// typing it would turn today's 200 into a 400. See typed.go.
	g.Post("/documents/:id/sign/complete", cloud.Handle(s, completeSign))

	zip.Post(g, "/filings", o.createFiling, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/filings", o.listFilings)
}

// Shutdown closes the store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// ---- templates ----

// templateView is the catalog projection: metadata + fields, NOT the full body (a
// list stays light; the body is fetched per template).
type templateView struct {
	ID            string   `json:"id"`
	Category      Category `json:"category"`
	Title         string   `json:"title"`
	Version       int      `json:"version"`
	Origin        string   `json:"origin"`
	CounselReview bool     `json:"counselReview"`
	Fields        []Field  `json:"fields"`
}

func toTemplateView(t Template) templateView {
	return templateView{ID: t.ID, Category: t.Category, Title: t.Title, Version: t.Version, Origin: t.Origin, CounselReview: t.CounselReview, Fields: t.Fields}
}

// ---- documents ----

// The prose for the one operation here that cannot be a typed op. Every other route
// in legal is typed and zipdoc lifts its doc comment into zipdoc_gen.go; the
// signature completion stays a raw handler (routes says why — it discards its decode
// error on purpose, and typing it would turn today's 200 into a 400), so there is no
// comment for anything to lift and the published document would carry an operationId
// and nothing else — an SDK method and a CLI command that cannot explain themselves.
// Declared through the same registry Register uses, so it renders only while the
// router actually serves the route.
func init() {
	openapi.Describe("/v1/legal/documents/:id/sign/complete", http.MethodPost,
		"Record that a generated document's signature request completed",
		"Records completion of the signature request opened over a generated document and "+
			"answers the document with a `signed` flag.\n\n"+
			"The e-sign provider's own status is consulted FIRST and is the default answer; an "+
			"explicit `signed` field in the body overrides it. That override is the whole point: "+
			"the default `manual` provider never self-completes, so a reviewer (or a real "+
			"provider's webhook) is what moves the document. A completion flips the document to "+
			"`signed`, stamps `signedAt`, and writes a `legal.document.signed` audit event; a "+
			"provider still reporting incomplete answers 200 with the document unchanged, so the "+
			"call is safe to repeat and never fabricates a signature.\n\n"+
			"Org-scoped and fails closed: a validated principal is required (403 without one), the "+
			"document is read under the caller's OWN org so another tenant's id is a 404, a "+
			"document with no open signature request is a 400, and a provider whose status call "+
			"errors is a 502.")
}

// completeSign records signature completion — a provider webhook or a reviewer signal.
// The stub never self-completes; this authenticated, audited endpoint is the signal.
func completeSign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	doc, err := s.State.store.GetDocument(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("document not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
	}
	if doc.EsignRef == "" {
		return zip.ErrBadRequest("no signature request to complete")
	}
	// A real provider's webhook drives completion; honor an explicit signal for the
	// stub. The provider's own Status is checked first.
	complete, err := s.State.esign.Status(c.Context(), org, doc.EsignRef)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "esign status failed")
	}
	var reqBody struct {
		Signed *bool `json:"signed"`
	}
	_ = decode(c, &reqBody)
	if reqBody.Signed != nil {
		complete = *reqBody.Signed
	}
	if !complete {
		return c.JSON(http.StatusOK, map[string]any{"document": docView(doc, false), "signed": false})
	}
	now := nowUnix()
	if err := s.State.store.UpdateDocumentSign(c.Context(), org, doc.ID, StatusSigned, doc.EsignProvider, doc.EsignRef, now, now); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update document: %v", err)
	}
	doc.Status, doc.UpdatedAt, doc.SignedAt = StatusSigned, now, now
	emitAudit(s, c, "legal.document.signed", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "provider": doc.EsignProvider})
	return c.JSON(http.StatusOK, map[string]any{"document": docView(doc, false), "signed": true})
}

// ---- filings ----

// ---- views ----

// docView renders a document; withBody controls whether the (sealed) rendered content
// is included — lists omit it, single reads to the owner include it.
func docView(d Document, withBody bool) map[string]any {
	v := map[string]any{
		"id":              d.ID,
		"templateId":      d.TemplateID,
		"templateVersion": d.TemplateVersion,
		"category":        d.Category,
		"title":           d.Title,
		"status":          d.Status,
		"createdAt":       d.CreatedAt,
		"updatedAt":       d.UpdatedAt,
	}
	if withBody {
		v["contentType"] = d.ContentType
		v["body"] = d.Body
	}
	if d.EsignProvider != "" {
		v["esignProvider"] = d.EsignProvider
	}
	if d.SignedAt != 0 {
		v["signedAt"] = d.SignedAt
	}
	return v
}

// ---- helpers ----

// emitAudit records a legal action on the shared tamper-evident trail. The `after`
// map carries opaque ids + template/category metadata ONLY — never the rendered
// document body (which may contain names) — and is redacted as a second layer.
func emitAudit(s *cloud.Service[state], c *zip.Ctx, action string, res audit.Resource, after map[string]any) {
	if s.State.audit == nil {
		return
	}
	org, _ := principal.Org(c)
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: c.User(), Email: c.UserEmail()},
		Action:    action,
		Resource:  res,
		Auth:      audit.AuthContext{Method: "gateway", IsAdmin: c.IsAdmin()},
		Outcome:   audit.Outcome{Result: "success", Status: 200},
		Method:    c.Method(),
		Path:      c.Path(),
		SourceIP:  clientIP(c),
		RequestID: c.RequestID(),
		After:     audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.audit.Append(c.Context(), rec); err != nil {
		s.Log.Warn("legal audit append failed", "err", err, "action", action)
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// bodyCap re-establishes decode's 1 MiB gate IN FRONT of the typed ops, for
// exactly the routes decode has always gated — and no others. A typed op receives
// its DECODED In, so a size check written inside one runs after the parse it is
// there to precede; cloud's global zip body limit is far larger, so without this
// the 413 this package has always answered would silently become a 400 about
// unparseable bytes.
//
// 403 OUTRANKS 413, because the org check has always run before decode: an
// oversized body from an unvalidated caller is refused as unauthenticated, and is
// never told how big its body may be.
func bodyCap() zip.Handler {
	return func(c *zip.Ctx) error {
		if len(c.Fiber().Body()) <= maxBody || !capped(c.Method(), strings.TrimSuffix(c.Path(), "/")) {
			return c.Continue()
		}
		if _, ok := principal.Org(c); !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
}

// capped names the routes whose request body decode() has always sized. The
// signature COMPLETION is deliberately absent: it discards its decode error, so an
// oversized body has always reached it and been ignored, and capping it here would
// turn today's 200 into a 413.
func capped(method, path string) bool {
	switch {
	case method == http.MethodPut:
		return strings.HasPrefix(path, routePrefix+"/templates/")
	case method != http.MethodPost:
		return false
	case path == routePrefix+"/documents", path == routePrefix+"/filings":
		return true
	}
	return strings.HasSuffix(path, "/sign")
}

func decode(c *zip.Ctx, v any) error {
	raw := c.Fiber().Body()
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	return c.Bind(v)
}

// clientIP is the caller's address, by the ONE rule — cloud.ClientIP. It lands in
// a durable audit record, and the LEFT-most X-Forwarded-For entry (and X-Real-Ip)
// are values the client writes: an address chosen by the party being audited is
// not evidence.
func clientIP(c *zip.Ctx) string { return cloud.ClientIP(c) }

func nowUnix() int64 { return time.Now().Unix() }

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
