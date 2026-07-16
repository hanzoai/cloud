// browse.go — Hanzo Git's JSON read/browse surface: the machine-readable twin of
// the server-rendered UI (ui.go). The console repo-browser (hanzoai/console
// components/products/git) consumes THESE endpoints; ui.go serves the same data as
// HTML for a plain browser. ONE set of go-git read helpers backs both — the tree
// walk, ref resolution, and README scan are shared (openGit/resolveRef/findRepo/
// cleanTreePath/readmeAt), so the two surfaces can never drift.
//
// Routes (JSON, org-scoped — distinct trailing segments that never shadow the
// :org/:repo smart-HTTP protocol routes):
//
//	GET /v1/git/repos/:name/refs                 → { branches, tags, default }
//	GET /v1/git/repos/:name/tree?ref&path        → { entries: [ {name,path,type,size,mode} ] }
//	GET /v1/git/repos/:name/blob?ref&path        → { path,size,encoding,content,binary,truncated }
//	GET /v1/git/repos/:name/commits?ref&path&limit → { commits: [ {sha,shortSha,message,author*,date} ] }
//	GET /v1/git/repos/:name/readme?ref           → { path, content, encoding }
//
// ref + path ride as ?ref=&path= QUERY params (the UI's own convention), so a
// slashed branch (feature/x) and a nested path are always unambiguous. Isolation is
// identical to the rest of git: the org is the gateway-minted, IAM-validated
// X-Org-Id (org(c)); a repo outside the caller's org is simply not found.
package git

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// maxBlobBytes caps a single file-view response; a larger blob returns truncated=true
// with no content (the browser offers a clone/download instead of megabytes of JSON).
const maxBlobBytes = 1 << 20 // 1 MiB

// ---- JSON DTOs (mirror the console GitApi normalizers verbatim) ----

type refJSON struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type refsJSON struct {
	Branches []refJSON `json:"branches"`
	Tags     []refJSON `json:"tags"`
	Default  string    `json:"default"`
}

type treeEntryJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // "tree" | "blob"
	Size int64  `json:"size"`
	Mode string `json:"mode"` // octal, e.g. "100644" / "040000" / "120000"
}

