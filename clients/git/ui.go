// ui.go — Hanzo Git's web UI: the browser surface of the embedded, IAM-native
// git host. Server-rendered HTML in the ONE cloud binary (no separate app, no
// stock-Gitea image), reading the SAME org-scoped store + go-git object storage
// the API/protocol handlers use. This is what lets git.hanzo.ai retire the
// standalone Gitea: repo list, repo home, tree browse, file view, commit log —
// all native.
//
// Isolation is identical to the rest of git: every page is scoped to the
// gateway-minted, IAM-VALIDATED X-Org-Id (org(c)); the :org path segment MUST
// equal the caller's own org, so the UI can never browse another tenant's repos.
// html/template auto-escaping is the XSS boundary — repo names, paths, and file
// contents are all rendered through it, never concatenated into HTML.
//
// Routes (browser, distinct from the /v1/git API + smart-HTTP protocol):
//
//	GET /git                         the caller's org repo list (home)
//	GET /git/:org/:repo              repo home: branches, HEAD, root tree, clone
//	GET /git/:org/:repo/tree/*?ref=  browse a subtree
//	GET /git/:org/:repo/blob/*?ref=  view a file
//	GET /git/:org/:repo/commits?ref= commit log
package git

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// uiRoutes registers the browser UI. Called from routes() in git.go.
func uiRoutes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/git", cloud.Handle(s, uiHome))
	app.Get("/git/:org/:repo", cloud.Handle(s, uiRepo))
	app.Get("/git/:org/:repo/tree/*", cloud.Handle(s, uiTree))
	app.Get("/git/:org/:repo/blob/*", cloud.Handle(s, uiBlob))
	app.Get("/git/:org/:repo/commits", cloud.Handle(s, uiCommits))
}

// uiOrg resolves the caller's validated org and enforces the :org path segment
// (when present) matches it — the same path-vs-identity guard the smart-HTTP
// handlers apply, so the UI is not a cross-tenant read hole.
func uiOrg(c *zip.Ctx) (string, error) {
	o, ok := org(c)
	if !ok || o == "" {
		return "", zip.ErrForbidden("sign in to view Hanzo Git")
	}
	if seg := c.Param("org"); seg != "" && seg != o {
		return "", zip.ErrForbidden("repository belongs to another organization")
	}
	return o, nil
}

// findRepo resolves a repo by name within the caller's org, returning its full
// metadata (so we get the Project sub-scope needed to open storage). Org-scoped:
// a name outside the caller's org is simply not found.
func findRepo(s *cloud.Service[state], ctx context.Context, org, name string) (Repo, bool) {
	st, err := storeFor(s, org)
	if err != nil {
		return Repo{}, false
	}
	repos, err := st.ListOrg(ctx, org)
	if err != nil {
		return Repo{}, false
	}
	for _, r := range repos {
		if r.Name == name {
			return r, true
		}
	}
	return Repo{}, false
}

// openGit opens the bare go-git repository backing a stored repo (read side).
func openGit(s *cloud.Service[state], r Repo) (*gogit.Repository, error) {
	storer, err := s.State.storage.storer(r.Org, r.Project, r.Name)
	if err != nil {
		return nil, err
	}
	return gogit.Open(storer, nil)
}

// resolveRef resolves a ref name (branch, tag, or hash; "" ⇒ HEAD/default) to a
// commit, returning the commit and the effective ref label for display.
func resolveRef(repo *gogit.Repository, r Repo, ref string) (*object.Commit, string, error) {
	label := ref
	if label == "" {
		if h, err := repo.Head(); err == nil {
			label = h.Name().Short()
		} else {
			label = firstNonEmptyStr(r.DefaultBranch, defaultBranchName)
		}
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(label))
	if err != nil {
		// Fall back to the branch ref form for a bare branch name.
		if ref2, e2 := repo.Reference(plumbing.NewBranchReferenceName(label), true); e2 == nil {
			h := ref2.Hash()
			hash = &h
		} else {
			return nil, label, err
		}
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, label, err
	}
	return commit, label, nil
}

