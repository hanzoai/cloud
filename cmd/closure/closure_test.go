package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A witness is only worth committing if the thing it witnesses cannot move
// without it. These pin the four properties that makes true, in the order they
// were learned:
//
//	the main module is EXCLUDED       — or every app is stale on every commit
//	a dependency's source is INCLUDED — or the bump that stopped the train is invisible
//	EVERY stale app is reported       — or three red runs cost what one should
//	the message names the REPAIR      — or knowing is not fixing

// fakePkg builds a package for the graph tests. Nil module means stdlib.
//
// THE DIRECTORY IS ALWAYS MADE, and no files is not the same fact as no
// directory. `go list` gives a Dir to every package it can PLACE, whatever is in
// it, and reports an empty one only for an import that resolves to nothing —
// which is why an empty one is now refused outright. A fixture that left Dir
// empty for the packages it had no files for was inventing a shape the toolchain
// does not emit, and a fixture that lies about its input can only test a
// different program.
func fakePkg(t *testing.T, dir, path, module, version string, files map[string]string, imports ...string) pkg {
	t.Helper()
	p := pkg{ImportPath: path, Imports: imports}
	if module != "" {
		p.Module = &struct {
			Path    string
			Version string
		}{Path: module, Version: version}
	}
	d := filepath.Join(dir, strings.ReplaceAll(path, "/", "_"))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p.Dir = d
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		p.GoFiles = append(p.GoFiles, name)
	}
	return p
}

