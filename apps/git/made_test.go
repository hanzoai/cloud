package git

// made_test.go — the browse reads of a repository in an org's own code
// workspace on the forge: read as the machine, only in the namespace the org's
// record names, and only for an org the estate's table does not.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/plane"
)

// shelfForge holds acme/site on the forge, readable by the machine and by no
// person.
type shelfForge struct {
	mu    sync.Mutex
	asked []string // "<path>?<query> as <sudo>"
}

func (f *shelfForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	asked := url.URL{Path: strings.TrimPrefix(r.URL.Path, forge.API), RawQuery: r.URL.RawQuery, ForceQuery: true}
	f.asked = append(f.asked, asked.String()+" as "+r.Header.Get("Sudo"))
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "token machine" || r.Header.Get("Sudo") != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	path, ref := strings.TrimPrefix(r.URL.Path, forge.API), r.URL.Query().Get("ref")
	switch {
	case path == "/repos/acme/site/contents-ext" && ref == "agent/abc":
		_ = json.NewEncoder(w).Encode(map[string]any{"dir_contents": []map[string]any{
			{"name": "index.html", "path": "index.html", "type": "file", "size": 11},
			{"name": "assets", "path": "assets", "type": "dir"},
		}})
	case path == "/repos/acme/site/contents-ext/index.html" && ref == "agent/abc":
		file := map[string]any{"name": "index.html", "path": "index.html", "type": "file", "size": 11}
		if r.URL.Query().Get("includes") == "file_content" {
			file["content"] = base64.StdEncoding.EncodeToString([]byte("<h1>hi</h1>"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"file_contents": file})
	case path == "/repos/acme/site/git/refs/heads":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"ref": "refs/heads/main", "object": map[string]any{"type": "commit", "sha": "0123456789abcdef0123456789abcdef01234567"}},
			{"ref": "refs/heads/agent/abc", "object": map[string]any{"type": "commit", "sha": "0b5c830000000000000000000000000000000000"}},
		})
	case path == "/repos/acme/site/git/refs/tags":
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"ref": "refs/tags/v1.0.0", "object": map[string]any{"type": "commit", "sha": "0123456789abcdef0123456789abcdef01234567"}},
		})
	case path == "/repos/acme/site":
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "site", "default_branch": "main"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *shelfForge) read() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// servesShelf stands the forge up and answers acme's workspace as made.
func servesShelf(t *testing.T) (*shelfForge, *[]string) {
	t.Helper()
	f := &shelfForge{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	var asked []string
	var mu sync.Mutex
	wasWS, wasSrc := madeWorkspace, madeSource
	madeWorkspace = func(org string, _ bool) (forge.Workspace, error) {
		mu.Lock()
		asked = append(asked, org)
		mu.Unlock()
		if org == "acme" {
			return forge.Workspace{Owner: "acme", Made: true}, nil
		}
		return forge.Workspace{}, nil
	}
	madeSource = func(context.Context) (*forge.Client, error) { return forge.New(srv.URL, "machine") }
	t.Cleanup(func() { madeWorkspace, madeSource = wasWS, wasSrc })
	return f, &asked
}

// A REPOSITORY IN THE ORG'S WORKSPACE IS READ FROM THE FORGE, as the machine,
// in the browse shapes: a directory, dirs first, and a file's text.
func TestAWorkspaceRepositoryIsBrowsedOnTheForge(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, _ := servesShelf(t)

		code, body := do(t, app, http.MethodGet, "/v1/git/repos/site/tree?ref=agent/abc", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("tree: %d %s", code, body)
		}
		var tree treeJSON
		if err := json.Unmarshal(body, &tree); err != nil {
			t.Fatal(err)
		}
		if len(tree.Entries) != 2 || tree.Entries[0].Name != "assets" || tree.Entries[0].Type != "tree" ||
			tree.Entries[1].Type != "blob" || tree.Entries[1].Size != 11 {
			t.Fatalf("tree = %+v", tree.Entries)
		}
		code, body = do(t, app, http.MethodGet, "/v1/git/repos/site/blob?ref=agent/abc&path=index.html", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("blob: %d %s", code, body)
		}
		var blob blobJSON
		if err := json.Unmarshal(body, &blob); err != nil || blob.Content != "<h1>hi</h1>" || blob.Binary || blob.Encoding != "utf8" {
			t.Fatalf("blob = %+v, %v", blob, err)
		}
		for _, a := range f.read() {
			if (!strings.HasPrefix(a, "/repos/acme/site/") && !strings.HasPrefix(a, "/repos/acme/site?")) || !strings.HasSuffix(a, " as ") {
				t.Fatalf("the forge was asked %s", a)
			}
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/site/blob?ref=agent/abc", "acme", nil); code != http.StatusBadRequest {
			t.Fatalf("a blob with no path answered %d", code)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/site/tree?ref=agent/abc&path=nope", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("a missing directory answered %d", code)
		}
		code, body = do(t, app, http.MethodGet, "/v1/git/repos/site/refs", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("refs: %d %s", code, body)
		}
		var refs refsJSON
		if err := json.Unmarshal(body, &refs); err != nil || refs.Default != "main" || len(refs.Branches) != 2 || len(refs.Tags) != 1 {
			t.Fatalf("refs = %+v, err=%v", refs, err)
		}
		if refs.Branches[1].Name != "agent/abc" || refs.Branches[1].SHA != "0b5c830000000000000000000000000000000000" {
			t.Fatalf("missing pushed agent branch in refs: %+v", refs.Branches)
		}
	})
}

