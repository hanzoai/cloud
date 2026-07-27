// Package search mounts the Hanzo Cloud /v1/search/* surface: a native-Go,
// multi-tenant full-text index on Base/SQLite that speaks the Meilisearch REST
// dialect.
//
// It is the in-binary replacement for the standalone Meilisearch containers.
// Search is a subsystem of the one cloud binary like every other client, so it
// inherits per-org tenancy, encryption at rest (cek), and the platform's auth
// and o11y instead of running its own process with its own master key and its
// own RWO volume.
//
// # Why the Meilisearch dialect
//
// Hanzo Chat drives search through the `meilisearch@0.38` JS client and its
// mongoMeili Mongoose plugin. Speaking that dialect means chat points MEILI_HOST
// at this surface and needs no client change:
//
//	GET    /v1/search/health                              {"status":"available"}
//	GET    /v1/search/version
//	POST   /v1/search/indexes                             {uid, primaryKey}
//	GET    /v1/search/indexes/:uid
//	GET    /v1/search/indexes/:uid/settings
//	PATCH  /v1/search/indexes/:uid/settings               {filterableAttributes}
//	POST   /v1/search/indexes/:uid/documents              [doc,…]  add/replace
//	PUT    /v1/search/indexes/:uid/documents              [doc,…]  update/upsert
//	GET    /v1/search/indexes/:uid/documents              ?limit&offset
//	GET    /v1/search/indexes/:uid/documents/:id
//	DELETE /v1/search/indexes/:uid/documents/:id
//	POST   /v1/search/indexes/:uid/documents/delete-batch [id,…]
//	POST   /v1/search/indexes/:uid/search                 {q, filter, limit, offset}
//	GET    /v1/search/tasks/:uid
//
// Error bodies use Meilisearch's {message, code, type, link} shape rather than
// cloud's, because the JS client branches on those codes — index_not_found is
// how mongoMeili decides to create an index.
//
// # Tenancy
//
// A standalone Meilisearch has one global keyspace guarded by a master key, so
// every consumer sharing an instance shares its indexes. Here the tenant is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026), never a client-supplied header — and every query
// filters WHERE org=?. Two orgs may both hold an index named "messages" without
// ever seeing each other's documents. The bearer token is the org's cloud API
// key; the JS client already sends `Authorization: Bearer …`, so the wire shape
// is unchanged.
//
// Within an org, chat scopes results to the end user with a `user = "<id>"`
// filter, which this surface honours as Meilisearch does.
//
// # Writes are synchronous
//
// Meilisearch queues writes and returns an EnqueuedTask. SQLite applies them
// before the response, so the task ids reported here are already complete and
// GET /tasks/:uid always reports `succeeded` — a client polling waitForTask
// resolves immediately rather than never.
package search

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Version is the pkgVersion this surface reports to a Meilisearch client. It
// names the dialect implementation, not the Meilisearch release it emulates.
const Version = "1.0.0"

const (
	defaultLimit = 20
	maxLimit     = 1000
	// maxUID bounds an index name. Index names are short labels ("messages"),
	// and the uid is a stored column, so this only stops an absurd body.
	maxUID = 256
)

