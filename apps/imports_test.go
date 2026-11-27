package apps_test

import (
	"bufio"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// An app is a plugin that will build in its own repository. What it may import
// is therefore what that repository can reach:
//
//	sdk/...      the public SDK (github.com/hanzoai/cloud after the move)
//	client/...   the typed plane clients of its siblings
//	apps/<self>  its own packages
//	any module outside this one, except a private org module
//
// Anything else — a sibling app, internal/..., a top-level package of this
// repository — is a dependency that cannot leave with the app. Each one that
// exists today is a line in exceptions.txt, and the file only shrinks: a new
// import fails here, and so does a line whose import is gone, so the ledger is
// always exactly the work left.
const (
	module = "github.com/hanzoai/cloud/"
	ledger = "exceptions.txt"
)

func allowed(app, rel string) bool {
	switch {
	case rel == "sdk" || strings.HasPrefix(rel, "sdk/"):
		return true
	case rel == "client" || strings.HasPrefix(rel, "client/"):
		return true
	case rel == "apps/"+app || strings.HasPrefix(rel, "apps/"+app+"/"):
		return true
	}
	return false
}

// found walks every Go file under apps/ (tests included: they move with the app)
// and returns the set of "apps/<app> <import>" pairs the rule refuses.
func found(t *testing.T) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != "." && (strings.HasPrefix(n, ".") || n == "testdata" || n == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || !strings.Contains(path, string(filepath.Separator)) {
			return nil
		}
		app := strings.SplitN(filepath.ToSlash(path), "/", 2)[0]
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			var rel string
			switch {
			case strings.HasPrefix(p, module):
				rel = strings.TrimPrefix(p, module)
				if allowed(app, rel) {
					continue
				}
			case strings.HasPrefix(p, "github.com/"+"hanzo-inc/"):
				rel = p // a private module: it cannot be required from a public one
			default:
				continue
			}
			got["apps/"+app+" "+rel] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk apps: %v", err)
	}
	return got
}

func listed(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(ledger)
	if err != nil {
		t.Fatalf("open %s: %v", ledger, err)
	}
	defer f.Close()
	out := map[string]bool{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	if err := s.Err(); err != nil {
		t.Fatalf("read %s: %v", ledger, err)
	}
	return out
}

func TestAnAppImportsOnlyWhatLeavesWithIt(t *testing.T) {
	got, have := found(t), listed(t)
	var added, gone []string
	for k := range got {
		if !have[k] {
			added = append(added, k)
		}
	}
	for k := range have {
		if !got[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(added)
	sort.Strings(gone)
	for _, k := range added {
		t.Errorf("%s: an app imports only sdk/..., client/..., its own packages and public modules; "+
			"reach a sibling through its client, and move what the SDK lacks into sdk/", k)
	}
	for _, k := range gone {
		t.Errorf("%s is listed in apps/%s but no longer imported: delete the line (the ledger only shrinks)", k, ledger)
	}
}

// The sibling imports still in the ledger must never close a loop: an app in a
// cycle cannot be built in its own repository even after its other imports move,
// because each side needs the other's module first.
func TestNoTwoAppsImportEachOther(t *testing.T) {
	next := map[string][]string{}
	for k := range found(t) {
		from, imp, _ := strings.Cut(k, " ")
		if !strings.HasPrefix(imp, "apps/") {
			continue
		}
		to := "apps/" + strings.SplitN(strings.TrimPrefix(imp, "apps/"), "/", 2)[0]
		next[from] = append(next[from], to)
	}
	const (
		unseen = iota
		open
		done
	)
	state := map[string]int{}
	var path []string
	var walk func(a string) bool
	walk = func(a string) bool {
		state[a] = open
		path = append(path, a)
		for _, b := range next[a] {
			switch state[b] {
			case open:
				i := len(path) - 1
				for path[i] != b {
					i--
				}
				t.Errorf("apps import each other: %s -> %s; reach one side through its client", strings.Join(path[i:], " -> "), b)
				return true
			case unseen:
				if walk(b) {
					return true
				}
			}
		}
		path = path[:len(path)-1]
		state[a] = done
		return false
	}
	apps := make([]string, 0, len(next))
	for a := range next {
		apps = append(apps, a)
	}
	sort.Strings(apps)
	for _, a := range apps {
		if state[a] == unseen && walk(a) {
			return
		}
	}
}
