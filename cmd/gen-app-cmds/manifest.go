package main

// The manifest is the light host's whole world.
//
// cmd/host links zip and this manifest and NOTHING else — no subsystem package,
// no package apps — so its build stays at a few hundred packages while the fat
// cmd/cloud links 3254. It can only do that if it knows three facts per app and
// no more: what the app is called, which absolute paths it answers, and whether
// it must already be running when the first request arrives. Everything else
// (what the app DOES) belongs to the app's own binary.
//
// Those three facts are DERIVED, never hand-written. A second list beside
// apps.Wire() is the duplication this whole design exists to avoid: it would go
// stale the first time someone adds a subsystem and the host would route to a
// binary that is not there, or fail to route to one that is.
//
//	name     — the Wire entry's Name.
//	prefixes — the composition root's declared Prefixes when it has one (that is
//	           the SOT stating what the subsystem owns), else the absolute route
//	           paths the app's own package registers.
//	eager    — apps.go's `eager` map. Everything absent from it is lazy, which is
//	           what makes 69 services cheap: an app nobody calls costs a route
//	           entry, not a process.
//
// An app that cannot be reduced to those three facts gets NO row and says why on
// stderr. That is the same honesty gen-app-cmds already applies to a fat stub:
// an app missing from the host 404s visibly, where an app mounted on a guessed
// prefix silently swallows a sibling's traffic.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// modPath is this module; an import under it resolves to a directory without
// asking the toolchain, which keeps regeneration offline for every in-repo app.
const modPath = "github.com/hanzoai/cloud"

// routerMethods are the zip.App/Router calls whose first string argument is a
// route path. Group is here because a group's own argument IS an absolute prefix
// when the receiver is the app.
var routerMethods = map[string]bool{
	"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true,
	"Head": true, "Options": true, "All": true, "Use": true,
	"Group": true, "Static": true, "Mount": true,
}

// manifestRow is one app as the host sees it.
type manifestRow struct {
	name     string
	prefixes []string
	eager    bool

	// registers records whether the app's own package registers any absolute
	// route path, as opposed to only declaring a subtree or falling back to the
	// convention. It is not written to the manifest; dropLayers reads it.
	registers bool
}

// manifest resolves every Wire entry to a row, in Wire order — which IS the
// routing order, because zip registers the mount in the order Load is called and
// the router takes the first match. account's /v1/iam/keys must stay ahead of
// iam's /v1/iam for the same reason it does in the fat binary.
func manifest(root string, specs []spec, eager map[string]bool) []manifestRow {
	var rows []manifestRow
	for _, s := range specs {
		pre, registers, why := prefixesFor(root, s)
		if len(pre) == 0 {
			fmt.Printf("no manifest row: %s — %s\n", s.name, why)
			continue
		}
		rows = append(rows, manifestRow{name: s.name, prefixes: pre, eager: eager[s.name], registers: registers})
	}
	return dropLayers(rows)
}

// dropLayers removes an app that registers no route of its own AND whose every
// declared prefix is claimed, exactly, by an app mounted AFTER it. Both halves
// are needed and each rules out a whole class on its own:
//
//   - Registering nothing is not enough. pubsub owns a NATS listener and metrics
//     answers through a module; neither is a layer.
//   - Being shadowed is not enough either. storage and provisioning both derive
//     /v1/s3, and tracker and analytics both derive /v1/tracker — in each pair
//     the EARLIER app is the real owner and wins the router's first match,
//     exactly as it does linked in. Nothing is dropped there.
//
// Together they describe one thing: a middleware layer. zen is it. zen declares
// /v1 to install a Claim on that subtree, serves the requests naming a zen*
// model in-process, and c.Next()s every other one to ai's /v1 catch-all. In one
// binary that fall-through is a route lookup. Across a process boundary it does
// not exist — the host picks ONE child, the request leaves, and nothing comes
// back to try the next candidate — so mounting the layer would take every /v1
// request ai serves and 404 all but zen*. A layer ships INSIDE the owner's
// binary or it does not ship; saying so here is why /v1/chat/completions reaches
// ai at all.
//
// The test is exact prefix equality, never subtree coverage: ai's /v1 covers
// nearly every app in the fleet as a subtree, and reading that as subsumption
// would empty the manifest.
func dropLayers(rows []manifestRow) []manifestRow {
	var out []manifestRow
	for i, r := range rows {
		if r.registers {
			out = append(out, r)
			continue
		}
		later := map[string]bool{}
		for _, q := range rows[i+1:] {
			for _, p := range q.prefixes {
				later[p] = true
			}
		}
		owns := false
		for _, p := range r.prefixes {
			if !later[p] {
				owns = true
				break
			}
		}
		if !owns {
			fmt.Printf("no manifest row: %s — it registers no route of its own, and every prefix it declares (%s) is answered by an app mounted after it: a middleware layer over that plane, not a process the router can send a request to\n",
				r.name, strings.Join(r.prefixes, " "))
			continue
		}
		out = append(out, r)
	}
	return out
}

