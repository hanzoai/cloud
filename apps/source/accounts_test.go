package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/secret"
)

// fakeKMS is an in-memory secret store that records every call in order, so a test
// can state which store the seal and the resolve went to and what came first.
type fakeKMS struct {
	mu      sync.Mutex
	vals    map[string][]byte
	calls   []string
	failDel bool
}

func (k *fakeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.vals[ref]
	if !ok {
		return nil, secret.ErrNone
	}
	return append([]byte(nil), v...), nil
}

func (k *fakeKMS) PutSecret(_ context.Context, ref string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, "put "+ref)
	k.vals[ref] = append([]byte(nil), v...)
	return nil
}

func (k *fakeKMS) DeleteSecret(_ context.Context, ref string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, "delete "+ref)
	if k.failDel {
		return errors.New("kms down")
	}
	if _, ok := k.vals[ref]; !ok {
		return secret.ErrNone
	}
	delete(k.vals, ref)
	return nil
}

func (k *fakeKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, errors.New("no") }

const keyValue = "sk-ant-api03-THIS-IS-A-SECRET-VALUE-0123456789"

func newKey(provider, label string) map[string]any {
	return map[string]any{"provider": provider, "kind": "key", "label": label, "value": keyValue}
}

func TestAccountLifecycleAndOrder(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		var seen strings.Builder
		do := func(method, path, user, org string, body any) (int, string) {
			c, b := r.as(method, path, user, org, body)
			seen.WriteString(b + "\n")
			return c, b
		}
		code, body := do("POST", "/v1/sources/accounts", "alice", "acme", newKey("anthropic", "work"))
		if code != 201 {
			t.Fatalf("create: %d %s", code, body)
		}
		var made Account
		_ = json.Unmarshal([]byte(body), &made)
		id := made.ID
		if !strings.Contains(body, `"provider":"anthropic","kind":"key","label":"work","fingerprint":"`) || !strings.Contains(body, `"classes":["key"]`) {
			t.Errorf("create body: %s", body)
		}
		// The seal went to exactly one place, which is the place the person is keyed by.
		if len(r.kms.calls) != 1 || !strings.HasPrefix(r.kms.calls[0], "put orgs/acme/users/") ||
			!strings.Contains(r.kms.calls[0], "/providers/anthropic/"+id+"@default") {
			t.Fatalf("kms calls: %v", r.kms.calls)
		}
		if got, err := r.kms.GetSecret(context.Background(), strings.TrimPrefix(r.kms.calls[0], "put ")); err != nil || string(got) != keyValue {
			t.Fatalf("the sealed value is not at the path it was sealed to: %q %v", got, err)
		}
		// Reads: list, never the value.
		_, list := do("GET", "/v1/sources/accounts", "alice", "acme", nil)
		if !strings.Contains(list, id) || strings.Contains(list, "value") {
			t.Errorf("list: %s", list)
		}
		// Isolation: another person, another org.
		for _, c := range []struct{ u, o string }{{"bob", "acme"}, {"alice", "other"}} {
			if _, l := do("GET", "/v1/sources/accounts", c.u, c.o, nil); strings.Contains(l, id) {
				t.Errorf("%s/%s lists alice's account: %s", c.o, c.u, l)
			}
			if code, _ := do("DELETE", "/v1/sources/accounts/"+id, c.u, c.o, nil); code != 404 {
				t.Errorf("%s/%s delete: want 404, got %d", c.o, c.u, code)
			}
		}
		if len(r.kms.vals) != 1 {
			t.Fatal("another person's delete reached KMS")
		}

		// Delete order: KMS first, and a KMS that fails leaves the account.
		r.kms.failDel = true
		if code, _ := do("DELETE", "/v1/sources/accounts/"+id, "alice", "acme", nil); code != 502 {
			t.Errorf("kms down: want 502, got %d", code)
		}
		if _, l := do("GET", "/v1/sources/accounts", "alice", "acme", nil); !strings.Contains(l, id) {
			t.Error("the row went although KMS did not forget the key")
		}
		r.kms.failDel = false
		r.kms.calls = nil
		code, body = do("DELETE", "/v1/sources/accounts/"+id, "alice", "acme", nil)
		if code != 200 || body != `{"id":"`+id+`","deleted":true}` {
			t.Fatalf("delete: %d %s", code, body)
		}
		if len(r.kms.calls) != 1 || !strings.HasPrefix(r.kms.calls[0], "delete ") || len(r.kms.vals) != 0 {
			t.Errorf("kms after delete: %v %v", r.kms.calls, r.kms.vals)
		}
		if _, l := do("GET", "/v1/sources/accounts", "alice", "acme", nil); strings.Contains(l, id) {
			t.Error("the row survived the delete")
		}

		// The value is in no response, no row, no audit record and no byte of the file.
		do("POST", "/v1/sources/accounts", "alice", "acme", newKey("openai", "second"))
		if strings.Contains(seen.String(), keyValue) || strings.Contains(seen.String(), "THIS-IS-A-SECRET") {
			t.Error("the key value appears in a response")
		}
		sto, err := r.sto.For(namespaceOf("acme"))
		if err != nil {
			t.Fatal(err)
		}
		for _, tbl := range []string{"sources", "source_providers", "source_cost", "source_snapshot", "provider_accounts"} {
			rows, err := sto.db.Query("SELECT * FROM " + tbl)
			if err != nil {
				t.Fatal(err)
			}
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				_ = rows.Scan(ptrs...)
				if strings.Contains(fmt.Sprint(vals...), "THIS-IS-A-SECRET") {
					t.Errorf("table %s holds the value: %v", tbl, vals)
				}
			}
			rows.Close()
		}
		recs, _, _ := r.audit.Query(context.Background(), audit.Filter{Limit: 100})
		for _, a := range recs {
			if b := fmt.Sprintf("%+v", a); strings.Contains(b, "THIS-IS-A-SECRET") {
				t.Errorf("audit holds the value: %s", b)
			}
		}
		var files int
		_ = filepath.Walk(r.dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				files++
				if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("THIS-IS-A-SECRET")) {
					t.Errorf("%s holds the value", p)
				}
			}
			return nil
		})
		if files == 0 && locus.DSN() == "" {
			t.Error("no database file was found to scan")
		}
		joined := ""
		for _, a := range recs {
			joined += a.Action + " "
		}
		if !strings.Contains(joined, "provider.account.create") || !strings.Contains(joined, "provider.account.delete") {
			t.Errorf("key add and delete are audited: %s", joined)
		}
	})
}

