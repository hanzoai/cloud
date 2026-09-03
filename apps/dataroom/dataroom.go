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
// viewers, per-page view analytics) is a self-contained goja bundle — the ESM-free
// port of the Papermark API handlers, taken as a PINNED module
// (github.com/hanzoai/dataroom, checksummed in go.sum) rather than copied in, the
// same way the sign and captable folds take theirs. It runs in-process on the REUSABLE
// clients/goja host — the SAME RW-Base binding captable (#97) pilots and esign
// (#100) reuses — which injects __db/__newId/__now and one SQLite file per tenant,
// one transaction per request. This leaf adds only: the per-tenant Schema, the
// object-storage client for document bytes, a bcrypt HostFn for link passwords, and
// the public link→org index. Zero domain logic lives in Go.
//
//	dataroom bundle (pinned module)  +  per-tenant Schema  +  __bcrypt HostFn
//	                    │
//	             clients/goja.NewBase(...)   ← __db/__newId/__now, per-tenant Base,
//	                    │                       one transaction per request
//	             /v1/dataroom/* zip routes
//
// STORAGE. Document BYTES never touch the bundle or local disk: the leaf stores
// them through the cloud object-storage client (deps.VFS — the S3 data
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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"

	luxlog "github.com/luxfi/log"

	"golang.org/x/crypto/bcrypt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/magic"
	"github.com/hanzoai/cloud/openapi"
	dataroombundle "github.com/hanzoai/dataroom"
	"github.com/zap-proto/zip"
)

// maxBody caps a JSON request body (document BYTES use the separate upload path).
const maxBody = 1 << 20 // 1 MiB

// maxUpload caps a document upload. Datarooms hold decks/PDFs, not media libraries.
const maxUpload = 64 << 20 // 64 MiB

// blobStore is the object-storage client the leaf stores/reads document bytes on.
// deps.VFS (the S3 data plane) satisfies it — not local FS.
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
	// domain is the deployment's own public API host, which the trust centre puts
	// in the mail that carries a grant. A grant's address has to be absolute — it
	// is read in somebody else's mail client, not in a page of ours.
	domain string
}

// mounted is the active service so Shutdown can release the per-tenant stores.
var mounted *cloud.Service[state]

// Use mounts the data room, resolving the object store the deployment gives it.
//
// The store used to arrive as a field on Deps, resolved once for the whole fleet
// whether or not a given subsystem stored a byte. It is built here instead,
// because building it is free and because a subsystem that is handed another
// subsystem's handle is a subsystem that cannot be read on its own.
//
// The store is the ONLY thing separating this from useWith, which is what the
// tests drive: the resolution is the deployment's, the behaviour is the app's,
// and neither has to pretend to be the other.
func Use(app cloud.Router, deps cloud.Deps) error {
	return useWith(app, deps, cloud.S3(luxlog.Default()))
}

