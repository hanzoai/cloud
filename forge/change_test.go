package forge

// change_test.go pins the two reads of a comparison: the diff split into files —
// every shape git writes a file in, and the bounds on what is kept — and the wire
// both reads take, as the actor.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// aDiff is a three-dot diff in every shape a run's branch produces: an edit, a
// file added, one deleted, one renamed and edited, one renamed and nothing else,
// a binary, a name with a space, a name git had to quote, and a last line with no
// newline.
const aDiff = `diff --git a/api/server.go b/api/server.go
index 1111111..2222222 100644
--- a/api/server.go
+++ b/api/server.go
@@ -1,4 +1,5 @@
 package api
-var port = 80
+var port = 8080
+var host = "0.0.0.0"
 
 func serve() {}
diff --git a/docs/NEW.md b/docs/NEW.md
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/docs/NEW.md
@@ -0,0 +1,2 @@
+# New
+hello
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 4444444..0000000
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
diff --git a/lib/util.go b/lib/helpers.go
similarity index 90%
rename from lib/util.go
rename to lib/helpers.go
index 5555555..6666666 100644
--- a/lib/util.go
+++ b/lib/helpers.go
@@ -3,3 +3,3 @@ package lib
 func a() {}
-func b() {}
+func c() {}
 func d() {}
diff --git a/moved.txt b/kept/moved.txt
similarity index 100%
rename from moved.txt
rename to kept/moved.txt
diff --git a/logo.png b/logo.png
index 7777777..8888888 100644
Binary files a/logo.png and b/logo.png differ
diff --git a/my notes.txt b/my notes.txt
index 9999999..aaaaaaa 100644
--- a/my notes.txt	
+++ b/my notes.txt	
@@ -1 +1 @@
-draft
+final
diff --git "a/na\303\257ve \"q\".txt" "b/na\303\257ve \"q\".txt"
index bbbbbbb..ccccccc 100644
--- "a/na\303\257ve \"q\".txt"
+++ "b/na\303\257ve \"q\".txt"
@@ -1 +1 @@
-x
\ No newline at end of file
+y
\ No newline at end of file`

