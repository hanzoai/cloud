package train

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// fakeObjects stands in for S3: a grant is a URL naming the key, and an object is
// stored, under the key it was sealed with, once the test says its bytes were put.
type fakeObjects struct {
	mu      sync.Mutex
	put     map[string]string // key → the sha256 of the bytes put there
	sealed  map[string]string // key → the key id the bytes were sealed under
	granted map[string]string // key → the key id the grant named
	mimes   map[string]string // key → the content type the grant bound
	ended   map[string]string
	removed []string
	aborted []string
	served  map[string]string // models bucket key → the sha256 of the bytes copied there
	// bytes are what write wrote, by key.
	bytes map[string][]byte
}

func newFakeObjects() *fakeObjects {
	return &fakeObjects{put: map[string]string{}, sealed: map[string]string{}, granted: map[string]string{}, mimes: map[string]string{}, ended: map[string]string{}, served: map[string]string{}}
}

func (f *fakeObjects) grant(_ context.Context, key, sum string, size int64, mime, seal string) (*upload, error) {
	cs, err := checksum(sum)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.granted[key], f.mimes[key] = seal, mime
	f.mu.Unlock()
	if size > partBytes {
		return &upload{Method: "PUT", Parts: []part{{Number: 1, Size: partBytes, URL: "https://s3.test/p1"}, {Number: 2, Offset: partBytes, Size: size - partBytes, URL: "https://s3.test/p2"}}, Expires: 1, id: "up-" + key}, nil
	}
	return &upload{Method: "PUT", URL: "https://s3.test/hanzo-train/" + key, Headers: map[string]string{"x-amz-checksum-sha256": cs}, Expires: 1}, nil
}

func (f *fakeObjects) verify(_ context.Context, key, sum string, _ int64, id, seal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended[key] = id
	got, ok := f.put[key]
	if !ok {
		return errMissing
	}
	if f.sealed[key] != seal {
		delete(f.put, key)
		return errPlain
	}
	if got != sum {
		return errMismatch
	}
	return nil
}

func (f *fakeObjects) serve(_ context.Context, key, org, sum string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.put[key] != sum {
		return errMissing
	}
	f.served["capabilities/"+org+"/"+sum] = sum
	return nil
}

func (f *fakeObjects) link(_ context.Context, key string) (string, error) {
	return "https://s3.test/get/" + key, nil
}

func (f *fakeObjects) remove(_ context.Context, key string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.put, key)
	f.removed = append(f.removed, key)
	f.aborted = append(f.aborted, ids...)
	return nil
}

func (f *fakeObjects) abort(_ context.Context, _ string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborted = append(f.aborted, ids...)
	return nil
}

func (f *fakeObjects) write(_ context.Context, key string, data []byte, mime, seal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256(data)
	f.put[key], f.sealed[key], f.mimes[key] = hex.EncodeToString(sum[:]), seal, mime
	if f.bytes == nil {
		f.bytes = map[string][]byte{}
	}
	f.bytes[key] = bytes.Clone(data)
	return nil
}

func (f *fakeObjects) read(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bytes[key]
	if !ok {
		return nil, errMissing
	}
	return bytes.Clone(b), nil
}

// store puts bytes at key, sealed under the key of the org the key is under.
func (f *fakeObjects) store(key, sum string) {
	org, _, _ := strings.Cut(key, "/")
	f.storeSealed(key, sum, keyID(org))
}

func (f *fakeObjects) storeSealed(key, sum, seal string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put[key], f.sealed[key] = sum, seal
}

func (f *fakeObjects) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.put[key]
	return ok
}

// books stands in for the ledger: a balance a gate weighs against, and every
// debit the app emitted.
type books struct {
	mu sync.Mutex
	// balance is micro-USD; below zero is unbounded.
	balance int64
	// unread says the balance cannot be read.
	unread bool
	// capped says a spend cap refuses whatever is asked.
	capped bool
	// asked is every amount a gate weighed.
	asked  []int64
	debits []booked
	// meet runs before a gate weighs, outside mu, and a test sets it to hold a weighing
	// at the moment two replicas could be weighing one wallet together.
	meet func()
}