// prefixesFor answers what paths s serves, whether s REGISTERS any of them
// itself (dropLayers' second half), or why that cannot be established.
func prefixesFor(root string, s spec) ([]string, bool, string) {
	// A PluginSpec entry already IS a plugin: the composition root passed its
	// prefixes to zip.Load literally, so they are stated, not inferred.
	if len(s.pluginPrefixes) > 0 {
		return normalize(s.pluginPrefixes), true, ""
	}
	if s.why != "" {
		// No lean standalone binary exists to mount, so a row would promise a
		// process that cannot be started. Same cause, same fix, as the fat stub.
		return nil, false, "it has no standalone binary — " + s.why
	}
	var found []string
	for _, e := range entries(root, s) {
		found = append(found, routePrefixes(e.dir, e.fn)...)
	}
	registered := normalize(found)
	// A declared Prefixes field is the composition root naming what the subsystem
	// owns. It outranks anything read out of the package, because it is the
	// statement MountAll itself already trusts to bound the subsystem's middleware
	// — but a declaration is not a registration, so what the package actually
	// registers still travels, separately, for dropLayers to read.
	if s.prefixes != nil {
		if p := declaredPrefixes(root, s.prefixes, s.imports); len(p) > 0 {
			return normalize(p), len(registered) > 0, ""
		}
	}
	if len(registered) > 0 {
		return registered, true, ""
	}
	// Nothing declared and nothing registered: fall back to the convention
	// MountSpec.Prefixes documents and Serve's generic liveness route already
	// assumes — /v1/<name>, where the child's own health probe lives. This is the
	// weakest of the three sources, so the generator names every app that lands
	// here: for one that really has no HTTP surface (a bus consumer) the mount is
	// inert, and a LAZY inert mount means the child never starts at all.
	fmt.Printf("convention prefix: %s — declares no Prefixes and registers no absolute route path; mounting at /v1/%s\n", s.name, s.name)
	return []string{"/v1/" + s.name}, false, ""
}

// entry is one route-registering function and the package it lives in.
type entry struct {
	dir string
	fn  string
}

// entries resolves a Wire entry's Mount field to the functions that register its
// routes. It reads the FUNCTION, not the package, because two Wire entries can
// share one: clients/account provides both MountAccount (the specific
// self-service routes) and MountBridge (the /v1/billing catch-all), and a
// package-wide scan would give each of them the other's paths — so the host
// would route the bridge's traffic to a binary that does not serve it.
func entries(root string, s spec) []entry {
	var out []entry
	ast.Inspect(s.mount, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		for p := range s.imports {
			// The qualifier's own import; cloud.CtxShutdown is an
			// adapter, never the subsystem's registrar.
			if p == modPath || path.Base(p) != id.Name {
				continue
			}
			for _, d := range appDirs(root, map[string]bool{p: true}) {
				out = append(out, entry{dir: d, fn: sel.Sel.Name})
			}
		}
		return true
	})
	return out
}

