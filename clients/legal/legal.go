package legal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// maxBody bounds a legal request body. Templates + merge maps are small; a custom
// template override is capped generously.
const maxBody = 1 << 20 // 1 MiB

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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("legal.Mount: nil zip.App")
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

func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/legal")
	g.Get("/health", cloud.Handle(s, health))
	g.Get("/templates", cloud.Handle(s, listTemplates))
	g.Get("/templates/:id", cloud.Handle(s, getTemplate))
	g.Put("/templates/:id", cloud.Handle(s, overrideTemplate))

	g.Post("/documents", cloud.Handle(s, generateDocument))
	g.Get("/documents", cloud.Handle(s, listDocuments))
	g.Get("/documents/:id", cloud.Handle(s, getDocument))
	g.Post("/documents/:id/sign", cloud.Handle(s, requestSign))
	g.Post("/documents/:id/sign/complete", cloud.Handle(s, completeSign))

	g.Post("/filings", cloud.Handle(s, createFiling))
	g.Get("/filings", cloud.Handle(s, listFilings))
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

// ---- health ----

func health(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "templates": len(Builtins())})
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

func listTemplates(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	cat, err := s.State.store.ResolveCatalog(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	out := make([]templateView, 0, len(cat))
	for _, t := range cat {
		out = append(out, toTemplateView(t))
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out, "disclaimer": APIDisclaimer})
}

func getTemplate(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	t, err := s.State.store.ResolveTemplate(c.Context(), org, c.Param("id"))
	if err == errNotFound {
		return zip.ErrNotFound("template not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "resolve template: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"template": t, "disclaimer": APIDisclaimer})
}

// overrideTemplate saves an org-specific version of a template (a custom NDA, say).
// The body must parse as a valid template; an override of a CounselReview builtin
// stays CounselReview (the boundary can be inherited but not dropped by an override).
func overrideTemplate(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := c.Param("id")
	var body struct {
		Category      Category `json:"category"`
		Title         string   `json:"title"`
		Body          string   `json:"body"`
		CounselReview bool     `json:"counselReview"`
		Fields        []Field  `json:"fields"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Body) == "" {
		return zip.ErrBadRequest("body is required")
	}
	t := Template{
		ID: id, Category: body.Category, Title: strings.TrimSpace(body.Title),
		CounselReview: body.CounselReview, Fields: body.Fields, Body: body.Body,
	}
	// Inherit metadata from the builtin when overriding one: a custom NDA keeps the
	// NDA category and its counsel-review posture cannot be downgraded below the
	// builtin's.
	if base, isBuiltin := builtin(id); isBuiltin {
		if t.Category == "" {
			t.Category = base.Category
		}
		if t.Title == "" {
			t.Title = base.Title
		}
		if base.CounselReview {
			t.CounselReview = true // never drop a builtin's counsel-review posture
		}
	}
	if !validCategory(t.Category) {
		return zip.ErrBadRequest("category must be formation, equity, ops, or sales")
	}
	// The counsel-review boundary is coupled to the CATEGORY, not just the builtin: a
	// formation or equity (securities) template is ALWAYS counsel-review, so a NEW org
	// template in one of those categories can never generate a securities-class
	// document without the notice.
	if counselRequired(t.Category) {
		t.CounselReview = true
	}
	if t.Title == "" {
		return zip.ErrBadRequest("title is required")
	}
	// Fail closed on an unparseable body OR one that references an UNDECLARED field
	// (which would render a silent blank), rather than storing a body that only fails
	// at generation time.
	if err := ValidateOverride(t); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	saved, err := s.State.store.SaveTemplateOverride(c.Context(), org, t)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "save override: %v", err)
	}
	emitAudit(s, c, "legal.template.override", audit.Resource{Type: "legal.template", ID: saved.ID},
		map[string]any{"templateId": saved.ID, "version": saved.Version, "category": saved.Category})
	return c.JSON(http.StatusOK, map[string]any{"template": saved, "disclaimer": APIDisclaimer})
}

// ---- documents ----

// generateDocument renders a document from a template + merge data (PURE render,
// deterministic), seals it in the org's store, and audits the generation. It fails
// closed on a missing merge field — no blank contract. The rendered body carries the
// counsel notice when the template is CounselReview.
func generateDocument(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		TemplateID string            `json:"templateId"`
		Data       map[string]string `json:"data"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.TemplateID) == "" {
		return zip.ErrBadRequest("templateId is required")
	}
	t, err := s.State.store.ResolveTemplate(c.Context(), org, body.TemplateID)
	if err == errNotFound {
		return zip.ErrNotFound("template not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "resolve template: %v", err)
	}
	rendered, err := Render(t, body.Data)
	if err != nil {
		// A missing-field / render error is the caller's — 400 with the honest reason
		// (the field names, never any secret).
		return zip.ErrBadRequest(err.Error())
	}
	id, err := genID("doc")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := nowUnix()
	doc := Document{
		ID: id, Org: org, TemplateID: t.ID, TemplateVersion: t.Version, Category: t.Category,
		Title: t.Title, ContentType: "text/markdown", Body: string(rendered),
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateDocument(c.Context(), doc); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create document: %v", err)
	}
	emitAudit(s, c, "legal.document.generate", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "templateId": t.ID, "templateVersion": t.Version, "category": t.Category})
	return c.JSON(http.StatusCreated, map[string]any{"document": docView(doc, true), "disclaimer": APIDisclaimer})
}

