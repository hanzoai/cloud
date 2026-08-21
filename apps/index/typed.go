package index

// typed.go is the index's TYPED plane — the ops that carry In/Out types, and so
// the only index routes that reach a schema, an MCP tool, a CLI command and a
// generated SDK method. Before this file the whole subsystem published
// seventeen operationIds and prose: no caller could learn from the document what
// an index row looks like, what a search answers with, or that a task reported
// `enqueued` is already finished.
//
// THE DIALECT DID NOT MOVE. Every model below is the shape the untyped handler
// assembled, spelled as a Go type. Those handlers built map[string]any, which
// encoding/json writes in SORTED KEY ORDER, so every model here declares its
// fields in ALPHABETICAL json-tag order and the typed answer is BYTE-identical
// to the map it replaces — not merely equal as JSON. index_wire_test.go pins
// that field by field, so a field added out of order fails rather than drifting.
//
// THE ERRORS DID NOT MOVE EITHER, and that is the harder half. Meilisearch
// answers every failure with {message, code, type, link} and its JS client
// BRANCHES on `code` — index_not_found is how mongoMeili decides to create an
// index — while zip renders a returned error as the flat {status, code, error}
// that has no `message` and no dialect `code` at all. So an op returns a *fault
// carrying the dialect body and envelope() writes it back. That is the same
// shape apps/goja's BundleErr and cloud's own Denied take, and it is not an
// escape from typing: the op still declares its In and its Out, so all five
// projections exist.
//
// WHAT IS NOT TYPED, AND WHY. Three writes take a top-level JSON ARRAY as their
// body — [doc,…] on the two document upserts, [id,…] on delete-batch — and each
// hangs off a :uid path segment. zip binds a path param by walking the input's
// STRUCT FIELDS (bindURL, typed.go), so an In that is a slice binds no uid, and
// an In that is a struct cannot decode an array body. They stay untyped and
// declare their bodies through openapi.Register + openapi.OneOf instead, so the
// SDKs stop offering a document upload with nowhere to put the documents.
// typed_wire_test.go holds that as a closed, measured ledger.

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// ops binds the service to the typed index ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// ---- the dialect's refusal, carried as an error ----------------------------

// faultBody is Meilisearch's error object, and it is a WIRE CONTRACT rather than
// a presentation choice: the JS client parses these four keys and branches on
// `code`. Fields are in alphabetical json-tag order because the untyped writer
// beside it emits a map and encoding/json sorts map keys, so the two spellings
// produce the same bytes.
type faultBody struct {
	// Code is the machine-readable reason a client branches on
	// (index_not_found, invalid_api_key, document_not_found).
	Code string `json:"code"`
	// Link is the dialect's documentation URL for Code.
	Link string `json:"link"`
	// Message is the human sentence, naming the uid or id where there is one.
	Message string `json:"message"`
	// Type is the dialect's coarse class: invalid_request, auth or system.
	Type string `json:"type"`
}

// fault is a dialect refusal carried as a Go error, which is the only refusal
// channel a typed op has. envelope() writes its body back untouched, so a typed
// op refuses with exactly the bytes meiliError writes beside it.
type fault struct {
	status int
	body   faultBody
}

func (f *fault) Error() string { return f.body.Message }

// Unwrap gives the refusal a status and a sentence OFF the HTTP path, where
// there is no response to write bytes into: an MCP tools/call and an in-process
// CLI invoke run the op without passing through envelope, so zip's own error
// handler renders this instead — the same status, code and sentence in zip's
// shape, rather than a blanket 500 that loses all three.
func (f *fault) Unwrap() error {
	return &zip.HTTPError{Status: f.status, Code: f.body.Code, Msg: f.body.Message}
}

// refuse builds the dialect refusal both planes share. ONE constructor, so the
// untyped writer and the typed error cannot describe one failure two ways.
func refuse(status int, code, msg, typ string) *fault {
	return &fault{status: status, body: faultBody{
		Code:    code,
		Link:    "https://www.meilisearch.com/docs/reference/errors/error_codes#" + code,
		Message: msg,
		Type:    typ,
	}}
}

