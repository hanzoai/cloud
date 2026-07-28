// ui.go — Hanzo Git's web UI: the browser surface of the embedded, IAM-native
// git host. Server-rendered HTML in the ONE cloud binary (no separate app, no
// stock git-host image), reading the SAME org-scoped store + go-git object storage
// the API/protocol handlers use. This is what lets git.hanzo.ai retire the
// standalone git web app: repo list, repo home, tree browse, file view, commit log —
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
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// uiRoutes registers the browser UI. Called from routes() in git.go.
//
// TWO mount points, ONE handler set. The /git/* prefix serves the UI on EVERY
// host (base "/git") — this is how the console embeds the git browser. The root
// mount serves the SAME handlers GitHub-style at "/" on the DEDICATED git host
// only (base ""), so a browse URL matches the clone URL (git.hanzo.ai/<org>/<repo>
// ↔ git.hanzo.ai/<org>/<repo>.git). onGitHost gates the root routes to the git
// host; on api/console they fall through (c.Next()) to the console SPA catch-all,
// so a bare /:org/:repo can never shadow it there. They register AFTER the root
// smart-HTTP routes (git.go), whose paths carry a distinct /info/refs |
// /git-*-pack tail, so a 2-segment UI route and a clone route never collide.
func uiRoutes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/git", cloud.Handle(s, uiHome))
	app.Get("/git/explore", cloud.Handle(s, uiExplore))
	app.Get("/git/:org/:repo", cloud.Handle(s, uiRepo))
	app.Get("/git/:org/:repo/tree/*", cloud.Handle(s, uiTree))
	app.Get("/git/:org/:repo/blob/*", cloud.Handle(s, uiBlob))
	app.Get("/git/:org/:repo/commits", cloud.Handle(s, uiCommits))

	onGit := onGitHost(s.State.gitHost)
	app.Get("/", onGit(cloud.Handle(s, uiHome)))
	app.Get("/explore", onGit(cloud.Handle(s, uiExplore)))
	app.Get("/:org/:repo", onGit(cloud.Handle(s, uiRepo)))
	app.Get("/:org/:repo/tree/*", onGit(cloud.Handle(s, uiTree)))
	app.Get("/:org/:repo/blob/*", onGit(cloud.Handle(s, uiBlob)))
	app.Get("/:org/:repo/commits", onGit(cloud.Handle(s, uiCommits)))
}

// uiBase is the URL base the UI links against for this request: "" on the
// dedicated git host (git.hanzo.ai serves the UI at the ROOT, GitHub-style, so a
// browse URL matches the clone URL) and "/git" everywhere else (the console
// embeds the git browser under /git). One canonical URL per host — never both.
func uiBase(s *cloud.Service[state], c *zip.Ctx) string {
	if h := s.State.gitHost; h != "" && strings.EqualFold(c.Fiber().Hostname(), h) {
		return ""
	}
	return "/git"
}

// uiCloneURL is the canonical clone URL the UI shows: the clean git-host form
// (https://git.hanzo.ai/<org>/<repo>.git) the root smart-HTTP routes serve, not
// the /v1/git-prefixed API form. Falls back to the API cloneURL when no git host
// is configured.
func uiCloneURL(s *cloud.Service[state], org, name string) string {
	if h := s.State.gitHost; h != "" {
		return fmt.Sprintf("https://%s/%s/%s.git", h, org, name)
	}
	return cloneURL(s, org, name)
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

// uiRepoAccess resolves the :org/:repo path for a browser READ, allowing anonymous
// access to PUBLIC repos while keeping private repos org-authed. Returns the repo's
// org + metadata. A private-or-missing repo answers the SAME 404 so anonymous
// probing can't distinguish existence (mirrors smart-HTTP's resolvePackRepo).
func uiRepoAccess(s *cloud.Service[state], c *zip.Ctx) (string, Repo, error) {
	pathOrg := c.Param("org")
	if pathOrg == "" || !orgRE.MatchString(pathOrg) {
		return "", Repo{}, zip.Errorf(http.StatusNotFound, "no such repository")
	}
	name := normalizeName(c.Param("repo"))
	r, ok := findRepo(s, c.Context(), pathOrg, name)
	if !ok {
		return "", Repo{}, zip.Errorf(http.StatusNotFound, "no such repository")
	}
	if r.Public {
		return pathOrg, r, nil // OSS: anyone reads
	}
	if authedOrg, authed := org(c); authed && authedOrg == pathOrg {
		return pathOrg, r, nil // private: only the owning org
	}
	return "", Repo{}, zip.Errorf(http.StatusNotFound, "no such repository")
}

func uiHome(s *cloud.Service[state], c *zip.Ctx) error {
	// Signed out ⇒ the public explore/landing (OSS-first). Signed in ⇒ your org's
	// repos. Both public and private show for the authed org; the world sees public.
	o, ok := org(c)
	if !ok || o == "" {
		return uiExplore(s, c)
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
	base := uiBase(s, c)
	return render(c, base, http.StatusOK, "Repositories", homeTmpl, homeData{Base: base, Org: o, Repos: rows})
}

func uiRepo(s *cloud.Service[state], c *zip.Ctx) error {
	o, r, err := uiRepoAccess(s, c)
	if err != nil {
		return err
	}
	ref := strings.TrimSpace(c.Query("ref"))
	base := uiBase(s, c)
	d := repoData{Base: base, Org: o, Repo: r.Name, Description: r.Description,
		CloneHTTP: uiCloneURL(s, o, r.Name), CloneSSH: sshURL(s, o, r.Name)}

	repo, err := openRepository(s, r)
	if err == nil {
		if branches := branchList(c.Context(), repo); len(branches) > 0 {
			d.Branches = branches
		}
		if rev, label, e := repo.Resolve(c.Context(), ref); e == nil {
			d.Ref = label
			d.Entries = treeEntries(c.Context(), repo, rev, "", o, r.Name, label, base)
			d.Commits = recentCommits(c.Context(), repo, rev, 10)
			if _, readme, ok := readmeAt(c.Context(), repo, rev); ok {
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
	return render(c, base, http.StatusOK, r.Name, repoTmpl, d)
}

func uiTree(s *cloud.Service[state], c *zip.Ctx) error {
	o, r, err := uiRepoAccess(s, c)
	if err != nil {
		return err
	}
	repo, err := openRepository(s, r)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "empty repository")
	}
	ref := strings.TrimSpace(c.Query("ref"))
	rev, label, err := repo.Resolve(c.Context(), ref)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "unknown ref")
	}
	sub := cleanTreePath(c.Fiber().Params("*"))
	base := uiBase(s, c)
	return render(c, base, http.StatusOK, r.Name+"/"+sub, treeTmpl, treeData{
		Base: base, Org: o, Repo: r.Name, Ref: label, Path: sub,
		Crumbs:  crumbs(o, r.Name, label, sub, base),
		Entries: treeEntries(c.Context(), repo, rev, sub, o, r.Name, label, base),
	})
}

func uiBlob(s *cloud.Service[state], c *zip.Ctx) error {
	o, r, err := uiRepoAccess(s, c)
	if err != nil {
		return err
	}
	repo, err := openRepository(s, r)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "empty repository")
	}
	ref := strings.TrimSpace(c.Query("ref"))
	rev, label, err := repo.Resolve(c.Context(), ref)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "unknown ref")
	}
	fp := cleanTreePath(c.Fiber().Params("*"))
	blob, err := repo.Blob(c.Context(), rev, fp, 0)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "no such file")
	}
	base := uiBase(s, c)
	d := blobData{Base: base, Org: o, Repo: r.Name, Ref: label, Path: fp,
		Crumbs: crumbs(o, r.Name, label, fp, base), Size: humanBytes(blob.Size)}
	if blob.Binary {
		d.Binary = true
	} else {
		d.Content = string(blob.Content)
		d.Lines = strings.Count(d.Content, "\n") + 1
	}
	return render(c, base, http.StatusOK, r.Name+"/"+fp, blobTmpl, d)
}

