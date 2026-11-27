package coding

// work_test.go pins the reads of what a run did: who may read a run, that they
// come from its sandbox while it runs and from what it saved after, and the
// three answers — the change, a directory and a file — in the shapes the builder
// renders.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

const (
	runSession = "sess_abc123def456"
	runBranch  = "agent/abc123def456"
	baseTip    = "1111111111111111111111111111111111111111"
	runTip     = "2222222222222222222222222222222222222222"
)

// runDiff renames one file with an edit, edits another, and adds a binary.
const runDiff = `diff --git a/lib/util.go b/lib/helpers.go
similarity index 90%
rename from lib/util.go
rename to lib/helpers.go
index 5555555..6666666 100644
--- a/lib/util.go
+++ b/lib/helpers.go
@@ -1,2 +1,2 @@
 package lib
-func b() {}
+func c() {}
diff --git a/api/server.go b/api/server.go
index 1111111..2222222 100644
--- a/api/server.go
+++ b/api/server.go
@@ -1 +1,2 @@
-var port = 80
+var port = 8080
+var host = "0.0.0.0"
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..7777777
Binary files /dev/null and b/logo.png differ
`

// workForge is the forge a run pushed to: hanzoai/site, which zoe may read and
// nobody else may, holding the run's branch once it has landed.
type workForge struct {
	mu     sync.Mutex
	url    string
	landed bool
	pr     bool // the run's pull request is open on the forge
	asked  []string
	sudo   []string
	// hold, when set, keeps every comparison waiting until it is closed.
	hold chan struct{}
	// many makes the branch carry more commits than one read lists, and long
	// makes the review of its pull request longer than one read keeps.
	many, long bool
}