type booked struct {
	payer string
	kind  string
	usage meter.Usage
}

func (b *books) afford(_ context.Context, payer account.Account, _ string, _ bool, micros int64) error {
	b.mu.Lock()
	meet := b.meet
	b.mu.Unlock()
	if meet != nil {
		meet()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, micros)
	if payer.Zero() {
		return cloud.ErrNoLedger
	}
	if b.unread {
		return errors.New("ledger unreachable")
	}
	if b.capped {
		return meter.ErrSpendCapExceeded
	}
	if b.balance >= 0 && micros > b.balance-b.spentLocked("") {
		return meter.ErrInsufficientBalance
	}
	return nil
}

func (b *books) debit(payer account.Account, kind string, u meter.Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.debits = append(b.debits, booked{payer: payer.String(), kind: kind, usage: u})
}

// spent is what was debited, micro-USD, of one kind or all.
func (b *books) spent(kind string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spentLocked(kind)
}

func (b *books) spentLocked(kind string) int64 {
	var n int64
	for _, d := range b.debits {
		if kind == "" || d.kind == kind {
			n += d.usage.AmountMicros
		}
	}
	return n
}

func (b *books) of(kind string) []booked {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []booked
	for _, d := range b.debits {
		if d.kind == kind {
			out = append(out, d)
		}
	}
	return out
}

type rig struct {
	t     *testing.T
	app   *zip.App
	objs  *fakeObjects
	books *books
	trail *audit.Recorder
	st    *state
	svc   *cloud.Service[state]
}

func mustNS(t *testing.T, org string) namespace.Namespace {
	t.Helper()
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func mount(t *testing.T, eng string) *rig {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	base := cloud.Base{Log: luxlog.New("test"), DataDir: t.TempDir(), Bill: cloud.NewMeter(cloud.Deps{}, "train")}
	objs := newFakeObjects()
	bk := &books{balance: -1}
	trail, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trail.Close() })
	st := state{
		stores:  cloud.NewOrgStoreAt(base, subsystem, openStore(testMaster)),
		objects: objs,
		seal:    func(_ context.Context, _ *store, org string) (string, error) { return keyID(org), nil },
		engine:  &engine{base: eng, http: http.DefaultClient},
		bell:    newBell(),
		audit:   trail,
		afford:  bk.afford,
		debit:   bk.debit,
		payers:  &lock.Keyed{},
		ttl:     600,
		retain:  30 * 24 * 3600,
		cap:     2,
		room:    4,
	}
	claimWait = 0
	svc := &cloud.Service[state]{Base: base, State: st}
	routes(app, svc)
	t.Cleanup(func() { _ = st.stores.CloseAll() })
	return &rig{t: t, app: app, objs: objs, books: bk, trail: trail, st: &svc.State, svc: svc}
}

// clock pins the app's clock at a start the test moves with tick.
type clock struct{ now int64 }

func (c *clock) tick(secs int64) { c.now += secs }

func pin(t *testing.T) *clock {
	t.Helper()
	c := &clock{now: 1_800_000_000}
	was := unix
	unix = func() int64 { return c.now }
	t.Cleanup(func() { unix = was })
	return c
}

