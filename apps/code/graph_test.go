// Copyright © 2026 Hanzo AI. MIT License.

package code

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

const repoKey = "code:p/r#"

// tree is a small repo with one of everything the graph reads: two Go modules,
// a TypeScript module graph, a CODEOWNERS and a decision record, beside Go the
// go tool does not build.
var tree = []client.IndexFile{
	{Path: "go.mod", Content: "module example.com/r\n\ngo 1.27\n"},
	{Path: "cmd/r/main.go", Content: "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/r/store\"\n\t\"example.com/other\"\n)\n\nfunc main() { fmt.Println(store.Open()) }\n"},
	{Path: "store/store.go", Content: "package store\n\ntype Store struct{}\n\nfunc (s *Store) Close() error { return nil }\n\nfunc Open() *Store { return &Store{} }\n"},
	{Path: "store/testdata/x.go", Content: "package x\n\nimport \"example.com/r/store\"\n\nfunc Fixture() {}\n"},
	{Path: "vendor/example.com/other/other.go", Content: "package other\n\nfunc Other() {}\n"},
	{Path: "tools/go.mod", Content: "module example.com/r/tools\n"},
	{Path: "tools/gen.go", Content: "package tools\n\nimport \"example.com/r/store\"\n"},
	{Path: "web/app.ts", Content: "import { util } from './lib/util.js'\nimport './side'\nexport * from '../web/lib'\nconst x = require('./missing')\nexport function app() {}\n"},
	{Path: "web/lib/util.ts", Content: "export const util = () => 1\n"},
	{Path: "web/lib/index.ts", Content: "export {}\n"},
	{Path: "web/side.tsx", Content: "export default 1\n"},
	{Path: ".github/CODEOWNERS", Content: "# who owns what\n*       @acme/core\n/store/ @z z@acme.test # the store\n[Docs]\n/docs/\n"},
	{Path: "CODEOWNERS", Content: "* @ignored\n"},
	{Path: "docs/adr/0001-storage.md", Content: "# 1. Storage\n\n## Status\n\nAccepted\n\n## Decision\n\nWe keep every org in\none SQLite file.\n\n## Consequences\n\nNone.\n"},
	{Path: "docs/adr/README.md", Content: "# Decisions\n"},
	{Path: "docs/adr/template.md", Content: "# Title\n\n## Decision\n\nTBD\n"},
}

// stated is everything a tree states, which fits one restatement.
func stated(t *testing.T, scope string, files []client.IndexFile) []client.Stated {
	t.Helper()
	out, whole := statements(scope, files)
	if !whole {
		t.Fatalf("%s states more than one restatement files", scope)
	}
	return out
}

func said(stated []client.Stated) []string {
	var out []string
	for _, s := range stated {
		out = append(out, strings.TrimPrefix(s.Entity, repoKey)+" "+s.Relation+" "+strings.TrimPrefix(s.Value, repoKey))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// THE FOUR RELATIONS, READ OFF ONE TREE. Everything is keyed under the repo, an
// import that leaves the repo states nothing, and a file the go tool does not
// build is not a package.
func TestATreeStatesItsCode(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		got := said(stated(t, repoKey, tree))
		for _, want := range []string{
			"cmd/r depends_on store",
			"tools depends_on store",
			"web/app.ts depends_on web/lib/util.ts",
			"web/app.ts depends_on web/side.tsx",
			"web/app.ts depends_on web/lib/index.ts",
			"store/store.go defines store/store.go::Store",
			"store/store.go defines store/store.go::Store.Close",
			"store/store.go defines store/store.go::Open",
			"web/app.ts defines web/app.ts::app",
			"@acme/core owner_of *",
			"@z owner_of store",
			"z@acme.test owner_of store",
			"docs/adr/0001-storage.md decides We keep every org in one SQLite file.",
		} {
			if !slices.Contains(got, want) {
				t.Errorf("missing %q", want)
			}
		}
		for _, s := range got {
			for _, never := range []string{"other", "fmt", "testdata", "missing", "Fixture", "vendor", "@ignored", "README", "template", "TBD", "docs owner_of"} {
				if strings.Contains(s, never) {
					t.Errorf("stated %q", s)
				}
			}
		}
		for _, s := range stated(t, repoKey, tree) {
			if s.Evidence == "" {
				t.Errorf("%s %s %s names no evidence", s.Entity, s.Relation, s.Value)
			}
		}
	})
}

// The same tree states the same things: a restatement of one commit is one
// restatement however often it is read.
func TestATreeStatesTheSameThingEveryTime(t *testing.T) {
	if a, b := stated(t, repoKey, tree), stated(t, repoKey, tree); !slices.Equal(a, b) {
		t.Fatal("two reads of one tree disagree")
	}
}