func (f *workForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.hold != nil && strings.Contains(r.URL.Path, "/compare/") {
		<-f.hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	if login, ok := strings.CutPrefix(path, "/users/"); ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "email": login + "@hanzo.ai"})
		return
	}
	f.asked = append(f.asked, path+"?"+r.URL.RawQuery)
	f.sudo = append(f.sudo, r.Header.Get("Sudo"))
	if r.Header.Get("Sudo") != "zoe" {
		// The forge's own answer to a person who cannot see the repository.
		w.WriteHeader(http.StatusNotFound)
		return
	}
	ref := r.URL.Query().Get("ref")
	pull := map[string]any{"number": 7, "html_url": f.url + "/hanzoai/site/pulls/7", "title": "Rename the helper",
		"state": "open", "merged": false, "mergeable": true,
		"head": map[string]any{"ref": runBranch, "repo": map[string]any{"full_name": "hanzoai/site"}}}
	// A fork's pull request from a branch of the run's name: newer than the run's,
	// so a lookup by branch name alone meets it first.
	fork := map[string]any{"number": 9, "html_url": f.url + "/hanzoai/site/pulls/9", "title": "Totally the run",
		"state": "open", "merged": false, "mergeable": true,
		"head": map[string]any{"ref": runBranch, "repo": map[string]any{"full_name": "mallory/site"}}}
	switch {
	case path == "/repos/hanzoai/site":
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "site", "full_name": "hanzoai/site", "default_branch": "main"})
	case path == "/repos/hanzoai/site/branches/"+runBranch && f.landed:
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"id": runTip}})
	case path == "/repos/hanzoai/site/compare/main..."+runBranch && f.landed:
		if r.URL.Query().Get("output") == "diff" {
			_, _ = w.Write([]byte(runDiff))
			return
		}
		commits := []map[string]any{
			{"sha": runTip, "commit": map[string]any{"message": "Rename the helper\n\nAnd its callers.",
				"author": map[string]any{"name": "hanzo-agent", "date": "2026-09-27T10:05:00+02:00"}}},
			{"sha": baseTip, "commit": map[string]any{"message": "Move the port",
				"author": map[string]any{"name": "hanzo-agent", "date": "2026-09-27T07:59:00Z"}}},
		}
		for i := 0; f.many && i < 400; i++ {
			commits = append(commits, map[string]any{"sha": baseTip, "commit": map[string]any{"message": "older",
				"author": map[string]any{"name": "hanzo-agent", "date": "2026-09-27T07:00:00Z"}}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total_commits": len(commits), "commits": commits})
	case path == "/repos/hanzoai/site/pulls/7" && f.pr:
		_ = json.NewEncoder(w).Encode(pull)
	case path == "/repos/hanzoai/site/pulls/9":
		_ = json.NewEncoder(w).Encode(fork)
	case path == "/repos/hanzoai/site/pulls" && f.pr:
		_ = json.NewEncoder(w).Encode([]map[string]any{fork, pull})
	case path == "/repos/hanzoai/site/pulls":
		_ = json.NewEncoder(w).Encode([]map[string]any{fork})
	case path == "/repos/hanzoai/site/pulls/7/reviews" && f.pr:
		body := "name it for what it does"
		if f.long {
			body = strings.Repeat("name it for what it does. ", 4096)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"user": map[string]any{"login": "ada"}, "state": "REQUEST_CHANGES", "body": body,
				"submitted_at": "2026-09-27T11:00:00Z"},
		})
	case path == "/repos/hanzoai/site/contents-ext" && ref == "main":
		_ = json.NewEncoder(w).Encode(map[string]any{"dir_contents": []map[string]any{
			{"name": "lib", "path": "lib", "type": "dir"},
			{"name": "README.md", "path": "README.md", "type": "file", "size": 6},
		}})
	case path == "/repos/hanzoai/site/contents-ext" && ref == runBranch && f.landed:
		_ = json.NewEncoder(w).Encode(map[string]any{"dir_contents": []map[string]any{
			{"name": "logo.png", "path": "logo.png", "type": "file", "size": 4},
			{"name": "lib", "path": "lib", "type": "dir"},
			{"name": "api", "path": "api", "type": "dir"},
		}})
	case path == "/repos/hanzoai/site/contents-ext/api/server.go" && ref == runBranch && f.landed:
		body := "var port = 8080\n"
		file := map[string]any{"name": "server.go", "path": "api/server.go", "type": "file", "size": len(body)}
		if r.URL.Query().Get("includes") == "file_content" {
			file["content"], file["encoding"] = base64.StdEncoding.EncodeToString([]byte(body)), "base64"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"file_contents": file})
	case path == "/repos/hanzoai/site/contents-ext/logo.png" && ref == runBranch && f.landed:
		file := map[string]any{"name": "logo.png", "path": "logo.png", "type": "file", "size": 4}
		if r.URL.Query().Get("includes") == "file_content" {
			file["content"] = base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 0, 'G'})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"file_contents": file})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// read is what the forge was asked, as a transcript.
func (f *workForge) read() (asked, sudo []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...), append([]string(nil), f.sudo...)
}

// servesWork stands the run's forge up, with zoe's run on it as the sessions app
// would let the caller see it: sessions names the sessions the caller may read,
// and every other is absent, which is the answer agents.Visible gives.
func servesWork(t *testing.T, landed bool) (*workForge, map[string]agents.Session) {
	t.Helper()
	iamAddr = map[string]client.Email{"sub-zoe": verified("zoe@hanzo.ai"), "sub-mal": verified("mal@hanzo.ai")}
	f := &workForge{landed: landed}
	f.url = servesForge(t, f)
	sessions := map[string]agents.Session{
		runSession: {ID: runSession, Org: "hanzo", Kind: kindCoding, Repo: "hanzoai/site", Base: "main", Branch: runBranch},
	}
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		if x, ok := sessions[id]; ok {
			return x, nil
		}
		return agents.Session{}, zip.ErrNotFound("session not found")
	}
	t.Cleanup(func() { visibleRun = was })
	return f, sessions
}

