package o11y

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// annApp mounts the native annotation-queue routes over a temp-dir SQLite store,
// exactly as mountAnnotationQueues registers them, so the HTTP contract + tenant
// isolation are exercised end-to-end without the rest of the o11y mount.
func annApp(t *testing.T) *zip.App {
	t.Helper()
	store, err := openAnnStore(t.TempDir() + "/o11y_annotations.db")
	if err != nil {
		t.Fatalf("openAnnStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &annService{store: store, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/o11y/annotation-queues", s.listQueues)
	app.Post("/v1/o11y/annotation-queues", s.createQueue)
	app.Get("/v1/o11y/annotation-queues/:id", s.getQueue)
	app.Patch("/v1/o11y/annotation-queues/:id", s.updateQueue)
	app.Delete("/v1/o11y/annotation-queues/:id", s.deleteQueue)
	app.Get("/v1/o11y/annotation-queues/:id/items", s.listItems)
	app.Post("/v1/o11y/annotation-queues/:id/items", s.addItems)
	app.Patch("/v1/o11y/annotation-queues/:id/items/:itemId", s.updateItem)
	return app
}

func annReq(method, path, org, project string, body any) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org)
	}
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	return req
}

type qListEnvelope struct {
	Data []annQueueView `json:"data"`
	Meta listMeta       `json:"meta"`
}

type iListEnvelope struct {
	Data []annItemView `json:"data"`
	Meta listMeta      `json:"meta"`
}

func createQueue(t *testing.T, app *zip.App, org, project, name string) annQueueView {
	t.Helper()
	code, body := do(t, app, annReq(http.MethodPost, "/v1/o11y/annotation-queues", org, project,
		map[string]any{"name": name, "scoreConfigIds": []string{"quality"}}))
	if code != http.StatusCreated {
		t.Fatalf("create queue %q: want 201, got %d (%s)", name, code, body)
	}
	var q annQueueView
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("unmarshal queue: %v", err)
	}
	return q
}

// TestAnnotationQueueLifecycle exercises the full workflow: create → list → detail →
// add items → list items → complete an item → counts update → delete → 404.
func TestAnnotationQueueLifecycle(t *testing.T) {
	app := annApp(t)

	q := createQueue(t, app, "o", "", "review-q")
	if q.ID == "" || q.Name != "review-q" {
		t.Fatalf("created queue = %+v", q)
	}
	if q.ScoreConfigIDs == nil || len(q.ScoreConfigIDs) != 1 || q.ScoreConfigIDs[0] != "quality" {
		t.Fatalf("scoreConfigIds must echo (non-null): %+v", q.ScoreConfigIDs)
	}

	// List returns the REST envelope {data, meta}.
	code, body := do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues", "o", "", nil))
	var list qListEnvelope
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list.Data) != 1 || list.Meta.TotalItems != 1 || list.Meta.Page != 1 || list.Meta.TotalPages != 1 {
		t.Fatalf("list envelope = %+v", list)
	}

	// Add two items: a trace + an observation.
	code, body = do(t, app, annReq(http.MethodPost, "/v1/o11y/annotation-queues/"+q.ID+"/items", "o", "",
		map[string]any{"items": []map[string]any{
			{"traceId": "trace-1"},
			{"observationId": "obs-9", "assignee": "reviewer@o"},
		}}))
	if code != http.StatusCreated {
		t.Fatalf("add items: want 201, got %d (%s)", code, body)
	}

	// Detail shows counts + embedded items, with the object mapped to traceId/observationId.
	code, body = do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues/"+q.ID, "o", "", nil))
	var detail annQueueDetailView
	if code != http.StatusOK {
		t.Fatalf("detail: %d %s", code, body)
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if detail.PendingCount != 2 || detail.CompletedCount != 0 || len(detail.Items) != 2 {
		t.Fatalf("detail counts/items = %+v", detail)
	}
	var traceItem annItemView
	for _, it := range detail.Items {
		if it.ObjectType == objectTrace {
			traceItem = it
		}
	}
	if traceItem.TraceID != "trace-1" || traceItem.Status != statusPending {
		t.Fatalf("trace item = %+v", traceItem)
	}

	// List items → envelope with both.
	code, body = do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues/"+q.ID+"/items", "o", "", nil))
	var items iListEnvelope
	_ = json.Unmarshal(body, &items)
	if code != http.StatusOK || items.Meta.TotalItems != 2 || len(items.Data) != 2 {
		t.Fatalf("list items = %d %+v", code, items)
	}

	// Complete the trace item.
	code, body = do(t, app, annReq(http.MethodPatch, "/v1/o11y/annotation-queues/"+q.ID+"/items/"+traceItem.ID, "o", "",
		map[string]any{"status": "COMPLETED", "assignee": "reviewer@o"}))
	if code != http.StatusOK {
		t.Fatalf("complete item: %d %s", code, body)
	}
	var completed annItemView
	_ = json.Unmarshal(body, &completed)
	if completed.Status != statusCompleted || completed.CompletedAt == "" {
		t.Fatalf("completed item = %+v", completed)
	}

	// Counts reflect the completion.
	code, body = do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues/"+q.ID, "o", "", nil))
	_ = json.Unmarshal(body, &detail)
	if detail.PendingCount != 1 || detail.CompletedCount != 1 {
		t.Fatalf("post-complete counts = pending %d completed %d", detail.PendingCount, detail.CompletedCount)
	}

	// A status filter narrows the item list.
	code, body = do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues/"+q.ID+"/items?status=COMPLETED", "o", "", nil))
	_ = json.Unmarshal(body, &items)
	if code != http.StatusOK || items.Meta.TotalItems != 1 {
		t.Fatalf("completed filter = %d %+v", code, items)
	}

	// Delete → 200; then detail 404.
	if code, _ := do(t, app, annReq(http.MethodDelete, "/v1/o11y/annotation-queues/"+q.ID, "o", "", nil)); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues/"+q.ID, "o", "", nil)); code != http.StatusNotFound {
		t.Fatalf("get deleted queue: want 404, got %d", code)
	}
}

