// Package dataroom is a secure document room you share by link and watch page by
// page.
//
// It folds hanzoai/dataroom (a Papermark fork: Next.js + Prisma + Postgres,
// "open-source DocSend/dataroom") FULLY into the unified hanzoai/cloud binary
// as an in-process subsystem (HIP-0106, task #101 / epic #96). Cloud serves the
// dataroom surface (/v1/dataroom/*) ITSELF — no standalone dataroom pod, no
// Postgres, no Next.js.
//
// WRAP, DON'T REWRITE — the read-WRITE variant, on the SHARED binding. The dataroom
// business logic (documents, data rooms, shareable links with access controls,
// viewers, per-page view analytics) is a self-contained goja bundle (bundle.js, the
// ESM-free port of the Papermark API handlers). It runs in-process on the REUSABLE
// clients/goja host — the SAME RW-Base binding captable (#97) pilots and esign
// (#100) reuses — which injects __db/__newId/__now and one SQLite file per tenant,
// one transaction per request. This leaf adds only: the per-tenant Schema, the
// object-storage seam for document bytes, a bcrypt HostFn for link passwords, and
// the public link→org index. Zero domain logic lives in Go.
//
//	dataroom bundle (bundle.js, go:embed)  +  per-tenant Schema  +  __bcrypt HostFn
//	                    │
//	             clients/goja.NewBase(...)   ← __db/__newId/__now, per-tenant Base,
//	                    │                       one transaction per request
//	             /v1/dataroom/* zip routes
//
// STORAGE. Document BYTES never touch the bundle or local disk: the leaf stores
// them through the cloud object-storage seam (deps.VFS — the SeaweedFS/S3 data
// plane) keyed by an org-scoped opaque key (a Go storage host-fn over the s3/storage
// subsystem), and the bundle persists only that key via __db. View-analytics events
// (page-by-page tracking) are Base rows in the tenant DB.
//
// AUTH. Admin routes require a validated cloud principal (principal.Org → org);
// public viewer routes carry no principal and resolve their org from the link index
// (a link id → org routing table — the one cross-tenant piece). Tenant isolation is
// the per-org SQLite file NewBase selects from that org.
//
// ACTIVATION: dataroom is NOT staged — it mounts under the mount-all default
// (empty CLOUD_ENABLE), so the one binary serves /v1/dataroom/* from first boot.
// There is no standalone dataroom pod to defer to (the Papermark/Next.js/Postgres
// app is retired by this fold — no such deployment runs in the fleet), so cloud's
// fresh per-tenant Base/SQLite is authoritative from the first write, with no data
// to migrate.
package dataroom

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

//go:embed bundle.js
var bundleJS []byte

// maxBody caps a JSON request body (document BYTES use the separate upload path).
const maxBody = 1 << 20 // 1 MiB

// maxUpload caps a document upload. Datarooms hold decks/PDFs, not media libraries.
const maxUpload = 64 << 20 // 64 MiB

