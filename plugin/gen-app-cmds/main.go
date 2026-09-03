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
//	           one-app form (import apps/<name>, mount <name>.Mount, Free),
//	WRITES     that app's apps/<pkg>/Makefile, the two lines naming which apps the
//	           package backs and including the one build contract, and
//	VALIDATES  the two sets are in bijection — every app has a command, and every
//	           app command is a manifest app — so the light host can never route
//	           to a binary that is not there, nor a binary go unrouted.
//
// The Makefile is written here because it is the SAME manifest row that decides
// it, and because the alternative was measured: `sandbox` was added with a main
// and no Makefile, so `make describe` had no rule to run for it, so it published
// no OpenAPI subset, so `make openapi` failed on an app that was otherwise
// complete. Every app Makefile already CLAIMED this file generated it. That
// claim was false for as long as a human had to remember the second file, and a
// generator's header comment that is false is worse than no comment, because the
// next person believes it.
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
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	"audit":     "auditlog",
	"sandboxes": "sandbox",
	"evals":     "eval",
	"plugins":   "plugin",
}

// notApps are the plugin/ directories that are tools, not fleet subsystems: the
// smoke prober, the generators, and the KMS re-seal migration. They are exempt
// from the bijection; everything ELSE under plugin/ must be a manifest app. (The
// light host is cmd/cloud — the ONE thing under cmd/, never here.)
var notApps = map[string]bool{
	"smoke":             true,
	"gen-app-cmds":      true,
	"gen-client-catalog": true,
	"gen-skills":        true,
	"gen-mcp-catalog":   true,
	"kmsreseal":         true,
}

func main() {
	root, err := repoRoot()
	if err != nil {
		die(err)
	}

	// Forward: every manifest app has a command; scaffold the ones that do not.
	// backs collects, per package directory, the app names that package serves —
	// plural because one package can back several apps (account serves two), and
	// because the Makefile's APPS variable has always been a list even while every
	// entry happened to hold a single name.
	//
	// The package comes from the app's OWN main, not from pkgOf: pkgOf is
	// consulted only when scaffolding a main that does not exist yet, so it is
	// silent about every app already committed. The main states which package it
	// mounts, for scaffolded and hand-written alike, so that is what is read.
	want := make(map[string]bool, len(manifest.Apps))
	backs := make(map[string][]string, len(manifest.Apps))
	for _, a := range manifest.Apps {
		want[a.Name] = true
		mainGo := filepath.Join(root, "plugin", a.Name, "main.go")
		if _, err := os.Stat(mainGo); err != nil {
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
		// An existing main is SOURCE and is left alone — but it is still read,
		// because it is the thing that says which package this app mounts.
		pkg, err := pkgFromMain(root, a.Name, mainGo)
		if err != nil {
			die(fmt.Errorf("%s: %w", a.Name, err))
		}
		if pkg != "" {
			backs[pkg] = append(backs[pkg], a.Name)
		}
	}

	// The third leg: the package's Makefile, naming the apps it backs and
	// including the one build contract. Without it `make describe` has no rule to
	// run for that app, so it publishes no OpenAPI subset, so the fleet compose
	// fails on an app that is otherwise complete.
	for pkg, apps := range backs {
		sort.Strings(apps)
		mk := filepath.Join(root, "apps", pkg, "Makefile")
		if err := writeIfChanged(mk, []byte(fmt.Sprintf(makefile, strings.Join(apps, " ")))); err != nil {
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

// pkgFromMain reads which apps/<pkg> directory a plugin main's app lives in.
//
// It parses the file rather than matching text: an import can be aliased, can
// sit in any group, and a string that looks like the path can appear in a
// comment. go/parser in ImportsOnly mode answers the question the file actually
// answers, and costs nothing at this scale.
//
// An app with NEITHER an apps/ import nor an apps/<name> directory is external:
// its code lives in its own module (hanzoai/authz, hanzoai/licensing,
// hanzoai/o11y/metrics), wired into the fleet but built and described over there. It
// returns "" and the caller writes no Makefile — there is no directory of ours
// for one to sit in.
//
// That is derived rather than listed on purpose. mk/fleet.mk names those three
// in EXTERNAL, and a second copy of the list here would be one more place to
// forget on the day a fourth arrives. "Has no package directory in this repo" is
// the same fact, and it maintains itself.
func pkgFromMain(root, name, path string) (string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return "", err
	}
	const want = modPath + "/apps/"
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil || !strings.HasPrefix(p, want) {
			continue
		}
		// The app package itself, never a subpackage of it (apps/x/wire).
		if rest := p[len(want):]; rest != "" && !strings.Contains(rest, "/") {
			return rest, nil
		}
	}
	if fi, err := os.Stat(filepath.Join(root, "apps", name)); err == nil && fi.IsDir() {
		return name, nil
	}
	return "", nil // external module — built and described in its own repo
}

// makefile is the whole build contract for an app package: which apps it backs,
// and the one file that says what build/test/vet/describe mean. Every app has
// the same two lines because there is one way to build an app.
const makefile = `# Generated by plugin/gen-app-cmds. DO NOT EDIT.
#
# The build contract is mk/plugin.mk — one file carrying every target an app
# needs: build, test, vet, openapi, clean. This names the app(s) this package
# backs and includes it. Written from the same manifest.Apps rows that write
# plugin/<app>/main.go, so an app cannot have a main and no Makefile.
APPS := %s
include ../../mk/plugin.mk
`

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
	"\t\tUse: %s.Use,\n" +
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