// state is search's own data; shared deps (logger, brand) live in the embedded
// cloud.Base, reached as s.Log / s.Brand.
type state struct {
	store *Store
	// taskSeq numbers the EnqueuedTask replies. It restarts at zero on boot,
	// which is sound because every task is already finished when it is minted.
	taskSeq *atomic.Int64
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the search surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("search.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("search.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("search.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("search.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "search.db"))
	if err != nil {
		return fmt.Errorf("search.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "search")
	s := &cloud.Service[state]{Base: b, State: state{store: store, taskSeq: new(atomic.Int64)}}
	mounted = s

	routes(app, s)

	b.Log.Info("search mounted", "brand", deps.Brand)
	return nil
}

// Shutdown releases the store.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// routes registers the Meilisearch dialect under /v1/search.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// health and version are registered as ABSOLUTE paths on app, the same idiom
	// every other OwnsHealth subsystem uses (clients/esign, clients/kms). Declared
	// on the group instead they do not survive the real mount composition — they
	// answered on a bare app in tests and 404'd in production, while every deeper
	// route on the same group worked.
	//
	// This subsystem sets OwnsHealth, so it serves its own health: Meilisearch's
	// {"status":"available"} body, failing closed when the store is unreadable.
	app.Get("/v1/search/health", cloud.Handle(s, health))
	app.Get("/v1/search/version", cloud.Handle(s, version))

	g := app.Group("/v1/search")

	g.Post("/indexes", cloud.Handle(s, createIndex))
	g.Get("/indexes/:uid", cloud.Handle(s, getIndex))
	g.Get("/indexes/:uid/settings", cloud.Handle(s, getSettings))
	g.Patch("/indexes/:uid/settings", cloud.Handle(s, patchSettings))
	g.Post("/indexes/:uid/search", cloud.Handle(s, searchIndex))

	// delete-batch is registered before /documents/:id so the literal segment
	// wins over the parameter.
	g.Post("/indexes/:uid/documents/delete-batch", cloud.Handle(s, deleteBatch))
	g.Post("/indexes/:uid/documents", cloud.Handle(s, addDocuments))
	g.Put("/indexes/:uid/documents", cloud.Handle(s, addDocuments))
	g.Get("/indexes/:uid/documents", cloud.Handle(s, listDocuments))
	g.Get("/indexes/:uid/documents/:id", cloud.Handle(s, getDocument))
	g.Delete("/indexes/:uid/documents/:id", cloud.Handle(s, deleteDocument))

	g.Get("/tasks/:uid", cloud.Handle(s, getTask))
}

// ---- health / version -----------------------------------------------------

// health fails closed: an unreadable store reports unavailable rather than
// letting a pod with a broken volume keep taking traffic.
func health(s *cloud.Service[state], c *zip.Ctx) error {
	if err := s.State.store.Ping(c.Context()); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "available"})
}

// version reports Meilisearch's version shape. commitSha names the
// implementation rather than a build hash, so a client logging it says which
// server answered instead of implying a Meilisearch release.
func version(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]string{
		"pkgVersion": Version, "commitSha": "hanzo-cloud", "commitDate": "",
	})
}

// ---- indexes --------------------------------------------------------------

func createIndex(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return forbidden(c)
	}
	var body struct {
		UID        string `json:"uid"`
		PrimaryKey string `json:"primaryKey"`
	}
	if err := c.Bind(&body); err != nil {
		return meiliError(c, http.StatusBadRequest, "bad_request", "invalid body", "system")
	}
	uid, ok := normUID(body.UID)
	if !ok {
		return meiliError(c, http.StatusBadRequest, "invalid_index_uid", "uid required", "invalid_request")
	}
	if _, err := s.State.store.EnsureIndex(c.Context(), org, uid, body.PrimaryKey); err != nil {
		return internal(c, err)
	}
	return enqueued(s, c, uid, "indexCreation")
}

func getIndex(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	idx, err := s.State.store.Index(c.Context(), org, uid)
	if errors.Is(err, errNoIndex) {
		return noIndex(c, uid)
	}
	if err != nil {
		return internal(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"uid": idx.UID, "primaryKey": idx.PrimaryKey,
		"createdAt": idx.CreatedAt, "updatedAt": idx.UpdatedAt,
	})
}

func getSettings(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	idx, err := s.State.store.Index(c.Context(), org, uid)
	if errors.Is(err, errNoIndex) {
		return noIndex(c, uid)
	}
	if err != nil {
		return internal(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"filterableAttributes": idx.FilterableAttributes})
}

