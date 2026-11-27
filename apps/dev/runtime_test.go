package dev

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cek"
	"zap-proto.io/go/rpc"
	"zap-proto.io/go/transport/quic"

	"github.com/hanzoai/cloud/apps/dev/loop"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/kit"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
)

// model is a gateway that says what it is told to.
type model struct {
	mu    sync.Mutex
	reply []*infer.ChatResponse
	seen  []infer.ChatRequest
}

func (m *model) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, *req)
	r := m.reply[0]
	m.reply = m.reply[1:]
	return r, nil
}
func (m *model) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }
func (m *model) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

func serving(t *testing.T, ai infer.Client) *Runtime {
	t.Helper()
	master := make([]byte, cek.KeyLen)
	for i := range master {
		master[i] = byte(i + 1)
	}
	if err := cek.SetMaster(master); err != nil {
		t.Fatal(err)
	}
	b := cloud.NewBase(cloud.Deps{}, "dev")
	b.DataDir = t.TempDir()
	var eng *tasks.Embedded
	{
		dir := kit.Sockets(t)
		var err error
		eng, err = tasks.Embed(context.Background(), tasks.EmbedConfig{Address: filepath.Join(dir, "t.sock"), DataDir: dir})
		if err != nil {
			t.Fatalf("embed the tasks engine: %v", err)
		}
		t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	}
	rt, err := NewRuntime(b, ai, func() *tasks.Embedded { return eng })
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	rt.Serve()
	t.Cleanup(func() { running.Store(nil); rt.Close() })
	for i := 0; rt.work.client2() == nil; i++ {
		if i > 200 {
			t.Fatal("the drive worker never came up")
		}
		time.Sleep(25 * time.Millisecond)
	}
	return rt
}

func TestAWasmSessionHoldsNoCellUntilATurnNeedsOne(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	leases := 0
	p.OnLease = func(string) { leases++ }
	call := &model{reply: []*infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "1", Name: "exec", Arguments: `{"argv":["echo","hi"]}`}}},
		{Content: "done"},
	}}
	rt := serving(t, call)
	app := mount(t)

	code, b := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", `{"runtime":"wasm"}`)
	if code != http.StatusCreated {
		t.Fatalf("POST wasm session = %d %s", code, b)
	}
	var s Session
	_ = json.Unmarshal(b, &s)
	if !strings.HasPrefix(s.ID, wasmPrefix) || s.Sandbox != "" || s.Zap != "https://zap.hanzo.ai/v1/dev/zap/"+s.ID {
		t.Fatalf("session %+v: a wasm session has its own id, no cell and the same zap shape", s)
	}
	if leases != 0 {
		t.Fatal("a cell was leased for a session that has run nothing")
	}

	var said []string
	if err := rt.Turn(as2(t), "hanzo", "u_alice", s.ID, "echo hi", func(t string) { said = append(said, t) }); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if leases != 1 {
		t.Errorf("%d cells leased, want one, on the first command", leases)
	}
	if len(said) != 1 || said[0] != "done" {
		t.Errorf("the person was shown %v, want the model's words", said)
	}
	if got, _ := rt.loop.Get("hanzo", "u_alice", s.ID); got.Lease == "" {
		t.Error("the cell the turn leased was not recorded on the session")
	}
	if len(call.seen) == 0 || len(call.seen[0].Messages) == 0 || call.seen[0].Messages[0].Role != infer.RoleSystem {
		t.Fatalf("expected system prelude on first turn, got %+v", call.seen)
	}
	sys := call.seen[0].Messages[0].Content
	for _, want := range []string{
		"build, customize, and mod any part of Hanzo",
		"https://hanzoskills.com/llms.txt",
		"https://hips.hanzo.ai/llms.txt",
		"/v1/tool/detect",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("dev system prelude missing %q", want)
		}
	}

	if code, b := as(t, app, "u_alice", http.MethodDelete, "/v1/dev/sessions/"+s.ID, ""); code >= 300 {
		t.Fatalf("DELETE = %d %s", code, b)
	}
	if p.Live() != 0 {
		t.Errorf("%d cells still leased after the session ended", p.Live())
	}
}

func as2(t *testing.T) context.Context { return acting("hanzo", "u_alice") }

