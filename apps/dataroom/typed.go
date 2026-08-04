package dataroom

// typed.go is dataroom's TYPED plane — the ops that carry In/Out types, and
// therefore the only dataroom routes that reach the published document, the MCP
// tool list, the CLI and the generated SDKs. An untyped route contributes a path
// and a method and nothing else, so before this file an agent asking the fleet
// door what it could do was told about company, captable and esign, and never
// that a data room could be opened at all.
//
// WHAT IS TYPED. Every JSON route on the ADMIN surface: the four collection and
// detail reads, the two analytics rollups, and the four writes. Each relays the
// goja bundle's own (status, body) exactly as the untyped handler beside it did —
// the bundle decides the answer, the Go host carries it — through the shared
// bundle-backed kit in apps/goja (Scalar, ScalarList, SizedIn, BundleErr,
// Envelope), which is the same kit captable's typed plane runs on.
//
// The package doc's claim that "NONE of the routes above can be a typed op"
// described the FIRST cut and is retired by this file. It rested on two premises
// that the kit answers: that a relayed answer is opaque (it is not — the bundle's
// shapers are total and the DDL types them, which is what the models below are),
// and that a typed error path would overwrite the bundle's own envelope (it does
// not — goja.BundleErr carries the bundle's status and BYTES, and goja.Envelope
// writes them back verbatim).
//
// WHAT IS NOT TYPED, AND WHY. Four routes remain relays, and each has a reason
// in the WIRE rather than in effort:
//
//   - POST /documents takes the file ITSELF as the raw request body, and the two
//     /file routes answer with a byte stream off object storage. No In/Out pair
//     describes bytes; typing them would mean inventing a base64 envelope the
//     route has never spoken.
//   - The three /view/* viewer routes carry NO principal — their tenant is
//     resolved from the public link index, not from the caller — so they have no
//     validated org for tenantOf to read. They are also the surface a visitor's
//     BROWSER drives, not the surface an agent calls.
//
// FIELD ORDER IS LOAD-BEARING, ONCE. The bundle's rows cross the goja boundary as
// map[string]any and are serialised by encoding/json, which sorts object keys.
// Every model below therefore declares its fields in ALPHABETICAL json-tag order,
// so the typed response is BYTE-identical to the relay it replaces — not merely
// equal as JSON. TestTypedReadsAreByteIdenticalToTheBundle pins that against the
// bundle's own bytes, so a field added out of order fails rather than drifting.
//
// NULLABILITY IS THE SCHEMA'S. A column schema.go leaves nullable arrives as JSON
// null, so it is a POINTER here; a NOT NULL column is a value. The booleans are
// real JS booleans by the time they cross (the bundle's truthy() shapes them from
// INTEGER 0/1), so they are bool and not *bool.