// store is the org's train.db, for reading what a test cannot see over the wire.
func (r *rig) store(org string) *store {
	r.t.Helper()
	st, err := r.st.stores.For(mustNS(r.t, org))
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

func (r *rig) do(method, path, org string, body any) (int, map[string]any) {
	r.t.Helper()
	return r.as(method, path, org, false, body)
}

// as is do with the caller a SuperAdmin or not.
func (r *rig) as(method, path, org string, super bool, body any) (int, map[string]any) {
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
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if super {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 60 * time.Second})
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func (r *rig) must(want int, method, path, org string, body any) map[string]any {
	r.t.Helper()
	code, m := r.as(method, path, org, true, body)
	if code != want {
		r.t.Fatalf("%s %s: %d %v, want %d", method, path, code, m, want)
	}
	return m
}

var supports = map[string]any{
	"adaptations": []string{"full", "readout"},
	"protect":     []string{"projection", "distillation"},
	"terms":       []string{"pairwise_margin", "hard_margin", "consistency"},
	"outputs":     []string{"capability", "checkpoint"},
}

func claimBody(machine string) map[string]any {
	return map[string]any{"machine": machine, "bases": []string{"kai"}, "devices": []string{"cpu"}, "supports": supports}
}

// platformClaim is Hanzo's executor's claim, from a GPU host.
func platformClaim(devices ...string) map[string]any {
	if len(devices) == 0 {
		devices = []string{"cuda"}
	}
	return map[string]any{"machine": "gpu-0", "platform": true, "bases": []string{"kai"}, "devices": devices, "supports": supports}
}

func job(machines ...string) map[string]any {
	return map[string]any{
		"base_model": "kai", "revision": "a7",
		"dataset":    map[string]any{"uri": "stage:t1"},
		"adaptation": map[string]any{"mode": "auto"},
		"protect":    map[string]any{"suites": []string{"typed_decisions"}, "projection": map[string]any{"strength": 0.5}, "distillation": true},
		"resources":  map[string]any{"machines": machines, "steps": 5},
		"evaluation": map[string]any{"suites": []string{"support_triage"}},
		"output":     map[string]any{"kind": "capability", "name": "triage"},
	}
}

// baseSHA is the base weights' SHA-256 a test's result names.
var baseSHA = strings.Repeat("ba", 32)

// route is a capability's route over one key, whatever its question.
func route(keys ...string) map[string]any { return map[string]any{"keys": keys} }

func str(m map[string]any, path ...string) string {
	var v any = m
	for _, p := range path {
		v = v.(map[string]any)[p]
	}
	return fmt.Sprint(v)
}

func TestAJobRunsFromClaimToPublish(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		j := r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx"))
		id := str(j, "id")
		if str(j, "status") != "queued" || str(j, "adaptation", "mode") != "full" || str(j, "adaptation", "chose", "mode") != "full" {
			t.Fatalf("created: %v", j)
		}
		if code, _ := r.do("GET", "/v1/train/jobs/"+id, "evil", nil); code != 404 {
			t.Fatalf("another org read the job: %d", code)
		}
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "evil", claimBody("dgx")); code != 204 {
			t.Fatalf("another org claimed: %d", code)
		}
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", claimBody("evo")); code != 204 {
			t.Fatalf("a machine the job does not name claimed it: %d", code)
		}
		a := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))
		task := a["task"].(map[string]any)
		lease, tid := str(task, "lease"), str(task, "id")
		if str(task, "role") != "lead" || !strings.HasPrefix(lease, "trl_") || str(a, "job", "status") != "running" {
			t.Fatalf("claim: %v", a)
		}
		path := "/v1/train/jobs/" + id + "/events"
		rep := func(events ...map[string]any) map[string]any {
			return r.must(200, "POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": events})
		}
		rep(map[string]any{"type": "status", "status": "running"},
			map[string]any{"type": "coordinator", "addr": "dgx.local:47100"},
			map[string]any{"type": "metric", "name": "nll", "step": 1, "value": 0.9},
			map[string]any{"type": "metric", "name": "nll", "step": 2, "value": 0.7},
			map[string]any{"type": "log", "line": "round 1"})
		if code, m := r.do("POST", path, "acme", map[string]any{"task": tid, "lease": "trl_forged", "events": []any{}}); code != 409 || str(m, "code") != "lease_lost" {
			t.Fatalf("a forged lease reported: %d %v", code, m)
		}
		if code, _ := r.do("POST", path, "evil", map[string]any{"task": tid, "lease": lease, "events": []any{}}); code != 404 {
			t.Fatalf("another org reported: %d", code)
		}
		rep(map[string]any{"type": "status", "status": "evaluating"})
		sum := strings.Repeat("ab", 32)
		reg := r.must(200, "POST", "/v1/train/jobs/"+id+"/artifacts", "acme",
			map[string]any{"task": tid, "lease": lease, "kind": "capability", "name": "triage", "sha256": sum, "size": 1024})
		if reg["stored"] != false || !strings.Contains(str(reg, "upload", "url"), "acme/"+sum) {
			t.Fatalf("grant: %v", reg)
		}
		res := map[string]any{"type": "result", "verdict": "accepted",
			"base":    map[string]any{"revision": "a7", "sha256": baseSHA},
			"suites":  []any{map[string]any{"suite": "typed_decisions", "role": "protected", "within": true}},
			"outputs": []any{map[string]any{"kind": "capability", "sha256": sum}}}
		if code, m := r.do("POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": []any{res}}); code != 409 || str(m, "code") != "artifact_missing" {
			t.Fatalf("a result before its output was stored: %d %v", code, m)
		}
		if code, m := r.do("POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": []any{map[string]any{"type": "artifact", "sha256": sum}}}); code != 409 || str(m, "code") != "artifact_missing" {
			t.Fatalf("an artifact event before the upload: %d %v", code, m)
		}
		r.objs.store("acme/"+sum, strings.Repeat("00", 32))
		if code, m := r.do("POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": []any{map[string]any{"type": "artifact", "sha256": sum}}}); code != 409 || str(m, "code") != "artifact_mismatch" {
			t.Fatalf("other bytes under the name: %d %v", code, m)
		}
		r.objs.store("acme/"+sum, sum)
		rep(map[string]any{"type": "artifact", "sha256": sum})
		out := rep(res)
		if str(out, "status") != "succeeded" {
			t.Fatalf("result: %v", out)
		}
		if code, m := r.do("POST", path, "acme", map[string]any{"task": tid, "lease": lease, "events": []any{res}}); code != 200 || str(m, "status") != "succeeded" {
			t.Fatalf("a resent result: %d %v", code, m)
		}
		j = r.must(200, "GET", "/v1/train/jobs/"+id, "acme", nil)
		if str(j, "status") != "succeeded" || str(j, "result", "verdict") != "accepted" || len(j["artifacts"].([]any)) != 1 {
			t.Fatalf("ended: %v", j)
		}
		ev := r.must(200, "GET", "/v1/train/jobs/"+id+"/events?after=0", "acme", nil)
		if n := len(ev["data"].([]any)); n != 8 || str(ev, "status") != "succeeded" {
			t.Fatalf("events: %d %v", n, ev)
		}
		ev = r.must(200, "GET", "/v1/train/jobs/"+id+"/events?after=7", "acme", nil)
		if n := len(ev["data"].([]any)); n != 1 {
			t.Fatalf("events after 7: %v", ev)
		}
		mx := r.must(200, "GET", "/v1/train/jobs/"+id+"/metrics", "acme", nil)
		if s := mx["data"].([]any)[0].(map[string]any); s["name"] != "nll" || len(s["points"].([]any)) != 2 {
			t.Fatalf("metrics: %v", mx)
		}
		arts := r.must(200, "GET", "/v1/train/jobs/"+id+"/artifacts", "acme", nil)
		if d := arts["data"].([]any)[0].(map[string]any); d["state"] != "stored" || !strings.Contains(fmt.Sprint(d["url"]), "acme/"+sum) {
			t.Fatalf("artifacts: %v", arts)
		}
		if code, _ := r.do("GET", "/v1/train/jobs/"+id+"/artifacts", "evil", nil); code != 404 {
			t.Fatalf("another org listed artifacts: %d", code)
		}
		p := r.must(200, "POST", "/v1/train/jobs/"+id+"/publish", "acme", map[string]any{"route": route("triage")})
		if str(p, "published", "name") != "triage" || str(p, "published", "sha256") != sum || p["published"].(map[string]any)["forced"] != false ||
			str(p, "published", "base_sha256") != baseSHA || str(p, "published", "route", "keys") != "[triage]" {
			t.Fatalf("publish: %v", p)
		}
		if code, m := r.do("POST", "/v1/train/jobs/"+id+"/cancel", "acme", nil); code != 409 || str(m, "code") != "terminal" {
			t.Fatalf("cancel after the end: %d %v", code, m)
		}
		// The same bytes again are already the org's: nothing to upload.
		j2 := r.must(200, "POST", "/v1/train/jobs", "acme", job("any"))
		a2 := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("any"))
		t2 := a2["task"].(map[string]any)
		reg = r.must(200, "POST", "/v1/train/jobs/"+str(j2, "id")+"/artifacts", "acme",
			map[string]any{"task": str(t2, "id"), "lease": str(t2, "lease"), "kind": "capability", "sha256": sum, "size": 1024})
		if reg["stored"] != true || reg["upload"] != nil {
			t.Fatalf("known bytes: %v", reg)
		}
	})
}

