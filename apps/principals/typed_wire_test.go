package principals

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Every route is a typed op, and every operation says what it does.
func TestEveryRouteIsTypedAndDescribed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		was := published
		published = designations
		t.Cleanup(func() { published = was })
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

		doc, err := spec.Of(app, spec.Info{Title: "principal", Version: "v1"})
		if err != nil {
			t.Fatalf("spec: %v", err)
		}
		reg, err := spec.Typed(app)
		if err != nil {
			t.Fatalf("typed: %v", err)
		}
		served, typed := map[string]bool{}, map[string]bool{}
		for path, item := range doc.Paths {
			if !strings.HasPrefix(path, "/v1/principal") {
				continue
			}
			for method, op := range item {
				key := strings.ToUpper(method) + " " + path
				served[key] = true
				if strings.TrimSpace(op.Description) == "" && strings.TrimSpace(op.Summary) == "" {
					t.Errorf("%s has no description — run: go generate ./apps/principals/", key)
				}
			}
		}
		for key := range reg.Ops {
			if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/principal") {
				typed[key] = true
			}
		}
		var untyped []string
		for key := range served {
			if !typed[key] {
				untyped = append(untyped, key)
			}
		}
		sort.Strings(untyped)
		if len(untyped) > 0 {
			t.Errorf("served but not typed: %s", strings.Join(untyped, ", "))
		}
		if n := len(typed); n != 10 {
			t.Errorf("%d typed ops, want the 10 this surface declares", n)
		}
	})
}

// A process with no audit trail answers no clearance and decides no match.
func TestNoTrailNoDecision(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		was := published
		published = designations
		t.Cleanup(func() { published = was })
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		app.Use(cloud.Bridge())
		if err := Use(app, cloud.Deps{MasterKey: make([]byte, 32)}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = Shutdown() })
		e := &env{t: t, app: app}
		if code, _ := e.raw("POST", "/v1/principal/clearance", member("acme"), map[string]any{"payee": "globex", "amount": "1.00"}); code != 503 {
			t.Fatalf("a clearance with no trail = %d, want 503", code)
		}
		if code, _ := e.raw("POST", "/v1/principal/matches/hit_x", reviewer("admin"), map[string]any{"decision": "cleared", "note": "x"}); code != 503 {
			t.Fatalf("a decision with no trail = %d, want 503", code)
		}
	})
}
