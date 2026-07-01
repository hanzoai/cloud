package eval

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// TestTenantIgnoresClientProjectID pins the cross-tenant isolation invariant
// (RED MED-1): tenant() scopes the console API key pair (console-pk-{org}) and
// MUST use ONLY the sanitized org (c.Org(), set by SanitizeIdentity from the
// validated bearer owner), NEVER the client-controllable X-Project-Id. A forged
// `X-Project-Id: victim-org` must not change the resolved tenant — otherwise a
// caller reads another org's eval data via that org's KMS keys.
func TestTenantIgnoresClientProjectID(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/echo-tenant", func(c *zip.Ctx) error { return c.String(http.StatusOK, tenant(c)) })

	call := func(orgHeader, projectHeader string) string {
		req := httptest.NewRequest(http.MethodGet, "/echo-tenant", nil)
		if orgHeader != "" {
			req.Header.Set("X-Org-Id", orgHeader) // in prod: minted by SanitizeIdentity
		}
		if projectHeader != "" {
			req.Header.Set("X-Project-Id", projectHeader) // client-controllable sub-scope
		}
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	// A forged project id must be ignored — the tenant is the authoritative org.
	if got := call("maxpower", "victim-org"); got != "maxpower" {
		t.Fatalf("forged X-Project-Id leaked into tenant: got %q, want %q", got, "maxpower")
	}
	// No project header: still the org.
	if got := call("maxpower", ""); got != "maxpower" {
		t.Fatalf("tenant without project: got %q, want %q", got, "maxpower")
	}
	// A project id alone (no org) must NOT become the tenant (no cross-tenant via project).
	if got := call("", "victim-org"); got != "" {
		t.Fatalf("X-Project-Id alone became the tenant: got %q, want empty", got)
	}
}

func TestLoopbackBase(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://127.0.0.1:8080",
		"0.0.0.0:9000":   "http://127.0.0.1:9000",
		"127.0.0.1:8000": "http://127.0.0.1:8000",
		"":               "http://127.0.0.1:8080",
		"garbage":        "http://127.0.0.1:8080",
	}
	for in, want := range cases {
		if got := loopbackBase(in); got != want {
			t.Errorf("loopbackBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildMessages(t *testing.T) {
	// string input → single user message
	got := buildMessages("hello")
	if len(got) != 1 || got[0]["role"] != "user" || got[0]["content"] != "hello" {
		t.Fatalf("string input: got %+v", got)
	}

	// object with messages → passthrough
	in := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "s"},
		map[string]any{"role": "user", "content": "u"},
	}}
	got = buildMessages(in)
	if len(got) != 2 || got[0]["role"] != "system" || got[1]["content"] != "u" {
		t.Fatalf("messages passthrough: got %+v", got)
	}

	// object without messages → stringified single user message
	got = buildMessages(map[string]any{"prompt": "p"})
	if len(got) != 1 || got[0]["role"] != "user" || !strings.Contains(got[0]["content"].(string), "prompt") {
		t.Fatalf("object fallback: got %+v", got)
	}
}

func TestParseJudge(t *testing.T) {
	t.Run("clean json", func(t *testing.T) {
		v, r, err := parseJudge(`{"score": 0.8, "reasoning": "good"}`)
		if err != nil || v != 0.8 || r != "good" {
			t.Fatalf("got %v %q %v", v, r, err)
		}
	})
	t.Run("json embedded in prose", func(t *testing.T) {
		v, r, err := parseJudge("Here is my verdict: {\"score\": 1, \"reasoning\": \"x\"} done")
		if err != nil || v != 1 || r != "x" {
			t.Fatalf("got %v %q %v", v, r, err)
		}
	})
	t.Run("bare float", func(t *testing.T) {
		v, _, err := parseJudge("0.5")
		if err != nil || v != 0.5 {
			t.Fatalf("got %v %v", v, err)
		}
	})
	t.Run("clamped above 1", func(t *testing.T) {
		v, _, err := parseJudge(`{"score": 1.7}`)
		if err != nil || v != 1 {
			t.Fatalf("got %v %v", v, err)
		}
	})
	t.Run("clamped below 0", func(t *testing.T) {
		v, _, err := parseJudge(`{"score": -3}`)
		if err != nil || v != 0 {
			t.Fatalf("got %v %v", v, err)
		}
	})
	t.Run("unparseable is an error, not a fake score", func(t *testing.T) {
		if _, _, err := parseJudge("the model refused"); err == nil {
			t.Fatal("expected error for unparseable judge reply")
		}
	})
}

func TestNormalizeJudge(t *testing.T) {
	// nil → defaults to the model under test + default name/criteria
	d := normalizeJudge(nil, "gpt-4o-mini")
	if d.Model != "gpt-4o-mini" || d.Name != "llm-judge" || d.Criteria == "" {
		t.Fatalf("defaults: %+v", d)
	}
	// overrides win, model falls back to under-test when blank
	o := normalizeJudge(&judgeSpec{Name: "acc", Criteria: "match exactly"}, "m")
	if o.Model != "m" || o.Name != "acc" || o.Criteria != "match exactly" {
		t.Fatalf("overrides: %+v", o)
	}
}

func TestBasicAndClampAndAsText(t *testing.T) {
	if got := basic("pk", "sk"); got != base64.StdEncoding.EncodeToString([]byte("pk:sk")) {
		t.Fatalf("basic = %q", got)
	}
	if clamp01(0.3) != 0.3 || clamp01(-1) != 0 || clamp01(9) != 1 {
		t.Fatal("clamp01 wrong")
	}
	if asText("s") != "s" {
		t.Fatal("asText string")
	}
	if asText(map[string]any{"a": 1}) != `{"a":1}` {
		t.Fatalf("asText json = %q", asText(map[string]any{"a": 1}))
	}
	if asText(nil) != "" {
		t.Fatal("asText nil")
	}
}

func TestUUIDv4Shape(t *testing.T) {
	id := uuidv4()
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Fatalf("uuid shape: %q", id)
	}
	if id[14] != '4' { // version nibble
		t.Fatalf("uuid version: %q", id)
	}
	if id == uuidv4() {
		t.Fatal("uuid not unique")
	}
}

func TestResolveKeysFailsClosed(t *testing.T) {
	// No env keys, no KMS → precise, config-naming error (never a silent pass).
	s := &service{cfg: config{}}
	_, _, err := s.resolveKeys(context.Background(), "acme")
	if err == nil {
		t.Fatal("expected fail-closed error with no keys")
	}
	if !strings.Contains(err.Error(), "CONSOLE_PUBLIC_KEY") || !strings.Contains(err.Error(), "console-pk-acme") {
		t.Fatalf("error must name the missing config: %v", err)
	}

	// Global env pair present → returned.
	s = &service{cfg: config{publicKey: "pk", secretKey: "sk"}}
	pk, sk, err := s.resolveKeys(context.Background(), "acme")
	if err != nil || pk != "pk" || sk != "sk" {
		t.Fatalf("global keys: %q %q %v", pk, sk, err)
	}
}
