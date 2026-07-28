package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// ---- store-level: tree linking, seq, tenant isolation ----

func testSessionStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkSession(org, id, parent, root string) Session {
	now := time.Now().Unix()
	return Session{
		ID: id, Org: org, Agent: "dev", Actor: "u", Status: StatusRunning,
		ParentID: parent, RootID: root, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func TestSessionTreeLinkingStore(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	// root -> child -> grandchild, all one org.
	if err := s.CreateSession(ctx, mkSession("acme", "root", "", "root")); err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := s.CreateSession(ctx, mkSession("acme", "child", "root", "root")); err != nil {
		t.Fatalf("child: %v", err)
	}
	if err := s.CreateSession(ctx, mkSession("acme", "gchild", "child", "root")); err != nil {
		t.Fatalf("gchild: %v", err)
	}
	tree, err := s.ListTree(ctx, "acme", "root", 0)
	if err != nil || len(tree) != 3 {
		t.Fatalf("tree want 3 nodes, got %d (%v)", len(tree), err)
	}
	// A parent that does not exist in the org is refused (no dangling tree).
	if err := s.CreateSession(ctx, Session{ID: "x", Org: "acme", ParentID: "nope", RootID: "nope", StartedAt: 1, CreatedAt: 1, UpdatedAt: 1}); err != errParentNotFound {
		t.Fatalf("dangling parent want errParentNotFound, got %v", err)
	}
	// A parent in ANOTHER org is refused (tree can't cross tenants).
	if err := s.CreateSession(ctx, mkSession("evil", "e", "root", "root")); err != errParentNotFound {
		t.Fatalf("cross-tenant parent want errParentNotFound, got %v", err)
	}
	nc, _ := s.CountChildren(ctx, "acme", "root")
	if nc != 1 {
		t.Fatalf("root direct children want 1, got %d", nc)
	}
}

func TestSessionEventSeqAndCounts(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	_ = s.CreateSession(ctx, mkSession("acme", "root", "", "root"))
	_ = s.CreateSession(ctx, mkSession("acme", "child", "root", "root"))
	for i := 0; i < 3; i++ {
		e, err := s.AppendEvent(ctx, Event{ID: genIDMust(t), SessionID: "root", Org: "acme", Kind: KindLog, CreatedAt: time.Now().Unix()})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if e.Seq != int64(i+1) {
			t.Fatalf("seq want %d, got %d", i+1, e.Seq)
		}
	}
	_, _ = s.AppendEvent(ctx, Event{ID: genIDMust(t), SessionID: "child", Org: "acme", Kind: KindSpawn, CreatedAt: time.Now().Unix()})
	counts, err := s.EventCountsByRoot(ctx, "acme", "root")
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["root"] != 3 || counts["child"] != 1 {
		t.Fatalf("event counts want root=3 child=1, got %+v", counts)
	}
	// Cross-tenant read sees nothing.
	other, _ := s.EventCountsByRoot(ctx, "evil", "root")
	if len(other) != 0 {
		t.Fatalf("cross-tenant counts must be empty, got %+v", other)
	}
	if n, _ := s.CountEvents(ctx, "evil", "root"); n != 0 {
		t.Fatalf("cross-tenant event count want 0, got %d", n)
	}
}

// TestSessionEventSeqConcurrent proves the store's per-session Seq is gap-free
// and duplicate-free under CONCURRENT appends — the exact race vector #4. The
// store runs on a single connection (SetMaxOpenConns(1)) so the MAX(seq)+1
// read-then-write is serialised; the UNIQUE(session_id,seq) index is the final
// backstop. N goroutines append to the SAME session in parallel; the returned
// seqs must be EXACTLY {1..N} (no gap = no lost write, no dupe = no double
// allocation), and the persisted count must equal N. Run under -race.
func TestSessionEventSeqConcurrent(t *testing.T) {
	s := testSessionStore(t)
	ctx := context.Background()
	if err := s.CreateSession(ctx, mkSession("acme", "root", "", "root")); err != nil {
		t.Fatalf("root: %v", err)
	}
	const n = 64
	var wg sync.WaitGroup
	seqs := make([]int64, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			id, err := genID("evt")
			if err != nil {
				errs[i] = err
				return
			}
			e, err := s.AppendEvent(ctx, Event{
				ID: id, SessionID: "root", Org: "acme", Kind: KindLog,
				CreatedAt: time.Now().Unix(),
			})
			if err != nil {
				errs[i] = err
				return
			}
			seqs[i] = e.Seq
		}(i)
	}
	wg.Wait()
	seen := map[int64]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("append %d: %v", i, errs[i])
		}
		if seen[seqs[i]] {
			t.Fatalf("duplicate seq %d — MAX+1 allocation raced", seqs[i])
		}
		seen[seqs[i]] = true
	}
	for want := int64(1); want <= n; want++ {
		if !seen[want] {
			t.Fatalf("gap: seq %d missing — a concurrent append was lost", want)
		}
	}
	if got, _ := s.CountEvents(ctx, "acme", "root"); got != n {
		t.Fatalf("persisted event count want %d, got %d", n, got)
	}
}

