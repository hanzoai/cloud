package studio

// studio_test.go — the whole mounted /v1/studio surface on a real durable engine,
// with the Library, the jobs board, the model plane and a render worker standing
// in for their processes. The worker speaks the render lane's real door.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/orm/pgtest"
	tasks "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/kit"
)

var testCfg = zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true}

// ── the Library ──────────────────────────────────────────────────────────

type stored struct {
	org, owner string
	item       Item
	data       []byte
	ready      bool
	key        string
}

// fakeLibrary keeps items in memory under the caller ctx states, and serves their
// bytes and their uploads over one HTTP server, as the Library and S3 do.
type fakeLibrary struct {
	mu    sync.Mutex
	items map[string]*stored
	srv   *httptest.Server
	n     int
}

func newLibrary(t *testing.T) *fakeLibrary {
	l := &fakeLibrary{items: map[string]*stored{}}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/")
		it := l.items[id]
		if it == nil {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write(it.data)
		case http.MethodPut:
			it.data, _ = io.ReadAll(r.Body)
		}
	}))
	t.Cleanup(l.srv.Close)
	return l
}

func (l *fakeLibrary) add(org, owner string, in libraryIn, data []byte, ready bool) Item {
	for _, s := range l.items {
		if s.org == org && s.owner == owner && in.Key != "" && s.key == in.Key {
			return s.item
		}
	}
	l.n++
	id := fmt.Sprintf("lib_test%06d", l.n)
	it := Item{ID: id, Kind: in.Kind, Source: "studio", Name: in.Name, Mime: in.Mime, Bytes: int64(len(data)),
		Width: in.Width, Height: in.Height, Owner: owner, Ref: in.Ref, Origin: in.Origin, URL: l.srv.URL + "/" + id}
	l.items[id] = &stored{org: org, owner: owner, item: it, data: data, ready: ready, key: in.Key}
	return it
}

func (l *fakeLibrary) put(ctx context.Context, in libraryIn, data []byte) (Item, error) {
	who := zip.CallerOf(ctx)
	if who.Org == "" || who.User == "" {
		return Item{}, zip.ErrForbidden("no person")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.add(who.Org, who.User, in, data, true), nil
}

func (l *fakeLibrary) reserve(ctx context.Context, in libraryIn, size int64) (Item, upload, error) {
	who := zip.CallerOf(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	it := l.add(who.Org, who.User, in, nil, false)
	return it, upload{URL: l.srv.URL + "/" + it.ID, Headers: map[string]string{"Content-Type": in.Mime}}, nil
}

func (l *fakeLibrary) commit(ctx context.Context, id string) (Item, error) {
	who := zip.CallerOf(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.items[id]
	if s == nil || s.org != who.Org || s.owner != who.User || len(s.data) == 0 {
		return Item{}, zip.ErrNotFound("no such upload")
	}
	s.ready = true
	s.item.Bytes = int64(len(s.data))
	if w, h, ok := dims(s.data); ok {
		s.item.Width, s.item.Height = w, h
	}
	return s.item, nil
}

func (l *fakeLibrary) get(ctx context.Context, id string) (Item, error) {
	who := zip.CallerOf(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.items[id]
	if s == nil || !s.ready || s.org != who.Org || (s.owner != who.User && !s.item.Shared) {
		return Item{}, zip.ErrNotFound("item not found")
	}
	return s.item, nil
}

// ── the jobs board ───────────────────────────────────────────────────────

type fakeJobs struct {
	mu   sync.Mutex
	last map[string]jobIn
	who  map[string]string
}

func (j *fakeJobs) track(ctx context.Context, in jobIn) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.last[in.Ref] = in
	j.who[in.Ref] = zip.CallerOf(ctx).Org + "/" + zip.CallerOf(ctx).User
	return nil
}

// ── the model plane ──────────────────────────────────────────────────────

type model struct {
	mu    sync.Mutex
	reply func(req *infer.ChatRequest) string
	calls atomic.Int32
	last  *infer.ChatRequest
	// billed is the nano-USD the meter says each answer was charged, as the
	// model API priced it; 0 answers unpriced.
	billed int64
}

func (m *model) ChatCompletion(_ context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	m.calls.Add(1)
	m.mu.Lock()
	m.last = req
	fn, billed := m.reply, m.billed
	m.mu.Unlock()
	resp := &infer.ChatResponse{Content: fn(req), TotalTokens: 1200}
	if billed > 0 {
		resp.Billed = &billed
	}
	return resp, nil
}

func (m *model) Embed(context.Context, *infer.EmbedRequest) ([][]float32, error) { return nil, nil }

func (m *model) Rerank(context.Context, *infer.RerankRequest) ([]float64, error) { return nil, nil }

// png is a real PNG of w×h.
func png(w, h int) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(kind string, data []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(kind)
		b.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), data...)))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 8, 2
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	return b.Bytes()
}

