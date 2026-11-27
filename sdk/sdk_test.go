package cloud_test

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

// sdk/ is the tree that becomes the public module github.com/hanzoai/cloud, so
// what its code may import is what that module can reach: itself and public
// modules. Never an app, never internal/..., never a private github.com/hanzo-inc
// module, and never a production driver: an SDK package names an interface and
// ships its local default (SQLite, local disk, in-process), and the driver for
// hanzo-sql, hanzo-kv, hanzo-s3 or a remote KMS registers into that interface
// from the host. scripts/sdk.sh proves the first half by building sdk/ alone;
// this proves the second, which a build cannot see.
//
// What does not hold yet is a line in exceptions.txt, and that file only
// shrinks: "<pkg> <import>" for code (a package that moved in with its
// production driver, before the driver moved out to internal/), and
// "<pkg>_test <import>" for a test that leans on this repository's test kits
// (the PostgreSQL leg of a store test runs against sqltest here).
const (
	module = "github.com/hanzoai/cloud/"
	ledger = "exceptions.txt"
)

// drivers are the production backends the SDK must not link.
var drivers = []string{
	"github.com/jackc/pgx",               // hanzo-sql
	"github.com/hanzokv/",                // hanzo-kv
	"github.com/hanzos3/",                // hanzo-s3
	"github.com/nats-io/",                // the cross-pod bus
	"github.com/hanzo-ds/",               // datastore (the OLAP mirror)
	"github.com/luxfi/kms/pkg/zapclient", // remote KMS
}

func refused(p string) (string, bool) {
	switch {
	case strings.HasPrefix(p, module+"sdk/") || p == module+"sdk":
		return "", false
	case strings.HasPrefix(p, module):
		return strings.TrimPrefix(p, module), true
	case strings.HasPrefix(p, "github.com/hanzo-inc/"):
		return p, true
	}
	for _, d := range drivers {
		if strings.HasPrefix(p, d) {
			return p, true
		}
	}
	return "", false
}

func TestTheSDKImportsOnlyItselfAndPublicModules(t *testing.T) {
	fset := token.NewFileSet()
	got := map[string]bool{}
	err := filepath.WalkDir("sdk", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != "sdk" && (strings.HasPrefix(n, ".") || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(path))
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if what, bad := refused(p); bad {
				if strings.HasSuffix(path, "_test.go") {
					got[pkg+"_test "+what] = true
				} else {
					got[pkg+" "+what] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk sdk: %v", err)
	}
	have := listed(t)
	for _, k := range keys(got) {
		if !have[k] {
			t.Errorf("%s: the SDK imports only sdk/... and public modules, and links no production driver; "+
				"name an interface here and register the driver from the host", k)
		}
	}
	for _, k := range keys(have) {
		if !got[k] {
			t.Errorf("%s is listed in sdk/%s but no longer imported: delete the line (the ledger only shrinks)", k, ledger)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func listed(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(filepath.Join("sdk", ledger))
	if err != nil {
		t.Fatalf("open %s: %v", ledger, err)
	}
	defer f.Close()
	out := map[string]bool{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if line := strings.TrimSpace(s.Text()); line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("read %s: %v", ledger, err)
	}
	return out
}
