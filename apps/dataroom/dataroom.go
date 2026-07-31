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

	hcloud "github.com/hanzoai/cloud"
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
var mounted *hcloud.Service[state]

// Mount wires the /v1/dataroom/* surface onto app per HIP-0106.
func Mount(app hcloud.Router, deps hcloud.Deps) error {
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

	s := &hcloud.Service[state]{Base: hcloud.NewBase(deps, "dataroom"), State: state{host: host, index: index, blob: deps.VFS}}
	mounted = s
	routes(app, s)

	log.Info("dataroom mounted in-process (goja + per-tenant Base)",
		"prefix", "/v1/dataroom", "brand", deps.Brand, "env", deps.Env)
	return nil
}

// routes wires the /v1/dataroom/* surface onto app.
func routes(app hcloud.Router, s *hcloud.Service[state]) {
	g := app.Group("/v1/dataroom")
	// --- admin surface (validated principal → org) ---------------------------
	g.Get("/documents", admin(s, "documents.list", nil, false))
	g.Post("/documents", hcloud.Handle(s, uploadDocument))
	g.Get("/documents/:id", adminID(s, "documents.get", false))
	g.Get("/documents/:id/file", hcloud.Handle(s, adminDownload))
	g.Get("/datarooms", admin(s, "datarooms.list", nil, false))
	g.Post("/datarooms", admin(s, "datarooms.create", nil, true))
	g.Get("/datarooms/:id", adminID(s, "datarooms.get", false))
	g.Post("/datarooms/:id/documents", adminID(s, "datarooms.addDocument", true))
	g.Get("/links", admin(s, "links.list", nil, false))
	g.Post("/links", hcloud.Handle(s, createLink))
	g.Get("/analytics/link/:linkId", adminParam(s, "analytics.link", "linkId", false))
	g.Get("/analytics/dataroom/:dataroomId", adminParam(s, "analytics.dataroom", "dataroomId", false))

	// --- viewer surface (public; org resolved from the link index) -----------
	g.Get("/view/:linkId", viewer(s, "view.link", false))
	g.Post("/view/:linkId/authenticate", viewer(s, "view.authenticate", true))
	g.Post("/view/:linkId/pageview", viewer(s, "view.recordPage", true))
	g.Get("/view/:linkId/document/:documentId/file", hcloud.Handle(s, viewerDownload))
}

