package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/zap-proto/zip"
)

// rpcReply is one JSON-RPC response, as a client reads it.
type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int              `json:"code"`
		Message string           `json:"message"`
		Data    []map[string]any `json:"data"`
	} `json:"error"`
}

// a2aCall sends one A2A request as the person user of org (both empty: no
// credential at all), with extra headers.
func a2aCall(t *testing.T, app *zip.App, org, user string, header map[string]string, method string, params any) (int, http.Header, rpcReply) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	return a2aRaw(t, app, org, user, header, body)
}

func a2aRaw(t *testing.T, app *zip.App, org, user string, header map[string]string, body []byte) (int, http.Header, rpcReply) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/a2a", bytes.NewReader(body))
	req.Host = "api.hanzo.ai"
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", user)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("A2A: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var r rpcReply
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("A2A answered %d with no JSON-RPC body: %s", resp.StatusCode, b)
	}
	return resp.StatusCode, resp.Header, r
}

func userMessage(text string) map[string]any {
	return map[string]any{"message": map[string]any{"messageId": "m-" + text, "role": "ROLE_USER", "parts": []any{map[string]any{"text": text}}}}
}

// taskOf reads a SendMessage result's task, or a GetTask/CancelTask result.
func taskOf(t *testing.T, r rpcReply, wrapped bool) a2aTask {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("want a task, got error %d %s", r.Error.Code, r.Error.Message)
	}
	if wrapped {
		var w struct {
			Task *a2aTask `json:"task"`
		}
		if err := json.Unmarshal(r.Result, &w); err != nil || w.Task == nil {
			t.Fatalf("SendMessage result has no task: %s", r.Result)
		}
		return *w.Task
	}
	var task a2aTask
	if err := json.Unmarshal(r.Result, &task); err != nil {
		t.Fatalf("not a task: %s", r.Result)
	}
	return task
}

func wantCode(t *testing.T, what string, r rpcReply, code int) {
	t.Helper()
	if r.Error == nil || r.Error.Code != code {
		t.Fatalf("%s: want error %d, got %+v (result %s)", what, code, r.Error, r.Result)
	}
}

