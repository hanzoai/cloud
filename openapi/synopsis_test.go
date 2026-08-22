package openapi

// What a product's description is allowed to be, and what it must never be.
//
// The prose travels: synopsis.go reads it once (when an app describes itself),
// describe.go stamps it into that app's subset, and the compose lifts it onto the
// fleet document's product tags. Each hop is cheap to get subtly wrong in a way
// no diff explains — a file note published as a product description reads exactly
// like a real one — so the rules are pinned here.
//
// Internal, because the fallback sentinel is fleetInfo: "this part said nothing
// about itself" is a statement about THIS package's identity value, and a test
// that could not name it would have to match its prose instead.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// tree writes a plugin/<app> whose main imports pkg, plus that package's files.
// files maps a filename to its full source, so a test can say precisely which
// file carries which comment — the whole question comment() answers.
func tree(t *testing.T, app, pkg string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "plugin", app)
	src := filepath.Join(root, "apps", pkg)
	for _, d := range []string{dir, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	main := "package main\n\nimport _ \"github.com/hanzoai/cloud/apps/" + pkg + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The comment that NAMES the package is the package's doc, wherever it sits.
//
// go/doc's rule — the first file in filename order carrying any leading comment —
// picks the wrong one in twenty of the fleet's packages, which open their
// alphabetically-first file with a note about that FILE and state the real package
// doc in <name>.go. Publishing "actions.go — the two GitOps write actions" as the
// deploy product's description is worse than publishing nothing: nothing is
// legibly absent, and that is legibly wrong.
func TestSynopsisTakesTheCommentThatNamesThePackage(t *testing.T) {
	dir := tree(t, "deploy", "deploy", map[string]string{
		"actions.go": "// actions.go — the two GitOps write actions.\npackage deploy\n",
		"deploy.go":  "// Package deploy mounts the GitOps control plane at /v1/deploy.\n//\n// A second paragraph, which a synopsis must not reach.\npackage deploy\n",
	})
	want := "Package deploy mounts the GitOps control plane at /v1/deploy."
	if got := Synopsis(dir); got != want {
		t.Errorf("Synopsis = %q, want %q", got, want)
	}
}

// A package that documents no package has no description. The fallback is the
// fleet's own sentence, applied by the writer — never a sentence invented here.
func TestSynopsisIsEmptyWithoutAPackageDoc(t *testing.T) {
	dir := tree(t, "security", "security", map[string]string{
		"security.go": "package security\n",
		"store.go":    "// store.go opens the store.\npackage security\n",
	})
	if got := Synopsis(dir); got != "" {
		t.Errorf("Synopsis = %q, want \"\" — a file note is not a package doc", got)
	}
}

// mount writes a plugin/<app> whose main imports these paths — in that order, no
// package files of its own — under a root carrying THIS repo's go.mod and go.sum.
// An import of another module therefore resolves to the directory the real app
// resolves to: the module cache, at the version this repo pins.
func mount(t *testing.T, app string, imports ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "plugin", app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"go.mod", "go.sum"} {
		pin, err := os.ReadFile(filepath.Join("..", f)) // openapi/ sits one below the root
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, f), pin, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	main := "package main\n\nimport (\n"
	for _, im := range imports {
		main += "\t_ " + strconv.Quote(im) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main+")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A subsystem in ANOTHER MODULE is read out of the module cache, at the version
// this repo pins — the same reading, from the other place a subsystem can live.
//
// plugin/authz's real import list, order intact, because two of those imports are
// traps. luxfi/log documents itself ("Package log provides a high-performance
// structured logging library") and comes FIRST, so a rule that took the first
// import carrying a package doc would describe /v1/authz as a logging library;
// what makes the subsystem the subsystem is that it is OURS. And cloud itself is
// imported twice over, which the host exclusion answers.
//
// It asserts the package, not the sentence: the words belong to hanzoai/authz and
// change whenever that repo has something better to say. What this repo owns is
// that they ARRIVE.
func TestSynopsisReadsASubsystemInAnotherModule(t *testing.T) {
	dir := mount(t, "authz",
		"github.com/luxfi/log",
		"github.com/hanzoai/authz/serve",
		"github.com/hanzoai/cloud",
		"github.com/hanzoai/cloud/openapi",
	)
	if got := Synopsis(dir); !strings.HasPrefix(got, "Package serve is ") {
		t.Errorf("Synopsis = %q, want the package doc of github.com/hanzoai/authz/serve", got)
	}
}

// The first import that DOCUMENTS ITSELF wins, not the first that resolves.
//
// plugin/licensing imports two packages of one module: pkg/licensing, for the one
// type its host's adapter has to name, and the front door it mounts. The type
// package documents no package — resolving is not the test, having something to
// say is — so "the first that resolves" would publish nothing here.
func TestSynopsisTakesTheImportThatDocumentsItself(t *testing.T) {
	dir := mount(t, "licensing",
		"github.com/hanzoai/licensing/pkg/licensing",
		"github.com/hanzoai/cloud",
		"github.com/hanzoai/licensing",
	)
	if got := Synopsis(dir); !strings.HasPrefix(got, "Package licensing is ") {
		t.Errorf("Synopsis = %q, want the package doc of github.com/hanzoai/licensing", got)
	}
}

// The HOST is not a subsystem. metrics mounts a function of cloud's own and
// imports nothing else of ours, so there is nothing here that says what metrics
// is — and cloud's package doc, which is the FLEET's identity, must not be
// published as one product's.
func TestSynopsisIsEmptyForAnAppThatMountsTheHost(t *testing.T) {
	if got := Synopsis(mount(t, "metrics", "github.com/hanzoai/cloud")); got != "" {
		t.Errorf("Synopsis = %q, want \"\"", got)
	}
}

// A tag is the app that serves the operation, so the composed document carries one
// tag per app that published anything, described in that app's own words — a
// shared address prefix is a fact about the ADDRESS (openapi/misfiled.go reads
// it), never a second tag.
func TestComposeDescribesACapabilityInItsOwnWords(t *testing.T) {
	part := func(app, says string, prefixes ...string) Part {
		p := Part{App: app, Doc: &Document{
			Info:  Info{Title: fleetInfo.Title, Description: says, Version: fleetInfo.Version},
			Paths: map[string]PathItem{},
		}}
		for _, prefix := range prefixes {
			p.Doc.Paths["/v1/"+prefix+"/"+app] = PathItem{
				"get": {OperationID: "get_" + app + "_" + prefix, Tags: []string{prefix}},
			}
		}
		return p
	}

	doc, err := Compose([]Part{
		part("kms", "Package kms is the key plane.", "kms"),
		part("knowledge", "Package knowledge is the knowledge base.", "kb"),
		part("billing", "Package billing meters and invoices.", "billing", "finance"),
		part("treasury", "Package treasury moves money.", "treasury", "finance"),
		part("metrics", "", "logs"),
		part("security", fleetInfo.Description, "security"),
		part("admin", "Package admin is the operator console.", "admin"),
		part("plugins", "Package plugin lists what is mounted.", "admin", "plugins"),
	})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	want := map[string]string{
		"kms":       "Package kms is the key plane.",
		"knowledge": "Package knowledge is the knowledge base.", // the app, not the kb prefix it answers at
		"billing":   "Package billing meters and invoices.",     // one tag however many prefixes
		"treasury":  "Package treasury moves money.",
		"metrics":   "", // no package doc
		"security":  "", // said nothing about itself
		"admin":     "Package admin is the operator console.",
		"plugins":   "Package plugin lists what is mounted.",
	}
	if len(doc.Tags) != len(want) {
		t.Fatalf("tags = %+v, want one per app that published an operation", doc.Tags)
	}
	for _, tag := range doc.Tags {
		if tag.Description != want[tag.Name] {
			t.Errorf("tag %q description = %q, want %q", tag.Name, tag.Description, want[tag.Name])
		}
	}
	for path, item := range doc.Paths {
		for _, op := range item {
			if len(op.Tags) != 1 || op.Tags[0] != op.App {
				t.Errorf("%s tags = %v, want exactly [%s]", path, op.Tags, op.App)
			}
		}
	}
}