func genIDMust(t *testing.T) string {
	t.Helper()
	id, err := genID("evt")
	if err != nil {
		t.Fatalf("genID: %v", err)
	}
	return id
}

// ---- HTTP: helpers ----

// doNoUser sends X-Org-Id WITHOUT X-User-Id — the anonymous-forge path the
// principal gate must refuse (no validated principal).
func doNoUser(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
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
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
}

// register is a helper that POSTs a session and returns its view.
func register(t *testing.T, app *zip.App, org string, body map[string]any) sessionView {
	t.Helper()
	code, b := do(t, app, http.MethodPost, "/v1/agents/sessions", org, body)
	if code != http.StatusCreated {
		t.Fatalf("register want 201, got %d (%s)", code, b)
	}
	var v sessionView
	mustJSON(t, b, &v)
	return v
}

// ---- HTTP: tree, org-scope, precedence ----

func TestSessionsHTTPTreeAndScope(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})

	// Route precedence: /v1/agents/sessions is NOT captured by /v1/agents/:name.
	code, b := do(t, app, http.MethodGet, "/v1/agents/sessions", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list sessions want 200 (not shadowed by :name), got %d (%s)", code, b)
	}
	var empty struct {
		Sessions []sessionView `json:"sessions"`
	}
	mustJSON(t, b, &empty)
	if len(empty.Sessions) != 0 {
		t.Fatalf("fresh org want 0 sessions, got %d", len(empty.Sessions))
	}

	// Build a tree: root (outer @hanzo/dev run) -> two subagents -> one grandchild.
	root := register(t, app, "acme", map[string]any{"agent": "hanzo-dev", "title": "outer run"})
	if root.RootSessionID != root.ID || root.ParentSessionID != "" {
		t.Fatalf("root must self-root with no parent, got %+v", root)
	}
	childA := register(t, app, "acme", map[string]any{"agent": "planner", "parentSessionId": root.ID})
	childB := register(t, app, "acme", map[string]any{"agent": "coder", "parentSessionId": root.ID})
	gchild := register(t, app, "acme", map[string]any{"agent": "tester", "parentSessionId": childA.ID})
	for _, c := range []sessionView{childA, childB, gchild} {
		if c.RootSessionID != root.ID {
			t.Fatalf("subagent %s must inherit rootSessionId %s, got %s", c.Agent, root.ID, c.RootSessionID)
		}
	}
	if gchild.ParentSessionID != childA.ID {
		t.Fatalf("grandchild parent want %s, got %s", childA.ID, gchild.ParentSessionID)
	}

	// Default list = ROOTS only (the outer-agent view).
	code, b = do(t, app, http.MethodGet, "/v1/agents/sessions", "acme", nil)
	mustJSON(t, b, &empty)
	if code != http.StatusOK || len(empty.Sessions) != 1 || empty.Sessions[0].ID != root.ID {
		t.Fatalf("default list want [root], got %d %+v", code, empty.Sessions)
	}
	if empty.Sessions[0].Children != 2 {
		t.Fatalf("root fan-out want 2, got %d", empty.Sessions[0].Children)
	}

	// The tree endpoint returns the full subagent-flow graph.
	code, b = do(t, app, http.MethodGet, "/v1/agents/sessions/"+root.ID+"/tree", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("tree want 200, got %d (%s)", code, b)
	}
	var tree treeNode
	mustJSON(t, b, &tree)
	if tree.Session.ID != root.ID || len(tree.Children) != 2 {
		t.Fatalf("tree root want 2 children, got %+v", tree)
	}
	// Find childA subtree and confirm the grandchild hangs off it.
	var found bool
	for _, ch := range tree.Children {
		if ch.Session.ID == childA.ID {
			if len(ch.Children) != 1 || ch.Children[0].Session.ID != gchild.ID {
				t.Fatalf("childA must have grandchild, got %+v", ch)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("childA not found in tree")
	}

	// Cross-tenant: evil cannot see, read, tree, control, or parent-under acme's root.
	code, b = do(t, app, http.MethodGet, "/v1/agents/sessions", "evil", nil)
	mustJSON(t, b, &empty)
	if len(empty.Sessions) != 0 {
		t.Fatalf("evil must see 0 sessions, got %d", len(empty.Sessions))
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/sessions/"+root.ID, "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil get acme session want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/agents/sessions/"+root.ID+"/tree", "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil tree acme session want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions/"+root.ID+"/stop", "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil control acme session want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions/"+root.ID+"/events", "evil",
		map[string]any{"kind": "log"}); code != http.StatusNotFound {
		t.Fatalf("evil append to acme session want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions", "evil",
		map[string]any{"agent": "x", "parentSessionId": root.ID}); code != http.StatusBadRequest {
		t.Fatalf("evil parent-under acme root want 400, got %d", code)
	}
}

// ---- HTTP: events + status ----

func TestSessionsHTTPEventsAndStatus(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	root := register(t, app, "acme", map[string]any{"agent": "dev"})

	// Append events: message, tool-call, spawn — seq is monotonic.
	for i, k := range []string{KindMessage, KindToolCall, KindSpawn} {
		code, b := do(t, app, http.MethodPost, "/v1/agents/sessions/"+root.ID+"/events", "acme",
			map[string]any{"kind": k, "payload": map[string]any{"n": i}})
		if code != http.StatusCreated {
			t.Fatalf("append %s want 201, got %d (%s)", k, code, b)
		}
		var ev eventView
		mustJSON(t, b, &ev)
		if ev.Seq != int64(i+1) || ev.Kind != k {
			t.Fatalf("event %s seq want %d, got %+v", k, i+1, ev)
		}
	}
	// Bad kind + bad payload are rejected.
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions/"+root.ID+"/events", "acme",
		map[string]any{"kind": "bogus"}); code != http.StatusBadRequest {
		t.Fatalf("bad kind want 400, got %d", code)
	}

	// Detail shows recent events + event count.
	code, b := do(t, app, http.MethodGet, "/v1/agents/sessions/"+root.ID, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("detail want 200, got %d (%s)", code, b)
	}
	var det sessionDetail
	mustJSON(t, b, &det)
	if det.Events != 3 || len(det.RecentEvents) != 3 {
		t.Fatalf("detail want 3 events, got %d / %d", det.Events, len(det.RecentEvents))
	}

	// PATCH running -> done sets endedAt; then terminal is monotonic.
	code, b = do(t, app, http.MethodPatch, "/v1/agents/sessions/"+root.ID, "acme",
		map[string]any{"status": StatusDone})
	if code != http.StatusOK {
		t.Fatalf("patch done want 200, got %d (%s)", code, b)
	}
	var done sessionView
	mustJSON(t, b, &done)
	if done.Status != StatusDone || done.EndedAt == "" {
		t.Fatalf("done must set endedAt, got %+v", done)
	}
	if code, _ := do(t, app, http.MethodPatch, "/v1/agents/sessions/"+root.ID, "acme",
		map[string]any{"status": StatusRunning}); code != http.StatusConflict {
		t.Fatalf("reopen finished session want 409, got %d", code)
	}
}

// ---- HTTP: control authz + tasks forward ----

// fakeTasks is an enabled TaskController capturing the last forwarded op.
type fakeTasks struct {
	mu       sync.Mutex
	signals  []string
	cancels  int
	lastWF   string
	failNext bool
}

func (f *fakeTasks) Signal(_ context.Context, wf, _ string, name string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return context.DeadlineExceeded
	}
	f.signals = append(f.signals, name)
	f.lastWF = wf
	return nil
}
func (f *fakeTasks) Cancel(_ context.Context, wf, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	f.lastWF = wf
	return nil
}
func (f *fakeTasks) Enabled() bool { return true }

