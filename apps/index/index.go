// Package index is fast full-text search over your own data, typos forgiven.
//
// A native-Go, multi-tenant full-text index on Base/SQLite that speaks the
// Meilisearch REST dialect.
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
//	GET    /v1/index/health                              {"status":"available"}
//	GET    /v1/index/version
//	POST   /v1/index/indexes                             {uid, primaryKey}
//	GET    /v1/index/indexes/:uid
//	GET    /v1/index/indexes/:uid/settings
//	PATCH  /v1/index/indexes/:uid/settings               {filterableAttributes}
//	POST   /v1/index/indexes/:uid/documents              [doc,…]  add/replace
//	PUT    /v1/index/indexes/:uid/documents              [doc,…]  update/upsert
//	GET    /v1/index/indexes/:uid/documents              ?limit&offset
//	GET    /v1/index/indexes/:uid/documents/:id
//	DELETE /v1/index/indexes/:uid/documents/:id
//	POST   /v1/index/indexes/:uid/documents/delete-batch [id,…]
//	POST   /v1/index/indexes/:uid/search                 {q, filter, limit, offset}
//	GET    /v1/index/tasks/:uid
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
package index

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
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
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

// state is index's own data; shared deps (logger, brand) live in the embedded
// cloud.Base, reached as s.Log / s.Brand.
type state struct {
	store *Store
	// taskSeq numbers the EnqueuedTask replies. It restarts at zero on boot,
	// which is sound because every task is already finished when it is minted.
	taskSeq *atomic.Int64
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the index surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("index.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("index.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("index.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("index.Mount: data dir: %w", err)
	}
	// Carry a store written under the subsystem's previous name over before
	// opening, so a rename never presents an existing tenant with an empty index.
	if err := migrateStore(deps.DataDir, "search.db", "index.db"); err != nil {
		return fmt.Errorf("index.Mount: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "index.db"))
	if err != nil {
		return fmt.Errorf("index.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "index")
	s := &cloud.Service[state]{Base: b, State: state{store: store, taskSeq: new(atomic.Int64)}}
	mounted = s

	routes(app, s)

	b.Log.Info("index mounted", "brand", deps.Brand)
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

// indexTenancy is the isolation sentence every org-scoped operation here needs
// and none of them can state alone: one tenant key, resolved the same way by
// tenant() in all fifteen. Written once so fifteen descriptions cannot become
// fifteen accounts of one predicate.
const indexTenancy = " The tenant is the org minted from the VALIDATED bearer's " +
	"owner claim, never a client-supplied header, and every query filters on it, so " +
	"two orgs may both hold an index named \"messages\" and neither can see the " +
	"other's documents. Without a validated principal the answer is 403 carrying " +
	"Meilisearch's `invalid_api_key` body."

// indexErrors is the other fact shared by the whole surface: error bodies are the
// dialect's, not cloud's, because a Meilisearch client BRANCHES on them.
const indexErrors = " Errors use Meilisearch's {message, code, type, link} shape " +
	"rather than cloud's, because that `code` is a wire contract a Meilisearch " +
	"client branches on."

// enqueuedIsAlreadyDone is the ONE rule a reader of this surface would otherwise
// get wrong, so every write states it. Meilisearch queues writes and answers an
// EnqueuedTask; this surface applies them to SQLite BEFORE answering, and then
// answers the same 202 EnqueuedTask for dialect compatibility. The status says
// "enqueued" and the work is already finished.
const enqueuedIsAlreadyDone = "\n\nThe 202 and its `enqueued` task are DIALECT " +
	"COMPATIBILITY, not a promise of later work: the write is already applied when " +
	"this answers, and the task it names is already complete. A client that polls " +
	"waitForTask resolves immediately rather than waiting, and a client that does " +
	"not poll has still had its write committed."

// The prose for this surface, all seventeen operations of it. Every route here is
// an untyped handler — the wire is the Meilisearch REST dialect, down to the
// error bodies — so there is no typed op for zipdoc to lift a doc comment from,
// and the whole subsystem published seventeen operationIds and nothing else:
// seventeen SDK methods and CLI commands that could not say what they search,
// whose data they touch, or that a task reported as `enqueued` is already done.
// Declared through the same registry openapi.Register uses, so a description
// renders only while the router actually serves the route and this can never
// invent one.
//
// Keyed by the WHOLE fiber pattern the group and leaf compose — health and
// version are absolute paths on the app, the other fifteen hang off /v1/index —
// because that is the path the router carries and the document renders.
func init() {
	openapi.Describe("/v1/index/health", http.MethodGet,
		"Report whether the search plane can serve",
		"Answers Meilisearch's `{\"status\":\"available\"}` when the index store is "+
			"readable. It FAILS CLOSED — an unreadable store answers 503 and "+
			"`unavailable` — so a replica whose volume has gone bad stops taking traffic "+
			"instead of answering every search with nothing found. It touches no tenant "+
			"data and needs no credential.")

	openapi.Describe("/v1/index/version", http.MethodGet,
		"Identify the search implementation answering",
		"Answers the version shape a Meilisearch client expects. It names THIS "+
			"implementation rather than a Meilisearch release — the commit field reads "+
			"`hanzo-cloud` — so a client that logs it records which server actually "+
			"answered instead of implying a Meilisearch build. Needs no credential.")

	openapi.Describe("/v1/index/stats", http.MethodGet,
		"Count the documents in each of your indexes",
		"Answers a document count per index for the caller's org, plus their sum. "+
			"`isIndexing` is always false, which is the honest answer here rather than a "+
			"stub: writes are applied before their response, so there is never a backlog "+
			"in progress to report."+indexTenancy+indexErrors)

	openapi.Describe("/v1/index/indexes", http.MethodGet,
		"List the indexes your org holds",
		"Answers every index in the caller's org with its primary key and timestamps. "+
			"It is the only way to enumerate what an org holds — without it an index "+
			"whose uid a caller has forgotten is unreachable."+indexTenancy+indexErrors)

	openapi.Describe("/v1/index/indexes", http.MethodPost,
		"Create an index",
		"Creates an index named by `uid` in the caller's org. `primaryKey` names the "+
			"document field that identifies a document and defaults to `id`. Creating an "+
			"index that already exists is not an error — it settles on the existing one, "+
			"primary key included — so a client that creates before every write is safe "+
			"to run repeatedly. A missing or over-long uid is 400 `invalid_index_uid`. A "+
			"new index starts with `user` filterable, which is what lets a multi-user app "+
			"narrow searches to one end user without configuring anything."+
			indexTenancy+indexErrors+enqueuedIsAlreadyDone)

	openapi.Describe("/v1/index/indexes/:uid", http.MethodGet,
		"Read one index's definition",
		"Answers a single index's uid, primary key and timestamps. An index the "+
			"caller's org does not hold is 404 `index_not_found` — which is the same "+
			"answer another org's index gives, since the org is a bound predicate on the "+
			"read."+indexTenancy+indexErrors)

	openapi.Describe("/v1/index/indexes/:uid", http.MethodDelete,
		"Delete an index and everything in it",
		"Drops one index in the caller's org together with all of its documents. This "+
			"is the only way to retire an index; without it a mistaken uid would be "+
			"permanent. It is idempotent — dropping an index that is not there still "+
			"succeeds."+indexTenancy+indexErrors+enqueuedIsAlreadyDone)

	openapi.Describe("/v1/index/indexes/:uid/settings", http.MethodGet,
		"Read an index's filterable attributes",
		"Answers the attributes an index allows filtering on. This dialect implements "+
			"the filterable-attributes setting and no other, so that is the whole of what "+
			"comes back. An index the caller's org does not hold is 404 "+
			"`index_not_found`."+indexTenancy+indexErrors)

	openapi.Describe("/v1/index/indexes/:uid/settings", http.MethodPatch,
		"Set which attributes an index can be filtered on",
		"Replaces an index's filterable attributes with the list in "+
			"`filterableAttributes`; omitting the field leaves them as they are. The "+
			"index is CREATED ON DEMAND rather than 404'd, because a client that "+
			"configures an index it has just asked for should not have to create it "+
			"first — this is the one read-shaped path on the surface that writes."+
			indexTenancy+indexErrors+enqueuedIsAlreadyDone)

	openapi.Describe("/v1/index/indexes/:uid/search", http.MethodPost,
		"Search an index, forgiving typos",
		"Answers the documents in one index matching `q`, ranked by how many of the "+
			"query's terms they match, with prefix matching so a partial word still "+
			"finds its document. `limit` defaults to 20 and is capped at 1000, `offset` "+
			"pages; a negative value falls back to the default rather than erroring.\n\n"+
			"`filter` takes a Meilisearch filter expression, or an array of them, and the "+
			"`user = \"…\"` and `user IN […]` forms are honoured — that is how an app with "+
			"many end users narrows results to one of them WITHIN the org. "+
			"`estimatedTotalHits` is exact for the page returned, not an estimate, "+
			"because every hit is materialised. An index the caller's org does not hold "+
			"is 404 `index_not_found`."+indexTenancy+indexErrors)

	openapi.Describe("/v1/index/indexes/:uid/documents", http.MethodPost,
		"Add or replace documents in an index",
		"Upserts documents into one index, keyed by the index's primary key: a "+
			"document whose key is already present is REPLACED, one that is not is added, "+
			"and it becomes searchable immediately. Send an array, or a single object — a "+
			"hand-rolled caller sending one document is accepted rather than 400'd. The "+
			"index is created on demand, so a first write needs no create call.\n\n"+
			"This and the PUT on the same path are the SAME operation: both are a whole "+
			"document upsert, which is what a Meilisearch client's addDocuments and "+
			"updateDocuments both reduce to here. A body that is neither an array nor an "+
			"object is 400."+indexTenancy+indexErrors+enqueuedIsAlreadyDone)

	openapi.Describe("/v1/index/indexes/:uid/documents", http.MethodPut,
		"Add or update documents in an index",
		"Upserts documents into one index, keyed by the index's primary key: a "+
			"document whose key is already present is REPLACED, one that is not is added, "+
			"and it becomes searchable immediately. Send an array, or a single object — a "+
			"hand-rolled caller sending one document is accepted rather than 400'd. The "+
			"index is created on demand, so a first write needs no create call.\n\n"+
			"This and the POST on the same path are the SAME operation, served by one "+
			"handler. Both exist because the Meilisearch dialect has both verbs; there is "+
			"no partial-update semantics on this one — a document is replaced whole "+
			"either way."+indexTenancy+indexErrors+enqueuedIsAlreadyDone)

	openapi.Describe("/v1/index/indexes/:uid/documents", http.MethodGet,
		"Page through the documents in an index",
		"Answers the documents in one index with a total count. `limit` defaults to 20 "+
			"and is capped at 1000, `offset` pages, and the response echoes both back so "+
			"a pager knows what it actually got. An index the caller's org does not hold "+
			"is 404 `index_not_found`."+indexTenancy+indexErrors)

	openapi.Describe("/v1/index/indexes/:uid/documents/:id", http.MethodGet,
		"Read one document by its primary key",
		"Answers the stored document whose primary key matches, exactly as it was "+
			"written. A missing document is 404 `document_not_found` and a missing index "+
			"is 404 `index_not_found` — two different codes, because a client that "+
			"branches on them treats the cases differently."+indexTenancy+indexErrors)

	openapi.Describe("/v1/index/indexes/:uid/documents/:id", http.MethodDelete,
		"Delete one document by its primary key",
		"Removes one document from an index. It is IDEMPOTENT: deleting a key that is "+
			"not there succeeds rather than 404, so a retry after a lost response is "+
			"safe."+indexTenancy+indexErrors+enqueuedIsAlreadyDone)

	openapi.Describe("/v1/index/indexes/:uid/documents/delete-batch", http.MethodPost,
		"Delete many documents by primary key in one call",
		"Removes every document named by an array of primary keys. Keys may be sent as "+
			"strings or numbers — a number keeps its exact decimal form, so an integer "+
			"key round-trips as `42` and never as scientific notation. Keys that are "+
			"absent from the index are skipped rather than failing the batch, so this is "+
			"idempotent. A body that is not an array is 400."+
			indexTenancy+indexErrors+enqueuedIsAlreadyDone)

	openapi.Describe("/v1/index/tasks/:uid", http.MethodGet,
		"Check a write task, which has already finished",
		"Answers `succeeded` for the task id given. It ALWAYS answers succeeded, and "+
			"that is honest rather than a stub: writes on this surface are applied before "+
			"their response returns, so by the time any task id exists to ask about, its "+
			"work is done. It exists so a Meilisearch client's waitForTask resolves at "+
			"once instead of polling forever for a queue that was never there. It "+
			"requires a validated principal but reads no tenant data.")
}

// routes registers the Meilisearch dialect under /v1/index.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// health and version are registered as ABSOLUTE paths on app, the same idiom
	// every other OwnsHealth subsystem uses (clients/esign, clients/kms). Declared
	// on the group instead they do not survive the real mount composition — they
	// answered on a bare app in tests and 404'd in production, while every deeper
	// route on the same group worked.
	//
	// This subsystem sets OwnsHealth, so it serves its own health: Meilisearch's
	// {"status":"available"} body, failing closed when the store is unreadable.
	app.Get("/v1/index/health", cloud.Handle(s, health))
	app.Get("/v1/index/version", cloud.Handle(s, version))

	g := app.Group("/v1/index")

	g.Get("/stats", cloud.Handle(s, stats))
	g.Get("/indexes", cloud.Handle(s, listIndexes))
	g.Post("/indexes", cloud.Handle(s, createIndex))
	g.Get("/indexes/:uid", cloud.Handle(s, getIndex))
	g.Delete("/indexes/:uid", cloud.Handle(s, deleteIndex))
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

// listIndexes answers everything an org has indexed. Without it an index whose
// uid a caller has forgotten is unreachable — there is no other way to enumerate
// what an org holds.
func listIndexes(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return forbidden(c)
	}
	idxs, _, err := s.State.store.Indexes(c.Context(), org)
	if err != nil {
		return internal(c, err)
	}
	results := make([]map[string]any, 0, len(idxs))
	for _, i := range idxs {
		results = append(results, map[string]any{
			"uid": i.UID, "primaryKey": i.PrimaryKey,
			"createdAt": i.CreatedAt, "updatedAt": i.UpdatedAt,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"results": results, "offset": 0, "limit": len(results), "total": len(results),
	})
}

// stats reports per-index document counts for the org, Meilisearch's shape.
func stats(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return forbidden(c)
	}
	idxs, counts, err := s.State.store.Indexes(c.Context(), org)
	if err != nil {
		return internal(c, err)
	}
	per := map[string]any{}
	total := 0
	for n, i := range idxs {
		per[i.UID] = map[string]any{"numberOfDocuments": counts[n], "isIndexing": false}
		total += counts[n]
	}
	return c.JSON(http.StatusOK, map[string]any{
		"databaseSize": total, "indexes": per,
	})
}

// deleteIndex drops an index and everything in it. Meilisearch has this and it
// is the only way to retire an index; without it a mistaken uid is permanent.
func deleteIndex(s *cloud.Service[state], c *zip.Ctx) error {
	org, uid, err := scope(c)
	if err != nil {
		return err
	}
	if err := s.State.store.DropIndex(c.Context(), org, uid); err != nil {
		return internal(c, err)
	}
	return enqueued(s, c, uid, "indexDeletion")
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
		mounted.Log.Error("index store", "err", err)
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