// declaredPrefixes reads a MountSpec.Prefixes field: either a []string literal
// written in place, or a package-level var in the app's own package (iam.Prefixes
// — the ONE list that also drives iam's real registrations).
func declaredPrefixes(root string, e ast.Expr, imports map[string]bool) []string {
	switch t := e.(type) {
	case *ast.CompositeLit:
		var out []string
		for _, el := range t.Elts {
			if s, ok := stringLit(el); ok {
				out = append(out, s)
			}
		}
		return out
	case *ast.SelectorExpr:
		for _, d := range appDirs(root, imports) {
			if v := pkgDirStrings(d)[t.Sel.Name]; len(v) > 0 {
				return v
			}
		}
	}
	return nil
}

// appDirs maps the import paths behind a Wire entry's fields to directories,
// dropping the cloud root (every entry mentions it; it is not an app).
func appDirs(root string, imports map[string]bool) []string {
	var out []string
	for p := range imports {
		if p == modPath {
			continue
		}
		if rest, ok := strings.CutPrefix(p, modPath+"/"); ok {
			out = append(out, filepath.Join(root, filepath.FromSlash(rest)))
			continue
		}
		// An external subsystem module (hanzoai/authz, hanzoai/licensing). The
		// module cache already holds it, so this reads a directory rather than
		// building anything.
		cmd := exec.Command("go", "list", "-f", "{{.Dir}}", p)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if b, err := cmd.Output(); err == nil {
			if d := strings.TrimSpace(string(b)); d != "" {
				out = append(out, d)
			}
		}
	}
	sort.Strings(out)
	return out
}

// routePrefixes returns every absolute path reachable from fn, following calls
// into the package's own functions — a Mount that delegates to routes(), which
// delegates to mountZAP(), still yields all three sets of paths.
//
// "Absolute" is decidable without type-checking: a path is relative exactly when
// its receiver came from a Group. Collecting group receivers first and reading
// only the calls made on something else is why plan's g.Get("/health") does not
// become the prefix "/health" and steal every subsystem's health probe.
func routePrefixes(dir, fn string) []string {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var out []string
	for _, pkg := range pkgs {
		vals := pkgStrings(pkg)
		funcs := map[string][]*ast.FuncDecl{}
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
					funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
				}
			}
		}
		// Which parameter positions receive a group, per callee name, over the
		// WHOLE package — computed before anything is read for paths, and to a
		// fixed point so a group handed down two levels is still known to be one.
		//
		// It is per NAME, not per declaration, because without type information
		// three distinct `register` methods are one name. That conflation is
		// resolved deliberately toward "group": mistaking the root app for a group
		// loses a root-level prefix, which 404s where you can see it, while
		// mistaking a group for the root app invents a root prefix that silently
		// swallows every sibling's traffic.
		grouped := map[string]map[int]bool{}
		for changed := true; changed; {
			changed = false
			for name, decls := range funcs {
				for _, fd := range decls {
					_, calls := walk(fd, vals, groupParams(fd, grouped[name]))
					for _, c := range calls {
						for i := range c.groupArgs {
							if grouped[c.name] == nil {
								grouped[c.name] = map[int]bool{}
							}
							if !grouped[c.name][i] {
								grouped[c.name][i] = true
								changed = true
							}
						}
					}
				}
			}
		}
		seen := map[string]bool{}
		for queue := []string{fn}; len(queue) > 0; {
			name := queue[0]
			queue = queue[1:]
			if seen[name] {
				continue
			}
			seen[name] = true
			for _, fd := range funcs[name] {
				paths, calls := walk(fd, vals, groupParams(fd, grouped[name]))
				out = append(out, paths...)
				for _, c := range calls {
					if funcs[c.name] != nil {
						queue = append(queue, c.name)
					}
				}
			}
		}
	}
	return out
}

// call is one call this body makes: the callee's name, and which of its
// arguments were groups.
type callSite struct {
	name      string
	groupArgs map[int]bool
}

