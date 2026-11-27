package tax

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of this package's operations that are not
// typed ops, each with the wire fact that keeps it raw.
var untypedByDesign = map[string]string{
	"GET /v1/tax/forms/{id}/pdf": "answers PDF BYTES under a Content-Disposition. A typed op's only response path is c.JSON.",
	"GET /v1/tax/inbox/{id}/pdf": "answers PDF BYTES under a Content-Disposition. A typed op's only response path is c.JSON.",
}

func describedApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{Audit: rec, MasterKey: make([]byte, 32)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Shutdown(); _ = rec.Close() })
	return app
}

// Every route is a typed op or named above with the reason it cannot be, and
// every operation says what it does.
func TestEveryRouteIsTypedAndDescribed(t *testing.T) {
	app := describedApp(t)
	doc, err := spec.Of(app, spec.Info{Title: "tax", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}
	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/tax") || strings.HasSuffix(path, "/health") {
			continue
		}
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			served[key] = true
			if strings.TrimSpace(op.Description) == "" && strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s has no description — run: go generate ./apps/tax/", key)
			}
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/tax") {
			typed[key] = true
		}
	}
	var untyped []string
	for key := range served {
		if !typed[key] && untypedByDesign[key] == "" {
			untyped = append(untyped, key)
		}
	}
	sort.Strings(untyped)
	if len(untyped) > 0 {
		t.Errorf("served but neither typed nor named: %s", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] || typed[key] {
			t.Errorf("untypedByDesign names %q, which is not served untyped", key)
		}
	}
	if n := len(typed); n != 26 {
		t.Errorf("%d typed ops, want the 26 this surface declares", n)
	}
}

// A process with no audit trail describes the surface and refuses every change
// before making it.
func TestNoTrailNoChange(t *testing.T) {
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Use(app, cloud.Deps{MasterKey: make([]byte, 32)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	e := &env{t: t, app: app}
	body := map[string]any{"name": "Acme", "classification": CCorp, "tinType": EIN, "tin": "12-3456789",
		"address": map[string]any{"line1": "1 Market St", "city": "SF", "state": "CA", "zip": "94105"}}
	if code := e.do("PUT", "/v1/tax/profile", admin("acme"), body, nil); code != 503 {
		t.Fatalf("a change with no trail = %d, want 503", code)
	}
	if code := e.do("GET", "/v1/tax/profile", member("acme"), nil, nil); code != 404 {
		t.Fatalf("the refused change was made anyway: %d", code)
	}
}