// media answers the ai app's media routes: an image is a PNG, speech an MP3.
type media struct {
	calls     atomic.Int32
	mu        sync.Mutex
	lastModel string
}

func (m *media) buy(ctx context.Context, in *client.MediaIn) (*client.Chat, error) {
	m.calls.Add(1)
	var top struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(in.Request, &top)
	m.mu.Lock()
	m.lastModel = top.Model
	m.mu.Unlock()
	switch in.Path {
	case "/v1/images/generations":
		b, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(png(1024, 1024))}}})
		return &client.Chat{Status: 200, Body: b}, nil
	case "/v1/audio/speech":
		return &client.Chat{Status: 200, Body: append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0}, 64)...)}, nil
	}
	return &client.Chat{Status: 404, Body: []byte(`{"error":{"message":"no route"}}`)}, nil
}

// ── the rig ──────────────────────────────────────────────────────────────

type rig struct {
	t     *testing.T
	app   *zip.App
	st    *state
	lib   *fakeLibrary
	jobs  *fakeJobs
	ai    *model
	media *media
	dir   string

	mu  sync.Mutex
	eng *tasks.Embedded
}

const worker = "app:hanzo/render-test"

func mount(t *testing.T) *rig {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("STUDIO_RENDER_WORKERS", worker)
	r := &rig{t: t, app: app, lib: newLibrary(t), jobs: &fakeJobs{last: map[string]jobIn{}, who: map[string]string{}},
		ai: &model{reply: func(*infer.ChatRequest) string { return "ok" }}, media: &media{}, dir: t.TempDir()}
	r.start(t)
	w := wiring{lib: r.lib, jobs: r.jobs, fleet: func(context.Context, string) ([]tasks.StandaloneActivity, error) { return nil, nil },
		gpu: gpuLane{}, buy: r.media.buy, engine: r.engine}
	if err := useWith(app, cloud.Deps{AI: r.ai}, w); err != nil {
		t.Fatalf("mount: %v", err)
	}
	r.st = mounted.State
	r.st.poll = 20 * time.Millisecond
	waitFor(t, "the run worker", func() bool { _, err := r.st.work.client(); return err == nil })
	t.Cleanup(func() {
		_ = Shutdown(context.Background())
		r.stop()
	})
	return r
}

func (r *rig) engine() *tasks.Embedded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.eng
}

func (r *rig) start(t *testing.T) {
	eng, err := tasks.Embed(context.Background(), tasks.EmbedConfig{Address: filepath.Join(kit.Sockets(t), "t.sock"), DataDir: r.dir})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	r.mu.Lock()
	r.eng = eng
	r.mu.Unlock()
}

func (r *rig) stop() {
	r.mu.Lock()
	eng := r.eng
	r.eng = nil
	r.mu.Unlock()
	if eng != nil {
		_ = eng.Stop(context.Background())
	}
}

// restart is the process dying mid-run: the worker and the engine go down, and a
// new engine over the same store comes up with a new worker.
func (r *rig) restart(t *testing.T) {
	r.st.work.stop()
	r.stop()
	r.start(t)
	r.st.work = newDurable(r.st, r.engine)
	r.st.cpu = cpuLane{engine: r.engine}
	if err := r.st.work.attach(r.engine()); err != nil {
		t.Fatalf("reattach: %v", err)
	}
}

// call is one request as person user of org; org "" is no principal.
func (r *rig) call(method, path, org, user string, body any) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", user)
	}
	resp, err := r.app.Test(req, testCfg)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (r *rig) run(want int, method, path, org, user string, body any) Run {
	r.t.Helper()
	code, raw := r.call(method, path, org, user, body)
	if code != want {
		r.t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
	}
	var v Run
	if want/100 == 2 {
		if err := json.Unmarshal(raw, &v); err != nil {
			r.t.Fatalf("decode %s: %v", raw, err)
		}
	}
	return v
}