// The document's prose for this surface. NONE of the routes above can be a typed
// op — a typed op's prose is lifted from its handler's doc comment by zipdoc, and
// every route here is an untyped relay: the domain logic is the goja bundle, so
// the answer is opaque bundle bytes (or, for the two file routes, a byte stream
// off the object-storage seam) that no Go In/Out pair describes. Declared through
// the same registry the projector reads, keyed by the fiber pattern verbatim, so
// prose renders only while the router actually serves the route and every consumer
// of the document — the generated SDKs, the MCP tool list, the spec-derived CLI —
// carries it. Without it the surface publishes an operationId and nothing else.
func init() {
	openapi.Describe("/v1/dataroom/health", http.MethodGet,
		"Liveness of the dataroom subsystem",
		"Answers {service, status} unconditionally — no principal, no tenant. It is registered "+
			"BEFORE the bundle, the link index and the object-storage seam are wired, so it keeps "+
			"answering when any of those fail and the subsystem degrades to health-only. That is "+
			"the point, and the limit: a 200 here says the process is alive, never that a data "+
			"room can be read or written.")

	// --- admin surface (validated principal → org) ---------------------------
	openapi.Describe("/v1/dataroom/documents", http.MethodGet,
		"List the org's documents, newest first",
		"Returns every document in the caller's own tenant store — name, opaque storage key, "+
			"content type, page count, size and timestamps — ordered newest first.\n\n"+
			"Requires a validated principal; 403 without one. Tenant isolation is the per-org "+
			"store itself: there is one SQLite file per org and the org is never a parameter, so "+
			"no input the caller controls can address another tenant's documents. Metadata only — "+
			"the bytes come from the file route.")

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

	openapi.Describe("/v1/dataroom/documents/:id", http.MethodGet,
		"Read one document's metadata",
		"Returns the document's name, opaque storage key, content type, page count, size and "+
			"timestamps.\n\n"+
			"Requires a validated principal; 403 without one. The lookup runs in the caller's own "+
			"tenant store, so an id belonging to another org is a 404 exactly like one that never "+
			"existed. Metadata only — the bytes are a separate read.")

	openapi.Describe("/v1/dataroom/documents/:id/file", http.MethodGet,
		"Download a document's bytes as its owner",
		"Streams the stored file back under its recorded content type, falling back to "+
			"application/octet-stream when none was recorded.\n\n"+
			"Requires a validated principal; 403 without one, and the document is resolved in the "+
			"caller's own tenant store, so another org's id is a 404. This is the OWNER's path and "+
			"applies no link gate at all — the per-link password, email and download controls live "+
			"on the viewer surface, not here. Bytes that cannot be fetched from object storage are "+
			"502, never a truncated or empty file.")

	openapi.Describe("/v1/dataroom/datarooms", http.MethodGet,
		"List the org's data rooms, newest first",
		"Returns every data room in the caller's own tenant store with its short public id, "+
			"name, description and timestamps, newest first.\n\n"+
			"Requires a validated principal; 403 without one. Documents are not included — a "+
			"room's contents come from the single-room read.")

	openapi.Describe("/v1/dataroom/datarooms", http.MethodPost,
		"Create a data room",
		"Creates an empty data room from {name, description} and answers with it, including the "+
			"short public id it is addressed by.\n\n"+
			"Requires a validated principal; 403 without one. `name` is required; without it the "+
			"call is 400 and the tenant store is untouched, because a dispatch answering 4xx rolls "+
			"its transaction back. A new room holds no documents and is reachable by nobody until "+
			"a share link is created over it.")

	openapi.Describe("/v1/dataroom/datarooms/:id", http.MethodGet,
		"Read one data room with its documents in display order",
		"Returns the room and every document attached to it, each carrying its membership id and "+
			"order index, sorted by that index with unordered documents last and creation time "+
			"breaking ties — the same order a link's visitor sees.\n\n"+
			"Requires a validated principal; 403 without one, and a room id outside the caller's "+
			"own tenant store is a 404.")

	openapi.Describe("/v1/dataroom/datarooms/:id/documents", http.MethodPost,
		"Attach an existing document to a data room",
		"Adds an already-uploaded document to the room by {documentId} and answers with the new "+
			"membership id. An optional `orderIndex` fixes its place in the viewer's list.\n\n"+
			"Requires a validated principal; 403 without one. Both the room and the document must "+
			"exist in the caller's own tenant store — either missing is a 404 — and a document "+
			"already in the room is a 409 rather than a duplicate row. It attaches, it never "+
			"uploads: the bytes must already be stored.")

	openapi.Describe("/v1/dataroom/links", http.MethodGet,
		"List the org's live share links and the gates they enforce",
		"Returns every non-archived link with the controls a visitor will meet: whether an "+
			"address is required, whether a password is set, the allow and deny lists, whether "+
			"download is permitted, and when the link expires.\n\n"+
			"Requires a validated principal; 403 without one. Archived links are omitted entirely. "+
			"A link reports only THAT a password is set — the stored form is a bcrypt hash and no "+
			"route returns it.")

	openapi.Describe("/v1/dataroom/links", http.MethodPost,
		"Create a share link with its access controls",
		"Mints a public link over one data room (`dataroomId`) or one document (`documentId`) — "+
			"one of the two is required — and answers with it. The controls are declared here and "+
			"enforced only on the viewer surface: `password` is hashed with bcrypt before storage "+
			"and is never readable back, `emailProtected` (on by default) makes a visitor state an "+
			"address, `allowList`/`denyList` narrow which addresses pass, `allowDownload` (off by "+
			"default) governs downloads, and `expiresAt` closes the link.\n\n"+
			"Requires a validated principal; 403 without one, and the target room or document must "+
			"exist in the caller's own tenant store or it is a 404.\n\n"+
			"Creating a link also writes dataroom's ONE cross-tenant row: the link id to owning org "+
			"mapping an anonymous visitor is routed through. That write is part of the operation — "+
			"if it fails the call is 500, so a link that no visitor could open is never handed "+
			"back as usable.")

	openapi.Describe("/v1/dataroom/analytics/link/:linkId", http.MethodGet,
		"Per-page view analytics for one share link",
		"Returns how the link was actually read: total viewing sessions, total page views, and "+
			"per page the view count, the summed dwell measure and its average.\n\n"+
			"Requires a validated principal; 403 without one. The link is resolved in the caller's "+
			"OWN tenant store, so another org's link id is a 404 — knowing a link id is enough to "+
			"open the room it shares, and never enough to read who has been reading it.")

	openapi.Describe("/v1/dataroom/analytics/dataroom/:dataroomId", http.MethodGet,
		"Per-page view analytics for a data room, across all its links",
		"Rolls up every link pointing at the room: session and page-view totals for the room, "+
			"plus the same per-page breakdown for each link beneath it.\n\n"+
			"Requires a validated principal; 403 without one, and a room id outside the caller's "+
			"own tenant store is a 404. Only links that NAME the room are counted — a link created "+
			"over a single document contributes nothing here, even when that document also sits in "+
			"the room.")

	// --- viewer surface (public; org resolved from the link index) -----------
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

// === admin dispatch (validated principal) ====================================

func admin(s *hcloud.Service[state], route string, params map[string]string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error { return adminDispatch(s, c, route, params, readBody) }
}
func adminID(s *hcloud.Service[state], route string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		return adminDispatch(s, c, route, map[string]string{"id": c.Param("id")}, readBody)
	}
}
func adminParam(s *hcloud.Service[state], route, param string, readBody bool) zip.Handler {
	return func(c *zip.Ctx) error {
		return adminDispatch(s, c, route, map[string]string{param: c.Param(param)}, readBody)
	}
}