// blobStore is the object-storage seam the leaf stores/reads document bytes on.
// deps.VFS (the S3/SeaweedFS data plane) satisfies it — not local FS.
type blobStore interface {
	Put(ctx context.Context, key string, payload []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// state is dataroom's own data; shared deps live in the embedded cloud.Base,
// reached as s.Log.
type state struct {
	host  *goja.BaseHost
	index *linkIndex
	blob  blobStore
}

// mounted is the active service so Shutdown can release the per-tenant stores.
var mounted *cloud.Service[state]

// Mount wires the /v1/dataroom/* surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("dataroom.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("dataroom.Mount: nil deps.Logger")
	}
	// A local child logger for the fallible pre-construction setup (the health-only
	// degrade paths return before the Service value exists). NewBase derives the
	// same "subsystem"=dataroom child for the mounted service below.
	log := deps.Logger.New("subsystem", "dataroom")
	if deps.DataDir == "" {
		return fmt.Errorf("dataroom.Mount: empty DataDir")
	}

	// Native /v1/dataroom/health — always answers (HealthOwner), no auth, BEFORE
	// any fallible setup, so liveness never depends on the bundle/index/storage.
	app.Get("/v1/dataroom/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"service": "dataroom", "status": "ok"})
	})

	host, err := goja.NewBase(goja.BaseConfig{
		Name:    "dataroom",
		Bundle:  bundleJS,
		Schema:  schema,
		DataDir: deps.DataDir,
		HostFns: bcryptHostFns(), // __bcrypt.hash/verify — link passwords hashed in Go
	})
	if err != nil {
		log.Error("dataroom bundle failed to load — serving health-only (cloud stays up)", "err", err)
		return nil
	}
	index, err := openLinkIndex(deps.DataDir)
	if err != nil {
		log.Error("dataroom link index failed — serving health-only (cloud stays up)", "err", err)
		return nil
	}
	if deps.VFS == nil {
		log.Error("deps.VFS is nil — document byte storage unavailable; serving health-only")
		return nil
	}

	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "dataroom"), State: state{host: host, index: index, blob: deps.VFS}}
	mounted = s
	routes(app, s)

	log.Info("dataroom mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/dataroom", "brand", deps.Brand, "env", deps.Env)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes wires the /v1/dataroom/* route table → bundle route names, in TWO planes
// over one dispatch.
//
// TEN routes are TYPED ops, so they carry In/Out types and reach the document, the
// MCP tool list, the CLI and the generated SDKs (typed.go, which holds the models
// and the prose): every JSON route on the admin surface. Each relays the bundle's
// own refusal bytes through goja.BundleErr, so what a client sees is unchanged.
//
// SEVEN stay untyped relays, and each has a reason in the WIRE. The upload takes
// the file itself as the raw body and the two /file routes answer with a byte
// stream, which no In/Out pair describes. The three /view/* viewer routes carry no
// principal — their tenant comes from the public link index — so they have no
// validated org a typed op could read, and they are the surface a visitor's
// browser drives rather than one an agent calls. Health is native and answers
// before any of this exists.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/dataroom")
	// Bridge FIRST: a typed op receives only a context, so the validated org
	// reaches it by being parked there — never as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. fiber runs middleware in registration order, so this must precede
	// the leaves below.
	g.Use(cloud.Bridge())
	// Then the bundle's own envelope: a typed op that must answer the bundle's
	// {"error": …} returns a goja.BundleErr, and this writes those bytes back
	// verbatim. Also before the leaves, for the same registration-order reason.
	g.Use(goja.Envelope())

	// --- admin surface, typed (validated principal → org) --------------------
	//
	// Declared on the GROUP, so each op's path is the prefix composed with its
	// leaf — the same composition the router does, and the identity every
	// projection keys on. cmd/zipdoc resolves the prefix the same way, so the doc
	// comments reach the document and the MCP tool list.
	o := ops{s: s}
	zip.Get(g, "/documents", o.listDocuments)
	zip.Get(g, "/documents/:id", o.getDocument)
	zip.Get(g, "/datarooms", o.listDatarooms)
	zip.Post(g, "/datarooms", o.createDataroom)
	zip.Get(g, "/datarooms/:id", o.getDataroom)
	zip.Post(g, "/datarooms/:id/documents", o.addDataroomDocument)
	zip.Get(g, "/links", o.listDataroomLinks)
	zip.Post(g, "/links", o.createDataroomLink)
	zip.Get(g, "/analytics/link/:linkId", o.getLinkAnalytics)
	zip.Get(g, "/analytics/dataroom/:dataroomId", o.getDataroomAnalytics)

	// --- admin surface, untyped: the bytes ------------------------------------
	// The file IS the body on the way in and a stream on the way out; there is no
	// In/Out pair for that, and inventing a base64 envelope would change the wire.
	g.Post("/documents", cloud.Handle(s, uploadDocument))
	g.Get("/documents/:id/file", cloud.Handle(s, adminDownload))

	// --- viewer surface (public; org resolved from the link index) -----------
	// No principal reaches these: the visitor is whoever holds the link id, and
	// the org is resolved from the link index, so tenantOf has nothing to read.
	g.Get("/view/:linkId", viewer(s, "view.link", false))
	g.Post("/view/:linkId/authenticate", viewer(s, "view.authenticate", true))
	g.Post("/view/:linkId/pageview", viewer(s, "view.recordPage", true))
	g.Get("/view/:linkId/document/:documentId/file", cloud.Handle(s, viewerDownload))
}

// The prose for the routes that are NOT typed ops — the upload, the two file
// streams, and the four public viewer routes. A typed op's prose is lifted from
// its handler's doc comment by zipdoc instead (typed.go, zipdoc_gen.go), so an op
// described here as well would be the same fact written in two places, and a
// second source can only be stale or accidentally correct; openapi.Describe
// panics on a duplicate, which is that rule enforced rather than remembered.
//
// Declared through the same registry the projector reads, keyed by the fiber
// pattern verbatim, so prose renders only while the router actually serves the
// route and every consumer of the document — the generated SDKs, the spec-derived
// CLI — carries it. Without it these routes publish an operationId and nothing
// else, which is all an untyped route can publish: it reaches no MCP tool at all.
func init() {
	openapi.Describe("/v1/dataroom/health", http.MethodGet,
		"Liveness of the dataroom subsystem",
		"Answers {service, status} unconditionally — no principal, no tenant. It is registered "+
			"BEFORE the bundle, the link index and the object-storage seam are wired, so it keeps "+
			"answering when any of those fail and the subsystem degrades to health-only. That is "+
			"the point, and the limit: a 200 here says the process is alive, never that a data "+
			"room can be read or written.")

	// --- admin surface (validated principal → org) ---------------------------
	openapi.Describe("/v1/dataroom/documents", http.MethodPost,
		"Upload a document's bytes and record it",
		"Takes the file ITSELF as the raw request body — not a JSON envelope, not multipart — "+
			"stores it on the object-storage seam, and records the metadata row, answering with "+
			"the new document. `?name=` names it (default \"document\"), the request's "+
			"Content-Type becomes the recorded mime type, and `?numPages=` is optional.\n\n"+
			"Requires a validated principal; 403 without one. An empty body is 400 and anything "+
			"over 64 MiB is 413 — a data room holds decks and PDFs, not a media library.\n\n"+
			"The storage key is 128 random bits under the tenant's own key prefix, minted before "+
			"the bytes are written: if the system's randomness is unavailable the upload fails 500 "+
			"rather than fall back to a predictable key that could overwrite another document's "+
			"bytes. A storage write that fails is 502 and no metadata row is recorded, so a "+
			"document never exists without its file.")

	openapi.Describe("/v1/dataroom/documents/:id/file", http.MethodGet,
		"Download a document's bytes as its owner",
		"Streams the stored file back under its recorded content type, falling back to "+
			"application/octet-stream when none was recorded.\n\n"+
			"Requires a validated principal; 403 without one, and the document is resolved in the "+
			"caller's own tenant store, so another org's id is a 404. This is the OWNER's path and "+
			"applies no link gate at all — the per-link password, email and download controls live "+
			"on the viewer surface, not here. Bytes that cannot be fetched from object storage are "+
			"502, never a truncated or empty file.")

	openapi.Describe("/v1/dataroom/view/:linkId", http.MethodGet,
		"What a share link's visitor sees before authenticating",
		"Answers the pre-auth face of a link to anyone holding its id: name and type, which gates "+
			"apply (whether an address is required, whether a password is set), whether download is "+
			"permitted, whether it has expired, and the name and description of the room behind it "+
			"— or, for a single-document link, that document's name and page count.\n\n"+
			"No principal is involved: the owning org is resolved from the link id through "+
			"dataroom's one cross-tenant routing table, and an unknown or archived link is a 404.\n\n"+
			"It is metadata only — a room's document list and every file stay behind the "+
			"authenticate step. An expired link is REPORTED as expired here rather than refused, "+
			"so a visitor learns why the next step will fail; nothing about the password beyond "+
			"its existence is disclosed.")

	openapi.Describe("/v1/dataroom/view/:linkId/authenticate", http.MethodPost,
		"Pass a share link's gates and open a viewing session",
		"Clears the link's access controls and answers with the viewing session — a `viewId`, "+
			"whether download is permitted, and the documents behind the link — which every later "+
			"viewer call is authorised by.\n\n"+
			"No principal: the visitor is whoever holds the link id, and the org is resolved from "+
			"it. The gates run in a fixed order and each is a flat refusal, never a hint. An "+
			"archived or unknown link is 404 and an expired one 403. A missing address on an "+
			"email-protected link is 401. An address on the deny list is 403, checked BEFORE the "+
			"allow list so deny always wins. An address the allow list does not admit is 403 — an "+
			"EMPTY allow list admits everyone, so a link with no list enforces the email gate "+
			"alone. A wrong or absent password is 401, decided against the stored bcrypt hash.\n\n"+
			"The address is taken as stated and recorded UNVERIFIED: it names a viewer for "+
			"analytics and repeat visits from it reuse one viewer record, but it proves nothing "+
			"about who is on the other end. A link gated only by email is openable by anyone the "+
			"link reaches.")

	openapi.Describe("/v1/dataroom/view/:linkId/pageview", http.MethodPost,
		"Record one page-view against an open viewing session",
		"Appends a single per-page analytics event — {viewId, pageNumber, documentId, "+
			"versionNumber, duration} — and answers with its id. These events are what the owner's "+
			"analytics count.\n\n"+
			"No principal: the `viewId` from the authenticate step IS the authorisation, and it "+
			"must belong to THIS link or the call is 404, so a session opened on one link cannot "+
			"write events onto another. `pageNumber` is required (400 without it); `documentId` "+
			"falls back to the document the session was opened on, and `duration` is the caller's "+
			"own dwell measure, summed per page by analytics.\n\n"+
			"Events are additive: the same page reported twice is two views, which is the metric's "+
			"whole point.")

	openapi.Describe("/v1/dataroom/view/:linkId/document/:documentId/file", http.MethodGet,
		"Read a document's bytes as an authorised link visitor",
		"Streams a document's bytes under its recorded content type to a visitor holding an open "+
			"viewing session.\n\n"+
			"No principal: `?viewId=` from the authenticate step is the authorisation and must "+
			"belong to this link, or the call is 403 — holding the link id alone gets no bytes. "+
			"The document must be reachable THROUGH this link (a member of the room the link "+
			"opens, or the single document the link names), so a visitor cannot walk to an "+
			"unrelated document by guessing an id; anything else is a 404, as is an unknown or "+
			"archived link. Bytes that cannot be fetched from object storage are 502.\n\n"+
			"`?download=1` additionally requires the link's `allowDownload` and is 403 when the "+
			"owner did not permit it. Read that flag precisely: it gates the DOWNLOAD intent, not "+
			"access to the bytes — without the parameter an authorised visitor is served the file "+
			"for in-place viewing whether or not downloads are allowed.")
}

// === viewer dispatch (public; org via the link index) ========================

func viewer(s *cloud.Service[state], route string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		linkID := c.Param("linkId")
		org, ok, err := s.State.index.org(linkID)
		if err != nil {
			s.Log.Error("dataroom link index read failed", "err", err)
			return zip.Errorf(http.StatusInternalServerError, "link resolution failed")
		}
		if !ok {
			return zip.ErrNotFound("link not found")
		}
		body, err := decodeBody(c, readBody)
		if err != nil {
			return err
		}
		return write(s, c, org, route, map[string]string{"linkId": linkID}, nil, body)
	}
}

// === object-storage seam (document bytes) ====================================

// uploadDocument stores the request body (the file bytes) on the object-storage
// seam, then records the metadata row via the bundle. The file is the raw request
// body; ?name= names it, Content-Type carries the mime type, ?numPages= is optional.
func uploadDocument(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	raw := c.Fiber().Body()
	if len(raw) == 0 {
		return zip.ErrBadRequest("empty body: send the file bytes as the request body")
	}
	if len(raw) > maxUpload {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "document too large")
	}
	name := c.Query("name")
	if name == "" {
		name = "document"
	}
	ct := c.Header("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	// ONE tenant encoding everywhere: the object-store key prefix uses the SAME
	// injective, path-safe goja.TenantSegment the per-tenant SQLite filename
	// does — never the raw org (which could carry a '/' and traverse the key
	// namespace, and would drift from the DB's encoding).
	rk, err := randKey()
	if err != nil {
		s.Log.Error("dataroom: crypto/rand unavailable", "err", err)
		return zip.Errorf(http.StatusInternalServerError, "storage key generation failed")
	}
	key := "dataroom/" + goja.TenantSegment(org) + "/" + rk
	if err := s.State.blob.Put(c.Context(), key, raw); err != nil {
		s.Log.Error("dataroom storage put failed", "err", err)
		return zip.Errorf(http.StatusBadGateway, "document storage unavailable")
	}
	body := map[string]any{"name": name, "fileKey": key, "contentType": ct, "fileSize": len(raw)}
	if np := c.Query("numPages"); np != "" {
		body["numPages"] = np
	}
	return write(s, c, org, "documents.create", nil, nil, body)
}

// adminDownload streams a document's bytes to an authenticated owner.
func adminDownload(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	resp, err := s.State.host.Dispatch(c.Context(), org, goja.BaseRequest{
		Route: "documents.file", Params: map[string]string{"id": c.Param("id")},
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	return streamFile(s, c, resp)
}

// viewerDownload streams a document's bytes to an authorised viewer.
func viewerDownload(s *cloud.Service[state], c *zip.Ctx) error {
	linkID := c.Param("linkId")
	org, ok, err := s.State.index.org(linkID)
	if err != nil || !ok {
		return zip.ErrNotFound("link not found")
	}
	resp, err := s.State.host.Dispatch(c.Context(), org, goja.BaseRequest{
		Route:  "view.file",
		Params: map[string]string{"linkId": linkID, "documentId": c.Param("documentId")},
		Query:  map[string]string{"viewId": c.Query("viewId"), "download": c.Query("download")},
	})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	return streamFile(s, c, resp)
}

// streamFile turns a {fileKey,contentType,name} bundle result into a byte stream
// from object storage. A non-200 bundle result (404/403) passes through as JSON.
func streamFile(s *cloud.Service[state], c *zip.Ctx, resp *goja.Response) error {
	if resp.Status != http.StatusOK {
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(resp.Status, resp.Body)
	}
	var f struct {
		FileKey     string `json:"fileKey"`
		ContentType string `json:"contentType"`
		Name        string `json:"name"`
	}
	if err := json.Unmarshal(resp.Body, &f); err != nil || f.FileKey == "" {
		return zip.Errorf(http.StatusInternalServerError, "malformed file reference")
	}
	data, err := s.State.blob.Get(c.Context(), f.FileKey)
	if err != nil {
		s.Log.Error("dataroom storage get failed", "key", f.FileKey, "err", err)
		return zip.Errorf(http.StatusBadGateway, "document storage unavailable")
	}
	ct := f.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	c.SetHeader("Content-Type", ct)
	return c.Bytes(http.StatusOK, data)
}

// write dispatches one bundle route on the tenant's Base store (one transaction
// per request) and writes {status, body}.
func write(s *cloud.Service[state], c *zip.Ctx, org, route string, params, query map[string]string, body any) error {
	resp, err := s.State.host.Dispatch(c.Context(), org, goja.BaseRequest{
		Route: route, Params: params, Query: query, Body: body,
	})
	if err != nil {
		s.Log.Error("dataroom dispatch failed", "route", route, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.Status, resp.Body)
}

// decodeBody decodes a JSON request body when readBody is set (bounded by maxBody).
func decodeBody(c *zip.Ctx, readBody bool) (any, error) {
	if !readBody {
		return nil, nil
	}
	raw := c.Fiber().Body()
	if len(raw) > maxBody {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var body any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, zip.ErrBadRequest("invalid JSON body")
	}
	return body, nil
}

// === injected host functions =================================================

// bcryptHostFns injects globalThis.__bcrypt so the bundle hashes/verifies link
// passwords in vetted Go — HASHED, never stored or compared as plaintext.
func bcryptHostFns() map[string]any {
	return map[string]any{
		"__bcrypt": map[string]any{
			"hash": func(pw string) string {
				h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
				if err != nil {
					return ""
				}
				return string(h)
			},
			"verify": func(pw, hash string) bool {
				return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
			},
		},
	}
}

// randKey returns 128 bits of crypto/rand as hex — the opaque object-store key
// suffix. A crypto/rand failure is unrecoverable and MUST fail the upload rather
// than degrade to a zero/predictable key (which could overwrite another
// document's bytes); the caller turns the error into a 500.
func randKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("dataroom: crypto/rand unavailable: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// shutdown closes the per-tenant stores + the goja engine + the link index.
func Shutdown(context.Context) error {
	if mounted == nil {
		return nil
	}
	var firstErr error
	if mounted.State.host != nil {
		if err := mounted.State.host.Close(); err != nil {
			firstErr = err
		}
	}
	if mounted.State.index != nil {
		if err := mounted.State.index.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	mounted = nil
	return firstErr
}
