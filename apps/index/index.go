package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
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
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("index: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("index.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("index.Use:  open store: %w", err)
	}
	b := cloud.NewBase(deps, "index")
	s := &cloud.Service[state]{Base: b, State: state{store: store, taskSeq: new(atomic.Int64)}}
	mounted = s

	// Bridge FIRST: a typed op receives only a context, so the validated org
	// reaches it by being parked there — never as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. Then the dialect's own envelope, which writes a *fault back as
	// Meilisearch's {message, code, type, link} bytes. fiber runs middleware in
	// registration order, so both must precede the leaves routes() registers.
	app.Use(cloud.Bridge())
	app.Use(envelope())
	routes(app, s)
	// The read AND the write, published for the processes that do NOT own this
	// store. `catalog` is one, and it needs both: without the read its browse is a
	// permanent 503, and without the write there is nothing for the browse to find
	// (rpc.go).
	expose()

	b.Log.Info("index mounted", "brand", cloud.Brand())
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

// The prose for the THREE routes that are not typed ops. Every other operation
// here carries its In and its Out (typed.go), so zipdoc lifts its prose from the
// handler's own doc comment; a description stated here as well would be the same
// fact in two places, and Fold replaces a structural operation with the typed one
// so the second copy would simply never render.
//
// These three cannot be typed ops, and the reason is in the WIRE: each takes a
// top-level JSON ARRAY as its body and hangs off a :uid path segment. zip binds a
// path parameter by walking the input's struct fields, so a slice In binds no uid,
// and a struct In cannot decode an array. The bodies are declared below through
// openapi.Register + openapi.OneOf, which is the honest declaration a single Go
// struct cannot make, so the document says what they accept even though no typed
// op describes them.
//
// Keyed by the WHOLE fiber pattern the group and leaf compose, because that is
// the path the router carries and the document renders.
func init() {
	spec.Describe("/v1/index/indexes/:uid/documents", http.MethodPost,
		"Add or replace documents in an index",
		"Writes documents into the caller's own index, keyed by the index's primary key: "+
			"a document whose key is already present is REPLACED whole. The body is the "+
			"dialect's own — an array of documents, or a single document — and each is "+
			"stored verbatim, so a read gives back exactly what was written.\n\n"+
			"The index is CREATED when it is missing rather than refused, because a "+
			"Meilisearch client writes before it configures.\n\n"+
			"The tenant is the org minted from the VALIDATED bearer's owner claim, never a "+
			"client-supplied header, so two orgs may both hold an index named \"messages\" "+
			"and neither can see the other's documents. Without a validated principal the "+
			"answer is 403 carrying the dialect's `invalid_api_key` body.\n\n"+
			"The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of "+
			"later work: the documents are searchable when this answers, and a client that "+
			"polls waitForTask resolves immediately.")
	spec.Describe("/v1/index/indexes/:uid/documents", http.MethodPut,
		"Add or update documents in an index",
		"The dialect's update spelling of the write above, and the same act: an upsert "+
			"keyed by the index's primary key. The JS client's addDocuments and "+
			"updateDocuments both reduce to this for whole documents, so both spellings are "+
			"served and both behave identically.\n\n"+
			"The tenant is the org minted from the VALIDATED bearer's owner claim, never a "+
			"client-supplied header. Without a validated principal the answer is 403 "+
			"carrying the dialect's `invalid_api_key` body.\n\n"+
			"The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of "+
			"later work: the write is already applied when this answers.")
	spec.Describe("/v1/index/indexes/:uid/documents/delete-batch", http.MethodPost,
		"Delete many documents by primary key in one call",
		"Removes every named document from the caller's own index. The body is the "+
			"dialect's own: a bare array of primary keys, which may be strings or numbers. "+
			"A key that is not there is not an error, so a client reconciling its own corpus "+
			"can send one list rather than checking each key first.\n\n"+
			"The tenant is the org minted from the VALIDATED bearer's owner claim, never a "+
			"client-supplied header. Without a validated principal the answer is 403 "+
			"carrying the dialect's `invalid_api_key` body.\n\n"+
			"The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of "+
			"later work: the documents are already gone when this answers.")

	// The BODIES the three untyped routes carry, declared through the reflection
	// client. Without this each renders as an operationId and a tag and NOTHING else —
	// indistinguishable from a route that takes no input and returns none — so every
	// SDK generated off the document offered a document upload with nowhere to put
	// the documents. OneOf is the honest declaration a single Go struct cannot make:
	// the wire really is two shapes on one path, and naming one would publish an API
	// that cannot send the other.
	//
	// json.RawMessage rather than map[string]any for a document: a document is
	// arbitrary JSON, and map[string]any publishes additionalProperties as an OBJECT
	// — an assertion that every value is one, which a document with a string field
	// refutes.
	docs := spec.OneOf{[]json.RawMessage{}, json.RawMessage{}}
	spec.Register("/v1/index/indexes/:uid/documents", http.MethodPost, docs, indexEnqueued{})
	spec.Register("/v1/index/indexes/:uid/documents", http.MethodPut, docs, indexEnqueued{})
	// delete-batch is ONE array, not a choice between two. It read as
	// OneOf{[]string, []float64} — "a list of strings or a list of numbers" —
	// which is wrong twice. It forbids the MIXED list the engine accepts, since an
	// id is a string or a number per document and not per request. And a union of
	// two unnamed arrays is not a name any generator can write: typescript-fetch
	// emitted `Array<string> | Array<number>ToJSON(...)` as the body expression,
	// which is a syntax error, so hanzo-js/sdk-fetch had not compiled since
	// v8.5.102. An id is whatever JSON an id is, which is the same argument
	// json.RawMessage already carries for a document one line up.
	spec.Register("/v1/index/indexes/:uid/documents/delete-batch", http.MethodPost,
		[]json.RawMessage{}, indexEnqueued{})
}