// A session is put away, its cell given back, and it carries on when brought back; an
// interrupt reaches the cell and keeps the session.
func TestASessionIsSuspendedResumedAndInterrupted(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	call := &model{reply: []*infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "1", Name: "exec", Arguments: `{"argv":["ls"]}`}}},
		{Content: "listed"},
	}}
	rt := serving(t, call)
	app := mount(t)
	code, b := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions", `{"runtime":"wasm"}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, b)
	}
	var s Session
	_ = json.Unmarshal(b, &s)
	if err := rt.Turn(as2(t), "hanzo", "u_alice", s.ID, "list", nil); err != nil {
		t.Fatalf("turn: %v", err)
	}

	if code, b := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+s.ID+"/interrupt", ""); code != http.StatusOK {
		t.Fatalf("interrupt = %d %s", code, b)
	}
	if n := len(p.Stops); n != 1 {
		t.Errorf("the interrupt reached %d cells, want the session's one", n)
	}

	if code, b := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+s.ID+"/suspend", ""); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, b)
	}
	if p.Live() != 0 {
		t.Errorf("%d cells still leased by a suspended session", p.Live())
	}
	if err := rt.Submit(as2(t), "hanzo", "u_alice", s.ID, "more"); err == nil {
		t.Error("a suspended session took a turn")
	}
	if code, b := as(t, app, "u_alice", http.MethodPost, "/v1/dev/sessions/"+s.ID+"/resume", ""); code != http.StatusOK {
		t.Fatalf("resume = %d %s", code, b)
	}
	if code, _ := as(t, app, "u_bob", http.MethodPost, "/v1/dev/sessions/"+s.ID+"/suspend", ""); code != http.StatusNotFound {
		t.Errorf("another person suspended it: %d", code)
	}
}

// A turn whose work cannot be done now is done later, with nobody waiting: the first
// lease is refused, the engine runs the drive again, and the command runs once.
func TestATurnTheWorkerCouldNotFinishIsRetriedInTheBackground(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	p.FailLeases = 1
	call := &model{reply: []*infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "1", Name: "exec", Arguments: `{"argv":["make"]}`}}},
		{Content: "built"},
	}}
	rt := serving(t, call)
	if err := rt.loop.New("hanzo", "u_alice", "w_retry"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Submit(as2(t), "hanzo", "u_alice", "w_retry", "build it"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	var lines []loop.Line
	for deadline := time.Now().Add(45 * time.Second); len(lines) == 0 && time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		lines, _ = rt.Lines("hanzo", "u_alice", "w_retry", 0)
	}
	if len(lines) != 1 || lines[0].Text != "built" {
		t.Fatalf("the person was shown %v after the retry, want the model's words", lines)
	}
	if p.Live() != 1 {
		t.Errorf("%d cells leased after one refusal and one retry, want one", p.Live())
	}
}

// A cell the sandbox took away does not wedge the session: the next command is done in
// a new one.
func TestALostCellIsReplacedAndTheSessionCarriesOn(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	call := &model{reply: []*infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "1", Name: "exec", Arguments: `{"argv":["one"]}`}}}, {Content: "first"},
		{ToolCalls: []infer.ToolCall{{ID: "2", Name: "exec", Arguments: `{"argv":["two"]}`}}}, {Content: "second"},
	}}
	rt := serving(t, call)
	if err := rt.loop.New("hanzo", "u_alice", "w_lost"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Turn(as2(t), "hanzo", "u_alice", "w_lost", "one", nil); err != nil {
		t.Fatal(err)
	}
	first, _ := rt.loop.Lease("hanzo", "u_alice", "w_lost")
	if _, err := sandboxpeer.SandboxEnd(acting("hanzo", "u_alice"), &client.EndIn{ID: first}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Turn(as2(t), "hanzo", "u_alice", "w_lost", "two", nil); err != nil {
		t.Fatalf("the turn after the cell was lost: %v", err)
	}
	if second, _ := rt.loop.Lease("hanzo", "u_alice", "w_lost"); second == "" || second == first {
		t.Errorf("the session still names the lost cell %q (now %q)", first, second)
	}
	if p.Live() != 1 {
		t.Errorf("%d cells leased, want the one replacement", p.Live())
	}
}

// frames collects what a session pushes to its client.
type frames struct {
	mu  sync.Mutex
	got []string
}

func (f *frames) push(_, status uint32, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, string(body))
	return nil
}

func (f *frames) has(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.got {
		if strings.Contains(g, sub) {
			return true
		}
	}
	return false
}

func request(id int, tag, cap string) []byte {
	return rpc.BuildRequest(rpc.Call{Method: 0, PromiseID: uint32(id), Cap: []byte(cap),
		Payload: []byte(`{"_tag":"Request","id":` + strconv.Itoa(id) + `,"tag":"` + tag + `","payload":{}}`)})
}

