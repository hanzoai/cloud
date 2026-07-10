// Package dataroom folds hanzoai/dataroom (a Papermark fork: Next.js + Prisma +
// Postgres, "open-source DocSend/dataroom") FULLY into the unified hanzoai/cloud
// binary as an in-process subsystem (HIP-0106, task #101 / epic #96). Cloud serves
// the dataroom surface (/v1/dataroom/*) ITSELF — no standalone dataroom pod, no
// Postgres, no Next.js.
//
// WRAP, DON'T REWRITE — the read-WRITE variant, on the SHARED binding. The dataroom
// business logic (documents, data rooms, shareable links with access controls,
// viewers, per-page view analytics) is a self-contained goja bundle (bundle.js, the
// ESM-free port of the Papermark API handlers). It runs in-process on the REUSABLE
// clients/gojabase host — the SAME RW-Base binding captable (#97) pilots and esign
// (#100) reuses — which injects __db/__newId/__now and one SQLite file per tenant,
// one transaction per request. This leaf adds only: the per-tenant Schema, the
// object-storage seam for document bytes, a bcrypt HostFn for link passwords, and
// the public link→org index. Zero domain logic lives in Go.
//
//	dataroom bundle (bundle.js, go:embed)  +  per-tenant Schema  +  __bcrypt HostFn
//	                    │
//	             clients/gojabase.New(...)   ← __db/__newId/__now, per-tenant Base,
//	                    │                       one transaction per request
//	             /v1/dataroom/* zip routes
//
// STORAGE. Document BYTES never touch the bundle or local disk: the leaf stores
// them through the cloud object-storage seam (deps.VFS — the SeaweedFS/S3 data
// plane) keyed by an org-scoped opaque key (a Go storage host-fn over the s3/storage
// subsystem), and the bundle persists only that key via __db. View-analytics events
// (page-by-page tracking) are Base rows in the tenant DB.
//
// AUTH. Admin routes require a validated cloud principal (principal.Tenant → org);
// public viewer routes carry no principal and resolve their org from the link index
// (a link id → org routing table — the one cross-tenant piece). Tenant isolation is
// the per-org SQLite file gojabase selects from that org.
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
	"github.com/hanzoai/cloud/clients/gojabase"
	"github.com/hanzoai/cloud/clients/principal"
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
	host  *gojabase.Host
	index *linkIndex
	blob  blobStore
}

// mounted is the active service so Shutdown can release the per-tenant stores.
var mounted *hcloud.Service[state]

// Mount wires the /v1/dataroom/* surface onto app per HIP-0106.
func Mount(app *zip.App, deps hcloud.Deps) error {
	if app == nil {
		return fmt.Errorf("dataroom.Mount: nil zip.App")
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

	host, err := gojabase.New(gojabase.Config{
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
func routes(app *zip.App, s *hcloud.Service[state]) {
	// --- admin surface (validated principal → org) ---------------------------
	app.Get("/v1/dataroom/documents", admin(s, "documents.list", nil, false))
	app.Post("/v1/dataroom/documents", hcloud.Handle(s, uploadDocument))
	app.Get("/v1/dataroom/documents/:id", adminID(s, "documents.get", false))
	app.Get("/v1/dataroom/documents/:id/file", hcloud.Handle(s, adminDownload))
	app.Get("/v1/dataroom/datarooms", admin(s, "datarooms.list", nil, false))
	app.Post("/v1/dataroom/datarooms", admin(s, "datarooms.create", nil, true))
	app.Get("/v1/dataroom/datarooms/:id", adminID(s, "datarooms.get", false))
	app.Post("/v1/dataroom/datarooms/:id/documents", adminID(s, "datarooms.addDocument", true))
	app.Get("/v1/dataroom/links", admin(s, "links.list", nil, false))
	app.Post("/v1/dataroom/links", hcloud.Handle(s, createLink))
	app.Get("/v1/dataroom/analytics/link/:linkId", adminParam(s, "analytics.link", "linkId", false))
	app.Get("/v1/dataroom/analytics/dataroom/:dataroomId", adminParam(s, "analytics.dataroom", "dataroomId", false))

	// --- viewer surface (public; org resolved from the link index) -----------
	app.Get("/v1/dataroom/view/:linkId", viewer(s, "view.link", false))
	app.Post("/v1/dataroom/view/:linkId/authenticate", viewer(s, "view.authenticate", true))
	app.Post("/v1/dataroom/view/:linkId/pageview", viewer(s, "view.recordPage", true))
	app.Get("/v1/dataroom/view/:linkId/document/:documentId/file", hcloud.Handle(s, viewerDownload))
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
	org, ok := principal.Tenant(c)
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
	org, ok := principal.Tenant(c)
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
	// injective, path-safe gojabase.TenantSegment the per-tenant SQLite filename
	// does — never the raw org (which could carry a '/' and traverse the key
	// namespace, and would drift from the DB's encoding).
	rk, err := randKey()
	if err != nil {
		s.Log.Error("dataroom: crypto/rand unavailable", "err", err)
		return zip.Errorf(http.StatusInternalServerError, "storage key generation failed")
	}
	key := "dataroom/" + gojabase.TenantSegment(org) + "/" + rk
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
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	resp, err := s.State.host.Dispatch(c.Context(), org, gojabase.Request{
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
	resp, err := s.State.host.Dispatch(c.Context(), org, gojabase.Request{
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
func streamFile(s *hcloud.Service[state], c *zip.Ctx, resp *gojabase.Response) error {
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
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	body, err := decodeBody(c, true)
	if err != nil {
		return err
	}
	resp, err := s.State.host.Dispatch(c.Context(), org, gojabase.Request{Route: "links.create", Body: body})
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
	resp, err := s.State.host.Dispatch(c.Context(), org, gojabase.Request{
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

func init() {
	// Order 134 (after captable's 133, before the AI /v1/* catch-all at 150 and the
	// node-service tier). cloud.HealthOwner: dataroom serves its OWN
	// /v1/dataroom/health so the generic liveness route never shadows it. NOT
	// staged — mounts under the mount-all default.
	hcloud.RegisterWithShutdown("dataroom", 134, hcloud.Typed(Mount), shutdown, hcloud.HealthOwner)
}

// shutdown closes the per-tenant stores + the goja engine + the link index.
func shutdown(context.Context) error {
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