// routes registers the Meilisearch dialect under /v1/index.
//
// FOURTEEN of the seventeen are TYPED ops (typed.go), declared on the group so
// each op's path is the prefix composed with its leaf — the same composition the
// router does, and the identity every projection keys on. THREE stay untyped
// because their body is a top-level JSON array on a :uid route; the init above
// declares those bodies.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}

	g := app.Group("/v1/index")
	// health and version hang off the SAME group as everything else, and for a
	// typed op that is not a change of address: zip registers every op on the
	// concrete *zip.App with the group's prefix already composed into its path
	// (scope.OpScope + registerTyped), so `g.Get("/health")` and
	// `app.Get("/v1/index/health")` are the same route on the same router.
	// The absolute form these two used is what an UNTYPED handler needs, because
	// that one lands on the group NODE; it is also the only form cmd/zipdoc cannot
	// resolve, and prose it cannot file is prose that never reaches the document.
	//
	// This subsystem sets OwnsHealth, so it serves its own health: Meilisearch's
	// {"status":"available"} body, failing closed when the store is unreadable —
	// two statuses over ONE shape, which the op declares and the answer chooses
	// (indexHealth.StatusCode).
	g.Get("/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))
	g.Get("/version", o.version)

	g.Get("/stats", o.stats)
	g.Get("/indexes", o.listIndexes)
	g.Post("/indexes", o.createIndex, zip.WithStatus(http.StatusAccepted))
	g.Get("/indexes/:uid", o.getIndex)
	g.Delete("/indexes/:uid", o.deleteIndex, zip.WithStatus(http.StatusAccepted))
	g.Get("/indexes/:uid/settings", o.getSettings)
	g.Patch("/indexes/:uid/settings", o.patchSettings, zip.WithStatus(http.StatusAccepted))
	g.Post("/indexes/:uid/search", o.searchIndex)

	// delete-batch is registered before /documents/:id so the literal segment
	// wins over the parameter.
	g.Raw(http.MethodPost, "/indexes/:uid/documents/delete-batch", cloud.Handle(s, deleteBatch))
	g.Raw(http.MethodPost, "/indexes/:uid/documents", cloud.Handle(s, addDocuments))
	g.Raw(http.MethodPut, "/indexes/:uid/documents", cloud.Handle(s, addDocuments))
	g.Get("/indexes/:uid/documents", o.listDocuments)
	g.Get("/indexes/:uid/documents/:id", o.getDocument)
	g.Delete("/indexes/:uid/documents/:id", o.deleteDocument, zip.WithStatus(http.StatusAccepted))

	g.Get("/tasks/:uid", o.getTask)
}

// ---- health / version -----------------------------------------------------

// ---- indexes --------------------------------------------------------------

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
		return ops{s: s}.fail(c, err)
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
		return ops{s: s}.fail(c, err)
	}
	return enqueued(s, c, uid, "documentAdditionOrUpdate")
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
		return ops{s: s}.fail(c, err)
	}
	return enqueued(s, c, uid, "documentDeletion")
}

// ---- search ---------------------------------------------------------------

// ---- tasks ----------------------------------------------------------------

func enqueued(s *cloud.Service[state], c *zip.Ctx, uid, typ string) error {
	return c.JSON(http.StatusAccepted, ops{s: s}.accepted(uid, typ))
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
		return "", "", meiliError(c, http.StatusForbidden, "invalid_api_key",
			"The provided API key is invalid.", "auth")
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
	// A NUL, or bytes that are not UTF-8, cannot be kept in a column of text on
	// hanzo-sql, so it is not a uid on either engine.
	if s == "" || len(s) > maxUID || dialect.Text(s) != nil {
		return "", false
	}
	return s, true
}

// meiliError writes Meilisearch's error body. The JS client branches on `code`,
// so these strings are part of the wire contract, not cosmetics.
func meiliError(c *zip.Ctx, status int, code, msg, typ string) error {
	f := refuse(status, code, msg, typ)
	return c.JSON(f.status, f.body)
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