// A wasm session's stream is answered by the Dev frame adapter once the bearer is proved
// and the session is the person's; a request that needs a workspace is refused in the
// client's own terms when no cell can be had, and otherwise carried to the real Dev
// server in a cell, which answers every frame after it.
func TestAWasmStreamIsAnsweredByTheFrameAdapterAndUpgradesOnNeed(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	rt := serving(t, &model{})
	if err := rt.loop.New("hanzo", "sub-1", "w_x"); err != nil {
		t.Fatal(err)
	}
	valid := accepts{"good": {Owner: "hanzo", Subject: "sub-1", Username: "alice"}, "other": {Owner: "hanzo", Subject: "sub-2"}}
	status := func(env []byte) uint32 { r, _ := rpc.ParseResponse(env); return r.Status }

	// Someone else's session is not found.
	if resp, _ := (&bridge{id: "w_x", sess: &quic.Session{}, valid: valid, push: new(frames).push}).dispatch(request(1, "subscribeServerConfig", "other")); status(resp) != rpc.StatusNotFound {
		t.Errorf("another person's session answered %d, want 404", status(resp))
	}

	// A chat-mode request is answered by the adapter, through the push.
	out := &frames{}
	b := &bridge{id: "w_x", sess: &quic.Session{}, valid: valid, push: out.push}
	if resp, err := b.dispatch(request(1, "subscribeServerConfig", "good")); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	if !out.has(`"requestId":1`) && !out.has(`"Chunk"`) {
		t.Fatalf("the frame adapter pushed %v, want the config the client asked for", out.got)
	}

	// A request that needs a workspace, with no cell to be had, is refused as the client reads it.
	p.FailLeases = 10
	if resp, err := b.dispatch(request(2, "terminal.open", "")); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	if !out.has("EnvironmentAuthorizationError") || !out.has(`"requiredPermission":"sandbox"`) {
		t.Errorf("a workspace request with no cell was answered %v, want the typed refusal", out.got)
	}

	// With a cell it is carried there unchanged, and so is everything after it.
	sock, got := cell(t)
	p.FailLeases = 0
	p.Stream = func(_, _, _, _ string) string { return sock }
	env := request(3, "terminal.open", "")
	if resp, err := b.dispatch(env); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	next := request(4, "terminal.write", "")
	if resp, err := b.dispatch(next); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	for deadline := time.Now().Add(5 * time.Second); len(got()) < 2 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	in := got()
	if len(in) != 2 || string(in[0]) != string(env) || string(in[1]) != string(next) {
		t.Fatalf("the cell received %d envelopes, want the request that needed it and the one after, unchanged", len(in))
	}
	if lease, _ := rt.loop.Lease("hanzo", "sub-1", "w_x"); lease == "" {
		t.Error("the cell the session was upgraded into is not recorded on it")
	}
}

func call(id uint32, method uint32, cap string) []byte {
	return rpc.BuildRequest(rpc.Call{Method: method, PromiseID: id | callBit, Cap: []byte(cap), Payload: []byte(`{}`)})
}

// What the client used to fetch over HTTP before it connected is answered on the same
// session: the descriptor even before the bearer, the session and the shell snapshot
// after it, each under the promise id it was asked with.
func TestTheCallsAClientMakesBeforeConnectingAreAnsweredOnTheSession(t *testing.T) {
	planetest.ServeSandboxes(t)
	rt := serving(t, &model{})
	if err := rt.loop.New("hanzo", "sub-1", "w_c"); err != nil {
		t.Fatal(err)
	}
	valid := accepts{"good": {Owner: "hanzo", Subject: "sub-1", Username: "alice"}}
	b := &bridge{id: "w_c", sess: &quic.Session{}, valid: valid, push: new(frames).push}
	read := func(env []byte) (rpc.Response, map[string]any) {
		t.Helper()
		r, err := rpc.ParseResponse(env)
		if err != nil {
			t.Fatalf("response: %v", err)
		}
		var v map[string]any
		_ = json.Unmarshal(r.Body, &v)
		return r, v
	}

	// Before the bearer: the descriptor is answered, anything else is refused.
	resp, _ := b.dispatch(call(7, 1, ""))
	if r, v := read(resp); r.Status != rpc.StatusOK || r.PromiseID != 7|callBit || v["environmentId"] == nil && v["label"] == nil {
		t.Errorf("the descriptor before the bearer = %+v %v", r, v)
	}
	if resp, _ := b.dispatch(call(8, 3, "")); func() bool { r, _ := read(resp); return r.Status != rpc.StatusUnauthorized }() {
		t.Error("the shell snapshot was answered with no bearer")
	}

	// With it: the session, then the shell snapshot, on the promise they were asked under.
	resp, _ = b.dispatch(call(9, 2, "good"))
	if r, v := read(resp); r.Status != rpc.StatusOK || r.PromiseID != 9|callBit || v["sessionMethod"] != "bearer-access-token" {
		t.Errorf("the session call = %+v %v", r, v)
	}
	resp, _ = b.dispatch(call(10, 3, ""))
	if r, v := read(resp); r.Status != rpc.StatusOK || r.PromiseID != 10|callBit || v["snapshotSequence"] == nil {
		t.Errorf("the shell snapshot = %+v %v", r, v)
	}
	if resp, _ := b.dispatch(call(11, 99, "")); func() bool { r, _ := read(resp); return r.Status != rpc.StatusNotFound }() {
		t.Error("a method nobody serves was answered")
	}
}

