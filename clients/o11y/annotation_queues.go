package o11y

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The native ANNOTATION QUEUES surface — human-review queues on the /v1/o11y
// surface the console's AnnotationQueuesModule consumes. The o11y span plane has
// flat annotations but no queue entity, so this is a cloud-native relational
// feature (annotation_store.go) registered BEFORE the hanzoai/o11y wildcard
// (inside MountO11y, order 69) so Fiber's in-order match gives it precedence.
//
//	GET    /v1/o11y/annotation-queues              list queues (org+project scoped)
//	POST   /v1/o11y/annotation-queues              create a queue
//	GET    /v1/o11y/annotation-queues/:id          queue detail (+ counts + items)
//	PATCH  /v1/o11y/annotation-queues/:id          update name/description/scoreConfigIds
//	DELETE /v1/o11y/annotation-queues/:id          delete a queue (+ its items)
//	GET    /v1/o11y/annotation-queues/:id/items    list items (status filter, paged)
//	POST   /v1/o11y/annotation-queues/:id/items    add items (traces/observations/sessions)
//	PATCH  /v1/o11y/annotation-queues/:id/items/:itemId  update item status/assignee
//
// Lists return the console REST envelope {data:[…], meta:{page,limit,totalItems,
// totalPages}}. Tenant isolation is principal.Org (the validated tenant) on EVERY
// handler; principal.ProjectScope narrows within the org. A cross-org id is a 404,
// never a cross-tenant read.

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
	log := deps.Logger.New("subsystem", "o11y-annotation-queues")
	s := &annService{store: store, log: log}
	annQueues = s

	// Static collection routes register before the :id param routes so an id can
	// never shadow a collection route (the eval discipline).
	a.Get("/v1/o11y/annotation-queues", s.listQueues)
	a.Post("/v1/o11y/annotation-queues", s.createQueue)
	a.Get("/v1/o11y/annotation-queues/:id", s.getQueue)
	a.Patch("/v1/o11y/annotation-queues/:id", s.updateQueue)
	a.Delete("/v1/o11y/annotation-queues/:id", s.deleteQueue)
	a.Get("/v1/o11y/annotation-queues/:id/items", s.listItems)
	a.Post("/v1/o11y/annotation-queues/:id/items", s.addItems)
	a.Patch("/v1/o11y/annotation-queues/:id/items/:itemId", s.updateItem)

	log.Info("o11y annotation-queues surface mounted (native)")
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

type annQueueView struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	ScoreConfigIDs []string `json:"scoreConfigIds"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

type annQueueDetailView struct {
	annQueueView
	PendingCount   int           `json:"pendingCount"`
	CompletedCount int           `json:"completedCount"`
	Items          []annItemView `json:"items"`
}

type annItemView struct {
	ID            string `json:"id"`
	QueueID       string `json:"queueId"`
	ObjectType    string `json:"objectType"`
	ObjectID      string `json:"objectId"`
	TraceID       string `json:"traceId,omitempty"`
	ObservationID string `json:"observationId,omitempty"`
	SessionID     string `json:"sessionId,omitempty"`
	Status        string `json:"status"`
	Assignee      string `json:"assignee,omitempty"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	CompletedAt   string `json:"completedAt,omitempty"`
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
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	TotalItems int `json:"totalItems"`
	TotalPages int `json:"totalPages"`
}

func listEnvelope(data any, page, limit, total int) map[string]any {
	return map[string]any{
		"data": data,
		"meta": listMeta{Page: page, Limit: limit, TotalItems: total, TotalPages: totalPages(total, limit)},
	}
}

// ── handlers ──────────────────────────────────────────────────────────────────

func (s *annService) listQueues(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	page, limit := pageLimit(c)
	rows, total, err := s.store.ListQueues(c.Context(), org, principal.ProjectScope(c), limit, (page-1)*limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list queues: %v", err)
	}
	out := make([]annQueueView, 0, len(rows))
	for _, q := range rows {
		out = append(out, toQueueView(q))
	}
	return c.JSON(http.StatusOK, listEnvelope(out, page, limit, total))
}

