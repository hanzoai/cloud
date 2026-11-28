package source

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/spec"
)

type rig struct {
	t     *testing.T
	app   *zip.App
	now   time.Time
	audit *audit.Recorder
	kms   *fakeKMS
	dir   string
	hdr   map[string]string
	sto   *cloud.OrgStore[*Store]
	svc   *service
}

func mount(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	rec, err := audit.Open(dir, "source-test", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	r := &rig{t: t, now: t0, audit: rec, kms: &fakeKMS{vals: map[string][]byte{}}, dir: dir}
	b := cloud.Base{Log: luxlog.New("test"), DataDir: dir}
	stores := cloud.NewOrgStore[*Store](b, "sources", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	s := &service{Base: b, State: state{stores: stores, audit: rec, kms: r.kms, now: func() time.Time { return r.now }}}
	r.sto = stores
	r.svc = s
	r.app = zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(r.app, s)
	return r
}

// as drives one request as user in org. An empty user is no validated principal:
// X-User-Id is minted only from a verified credential.
func (r *rig) as(method, path, user, org string, body any) (int, string) {
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
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	for k, v := range r.hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

type device struct {
	pub  string
	priv ed25519.PrivateKey
}

func newDevice(t *testing.T) device {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return device{base64.RawURLEncoding.EncodeToString(pub), priv}
}

func (d device) link(name string) map[string]any {
	return map[string]any{"kind": "desktop", "name": name, "os": "darwin", "arch": "arm64", "version": "1.8.4", "key": d.pub, "zap": "https://mac.example/zap"}
}

// beat builds a signed heartbeat for id at the rig's clock.
func (d device) beat(id string, at time.Time, ps []SourceProvider) *SourceBeat {
	b := &SourceBeat{ID: id, At: at.Format(time.RFC3339), Version: "1.8.4", Zap: "https://mac.example/zap", Providers: ps}
	b.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(d.priv, beatMessage(id, b)))
	return b
}

func (r *rig) link(user, org string, d device) string {
	r.t.Helper()
	code, body := r.as("POST", "/v1/sources", user, org, d.link("Zach's MacBook"))
	if code != 201 {
		r.t.Fatalf("link: %d %s", code, body)
	}
	var out SourceLinked
	_ = json.Unmarshal([]byte(body), &out)
	return out.ID
}

func TestLinkWireAndIdempotency(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d := newDevice(t)
		code, body := r.as("POST", "/v1/sources", "alice", "acme", d.link("Zach's MacBook"))
		var out SourceLinked
		_ = json.Unmarshal([]byte(body), &out)
		if code != 201 || !strings.HasPrefix(out.ID, "src_") || body != `{"id":"`+out.ID+`","interval":30,"level":"metadata"}` {
			t.Fatalf("link: %d %s", code, body)
		}
		code, body2 := r.as("POST", "/v1/sources", "alice", "acme", d.link("renamed"))
		if code != 200 || body2 != body {
			t.Fatalf("relink: %d %s (first %s)", code, body2, body)
		}
		code, body = r.as("GET", "/v1/sources", "alice", "acme", nil)
		want := `{"sources":[{"id":"` + out.ID + `","kind":"desktop","name":"renamed","os":"darwin","arch":"arm64","version":"1.8.4","state":"offline","linked_at":"2026-11-20T10:00:00Z","zap":"https://mac.example/zap","providers":[]}]}`
		if code != 200 || body != want {
			t.Fatalf("list: %d\n got %s\nwant %s", code, body, want)
		}
		if n := len(r.auditRows()); n != 1 {
			t.Errorf("relink audited %d times, want link once", n)
		}
	})
}

func (r *rig) auditRows() []string {
	recs, _, err := r.audit.Query(context.Background(), audit.Filter{Limit: 100})
	if err != nil {
		r.t.Fatalf("audit query: %v", err)
	}
	var out []string
	for _, a := range recs {
		out = append(out, a.Action+" "+a.Resource.ID+" "+a.Actor.Sub)
	}
	return out
}

func TestLinkRefusals(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d := newDevice(t)
		bad := func(mut func(m map[string]any)) map[string]any { m := d.link("x"); mut(m); return m }
		for _, c := range []struct {
			name, user string
			body       any
			want       int
		}{
			{"no principal", "", d.link("x"), 401},
			{"machine token", "app:acme/ci", d.link("x"), 403},
			{"no kind", "alice", bad(func(m map[string]any) { delete(m, "kind") }), 400},
			{"unknown kind", "alice", bad(func(m map[string]any) { m["kind"] = "tablet" }), 400},
			{"no name", "alice", bad(func(m map[string]any) { m["name"] = " " }), 400},
			{"short key", "alice", bad(func(m map[string]any) { m["key"] = "AAAA" }), 400},
			{"http zap", "alice", bad(func(m map[string]any) { m["zap"] = "http://x" }), 400},
		} {
			if code, body := r.as("POST", "/v1/sources", c.user, "acme", c.body); code != c.want {
				t.Errorf("%s: want %d, got %d %s", c.name, c.want, code, body)
			}
		}
		// Identity is never the body: a body naming another subject changes nothing.
		m := d.link("x")
		m["subject"], m["owner"] = "bob", "other"
		if code, _ := r.as("POST", "/v1/sources", "alice", "acme", m); code != 201 {
			t.Fatal("link with extra fields")
		}
		if _, body := r.as("GET", "/v1/sources", "bob", "acme", nil); body != `{"sources":[]}` {
			t.Errorf("bob sees %s", body)
		}
	})
}