// groupParams names the callee parameters that received a group. A registrar is
// routinely handed one — team does tg := app.Group("/v1/team") and then
// b.register(tg, guard) — so without carrying that across the call, register's
// r.Get("/bots") reads as a ROOT route and the app claims /bots for the whole
// fleet.
func groupParams(fd *ast.FuncDecl, args map[int]bool) map[string]bool {
	out := map[string]bool{}
	i := 0
	for _, f := range fd.Type.Params.List {
		for _, n := range f.Names {
			if args[i] {
				out[n.Name] = true
			}
			i++
		}
		if len(f.Names) == 0 {
			i++
		}
	}
	return out
}

// walk reads one function body: the absolute route paths it registers, and the
// package functions it calls. inGroups names this body's own parameters that
// the caller bound to a group.
func walk(fd *ast.FuncDecl, pkgVals map[string][]string, inGroups map[string]bool) (paths []string, calls []callSite) {
	groups := map[string]bool{}
	for p := range inGroups {
		groups[p] = true
	}
	vals := bind(fd, pkgVals)
	// Which calls this body makes purely for their EFFECT. A Group whose result
	// is thrown away installs middleware on a subtree and registers nothing —
	// zen's a.Group("/v1", z.Claim()) — while an assigned one is the root of a
	// route tree the app owns (team's tg := app.Group("/v1/team")). Same method,
	// opposite meanings, and only the statement around it tells them apart.
	discarded := map[*ast.CallExpr]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if st, ok := n.(*ast.ExprStmt); ok {
			if call, ok := st.X.(*ast.CallExpr); ok {
				discarded[call] = true
			}
		}
		return true
	})
	// Group bindings first, in a pass of their own: a body may register on a
	// group before the line that creates it is reached in AST order (a closure),
	// and mistaking a group for the app is what invents a bogus root prefix.
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		var lhs, rhs []ast.Expr
		switch t := n.(type) {
		case *ast.AssignStmt:
			lhs, rhs = t.Lhs, t.Rhs
		case *ast.ValueSpec:
			for _, id := range t.Names {
				lhs = append(lhs, id)
			}
			rhs = t.Values
		default:
			return true
		}
		if len(lhs) != len(rhs) {
			return true
		}
		for i, r := range rhs {
			if call, ok := r.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Group" {
					if id, ok := lhs[i].(*ast.Ident); ok {
						groups[id.Name] = true
					}
				}
			}
		}
		return true
	})
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// A registrar is usually HANDED to a mounter rather than called:
		// cloud.Mount(app, deps, "billing", build, routes) is the shape almost
		// every subsystem uses, and `routes` is an argument there, not a callee.
		// Following referenced functions as well as called ones is what makes the
		// walk see those routes at all.
		groupArgs := map[int]bool{}
		for i, a := range call.Args {
			if relative(a, groups) {
				groupArgs[i] = true
			}
			if id, ok := a.(*ast.Ident); ok {
				// Passed by reference, not called here: whoever calls it decides
				// its arguments, so nothing is known about its parameters yet.
				calls = append(calls, callSite{name: id.Name})
			}
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			calls = append(calls, callSite{name: f.Name, groupArgs: groupArgs})
		case *ast.SelectorExpr:
			// A method on a package value (h.register(tg)) is followed by name;
			// the receiver is irrelevant since only this package is searched.
			calls = append(calls, callSite{name: f.Sel.Name, groupArgs: groupArgs})
			if !routerMethods[f.Sel.Name] {
				return true
			}
			if f.Sel.Name == "Group" && len(call.Args) > 1 && discarded[call] {
				return true // middleware on a subtree, not a route on one
			}
			// Two registration forms, one meaning. The method form puts the
			// router in the receiver and the path first: app.Get("/v1/x", h).
			// The TYPED form is a generic function with the router as its first
			// argument and the path second: zip.Get(reg, "/v1/x", h). Subsystems
			// are being converted to the typed form (it is what earns an
			// operation its schema), so reading only the method form means an
			// app's prefixes silently shrink as it is modernized.
			router, at := f.X, 0
			if id, ok := f.X.(*ast.Ident); ok && id.Name == "zip" && len(call.Args) >= 2 {
				router, at = call.Args[0], 1
			}
			if len(call.Args) <= at || relative(router, groups) {
				return true
			}
			paths = append(paths, evalStrings(call.Args[at], vals)...)
		}
		return true
	})
	return paths, calls
}