// cleanTreePath normalizes a browse path to a tree-relative, traversal-free
// path ("" for the root). It is NOT a filesystem path — go-git resolves it
// within the commit tree — but we still reject "." / ".." defensively.
func cleanTreePath(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/")
}

// ---- handlers ----

func uiHome(s *cloud.Service[state], c *zip.Ctx) error {
	o, err := uiOrg(c)
	if err != nil {
		return err
	}
	st, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	repos, err := st.ListOrg(c.Context(), o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list repos: %v", err)
	}
	rows := make([]repoRow, 0, len(repos))
	for _, r := range repos {
		rows = append(rows, repoRow{Name: r.Name, Description: r.Description,
			DefaultBranch: firstNonEmptyStr(r.DefaultBranch, defaultBranchName),
			Size:          humanBytes(r.SizeBytes), Updated: rfc3339(r.UpdatedAt)})
	}
	return render(c, http.StatusOK, "Repositories", homeTmpl, homeData{Org: o, Repos: rows})
}

func uiRepo(s *cloud.Service[state], c *zip.Ctx) error {
	o, err := uiOrg(c)
	if err != nil {
		return err
	}
	name := normalizeName(c.Param("repo"))
	r, ok := findRepo(s, c.Context(), o, name)
	if !ok {
		return zip.Errorf(http.StatusNotFound, "no such repository")
	}
	ref := strings.TrimSpace(c.Query("ref"))
	d := repoData{Org: o, Repo: r.Name, Description: r.Description,
		CloneHTTP: cloneURL(s, o, r.Name), CloneSSH: sshURL(s, o, r.Name)}

	repo, err := openGit(s, r)
	if err == nil {
		if branches := branchList(repo); len(branches) > 0 {
			d.Branches = branches
		}
		if commit, label, e := resolveRef(repo, r, ref); e == nil {
			d.Ref = label
			d.Entries = treeEntries(commit, "", o, r.Name, label)
			d.Commits = recentCommits(repo, commit, 10)
			if readme := findReadme(commit); readme != "" {
				d.Readme = readme
			}
		} else {
			d.Empty = true
		}
	} else {
		d.Empty = true
	}
	if d.Ref == "" {
		d.Ref = firstNonEmptyStr(r.DefaultBranch, defaultBranchName)
	}
	return render(c, http.StatusOK, r.Name, repoTmpl, d)
}

func uiTree(s *cloud.Service[state], c *zip.Ctx) error {
	o, err := uiOrg(c)
	if err != nil {
		return err
	}
	name := normalizeName(c.Param("repo"))
	r, ok := findRepo(s, c.Context(), o, name)
	if !ok {
		return zip.Errorf(http.StatusNotFound, "no such repository")
	}
	repo, err := openGit(s, r)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "empty repository")
	}
	ref := strings.TrimSpace(c.Query("ref"))
	commit, label, err := resolveRef(repo, r, ref)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "unknown ref")
	}
	sub := cleanTreePath(c.Fiber().Params("*"))
	return render(c, http.StatusOK, r.Name+"/"+sub, treeTmpl, treeData{
		Org: o, Repo: r.Name, Ref: label, Path: sub,
		Crumbs:  crumbs(o, r.Name, label, sub),
		Entries: treeEntries(commit, sub, o, r.Name, label),
	})
}

func uiBlob(s *cloud.Service[state], c *zip.Ctx) error {
	o, err := uiOrg(c)
	if err != nil {
		return err
	}
	name := normalizeName(c.Param("repo"))
	r, ok := findRepo(s, c.Context(), o, name)
	if !ok {
		return zip.Errorf(http.StatusNotFound, "no such repository")
	}
	repo, err := openGit(s, r)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "empty repository")
	}
	ref := strings.TrimSpace(c.Query("ref"))
	commit, label, err := resolveRef(repo, r, ref)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "unknown ref")
	}
	fp := cleanTreePath(c.Fiber().Params("*"))
	file, err := commit.File(fp)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "no such file")
	}
	d := blobData{Org: o, Repo: r.Name, Ref: label, Path: fp,
		Crumbs: crumbs(o, r.Name, label, fp), Size: humanBytes(file.Size)}
	if bin, _ := file.IsBinary(); bin {
		d.Binary = true
	} else if txt, e := file.Contents(); e == nil {
		d.Content = txt
		d.Lines = strings.Count(txt, "\n") + 1
	}
	return render(c, http.StatusOK, r.Name+"/"+fp, blobTmpl, d)
}

