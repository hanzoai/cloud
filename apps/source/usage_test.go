package source

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// send posts a raw usage body with a signature header.
func (r *rig) send(path, user, org string, body []byte, sig string) (int, string) {
	r.t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", user)
	req.Header.Set("X-Org-Id", org)
	if sig != "" {
		req.Header.Set("x-hanzo-sig", sig)
	}
	resp, err := r.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func (d device) sign(id string, body []byte) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(d.priv, usageMessage(id, body)))
}

func report(id string, mut func(m map[string]any)) []byte {
	m := map[string]any{
		"version": 1, "deviceId": id, "from": "2026-11-20T00:00:00Z", "to": "2026-11-20T10:00:00Z",
		"cost": []any{
			map[string]any{"day": "2026-11-20", "provider": "anthropic", "model": "sonnet", "inputTokens": 100, "outputTokens": 50, "cacheTokens": 10, "costUsd": 0.5},
			map[string]any{"day": "2026-11-20", "provider": "openai", "model": "gpt", "inputTokens": 7, "outputTokens": 3, "cacheTokens": 0},
		},
		"snapshots": []any{map[string]any{
			"provider": "anthropic", "fetchedAt": "2026-11-20T10:00:00Z", "source": "oauth", "plan": "max",
			"windows": []any{map[string]any{"kind": "session", "label": "5h", "usedPercent": 42.5, "windowMinutes": 300, "resetsAt": "2026-11-20T15:00:00Z"}},
			"credits": map[string]any{"currency": "usd", "balance": 12.5},
			"status":  map[string]any{"indicator": "none", "incidents": []any{}, "checkedAt": "2026-11-20T10:00:00Z"},
		}},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func (r *rig) report(user, org string, d device, id string, body []byte) (int, string) {
	return r.send("/v1/sources/"+id+"/usage", user, org, body, d.sign(id, body))
}

func TestUsageReportAndReplay(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d := newDevice(t)
		id := r.link("alice", "acme", d)
		body := report(id, nil)
		code, out := r.report("alice", "acme", d, id, body)
		if code != 200 || out != `{"accepted":3}` {
			t.Fatalf("report: %d %s", code, out)
		}
		_, first := r.as("GET", "/v1/sources/usage?by=provider", "alice", "acme", nil)
		// Replay of the same report, after the 10 s gap, changes nothing.
		r.now = t0.Add(11 * time.Second)
		if code, out = r.report("alice", "acme", d, id, body); code != 200 || out != `{"accepted":3}` {
			t.Fatalf("replay: %d %s", code, out)
		}
		if _, again := r.as("GET", "/v1/sources/usage?by=provider", "alice", "acme", nil); again != first {
			t.Errorf("replay double counted\n first %s\n again %s", first, again)
		}
		want := `{"rows":[{"key":"anthropic","inputTokens":100,"outputTokens":50,"cacheTokens":10,"costUsd":0.5},{"key":"openai","inputTokens":7,"outputTokens":3,"cacheTokens":0,"costUsd":0}],"snapshots":[{"sourceId":"` + id +
			`","provider":"anthropic","fetchedAt":"2026-11-20T10:00:00Z","source":"oauth","plan":"max","windows":[{"kind":"session","label":"5h","usedPercent":42.5,"windowMinutes":300,"resetsAt":"2026-11-20T15:00:00Z"}],"credits":{"currency":"usd","balance":12.5},"status":{"indicator":"none","incidents":[],"checkedAt":"2026-11-20T10:00:00Z"}}]}`
		if first != want {
			t.Errorf("aggregation\n got %s\nwant %s", first, want)
		}
		// A corrected resend replaces the day, it does not add to it.
		r.now = t0.Add(30 * time.Second)
		fix := report(id, func(m map[string]any) {
			m["cost"] = []any{map[string]any{"day": "2026-11-20", "provider": "anthropic", "model": "sonnet", "inputTokens": 1, "outputTokens": 1, "cacheTokens": 1}}
		})
		r.report("alice", "acme", d, id, fix)
		_, got := r.as("GET", "/v1/sources/usage?by=model&from=2026-11-20&to=2026-11-20", "alice", "acme", nil)
		if !strings.Contains(got, `{"key":"sonnet","inputTokens":1,"outputTokens":1,"cacheTokens":1,"costUsd":0}`) || !strings.Contains(got, `"key":"gpt"`) {
			t.Errorf("replace by (day, provider, model): %s", got)
		}
		if _, got := r.as("GET", "/v1/sources/usage?by=source&from=2026-12-01", "alice", "acme", nil); !strings.HasPrefix(got, `{"rows":[]`) {
			t.Errorf("day filter: %s", got)
		}
	})
}