func listDocuments(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	docs, err := s.State.store.ListDocuments(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list documents: %v", err)
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, docView(d, false)) // list omits the body
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"data": out, "disclaimer": APIDisclaimer})
}

func getDocument(s *cloud.Service[state], c *zip.Ctx) error {
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
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"document": docView(doc, true), "disclaimer": APIDisclaimer})
}

// requestSign opens an e-signature request over a document via the esign seam.
func requestSign(s *cloud.Service[state], c *zip.Ctx) error {
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
	var body struct {
		Signers []Signer `json:"signers"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if len(body.Signers) == 0 {
		return zip.ErrBadRequest("at least one signer is required")
	}
	ref, err := s.State.esign.Request(c.Context(), org, doc.ID, doc.Title, body.Signers)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "esign request failed")
	}
	now := nowUnix()
	if err := s.State.store.UpdateDocumentSign(c.Context(), org, doc.ID, StatusOutForSig, s.State.esign.Name(), ref, now, 0); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update document: %v", err)
	}
	doc.Status, doc.EsignProvider, doc.EsignRef, doc.UpdatedAt = StatusOutForSig, s.State.esign.Name(), ref, now
	emitAudit(s, c, "legal.document.sign_requested", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "provider": doc.EsignProvider, "signers": len(body.Signers)})
	return c.JSON(http.StatusOK, map[string]any{"document": docView(doc, false), "esignRef": ref, "provider": doc.EsignProvider})
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

func createFiling(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		DocumentIDs  []string `json:"documentIds"`
		Jurisdiction string   `json:"jurisdiction"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if len(body.DocumentIDs) == 0 {
		return zip.ErrBadRequest("documentIds is required")
	}
	// Each document must belong to the org (no cross-tenant filing).
	for _, docID := range body.DocumentIDs {
		if _, err := s.State.store.GetDocument(c.Context(), org, docID); err == errNotFound {
			return zip.ErrNotFound("document not found: " + docID)
		} else if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
		}
	}
	status, note, err := s.State.filer.Submit(c.Context(), org, body.Jurisdiction, body.DocumentIDs)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "filing submit failed")
	}
	id, err := genID("filing")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := nowUnix()
	f := Filing{
		ID: id, Org: org, DocumentIDs: body.DocumentIDs, Jurisdiction: body.Jurisdiction,
		Provider: s.State.filer.Name(), Status: status, Note: note, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateFiling(c.Context(), f); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create filing: %v", err)
	}
	emitAudit(s, c, "legal.filing.create", audit.Resource{Type: "legal.filing", ID: f.ID},
		map[string]any{"filingId": f.ID, "provider": f.Provider, "status": f.Status, "documents": len(f.DocumentIDs)})
	return c.JSON(http.StatusCreated, map[string]any{"filing": f, "disclaimer": APIDisclaimer})
}

func listFilings(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	fs, err := s.State.store.ListFilings(c.Context(), org, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list filings: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": fs, "disclaimer": APIDisclaimer})
}

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

func limitOf(c *zip.Ctx) int {
	n, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	return n
}

func clientIP(c *zip.Ctx) string {
	if xff := c.Header("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	return c.Header("X-Real-Ip")
}

func nowUnix() int64 { return time.Now().Unix() }

func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