func uiCommits(s *cloud.Service[state], c *zip.Ctx) error {
	o, err := uiOrg(c)
	if err != nil {
		return err
	}
	name := normalizeName(c.Param("repo"))
	r, ok := findRepo(s, c.Context(), o, name)
	if !ok {
		return zip.Errorf(http.StatusNotFound, "no such repository")
	}
	repo, err := openGit(s, r)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "empty repository")
	}
	ref := strings.TrimSpace(c.Query("ref"))
	commit, label, err := resolveRef(repo, r, ref)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "unknown ref")
	}
	return render(c, http.StatusOK, r.Name+" commits", commitsTmpl, commitsData{
		Org: o, Repo: r.Name, Ref: label, Commits: recentCommits(repo, commit, 100),
	})
}

// ---- go-git read helpers ----

func branchList(repo *gogit.Repository) []string {
	iter, err := repo.Branches()
	if err != nil {
		return nil
	}
	defer iter.Close()
	var out []string
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		out = append(out, ref.Name().Short())
		return nil
	})
	sort.Strings(out)
	return out
}

// treeEntries lists the immediate children of a subtree, dirs first then files,
// each with a UI link that carries the ref.
func treeEntries(commit *object.Commit, sub, org, repo, ref string) []entry {
	tree, err := commit.Tree()
	if err != nil {
		return nil
	}
	if sub != "" {
		tree, err = tree.Tree(sub)
		if err != nil {
			return nil
		}
	}
	var dirs, files []entry
	for _, e := range tree.Entries {
		child := e.Name
		full := e.Name
		if sub != "" {
			full = sub + "/" + e.Name
		}
		q := "?ref=" + template.URLQueryEscaper(ref)
		if e.Mode == 0o40000 { // directory
			dirs = append(dirs, entry{Name: child, IsDir: true,
				Href: "/git/" + org + "/" + repo + "/tree/" + full + q})
		} else {
			files = append(files, entry{Name: child,
				Href: "/git/" + org + "/" + repo + "/blob/" + full + q})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return append(dirs, files...)
}

func recentCommits(repo *gogit.Repository, from *object.Commit, n int) []commitRow {
	iter, err := repo.Log(&gogit.LogOptions{From: from.Hash})
	if err != nil {
		return nil
	}
	defer iter.Close()
	var out []commitRow
	for i := 0; i < n; i++ {
		c, err := iter.Next()
		if err != nil {
			break
		}
		msg := c.Message
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		out = append(out, commitRow{
			Short: c.Hash.String()[:8], Message: msg,
			Author: c.Author.Name, When: c.Author.When.UTC().Format("2006-01-02 15:04"),
		})
	}
	return out
}

// findReadme returns the rendered-as-text README at the tree root, if any.
func findReadme(commit *object.Commit) string {
	tree, err := commit.Tree()
	if err != nil {
		return ""
	}
	for _, cand := range []string{"README.md", "README.MD", "Readme.md", "README", "readme.md"} {
		if f, err := tree.File(cand); err == nil {
			if bin, _ := f.IsBinary(); !bin {
				if txt, e := f.Contents(); e == nil {
					return txt
				}
			}
		}
	}
	return ""
}

// crumbs builds path breadcrumbs, each linking to its tree.
func crumbs(org, repo, ref, p string) []crumb {
	out := []crumb{{Name: repo, Href: "/git/" + org + "/" + repo + "?ref=" + template.URLQueryEscaper(ref)}}
	if p == "" {
		return out
	}
	acc := ""
	for _, seg := range strings.Split(p, "/") {
		if acc == "" {
			acc = seg
		} else {
			acc = acc + "/" + seg
		}
		out = append(out, crumb{Name: seg,
			Href: "/git/" + org + "/" + repo + "/tree/" + acc + "?ref=" + template.URLQueryEscaper(ref)})
	}
	return out
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
