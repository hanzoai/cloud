package host

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
//	              said "/v1/provisioning/", a path nothing answered then.
//	projects      answers /v1/sites; only /v1/project was listed.
//	venue         answers /v1/cloud; absent entirely.
//	tools         answered /v1/skills, /v1/plugins, /v1/mcp/servers beside /v1/tool;
//	              all three have since folded under it.
//	ask auto automations content flow platform todo translate — missing outright.
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
// manifest.Apps and UsePrefixes use — gen-app-cmds already pins that a plugin
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

// Billing follows the OWNER of a path, never the shortest tree that contains it.
//
// Prefixes nest both ways and each way has a failure. A Free app under a metered
// app's tree gets billed on its neighbour's standing — a 402 in front of
// something nobody charges for. A metered app under a FREE app's tree stops
// getting billed — a leak. Both are the same defect: a bare HasPrefix scan asks
// which tree the path falls in, when the question is who answers it.
//
// The nesting pairs are DERIVED from the manifest rather than named, because the
// fleet's are not stable. The pair this test was written for — product's four
// Free reads under provisioning's metered /v1/vector and /v1/search/query — is
// gone: product dissolved into the two capabilities that owned its roots, and it
// was the fleet's last metered-over-free nesting. The derived half is therefore
// allowed to find nothing; it fails the day the shape comes back, which is the
// only thing a ratchet has to do. The other direction is live and is asserted
// directly beneath it.
func TestBillingFollowsTheOwnerNotTheTree(t *testing.T) {
	for _, a := range manifest.Apps {
		if meteredSet[a.Name] {
			continue
		}
		for _, p := range manifest.PrefixesFor(a.Name) {
			if manifest.OwnerOf(p) != a.Name {
				continue // a deeper row answers here; not this app's address
			}
			if Billable("POST", p) {
				t.Errorf("Billable(POST %s) = true, but %s owns it and declares cloud.Free — "+
					"a metered neighbour's shorter prefix is being read as ownership", p, a.Name)
			}
		}
	}

	// lsp is metered at its own root now (/v1/lsp, out from under free code's
	// /v1/code — HIP-0139 §3). The live direction is the mirror: the metered
	// owner is billed at its own address, and free code beside it is not.
	for _, p := range []string{"/v1/lsp", "/v1/lsp/definition"} {
		if got := manifest.OwnerOf(p); got != "lsp" {
			t.Fatalf("OwnerOf(%s) = %q, want lsp — this test's subject moved", p, got)
		}
		if !Billable("POST", p) {
			t.Errorf("Billable(POST %s) = false; lsp owns this path and declares cloud.Metered", p)
		}
	}
	// The other half of "billing follows the owner": /v1/code/symbols reads like an
	// lsp address and is not one. code owns it and code's OWN declaration decides
	// it — which is a live claim in both directions now that code declares Metered
	// (its /ask synthesizes and its /search embeds, both through the wrapped AI
	// client). The check is that the answer comes from the owner, not that the
	// answer is no.
	if got := manifest.OwnerOf("/v1/code/symbols"); got != "code" {
		t.Fatalf("OwnerOf(/v1/code/symbols) = %q, want code — this test's subject moved", got)
	}
	if !Billable("POST", "/v1/code/symbols") {
		t.Error("Billable(POST /v1/code/symbols) = false; code owns this path and declares cloud.Metered")
	}
}
