package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

// toolchain are the inputs every binary is built FROM rather than out of. A
// change to one moves every binary in ways no import graph shows.
var toolchain = map[string]bool{
	"Dockerfile":    true,
	".dockerignore": true,
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "dirs":
		if len(os.Args) != 3 {
			usage()
		}
		err = dirs(os.Args[2])
	case "bins":
		err = bins(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cone:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: cone dirs <base> | cone bins <dir>… < roots")
	os.Exit(2)
}

func dirs(base string) error {
	out, err := exec.Command("git", "diff", "--name-only", "--no-renames", base, "HEAD").Output()
	if err != nil {
		return fmt.Errorf("git diff %s HEAD: %w", base, err)
	}
	touched := map[string]bool{}
	for _, p := range strings.Fields(string(out)) {
		if toolchain[p] {
			fmt.Println("all")
			return nil
		}
		if p == "go.mod" {
			mods, all, err := moved(base)
			if err != nil {
				return err
			}
			if all {
				fmt.Println("all")
				return nil
			}
			for _, m := range mods {
				touched["mod:"+m] = true
			}
			continue
		}
		if p == "manifest/apps.go" {
			added, err := spawned(base)
			if err != nil {
				return err
			}
			for _, a := range added {
				touched["app:"+a] = true
			}
		}
		for _, d := range owners(p) {
			touched[d] = true
		}
	}
	for _, d := range sorted(touched) {
		fmt.Println(d)
	}
	return nil
}

// embedLine is a //go:embed directive, wherever gofmt indents it.
var embedLine = regexp.MustCompile(`^\s*//go:embed\s`)

// appRow is one row of manifest.Apps, read the way the Dockerfile reads it.
var appRow = regexp.MustCompile(`\{Name: "([^"]*)"`)

// spawned are the apps manifest/apps.go spawns at HEAD and did not at base: each
// is a binary the base image does not carry. A coresident app is never spawned,
// so it has no binary of its own either way.
func spawned(base string) ([]string, error) {
	old, err := exec.Command("git", "show", base+":manifest/apps.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:manifest/apps.go: %w", base, err)
	}
	now, err := os.ReadFile("manifest/apps.go")
	if err != nil {
		return nil, err
	}
	had := apps(old)
	var out []string
	for a := range apps(now) {
		if !had[a] {
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out, nil
}

func apps(src []byte) map[string]bool {
	out := map[string]bool{}
	for _, l := range strings.Split(string(src), "\n") {
		if m := appRow.FindStringSubmatch(l); m != nil && !strings.Contains(l, "Coresident: true") {
			out[m[1]] = true
		}
	}
	return out
}

// modfile is what go.mod says, as `go mod edit -json` prints it.
type modfile struct {
	Go        string
	Toolchain *struct{ Name string }
	Require   []struct{ Path, Version string }
	Replace   []struct {
		Old struct{ Path, Version string }
		New struct{ Path, Version string }
	}
}

// moved are the modules whose selected source differs between base's go.mod and
// HEAD's: a requirement added, dropped or at another version, or a replacement
// added, dropped or pointed elsewhere. all is a go or toolchain line that moved,
// which changes the compiler under every binary.
func moved(base string) (mods []string, all bool, err error) {
	old, err := exec.Command("git", "show", base+":go.mod").Output()
	if err != nil {
		return nil, false, fmt.Errorf("git show %s:go.mod: %w", base, err)
	}
	now, err := os.ReadFile("go.mod")
	if err != nil {
		return nil, false, err
	}
	a, err := parse(old)
	if err != nil {
		return nil, false, err
	}
	b, err := parse(now)
	if err != nil {
		return nil, false, err
	}
	if a.Go != b.Go || (a.Toolchain == nil) != (b.Toolchain == nil) || a.Toolchain != nil && a.Toolchain.Name != b.Toolchain.Name {
		return nil, true, nil
	}
	m := map[string]bool{}
	diff := func(x, y map[string]string) {
		for k, v := range x {
			if y[k] != v {
				m[k] = true
			}
		}
	}
	ra, rb := requires(a), requires(b)
	diff(ra, rb)
	diff(rb, ra)
	pa, pb := replaces(a), replaces(b)
	diff(pa, pb)
	diff(pb, pa)
	return sorted(m), false, nil
}

func parse(b []byte) (modfile, error) {
	f, err := os.CreateTemp("", "cone-*.mod")
	if err != nil {
		return modfile{}, err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		return modfile{}, err
	}
	f.Close()
	out, err := exec.Command("go", "mod", "edit", "-json", f.Name()).Output()
	if err != nil {
		return modfile{}, fmt.Errorf("go mod edit -json: %w", err)
	}
	var m modfile
	return m, json.Unmarshal(out, &m)
}

func requires(m modfile) map[string]string {
	out := map[string]string{}
	for _, r := range m.Require {
		out[r.Path] = r.Version
	}
	return out
}

func replaces(m modfile) map[string]string {
	out := map[string]string{}
	for _, r := range m.Replace {
		out[r.Old.Path] = r.Old.Version + "=>" + r.New.Path + "@" + r.New.Version
	}
	return out
}

// owners are the package directories one changed path can move, as they stand
// at HEAD. A deleted file still moves the package that held it; a deleted
// package moves nothing by itself, because every package that imported it had
// to change its own files to stop.
func owners(p string) []string {
	d, ext := path.Dir(p), path.Ext(p)
	var out []string
	if (ext == ".go" || source[ext]) && !ignored(d) && goFiles(d) {
		out = append(out, d)
	}
	// testdata is read by the tests of the package that holds it.
	if i := strings.Index(d+"/", "/testdata/"); i > 0 {
		if h := d[:i]; !ignored(h) && goFiles(h) {
			out = append(out, h)
		}
	}
	if ext == ".go" {
		return out
	}
	for a := d; ; a = path.Dir(a) {
		if !ignored(a) && embeds(a) {
			out = append(out, a)
		}
		if a == "." {
			return out
		}
	}
}

// source are the files besides .go that the go command compiles out of a
// package's own directory.
var source = map[string]bool{
	".c": true, ".h": true, ".s": true, ".S": true, ".sx": true, ".cc": true,
	".cpp": true, ".cxx": true, ".hh": true, ".hpp": true, ".hxx": true,
	".m": true, ".f": true, ".F": true, ".for": true, ".f90": true,
	".syso": true, ".swig": true, ".swigcxx": true,
}

// ignored is a directory Go never makes a package of, so its files are data
// for an enclosing one.
func ignored(d string) bool {
	for _, e := range strings.Split(d, "/") {
		if e == "testdata" || strings.HasPrefix(e, ".") && e != "." || strings.HasPrefix(e, "_") {
			return true
		}
	}
	return false
}

func goFiles(d string) bool {
	es, err := os.ReadDir(d)
	if err != nil {
		return false
	}
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

func embeds(d string) bool {
	es, err := os.ReadDir(d)
	if err != nil {
		return false
	}
	for _, e := range es {
		n := e.Name()
		// Test files too: a test's embed is what its gate reads, and counting
		// it only rebuilds more.
		if e.IsDir() || !strings.HasSuffix(n, ".go") {
			continue
		}
		b, err := fs.ReadFile(os.DirFS(d), n)
		if err != nil {
			continue
		}
		// Indented too: a directive inside a `var (` block is gofmt'd with a tab.
		for _, l := range bytes.Split(b, []byte("\n")) {
			if embedLine.Match(l) {
				return true
			}
		}
	}
	return false
}

func bins(touched []string) error {
	var roots []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if r := strings.TrimSpace(sc.Text()); r != "" {
			roots = append(roots, r)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(roots) == 0 || len(touched) == 0 {
		return nil
	}

	// Directory to import path, for the module's own packages only: a touched
	// directory that is no package at HEAD reaches nothing.
	mod, err := goList("-m")
	if err != nil {
		return err
	}
	module := strings.TrimSpace(mod)
	pkgs := map[string]bool{}
	var mods []string
	for _, d := range touched {
		if m, ok := strings.CutPrefix(d, "mod:"); ok {
			mods = append(mods, m)
			continue
		}
		if strings.HasPrefix(d, "app:") {
			continue
		}
		d = strings.TrimPrefix(path.Clean(d), "./")
		if d == "." {
			pkgs[module] = true
		} else {
			pkgs[module+"/"+d] = true
		}
	}
	// A package of a moved module, or of a module nested under its path, which
	// errs toward rebuilding.
	reached := func(p string) bool {
		if pkgs[p] {
			return true
		}
		for _, m := range mods {
			if p == m || strings.HasPrefix(p, m+"/") {
				return true
			}
		}
		return false
	}

	args := []string{"-e", "-f", "{{.ImportPath}}{{range .Deps}} {{.}}{{end}}"}
	if t := os.Getenv("CONE_TAGS"); t != "" {
		args = append(args, "-tags", t)
	}
	out, err := goList(append(args, roots...)...)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(roots) {
		return fmt.Errorf("go list answered %d packages for %d roots", len(lines), len(roots))
	}
	for i, l := range lines {
		for _, p := range strings.Fields(l) {
			if reached(p) {
				fmt.Println(roots[i])
				break
			}
		}
	}
	return nil
}

func goList(args ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
