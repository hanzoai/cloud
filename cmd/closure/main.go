// Command closure records what each app's document was generated FROM, and
// checks — in about five seconds — that no document has been left behind by a
// dependency that moved.
//
// plugin/<app>/openapi.json is a projection of that app's router, and the router
// is built from source: the app's own, and every package it links. `make -f
// mk/fleet.mk check` proves the projection is current by REGENERATING it. That is
// exact, it is the decider, and it stays. It is the wrong place to LEARN, because
// it costs one link per app — ten minutes warm on a twenty-core box, fifty on the
// runner — and it is the LAST gate in hanzo.yml, so it reports a thirty-second
// regeneration after everything cheap has already been paid for.
//
// THE BLIND SPOT THIS WAS LEARNED FROM. A commit that moves a version in go.mod
// and touches nothing else silently invalidates a committed document. hanzoai/iam
// v1.34.21 → v1.34.29 added EnableCodeSignin to the type behind iam.Application;
// the commit touched go.mod, go.sum and apps/iam and not one generated file;
// plugin/iam/openapi.json went stale on main, and the release train stopped for
// fifty-three minutes to say so. Nothing in the tree could have said it sooner,
// because nothing in the tree recorded what that document had been generated
// from. This records it.
//
// PACKAGES, NOT MODULES, AND THE MEASUREMENT SAYS WHY — including where it does
// NOT help, which matters more.
//
// Module granularity is unusable: github.com/hanzoai/iam is in the import closure
// of 122 of 125 apps, and so are eighteen other hanzoai modules. "iam moved, so
// regenerate everything that imports iam" therefore names the whole fleet on every
// bump, forever, whatever the bump did. A gate that always says the same thing is
// not consulted.
//
// Package granularity asks a strictly narrower question — which apps link a package
// whose SOURCE moved — and it is bounded by reach, which is the part that is
// guaranteed rather than lucky: an app's digest cannot move unless a package in ITS
// closure moved, so a module only 3 apps link can never implicate more than 3.
// github.com/hanzoai/o11y is in 1 closure, hanzoai/ai in 3, hanzoai/plans in 6:
// those bumps resolve to a handful of links and about a minute.
//
// It is NOT a promise of a small number, and the bump this was built for is exactly
// the case that shows it. v1.34.21 → v1.34.29 moved twelve iam packages. Ten of them
// are reached by four apps or fewer — including internal/bootstrap, where
// EnableCodeSignin actually lives — but pkg/schema moved too, and 122 apps link
// that. So this reports 121, and 121 is the CORRECT answer: those closures really
// did move, and `make describe` really was the fix that landed. Package granularity
// did not shrink that one. What it did was answer in two seconds instead of
// fifty-three minutes, which is the whole point and is true of every bump.
//
// The value is the SOURCE, not the module's version string, for the same reason: a
// bump that changes no byte this repo reaches moves no digest and asks for nothing,
// so a retag or a go.sum churn is not a false alarm.
//
// THE MAIN MODULE IS EXCLUDED, and not to save time. cloud's own source is
// already policed from source by app-contract, and every app links the root
// package — so hashing it would move all 125 digests on every commit and this
// would report the whole fleet, every time, forever. The dependency axis is the
// one with no other checker, and it is the axis this measures.
//
// IT PREDICTS, IT DOES NOT DECIDE. A moved closure means the document was
// generated from source that has since changed; it does not prove the projection
// differs, because most changes never reach a route. Regenerating is how you find
// out, and app-contract still regenerates everything and still decides. This
// exists to move that discovery from minute fifty to second five, and to name the
// apps worth regenerating rather than the fleet.
//
//	check:  go run ./cmd/closure          # the gate. reports EVERY stale app at once
//	record: go run ./cmd/closure -write   # run by `make describe`, never by hand
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// The module this repo IS. Its packages are the ones app-contract already checks
// from source, and the ones every app links, so they are not part of the witness.
const mainModule = "github.com/hanzoai/cloud"

// Where the roots live, and what makes one an app: plugin/<app> is the app's
// main package and plugin/<app>/openapi.json is the document it projects. An app
// with no committed document (kafka, and the coresident apps — mk/fleet.mk names
// both and why) has nothing here to be stale, and drops out by having no file
// rather than by being listed a second time.
const (
	rootPattern = "./plugin/..."
	rootPrefix  = mainModule + "/plugin/"
)

