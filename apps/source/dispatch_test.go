package source

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A fixed beat, signed over fixed bytes: the exact message the desktop and the
// extension must produce.
func TestHeartbeatMessageIsByteExact(t *testing.T) {
	b := &SourceBeat{At: "2026-11-20T10:00:00Z", Version: "1.8.4", Zap: "https://mac.example/zap", Busy: 0,
		Providers: []SourceProvider{
			{"claude", "claude-1", "login", "ready", "max", true},
			{"openai", "key-1", "key", "limited", "", false},
		}}
	want := "hanzo-source-heartbeat/1\n" +
		"src_0123456789abcdef0123456789abcdef\n" +
		"2026-11-20T10:00:00Z\n" +
		"1.8.4\n" +
		"https://mac.example/zap\n" +
		"0\n" +
		"claude,claude-1,login,ready,max,true\n" +
		"openai,key-1,key,limited,,false\n"
	if got := string(beatMessage("src_0123456789abcdef0123456789abcdef", b)); got != want {
		t.Errorf("message\n got %q\nwant %q", got, want)
	}
}

// ready is a desktop with a ZAP endpoint, a fresh beat and one shared, ready claude login.
type ready struct {
	r   *rig
	d   device
	id  string
	pth string
}

func newReady(t *testing.T) *ready {
	r := mount(t)
	d := newDevice(t)
	id := r.link("alice", "acme", d)
	x := &ready{r: r, d: d, id: id, pth: "/v1/sources/" + id}
	x.beat(t, []SourceProvider{{"claude", "claude-1", "login", "ready", "max", true}})
	return x
}

func (x *ready) beat(t *testing.T, ps []SourceProvider) {
	t.Helper()
	if code, body := x.r.as("POST", x.pth+"/heartbeat", "alice", "acme", x.d.beat(x.id, x.r.now, ps)); code != 200 {
		t.Fatalf("beat: %d %s", code, body)
	}
}

func (x *ready) dispatch(user, org, provider, instance string) (int, string) {
	return x.r.as("POST", x.pth+"/dispatch", user, org, map[string]string{"provider": provider, "instance": instance})
}

func codeOf(body string) string {
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal([]byte(body), &p)
	return p.Code
}

func TestDispatchGrantsAndLeases(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		x := newReady(t)
		code, body := x.dispatch("alice", "acme", "claude", "claude-1")
		var got SourceRoute
		_ = json.Unmarshal([]byte(body), &got)
		if code != 200 || got.Zap != "https://mac.example/zap" || got.Provider != "claude" || got.Instance != "claude-1" ||
			!strings.HasPrefix(got.Lease, "lse_") || got.Expires != "2026-11-20T10:15:00Z" {
			t.Fatalf("grant: %d %s", code, body)
		}
		// Exclusive: a second run on the same instance is busy, for anyone it could be.
		if code, body := x.dispatch("alice", "acme", "claude", "claude-1"); code != 409 || codeOf(body) != "busy" {
			t.Errorf("second lease: %d %s", code, body)
		}
		// Another person cannot release it; the holder can, and then it is free.
		if code, _ := x.r.as("DELETE", x.pth+"/dispatch/"+got.Lease, "bob", "acme", nil); code != 404 {
			t.Errorf("bob releases alice's lease: want 404, got %d", code)
		}
		if code, body := x.r.as("DELETE", x.pth+"/dispatch/"+got.Lease, "alice", "acme", nil); code != 200 || body != `{"lease":"`+got.Lease+`","released":true}` {
			t.Errorf("release: %d %s", code, body)
		}
		if code, _ := x.r.as("DELETE", x.pth+"/dispatch/"+got.Lease, "alice", "acme", nil); code != 404 {
			t.Errorf("release twice: want 404, got %d", code)
		}
		code, body = x.dispatch("alice", "acme", "claude", "claude-1")
		if code != 200 {
			t.Fatalf("after release: %d %s", code, body)
		}
		// A lease lapses by itself after 15 minutes. Beats keep the source online.
		x.r.now = x.r.now.Add(14 * time.Minute)
		x.beat(t, []SourceProvider{{"claude", "claude-1", "login", "ready", "max", true}})
		if code, body := x.dispatch("alice", "acme", "claude", "claude-1"); code != 409 || codeOf(body) != "busy" {
			t.Errorf("before expiry: %d %s", code, body)
		}
		x.r.now = x.r.now.Add(time.Minute + time.Second)
		x.beat(t, []SourceProvider{{"claude", "claude-1", "login", "ready", "max", true}})
		if code, body := x.dispatch("alice", "acme", "claude", "claude-1"); code != 200 {
			t.Errorf("after expiry: %d %s", code, body)
		}
	})
}

