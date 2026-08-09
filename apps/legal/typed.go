package legal

// typed.go is the /v1/legal surface as TYPED ops — ten of the eleven.
//
// A typed op is ONE registry entry with N projections: the REST route, the
// OpenAPI operation's schema AND prose, the MCP tool an agent calls, the CLI
// command and every generated SDK method all follow from the same declaration.
// An untyped route gets a route and nothing else, which is what this surface was.
//
// Four wire details had to be carried over deliberately rather than inherited:
//
//   - the 1 MiB REQUEST-BODY CAP (decode, legal.go). A typed op receives its
//     DECODED In, so a size check inside it would run after the parse it exists to
//     precede — checkBody puts it back in front, at the point in the sequence each
//     raw handler reached it. cloud's global zip body limit is far larger, so a
//     naive conversion would have silently dropped a 413 this package has always
//     answered.
//   - 201 on the two creates, DECLARED with zip.WithStatus so the document keys
//     its response on the code the route actually sends.
//   - Cache-Control: no-store on the two document reads, which carry rendered
//     contract text.
//   - the CONDITIONAL keys of a document view. The raw handler built a map and
//     omitted esignProvider / signedAt when unset, and the body + contentType from
//     the LIST; two Out types say the same thing without an omitempty that would
//     also drop an empty body from the single read.
//
// POST /documents/:id/sign/complete STAYS UNTYPED, and it is a wire fact rather
// than an omission: it DISCARDS its decode error (`_ = decode(c, &reqBody)`,
// legal.go), so a caller may post an unparseable body and still drive the
// provider-reported completion. zip's invoke refuses a body it cannot parse
// BEFORE the handler runs (typed.go:239) and an In cannot rescue it —
// encoding/json validates the whole document before it will call a custom
// UnmarshalJSON — so typing it turns today's 200 into a 400.
// TestCompleteSignIgnoresAnUnparseableBody pins that tolerance.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listTemplates), which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op that takes nothing off the wire — it is addressed
// entirely by the caller's validated principal.
type noInput struct{}

// checkBody replays decode's REQUEST-BODY GATE at the point in the sequence the
// raw handler reached it: an empty body is fine (these routes have always
// tolerated one), a body over 1 MiB is 413, and anything c.Bind cannot parse —
// including a content type this service does not read — is 400.
//
// It calls the SAME decode over an empty target, so it is the same decision and
// the same message rather than a second implementation free to drift. The cap is
// the reason it exists: a typed op receives its DECODED In, so a size check
// written inside one would run after the parse it is there to precede, and cloud's
// global zip body limit is far larger than this package's.
func checkBody(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil // off the HTTP path there is no body to gate
	}
	return decode(c, &struct{}{})
}

