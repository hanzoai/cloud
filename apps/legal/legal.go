package legal

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
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
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/apps/principal"
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
	if err := routes(app, s); err != nil {
		return err
	}
	s.Log.Info("legal mounted", "brand", deps.Brand, "templates", len(Builtins()), "audit", deps.Audit != nil)
	return nil
}

func routes(app cloud.Router, s *cloud.Service[state]) error {
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("legal.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}
	o := ops{s: s}
	// The bridge FIRST — fiber runs middleware in registration order — bounded to
	// legal's own subtree; every org-scoped op below resolves its tenant through it.
	app.Group("/v1/legal").Use(cloud.Bridge())

	zip.Get(zapp, "/v1/legal/health", o.health)
	zip.Get(zapp, "/v1/legal/templates", o.listTemplates)
	zip.Get(zapp, "/v1/legal/templates/:id", o.getTemplate)
	zip.Put(zapp, "/v1/legal/templates/:id", o.overrideTemplate)

	zip.Post(zapp, "/v1/legal/documents", o.generateDocument, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/legal/documents", o.listDocuments)
	zip.Get(zapp, "/v1/legal/documents/:id", o.getDocument)
	zip.Post(zapp, "/v1/legal/documents/:id/sign", o.requestSign)
	zip.Post(zapp, "/v1/legal/documents/:id/sign/complete", o.completeSign)

	zip.Post(zapp, "/v1/legal/filings", o.createFiling, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/legal/filings", o.listFilings)
	return nil
}

// ops binds the service to legal's typed handlers: a TypedHandler has no parameter
// for the service, so it arrives as a RECEIVER — also the one bound form
// cmd/zipdoc lifts prose from.
type ops struct{ s *cloud.Service[state] }

// None is the input of an op that takes none: no body, no query, no path param.
type None struct{}

// Page bounds a list read.
type Page struct {
	// Limit caps the rows returned; 0 means the store's own default.
	Limit int `json:"limit"`
}

// tenant resolves the org — the tenant-isolation KEY — that cloud.Bridge carried
// across the typed seam from the validated IAM owner claim. It is never an In
// field: an In field is what the caller says about itself.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// noStore marks a response uncacheable — a rendered document carries names and
// terms. A no-op off the HTTP path, where there is no response to mark.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
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

// LibraryHealth is the subsystem's liveness answer.
type LibraryHealth struct {
	// Status is "ok" while the surface is serving.
	Status string `json:"status"`
	// Templates is how many built-in templates the library ships.
	Templates int `json:"templates"`
}

// health reports that the legal surface is serving and how many built-in
// templates the library ships.
//
// Response: {"status": "ok", "templates": 12}
func (o ops) health(ctx context.Context, _ *None) (*LibraryHealth, error) {
	return &LibraryHealth{Status: "ok", Templates: len(Builtins())}, nil
}

// ---- templates ----

// templateView is the catalog projection: metadata + fields, NOT the full body (a
// list stays light; the body is fetched per template).
type templateView struct {
	// ID is the template id, the value every other template route takes.
	ID string `json:"id"`
	// Category is the corporate need the template serves.
	Category Category `json:"category"`
	// Title is the template's human title.
	Title string `json:"title"`
	// Version increments on each org override; a builtin is version 1.
	Version int `json:"version"`
	// Origin is "builtin" or "org".
	Origin string `json:"origin"`
	// CounselReview marks a template whose rendered document carries the mandatory
	// counsel-review notice.
	CounselReview bool `json:"counselReview"`
	// Fields are the merge fields the template consumes; all are required.
	Fields []MergeField `json:"fields"`
}

func toTemplateView(t DocumentTemplate) templateView {
	return templateView{ID: t.ID, Category: t.Category, Title: t.Title, Version: t.Version, Origin: t.Origin, CounselReview: t.CounselReview, Fields: t.Fields}
}

// TemplateCatalog is the org's resolved template library.
type TemplateCatalog struct {
	// Data is every template resolvable for the org: the builtins, with the org's
	// own overrides substituted in.
	Data []templateView `json:"data"`
	// Disclaimer is the boundary made visible on the wire: this is document
	// tooling, not legal advice.
	Disclaimer string `json:"disclaimer"`
}

// listTemplates returns the template library resolved for the caller's org — the
// builtins with the org's own overrides substituted in — as metadata and merge
// fields, without the template bodies.
func (o ops) listTemplates(ctx context.Context, _ *None) (*TemplateCatalog, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	cat, err := s.State.store.ResolveCatalog(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	out := make([]templateView, 0, len(cat))
	for _, t := range cat {
		out = append(out, toTemplateView(t))
	}
	return &TemplateCatalog{Data: out, Disclaimer: APIDisclaimer}, nil
}

// TemplateRef addresses one template by id.
type TemplateRef struct {
	// ID is the template id from the path.
	ID string `json:"id"`
}

// TemplateResponse carries one full template, body included.
type TemplateResponse struct {
	// DocumentTemplate is the resolved template, including its text/template body.
	Template DocumentTemplate `json:"template"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// getTemplate returns one template resolved for the caller's org — the org's
// override if it has one, else the builtin — including the template body.
//
// Example: {"id": "nda"}
func (o ops) getTemplate(ctx context.Context, in *TemplateRef) (*TemplateResponse, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.State.store.ResolveTemplate(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("template not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve template: %v", err)
	}
	return &TemplateResponse{Template: t, Disclaimer: APIDisclaimer}, nil
}

// OverrideTemplateRequest is an org-specific version of a template.
type OverrideTemplateRequest struct {
	// ID is the template id from the path; a body value is ignored.
	ID string `json:"id"`
	// Category is the corporate need it serves; inherited from the builtin when
	// overriding one, and required otherwise.
	Category Category `json:"category"`
	// Title is the template's human title; inherited from the builtin when empty.
	Title string `json:"title"`
	// Body is the text/template source, required. It may reference only the
	// declared fields — an undeclared one is refused rather than rendered blank.
	Body string `json:"body"`
	// CounselReview requests the counsel-review notice. It can be raised but never
	// lowered: a formation or equity template always carries the notice.
	CounselReview bool `json:"counselReview"`
	// Fields declares the merge fields the body consumes; all are required at render.
	Fields []MergeField `json:"fields"`
}

// overrideTemplate saves an org-specific version of a template (a custom NDA, say)
// and returns it at its new version. The body must parse and may reference only
// declared fields; a formation or equity template always keeps the counsel-review
// notice, which an override can raise but never drop.
//
// Example: {"id": "nda", "title": "Acme NDA", "body": "# NDA for {{.company}}", "fields": [{"key": "company", "label": "Company"}]}
func (o ops) overrideTemplate(ctx context.Context, in *OverrideTemplateRequest) (*TemplateResponse, error) {
	s := o.s
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	id := in.ID
	if strings.TrimSpace(in.Body) == "" {
		return nil, zip.ErrBadRequest("body is required")
	}
	t := DocumentTemplate{
		ID: id, Category: in.Category, Title: strings.TrimSpace(in.Title),
		CounselReview: in.CounselReview, Fields: in.Fields, Body: in.Body,
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
		return nil, zip.ErrBadRequest("category must be formation, equity, ops, or sales")
	}
	// The counsel-review boundary is coupled to the CATEGORY, not just the builtin: a
	// formation or equity (securities) template is ALWAYS counsel-review, so a NEW org
	// template in one of those categories can never generate a securities-class
	// document without the notice.
	if counselRequired(t.Category) {
		t.CounselReview = true
	}
	if t.Title == "" {
		return nil, zip.ErrBadRequest("title is required")
	}
	// Fail closed on an unparseable body OR one that references an UNDECLARED field
	// (which would render a silent blank), rather than storing a body that only fails
	// at generation time.
	if err := ValidateOverride(t); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	saved, err := s.State.store.SaveTemplateOverride(ctx, org, t)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save override: %v", err)
	}
	emitAudit(s, ctx, "legal.template.override", audit.Resource{Type: "legal.template", ID: saved.ID},
		map[string]any{"templateId": saved.ID, "version": saved.Version, "category": saved.Category})
	return &TemplateResponse{Template: saved, Disclaimer: APIDisclaimer}, nil
}

// ---- documents ----

// GenerateRequest names the template to render and supplies its merge data.
type GenerateRequest struct {
	// TemplateID is the template to render, required.
	TemplateID string `json:"templateId"`
	// Data supplies one value per declared merge field. Every declared field is
	// required — a missing one is refused rather than rendered as a blank.
	Data map[string]string `json:"data"`
}

// GeneratedDocument is a freshly rendered document, body included.
type GeneratedDocument struct {
	// Document is the rendered document, including its content.
	Document DocumentDetail `json:"document"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// generateDocument renders a document from a template and merge data (a pure,
// deterministic render), seals it in the org's store and audits the generation.
// It fails closed on a missing merge field — no blank contract — and the rendered
// body carries the counsel-review notice whenever the template requires it.
//
// Example: {"templateId": "nda", "data": {"company": "Acme, Inc.", "counterparty": "Beta LLC"}}
func (o ops) generateDocument(ctx context.Context, in *GenerateRequest) (*GeneratedDocument, error) {
	s := o.s
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.TemplateID) == "" {
		return nil, zip.ErrBadRequest("templateId is required")
	}
	t, err := s.State.store.ResolveTemplate(ctx, org, in.TemplateID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("template not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve template: %v", err)
	}
	rendered, err := Render(t, in.Data)
	if err != nil {
		// A missing-field / render error is the caller's — 400 with the honest reason
		// (the field names, never any secret).
		return nil, zip.ErrBadRequest(err.Error())
	}
	id, err := genID("doc")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := nowUnix()
	doc := Document{
		ID: id, Org: org, TemplateID: t.ID, TemplateVersion: t.Version, Category: t.Category,
		Title: t.Title, ContentType: "text/markdown", Body: string(rendered),
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateDocument(ctx, doc); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create document: %v", err)
	}
	emitAudit(s, ctx, "legal.document.generate", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "templateId": t.ID, "templateVersion": t.Version, "category": t.Category})
	return &GeneratedDocument{Document: docDetail(doc), Disclaimer: APIDisclaimer}, nil
}

// DocumentList is the org's generated documents, bodies omitted.
type DocumentList struct {
	// Data is one row per document, most recent first, without the rendered body.
	Data []DocumentView `json:"data"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// listDocuments returns the caller org's generated documents, most recent first,
// without their rendered bodies.
//
// Example: {"limit": 50}
func (o ops) listDocuments(ctx context.Context, in *Page) (*DocumentList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	docs, err := s.State.store.ListDocuments(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list documents: %v", err)
	}
	out := make([]DocumentView, 0, len(docs))
	for _, d := range docs {
		out = append(out, docView(d)) // list omits the body
	}
	noStore(ctx)
	return &DocumentList{Data: out, Disclaimer: APIDisclaimer}, nil
}

// DocumentRef addresses one of the caller org's documents by id.
type DocumentRef struct {
	// ID is the document id from the path, as returned by generate.
	ID string `json:"id"`
}

// getDocument returns one of the caller org's documents including its rendered
// body. A document belonging to another org reads as not found.
//
// Example: {"id": "doc_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"}
func (o ops) getDocument(ctx context.Context, in *DocumentRef) (*GeneratedDocument, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := s.State.store.GetDocument(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("document not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
	}
	noStore(ctx)
	return &GeneratedDocument{Document: docDetail(doc), Disclaimer: APIDisclaimer}, nil
}

// SignRequest opens an e-signature request over a document.
type SignRequest struct {
	// ID is the document id from the path; a body value is ignored.
	ID string `json:"id"`
	// Signers are the parties to sign, at least one.
	Signers []Signer `json:"signers"`
}

// SignRequested is the opened signature request.
type SignRequested struct {
	// Document is the document, now out for signature, without its body.
	Document DocumentView `json:"document"`
	// EsignRef is the provider's reference for the open request.
	EsignRef string `json:"esignRef"`
	// Provider is the e-signature provider that holds it.
	Provider string `json:"provider"`
}

// requestSign opens an e-signature request over one of the caller org's documents
// through the configured provider, and moves the document to out_for_signature.
//
// Example: {"id": "doc_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "signers": [{"name": "Ada Lovelace", "email": "ada@acme.com"}]}
func (o ops) requestSign(ctx context.Context, in *SignRequest) (*SignRequested, error) {
	s := o.s
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := s.State.store.GetDocument(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("document not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
	}
	if len(in.Signers) == 0 {
		return nil, zip.ErrBadRequest("at least one signer is required")
	}
	ref, err := s.State.esign.Request(ctx, org, doc.ID, doc.Title, in.Signers)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "esign request failed")
	}
	now := nowUnix()
	if err := s.State.store.UpdateDocumentSign(ctx, org, doc.ID, StatusOutForSig, s.State.esign.Name(), ref, now, 0); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update document: %v", err)
	}
	doc.Status, doc.EsignProvider, doc.EsignRef, doc.UpdatedAt = StatusOutForSig, s.State.esign.Name(), ref, now
	emitAudit(s, ctx, "legal.document.sign_requested", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "provider": doc.EsignProvider, "signers": len(in.Signers)})
	return &SignRequested{Document: docView(doc), EsignRef: ref, Provider: doc.EsignProvider}, nil
}

// CompleteSignRequest records signature completion for a document.
type CompleteSignRequest struct {
	// ID is the document id from the path; a body value is ignored.
	ID string `json:"id"`
	// Signed overrides the provider's own answer when present. Absent, the
	// provider's reported status decides.
	Signed *bool `json:"signed"`
}

// SignResult reports the document's state after a completion check.
type SignResult struct {
	// Document is the document, without its body.
	Document DocumentView `json:"document"`
	// Signed is true once the signature is recorded; false leaves it out for
	// signature, unchanged.
	Signed bool `json:"signed"`
}

// completeSign records signature completion for a document that has an open
// request — the signal a provider webhook or a reviewer sends. The provider's own
// status is checked first; an explicit signed value in the body overrides it.
//
// Example: {"id": "doc_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "signed": true}
// Response: {"document": {"id": "doc_4c1e9b7a2d6f0538e4a7c9b1d3f5027a", "status": "signed"}, "signed": true}
func (o ops) completeSign(ctx context.Context, in *CompleteSignRequest) (*SignResult, error) {
	s := o.s
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := s.State.store.GetDocument(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("document not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
	}
	if doc.EsignRef == "" {
		return nil, zip.ErrBadRequest("no signature request to complete")
	}
	// A real provider's webhook drives completion; honor an explicit signal for the
	// stub. The provider's own Status is checked first.
	complete, err := s.State.esign.Status(ctx, org, doc.EsignRef)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "esign status failed")
	}
	if in.Signed != nil {
		complete = *in.Signed
	}
	if !complete {
		return &SignResult{Document: docView(doc), Signed: false}, nil
	}
	now := nowUnix()
	if err := s.State.store.UpdateDocumentSign(ctx, org, doc.ID, StatusSigned, doc.EsignProvider, doc.EsignRef, now, now); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update document: %v", err)
	}
	doc.Status, doc.UpdatedAt, doc.SignedAt = StatusSigned, now, now
	emitAudit(s, ctx, "legal.document.signed", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "provider": doc.EsignProvider})
	return &SignResult{Document: docView(doc), Signed: true}, nil
}

// ---- filings ----

// FilingRequest tracks a state/agency filing of one or more generated documents.
type FilingRequest struct {
	// DocumentIDs are the documents to file, at least one; each must belong to the
	// caller's org.
	DocumentIDs []string `json:"documentIds"`
	// Jurisdiction is the state or agency the filing is for.
	Jurisdiction string `json:"jurisdiction"`
}

// FilingResponse carries one filing record.
type FilingResponse struct {
	// DocumentFiling is the tracking record. Its status is "manual" — file through your
	// registered agent — until a filing partner is wired.
	Filing DocumentFiling `json:"filing"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// createFiling records a filing of one or more of the caller org's documents and
// submits it through the filing seam. The platform does not file autonomously:
// with no partner wired the honest status is "manual".
//
// Example: {"documentIds": ["doc_4c1e9b7a2d6f0538e4a7c9b1d3f5027a"], "jurisdiction": "DE"}
func (o ops) createFiling(ctx context.Context, in *FilingRequest) (*FilingResponse, error) {
	s := o.s
	if err := capBody(ctx); err != nil {
		return nil, err
	}
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.DocumentIDs) == 0 {
		return nil, zip.ErrBadRequest("documentIds is required")
	}
	// Each document must belong to the org (no cross-tenant filing).
	for _, docID := range in.DocumentIDs {
		if _, err := s.State.store.GetDocument(ctx, org, docID); err == errNotFound {
			return nil, zip.ErrNotFound("document not found: " + docID)
		} else if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
		}
	}
	status, note, err := s.State.filer.Submit(ctx, org, in.Jurisdiction, in.DocumentIDs)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "filing submit failed")
	}
	id, err := genID("filing")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := nowUnix()
	f := DocumentFiling{
		ID: id, Org: org, DocumentIDs: in.DocumentIDs, Jurisdiction: in.Jurisdiction,
		Provider: s.State.filer.Name(), Status: status, Note: note, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.State.store.CreateFiling(ctx, f); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create filing: %v", err)
	}
	emitAudit(s, ctx, "legal.filing.create", audit.Resource{Type: "legal.filing", ID: f.ID},
		map[string]any{"filingId": f.ID, "provider": f.Provider, "status": f.Status, "documents": len(f.DocumentIDs)})
	return &FilingResponse{Filing: f, Disclaimer: APIDisclaimer}, nil
}