// envelope writes a *fault back as the dialect's own bytes under its own status.
// Anything else propagates unchanged. Install it BEFORE the leaves it serves —
// fiber runs middleware in registration order, so one installed after them never
// runs.
func envelope() zip.Handler {
	return func(c *zip.Ctx) error {
		err := c.Continue()
		if f, ok := errors.AsType[*fault](err); ok {
			return c.JSON(f.status, f.body)
		}
		return err
	}
}

// ---- the facts every op resolves the same way ------------------------------

// org is the tenant-isolation KEY for a typed op: the org minted from the
// VALIDATED bearer's owner claim, parked by cloud.Bridge and read back here. It
// is never an In field — a caller that could name its own tenant would be
// reading another org's documents — and its absence is the dialect's own
// `invalid_api_key`, which is what the untyped surface has always answered.
func (o ops) org(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", refuse(http.StatusForbidden, "invalid_api_key",
			"The provided API key is invalid.", "auth")
	}
	return org, nil
}

// at resolves the (org, uid) pair every index-addressed op needs, refusing with
// the dialect's own body when either is missing — the tenant first, exactly the
// order the untyped handlers used, so a caller with no principal never learns
// whether a uid was well formed.
func (o ops) at(ctx context.Context, uid string) (string, string, error) {
	org, err := o.org(ctx)
	if err != nil {
		return "", "", err
	}
	clean, ok := normUID(uid)
	if !ok {
		return "", "", refuse(http.StatusBadRequest, "invalid_index_uid",
			"An index uid is required.", "invalid_request")
	}
	return org, clean, nil
}

// exists refuses with `index_not_found` for an index this org does not hold. It
// is the read path's precondition: a Meilisearch client reads that code as
// permission to create the index, so the reads must answer it and the writes
// (which create on demand) must not.
func (o ops) exists(ctx context.Context, org, uid string) error {
	if _, err := o.s.State.store.Index(ctx, org, uid); errors.Is(err, errNoIndex) {
		return absent(uid)
	} else if err != nil {
		return o.internal(err)
	}
	return nil
}

// absent is the dialect's index_not_found, named once because it is the code the
// whole client library pivots on.
func absent(uid string) *fault {
	return refuse(http.StatusNotFound, "index_not_found",
		"Index `"+uid+"` not found.", "invalid_request")
}

// internal reports a store failure without leaking the query or path that
// produced it: the detail goes to the log, the caller gets the dialect's opaque
// `internal`.
func (o ops) internal(err error) *fault {
	o.s.Log.Error("index store", "err", err)
	return refuse(http.StatusInternalServerError, "internal",
		"An internal error occurred.", "system")
}

// accepted is the EnqueuedTask every write answers with. The task is already
// finished when this is built — SQLite applied the write before the op returned
// — and the 202 is dialect compatibility, not a promise of later work.
func (o ops) accepted(uid, typ string) *indexEnqueued {
	return &indexEnqueued{
		EnqueuedAt: time.Now().UTC().Format(time.RFC3339),
		IndexUID:   uid,
		Status:     "enqueued",
		TaskUID:    o.s.State.taskSeq.Add(1),
		Type:       typ,
	}
}

// ---- models ----------------------------------------------------------------

// noInput is the In of an op addressed entirely by the caller's principal: the
// org IS the address and there is nothing to bind off the wire.
type noInput struct{}

// indexHealth is the dialect's health object: one word, and the only one a
// Meilisearch client checks before it will use a server at all.
type indexHealth struct {
	// Status is `available` when the store is readable and `unavailable` when it
	// is not — the second answer rides a 503, so a pod with a broken volume is
	// taken out of rotation rather than serving empty searches.
	Status string `json:"status"`
}

