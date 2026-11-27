package code

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
)

func newStore(t *testing.T, org string) *Store {
	t.Helper()
	db, err := cloud.OrgDB(t.TempDir(), principal.MustOrgNamespace(org, ""), "code")
	if err != nil {
		t.Fatalf("OrgDB: %v", err)
	}
	st, err := openStore(db)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func writeFixture(t *testing.T, st *Store, repo, path, content string) {
	t.Helper()
	p := Parse(path, content)
	if err := st.writeFile(context.Background(), repo, path, int64(len(content)), sha256Hex(content), 1, p, testSpace, nil); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

func TestStoreLexicalAndSymbol(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t, "code.db")
		writeFixture(t, st, "r", "greeter.go", goFixture)

		m := ftsTerms("greet")
		if len(m) == 0 {
			t.Fatal("ftsTerms(greet) is empty")
		}
		rows, err := st.ftsSearch(context.Background(), "r", m, false, 10)
		if err != nil {
			t.Fatalf("ftsSearch: %v", err)
		}
		if len(rows) == 0 {
			t.Fatal("lexical search for greet returned nothing")
		}

		syms, err := st.symbolSearch(context.Background(), "r", "Hello", 10)
		if err != nil {
			t.Fatalf("symbolSearch: %v", err)
		}
		if _, ok := findSym(syms, "Hello"); !ok {
			t.Fatalf("symbol search for Hello returned %+v", syms)
		}
	})
}

// Two org files are physically separate: a search in one never sees the other's
// code. This is the org boundary — one SQLite file per org.
func TestStoreIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		adb, err := cloud.OrgDB(dir, principal.MustOrgNamespace("orgA", ""), "code")
		if err != nil {
			t.Fatal(err)
		}
		a, err := openStore(adb)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = a.Close() }()
		bdb, err := cloud.OrgDB(dir, principal.MustOrgNamespace("orgB", ""), "code")
		if err != nil {
			t.Fatal(err)
		}
		b, err := openStore(bdb)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = b.Close() }()

		// Disjoint token sets so a hit can only come from the store's OWN file.
		writeFixture(t, a, "r", "a.go", "package a\n\nfunc Xylophone() {}\n")
		writeFixture(t, b, "r", "b.go", "package b\n\nfunc Quokka() {}\n")

		m := ftsTerms("Quokka")
		if rows, _ := a.ftsSearch(context.Background(), "r", m, false, 10); len(rows) != 0 {
			t.Fatalf("org A saw org B's symbol: %d rows", len(rows))
		}
		if syms, _ := a.symbolSearch(context.Background(), "r", "Quokka", 10); len(syms) != 0 {
			t.Fatalf("org A symbol-searched org B's def: %d", len(syms))
		}
		m2 := ftsTerms("Xylophone")
		if rows, _ := a.ftsSearch(context.Background(), "r", m2, false, 10); len(rows) == 0 {
			t.Fatal("org A cannot find its own symbol")
		}
	})
}

func TestIncrementalHashAndDelete(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t, "code.db")
		writeFixture(t, st, "r", "greeter.go", goFixture)

		h, err := st.fileHash(context.Background(), "r", "greeter.go")
		if err != nil || h != sha256Hex(goFixture) {
			t.Fatalf("fileHash=%q err=%v want %q", h, err, sha256Hex(goFixture))
		}
		if miss, _ := st.fileHash(context.Background(), "r", "nope.go"); miss != "" {
			t.Fatalf("unknown file hash=%q want empty", miss)
		}

		// deleteFile removes the file's artifacts across every tier.
		if err := st.deleteFile(context.Background(), "r", "greeter.go"); err != nil {
			t.Fatalf("deleteFile: %v", err)
		}
		if syms, _ := st.symbolSearch(context.Background(), "r", "Hello", 10); len(syms) != 0 {
			t.Fatalf("symbols survived deleteFile: %+v", syms)
		}
		nf, ns, nc, _ := st.stats(context.Background(), "r")
		if nf|ns|nc != 0 {
			t.Fatalf("stats after delete: files=%d symbols=%d chunks=%d want 0", nf, ns, nc)
		}
	})
}