type createQueueReq struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	ScoreConfigIDs []string `json:"scoreConfigIds"`
}

func (s *annService) createQueue(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body createQueueReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if !annQueueNameRE.MatchString(name) {
		return zip.ErrBadRequest("name is required (1–128 printable chars)")
	}
	if len(body.Description) > maxAnnFieldLen {
		return zip.ErrBadRequest("description too long")
	}
	ids, err := cleanScoreConfigIDs(body.ScoreConfigIDs)
	if err != nil {
		return err
	}
	id, err := genID("annq")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	q, err := s.store.CreateQueue(c.Context(), annQueue{
		ID: id, Org: org, Project: principal.ProjectScope(c), Name: name,
		Description: strings.TrimSpace(body.Description), ScoreConfigIDs: ids,
		CreatedAt: now, UpdatedAt: now,
	})
	if err == errQueueConflict {
		return zip.Errorf(http.StatusConflict, "a queue named %q already exists in this project", name)
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create queue: %v", err)
	}
	return c.JSON(http.StatusCreated, toQueueView(q))
}

func (s *annService) getQueue(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := strings.TrimSpace(c.Param("id"))
	q, err := s.store.GetQueue(c.Context(), org, id)
	if err == errQueueNotFound {
		return zip.ErrNotFound("annotation queue not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	pending, completed, err := s.store.QueueCounts(c.Context(), org, id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "queue counts: %v", err)
	}
	items, _, err := s.store.ListItems(c.Context(), org, id, "", annDetailItems, 0)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "queue items: %v", err)
	}
	iv := make([]annItemView, 0, len(items))
	for _, it := range items {
		iv = append(iv, toItemView(it))
	}
	return c.JSON(http.StatusOK, annQueueDetailView{
		annQueueView: toQueueView(q), PendingCount: pending, CompletedCount: completed, Items: iv,
	})
}

type updateQueueReq struct {
	Name           *string   `json:"name"`
	Description    *string   `json:"description"`
	ScoreConfigIDs *[]string `json:"scoreConfigIds"`
}

func (s *annService) updateQueue(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := strings.TrimSpace(c.Param("id"))
	var body updateQueueReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	cur, err := s.store.GetQueue(c.Context(), org, id)
	if err == errQueueNotFound {
		return zip.ErrNotFound("annotation queue not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if !annQueueNameRE.MatchString(name) {
			return zip.ErrBadRequest("name must be 1–128 printable chars")
		}
		cur.Name = name
	}
	if body.Description != nil {
		if len(*body.Description) > maxAnnFieldLen {
			return zip.ErrBadRequest("description too long")
		}
		cur.Description = strings.TrimSpace(*body.Description)
	}
	if body.ScoreConfigIDs != nil {
		ids, err := cleanScoreConfigIDs(*body.ScoreConfigIDs)
		if err != nil {
			return err
		}
		cur.ScoreConfigIDs = ids
	}
	cur.UpdatedAt = time.Now().Unix()
	q, err := s.store.UpdateQueue(c.Context(), cur)
	if err == errQueueConflict {
		return zip.Errorf(http.StatusConflict, "a queue named %q already exists in this project", cur.Name)
	}
	if err == errQueueNotFound {
		return zip.ErrNotFound("annotation queue not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update queue: %v", err)
	}
	return c.JSON(http.StatusOK, toQueueView(q))
}

func (s *annService) deleteQueue(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := strings.TrimSpace(c.Param("id"))
	existed, err := s.store.DeleteQueue(c.Context(), org, id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete queue: %v", err)
	}
	if !existed {
		return zip.ErrNotFound("annotation queue not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": true})
}

