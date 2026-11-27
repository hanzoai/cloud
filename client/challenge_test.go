// Copyright © 2026 Hanzo AI. MIT License.

package client_test

// A credential-less initialize or tools/call at the EDGE is told where to sign in;
// the same calls on the plane are not challenged, because a sibling on the
// surface's socket carries its identity as headers the socket vouches for. What is
// listed stays open on both: tools/list, describe and ping need no credential.
// initialize does not, because a client that is answered 200 to its first request
// believes the server is open and never offers its person a sign-in.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/mcp"
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	client.Use(app, manifest.MCPPath, nil, nowhere)

	code, hdr, body := post(t, app, manifest.MCPPath, callWithoutTool, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("POST %s tools/call with no credential = %d, want 401: %s", manifest.MCPPath, code, body)
	}
	auth := hdr.Get("WWW-Authenticate")
	want := `resource_metadata="http://cloud` + mcp.MetadataMCP + `"`
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

	// Reading what is listed is open.
	for _, m := range []string{"tools/list", "ping"} {
		code, _, body := post(t, app, manifest.MCPPath, `{"jsonrpc":"2.0","id":1,"method":"`+m+`"}`, nil)
		if code != 200 {
			t.Errorf("%s with no credential = %d, want 200: %s", m, code, body)
		}
	}
}

// The first thing an MCP client sends is initialize (or, under the stateless
// revision, server/discover). Answered 200 it never learns it must sign in, which
// is why Claude.ai, ChatGPT, Codex, Cursor and the rest could not offer "sign in"
// to this server. With nothing to act as it is 401 and a challenge that names the
// MCP server's own resource metadata; with anything the identity boundary can read
// it is the server's answer, and a notification is still acknowledged.
func TestInitializeAtTheEdgeNeedsACredential(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	client.Use(app, manifest.MCPPath, nil, nowhere)

	for _, m := range []string{"initialize", "server/discover"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + m + `","params":{"protocolVersion":"2025-06-18"}}`
		code, hdr, out := post(t, app, manifest.MCPPath, body, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("%s with no credential = %d, want 401: %s", m, code, out)
		}
		want := `Bearer resource_metadata="http://cloud/.well-known/oauth-protected-resource/v1/mcp"`
		if got := hdr.Get("WWW-Authenticate"); got != want {
			t.Errorf("%s: WWW-Authenticate = %q, want %q", m, got, want)
		}

		for name, h := range map[string]map[string]string{
			"a bearer":         {"Authorization": "Bearer x"},
			"the alt spelling": {"X-Authorization": "Bearer x"},
			"a session":        {"Cookie": "session=x"},
		} {
			code, _, out := post(t, app, manifest.MCPPath, body, h)
			if code != 200 || !strings.Contains(out, `"serverInfo"`) {
				t.Errorf("%s with %s = %d %s, want the server's answer", m, name, code, out)
			}
		}
	}

	// A notification has no id and expects no answer; it is not a connection.
	code, _, _ := post(t, app, manifest.MCPPath, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if code != http.StatusAccepted {
		t.Errorf("notifications/initialized = %d, want 202", code)
	}
}

// The plane never challenges initialize either: a sibling's identity is the
// socket's to vouch for, and it cannot sign in.
func TestThePlaneAnswersInitializeWithoutACredential(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	d := client.Use(zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}}), manifest.MCPPath, nil, nowhere)
	d.Serve(app, manifest.MCPPath, nowhere)

	code, _, body := post(t, app, manifest.MCPPath, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if code != 200 || !strings.Contains(body, `"serverInfo"`) {
		t.Errorf("the plane's initialize = %d %s, want 200", code, body)
	}
}

func TestThePlaneEndpointIsNeverChallenged(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	d := client.Use(zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}}), manifest.MCPPath, nil, nowhere)
	d.Serve(app, manifest.MCPPath, nowhere)

	code, _, body := post(t, app, manifest.MCPPath, callWithoutTool, nil)
	if code != 200 || !strings.Contains(body, "-32602") {
		t.Fatalf("the plane endpoint answered %d %s to a header-less call, want 200 with the MCP server's own unknown-tool error", code, body)
	}
}

