// Command gen-app-cmds keeps the per-app command stubs in step with the fleet
// manifest.
//
// manifest.Apps (manifest/apps.go) is the hand-authored SOURCE OF TRUTH for the
// fleet — name, prefixes, eager. Each app ALSO ships a standalone binary at
// plugin/<name>/main.go: its own one-app composition root, which states that app's
// Mount/Shutdown/OwnsHealth/Price once, where they are used. This tool binds the
// two views. It reads the manifest as a VALUE (it imports the package and ranges
// manifest.Apps — not a re-parse of the source text) and:
//
//	SCAFFOLDS  a plugin/<name>/main.go for a manifest app that has none, the lean
//	           one-app form (import apps/<name>, mount <name>.Mount, Free), and
//	VALIDATES  the two sets are in bijection — every app has a command, and every
//	           app command is a manifest app — so the light host can never route
//	           to a binary that is not there, nor a binary go unrouted.
//
// It does NOT rewrite an existing main: that file is SOURCE, hand-edited the
// moment an app needs a Shutdown hook, an OwnsHealth, a whole-app App grant, a
// metered Price, or a shared package (account serves two apps by two funcs) —
// none of which a scaffold can guess. So idempotency is structural: a clean,
// consistent tree is left untouched, which is what lets CI diff it.
//
//	run:  go run ./plugin/gen-app-cmds
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/manifest"
)

const modPath = "github.com/hanzoai/cloud"

// pkgOf overrides the client package a scaffold imports for an app whose package
// name is not simply its app name. An app absent here imports apps/<name> and
// mounts <name>.Mount — the shape written for a brand-new subsystem. Apps with a
// richer spec (account's two funcs, metrics' App grant, the external authz /
// licensing modules, o11y's hand-written plugin main) are ALREADY committed and
// are never scaffolded, so they need no entry: this map is consulted ONLY when a
// plugin/<name> is missing, and those are not.
var pkgOf = map[string]string{
	"audit":      "auditlog",
	"evals":      "eval",
	"plugins":    "plugin",
}

// notApps are the plugin/ directories that are tools, not fleet subsystems: the
// smoke prober, this generator, and the two one-off migration utilities. They
// are exempt from the bijection; everything ELSE under plugin/ must be a manifest
// app. (The light host is cmd/cloud — the ONE thing under cmd/, never here.)
var notApps = map[string]bool{
	"smoke":                true,
	"gen-app-cmds":         true,
	"kmsreseal":            true,
	"migrate-pg-to-sqlite": true,
}

func main() {
	root, err := repoRoot()
	if err != nil {
		die(err)
	}

	// Forward: every manifest app has a command; scaffold the ones that do not.
	want := make(map[string]bool, len(manifest.Apps))
	for _, a := range manifest.Apps {
		want[a.Name] = true
		mainGo := filepath.Join(root, "plugin", a.Name, "main.go")
		if _, err := os.Stat(mainGo); err == nil {
			continue // exists: it is source, leave it.
		}
		src, err := scaffold(a.Name)
		if err != nil {
			die(fmt.Errorf("%s: %w", a.Name, err))
		}
		if err := os.MkdirAll(filepath.Dir(mainGo), 0o755); err != nil {
			die(err)
		}
		if err := writeIfChanged(mainGo, src); err != nil {
			die(err)
		}
	}

	// Reverse: no plugin/<x> app binary the manifest does not list. An orphan
	// builds a plugin the host never routes to (or, worse, whose routes a sibling
	// then answers) — the exact drift the bijection exists to refuse.
	ents, err := os.ReadDir(filepath.Join(root, "plugin"))
	if err != nil {
		die(err)
	}
	var orphans []string
	for _, e := range ents {
		if !e.IsDir() || notApps[e.Name()] || want[e.Name()] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "plugin", e.Name(), "main.go")); err == nil {
			orphans = append(orphans, e.Name())
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		die(fmt.Errorf("plugin/{%s} exist but are not in manifest.Apps — add the row (the host routes only to manifest apps), delete the command, or name it in notApps if it is a tool",
			strings.Join(orphans, ",")))
	}
}

// scaffold renders the lean one-app main for a new subsystem.
func scaffold(name string) ([]byte, error) {
	pkg := name
	if p, ok := pkgOf[name]; ok {
		pkg = p
	}
	src := fmt.Sprintf(stub, modPath+"/apps/"+pkg, name, pkg, name, name, pkg, name)
	return format.Source([]byte(src))
}

// stub is the lean one-app composition root: it links only this app's own graph,
// never package apps, so the build is the one app and not the fleet.
const stub = "package main\n\n" +
	"import (\n" +
	"\t\"fmt\"\n" +
	"\t\"os\"\n\n" +
	"\t\"github.com/hanzoai/cloud\"\n" +
	"\t%q\n" +
	")\n\n" +
	"// Standalone entry for the %s app.\n" +
	"//\n" +
	"// This is the app's OWN composition root — it links only apps/%s and the\n" +
	"// cloud request tier, never package apps, so the build is this one subsystem\n" +
	"// and not the whole fleet. The light host loads it as a plugin; run directly it\n" +
	"// serves standalone. Its OpenAPI subset comes from `%s openapi`.\n" +
	"//\n" +
	"// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned —\n" +
	"// add a Shutdown/OwnsHealth/metered Price here if the app grows to need one.\n" +
	"func main() {\n" +
	"\tif err := cloud.Listen([]cloud.Plugin{{\n" +
	"\t\tName:  %q,\n" +
	"\t\tPrice: cloud.Free,\n" +
	"\t\tMount: %s.Mount,\n" +
	"\t}}, []string{%q}); err != nil {\n" +
	"\t\tfmt.Fprintln(os.Stderr, err)\n" +
	"\t\tos.Exit(1)\n" +
	"\t}\n" +
	"}\n"

// writeIfChanged keeps generation idempotent, so a no-op run leaves the tree
// clean and CI's diff means what it says.
func writeIfChanged(path string, src []byte) error {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, src) {
		return nil
	}
	if err := os.WriteFile(path, src, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}

// repoRoot finds the module root by walking up from the working directory for a
// go.mod, so the tool works whether run from the repo root or via `go run`.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "gen-app-cmds:", err)
	os.Exit(1)
}