// status is the HTTP status an op's error answers with.
func status(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// boxDiff is a run's change as git prints it with --no-renames: an edit, a new
// binary, and a deletion.
const boxDiff = `diff --git a/api/server.go b/api/server.go
index 1111111..2222222 100644
--- a/api/server.go
+++ b/api/server.go
@@ -1 +1,2 @@
-var port = 80
+var port = 8080
+var host = "0.0.0.0"
diff --git a/logo.png b/logo.png
new file mode 100644
index 0000000..7777777
Binary files /dev/null and b/logo.png differ
diff --git a/lib/old.go b/lib/old.go
deleted file mode 100644
index 5555555..0000000
--- a/lib/old.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package lib
-func b() {}
`

// workBox is a run's sandbox: running, or parked, answering the reads a run's
// change, tree and files are made of, and every call noted with its caller.
type workBox struct {
	mu     sync.Mutex
	parked bool
	calls  []string
}

// servesBox stands the run's sandbox up and zoe's run beside it, as the sessions
// app would let the caller see it.
func servesBox(t *testing.T) (*workBox, map[string]agents.Session) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	b := &workBox{}
	sandboxes := zip.New(zip.Config{AppName: sandboxpeer.App, DisableStartupMessage: true})
	sandboxes.Post("/sandbox/run", func(ctx context.Context, in *client.RunIn) (*client.Ran, error) {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.calls = append(b.calls, zip.CallerOf(ctx).User+" "+strings.Join(in.Argv, " "))
		if b.parked {
			return nil, zip.ErrConflict("sandbox is parked")
		}
		switch {
		case in.ID != "m_box":
			return nil, zip.ErrNotFound("sandbox not found")
		case in.Argv[0] == "true":
			return &client.Ran{}, nil
		case in.Argv[0] == "bash" && strings.Contains(in.Argv[2], "--no-renames"):
			return &client.Ran{Stdout: "commit\t" + runTip + "\thanzo-agent\t2026-09-27T10:05:00Z\tMove the port\n" +
				"commit\t" + baseTip + "\tzoe\t2026-09-27T09:00:00Z\tFirst\ndiff\n" + boxDiff}, nil
		case in.Argv[0] == "find" && in.Argv[1] == "/work":
			return &client.Ran{Stdout: "f\t6\tREADME.md\nd\t4096\tlib\nf\t4\tlogo.png\nd\t4096\tapi\n"}, nil
		case in.Argv[0] == "sh" && in.Argv[len(in.Argv)-1] == "/work/api/server.go":
			return &client.Ran{Stdout: "16\nvar port = 8080\n"}, nil
		case in.Argv[0] == "sh" && in.Argv[len(in.Argv)-1] == "/work/logo.png":
			return &client.Ran{Stdout: "4\n\x89P\x00G"}, nil
		}
		return &client.Ran{ExitCode: 1, Stderr: "No such file or directory"}, nil
	}, zip.WithOperationID(client.SandboxRun))
	plane.Bind()
	go func() { _ = sandboxes.Listen(zip.SocketPath(sandboxpeer.App)) }()
	t.Cleanup(func() { _ = sandboxes.Shutdown() })
	waitListening(t, sandboxpeer.App)

	sessions := map[string]agents.Session{
		runSession: {ID: runSession, Org: "hanzo", Kind: kindCoding, Repo: "hanzoai/site", Base: "main", Branch: runBranch, Box: "m_box"},
	}
	was := visibleRun
	visibleRun = func(_ context.Context, id string) (agents.Session, error) {
		if x, ok := sessions[id]; ok {
			return x, nil
		}
		return agents.Session{}, zip.ErrNotFound("session not found")
	}
	t.Cleanup(func() { visibleRun = was })
	return b, sessions
}

// shelved is an object store in memory.
type shelved struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *shelved) grant(context.Context, string, string) (client.SiteUpload, error) {
	return client.SiteUpload{}, errors.New("no uploads in a test")
}

func (s *shelved) put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = append([]byte(nil), data...)
	return nil
}

