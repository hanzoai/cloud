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
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// uiAccess is the read rule every repo-addressed page shares, because they all
// go through uiRepoAccess. Stated once so six descriptions cannot become six
// accounts of one gate.
const uiAccess = " A public repository is readable by anyone; a private one only " +
	"by its own org. A repository that does not exist and one belonging to another " +
	"org answer the SAME 404, so the page is never an existence oracle."

// uiHTML is the fact an SDK or CLI consumer most needs about these twelve
// operations and cannot see from the operationId: they are pages, not data.
const uiHTML = " This is a server-rendered browser page, not JSON — the console " +
	"repo-browser reads the same repository through the JSON ops under /v1/git. " +
	"Repository names, paths and file contents all render through auto-escaping " +
	"templates rather than being concatenated into HTML."

// The two mounts, one handler set. Which mount a page is on is a real difference
// in who can reach it, so each says which it is.
const (
	uiEverywhere = " Served on every host, which is how the console embeds the git " +
		"browser under /git."
	uiGitHostOnly = " Served only on the dedicated git host, where a browse URL " +
		"matches the clone URL; on the API and console hosts it falls through to " +
		"their own routes, so it can never shadow them."
)

// The prose for git's twelve browser pages. They are HTML handlers, so no typed
// op can carry them and zipdoc has nothing to lift — left bare, every one of them
// reached the published document as an operationId and a tag, indistinguishable
// from a JSON route that takes no input. openapi.Describe attaches prose to a
// route the router already carries; it can no more add a page than Register can
// add an operation.
//
// One table, two mounts: the SAME six handlers answer under /git on every host
// and at the root on the git host, so the descriptions are generated in pairs
// from one row and cannot drift apart.
func init() {
	for _, p := range []struct{ embedded, root, summary, lead string }{
		{"/git", "/", "Browse your org's repositories",
			"The repository list for the signed-in caller's org — each repo with its " +
				"description, default branch, size and last update. SIGNED OUT it renders " +
				"the public explore page instead of refusing, because most Hanzo repos are " +
				"open source and the open face is the default one; signed in, the caller's " +
				"own org shows its private repositories alongside its public ones."},
		{"/git/explore", "/explore", "Discover public repositories across every org",
			"The open, unauthenticated face of the git host: every PUBLIC repository in " +
				"the fleet, org-qualified, so a project can be found and cloned with no " +
				"account at all — signing in is for private repos and for writes. " +
				"Repositories live in per-org stores with no global index, so this unions " +
				"each org's public rows and is bounded to a fixed number of stores per " +
				"request, keeping discovery quick however many orgs exist. A fleet with no " +
				"orgs yet is an empty page, not an error."},
		{"/git/:org/:repo", "/:org/:repo", "Open a repository's home page",
			"A repository at a glance: its branches, the tree at the tip, its most recent " +
				"commits, its README rendered, and the HTTPS and SSH clone URLs. `?ref=` " +
				"selects a branch, tag or commit; the default branch is used when it is " +
				"omitted. A repository with no commits yet renders its clone instructions " +
				"rather than an error, which is what a caller who has just created one " +
				"needs to see."},
		{"/git/:org/:repo/tree/*", "/:org/:repo/tree/*", "Browse a directory inside a repository",
			"The contents of one directory at one revision, with breadcrumbs back up and " +
				"links onward into subdirectories and files. The path after /tree/ is the " +
				"directory and `?ref=` selects the branch, tag or commit, defaulting to the " +
				"repository's own default branch. An unknown ref is 404, as is a repository " +
				"with no commits."},
		{"/git/:org/:repo/blob/*", "/:org/:repo/blob/*", "View a file in a repository",
			"One file's contents at one revision, with its size and line count. A BINARY " +
				"file is reported as binary rather than dumped into the page. The path " +
				"after /blob/ is the file and `?ref=` selects the branch, tag or commit. An " +
				"unknown ref or a path that is not a file in it is 404."},
		{"/git/:org/:repo/commits", "/:org/:repo/commits", "Read a repository's commit log",
			"The hundred most recent commits on one ref, each with its author, message " +
				"and date. `?ref=` selects the branch, tag or commit, defaulting to the " +
				"repository's default branch; an unknown one is 404."},
	} {
		openapi.Describe(p.embedded, http.MethodGet, p.summary, p.lead+access(p.embedded)+uiHTML+uiEverywhere)
		openapi.Describe(p.root, http.MethodGet, p.summary, p.lead+access(p.root)+uiHTML+uiGitHostOnly)
	}
}

// access returns the read rule for a page, which applies to exactly the pages
// that address a repository — the two listing pages gate per ROW instead, so
// claiming the repo rule on them would be false.
func access(path string) string {
	if strings.Contains(path, ":repo") {
		return uiAccess
	}
	return ""
}

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
//
// Every route here stays raw because it serves a server-rendered HTML page to
// a browser, not JSON; the JSON twin of each page is a typed op under
// /v1/git/repos/:name (git.go).
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
func uiCloneURL(s *cloud.Service[state], org, project, name string) string {
	if h := s.State.gitHost; h != "" {
		if project == "" {
			return fmt.Sprintf("https://%s/%s/%s.git", h, org, name)
		}
		return fmt.Sprintf("https://%s/%s/%s/%s.git", h, org, project, name)
	}
	return cloneURL(s, org, project, name)
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
		CloneHTTP: uiCloneURL(s, o, r.Project, r.Name), CloneSSH: sshURL(s, o, r.Project, r.Name)}

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
	for seg := range strings.SplitSeq(p, "/") {
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