// settle waits for a run to end.
func (r *rig) settle(org, user, id string) Run {
	r.t.Helper()
	var v Run
	waitFor(r.t, "run "+id+" to end", func() bool {
		v = r.run(200, http.MethodGet, prefix+"/runs/"+id, org, user, nil)
		return v.Status == succeeded || v.Status == failed || v.Status == canceled
	})
	return v
}

// render is a render worker on the door: it claims one job, renders a PNG of the
// job's size (or bytes standing in for a PDF, a deck or an MP4), uploads it where
// the door says, and completes. hold, when set, is waited on after the claim.
func (r *rig) render(hold <-chan struct{}) (string, bool) {
	r.t.Helper()
	code, raw := r.call(http.MethodPost, prefix+"/render/namespaces/"+laneNS+"/activities/claim", "studio-render", worker,
		map[string]any{"identity": "w1", "leaseSeconds": 60})
	if code == http.StatusNoContent {
		return "", false
	}
	if code != 200 {
		r.t.Fatalf("claim = %d: %s", code, raw)
	}
	var a tasks.StandaloneActivity
	_ = json.Unmarshal(raw, &a)
	if hold != nil {
		<-hold
	}
	b, _ := json.Marshal(a.Input)
	var in struct {
		Op   string         `json:"op"`
		With map[string]any `json:"with"`
	}
	_ = json.Unmarshal(b, &in)
	p := Part{Op: in.Op, With: in.With}
	mime, _ := output(p)
	data := []byte("%PDF-1.7 rendered")
	if mime == "image/png" {
		data = png(int(num(in.With, "width", 1)), int(num(in.With, "height", 1)))
	}
	base := prefix + "/render/namespaces/" + laneNS + "/activities/" + a.Execution.WorkflowId + "/" + a.Execution.RunId
	sum := sha256.Sum256(data)
	code, raw = r.call(http.MethodPost, base+"/output", "studio-render", worker,
		map[string]any{"mime": mime, "bytes": len(data), "sha256": hex.EncodeToString(sum[:]), "name": "out"})
	if code != 200 {
		r.t.Fatalf("output = %d: %s", code, raw)
	}
	var up upload
	_ = json.Unmarshal(raw, &up)
	req, _ := http.NewRequest(http.MethodPut, up.URL, bytes.NewReader(data))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		r.t.Fatalf("put: %v", err)
	}
	_ = resp.Body.Close()
	code, raw = r.call(http.MethodPost, base+"/complete", "studio-render", worker,
		map[string]any{"result": map[string]any{"mime": mime, "bytes": len(data)}, "identity": "w1"})
	if code != 200 {
		r.t.Fatalf("complete = %d: %s", code, raw)
	}
	return a.Execution.WorkflowId, true
}

// renderOne waits for a job and renders it.
func (r *rig) renderOne() string {
	r.t.Helper()
	var id string
	waitFor(r.t, "a render job", func() bool {
		var ok bool
		id, ok = r.render(nil)
		return ok
	})
	return id
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// events reads a run's whole event stream.
func (r *rig) events(org, user, id string, after int) []map[string]any {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("%s/runs/%s/events?after=%d", prefix, id, after), nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", user)
	resp, err := r.app.Test(req, testCfg)
	if err != nil {
		r.t.Fatalf("events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		r.t.Fatalf("events = %d: %s", resp.StatusCode, raw)
	}
	var out []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	ev := map[string]any{}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			ev["id"] = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			ev["event"] = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var d map[string]any
			_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d)
			ev["data"] = d
		case line == "" && ev["event"] != nil:
			out = append(out, ev)
			ev = map[string]any{}
		}
	}
	return out
}

// ── the tests ────────────────────────────────────────────────────────────

func TestTheCatalogValidates(t *testing.T) {
	if n := len(catalog()); n < 10 {
		t.Fatalf("catalog has %d templates", n)
	}
	for _, id := range []string{"image", "poster", "document", "slides", "voice", "video.edit", "film"} {
		if _, ok := catalogTemplate(id); !ok {
			t.Errorf("no catalog template %s", id)
		}
	}
}