func TestCapThroughHTTP(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		for n := 0; n < maxActive; n++ {
			r.now = t0.Add(time.Duration(n) * 7 * time.Minute)
			r.link("alice", "acme", newDevice(t))
		}
		r.now = r.now.Add(7 * time.Minute)
		if code, _ := r.as("POST", "/v1/sources", "alice", "acme", newDevice(t).link("26")); code != 409 {
			t.Fatalf("26th source: want 409, got %d", code)
		}
		if code, _ := r.as("POST", "/v1/sources", "bob", "acme", newDevice(t).link("b")); code != 201 {
			t.Fatalf("bob's first: want 201, got %d", code)
		}
	})
}

func TestOwnerOnly(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d := newDevice(t)
		id := r.link("alice", "acme", d)
		path := "/v1/sources/" + id
		// Another person in the org, a person in another org, an org admin and a
		// SuperAdmin (owner admin) all get the same 404 an unknown id gets.
		for _, c := range []struct{ user, org string }{{"bob", "acme"}, {"alice", "other"}, {"root", "admin"}} {
			for _, m := range []string{"GET", "DELETE"} {
				if code, _ := r.as(m, path, c.user, c.org, nil); code != 404 {
					t.Errorf("%s %s/%s: want 404, got %d", m, c.org, c.user, code)
				}
			}
			code, _ := r.as("POST", path+"/heartbeat", c.user, c.org, d.beat(id, t0, nil))
			if code != 404 {
				t.Errorf("heartbeat %s/%s: want 404, got %d", c.org, c.user, code)
			}
		}
		if code, body := r.as("GET", path, "alice", "acme", nil); code != 200 || !strings.Contains(body, `"state":"offline"`) {
			t.Errorf("owner GET: %d %s", code, body)
		}
		if code, _ := r.as("GET", path, "app:acme/ci", "acme", nil); code != 403 {
			t.Errorf("machine GET: want 403, got %d", code)
		}
		if code, _ := r.as("GET", path, "", "acme", nil); code != 401 {
			t.Errorf("anonymous GET: want 401, got %d", code)
		}
	})
}

func TestHeartbeat(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d, other := newDevice(t), newDevice(t)
		id := r.link("alice", "acme", d)
		path := "/v1/sources/" + id + "/heartbeat"
		ps := []SourceProvider{{"claude", "claude-1", "login", "ready", "max", false}}

		// A bad signature: wrong key, tampered content, and none at all.
		forged := other.beat(id, t0, ps)
		tampered := d.beat(id, t0, ps)
		tampered.Providers = []SourceProvider{{"claude", "claude-1", "login", "limited", "max", false}}
		unsigned := d.beat(id, t0, ps)
		unsigned.Sig = ""
		for name, b := range map[string]*SourceBeat{"wrong key": forged, "tampered": tampered, "unsigned": unsigned} {
			if code, body := r.as("POST", path, "alice", "acme", b); code != 401 {
				t.Errorf("%s: want 401, got %d %s", name, code, body)
			}
		}
		// A valid token alone, even for the owner, does not make a source alive.
		if _, body := r.as("GET", "/v1/sources/"+id, "alice", "acme", nil); !strings.Contains(body, `"state":"offline"`) {
			t.Fatalf("a refused beat marked it live: %s", body)
		}

		code, body := r.as("POST", path, "alice", "acme", d.beat(id, t0, ps))
		if code != 200 || body != `{"interval":30,"level":"metadata","revoked":false}` {
			t.Fatalf("beat: %d %s", code, body)
		}
		_, body = r.as("GET", "/v1/sources/"+id, "alice", "acme", nil)
		want := `{"id":"` + id + `","kind":"desktop","name":"Zach's MacBook","os":"darwin","arch":"arm64","version":"1.8.4","state":"online","last_seen":"2026-11-20T10:00:00Z","linked_at":"2026-11-20T10:00:00Z","zap":"https://mac.example/zap","providers":[{"provider":"claude","instance":"claude-1","kind":"login","state":"ready","plan":"max","shared":false}]}`
		if body != want {
			t.Errorf("get after beat\n got %s\nwant %s", body, want)
		}
		// Online is a heartbeat in the last 90 seconds.
		r.now = t0.Add(90 * time.Second)
		if _, body := r.as("GET", "/v1/sources/"+id, "alice", "acme", nil); !strings.Contains(body, `"state":"online"`) {
			t.Errorf("at 90s: %s", body)
		}
		r.now = t0.Add(91 * time.Second)
		if _, body := r.as("GET", "/v1/sources/"+id, "alice", "acme", nil); !strings.Contains(body, `"state":"offline"`) {
			t.Errorf("at 91s: %s", body)
		}
		// Replay of the same signed beat, and a beat inside 10 s.
		if code, _ := r.as("POST", path, "alice", "acme", d.beat(id, t0, ps)); code != 409 {
			t.Errorf("replay: want 409, got %d", code)
		}
		r.now = t0.Add(95 * time.Second)
		if code, _ := r.as("POST", path, "alice", "acme", d.beat(id, r.now, ps)); code != 200 {
			t.Errorf("next beat: want 200, got %d", code)
		}
		if code, _ := r.as("POST", path, "alice", "acme", d.beat(id, r.now.Add(5*time.Second), ps)); code != 429 {
			t.Errorf("beat within 10s: want 429, got %d", code)
		}
		// Provider vocabulary and clock skew are refused.
		r.now = t0.Add(5 * time.Minute)
		badState := []SourceProvider{{"claude", "claude-1", "login", "great", "", false}}
		if code, _ := r.as("POST", path, "alice", "acme", d.beat(id, r.now, badState)); code != 400 {
			t.Errorf("bad state: want 400, got %d", code)
		}
		if code, _ := r.as("POST", path, "alice", "acme", d.beat(id, r.now.Add(-time.Hour), ps)); code != 400 {
			t.Errorf("skewed at: want 400, got %d", code)
		}
		if code, _ := r.as("POST", path, "app:acme/ci", "acme", d.beat(id, r.now, ps)); code != 403 {
			t.Errorf("machine beat: want 403, got %d", code)
		}
	})
}