func (s *annService) listItems(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := strings.TrimSpace(c.Param("id"))
	// The queue must exist in THIS org (a real 404, never a cross-tenant list).
	if _, err := s.store.GetQueue(c.Context(), org, id); err == errQueueNotFound {
		return zip.ErrNotFound("annotation queue not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	status, err := parseStatusFilter(c.Query("status"))
	if err != nil {
		return err
	}
	page, limit := pageLimit(c)
	rows, total, err := s.store.ListItems(c.Context(), org, id, status, limit, (page-1)*limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list items: %v", err)
	}
	out := make([]annItemView, 0, len(rows))
	for _, it := range rows {
		out = append(out, toItemView(it))
	}
	return c.JSON(http.StatusOK, listEnvelope(out, page, limit, total))
}

type itemInput struct {
	ObjectType    string `json:"objectType"`
	ObjectID      string `json:"objectId"`
	TraceID       string `json:"traceId"`
	ObservationID string `json:"observationId"`
	SessionID     string `json:"sessionId"`
	Assignee      string `json:"assignee"`
}

type addItemsReq struct {
	Items []itemInput `json:"items"`
}

func (s *annService) addItems(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := strings.TrimSpace(c.Param("id"))
	q, err := s.store.GetQueue(c.Context(), org, id)
	if err == errQueueNotFound {
		return zip.ErrNotFound("annotation queue not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	var body addItemsReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Items) == 0 {
		return zip.ErrBadRequest("items is required (1 or more)")
	}
	if len(body.Items) > maxAnnItemsBatch {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "too many items (max %d)", maxAnnItemsBatch)
	}
	now := time.Now().Unix()
	items := make([]annItem, 0, len(body.Items))
	for _, in := range body.Items {
		objType, objID, err := resolveObject(in)
		if err != nil {
			return err
		}
		if len(in.Assignee) > maxAnnFieldLen {
			return zip.ErrBadRequest("assignee too long")
		}
		itemID, err := genID("annqi")
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}
		items = append(items, annItem{
			ID: itemID, Org: org, Project: q.Project, QueueID: id,
			ObjectType: objType, ObjectID: objID, Status: statusPending,
			Assignee: strings.TrimSpace(in.Assignee), CreatedAt: now, UpdatedAt: now,
		})
	}
	if err := s.store.AddItems(c.Context(), items); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "add items: %v", err)
	}
	out := make([]annItemView, 0, len(items))
	for _, it := range items {
		out = append(out, toItemView(it))
	}
	return c.JSON(http.StatusCreated, map[string]any{"data": out})
}

type updateItemReq struct {
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
}

func (s *annService) updateItem(c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	queueID := strings.TrimSpace(c.Param("id"))
	itemID := strings.TrimSpace(c.Param("itemId"))
	// The queue must be owned by this org before its items are mutated.
	if _, err := s.store.GetQueue(c.Context(), org, queueID); err == errQueueNotFound {
		return zip.ErrNotFound("annotation queue not found")
	} else if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get queue: %v", err)
	}
	var body updateItemReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	status := strings.ToUpper(strings.TrimSpace(body.Status))
	if status != statusPending && status != statusCompleted {
		return zip.ErrBadRequest("status must be PENDING or COMPLETED")
	}
	if len(body.Assignee) > maxAnnFieldLen {
		return zip.ErrBadRequest("assignee too long")
	}
	now := time.Now().Unix()
	var completedAt int64
	if status == statusCompleted {
		completedAt = now
	}
	it, err := s.store.UpdateItem(c.Context(), org, itemID, status, strings.TrimSpace(body.Assignee), completedAt, now)
	if err == errItemNotFound {
		return zip.ErrNotFound("annotation queue item not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "update item: %v", err)
	}
	if it.QueueID != queueID {
		// The item exists in this org but under a DIFFERENT queue — treat as not
		// found for this queue rather than reveal it.
		return zip.ErrNotFound("annotation queue item not found")
	}
	return c.JSON(http.StatusOK, toItemView(it))
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

// pageLimit resolves the 1-based page + bounded limit from the query. Defaults:
// page 1, limit 20; limit is clamped to [1, annMaxLimit].
func pageLimit(c *zip.Ctx) (page, limit int) {
	page = 1
	if p, err := strconv.Atoi(strings.TrimSpace(c.Query("page"))); err == nil && p > 1 {
		page = p
	}
	limit = annDefaultLimit
	if l, err := strconv.Atoi(strings.TrimSpace(c.Query("limit"))); err == nil && l > 0 {
		limit = l
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
