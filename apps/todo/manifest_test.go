package todo_test

// The routed-surface gate for todo. It is in an EXTERNAL test package
// (todo_test) because it mounts the app the way a host does — through the
// exported Mount — and because internal/manifesttest imports cloud, which the
// in-package tests do not need.
//
// What it caught: /todo (the embedded board SPA, the whole reason
// todo.hanzo.ai exists) was registered by Mount and claimed by no manifest
// prefix, so in the fleet the host's page reached whoever owns "/" and the
// visitor got the console's HTML shell with a 200. Every other gate was green —
// the SPA is an untyped route, so it is in no openapi.json and the published-path
// oracle had nothing to compare.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/todo"
	"github.com/hanzoai/cloud/internal/manifesttest"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

func TestEveryRouteTodoServesIsRoutedToIt(t *testing.T) {
	// The harness mounts the app as a deployed process would, and a deployed process
	// holds the shared anti-forgery key its writes verify against (apps/account, Shared).
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	manifesttest.Case{
		Name:  "todo",
		Mount: todo.Mount,
		// zip's own per-process control plane (the document, the agent MCP server,
		// the op plane) is served by the HOST for itself, not routed per app. Every
		// app inherits it, so no app's row claims it.
		Exempt: func(p string) bool {
			return strings.HasPrefix(p, "/.well-known/zip") ||
				strings.HasPrefix(p, "/mcp") ||
				strings.HasPrefix(p, "/openapi") ||
				p == "/health" || p == "/healthz" || p == "/readyz"
		},
	}.Run(t)
	t.Cleanup(func() { _ = todo.Shutdown() })
}