func TestSessionsControlAuthzAndForward(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	ft := &fakeTasks{}
	mounted.State.tasks = ft // inject an enabled durable-execution backend for this test

	// A task-backed session forwards control to the tasks engine.
	backed := register(t, app, "acme", map[string]any{
		"agent": "dev", "taskWorkflowId": "wf-123", "taskRunId": "run-1",
	})
	// pause -> Signal("pause")
	code, b := do(t, app, http.MethodPost, "/v1/agents/sessions/"+backed.ID+"/pause", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("pause want 200, got %d (%s)", code, b)
	}
	var res struct {
		Command   string    `json:"command"`
		Event     eventView `json:"event"`
		Forwarded bool      `json:"forwarded"`
	}
	mustJSON(t, b, &res)
	if !res.Forwarded || res.Command != CmdPause || res.Event.Kind != KindControl {
		t.Fatalf("pause must forward + record a control event, got %+v", res)
	}
	// message (steer) -> Signal("message")
	do(t, app, http.MethodPost, "/v1/agents/sessions/"+backed.ID+"/message", "acme",
		map[string]any{"message": "focus on the bug"})
	// stop -> Cancel
	code, _ = do(t, app, http.MethodPost, "/v1/agents/sessions/"+backed.ID+"/stop", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("stop want 200, got %d", code)
	}
	ft.mu.Lock()
	gotSignals, gotCancels, gotWF := append([]string{}, ft.signals...), ft.cancels, ft.lastWF
	ft.mu.Unlock()
	if len(gotSignals) != 2 || gotSignals[0] != CmdPause || gotSignals[1] != CmdMessage {
		t.Fatalf("want signals [pause,message], got %v", gotSignals)
	}
	if gotCancels != 1 || gotWF != "wf-123" {
		t.Fatalf("want 1 cancel on wf-123, got cancels=%d wf=%s", gotCancels, gotWF)
	}

	// Control is recorded as an event even on a NON-task-backed session
	// (forwarded=false) — stream-consuming surfaces act on it.
	plain := register(t, app, "acme", map[string]any{"agent": "dev"})
	code, b = do(t, app, http.MethodPost, "/v1/agents/sessions/"+plain.ID+"/pause", "acme", nil)
	mustJSON(t, b, &res)
	if code != http.StatusOK || res.Forwarded {
		t.Fatalf("plain pause want 200 forwarded=false, got %d %+v", code, res)
	}
	// The control command landed in the event log.
	_, b = do(t, app, http.MethodGet, "/v1/agents/sessions/"+plain.ID, "acme", nil)
	var det sessionDetail
	mustJSON(t, b, &det)
	if det.Events != 1 || det.RecentEvents[0].Kind != KindControl {
		t.Fatalf("control must be recorded as an event, got %+v", det.RecentEvents)
	}

	// A forward FAILURE is a 502 but the intent is still recorded.
	ft.failNext = true
	code, _ = do(t, app, http.MethodPost, "/v1/agents/sessions/"+backed.ID+"/resume", "acme", nil)
	// backed was stopped above (running still — stop only records/cancels, status
	// is surface-owned), so resume is allowed; the forward fails -> 502.
	if code != http.StatusBadGateway {
		t.Fatalf("forward failure want 502, got %d", code)
	}

	// AuthZ: X-Org-Id without a validated principal (no X-User-Id) is refused.
	if code, _ := doNoUser(t, app, http.MethodPost, "/v1/agents/sessions/"+backed.ID+"/pause", "acme", nil); code != http.StatusForbidden {
		t.Fatalf("control without validated principal want 403, got %d", code)
	}
	if code, _ := doNoUser(t, app, http.MethodPost, "/v1/agents/sessions", "acme",
		map[string]any{"agent": "x"}); code != http.StatusForbidden {
		t.Fatalf("register without validated principal want 403, got %d", code)
	}

	// Control on a finished session is refused (409).
	fin := register(t, app, "acme", map[string]any{"agent": "dev", "status": StatusDone})
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/sessions/"+fin.ID+"/pause", "acme", nil); code != http.StatusConflict {
		t.Fatalf("control a finished session want 409, got %d", code)
	}
}

