// Copyright © 2026 Hanzo AI. MIT License.

package surface_test

// A credential-less tools/call at the EDGE is told where to sign in; the same
// call on the plane is not challenged, because a sibling on the surface's socket
// carries its identity as headers the socket vouches for. Reading stays open on
// both: initialize, tools/list and describe need no credential.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/surface"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// nowhere reaches no subsystem: every call that gets past the MCP server is an
// outage, which is what makes the server's own answers observable on their own.
func nowhere(string) (string, string, error) { return "", "", errors.New("no subsystem here") }

func post(t *testing.T, app *zip.App, path, body string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://cloud"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

const callWithoutTool = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nothing","arguments":{}}}`

func TestACredentiallessCallAtTheEdgeIsChallenged(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	surface.Use(app, manifest.MCPPath, nil, nowhere)

	code, hdr, body := post(t, app, manifest.MCPPath, callWithoutTool, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("POST %s tools/call with no credential = %d, want 401: %s", manifest.MCPPath, code, body)
	}
	auth := hdr.Get("WWW-Authenticate")
	want := `resource_metadata="http://cloud` + manifest.ResourceMetadataPath + `"`
	if !strings.HasPrefix(auth, "Bearer ") || !strings.Contains(auth, want) {
		t.Fatalf("WWW-Authenticate = %q, want a Bearer challenge carrying %s", auth, want)
	}
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil || env.Error == nil {
		t.Fatalf("the 401 body is not a JSON-RPC error: %s", body)
	}

	// Any credential the identity boundary could read is enough to be forwarded
	// — the MCP server validates nothing. Past it, the only subsystem is nowhere,
	// so the answer is the server's own -32602 and a 200.
	for name, h := range map[string]map[string]string{
		"a bearer":         {"Authorization": "Bearer x"},
		"the alt spelling": {"X-Authorization": "Bearer x"},
		"a session":        {"Cookie": "session=x"},
	} {
		code, _, body := post(t, app, manifest.MCPPath, callWithoutTool, h)
		if code != 200 || !strings.Contains(body, "-32602") {
			t.Errorf("%s: = %d %s, want 200 with the MCP server's own unknown-tool error", name, code, body)
		}
	}

	// Reading is open.
	for _, m := range []string{"initialize", "tools/list", "ping"} {
		code, _, body := post(t, app, manifest.MCPPath, `{"jsonrpc":"2.0","id":1,"method":"`+m+`"}`, nil)
		if code != 200 {
			t.Errorf("%s with no credential = %d, want 200: %s", m, code, body)
		}
	}
}

func TestThePlaneEndpointIsNeverChallenged(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	d := surface.Use(zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}}), manifest.MCPPath, nil, nowhere)
	d.Serve(app, manifest.MCPPath, nowhere)

	code, _, body := post(t, app, manifest.MCPPath, callWithoutTool, nil)
	if code != 200 || !strings.Contains(body, "-32602") {
		t.Fatalf("the plane endpoint answered %d %s to a header-less call, want 200 with the MCP server's own unknown-tool error", code, body)
	}
}