func patchSettings(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	// mongoMeili patches settings on an index it has just asked for, so create
	// on demand rather than 404 and leave chat without an index.
	if _, err := s.State.store.EnsureIndex(c.Context(), org, uid, ""); err != nil {
		return internal(c, err)
	}
	var body struct {
		FilterableAttributes []string `json:"filterableAttributes"`
	}
	if err := c.Bind(&body); err != nil {
		return meiliError(c, http.StatusBadRequest, "bad_request", "invalid body", "system")
	}
	if body.FilterableAttributes != nil {
		if err := s.State.store.SetFilterable(c.Context(), org, uid, body.FilterableAttributes); err != nil {
			return internal(c, err)
		}
	}
	return enqueued(s, c, uid, "settingsUpdate")
}

// ---- documents ------------------------------------------------------------

// addDocuments serves both POST (add or replace) and PUT (add or update). Both
// are an upsert keyed by the index's primary key, which is what the JS client's
// addDocuments and updateDocuments both reduce to for whole documents.
func addDocuments(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	idx, err := s.State.store.EnsureIndex(c.Context(), org, uid, "")
	if err != nil {
		return internal(c, err)
	}
	var docs []map[string]any
	if err := c.Bind(&docs); err != nil {
		// A single object is accepted too; the JS client sends arrays, but a
		// hand-rolled caller sending one document should not get a 400.
		var one map[string]any
		if err2 := c.Bind(&one); err2 != nil || one == nil {
			return meiliError(c, http.StatusBadRequest, "bad_request", "expected an array of documents", "system")
		}
		docs = []map[string]any{one}
	}
	if err := s.State.store.Upsert(c.Context(), org, uid, idx.PrimaryKey, docs); err != nil {
		return internal(c, err)
	}
	return enqueued(s, c, uid, "documentAdditionOrUpdate")
}

func listDocuments(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	if _, err := s.State.store.Index(c.Context(), org, uid); errors.Is(err, errNoIndex) {
		return noIndex(c, uid)
	} else if err != nil {
		return internal(c, err)
	}
	limit := boundedInt(c.Query("limit"), defaultLimit)
	offset := boundedInt(c.Query("offset"), 0)
	docs, total, err := s.State.store.Documents(c.Context(), org, uid, limit, offset)
	if err != nil {
		return internal(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"results": docs, "offset": offset, "limit": limit, "total": total,
	})
}

func getDocument(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	if _, err := s.State.store.Index(c.Context(), org, uid); errors.Is(err, errNoIndex) {
		return noIndex(c, uid)
	} else if err != nil {
		return internal(c, err)
	}
	id := c.Param("id")
	doc, err := s.State.store.Document(c.Context(), org, uid, id)
	if errors.Is(err, sql.ErrNoRows) {
		return meiliError(c, http.StatusNotFound, "document_not_found",
			"Document `"+id+"` not found.", "invalid_request")
	}
	if err != nil {
		return internal(c, err)
	}
	return c.JSON(http.StatusOK, doc)
}

func deleteDocument(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	if err := s.State.store.Delete(c.Context(), org, uid, []string{c.Param("id")}); err != nil {
		return internal(c, err)
	}
	return enqueued(s, c, uid, "documentDeletion")
}

func deleteBatch(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	var ids []any
	if err := c.Bind(&ids); err != nil {
		return meiliError(c, http.StatusBadRequest, "bad_request", "expected an array of ids", "system")
	}
	pks := make([]string, 0, len(ids))
	for _, id := range ids {
		if pk := stringify(id); pk != "" {
			pks = append(pks, pk)
		}
	}
	if err := s.State.store.Delete(c.Context(), org, uid, pks); err != nil {
		return internal(c, err)
	}
	return enqueued(s, c, uid, "documentDeletion")
}

// ---- search ---------------------------------------------------------------