// A TREE THAT STATES TOO MUCH IS NOT RESTATED. One small file can define more
// symbols than one restatement files, so the tree is refused whole, before the
// graph is asked, and nothing past the bound is ever built.
func TestATreeThatStatesTooMuchIsRefused(t *testing.T) {
	var src strings.Builder
	src.WriteString("package big\n")
	for i := range client.RestateMax + 1 {
		fmt.Fprintf(&src, "func F%d() {}\n", i)
	}
	big := []client.IndexFile{{Path: "big/big.go", Content: src.String()}}
	if out, whole := statements(repoKey, big); whole || out != nil {
		t.Fatalf("statements over the bound = %d, %v; want none and not whole", len(out), whole)
	}
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	_, err := planeGraph(ctx, &client.Tree{Repo: "r", At: "2026-01-02T03:04:05Z", Source: "s", Files: big})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("planeGraph over the bound = %v, want 413", err)
	}
}

// A decision is its Decision section — MADR calls it Decision Outcome — else its
// title, never text inside a code fence, and never more than a graph value holds.
func TestADecisionIsItsDecisionSection(t *testing.T) {
	for md, want := range map[string]string{
		"# Use Base\n\n## Context\n\nx\n\n## Decision Outcome\n\nChosen: Base.\n": "Chosen: Base.",
		"# Use Base\n\n## Context\n\n```sh\n## Decision\n```\n\nx\n":              "Use Base",
		"no headings at all\n": "",
		"# Title\n\n## Decision\n\n" + strings.Repeat("é", valueMax) + "\n": strings.Repeat("é", valueMax/2),
	} {
		if got := decided(md); got != want {
			t.Errorf("decided(%.40q) = %.40q, want %.40q", md, got, want)
		}
	}
}

// The op states its tenant on the call, never in the argument, and names the
// commit it restates.
func TestTheGraphOpRefusesAnUnidentifiedTree(t *testing.T) {
	if _, err := planeGraph(context.Background(), &client.Tree{Repo: "r", At: "2026-01-01T00:00:00Z", Source: "s"}); err == nil {
		t.Error("a tree with no org on the call was accepted")
	}
}

// A deployment that hosts no graph restates nothing and fails nothing, so the
// push that reached it is not retried for a plane that is not there.
func TestTheGraphOpIsInertWithoutAGraph(t *testing.T) {
	dir, err := os.MkdirTemp("", "codeg")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(func() {
		plane.Unbind()
		_ = os.RemoveAll(dir)
	})
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme"})
	out, err := planeGraph(ctx, &client.Tree{Repo: "r", At: "2026-01-02T03:04:05Z", Source: "s", Files: tree})
	if err != nil || out.Begun != 0 || out.Ended != 0 {
		t.Fatalf("planeGraph with no graph = %+v, %v; want an empty restatement", out, err)
	}
}

// THE OP RESTATES ONE REPO FOR ITS CALLER: the scope is the repo's key under the
// caller's project — none for the default — the commit's instant and address
// pass through as given, and the restatement speaks for the four relations a
// tree states and no other. The graph plane is a stand-in declared under the
// real op id, so the call resolves as it does against the live plane.
func TestTheGraphOpRestatesTheRepoForItsCaller(t *testing.T) {
	// A short run dir: a unix socket path is capped near 104 bytes.
	dir, err := os.MkdirTemp("", "codeg")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(func() {
		cloud.ResetPlane()
		plane.Unbind()
		_ = os.RemoveAll(dir)
	})

	var got client.Restate
	var org string
	cloud.Plane().Post("/graph/restate", func(ctx context.Context, in *client.Restate) (*client.Restated, error) {
		got, org = *in, cloud.Who(ctx).Org
		return &client.Restated{Begun: len(in.Stated)}, nil
	}, zip.WithOperationID(client.GraphRestate))
	stop, err := cloud.ServePlane("graph", luxlog.New("test"))
	if err != nil {
		t.Fatalf("serve graph plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })

	const at, source = "2026-01-02T03:04:05Z", "https://git.example/acme/p/r/commit/abc"
	for project, scope := range map[string]string{"p": repoKey, "default": "code:r#", "": "code:r#"} {
		ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", Project: project})
		out, err := planeGraph(ctx, &client.Tree{Repo: "r", At: at, Source: source, Files: tree})
		if err != nil {
			t.Fatalf("project %q: %v", project, err)
		}
		want := stated(t, scope, tree)
		if org != "acme" || got.Scope != scope || got.At != at || got.Source != source ||
			!slices.Equal(got.Relations, relations) || !slices.Equal(got.Stated, want) || out.Begun != len(want) {
			t.Errorf("project %q restated %q %v at %s from %s for %q, want %q at %s from %s for acme",
				project, got.Scope, got.Relations, got.At, got.Source, org, scope, at, source)
		}
	}
}
