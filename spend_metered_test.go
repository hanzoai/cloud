package cloud

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

// TestMeteredSurfacesRequireStanding is the drift check spend.go has cited since the
// day meteredTrees was written — "TestMeteredSurfacesRequireStanding fails if the two
// drift" — for a test that did not exist. Three comments in this repo have now been
// caught describing a gate nobody built, which is worse than no gate: it reads as
// enforced, so nobody looks. It did drift, in six ways, and every one of them was a
// surface that charges a customer and could not be gated:
//
//	provisioning  routed at /v1/{datastore,docdb,kv,search,sql,vector} — the list
//	              said "/v1/provisioning/", a path nothing answers.
//	projects      answers /v1/sites; only /v1/projects was listed.
//	venue         answers /v1/cloud; absent entirely.
//	tools         answers /v1/skills, /v1/plugins, /v1/mcp/servers beside /v1/tools.
//	ask auto automations content flow platform tracker translate — missing outright.
//
// Price is a SOURCE fact: each plugin/<name>/main.go is its own composition root, so
// there is no list to walk at runtime. This reads the source, exactly as
// TestPriceDeclared does — the same walk, asking the next question.
func TestMeteredSurfacesRequireStanding(t *testing.T) {
	metered := meteredSurfaces(t)
	if len(metered) == 0 {
		t.Fatal("found no Price: cloud.Metered composition root — the pattern this test " +
			"recognises has moved, so it is asserting nothing")
	}

	declared := map[string]bool{}
	for _, name := range meteredApps {
		declared[name] = true
	}

	for _, name := range metered {
		if !declared[name] {
			t.Errorf("plugin/%s declares Price: cloud.Metered but is missing from meteredApps.\n"+
				"Money moves inside its handlers, so SpendGate must require standing before "+
				"they run — without the entry the surface is free the moment enforcement is "+
				"switched on. Add %q to meteredApps in spend.go.", name, name)
			continue
		}
		// The name is only half the fact. Assert the surface is actually reachable as
		// billable through the SAME resolution Billable performs, so a manifest row whose
		// prefixes move re-fails here rather than silently narrowing coverage.
		for _, prefix := range append([]string{"/v1/" + name}, manifest.PrefixesFor(name)...) {
			if prefix == "/v1" {
				continue // ai's terminal catch-all — see meteredPrefixes.
			}
			path := prefix + "/probe"
			if Reachable(path) {
				continue // the pay path is never gated, whatever declares it.
			}
			if !Billable(http.MethodPost, path) {
				t.Errorf("%s: POST %s is not Billable, but %s declares cloud.Metered.\n"+
					"meteredTrees is resolved from meteredApps through the manifest; if this "+
					"fails the two no longer agree about which paths the surface answers.",
					name, path, name)
			}
		}
	}

	// The reverse drift: an entry naming a surface that is no longer Metered would keep
	// gating a path nobody charges for, which is the outage half of the asymmetry.
	live := map[string]bool{}
	for _, name := range metered {
		live[name] = true
	}
	for _, name := range meteredApps {
		if !live[name] {
			t.Errorf("meteredApps lists %q, but plugin/%s does not declare Price: cloud.Metered.\n"+
				"Requiring standing for a surface nobody charges 402s a customer for free work.",
				name, name)
		}
	}
	t.Logf("%d metered surfaces resolve to %d billable trees", len(metered), len(meteredTrees))
}

// meteredSurfaces reads the app name out of every plugin/<name>/main.go that declares
// Price: cloud.Metered. The name comes from the DIRECTORY, which is the same key
// manifest.Apps and MountPrefixes use — gen-app-cmds already pins that a plugin
// directory and a manifest row are one-to-one.
func meteredSurfaces(t *testing.T) []string {
	t.Helper()
	roots, err := filepath.Glob(filepath.Join("plugin", "*", "main.go"))
	if err != nil {
		t.Fatalf("glob composition roots: %v", err)
	}
	if len(roots) == 0 {
		t.Fatal("no plugin/*/main.go found — this test is walking the wrong tree")
	}

	var out []string
	for _, root := range roots {
		src, err := os.ReadFile(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), root, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", root, err)
		}
		if pluginIsMetered(file) {
			out = append(out, filepath.Base(filepath.Dir(root)))
		}
	}
	return out
}

// pluginIsMetered reports whether a composition root declares Price: cloud.Metered.
// It matches the same []cloud.Plugin{{…}} literal TestPriceDeclared walks — the
// elements are implicit, so the slice type is what names cloud.Plugin.
func pluginIsMetered(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		arr, ok := lit.Type.(*ast.ArrayType)
		if !ok || !isCloudPlugin(arr.Elt) {
			return true
		}
		for _, elt := range lit.Elts {
			plugin, ok := elt.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, field := range plugin.Elts {
				kv, ok := field.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Price" {
					continue
				}
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok && sel.Sel.Name == "Metered" {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// A Free app's surface is not billable just because a metered app is routed a
// SHORTER prefix over the same tree.
//
// provisioning is metered and routed /v1/vector and /v1/search; product is Free
// and routed the more specific /v1/vector/collections, /v1/vector/stats,
// /v1/search/indexes and /v1/search/stats. A bare HasPrefix scan bills all four
// on provisioning's standing, which gates a Free product behind a balance. The
// router resolves by longest prefix; ownership has to as well.
func TestFreeSurfaceUnderAMeteredPrefixIsNotBillable(t *testing.T) {
	for _, p := range []string{
		"/v1/vector/collections",
		"/v1/vector/stats",
		"/v1/search/indexes",
		"/v1/search/stats",
	} {
		if Billable("POST", p) {
			t.Errorf("Billable(POST %s) = true; product declares cloud.Free and owns this path", p)
		}
	}

	// The metered tree around them still bills — this must not become a hole.
	for _, p := range []string{"/v1/vector", "/v1/vector/anything-else", "/v1/search/query"} {
		if !Billable("POST", p) {
			t.Errorf("Billable(POST %s) = false; provisioning is metered and owns this path", p)
		}
	}
}

// OwnerOf resolves by LONGEST prefix, which is what makes the check above sound.
func TestOwnerOfPrefersTheMoreSpecificApp(t *testing.T) {
	if got := manifest.OwnerOf("/v1/vector/collections"); got != "product" {
		t.Errorf("OwnerOf(/v1/vector/collections) = %q, want product", got)
	}
	if got := manifest.OwnerOf("/v1/vector"); got != "provisioning" {
		t.Errorf("OwnerOf(/v1/vector) = %q, want provisioning", got)
	}
	// ai is declared the bare /v1 catch-all, so an otherwise-unmatched /v1 path
	// legitimately resolves to it — that IS the routing, not a miss. Only a path
	// outside every declared prefix is unowned.
	if got := manifest.OwnerOf("/v1/nothing-routes-here"); got != "ai" {
		t.Errorf("OwnerOf(unmatched /v1) = %q, want ai (the catch-all)", got)
	}
	if got := manifest.OwnerOf("/healthz"); got != "" {
		t.Errorf("OwnerOf(outside every prefix) = %q, want empty", got)
	}
}