import (
	"context"
	"encoding/json"
	"net/http"

	hcloud "github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// ops binds the service to the typed dataroom ops. A TypedHandler takes no
// service parameter, so the service arrives as a RECEIVER and every op is a
// method value — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *hcloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire. The dataroom collection reads are org-scoped, so
// the org IS the address and there is no parameter to bind.
type noInput struct{}

// tenantOf is the validated org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. It is the same principal.Org answer the untyped
// dispatch reads off the request, so both planes gate identically.
func tenantOf(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// bundleMessage is the human sentence in a dataroom refusal, for the Error()
// string an off-HTTP caller sees. The bundle's envelope is a single `error` key
// (its err() helper, and its top-level catch), which is a SHAPE OF ITS OWN — this
// is why the message extractor stays with the app while the carrier is shared. A
// body that is not an envelope falls back to the status text, so the error is
// never empty.
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
	return "dataroom dispatch failed"
}

// call is the response path for a BODYLESS typed op: resolve the tenant, run the
// bundle route on that tenant's store, and decode a 2xx body into out. A non-2xx
// is the BUNDLE's answer and comes back as a goja.BundleErr, so the client gets
// the same bytes under the same status the untyped relay wrote.
func (o ops) call(ctx context.Context, route string, params map[string]string, out any) error {
	org, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return o.run(ctx, org, route, params, nil, out)
}

// write is the response path for a BODY-CARRYING typed op: refuse an oversized
// body with the relay's 413 — after the tenant, before the work, the order the
// relay used — then assemble the caller's verbatim tokens and dispatch.
func (o ops) write(ctx context.Context, route string, size goja.SizedIn, params map[string]string, fields map[string]goja.BodyField, out any) error {
	org, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if size.Oversize() {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	body, err := goja.Body(fields)
	if err != nil {
		o.s.Log.Error("dataroom body assembly failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	return o.run(ctx, org, route, params, body, out)
}

// run is the shared tail of BOTH typed paths. It runs the bundle route on the
// tenant's store and decodes the answer, so there is ONE place that turns a
// bundle response into either an out value or a goja.BundleErr.
//
// Only a failure of the HOST itself — the engine never ran, or it answered
// something that is not the out shape — becomes cloud's own 500, which is the
// answer the untyped dispatch already gives in exactly that case.
func (o ops) run(ctx context.Context, org, route string, params map[string]string, body any, out any) error {
	resp, err := o.s.State.host.Dispatch(ctx, org, goja.BaseRequest{Route: route, Params: params, Body: body})
	if err != nil {
		o.s.Log.Error("dataroom dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	if resp.Status/100 != 2 {
		return &goja.BundleErr{Status: resp.Status, Body: resp.Body, Msg: bundleMessage(resp.Status, resp.Body)}
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		o.s.Log.Error("dataroom response decode failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	return nil
}

// ---- documents ----

// dataroomDocument is one uploaded file's metadata. The BYTES are not here: they
// live on the object-storage seam under fileKey and are read by the file routes.
type dataroomDocument struct {
	// ContentType is the mime type recorded at upload, null when none was sent.
	ContentType *string `json:"contentType"`
	// CreatedAt is when the document was uploaded, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// FileKey is the opaque object-storage key the bytes are stored under. It is
	// scoped to the tenant's own key prefix and is not a URL.
	FileKey string `json:"fileKey"`
	// FileSize is the stored byte count, null when it was not recorded.
	FileSize *int64 `json:"fileSize"`
	// ID is the document id.
	ID string `json:"id"`
	// Name is the document's display name.
	Name string `json:"name"`
	// NumPages is the page count, null when it was not supplied at upload.
	NumPages *int64 `json:"numPages"`
	// Type is the document's kind, null when it was not recorded.
	Type *string `json:"type"`
	// UpdatedAt is when the document row last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// dataroomDocuments is the caller org's documents.
type dataroomDocuments struct {
	// Documents is every document in the caller's own store, newest first.
	Documents []dataroomDocument `json:"documents"`
}

// ListDocuments returns every document in the caller org's own store, newest
// first — name, opaque storage key, content type, page count, size and
// timestamps.
//
// Tenant isolation is the per-org store itself: there is one SQLite file per org
// and the org is never a parameter, so no input the caller controls can address
// another tenant's documents. Metadata only — the bytes come from the file route.
func (o ops) listDocuments(ctx context.Context, _ *noInput) (*dataroomDocuments, error) {
	var out dataroomDocuments
	if err := o.call(ctx, "documents.list", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// documentRef addresses one of the caller org's documents.
type documentRef struct {
	// ID is the document to read. It is the path segment: the URL is the
	// addressing authority, and the org it is resolved in comes from the caller's
	// principal, so an id from another tenant is simply not found.
	ID string `json:"id"`
}

// dataroomDocumentOne is one document's metadata.
type dataroomDocumentOne struct {
	// Document is the document itself.
	Document dataroomDocument `json:"document"`
}

// GetDocument reads one of the caller org's documents — its name, opaque storage
// key, content type, page count, size and timestamps.
//
// The lookup runs in the caller's own tenant store, so an id belonging to another
// org is not found exactly like one that never existed. Metadata only: the bytes
// are a separate read.
func (o ops) getDocument(ctx context.Context, in *documentRef) (*dataroomDocumentOne, error) {
	var out dataroomDocumentOne
	if err := o.call(ctx, "documents.get", map[string]string{"id": in.ID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- data rooms ----

// dataroomRoom is one data room: a named collection of documents that a share
// link is opened over.
type dataroomRoom struct {
	// CreatedAt is when the room was created, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// Description is the room's description, null when none was given.
	Description *string `json:"description"`
	// ID is the room id, which is what other dataroom calls address it by.
	ID string `json:"id"`
	// Name is the room's display name.
	Name string `json:"name"`
	// PId is the room's short public identifier, unique within the tenant.
	PId string `json:"pId"`
	// UpdatedAt is when the room last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// dataroomRooms is the caller org's data rooms.
type dataroomRooms struct {
	// Datarooms is every data room in the caller's own store, newest first.
	Datarooms []dataroomRoom `json:"datarooms"`
}

// ListDatarooms returns every data room in the caller org's own store, newest
// first, with its short public id, name, description and timestamps.
//
// Documents are not included — a room's contents come from reading the single
// room.
func (o ops) listDatarooms(ctx context.Context, _ *noInput) (*dataroomRooms, error) {
	var out dataroomRooms
	if err := o.call(ctx, "datarooms.list", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// dataroomCreate is the data room to create.
type dataroomCreate struct {
	goja.SizedIn
	// Description is the room's description. Optional; any JSON scalar is
	// accepted and stored as its text, and omitting it leaves the room with none.
	Description goja.Scalar `json:"description,omitempty"`
	// Name is the room's display name. Required, and a room without one is
	// refused with the data room's own validation error.
	Name goja.Scalar `json:"name,omitempty"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the data room. See goja.SizedIn.Fill.
func (in *dataroomCreate) UnmarshalJSON(b []byte) error {
	type body dataroomCreate // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = dataroomCreate(v)
	return nil
}

// dataroomRoomOne is one data room.
type dataroomRoomOne struct {
	// Dataroom is the room itself.
	Dataroom dataroomRoom `json:"dataroom"`
}

// CreateDataroom opens a new data room for the caller org and answers with it,
// including the short public id it is addressed by.
//
// `name` is required; without it the call is refused and the tenant store is
// untouched, because a dispatch answering 4xx rolls its transaction back. A new
// room holds no documents and is reachable by NOBODY until a share link is
// created over it — opening a room and granting access are two separate acts, so
// a room cannot leak by existing.
func (o ops) createDataroom(ctx context.Context, in *dataroomCreate) (*dataroomRoomOne, error) {
	var out dataroomRoomOne
	err := o.write(ctx, "datarooms.create", in.SizedIn, nil, map[string]goja.BodyField{
		"description": in.Description,
		"name":        in.Name,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// dataroomRef addresses one of the caller org's data rooms.
type dataroomRef struct {
	// ID is the room to read. It is the path segment: the URL is the addressing
	// authority, and the org it is resolved in comes from the caller's principal,
	// so an id from another tenant is simply not found.
	ID string `json:"id"`
}

// dataroomMember is one document as it sits INSIDE a data room: the document's
// own metadata plus the membership that put it there.
type dataroomMember struct {
	// ContentType is the mime type recorded at upload, null when none was sent.
	ContentType *string `json:"contentType"`
	// CreatedAt is when the document was uploaded, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// DataroomDocumentId is the membership id — this document's place in THIS
	// room, distinct from the document id.
	DataroomDocumentId string `json:"dataroomDocumentId"`
	// FileKey is the opaque object-storage key the bytes are stored under.
	FileKey string `json:"fileKey"`
	// FileSize is the stored byte count, null when it was not recorded.
	FileSize *int64 `json:"fileSize"`
	// ID is the document id.
	ID string `json:"id"`
	// Name is the document's display name.
	Name string `json:"name"`
	// NumPages is the page count, null when it was not supplied at upload.
	NumPages *int64 `json:"numPages"`
	// OrderIndex is the document's place in the viewer's list, null when it was
	// added without one. Unordered documents sort last.
	OrderIndex *int64 `json:"orderIndex"`
	// Type is the document's kind, null when it was not recorded.
	Type *string `json:"type"`
	// UpdatedAt is when the document row last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// dataroomRoomDetail is one data room together with its contents.
type dataroomRoomDetail struct {
	// CreatedAt is when the room was created, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// Description is the room's description, null when none was given.
	Description *string `json:"description"`
	// Documents is every document in the room, in the order a visitor sees them.
	Documents []dataroomMember `json:"documents"`
	// ID is the room id.
	ID string `json:"id"`
	// Name is the room's display name.
	Name string `json:"name"`
	// PId is the room's short public identifier.
	PId string `json:"pId"`
	// UpdatedAt is when the room last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// dataroomRoomDetailOne is one data room with its documents.
type dataroomRoomDetailOne struct {
	// Dataroom is the room and its contents.
	Dataroom dataroomRoomDetail `json:"dataroom"`
}

// GetDataroom reads one of the caller org's data rooms together with every
// document in it, each carrying its membership id and order index.
//
// The documents are sorted by that index with unordered ones last and creation
// time breaking ties — the SAME order a link's visitor sees, so this is what the
// room looks like from the outside. A room id outside the caller's own tenant
// store is not found.
func (o ops) getDataroom(ctx context.Context, in *dataroomRef) (*dataroomRoomDetailOne, error) {
	var out dataroomRoomDetailOne
	if err := o.call(ctx, "datarooms.get", map[string]string{"id": in.ID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// dataroomAddDocument attaches an already-uploaded document to a data room.
type dataroomAddDocument struct {
	goja.SizedIn
	// DocumentId is the document to attach. Required, and it must already exist
	// in the caller's own store — this route attaches, it never uploads.
	DocumentId goja.Scalar `json:"documentId,omitempty"`
	// ID is the room to add to. It is the path segment: the URL is the addressing
	// authority, and the org it is resolved in comes from the caller's principal,
	// so an id from another tenant is simply not found.
	ID string `json:"id"`
	// OrderIndex fixes the document's place in the viewer's list. Optional; a
	// number or a numeric string is accepted, and anything that is not a number
	// leaves the document unordered, which sorts it last.
	OrderIndex goja.Scalar `json:"orderIndex,omitempty"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the data room. See goja.SizedIn.Fill.
func (in *dataroomAddDocument) UnmarshalJSON(b []byte) error {
	type body dataroomAddDocument // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = dataroomAddDocument(v)
	return nil
}

// dataroomMembership is the answer to attaching a document to a room.
type dataroomMembership struct {
	// DataroomDocumentId is the new membership id.
	DataroomDocumentId string `json:"dataroomDocumentId"`
	// DataroomId is the room the document was added to.
	DataroomId string `json:"dataroomId"`
	// DocumentId is the document that was added.
	DocumentId string `json:"documentId"`
}

// AddDataroomDocument puts an already-uploaded document into one of the caller
// org's data rooms and answers with the new membership id.
//
// It ATTACHES, it never uploads: the bytes must already be stored, so the usual
// order is upload the document, then add it to the room. Both the room and the
// document must exist in the caller's own store — either missing is not found —
// and a document already in the room is refused as a conflict rather than
// duplicated.
func (o ops) addDataroomDocument(ctx context.Context, in *dataroomAddDocument) (*dataroomMembership, error) {
	var out dataroomMembership
	err := o.write(ctx, "datarooms.addDocument", in.SizedIn, map[string]string{"id": in.ID}, map[string]goja.BodyField{
		"documentId": in.DocumentId,
		"orderIndex": in.OrderIndex,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- share links (granting access) ----

// dataroomLink is one public share link and the gates it enforces. The password
// is NEVER carried here — only whether one is set.
type dataroomLink struct {
	// AllowDownload is whether a visitor may download, rather than only view.
	AllowDownload bool `json:"allowDownload"`
	// AllowList narrows which addresses pass the email gate. An entry may be a
	// full address, an "@domain.com" suffix, or a bare "domain.com". An EMPTY
	// list admits everyone.
	AllowList []string `json:"allowList"`
	// CreatedAt is when the link was minted, in unix milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// DataroomId is the room the link opens, null for a single-document link.
	DataroomId *string `json:"dataroomId"`
	// DenyList rejects addresses, in the same three forms as the allow list, and
	// is checked BEFORE it — so deny always wins.
	DenyList []string `json:"denyList"`
	// DocumentId is the document the link opens, null for a room link.
	DocumentId *string `json:"documentId"`
	// EmailProtected is whether a visitor must state an address to enter.
	EmailProtected bool `json:"emailProtected"`
	// ExpiresAt is when the link closes, in unix milliseconds; null never expires.
	ExpiresAt *int64 `json:"expiresAt"`
	// HasPassword reports THAT a password is set. The stored form is a bcrypt
	// hash and no route returns it.
	HasPassword bool `json:"hasPassword"`
	// ID is the link id — the public token a visitor opens the room with.
	ID string `json:"id"`
	// IsArchived is whether the link has been retired.
	IsArchived bool `json:"isArchived"`
	// LinkType is DATAROOM_LINK or DOCUMENT_LINK.
	LinkType string `json:"linkType"`
	// Name is the link's label, null when none was given.
	Name *string `json:"name"`
	// UpdatedAt is when the link last changed, in unix milliseconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// dataroomLinks is the caller org's live share links.
type dataroomLinks struct {
	// Links is every non-archived link, newest first.
	Links []dataroomLink `json:"links"`
}

// ListDataroomLinks returns every live share link in the caller org's own store,
// newest first, with the controls a visitor will meet: whether an address is
// required, whether a password is set, the allow and deny lists, whether download
// is permitted, and when the link expires.
//
// Archived links are omitted entirely. A link reports only THAT a password is
// set — the stored form is a bcrypt hash and no route returns it.
func (o ops) listDataroomLinks(ctx context.Context, _ *noInput) (*dataroomLinks, error) {
	var out dataroomLinks
	if err := o.call(ctx, "links.list", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// dataroomLinkCreate is the share link to mint and the access controls it will
// enforce.
type dataroomLinkCreate struct {
	goja.SizedIn
	// AllowDownload permits downloading rather than viewing only. Optional and
	// OFF by default; any truthy JSON value turns it on.
	AllowDownload goja.Scalar `json:"allowDownload,omitempty"`
	// AllowList narrows which addresses pass the email gate. Optional; an entry
	// may be a full address ("ada@example.com"), an "@domain.com" suffix, or a
	// bare "domain.com". An omitted or EMPTY list admits everyone, so a link with
	// no list enforces the email gate alone.
	AllowList goja.ScalarList `json:"allowList,omitempty"`
	// DataroomId is the room to share. One of dataroomId or documentId is
	// required, and the target must exist in the caller's own store.
	DataroomId goja.Scalar `json:"dataroomId,omitempty"`
	// DenyList rejects addresses, in the same three forms as the allow list.
	// Optional. It is checked BEFORE the allow list, so deny always wins.
	DenyList goja.ScalarList `json:"denyList,omitempty"`
	// DocumentId shares a SINGLE document instead of a room. One of dataroomId or
	// documentId is required.
	DocumentId goja.Scalar `json:"documentId,omitempty"`
	// EmailProtected makes a visitor state an address before entering. Optional
	// and ON by default: it is disabled only by the JSON literal false, so any
	// other value — including the string "false" — leaves the gate on.
	EmailProtected goja.Scalar `json:"emailProtected,omitempty"`
	// ExpiresAt closes the link, in unix milliseconds. Optional; a number or a
	// numeric string is accepted, and omitting it means the link never expires.
	ExpiresAt goja.Scalar `json:"expiresAt,omitempty"`
	// Name labels the link. Optional; any JSON scalar is stored as its text.
	Name goja.Scalar `json:"name,omitempty"`
	// Password gates the link. Optional; it is hashed with bcrypt before storage
	// and is never readable back through any route.
	Password goja.Scalar `json:"password,omitempty"`
}

// UnmarshalJSON keeps the caller's tokens and records the body size; it refuses
// nothing, leaving every judgement to the data room. See goja.SizedIn.Fill.
func (in *dataroomLinkCreate) UnmarshalJSON(b []byte) error {
	type body dataroomLinkCreate // sheds the method, so this does not recurse
	var v body
	in.Fill(maxBody, b, &v)
	v.SizedIn = in.SizedIn
	*in = dataroomLinkCreate(v)
	return nil
}

// dataroomLinkOne is one share link.
type dataroomLinkOne struct {
	// Link is the link itself, including the id a visitor opens it with.
	Link dataroomLink `json:"link"`
}

// CreateDataroomLink grants access: it mints a public share link over one data
// room (`dataroomId`) or one document (`documentId`) — one of the two is
// required — and answers with the link, whose `id` is the token a visitor opens
// it with.
//
// This is how a party is let in. The controls are declared HERE and enforced on
// the viewer surface: `password` is hashed with bcrypt before storage and is
// never readable back, `emailProtected` (on by default) makes a visitor state an
// address, `allowList`/`denyList` narrow which addresses pass, `allowDownload`
// (off by default) governs downloads, and `expiresAt` closes the link. The target
// room or document must exist in the caller's own store or it is not found.
//
// Creating a link also writes dataroom's ONE cross-tenant row: the link id to
// owning org mapping an anonymous visitor is routed through. That write is part
// of the operation — if it fails the call is 500 — so a link that no visitor
// could open is never handed back as usable.
//
// The address a visitor later states is recorded UNVERIFIED, so a link gated only
// by email is openable by anyone the link reaches. Use a password for a link that
// must not travel.
func (o ops) createDataroomLink(ctx context.Context, in *dataroomLinkCreate) (*dataroomLinkOne, error) {
	var out dataroomLinkOne
	err := o.write(ctx, "links.create", in.SizedIn, nil, map[string]goja.BodyField{
		"allowDownload":  in.AllowDownload,
		"allowList":      in.AllowList,
		"dataroomId":     in.DataroomId,
		"denyList":       in.DenyList,
		"documentId":     in.DocumentId,
		"emailProtected": in.EmailProtected,
		"expiresAt":      in.ExpiresAt,
		"name":           in.Name,
		"password":       in.Password,
	}, &out)
	if err != nil {
		return nil, err
	}
	// The cross-tenant index is part of the operation, not a follow-up: an
	// anonymous visitor resolves the org from it, so a link absent from it opens
	// for nobody. Failing the call is what keeps an unusable link from being
	// handed back as usable — the same rule the untyped relay enforced.
	if out.Link.ID != "" {
		if err := o.s.State.index.put(out.Link.ID, orgOf(ctx)); err != nil {
			o.s.Log.Error("dataroom link index write failed", "link", out.Link.ID, "err", err)
			return nil, zip.Errorf(http.StatusInternalServerError, "link index write failed")
		}
	}
	return &out, nil
}

// orgOf re-reads the validated org for the link-index write. The op has already
// passed tenantOf by the time it is called — the dispatch could not have run
// otherwise — so the answer is the same org the bundle wrote under, and an empty
// string is unreachable rather than handled.
func orgOf(ctx context.Context) string {
	org, _ := principal.OrgFrom(ctx)
	return org
}

// ---- analytics ----

// dataroomPageStat is how one page of a document was read.
type dataroomPageStat struct {
	// AvgDuration is totalDuration divided by views, rounded; 0 when unviewed.
	AvgDuration int64 `json:"avgDuration"`
	// PageNumber is the page these counts are for.
	PageNumber int64 `json:"pageNumber"`
	// TotalDuration is the summed dwell measure reported for the page.
	TotalDuration int64 `json:"totalDuration"`
	// Views is how many times the page was viewed.
	Views int64 `json:"views"`
}

// dataroomLinkStats is how one share link was actually read.
type dataroomLinkStats struct {
	// LinkId is the link these counts are for.
	LinkId string `json:"linkId"`
	// Pages is the per-page breakdown, in page order.
	Pages []dataroomPageStat `json:"pages"`
	// TotalPageViews is how many page views the link received.
	TotalPageViews int64 `json:"totalPageViews"`
	// TotalViews is how many viewing sessions the link opened.
	TotalViews int64 `json:"totalViews"`
}

// linkRef addresses one of the caller org's share links.
type linkRef struct {
	// LinkID is the link to report on. It is the path segment, resolved in the
	// caller's own tenant store.
	LinkID string `json:"linkId"`
}

// GetLinkAnalytics reports how one share link was actually read: total viewing
// sessions, total page views, and per page the view count, the summed dwell
// measure and its average.
//
// The link is resolved in the caller's OWN tenant store, so another org's link id
// is not found — knowing a link id is enough to OPEN the room it shares, and
// never enough to read who has been reading it.
func (o ops) getLinkAnalytics(ctx context.Context, in *linkRef) (*dataroomLinkStats, error) {
	var out dataroomLinkStats
	if err := o.call(ctx, "analytics.link", map[string]string{"linkId": in.LinkID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// dataroomStats is how a data room was read across every link into it.
type dataroomStats struct {
	// DataroomId is the room these counts are for.
	DataroomId string `json:"dataroomId"`
	// Links is the same per-page breakdown for each link into the room.
	Links []dataroomLinkStats `json:"links"`
	// TotalPageViews is the room's page views across every link.
	TotalPageViews int64 `json:"totalPageViews"`
	// TotalViews is the room's viewing sessions across every link.
	TotalViews int64 `json:"totalViews"`
}

// roomStatsRef addresses one of the caller org's data rooms for analytics.
type roomStatsRef struct {
	// DataroomID is the room to report on. It is the path segment, resolved in
	// the caller's own tenant store.
	DataroomID string `json:"dataroomId"`
}

// GetDataroomAnalytics rolls up every share link pointing at one data room:
// session and page-view totals for the room, plus the per-page breakdown for each
// link beneath it.
//
// A room id outside the caller's own tenant store is not found. Only links that
// NAME the room are counted — a link created over a single document contributes
// nothing here, even when that document also sits in the room.
func (o ops) getDataroomAnalytics(ctx context.Context, in *roomStatsRef) (*dataroomStats, error) {
	var out dataroomStats
	if err := o.call(ctx, "analytics.dataroom", map[string]string{"dataroomId": in.DataroomID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