func TestSplit_EveryShapeOfFile(t *testing.T) {
	got, _, err := split(strings.NewReader(aDiff))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	want := []Change{
		{Path: "api/server.go", Status: "modified", Additions: 2, Deletions: 1},
		{Path: "docs/NEW.md", Status: "added", Additions: 2},
		{Path: "old.txt", Status: "deleted", Deletions: 1},
		{Path: "lib/helpers.go", From: "lib/util.go", Status: "renamed", Additions: 1, Deletions: 1},
		{Path: "kept/moved.txt", From: "moved.txt", Status: "renamed"},
		{Path: "logo.png", Status: "modified"},
		{Path: "my notes.txt", Status: "modified", Additions: 1, Deletions: 1},
		{Path: `naïve "q".txt`, Status: "modified", Additions: 1, Deletions: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("split %d files, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Path != w.Path || g.From != w.From || g.Status != w.Status ||
			g.Additions != w.Additions || g.Deletions != w.Deletions || g.Truncated {
			t.Errorf("file %d = %+v, want %+v", i, g, w)
		}
	}
	// A patch is the file's hunks and nothing before them, so a reader renders it
	// without first stripping headers that describe the file rather than change it.
	if p := got[0].Patch; p != "@@ -1,4 +1,5 @@\n package api\n-var port = 80\n+var port = 8080\n+var host = \"0.0.0.0\"\n \n func serve() {}\n" {
		t.Errorf("the edit's patch is %q", p)
	}
	if p := got[3].Patch; !strings.HasPrefix(p, "@@ -3,3 +3,3 @@ package lib\n") || !strings.Contains(p, "+func c() {}\n") {
		t.Errorf("the renamed file's edit is not its patch: %q", p)
	}
	for _, i := range []int{4, 5} {
		if got[i].Patch != "" {
			t.Errorf("%s moved no line and carries a patch: %q", got[i].Path, got[i].Patch)
		}
	}
	if p := got[7].Patch; !strings.HasSuffix(p, "+y\n\\ No newline at end of file") {
		t.Errorf("the last file's patch lost its end: %q", p)
	}
}

// An empty comparison is an empty list, not a nil one a caller renders as null.
func TestSplit_NothingChangedIsAnEmptyList(t *testing.T) {
	got, _, err := split(strings.NewReader(""))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("split(\"\") = %#v, %v; want an empty list", got, err)
	}
}

// A patch past its cap ends at the last WHOLE line that fits and says so; the
// counts are the whole file's all the same, because they are what a reader sums.
func TestSplit_ALongPatchIsCutAtALineAndCountedWhole(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,5000 @@\n")
	line := "+" + strings.Repeat("x", 99) + "\n"
	for range 5000 {
		b.WriteString(line)
	}
	got, _, err := split(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	f := got[0]
	if !f.Truncated || f.Additions != 5000 {
		t.Fatalf("truncated=%v additions=%d, want true and 5000", f.Truncated, f.Additions)
	}
	if len(f.Patch) > maxPatch || !strings.HasSuffix(f.Patch, "\n") || !strings.HasPrefix(f.Patch, "@@") {
		t.Fatalf("patch of %d bytes does not end at a whole line under %d", len(f.Patch), maxPatch)
	}
}

// Past the total cap every later file is still LISTED with its counts, carrying
// no patch and marked; a list that dropped them would read as the whole change.
func TestSplit_PastTheTotalCapFilesAreListedWithoutPatches(t *testing.T) {
	var b strings.Builder
	n := maxPatches/(maxPatch-1024) + 3
	body := strings.Repeat("+"+strings.Repeat("y", 63)+"\n", (maxPatch-1024)/64)
	for i := range n {
		name := "f" + strings.Repeat("0", 3) + string(rune('a'+i%26)) + ".txt"
		b.WriteString("diff --git a/" + name + " b/" + name + "\n@@ -0,0 +1 @@\n" + body)
	}
	got, _, err := split(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(got) != n {
		t.Fatalf("listed %d files, want every one of %d", len(got), n)
	}
	total := 0
	for _, f := range got {
		total += len(f.Patch)
		if f.Additions == 0 {
			t.Fatalf("%s lost its counts", f.Path)
		}
	}
	if total > maxPatches {
		t.Fatalf("kept %d bytes of patch, past the %d cap", total, maxPatches)
	}
	last := got[len(got)-1]
	if !last.Truncated || last.Patch != "" {
		t.Fatalf("the file past the cap = truncated %v, patch %d bytes; want marked and empty", last.Truncated, len(last.Patch))
	}
}

// A line longer than the read buffer — a minified bundle — is one line: counted
// once, and never mistaken for a header by the piece that ends it.
func TestSplit_ALineLongerThanTheBufferIsOneLine(t *testing.T) {
	long := "+" + strings.Repeat("z", 200<<10) + "diff --git a/x b/x\n"
	d := "diff --git a/app.min.js b/app.min.js\n--- a/app.min.js\n+++ b/app.min.js\n@@ -1 +1 @@\n-old\n" + long
	got, _, err := split(strings.NewReader(d))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(got) != 1 || got[0].Additions != 1 || got[0].Deletions != 1 || !got[0].Truncated {
		t.Fatalf("got %+v, want one file, one line each way, truncated", got)
	}
	if strings.Contains(got[0].Patch, "zzz") {
		t.Fatal("a cut line was kept in part")
	}
}

// A diff past its byte bound is CUT and says so: the file being read when the
// budget ran out is listed, marked, and the change reports there is more — a
// reader is shown what fits rather than a 502, and nothing reads as whole.
func TestSplit_ADiffPastTheBoundIsCutAndSaysSo(t *testing.T) {
	r := &endless{line: "+" + strings.Repeat("q", 1023) + "\n"}
	got, more, err := split(r)
	if err != nil {
		t.Fatalf("an endless diff answered %v, want a cut", err)
	}
	if !more || len(got) != 1 || !got[0].Truncated || len(got[0].Patch) > maxPatch {
		t.Fatalf("more=%v files=%d, want one cut file and more", more, len(got))
	}
}

// A diff naming more files than one read lists is cut at the bound: the files
// before it are listed whole, and the change says there are more.
func TestSplit_PastTheFileBoundTheChangeIsCut(t *testing.T) {
	var b strings.Builder
	for i := range maxChanges + 5 {
		name := "f" + strconv.Itoa(i) + ".txt"
		b.WriteString("diff --git a/" + name + " b/" + name + "\n@@ -0,0 +1 @@\n+x\n")
	}
	got, more, err := split(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if !more || len(got) != maxChanges || got[len(got)-1].Additions != 1 {
		t.Fatalf("more=%v files=%d, want %d whole files and more", more, len(got), maxChanges)
	}
	// The control: a change within the bound is whole.
	if _, more, _ := split(strings.NewReader(aDiff)); more {
		t.Fatal("a small change said it was cut")
	}
}

// endless is a diff that never ends: one file, then the same line forever.
type endless struct {
	line string
	head bool
}

func (e *endless) Read(p []byte) (int, error) {
	if !e.head {
		e.head = true
		return copy(p, "diff --git a/x b/x\n@@ -0,0 +1 @@\n"), nil
	}
	n := 0
	for n+len(e.line) <= len(p) {
		n += copy(p[n:], e.line)
	}
	if n == 0 {
		n = copy(p, e.line)
	}
	return n, nil
}

// changeStub is a repository with a run's branch compared against its base.
func changeStub(t *testing.T) *forgeStub {
	t.Helper()
	s := newStub(t)
	s.visible["z"] = []string{"hanzoai"}
	s.repos["hanzoai"] = []Repo{{Name: "cloud", FullName: "hanzoai/cloud", Branch: "main"}}
	s.compares["hanzoai/cloud@main...agent/x"] = compared{
		commits: []map[string]any{
			{"sha": sideSHA, "commit": map[string]any{"message": "Rename the helper\n\nbody", "author": map[string]any{"name": "hanzo-agent", "date": "2026-09-27T10:05:00+02:00"}}},
			{"sha": mainSHA, "commit": map[string]any{"message": "Fix the port", "author": map[string]any{"name": "hanzo-agent", "date": "2026-09-27T08:00:00Z"}}},
		},
		diff: aDiff,
	}
	return s
}

// The comparison is read as the ACTOR, through the wildcard route with the
// slashed branch whole, and asks for neither per-commit files nor signatures.
func TestCompare_IsTheActorsReadOfTheCommits(t *testing.T) {
	s := changeStub(t)
	got, more, err := s.client(t).As("z").Compare(t.Context(), "hanzoai", "cloud", "main", "agent/x")
	if err != nil || more {
		t.Fatalf("Compare: %v, more=%v", err, more)
	}
	if len(got) != 2 || got[0].SHA != sideSHA || got[0].Author != "hanzo-agent" || got[0].Message != "Rename the helper\n\nbody" {
		t.Fatalf("commits = %+v, want the forge's two, newest first", got)
	}
	if !got[0].Date.Equal(time.Date(2026, 9, 27, 8, 5, 0, 0, time.UTC)) {
		t.Fatalf("date = %v", got[0].Date)
	}
	for _, r := range s.got.all() {
		if r.Header.Get("Sudo") != "z" {
			t.Fatalf("%s carried Sudo=%q", r.URL.Path, r.Header.Get("Sudo"))
		}
		if r.URL.Path != "/v1/repos/hanzoai/cloud/compare/main...agent/x" {
			t.Fatalf("asked %s", r.URL.Path)
		}
		if r.URL.Query().Get("files") != "false" || r.URL.Query().Get("verification") != "false" {
			t.Fatalf("query %q asks for more than the commits", r.URL.RawQuery)
		}
	}
}

func TestChanges_IsTheComparisonsDiffSplit(t *testing.T) {
	s := changeStub(t)
	got, more, err := s.client(t).As("z").Changes(t.Context(), "hanzoai", "cloud", "main", "agent/x")
	if err != nil || more {
		t.Fatalf("Changes: %v, more=%v", err, more)
	}
	if len(got) != 8 || got[3].Status != "renamed" || got[3].From != "lib/util.go" {
		t.Fatalf("changes = %+v", got)
	}
	for _, r := range s.got.all() {
		if r.URL.Query().Get("output") != "diff" || r.Header.Get("Sudo") != "z" {
			t.Fatalf("%s?%s as %q is not the actor's diff", r.URL.Path, r.URL.RawQuery, r.Header.Get("Sudo"))
		}
	}
}

// A comparison the actor cannot see is refused, and a ref that is a range or a
// flag never reaches the wire.
func TestCompare_RefusesWhatTheActorCannotSeeAndWhatIsNotARef(t *testing.T) {
	s := changeStub(t)
	s.visible["y"] = nil
	if _, _, err := s.client(t).As("y").Compare(t.Context(), "hanzoai", "cloud", "main", "agent/x"); !errors.Is(err, ErrUnknownActor) {
		t.Fatalf("an outsider's comparison answered %v, want ErrUnknownActor", err)
	}
	before := len(s.got.all())
	for _, bad := range []string{"main..x", "-x", "a?b", ""} {
		if _, _, err := s.client(t).As("z").Changes(t.Context(), "hanzoai", "cloud", "main", bad); err == nil {
			t.Fatalf("head %q was accepted", bad)
		}
	}
	if len(s.got.all()) != before {
		t.Fatal("a bad ref reached the forge")
	}
}

// A comparison of more commits than one read lists keeps the newest and says
// there are more; the forge's list past the bound is left unread.
func TestCompare_KeepsTheNewestAndSaysThereAreMore(t *testing.T) {
	s := changeStub(t)
	var many []map[string]any
	for i := range maxCommits + 50 {
		many = append(many, map[string]any{"sha": fmt.Sprintf("%040d", i),
			"commit": map[string]any{"message": "c", "author": map[string]any{"name": "a", "date": "2026-09-27T08:00:00Z"}}})
	}
	s.compares["hanzoai/cloud@main...agent/many"] = compared{commits: many}
	got, more, err := s.client(t).As("z").Compare(t.Context(), "hanzoai", "cloud", "main", "agent/many")
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !more || len(got) != maxCommits || got[0].SHA != fmt.Sprintf("%040d", 0) {
		t.Fatalf("more=%v commits=%d first=%s, want the newest %d and more", more, len(got), got[0].SHA, maxCommits)
	}
}
