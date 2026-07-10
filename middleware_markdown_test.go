package cloud

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// hit is an ordered search-result row: the JSON body preserves this field order,
// so the rendered markdown table columns are repo,path,line.
type hit struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

func mdTestApp(prefixes []string) *zip.App {
	app := zip.New(zip.Config{})
	app.Use(MarkdownNegotiation(prefixes))
	app.Get("/v1/code/search", func(c *zip.Ctx) error {
		return c.JSON(200, []hit{
			{"hanzoai/cloud", "serve.go", 76},
			{"hanzoai/iam", "auth.go", 142},
		})
	})
	app.Get("/v1/plain", func(c *zip.Ctx) error {
		return c.String(200, "hello")
	})
	app.Get("/v1/boom", func(c *zip.Ctx) error {
		return zip.ErrBadRequest("nope")
	})
	return app
}

func mdRequest(t *testing.T, app *zip.App, target string, headers map[string]string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

func TestMarkdown_FormatQuery(t *testing.T) {
	code, ct, body := mdRequest(t, mdTestApp(nil), "/v1/code/search?format=md", nil)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content-type = %q, want text/markdown", ct)
	}
	if !strings.Contains(body, "| repo | path | line |") {
		t.Fatalf("body is not the expected table:\n%s", body)
	}
	if !strings.Contains(body, "| hanzoai/cloud | serve.go | 76 |") {
		t.Fatalf("missing row:\n%s", body)
	}
}

func TestMarkdown_AcceptHeader(t *testing.T) {
	_, ct, body := mdRequest(t, mdTestApp(nil), "/v1/code/search", map[string]string{"Accept": "text/markdown"})
	if !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("content-type = %q, want text/markdown", ct)
	}
	if !strings.Contains(body, "| repo | path | line |") {
		t.Fatalf("not a table:\n%s", body)
	}
}

func TestMarkdown_DefaultIsJSON(t *testing.T) {
	_, ct, body := mdRequest(t, mdTestApp(nil), "/v1/code/search", nil)
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json (machine default)", ct)
	}
	var v []hit
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("default response is not valid JSON: %v\n%s", err, body)
	}
	if len(v) != 2 || v[0].Repo != "hanzoai/cloud" {
		t.Fatalf("json body altered: %+v", v)
	}
}

func TestMarkdown_PrefixDefaultsToMarkdown(t *testing.T) {
	// With /v1/code/ registered as a default-markdown prefix, no Accept/format
	// still yields markdown.
	_, ct, _ := mdRequest(t, mdTestApp([]string{"/v1/code/"}), "/v1/code/search", nil)
	if !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("prefix default not honored: content-type = %q", ct)
	}
}

func TestMarkdown_AcceptJSONOverridesPrefix(t *testing.T) {
	// A caller on a markdown-default endpoint can still force JSON.
	_, ct, _ := mdRequest(t, mdTestApp([]string{"/v1/code/"}), "/v1/code/search",
		map[string]string{"Accept": "application/json"})
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Accept: application/json did not override prefix: %q", ct)
	}
	_, ct2, _ := mdRequest(t, mdTestApp([]string{"/v1/code/"}), "/v1/code/search?format=json", nil)
	if !strings.HasPrefix(ct2, "application/json") {
		t.Fatalf("?format=json did not override prefix: %q", ct2)
	}
}

func TestMarkdown_NonJSONUntouched(t *testing.T) {
	// A text/plain handler is never transformed, even when markdown is requested.
	_, ct, body := mdRequest(t, mdTestApp(nil), "/v1/plain?format=md", nil)
	if strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("plain response wrongly transformed to markdown")
	}
	if body != "hello" {
		t.Fatalf("plain body altered: %q", body)
	}
}

func TestMarkdown_ErrorStaysJSON(t *testing.T) {
	// A handler error is a JSON error body — a formatting choice never rewrites it.
	code, ct, body := mdRequest(t, mdTestApp(nil), "/v1/boom?format=md", nil)
	if code != 400 {
		t.Fatalf("status %d, want 400", code)
	}
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("error content-type = %q, want application/json", ct)
	}
	if !strings.Contains(body, "nope") {
		t.Fatalf("error body: %s", body)
	}
}

func TestMarkdown_VaryHeaderSet(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/code/search", nil)
	resp, err := mdTestApp(nil).Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Vary"); got != "Accept" {
		t.Fatalf("Vary = %q, want Accept", got)
	}
}

func TestWantsMarkdown(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		accept   string
		format   string
		prefixes []string
		want     bool
	}{
		{"format md", "/v1/x", "", "md", nil, true},
		{"format markdown", "/v1/x", "", "markdown", nil, true},
		{"format json wins over md accept", "/v1/x", "text/markdown", "json", nil, false},
		{"accept markdown", "/v1/x", "text/markdown", "", nil, true},
		{"accept json", "/v1/x", "application/json", "", nil, false},
		{"accept md preferred by q", "/v1/x", "application/json;q=0.5, text/markdown;q=0.9", "", nil, true},
		{"accept json preferred by q", "/v1/x", "text/markdown;q=0.3, application/json;q=0.9", "", nil, false},
		{"accept star only falls to prefix", "/v1/code/y", "*/*", "", []string{"/v1/code/"}, true},
		{"no signal, no prefix", "/v1/x", "", "", nil, false},
		{"prefix match", "/v1/agents/run", "", "", []string{"/v1/agents/"}, true},
		{"prefix miss", "/v1/models", "", "", []string{"/v1/agents/"}, false},
		{"accept json overrides prefix", "/v1/code/y", "application/json", "", []string{"/v1/code/"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wantsMarkdown(tc.path, tc.accept, tc.format, tc.prefixes); got != tc.want {
				t.Fatalf("wantsMarkdown(%q,%q,%q,%v) = %v, want %v",
					tc.path, tc.accept, tc.format, tc.prefixes, got, tc.want)
			}
		})
	}
}

func TestIsJSONContentType(t *testing.T) {
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "APPLICATION/JSON"} {
		if !isJSONContentType(ct) {
			t.Fatalf("%q should be JSON", ct)
		}
	}
	for _, ct := range []string{"text/plain", "text/markdown", "text/event-stream", ""} {
		if isJSONContentType(ct) {
			t.Fatalf("%q should not be JSON", ct)
		}
	}
}
