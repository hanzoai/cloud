package code

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
)

func newStore(t *testing.T, org string) *Store {
	t.Helper()
	db, err := cloud.OrgDB(t.TempDir(), org, "", "code")
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
	if err := st.writeFile(context.Background(), repo, path, int64(len(content)), sha256Hex(content), 1, p, nil); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

func TestStoreLexicalAndSymbol(t *testing.T) {
	st := newStore(t, "code.db")
	writeFixture(t, st, "r", "greeter.go", goFixture)

	m, ok := buildFTSMatch("greet")
	if !ok {
		t.Fatal("buildFTSMatch(greet) not ok")
	}
	rows, err := st.ftsSearch(context.Background(), "r", m, 10)
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
}

// Two org files are physically separate: a search in one never sees the other's
// code. This is the org boundary — one SQLite file per org.
func TestStoreIsolation(t *testing.T) {
	dir := t.TempDir()
	adb, err := cloud.OrgDB(dir, "orgA", "", "code")
	if err != nil {
		t.Fatal(err)
	}
	a, err := openStore(adb)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	bdb, err := cloud.OrgDB(dir, "orgB", "", "code")
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

	m, _ := buildFTSMatch("Quokka")
	if rows, _ := a.ftsSearch(context.Background(), "r", m, 10); len(rows) != 0 {
		t.Fatalf("org A saw org B's symbol: %d rows", len(rows))
	}
	if syms, _ := a.symbolSearch(context.Background(), "r", "Quokka", 10); len(syms) != 0 {
		t.Fatalf("org A symbol-searched org B's def: %d", len(syms))
	}
	m2, _ := buildFTSMatch("Xylophone")
	if rows, _ := a.ftsSearch(context.Background(), "r", m2, 10); len(rows) == 0 {
		t.Fatal("org A cannot find its own symbol")
	}
}

func TestIncrementalHashAndDelete(t *testing.T) {
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
}

func TestVectorCodecAndCosine(t *testing.T) {
	v := []float32{1, -2, 3.5, 0}
	if got := decodeVec(encodeVec(v)); len(got) != len(v) {
		t.Fatalf("codec len=%d want %d", len(got), len(v))
	} else {
		for i := range v {
			if got[i] != v[i] {
				t.Fatalf("codec[%d]=%v want %v", i, got[i], v[i])
			}
		}
	}
	if c := cosine([]float32{1, 0}, []float32{1, 0}); c < 0.999 {
		t.Errorf("cosine of identical=%v want ~1", c)
	}
	if c := cosine([]float32{1, 0}, []float32{0, 1}); c != 0 {
		t.Errorf("cosine of orthogonal=%v want 0", c)
	}
	if c := cosine([]float32{1}, []float32{1, 2}); c != 0 {
		t.Errorf("cosine of mismatched dims=%v want 0", c)
	}
}