func uiCommits(s *cloud.Service[state], c *zip.Ctx) error {
	o, r, err := uiRepoAccess(s, c)
	if err != nil {
		return err
	}
	repo, err := openRepository(s, r)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "empty repository")
	}
	ref := strings.TrimSpace(c.Query("ref"))
	rev, label, err := repo.Resolve(c.Context(), ref)
	if err != nil {
		return zip.Errorf(http.StatusNotFound, "unknown ref")
	}
	base := uiBase(s, c)
	return render(c, base, http.StatusOK, r.Name+" commits", commitsTmpl, commitsData{
		Base: base, Org: o, Repo: r.Name, Ref: label, Commits: recentCommits(c.Context(), repo, rev, 100),
	})
}

// ---- read helpers (Repository model — see repository.go) ----

func branchList(ctx context.Context, repo Repository) []string {
	branches, _, err := repo.Refs(ctx)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(branches))
	for _, b := range branches {
		out = append(out, b.Name)
	}
	return out
}

// treeEntries lists the immediate children of a subtree, dirs first then files,
// each with a UI link (base-prefixed) that carries the ref. Ordering comes from
// the model, which sorts dirs then files by name.
func treeEntries(ctx context.Context, r Repository, rev Revision, sub, org, repo, ref, base string) []entry {
	rows, err := r.Tree(ctx, rev, sub)
	if err != nil {
		return nil
	}
	out := make([]entry, 0, len(rows))
	q := "?ref=" + template.URLQueryEscaper(ref)
	for _, e := range rows {
		kind := "blob"
		if e.Dir {
			kind = "tree"
		}
		out = append(out, entry{Name: e.Name, IsDir: e.Dir,
			Href: base + "/" + org + "/" + repo + "/" + kind + "/" + e.Path + q})
	}
	return out
}

// recentCommits renders the newest n changes from rev. The short form here is
// EIGHT characters — the HTML surface has always shown eight where the JSON
// browse shows seven (ShortRev). Left as-is rather than silently changing a
// rendered page.
func recentCommits(ctx context.Context, r Repository, rev Revision, n int) []commitRow {
	changes, err := r.Log(ctx, rev, "", n)
	if err != nil {
		return nil
	}
	var out []commitRow
	for _, c := range changes {
		short := c.Rev.String()
		if len(short) > 8 {
			short = short[:8]
		}
		out = append(out, commitRow{
			Short: short, Message: firstLine(c.Message),
			Author: c.AuthorName, When: c.When.UTC().Format("2006-01-02 15:04"),
		})
	}
	return out
}

// crumbs builds path breadcrumbs, each linking (base-prefixed) to its tree.
func crumbs(org, repo, ref, p, base string) []crumb {
	out := []crumb{{Name: repo, Href: base + "/" + org + "/" + repo + "?ref=" + template.URLQueryEscaper(ref)}}
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
			Href: base + "/" + org + "/" + repo + "/tree/" + acc + "?ref=" + template.URLQueryEscaper(ref)})
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