// bind adds the local string values a body introduces to the package-level ones:
// the variable of a range over a []string, and a plain alias of one. Both are
// how this tree writes a family of routes — provisioning registers /v1/<kind>
// for every kind, exec owns each of its prefixes — so without them those apps
// read as registering nothing at all.
func bind(fd *ast.FuncDecl, pkgVals map[string][]string) map[string][]string {
	vals := map[string][]string{}
	for k, v := range pkgVals {
		vals[k] = v
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.RangeStmt:
			if id, ok := t.Value.(*ast.Ident); ok {
				if v := sliceStrings(t.X, vals); len(v) > 0 {
					vals[id.Name] = v
				}
			}
		case *ast.AssignStmt:
			if len(t.Lhs) != len(t.Rhs) {
				return true
			}
			for i, r := range t.Rhs {
				id, ok := t.Lhs[i].(*ast.Ident)
				if !ok {
					continue
				}
				if v := evalStrings(r, vals); len(v) > 0 {
					vals[id.Name] = v
				}
			}
		}
		return true
	})
	return vals
}

// sliceStrings reads a []string — written in place or named — into its elements.
func sliceStrings(e ast.Expr, vals map[string][]string) []string {
	switch t := e.(type) {
	case *ast.Ident:
		return vals[t.Name]
	case *ast.CompositeLit:
		var out []string
		for _, el := range t.Elts {
			out = append(out, evalStrings(el, vals)...)
		}
		return out
	}
	return nil
}

// relative reports whether calls on x are scoped to a group, and so register
// paths beneath a prefix rather than at one.
func relative(x ast.Expr, groups map[string]bool) bool {
	switch t := x.(type) {
	case *ast.Ident:
		return groups[t.Name]
	case *ast.CallExpr:
		sel, ok := t.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Group"
	case *ast.SelectorExpr:
		return groups[t.Sel.Name]
	}
	return false
}

// pkgStrings collects package-level string values: a constant, a var, or a
// []string. One map, because a route path is built from all three the same way —
// agentskills' wellKnown+"/index.json", exec's range over prefixes. A route
// spelled that way exists nowhere as a literal; without this the app reads as
// registering nothing and silently drops off the host.
// It resolves to a FIXED POINT rather than in one pass, because a value is
// routinely built from one declared in another file — deploy's
// loginPath = dashPrefix + "/login" — and ast.Package holds its files in a MAP.
// One pass therefore resolved such a name only when Go's randomized map order
// happened to visit its dependency first, and the generated manifest differed
// between runs of the SAME tree: deploy's /v1/deploy/{login,callback,logout}
// appeared about half the time. Iterating until nothing new resolves makes the
// output a function of the source and nothing else, which is what lets CI
// regenerate and diff.
func pkgStrings(pkg *ast.Package) map[string][]string {
	out := map[string][]string{}
	for changed := true; changed; {
		changed = false
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
					continue
				}
				for _, sp := range gd.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok || len(vs.Names) != len(vs.Values) {
						continue
					}
					for i, v := range vs.Values {
						s := evalStrings(v, out)
						if len(s) == 0 {
							s = sliceStrings(v, out)
						}
						name := vs.Names[i].Name
						if len(s) > 0 && !sameStrings(out[name], s) {
							out[name] = s
							changed = true
						}
					}
				}
			}
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pkgDirStrings is pkgStrings for a directory not already parsed.
func pkgDirStrings(dir string) map[string][]string {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, pkg := range pkgs {
		for k, v := range pkgStrings(pkg) {
			out[k] = v
		}
	}
	return out
}