// Mount wires the /v1/dataroom/* surface onto app per HIP-0106.
func useWith(app cloud.Router, deps cloud.Deps, s3 cloud.VFSClient) error {
	if app == nil {
		return fmt.Errorf("dataroom.Use:  nil app")
	}
	// A local child logger for the fallible pre-construction setup (the health-only
	// degrade paths return before the Service value exists). NewBase derives the
	// same "subsystem"=dataroom child for the mounted service below.
	log := luxlog.Default().New("subsystem", "dataroom")
	if deps.DataDir == "" {
		return fmt.Errorf("dataroom.Use:  empty DataDir")
	}

	// Native /v1/dataroom/health — always answers (HealthOwner), no auth, BEFORE any
	// fallible setup, so liveness never depends on the bundle, the index or storage.
	//
	// A TYPED op, declared ABSOLUTELY on the app rather than on the group the rest
	// of the surface hangs off. That is what "before any fallible setup" means in
	// code: the group is built after the bundle loads, and a bundle that fails to
	// load returns early — so a probe registered there would not exist in exactly
	// the case an operator is probing for. It was raw only because of WHERE it is
	// registered, never because of its wire, which is why its own ledger carried it
	// as a DEBT rather than a refusal.
	//
	// The registry is checked for nil, which every other app that reaches for it
	// does: ZipApp returns nil for a Router that is neither a *zip.App nor a scope
	// (scope.go), and zip.Get on a nil registry PANICS at mount rather than failing
	// it. A subsystem that cannot register should refuse and say so — the difference
	// between a degraded plugin and a crashing one.
	reg := cloud.ZipApp(app)
	if reg == nil {
		return fmt.Errorf("dataroom.Use:  router carries no typed-op registry")
	}
	zip.Get(reg, "/v1/dataroom/health", probeOps{}.health)

	bundle, err := dataroombundle.Bundle()
	if err != nil {
		log.Error("dataroom bundle failed to load — serving health-only (cloud stays up)", "err", err)
		return nil
	}
	host, err := goja.NewBase(goja.BaseConfig{
		Name:    "dataroom",
		Bundle:  bundle,
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
	if s3 == nil {
		log.Error("no object store — document byte storage unavailable; serving health-only")
		return nil
	}

	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "dataroom"), State: state{
		host: host, index: index, blob: s3, domain: "https://" + deps.Domain,
	}}
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
	// Then what every answer here says about being kept. See held.
	g.Use(held())

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

	// --- trust centre ---------------------------------------------------------
	//
	// Three audiences under one prefix, each with its own gate (trust_typed.go).
	// `center` is a LITERAL segment beside `artifacts` and `requests`, so the
	// public address `/trust/center/:slug` can never be confused with a managed
	// route — without it a centre published as "requests" would shadow the queue.
	//
	// The two SuperAdmin-only and the six org-scoped ops each ask their own gate,
	// because a typed op is also an MCP tool and an internal-plane op and both
	// invoke it with no route to hang middleware on. `cloud.Gate(cloud.Super)` on the platform
	// group is the routed endpoint's first refusal, so a non-SuperAdmin sending an
	// unparseable body is told about authority rather than about JSON.
	zip.Get(g, "/trust", o.readDesk)
	zip.Put(g, "/trust", o.setCenter)
	zip.Post(g, "/trust/artifacts", o.publish)
	zip.Patch(g, "/trust/artifacts/:id", o.amend)
	zip.Post(g, "/trust/requests/:id/grant", o.grant)
	zip.Post(g, "/trust/requests/:id/refuse", o.refuse)
	zip.Get(g, "/trust/center/:slug", o.readCenter)
	zip.Post(g, "/trust/center/:slug/requests", o.askCenter)

	// The one cross-tenant read, on its own gated group at the operator's depth.
	// It carries its OWN Bridge: a group is a separate app and Use is scoped to the
	// app it was called on, so the one installed above reaches nothing here — and
	// without it the op would find no request, read no attested SuperAdmin, and
	// refuse the very caller it is for.
	platform := app.Group("/v1/admin/dataroom", cloud.Gate(cloud.Super))
	platform.Use(cloud.Bridge())
	platform.Use(held())
	zip.Get(platform, "/trust", o.roster)

	// A public item's bytes. Untyped for the same reason the two admin file routes
	// are: the answer is a byte stream under the document's own content type, and
	// no In/Out pair describes one.
	g.Get("/trust/center/:slug/file/:item", cloud.Handle(s, trustFile))

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
			"BEFORE the bundle, the link index and the object-storage client are wired, so it keeps "+
			"answering when any of those fail and the subsystem degrades to health-only. That is "+
			"the point, and the limit: a 200 here says the process is alive, never that a data "+
			"room can be read or written.")

	// --- admin surface (validated principal → org) ---------------------------
	openapi.Describe("/v1/dataroom/documents", http.MethodPost,
		"Upload a document's bytes and record it",
		"Takes the file ITSELF as the raw request body — not a JSON envelope, not multipart — "+
			"stores it on the object-storage client, and records the metadata row, answering with "+
			"the new document. `?name=` names it (default \"document\"), the request's "+
			"Content-Type is recorded as the document's mime type, and `?numPages=` is optional. "+
			"That recorded type is metadata the owner sees; what the file is later SERVED as is "+
			"read from the bytes.\n\n"+
			"Requires a validated principal; 403 without one. An empty body is 400 and anything "+
			"over 64 MiB is 413 — a data room holds decks and PDFs, not a media library.\n\n"+
			"The storage key is 128 random bits under the tenant's own key prefix, minted before "+
			"the bytes are written: if the system's randomness is unavailable the upload fails 500 "+
			"rather than fall back to a predictable key that could overwrite another document's "+
			"bytes. A storage write that fails is 502 and no metadata row is recorded, so a "+
			"document never exists without its file.")

	openapi.Describe("/v1/dataroom/documents/:id/file", http.MethodGet,
		"Download a document's bytes as its owner",
		"Streams the stored file back under the type read from its BYTES — a raster image or a "+
			"PDF renders in place, and anything else is served as application/octet-stream with an "+
			"attachment disposition, so a stored file never executes as markup in this origin. "+
			"Every response carries nosniff, which keeps the declared type binding.\n\n"+
			"Requires a validated principal; 403 without one, and the document is resolved in the "+
			"caller's own tenant store, so another org's id is a 404. This is the OWNER's path and "+
			"applies no link gate at all — the per-link password, email and download controls live "+
			"on the viewer surface, not here. Bytes that cannot be fetched from object storage are "+
			"502, never a truncated or empty file.")

	openapi.Describe("/v1/dataroom/trust/center/:slug/file/:item", http.MethodGet,
		"Read a public trust-centre item's bytes",
		"Streams the file behind an item a trust centre publishes openly — a policy, a filled "+
			"questionnaire, a knowledge-base attachment — under the type read from its bytes: a "+
			"picture or a PDF renders in place, anything else downloads inert.\n\n"+
			"No principal and no link: these are the things an org states about itself, so they are "+
			"served to anyone who asks. The narrowing is in the lookup rather than in a check: the "+
			"item must be public, must not be retired, and must belong to a centre its owner has "+
			"published, so an item released only on request is NOT FOUND here rather than refused — "+
			"the same answer an id that never existed gets, which is what stops this reporting what "+
			"the released-on-request tier holds.\n\n"+
			"Bytes that cannot be fetched from object storage are 502, never a truncated file.")

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
		"Streams a document's bytes to a visitor holding an open viewing session, under the type "+
			"read from those bytes: a picture or a PDF renders in place, anything else downloads "+
			"inert.\n\n"+
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

