package esign

// typed.go is esign's TYPED plane — the ops that carry In/Out types, and so the
// only esign routes that reach a schema, the MCP tool list, the CLI and the
// generated SDKs. Before this file the whole surface published thirteen
// operationIds and NO body: every SDK offered "upload a PDF for signature" with
// nowhere to put the PDF, and an agent asking the fleet door what it could do was
// told a document could be signed and never what to send.
//
// The package doc and HIP-1125 §3 both said nothing here could be typed, on one
// premise: every route is built by a handler FACTORY closing over a bundle route
// name, and a closure has no doc comment for the registry to lift. That premise
// is about the FACTORY, not about the routes — apps/dataroom retired the same
// claim by writing the ops as methods over the same bundle seam, and this file is
// that answer one subsystem over. The shared kit it runs on is apps/goja
// (Scalar, ScalarList, Raw, SizedIn, BundleErr, Envelope), which is also where
// captable's and dataroom's typed planes live.
//
// WHAT A RELAY HAS TO KEEP. The bundle decides the answer and the Go host carries
// it, so typing may not change either half:
//
//   - WHAT IT ANSWERS. The bundle authors its own refusal envelope, `{"error":…}`,
//     under its own status. goja.BundleErr carries both and goja.Envelope writes
//     them back verbatim, so a 409 "recipients can only be added while DRAFT"
//     still reads exactly as it did.
//   - WHAT IT ACCEPTS. The bundle validates with COERCING helpers — `str(v)` on a
//     title, `num(v, 1)` on a page — so a Go string or int field would refuse
//     input the route accepts today. Every caller-supplied field below is a
//     goja.Scalar (or goja.Raw where the value may be any JSON), which carries the
//     caller's token to the bundle byte for byte and leaves the bundle the only
//     judge of it.
//
// FIELD ORDER IS LOAD-BEARING. The bundle's answer crosses the goja boundary as
// map[string]any (goja.go:255 Export) and is serialised by encoding/json, which
// SORTS object keys. Every model below therefore declares its fields in
// ALPHABETICAL json-tag order, so a typed answer is BYTE-identical to the relay it
// replaces rather than merely equal as JSON. TestTypedAnswersAreByteIdentical pins
// that against the bundle's own bytes, so a field added out of order fails here
// instead of drifting into a client.
//
// NUMBERS ARE THE CALLER'S OR THE HOST'S, and which one decides the Go type. A
// timestamp is minted by the host (`__now()`), always integral, so it is an int64.
// A page, a signing order and a field's geometry are whatever `num()` made of what
// the caller sent — the bundle does not round — so they are float64: declaring
// them integer would publish a constraint the route does not enforce and would
// fail to decode the answer to a fractional page the bundle accepts today.
//
// NULLABILITY IS THE SCHEMA'S. A column schema.go leaves nullable arrives as JSON
// null, so it is a POINTER here; a NOT NULL column is a value.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out FIELD into
// zipdoc_gen.go, which is the only way that prose reaches the published document,
// the MCP tool description and the CLI help — Go drops comments at compile time.
// Run by `make -C apps/esign describe` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed esign ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// ─────────────────────────────────────────────────────────────────────────────
// the two doors
// ─────────────────────────────────────────────────────────────────────────────

// owner is the tenant of a SENDER's call: the validated principal's own org, read
// off the context where cloud.Bridge parked it. Never an In field — an In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the caller
// asserted for itself.
func (o ops) owner(ctx context.Context) (string, error) { return principal.Acting(ctx) }

