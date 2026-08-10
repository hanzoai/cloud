package dataset

// wire_test.go makes "every route is a typed op" a GATE instead of a paragraph.
// Prose cannot fail: a route added tomorrow as a raw func(*zip.Ctx) error would
// leave the claim standing and the route invisible to the document, the MCP tool
// list, the CLI and every generated SDK. Here the claim is a test, so the route
// that falsifies it says so.
//
// This plane has NO untyped route and no exemption list. Nothing here answers
// with a status the typed envelope cannot carry, and nothing here relays opaque
// bytes — so the closed list of exceptions is empty, and this test asserts that
// it stays empty.

import (
	"maps"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// served is every operation the live router answers, and typed is the subset
// carrying a registry entry — read from the SAME mount at one instant, so the
// document and the tool catalogue cannot be compared against different routers.
func projections(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("datasettest"), DisableStartupMessage: true})
	compose(app)
	if err := mount(newPlane(&fake{}), app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	doc, err := openapi.Spec(app, openapi.Info{Title: "dataset", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	maps.Copy(typed, reg.Ops)
	return served, typed
}

// TestEveryRouteIsATypedOp — no exemptions, and the empty exemption list is the
// assertion.
func TestEveryRouteIsATypedOp(t *testing.T) {
	served, typed := projections(t)
	if len(served) == 0 {
		t.Fatal("the plane serves nothing")
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; !ok {
			untyped = append(untyped, key)
		}
	}
	sort.Strings(untyped)
	if len(untyped) > 0 {
		t.Fatalf("these operations are not typed ops, so no SDK, CLI or MCP tool exists for them:\n  %s",
			strings.Join(untyped, "\n  "))
	}
}

// TestTheSurfaceIsExactlyWhatItSays freezes the address of every leaf. A path is
// the identity every projection keys on, so a rename is a breaking change to five
// generated artefacts at once and must be a deliberate edit here.
func TestTheSurfaceIsExactlyWhatItSays(t *testing.T) {
	want := []string{
		"DELETE /v1/risk/datasets/{name}",
		"GET /v1/risk/datasets",
		"GET /v1/risk/datasets/{name}",
		"GET /v1/risk/datasets/{name}/export",
		"GET /v1/risk/datasets/{name}/lineage",
		"POST /v1/risk/datasets",
		"POST /v1/risk/datasets/{name}/materialize",
	}
	served, _ := projections(t)
	var got []string
	for key := range served {
		got = append(got, key)
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the surface moved:\n want:\n  %s\n got:\n  %s",
			strings.Join(want, "\n  "), strings.Join(got, "\n  "))
	}
}

// TestEveryOpIsNamedTaggedAndDescribed. An operation id is what an SDK method and
// a CLI command are called; a tag is how the document groups it; a description is
// the whole of what an MCP tool tells an agent. A blank one of any of the three
// ships a surface nobody can use without reading this source.
func TestEveryOpIsNamedTaggedAndDescribed(t *testing.T) {
	_, typed := projections(t)
	ids := map[string]string{}
	for key, op := range typed {
		if op.OperationID == "" {
			t.Errorf("%s has no operation id", key)
			continue
		}
		if prev, dup := ids[op.OperationID]; dup {
			t.Errorf("operation id %q is claimed by both %s and %s", op.OperationID, prev, key)
		}
		ids[op.OperationID] = key
		if !strings.HasPrefix(op.OperationID, product) {
			t.Errorf("%s is called %q; every leaf of this plane belongs to the %s face", key, op.OperationID, product)
		}
		if len(op.Tags) == 0 {
			t.Errorf("%s carries no tag", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s has no summary", key)
		}
		if len(strings.TrimSpace(op.Description)) < 80 {
			t.Errorf("%s has no usable description; an MCP tool is its description", key)
		}
	}
}

// TestTheManifestNamesWhatTheRouterServes is the pair check that inference went
// down for the want of: the host routes by the manifest's prefixes, the router
// answers its own addresses, and nothing but this compares the two.
func TestTheManifestNamesWhatTheRouterServes(t *testing.T) {
	prefixes := manifest.PrefixesFor("dataset")
	if len(prefixes) == 0 {
		t.Fatal("the manifest carries no row for this app, so the host would route none of it")
	}
	served, _ := projections(t)
	for key := range served {
		_, path, _ := strings.Cut(key, " ")
		covered := false
		for _, p := range prefixes {
			if path == p || strings.HasPrefix(path, p+"/") {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s is served but no manifest prefix reaches it", key)
		}
	}
	for _, p := range prefixes {
		reached := false
		for key := range served {
			_, path, _ := strings.Cut(key, " ")
			if path == p || strings.HasPrefix(path, p+"/") {
				reached = true
				break
			}
		}
		if !reached {
			t.Errorf("the manifest claims %q and the router answers nothing under it", p)
		}
	}
}
