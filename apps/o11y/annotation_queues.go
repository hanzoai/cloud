package o11y

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The native ANNOTATION QUEUES surface — human-review queues on the /v1/o11y
// surface the console's AnnotationQueuesModule consumes. The o11y span plane has
// flat annotations but no queue entity, so this is a cloud-native relational
// feature (annotation_store.go) registered BEFORE the hanzoai/o11y wildcard
// (inside MountO11y, order 69) so Fiber's in-order match gives it precedence.
//
//	GET    /v1/o11y/reviews              list queues (org+project scoped)
//	POST   /v1/o11y/reviews              create a queue
//	GET    /v1/o11y/reviews/:id          queue detail (+ counts + items)
//	PATCH  /v1/o11y/reviews/:id          update name/description/scoreConfigIds
//	DELETE /v1/o11y/reviews/:id          delete a queue (+ its items)
//	GET    /v1/o11y/reviews/:id/items    list items (status filter, paged)
//	POST   /v1/o11y/reviews/:id/items    add items (traces/observations/sessions)
//	PATCH  /v1/o11y/reviews/:id/items/:itemId  update item status/assignee
//
// Lists return the console REST envelope {data:[…], meta:{page,limit,totalItems,
// totalPages}}. Every route is a TYPED op, so tenant isolation is tenantOf (the
// validated org, off the context cloud.Bridge parked it on — never an In field)
// on EVERY handler, and callerProject narrows within the org. Both live in
// typed.go. A cross-org id is a 404, never a cross-tenant read.

const (
	statusPending   = "PENDING"
	statusCompleted = "COMPLETED"

	objectTrace       = "TRACE"
	objectObservation = "OBSERVATION"
	objectSession     = "SESSION"

	annDefaultLimit  = 20
	annMaxLimit      = 100
	annDetailItems   = 100 // items embedded in a queue-detail response
	maxScoreConfigs  = 64
	maxAnnItemsBatch = 200 // items one POST may enqueue
	maxAnnFieldLen   = 512
)

// annQueueNameRE constrains a queue name (display handle): printable, bounded
// (no control chars, 1–128 runes).
var annQueueNameRE = regexp.MustCompile(`^[\P{Cc}]{1,128}$`)

// validObjectType is the closed set an item may reference.
var validObjectType = map[string]bool{objectTrace: true, objectObservation: true, objectSession: true}

// annService owns the annotation-queue store + logger. Package-scoped so
// ShutdownO11y can close the store; nil when the mount is skipped.
type annService struct {
	store *annStore
	log   luxlog.Logger
}

var annQueues *annService