// A cell session answers the descriptor before the bearer the way a wasm one does, and
// nothing else: cloud serves it, and the cell is not asked.
func TestACellSessionAnswersTheDescriptorBeforeTheBearer(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	asked := false
	p.Stream = func(_, _, _, _ string) string { asked = true; return "" }
	serving(t, &model{})
	b := &bridge{id: "m_x", sess: &quic.Session{}, valid: accepts{}, push: new(frames).push}
	resp, err := b.dispatch(call(5, 1, ""))
	r, perr := rpc.ParseResponse(resp)
	if err != nil || perr != nil || r.Status != rpc.StatusOK || r.PromiseID != 5|callBit || !strings.Contains(string(r.Body), "{") {
		t.Fatalf("the descriptor on a cell session = %+v %v %v", r, err, perr)
	}
	resp, _ = b.dispatch(call(6, 3, ""))
	if r, _ := rpc.ParseResponse(resp); r.Status != rpc.StatusUnauthorized {
		t.Errorf("the shell snapshot before the bearer on a cell session = %d, want 401", r.Status)
	}
	if asked {
		t.Error("the cell was reached before the bearer was proved")
	}
}

// chatProject is the one project a chat session's adapter answers for: frame derives
// it from the environment's id (frame/state.go uuidFrom).
func chatProject(env string) string {
	h := sha256.Sum256([]byte("project:" + env))
	h[6], h[8] = h[6]&0x0f|0x50, h[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// A command the model asks for in a chat thread is done in the person's cell through
// the session's journal: it runs once, the cell is recorded on the session, and the
// thread carries on with what it printed.
func TestAChatThreadsCommandRunsInTheCellThroughTheJournal(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) {
		return "hi from the cell\n", "", 0, nil
	}
	// The fake cell runs a line that starts ": > " as the program (planetest isProgram).
	ai := &model{reply: []*infer.ChatResponse{
		{ToolCalls: []infer.ToolCall{{ID: "1", Name: "exec", Arguments: `{"argv":["sh","-c",": > m; echo hi"]}`}}},
		{Content: "it said hi"},
	}}
	rt := serving(t, ai)
	if err := rt.loop.New("hanzo", "sub-1", "w_c"); err != nil {
		t.Fatal(err)
	}
	out := &frames{}
	b := &bridge{id: "w_c", sess: &quic.Session{}, valid: accepts{"good": {Owner: "hanzo", Subject: "sub-1", Username: "alice"}}, push: out.push}
	launch := `{"_tag":"Request","id":1,"tag":"orchestration.launchThread","payload":{"commandId":"c1","threadId":"5b0f7c1e-2a8d-4c3e-9f61-0d2b7a9e4c11",` +
		`"projectId":"` + chatProject("w_c") + `","title":"Echo","modelSelection":{"instanceId":"hanzo","model":"enso"},` +
		`"initialMessage":{"messageId":"m1","text":"run echo hi","attachments":[]}}}`
	if resp, err := b.dispatch(rpc.BuildRequest(rpc.Call{PromiseID: 1, Cap: []byte("good"), Payload: []byte(launch)})); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	// The thread's reply reaches the client that subscribes to it.
	sub := `{"_tag":"Request","id":2,"tag":"orchestration.subscribeThread","payload":{"threadId":"5b0f7c1e-2a8d-4c3e-9f61-0d2b7a9e4c11"}}`
	if resp, err := b.dispatch(rpc.BuildRequest(rpc.Call{PromiseID: 2, Payload: []byte(sub)})); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	for deadline := time.Now().Add(10 * time.Second); !out.has("it said hi") && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if !out.has("it said hi") {
		t.Fatalf("the thread never answered after its command; pushed %d frames", len(out.got))
	}
	if p.Ran() != 1 {
		t.Errorf("the cell ran %d commands, want the one", p.Ran())
	}
	ai.mu.Lock()
	told, _ := json.Marshal(ai.seen[len(ai.seen)-1].Messages)
	ai.mu.Unlock()
	if !strings.Contains(string(told), "hi from the cell") {
		t.Errorf("the model was not told what the command printed: %s", told)
	}
	if lease, _ := rt.loop.Lease("hanzo", "sub-1", "w_c"); lease == "" {
		t.Error("the cell the thread's command leased is not recorded on the session")
	}
}

// streamingModel is a gateway that streams: each delta is handed on, and the next
// is written only once the client has been pushed the last.
type streamingModel struct {
	model
	deltas []string
	shown  func(string) bool
}

func (m *streamingModel) ChatStream(ctx context.Context, req *infer.ChatRequest, emit func(string) error) (*infer.ChatResponse, error) {
	m.mu.Lock()
	m.seen = append(m.seen, *req)
	m.mu.Unlock()
	text := ""
	for _, d := range m.deltas {
		text += d
		if err := emit(d); err != nil {
			return nil, err
		}
		for deadline := time.Now().Add(5 * time.Second); !m.shown(text) && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		}
	}
	return &infer.ChatResponse{Content: text}, nil
}

