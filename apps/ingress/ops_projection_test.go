package ingress

// The point of a typed op is that ONE registration feeds every projection. The
// neighbouring wire tests (control_test.go) prove the REST half is unchanged; these
// pin the other three, which nothing else would notice breaking: the ops are in
// the registry the OpenAPI document, the MCP tool list and the CLI are read from;
// they refuse an invocation that arrives off the HTTP path; and the prose the
// registry publishes is actually there.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// typedOps is the WHOLE /v1/ingress control plane. There is no raw-handler list
// beside it because there are no raw handlers left: every route this subsystem
// serves is a typed op. A route leaving this list is a projection regression.
var typedOps = []string{
	"DELETE /v1/ingress/middlewares/:id",
	"DELETE /v1/ingress/routes/:id",
	"DELETE /v1/ingress/services/:id",
	"GET /v1/ingress/middlewares",
	"GET /v1/ingress/middlewares/:id",
	"GET /v1/ingress/routes",
	"GET /v1/ingress/routes/:id",
	"GET /v1/ingress/services",
	"GET /v1/ingress/services/:id",
	"GET /v1/ingress/status",
	"GET /v1/ingress/tls",
	"POST /v1/ingress/middlewares",
	"POST /v1/ingress/routes",
	"POST /v1/ingress/services",
	"PUT /v1/ingress/middlewares/:id",
	"PUT /v1/ingress/routes/:id",
	"PUT /v1/ingress/services/:id",
	"PUT /v1/ingress/tls",
}

func newOpsApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_INGRESS_EDGE_ENABLED", "")
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	return app
}

// TestSurfaceIsRegistered checks that the live router serves EXACTLY these ops and
// that every one reached the registry, so the REST surface and the projected
// surfaces cannot drift apart.
func TestSurfaceIsRegistered(t *testing.T) {
	app := newOpsApp(t)

	live := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method == "HEAD" { // fiber mirrors every GET; not a surface of ours
			continue
		}
		if !strings.HasPrefix(r.Path, "/v1/ingress") {
			continue // zip's own /.well-known + docs routes, not this subsystem's
		}
		live[r.Method+" "+r.Path] = true
	}
	if len(live) != len(typedOps) {
		t.Errorf("live /v1/ingress routes = %d, want %d — a route nobody decided on",
			len(live), len(typedOps))
	}
	for _, op := range typedOps {
		if !live[op] {
			t.Errorf("typed op %s is not a live route", op)
		}
	}

	// The registry, read through the CLI projection — the same app.ops the OpenAPI
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
// passes through the route group, so neither carries the bridge that parks the
// request. The edge config is SuperAdmin-only, and admin-ness is a header fact, so
// every op must refuse an invocation that arrives that way — the same 403 an
// unauthenticated REST call gets, from the handler's own gate, with no second gate
// to keep in sync.
func TestProjectionsFailClosed(t *testing.T) {
	app := newOpsApp(t)
	for _, cmd := range app.Commands() {
		// A bare context: what LocalInvoke hands an op off the HTTP path.
		_, err := zip.LocalInvoke(context.Background(), cmd, nil, []byte(`{}`))
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Errorf("%s %s off the HTTP path: err=%v, want 403", cmd.Method, cmd.Path, err)
		}
	}
}

// TestSpecCarriesProse proves the doc comments are LIVE, not merely written. Go
// drops comments at compile time, so the only path from source to spec is the
// build-time cmd/zipdoc pass (//go:generate in ingress.go) that emits
// zipdoc_gen.go. A handler comment edited without re-running it, or the generated
// file deleted, leaves the spec describing nothing — this fails when that happens.
func TestSpecCarriesProse(t *testing.T) {
	spec, err := json.Marshal(newOpsApp(t).OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Description string `json:"description"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(spec, &doc); err != nil {
		t.Fatalf("spec: %v", err)
	}
	described := 0
	for path, methods := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/ingress") {
			continue
		}
		for method, op := range methods {
			if strings.TrimSpace(op.Description) == "" {
				t.Errorf("%s %s has no description — the prose never reached the document, "+
					"the MCP tool list or the SDK", strings.ToUpper(method), path)
				continue
			}
			described++
		}
	}
	if described != len(typedOps) {
		t.Errorf("described operations = %d, want %d", described, len(typedOps))
	}
}

// TestToolsCarryTheirProse pins the MCP projection, the sibling of the one above
// and the only one that goes QUIET instead of red. zip's tool list once read
// op.Summary — a field cloud sets nowhere, because the doc comment is the source —
// so every tool served an empty description over a nameless schema while the spec
// looked perfect (fixed in zip v1.17.6; an older zip silently reverts it). Nothing
// else in this package would notice: the spec test above reads a different field
// of the same registry entry. So: every op is a tool, every tool carries its
// handler's prose, and an op that takes input NAMES its fields — a model choosing
// a tool from a list is doing what a human reading the spec does, with the same
// words or with nothing.
func TestToolsCarryTheirProse(t *testing.T) {
	tools := map[string]map[string]any{}
	for _, tool := range newOpsApp(t).MCPTools() {
		name, _ := tool["name"].(string)
		tools[name] = tool
	}
	if len(tools) != len(typedOps) {
		t.Errorf("MCP tools = %d, want %d — an op no agent can call", len(tools), len(typedOps))
	}
	for name, tool := range tools {
		if d, _ := tool["description"].(string); strings.TrimSpace(d) == "" {
			t.Errorf("tool %s has no description — a nameless tool in every agent's list", name)
		}
	}
	// The five no-input ops (status, tls, and the three lists) take nothing off the
	// wire, so an empty schema is the truth for them. Every op that DOES take input
	// must name what it takes: id from the URL, the object's own fields from the body.
	for name, want := range map[string][]string{
		"post_ingress_routes":              {"host", "service", "tls"},
		"put_ingress_routes_by_id":         {"id", "host", "service"},
		"delete_ingress_routes_by_id":      {"id"},
		"get_ingress_routes_by_id":         {"id"},
		"post_ingress_services":            {"backends"},
		"post_ingress_middlewares":         {"type", "config"},
		"delete_ingress_middlewares_by_id": {"id"},
		"put_ingress_tls":                  {"extraHosts"},
	} {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("no MCP tool named %q — the op is invisible to agents", name)
			continue
		}
		in, _ := tool["inputSchema"].(map[string]any)
		props, _ := in["properties"].(map[string]any)
		for _, f := range want {
			if _, ok := props[f]; !ok {
				t.Errorf("tool %s inputSchema has no %q — the agent cannot know to send it", name, f)
			}
		}
	}
}