func TestRevokeThenGone(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d := newDevice(t)
		id := r.link("alice", "acme", d)
		path := "/v1/sources/" + id
		if code, _ := r.as("POST", path+"/heartbeat", "alice", "acme", d.beat(id, t0, nil)); code != 200 {
			t.Fatal("beat before revoke")
		}
		code, body := r.as("DELETE", path, "alice", "acme", nil)
		if code != 200 || body != `{"id":"`+id+`","revoked":true}` {
			t.Fatalf("revoke: %d %s", code, body)
		}
		r.now = t0.Add(time.Minute)
		if code, _ := r.as("POST", path+"/heartbeat", "alice", "acme", d.beat(id, r.now, nil)); code != 410 {
			t.Errorf("beat after revoke: want 410, got %d", code)
		}
		// Even a mis-signed beat from a revoked source is 410, so it forgets the link.
		bad := d.beat(id, r.now, nil)
		bad.Sig = ""
		if code, _ := r.as("POST", path+"/heartbeat", "alice", "acme", bad); code != 410 {
			t.Errorf("unsigned beat after revoke: want 410, got %d", code)
		}
		if _, body := r.as("GET", "/v1/sources", "alice", "acme", nil); body != `{"sources":[]}` {
			t.Errorf("list after revoke: %s", body)
		}
		if _, body := r.as("GET", path, "alice", "acme", nil); !strings.Contains(body, `"state":"revoked"`) {
			t.Errorf("get after revoke: %s", body)
		}
		if code, _ := r.as("DELETE", path, "alice", "acme", nil); code != 200 {
			t.Errorf("second revoke: want 200, got %d", code)
		}
		// Audited: the link and the revoke, each by the person, naming the source.
		rows := r.auditRows()
		if len(rows) < 2 {
			t.Fatalf("audit rows: %v", rows)
		}
		joined := strings.Join(rows, "|")
		for _, w := range []string{"source.link " + id + " alice", "source.revoke " + id + " alice"} {
			if !strings.Contains(joined, w) {
				t.Errorf("audit lacks %q: %v", w, rows)
			}
		}
	})
}

// untypedByDesign is the closed list of routes that are not typed ops, each with
// the wire fact that keeps it raw.
var untypedByDesign = map[string]string{
	"POST /v1/machines/{id}/usage": "the signature covers the exact body bytes and rides in the " +
		"x-hanzo-sig header, neither of which a typed op can see.",
	"POST /v1/sources/{id}/usage": "the signature covers the exact body bytes and rides in the " +
		"x-hanzo-sig header, neither of which a typed op can see.",
}

// Every other route is a typed op, so it is a document operation, a tool and a ZAP op.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	r := mount(t)
	doc, err := spec.Of(r.app, spec.Info{Title: "source", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(r.app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}
	served := 0
	for path, item := range doc.Paths {
		for method := range item {
			served++
			key := strings.ToUpper(method) + " " + path
			_, typed := reg.Ops[key]
			_, named := untypedByDesign[key]
			if typed == named {
				t.Errorf("%s: typed=%v named=%v, want exactly one", key, typed, named)
			}
		}
	}
	if served != 24 {
		t.Errorf("served %d operations, want 24", served)
	}
}