func TestAnImageRunMakesALibraryItemAndStreams(t *testing.T) {
	r := mount(t)
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "image", "input": map[string]any{"prompt": "a lighthouse"}})
	if v.Status != queued || v.EstimateCents != 8 || v.Creator != "ann" || v.Org != "acme" {
		t.Fatalf("created run = %+v", v)
	}
	done := r.settle("acme", "ann", v.ID)
	if done.Status != succeeded || len(done.Outputs) != 1 || done.Outputs[0].Width != 1024 || done.ChargedCents != 8 {
		t.Fatalf("finished run = %+v", done)
	}
	r.media.mu.Lock()
	gotModel := r.media.lastModel
	r.media.mu.Unlock()
	if gotModel != "iro" {
		t.Fatalf("image.generate default model = %q, want iro", gotModel)
	}
	it := r.lib.items[done.Outputs[0].ID]
	if it.owner != "ann" || it.org != "acme" || it.item.Ref != v.ID || it.item.Origin != "/studio?run="+v.ID {
		t.Fatalf("library item = %+v", it)
	}
	evs := r.events("acme", "ann", v.ID, 0)
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e["event"].(string))
	}
	got := strings.Join(kinds, ",")
	if !strings.Contains(got, "run") || !strings.Contains(got, "step") || !strings.Contains(got, "asset") || !strings.HasSuffix(got, "done") {
		t.Fatalf("events = %s", got)
	}
	// A reconnect after the last event gets the rest and nothing twice.
	last := 0
	_, _ = fmt.Sscan(evs[len(evs)-2]["id"].(string), &last)
	tail := r.events("acme", "ann", v.ID, last)
	if len(tail) != 1 || tail[0]["event"] != "done" {
		t.Fatalf("resume after %d = %v", last, tail)
	}
	if j := r.jobs.last[v.ID]; j.State != "succeeded" || len(j.Outputs) != 1 || j.Cost != 8 || r.jobs.who[v.ID] != "acme/ann" {
		t.Fatalf("jobs board = %+v as %s", j, r.jobs.who[v.ID])
	}
}

func TestAPosterRendersAtItsSizeOnTheRenderLane(t *testing.T) {
	r := mount(t)
	r.ai.reply = func(*infer.ChatRequest) string {
		return "Here it is:\n```html\n<!doctype html><html><body>JAZZ</body></html>\n```"
	}
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "poster",
		"input": map[string]any{"brief": "Jazz night", "width": 1080, "height": 1350}})
	r.renderOne()
	done := r.settle("acme", "ann", v.ID)
	if done.Status != succeeded || len(done.Outputs) != 1 {
		t.Fatalf("poster = %+v", done)
	}
	if o := done.Outputs[0]; o.Width != 1080 || o.Height != 1350 || o.Mime != "image/png" {
		t.Fatalf("poster output = %+v", o)
	}
}

// A chat step costs what the meter charged for it — the model API's price for the
// answer — and not a rate of Studio's own: 1,200 tokens Studio priced at $2 a
// million (one cent) were billed $0.1235 by the model that answered them, and the
// run said one cent.
func TestAChatStepCostsWhatTheModelWasBilled(t *testing.T) {
	r := mount(t)
	r.ai.billed = 123_456_789 // $0.123456789
	r.ai.reply = func(*infer.ChatRequest) string {
		return "```html\n<!doctype html><html><body>JAZZ</body></html>\n```"
	}
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "poster",
		"input": map[string]any{"brief": "Jazz night", "width": 1080, "height": 1350}})
	r.renderOne()
	done := r.settle("acme", "ann", v.ID)
	if done.Status != succeeded {
		t.Fatalf("poster = %+v", done)
	}
	for _, st := range done.Steps {
		if st.ID == "design" && st.CostCents != 13 {
			t.Fatalf("the chat step cost %d cents, want 13: what its answer was billed, rounded up", st.CostCents)
		}
	}
}