func TestARejectedJobPublishesOnlyByForce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		a := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))
		task := a["task"].(map[string]any)
		tid, lease := str(task, "id"), str(task, "lease")
		sum := strings.Repeat("cd", 32)
		r.must(200, "POST", "/v1/train/jobs/"+id+"/artifacts", "acme", map[string]any{"task": tid, "lease": lease, "kind": "capability", "sha256": sum, "size": 9})
		r.objs.store("acme/"+sum, sum)
		res := map[string]any{"type": "result", "verdict": "rejected", "base": map[string]any{"sha256": baseSHA}, "outputs": []any{map[string]any{"kind": "capability", "sha256": sum}}}
		out := r.must(200, "POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": tid, "lease": lease,
			"events": []any{map[string]any{"type": "artifact", "sha256": sum}, res}})
		if str(out, "status") != "rejected" {
			t.Fatalf("rejected: %v", out)
		}
		if code, m := r.do("POST", "/v1/train/jobs/"+id+"/publish", "acme", map[string]any{"route": route("x")}); code != 409 || str(m, "code") != "not_publishable" {
			t.Fatalf("publish without force: %d %v", code, m)
		}
		p := r.must(200, "POST", "/v1/train/jobs/"+id+"/publish", "acme", map[string]any{"force": true, "name": "anyway", "route": route("x")})
		if p["published"].(map[string]any)["forced"] != true {
			t.Fatalf("forced publish: %v", p)
		}
	})
}