// === object-storage client (document bytes) ====================================

// uploadDocument stores the request body (the file bytes) on the object-storage
// client, then records the metadata row via the bundle. The file is the raw request
// body; ?name= names it, Content-Type carries the mime type, ?numPages= is optional.
func uploadDocument(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return principal.Refused(c)
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
		return principal.Refused(c)
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

// streamFile turns a {fileKey,name} bundle result into a byte stream from object
// storage. A non-200 bundle result (404/403) passes through as JSON.
//
// The served type is the one magic reads out of the STORED BYTES, never the
// contentType recorded at upload — that is the uploader's own word, and this origin
// is api.hanzo.ai, where a response served as markup runs beside every console and
// reads whatever that console holds. So a document renders in place only when its
// bytes say picture or PDF; everything else leaves inert, as application/octet-stream
// under an attachment disposition. nosniff keeps the declared type binding, so a PDF
// that is also valid markup is still only a PDF.
func streamFile(s *cloud.Service[state], c *zip.Ctx, resp *goja.Response) error {
	if resp.Status != http.StatusOK {
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(resp.Status, resp.Body)
	}
	var f struct {
		FileKey string `json:"fileKey"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(resp.Body, &f); err != nil || f.FileKey == "" {
		return zip.Errorf(http.StatusInternalServerError, "malformed file reference")
	}
	data, err := s.State.blob.Get(c.Context(), f.FileKey)
	if err != nil {
		s.Log.Error("dataroom storage get failed", "key", f.FileKey, "err", err)
		return zip.Errorf(http.StatusBadGateway, "document storage unavailable")
	}
	c.SetHeader("X-Content-Type-Options", "nosniff")
	if kind := magic.Type(data); kind != "" {
		c.SetHeader("Content-Type", kind)
	} else {
		c.SetHeader("Content-Type", "application/octet-stream")
		c.SetHeader("Content-Disposition", disposition(f.Name))
	}
	return c.Bytes(http.StatusOK, data)
}

// disposition names the download without letting the name reach the wire raw:
// mime.FormatMediaType quotes and percent-encodes, so a name carrying a line break
// or a quote becomes a parameter value rather than a second header. Only the last
// path segment is offered, and a name that survives neither is simply omitted —
// a bare attachment is complete on its own.
func disposition(name string) string {
	base := path.Base(strings.TrimSpace(name))
	if base == "." || base == "/" {
		return "attachment"
	}
	if d := mime.FormatMediaType("attachment", map[string]string{"filename": base}); d != "" {
		return d
	}
	return "attachment"
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

// held says this answer is one reader's and is not to be kept. Every answer this
// subsystem writes carries it, because every one of them is a tenant's own
// material and all of them are authorised by state that MOVES: a link is revoked,
// its password changed, a viewing session closed, a trust item retired, a centre
// withdrawn. A copy held by a shared cache would go on answering under the
// permission that has already been taken back, and the viewer routes are reached
// with no principal at all — a link id is the whole of the caller's identity, so
// the URL alone is enough for a cache to key on and hand to the next visitor.
//
// ON THE GROUP, which is what makes "every answer" true. A typed op returns its
// Out and touches no response at all — the twenty typed ops answer through
// ops.call and ops.run (typed.go) — so a header written where the byte stream and
// the untyped relay write theirs reached those two paths and nothing else,
// leaving /documents, /datarooms, /trust and /links free for anyone to store.
// /links is the one that matters most: it answers with the link ids, which are
// the whole of a visitor's credential on the viewer surface. Every route this
// subsystem serves hangs off one of its two groups, so one registration on each
// carries all of them.
//
// Before the leaf rather than after it, so a refusal is as unstorable as an
// answer: a 403 from the viewer surface is keyed by the same link id and shaped
// by the same tenant as the bytes it withholds.
//
// Vary names the credentials the SAME address answers differently under: an owner
// reading /documents/:id/file and another org reading the identical URL get
// different bytes, so a cache that stores anyway must still not cross them.
//
// It is APPENDED and never assigned. Vary is a LIST, and the edge has already
// named Origin on it by the time a request arrives here: every CORS answer
// depends on Origin, including the one that carries no CORS header at all
// (middleware_edge.go). SetHeader overwrites, so assigning drops that and lets a
// shared cache hand one origin the answer computed for another. fiber's Vary
// appends and is idempotent, which is what that middleware uses for the same
// reason.
func held() zip.Handler {
	return func(c *zip.Ctx) error {
		c.SetHeader("Cache-Control", "private, no-store")
		c.Fiber().Vary("Authorization", "Cookie")
		return c.Continue()
	}
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

// probeOps carries the liveness probe. It holds NOTHING, which is the point: the
// probe must answer before the bundle, the index and the object store exist, so it
// can depend on none of them.
type probeOps struct{}

// dataroomLiveness is what the probe answers. It is a constant shape — the route
// reports that this process is up and serving, and deliberately reports nothing
// about the bundle or the store, because a probe that failed on a dependency would
// take the whole subsystem out of rotation over a data room nobody is reading.
type dataroomLiveness struct {
	// Service names the subsystem answering, so a probe response is attributable
	// when several are collected together.
	Service string `json:"service"`
	// Status is `ok`. This probe has no degraded answer by design: it reports
	// process liveness and nothing that could be false while the process serves.
	Status string `json:"status"`
}

// Health reports that the data room subsystem is up.
//
// It answers before the bundle loads, holds no state and touches no store, so it
// stays true in exactly the situation an operator is probing for. It says nothing
// about whether a room can be OPENED — that is what the room operations answer —
// because a liveness probe that fails on a dependency takes a working process out
// of rotation.
func (probeOps) health(context.Context, *noProbeInput) (*dataroomLiveness, error) {
	return &dataroomLiveness{Service: "dataroom", Status: "ok"}, nil
}

// noProbeInput is the In of an op that reads nothing off the wire.
type noProbeInput struct{}