// world is the smallest graph that has every shape this has to get right: an app
// that reaches a dependency package, a SECOND app that does not, and a package of
// the main module that both reach.
func world(t *testing.T, depBody string) (root string, pkgs []pkg) {
	t.Helper()
	root = t.TempDir()
	for _, app := range []string{"iam", "wallets"} {
		if err := os.MkdirAll(filepath.Join(root, "plugin", app), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "plugin", app, "openapi.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := t.TempDir()
	return root, []pkg{
		fakePkg(t, src, mainModule+"/plugin/iam", mainModule, "", nil, mainModule+"/apps/iam"),
		fakePkg(t, src, mainModule+"/plugin/wallets", mainModule, "", nil, mainModule+"/apps/wallets"),
		fakePkg(t, src, mainModule+"/apps/iam", mainModule, "",
			map[string]string{"iam.go": "package iam"},
			"github.com/hanzoai/iam/server", mainModule+"/root"),
		fakePkg(t, src, mainModule+"/apps/wallets", mainModule, "",
			map[string]string{"w.go": "package wallets"}, mainModule+"/root"),
		fakePkg(t, src, mainModule+"/root", mainModule, "", map[string]string{"root.go": "package root"}),
		fakePkg(t, src, "github.com/hanzoai/iam/server", "github.com/hanzoai/iam", "v1.34.21",
			map[string]string{"server.go": depBody}),
	}
}

// THE PROPERTY THE WHOLE TOOL RESTS ON. Every app links the main module's root
// package, so hashing it would move all 125 digests on every commit and this
// would report the fleet, every time, forever — noise that gets a gate deleted.
func TestMainModuleSourceIsNotWitnessed(t *testing.T) {
	root, pkgs := world(t, "package server")
	before, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}

	// Edit the main module's own source, in a package BOTH apps link.
	for _, p := range pkgs {
		if p.ImportPath == mainModule+"/root" {
			if err := os.WriteFile(filepath.Join(p.Dir, "root.go"), []byte("package root // moved"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	for app := range before.Apps {
		if before.Apps[app] != after.Apps[app] {
			t.Errorf("%s went stale because the MAIN MODULE changed; app-contract polices that axis, "+
				"and witnessing it here reports the whole fleet on every commit", app)
		}
	}
}

// THE BUMP THAT STOPPED THE TRAIN. hanzoai/iam v1.34.21 → v1.34.29 changed a
// dependency's source and nothing in the tree could see it. This is that, reduced:
// the dependency's source moves, and ONLY the app that links it goes stale.
func TestDependencySourceMovesExactlyItsDependents(t *testing.T) {
	root, pkgs := world(t, "package server // v1.34.21")
	before, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}

	for i := range pkgs {
		if pkgs[i].ImportPath == "github.com/hanzoai/iam/server" {
			if err := os.WriteFile(filepath.Join(pkgs[i].Dir, "server.go"),
				[]byte("package server // v1.34.29, EnableCodeSignin"), 0o644); err != nil {
				t.Fatal(err)
			}
			pkgs[i].Module.Version = "v1.34.29"
		}
	}
	after, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}

	if before.Apps["iam"] == after.Apps["iam"] {
		t.Error("iam links github.com/hanzoai/iam/server and its source moved — the digest MUST move, " +
			"or this gate cannot see the failure it exists for")
	}
	if before.Apps["wallets"] != after.Apps["wallets"] {
		t.Error("wallets does not link that package — flagging it is the module-granularity " +
			"over-approximation this tool exists to avoid (measured: 122 of 125 apps, against 4 that matter)")
	}
}

// A version that moves with no source change under it is NOT staleness. This is
// what keeps a `go mod tidy` that only rewrites go.sum from demanding a fleet
// rebuild — false failures are how a gate earns being switched off.
func TestVersionAloneIsNotStaleness(t *testing.T) {
	root, pkgs := world(t, "package server")
	before, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range pkgs {
		if pkgs[i].Module != nil && pkgs[i].Module.Path == "github.com/hanzoai/iam" {
			pkgs[i].Module.Version = "v1.34.29" // retagged, same bytes
		}
	}
	after, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	if before.Apps["iam"] != after.Apps["iam"] {
		t.Error("the version string moved but not one byte of source did; demanding a regeneration " +
			"for that is a false failure")
	}
	if after.Modules["github.com/hanzoai/iam"] != "v1.34.29" {
		t.Error("the new version must still be RECORDED — it is what lets the message name the cause")
	}
}

// An app with no committed document has nothing here to be stale. kafka and the
// coresident apps are exempt from describing (mk/fleet.mk says why); they drop out
// by having no file rather than by being named in a second list that can fall out
// of step with the first.
func TestAppWithoutADocumentIsNotWitnessed(t *testing.T) {
	root, pkgs := world(t, "package server")
	if err := os.Remove(filepath.Join(root, "plugin", "wallets", "openapi.json")); err != nil {
		t.Fatal(err)
	}
	have, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := have.Apps["wallets"]; ok {
		t.Error("wallets has no document, so it has no closure to witness")
	}
	if _, ok := have.Apps["iam"]; !ok {
		t.Error("iam has a document and must be witnessed")
	}
}

// THE MASKING PROPERTY. hanzo.yml's sub-steps hide each other — the first red one
// is the only one you see — and that cost three sequential red runs in one day.
// This reports every stale app in ONE pass.
func TestEveryStaleAppIsReportedInOnePass(t *testing.T) {
	want := witness{
		Modules: map[string]string{"github.com/hanzoai/iam": "v1.34.21"},
		Apps:    map[string]string{"iam": "a", "projects": "b", "meet": "c", "wallets": "d"},
	}
	have := witness{
		Modules: map[string]string{"github.com/hanzoai/iam": "v1.34.29"},
		Apps:    map[string]string{"iam": "A", "projects": "B", "meet": "C", "wallets": "d"},
	}
	var buf bytes.Buffer
	err := compare(&buf, want, have)
	if err == nil {
		t.Fatal("three documents moved; the gate must be red")
	}
	out := buf.String()
	for _, app := range []string{"iam", "projects", "meet"} {
		if !strings.Contains(out, "plugin/"+app+"/openapi.json") {
			t.Errorf("report does not name %s — reporting the first stale app is the masking this replaces:\n%s", app, out)
		}
	}
	if strings.Contains(out, "plugin/wallet/openapi.json") {
		t.Errorf("wallets is current and must not be named:\n%s", out)
	}
	if !strings.Contains(out, "github.com/hanzoai/iam  v1.34.21 → v1.34.29") {
		t.Errorf("the report must NAME the dependency that moved, or you bisect for it:\n%s", out)
	}
}

// Knowing is not fixing. The failure carries the command that repairs it, scoped
// to what actually moved — four links and a minute, against one link per app.
func TestReportCarriesTheScopedRepair(t *testing.T) {
	// A realistic fleet: one app moved out of many, which is the scoped branch.
	want := witness{Apps: map[string]string{"iam": "a"}}
	have := witness{Apps: map[string]string{"iam": "A"}}
	for i := range 120 {
		name := "app" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		want.Apps[name], have.Apps[name] = "same", "same"
	}
	var buf bytes.Buffer
	if err := compare(&buf, want, have); err == nil {
		t.Fatal("want red")
	}
	out := buf.String()
	if !strings.Contains(out, "make -f mk/fleet.mk describe/iam") {
		t.Errorf("the repair must be scoped to the app that moved:\n%s", out)
	}
	if !strings.Contains(out, "make closure") {
		t.Errorf("the repair must re-record the witness, or the next run is red again:\n%s", out)
	}
}

// Past a third of the fleet the scoped form is longer to read than the sweep is to
// run. Nineteen hanzoai modules are in 122 of 125 closures, so this branch is the
// common one for a head-module bump and it must name the sweep, not 122 targets.
func TestFleetWideMovementNamesTheSweep(t *testing.T) {
	stale := make([]string, 0, 122)
	for i := range 122 {
		stale = append(stale, string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if got := repair(stale, 125); got != "make describe" {
		t.Errorf("122 of 125 apps must repair with the sweep, got %q", got)
	}
	if got := repair([]string{"iam"}, 125); !strings.HasPrefix(got, "make -f mk/fleet.mk describe/iam") {
		t.Errorf("one app must repair with one target, got %q", got)
	}
}

// The witness is committed, so it has to be byte-stable: two runs of the same tree
// on two machines must produce the same file or every commit carries a spurious
// diff. Map iteration order is the classic way that goes wrong.
func TestWitnessIsByteStable(t *testing.T) {
	root, pkgs := world(t, "package server")
	var first []byte
	for i := range 5 {
		have, err := snapshot(root, pkgs, []string{"iam", "wallets"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.MarshalIndent(have, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
		} else if !bytes.Equal(first, b) {
			t.Fatal("the witness is not byte-stable across runs; it cannot be committed")
		}
	}
}

// A file appearing in a package is a change to that package even when no existing
// byte moves — a new type in a new file is exactly how a schema arrives.
func TestANewFileMovesTheDigest(t *testing.T) {
	root, pkgs := world(t, "package server")
	before, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range pkgs {
		if pkgs[i].ImportPath == "github.com/hanzoai/iam/server" {
			if err := os.WriteFile(filepath.Join(pkgs[i].Dir, "extra.go"), []byte("package server"), 0o644); err != nil {
				t.Fatal(err)
			}
			pkgs[i].GoFiles = append(pkgs[i].GoFiles, "extra.go")
		}
	}
	after, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	if before.Apps["iam"] == after.Apps["iam"] {
		t.Error("a new file in a linked package must move the digest")
	}
}

// A package's type surface is the SET OF FILES as much as the bytes in them, so a
// rename that moves no byte still moves the package. Hashing contents alone would
// read a rename — and a file swapped for another of the same length — as no change.
func TestARenameMovesTheDigest(t *testing.T) {
	root, pkgs := world(t, "package server")
	before, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range pkgs {
		if pkgs[i].ImportPath == "github.com/hanzoai/iam/server" {
			d := pkgs[i].Dir
			if err := os.Rename(filepath.Join(d, "server.go"), filepath.Join(d, "srv.go")); err != nil {
				t.Fatal(err)
			}
			pkgs[i].GoFiles = []string{"srv.go"}
		}
	}
	after, err := snapshot(root, pkgs, []string{"iam", "wallets"})
	if err != nil {
		t.Fatal(err)
	}
	if before.Apps["iam"] == after.Apps["iam"] {
		t.Error("the same bytes under a new filename is still a changed package")
	}
}

// THE UNRUNNABLE REPAIR. kafka cannot mount without a live broker and a coresident
// app is middleware on a sibling's router, so mk/fleet.mk has no describe/<app>
// target for either — and this printed `make -f mk/fleet.mk describe/zen`, which
// answered "No rule to make target". A gate whose fix does not run is the failure
// mk/fleet.mk already carries a paragraph about, reintroduced from the other side.
func TestAnAppThatCannotDescribeItselfIsNotWitnessed(t *testing.T) {
	root, pkgs := world(t, "package server")
	// wallets has a document but is NOT describable — the coresident/kafka shape.
	have, err := snapshot(root, pkgs, []string{"iam"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := have.Apps["wallets"]; ok {
		t.Error("wallets cannot regenerate its own document, so the repair command could " +
			"only ever name a make target that does not exist")
	}
	if _, ok := have.Apps["iam"]; !ok {
		t.Error("iam describes itself and must be witnessed")
	}
}

// The witness may only be written through the one place that knows the set. Left to
// default, it witnessed every app with a document — which is how the unrunnable
// command got printed in the first place.
func TestWritingTheWitnessRefusesWithoutTheDescribableSet(t *testing.T) {
	for _, write := range []bool{true, false} {
		if err := run(write, nil); err == nil {
			t.Fatalf("write=%v without -describable must be refused; CHECKING without it filtered "+
				"every app out and reported \"0 app documents current\" with exit 0", write)
		} else if !strings.Contains(err.Error(), "make closure") {
			t.Errorf("the refusal must name the command that passes it, got: %v", err)
		}
	}
}

// A comparison with no subject is not a pass. Whatever empties the set — a bad
// filter, a moved directory — the answer must be red; this reported a confident
// "0 app documents current with their dependencies" and exited 0.
func TestAnEmptyComparisonIsNotAPass(t *testing.T) {
	var buf bytes.Buffer
	err := compare(&buf, witness{Apps: map[string]string{"iam": "a"}}, witness{Apps: map[string]string{}})
	if err == nil {
		t.Fatalf("comparing nothing must be red, got green with output: %q", buf.String())
	}
}

// A dependency the toolchain could not place must be an ERROR, never a digest.
// `go list -e` lets a broken package through on purpose, and a package with no
// directory has no source to hash — so the witness would compare equal (missing on
// both sides) or stale (missing on one), and neither answer has anything to do
// with whether a document is current.
//
// TWO SHAPES, AND THIS CARRIED ONLY THE ONE THAT NEVER HAPPENED. The guard it was
// asserting lived inside hashPkg, which witnessed() never reaches for a package
// with no MODULE — and no module is exactly what `go list -e` reports for one it
// could not fetch. So the property held, this test passed, and the gate still
// called all 116 documents stale on every run once hanzoai/orm v0.6.24 went
// missing from the forge. The shape a test does not carry is the shape that ships.
// THE THIRD CASE IS WHY THE OTHER TWO WERE NOT ENOUGH. Naming the packages is
// where this stopped, so twice in one day the answer to "why" was hunted by hand
// and both hunts went to the wrong host: `go mod download` run in a shell dials
// whatever THAT machine's insteadOf says, and a warm cache does not dial at all,
// so it reports success for the module CI could not fetch. The toolchain had
// already recorded the reason and `-e` had already swallowed it.
func TestAnUnresolvedDependencyIsAnErrorNotADigest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		strip func(*pkg)
		says  string
	}{
		{"module attributed, source absent", func(p *pkg) { p.Dir = "" }, "no reason recorded"},
		{"no module either, which is what an unfetchable one reports", func(p *pkg) { p.Dir = ""; p.Module = nil }, "no reason recorded"},
		{"the reason go list recorded is the reason printed", func(p *pkg) {
			p.Dir, p.Module = "", nil
			p.Error = &struct{ Err string }{Err: "could not read Username for 'https://github.com'"}
		}, "could not read Username"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, pkgs := world(t, "package server")
			for i := range pkgs {
				if pkgs[i].ImportPath == "github.com/hanzoai/iam/server" {
					tc.strip(&pkgs[i])
				}
			}
			if _, err := snapshot(root, pkgs, []string{"iam", "wallets"}); err == nil {
				t.Fatal("an unresolvable dependency must fail loudly, not hash to a constant")
			} else if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the error must carry the toolchain's own reason, got: %v", err)
			}
		})
	}
}

// reach is what makes ONE `go list` enough for all 125 apps: the union comes back
// once and each root's own closure is this walk. A cycle must not hang it.
func TestReachWalksTransitivelyAndTerminates(t *testing.T) {
	byPath := map[string]*pkg{}
	for _, p := range []pkg{
		{ImportPath: "a", Imports: []string{"b"}},
		{ImportPath: "b", Imports: []string{"c"}},
		{ImportPath: "c", Imports: []string{"a"}}, // cycle
		{ImportPath: "z"},
	} {
		q := p
		byPath[p.ImportPath] = &q
	}
	got := reach(byPath, byPath["a"])
	for _, want := range []string{"a", "b", "c"} {
		if !got[want] {
			t.Errorf("closure of a must contain %q, got %v", want, got)
		}
	}
	if got["z"] {
		t.Errorf("z is unreachable from a, got %v", got)
	}
}