// StatusCode is how this answer states which of its two declared statuses it is:
// an unreadable store is a 503 carrying the same body, which is the dialect's
// own shape for it and the reason this op declares two statuses rather than
// returning an error for one of them.
func (h *indexHealth) StatusCode() int {
	if h.Status == "available" {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

// indexVersion identifies the implementation answering, in the dialect's own
// version shape.
type indexVersion struct {
	// CommitDate is empty here: this surface is a dialect implementation, not a
	// build of Meilisearch, so there is no upstream commit to date.
	CommitDate string `json:"commitDate"`
	// CommitSha names the implementation (`hanzo-cloud`) rather than a build
	// hash, so a client logging it records which server answered instead of
	// implying a Meilisearch release.
	CommitSha string `json:"commitSha"`
	// PkgVersion is this dialect implementation's own version.
	PkgVersion string `json:"pkgVersion"`
}

// indexCount is one index's row in the stats report.
type indexCount struct {
	// IsIndexing is always false: writes are applied before their response, so
	// there is never a background pass a caller could be waiting on.
	IsIndexing bool `json:"isIndexing"`
	// NumberOfDocuments is how many documents this org holds in that index.
	NumberOfDocuments int `json:"numberOfDocuments"`
}

// indexStats is the per-index document count for the caller's own org.
type indexStats struct {
	// DatabaseSize is the org's total document count across its indexes. It is a
	// count, not bytes: the store is shared by every tenant, so a byte figure
	// would either be the whole file (another tenant's size) or a fiction.
	DatabaseSize int `json:"databaseSize"`
	// Indexes maps each index uid to its own count.
	Indexes map[string]indexCount `json:"indexes"`
}

// indexView is one index's definition — the dialect's index object.
type indexView struct {
	// CreatedAt is when this org first created the index, RFC 3339.
	CreatedAt string `json:"createdAt"`
	// PrimaryKey is the document field that identifies a row; an upsert is keyed
	// on it. Empty until a document establishes one.
	PrimaryKey string `json:"primaryKey"`
	// UID is the index's name within the org. Two orgs may both hold `messages`.
	UID string `json:"uid"`
	// UpdatedAt is when the index or its settings last changed, RFC 3339.
	UpdatedAt string `json:"updatedAt"`
}

// indexList is a page of index definitions, in the dialect's paging envelope.
type indexList struct {
	// Limit is how many rows this page could hold.
	Limit int `json:"limit"`
	// Offset is where this page starts.
	Offset int `json:"offset"`
	// Results are the index definitions themselves.
	Results []indexView `json:"results"`
	// Total is how many indexes the org holds altogether.
	Total int `json:"total"`
}

// indexSettings is the settings subset this surface implements.
type indexSettings struct {
	// FilterableAttributes are the document fields a search `filter` may
	// constrain. A field not listed here cannot be filtered on.
	FilterableAttributes []string `json:"filterableAttributes"`
}

// indexEnqueued is the dialect's EnqueuedTask, answered by every write.
type indexEnqueued struct {
	// EnqueuedAt is when the task was recorded, RFC 3339 — which is also when it
	// completed.
	EnqueuedAt string `json:"enqueuedAt"`
	// IndexUID names the index the write landed in.
	IndexUID string `json:"indexUid"`
	// Status is always `enqueued`, for dialect compatibility. The work is
	// already done.
	Status string `json:"status"`
	// TaskUID identifies the task for a client that polls it. Polling resolves
	// immediately.
	TaskUID int64 `json:"taskUid"`
	// Type is the dialect's name for the kind of write: indexCreation,
	// indexDeletion, settingsUpdate, documentAdditionOrUpdate, documentDeletion.
	Type string `json:"type"`
}

// indexTask is a write task's record. Every task this surface mints is already
// finished, so this always reports success.
type indexTask struct {
	// EnqueuedAt, StartedAt and FinishedAt are the same instant: the write was
	// applied before its task id was minted.
	EnqueuedAt string `json:"enqueuedAt"`
	// FinishedAt is when the write completed.
	FinishedAt string `json:"finishedAt"`
	// StartedAt is when the write began.
	StartedAt string `json:"startedAt"`
	// Status is always `succeeded`.
	Status string `json:"status"`
	// Type names the kind of write, for a client that inspects it.
	Type string `json:"type"`
	// UID echoes the task id that was asked about.
	UID int64 `json:"uid"`
}

// indexDocuments is a page of stored documents. A document is whatever JSON was
// written, so the rows are unconstrained.
type indexDocuments struct {
	// Limit is how many documents this page could hold.
	Limit int `json:"limit"`
	// Offset is where this page starts.
	Offset int `json:"offset"`
	// Results are the documents themselves, exactly as they were stored.
	Results []json.RawMessage `json:"results"`
	// Total is how many documents the index holds altogether.
	Total int `json:"total"`
}

// indexHits is a search answer, in the dialect's own result shape.
type indexHits struct {
	// EstimatedTotalHits is the dialect's name for the match count. Every hit is
	// materialised here, so for this page it is exact rather than estimated.
	EstimatedTotalHits int `json:"estimatedTotalHits"`
	// Hits are the matching documents, most relevant first, exactly as stored.
	Hits []json.RawMessage `json:"hits"`
	// Limit is how many hits this page could hold.
	Limit int `json:"limit"`
	// Offset is where this page starts.
	Offset int `json:"offset"`
	// ProcessingTimeMs is how long the query took, in milliseconds.
	ProcessingTimeMs int64 `json:"processingTimeMs"`
	// Query echoes the search terms, which is what a client renders above the
	// results.
	Query string `json:"query"`
}

// indexNew names an index to create. The uid is a BODY field and carries
// url:"-" so a query string cannot redirect the write: the untyped handler read
// the body and nothing else, and zip's binder would otherwise let `?uid=` win.
type indexNew struct {
	// PrimaryKey is the document field that identifies a row. Optional — the
	// first write establishes one when it is omitted.
	PrimaryKey string `json:"primaryKey" url:"-"`
	// UID is the index's name within the org. Required.
	UID string `json:"uid" url:"-"`
}

// indexAt addresses one index by uid.
type indexAt struct {
	// UID is the index's name within the org.
	UID string `json:"-" url:"uid"`
}

// indexFilter sets which attributes an index can be filtered on. The list is a
// BODY field (url:"-") and the uid is the path segment, so the URL names the
// target and the body cannot redirect it.
type indexFilter struct {
	// FilterableAttributes replaces the whole filterable set. Omitted leaves it
	// unchanged; an empty array clears it.
	FilterableAttributes []string `json:"filterableAttributes" url:"-"`
	// UID is the index's name within the org.
	UID string `json:"-" url:"uid"`
}

// indexQuery is a search. Every field but the uid is body-only (url:"-"),
// because the untyped handler read the body alone and a query string that could
// override `q` would silently return a different result set for the same call.
type indexQuery struct {
	// Filter narrows the hits, in the dialect's filter syntax: a string, or an
	// array of strings combined with AND. Only attributes named in the index's
	// filterableAttributes can be constrained. Left unconstrained here because
	// the dialect accepts both shapes and naming one would publish an API that
	// cannot send the other.
	Filter json.RawMessage `json:"filter,omitempty" url:"-"`
	// Limit is how many hits to return. Absent means 20; the ceiling is 1000.
	Limit *int `json:"limit,omitempty" url:"-"`
	// Offset is where to start. Absent means 0.
	Offset *int `json:"offset,omitempty" url:"-"`
	// Q is the search text. Typos are forgiven. An empty Q matches everything,
	// which is how a client lists an index by relevance rather than by insertion
	// order.
	Q string `json:"q" url:"-"`
	// UID is the index's name within the org.
	UID string `json:"-" url:"uid"`
}

// indexPage pages through an index's documents.
//
// Limit and Offset are STRINGS, deliberately. The handler defaults on an absent
// OR unparseable value and honours an explicit 0, and an int field cannot tell
// those apart: zip's setScalar leaves an unparseable value at the field's zero,
// so `?limit=abc` and a missing `?limit=` would both become 0 where this surface
// has always answered 20. TestPagingKeepsItsDefaults pins all three cases.
type indexPage struct {
	// Limit is how many documents to return, as it appears in the URL. Absent or
	// unparseable means 20; the ceiling is 1000.
	Limit string `json:"-" url:"limit"`
	// Offset is where to start, as it appears in the URL. Absent or unparseable
	// means 0.
	Offset string `json:"-" url:"offset"`
	// UID is the index's name within the org.
	UID string `json:"-" url:"uid"`
}

// indexDoc addresses one document by its primary key.
type indexDoc struct {
	// ID is the document's primary-key value.
	ID string `json:"-" url:"id"`
	// UID is the index's name within the org.
	UID string `json:"-" url:"uid"`
}

// indexTaskAt addresses one write task.
//
// UID is an int64 and an unparseable one binds as 0, which is exactly what the
// untyped handler did (strconv.ParseInt with its error dropped): every task is
// already complete, so the id is echoed rather than looked up and there is
// nothing a bad value can reach.
type indexTaskAt struct {
	// UID is the task id, as EnqueuedTask reported it.
	UID int64 `json:"-" url:"uid"`
}

// ---- ops -------------------------------------------------------------------

// health reports whether the search plane can serve.
//
// Answers the dialect's `{"status":"available"}` when the index store is
// readable. It FAILS CLOSED — an unreadable store answers 503 with
// `{"status":"unavailable"}` rather than an empty result set, because a
// Meilisearch client probes this before it will use a server at all and a
// cheerful 200 over a broken volume turns "search is down" into "nothing
// matched". It requires no principal and reads no tenant data.
func (o ops) health(ctx context.Context, _ *noInput) (*indexHealth, error) {
	if err := o.s.State.store.Ping(ctx); err != nil {
		return &indexHealth{Status: "unavailable"}, nil
	}
	return &indexHealth{Status: "available"}, nil
}

// version identifies the search implementation answering.
//
// Reports the dialect's version shape with `commitSha` naming this
// implementation rather than a Meilisearch build, so a client that logs the
// version records which server answered instead of implying a release of
// software this is not. It requires no principal and reads no tenant data.
func (o ops) version(_ context.Context, _ *noInput) (*indexVersion, error) {
	return &indexVersion{CommitDate: "", CommitSha: "hanzo-cloud", PkgVersion: Version}, nil
}

// stats counts the documents in each of your indexes.
//
// Reports every index the caller's own org holds with its document count, plus
// the org's total. `isIndexing` is always false because writes here are applied
// before their response — there is never a background pass to wait on.
//
// The tenant is the org minted from the VALIDATED bearer's owner claim, never a
// client-supplied header, so this counts the caller's own documents and no
// other tenant's. Without a validated principal the answer is 403 carrying the
// dialect's `invalid_api_key` body.
func (o ops) stats(ctx context.Context, _ *noInput) (*indexStats, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	idxs, counts, err := o.s.State.store.Indexes(ctx, org)
	if err != nil {
		return nil, o.internal(err)
	}
	out := &indexStats{Indexes: map[string]indexCount{}}
	for n, i := range idxs {
		out.Indexes[i.UID] = indexCount{NumberOfDocuments: counts[n]}
		out.DatabaseSize += counts[n]
	}
	return out, nil
}

// listIndexes lists the indexes your org holds.
//
// Answers every index in the caller's own org with its primary key and
// timestamps. Without it an index whose uid a caller has forgotten is
// unreachable — there is no other way to enumerate what an org holds. The page
// is the whole set: an org's index count is small by construction, so `limit`
// and `total` both report it.
//
// The tenant is the org minted from the VALIDATED bearer's owner claim, never a
// client-supplied header, and two orgs may both hold an index named "messages"
// without either seeing the other. Without a validated principal the answer is
// 403 carrying the dialect's `invalid_api_key` body.
func (o ops) listIndexes(ctx context.Context, _ *noInput) (*indexList, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	idxs, _, err := o.s.State.store.Indexes(ctx, org)
	if err != nil {
		return nil, o.internal(err)
	}
	results := make([]indexView, 0, len(idxs))
	for _, i := range idxs {
		results = append(results, view(i))
	}
	return &indexList{Limit: len(results), Offset: 0, Results: results, Total: len(results)}, nil
}

// createIndex creates an index.
//
// Registers a named index in the caller's own org and answers the dialect's
// EnqueuedTask. It is idempotent: creating an index that already exists returns
// the same receipt and changes nothing, which is what lets a client create on
// startup without checking first.
//
// `primaryKey` is optional — the first write establishes one when it is omitted.
// An index is a ROW here rather than a table, so an unusual uid is stored
// verbatim instead of being sanitised into a schema name.
//
// The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
// later work: the write is already applied when this answers. A client that
// polls waitForTask resolves immediately.
//
// Example: {"uid": "messages", "primaryKey": "id"}
func (o ops) createIndex(ctx context.Context, in *indexNew) (*indexEnqueued, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	uid, ok := normUID(in.UID)
	if !ok {
		return nil, refuse(http.StatusBadRequest, "invalid_index_uid", "uid required", "invalid_request")
	}
	if _, err := o.s.State.store.EnsureIndex(ctx, org, uid, in.PrimaryKey); err != nil {
		return nil, o.internal(err)
	}
	return o.accepted(uid, "indexCreation"), nil
}

// getIndex reads one index's definition.
//
// Answers the index's uid, primary key and timestamps. An index this org does
// not hold answers 404 carrying the dialect's `index_not_found` — the code a
// Meilisearch client reads as permission to create it, which is why this is a
// refusal rather than an empty object.
//
// The uid is scoped to the caller's own org, so another tenant's index is
// indistinguishable from one that never existed: this surface is not an
// existence oracle.
func (o ops) getIndex(ctx context.Context, in *indexAt) (*indexView, error) {
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	idx, err := o.s.State.store.Index(ctx, org, uid)
	if errors.Is(err, errNoIndex) {
		return nil, absent(uid)
	}
	if err != nil {
		return nil, o.internal(err)
	}
	out := view(idx)
	return &out, nil
}

// deleteIndex deletes an index and everything in it.
//
// Drops the index and every document in it from the caller's own org, and
// answers the dialect's EnqueuedTask. This is the only way to retire an index;
// without it a mistaken uid is permanent. Deleting an index that is not there
// succeeds, so a cleanup pass is safe to re-run.
//
// The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
// later work: the documents are already gone when this answers.
func (o ops) deleteIndex(ctx context.Context, in *indexAt) (*indexEnqueued, error) {
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.store.DropIndex(ctx, org, uid); err != nil {
		return nil, o.internal(err)
	}
	return o.accepted(uid, "indexDeletion"), nil
}

// getSettings reads an index's filterable attributes.
//
// Answers the settings subset this surface implements: the attributes a search
// `filter` may constrain. An index this org does not hold answers 404 carrying
// the dialect's `index_not_found`.
func (o ops) getSettings(ctx context.Context, in *indexAt) (*indexSettings, error) {
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	idx, err := o.s.State.store.Index(ctx, org, uid)
	if errors.Is(err, errNoIndex) {
		return nil, absent(uid)
	}
	if err != nil {
		return nil, o.internal(err)
	}
	return &indexSettings{FilterableAttributes: idx.FilterableAttributes}, nil
}

// patchSettings sets which attributes an index can be filtered on.
//
// Replaces the whole filterable set. An attribute not listed here cannot be
// used in a search `filter`, so this is what makes a per-user or per-tag
// narrowing possible at all.
//
// It CREATES the index when it is missing rather than answering 404, because a
// Meilisearch client configures settings on an index it has just asked for and
// a refusal there leaves the client with no index at all.
//
// The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
// later work: the setting is already applied when this answers.
//
// Example: {"filterableAttributes": ["user", "conversationId"]}
func (o ops) patchSettings(ctx context.Context, in *indexFilter) (*indexEnqueued, error) {
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	if _, err := o.s.State.store.EnsureIndex(ctx, org, uid, ""); err != nil {
		return nil, o.internal(err)
	}
	if in.FilterableAttributes != nil {
		if err := o.s.State.store.SetFilterable(ctx, org, uid, in.FilterableAttributes); err != nil {
			return nil, o.internal(err)
		}
	}
	return o.accepted(uid, "settingsUpdate"), nil
}

// searchIndex searches an index, forgiving typos.
//
// Ranks the org's documents in one index against `q` and answers the matching
// documents whole, most relevant first. A prefix matches, so a partial word
// finds the documents containing it, and `filter` narrows the result to
// documents whose filterable attributes match — which is how a caller scopes
// results to one end user within its own org.
//
// `estimatedTotalHits` is the dialect's name for the count; every hit is
// materialised here, so for this page it is exact. An index this org does not
// hold answers 404 carrying the dialect's `index_not_found`.
//
// Example: {"q": "roadmap", "filter": "user = alice", "limit": 20}
func (o ops) searchIndex(ctx context.Context, in *indexQuery) (*indexHits, error) {
	start := time.Now()
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	if err := o.exists(ctx, org, uid); err != nil {
		return nil, err
	}
	limit, offset := defaultLimit, 0
	if in.Limit != nil {
		limit = bound(*in.Limit, defaultLimit)
	}
	if in.Offset != nil {
		offset = bound(*in.Offset, 0)
	}
	var filter any
	if len(in.Filter) > 0 {
		if err := json.Unmarshal(in.Filter, &filter); err != nil {
			return nil, refuse(http.StatusBadRequest, "bad_request", "invalid body", "system")
		}
	}
	hits, err := o.s.State.store.Search(ctx, org, uid, in.Q, ParseUserFilter(filter), limit, offset)
	if err != nil {
		return nil, o.internal(err)
	}
	return &indexHits{
		EstimatedTotalHits: len(hits),
		Hits:               hits,
		Limit:              limit,
		Offset:             offset,
		ProcessingTimeMs:   time.Since(start).Milliseconds(),
		Query:              in.Q,
	}, nil
}

// listDocuments pages through the documents in an index.
//
// Answers the org's stored documents in insertion order, whole, with the page's
// bounds and the index's total. It is the enumeration surface — search ranks by
// relevance and cannot walk a corpus — so a caller reconciling what it has
// written reads it here.
//
// An index this org does not hold answers 404 carrying the dialect's
// `index_not_found`.
func (o ops) listDocuments(ctx context.Context, in *indexPage) (*indexDocuments, error) {
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	if err := o.exists(ctx, org, uid); err != nil {
		return nil, err
	}
	limit := boundedInt(in.Limit, defaultLimit)
	offset := boundedInt(in.Offset, 0)
	docs, total, err := o.s.State.store.Documents(ctx, org, uid, limit, offset)
	if err != nil {
		return nil, o.internal(err)
	}
	return &indexDocuments{Limit: limit, Offset: offset, Results: docs, Total: total}, nil
}

// getDocument reads one document by its primary key.
//
// Answers the stored document exactly as it was written — this surface keeps
// documents whole rather than projecting them, so what comes back is what went
// in. A primary key this index does not hold answers 404 carrying the dialect's
// `document_not_found`; an index this org does not hold answers
// `index_not_found`, and the two are different facts a client acts on
// differently.
func (o ops) getDocument(ctx context.Context, in *indexDoc) (*json.RawMessage, error) {
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	if err := o.exists(ctx, org, uid); err != nil {
		return nil, err
	}
	doc, err := o.s.State.store.Document(ctx, org, uid, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, refuse(http.StatusNotFound, "document_not_found",
			"Document `"+in.ID+"` not found.", "invalid_request")
	}
	if err != nil {
		return nil, o.internal(err)
	}
	return &doc, nil
}

// deleteDocument deletes one document by its primary key.
//
// Removes the document from the caller's own org and answers the dialect's
// EnqueuedTask. Deleting a key that is not there succeeds, so a client
// reconciling its own corpus can delete without checking first.
//
// The 202 and its `enqueued` task are DIALECT COMPATIBILITY, not a promise of
// later work: the document is already gone when this answers.
func (o ops) deleteDocument(ctx context.Context, in *indexDoc) (*indexEnqueued, error) {
	org, uid, err := o.at(ctx, in.UID)
	if err != nil {
		return nil, err
	}
	if err := o.s.State.store.Delete(ctx, org, uid, []string{in.ID}); err != nil {
		return nil, o.internal(err)
	}
	return o.accepted(uid, "documentDeletion"), nil
}

// getTask checks a write task, which has already finished.
//
// Always reports `succeeded`. Writes here are applied to SQLite before their
// EnqueuedTask is returned, so a client polling waitForTask resolves on its
// first call rather than waiting for a queue that was never there. The three
// timestamps are the same instant for the same reason.
//
// It requires a validated principal but reads no tenant data: the task id it
// echoes was minted by this process and names nothing about any org.
func (o ops) getTask(ctx context.Context, in *indexTaskAt) (*indexTask, error) {
	if _, err := o.org(ctx); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return &indexTask{
		EnqueuedAt: now, FinishedAt: now, StartedAt: now,
		Status: "succeeded", Type: "documentAdditionOrUpdate", UID: in.UID,
	}, nil
}

// view is the ONE reading of a stored Index into the published shape, so a
// listing and a detail read cannot disagree about what an index looks like.
func view(i Index) indexView {
	return indexView{
		CreatedAt:  i.CreatedAt,
		PrimaryKey: i.PrimaryKey,
		UID:        i.UID,
		UpdatedAt:  i.UpdatedAt,
	}
}

// fail is the untyped writes' spelling of ops.internal: the same log line and
// the same dialect body, written to the response because a raw handler has one.
// ONE reading of a store failure, two ways of delivering it.
func (o ops) fail(c *zip.Ctx, err error) error {
	f := o.internal(err)
	return c.JSON(f.status, f.body)
}