// The challenge names the host the request was sent to, read off its own Host
// header, even behind a host that trusts its proxies: a caller's X-Forwarded-Host
// reaches neither the header nor the body.
func TestTheChallengeNamesTheRequestsOwnHost(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}, TrustProxy: true,
		TrustedProxies: []string{"0.0.0.0/0", "::/0"}, ProxyHeader: "X-Forwarded-For"})
	client.Use(app, manifest.MCPPath, nil, nowhere)
	req, err := http.NewRequest(http.MethodPost, "http://api.lux.network"+manifest.MCPPath, strings.NewReader(callWithoutTool))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "red-probe.example")
	req.Header.Set("X-Forwarded-Proto", "http")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	want := `Bearer resource_metadata="https://api.lux.network` + mcp.MetadataMCP + `"`
	if got := resp.Header.Get("WWW-Authenticate"); resp.StatusCode != http.StatusUnauthorized || got != want {
		t.Errorf("tools/call on api.lux.network = %d, WWW-Authenticate %q, want 401 and %q", resp.StatusCode, got, want)
	}
	if strings.Contains(string(b), "red-probe") {
		t.Errorf("the challenge body names the forwarded host: %s", b)
	}
}

// bearerExpiring is a JWT bearer whose payload expires at exp. The signature is
// not one: the MCP server never reads it, and the owning plugin is who verifies.
func bearerExpiring(exp int64) string {
	enc := base64.RawURLEncoding.EncodeToString
	return "Bearer " + enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp))) + ".sig"
}

// A credential that has run out is not "no credential": the client held one, and
// the MCP authorization spec has the server say so (401 invalid_token) so the
// client refreshes or signs in again, instead of reading the plugin's refusal as
// a tool error and carrying on with a dead token. Every request is answered so,
// tools/list included, because the client is wrong to keep sending it.
func TestAnExpiredBearerAtTheEdgeIsInvalidToken(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	client.Use(app, manifest.MCPPath, nil, nowhere)
	dead := map[string]string{"Authorization": bearerExpiring(time.Now().Add(-time.Minute).Unix())}

	for _, m := range []string{"initialize", "tools/list", "tools/call"} {
		code, hdr, body := post(t, app, manifest.MCPPath, `{"jsonrpc":"2.0","id":1,"method":"`+m+`","params":{}}`, dead)
		if code != http.StatusUnauthorized {
			t.Errorf("%s with an expired bearer = %d, want 401: %s", m, code, body)
			continue
		}
		auth := hdr.Get("WWW-Authenticate")
		for _, want := range []string{`error="invalid_token"`, `resource_metadata="http://cloud` + mcp.MetadataMCP + `"`} {
			if !strings.Contains(auth, want) {
				t.Errorf("%s: WWW-Authenticate = %q, want %s", m, auth, want)
			}
		}
	}

	// What is not expired, or is not a JWT at all, is the plugin's to judge.
	for name, h := range map[string]string{
		"a live JWT":        bearerExpiring(time.Now().Add(time.Hour).Unix()),
		"a JWT with no exp": "Bearer " + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".sig",
		"an API key":        "Bearer hk-0123456789abcdef",
		"not a bearer":      "Basic dTpw",
		"a malformed JWT":   "Bearer a.%%%.c",
	} {
		code, _, body := post(t, app, manifest.MCPPath, callWithoutTool, map[string]string{"Authorization": h})
		if code != 200 || !strings.Contains(body, "-32602") {
			t.Errorf("%s = %d %s, want the server's own answer", name, code, body)
		}
	}

	// The plane vouches for its siblings and never reads a bearer.
	plane := zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	client.Use(zip.New(zip.Config{DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}}), manifest.MCPPath, nil, nowhere).
		Serve(plane, manifest.MCPPath, nowhere)
	if code, _, body := post(t, plane, manifest.MCPPath, callWithoutTool, dead); code != 200 {
		t.Errorf("the plane answered an expired bearer %d %s, want 200", code, body)
	}
}