// mountAnnotationQueues opens the queue metastore and registers the routes. Called
// by MountO11y inside the one order-69 mount, so every route precedes the order-70
// wildcard. A store-open failure fails the mount (a broken data plane must not
// silently serve empty queues).
func mountAnnotationQueues(a cloud.Router, deps cloud.Deps) error {
	if deps.DataDir == "" {
		return fmt.Errorf("o11y.mountAnnotationQueues: empty DataDir")
	}
	store, err := openAnnStore(filepath.Join(deps.DataDir, "o11y_annotations.db"))
	if err != nil {
		return fmt.Errorf("o11y.mountAnnotationQueues: open store: %w", err)
	}
	log := deps.Logger.New("subsystem", "o11y-reviews")
	s := &annService{store: store, log: log}
	annQueues = s

	// TYPED ops on the group: the op's path is the prefix composed with its leaf,
	// which is the identity every projection (document, MCP tool, CLI command, SDK
	// method) keys on, and cmd/zipdoc resolves it the same way so the doc comments
	// below reach all of them. Static collection routes register before the :id
	// param routes so an id can never shadow a collection route (the eval
	// discipline).
	g := a.Group(o11yPrefix)
	zip.Get(g, "/reviews", s.listQueues)
	zip.Post(g, "/reviews", s.createQueue, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/reviews/:id", s.getQueue)
	zip.Patch(g, "/reviews/:id", s.updateQueue)
	zip.Delete(g, "/reviews/:id", s.deleteQueue)
	zip.Get(g, "/reviews/:id/items", s.listItems)
	zip.Post(g, "/reviews/:id/items", s.addItems, zip.WithStatus(http.StatusCreated))
	zip.Patch(g, "/reviews/:id/items/:itemId", s.updateItem)

	log.Info("o11y reviews surface mounted (native)")
	return nil
}

// shutdownAnnotationQueues closes the queue metastore. Idempotent, nil-safe.
func shutdownAnnotationQueues() error {
	if annQueues == nil {
		return nil
	}
	err := annQueues.store.Close()
	annQueues = nil
	return err
}

// ── views + envelope ──────────────────────────────────────────────────────────

// NOTHING here EMBEDS. Go's encoding/json flattens an embedded struct, but zip's
// schema builder walks reflect fields and skips the ones it cannot name, so an
// embedded shape is published as a schema MISSING every field it carries — a
// document that lies about the wire, and a generated SDK type with holes in it.
// Written flat, the schema and the bytes agree.

type annQueueView struct {
	// ID is the queue's id.
	ID string `json:"id"`
	// Name is its display handle.
	Name string `json:"name"`
	// Description is its free text, omitted when empty.
	Description string `json:"description,omitempty"`
	// ScoreConfigIDs are the eval score-configs reviewers grade against.
	ScoreConfigIDs []string `json:"scoreConfigIds"`
	// CreatedAt is when it was created, RFC3339 in UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when it last changed, RFC3339 in UTC.
	UpdatedAt string `json:"updatedAt"`
}

// annQueueDetailView is one queue plus its review progress. It repeats
// annQueueView's fields IN ORDER rather than embedding it — see the note above —
// so the bytes are what they always were and the schema says so.
type annQueueDetailView struct {
	// ID is the queue's id.
	ID string `json:"id"`
	// Name is its display handle.
	Name string `json:"name"`
	// Description is its free text, omitted when empty.
	Description string `json:"description,omitempty"`
	// ScoreConfigIDs are the eval score-configs reviewers grade against.
	ScoreConfigIDs []string `json:"scoreConfigIds"`
	// CreatedAt is when it was created, RFC3339 in UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when it last changed, RFC3339 in UTC.
	UpdatedAt string `json:"updatedAt"`
	// PendingCount is how many of its items are still awaiting review.
	PendingCount int `json:"pendingCount"`
	// CompletedCount is how many have been reviewed.
	CompletedCount int `json:"completedCount"`
	// Items is the queue's first page of items (up to 100).
	Items []annItemView `json:"items"`
}

type annItemView struct {
	// ID is the item's id.
	ID string `json:"id"`
	// QueueID is the queue it belongs to.
	QueueID string `json:"queueId"`
	// ObjectType is what it references: TRACE, OBSERVATION or SESSION.
	ObjectType string `json:"objectType"`
	// ObjectID is the referenced object's id.
	ObjectID string `json:"objectId"`
	// TraceID echoes objectId when objectType is TRACE.
	TraceID string `json:"traceId,omitempty"`
	// ObservationID echoes objectId when objectType is OBSERVATION.
	ObservationID string `json:"observationId,omitempty"`
	// SessionID echoes objectId when objectType is SESSION.
	SessionID string `json:"sessionId,omitempty"`
	// Status is PENDING or COMPLETED.
	Status string `json:"status"`
	// Assignee is the reviewer it is for, omitted when unassigned.
	Assignee string `json:"assignee,omitempty"`
	// CreatedAt is when it was enqueued, RFC3339 in UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when it last changed, RFC3339 in UTC.
	UpdatedAt string `json:"updatedAt"`
	// CompletedAt is when it was reviewed, omitted while pending.
	CompletedAt string `json:"completedAt,omitempty"`
}

// toQueueDetailView projects one queue + its progress into the detail response,
// so the flat repetition of annQueueView's fields is written in exactly one
// place.
func toQueueDetailView(q annQueue, pending, completed int, items []annItemView) annQueueDetailView {
	v := toQueueView(q)
	return annQueueDetailView{
		ID: v.ID, Name: v.Name, Description: v.Description, ScoreConfigIDs: v.ScoreConfigIDs,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		PendingCount: pending, CompletedCount: completed, Items: items,
	}
}

func toQueueView(q annQueue) annQueueView {
	ids := q.ScoreConfigIDs
	if ids == nil {
		ids = []string{} // the console reads scoreConfigIds: string[] — never null
	}
	return annQueueView{
		ID: q.ID, Name: q.Name, Description: q.Description, ScoreConfigIDs: ids,
		CreatedAt: rfc3339Unix(q.CreatedAt), UpdatedAt: rfc3339Unix(q.UpdatedAt),
	}
}

func toItemView(it annItem) annItemView {
	v := annItemView{
		ID: it.ID, QueueID: it.QueueID, ObjectType: it.ObjectType, ObjectID: it.ObjectID,
		Status: it.Status, Assignee: it.Assignee,
		CreatedAt: rfc3339Unix(it.CreatedAt), UpdatedAt: rfc3339Unix(it.UpdatedAt),
	}
	switch it.ObjectType {
	case objectTrace:
		v.TraceID = it.ObjectID
	case objectObservation:
		v.ObservationID = it.ObjectID
	case objectSession:
		v.SessionID = it.ObjectID
	}
	if it.CompletedAt > 0 {
		v.CompletedAt = rfc3339Unix(it.CompletedAt)
	}
	return v
}

type listMeta struct {
	// Page is the 1-based page this response is.
	Page int `json:"page"`
	// Limit is how many rows one page holds.
	Limit int `json:"limit"`
	// TotalItems is how many rows match in total.
	TotalItems int `json:"totalItems"`
	// TotalPages is ceil(totalItems/limit), at least 1.
	TotalPages int `json:"totalPages"`
}

func newListMeta(page, limit, total int) listMeta {
	return listMeta{Page: page, Limit: limit, TotalItems: total, TotalPages: totalPages(total, limit)}
}

// ── the typed shapes (the published contract) ─────────────────────────────────

// annPage is the paging a list route reads off the query string.
type annPage struct {
	// Page is the 1-based page to read. Default 1.
	Page int `json:"page"`
	// Limit is how many rows to return. Default 20, capped at 100.
	Limit int `json:"limit"`
}

// annQueueRef addresses one queue. The id is the path segment: the URL is the
// addressing authority, so it binds from there whatever a body says.
type annQueueRef struct {
	// ID is the annotation queue to act on, from the path.
	ID string `json:"id"`
}

// annQueueList is a page of the caller org+project's annotation queues, in the
// console REST envelope.
type annQueueList struct {
	// Data is the page of queues.
	Data []annQueueView `json:"data"`
	// Meta is the paging that produced it.
	Meta listMeta `json:"meta"`
}

// annItemList is a page of one queue's items, in the console REST envelope.
type annItemList struct {
	// Data is the page of items.
	Data []annItemView `json:"data"`
	// Meta is the paging that produced it.
	Meta listMeta `json:"meta"`
}

// annQueueDeleted acknowledges a queue deletion.
type annQueueDeleted struct {
	// Deleted is true when the queue (and its items) were removed.
	Deleted bool `json:"deleted"`
}

// ── handlers ──────────────────────────────────────────────────────────────────

// ListAnnotationQueues returns a page of the caller org's human-review queues,
// newest first, narrowed to the caller's project. Another org's queues are never
// visible.
//
// Example: {"page": 1, "limit": 20}
func (s *annService) listQueues(ctx context.Context, in *annPage) (*annQueueList, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	page, limit := pageLimit(in.Page, in.Limit)
	rows, total, err := s.store.ListQueues(ctx, org, callerProject(ctx), limit, (page-1)*limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list queues: %v", err)
	}
	out := make([]annQueueView, 0, len(rows))
	for _, q := range rows {
		out = append(out, toQueueView(q))
	}
	return &annQueueList{Data: out, Meta: newListMeta(page, limit, total)}, nil
}

type createQueueReq struct {
	// Name is the queue's display handle, 1–128 printable characters. It must be
	// unique within the org's project. Required.
	Name string `json:"name"`
	// Description is optional free text, up to 512 characters.
	Description string `json:"description"`
	// ScoreConfigIDs are the eval score-configs reviewers grade against.
	ScoreConfigIDs []string `json:"scoreConfigIds"`
}

// CreateAnnotationQueue creates a human-review queue in the caller's org and
// project. A name already used by another queue in the same project is a 409.
//
// Example: {"name": "hallucination review", "scoreConfigIds": ["quality"]}
func (s *annService) createQueue(ctx context.Context, in *createQueueReq) (*annQueueView, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	body := *in
	name := strings.TrimSpace(body.Name)
	if !annQueueNameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name is required (1–128 printable chars)")
	}
	if len(body.Description) > maxAnnFieldLen {
		return nil, zip.ErrBadRequest("description too long")
	}
	ids, err := cleanScoreConfigIDs(body.ScoreConfigIDs)
	if err != nil {
		return nil, err
	}
	id, err := genID("annq")
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	q, err := s.store.CreateQueue(ctx, annQueue{
		ID: id, Org: org, Project: callerProject(ctx), Name: name,
		Description: strings.TrimSpace(body.Description), ScoreConfigIDs: ids,
		CreatedAt: now, UpdatedAt: now,
	})
	if err == errQueueConflict {
		return nil, zip.Errorf(http.StatusConflict, "a queue named %q already exists in this project", name)
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create queue: %v", err)
	}
	v := toQueueView(q)
	return &v, nil
}

