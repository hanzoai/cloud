package cloud_test

// Two facts about a subsystem live in two different files, and until this test
// nothing made them agree. Both disagreements shipped, and each cost a release
// cycle.
//
//   - What a subsystem OWNS is in plugin/<app>/main.go — the app's own
//     composition root, the cloud.Plugin it hands cloud.Listen.
//   - What it SERVES is the route table its live router composes, projected at
//     build time into plugin/<app>/openapi.json (describe.go: ONE mount of ONE
//     registry, generated from the app's OWN router, from the code alone).
//
// The projection is a committed artifact and `make test`'s check
// regenerates it and fails on any diff, so reading it here is reading the router
// — one hop, with a check on the hop.
//
// DERIVED, NEVER LISTED. The specs are parsed out of the mains and the routes
// read off the projections, so a subsystem added tomorrow is checked tomorrow
// and one nobody remembers is checked anyway. A hand-kept list is exactly the
// artifact that would have gone stale before the outage it was written for.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/manifest"
)

// spec is one cloud.Plugin as its main declares it, plus the routes the app's
// projection says the program composed.
type spec struct {
	dir        string   // plugin/<dir> — so a failure names the file to open
	name       string   // cloud.Plugin.Name
	ownsHealth bool     // cloud.Plugin.OwnsHealth
	prefixes   []string // cloud.Plugin.Prefixes, as declared (nil = the /v1/<name> default)
	global     bool     // App instead of Use:  the whole binary, bounded by nothing
	paths      []string // every address the projection says this dir's program answers
}

// owns is production's rule, not a copy of it: UsePrefixes fills the
// /v1/<name> default, and `under` is the same subtree test scope.go applies to
// every request.
func (s spec) owns(path string) bool {
	for _, p := range cloud.UsePrefixes(s.name, s.prefixes) {
		if under(path, p) {
			return true
		}
	}
	return false
}

// under is scope.go's rule, which is unexported there. "/v1/kms" is under
// "/v1/kms" and under nothing shorter; "/v1/kmsx" is under neither.
func under(path, p string) bool {
	return path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/")
}

// param puts the two spellings of a path parameter into one. A router declares
// :org and an OpenAPI document writes {org}; they are the same segment, and
// comparing them raw reports entitlements and skills as escaping their own
// declared prefix.
var param = strings.NewReplacer("{", ":", "}", "")

// specs reads every app under plugin/. An app whose main declares no
// cloud.Plugin is not one of these and is skipped by DERIVATION, not by name:
// it builds its own router and never reaches serve.go's liveness loop, so
// neither rule below has anything to say about it. o11y is the case — hand
// written, mounts itself, registers its own health directly.
func specs(t *testing.T) []spec {
	t.Helper()
	dirs, err := filepath.Glob("plugin/*/main.go")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no plugin mains found: %v", err)
	}
	var out []spec
	for _, main := range dirs {
		dir := filepath.Base(filepath.Dir(main))
		file, err := parser.ParseFile(token.NewFileSet(), main, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", main, err)
		}
		declared := plugins(file)
		if len(declared) == 0 {
			continue
		}
		paths := served(t, filepath.Join("plugin", dir, cloud.SpecFile))
		for i := range declared {
			declared[i].dir, declared[i].paths = dir, paths
		}
		out = append(out, declared...)
	}
	if len(out) < 100 {
		t.Fatalf("parsed %d specs from plugin/*/main.go — the reader is broken, not the fleet", len(out))
	}
	return out
}

