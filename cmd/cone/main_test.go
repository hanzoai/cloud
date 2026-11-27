package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// tree makes a git repository in a temp directory holding files, commits it as
// the base, and chdirs there for the test.
func tree(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		f := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	git(t, "init", "-q")
	git(t, "add", "-A")
	git(t, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
	git(t, "tag", "base")
}

func git(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// change rewrites paths, commits, and returns what `dirs base` prints.
func change(t *testing.T, paths ...string) []string {
	t.Helper()
	files := map[string]string{}
	for _, p := range paths {
		files[p] = "changed\n"
	}
	return rewrite(t, files)
}

// rewrite writes each file's new content, commits, and returns what `dirs base`
// prints.
func rewrite(t *testing.T, files map[string]string) []string {
	t.Helper()
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, "add", "-A")
	git(t, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "change")
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := dirs("base")
	w.Close()
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return strings.Fields(b.String())
}

var base = map[string]string{
	"go.mod":                      "module example.com/m\n\ngo 1.27\n\nrequire (\n\texample.com/x v1.0.0\n\texample.com/y v1.0.0\n)\n",
	"go.sum":                      "example.com/x v1.0.0 h1:x=\n",
	"root.go":                     "package m\n",
	"apps/pay/pay.go":             "package pay\n",
	"apps/pay/testdata/card.json": "{}\n",
	"apps/pay/sub/x.go":           "package sub\n",
	"apps/doc/doc.go":             "package doc\n\nimport _ \"embed\"\n\n//go:embed page/index.html\nvar page string\n",
	"apps/doc/page/index.html":    "<p>\n",
	"apps/blk/blk.go":             "package blk\n\nimport _ \"embed\"\n\nvar (\n\t//go:embed tpl/a.txt\n\ta string\n)\n",
	"apps/blk/tpl/a.txt":          "a\n",
	"apps/tst/tst.go":             "package tst\n",
	"apps/tst/tst_test.go":        "package tst\n\nimport _ \"embed\"\n\n//go:embed fix/x.json\nvar x string\n",
	"apps/tst/fix/x.json":         "{}\n",
	"apps/pay/LLM.md":             "# pay\n",
	"apps/pay/shim.c":             "int x;\n",
	"manifest/apps.go":            "package manifest\n\nvar Apps = []App{\n\t{Name: \"pay\"},\n\t{Name: \"zen\", Coresident: true},\n}\n",
	".hanzo/workflows/dev.yml":    "on: push\n",
	"README.md":                   "# m\n",
}

func TestDirs(t *testing.T) {
	for _, c := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"a go file is its own directory's package", []string{"apps/pay/pay.go"}, []string{"apps/pay"}},
		{"a nested package is not its parent", []string{"apps/pay/sub/x.go"}, []string{"apps/pay/sub"}},
		{"embedded data reaches the package that embeds it", []string{"apps/doc/page/index.html"}, []string{"apps/doc"}},
		{"testdata reaches the package whose tests read it", []string{"apps/pay/testdata/card.json"}, []string{"apps/pay"}},
		{"an indented embed in a var block is an embed", []string{"apps/blk/tpl/a.txt"}, []string{"apps/blk"}},
		{"a test's embed reaches its package", []string{"apps/tst/fix/x.json"}, []string{"apps/tst"}},
		{"a workflow reaches no package", []string{".hanzo/workflows/dev.yml"}, nil},
		{"prose at the root reaches no package", []string{"README.md"}, nil},
		{"prose beside a package reaches nothing", []string{"apps/pay/LLM.md"}, nil},
		{"a cgo source is its package's", []string{"apps/pay/shim.c"}, []string{"apps/pay"}},
		{"the root package is a package", []string{"root.go"}, []string{"."}},
		{"the Dockerfile moves everything", []string{"Dockerfile"}, []string{"all"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree(t, base)
			if got := change(t, c.paths...); !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Fatalf("dirs = %v, want %v", got, c.want)
			}
		})
	}
}

// TestModules: go.mod moves the modules whose selected source moved, and only
// a new go line moves everything.
func TestModules(t *testing.T) {
	mod := func(body string) string { return "module example.com/m\n\n" + body }
	for _, c := range []struct {
		name string
		mod  string
		want []string
	}{
		{"a bumped requirement is that module", mod("go 1.27\n\nrequire (\n\texample.com/x v1.0.1\n\texample.com/y v1.0.0\n)\n"), []string{"mod:example.com/x"}},
		{"an added and a dropped requirement are both moved", mod("go 1.27\n\nrequire (\n\texample.com/x v1.0.0\n\texample.com/z v1.0.0\n)\n"), []string{"mod:example.com/y", "mod:example.com/z"}},
		{"a replacement is its module", mod("go 1.27\n\nrequire (\n\texample.com/x v1.0.0\n\texample.com/y v1.0.0\n)\n\nreplace example.com/y => example.com/fork v1.2.3\n"), []string{"mod:example.com/y"}},
		{"a new go line moves everything", mod("go 1.26\n\nrequire (\n\texample.com/x v1.0.0\n\texample.com/y v1.0.0\n)\n"), []string{"all"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tree(t, base)
			if got := rewrite(t, map[string]string{"go.mod": c.mod, "apps/pay/pay.go": "package pay\n\n// moved\n"}); !reflect.DeepEqual(got, wantWith(c.want)) {
				t.Fatalf("dirs = %v, want %v", got, wantWith(c.want))
			}
		})
	}
	t.Run("go.sum alone selects nothing", func(t *testing.T) {
		tree(t, base)
		if got := change(t, "go.sum"); len(got) != 0 {
			t.Fatalf("dirs = %v, want nothing", got)
		}
	})
}

// wantWith is a module result beside the one package every case also touches,
// unless the result is everything.
func wantWith(mods []string) []string {
	if len(mods) == 1 && mods[0] == "all" {
		return mods
	}
	return append([]string{"apps/pay"}, mods...)
}

// TestApps: a newly spawned app is a binary the base does not have; a newly
// coresident one is not, and the manifest stays an ordinary package.
func TestApps(t *testing.T) {
	tree(t, base)
	got := rewrite(t, map[string]string{"manifest/apps.go": "package manifest\n\nvar Apps = []App{\n\t{Name: \"pay\"},\n\t{Name: \"zen\", Coresident: true},\n\t{Name: \"doc\"},\n\t{Name: \"kv\", Coresident: true},\n}\n"})
	if want := []string{"app:doc", "manifest"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dirs = %v, want %v", got, want)
	}
}

func TestIgnored(t *testing.T) {
	for d, want := range map[string]bool{
		".":                 false,
		"apps/pay":          false,
		"apps/pay/testdata": true,
		".hanzo/workflows":  true,
		"apps/_old":         true,
	} {
		if got := ignored(d); got != want {
			t.Errorf("ignored(%q) = %v, want %v", d, got, want)
		}
	}
}