// ONLY THE ORG'S OWN WORKSPACE: an org with none, and the estate, read nothing
// from the forge here — the estate is not even asked about a workspace.
func TestOnlyTheOrgsOwnWorkspaceIsBrowsed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, asked := servesShelf(t)
		for _, org := range []string{"globex", "hanzo"} {
			if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/site/tree?ref=agent/abc", org, nil); code != http.StatusNotFound {
				t.Fatalf("%s read acme's repository: %d", org, code)
			}
		}
		if got := f.read(); len(got) != 0 {
			t.Fatalf("the forge was asked %v", got)
		}
		if strings.Join(*asked, ",") != "globex" {
			t.Fatalf("workspaces asked about: %v", *asked)
		}
	})
}

// A REPOSITORY THIS APP HOLDS ANSWERS FIRST, at a ref it holds.
func TestARepositoryHeldHereAnswersFirst(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, asked := servesShelf(t)
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/site/push", "acme", map[string]any{
			"files": []map[string]any{{"path": "index.html", "content": "<h1>here</h1>"}},
		}); code != http.StatusOK {
			t.Fatalf("push: %d %s", code, b)
		}
		*asked = nil
		if code, b := do(t, app, http.MethodGet, "/v1/git/repos/site/tree", "acme", nil); code != http.StatusOK {
			t.Fatalf("tree: %d %s", code, b)
		}
		code, body := do(t, app, http.MethodGet, "/v1/git/repos/site/blob?ref=main&path=index.html", "acme", nil)
		var blob blobJSON
		if code != http.StatusOK || json.Unmarshal(body, &blob) != nil || blob.Content != "<h1>here</h1>" {
			t.Fatalf("blob: %d %s", code, body)
		}
		if len(f.read()) != 0 || len(*asked) != 0 {
			t.Fatalf("a repository held here was looked for on the forge: %v %v", f.read(), *asked)
		}
	})
}