// evalStrings folds a route path expression to every value it can take: a
// literal, a known name, or a concatenation of those. It is multi-valued because
// the name may be a loop variable — provisioning's "/v1/"+kind is one expression
// and seven routes.
func evalStrings(e ast.Expr, vals map[string][]string) []string {
	switch t := e.(type) {
	case *ast.BasicLit:
		if s, ok := stringLit(t); ok {
			return []string{s}
		}
	case *ast.Ident:
		return vals[t.Name]
	case *ast.BinaryExpr:
		if t.Op != token.ADD {
			return nil
		}
		var out []string
		for _, l := range evalStrings(t.X, vals) {
			for _, r := range evalStrings(t.Y, vals) {
				out = append(out, l+r)
			}
		}
		return out
	}
	return nil
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// normalize turns raw registered paths into the prefix set to mount.
//
// It NEVER widens a path to a shorter common ancestor. account registers
// /v1/iam/keys and /v1/iam/onboard; folding those to /v1/iam would hand it every
// request meant for the identity plane. Each stays exactly as deep as it was
// written, and zip mounts the path plus its subtree.
//
// A path that is unbounded at the ROOT is dropped rather than mounted. "/" and
// "/*" claim the entire surface; so does "/:org/:repo", which git registers for
// its browse UI and which matches every two-segment request in the fleet. In one
// binary such a route is harmless — its handler inspects the Host and falls
// through to the next matching route. Across a process boundary there is no
// falling through: the request has already left the host. So a plugin prefix
// must start with a literal segment, and a subsystem that needs a root wildcard
// keeps it by staying linked in.
func normalize(paths []string) []string {
	seen := map[string]bool{}
	var kept []string
	for _, p := range paths {
		p = strings.TrimSuffix(strings.TrimSuffix(p, "*"), "/")
		if p == "" || p == "/" || !strings.HasPrefix(p, "/") || strings.Contains(p, "://") {
			continue
		}
		if first, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/"); strings.HasPrefix(first, ":") || strings.HasPrefix(first, "+") || first == "" {
			continue
		}
		if !seen[p] {
			seen[p] = true
			kept = append(kept, p)
		}
	}
	sort.Strings(kept)
	var out []string
	for _, p := range kept {
		covered := false
		for _, q := range out {
			if p == q || strings.HasPrefix(p, q+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

// eagerSet reads apps.go's `eager` map — the ONE place that says which
// subsystems own a listener or a background loop and therefore must start with
// the host instead of on first request.
func eagerSet(file string) map[string]bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		die(fmt.Errorf("parse: %w", err))
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "eager" || len(vs.Values) != 1 {
			return true
		}
		cl, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, kok := stringLit(kv.Key)
			v, vok := kv.Value.(*ast.Ident)
			if kok && vok && v.Name == "true" {
				out[k] = true
			}
		}
		return false
	})
	if len(out) == 0 {
		die(fmt.Errorf("apps.go declares no `eager` map — every plugin would start lazily, including the ones that must be listening from t=0"))
	}
	return out
}

// writeManifest renders manifest/apps.go. A generated Go file, not a data file:
// the host stays one static binary with nothing to lose or mis-mount at run
// time, and a malformed manifest fails at compile rather than at 3am.
func writeManifest(root string, rows []manifestRow) error {
	var b strings.Builder
	b.WriteString("// Code generated by cmd/gen-app-cmds. DO NOT EDIT.\n\n")
	b.WriteString("package manifest\n\n")
	b.WriteString("// Apps is every subsystem that ships as its own binary, in apps.Wire()'s\n")
	b.WriteString("// mount order — which IS the routing order: the host loads them in this\n")
	b.WriteString("// sequence and the router takes the first prefix that matches.\n")
	b.WriteString("var Apps = []App{\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "\t{Name: %q, Prefixes: []string{", r.name)
		for i, p := range r.prefixes {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", p)
		}
		b.WriteString("}")
		if r.eager {
			b.WriteString(", Eager: true")
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")

	dir := filepath.Join(root, "manifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeIfChanged(filepath.Join(dir, "apps.go"), []byte(b.String()))
}