// noStore pins Cache-Control: no-store on the response a typed op is serving,
// reached through the request cloud.Bridge parked — a typed op returns its Out and
// has no response value of its own. Set on SUCCESS paths only, exactly where the
// raw handlers set it: a rendered contract must never be cached.
func noStore(ctx context.Context) {
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// audited emits the same audit record the raw handlers emitted, reached through
// the request cloud.Bridge parked: the actor's subject, email and admin bit live
// in headers a typed op cannot see, and an audit trail that lost the actor would
// be a log, not a trail. No-op off the HTTP path, where there is no attested actor.
func (o ops) audited(ctx context.Context, action string, res audit.Resource, after map[string]any) {
	if c, ok := cloud.Request(ctx); ok {
		emitAudit(o.s, c, action, res, after)
	}
}

// ----- health ---------------------------------------------------------------

// legalHealth is the legal subsystem's own liveness answer.
type legalHealth struct {
	// Status is "ok" when the subsystem is serving.
	Status string `json:"status"`
	// Templates is how many built-in templates the catalog carries.
	Templates int `json:"templates"`
}

// LegalHealth reports that the legal subsystem is serving and how many built-in
// templates its catalog carries. It reads no tenant, so a liveness prober that
// sends no principal is answered rather than refused.
func (o ops) health(ctx context.Context, _ *noInput) (*legalHealth, error) {
	return &legalHealth{Status: "ok", Templates: len(Builtins())}, nil
}

// The fleet's schema namespace is FLAT — openapi.Weave refuses one name meaning two
// things — and three of legal's domain types share a name with an already-published
// one: apps/guide has a "Template", apps/company a "Filing" and a "Signer". These
// three are DEFINED types over them, not second shapes: the fields and their json
// tags are the same values, so the wire is byte-identical, and the published name
// now says which plane it belongs to.
type legalTemplate Template

type legalFiling Filing

// legalSigner is one person who must sign a document: their name and email.
type legalSigner Signer

// ----- templates ------------------------------------------------------------

// templateCatalog is the org's effective template library: the built-ins, with any
// template the org has overridden replaced by its own latest version.
type templateCatalog struct {
	// Data is the catalog, metadata and merge fields only — never the template
	// bodies, which are fetched one at a time.
	Data []templateView `json:"data"`
	// Disclaimer is the boundary made visible on the wire: Hanzo Legal is document
	// tooling, not legal advice.
	Disclaimer string `json:"disclaimer"`
}

// templateReply is one full template, body included.
type templateReply struct {
	// Template is the resolved template — the org's override if it has one, else
	// the built-in.
	Template legalTemplate `json:"template"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// ListLegalTemplates returns the org's effective template catalog: every built-in
// template, with any the org has overridden replaced by its own latest version.
//
// The listing carries each template's metadata and its declared MERGE FIELDS — the
// keys a document generation must supply — but never the template bodies; fetch one
// template to get its body. Templates in the formation and equity categories are
// marked counselReview: every document rendered from them carries a counsel notice,
// and that posture cannot be dropped by an override.
func (o ops) listTemplates(ctx context.Context, _ *noInput) (*templateCatalog, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	cat, err := o.s.State.store.ResolveCatalog(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "catalog: %v", err)
	}
	out := make([]templateView, 0, len(cat))
	for _, t := range cat {
		out = append(out, toTemplateView(t))
	}
	return &templateCatalog{Data: out, Disclaimer: APIDisclaimer}, nil
}

// templateRef addresses ONE template by its id, which is the path segment.
type templateRef struct {
	// ID is the template's stable id, e.g. "nda" or "safe".
	ID string `json:"id"`
}

// GetLegalTemplate returns one template resolved for the caller's org — the org's
// own override if it has saved one, else the built-in — with its full text/template
// body and its declared merge fields. 404 when neither exists.
func (o ops) getTemplate(ctx context.Context, in *templateRef) (*templateReply, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	t, err := o.s.State.store.ResolveTemplate(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("template not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "resolve template: %v", err)
	}
	return &templateReply{Template: legalTemplate(t), Disclaimer: APIDisclaimer}, nil
}

// templateOverride is an org's own version of a template.
type templateOverride struct {
	// ID is the template to override, from the path. Overriding a built-in id
	// inherits that built-in's category, title and counsel-review posture.
	ID string `json:"id"`
	// Category groups the template: formation, equity, ops or sales. Optional when
	// overriding a built-in, which supplies its own.
	Category Category `json:"category"`
	// Title is the template's display name. Required unless a built-in supplies it.
	Title string `json:"title"`
	// Body is the text/template source. Required. Every {{.key}} it references must
	// be declared in Fields, or the save is refused rather than rendering a blank
	// into a contract later.
	Body string `json:"body"`
	// CounselReview marks a template whose documents must carry the counsel notice.
	// It can be raised but never lowered: a formation or equity template is always
	// counsel-review, and an override of a counsel-review built-in stays one.
	CounselReview bool `json:"counselReview"`
	// Fields declares the merge fields the body consumes. Every declared field is
	// REQUIRED at generation — the engine fails closed on a missing one.
	Fields []Field `json:"fields"`
}

// SaveLegalTemplateOverride saves the org's own version of a template — a custom
// NDA, a house MSA — and returns it with its new version number. It takes effect
// for that org only; other orgs keep the built-in.
//
// Two boundaries cannot be crossed here. Overriding a built-in INHERITS its
// category and its counsel-review posture, which can be raised but never dropped;
// and a formation or equity template is counsel-review whatever the caller sends,
// so no org can generate a securities-class document without the notice.
//
// The body is validated on save, not at generation: a template that references an
// UNDECLARED merge field is refused with 400 rather than stored and rendered blank
// into a contract months later.
//
// Example: {"id": "nda", "title": "Acme Mutual NDA", "body": "…{{.counterparty}}…",
// "fields": [{"key": "counterparty", "label": "Counterparty"}]}
func (o ops) overrideTemplate(ctx context.Context, in *templateOverride) (*templateReply, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkBody(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, zip.ErrBadRequest("body is required")
	}
	t := Template{
		ID: in.ID, Category: in.Category, Title: strings.TrimSpace(in.Title),
		CounselReview: in.CounselReview, Fields: in.Fields, Body: in.Body,
	}
	// Inherit metadata from the builtin when overriding one: a custom NDA keeps the
	// NDA category and its counsel-review posture cannot be downgraded below the
	// builtin's.
	if base, isBuiltin := builtin(in.ID); isBuiltin {
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
	saved, err := o.s.State.store.SaveTemplateOverride(ctx, org, t)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save override: %v", err)
	}
	o.audited(ctx, "legal.template.override", audit.Resource{Type: "legal.template", ID: saved.ID},
		map[string]any{"templateId": saved.ID, "version": saved.Version, "category": saved.Category})
	return &templateReply{Template: legalTemplate(saved), Disclaimer: APIDisclaimer}, nil
}

// ----- documents ------------------------------------------------------------

// documentSummary is a generated document WITHOUT its rendered content — the shape
// a listing and the two signature replies answer with.
type documentSummary struct {
	// ID is the document's server-minted handle, "doc_"-prefixed.
	ID string `json:"id"`
	// TemplateID is the template it was rendered from.
	TemplateID string `json:"templateId"`
	// TemplateVersion is WHICH version of that template rendered it, so the
	// document is reproducible and auditable.
	TemplateVersion int `json:"templateVersion"`
	// Category is the template's category: formation, equity, ops or sales.
	Category Category `json:"category"`
	// Title is the document's title, inherited from the template.
	Title string `json:"title"`
	// Status is the lifecycle state: draft, out_for_signature, signed or voided.
	// There is deliberately no "legally valid" state — that is counsel's
	// determination, not the platform's.
	Status DocStatus `json:"status"`
	// CreatedAt is when the document was generated, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when it last changed, in unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
	// EsignProvider names the e-signature provider handling it, absent until a
	// signature has been requested.
	EsignProvider string `json:"esignProvider,omitempty"`
	// SignedAt is when the provider reported completion, in unix seconds. Absent
	// until then.
	SignedAt int64 `json:"signedAt,omitempty"`
}

// documentView is a document WITH its rendered content — the shape a single read
// and the generation reply answer with. It is a distinct type rather than
// documentSummary with omitempty fields, because an empty rendered body must still
// appear as "body": "" the way the raw handler wrote it.
type documentView struct {
	documentSummary
	// ContentType is the rendered body's media type — text/markdown.
	ContentType string `json:"contentType"`
	// Body is the rendered document. It is sealed at rest and returned only to the
	// owning org. When the template is counsel-review it opens with the counsel
	// notice, which the engine prepends and no caller can suppress.
	Body string `json:"body"`
}

func toDocumentSummary(d Document) documentSummary {
	return documentSummary{
		ID: d.ID, TemplateID: d.TemplateID, TemplateVersion: d.TemplateVersion,
		Category: d.Category, Title: d.Title, Status: d.Status,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		EsignProvider: d.EsignProvider, SignedAt: d.SignedAt,
	}
}

func toDocumentView(d Document) documentView {
	return documentView{documentSummary: toDocumentSummary(d), ContentType: d.ContentType, Body: d.Body}
}

// documentReply is one generated or fetched document with the boundary disclaimer.
type documentReply struct {
	// Document is the document, rendered content included.
	Document documentView `json:"document"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// documentPage is one page of the org's documents, newest first.
type documentPage struct {
	// Data are the documents, WITHOUT their rendered content — fetch one to read it.
	Data []documentSummary `json:"data"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// generateRequest renders one document from a template plus the org's own data.
type generateRequest struct {
	// TemplateID is the template to render. Required; resolved for the caller's
	// org, so an override wins over the built-in.
	TemplateID string `json:"templateId"`
	// Data supplies every merge field the template declares, keyed by field key.
	// Every declared field is REQUIRED: a missing one is refused with 400 rather
	// than rendered as a blank into a contract.
	Data map[string]string `json:"data"`
}

// GenerateLegalDocument renders a document from a template and the caller's own
// merge data, seals it in the org's store, and returns it with its rendered body.
//
// The render is PURE and deterministic — no clock, no I/O — so the same template
// version and the same data always produce identical bytes, which is what makes a
// generated contract reproducible. It fails CLOSED on a missing merge field: there
// is no blank-filled contract, only a 400 naming the fields that were absent. When
// the template is counsel-review the rendered body opens with the counsel notice,
// which no caller can suppress.
//
// The document is a DRAFT. Hanzo Legal manages documents; it does not give legal
// advice and does not determine that a document is valid or sufficient.
//
// Example: {"templateId": "nda", "data": {"counterparty": "Acme, Inc.", "date": "2026-07-30"}}
func (o ops) generateDocument(ctx context.Context, in *generateRequest) (*documentReply, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkBody(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.TemplateID) == "" {
		return nil, zip.ErrBadRequest("templateId is required")
	}
	t, err := o.s.State.store.ResolveTemplate(ctx, org, in.TemplateID)
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
	id := mint.ID("doc")
	now := nowUnix()
	doc := Document{
		ID: id, Org: org, TemplateID: t.ID, TemplateVersion: t.Version, Category: t.Category,
		Title: t.Title, ContentType: "text/markdown", Body: string(rendered),
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.CreateDocument(ctx, doc); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create document: %v", err)
	}
	o.audited(ctx, "legal.document.generate", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "templateId": t.ID, "templateVersion": t.Version, "category": t.Category})
	return &documentReply{Document: toDocumentView(doc), Disclaimer: APIDisclaimer}, nil
}

// documentFilter pages the org's documents.
type documentFilter struct {
	// Limit bounds the page. Absent or unparseable means the store's own default.
	Limit int `json:"limit"`
}

// ListLegalDocuments returns the org's generated documents, newest first, WITHOUT
// their rendered content — fetch one document to read its body.
//
// The response is marked no-store: these records name the counterparties an org is
// contracting with, and must not sit in a shared cache.
func (o ops) listDocuments(ctx context.Context, in *documentFilter) (*documentPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	docs, err := o.s.State.store.ListDocuments(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list documents: %v", err)
	}
	out := make([]documentSummary, 0, len(docs))
	for _, d := range docs {
		out = append(out, toDocumentSummary(d)) // list omits the body
	}
	noStore(ctx)
	return &documentPage{Data: out, Disclaimer: APIDisclaimer}, nil
}

// documentRef addresses ONE document by its id, which is the path segment.
type documentRef struct {
	// ID is the document's server-minted handle, "doc_"-prefixed.
	ID string `json:"id"`
}

// GetLegalDocument returns one of the org's documents WITH its rendered body. 404
// when the org has no document with that id — a document is never readable across
// orgs.
//
// The response is marked no-store: the body is contract text, sealed at rest and
// returned only to the owning org, and must not sit in a shared cache.
func (o ops) getDocument(ctx context.Context, in *documentRef) (*documentReply, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := o.s.State.store.GetDocument(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("document not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
	}
	noStore(ctx)
	return &documentReply{Document: toDocumentView(doc), Disclaimer: APIDisclaimer}, nil
}

// signRequest opens an e-signature request over one document.
type signRequest struct {
	// ID is the document to send for signature, from the path.
	ID string `json:"id"`
	// Signers are the people who must sign, by name and email. At least one is
	// required.
	Signers []legalSigner `json:"signers"`
}

// signReply is the document after a signature request, plus the provider's handle
// on it.
type signReply struct {
	// Document is the document, now out for signature. Its rendered body is not
	// repeated here.
	Document documentSummary `json:"document"`
	// EsignRef is the provider's own reference for the request — what a webhook or
	// a status poll quotes.
	EsignRef string `json:"esignRef"`
	// Provider names the e-signature provider that took the request. "manual" means
	// no provider is wired on this deployment and the org fulfils it out of band.
	Provider string `json:"provider"`
}

// RequestLegalSignature opens an e-signature request over one document and moves it
// to out_for_signature, returning the provider's reference for the request.
//
// The provider is whatever this deployment has wired. The honest default is
// "manual": the request is recorded and the org fulfils it out of band — nothing
// here fabricates a signature, and the stub never reports itself complete.
//
// Example: {"id": "doc_1f…", "signers": [{"name": "Ada", "email": "ada@acme.com"}]}
func (o ops) requestSign(ctx context.Context, in *signRequest) (*signReply, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	doc, err := o.s.State.store.GetDocument(ctx, org, in.ID)
	if err == errNotFound {
		return nil, zip.ErrNotFound("document not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
	}
	if err := checkBody(ctx); err != nil {
		return nil, err
	}
	if len(in.Signers) == 0 {
		return nil, zip.ErrBadRequest("at least one signer is required")
	}
	signers := make([]Signer, 0, len(in.Signers))
	for _, sg := range in.Signers {
		signers = append(signers, Signer(sg))
	}
	ref, err := o.s.State.esign.Request(ctx, org, doc.ID, doc.Title, signers)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "esign request failed")
	}
	now := nowUnix()
	if err := o.s.State.store.UpdateDocumentSign(ctx, org, doc.ID, StatusOutForSig, o.s.State.esign.Name(), ref, now, 0); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update document: %v", err)
	}
	doc.Status, doc.EsignProvider, doc.EsignRef, doc.UpdatedAt = StatusOutForSig, o.s.State.esign.Name(), ref, now
	o.audited(ctx, "legal.document.sign_requested", audit.Resource{Type: "legal.document", ID: doc.ID},
		map[string]any{"documentId": doc.ID, "provider": doc.EsignProvider, "signers": len(in.Signers)})
	return &signReply{Document: toDocumentSummary(doc), EsignRef: ref, Provider: doc.EsignProvider}, nil
}

// ----- filings --------------------------------------------------------------

// filingRequest opens a filing over one or more of the org's documents.
type filingRequest struct {
	// DocumentIDs are the documents to file. At least one is required, and every
	// one must belong to the caller's org — a filing can never reach across orgs.
	DocumentIDs []string `json:"documentIds"`
	// Jurisdiction is the state or agency the filing is for, e.g. "DE".
	Jurisdiction string `json:"jurisdiction"`
}

// filingReply is one filing record with the boundary disclaimer.
type filingReply struct {
	// Filing is the tracking record.
	Filing legalFiling `json:"filing"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// filingPage is one page of the org's filings, newest first.
type filingPage struct {
	// Data are the filing records.
	Data []legalFiling `json:"data"`
	// Disclaimer is the boundary made visible on the wire.
	Disclaimer string `json:"disclaimer"`
}

// CreateLegalFiling records a filing of one or more of the org's documents with a
// state or agency, and returns the tracking record.
//
// It is a TRACKING record, not an autonomous filing. With no filing partner wired
// the honest status is "manual" and the note says so: the documents were generated
// for signature, and the org files them through its registered agent. Nothing here
// invents a filing id it does not have.
//
// Every document id must belong to the caller's org; one that does not is a 404
// naming it, so a filing can never reach across tenants.
//
// Example: {"documentIds": ["doc_1f…"], "jurisdiction": "DE"}
func (o ops) createFiling(ctx context.Context, in *filingRequest) (*filingReply, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkBody(ctx); err != nil {
		return nil, err
	}
	if len(in.DocumentIDs) == 0 {
		return nil, zip.ErrBadRequest("documentIds is required")
	}
	// Each document must belong to the org (no cross-tenant filing).
	for _, docID := range in.DocumentIDs {
		if _, err := o.s.State.store.GetDocument(ctx, org, docID); err == errNotFound {
			return nil, zip.ErrNotFound("document not found: " + docID)
		} else if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "get document: %v", err)
		}
	}
	status, note, err := o.s.State.filer.Submit(ctx, org, in.Jurisdiction, in.DocumentIDs)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "filing submit failed")
	}
	id := mint.ID("filing")
	now := nowUnix()
	f := Filing{
		ID: id, Org: org, DocumentIDs: in.DocumentIDs, Jurisdiction: in.Jurisdiction,
		Provider: o.s.State.filer.Name(), Status: status, Note: note, CreatedAt: now, UpdatedAt: now,
	}
	if err := o.s.State.store.CreateFiling(ctx, f); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create filing: %v", err)
	}
	o.audited(ctx, "legal.filing.create", audit.Resource{Type: "legal.filing", ID: f.ID},
		map[string]any{"filingId": f.ID, "provider": f.Provider, "status": f.Status, "documents": len(f.DocumentIDs)})
	return &filingReply{Filing: legalFiling(f), Disclaimer: APIDisclaimer}, nil
}

// ListLegalFilings returns the org's filing records, newest first — which documents
// were filed where, through which provider, and what the filing's honest status is.
func (o ops) listFilings(ctx context.Context, in *documentFilter) (*filingPage, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	fs, err := o.s.State.store.ListFilings(ctx, org, in.Limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list filings: %v", err)
	}
	page := make([]legalFiling, 0, len(fs))
	for _, f := range fs {
		page = append(page, legalFiling(f))
	}
	return &filingPage{Data: page, Disclaimer: APIDisclaimer}, nil
}