// FilingList is the org's filing records.
type FilingList struct {
	// Data is one row per filing, most recent first.
	Data []DocumentFiling `json:"data"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// listFilings returns the caller org's filing records, most recent first.
//
// Example: {"limit": 50}
func (o ops) listFilings(ctx context.Context, in *Page) (*FilingList, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	fs, err := s.State.store.ListFilings(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list filings: %v", err)
	}
	return &FilingList{Data: fs, Disclaimer: APIDisclaimer}, nil
}

// ---- views ----

// DocumentView is a document WITHOUT its rendered body — what a list read and a
// signature response carry.
type DocumentView struct {
	// ID is the document id.
	ID string `json:"id"`
	// TemplateID is the template it was rendered from.
	TemplateID string `json:"templateId"`
	// TemplateVersion is that template's version at render time, so the document
	// is reproducible.
	TemplateVersion int `json:"templateVersion"`
	// Category is the template's category.
	Category Category `json:"category"`
	// Title is the document title.
	Title string `json:"title"`
	// Status is the lifecycle state: draft, out_for_signature, signed or voided.
	// There is deliberately no "valid" state — validity is counsel's determination.
	Status DocStatus `json:"status"`
	// CreatedAt is the unix second the document was generated.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second of the last lifecycle change.
	UpdatedAt int64 `json:"updatedAt"`
	// EsignProvider is the provider holding the signature request, when one is open.
	EsignProvider string `json:"esignProvider,omitempty"`
	// SignedAt is the unix second the signature was recorded, once signed.
	SignedAt int64 `json:"signedAt,omitempty"`
}

// DocumentDetail is a document WITH its rendered body — what a single read to the
// owning org carries. The fields are spelled out rather than embedding
// DocumentView, because Go inlines an embedded struct's fields while the schema
// projector would render it as a nested object the wire never carries.
type DocumentDetail struct {
	// ID is the document id.
	ID string `json:"id"`
	// TemplateID is the template it was rendered from.
	TemplateID string `json:"templateId"`
	// TemplateVersion is that template's version at render time.
	TemplateVersion int `json:"templateVersion"`
	// Category is the template's category.
	Category Category `json:"category"`
	// Title is the document title.
	Title string `json:"title"`
	// Status is the lifecycle state: draft, out_for_signature, signed or voided.
	Status DocStatus `json:"status"`
	// CreatedAt is the unix second the document was generated.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is the unix second of the last lifecycle change.
	UpdatedAt int64 `json:"updatedAt"`
	// ContentType is the rendered body's media type.
	ContentType string `json:"contentType"`
	// Body is the rendered document, sealed at rest and returned only to the
	// owning org. It carries the counsel-review notice when the template requires it.
	Body string `json:"body"`
	// EsignProvider is the provider holding the signature request, when one is open.
	EsignProvider string `json:"esignProvider,omitempty"`
	// SignedAt is the unix second the signature was recorded, once signed.
	SignedAt int64 `json:"signedAt,omitempty"`
}

// docView projects a document without its (sealed) rendered content — what lists
// and signature responses carry.
func docView(d Document) DocumentView {
	return DocumentView{
		ID: d.ID, TemplateID: d.TemplateID, TemplateVersion: d.TemplateVersion,
		Category: d.Category, Title: d.Title, Status: d.Status,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		EsignProvider: d.EsignProvider, SignedAt: d.SignedAt,
	}
}

// docDetail projects a document WITH its rendered content, for the owning org.
func docDetail(d Document) DocumentDetail {
	return DocumentDetail{
		ID: d.ID, TemplateID: d.TemplateID, TemplateVersion: d.TemplateVersion,
		Category: d.Category, Title: d.Title, Status: d.Status,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		ContentType: d.ContentType, Body: d.Body,
		EsignProvider: d.EsignProvider, SignedAt: d.SignedAt,
	}
}

// ---- helpers ----

// emitAudit records a legal action on the shared tamper-evident trail. The `after`
// map carries opaque ids + template/category metadata ONLY — never the rendered
// document body (which may contain names) — and is redacted as a second layer.
func emitAudit(s *cloud.Service[state], ctx context.Context, action string, res audit.Resource, after map[string]any) {
	if s.State.audit == nil {
		return
	}
	c, ok := cloud.Request(ctx)
	if !ok {
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

// capBody holds the 1 MiB request-body bound the raw handlers enforced before
// decoding. A typed op is handed its In already decoded, so the check moves to the
// top of each write op — same limit, same 413, one line. A no-op off the HTTP
// path, where there is no body to bound.
func capBody(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil
	}
	if len(c.Fiber().Body()) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	return nil
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