// GetAnnotationQueue returns one review queue with its pending and completed
// counts and its first page of items. A queue id belonging to another org is a
// 404, never a cross-tenant read.
//
// Example: {"id": "annq_1"}
func (s *annService) getQueue(ctx context.Context, in *annQueueRef) (*annQueueDetailView, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	q, err := s.store.GetQueue(ctx, org, id)
	if err == errQueueNotFound {
		return nil, zip.ErrNotFound("annotation queue not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	pending, completed, err := s.store.QueueCounts(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "queue counts: %v", err)
	}
	items, _, err := s.store.ListItems(ctx, org, id, "", annDetailItems, 0)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "queue items: %v", err)
	}
	iv := make([]annItemView, 0, len(items))
	for _, it := range items {
		iv = append(iv, toItemView(it))
	}
	v := toQueueDetailView(q, pending, completed, iv)
	return &v, nil
}

// updateQueueIn is a partial update. Every field is optional — a field the
// request omits is left alone — and the id comes from the path.
type updateQueueIn struct {
	// ID is the annotation queue to update, from the path.
	ID string `json:"id"`
	// Name replaces the queue's display handle when present, 1–128 printable
	// characters and unique within the project.
	Name *string `json:"name"`
	// Description replaces the free text when present, up to 512 characters.
	Description *string `json:"description"`
	// ScoreConfigIDs replaces the whole score-config set when present.
	ScoreConfigIDs *[]string `json:"scoreConfigIds"`
}