// signer is the tenant of a RECIPIENT's call, and the order it resolves in is the
// security property.
//
// The token is the whole credential, so the TOKEN resolves first, against the
// cross-tenant index, and the org it resolves to is what selects the tenant store.
// The `org` path segment is only the claim the caller makes about which tenant
// they are addressing, and it is checked against that answer — never used to
// select anything. Reading it the other way round (open the store the claim names,
// then look the token up inside it) is what let an unauthenticated caller mint
// tenant databases, because opening a per-tenant store CREATES the encrypted file
// and runs the schema DDL. A token that does not resolve is refused before any
// per-tenant file is touched, and a token that resolves under a different org is
// refused identically, so the answer never separates "no such token" from "not
// yours".
func (o ops) signer(in esignTokenRef) (string, error) {
	if in.Token == "" {
		return "", zip.ErrNotFound("unknown signing token")
	}
	org, ok, err := o.s.State.index.org(in.Token)
	if err != nil {
		o.s.Log.Error("esign token index read failed", "err", err)
		return "", zip.Errorf(http.StatusInternalServerError, "token resolution failed")
	}
	if !ok || org != in.Org {
		return "", zip.ErrNotFound("unknown signing token")
	}
	return org, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// the relay tail
// ─────────────────────────────────────────────────────────────────────────────

// bundleMessage is the human sentence in an esign refusal, for the Error() string
// a caller OFF the HTTP path sees — an MCP tools/call and an in-process CLI invoke
// never pass through Envelope, so zip's own error handler renders this instead of
// a blanket 500. The bundle's envelope is a single `error` key (its HttpError
// catch), which is a shape of its own — this is why the message extractor stays
// with the app while the carrier is shared. A body that is not an envelope falls
// back to the status text, so the sentence is never empty.
func bundleMessage(status int, body []byte) string {
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error != "" {
		return env.Error
	}
	if s := http.StatusText(status); s != "" {
		return s
	}
	return "esign dispatch failed"
}

// run is the shared tail of every typed op: dispatch the bundle route on the
// resolved tenant's store, index any signing tokens the answer minted, and decode
// the answer into out. ONE place turns a bundle response into either an out value
// or a goja.BundleErr, so the two doors cannot come to disagree about what a
// refusal looks like.
//
// The cross-tenant index write is part of the operation and not a follow-up: a
// recipient's only way in is the token, and a token absent from the index opens
// for nobody, so failing the call is what keeps a signing link nobody could open
// from being handed back as usable. It runs on the same statuses the untyped relay
// ran it on, reading the answer's BYTES, so both halves of the surface index
// identically.
//
// Only a failure of the HOST itself — the engine never ran, or it answered
// something that is not the out shape — becomes cloud's own 500, which is what the
// untyped dispatch already answers in exactly that case.
func (o ops) run(ctx context.Context, org, route string, params map[string]string, body, out any) error {
	resp, err := o.s.State.host.Dispatch(ctx, org, goja.BaseRequest{Route: route, Params: params, Body: body})
	if err != nil {
		o.s.Log.Error("esign dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "esign dispatch failed")
	}
	if resp.Status >= 300 {
		return &goja.BundleErr{Status: resp.Status, Body: resp.Body, Msg: bundleMessage(resp.Status, resp.Body)}
	}
	if err := o.s.State.index.record(route, org, resp.Body); err != nil {
		o.s.Log.Error("esign token index write failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "token index write failed")
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		o.s.Log.Error("esign response decode failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "esign dispatch failed")
	}
	return nil
}

// write is run for a BODY-carrying op: refuse an oversized body with the relay's
// 413 — after the tenant, before the work, the order the relay used — then
// assemble the caller's verbatim tokens and dispatch.
func (o ops) write(ctx context.Context, org, route string, size goja.SizedIn, params map[string]string, fields map[string]goja.BodyField, out any) error {
	if size.Oversize() {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	body, err := goja.Body(fields)
	if err != nil {
		o.s.Log.Error("esign body assembly failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "esign dispatch failed")
	}
	return o.run(ctx, org, route, params, body, out)
}

// errUnreadable is the relay's own rule about a body it cannot read, kept exactly.
//
// The untyped relay decoded the body into `any`, so it refused BYTES THAT ARE NOT
// JSON with a 400 and accepted every other shape — an array, a bare number — which
// the bundle then read as an object with no keys. goja.SizedIn.Fill is tolerant of
// both, which is right for a relay that refused neither, so the "not JSON at all"
// half is restated here. Order is kept too: the relay measured the body before
// parsing it, so an oversized body is a 413 whatever its contents, and the tenant
// is resolved before either, so a caller with no principal is still refused first.
var errUnreadable = errors.New("invalid JSON body")

// unreadable reports the parse refusal for a body Fill has already measured.
func unreadable(size goja.SizedIn, b []byte) error {
	if size.Oversize() || len(b) == 0 || json.Valid(b) {
		return nil
	}
	return errUnreadable
}

// ─────────────────────────────────────────────────────────────────────────────
// inputs
// ─────────────────────────────────────────────────────────────────────────────

// esignNone is the In of an op that takes nothing off the wire: the caller's
// principal is the whole address.
type esignNone struct{}

// esignRef addresses one of the caller org's documents.
type esignRef struct {
	// ID is the document to act on. It is the path segment: the URL is the
	// addressing authority, and the org it is resolved in comes from the caller's
	// principal, so an id belonging to another tenant is simply not found.
	ID string `json:"id"`
}

// esignSendIn addresses the document to send. It carries no body of its own — the
// recipients and fields were fixed when they were added — but it measures the body
// anyway, because the relay it replaces capped one.
type esignSendIn struct {
	goja.SizedIn
	// ID is the document to send. The URL is the addressing authority.
	ID string `json:"id"`
}

// UnmarshalJSON measures the body and refuses only what the relay refused.
func (in *esignSendIn) UnmarshalJSON(b []byte) error {
	type alias esignSendIn
	in.Fill(maxBody, b, (*alias)(in))
	return unreadable(in.SizedIn, b)
}

// esignUploadIn is the PDF and the covering note that open a draft.
type esignUploadIn struct {
	goja.SizedIn
	// Title is the document's name, shown to every recipient and used to build the
	// download filename. Required — a request without one is refused and nothing is
	// stored.
	Title goja.Scalar `json:"title,omitempty" url:"-"`
	// PdfBase64 is the document itself, base64-encoded; a `data:` URL prefix is
	// accepted and stripped. Required. The bytes go to object storage rather than
	// into the tenant database, and this ORIGINAL is kept under its own key so it
	// survives sealing untouched.
	PdfBase64 goja.Scalar `json:"pdfBase64,omitempty" url:"-"`
	// ExternalID is your own identifier for this document, stored and echoed back
	// so a document here can be matched to a record in your system. Optional.
	ExternalID goja.Scalar `json:"externalId,omitempty" url:"-"`
	// Subject is the covering subject line carried with the document. Optional, and
	// nothing in this surface emails it — delivering the signing links is the
	// caller's.
	Subject goja.Scalar `json:"subject,omitempty" url:"-"`
	// Message is the covering message carried with the document. Optional, and like
	// the subject it is stored rather than sent.
	Message goja.Scalar `json:"message,omitempty" url:"-"`
	// SigningOrder chooses PARALLEL — the default, where everyone may sign at once
	// — or SEQUENTIAL, where each signer waits for the ones ahead of them. Anything
	// else reads as PARALLEL, and the choice is fixed for the document's life.
	SigningOrder goja.Scalar `json:"signingOrder,omitempty" url:"-"`
}

// UnmarshalJSON measures the body and refuses only what the relay refused.
func (in *esignUploadIn) UnmarshalJSON(b []byte) error {
	type alias esignUploadIn
	in.Fill(maxBody, b, (*alias)(in))
	return unreadable(in.SizedIn, b)
}

// esignRecipientIn is the person being asked to sign.
type esignRecipientIn struct {
	goja.SizedIn
	// ID is the draft to add them to. It is the path segment, kept out of the body
	// so a body naming another document cannot redirect the write.
	ID string `json:"-" url:"id"`
	// Email is where the signing link is meant to go, lower-cased on the way in.
	// Required.
	Email goja.Scalar `json:"email,omitempty" url:"-"`
	// Name is the recipient's display name, also the fallback a NAME field is
	// filled with when they leave it blank. Optional.
	Name goja.Scalar `json:"name,omitempty" url:"-"`
	// Role is SIGNER (the default), CC, VIEWER, APPROVER or ASSISTANT; anything
	// else reads as SIGNER. A CC recipient is recorded as already complete, because
	// they are never asked to sign. SIGNER and APPROVER are the roles a document
	// waits for before it can seal.
	Role goja.Scalar `json:"role,omitempty" url:"-"`
	// SigningOrder is this recipient's position when the document is SEQUENTIAL —
	// lower signs first, and ties fall back to the order they were added. Ignored
	// by a PARALLEL document. Optional.
	SigningOrder goja.Scalar `json:"signingOrder,omitempty" url:"-"`
}

// UnmarshalJSON measures the body and refuses only what the relay refused.
func (in *esignRecipientIn) UnmarshalJSON(b []byte) error {
	type alias esignRecipientIn
	in.Fill(maxBody, b, (*alias)(in))
	return unreadable(in.SizedIn, b)
}

// esignFieldIn places one field on the page for one recipient.
type esignFieldIn struct {
	goja.SizedIn
	// ID is the draft to place the field on. It is the path segment, kept out of
	// the body so a body naming another document cannot redirect the write.
	ID string `json:"-" url:"id"`
	// RecipientID is who must fill this field. Required, and it must belong to this
	// document — a recipient from elsewhere is refused rather than silently
	// accepted.
	RecipientID goja.Scalar `json:"recipientId,omitempty" url:"-"`
	// Type is what the field collects: SIGNATURE, FREE_SIGNATURE, INITIALS, NAME,
	// EMAIL, DATE, TEXT, NUMBER, RADIO, CHECKBOX or DROPDOWN. Required, and
	// anything else is refused.
	Type goja.Scalar `json:"type,omitempty" url:"-"`
	// Page is the 1-based page the field sits on, defaulting to 1.
	Page goja.Scalar `json:"page,omitempty" url:"-"`
	// PositionX is the field's horizontal position on that page, defaulting to 0.
	PositionX goja.Scalar `json:"positionX,omitempty" url:"-"`
	// PositionY is the field's vertical position on that page, defaulting to 0.
	PositionY goja.Scalar `json:"positionY,omitempty" url:"-"`
	// Width is the field's width, defaulting to -1, which means the renderer
	// chooses one when the document is sealed.
	Width goja.Scalar `json:"width,omitempty" url:"-"`
	// Height is the field's height, defaulting to -1, which means the renderer
	// chooses one when the document is sealed.
	Height goja.Scalar `json:"height,omitempty" url:"-"`
	// FieldMeta is your own metadata for this field — a label, a placeholder, a
	// required flag — stored verbatim and handed back on every read of the
	// document. Any JSON, and esign never interprets it.
	FieldMeta goja.Raw `json:"fieldMeta,omitempty" url:"-"`
}

// UnmarshalJSON measures the body and refuses only what the relay refused.
func (in *esignFieldIn) UnmarshalJSON(b []byte) error {
	type alias esignFieldIn
	in.Fill(maxBody, b, (*alias)(in))
	return unreadable(in.SizedIn, b)
}

// esignTokenRef is the signer's door: the capability that opens it and the tenant
// the caller claims it belongs to.
//
// Both are path segments and NEITHER is in the body, which is what stops a body
// from naming a different tenant than the URL the router matched. The org is not
// an identity the caller asserts — it is checked against what the token index
// answers, and the token is what selects the store.
type esignTokenRef struct {
	// Org is the tenant the link claims to belong to. It selects nothing: the token
	// resolves to its owning org first, and a claim that disagrees is the same 404
	// an unknown token gets.
	Org string `json:"-" url:"org"`
	// Token is the crypto-random signing capability from the link, and it is the
	// whole credential — there is no account behind this door. It names the
	// recipient, so a signer reaches only their own fields.
	Token string `json:"-" url:"token"`
}

// esignValueIn fills in one of the signer's fields.
type esignValueIn struct {
	goja.SizedIn
	esignTokenRef
	// FieldID is the field being filled. It must belong to this recipient: another
	// recipient's field is refused even under a valid token.
	FieldID string `json:"-" url:"fieldId"`
	// Value is what goes in the field. A signature takes the drawn image bytes or a
	// typed name; a DATE, NAME or EMAIL field falls back to today, the recipient's
	// name or their email when this is omitted; every other type requires it.
	Value goja.Scalar `json:"value,omitempty" url:"-"`
	// IsBase64 marks Value as drawn signature image bytes rather than a typed
	// signature. Only a signature field reads it, and a `data:` URL prefix is
	// stripped.
	IsBase64 goja.Scalar `json:"isBase64,omitempty" url:"-"`
}

// UnmarshalJSON measures the body and refuses only what the relay refused.
func (in *esignValueIn) UnmarshalJSON(b []byte) error {
	type alias esignValueIn
	in.Fill(maxBody, b, (*alias)(in))
	return unreadable(in.SizedIn, b)
}

// esignRejectIn declines to sign.
type esignRejectIn struct {
	goja.SizedIn
	esignTokenRef
	// Reason is why the signer is declining. Optional, stored, and written onto the
	// audit trail with the rejection — it is what the sender sees.
	Reason goja.Scalar `json:"reason,omitempty" url:"-"`
}

// UnmarshalJSON measures the body and refuses only what the relay refused.
func (in *esignRejectIn) UnmarshalJSON(b []byte) error {
	type alias esignRejectIn
	in.Fill(maxBody, b, (*alias)(in))
	return unreadable(in.SizedIn, b)
}

// esignCompletionIn finishes this signer's part. It carries no body of its own but
// measures one, because the relay it replaces capped one.
//
// It is NOT esignCompleteIn, which apps/company already publishes with a different
// shape ({signed: bool}, for POST /v1/company/esign/complete). The name is free
// today only because this input's fields are all path segments, so it publishes no
// body and never enters the flat fleet schema namespace — and that is exactly the
// landmine: the day this op gains one body field, openapi.Compose would refuse the
// whole package for a clash that looks like it came from company. Failure mode #5
// in its cheap form: the app whose schema is not yet published is the one that
// moves, and it moves before the collision, not after.
type esignCompletionIn struct {
	goja.SizedIn
	esignTokenRef
}

// UnmarshalJSON measures the body and refuses only what the relay refused.
func (in *esignCompletionIn) UnmarshalJSON(b []byte) error {
	type alias esignCompletionIn
	in.Fill(maxBody, b, (*alias)(in))
	return unreadable(in.SizedIn, b)
}

// ─────────────────────────────────────────────────────────────────────────────
// answers
// ─────────────────────────────────────────────────────────────────────────────

// esignHealth is the subsystem's liveness answer.
type esignHealth struct {
	// Service names the subsystem that answered, so a probe reading several looks
	// the same on each.
	Service string `json:"service"`
	// Status is ok whenever the subsystem is mounted. It is never anything else:
	// this route is registered before the document host is built, so it is
	// reachability and not a promise that documents can be stored.
	Status string `json:"status"`
}

// esignField is one field placed on the page — where it sits, who fills it, and
// what has been put in it.
type esignField struct {
	// CustomText is the value a non-signature field was filled with, empty until it
	// is. A signature's value is not here: it is stored separately and rendered
	// onto the page at sealing.
	CustomText string `json:"customText"`
	// FieldMeta is the caller's own metadata for this field, stored verbatim at
	// placement and never interpreted. Null when none was supplied.
	FieldMeta json.RawMessage `json:"fieldMeta"`
	// Height is the field's height, -1 when the renderer is to choose one.
	Height float64 `json:"height"`
	// ID is the field id.
	ID string `json:"id"`
	// Inserted is whether this field has been filled in.
	Inserted bool `json:"inserted"`
	// Page is the 1-based page the field sits on.
	Page float64 `json:"page"`
	// PositionX is the field's horizontal position on that page.
	PositionX float64 `json:"positionX"`
	// PositionY is the field's vertical position on that page.
	PositionY float64 `json:"positionY"`
	// RecipientID is who must fill this field. It is absent on a signer's own view
	// of a document, where every field returned is already theirs.
	RecipientID string `json:"recipientId,omitempty"`
	// Type is what the field collects — SIGNATURE, DATE, NAME, EMAIL, TEXT and the
	// rest.
	Type string `json:"type"`
	// Width is the field's width, -1 when the renderer is to choose one.
	Width float64 `json:"width"`
}

// esignRecipient is one party on a document, as the SENDER sees them. It carries
// no signing token: those are answered only where they are minted, so listing a
// document cannot hand one recipient another's credential.
type esignRecipient struct {
	// Email is where this recipient's signing link is meant to go, lower-cased.
	Email string `json:"email"`
	// ID is the recipient id, which is what a field is placed against.
	ID string `json:"id"`
	// Name is the recipient's display name, empty when none was given.
	Name string `json:"name"`
	// ReadStatus is NOT_OPENED until they first open their link, then OPENED.
	ReadStatus string `json:"readStatus"`
	// RejectionReason is why they declined, null unless they did.
	RejectionReason *string `json:"rejectionReason"`
	// Role is SIGNER, CC, VIEWER, APPROVER or ASSISTANT. A document waits only for
	// its SIGNERs and APPROVERs before it can seal.
	Role string `json:"role"`
	// SendStatus is NOT_SENT until the document goes out, then SENT. A CC recipient
	// is SENT from the moment they are added.
	SendStatus string `json:"sendStatus"`
	// SignedAt is when they finished or declined, in unix milliseconds; null while
	// neither has happened.
	SignedAt *int64 `json:"signedAt"`
	// SigningOrder is their position in a SEQUENTIAL document, null when they were
	// added without one. A PARALLEL document ignores it.
	SigningOrder *float64 `json:"signingOrder"`
	// SigningStatus is NOT_SIGNED, SIGNED or REJECTED. A CC recipient is SIGNED
	// from the moment they are added, because they are never asked.
	SigningStatus string `json:"signingStatus"`
}

// esignDocument is one document with its recipients and field layout — the view a
// sender's UI renders, and where the recipient and field ids come from.
type esignDocument struct {
	// CompletedAt is when the document sealed, in unix milliseconds; null until it
	// does.
	CompletedAt *int64 `json:"completedAt"`
	// CreatedAt is when the document was uploaded, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// ExternalID is the caller's own identifier for this document, echoed back as
	// it was given; null when none was.
	ExternalID *string `json:"externalId"`
	// Fields is every field on the document, ordered by page and then by when it
	// was placed.
	Fields []esignField `json:"fields"`
	// ID is the document id.
	ID string `json:"id"`
	// Message is the covering message stored with the document; null when none was
	// given. Nothing in this surface sends it.
	Message *string `json:"message"`
	// Recipients is everyone on the document, ordered by signing order and then by
	// when they were added — which is also the order a SEQUENTIAL document enforces.
	Recipients []esignRecipient `json:"recipients"`
	// SigningOrder is PARALLEL or SEQUENTIAL, fixed when the document was created.
	SigningOrder string `json:"signingOrder"`
	// Source is how the document came to exist. It is DOCUMENT for everything this
	// surface creates.
	Source string `json:"source"`
	// Status is DRAFT while recipients and fields may still be added, PENDING once
	// it has gone out, then COMPLETED when every signer has finished or REJECTED if
	// any one of them declined.
	Status string `json:"status"`
	// Subject is the covering subject line stored with the document; null when none
	// was given.
	Subject *string `json:"subject"`
	// Title is the document's name, and the stem of the download filename.
	Title string `json:"title"`
	// UpdatedAt is when the document last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// esignDocuments is the caller org's documents.
type esignDocuments struct {
	// Documents is the caller org's documents, newest first, capped at 200. There
	// is no paging, so read it as the recent window rather than a complete export.
	Documents []esignDocument `json:"documents"`
}

// esignInvite is a newly added recipient together with the signing capability
// minted for them. It is one of only two places a token is ever answered.
type esignInvite struct {
	// Email is the address the invitation is for, lower-cased.
	Email string `json:"email"`
	// ID is the new recipient's id, which is what a field is placed against.
	ID string `json:"id"`
	// Name is the recipient's display name, empty when none was given.
	Name string `json:"name"`
	// Role is the role they were recorded with — SIGNER unless another was asked
	// for.
	Role string `json:"role"`
	// Token is the crypto-random signing capability for this recipient. It is the
	// entire credential their door accepts, so treat it as a secret and hand it
	// only to them: the signing link is built from it.
	Token string `json:"token"`
}

// esignPlacement is a field just placed on the page.
type esignPlacement struct {
	// ID is the new field's id.
	ID string `json:"id"`
	// Page is the page it was placed on.
	Page float64 `json:"page"`
	// RecipientID is who must fill it.
	RecipientID string `json:"recipientId"`
	// Type is what it collects.
	Type string `json:"type"`
}

// esignLink is one signer's way in: the token, and the path to hand them.
type esignLink struct {
	// Email is the address this link is meant for.
	Email string `json:"email"`
	// RecipientID is the recipient the link identifies.
	RecipientID string `json:"recipientId"`
	// Role is their role — only a SIGNER or an APPROVER gets a link, because only
	// they are asked to act.
	Role string `json:"role"`
	// SigningPath is the tail of the address to send them, relative to wherever the
	// signing page is served.
	SigningPath string `json:"signingPath"`
	// Token is the crypto-random signing capability. It is the entire credential,
	// so treat it as a secret and give each one only to the recipient it names.
	Token string `json:"token"`
}

// esignLinks is a sent document and the way in for each of its signers.
type esignLinks struct {
	// ID is the document that went out.
	ID string `json:"id"`
	// Recipients is one link per signing recipient. Nothing is emailed by this
	// call; delivering the links is the caller's.
	Recipients []esignLink `json:"recipients"`
	// Status is PENDING — the state a sent document is in until every signer has
	// finished.
	Status string `json:"status"`
}

// esignPDF is the document's current PDF.
type esignPDF struct {
	// Filename is the name to save it under, built from the title and marked
	// _signed once it is sealed.
	Filename string `json:"filename"`
	// ID is the document.
	ID string `json:"id"`
	// PdfBase64 is the PDF itself, base64-encoded. There is one field either way,
	// so Sealed is what tells you which artifact you are holding.
	PdfBase64 string `json:"pdfBase64"`
	// Sealed is whether this is the SEALED artifact — the field values rendered
	// onto the page and a real x509 PKCS#7 signature applied — rather than the
	// original upload.
	Sealed bool `json:"sealed"`
	// Status is the document's state at the moment it was read.
	Status string `json:"status"`
}

// esignEvent is one entry on the audit trail.
type esignEvent struct {
	// CreatedAt is when it happened, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// Data is the event's own detail, whose shape depends on the type — the field
	// and recipient a signature was inserted for, the reason a document was
	// rejected. Null when the event carried none.
	Data json.RawMessage `json:"data"`
	// Email is the email of whoever caused it, null for an event with no actor —
	// the sender's own calls are recorded without one.
	Email *string `json:"email"`
	// ID is the entry id.
	ID string `json:"id"`
	// Name is the name of whoever caused it, null when it was not recorded.
	Name *string `json:"name"`
	// Type is what happened: DOCUMENT_CREATED, RECIPIENT_CREATED, FIELD_CREATED,
	// DOCUMENT_SENT, DOCUMENT_OPENED, DOCUMENT_FIELD_INSERTED,
	// DOCUMENT_RECIPIENT_COMPLETED, DOCUMENT_RECIPIENT_REJECTED or
	// DOCUMENT_COMPLETED.
	Type string `json:"type"`
}

// esignTrail is a document's evidence record — the whole history behind its
// signatures.
type esignTrail struct {
	// DocumentID is the document the trail belongs to.
	DocumentID string `json:"documentId"`
	// Entries is every recorded event in order, oldest first. It is append-only:
	// nothing in this surface edits or removes an entry.
	Entries []esignEvent `json:"entries"`
}

// esignState is the document as a SIGNER sees it: enough to know what they are
// being asked to sign, and nothing about the other parties.
type esignState struct {
	// ID is the document id.
	ID string `json:"id"`
	// Status is PENDING while it is out for signature.
	Status string `json:"status"`
	// Title is the document's name.
	Title string `json:"title"`
}

// esignSigner is the recipient a signing token identifies — you, at this door.
type esignSigner struct {
	// Email is the address the link was issued to.
	Email string `json:"email"`
	// ID is the recipient id.
	ID string `json:"id"`
	// Name is the display name recorded for them, empty when none was given.
	Name string `json:"name"`
	// Role is the role they were added with.
	Role string `json:"role"`
	// SigningStatus is NOT_SIGNED until they finish or decline.
	SigningStatus string `json:"signingStatus"`
}

// esignSession is everything the signer's page needs: what they are signing, who
// the link says they are, the fields THEY must fill, and the PDF to display.
type esignSession struct {
	// Document is what is being signed.
	Document esignState `json:"document"`
	// Fields is only the fields this recipient must fill — never another party's,
	// so the layout a signer sees cannot reveal what anyone else was asked for.
	Fields []esignField `json:"fields"`
	// PdfBase64 is the PDF to display, base64-encoded. Null when the document's
	// stored bytes are missing.
	PdfBase64 *string `json:"pdfBase64"`
	// Recipient is who the token says you are.
	Recipient esignSigner `json:"recipient"`
}

// esignInsertion is one field recorded as filled.
type esignInsertion struct {
	// FieldID is the field that was filled.
	FieldID string `json:"fieldId"`
	// Inserted is true — the field now holds a value. Filling every field still
	// leaves the document pending until the completion call.
	Inserted bool `json:"inserted"`
}

// esignRejection is a refusal to sign, and what it did to the document.
type esignRejection struct {
	// RecipientID is the recipient who declined.
	RecipientID string `json:"recipientId"`
	// Status is REJECTED — one declining signer ends the document for everyone, and
	// there is no route back.
	Status string `json:"status"`
}

// esignCompletion is one signer finishing, and whether that finished the document.
type esignCompletion struct {
	// DocumentStatus is COMPLETED when this was the last signature and the document
	// sealed here, PENDING while others have still to sign.
	DocumentStatus string `json:"documentStatus"`
	// RecipientID is the recipient who finished.
	RecipientID string `json:"recipientId"`
	// Sealed is whether the document sealed on this call — the field values
	// rendered onto the PDF and a real x509 PKCS#7 signature applied.
	Sealed bool `json:"sealed"`
}

// ─────────────────────────────────────────────────────────────────────────────
// the sender's door — a validated principal, scoped to its own org
// ─────────────────────────────────────────────────────────────────────────────

// health reports whether the e-signature surface is mounted.
//
// It answers ok whenever the subsystem is mounted, takes no tenant and needs no
// principal. It is deliberately shallow: it is registered before the document host
// is built, so it still answers on a deployment that came up WITHOUT object
// storage and therefore serves nothing else. Read it as reachability, never as a
// promise that documents can be stored.
func health(context.Context, *esignNone) (*esignHealth, error) {
	return &esignHealth{Service: "esign", Status: "ok"}, nil
}

// CreateDocument uploads a PDF and opens a draft ready for recipients and fields.
//
// It answers 201 with the document in DRAFT — the state where recipients and
// fields may still be added, and the only state they may. The bytes go to object
// storage rather than into the tenant database, and the original is kept under its
// own key so it survives sealing untouched: a completed document can always be
// compared against what was uploaded. Creation is recorded on the audit trail.
//
// This is the sender's door: a validated principal is required, and the document
// lands in that principal's OWN org. Isolation is physical rather than a filter —
// each tenant has its own store — so another org's document id is simply not
// there. A body over 32 MiB is refused with 413.
//
// Example: {"title":"Mutual NDA","pdfBase64":"JVBERi0xLjQK…","signingOrder":"SEQUENTIAL"}
func (o ops) createDocument(ctx context.Context, in *esignUploadIn) (*esignDocument, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignDocument
	return &out, o.write(ctx, org, "documents.create", in.SizedIn, nil, map[string]goja.BodyField{
		"title":        in.Title,
		"pdfBase64":    in.PdfBase64,
		"externalId":   in.ExternalID,
		"subject":      in.Subject,
		"message":      in.Message,
		"signingOrder": in.SigningOrder,
	}, &out)
}

// ListDocuments returns your org's documents, newest first.
//
// Each carries its status, recipients and field layout. The listing is capped at
// 200 and there is no paging, so treat it as the recent window rather than a
// complete export. It reads the caller's own tenant store, so no other org's
// documents can appear in it.
func (o ops) listDocuments(ctx context.Context, _ *esignNone) (*esignDocuments, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignDocuments
	return &out, o.run(ctx, org, "documents.list", nil, nil, &out)
}

// GetDocument returns one document with its recipients and field layout.
//
// It answers the document, its recipients with each one's read and signing status,
// and every field with its type, page and position — the view a sender's UI
// renders, and where the field ids come from. The id is resolved in the caller's
// OWN tenant store, so another org's document id is a 404 rather than a refusal
// that would confirm it exists.
func (o ops) getDocument(ctx context.Context, in *esignRef) (*esignDocument, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignDocument
	return &out, o.run(ctx, org, "documents.get", map[string]string{"id": in.ID}, nil, &out)
}

// AddRecipient adds someone to a draft and mints their signing token.
//
// It answers 201 with the recipient's id and their signing TOKEN — the
// crypto-random capability that is the only credential the signer's door accepts —
// so this response is where the signing link is built from. A CC recipient is
// recorded as already complete, because they are never asked to sign.
//
// Only while DRAFT: adding a recipient to a document already sent is a 409,
// because the field layout and the turn order were fixed when it went out. An
// unknown document is a 404. The addition is recorded on the audit trail.
//
// Example: {"email":"counterparty@example.com","name":"Dana Lee","role":"SIGNER"}
func (o ops) addRecipient(ctx context.Context, in *esignRecipientIn) (*esignInvite, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignInvite
	return &out, o.write(ctx, org, "recipients.add", in.SizedIn, map[string]string{"id": in.ID},
		map[string]goja.BodyField{
			"email":        in.Email,
			"name":         in.Name,
			"role":         in.Role,
			"signingOrder": in.SigningOrder,
		}, &out)
}

// AddField places a field on the page for one recipient to fill.
//
// It adds a signature, date, name, email or text box at a page and position for
// ONE named recipient, and answers 201 with its id. The recipient must belong to
// this document; one from elsewhere is refused.
//
// Fields are what make a recipient signable: a document cannot be sent while any
// signing recipient has none. Only while DRAFT — adding a field to a sent document
// is a 409 — and an unknown document is a 404. The addition is recorded on the
// audit trail.
//
// Example: {"recipientId":"rec_2f…","type":"SIGNATURE","page":1,"positionX":72,"positionY":640}
func (o ops) addField(ctx context.Context, in *esignFieldIn) (*esignPlacement, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignPlacement
	return &out, o.write(ctx, org, "fields.add", in.SizedIn, map[string]string{"id": in.ID},
		map[string]goja.BodyField{
			"recipientId": in.RecipientID,
			"type":        in.Type,
			"page":        in.Page,
			"positionX":   in.PositionX,
			"positionY":   in.PositionY,
			"width":       in.Width,
			"height":      in.Height,
			"fieldMeta":   in.FieldMeta,
		}, &out)
}

// SendDocument sends the document out and answers each signer's link.
//
// It moves the document from DRAFT to PENDING and answers the signing tokens — one
// per signing recipient, with the path to hand them — which is how the links reach
// the people who must sign. Nothing is emailed by this call; delivering the links
// is the caller's.
//
// It refuses to send an unsignable document: no recipients at all is a 400, and so
// is any signing recipient with no fields to fill, named in the error. Re-sending
// an already-pending document is allowed and re-issues the same links rather than
// restarting anything; a completed document is a 409, and an unknown one a 404.
// The send is recorded on the audit trail.
func (o ops) sendDocument(ctx context.Context, in *esignSendIn) (*esignLinks, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignLinks
	return &out, o.write(ctx, org, "documents.send", in.SizedIn, map[string]string{"id": in.ID}, nil, &out)
}

// DownloadDocument returns the document — the sealed PDF once it is complete.
//
// It answers the document's current PDF as base64 with a sealed flag and a
// filename. Before completion that is the original upload; once every signer has
// finished it is the SEALED artifact, with the field values rendered onto the page
// and a real x509 PKCS#7 digital signature applied. There is one pdfBase64 field
// either way, so sealed is what tells you which you are holding.
//
// The id is resolved in the caller's OWN tenant store, so another org's document
// id is a 404.
func (o ops) downloadDocument(ctx context.Context, in *esignRef) (*esignPDF, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignPDF
	return &out, o.run(ctx, org, "documents.download", map[string]string{"id": in.ID}, nil, &out)
}

// AuditDocument returns the document's full audit trail, oldest first.
//
// It answers every recorded event for the document in order — created, recipient
// added, field created, sent, opened, each field inserted, each recipient
// completed or rejected, and completion — with the actor and timestamp on each.
// This is the evidence record behind a signature, so it is append-only and nothing
// in the surface edits it.
//
// The id is resolved in the caller's OWN tenant store, so another org's document
// id is a 404.
func (o ops) auditDocument(ctx context.Context, in *esignRef) (*esignTrail, error) {
	org, err := o.owner(ctx)
	if err != nil {
		return nil, err
	}
	var out esignTrail
	return &out, o.run(ctx, org, "documents.audit", map[string]string{"id": in.ID}, nil, &out)
}

// ─────────────────────────────────────────────────────────────────────────────
// the signer's door — no account, the token IS the credential
// ─────────────────────────────────────────────────────────────────────────────

// ViewSigning opens a document you were asked to sign, using your signing link.
//
// It answers the document, the recipient the link identifies, the fields THAT
// recipient must fill, and the PDF to display. The first open also marks the
// recipient as having opened it and records that on the audit trail, so this read
// has a side effect by design.
//
// This door takes NO account: the signing token is the entire credential, and it
// names the recipient, so a signer sees only their own fields and never the other
// recipients' tokens. The token resolves to its owning tenant FIRST, before any
// per-tenant store is opened, and the org segment is only checked against that
// answer. An unknown or wrong-org token is one and the same 404, never a hint that
// some other document exists.
func (o ops) viewSigning(ctx context.Context, in *esignTokenRef) (*esignSession, error) {
	org, err := o.signer(*in)
	if err != nil {
		return nil, err
	}
	var out esignSession
	return &out, o.run(ctx, org, "sign.view", map[string]string{"org": org, "token": in.Token}, nil, &out)
}

// SignField fills in one of your fields.
//
// It records a value for one field and marks it inserted. A signature field takes
// a value with isBase64 true for drawn image bytes, or false for a typed
// signature; a date, name or email field falls back to today, the recipient's name
// or their email when the value is omitted; any other type requires one.
//
// Nothing is sealed here — filling every field still leaves the document pending
// until the completion call. The token is the whole credential and it bounds what
// can be written: a field belonging to another recipient is refused with 401 even
// under a valid token, an unknown field is a 404, and a field already filled is a
// 409. A document not out for signature is a 409, as is a recipient who has
// already completed or rejected. Under SEQUENTIAL order a signer whose turn has
// not come is refused 403 until every earlier signer has signed. Each insertion is
// recorded on the audit trail.
//
// Example: {"value":"Dana Lee","isBase64":false}
func (o ops) signField(ctx context.Context, in *esignValueIn) (*esignInsertion, error) {
	org, err := o.signer(in.esignTokenRef)
	if err != nil {
		return nil, err
	}
	var out esignInsertion
	return &out, o.write(ctx, org, "sign.field", in.SizedIn,
		map[string]string{"org": org, "token": in.Token, "fieldId": in.FieldID},
		map[string]goja.BodyField{"value": in.Value, "isBase64": in.IsBase64}, &out)
}

// CompleteSigning finishes your signing — and seals the document if you were the
// last.
//
// It marks this recipient as done and answers whether the DOCUMENT sealed with it.
// When every signing recipient has completed, sealing happens right here in the
// same call: the collected values are rendered onto the PDF, a real x509 PKCS#7
// signature is applied, the sealed bytes are stored beside the untouched original,
// and the document moves to COMPLETED. Until then the answer is the recipient's
// own completion with the document still pending.
//
// It refuses to complete a half-filled signature: a recipient with any unfilled
// field is a 400 naming how many remain. A document not out for signature is a
// 409, as is a recipient who has already completed, and under SEQUENTIAL order a
// signer out of turn is a 403. The token is the whole credential — no account, and
// a token that does not resolve under the org segment is a 404. Sealing and
// completion are one transaction, so a failure anywhere leaves the document
// exactly as it was.
func (o ops) completeSigning(ctx context.Context, in *esignCompletionIn) (*esignCompletion, error) {
	org, err := o.signer(in.esignTokenRef)
	if err != nil {
		return nil, err
	}
	var out esignCompletion
	return &out, o.write(ctx, org, "sign.complete", in.SizedIn,
		map[string]string{"org": org, "token": in.Token}, nil, &out)
}

// RejectSigning declines to sign, with an optional reason.
//
// It records this recipient's refusal and moves the WHOLE DOCUMENT to REJECTED —
// one declining signer ends it for everyone, and there is no route back: the
// document cannot then be signed or completed. An optional reason is stored and
// written onto the audit trail with the rejection, which is what the sender sees.
//
// A document not out for signature is a 409, and so is a recipient who has already
// signed or already rejected — a refusal cannot be taken back or repeated. The
// token is the whole credential; one that does not resolve under the org segment
// is a 404.
//
// Example: {"reason":"Terms changed since we agreed them."}
func (o ops) rejectSigning(ctx context.Context, in *esignRejectIn) (*esignRejection, error) {
	org, err := o.signer(in.esignTokenRef)
	if err != nil {
		return nil, err
	}
	var out esignRejection
	return &out, o.write(ctx, org, "sign.reject", in.SizedIn,
		map[string]string{"org": org, "token": in.Token},
		map[string]goja.BodyField{"reason": in.Reason}, &out)
}

// declare registers the esign surface as typed ops on the subsystem's own group,
// so each op's path is the prefix composed with its leaf — the same composition
// the router does, and the identity every projection keys on. The group is built
// HERE rather than passed in because cmd/zipdoc resolves an op's prefix by finding
// `g := <router>.Group("/prefix")` in the SAME file as the registration, and
// refuses generation outright when it cannot — the right refusal, since filing
// prose under a path the API does not serve loses it silently from both the
// document and the tool list.
//
// ONE group, and that is load-bearing rather than tidy. zip's Group returns a NEW
// App included at the prefix (compose.go:325), NOT a handle on a shared stack, so
// two calls for /v1/esign are two sub-applications with two middleware stacks and
// a Use on the first never reaches a leaf on the second. It is the ordering trap
// one level up: not "installed after its leaves" but "installed on a different
// group", and it fails the same silent way.
//
// Bridge FIRST: a typed op receives only a context, so the validated org reaches
// it by being parked there, never as an In field. Then the bundle's own envelope,
// which writes a goja.BundleErr's status and BYTES back verbatim — without it a
// bundle refusal is rendered in zip's vocabulary instead of its own. Middleware
// runs in registration order, so both precede every leaf.
//
// s is nil when the document plane could not be opened, and then health is the
// whole served surface. Every op below is still DECLARED in the source, which is
// what zipdoc reads, and none of the rest is REGISTERED, which is what the
// document reads — so a health-only deployment publishes health alone rather than
// advertising twelve operations nothing answers.
func declare(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/esign")
	g.Use(cloud.Bridge())
	g.Use(goja.Envelope())

	// Health answers whenever the subsystem is mounted (OwnsHealth), no JS, no
	// auth. It is a plain function rather than a method because it reads no
	// service — which is also why it survives an unopened plane.
	zip.Get(g, "/health", health)
	if s == nil {
		return
	}
	o := ops{s: s}

	// The sender's door.
	zip.Post(g, "/documents", o.createDocument, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/documents", o.listDocuments)
	zip.Get(g, "/documents/:id", o.getDocument)
	zip.Post(g, "/documents/:id/recipients", o.addRecipient, zip.WithStatus(http.StatusCreated))
	zip.Post(g, "/documents/:id/fields", o.addField, zip.WithStatus(http.StatusCreated))
	zip.Post(g, "/documents/:id/send", o.sendDocument)
	zip.Get(g, "/documents/:id/download", o.downloadDocument)
	zip.Get(g, "/documents/:id/audit", o.auditDocument)

	// The signer's door.
	zip.Get(g, "/o/:org/sign/:token", o.viewSigning)
	zip.Post(g, "/o/:org/sign/:token/fields/:fieldId", o.signField)
	zip.Post(g, "/o/:org/sign/:token/complete", o.completeSigning)
	zip.Post(g, "/o/:org/sign/:token/reject", o.rejectSigning)
}