func (s *shelved) open(_ context.Context, key string) (io.ReadCloser, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, 0, errNoArtifact
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

// stored stands in for the object store.
func stored(t *testing.T) *shelved {
	t.Helper()
	s := &shelved{m: map[string][]byte{}}
	was := runs
	runs = s
	t.Cleanup(func() { runs = was })
	return s
}

// wantChange is the change boxDiff and its two commits read as.
func wantChange(t *testing.T, got *client.CodingChanges) {
	t.Helper()
	if len(got.Commits) != 2 || got.Commits[0].SHA != runTip || got.Commits[0].Message != "Move the port" {
		t.Fatalf("commits %+v", got.Commits)
	}
	files := map[string]client.CodingFile{}
	for _, f := range got.Files {
		files[f.Path] = f
	}
	if f := files["api/server.go"]; f.Status != "modified" || f.Additions != 2 || f.Deletions != 1 || !strings.HasPrefix(f.Patch, "@@ -1 +1,2 @@") {
		t.Errorf("the edit read as %+v", f)
	}
	if f := files["logo.png"]; f.Status != "added" || f.Patch != "" {
		t.Errorf("the binary read as %+v", f)
	}
	if f := files["lib/old.go"]; f.Status != "deleted" || f.Deletions != 2 {
		t.Errorf("the deletion read as %+v", f)
	}
	if len(got.Files) != 3 {
		t.Errorf("files %+v", got.Files)
	}
}

// A RUN'S CHANGE IS READ FROM ITS SANDBOX while that runs: its commits since its
// branch started and its whole change, as the caller.
func TestChanges_AreReadFromTheRunsSandbox(t *testing.T) {
	b, _ := servesBox(t)
	stored(t)
	got, err := Changes(asCaller("sub-zoe"), runSession)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	wantChange(t, got)
	if got.Repo != "hanzoai/site" || got.Head != runBranch || got.Base != "main" {
		t.Fatalf("the change names %+v", got)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.calls {
		if !strings.HasPrefix(c, "sub-zoe ") {
			t.Fatalf("a read of the sandbox was not the caller's: %q", c)
		}
	}
}

// ONCE THE SANDBOX IS PARKED OR GONE, THE CHANGE IS WHAT IT SAVED: its commits
// from its index and its files from its patch.
func TestChanges_OfAParkedSandboxAreWhatTheRunSaved(t *testing.T) {
	b, _ := servesBox(t)
	b.parked = true
	s := stored(t)
	ix := index{Commits: []client.CodingCommit{{SHA: runTip, Message: "Move the port"}, {SHA: baseTip, Message: "First"}}}
	ix.Artifacts = []client.Artifact{{Name: "changes.patch", Kind: kindPatch, Size: int64(len(boxDiff))}}
	if err := saveIndex(context.Background(), "hanzo", runSession, ix); err != nil {
		t.Fatal(err)
	}
	_ = s.put(context.Background(), "hanzo/"+runSession+"/changes.patch", []byte(boxDiff))
	got, err := Changes(asCaller("sub-zoe"), runSession)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	wantChange(t, got)
}

// A RUN THAT SAVED NOTHING AND HAS NO SANDBOX IS AN EMPTY CHANGE, not an error,
// and its base still comes from the repository when the session named none.
func TestChanges_ARunWithNothingSavedIsAnEmptyChange(t *testing.T) {
	_, sessions := servesWork(t, false)
	stored(t)
	x := sessions[runSession]
	x.Base = ""
	sessions[runSession] = x
	got, err := Changes(asCaller("sub-zoe"), runSession)
	if err != nil || len(got.Commits) != 0 || len(got.Files) != 0 || got.Pull != nil || got.Base != "main" {
		t.Fatalf("Changes = %+v, %v", got, err)
	}
}

// A RUN THE CALLER CANNOT SEE IS NOT FOUND, on every read, whoever holds it.
func TestReads_ARunTheCallerCannotSeeIsNotFound(t *testing.T) {
	servesBox(t)
	stored(t)
	ctx := asCaller("sub-mal")
	if _, err := Changes(ctx, "sess_not_yours"); status(err) != http.StatusNotFound {
		t.Fatalf("Changes = %v, want 404", err)
	}
	if _, err := Tree(ctx, "sess_not_yours", ""); status(err) != http.StatusNotFound {
		t.Fatalf("Tree = %v, want 404", err)
	}
	if _, err := Blob(ctx, "sess_not_yours", "README.md"); status(err) != http.StatusNotFound {
		t.Fatalf("Blob = %v, want 404", err)
	}
	if _, err := Artifacts(ctx, "sess_not_yours"); status(err) != http.StatusNotFound {
		t.Fatalf("Artifacts = %v, want 404", err)
	}
}

// A DIRECTORY IS ITS WORKSPACE AS IT IS, directories first, .git aside.
func TestTree_ReadsTheWorkspace(t *testing.T) {
	servesBox(t)
	got, err := Tree(asCaller("sub-zoe"), runSession, "")
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Type+":"+e.Path)
	}
	if want := "tree:api tree:lib blob:README.md blob:logo.png"; strings.Join(names, " ") != want || got.Ref != runBranch {
		t.Fatalf("Tree = %v at %q, want %s", names, got.Ref, want)
	}
	for _, p := range []string{"../etc", "a/../../b", "lib/../../x"} {
		if _, err := Tree(asCaller("sub-zoe"), runSession, p); status(err) != http.StatusBadRequest {
			t.Errorf("Tree(%q) = %v, want 400", p, err)
		}
	}
}