// A message is a task the default assistant answers as an agent run: the answer
// is the task's artifact and the last message of its history, and the task is
// kept for its person to read, list and (once it is over) not cancel.
func TestA2AMessageIsATaskTheAssistantAnswers(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "the answer"})
	code, _, r := a2aCall(t, app, "maxpower", "u-1", map[string]string{"A2A-Version": "1.0"}, "SendMessage", userMessage("hi"))
	task := taskOf(t, r, true)
	if code != http.StatusOK || task.Status.State != taskCompleted || task.ContextID == "" || task.Status.Timestamp == "" {
		t.Fatalf("%d %+v", code, task)
	}
	if len(task.Artifacts) != 1 || *task.Artifacts[0].Parts[0].Text != "the answer" {
		t.Fatalf("artifacts = %+v", task.Artifacts)
	}
	if len(task.History) != 2 || task.History[0].MessageID != "m-hi" || task.History[0].Role != "ROLE_USER" ||
		task.History[1].Role != "ROLE_AGENT" || *task.History[1].Parts[0].Text != "the answer" {
		t.Fatalf("history = %+v", task.History)
	}
	if string(r.ID) != "7" {
		t.Fatalf("the response answers id %s, not the request's", r.ID)
	}

	// The run behind it is an ordinary run of the assistant, in its person's history.
	if code, body := do(t, app, http.MethodGet, "/v1/agent/runs", "maxpower", nil); code != http.StatusOK {
		t.Fatalf("runs: %d %s", code, body)
	}

	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "GetTask", map[string]any{"id": task.ID, "historyLength": 1})
	got := taskOf(t, r, false)
	if got.ID != task.ID || got.Status.State != taskCompleted || len(got.History) != 1 || got.History[0].Role != "ROLE_AGENT" {
		t.Fatalf("GetTask historyLength 1 = %+v", got)
	}
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "ListTasks", map[string]any{})
	var list struct {
		Tasks     []a2aTask `json:"tasks"`
		Next      *string   `json:"nextPageToken"`
		TotalSize int       `json:"totalSize"`
		PageSize  int       `json:"pageSize"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil || len(list.Tasks) != 1 || list.TotalSize != 1 ||
		list.Next == nil || *list.Next != "" || list.PageSize != 50 || len(list.Tasks[0].Artifacts) != 0 {
		t.Fatalf("ListTasks = %s (%v)", r.Result, err)
	}
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "CancelTask", map[string]any{"id": task.ID})
	wantCode(t, "cancel a completed task", r, a2aTaskNotCancelable)
}

// A conversation goes on by sending the next message in the task's context: the
// assistant is shown the turns before it.
func TestA2AConversationGoesOnInItsContext(t *testing.T) {
	ai := &fakeAI{content: "first answer"}
	app := mountApp(t, ai)
	_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", userMessage("first question"))
	first := taskOf(t, r, true)

	next := userMessage("second question")
	next["message"].(map[string]any)["contextId"] = first.ContextID
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", next)
	second := taskOf(t, r, true)
	if second.ContextID != first.ContextID || second.ID == first.ID {
		t.Fatalf("second task %+v in context %s", second, first.ContextID)
	}
	var saw []string
	for _, m := range ai.gotMsgs {
		saw = append(saw, m.Role+":"+m.Content)
	}
	joined := strings.Join(saw, "|")
	if !strings.Contains(joined, infer.RoleUser+":first question") || !strings.Contains(joined, infer.RoleAssistant+":first answer") {
		t.Fatalf("the second turn was not sent with the first: %s", joined)
	}

	// Naming a task to continue it is refused with where the conversation goes on.
	follow := userMessage("third")
	follow["message"].(map[string]any)["taskId"] = second.ID
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", follow)
	wantCode(t, "a message to a finished task", r, a2aUnsupported)
	if !strings.Contains(r.Error.Message, first.ContextID) {
		t.Fatalf("the refusal does not say which context to continue in: %s", r.Error.Message)
	}
}

// With no credential the answer is 401 with the challenge an OAuth client follows
// to find where to sign in.
func TestA2AWithoutACredentialIsChallenged(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	code, h, r := a2aCall(t, app, "", "", nil, "SendMessage", userMessage("hi"))
	if code != http.StatusUnauthorized || !strings.Contains(h.Get("WWW-Authenticate"), `resource_metadata="https://api.hanzo.ai/.well-known/oauth-protected-resource"`) {
		t.Fatalf("%d %q", code, h.Get("WWW-Authenticate"))
	}
	wantCode(t, "no credential", r, a2aUnauthenticated)
}

// A task is its person's: another org, and another member of the same org, are
// told it does not exist.
func TestA2ATasksAreTheirPersons(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "mine"})
	_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", userMessage("hi"))
	task := taskOf(t, r, true)
	for _, who := range [][2]string{{"acme", "u-1"}, {"maxpower", "u-2"}} {
		_, _, r = a2aCall(t, app, who[0], who[1], nil, "GetTask", map[string]any{"id": task.ID})
		wantCode(t, who[0]+"/"+who[1]+" reads the task", r, a2aTaskNotFound)
		_, _, r = a2aCall(t, app, who[0], who[1], nil, "ListTasks", nil)
		if !strings.Contains(string(r.Result), `"totalSize":0`) {
			t.Fatalf("%s/%s lists %s", who[0], who[1], r.Result)
		}
	}
}

// A model failure is a failed task whose status says why; nothing is hidden.
func TestA2AAFailedRunIsAFailedTask(t *testing.T) {
	app := mountApp(t, &fakeAI{err: errors.New("upstream exploded")})
	_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", userMessage("hi"))
	task := taskOf(t, r, true)
	if task.Status.State != taskFailed || task.Status.Message == nil || len(task.Artifacts) != 0 {
		t.Fatalf("%+v", task)
	}
}

// heldAI answers only when released, so a test can hold a run in flight.
type heldAI struct {
	release chan struct{}
	started chan struct{}
}

func (h *heldAI) ChatCompletion(ctx context.Context, _ *infer.ChatRequest) (*infer.ChatResponse, error) {
	select {
	case h.started <- struct{}{}:
	default:
	}
	select {
	case <-h.release:
		return &infer.ChatResponse{Content: "late"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (h *heldAI) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }
func (h *heldAI) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

// returnImmediately answers with the working task at once; CancelTask stops it,
// and the run finishing afterwards does not undo the cancel the caller was told.
func TestA2AReturnImmediatelyThenCancel(t *testing.T) {
	held := &heldAI{release: make(chan struct{}), started: make(chan struct{}, 1)}
	app := mountApp(t, held)
	send := userMessage("slow")
	send["configuration"] = map[string]any{"returnImmediately": true}
	_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", send)
	task := taskOf(t, r, true)
	if task.Status.State != taskWorking {
		t.Fatalf("returnImmediately answered %s", task.Status.State)
	}
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "CancelTask", map[string]any{"id": task.ID})
	if got := taskOf(t, r, false); got.Status.State != taskCanceled || got.Status.Message == nil {
		t.Fatalf("CancelTask = %+v", got)
	}
	close(held.release)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, running := a2aRunning.Load(task.ID); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the canceled run never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "GetTask", map[string]any{"id": task.ID})
	if got := taskOf(t, r, false); got.Status.State != taskCanceled {
		t.Fatalf("after the run ended the task is %s", got.Status.State)
	}
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "CancelTask", map[string]any{"id": task.ID})
	wantCode(t, "cancel twice", r, a2aTaskNotCancelable)
}

// A run that outlives the request that started it finishes on its own: it reads
// nothing of that request once the handler has returned (the request's context is
// recycled for the next one), and its task ends completed with the answer.
func TestA2AReturnImmediatelyOutlivesItsRequest(t *testing.T) {
	held := &heldAI{release: make(chan struct{}), started: make(chan struct{}, 1)}
	app := mountApp(t, held)
	send := userMessage("later")
	send["configuration"] = map[string]any{"returnImmediately": true}
	_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", send)
	task := taskOf(t, r, true)
	<-held.started
	// Other requests reuse the pool the first one's context came from.
	for range 3 {
		_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "GetTask", map[string]any{"id": task.ID})
		if got := taskOf(t, r, false); got.Status.State != taskWorking {
			t.Fatalf("while held the task is %s", got.Status.State)
		}
	}
	close(held.release)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, running := a2aRunning.Load(task.ID); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "GetTask", map[string]any{"id": task.ID})
	got := taskOf(t, r, false)
	if got.Status.State != taskCompleted || len(got.Artifacts) != 1 || *got.Artifacts[0].Parts[0].Text != "late" {
		t.Fatalf("after the request ended the task is %+v", got)
	}
}

// ListTasks pages newest first, and its token is one only this server wrote.
func TestA2AListTasksPages(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "ok"})
	for _, q := range []string{"one", "two", "three"} {
		_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "SendMessage", userMessage(q))
		taskOf(t, r, true)
		time.Sleep(5 * time.Millisecond) // distinct status timestamps
	}
	type page struct {
		Tasks []a2aTask `json:"tasks"`
		Next  string    `json:"nextPageToken"`
		Total int       `json:"totalSize"`
	}
	read := func(params map[string]any) page {
		_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "ListTasks", params)
		var p page
		if r.Error != nil || json.Unmarshal(r.Result, &p) != nil {
			t.Fatalf("ListTasks %v: %+v %s", params, r.Error, r.Result)
		}
		return p
	}
	p1 := read(map[string]any{"pageSize": 2, "historyLength": 0})
	if len(p1.Tasks) != 2 || p1.Total != 3 || p1.Next == "" || p1.Tasks[0].History != nil || p1.Tasks[0].Status.Timestamp == "" {
		t.Fatalf("page 1 = %+v", p1)
	}
	if p1.Tasks[0].Status.Timestamp < p1.Tasks[1].Status.Timestamp {
		t.Fatalf("page 1 is not newest first: %s before %s", p1.Tasks[0].Status.Timestamp, p1.Tasks[1].Status.Timestamp)
	}
	p2 := read(map[string]any{"pageSize": 2, "pageToken": p1.Next})
	if len(p2.Tasks) != 1 || p2.Next != "" || p2.Tasks[0].ID == p1.Tasks[0].ID || p2.Tasks[0].ID == p1.Tasks[1].ID {
		t.Fatalf("page 2 = %+v", p2)
	}
	_, _, r := a2aCall(t, app, "maxpower", "u-1", nil, "ListTasks", map[string]any{"pageToken": "not-ours"})
	wantCode(t, "a token this server did not write", r, rpcInvalidParams)
	_, _, r = a2aCall(t, app, "maxpower", "u-1", nil, "ListTasks", map[string]any{"pageSize": 101})
	wantCode(t, "pageSize over 100", r, rpcInvalidParams)
	if got := read(map[string]any{"status": taskCompleted}); got.Total != 3 {
		t.Fatalf("status filter = %+v", got)
	}
}

// What the server does not speak it says so, in A2A's own codes.
func TestA2ARefusesWhatItDoesNotSpeak(t *testing.T) {
	app := mountApp(t, &fakeAI{content: "x"})
	cases := []struct {
		what   string
		header map[string]string
		method string
		params any
		code   int
	}{
		{"an A2A 0.3 method", nil, "message/send", userMessage("hi"), a2aVersionNotSupported},
		{"an A2A-Version it does not speak", map[string]string{"A2A-Version": "0.3"}, "SendMessage", userMessage("hi"), a2aVersionNotSupported},
		{"streaming", nil, "SendStreamingMessage", userMessage("hi"), a2aUnsupported},
		{"resubscribing", nil, "SubscribeToTask", map[string]any{"id": "task_x"}, a2aUnsupported},
		{"the extended card", nil, "GetExtendedAgentCard", nil, a2aUnsupported},
		{"push notifications", nil, "CreateTaskPushNotificationConfig", map[string]any{}, a2aPushNotSupported},
		{"an unknown method", nil, "DoSomething", nil, rpcMethodNotFound},
		{"a task that does not exist", nil, "GetTask", map[string]any{"id": "task_nope"}, a2aTaskNotFound},
		{"a message to a task that does not exist", nil, "SendMessage", map[string]any{"message": map[string]any{
			"messageId": "m", "role": "ROLE_USER", "taskId": "task_nope", "parts": []any{map[string]any{"text": "hi"}}}}, a2aTaskNotFound},
		{"a file", nil, "SendMessage", map[string]any{"message": map[string]any{
			"messageId": "m", "role": "ROLE_USER", "parts": []any{map[string]any{"url": "https://example.com/a.pdf"}}}}, a2aContentType},
		{"output it cannot give", nil, "SendMessage", map[string]any{"message": userMessage("hi")["message"],
			"configuration": map[string]any{"acceptedOutputModes": []string{"image/png"}}}, a2aContentType},
		{"an agent's own message", nil, "SendMessage", map[string]any{"message": map[string]any{
			"messageId": "m", "role": "ROLE_AGENT", "parts": []any{map[string]any{"text": "hi"}}}}, rpcInvalidParams},
	}
	for _, c := range cases {
		code, _, r := a2aCall(t, app, "maxpower", "u-1", c.header, c.method, c.params)
		if code != http.StatusOK {
			t.Errorf("%s: HTTP %d; a JSON-RPC refusal is a 200", c.what, code)
		}
		if r.Error == nil || r.Error.Code != c.code {
			t.Errorf("%s: want %d, got %+v", c.what, c.code, r.Error)
			continue
		}
		if c.code >= -32009 && c.code <= -32001 {
			if len(r.Error.Data) != 1 || r.Error.Data[0]["domain"] != "a2a-protocol.org" || r.Error.Data[0]["reason"] == "" {
				t.Errorf("%s: an A2A error carries ErrorInfo in a2a-protocol.org, got %+v", c.what, r.Error.Data)
			}
		}
	}
	if _, _, r := a2aRaw(t, app, "maxpower", "u-1", nil, []byte("{not json")); r.Error == nil || r.Error.Code != rpcParse {
		t.Errorf("parse error: %+v", r.Error)
	}
	if _, _, r := a2aRaw(t, app, "maxpower", "u-1", nil, []byte(`{"jsonrpc":"2.0","method":"GetTask","params":{"id":"x"}}`)); r.Error == nil || r.Error.Code != rpcInvalidRequest {
		t.Errorf("a request without an id: %+v", r.Error)
	}
}