func TestAnArtifactOverOnePartIsUploadedInParts(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		task := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		sum := strings.Repeat("ef", 32)
		reg := r.must(200, "POST", "/v1/train/jobs/"+id+"/artifacts", "acme", map[string]any{"task": str(task, "id"), "lease": str(task, "lease"),
			"kind": "capability", "sha256": sum, "size": partBytes + 5})
		up := reg["upload"].(map[string]any)
		if up["url"] != nil || len(up["parts"].([]any)) != 2 || up["id"] != nil {
			t.Fatalf("grant: %v", up)
		}
		r.objs.store("acme/"+sum, sum)
		r.must(200, "POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(task, "id"), "lease": str(task, "lease"),
			"events": []any{map[string]any{"type": "artifact", "sha256": sum}}})
		if got := r.objs.ended["acme/"+sum]; got != "up-acme/"+sum {
			t.Fatalf("verify completed upload %q", got)
		}
	})
}

func TestCancelStopsTheExecutor(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		task := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		j := r.must(200, "POST", "/v1/train/jobs/"+id+"/cancel", "acme", nil)
		if str(j, "status") != "cancelled" {
			t.Fatalf("cancel: %v", j)
		}
		out := r.must(200, "POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(task, "id"), "lease": str(task, "lease"), "events": []any{}})
		if out["cancel"] != true {
			t.Fatalf("the executor was not told: %v", out)
		}
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", claimBody("dgx")); code != 204 {
			t.Fatalf("a cancelled job was claimed: %d", code)
		}
	})
}

func TestASilentLeadFailsItsJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		r.st.ttl = -1
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		task := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		j := r.must(200, "GET", "/v1/train/jobs/"+id, "acme", nil)
		if str(j, "status") != "failed" || !strings.HasPrefix(str(j, "error"), "executor_lost") {
			t.Fatalf("lapsed lease: %v", j)
		}
		if code, m := r.do("POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(task, "id"), "lease": str(task, "lease"), "events": []any{}}); code != 409 || str(m, "code") != "lease_lost" {
			t.Fatalf("a lost lease reported: %d %v", code, m)
		}
	})
}