func TestUsageRefusals(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d, other := newDevice(t), newDevice(t)
		id := r.link("alice", "acme", d)
		ok := report(id, nil)

		// Signature: absent, another key's, and over other bytes.
		if code, _ := r.send("/v1/sources/"+id+"/usage", "alice", "acme", ok, ""); code != 401 {
			t.Errorf("unsigned: want 401, got %d", code)
		}
		if code, _ := r.send("/v1/sources/"+id+"/usage", "alice", "acme", ok, other.sign(id, ok)); code != 401 {
			t.Errorf("wrong key: want 401, got %d", code)
		}
		if code, _ := r.send("/v1/sources/"+id+"/usage", "alice", "acme", ok, d.sign(id, report(id, func(m map[string]any) { m["to"] = "2026-11-21T00:00:00Z" }))); code != 401 {
			t.Errorf("signature over other bytes: want 401, got %d", code)
		}
		// Identity: nobody, a machine, another person, another org.
		if code, _ := r.send("/v1/sources/"+id+"/usage", "", "acme", ok, d.sign(id, ok)); code != 401 {
			t.Errorf("anonymous: want 401, got %d", code)
		}
		if code, _ := r.report("app:acme/ci", "acme", d, id, ok); code != 403 {
			t.Errorf("machine: want 403, got %d", code)
		}
		if code, _ := r.report("bob", "acme", d, id, ok); code != 404 {
			t.Errorf("another person's source: want 404, got %d", code)
		}
		if code, _ := r.report("alice", "other", d, id, ok); code != 404 {
			t.Errorf("another org: want 404, got %d", code)
		}

		// Schema: each is rejected whole, with 400, and stores nothing.
		for name, mut := range map[string]func(m map[string]any){
			"wrong version":     func(m map[string]any) { m["version"] = 2 },
			"wrong deviceId":    func(m map[string]any) { m["deviceId"] = "src_other" },
			"unknown field":     func(m map[string]any) { m["prompt"] = "hello" },
			"missing cost":      func(m map[string]any) { delete(m, "cost") },
			"bad instant":       func(m map[string]any) { m["from"] = "yesterday" },
			"negative tokens":   func(m map[string]any) { m["cost"].([]any)[0].(map[string]any)["inputTokens"] = -1 },
			"fractional tokens": func(m map[string]any) { m["cost"].([]any)[0].(map[string]any)["inputTokens"] = 1.5 },
			"bad day":           func(m map[string]any) { m["cost"].([]any)[0].(map[string]any)["day"] = "20-11-2026" },
			"bad provider id":   func(m map[string]any) { m["cost"].([]any)[0].(map[string]any)["provider"] = "Claude Code" },
			"cost field extra":  func(m map[string]any) { m["cost"].([]any)[0].(map[string]any)["file"] = "/etc/passwd" },
			"repeated cost row": func(m map[string]any) { c := m["cost"].([]any); m["cost"] = append(c, c[0]) },
			"unknown window": func(m map[string]any) {
				m["snapshots"].([]any)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)["kind"] = "hourly"
			},
			"percent over 100": func(m map[string]any) {
				m["snapshots"].([]any)[0].(map[string]any)["windows"].([]any)[0].(map[string]any)["usedPercent"] = 101
			},
			"unknown source":  func(m map[string]any) { m["snapshots"].([]any)[0].(map[string]any)["source"] = "browser" },
			"account present": func(m map[string]any) { m["snapshots"].([]any)[0].(map[string]any)["account"] = "z@hanzo.ai" },
			"error is prose": func(m map[string]any) {
				m["snapshots"].([]any)[0].(map[string]any)["error"] = "Could not log in as z@hanzo.ai"
			},
			"unknown indicator": func(m map[string]any) {
				m["snapshots"].([]any)[0].(map[string]any)["status"].(map[string]any)["indicator"] = "red"
			},
			"long status text": func(m map[string]any) {
				m["snapshots"].([]any)[0].(map[string]any)["status"].(map[string]any)["description"] = strings.Repeat("x", 201)
			},
		} {
			body := report(id, mut)
			if code, out := r.report("alice", "acme", d, id, body); code != 400 {
				t.Errorf("%s: want 400, got %d %s", name, code, out)
			}
		}
		if _, got := r.as("GET", "/v1/sources/usage?by=source", "alice", "acme", nil); got != `{"rows":[],"snapshots":[]}` {
			t.Errorf("a refused report stored something: %s", got)
		}
		// An empty account and an error code are accepted.
		accepted := report(id, func(m map[string]any) {
			s := m["snapshots"].([]any)[0].(map[string]any)
			s["account"], s["error"] = "", "auth_expired"
		})
		if code, out := r.report("alice", "acme", d, id, accepted); code != 200 {
			t.Errorf("empty account and a code: %d %s", code, out)
		}
		_, got := r.as("GET", "/v1/sources/usage?by=source", "alice", "acme", nil)
		if strings.Contains(got, `"account"`) || !strings.Contains(got, `"error":"auth_expired"`) {
			t.Errorf("stored snapshot: %s", got)
		}
		// Size and rate.
		r.now = t0.Add(time.Second)
		if code, _ := r.report("alice", "acme", d, id, ok); code != 429 {
			t.Errorf("within 10s: want 429, got %d", code)
		}
		r.now = t0.Add(time.Minute)
		big := report(id, func(m map[string]any) { m["pad"] = strings.Repeat("x", maxReport) })
		if code, _ := r.report("alice", "acme", d, id, big); code != 413 {
			t.Errorf("over 1 MiB: want 413, got %d", code)
		}
	})
}