// TestNearestReportsItsCap: the semantic tier reads at most its cap and SAYS
// when rows remained past it — the answer is then the best of what it read, and a
// caller that was not told would take it for the best of the index. A vector of
// another dimension (another model's, or one model's at another size) is never
// written beside the index's, so it neither errors a read nor counts against the
// cap.
func TestNearestReportsItsCap(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := newStore(t, "acme")
		p := Parse("greeter.go", goFixture)
		n := len(p.Chunks)
		if n < 3 {
			t.Fatalf("fixture has %d chunks, want at least 3", n)
		}
		// cos([1,0], [1,i]) falls as i grows, so chunk 0 is the best match.
		vecs := make([][]float32, n)
		for i := range vecs {
			vecs[i] = []float32{1, float32(i)}
		}
		if err := st.writeFile(ctx, "r", "greeter.go", int64(len(goFixture)), sha256Hex(goFixture), 1, p, testSpace, vecs); err != nil {
			t.Fatal(err)
		}
		other := Parse("other.go", goFixture)
		wide := make([][]float32, len(other.Chunks))
		for i := range wide {
			wide[i] = []float32{1, 0, 0}
		}
		err := st.writeFile(ctx, "r", "other.go", int64(len(goFixture)), sha256Hex(goFixture)+"x", 1, other, testSpace, wide)
		if !errors.Is(err, vec.ErrMixed) || !strings.Contains(err.Error(), "dimensions") {
			t.Fatalf("a file of another dimension must be refused, got %v", err)
		}
		if h, err := st.fileHash(ctx, "r", "other.go"); err != nil || h != "" {
			t.Fatalf("a refused write left the file behind: hash %q err %v", h, err)
		}
		q := []float32{1, 0}

		top, capped, err := st.nearest(ctx, "r", q, testSpace, n, n)
		if err != nil || capped || len(top) != n || top[0].Score != 1 {
			t.Fatalf("a cap the index fits under: capped=%v err=%v top=%v", capped, err, top)
		}
		for i := 1; i < len(top); i++ {
			if top[i].Score > top[i-1].Score {
				t.Fatalf("not best first: %v", top)
			}
		}
		if _, capped, err := st.nearest(ctx, "", q, testSpace, 1, n-1); err != nil || !capped {
			t.Fatalf("a cap below the index must report itself: capped=%v err=%v", capped, err)
		}
		if top, capped, err := st.nearest(ctx, "", q, testSpace, 2, n+1); err != nil || capped || len(top) != 2 {
			t.Fatalf("with every vector read: capped=%v err=%v top=%v", capped, err, top)
		}
	})
}