// A FILE IS THE GIT BROWSE SHAPE: text verbatim, a binary base64.
func TestBlob_IsTheGitBrowseShape(t *testing.T) {
	servesBox(t)
	got, err := Blob(asCaller("sub-zoe"), runSession, "api/server.go")
	if err != nil || got.Content != "var port = 8080\n" || got.Encoding != "utf8" || got.Size != 16 {
		t.Fatalf("Blob = %+v, %v", got, err)
	}
	bin, err := Blob(asCaller("sub-zoe"), runSession, "logo.png")
	if err != nil || !bin.Binary || bin.Encoding != "base64" {
		t.Fatalf("binary Blob = %+v, %v", bin, err)
	}
	if _, err := Blob(asCaller("sub-zoe"), runSession, "../../etc/passwd"); status(err) != http.StatusBadRequest {
		t.Fatalf("a path out of the tree = %v", err)
	}
}

// A PARKED SANDBOX'S TREE IS A CONFLICT that says to resume it.
func TestTree_OfAParkedSandboxSaysToResumeIt(t *testing.T) {
	b, _ := servesBox(t)
	b.parked = true
	_, err := Tree(asCaller("sub-zoe"), runSession, "")
	if status(err) != http.StatusConflict || !strings.Contains(err.Error(), "/v1/sandbox/m_box/resume") {
		t.Fatalf("Tree of a parked sandbox = %v", err)
	}
}

// WHAT A RUN LEFT IS LISTED FROM ITS INDEX, and only a stored artifact it lists
// is opened.
func TestArtifacts_AreWhatTheIndexLists(t *testing.T) {
	servesBox(t)
	s := stored(t)
	ix := index{}
	ix.Artifacts = []client.Artifact{{Name: "src/app.ts", Kind: kindFile, Size: 5},
		{Name: "port 5173", Kind: kindPreview, Port: 5173}}
	if err := saveIndex(context.Background(), "hanzo", runSession, ix); err != nil {
		t.Fatal(err)
	}
	_ = s.put(context.Background(), "hanzo/"+runSession+"/files/src/app.ts", []byte("hello"))
	_ = s.put(context.Background(), "hanzo/"+runSession+"/files/secret.txt", []byte("nope"))
	list, err := Artifacts(asCaller("sub-zoe"), runSession)
	if err != nil || len(list.Artifacts) != 2 || list.Saved == "" {
		t.Fatalf("Artifacts = %+v, %v", list, err)
	}
	r, n, err := Artifact(asCaller("sub-zoe"), runSession, "src/app.ts")
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	b, _ := io.ReadAll(r)
	if string(b) != "hello" || n != 5 {
		t.Fatalf("Artifact = %q (%d)", b, n)
	}
	for _, name := range []string{"secret.txt", "port 5173", "../index.json"} {
		if _, _, err := Artifact(asCaller("sub-zoe"), runSession, name); status(err) != http.StatusNotFound {
			t.Errorf("Artifact(%q) = %v, want 404", name, err)
		}
	}
}

// A CHANGE IS BOUNDED: one file's patch is cut at its cap and says so.
func TestDiffFiles_CutsALongPatch(t *testing.T) {
	long := "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1 @@\n+" + strings.Repeat("x", maxFilePatch) + "\n"
	files, _ := diffFiles(long)
	if len(files) != 1 || !files[0].Truncated || len(files[0].Patch) != maxFilePatch || files[0].Additions != 1 {
		t.Fatalf("a long patch read as %d files, truncated=%v, %d bytes", len(files), files[0].Truncated, len(files[0].Patch))
	}
}