func searchIndex(s *cloud.Service[state], c *zip.Ctx) error {
	start := time.Now()
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	if _, err := s.State.store.Index(c.Context(), org, uid); errors.Is(err, errNoIndex) {
		return noIndex(c, uid)
	} else if err != nil {
		return internal(c, err)
	}
	var body struct {
		Q      string `json:"q"`
		Filter any    `json:"filter"`
		Limit  *int   `json:"limit"`
		Offset *int   `json:"offset"`
	}
	if err := c.Bind(&body); err != nil {
		return meiliError(c, http.StatusBadRequest, "bad_request", "invalid body", "system")
	}
	limit, offset := defaultLimit, 0
	if body.Limit != nil {
		limit = bound(*body.Limit, defaultLimit)
	}
	if body.Offset != nil {
		offset = bound(*body.Offset, 0)
	}
	hits, err := s.State.store.Search(c.Context(), org, uid, body.Q, ParseUserFilter(body.Filter), limit, offset)
	if err != nil {
		return internal(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"hits":             hits,
		"query":            body.Q,
		"processingTimeMs": time.Since(start).Milliseconds(),
		"limit":            limit,
		"offset":           offset,
		// Meilisearch reports an estimate; every hit is materialised here, so
		// the count is exact for this page.
		"estimatedTotalHits": len(hits),
	})
}

// ---- tasks ----------------------------------------------------------------

// getTask always reports success: writes are applied before the EnqueuedTask is
// returned, so any client polling waitForTask resolves at once.
func getTask(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := tenant(c); !ok {
		return forbidden(c)
	}
	id, _ := strconv.ParseInt(c.Param("uid"), 10, 64)
	now := time.Now().UTC().Format(time.RFC3339)
	return c.JSON(http.StatusOK, map[string]any{
		"uid": id, "status": "succeeded", "type": "documentAdditionOrUpdate",
		"enqueuedAt": now, "startedAt": now, "finishedAt": now,
	})
}

func enqueued(s *cloud.Service[state], c *zip.Ctx, uid, typ string) error {
	return c.JSON(http.StatusAccepted, map[string]any{
		"taskUid": s.State.taskSeq.Add(1), "indexUid": uid, "status": "enqueued",
		"type": typ, "enqueuedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- shared helpers -------------------------------------------------------

// tenant resolves the org — the tenant-isolation KEY — for a request. It uses
// principal.Org EXACTLY as SanitizeIdentity minted it from the validated IAM
// owner claim (HIP-0026): never lowercased, stripped, or truncated.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// scope resolves the (org, index) pair every index-addressed route needs,
// answering with the Meilisearch error body when either is missing.
func scope(c *zip.Ctx) (string, string, error) {
	org, ok := tenant(c)
	if !ok {
		return "", "", forbidden(c)
	}
	uid, ok := normUID(c.Param("uid"))
	if !ok {
		return "", "", meiliError(c, http.StatusBadRequest, "invalid_index_uid",
			"An index uid is required.", "invalid_request")
	}
	return org, uid, nil
}

func normUID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxUID {
		return "", false
	}
	return s, true
}

// meiliError writes Meilisearch's error body. The JS client branches on `code`,
// so these strings are part of the wire contract, not cosmetics.
func meiliError(c *zip.Ctx, status int, code, msg, typ string) error {
	return c.JSON(status, map[string]any{
		"message": msg, "code": code, "type": typ,
		"link": "https://www.meilisearch.com/docs/reference/errors/error_codes#" + code,
	})
}

func noIndex(c *zip.Ctx, uid string) error {
	return meiliError(c, http.StatusNotFound, "index_not_found",
		"Index `"+uid+"` not found.", "invalid_request")
}

func forbidden(c *zip.Ctx) error {
	return meiliError(c, http.StatusForbidden, "invalid_api_key",
		"The provided API key is invalid.", "auth")
}

// internal reports a store failure without leaking the query or path that
// produced it.
func internal(c *zip.Ctx, err error) error {
	if mounted != nil {
		mounted.Log.Error("search store", "err", err)
	}
	return meiliError(c, http.StatusInternalServerError, "internal",
		"An internal error occurred.", "system")
}

// bound clamps a caller-supplied paging value into [0, maxLimit], falling back
// to def when it is negative.
func bound(n, def int) int {
	if n < 0 {
		return def
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func boundedInt(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return bound(n, def)
}