// The witness, beside openapi/floor.json — the other recorded invariant of the
// same document, kept honest the same way.
const witnessPath = "openapi/closure.json"

// ONE CONFIGURATION, STATED HERE, because a digest that depends on the box that
// wrote it cannot be committed. linux/amd64 is what the clusters run and what the
// runner builds; CGO_ENABLED=0 is mk/go.mk's floor and what mk/plugin.mk links
// every app binary with. Build constraints select FILES, so any other setting
// hashes a different set of them — a developer on darwin/arm64 has to compute the
// same value as the runner, and pinning is the only way that holds.
var listEnv = []string{
	"GOOS=linux",
	"GOARCH=amd64",
	"CGO_ENABLED=0",
	"GOWORK=off",
	"GOPRIVATE=github.com/hanzoai/*",
}

// pkg is the slice of `go list` output this needs: who a package is, where its
// source is, and what it imports.
type pkg struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Imports    []string
	Module     *struct {
		Path    string
		Version string
	}
}

// witnessed says whether a package is part of what a document was generated FROM.
//
// ONE definition, because two agree about it — the hashing and the digest — and a
// filter written twice is a filter that can be broken once. Stated in two places it
// was: hashing skipped the main module while the digest still named its packages,
// so those lines carried an EMPTY hash and compared equal no matter what the source
// did. Silently, and in the safe direction, which is the kind that survives review.
//
// Stdlib (no module) is out because it moves with the toolchain, not with this repo.
// The main module is out because app-contract already polices cloud's own source
// from source, and every app links the root package — witnessing it would move all
// 125 digests on every commit and report the fleet, every time, forever.
func witnessed(p *pkg) bool {
	return p != nil && p.Module != nil && p.Module.Path != mainModule
}

// witness is the committed record: what each document was generated from, and the
// module versions that closure resolved to at the time.
//
// Modules are recorded for the MESSAGE, never for the comparison — the digests
// decide. Without them a failure can say "four documents moved" and not which
// dependency moved them, and a gate that cannot name the cause makes you bisect.
type witness struct {
	Modules map[string]string `json:"modules"`
	Apps    map[string]string `json:"apps"`
}

func main() {
	write := flag.Bool("write", false, "record the current closure (run by `make closure`)")
	// WHICH APPS CAN DESCRIBE THEMSELVES, passed in rather than worked out here.
	//
	// Not every app with a document can regenerate one: kafka's Mount is fail-closed
	// on a live broker, and a coresident app is middleware on a sibling's router and
	// refuses to mount alone. mk/fleet.mk owns that set — one is named, the other is
	// DERIVED from manifest/apps.go — and it is $(DESCRIBABLE) there.
	//
	// It is passed because the alternative was worse in a way that was measured, not
	// argued: this tool restated nothing, witnessed every app with a document, and
	// so printed `make -f mk/fleet.mk describe/zen` for a coresident app. That target
	// does not exist, and `make` answered "No rule to make target". A gate whose
	// repair command does not run is the exact failure mk/fleet.mk already carries a
	// paragraph about — the fix for a red gate routed through a sweep that could not
	// complete. One definition, read here, never a second copy to fall out of step.
	describable := flag.String("describable", "", "apps that can regenerate their own document (mk/fleet.mk $(DESCRIBABLE))")
	flag.Parse()

	if err := run(*write, strings.Fields(*describable)); err != nil {
		fmt.Fprintln(os.Stderr, "closure:", err)
		os.Exit(1)
	}
}

func run(write bool, describable []string) error {
	// REQUIRED IN BOTH MODES, and the second one is the one that bit.
	//
	// Refusing to WRITE without it is obvious: defaulting to "every app with a
	// document" is how the unrunnable `describe/zen` repair got printed. Refusing to
	// CHECK without it is the same fact from the dangerous side — the filter emptied
	// the comparison set, nothing was compared, and the gate printed "0 app documents
	// current" and exited 0. A gate that silently checks nothing is worse than no
	// gate, because it is believed.
	if len(describable) == 0 {
		return fmt.Errorf("-describable is required — run `make closure` or `make closure-check`, which pass mk/fleet.mk's $(DESCRIBABLE)")
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	pkgs, err := list(root)
	if err != nil {
		return err
	}
	have, err := snapshot(root, pkgs, describable)
	if err != nil {
		return err
	}
	if write {
		return record(filepath.Join(root, witnessPath), have)
	}
	want, err := load(filepath.Join(root, witnessPath))
	if err != nil {
		return err
	}
	return compare(os.Stderr, want, have)
}

// repoRoot finds the module root by walking up to the go.mod that declares
// mainModule, so the tool works from anywhere — `make` runs it from the root, a
// developer runs it from wherever they are.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(b), "module "+mainModule) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s go.mod above %q", mainModule, dir)
		}
		dir = parent
	}
}