// ---- run integration (#5): a run opens a root session ----

func TestRunOpensRootSession(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "the answer"})
	do(t, app, http.MethodPost, "/v1/agents", "acme",
		map[string]any{"name": "helper", "model": "m", "instructions": "x"})
	if code, _ := do(t, app, http.MethodPost, "/v1/agents/helper/run", "acme", map[string]any{"input": "hi"}); code != http.StatusOK {
		t.Fatalf("run want 200")
	}
	// The run is now visible as a root session with a log event.
	_, b := do(t, app, http.MethodGet, "/v1/agents/sessions", "acme", nil)
	var lst struct {
		Sessions []sessionView `json:"sessions"`
	}
	mustJSON(t, b, &lst)
	if len(lst.Sessions) != 1 {
		t.Fatalf("run should open 1 root session, got %d", len(lst.Sessions))
	}
	s := lst.Sessions[0]
	if s.Agent != "helper" || s.Status != StatusDone || s.Events != 1 {
		t.Fatalf("run session shape wrong: %+v", s)
	}
}

// ---- bus (the ZAP stream seam) ----

func TestBusFanoutOrgFilterAndOverrun(t *testing.T) {
	b := newBus()
	chA, cancelA := b.subscribe("acme")
	chB, _ := b.subscribe("evil")
	defer cancelA()

	b.publish(streamUpdate{Org: "acme", RootID: "r", Type: "session"})
	select {
	case u := <-chA:
		if u.Org != "acme" {
			t.Fatalf("acme sub got wrong org %s", u.Org)
		}
	case <-time.After(time.Second):
		t.Fatal("acme sub got no update")
	}
	// evil must NOT receive acme's update (org filter).
	select {
	case <-chB:
		t.Fatal("evil sub must not receive acme update")
	default:
	}

	// Overrun: fill acme's buffer past capacity — the laggard is dropped (closed).
	for i := 0; i < subBuffer+10; i++ {
		b.publish(streamUpdate{Org: "acme", RootID: "r", Type: "event"})
	}
	// Drain until closed.
	dropped := false
	for i := 0; i < subBuffer+20; i++ {
		if _, open := <-chA; !open {
			dropped = true
			break
		}
	}
	if !dropped {
		t.Fatal("overrun laggard must be dropped (channel closed)")
	}

	// close() unblocks remaining subscribers.
	b.close()
	if _, open := <-chB; open {
		t.Fatal("close() must close evil sub channel")
	}
}

// TestPublishReachesSubscriber proves a live registration fans out to a bus
// subscriber — the exact path the SSE/ZAP stream handler consumes.
func TestPublishReachesSubscriber(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	ch, cancel := mounted.State.bus.subscribe("acme")
	defer cancel()
	root := register(t, app, "acme", map[string]any{"agent": "dev"})
	select {
	case u := <-ch:
		if u.Type != "session" || u.Session == nil || u.Session.ID != root.ID {
			t.Fatalf("subscriber should receive the registered session, got %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber received no update for a live registration")
	}
}
