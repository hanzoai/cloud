package provider

// The point of a typed op is that ONE registration feeds every projection. This
// pins the two properties that would silently break it: the wire is unchanged
// (same method+path set the raw handlers served), and the ops that carry a real
// shape are actually IN the registry the OpenAPI document, the MCP tool list and
// the CLI are read from — a route that slips back to a raw handler is invisible to
// all three and nothing else would notice.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// typedOps is every route registered with zip.<Verb>(zapp, …) in routes(). A route
// leaving this list is a projection regression; one joining it is the migration
// working, and the list is the place to say so.
var typedOps = []string{
	"DELETE /v1/connection/:id",
	"DELETE /v1/provider/github/repos/:repo/pages",
	"GET /v1/connection",
	"GET /v1/connection/:id/token",
	"GET /v1/connection/providers",
	"POST /v1/connection/:id/refresh",
	"POST /v1/connection/:provider/credential",
	"POST /v1/connection/:provider/device",
	"POST /v1/connection/:provider/device/:flow/poll",
	"GET /v1/provider",
	"GET /v1/provider/:provider",
	"GET /v1/provider/github/installations",
	"GET /v1/provider/github/repos",
	"GET /v1/provider/github/repos/:owner/:repo/branches",
	"GET /v1/provider/github/user",
	"POST /v1/provider/github/user/connect",
	"POST /v1/provider/github/user/complete",
	"POST /v1/provider/github/user/disconnect",
	"GET /v1/provider/google/user",
	"POST /v1/provider/google/user/connect",
	"POST /v1/provider/google/user/complete",
	"POST /v1/provider/google/user/disconnect",
	"GET /v1/provider/gitlab/projects",
	"POST /v1/provider/github/search",
	"POST /v1/provider/github/fork",
	"GET /v1/provider/github/repos/:repo/pages",
	"POST /v1/provider/:provider/connect",
	"POST /v1/provider/:provider/disconnect",
	"POST /v1/provider/:provider/run",
	"POST /v1/provider/:provider/verify",
	"POST /v1/provider/github/claim",
	"POST /v1/provider/github/issues/backfill",
	"POST /v1/provider/slack/join",
	"GET /v1/provider/slack/channels",
	"GET /v1/provider/slack/messages",
	"PUT /v1/provider/slack/messages",
	"DELETE /v1/provider/slack/messages",
	"POST /v1/provider/slack/reactions",
	"POST /v1/provider/slack/search",
	"POST /v1/provider/slack/messages",
	"POST /v1/provider/linear/claim",
	"POST /v1/provider/linear/comments",
	"POST /v1/provider/linear/issues/backfill",
	"POST /v1/provider/github/repos/:repo/pages",
	"POST /v1/provider/github/repos/:repo/pages/builds",
	"POST /v1/provider/github/repos/import",
	"POST /v1/provider/telegram/connect",
	"POST /v1/provider/whatsapp/groups",
	"GET /v1/provider/whatsapp/groups",
	"PUT /v1/provider/github/repos/:repo/pages",
}

// rawRoutes is every route that stays a raw handler, with the reason it cannot be
// a typed op. Registered here so the reason is checked, not just written down.
//
// Three reasons remain, and all are properties of the WIRE, not of effort: a 302
// is not a JSON body (and zip.WithStatus takes 2xx only), a signature over the raw
// request bytes cannot be checked by an op handed the decoded In, and a body this
// package decodes only a SUBSET of cannot be an In without publishing that subset
// as the whole wire.
//
// "202 Accepted" was one of them until zip v1.18.2 gave WithStatus a vocabulary for
// it; /repos/import and /pages/builds are typed ops now.
var rawRoutes = map[string]string{
	"GET /v1/provider/:provider/callback":    "302 to the console, or the chooser page (text/html)",
	"POST /v1/provider/:provider/choose":     "a form post from the chooser page, answered with a 302",
	"GET /v1/provider/twitter/callback":      "302 to the console; the X app's other registered redirect",
	"GET /v1/provider/github/user/callback":  "302 to the console",
	"GET /v1/provider/google/user/callback":  "302 to the console",
	"GET /v1/provider/discord/link":          "302",
	"GET /v1/provider/discord/link/callback": "302",
	"GET /v1/provider/discord/link/discord":  "302",
	// The Marketplace / "Add to Slack" entry point. Unauthenticated BY DESIGN —
	// the person clicking Install in Slack's directory has no Hanzo session — and
	// it reveals nothing: the consent URL it 302s to carries only the PUBLIC
	// client_id and the scopes we would ask for anyway.
	"GET /v1/provider/slack/install":          "public — 302 to Slack consent",
	"GET /v1/provider/slack/link":             "302",
	"GET /v1/provider/slack/link/callback":    "302",
	"GET /v1/provider/slack/link/slack":       "302",
	"GET /v1/provider/teams/link":             "302",
	"GET /v1/provider/teams/link/aad":         "302",
	"GET /v1/provider/teams/link/callback":    "302",
	"GET /v1/provider/telegram/link":          "302",
	"GET /v1/provider/telegram/link/auth":     "302",
	"GET /v1/provider/telegram/link/callback": "302",
	"POST /v1/provider/github/webhook":        "HMAC over the raw body",
	"POST /v1/provider/forge/webhook":         "HMAC over the raw body",
	"POST /v1/provider/linear/webhook":        "HMAC over the raw body, with the organization's own secret",
	"POST /v1/provider/discord/interactions":  "Ed25519 over the raw body",
	"POST /v1/provider/openrouter/webhook":    "OTLP body, decoded as a subset",
	"POST /v1/provider/slack/commands":        "HMAC over the raw form body",
	"POST /v1/provider/slack/events":          "HMAC over the raw body",
	"POST /v1/provider/teams/events":          "Bot Framework JWT",
	"POST /v1/provider/telegram/webhook":      "secret token header",
	"GET /v1/provider/whatsapp/webhook":       "verify token, echoed challenge",
	"POST /v1/provider/whatsapp/webhook":      "X-Hub-Signature-256 over the raw body",
	"GET /v1/provider/slack/file":             "the file's own bytes, never JSON",
	"GET /v1/provider/:provider/logo":         "an image's own bytes, never JSON",
}

