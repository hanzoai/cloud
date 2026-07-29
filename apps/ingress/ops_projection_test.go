package ingress

// The point of a typed op is that ONE registration feeds every projection. The
// wire tests next door (control_test.go) prove the REST half is unchanged; these
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
	if err := Mount(app, cloud.Deps{Logger: luxlog.NewNoOpLogger(), DataDir: t.TempDir()}); err != nil {
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