func TestUsageRevokedAndIsolated(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d, e := newDevice(t), newDevice(t)
		a := r.link("alice", "acme", d)
		b := r.link("bob", "acme", e)
		r.report("alice", "acme", d, a, report(a, nil))
		r.report("bob", "acme", e, b, report(b, func(m map[string]any) {
			m["cost"] = []any{map[string]any{"day": "2026-11-20", "provider": "xai", "model": "grok", "inputTokens": 1000, "outputTokens": 0, "cacheTokens": 0}}
			m["snapshots"] = []any{}
		}))
		// Each person reads their own and only theirs, in every grouping.
		for _, by := range []string{"source", "provider", "model", "day", "user"} {
			_, got := r.as("GET", "/v1/sources/usage?by="+by, "alice", "acme", nil)
			if strings.Contains(got, "grok") || strings.Contains(got, b) || strings.Contains(got, "xai") {
				t.Errorf("alice by=%s reads bob's: %s", by, got)
			}
		}
		// Another org has nothing; an org admin by=user sees totals, not sources.
		if _, got := r.as("GET", "/v1/sources/usage?by=provider", "alice", "other", nil); got != `{"rows":[],"snapshots":[]}` {
			t.Errorf("another org: %s", got)
		}
		r.hdr = map[string]string{"X-User-IsOrgAdmin": "true", "X-User-Owner": "acme"}
		_, got := r.as("GET", "/v1/sources/usage?by=user", "carol", "acme", nil)
		if !strings.Contains(got, `"key":"alice"`) || !strings.Contains(got, `"key":"bob"`) || strings.Contains(got, `"snapshots":[{`) {
			t.Errorf("org admin by=user: %s", got)
		}
		if _, got := r.as("GET", "/v1/sources/usage?by=provider", "carol", "acme", nil); strings.Contains(got, "xai") || strings.Contains(got, "anthropic") {
			t.Errorf("org admin by=provider reads others: %s", got)
		}
		if _, got := r.as("GET", "/v1/sources/usage?by=day", "carol", "acme", nil); got != `{"rows":[],"snapshots":[]}` {
			t.Errorf("org admin by=day reads others: %s", got)
		}
		if code, _ := r.as("GET", "/v1/sources/"+a, "carol", "acme", nil); code != 404 {
			t.Errorf("org admin reads alice's source: want 404, got %d", code)
		}
		// A SuperAdmin acting in acme from the admin org has no org-admin read.
		r.hdr = map[string]string{"X-User-IsOrgAdmin": "true", "X-User-IsAdmin": "true", "X-User-Owner": "admin"}
		if _, got := r.as("GET", "/v1/sources/usage?by=user", "root", "acme", nil); strings.Contains(got, `"key":"alice"`) || strings.Contains(got, `"key":"bob"`) {
			t.Errorf("SuperAdmin by=user: %s", got)
		}
		// A plain member's by=user is their own row.
		r.hdr = nil
		if _, got := r.as("GET", "/v1/sources/usage?by=user", "alice", "acme", nil); strings.Contains(got, "bob") {
			t.Errorf("member by=user: %s", got)
		}
		if code, _ := r.as("GET", "/v1/sources/usage?by=nothing", "alice", "acme", nil); code != 400 {
			t.Errorf("bad by: want 400, got %d", code)
		}

		// Revoke: 410 on the next report, and the snapshots go with the source.
		r.as("DELETE", "/v1/sources/"+a, "alice", "acme", nil)
		r.now = t0.Add(time.Minute)
		if code, _ := r.report("alice", "acme", d, a, report(a, nil)); code != 410 {
			t.Errorf("report after revoke: want 410, got %d", code)
		}
		if _, got := r.as("GET", "/v1/sources/usage?by=provider", "alice", "acme", nil); strings.Contains(got, `"snapshots":[{`) {
			t.Errorf("a revoked source's snapshot is still served: %s", got)
		}
	})
}

