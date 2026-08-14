package forge

// tree_test.go pins the two reads a consumer of code makes, at the WIRE — which
// is where their properties live. A tree reader that returns the right files
// while calling the archive route without a Sudo header has still failed, and so
// has one that returns a file whose path leaves the repository.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// member is one entry of a test archive — tars own word for it. Typeflag zero
// means a regular file; the cases that need a directory or a symlink say so.
type member struct {
	name string
	body string
	flag byte
	size int64 // overrides len(body) — for the truncation case, which needs a
	// header claiming more than the writer produces is worth writing
}

// archiveOf builds the tar.gz the forge's /archive route serves, the way `git
// archive --prefix=<repo>/` does: every entry under one directory named for the
// repository, and that directory FIRST. A test that wants the un-prefixed shape
// passes an empty root.
func archiveOf(t *testing.T, root string, members ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	write := func(e member) {
		t.Helper()
		size := e.size
		if size == 0 {
			size = int64(len(e.body))
		}
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: size, Typeflag: e.flag}
		if e.flag == tar.TypeDir || e.flag == tar.TypeSymlink {
			hdr.Size = 0
		}
		if e.flag == tar.TypeSymlink {
			hdr.Linkname = e.body
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", e.name, err)
		}
		if hdr.Size > 0 {
			body := e.body
			if int64(len(body)) < hdr.Size {
				body += strings.Repeat("x", int(hdr.Size)-len(body))
			}
			if _, err := tw.Write([]byte(body[:hdr.Size])); err != nil {
				t.Fatalf("tar body %s: %v", e.name, err)
			}
		}
	}
	if root != "" {
		write(member{name: root + "/", flag: tar.TypeDir})
		for i := range members {
			members[i].name = root + "/" + members[i].name
		}
	}
	for _, e := range members {
		write(e)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

const (
	mainSHA = "9c955a4710000000000000000000000000000000"
	sideSHA = "1122334455667788990011223344556677889900"
)

// codeStub is the stub with one repository the actor can see, one commit, and
// one archive at it.
func codeStub(t *testing.T, tgz []byte) *forgeStub {
	t.Helper()
	s := newStub(t)
	s.visible["z"] = []string{"hanzoai"}
	s.repos["hanzoai"] = []Repo{{Name: "cloud", FullName: "hanzoai/cloud", Branch: "main"}}
	s.refs["hanzoai/cloud@HEAD"] = mainSHA
	s.refs["hanzoai/cloud@main"] = mainSHA
	s.refs["hanzoai/cloud@v1.2.3"] = mainSHA
	s.refs["hanzoai/cloud@"+mainSHA] = mainSHA
	s.refs["hanzoai/cloud@agent/x"] = sideSHA
	s.trees["hanzoai/cloud@"+mainSHA] = tgz
	s.trees["hanzoai/cloud@"+sideSHA] = tgz
	return s
}

// ── resolve ──────────────────────────────────────────────────────────────────

// TestResolve_EveryShapeOfRef proves one call answers all four things a caller
// may name. The default branch is asked for as HEAD rather than by reading the
// repository first, which is the round trip this saves on every lsp request.
func TestResolve_EveryShapeOfRef(t *testing.T) {
	s := codeStub(t, archiveOf(t, "cloud", member{name: "a.go", body: "package p\n"}))
	c := s.client(t).As("z")

	for _, tc := range []struct{ name, ref, want string }{
		{"empty is the default branch", "", mainSHA},
		{"a branch", "main", mainSHA},
		{"a tag", "v1.2.3", mainSHA},
		{"a sha", mainSHA, mainSHA},
		{"a slashed branch", "agent/x", sideSHA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Resolve(t.Context(), "hanzoai", "cloud", tc.ref)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.ref, err)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

// TestResolve_SlashedRefTakesTheWildcardRoute fixes the routing fact the whole
// slashed case rests on: /git/commits/{sha} is ONE segment, so a name carrying a
// slash has to be asked for at /branches/*, and it has to travel UNESCAPED —
// "agent%2Fx" is a branch nobody has.
func TestResolve_SlashedRefTakesTheWildcardRoute(t *testing.T) {
	s := codeStub(t, nil)
	if _, err := s.client(t).As("z").Resolve(t.Context(), "hanzoai", "cloud", "agent/x"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var asked []string
	for _, r := range s.got.all() {
		asked = append(asked, r.URL.Path)
	}
	want := "/v1/repos/hanzoai/cloud/branches/agent/x"
	for _, p := range asked {
		if p == want {
			return
		}
		if strings.Contains(p, "%2F") || strings.Contains(p, "%2f") {
			t.Fatalf("the branch was escaped into one segment: %s", p)
		}
	}
	t.Fatalf("paths = %v, want a request to %s", asked, want)
}

// TestResolve_IsSudoed proves the resolve carries the actor. Unsudoed it would
// be the machine — a site administrator — so every repository on the forge would
// resolve for every caller.
func TestResolve_IsSudoed(t *testing.T) {
	s := codeStub(t, nil)
	if _, err := s.client(t).As("z").Resolve(t.Context(), "hanzoai", "cloud", "main"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, r := range s.got.all() {
		if r.Header.Get("Sudo") != "z" {
			t.Fatalf("%s carried Sudo=%q, want z", r.URL.Path, r.Header.Get("Sudo"))
		}
	}
}

// TestResolve_UnscopedRefuses proves an unscoped client resolves nothing. The
// refusal is the point: falling back to the machine identity here would resolve
// commits in repositories the caller cannot open.
func TestResolve_UnscopedRefuses(t *testing.T) {
	s := codeStub(t, nil)
	if _, err := s.client(t).Resolve(t.Context(), "hanzoai", "cloud", "main"); !errors.Is(err, ErrNoActor) {
		t.Fatalf("err = %v, want ErrNoActor", err)
	}
	if n := len(s.got.all()); n != 0 {
		t.Fatalf("%d request(s) reached the forge unscoped", n)
	}
}

// TestResolve_KeepsTheCommitCheap pins the query that keeps a per-request
// resolve small: the defaults attach the commit's diff, which for a merge is
// megabytes on a call apps/lsp makes on every hover.
func TestResolve_KeepsTheCommitCheap(t *testing.T) {
	s := codeStub(t, nil)
	if _, err := s.client(t).As("z").Resolve(t.Context(), "hanzoai", "cloud", "main"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, r := range s.got.all() {
		if !strings.Contains(r.URL.Path, "/git/commits/") {
			continue
		}
		for _, off := range []string{"stat", "verification", "files"} {
			if r.URL.Query().Get(off) != "false" {
				t.Fatalf("%s=%q, want false", off, r.URL.Query().Get(off))
			}
		}
		return
	}
	t.Fatal("no commit read reached the forge")
}

// TestResolve_RefusesUnaddressableRefs proves a ref is narrowed BEFORE it is
// interpolated into a URL path. A value bearing "?" or ".." addresses a
// different endpoint than the call site wrote, so no call site can be the one
// that forgot.
func TestResolve_RefusesUnaddressableRefs(t *testing.T) {
	s := codeStub(t, nil)
	c := s.client(t).As("z")
	for _, ref := range []string{
		"../../../admin/users",
		"main?limit=1",
		"main#frag",
		"ma%2Fin",
		"a..b",
		"-oProxyCommand=x",
		"/main",
		"main/",
		"main branch",
	} {
		if _, err := c.Resolve(t.Context(), "hanzoai", "cloud", ref); err == nil {
			t.Fatalf("Resolve(%q) was accepted", ref)
		}
	}
	if n := len(s.got.all()); n != 0 {
		t.Fatalf("%d unaddressable ref(s) reached the forge", n)
	}
}

// TestResolve_RefusesANonCommitAnswer proves the forge's own reply is checked
// before it is spliced into the next request's path. It costs one comparison and
// it is what stops a tampered forge choosing which endpoint we call next.
func TestResolve_RefusesANonCommitAnswer(t *testing.T) {
	s := codeStub(t, nil)
	s.refs["hanzoai/cloud@main"] = "../../users/admin"
	_, err := s.client(t).As("z").Resolve(t.Context(), "hanzoai", "cloud", "main")
	if err == nil || !strings.Contains(err.Error(), "not a commit") {
		t.Fatalf("err = %v, want a refusal naming the answer as not a commit", err)
	}
}

// TestResolve_UnknownRefIsNotACommit proves a ref the forge does not know fails
// rather than resolving to an empty sha — an empty revision reaching delivery is
// how an empty desired set gets handed to a pruning reconcile.
func TestResolve_UnknownRefIsNotACommit(t *testing.T) {
	s := codeStub(t, nil)
	if _, err := s.client(t).As("z").Resolve(t.Context(), "hanzoai", "cloud", "nope"); err == nil {
		t.Fatal("an unknown ref resolved")
	}
}

// TestTip_StillReadsAsTheMachine proves the shared branch wire did not take the
// policy with it: Tip asks whether the push this platform dispatched landed, and
// answering that through a human's visibility would make a permissions change
// look like a failed push.
func TestTip_StillReadsAsTheMachine(t *testing.T) {
	s := codeStub(t, nil)
	sha, found, err := s.client(t).As("z").Tip(t.Context(), "hanzoai", "cloud", "agent/x")
	if err != nil || !found || sha != sideSHA {
		t.Fatalf("Tip = (%q, %v, %v), want the branch tip", sha, found, err)
	}
	for _, r := range s.got.all() {
		if r.Header.Get("Sudo") != "" {
			t.Fatalf("Tip sudoed as %q — it must read as the machine", r.Header.Get("Sudo"))
		}
	}
}

// ── the tree ─────────────────────────────────────────────────────────────────

func pathsOf(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// TestFiles_ReadsTheWholeTreeAtOneCommit is the shape of the whole read: the
// archive's own root is stripped, directories are not files, and the revision
// reported is the COMMIT that was resolved rather than the name asked for.
func TestFiles_ReadsTheWholeTreeAtOneCommit(t *testing.T) {
	s := codeStub(t, archiveOf(t, "cloud",
		member{name: "go.mod", body: "module x\n"},
		member{name: "apps", flag: tar.TypeDir},
		member{name: "apps/lsp", flag: tar.TypeDir},
		member{name: "apps/lsp/lsp.go", body: "package lsp\n"},
	))

	tree, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if tree.Rev != mainSHA {
		t.Fatalf("Rev = %q, want the commit main resolved to", tree.Rev)
	}
	want := []string{"go.mod", "apps/lsp/lsp.go"}
	got := pathsOf(tree.Files)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	if string(tree.Files[1].Data) != "package lsp\n" {
		t.Fatalf("content = %q", tree.Files[1].Data)
	}
}

// TestFiles_ReadsAtTheResolvedShaNotTheName pins the consistency property: the
// archive is asked for at the commit, so a push landing between the resolve and
// the read cannot produce a tree assembled from two commits.
func TestFiles_ReadsAtTheResolvedShaNotTheName(t *testing.T) {
	s := codeStub(t, archiveOf(t, "cloud", member{name: "a.go", body: "package p\n"}))
	if _, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", ""); err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := "/v1/repos/hanzoai/cloud/archive/" + mainSHA + ".tar.gz"
	for _, r := range s.got.all() {
		if r.URL.Path == want {
			if r.Header.Get("Sudo") != "z" {
				t.Fatalf("archive read carried Sudo=%q, want z", r.Header.Get("Sudo"))
			}
			return
		}
		if strings.Contains(r.URL.Path, "/archive/main") {
			t.Fatalf("the archive was read at the NAME: %s", r.URL.Path)
		}
	}
	t.Fatal("no archive read reached the forge")
}

// TestFiles_PrefixSelectsADirectory proves the prefix is a directory and not a
// substring: `apps/lsp` must not select `apps/lsprc`.
func TestFiles_PrefixSelectsADirectory(t *testing.T) {
	s := codeStub(t, archiveOf(t, "cloud",
		member{name: "go.mod", body: "module x\n"},
		member{name: "apps/lsp/lsp.go", body: "package lsp\n"},
		member{name: "apps/lsp/mount.go", body: "package lsp\n"},
		member{name: "apps/lsprc/other.go", body: "package other\n"},
	))

	tree, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "apps/lsp")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := []string{"apps/lsp/lsp.go", "apps/lsp/mount.go"}
	if got := pathsOf(tree.Files); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

// TestFiles_LargeFileIsListedNotRead pins the truncation contract delivery
// depends on. A manifest that was listed but not read must arrive MARKED, so the
// consumer refuses the whole desired set — silently dropping it is what deletes
// whatever that file declared.
func TestFiles_LargeFileIsListedNotRead(t *testing.T) {
	s := codeStub(t, archiveOf(t, "cloud",
		member{name: "small.yaml", body: "kind: ConfigMap\n"},
		member{name: "huge.yaml", body: "kind: ", size: maxBlob + 1},
		member{name: "after.yaml", body: "kind: Secret\n"},
	))

	tree, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(tree.Files) != 3 {
		t.Fatalf("files = %v, want all three listed", pathsOf(tree.Files))
	}
	big := tree.Files[1]
	if !big.Truncated || big.Data != nil {
		t.Fatalf("huge.yaml = %+v, want truncated with no data", big)
	}
	// The entry AFTER the oversized one must still arrive: skipping a file means
	// stepping over its bytes, not abandoning the stream.
	if got := string(tree.Files[2].Data); got != "kind: Secret\n" {
		t.Fatalf("the entry after a truncated one = %q", got)
	}
}

// TestFiles_RefusesAPathThatLeavesTheRepository is the write-escape refusal.
// apps/lsp materialises these paths on the daemon's disk, so an entry naming
// somewhere else is a write wherever it points.
func TestFiles_RefusesAPathThatLeavesTheRepository(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "/etc/passwd", "a/../../b"} {
		s := codeStub(t, archiveOf(t, "", member{name: name, body: "x"}))
		_, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
		if err == nil || !strings.Contains(err.Error(), "leaves the repository") {
			t.Fatalf("entry %q: err = %v, want a refusal", name, err)
		}
	}
}

// TestFiles_SkipsWhatIsNotAFile proves a symlink is not read. Following one is
// how a tree read reaches a path the repository does not contain.
func TestFiles_SkipsWhatIsNotAFile(t *testing.T) {
	s := codeStub(t, archiveOf(t, "cloud",
		member{name: "real.go", body: "package p\n"},
		member{name: "link.go", body: "../../../etc/passwd", flag: tar.TypeSymlink},
	))
	tree, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got := pathsOf(tree.Files); len(got) != 1 || got[0] != "real.go" {
		t.Fatalf("paths = %v, want real.go only", got)
	}
}

// TestFiles_UnprefixedArchiveIsReadWhole proves the root is stripped only when
// the archive actually opens with it. A forge configured without
// PrefixArchiveFiles serves bare paths, and stripping a guessed segment there
// would file every manifest one directory up.
func TestFiles_UnprefixedArchiveIsReadWhole(t *testing.T) {
	s := codeStub(t, archiveOf(t, "",
		member{name: "cloud", flag: tar.TypeDir}, // a real directory that shares the repo's name
		member{name: "cloud/inner.go", body: "package p\n"},
		member{name: "top.go", body: "package p\n"},
	))
	tree, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := []string{"cloud/inner.go", "top.go"}
	if got := pathsOf(tree.Files); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v — the repo-named directory is content, not a root", got, want)
	}
}

// TestFiles_RefusesAnArchiveWithTwoRoots proves a mixed archive is an error
// rather than a guess. Guessing which path space an entry meant is how a
// manifest ends up filed somewhere it does not live.
func TestFiles_RefusesAnArchiveWithTwoRoots(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range []*tar.Header{
		{Name: "cloud/", Mode: 0o755, Typeflag: tar.TypeDir},
		{Name: "elsewhere/x.go", Mode: 0o644, Size: 1},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write([]byte("x"))
		}
	}
	_ = tw.Close()
	_ = zw.Close()

	s := codeStub(t, buf.Bytes())
	_, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
	if err == nil || !strings.Contains(err.Error(), "outside its own root") {
		t.Fatalf("err = %v, want a refusal naming the mixed root", err)
	}
}

// TestFiles_BoundsAnExpandingArchive proves the limit is on the DECOMPRESSED
// stream. A bound on the socket cannot see an expansion, and this process
// decompresses into its own memory.
func TestFiles_BoundsAnExpandingArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	// Zeros compress to almost nothing and expand past the whole-tree bound.
	if err := tw.WriteHeader(&tar.Header{Name: "bomb.bin", Mode: 0o644, Size: maxTree * 2}); err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 1<<20)
	for written := int64(0); written < maxTree*2; written += int64(len(block)) {
		if _, err := tw.Write(block); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	if buf.Len() > 8<<20 {
		t.Fatalf("the bomb is %d compressed bytes — it is not testing an expansion", buf.Len())
	}

	s := codeStub(t, buf.Bytes())
	done := make(chan error, 1)
	go func() {
		_, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an archive expanding past the bound was accepted")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the read did not stop at the bound")
	}
}

// TestFiles_RefusesATreeThatEndsExactlyAtTheBound is the silent-truncation case,
// which is the dangerous one: an archive whose end-of-archive marker sits at the
// budget ends CLEANLY, so the walk succeeds and the files past it are simply
// absent. A short tree presented as a whole one is what hands a pruning
// reconcile an incomplete desired set, so spending the budget is a failure even
// when the tar parsed.
func TestFiles_RefusesATreeThatEndsExactlyAtTheBound(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	// One file whose header + padded body + the two trailing zero blocks land
	// exactly on maxTree, so tar reaches a legitimate end of archive with the
	// budget spent to the byte.
	const block = 512
	body := int64(maxTree - 3*block) // header + body + 2 end-of-archive blocks
	if err := tw.WriteHeader(&tar.Header{Name: "big.bin", Mode: 0o644, Size: body}); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for w := int64(0); w < body; w += int64(len(chunk)) {
		n := min(int64(len(chunk)), body-w)
		if _, err := tw.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = zw.Close()

	s := codeStub(t, buf.Bytes())
	_, err := s.client(t).As("z").Files(t.Context(), "hanzoai", "cloud", "main", "")
	if err == nil || !strings.Contains(err.Error(), "expands past") {
		t.Fatalf("err = %v, want a refusal for an archive that spent the whole budget", err)
	}
}

// TestFiles_AnActorWhoCannotSeeItReadsNothing proves the tree read is the
// forge's ACL and not ours: a repository outside the actor's visibility answers
// the same way one that does not exist does.
func TestFiles_AnActorWhoCannotSeeItReadsNothing(t *testing.T) {
	s := codeStub(t, archiveOf(t, "cloud", member{name: "a.go", body: "package p\n"}))
	s.visible["outsider"] = []string{"someone-else"}
	_, err := s.client(t).As("outsider").Files(t.Context(), "hanzoai", "cloud", "main", "")
	if !errors.Is(err, ErrUnknownActor) {
		t.Fatalf("err = %v, want ErrUnknownActor", err)
	}
}

// ── the repository's own clock ───────────────────────────────────────────────

// TestRepo_CarriesWhenItLastMoved proves updated_at decodes as a TIME. It is
// compared against a window and formatted as a date, and a string here would put
// the parse — and the chance to get the layout wrong — at every call site.
func TestRepo_CarriesWhenItLastMoved(t *testing.T) {
	s := newStub(t)
	s.visible["z"] = []string{"hanzoai"}
	s.repos["hanzoai"] = []Repo{{
		Name: "cloud", FullName: "hanzoai/cloud", Size: 4096,
		UpdatedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}}
	got, err := s.client(t).As("z").Repo(context.Background(), "hanzoai", "cloud")
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if !got.UpdatedAt.Equal(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("UpdatedAt = %v, want the forge's own timestamp", got.UpdatedAt)
	}
}
