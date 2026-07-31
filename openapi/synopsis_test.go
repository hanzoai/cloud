package openapi

// What a product's description is allowed to be, and what it must never be.
//
// The prose travels: synopsis.go reads it once (when an app describes itself),
// describe.go stamps it into that app's subset, and the weave lifts it onto the
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

// An app whose subsystem is another MODULE (authz, licensing, metrics) imports no
// package in this repo. There is nothing to read and nothing to guess.
func TestSynopsisIsEmptyForAnAppFromAnotherModule(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plugin", "authz")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	main := "package main\n\nimport _ \"github.com/hanzoai/authz/serve\"\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Synopsis(dir); got != "" {
		t.Errorf("Synopsis = %q, want \"\"", got)
	}
}

// A product is described by the app that SERVES it, in that app's own words,
// carried on the subset the app wrote. The tag LIST stays a function of the
// document's operations — every product is named — so a consumer enumerating
// products loses nothing because nobody wrote a sentence.
//
// A tag is a path prefix; an app is a mount; the two are only sometimes one word.
// Keying this by app name alone left every product whose prefix differs from its
// app undescribed — 59 of 150 tags, ~50 of them served by one app that had already
// written the sentence (kb ← knowledge, scrape ← websearch, machines ← visor).
//
// Everything else here is a way of saying NOTHING, because each alternative would
// publish a guess a consumer cannot tell from a fact:
//
//   - two apps serve the product → no description (never a coin flip),
//   - the sole owner has no package doc → no description,
//   - the part carries the FLEET's identity → it has said nothing about itself,
//     and stamping that would hand every undescribed product the same sentence.
//
// Nothing is ever derived from the product's own name.
func TestWeaveDescribesAProductInTheWordsOfTheAppThatServesIt(t *testing.T) {
	// Each app at its OWN address under the shared prefix — which is how a product
	// comes to have two owners at all: /v1/finance/invoices is billing's and
	// /v1/finance/ledger is treasury's, no address contested.
	part := func(app, says string, products ...string) Part {
		p := Part{App: app, Doc: &Document{
			Info:  Info{Title: fleetInfo.Title, Description: says, Version: fleetInfo.Version},
			Paths: map[string]PathItem{},
		}}
		for _, prod := range products {
			p.Doc.Paths["/v1/"+prod+"/"+app] = PathItem{
				"get": {OperationID: "get_" + app + "_" + prod, Tags: []string{prod}},
			}
		}
		return p
	}

	doc, err := Weave([]Part{
		part("kms", "Package kms is the key plane.", "kms"),
		part("knowledge", "Package knowledge is the knowledge base.", "kb"),
		part("billing", "Package billing meters and invoices.", "billing", "finance"),
		part("treasury", "Package treasury moves money.", "treasury", "finance"),
		part("metrics", "", "logs"),
		part("security", fleetInfo.Description, "security"),
		// admin the APP is named for the tag; plugins also serves the prefix.
		part("admin", "Package admin is the operator console.", "admin"),
		part("plugins", "Package plugin lists what is mounted.", "admin", "plugins"),
	})
	if err != nil {
		t.Fatalf("weave: %v", err)
	}
	want := map[string]string{
		"kms":      "Package kms is the key plane.",            // app and prefix are one word
		"kb":       "Package knowledge is the knowledge base.", // inherited from its sole owner
		"finance":  "",                                         // two owners — silence
		"logs":     "",                                         // sole owner has no package doc
		"security": "",                                         // said nothing about itself
		"admin":    "Package admin is the operator console.",   // the app it is NAMED for still wins
		"plugins":  "Package plugin lists what is mounted.",
		"billing":  "Package billing meters and invoices.",
		"treasury": "Package treasury moves money.",
	}
	if len(doc.Tags) != len(want) {
		t.Fatalf("tags = %+v, want one per product — an app that is not a prefix (knowledge, metrics) is not a tag", doc.Tags)
	}
	for _, tag := range doc.Tags {
		if tag.Description != want[tag.Name] {
			t.Errorf("tag %q description = %q, want %q", tag.Name, tag.Description, want[tag.Name])
		}
	}
}