func TestUsageByDay(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d := newDevice(t)
		id := r.link("alice", "acme", d)
		body := report(id, func(m map[string]any) {
			m["cost"] = []any{
				map[string]any{"day": "2026-11-19", "provider": "anthropic", "model": "sonnet", "inputTokens": 200, "outputTokens": 80, "cacheTokens": 20, "costUsd": 1.25},
				map[string]any{"day": "2026-11-20", "provider": "anthropic", "model": "sonnet", "inputTokens": 100, "outputTokens": 50, "cacheTokens": 10, "costUsd": 0.5},
				map[string]any{"day": "2026-11-20", "provider": "openai", "model": "gpt", "inputTokens": 7, "outputTokens": 3, "cacheTokens": 0, "costUsd": 0.25},
			}
			m["snapshots"] = []any{}
		})
		if code, out := r.report("alice", "acme", d, id, body); code != 200 || out != `{"accepted":3}` {
			t.Fatalf("report: %d %s", code, out)
		}
		// Per-day split sums across providers/models within each day, ordered by day.
		code, got := r.as("GET", "/v1/sources/usage?by=day", "alice", "acme", nil)
		want := `{"rows":[{"key":"2026-11-19","inputTokens":200,"outputTokens":80,"cacheTokens":20,"costUsd":1.25},{"key":"2026-11-20","inputTokens":107,"outputTokens":53,"cacheTokens":10,"costUsd":0.75}],"snapshots":[]}`
		if code != 200 || got != want {
			t.Errorf("by=day\n got %d %s\nwant 200 %s", code, got, want)
		}
		// Date range filter narrows by=day to the requested window.
		code, got = r.as("GET", "/v1/sources/usage?by=day&from=2026-11-20&to=2026-11-20", "alice", "acme", nil)
		want = `{"rows":[{"key":"2026-11-20","inputTokens":107,"outputTokens":53,"cacheTokens":10,"costUsd":0.75}],"snapshots":[]}`
		if code != 200 || got != want {
			t.Errorf("by=day range\n got %d %s\nwant 200 %s", code, got, want)
		}
	})
}