type blobJSON struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Encoding  string `json:"encoding"` // "utf8" | "base64"
	Content   string `json:"content"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

type commitJSON struct {
	SHA         string `json:"sha"`
	ShortSHA    string `json:"shortSha"`
	Message     string `json:"message"`
	AuthorName  string `json:"authorName"`
	AuthorEmail string `json:"authorEmail"`
	Date        string `json:"date"`
}

type readmeJSON struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// ---- handlers ----

// browseTarget resolves the caller's org, the named repo, its bare go-git repo, and
// the requested ref (?ref=, "" ⇒ HEAD/default) to a commit — the shared preamble for
// tree/blob/commits/readme. Mirrors the uiTree/uiBlob guard, org-scoped.
func browseTarget(s *cloud.Service[state], c *zip.Ctx) (*gogit.Repository, *object.Commit, error) {
	o, ok := org(c)
	if !ok {
		return nil, nil, zip.ErrForbidden("X-Org-Id required")
	}
	r, found := findRepo(s, c.Context(), o, normalizeName(c.Param("name")))
	if !found {
		return nil, nil, zip.ErrNotFound("repo not found")
	}
	repo, err := openGit(s, r)
	if err != nil {
		return nil, nil, zip.ErrNotFound("empty repository")
	}
	commit, _, err := resolveRef(repo, r, strings.TrimSpace(c.Query("ref")))
	if err != nil {
		return nil, nil, zip.ErrNotFound("unknown ref")
	}
	return repo, commit, nil
}

// browseRefs lists the repo's branches + tags + default branch. Unlike the others it
// tolerates an empty repo (no HEAD) — it still reports the (empty) ref sets + default.
func browseRefs(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	r, found := findRepo(s, c.Context(), o, normalizeName(c.Param("name")))
	if !found {
		return zip.ErrNotFound("repo not found")
	}
	out := refsJSON{Branches: []refJSON{}, Tags: []refJSON{}, Default: firstNonEmptyStr(r.DefaultBranch, defaultBranchName)}
	if repo, err := openGit(s, r); err == nil {
		out.Branches = collectRefs(repo.Branches())
		out.Tags = collectRefs(repo.Tags())
	}
	return c.JSON(http.StatusOK, out)
}

// browseTree lists the immediate children of a subtree at a ref, dirs first then files.
func browseTree(s *cloud.Service[state], c *zip.Ctx) error {
	_, commit, err := browseTarget(s, c)
	if err != nil {
		return err
	}
	entries, err := treeEntriesJSON(commit, cleanTreePath(c.Query("path")))
	if err != nil {
		return zip.ErrNotFound("no such directory")
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": entries})
}

// browseBlob returns a single file's bytes at a ref+path (utf8 inline, or base64 for
// binary; truncated past maxBlobBytes).
func browseBlob(s *cloud.Service[state], c *zip.Ctx) error {
	_, commit, err := browseTarget(s, c)
	if err != nil {
		return err
	}
	fp := cleanTreePath(c.Query("path"))
	if fp == "" {
		return zip.ErrBadRequest("path is required")
	}
	file, err := commit.File(fp)
	if err != nil {
		return zip.ErrNotFound("no such file")
	}
	out := blobJSON{Path: fp, Size: file.Size, Encoding: "utf8"}
	if file.Size > maxBlobBytes {
		out.Truncated = true
		return c.JSON(http.StatusOK, out)
	}
	if bin, _ := file.IsBinary(); bin {
		reader, rerr := file.Reader()
		if rerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "read blob: %v", rerr)
		}
		defer func() { _ = reader.Close() }()
		raw, rerr := io.ReadAll(reader)
		if rerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "read blob: %v", rerr)
		}
		out.Binary = true
		out.Encoding = "base64"
		out.Content = base64.StdEncoding.EncodeToString(raw)
	} else if txt, terr := file.Contents(); terr == nil {
		out.Content = txt
	} else {
		return zip.Errorf(http.StatusInternalServerError, "read blob: %v", terr)
	}
	return c.JSON(http.StatusOK, out)
}

// browseCommits returns the ref's history (or a file's history when ?path is set),
// newest first, capped at ?limit (default 50, max 100).
func browseCommits(s *cloud.Service[state], c *zip.Ctx) error {
	repo, commit, err := browseTarget(s, c)
	if err != nil {
		return err
	}
	limit := 50
	if q := strings.TrimSpace(c.Query("limit")); q != "" {
		if n, e := strconv.Atoi(q); e == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}
	opts := &gogit.LogOptions{From: commit.Hash}
	if p := cleanTreePath(c.Query("path")); p != "" {
		opts.FileName = &p
	}
	iter, err := repo.Log(opts)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "log: %v", err)
	}
	defer iter.Close()
	out := []commitJSON{}
	for i := 0; i < limit; i++ {
		cm, e := iter.Next()
		if e != nil {
			break
		}
		out = append(out, commitJSON{
			SHA:         cm.Hash.String(),
			ShortSHA:    shortHash(cm.Hash.String()),
			Message:     firstLine(cm.Message),
			AuthorName:  cm.Author.Name,
			AuthorEmail: cm.Author.Email,
			Date:        cm.Author.When.UTC().Format(time.RFC3339),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"commits": out})
}

// browseReadme returns the rendered-as-text README at the tree root; 404 when none.
func browseReadme(s *cloud.Service[state], c *zip.Ctx) error {
	_, commit, err := browseTarget(s, c)
	if err != nil {
		return err
	}
	name, content, ok := readmeAt(commit)
	if !ok {
		return zip.ErrNotFound("no readme")
	}
	return c.JSON(http.StatusOK, readmeJSON{Path: name, Content: content, Encoding: "utf8"})
}

// ---- shared read helpers ----

// collectRefs folds a branch/tag iterator into name+sha rows, sorted by name.
func collectRefs(iter storer.ReferenceIter, err error) []refJSON {
	out := []refJSON{}
	if err != nil {
		return out
	}
	defer iter.Close()
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		out = append(out, refJSON{Name: ref.Name().Short(), SHA: ref.Hash().String()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// treeEntriesJSON lists the immediate children of a subtree, dirs first then files
// (the same order as the HTML treeEntries), each with its blob size + octal mode.
func treeEntriesJSON(commit *object.Commit, sub string) ([]treeEntryJSON, error) {
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	if sub != "" {
		if tree, err = tree.Tree(sub); err != nil {
			return nil, err
		}
	}
	dirs, files := []treeEntryJSON{}, []treeEntryJSON{}
	for i := range tree.Entries {
		e := tree.Entries[i]
		full := e.Name
		if sub != "" {
			full = sub + "/" + e.Name
		}
		if e.Mode == filemode.Dir {
			dirs = append(dirs, treeEntryJSON{Name: e.Name, Path: full, Type: "tree", Mode: modeString(e.Mode)})
			continue
		}
		var size int64
		if f, ferr := tree.TreeEntryFile(&e); ferr == nil {
			size = f.Size
		}
		files = append(files, treeEntryJSON{Name: e.Name, Path: full, Type: "blob", Size: size, Mode: modeString(e.Mode)})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return append(dirs, files...), nil
}

// readmeAt returns the tree-root README's filename + rendered-as-text content, if any.
// The ONE readme scan — the HTML repo home (ui.go) and the JSON /readme both use it.
func readmeAt(commit *object.Commit) (string, string, bool) {
	tree, err := commit.Tree()
	if err != nil {
		return "", "", false
	}
	for _, cand := range []string{"README.md", "README.MD", "Readme.md", "README", "readme.md"} {
		if f, ferr := tree.File(cand); ferr == nil {
			if bin, _ := f.IsBinary(); !bin {
				if txt, e := f.Contents(); e == nil {
					return cand, txt, true
				}
			}
		}
	}
	return "", "", false
}

func modeString(m filemode.FileMode) string { return fmt.Sprintf("%06o", uint32(m)) }

func shortHash(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