func TestAnotherOrgCannotReadARun(t *testing.T) {
	r := mount(t)
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "image", "input": map[string]any{"prompt": "x"}})
	r.settle("acme", "ann", v.ID)
	for _, path := range []string{"/runs/" + v.ID, "/runs/" + v.ID + "/versions", "/runs/" + v.ID + "/events"} {
		if code, raw := r.call(http.MethodGet, prefix+path, "globex", "gus", nil); code != 404 {
			t.Errorf("globex GET %s = %d %s", path, code, raw)
		}
	}
	if code, _ := r.call(http.MethodPost, prefix+"/runs/"+v.ID+"/cancel", "globex", "gus", nil); code != 404 {
		t.Errorf("globex cancel = %d", code)
	}
	// Inside the org, a run is its creator's until shared.
	if code, _ := r.call(http.MethodGet, prefix+"/runs/"+v.ID, "acme", "bob", nil); code != 404 {
		t.Errorf("bob read ann's unshared run: %d", code)
	}
	shared := true
	r.run(200, http.MethodPatch, prefix+"/runs/"+v.ID, "acme", "ann", RunPatch{Shared: &shared})
	if code, _ := r.call(http.MethodGet, prefix+"/runs/"+v.ID, "acme", "bob", nil); code != 200 {
		t.Errorf("bob cannot read ann's shared run: %d", code)
	}
	if code, _ := r.call(http.MethodPatch, prefix+"/runs/"+v.ID, "acme", "bob", RunPatch{Shared: new(bool)}); code != 404 {
		t.Errorf("bob unshared ann's run: %d", code)
	}
	if code, _ := r.call(http.MethodGet, prefix+"/runs", "", "", nil); code != 401 && code != 403 {
		t.Errorf("anonymous list = %d", code)
	}
	page := RunPage{}
	_, raw := r.call(http.MethodGet, prefix+"/runs", "globex", "gus", nil)
	_ = json.Unmarshal(raw, &page)
	if len(page.Data) != 0 {
		t.Errorf("globex lists %d runs", len(page.Data))
	}
}

func TestARunKilledMidStepResumes(t *testing.T) {
	r := mount(t)
	r.ai.reply = func(*infer.ChatRequest) string { return "<html><body>A</body></html>" }
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "poster",
		"input": map[string]any{"brief": "x", "width": 400, "height": 500}})
	// The render step is enqueued: the chat step finished first.
	waitFor(t, "the render job", func() bool {
		st, ok, _ := r.st.stepRow(scoped(context.Background(), "acme"), v.ID, "render")
		return ok && st.Status == running
	})
	r.restart(t)
	r.renderOne()
	done := r.settle("acme", "ann", v.ID)
	if done.Status != succeeded || done.Outputs[0].Width != 400 {
		t.Fatalf("resumed run = %+v", done)
	}
	if n := r.ai.calls.Load(); n != 1 {
		t.Fatalf("the finished design step ran %d times", n)
	}
}

func TestCancelAndRetryKeepWhatFinished(t *testing.T) {
	r := mount(t)
	r.ai.reply = func(*infer.ChatRequest) string { return "<html><body>B</body></html>" }
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "poster",
		"input": map[string]any{"brief": "x", "width": 300, "height": 300}})
	waitFor(t, "the render job", func() bool {
		st, ok, _ := r.st.stepRow(scoped(context.Background(), "acme"), v.ID, "render")
		return ok && st.Status == running
	})
	c := r.run(200, http.MethodPost, prefix+"/runs/"+v.ID+"/cancel", "acme", "ann", nil)
	if c.Status != canceled {
		t.Fatalf("cancelled run = %s", c.Status)
	}
	if code, _ := r.call(http.MethodPost, prefix+"/runs/"+v.ID+"/retry", "acme", "bob", nil); code != 404 {
		t.Fatalf("bob retried ann's run: %d", code)
	}
	r.run(200, http.MethodPost, prefix+"/runs/"+v.ID+"/retry", "acme", "ann", nil)
	r.renderOne()
	done := r.settle("acme", "ann", v.ID)
	if done.Status != succeeded {
		t.Fatalf("retried run = %+v", done)
	}
	if n := r.ai.calls.Load(); n != 1 {
		t.Fatalf("the design step ran %d times across the retry", n)
	}
	if code, _ := r.call(http.MethodPost, prefix+"/runs/"+v.ID+"/retry", "acme", "ann", nil); code != 409 {
		t.Fatalf("retry of a succeeded run = %d", code)
	}
}