func adminDispatch(s *hcloud.Service[state], c *zip.Ctx, route string, params map[string]string, readBody bool) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	body, err := decodeBody(c, readBody)
	if err != nil {
		return err
	}
	return write(s, c, org, route, params, nil, body)
}

// === viewer dispatch (public; org via the link index) ========================

func viewer(s *hcloud.Service[state], route string, readBody bool) zip.Handler {
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
func uploadDocument(s *hcloud.Service[state], c *zip.Ctx) error {
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
func adminDownload(s *hcloud.Service[state], c *zip.Ctx) error {
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
func viewerDownload(s *hcloud.Service[state], c *zip.Ctx) error {
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
func streamFile(s *hcloud.Service[state], c *zip.Ctx, resp *goja.Response) error {
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

// createLink dispatches links.create and, on success, records the new link id in
// the cross-tenant index so a public viewer can resolve it to this org.
func createLink(s *hcloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	body, err := decodeBody(c, true)
	if err != nil {
		return err
	}
	resp, err := s.State.host.Dispatch(c.Context(), org, goja.BaseRequest{Route: "links.create", Body: body})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "dataroom dispatch failed")
	}
	if resp.Status == http.StatusOK {
		var out struct {
			Link struct {
				ID string `json:"id"`
			} `json:"link"`
		}
		if json.Unmarshal(resp.Body, &out) == nil && out.Link.ID != "" {
			if err := s.State.index.put(out.Link.ID, org); err != nil {
				s.Log.Error("dataroom link index write failed", "link", out.Link.ID, "err", err)
				return zip.Errorf(http.StatusInternalServerError, "link index write failed")
			}
		}
	}
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.Status, resp.Body)
}

// write dispatches one bundle route on the tenant's Base store (one transaction
// per request) and writes {status, body}.
func write(s *hcloud.Service[state], c *zip.Ctx, org, route string, params, query map[string]string, body any) error {
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
