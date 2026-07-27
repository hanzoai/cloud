// browse.go — Hanzo Git's JSON read/browse surface: the machine-readable twin of
// the server-rendered UI (ui.go). The console repo-browser (hanzoai/console
// components/products/git) consumes THESE endpoints; ui.go serves the same data as
// HTML for a plain browser. Both read through the ONE Repository model
// (repository.go), so the two surfaces can never drift and neither knows which
// version-control system is underneath.
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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// browseTarget resolves the caller's org, the named repo, its Repository, and the
// requested ref (?ref=, "" ⇒ HEAD/default) to a revision — the shared preamble for
// tree/blob/commits/readme. Mirrors the uiTree/uiBlob guard, org-scoped.
func browseTarget(s *cloud.Service[state], c *zip.Ctx) (Repository, Revision, error) {
	o, ok := org(c)
	if !ok {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	r, found := findRepo(s, c.Context(), o, normalizeName(c.Param("name")))
	if !found {
		return nil, "", zip.ErrNotFound("repo not found")
	}
	repo, err := openRepository(s, r)
	if err != nil {
		return nil, "", zip.ErrNotFound("empty repository")
	}
	rev, _, err := repo.Resolve(c.Context(), strings.TrimSpace(c.Query("ref")))
	if err != nil {
		return nil, "", zip.ErrNotFound("unknown ref")
	}
	return repo, rev, nil
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
	if repo, err := openRepository(s, r); err == nil {
		branches, tags, _ := repo.Refs(c.Context())
		out.Branches, out.Tags = refsToJSON(branches), refsToJSON(tags)
	}
	return c.JSON(http.StatusOK, out)
}

// browseTree lists the immediate children of a subtree at a ref, dirs first then files.
func browseTree(s *cloud.Service[state], c *zip.Ctx) error {
	repo, rev, err := browseTarget(s, c)
	if err != nil {
		return err
	}
	entries, err := repo.Tree(c.Context(), rev, cleanTreePath(c.Query("path")))
	if err != nil {
		return zip.ErrNotFound("no such directory")
	}
	out := make([]treeEntryJSON, 0, len(entries))
	for _, e := range entries {
		kind := "blob"
		if e.Dir {
			kind = "tree"
		}
		out = append(out, treeEntryJSON{Name: e.Name, Path: e.Path, Type: kind, Size: e.Size, Mode: e.Mode})
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": out})
}

// browseBlob returns a single file's bytes at a ref+path (utf8 inline, or base64 for
// binary; truncated past maxBlobBytes).
func browseBlob(s *cloud.Service[state], c *zip.Ctx) error {
	repo, rev, err := browseTarget(s, c)
	if err != nil {
		return err
	}
	fp := cleanTreePath(c.Query("path"))
	if fp == "" {
		return zip.ErrBadRequest("path is required")
	}
	b, err := repo.Blob(c.Context(), rev, fp, maxBlobBytes)
	if err != nil {
		if errors.Is(err, ErrNoPath) {
			return zip.ErrNotFound("no such file")
		}
		return zip.Errorf(http.StatusInternalServerError, "read blob: %v", err)
	}
	out := blobJSON{Path: b.Path, Size: b.Size, Encoding: "utf8", Truncated: b.Truncated, Binary: b.Binary}
	switch {
	case b.Truncated:
		// no content — the client is expected to clone instead
	case b.Binary:
		out.Encoding = "base64"
		out.Content = base64.StdEncoding.EncodeToString(b.Content)
	default:
		out.Content = string(b.Content)
	}
	return c.JSON(http.StatusOK, out)
}

// browseCommits returns the ref's history (or a file's history when ?path is set),
// newest first, capped at ?limit (default 50, max 100).
func browseCommits(s *cloud.Service[state], c *zip.Ctx) error {
	repo, rev, err := browseTarget(s, c)
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
	changes, err := repo.Log(c.Context(), rev, cleanTreePath(c.Query("path")), limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "log: %v", err)
	}
	out := []commitJSON{}
	for _, cm := range changes {
		out = append(out, commitJSON{
			SHA:         cm.Rev.String(),
			ShortSHA:    ShortRev(cm.Rev),
			Message:     firstLine(cm.Message),
			AuthorName:  cm.AuthorName,
			AuthorEmail: cm.AuthorEmail,
			Date:        cm.When.UTC().Format(time.RFC3339),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"commits": out})
}

// browseReadme returns the rendered-as-text README at the tree root; 404 when none.
func browseReadme(s *cloud.Service[state], c *zip.Ctx) error {
	repo, rev, err := browseTarget(s, c)
	if err != nil {
		return err
	}
	name, content, ok := readmeAt(c.Context(), repo, rev)
	if !ok {
		return zip.ErrNotFound("no readme")
	}
	return c.JSON(http.StatusOK, readmeJSON{Path: name, Content: content, Encoding: "utf8"})
}

// ---- shared read helpers ----

// refsToJSON projects the model's refs onto the wire shape the console expects.
func refsToJSON(refs []Ref) []refJSON {
	out := make([]refJSON, 0, len(refs))
	for _, r := range refs {
		out = append(out, refJSON{Name: r.Name, SHA: r.Rev.String()})
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