func TestVersionsAndIdempotency(t *testing.T) {
	r := mount(t)
	a := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "image", "input": map[string]any{"prompt": "v1"}})
	r.settle("acme", "ann", a.ID)
	b := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"parent": a.ID, "input": map[string]any{"prompt": "v2"}})
	if b.Version != 2 || b.Root != a.ID || b.Parent != a.ID || b.Input["prompt"] != "v2" {
		t.Fatalf("version 2 = %+v", b)
	}
	r.settle("acme", "ann", b.ID)
	_, raw := r.call(http.MethodGet, prefix+"/runs/"+b.ID+"/versions", "acme", "ann", nil)
	var page RunPage
	_ = json.Unmarshal(raw, &page)
	if len(page.Data) != 2 || page.Data[0].ID != a.ID {
		t.Fatalf("versions = %s", raw)
	}
	// One Idempotency-Key from one person starts one run, however often it is sent.
	keyed := func() string {
		req := httptest.NewRequest(http.MethodPost, prefix+"/runs", strings.NewReader(`{"template":"image","input":{"prompt":"k"}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", "ann")
		req.Header.Set("Idempotency-Key", "k-1")
		resp, err := r.app.Test(req, testCfg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var v Run
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return v.ID
	}
	first, second := keyed(), keyed()
	if first == "" || first != second {
		t.Fatalf("one Idempotency-Key started %q and %q", first, second)
	}
	r.settle("acme", "ann", first)
}

func TestAWalletThatCannotPayIsRefusedBeforeAnythingRuns(t *testing.T) {
	r := mount(t)
	code, raw := r.call(http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"graph": map[string]any{"steps": []any{
		map[string]any{"id": "a", "op": "chat", "with": map[string]any{"prompt": "{{steps.b.text}}"}},
		map[string]any{"id": "b", "op": "chat", "with": map[string]any{"prompt": "{{steps.a.text}}"}},
	}}})
	if code != 422 {
		t.Fatalf("a cycle = %d %s", code, raw)
	}
	code, raw = r.call(http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "image", "input": map[string]any{}})
	if code != 422 || !strings.Contains(string(raw), "prompt") {
		t.Fatalf("a missing input = %d %s", code, raw)
	}
	code, raw = r.call(http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "image", "input": map[string]any{"prompt": "x", "ref": "lib_somebodyelses"}})
	if code != 422 {
		t.Fatalf("an item the person cannot read = %d %s", code, raw)
	}
}

func TestSavedThingsAreTheirCreatorsUntilShared(t *testing.T) {
	r := mount(t)
	code, raw := r.call(http.MethodPost, prefix+"/brands", "acme", "ann", map[string]any{"name": "Acme",
		"tokens": map[string]any{"color": map[string]any{"primary": map[string]any{"$value": "#ff5a1f", "$type": "color"}}}})
	if code != 201 {
		t.Fatalf("brand = %d %s", code, raw)
	}
	var b Brand
	_ = json.Unmarshal(raw, &b)
	if code, _ := r.call(http.MethodGet, prefix+"/brands/"+b.ID, "acme", "bob", nil); code != 404 {
		t.Fatalf("bob read ann's brand: %d", code)
	}
	if code, _ := r.call(http.MethodGet, prefix+"/brands/"+b.ID, "globex", "ann", nil); code != 404 {
		t.Fatalf("another org read ann's brand: %d", code)
	}
	// A run reads a brand's tokens.
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"brand": b.ID, "graph": map[string]any{"steps": []any{
		map[string]any{"id": "say", "op": "chat", "with": map[string]any{"prompt": "use {{brand.color.primary}}"}},
	}}})
	r.settle("acme", "ann", v.ID)
	if got := r.ai.last.Prompt; got != "use #ff5a1f" {
		t.Fatalf("prompt = %q", got)
	}
	code, raw = r.call(http.MethodPost, prefix+"/documents", "acme", "ann", map[string]any{"kind": "deck", "title": "Q3",
		"ir": map[string]any{"slides": []any{map[string]any{"layout": "title", "title": "Q3"}}}})
	if code != 201 {
		t.Fatalf("deck = %d %s", code, raw)
	}
	var d Document
	_ = json.Unmarshal(raw, &d)
	v = r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "slides.render", "input": map[string]any{"document": d.ID}})
	r.renderOne()
	done := r.settle("acme", "ann", v.ID)
	if done.Status != succeeded || done.Outputs[0].Kind != "slides" {
		t.Fatalf("deck run = %+v", done)
	}
	if code, raw := r.call(http.MethodPost, prefix+"/runs", "acme", "bob", map[string]any{"template": "slides.render", "input": map[string]any{"document": d.ID}}); code != 422 {
		t.Fatalf("bob rendered ann's deck: %d %s", code, raw)
	}
}

func TestTheDoorAdmitsTheRenderWorkerAlone(t *testing.T) {
	r := mount(t)
	for _, who := range []string{"ann", "app:hanzo/other"} {
		if code, _ := r.call(http.MethodPost, prefix+"/render/namespaces/"+laneNS+"/activities/claim", "acme", who, map[string]any{}); code != 403 {
			t.Errorf("%s claimed a render job: %d", who, code)
		}
	}
	if code, _ := r.call(http.MethodPost, prefix+"/render/namespaces/gpu-jobs/activities/claim", "studio-render", worker, map[string]any{}); code != 404 {
		t.Errorf("the door served another lane: %d", code)
	}
}

func TestChatPlansAndStartsRuns(t *testing.T) {
	r := mount(t)
	r.ai.reply = func(*infer.ChatRequest) string {
		return "I'll make that image.\n" + marker + "\n[{\"template\": \"image\", \"input\": {\"prompt\": \"a red fox\"}, \"title\": \"Fox\"}]"
	}
	req := httptest.NewRequest(http.MethodPost, prefix+"/chat", strings.NewReader(`{"message":"make me a fox"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "ann")
	resp, err := r.app.Test(req, testCfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	body := string(raw)
	for _, want := range []string{"event: thread", "event: delta", "event: run", "event: done", "I'll make that image."} {
		if !strings.Contains(body, want) {
			t.Fatalf("chat stream lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "a red fox\"}, \"title\"") {
		t.Fatalf("the plan leaked into the prose:\n%s", body)
	}
	_, raw = r.call(http.MethodGet, prefix+"/threads", "acme", "ann", nil)
	var threads ThreadPage
	_ = json.Unmarshal(raw, &threads)
	if len(threads.Data) != 1 {
		t.Fatalf("threads = %s", raw)
	}
	if code, _ := r.call(http.MethodGet, prefix+"/threads/"+threads.Data[0].ID, "acme", "bob", nil); code != 404 {
		t.Fatalf("bob read ann's thread: %d", code)
	}
}

// The same run on PostgreSQL — the store production opens — and row-level
// security as the second guard alone: a raw statement as Studio's own login, with
// no condition of the builder's, reads only the org its transaction names.
func TestPostgresRunsAndHoldsTheOrg(t *testing.T) {
	if pg == nil {
		t.Skip("no PostgreSQL on this machine: " + why)
	}
	dir := strings.TrimPrefix(strings.Fields(pg.DSN("", ""))[0], "host=")
	t.Setenv("CLOUD_SQL_ADMIN_DSN", "postgres://"+pgtest.Super+"@localhost:5432/postgres?sslmode=disable&host="+url.QueryEscape(dir))
	r := mount(t)
	r.ai.reply = func(*infer.ChatRequest) string { return "<html><body>PG</body></html>" }
	v := r.run(201, http.MethodPost, prefix+"/runs", "acme", "ann", map[string]any{"template": "poster",
		"input": map[string]any{"brief": "x", "width": 640, "height": 800}})
	r.renderOne()
	done := r.settle("acme", "ann", v.ID)
	if done.Status != succeeded || done.Outputs[0].Width != 640 || done.Outputs[0].Height != 800 {
		t.Fatalf("poster on postgres = %+v", done)
	}
	if code, _ := r.call(http.MethodGet, prefix+"/runs/"+v.ID, "globex", "gus", nil); code != 404 {
		t.Fatalf("globex read acme's run on postgres: %d", code)
	}
	login, err := pg.Open("studio", "studio")
	if err != nil {
		t.Fatalf("open as the store's login: %v", err)
	}
	defer func() { _ = login.Close() }()
	for org, want := range map[string]int{"acme": 1, "globex": 0} {
		tx, err := login.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`SELECT set_config('role', 'studio_tenant', true), set_config('app.org', $1, true)`, org); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM studio.runs WHERE id = $1`, v.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback()
		if n != want {
			t.Fatalf("as %s the login reads %d of acme's run, want %d", org, n, want)
		}
	}
}