// list asks the toolchain for the whole plugin closure in ONE invocation: 4312
// packages in under two seconds. -e so a package that does not build is reported
// rather than aborting the walk — a broken app is app-contract's to fail, and this
// gate going red for it would be the masking this whole change exists to remove.
func list(root string) ([]pkg, error) {
	cmd := exec.Command("go", "list", "-e", "-deps",
		"-json=ImportPath,Dir,GoFiles,CgoFiles,Imports,Module", rootPattern)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), listEnv...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	var pkgs []pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("go list output: %w", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, nil
}

// snapshot reduces the package graph to the witness: one digest per app that has
// a document, plus the module versions its closure reached.
func snapshot(root string, pkgs []pkg, describable []string) (witness, error) {
	can := make(map[string]bool, len(describable))
	for _, app := range describable {
		can[app] = true
	}
	byPath := make(map[string]*pkg, len(pkgs))
	for i := range pkgs {
		byPath[pkgs[i].ImportPath] = &pkgs[i]
	}

	// Hash every external package once, in parallel — 3718 packages and 222 MB of
	// source, shared many times over across 125 closures.
	hash, err := hashAll(pkgs)
	if err != nil {
		return witness{}, err
	}

	w := witness{Modules: map[string]string{}, Apps: map[string]string{}}
	for i := range pkgs {
		p := &pkgs[i]
		app := strings.TrimPrefix(p.ImportPath, rootPrefix)
		if app == p.ImportPath || strings.Contains(app, "/") {
			continue // not a plugin root
		}
		if _, err := os.Stat(filepath.Join(root, "plugin", app, "openapi.json")); err != nil {
			continue // no document to be stale
		}
		// An app that cannot describe itself has a document nothing regenerates — a
		// constant, and a constant cannot go stale. Witnessing it would report a
		// document no command can repair.
		if !can[app] {
			continue
		}

		var lines []string
		for dep := range reach(byPath, p) {
			d := byPath[dep]
			if !witnessed(d) {
				continue
			}
			lines = append(lines, dep+"\x00"+hash[dep])
			w.Modules[d.Module.Path] = d.Module.Version
		}
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		w.Apps[app] = hex.EncodeToString(sum[:])
	}
	return w, nil
}

// reach walks the import graph from one root. `go list -deps` gives the union of
// every root's closure; the per-root closure is this walk over the Imports it also
// gives, which is why one invocation is enough for all 125.
func reach(byPath map[string]*pkg, from *pkg) map[string]bool {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		if p := byPath[path]; p != nil {
			for _, imp := range p.Imports {
				walk(imp)
			}
		}
	}
	for _, imp := range from.Imports {
		walk(imp)
	}
	return seen
}

// hashAll digests the SOURCE of every external package. Filenames are hashed
// alongside contents so a file appearing or being renamed moves the digest —
// a package's type surface is the set of files as much as the bytes in them.
func hashAll(pkgs []pkg) (map[string]string, error) {
	type result struct {
		path string
		sum  string
		err  error
	}
	work := make(chan *pkg)
	out := make(chan result)

	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Go(func() {
			for p := range work {
				sum, err := hashPkg(p)
				out <- result{p.ImportPath, sum, err}
			}
		})
	}
	go func() {
		for i := range pkgs {
			if p := &pkgs[i]; witnessed(p) {
				work <- p
			}
		}
		close(work)
		wg.Wait()
		close(out)
	}()

	hash := make(map[string]string, len(pkgs))
	for r := range out {
		if r.err != nil {
			return nil, r.err
		}
		hash[r.path] = r.sum
	}
	return hash, nil
}