// A chat thread's reply reaches the client as the model writes it: each increment is
// pushed before the next is written, and the reply ends whole.
func TestAChatReplyIsStreamedAsTheModelWritesIt(t *testing.T) {
	planetest.ServeSandboxes(t)
	out := &frames{}
	ai := &streamingModel{deltas: []string{"Bon", "jour"}, shown: func(s string) bool { return out.has(`"text":"` + s + `"`) }}
	rt := serving(t, ai)
	if err := rt.loop.New("hanzo", "sub-1", "w_s"); err != nil {
		t.Fatal(err)
	}
	b := &bridge{id: "w_s", sess: &quic.Session{}, valid: accepts{"good": {Owner: "hanzo", Subject: "sub-1", Username: "alice"}}, push: out.push}
	const thread = "5b0f7c1e-2a8d-4c3e-9f61-0d2b7a9e4c11"
	launch := `{"_tag":"Request","id":1,"tag":"orchestration.launchThread","payload":{"commandId":"c1","threadId":"` + thread + `",` +
		`"projectId":"` + chatProject("w_s") + `","title":"Hi","modelSelection":{"instanceId":"hanzo","model":"enso"},` +
		`"initialMessage":{"messageId":"m1","text":"say hi in French","attachments":[]}}}`
	sub := `{"_tag":"Request","id":2,"tag":"orchestration.subscribeThread","payload":{"threadId":"` + thread + `"}}`
	for i, f := range []string{launch, sub} {
		cap := ""
		if i == 0 {
			cap = "good"
		}
		if resp, err := b.dispatch(rpc.BuildRequest(rpc.Call{PromiseID: uint32(i + 1), Cap: []byte(cap), Payload: []byte(f)})); resp != nil || err != nil {
			t.Fatalf("dispatch = %v %v", resp, err)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); !out.has(`"text":"Bonjour","attachments":[],"streaming":false`) && !out.has(`"streaming":false,"text":"Bonjour"`) && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if !out.has(`"text":"Bon"`) {
		t.Error("the first increment was never pushed on its own")
	}
	if !out.has(`"text":"Bonjour"`) {
		t.Fatal("the reply never arrived whole")
	}
	ai.mu.Lock()
	asked := len(ai.seen)
	ai.mu.Unlock()
	if asked == 0 {
		t.Error("the model was not asked through the stream")
	}
}

// answering is a Dev server's ZAP socket that keeps what it is sent and answers
// each envelope under its own promise.
func answering(t *testing.T) (path string, got func() []rpc.Call) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path = filepath.Join(dir, "zap.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var in []rpc.Call
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for {
			env, err := quic.ReadEnvelope(c)
			if err != nil {
				return
			}
			call, err := rpc.ParseRequest(env)
			if err != nil {
				return
			}
			mu.Lock()
			in = append(in, call)
			mu.Unlock()
			if quic.WriteEnvelope(c, rpc.BuildResponse(rpc.StatusOK, call.PromiseID, []byte(fmt.Sprintf("answer %d", call.PromiseID)))) != nil {
				return
			}
		}
	}()
	return path, func() []rpc.Call { mu.Lock(); defer mu.Unlock(); return append([]rpc.Call(nil), in...) }
}