// UpdateAnnotationQueue changes a review queue's name, description or
// score-config set. A field the request omits is left alone. A name another
// queue in the same project already uses is a 409; a queue id belonging to
// another org is a 404.
//
// Example: {"id": "annq_1", "name": "hallucination review v2"}
func (s *annService) updateQueue(ctx context.Context, in *updateQueueIn) (*annQueueView, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	body := in
	cur, err := s.store.GetQueue(ctx, org, id)
	if err == errQueueNotFound {
		return nil, zip.ErrNotFound("annotation queue not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if !annQueueNameRE.MatchString(name) {
			return nil, zip.ErrBadRequest("name must be 1–128 printable chars")
		}
		cur.Name = name
	}
	if body.Description != nil {
		if len(*body.Description) > maxAnnFieldLen {
			return nil, zip.ErrBadRequest("description too long")
		}
		cur.Description = strings.TrimSpace(*body.Description)
	}
	if body.ScoreConfigIDs != nil {
		ids, err := cleanScoreConfigIDs(*body.ScoreConfigIDs)
		if err != nil {
			return nil, err
		}
		cur.ScoreConfigIDs = ids
	}
	cur.UpdatedAt = time.Now().Unix()
	q, err := s.store.UpdateQueue(ctx, cur)
	if err == errQueueConflict {
		return nil, zip.Errorf(http.StatusConflict, "a queue named %q already exists in this project", cur.Name)
	}
	if err == errQueueNotFound {
		return nil, zip.ErrNotFound("annotation queue not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update queue: %v", err)
	}
	v := toQueueView(q)
	return &v, nil
}

// DeleteAnnotationQueue removes one review queue and every item in it. A queue
// id belonging to another org answers the same 404 an unknown id does, so a
// probe learns nothing about what exists.
//
// Example: {"id": "annq_1"}
func (s *annService) deleteQueue(ctx context.Context, in *annQueueRef) (*annQueueDeleted, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	existed, err := s.store.DeleteQueue(ctx, org, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete queue: %v", err)
	}
	if !existed {
		return nil, zip.ErrNotFound("annotation queue not found")
	}
	return &annQueueDeleted{Deleted: true}, nil
}

// listItemsIn is a page of one queue's items, optionally filtered by status.
type listItemsIn struct {
	// ID is the annotation queue whose items to list, from the path.
	ID string `json:"id"`
	// Status filters to PENDING or COMPLETED items. Absent returns both.
	Status string `json:"status"`
	// Page is the 1-based page to read. Default 1.
	Page int `json:"page"`
	// Limit is how many rows to return. Default 20, capped at 100.
	Limit int `json:"limit"`
}

