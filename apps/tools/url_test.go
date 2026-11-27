package tools

// url_test.go — an MCP server's address never carries a credential out. One
// is refused in the URL at registration; a member is shown the server's scheme
// and host and not a path or query an admin may have put a key in; and a request
// that got no answer says so without spelling where it was sent.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/outbound"
)

func TestAServersURLCarriesNoCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, u := range []string{
			"https://user:pass@mcp.example.com/rpc",
			"https://token@mcp.example.com/rpc",
			"https://:pass@mcp.example.com/rpc",
		} {
			if err := validateServerURL(u); err == nil || !strings.Contains(err.Error(), "credential") {
				t.Fatalf("validateServerURL(%q) = %v, want a refusal naming the credential", u, err)
			}
		}
		app := newApp(t, nil)
		r := do(t, app, http.MethodPost, "/v1/tool/mcp/servers", "acme",
			map[string]any{"name": "Docs", "url": "https://bob:hunter2@docs.example.com/mcp"})
		if r.Code != http.StatusBadRequest || strings.Contains(string(r.Body), "hunter2") {
			t.Fatalf("registering a URL with a password: %d %s", r.Code, r.Body)
		}
	})
}

func TestAMemberIsShownTheServersHostAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := newApp(t, nil)
		const full = "https://docs.example.com/mcp/v2?key=SECRET_IN_QUERY"
		if r := do(t, app, http.MethodPost, "/v1/tool/mcp/servers", "acme",
			map[string]any{"name": "Docs", "url": full}); r.Code != http.StatusCreated {
			t.Fatalf("register: %d %s", r.Code, r.Body)
		}
		urls := func(r result) []string {
			t.Helper()
			var out mcpServerList
			if err := json.Unmarshal(r.Body, &out); err != nil || r.Code != http.StatusOK {
				t.Fatalf("list: %d %s %v", r.Code, r.Body, err)
			}
			var us []string
			for _, s := range out.Servers {
				us = append(us, s.URL)
			}
			return us
		}
		if got := urls(member(t, app, http.MethodGet, "/v1/tool/mcp/servers", "acme", nil)); len(got) != 1 || got[0] != "https://docs.example.com" {
			t.Fatalf("a member was shown %v, want the scheme and host alone", got)
		}
		if got := urls(do(t, app, http.MethodGet, "/v1/tool/mcp/servers", "acme", nil)); len(got) != 1 || got[0] != full {
			t.Fatalf("an admin was shown %v, want the whole URL", got)
		}
		if got := urls(send(t, app, http.MethodGet, "/v1/tool/mcp/servers", "acme", nil, true)); len(got) != 1 || got[0] != full {
			t.Fatalf("a SuperAdmin was shown %v, want the whole URL", got)
		}
	})
}

// A server that could not be reached is "upstream unreachable" to whoever called
// the tool — a member, or a run's harness through the relay — and the cause is
// still there for a caller that asks errors.Is.
func TestAnUnreachableServerIsNotNamedByItsAddress(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store, err := OpenMCPServerStore(t.TempDir(), testMaster, testKMS)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()
		if _, err := store.Write(context.Background(), MCPServer{
			ID: "m1", Org: "acme", Name: "x", URL: "http://127.0.0.1:9/rpc?key=SECRET_IN_QUERY",
		}, true); err != nil {
			t.Fatal(err)
		}
		p := newMCPProvider(store, nil)
		_, err = p.Dispatch(context.Background(), Principal{Org: "acme"}, "m1_echo", nil)
		if err == nil {
			t.Fatal("a dial the guard refuses answered")
		}
		if msg := err.Error(); strings.Contains(msg, "SECRET") || strings.Contains(msg, "127.0.0.1") || !strings.Contains(msg, "upstream unreachable") {
			t.Fatalf("the failure reads %q", msg)
		}
		if !errors.Is(err, outbound.ErrRefused) {
			t.Fatalf("the cause is gone: %v", err)
		}
	})
}