// plugins pulls every cloud.Plugin literal out of one main. The shape is fixed
// by plugin/gen-app-cmds — `cloud.Listen([]cloud.Plugin{{…}}, …)` — and one main
// (plugin/ai) declares two.
func plugins(file *ast.File) []spec {
	var out []spec
	ast.Inspect(file, func(n ast.Node) bool {
		lit, isLit := n.(*ast.CompositeLit)
		if !isLit {
			return true
		}
		arr, isArr := lit.Type.(*ast.ArrayType)
		if !isArr || !isSel(arr.Elt, "cloud", "Plugin") {
			return true
		}
		for _, el := range lit.Elts {
			e, isStruct := el.(*ast.CompositeLit)
			if !isStruct {
				continue
			}
			var s spec
			for _, f := range e.Elts {
				kv, isKV := f.(*ast.KeyValueExpr)
				if !isKV {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				switch key.Name {
				case "Name":
					s.name = str(kv.Value)
				case "OwnsHealth":
					id, _ := kv.Value.(*ast.Ident)
					s.ownsHealth = id != nil && id.Name == "true"
				case "App":
					s.global = true
				case "Prefixes":
					s.prefixes = prefixes(kv.Value)
				}
			}
			if s.name != "" {
				out = append(out, s)
			}
		}
		return false
	})
	return out
}

// prefixes evaluates the Prefixes expression. There are exactly three spellings
// in the tree and each is answered by CALLING the real function, so this test
// cannot drift from the manifest it reads: PrefixesFor (what the host routes
// here), GrantFor (what a co-resident app may wrap — zen), and a literal.
func prefixes(v ast.Expr) []string {
	switch e := v.(type) {
	case *ast.CallExpr:
		if len(e.Args) != 1 {
			return nil
		}
		switch {
		case isSel(e.Fun, "manifest", "PrefixesFor"):
			return manifest.PrefixesFor(str(e.Args[0]))
		case isSel(e.Fun, "manifest", "GrantFor"):
			return manifest.GrantFor(str(e.Args[0]))
		}
	case *ast.CompositeLit:
		var out []string
		for _, el := range e.Elts {
			if s := str(el); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func isSel(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

func str(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// served reads the addresses out of an app's committed projection.
func served(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — every app main that declares a cloud.Plugin has one", path, err)
	}
	var doc struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		out = append(out, param.Replace(p))
	}
	sort.Strings(out)
	return out
}

// TestHealthOwnershipMatchesWhatIsRegistered is the duplicate-declaration
// outage. serve.go registers a generic GET /v1/<name>/health for every enabled
// subsystem that does NOT set OwnsHealth. That makes OwnsHealth a claim with two
// halves, and each half fails differently:
//
//   - Claim it FALSELY — register /v1/<name>/health in Mount and leave the field
//     unset — and one address is declared twice. zip refuses the whole program
//     at boot, so the binary builds, links, passes vet and crash-loops. This is
//     what authz, domain, experiments and metrics did.
//
//   - Claim it and OWN NOTHING and the failure is the opposite kind: the program
//     composes perfectly and the address is simply gone, because the field's
//     ONLY effect is to suppress the generic route. Nothing anywhere notices —
//     the process is up, its own /v1/health is 200 — and a liveness probe on the
//     subsystem gets a 404.
//
// The second half asks the FLEET's routing table which health address belongs to
// this app rather than assuming /v1/<name>/health, because two apps are not
// named for the surface they serve: plan answers /v1/plan and storage answers
// /v1/s3. Assuming the convention would report both as broken when both are
// correct.
func TestHealthOwnershipMatchesWhatIsRegistered(t *testing.T) {
	for _, s := range specs(t) {
		generic := "/v1/" + s.name + "/health"
		var mine []string
		for _, p := range s.paths {
			if strings.HasSuffix(p, "/health") && manifest.OwnerOf(p) == s.name {
				mine = append(mine, p)
			}
		}
		switch {
		case !s.ownsHealth && slices.Contains(s.paths, generic):
			t.Errorf("%s registers %s in Mount and its main leaves OwnsHealth false — "+
				"serve.go declares that same address for every subsystem without it, so the "+
				"program has one address declared twice and zip refuses to compose it. "+
				"Set OwnsHealth: true in plugin/%s/main.go.", s.name, generic, s.dir)
		case s.ownsHealth && len(mine) == 0:
			t.Errorf("%s declares OwnsHealth: true and registers no health route the fleet "+
				"routes to it — OwnsHealth's only effect is to suppress serve.go's generic "+
				"%s, so nothing answers it. Either serve a real probe in Mount or drop the "+
				"field from plugin/%s/main.go.", s.name, generic, s.dir)
		}
	}
}

// TestDeclaredPrefixesCoverTheSurface is the other release cycle. label answers
// /v1/risk/labels and team answers /collaborator, and neither main said so — so
// scope defaulted both to the /v1/<name> convention, every group they built sat
// outside the subtrees they owned, and UseAll refused their mounts.
//
// THE ASSERTION IS ONE-DIRECTIONAL, AND THE DIRECTION MATTERS. It says every
// route the app registers is under a prefix it owns. It does NOT say the
// prefixes EQUAL the manifest row, and asserting that breaks deploy's boot:
// manifest.Apps is a ROUTING table, free to enumerate LEAVES — deploy's row is
// 14 specific paths — while deploy legitimately bridges /v1/deploy, the parent
// of all 14. Requiring equality turns a correct parent into an escape.
//
// # What this can decide, and what it cannot
//
// It reads a PROJECTION, and a projection holds addresses. Middleware has no
// address, so no projection can see where a subsystem installed any — which is
// the fact UseAll actually refuses on. This therefore checks the half a
// document can answer: A DECLARATION IS A CLAIM, AND A CLAIM MUST BE TRUE. A
// main that writes `Prefixes:` and names less than it serves has written a
// falsehood, and this finds it before a build.
//
// The other half — a main that declares NOTHING, takes the /v1/<name> default,
// and serves somewhere else — is decided by running the binary, because that is
// where middleware exists: UseAll refuses the mount and the process dies at
// boot. `make compose` is that check and it covers all 120 apps. Restating it
// here from a document would be a worse copy of a check that already runs.
//
// A global subsystem (App, not Mount) is bounded by nothing on purpose: it
// receives the bare app because it means to reach the whole binary, and that
// grant is stated in its main where a reviewer reads it. There is no prefix to
// be under, so this rule is silent about it — and stays silent by DERIVATION,
// off the same field UseAll switches on.
func TestDeclaredPrefixesCoverTheSurface(t *testing.T) {
	byDir := map[string][]spec{}
	for _, s := range specs(t) {
		byDir[s.dir] = append(byDir[s.dir], s)
	}
	for dir, ss := range byDir {
		// One projection per BINARY, so a main mounting two subsystems composes
		// both into one table, and the question is whether SOMETHING in that
		// binary claims each address. A binary holding a global plugin claims
		// everything, so there is nothing left to check.
		claims := false
		for _, s := range ss {
			if s.global {
				claims = false
				break
			}
			claims = claims || len(s.prefixes) > 0
		}
		if !claims {
			continue
		}
		for _, p := range ss[0].paths {
			owned := false
			var have []string
			for _, s := range ss {
				owned = owned || s.owns(p)
				have = append(have, cloud.UsePrefixes(s.name, s.prefixes)...)
			}
			if !owned {
				t.Errorf("plugin/%s/main.go grants %s and the app serves %s, which is under "+
					"none of them — its middleware installs on the grant and can never run "+
					"here, and the day it builds a group at this path UseAll refuses the "+
					"mount. The grant in that file is a claim, and it is not true.",
					dir, strings.Join(have, ", "), p)
			}
		}
	}
}