// TestAnnotationQueueOrgIsolation: org is the hard tenant boundary — a sibling org
// never sees, reads, mutates, or adds to another org's queue.
func TestAnnotationQueueOrgIsolation(t *testing.T) {
	app := annApp(t)
	q := createQueue(t, app, "owner", "", "secret-q")

	// Sibling org lists zero.
	code, body := do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues", "intruder", "", nil))
	var list qListEnvelope
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Data) != 0 {
		t.Fatalf("intruder list must be empty, got %d %+v", code, list.Data)
	}

	// Sibling org: detail, add-items, patch, delete of the owner's queue → 404.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/o11y/annotation-queues/" + q.ID, nil},
		{http.MethodPost, "/v1/o11y/annotation-queues/" + q.ID + "/items", map[string]any{"items": []map[string]any{{"traceId": "x"}}}},
		{http.MethodGet, "/v1/o11y/annotation-queues/" + q.ID + "/items", nil},
		{http.MethodPatch, "/v1/o11y/annotation-queues/" + q.ID, map[string]any{"name": "hijack"}},
		{http.MethodDelete, "/v1/o11y/annotation-queues/" + q.ID, nil},
	} {
		if code, _ := do(t, app, annReq(tc.method, tc.path, "intruder", "", tc.body)); code != http.StatusNotFound {
			t.Fatalf("intruder %s %s: want 404, got %d", tc.method, tc.path, code)
		}
	}

	// The owner's queue survives every cross-tenant attempt.
	if code, _ := do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues/"+q.ID, "owner", "", nil)); code != http.StatusOK {
		t.Fatalf("owner queue must survive, got %d", code)
	}
}

// TestAnnotationQueueProjectIsolation: a named project sees only its own queues; the
// default project (no header) sees the whole org.
func TestAnnotationQueueProjectIsolation(t *testing.T) {
	app := annApp(t)
	createQueue(t, app, "o", "alpha", "qa-alpha")
	createQueue(t, app, "o", "beta", "qa-beta")
	createQueue(t, app, "o", "", "qa-default")

	check := func(project string, want int) {
		t.Helper()
		code, body := do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues", "o", project, nil))
		var list qListEnvelope
		_ = json.Unmarshal(body, &list)
		if code != http.StatusOK || len(list.Data) != want {
			t.Fatalf("project %q: want %d queues, got %d %+v", project, want, len(list.Data), list.Data)
		}
	}
	check("alpha", 1) // only qa-alpha
	check("beta", 1)  // only qa-beta
	check("", 3)      // default project == whole org
}

