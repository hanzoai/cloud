// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package manifest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// prefixLiteral matches a Prefixes field assigned a []string LITERAL, e.g.
//
//	Prefixes: []string{"/v1/thing"},
//
// as opposed to Prefixes: manifest.PrefixesFor("thing").
var prefixLiteral = regexp.MustCompile(`Prefixes:\s*\[\]string\{`)

// TestNoPluginRestatesItsPrefixes keeps "which paths does this app answer" a
// SINGLE fact.
//
// It was two: Apps here, which the light host routes on, and a literal in
// plugin/<app>/main.go, where the app declared its own surface. Four plugins
// restated it. All four happened to agree when I checked — and "happened to" is
// the defect, not the reassurance: the copies live in different files, move in
// different changes, and a disagreement produces no error anywhere. The host
// simply never forwards a path the app is serving, or forwards one it is not.
//
// v1.801.318/.319 is what that costs. ai's row read "/v1/ai" while its router
// served /v1/chat/completions and /v1/models at top level; each half was locally
// reasonable, the pair was wrong, and nothing examined the pair. Inference
// returned 405/404 fleet-wide with every pod Ready and the UI serving 200.
//
// So the manifest is the one list and an app reads it (PrefixesFor). This test is
// what keeps a literal from creeping back — at which point there would be two
// again, and the next disagreement would be as invisible as the last.
func TestNoPluginRestatesItsPrefixes(t *testing.T) {
	dirs, err := filepath.Glob("../plugin/*/main.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(dirs) == 0 {
		t.Skip("plugin sources not present in this checkout")
	}
	for _, f := range dirs {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		if prefixLiteral.Match(b) {
			app := filepath.Base(filepath.Dir(f))
			t.Errorf("%s declares a Prefixes literal — use manifest.PrefixesFor(%q) so the host's list "+
				"and the app's surface cannot disagree", f, app)
		}
	}
}

// TestEveryPluginNameIsInTheManifest closes the other direction. PrefixesFor
// returns nil for an unknown name, which zip reads as "claims nothing" — a plugin
// that quietly routes nowhere. A name absent from Apps was never reachable, so
// this makes the absence loud instead of letting PrefixesFor swallow it.
func TestEveryPluginNameIsInTheManifest(t *testing.T) {
	dirs, err := filepath.Glob("../plugin/*/main.go")
	if err != nil || len(dirs) == 0 {
		t.Skip("plugin sources not present in this checkout")
	}
	known := map[string]bool{}
	for _, a := range Apps {
		known[a.Name] = true
	}
	// Not every plugin/ dir is a fleet app — some are one-shot CLI tools (smoke,
	// gen-app-cmds, kmsreseal, migrate-pg-to-sqlite). Rather than hardcode which,
	// DERIVE it: an app serves requests, so it calls cloud.Serve. A tool does not.
	// A name list here would need editing every time a tool is added, and would
	// eventually be wrong in the direction that hides a real app.
	for _, f := range dirs {
		app := filepath.Base(filepath.Dir(f))
		if !callsServe(f) {
			continue // a tool, not a served app
		}
		if !known[app] {
			t.Errorf("plugin/%s has no row in Apps — PrefixesFor(%q) returns nil, so it would claim "+
				"no path and serve nothing", app, app)
		}
	}
}

// callsServe reports whether the file CALLS cloud.Serve — parsed, not grepped.
//
// A substring match reads the generator as an app: plugin/gen-app-cmds contains
// "cloud.Serve" inside the string TEMPLATE it emits for new apps. Matching text
// finds the mention; only parsing finds the call. Same distinction that makes the
// rest of this file worth having — check the structure, not a spelling.
func callsServe(path string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Serve" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "cloud" {
			found = true
			return false
		}
		return true
	})
	return found
}