// When a chat session moves into its cell, the cell's Dev server is handed the
// session's threads first, under the ids the client holds and with what was said in
// them, on an envelope that carries the bearer. The request that needed the cell
// follows, and the cell's answer to the import never reaches the client.
func TestAnUpgradeCarriesTheChatThreadsIntoTheCellFirst(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	rt := serving(t, &model{reply: []*infer.ChatResponse{{Content: "it said hi"}}})
	if err := rt.loop.New("hanzo", "sub-1", "w_u"); err != nil {
		t.Fatal(err)
	}
	out := &frames{}
	b := &bridge{id: "w_u", sess: &quic.Session{}, valid: accepts{"good": {Owner: "hanzo", Subject: "sub-1", Username: "alice"}}, push: out.push}
	const thread = "5b0f7c1e-2a8d-4c3e-9f61-0d2b7a9e4c11"
	launch := `{"_tag":"Request","id":1,"tag":"orchestration.launchThread","payload":{"commandId":"c1","threadId":"` + thread + `",` +
		`"projectId":"` + chatProject("w_u") + `","title":"Echo","modelSelection":{"instanceId":"hanzo","model":"enso"},` +
		`"initialMessage":{"messageId":"m1","text":"say hi","attachments":[]}}}`
	sub := `{"_tag":"Request","id":2,"tag":"orchestration.subscribeThread","payload":{"threadId":"` + thread + `"}}`
	for i, f := range []string{launch, sub} {
		cap := ""
		if i == 0 {
			cap = "good"
		}
		if resp, err := b.dispatch(rpc.BuildRequest(rpc.Call{PromiseID: uint32(i + 1), Cap: []byte(cap), Payload: []byte(f)})); resp != nil || err != nil {
			t.Fatalf("dispatch = %v %v", resp, err)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); !out.has("it said hi") && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if !out.has("it said hi") {
		t.Fatal("the chat thread never answered")
	}

	sock, got := answering(t)
	p.Stream = func(_, _, _, _ string) string { return sock }
	env := request(3, "terminal.open", "")
	if resp, err := b.dispatch(env); resp != nil || err != nil {
		t.Fatalf("dispatch = %v %v", resp, err)
	}
	for deadline := time.Now().Add(5 * time.Second); (len(got()) < 2 || !out.has("answer 3")) && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	in := got()
	if len(in) != 2 {
		t.Fatalf("the cell received %d envelopes, want the import and the request", len(in))
	}
	imp := in[0]
	if imp.PromiseID != importPromise || string(imp.Cap) != "good" {
		t.Errorf("the first envelope is promise %d with cap %q, want cloud's import with the bearer", imp.PromiseID, imp.Cap)
	}
	var f struct {
		Tag     string `json:"tag"`
		ID      int64  `json:"id"`
		Payload struct {
			Threads []struct {
				ThreadID string `json:"threadId"`
				Title    string `json:"title"`
				Messages []struct{ Role, Text string }
			} `json:"threads"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(imp.Payload, &f); err != nil {
		t.Fatal(err)
	}
	if f.Tag != "chatThreads.import" || f.ID != importRequestID || len(f.Payload.Threads) != 1 {
		t.Fatalf("the import is %s", imp.Payload)
	}
	th := f.Payload.Threads[0]
	if th.ThreadID != thread || th.Title != "Echo" || len(th.Messages) != 2 ||
		th.Messages[0].Text != "say hi" || th.Messages[1].Role != "assistant" || th.Messages[1].Text != "it said hi" {
		t.Errorf("the import carries %+v, want the thread under its id with what was said", th)
	}
	if in[1].PromiseID != 3 || string(in[1].Payload) != string(rpc.Call{}.Payload)+`{"_tag":"Request","id":3,"tag":"terminal.open","payload":{}}` {
		t.Errorf("the second envelope is promise %d %s, want the request that needed the cell", in[1].PromiseID, in[1].Payload)
	}
	if !out.has("answer 3") || out.has(fmt.Sprintf("answer %d", importPromise)) {
		t.Errorf("the client was pushed %v: want the request's answer and not the import's", out.got)
	}
}