// pathsOf is the files whose chunks a substring search returned, sorted.
func pathsOf(rows []chunkRow) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Path] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// The substring index returns the same chunks on both engines: a whole token, a
// part of one anywhere in it, a subword of an identifier, any case, and terms
// that all must hold or any may.
func TestSubstringSearchFindsTheSameChunksOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t, "acme")
		writeFixture(t, st, "r", "greeter.go", goFixture)
		writeFixture(t, st, "r", "users.ts", tsFixture)
		writeFixture(t, st, "r", "animal.py", pyFixture)
		ctx := context.Background()
		for _, c := range []struct {
			name  string
			terms []string
			all   bool
			want  []string
		}{
			{"a whole token", []string{"greet"}, false, []string{"greeter.go"}},
			{"a part of a token", []string{"reeter"}, false, []string{"greeter.go"}},
			{"the middle of an identifier", []string{"etuse"}, false, []string{"users.ts"}},
			{"a subword of an identifier", []string{"sound"}, false, []string{"animal.py"}},
			{"any case", []string{"GetUser"}, false, []string{"users.ts"}},
			{"every term", []string{"getuser", "fetchuser"}, true, []string{"users.ts"}},
			{"every term, one absent", []string{"getuser", "greeter"}, true, nil},
			{"any term", []string{"getuser", "greeter"}, false, []string{"greeter.go", "users.ts"}},
			{"an operator of three characters", []string{":= "}, false, nil},
			{"too short", []string{"ge"}, false, nil},
			{"nothing like it", []string{"zebra"}, false, nil},
			{"a hostile term", []string{`"`, `a'b"c`, `%_\`, "a\x00bcd"}, false, nil},
		} {
			rows, err := st.ftsSearch(ctx, "r", c.terms, c.all, 20)
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
				continue
			}
			if got := pathsOf(rows); !slices.Equal(got, c.want) && (len(got) != 0 || len(c.want) != 0) {
				t.Errorf("%s: %q found %v, want %v", c.name, c.terms, got, c.want)
			}
		}
		// The repo narrows it, by any spelling the repo was indexed under.
		if rows, _ := st.ftsSearch(ctx, "owner/r", []string{"greet"}, false, 20); len(rows) == 0 {
			t.Error("a repo named with its owner did not find the repo indexed bare")
		}
		if rows, _ := st.ftsSearch(ctx, "zzz", []string{"greet"}, false, 20); len(rows) != 0 {
			t.Errorf("a repo that was never indexed found %d chunks", len(rows))
		}
		if rows, _ := st.ftsSearch(ctx, "R", []string{"greet"}, false, 20); len(rows) != 0 {
			t.Errorf("a repo's name is case-sensitive and found %d chunks", len(rows))
		}
		if rows, _ := st.ftsSearch(ctx, "", []string{"greet"}, false, 20); len(rows) == 0 {
			t.Error("no repo searches every repo and found nothing")
		}
	})
}

// A re-index replaces what a file held, in the substring index as everywhere: the old
// words stop finding it and the new ones start.
func TestReindexingAFileReplacesItsSubstrings(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := newStore(t, "acme")
		writeFixture(t, st, "r", "a.go", "package a\n\nfunc Xylophone() {}\n")
		writeFixture(t, st, "r", "a.go", "package a\n\nfunc Quokka() {}\n")
		ctx := context.Background()
		if rows, _ := st.ftsSearch(ctx, "r", []string{"xylophone"}, false, 10); len(rows) != 0 {
			t.Fatalf("the old text is still found: %d chunks", len(rows))
		}
		if rows, _ := st.ftsSearch(ctx, "r", []string{"quokka"}, false, 10); len(rows) == 0 {
			t.Fatal("the new text is not found")
		}
	})
}

// A NUL, or bytes that are not UTF-8, cannot be kept in a column of text on hanzo-sql
// and is not source. The index op refuses the call and names the file; a push, which is
// background enrichment, indexes the rest.
func TestAFileThatIsNotTextIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := newTestApp(t)
		// Over the wire only a NUL can arrive: JSON carries text, and encoding/json
		// would have made the bytes that are not UTF-8 into U+FFFD before they left.
		body := indexIn{Repo: "svc", Files: []fileInput{{Path: "ok.go", Content: goFixture}, {Path: "bad.bin", Content: "a\x00b"}}}
		status, b := doAuth(t, app, http.MethodPost, "/v1/code/index", "acme", body)
		if status != http.StatusBadRequest || !strings.Contains(string(b), "bad.bin") {
			t.Fatalf("a file with a NUL: status %d, want 400 naming the file: %s", status, b)
		}
		tree, err := s.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		if n, _, _, _ := tree.stats(context.Background(), "svc"); n != 0 {
			t.Fatalf("a refused call still indexed %d files", n)
		}

		mounted = s
		t.Cleanup(func() { mounted = nil })
		res, err := IndexFiles(context.Background(), "acme", account.Account{}, "", "svc", []File{
			{Path: "ok.go", Content: goFixture}, {Path: "bad.bin", Content: "a\x00b"}, {Path: "latin.c", Content: "caf\xe9"},
		})
		if err != nil || res.Indexed != 1 {
			t.Fatalf("a push with two binary files indexed %d (%v), want the one text file", res.Indexed, err)
		}
	})
}
