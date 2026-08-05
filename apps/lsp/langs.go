package lsp

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// langs.go is the language table — the ONE place that answers three questions
// about a language: how to RECOGNIZE it in a checkout, how to START its server,
// and how to FETCH its dependencies.
//
// The servers and their argv are PORTED, not invented, from the Python tool at
// hanzo/python-sdk/pkg/hanzo-tools-lsp/hanzo_tools/lsp/lsp_tool.py (LSP_SERVERS):
// same binaries, same flags, same root markers, same extensions. A second
// opinion about how to spawn gopls is a second bug surface, so there is not one.
//
// What is NOT ported is install_cmd. The Python tool installs a language server
// on demand onto the machine it runs on; this service runs the server in a cloud
// image that already ships the toolchain (phase 2: the cloud-lsp Dockerfile). A
// cloud worker that can `npm install -g` at request time is a worker an attacker
// can make write to its own filesystem, so the capability is removed rather than
// guarded.
//
// # Scripts-off is the default, and it is stated here
//
// Fetching dependencies is the dangerous half of this service. `npm install`
// runs postinstall; `cargo build` runs build.rs; `pip install` of an sdist runs
// setup.py. Each is arbitrary code from a third party executing inside the
// worker — remote code execution by design, triggered by whatever the caller
// asked us to check out.
//
// It is also UNNECESSARY. A language server resolves definitions, references and
// types from SOURCE — the dependency's .go/.d.ts/.pyi/.rs files — not from the
// artifacts a build script produces. Turning scripts off costs some generated
// code and some proc-macro expansions; it does not cost go-to-definition.
//
// So Executes marks the fetches that run dependency-authored code, and fetchable
// (workspace.go) is the ONE predicate that reads it. Today it refuses them all.
type Lang struct {
	// Name is BOTH the table key and the LSP languageId sent on didOpen. One
	// string, so a language cannot be called one thing here and another on the
	// wire. (The per-file refinement TypeScript needs — tsx vs jsx vs plain js —
	// is a property of the FILE, not the language, and lives in ID.)
	Name string

	// Start is the argv that runs the server speaking JSON-RPC on its stdio.
	Start []string

	// Roots are the marker files that make a directory this language's root.
	Roots []string

	// Exts are the file extensions this server answers for.
	Exts []string

	// Fetch is the argv that populates the dependency tree, run once per
	// checkout. Nil means the language has nothing to fetch.
	Fetch []string

	// Executes reports that Fetch runs code authored by the DEPENDENCIES rather
	// than only downloading them. It is the whole of the scripts-off policy's
	// input; see fetchable in workspace.go for the policy itself.
	Executes bool

	// Env is added to the server's and the fetch's environment.
	Env []string

	// Init is initializationOptions on the initialize request — server-specific
	// settings. It is where a server that would otherwise run project code at
	// load time is told not to.
	Init map[string]any
}