// A HELD COPY THAT CANNOT RESOLVE THE REF YIELDS TO THE WORKSPACE: the empty copy
// a project's visibility makes here, and one without the branch a run pushed to
// the forge, are both read there — and nowhere for an org with no workspace.
func TestAHeldCopyWithoutTheRefYieldsToTheWorkspace(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		f, asked := servesShelf(t)
		s := mounted.Load()
		for _, org := range []string{"acme", "globex"} {
			st, err := storeFor(s, org)
			if err != nil {
				t.Fatal(err)
			}
			if err := provision(s, context.Background(), st, Repo{ID: "repo_site_" + org, Org: org, Name: "site", DefaultBranch: "main", CreatedAt: 1, UpdatedAt: 1}); err != nil {
				t.Fatal(err)
			}
		}
		reads := func(stage string) {
			t.Helper()
			code, body := do(t, app, http.MethodGet, "/v1/git/repos/site/tree?ref=agent/abc", "acme", nil)
			var tree treeJSON
			if code != http.StatusOK || json.Unmarshal(body, &tree) != nil || len(tree.Entries) != 2 {
				t.Fatalf("%s: tree: %d %s", stage, code, body)
			}
			code, body = do(t, app, http.MethodGet, "/v1/git/repos/site/blob?ref=agent/abc&path=index.html", "acme", nil)
			var blob blobJSON
			if code != http.StatusOK || json.Unmarshal(body, &blob) != nil || blob.Content != "<h1>hi</h1>" {
				t.Fatalf("%s: blob: %d %s", stage, code, body)
			}
			if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/site/blob?ref=agent/abc", "acme", nil); code != http.StatusBadRequest {
				t.Fatalf("%s: a blob with no path answered %d", stage, code)
			}
		}
		reads("empty")
		// An empty placeholder copy (0 local branches) must ALSO yield /refs to the
		// org's made workspace on the forge so pushed agent branches are visible.
		if code, body := do(t, app, http.MethodGet, "/v1/git/repos/site/refs", "acme", nil); code != http.StatusOK {
			t.Fatalf("empty placeholder refs: %d %s", code, body)
		} else {
			var refs refsJSON
			if err := json.Unmarshal(body, &refs); err != nil || len(refs.Branches) != 2 || refs.Branches[1].Name != "agent/abc" {
				t.Fatalf("empty placeholder refs did not yield to forge: %+v (%v)", refs, err)
			}
		}
		// An org with no made workspace still gets an empty 200 for its empty local repo.
		if code, body := do(t, app, http.MethodGet, "/v1/git/repos/site/refs", "globex", nil); code != http.StatusOK {
			t.Fatalf("globex empty refs: %d %s", code, body)
		} else {
			var refs refsJSON
			if err := json.Unmarshal(body, &refs); err != nil || len(refs.Branches) != 0 {
				t.Fatalf("globex empty refs unexpectedly returned branches: %+v (%v)", refs, err)
			}
		}
		if code, b := do(t, app, http.MethodPost, "/v1/git/repos/site/push", "acme", map[string]any{
			"files": []map[string]any{{"path": "index.html", "content": "<h1>here</h1>"}},
		}); code != http.StatusOK {
			t.Fatalf("push: %d %s", code, b)
		}
		reads("without the branch")
		for _, a := range f.read() {
			if (!strings.HasPrefix(a, "/repos/acme/site/") && !strings.HasPrefix(a, "/repos/acme/site?")) || !strings.HasSuffix(a, " as ") {
				t.Fatalf("the forge was asked %s", a)
			}
		}

		before, workspaces := len(f.read()), len(*asked)
		code, body := do(t, app, http.MethodGet, "/v1/git/repos/site/tree?ref=agent/abc", "globex", nil)
		if code != http.StatusNotFound || !strings.Contains(string(body), "unknown ref") {
			t.Fatalf("globex's empty copy answered %d %s", code, body)
		}
		if len(f.read()) != before || strings.Join((*asked)[workspaces:], ",") != "globex" {
			t.Fatalf("globex read the forge: %v %v", f.read()[before:], (*asked)[workspaces:])
		}
	})
}

// A DEPLOYMENT WITHOUT THE PROJECT APP KEEPS NO RECORD, so a repository it does
// not hold is simply not found.
func TestNoRecordIsNotFound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		was := madeWorkspace
		madeWorkspace = func(string, bool) (forge.Workspace, error) { return forge.Workspace{}, plane.ErrNoPeer }
		t.Cleanup(func() { madeWorkspace = was })
		if code, _ := do(t, app, http.MethodGet, "/v1/git/repos/site/tree", "acme", nil); code != http.StatusNotFound {
			t.Fatalf("answered %d", code)
		}
	})
}
