// Copyright © 2026 Hanzo AI. MIT License.

package cloud_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A package global an app fills at Use is EMPTY in every other binary, so an
// exported function that reads one answers a stranger with a zero.
//
// Every subsystem is its own process. `var mounted *Client`, assigned by the flags
// app's Use, is nil in the ai binary that links the package and never mounts it —
// and `flags.Int` there returned its caller's compiled default while reporting it
// as the operator's setting. The AI spend ceiling ran on a three-hour window
// whatever an operator set; a waitlist switched off stayed on; the admin cockpit's
// switchboard rendered empty and its writes were refused.
//
// It is not a new mistake. apps/rollingcap installed the AI-spend ceiling by
// writing a package global in the root package and was DELETED for it — "a global
// written in one child is invisible in every other, so the ceiling has never
// applied to a completion" — and apps/catalogsync went the same way. This gate is
// that lesson made mechanical: the shape cannot come back without a reader here
// naming why.
//
// WHAT IT REFUSES is narrow and deliberate: an exported function that reads a
// global its own Use assigns, CALLED FROM ANOTHER APP. A global read only by its
// own app's Use and Shutdown is a process's handle on the thing it started, which
// is what a process is for. The cure for a refusal is a plane op — the caller asks
// the app that owns the state — never a second global.
func TestNoAppReadsAnotherAppsGlobal(t *testing.T) {
	apps := appDirs(t)
	type finding struct{ app, global, fn, caller string }
	var found []finding

	for app, files := range apps {
		globals := globalsAssignedInUse(t, files)
		if len(globals) == 0 {
			continue
		}
		for fn, reads := range exportedReaders(t, files, globals) {
			for other, otherFiles := range apps {
				if other == app {
					continue
				}
				if callsQualified(t, otherFiles, app, fn) {
					found = append(found, finding{app, reads, fn, other})
				}
			}
		}
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].app != found[j].app {
			return found[i].app < found[j].app
		}
		if found[i].fn != found[j].fn {
			return found[i].fn < found[j].fn
		}
		return found[i].caller < found[j].caller
	})

	for _, f := range found {
		key := f.app + "." + f.fn
		if why, ok := crossAppGlobals[key]; ok {
			if why == "" {
				t.Errorf("%s is excused with an empty reason — say what it is", key)
			}
			continue
		}
		t.Errorf("apps/%s calls %s.%s, which reads the package global %q that only %s's own Use fills.\n"+
			"\tIn the %s binary that global is nil, so the call answers a zero and reports it as an answer.\n"+
			"\tPublish a plane op from apps/%s and call it through plane/%s, or name it in crossAppGlobals with the reason.",
			f.caller, f.app, f.fn, f.global, f.app, f.caller, f.app, f.app)
	}

	// The ledger may not outlive what it excuses.
	live := map[string]bool{}
	for _, f := range found {
		live[f.app+"."+f.fn] = true
	}
	for key := range crossAppGlobals {
		if !live[key] {
			t.Errorf("crossAppGlobals excuses %s, which no app calls across a boundary any more — delete the entry", key)
		}
	}
}

// crossAppGlobals is every cross-app read of a Use-assigned global that still
// stands, and why. It may only SHRINK: an entry is deleted when the call moves to
// the plane, and a new one is a decision somebody writes down.
//
// Each of these is a known defect with a stated symptom, not an approved design.
// They are listed rather than fixed in one commit because each cure is a different
// op with a different wire, and a wrong wire is worse than a named bug.
var crossAppGlobals = map[string]string{
	"agents.Ready":                   "called by link — asks whether the agent plane is up; false where it is not mounted, so a live plane reads as absent.",
	"agents.StopSessions":            "called by link — stops an org's runs; stops nothing and reports success.",
	"author.AccrueForOrg":            "called by affiliate — accrues an author's royalty; accrues nothing — a payout silently not made.",
	"blueprint.EstimateService":      "called by platform — prices a service's compute; answers zero, so a deploy is quoted free.",
	"blueprint.Rates":                "called by author — the rate card a royalty is a share OF; zero rates make every share zero.",
	"code.Ready":                     "called by search — whether the code index is up; false, so the leg reports itself disabled.",
	"code.Search":                    "called by search — the code leg of the fused query; returns nothing, so /v1/search drops a whole corpus.",
	"content.Generate":               "called by auto — generates a document; refused.",
	"content.Publish":                "called by auto — publishes one; refused.",
	"content.Transition":             "called by auto — moves one through its workflow; refused.",
	"destination.Connect":            "called by auto — connects a destination; refused.",
	"flags.Assign":                   "called by experiment — assigns an experiment variant; falls back to the definition's own rollout, so an operator's setting is unobservable.",
	"flags.GetDef":                   "called by experiment — reads a flag definition; yields none.",
	"flags.PutDef":                   "called by experiment — writes one; refused.",
	"index.Query":                    "called by catalog, search — the lexical leg for search and catalog; returns nothing, so both silently lose a corpus.",
	"index.Ready":                    "called by catalog, search — whether that index is up; false, so the leg reads as unprovisioned.",
	"index.Reconcile":                "called by catalog — rebuilds the cross-org corpus; writes nothing.",
	"integrations.InstallationToken": "called by git, sync — mints a GitHub App installation token; refused, so no push reaches the forge from these binaries.",
	"integrations.TokenFor":          "called by content, destination, projects — fetches a customer's connector credential; refused with 'integrations: not mounted' — every feature spending a customer's OAuth token from another app.",
	"link.RoutedBreakdown":           "called by billing — routed usage for a bill; empty, so the bill omits it.",
	"plan.Entitlements":              "called by usage, world — what a plan includes; 'plans not mounted', so both callers fall back.",
	"projects.LiveSites":             "called by catalog — the deployed sites the catalog indexes; empty, so no site reaches it.",
	"projects.Ready":                 "called by catalog — whether that plane is up; false.",
	"search.ForOrg":                  "called by team — the fused query; degrades to whichever legs happen to be reachable rather than refusing.",
	"template.Lookup":                "called by projects — resolves a starter kit at fork time; not found, so a fork of a private template fails.",
	"wallet.Live":                    "called by x402 — checks the wallet plane; false.",
	"wallet.ResolvePaymentTarget":    "called by x402 — resolves where a payment settles; unresolved, so settlement has no target.",
	"wallet.TreasuryAnchorSigner":    "called by treasury — signs an on-chain anchor; no signer, so the anchor stays pending.",
	"x402.Settle":                    "called by marketplace — settles a paid tool call; settles nothing.",
}