func TestAccountRefusals(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		for name, c := range map[string]struct {
			user string
			body map[string]any
			want int
		}{
			"login kind":    {"alice", map[string]any{"provider": "anthropic", "kind": "login", "label": "x", "value": "v"}, 400},
			"unknown":       {"alice", newKey("madeup", "x"), 400},
			"no label":      {"alice", newKey("openai", " "), 400},
			"no value":      {"alice", map[string]any{"provider": "openai", "kind": "key", "label": "x", "value": ""}, 400},
			"machine token": {"app:acme/ci", newKey("openai", "x"), 403},
			"no principal":  {"", newKey("openai", "x"), 401},
		} {
			if code, body := r.as("POST", "/v1/sources/accounts", c.user, "acme", c.body); code != c.want {
				t.Errorf("%s: want %d, got %d %s", name, c.want, code, body)
			}
		}
		if len(r.kms.calls) != 0 {
			t.Errorf("a refused request reached KMS: %v", r.kms.calls)
		}
		if code, _ := r.as("GET", "/v1/sources/accounts", "app:acme/ci", "acme", nil); code != 403 {
			t.Errorf("machine list: want 403, got %d", code)
		}
	})
}

func TestLoginsAreDerivedAndReadOnly(t *testing.T) {
	sqlite(t, func(t *testing.T) {
		r := mount(t)
		d := newDevice(t)
		id := r.link("alice", "acme", d)
		beat := d.beat(id, t0, []SourceProvider{{"claude", "claude-1", "login", "ready", "max", false}, {"anthropic", "key-1", "key", "ready", "", false}})
		if code, body := r.as("POST", "/v1/sources/"+id+"/heartbeat", "alice", "acme", beat); code != 200 {
			t.Fatalf("beat: %d %s", code, body)
		}
		_, list := r.as("GET", "/v1/sources/accounts", "alice", "acme", nil)
		want := `{"accounts":[{"id":"` + id + `:claude-1","provider":"claude","kind":"login","label":"claude-1","created":"2026-11-20T10:00:00Z","classes":["local","desktop"],"sourceId":"` + id + `","instance":"claude-1","state":"ready","plan":"max","shared":false}]}`
		if list != want {
			t.Errorf("logins\n got %s\nwant %s", list, want)
		}
		if code, _ := r.as("DELETE", "/v1/sources/accounts/"+id+":claude-1", "alice", "acme", nil); code != 400 {
			t.Errorf("delete a login: want 400, got %d", code)
		}
		// Logging out on the source removes it from the list.
		r.now = t0.Add(time.Minute)
		r.as("POST", "/v1/sources/"+id+"/heartbeat", "alice", "acme", d.beat(id, r.now, nil))
		if _, l := r.as("GET", "/v1/sources/accounts", "alice", "acme", nil); l != `{"accounts":[]}` {
			t.Errorf("after logout: %s", l)
		}
		// Another person never lists it.
		if _, l := r.as("GET", "/v1/sources/accounts", "bob", "acme", nil); l != `{"accounts":[]}` {
			t.Errorf("bob: %s", l)
		}
	})
}

func TestClassesFailClosed(t *testing.T) {
	for _, c := range []struct {
		provider, kind, source string
		want                   string
	}{
		{"anthropic", "key", "", "key"}, {"openrouter", "key", "", "key"},
		{"claude", "login", "desktop", "local,desktop"}, {"codex", "login", "desktop", "local,desktop"},
		{"copilot", "login", "desktop", "local,desktop"},
		{"gemini", "login", "desktop", "local"}, {"antigravity", "login", "desktop", "local"},
		{"cursor", "login", "desktop", "local"}, {"kiro", "login", "desktop", "local"},
		{"grok", "login", "desktop", "local"}, {"opencode", "login", "desktop", "local"}, {"pi", "login", "desktop", "local"},
		{"claude", "login", "browser", "local"}, {"chatgpt", "login", "browser", "local"},
		{"unknown", "login", "desktop", "local"}, {"unknown", "key", "", "local"}, {"copilot", "key", "", "local"}, {"", "login", "desktop", "local"},
	} {
		if got := strings.Join(classes(c.provider, c.kind, c.source), ","); got != c.want {
			t.Errorf("classes(%q,%q,%q) = %q, want %q", c.provider, c.kind, c.source, got, c.want)
		}
	}
}
