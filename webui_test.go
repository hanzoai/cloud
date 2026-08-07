package cloud

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hanzoai/cloud/console"
	"github.com/zap-proto/zip"
)

func get(t *testing.T, app *zip.App, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", "http://x"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestTmpConsole(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Get("/v1/kv/:key", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"k": c.Param("key")}) })
	if err := mountConsole(app); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path   string
		status int
		ctype  string
		want   string
	}{
		{"/", 200, "text/html", "<title>cloud console</title>"},
		{"/console.css", 200, "text/css", "--accent"},
		{"/console.js", 200, "javascript", "openapi.json"},
		{"/kv", 200, "text/html", "<title>"},
		{"/deep/client/route", 200, "text/html", "console.js"},
		{"/../webui.go", 200, "text/html", "<title>"},
		{"/v1/nope", 404, "application/json", `{"error":{"code":"not_found","message":"no such route"}}`},
		{"/v1", 404, "application/json", "not_found"},
		{"/zap/x", 404, "application/json", "not_found"},
		{"/v1/kv/abc", 200, "application/json", `"k":"abc"`},
	}
	for _, c := range cases {
		res, body := get(t, app, c.path)
		if res.StatusCode != c.status {
			t.Errorf("%s: status %d want %d", c.path, res.StatusCode, c.status)
		}
		if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, c.ctype) {
			t.Errorf("%s: content-type %q want %q", c.path, ct, c.ctype)
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: body %q missing %q", c.path, body[:min(len(body), 120)], c.want)
		}
	}

	if err := serveFS(zip.New(zip.Config{}), fstest.MapFS{"other.txt": {Data: []byte("x")}}); err == nil {
		t.Error("serveFS accepted an FS with no index.html")
	}

	_ = console.FS()
}