func hashPkg(p *pkg) (string, error) {
	// A package the toolchain could not place has no source to hash, and hashing
	// nothing yields a CONSTANT — the same digest for every unresolved package, and a
	// witness that silently compares equal or silently compares stale depending on
	// which side of the run it happened on. `-e` above is what allows a broken
	// package through (deliberately: a build error is app-contract's to report, not
	// this gate's), so the missing directory has to be caught here instead.
	if p.Dir == "" {
		return "", fmt.Errorf("%s: the toolchain could not locate this package — run `go mod download` (a missing module cannot be witnessed, and hashing nothing would compare equal)", p.ImportPath)
	}

	files := append(append([]string{}, p.GoFiles...), p.CgoFiles...)
	sort.Strings(files)

	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(p.Dir, f))
		if err != nil {
			return "", fmt.Errorf("%s: %w", p.ImportPath, err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func record(path string, w witness) error {
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf(">> %s — %d app documents, %d modules\n", witnessPath, len(w.Apps), len(w.Modules))
	return nil
}

func load(path string) (witness, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return witness{}, fmt.Errorf("%s is missing — run `make describe`: %w", witnessPath, err)
	}
	var w witness
	if err := json.Unmarshal(b, &w); err != nil {
		return witness{}, fmt.Errorf("%s: %w", witnessPath, err)
	}
	return w, nil
}

// compare reports EVERY stale document in one pass, and names the dependency that
// moved. Reporting the first one is how three separate red runs cost hours: each
// told you about one file, and the next one found the next.
func compare(w io.Writer, want, have witness) error {
	// An app that has a document but no recorded closure is stale by definition:
	// nothing ever recorded what it was generated from, and the empty string never
	// equals a digest — so a NEW app needs no separate case.
	var stale []string
	for app, sum := range have.Apps {
		if want.Apps[app] != sum {
			stale = append(stale, app)
		}
	}
	sort.Strings(stale)

	// FAIL CLOSED ON AN EMPTY COMPARISON. Nothing to compare is not "everything is
	// current" — it is the gate having lost its subject, which is how this reported
	// green while checking nothing at all. Belt to the -describable brace above:
	// whatever empties the set, the answer is red rather than a confident zero.
	if len(have.Apps) == 0 {
		return fmt.Errorf("no app documents were compared — the gate has nothing to check, which is a defect in the gate and never a pass")
	}

	if len(stale) == 0 {
		fmt.Fprintf(w, ">> %d app documents current with their dependencies\n", len(have.Apps))
		return nil
	}

	var moved []string
	for mod, version := range have.Modules {
		if was, ok := want.Modules[mod]; !ok {
			moved = append(moved, fmt.Sprintf("    %s  (new)  %s", mod, version))
		} else if was != version {
			moved = append(moved, fmt.Sprintf("    %s  %s → %s", mod, was, version))
		}
	}
	sort.Strings(moved)

	fmt.Fprintf(w, "\nSTALE: %d app document(s) were generated from source that has since moved.\n\n", len(stale))
	if len(moved) > 0 {
		fmt.Fprintln(w, "  dependencies that moved since these documents were recorded:")
		for _, m := range moved {
			fmt.Fprintln(w, m)
		}
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "  no module version moved — a dependency's source changed underneath a")
		fmt.Fprintln(w, "  pinned version (a replace, a workspace, or a retagged release).")
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "  documents to regenerate:")
	for _, app := range stale {
		fmt.Fprintf(w, "    plugin/%s/openapi.json\n", app)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  fix:")
	fmt.Fprintf(w, "    %s\n", repair(stale, len(have.Apps)))
	fmt.Fprintln(w, "    # then commit plugin/*/openapi.json, openapi.yaml, public.yaml and openapi/closure.json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  This is the cheap warning, not the verdict: regenerating may produce no change,")
	fmt.Fprintln(w, "  in which case only openapi/closure.json moves and that is the whole fix.")
	fmt.Fprintln(w)
	return fmt.Errorf("%d app document(s) stale", len(stale))
}

// repair names the CHEAPEST command that regenerates what moved. A bump to a
// package four apps link is four links and about a minute, where `make describe`
// is one link per app and 6m39s warm. Past a third of the fleet the scoped form is
// longer to read than the sweep is to run, so it stops — and that branch is the
// common one for the nineteen hanzoai modules in 122 of 125 closures, which is why
// it exists rather than emitting 121 targets nobody will paste.
func repair(stale []string, total int) string {
	if len(stale)*3 > total {
		return "make describe"
	}
	targets := make([]string, len(stale))
	for i, app := range stale {
		targets[i] = "describe/" + app
	}
	return "make -f mk/fleet.mk " + strings.Join(targets, " ") + " && make closure && make -f mk/fleet.mk openapi"
}