// table is every language this service speaks, keyed by Lang.Name.
var table = map[string]Lang{
	"go": {
		Name:  "go",
		Start: []string{"gopls", "serve", "-mode=stdio"},
		Roots: []string{"go.work", "go.mod", "go.sum"},
		Exts:  []string{".go"},
		// `go mod download` resolves and verifies modules against the checksum
		// database. It does NOT build them, so no module's code runs: the Go
		// toolchain has no install-time hook to abuse. Executes stays false.
		Fetch: []string{"go", "mod", "download"},
		Env:   []string{"GOWORK=auto", "GOFLAGS=-mod=mod"},
	},
	"python": {
		Name:  "python",
		Start: []string{"pyright-langserver", "--stdio"},
		Roots: []string{"pyproject.toml", "setup.py", "requirements.txt", "pyrightconfig.json"},
		Exts:  []string{".py", ".pyi"},
		// uv sync BUILDS any dependency published only as an sdist, which runs
		// that dependency's setup.py / PEP-517 backend as us. --no-install-project
		// spares us the CHECKOUT's own build, not its dependencies'. So this one
		// executes, and today it does not run: pyright reads .py/.pyi source and
		// resolves the stdlib and any vendored packages without it.
		Fetch:    []string{"uv", "sync", "--frozen", "--no-install-project"},
		Executes: true,
	},
	"typescript": {
		Name:  "typescript",
		Start: []string{"typescript-language-server", "--stdio"},
		Roots: []string{"tsconfig.json", "package.json"},
		Exts:  []string{".ts", ".tsx", ".js", ".jsx"},
		// --ignore-scripts is the whole reason this fetch is allowed: it is npm's
		// own switch for "place the tree, run none of its lifecycle hooks". The
		// .d.ts files under node_modules are what the server actually reads.
		Fetch: []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"},
	},
	"rust": {
		Name:  "rust",
		Start: []string{"rust-analyzer"},
		Roots: []string{"Cargo.toml"},
		Exts:  []string{".rs"},
		// `cargo fetch` downloads and unpacks; it does not compile, so no build.rs
		// runs. `cargo build` would, which is why the fetch is not that.
		Fetch: []string{"cargo", "fetch", "--locked"},
		// The fetch being safe is not enough: rust-analyzer COMPILES AND RUNS
		// build.rs and expands proc macros at workspace load, by default. That is
		// the same remote code execution the fetch was careful to avoid, arriving
		// through the server instead. Both are turned off here — scripts-off has
		// to hold for the server or it does not hold at all.
		Init: map[string]any{
			"cargo":     map[string]any{"buildScripts": map[string]any{"enable": false}},
			"procMacro": map[string]any{"enable": false},
		},
	},
	"cpp": {
		Name:  "cpp",
		Start: []string{"clangd"},
		Roots: []string{"compile_commands.json", "CMakeLists.txt"},
		Exts:  []string{".cpp", ".cc", ".cxx", ".c", ".h", ".hpp"},
		// No fetch: C++ has no dependency resolver to run. clangd answers from
		// compile_commands.json when the checkout carries one and degrades to
		// single-file mode when it does not.
	},
}

// langFor picks the language for a repo-relative path by extension.
//
// Extension, not root markers, decides — the question being asked is "which
// server answers about THIS file", and a polyglot repo (a Go service with a
// TypeScript console) has several right answers at once, one per file. Roots
// then narrow WHERE that server is rooted, which is rootFor's job.
func langFor(path string) (Lang, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return Lang{}, false
	}
	// Deterministic: map iteration is randomized, and a file that resolved to a
	// different server between two identical requests would answer differently
	// for no reason the caller can see. Names are walked in sorted order.
	for _, name := range langNames {
		l := table[name]
		if slices.Contains(l.Exts, ext) {
			return l, true
		}
	}
	return Lang{}, false
}

// langNames is table's keys in sorted order — the tie-break that makes langFor a
// function of its argument alone.
var langNames = func() []string {
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}()

// rootFor finds the directory a server should be rooted at: the DEEPEST marker
// at or above the file, bounded by the checkout.
//
// Deepest wins because the marker nearest the file describes it best — a file in
// a repo whose root go.mod is the umbrella and whose subdirectory go.mod is the
// real module belongs to the subdirectory. With no marker anywhere the checkout
// root is the answer, which is what a single-file language wants.
//
// dir is the checkout root and is the hard ceiling: the walk starts there and
// only descends, so no marker outside the tenant's own tree can ever root a
// server.
func rootFor(dir, path string, l Lang) string {
	best, cur := dir, dir
	for _, seg := range strings.Split(filepath.Dir(path), string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		for _, marker := range l.Roots {
			if _, err := os.Stat(filepath.Join(cur, marker)); err == nil {
				best = cur
				break
			}
		}
	}
	return best
}

// ID is the LSP languageId for one FILE. It is Name for every language but
// TypeScript, whose server distinguishes four dialects that share one toolchain
// — and gets the wrong answer for a React file told it is plain TypeScript.
// Ported from the Python tool's _open_document.
func (l Lang) ID(path string) string {
	if l.Name != "typescript" {
		return l.Name
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsx":
		return "typescriptreact"
	case ".jsx":
		return "javascriptreact"
	case ".js":
		return "javascript"
	default:
		return "typescript"
	}
}