func TestAJoinWaitsForItsLeadsAddress(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx", "evo")), "id")
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", claimBody("evo")); code != 204 {
			t.Fatalf("a join was offered before its lead coordinated: %d", code)
		}
		lead := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		r.must(200, "POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(lead, "id"), "lease": str(lead, "lease"),
			"events": []any{map[string]any{"type": "coordinator", "addr": "dgx.local:47100"}}})
		join := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("evo"))["task"].(map[string]any)
		if str(join, "role") != "join" || str(join, "coordinator") != "dgx.local:47100" {
			t.Fatalf("join: %v", join)
		}
		if code, m := r.do("POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(join, "id"), "lease": str(join, "lease"),
			"events": []any{map[string]any{"type": "result", "verdict": "none"}}}); code != 400 {
			t.Fatalf("a join reported a result: %d %v", code, m)
		}
		r.must(200, "POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(join, "id"), "lease": str(join, "lease"),
			"events": []any{map[string]any{"type": "status", "status": "done"}}})
	})
}

func TestAnExecutorIsHandedOnlyWhatItsBuildRuns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx"))
		old := claimBody("dgx")
		old["supports"] = map[string]any{"adaptations": []string{"full"}, "protect": []string{"projection"}, "outputs": []string{"capability"}}
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", old); code != 204 {
			t.Fatalf("a build without protection was handed a protected job: %d", code)
		}
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", claimBody("dgx")); code != 200 {
			t.Fatalf("a build that runs it was not handed it: %d", code)
		}
	})
}

func TestAClaimWaitsForAJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		claimWait = 10 * time.Second
		done := make(chan int)
		go func() {
			code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))
			done <- code
		}()
		time.Sleep(300 * time.Millisecond)
		r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx"))
		select {
		case code := <-done:
			if code != 200 {
				t.Fatalf("claim: %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a waiting claim was not woken by the job")
		}
	})
}

func TestTheCatalogIsWhatACreateAccepts(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		m := r.must(200, "GET", "/v1/train/models", "acme", nil)
		if d := m["data"].([]any); len(d) != 2 || str(d[1].(map[string]any), "base") != "kai" {
			t.Fatalf("models: %v", m)
		}
		if code, m := r.do("POST", "/v1/train/jobs", "acme", map[string]any{"base_model": "zen7", "dataset": map[string]any{"uri": "hf://x/y"}}); code != 400 || str(m, "code") != "unsupported_base" {
			t.Fatalf("an llm job: %d %v", code, m)
		}
		if code, _ := r.do("POST", "/v1/train/jobs", "", job()); code != 403 && code != 401 {
			t.Fatalf("a caller with no org created a job: %d", code)
		}
	})
}

// TestTheCatalogIsOneShape: every org reads every base it can train in one shape —
// base, methods, where it runs, the price of a device-second there — ordered by
// base, its runs by place, then price, and only the runs it can start.
func TestTheCatalogIsOneShape(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		code, m := r.do("GET", "/v1/train/models", "acme", nil)
		if code != 200 {
			t.Fatalf("an org that is not a SuperAdmin: %d %v", code, m)
		}
		raw, _ := json.Marshal(m["data"])
		want := `[{"adaptations":["full","readout","auto"],"base":"kai","datasets":["stage","artifact"],` +
			`"devices":["cuda","rocm","metal","vulkan","cpu"],"executor":"train serve","nouns":["jobs"],` +
			`"outputs":["capability","checkpoint"],"protect":["projection","distillation"],"runs":[` +
			`{"devices":["cpu"],"price_nano_usd":22222,"where":"hanzo"},` +
			`{"devices":["cuda","rocm","metal","vulkan"],"price_nano_usd":966667,"where":"hanzo"},` +
			`{"devices":["cuda","rocm","metal","vulkan","cpu"],"price_nano_usd":0,"where":"machines"}],` +
			`"terms":["pairwise_margin","hard_margin","consistency"]}]`
		if got := string(raw); got != want {
			t.Fatalf("catalog\n got %s\nwant %s", got, want)
		}
		// A SuperAdmin also reads the engine's clients, priced the way a client op is charged.
		sup := r.must(200, "GET", "/v1/train/models", "acme", nil)
		d := sup["data"].([]any)
		if len(d) != 2 || str(d[0].(map[string]any), "base") != "engine" || str(d[1].(map[string]any), "base") != "kai" {
			t.Fatalf("super, ordered by base: %v", sup)
		}
		run := d[0].(map[string]any)["runs"].([]any)[0].(map[string]any)
		if run["where"] != "hanzo" || run["price_nano_usd"].(float64) != clientFloorMicros*1_000 {
			t.Fatalf("engine run %v", run)
		}
		// And what the catalog withholds, a create refuses.
		if code, _ := r.do("POST", "/v1/train/clients", "acme", map[string]any{"base_model": "x/y"}); code != 403 {
			t.Fatalf("a client from an org the catalog lists no clients to: %d", code)
		}
	})
}