// TestAnnotationQueueValidation: the boundary rejects a blank name, a duplicate
// name in the same project, a bad objectType, an objectless item, and a bad status.
func TestAnnotationQueueValidation(t *testing.T) {
	app := annApp(t)

	if code, _ := do(t, app, annReq(http.MethodPost, "/v1/o11y/annotation-queues", "o", "",
		map[string]any{"name": "  "})); code != http.StatusBadRequest {
		t.Fatalf("blank name: want 400, got %d", code)
	}

	createQueue(t, app, "o", "", "dupe")
	if code, _ := do(t, app, annReq(http.MethodPost, "/v1/o11y/annotation-queues", "o", "",
		map[string]any{"name": "dupe"})); code != http.StatusConflict {
		t.Fatalf("duplicate name: want 409, got %d", code)
	}

	q := createQueue(t, app, "o", "", "valid-ops")
	if code, _ := do(t, app, annReq(http.MethodPost, "/v1/o11y/annotation-queues/"+q.ID+"/items", "o", "",
		map[string]any{"items": []map[string]any{{"objectType": "BOGUS", "objectId": "x"}}})); code != http.StatusBadRequest {
		t.Fatalf("bad objectType: want 400, got %d", code)
	}
	if code, _ := do(t, app, annReq(http.MethodPost, "/v1/o11y/annotation-queues/"+q.ID+"/items", "o", "",
		map[string]any{"items": []map[string]any{{"assignee": "nobody"}}})); code != http.StatusBadRequest {
		t.Fatalf("objectless item: want 400, got %d", code)
	}

	// Add a real item, then a bad status update.
	_, _ = do(t, app, annReq(http.MethodPost, "/v1/o11y/annotation-queues/"+q.ID+"/items", "o", "",
		map[string]any{"items": []map[string]any{{"traceId": "t1"}}}))
	_, body := do(t, app, annReq(http.MethodGet, "/v1/o11y/annotation-queues/"+q.ID, "o", "", nil))
	var detail annQueueDetailView
	_ = json.Unmarshal(body, &detail)
	if len(detail.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(detail.Items))
	}
	if code, _ := do(t, app, annReq(http.MethodPatch, "/v1/o11y/annotation-queues/"+q.ID+"/items/"+detail.Items[0].ID, "o", "",
		map[string]any{"status": "MAYBE"})); code != http.StatusBadRequest {
		t.Fatalf("bad status: want 400, got %d", code)
	}
}

// TestAnnotationQueueRequiresPrincipal: a forged request (X-Org-Id, no validated
// X-User-Id) is refused on every route.
func TestAnnotationQueueRequiresPrincipal(t *testing.T) {
	app := annApp(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/o11y/annotation-queues"},
		{http.MethodPost, "/v1/o11y/annotation-queues"},
		{http.MethodGet, "/v1/o11y/annotation-queues/x"},
		{http.MethodPost, "/v1/o11y/annotation-queues/x/items"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-Org-Id", "victim") // forged, no X-User-Id
		if code, _ := do(t, app, req); code != http.StatusForbidden {
			t.Fatalf("%s %s: want 403 for forged (no X-User-Id), got %d", tc.method, tc.path, code)
		}
	}
}

func TestTotalPages(t *testing.T) {
	cases := []struct{ total, limit, want int }{
		{0, 20, 1}, {1, 20, 1}, {20, 20, 1}, {21, 20, 2}, {40, 20, 2}, {41, 20, 3},
	}
	for _, tc := range cases {
		if got := totalPages(tc.total, tc.limit); got != tc.want {
			t.Fatalf("totalPages(%d,%d) = %d, want %d", tc.total, tc.limit, got, tc.want)
		}
	}
}