// parseFiles parses one app's files, skipping any that will not parse.
func parseFiles(t *testing.T, files []string) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	var out []*ast.File
	for _, f := range files {
		a, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			continue
		}
		out = append(out, a)
	}
	return fset, out
}

// globalsAssignedInUse names the package-level vars an app's Use assigns.
func globalsAssignedInUse(t *testing.T, files []string) map[string]bool {
	t.Helper()
	_, asts := parseFiles(t, files)
	pkg := map[string]bool{}
	for _, a := range asts {
		for _, d := range a.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, s := range g.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					pkg[n.Name] = true
				}
			}
		}
	}
	assigned := map[string]bool{}
	for _, a := range asts {
		for _, d := range a.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != "Use" || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && pkg[id.Name] {
						assigned[id.Name] = true
					}
				}
				return true
			})
		}
	}
	return assigned
}

// exportedReaders maps each exported function that reads one of those globals to
// the global it reads. Use and Shutdown are excluded: a process holding a handle
// on what it started is what a process is.
func exportedReaders(t *testing.T, files []string, globals map[string]bool) map[string]string {
	t.Helper()
	_, asts := parseFiles(t, files)
	out := map[string]string{}
	for _, a := range asts {
		for _, d := range a.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil {
				continue
			}
			name := fd.Name.Name
			if !ast.IsExported(name) || name == "Use" || name == "Shutdown" {
				continue
			}
			// Every file of the app is parsed separately, so an identifier
			// resolved WITHIN its own file carries an Obj and one resolved across
			// files does not. Filtering on that told the two apart backwards and
			// hid every same-file reader — flags.SetPlatformSwitch among them,
			// while flags.GetDef one file over was found. Locals are excluded by
			// name instead: a function that declares its own `mounted` is not
			// reading the package's.
			// EVERY binding form, or the check reports a pure function. kms.OrgPath
			// reads no global and was flagged because `for _, s := range sub` binds
			// through a RangeStmt rather than an AssignStmt, so `s` looked like a
			// package name. Params, :=, range, inner var and named results all bind.
			locals := map[string]bool{}
			bind := func(ns []*ast.Ident) {
				for _, n := range ns {
					locals[n.Name] = true
				}
			}
			bind(fieldNames(fd.Type.Params))
			bind(fieldNames(fd.Type.Results))
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.RangeStmt:
					for _, e := range []ast.Expr{v.Key, v.Value} {
						if id, ok := e.(*ast.Ident); ok {
							locals[id.Name] = true
						}
					}
				case *ast.GenDecl:
					if v.Tok == token.VAR {
						for _, sp := range v.Specs {
							bind(sp.(*ast.ValueSpec).Names)
						}
					}
				case *ast.FuncLit:
					bind(fieldNames(v.Type.Params))
					bind(fieldNames(v.Type.Results))
				}
				if as, ok := n.(*ast.AssignStmt); ok && as.Tok == token.DEFINE {
					for _, lhs := range as.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							locals[id.Name] = true
						}
					}
				}
				id, ok := n.(*ast.Ident)
				if ok && globals[id.Name] && !locals[id.Name] {
					out[name] = id.Name
				}
				return true
			})
		}
	}
	return out
}

// callsQualified reports whether any file calls pkg.fn(...).
func callsQualified(t *testing.T, files []string, pkg, fn string) bool {
	t.Helper()
	_, asts := parseFiles(t, files)
	hit := false
	for _, a := range asts {
		ast.Inspect(a, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != fn {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
				hit = true
			}
			return true
		})
	}
	return hit
}

// fieldNames is every name a parameter or result list binds.
func fieldNames(fl *ast.FieldList) []*ast.Ident {
	if fl == nil {
		return nil
	}
	var out []*ast.Ident
	for _, f := range fl.List {
		out = append(out, f.Names...)
	}
	return out
}

// appDirs is every apps/<name> with its non-test Go files.
func appDirs(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir("apps")
	if err != nil {
		t.Fatalf("read apps/: %v", err)
	}
	out := map[string][]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		fs, err := filepath.Glob(filepath.Join("apps", e.Name(), "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", e.Name(), err)
		}
		var keep []string
		for _, f := range fs {
			if !strings.HasSuffix(f, "_test.go") {
				keep = append(keep, f)
			}
		}
		if len(keep) > 0 {
			out[e.Name()] = keep
		}
	}
	return out
}