// TestEveryOrgTrains: an ordinary member's org, no flag and no SuperAdmin, queues a
// job on its own machines with no budget, and one Hanzo runs only inside a budget.
func TestEveryOrgTrains(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		if code, m := r.do("POST", "/v1/train/jobs", "acme", job("dgx")); code != 200 {
			t.Fatalf("a job on the org's machines: %d %v", code, m)
		}
		j := job()
		if code, m := r.do("POST", "/v1/train/jobs", "acme", j); code != 400 || str(m, "code") != "budget_required" {
			t.Fatalf("a job Hanzo runs with no budget: %d %v", code, m)
		}
		j["resources"] = map[string]any{"budget": 500}
		if code, m := r.do("POST", "/v1/train/jobs", "acme", j); code != 200 {
			t.Fatalf("a budgeted job Hanzo runs: %d %v", code, m)
		}
	})
}

// fakeEngine is the engine's training wire, recording what it was sent.
func fakeEngine(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	seen := []string{}
	ids := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		body, _ := io.ReadAll(q.Body)
		mu.Lock()
		seen = append(seen, q.Method+" "+q.URL.Path+" "+string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case q.Method == "POST" && q.URL.Path == "/v1/train/clients":
			mu.Lock()
			id := fmt.Sprintf("tc-%d", len(ids))
			ids = append(ids, id)
			mu.Unlock()
			fmt.Fprintf(w, `{"id":%q,"base_model":"m","status":"loading"}`, id)
		case q.Method == "GET" && q.URL.Path == "/v1/train/clients":
			mu.Lock()
			var cs []string
			for _, id := range ids {
				cs = append(cs, fmt.Sprintf(`{"id":%q}`, id))
			}
			mu.Unlock()
			fmt.Fprintf(w, `{"clients":[%s]}`, strings.Join(cs, ","))
		case strings.Contains(q.URL.Path, "tc-lost"):
			w.WriteHeader(404)
			fmt.Fprint(w, "no training client `tc-lost`")
		case strings.HasSuffix(q.URL.Path, "/forward_backward"):
			fmt.Fprint(w, `{"loss":1.5}`)
		default:
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestAnEngineWithoutTheTrainingPlaneIsUnavailable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		srv := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(srv.Close)
		r := mount(t, srv.URL)
		if code, m := r.as("POST", "/v1/train/clients", "acme", true, map[string]any{"base_model": "m"}); code != 503 || str(m, "code") != "training_unavailable" {
			t.Fatalf("create on an engine with no training plane: %d %v", code, m)
		}
	})
}

func TestAClientTheEngineLostIsDropped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		srv, _ := fakeEngine(t)
		r := mount(t, srv.URL)
		id := str(r.must(200, "POST", "/v1/train/clients", "acme", map[string]any{"base_model": "m"}), "id")
		st, err := r.st.stores.For(mustNS(t, "acme"))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.addClient(context.Background(), client{ID: "tc-lost", Base: "m", Spec: "{}", Created: 1}); err != nil {
			t.Fatal(err)
		}
		if code, _ := r.do("GET", "/v1/train/clients/tc-lost", "acme", nil); code != 404 {
			t.Fatalf("a client the engine lost: %d", code)
		}
		if ok, _ := st.owns(context.Background(), "tc-lost"); ok {
			t.Fatal("a client the engine lost is still the org's")
		}
		r.must(200, "GET", "/v1/train/clients/"+id, "acme", nil)
	})
}

func TestClientsAreTheEnginesWireUnderTheOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		srv, seen := fakeEngine(t)
		r := mount(t, srv.URL)
		if code, _ := r.do("POST", "/v1/train/clients", "acme", map[string]any{"base_model": "m"}); code != 403 {
			t.Fatalf("a client from someone who is not a SuperAdmin: %d", code)
		}
		c := r.must(200, "POST", "/v1/train/clients", "acme", map[string]any{"base_model": "HuggingFaceTB/SmolLM2-135M", "lora_config": map[string]any{"rank": 8}})
		id := str(c, "id")
		if id != "tc-0" || str(c, "status") != "loading" {
			t.Fatalf("create: %v", c)
		}
		r.must(200, "POST", "/v1/train/clients", "evil", map[string]any{"base_model": "m"})
		l := r.must(200, "GET", "/v1/train/clients", "acme", nil)
		if cs := l["clients"].([]any); len(cs) != 1 || str(cs[0].(map[string]any), "id") != id {
			t.Fatalf("list: %v", l)
		}
		for _, p := range []string{"/v1/train/clients/" + id, "/v1/train/clients/" + id + "/forward_backward"} {
			method := "GET"
			if strings.HasSuffix(p, "backward") {
				method = "POST"
			}
			if code, _ := r.do(method, p, "evil", map[string]any{"data": []any{}}); code != 404 {
				t.Fatalf("another org reached %s: %d", p, code)
			}
		}
		fb := r.must(200, "POST", "/v1/train/clients/"+id+"/forward_backward", "acme", map[string]any{"data": []any{map[string]any{"prompt": "2+2=", "completion": "4"}}})
		if fb["loss"] != 1.5 {
			t.Fatalf("forward_backward: %v", fb)
		}
		if code, m := r.do("POST", "/v1/train/clients/"+id+"/save_weights", "acme", map[string]any{"name": "a", "output": map[string]any{"kind": "capability"}}); code != 400 || str(m, "code") != "unsupported_output" {
			t.Fatalf("a capability from a client: %d %v", code, m)
		}
		r.must(200, "POST", "/v1/train/clients/"+id+"/save_weights", "acme", map[string]any{"name": "a", "output": map[string]any{"kind": "lora"}})
		if code, m := r.as("POST", "/v1/train/clients", "acme", true, map[string]any{"base_model": "kai"}); code != 400 || str(m, "code") != "unsupported_base" {
			t.Fatalf("a kai client: %d %v", code, m)
		}
		if code, m := r.as("POST", "/v1/train/clients", "acme", true, map[string]any{"base_model": "m", "protect": map[string]any{"suites": []string{"x"}}}); code != 400 || str(m, "code") != "unsupported_protect" {
			t.Fatalf("a protected client: %d %v", code, m)
		}
		r.must(200, "POST", "/v1/train/clients", "acme", map[string]any{"base_model": "m"})
		if code, m := r.as("POST", "/v1/train/clients", "acme", true, map[string]any{"base_model": "m"}); code != 409 || str(m, "code") != "client_limit" {
			t.Fatalf("a third client: %d %v", code, m)
		}
		r.st.room = 3
		if code, m := r.as("POST", "/v1/train/clients", "other", true, map[string]any{"base_model": "m"}); code != 503 || str(m, "code") != "engine_full" {
			t.Fatalf("past the engine's room: %d %v", code, m)
		}
		r.must(200, "DELETE", "/v1/train/clients/"+id, "acme", nil)
		if code, _ := r.do("GET", "/v1/train/clients/"+id, "acme", nil); code != 404 {
			t.Fatalf("a deleted client answered: %d", code)
		}
		want := []string{
			`POST /v1/train/clients {"base_model":"HuggingFaceTB/SmolLM2-135M","lora_config":{"rank":8}}`,
			`POST /v1/train/clients/tc-0/forward_backward {"data":[{"completion":"4","prompt":"2+2="}]}`,
			`POST /v1/train/clients/tc-0/save_weights {"name":"a"}`,
		}
		for _, w := range want {
			found := false
			for _, s := range *seen {
				found = found || s == w
			}
			if !found {
				t.Fatalf("the engine was not sent %s; it saw %v", w, *seen)
			}
		}
	})
}