// TestSurfaceIsRegistered checks that every op above is a route on the live router
// and that the typed ones reached the registry, so the REST surface and the
// projected surfaces cannot drift apart.
func TestSurfaceIsRegistered(t *testing.T) {
	app := newApp(t, newKMS(t))

	live := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method == "HEAD" { // fiber mirrors every GET; not a surface of ours
			continue
		}
		live[r.Method+" "+r.Path] = true
	}
	// The surface is EXACTLY the typed ops plus the raw ones. A route that is
	// neither is a route nobody decided on.
	if len(live) != len(typedOps)+len(rawRoutes) {
		t.Errorf("live routes = %d, want %d (%d typed + %d raw)",
			len(live), len(typedOps)+len(rawRoutes), len(typedOps), len(rawRoutes))
	}
	for _, op := range typedOps {
		if !live[op] {
			t.Errorf("typed op %s is not a live route", op)
		}
	}
	for route, why := range rawRoutes {
		if !live[route] {
			t.Errorf("raw route %s (%s) is not a live route", route, why)
		}
	}

	// The registry, read through the CLI projection — the same a.ops the OpenAPI
	// document and the MCP tool list are built from.
	got := make([]string, 0, len(typedOps))
	for _, c := range app.Commands() {
		got = append(got, c.Method+" "+c.Path)
	}
	sort.Strings(got)
	want := append([]string(nil), typedOps...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("registry ops:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestProjectionsFailClosed is the security half of making these ops projections.
// zip publishes every typed op as an MCP tool and a CLI command, and NEITHER
// passes through the route group, so neither carries the channel that parks the
// validated org. Every org-scoped op must therefore refuse an invocation that
// arrives that way — with the same 401 an unauthenticated REST call gets, from the
// handler's own gate. A tenant-scoped op that answered here would serve data with
// no principal at all.
func TestProjectionsFailClosed(t *testing.T) {
	app := newApp(t, newKMS(t))
	for _, cmd := range app.Commands() {
		// A bare context: what LocalInvoke hands an op off the HTTP path.
		_, err := zip.LocalInvoke(context.Background(), cmd, nil, []byte(`{}`))
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
			t.Errorf("%s %s off the HTTP path: err=%v, want 401", cmd.Method, cmd.Path, err)
		}
	}
}

// TestSpecCarriesProse proves the doc comments are LIVE, not merely written. Go
// drops comments at compile time, so the only path from source to spec is the
// build-time cmd/zipdoc pass (//go:generate in ops.go) that emits zipdoc_gen.go. A
// handler comment edited without re-running it, or the generated file deleted,
// leaves the spec describing nothing — this fails when that happens.
func TestSpecCarriesProse(t *testing.T) {
	spec, err := json.Marshal(newApp(t, newKMS(t)).OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	for _, want := range []string{
		"catalog the console's Integrations page renders", // an op's prose
		"Providers is the whole catalog",                  // an Out field's prose
		"start=9f3c1d2e4b5a6c7d8e9f0a1b2c3d4e5f",          // a Response: example
		"cf-scoped-api-token",                             // an Example: request body
	} {
		if !strings.Contains(string(spec), want) {
			// The remediation names the directive's OWN package (ops.go), and
			// -run zipdoc so no unrelated generator fires. It used to name
			// ./clients/integrations, which does not exist — an error message
			// that sends you nowhere is worse than none, and only a reader who
			// tried it would ever find out.
			t.Errorf("openapi spec is missing %q — re-run `go generate -run zipdoc ./apps/provider/...`", want)
		}
	}
}
