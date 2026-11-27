package legal

// plane.go — signing, published for the processes that do not hold documents.
//
// A capability that needs something signed — the tax profile's W-9 certification
// is the first — asks here rather than keeping a signature flow of its own. The
// document, the provider's request, the completion signal and the audit record
// are all legal's, so a signature requested by a peer is indistinguishable from
// one a person requested at /v1/legal: the same row, the same completion route
// (/v1/legal/documents/{id}/sign/complete), the same trail.
//
// Two differences from the HTTP surface, both deliberate:
//
//   - ONLY A BUILT-IN TEMPLATE is rendered. An org may override a template for its
//     own documents; a peer relies on this text meaning what the platform says it
//     means, and an override could delete the sentence it relies on.
//   - The tenant is the CALLER's (cloud.Tenant), never a field, so a peer cannot
//     open a signature in another org's name.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// expose publishes the signing ops. Use calls it.
func expose(s *cloud.Service[state]) {
	p := plane{s}
	cloud.Plane().Post("/legal/sign", p.sign,
		zip.WithOperationID(client.LegalSign),
		zip.WithSummary("Generate a built-in document and open its signature request"))
	cloud.Plane().Post("/legal/document", p.document,
		zip.WithOperationID(client.LegalDocument),
		zip.WithSummary("Where one of the caller's documents stands"))
}

// plane binds the service to the plane handlers; a method value is the only bound
// form zipdoc can lift prose from.
type plane struct{ s *cloud.Service[state] }

// Renders a BUILT-IN template for the caller's org, stores the document and opens
// its signature request, in one step — a peer that needs a signature has nothing
// to do between the two.
//
// The render fails closed on a missing field exactly as a person's generation
// does. The signature is the provider's to complete: with the default manual
// provider that is the completion route on /v1/legal, and nothing here reports a
// document signed.
func (p plane) sign(ctx context.Context, in *client.LegalSignIn) (*client.LegalSigning, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("legal sign: no org on the call")
	}
	t, ok := builtin(strings.TrimSpace(in.Template))
	if !ok {
		return nil, zip.ErrNotFound("legal sign: no built-in template " + in.Template)
	}
	if len(in.Signers) == 0 {
		return nil, zip.ErrBadRequest("legal sign: at least one signer is required")
	}
	data := make(map[string]string, len(in.Fields))
	for _, f := range in.Fields {
		data[f.Key] = f.Value
	}
	body, err := Render(t, data)
	if err != nil {
		return nil, zip.ErrBadRequest("legal sign: " + err.Error())
	}
	now := nowUnix()
	doc := Document{
		ID: mint.ID("doc"), Org: org, TemplateID: t.ID, TemplateVersion: t.Version, Category: t.Category,
		Title: t.Title, ContentType: "text/markdown", Body: string(body),
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now,
	}
	st := p.s.State
	if err := st.store.CreateDocument(ctx, doc); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "legal sign: create document: %v", err)
	}
	signers := make([]Signer, 0, len(in.Signers))
	for _, sg := range in.Signers {
		signers = append(signers, Signer{Name: sg.Name, Email: sg.Email})
	}
	ref, err := st.esign.Request(ctx, org, doc.ID, doc.Title, signers)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "legal sign: esign request failed")
	}
	if err := st.store.UpdateDocumentSign(ctx, org, doc.ID, StatusOutForSig, st.esign.Name(), ref, now, 0, ""); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "legal sign: update document: %v", err)
	}
	doc.Status, doc.EsignProvider, doc.EsignRef = StatusOutForSig, st.esign.Name(), ref
	p.audited(ctx, org, "legal.document.sign_requested", doc.ID,
		map[string]any{"documentId": doc.ID, "templateId": t.ID, "provider": doc.EsignProvider, "signers": len(signers)})
	return signing(doc), nil
}

// Answers where one of the caller's documents stands: its status, when it was
// signed, and the digest of the text that was signed. Another org's id is not
// found, exactly as on /v1/legal.
func (p plane) document(ctx context.Context, in *client.LegalDocumentIn) (*client.LegalSigning, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("legal document: no org on the call")
	}
	doc, err := p.s.State.store.GetDocument(ctx, org, strings.TrimSpace(in.ID))
	if err == errNotFound {
		return nil, zip.ErrNotFound("document not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "legal document: %v", err)
	}
	return signing(doc), nil
}

func signing(d Document) *client.LegalSigning {
	sum := sha256.Sum256([]byte(d.Body))
	return &client.LegalSigning{
		ID: d.ID, Template: d.TemplateID, Status: string(d.Status), Provider: d.EsignProvider,
		Signed: d.SignedAt, SignedBy: d.SignedBy, Digest: hex.EncodeToString(sum[:]),
	}
}

// audited records a plane act on the shared trail. The actor is the caller the
// plane carried — the person the peer is acting for, whose identity the edge
// validated — so a signature a peer opened names the same human a direct one does.
func (p plane) audited(ctx context.Context, org, action, id string, after map[string]any) {
	if p.s.State.audit == nil {
		return
	}
	who := cloud.Who(ctx)
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: who.User, Email: who.Email},
		Action:    action,
		Resource:  audit.Resource{Type: "legal.document", ID: id},
		Auth:      audit.AuthContext{Method: "plane", IsAdmin: who.Admin},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		RequestID: who.RequestID,
		After:     audit.Redact(mustJSON(after)),
	}
	if _, err := p.s.State.audit.Append(ctx, rec); err != nil {
		p.s.Log.Warn("legal audit append failed", "err", err, "action", action)
	}
}