// ListAnnotationQueueItems returns a page of one review queue's items, newest
// first, optionally filtered to PENDING or COMPLETED. A queue id belonging to
// another org is a 404, never a cross-tenant list.
//
// Example: {"id": "annq_1", "status": "PENDING"}
func (s *annService) listItems(ctx context.Context, in *listItemsIn) (*annItemList, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	// The queue must exist in THIS org (a real 404, never a cross-tenant list).
	if _, err := s.store.GetQueue(ctx, org, id); err == errQueueNotFound {
		return nil, zip.ErrNotFound("annotation queue not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	status, err := parseStatusFilter(in.Status)
	if err != nil {
		return nil, err
	}
	page, limit := pageLimit(in.Page, in.Limit)
	rows, total, err := s.store.ListItems(ctx, org, id, status, limit, (page-1)*limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list items: %v", err)
	}
	out := make([]annItemView, 0, len(rows))
	for _, it := range rows {
		out = append(out, toItemView(it))
	}
	return &annItemList{Data: out, Meta: newListMeta(page, limit, total)}, nil
}

type itemInput struct {
	// ObjectType is TRACE, OBSERVATION or SESSION — the generic form, paired
	// with objectId.
	ObjectType string `json:"objectType"`
	// ObjectID is the referenced object's id, paired with objectType.
	ObjectID string `json:"objectId"`
	// TraceID references a trace — the console-friendly form of
	// objectType=TRACE.
	TraceID string `json:"traceId"`
	// ObservationID references an observation — the console-friendly form of
	// objectType=OBSERVATION.
	ObservationID string `json:"observationId"`
	// SessionID references a session — the console-friendly form of
	// objectType=SESSION.
	SessionID string `json:"sessionId"`
	// Assignee is the reviewer this item is for, up to 512 characters.
	Assignee string `json:"assignee"`
}

// addItemsIn enqueues items on one queue. The queue id comes from the path.
type addItemsIn struct {
	// ID is the annotation queue to add to, from the path.
	ID string `json:"id"`
	// Items are the objects to enqueue for review, 1–200 per request. Each names
	// exactly one object.
	Items []itemInput `json:"items"`
}

// annItemsCreated is the batch of items one add enqueued.
type annItemsCreated struct {
	// Data is every item created by this request, in request order.
	Data []annItemView `json:"data"`
}

// AddAnnotationQueueItems enqueues traces, observations or sessions on a review
// queue. Each item names exactly one object, either by traceId / observationId /
// sessionId or by objectType plus objectId; every item enters PENDING. A queue
// id belonging to another org is a 404.
//
// Example: {"id": "annq_1", "items": [{"traceId": "tr_1"}]}
func (s *annService) addItems(ctx context.Context, in *addItemsIn) (*annItemsCreated, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	q, err := s.store.GetQueue(ctx, org, id)
	if err == errQueueNotFound {
		return nil, zip.ErrNotFound("annotation queue not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	if len(in.Items) == 0 {
		return nil, zip.ErrBadRequest("items is required (1 or more)")
	}
	if len(in.Items) > maxAnnItemsBatch {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "too many items (max %d)", maxAnnItemsBatch)
	}
	now := time.Now().Unix()
	items := make([]annItem, 0, len(in.Items))
	for _, item := range in.Items {
		objType, objID, err := resolveObject(item)
		if err != nil {
			return nil, err
		}
		if len(item.Assignee) > maxAnnFieldLen {
			return nil, zip.ErrBadRequest("assignee too long")
		}
		itemID, err := genID("annqi")
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}
		items = append(items, annItem{
			ID: itemID, Org: org, Project: q.Project, QueueID: id,
			ObjectType: objType, ObjectID: objID, Status: statusPending,
			Assignee: strings.TrimSpace(item.Assignee), CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := s.store.AddItems(ctx, items); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "add items: %v", err)
	}
	out := make([]annItemView, 0, len(items))
	for _, it := range items {
		out = append(out, toItemView(it))
	}
	return &annItemsCreated{Data: out}, nil
}

// updateItemIn addresses one item of one queue. Both ids come from the path.
type updateItemIn struct {
	// ID is the annotation queue the item belongs to, from the path.
	ID string `json:"id"`
	// ItemID is the item to update, from the path.
	ItemID string `json:"itemId"`
	// Status is the item's new review state: PENDING or COMPLETED. Required.
	Status string `json:"status"`
	// Assignee replaces the reviewer this item is for, up to 512 characters.
	Assignee string `json:"assignee"`
}

// UpdateAnnotationQueueItem moves one queue item between PENDING and COMPLETED
// and sets its assignee. Completing an item stamps its completedAt. An item that
// exists under a different queue answers the same 404 an unknown item does, and
// so does a queue belonging to another org.
//
// Example: {"id": "annq_1", "itemId": "annqi_1", "status": "COMPLETED"}
func (s *annService) updateItem(ctx context.Context, in *updateItemIn) (*annItemView, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	queueID := strings.TrimSpace(in.ID)
	itemID := strings.TrimSpace(in.ItemID)
	// The queue must be owned by this org before its items are mutated.
	if _, err := s.store.GetQueue(ctx, org, queueID); err == errQueueNotFound {
		return nil, zip.ErrNotFound("annotation queue not found")
	} else if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	body := in
	status := strings.ToUpper(strings.TrimSpace(body.Status))
	if status != statusPending && status != statusCompleted {
		return nil, zip.ErrBadRequest("status must be PENDING or COMPLETED")
	}
	if len(body.Assignee) > maxAnnFieldLen {
		return nil, zip.ErrBadRequest("assignee too long")
	}
	now := time.Now().Unix()
	var completedAt int64
	if status == statusCompleted {
		completedAt = now
	}
	it, err := s.store.UpdateItem(ctx, org, itemID, status, strings.TrimSpace(body.Assignee), completedAt, now)
	if err == errItemNotFound {
		return nil, zip.ErrNotFound("annotation queue item not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "update item: %v", err)
	}
	if it.QueueID != queueID {
		// The item exists in this org but under a DIFFERENT queue — treat as not
		// found for this queue rather than reveal it.
		return nil, zip.ErrNotFound("annotation queue item not found")
	}
	v := toItemView(it)
	return &v, nil
}

// ── validation helpers ────────────────────────────────────────────────────────

// resolveObject maps an item input to (objectType, objectId). A traceId /
// observationId / sessionId is the console-friendly form; an explicit
// objectType+objectId is the generic form. Exactly one object must be identified.
func resolveObject(in itemInput) (string, string, error) {
	switch {
	case strings.TrimSpace(in.TraceID) != "":
		return objectTrace, boundedID(in.TraceID), nil
	case strings.TrimSpace(in.ObservationID) != "":
		return objectObservation, boundedID(in.ObservationID), nil
	case strings.TrimSpace(in.SessionID) != "":
		return objectSession, boundedID(in.SessionID), nil
	case strings.TrimSpace(in.ObjectID) != "":
		t := strings.ToUpper(strings.TrimSpace(in.ObjectType))
		if !validObjectType[t] {
			return "", "", zip.ErrBadRequest("objectType must be TRACE, OBSERVATION, or SESSION")
		}
		return t, boundedID(in.ObjectID), nil
	default:
		return "", "", zip.ErrBadRequest("each item needs a traceId, observationId, sessionId, or objectType+objectId")
	}
}

func boundedID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxAnnFieldLen {
		return s[:maxAnnFieldLen]
	}
	return s
}

// cleanScoreConfigIDs trims, drops blanks/dupes, and bounds the set. Each id is a
// bounded token (it references an eval score-config).
func cleanScoreConfigIDs(xs []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x == "" || seen[x] {
			continue
		}
		if len(x) > maxAnnFieldLen {
			return nil, zip.ErrBadRequest("scoreConfigId too long")
		}
		seen[x] = true
		out = append(out, x)
		if len(out) > maxScoreConfigs {
			return nil, zip.Errorf(http.StatusBadRequest, "too many scoreConfigIds (max %d)", maxScoreConfigs)
		}
	}
	return out, nil
}

func parseStatusFilter(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return "", nil
	}
	if s != statusPending && s != statusCompleted {
		return "", zip.ErrBadRequest("status filter must be PENDING or COMPLETED")
	}
	return s, nil
}

// pageLimit bounds the 1-based page + limit the caller asked for. Defaults:
// page 1, limit 20; limit is clamped to [1, annMaxLimit]. A missing or
// unparseable query value arrives as 0 and takes the default — the same branch
// a malformed string took when this parsed the query itself.
func pageLimit(rawPage, rawLimit int) (page, limit int) {
	page = 1
	if rawPage > 1 {
		page = rawPage
	}
	limit = annDefaultLimit
	if rawLimit > 0 {
		limit = rawLimit
	}
	if limit > annMaxLimit {
		limit = annMaxLimit
	}
	return page, limit
}

func rfc3339Unix(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// genID mints a prefixed random id (prefix_<32 hex>), the eval-metastore id shape.
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