func TestDispatchRefusals(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		x := newReady(t)
		login := func(provider, state string, shared bool) []SourceProvider {
			return []SourceProvider{{provider, provider + "-1", "login", state, "", shared}}
		}
		step := func() { x.r.now = x.r.now.Add(11 * time.Second) }

		// Identity: nobody, a machine, another person, another org, an org admin and a SuperAdmin.
		if code, _ := x.dispatch("", "acme", "claude", "claude-1"); code != 401 {
			t.Errorf("anonymous: %d", code)
		}
		if code, _ := x.dispatch("app:acme/ci", "acme", "claude", "claude-1"); code != 403 {
			t.Errorf("machine: %d", code)
		}
		for _, c := range []struct {
			u, o string
			h    map[string]string
		}{
			{"bob", "acme", nil}, {"alice", "other", nil},
			{"carol", "acme", map[string]string{"X-User-IsOrgAdmin": "true", "X-User-Owner": "acme"}},
			{"root", "acme", map[string]string{"X-User-IsOrgAdmin": "true", "X-User-IsAdmin": "true", "X-User-Owner": "admin"}},
		} {
			x.r.hdr = c.h
			if code, _ := x.dispatch(c.u, c.o, "claude", "claude-1"); code != 404 {
				t.Errorf("%s/%s: want 404, got %d", c.o, c.u, code)
			}
		}
		x.r.hdr = nil

		check := func(name string, status int, reason, provider, instance string) {
			t.Helper()
			code, body := x.dispatch("alice", "acme", provider, instance)
			if code != status || codeOf(body) != reason {
				t.Errorf("%s: want %d %s, got %d %s", name, status, reason, code, body)
			}
		}
		check("unknown instance", 409, "not_ready", "claude", "claude-9")
		check("wrong provider for the instance", 409, "not_ready", "codex", "claude-1")

		step()
		x.beat(t, login("claude", "needs_login", true))
		check("not ready", 409, "not_ready", "claude", "claude-1")
		step()
		x.beat(t, login("claude", "limited", true))
		check("limited", 409, "not_ready", "claude", "claude-1")
		step()
		x.beat(t, login("claude", "ready", false))
		check("not shared", 409, "not_shared", "claude", "claude-1")

		// A class without desktop, shared and ready as it may be.
		step()
		x.beat(t, login("gemini", "ready", true))
		check("class", 403, "class", "gemini", "gemini-1")
		check("unknown provider", 403, "class", "madeup", "madeup-1")

		// Offline: a source that has not beaten in 90 s.
		step()
		x.beat(t, login("claude", "ready", true))
		x.r.now = x.r.now.Add(91 * time.Second)
		check("offline", 409, "offline", "claude", "claude-1")

		// No endpoint: a desktop that registered none and beats none.
		d2 := newDevice(t)
		m := d2.link("bare")
		m["zap"] = ""
		_, body := x.r.as("POST", "/v1/sources", "alice", "acme", m)
		var l SourceLinked
		_ = json.Unmarshal([]byte(body), &l)
		b := d2.beat(l.ID, x.r.now, []SourceProvider{{"claude", "claude-1", "login", "ready", "", true}})
		b.Zap = ""
		b.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(d2.priv, beatMessage(l.ID, b)))
		x.r.as("POST", "/v1/sources/"+l.ID+"/heartbeat", "alice", "acme", b)
		code, body := x.r.as("POST", "/v1/sources/"+l.ID+"/dispatch", "alice", "acme", map[string]string{"provider": "claude", "instance": "claude-1"})
		if code != 409 || codeOf(body) != "no_endpoint" {
			t.Errorf("no endpoint: %d %s", code, body)
		}

		// A browser is view-only, however ready and shared its login looks.
		bd := newDevice(t)
		bm := bd.link("Chrome")
		bm["kind"] = "browser"
		_, body = x.r.as("POST", "/v1/sources", "alice", "acme", bm)
		var bl SourceLinked
		_ = json.Unmarshal([]byte(body), &bl)
		x.r.as("POST", "/v1/sources/"+bl.ID+"/heartbeat", "alice", "acme", bd.beat(bl.ID, x.r.now, []SourceProvider{{"claude", "claude-1", "login", "ready", "", true}}))
		code, body = x.r.as("POST", "/v1/sources/"+bl.ID+"/dispatch", "alice", "acme", map[string]string{"provider": "claude", "instance": "claude-1"})
		if code != 403 || codeOf(body) != "class" {
			t.Errorf("browser: %d %s", code, body)
		}
		_, list := x.r.as("GET", "/v1/sources/accounts", "alice", "acme", nil)
		if !strings.Contains(list, `"sourceId":"`+bl.ID+`","instance":"claude-1","state":"ready","plan":"","shared":true`) && !strings.Contains(list, `"classes":["local"],"sourceId":"`+bl.ID) {
			t.Errorf("a browser login lists as local only: %s", list)
		}

		// Revoked: 410 with its own code, and the lease table is cleared.
		x.r.now = x.r.now.Add(time.Minute)
		x.beat(t, login("claude", "ready", true))
		if code, _ := x.dispatch("alice", "acme", "claude", "claude-1"); code != 200 {
			t.Fatal("grant before revoke")
		}
		x.r.as("DELETE", x.pth, "alice", "acme", nil)
		check("revoked", 410, "revoked", "claude", "claude-1")
		sto, _ := x.r.sto.For(namespaceOf("acme"))
		var n int
		_ = sto.db.QueryRow(`SELECT COUNT(*) FROM source_lease WHERE source=$1`, x.id).Scan(&n)
		if n != 0 {
			t.Errorf("a revoked source still holds %d leases", n)
		}
		// Every grant and refusal is audited.
		rows := strings.Join(x.r.auditRows(), "|")
		if !strings.Contains(rows, "source.dispatch "+x.id+" alice") {
			t.Errorf("dispatch not audited: %s", rows)
		}
	})
}
