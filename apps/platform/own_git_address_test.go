package platform

// own_git_address_test.go — "Add to project" on a GitHub repository whose
// owner is not the Hanzo org's name (#766).
//
// The builder (hanzoai/build, src/landing.tsx) sends a GitHub row as this
// deployment's own git address, https://api.hanzo.ai/v1/git/<github owner>/<name>.git.
// That address is the embedded git server's, whose first segment is a HANZO org:
// for a GitHub owner that is not the caller's org it names no repository the build
// can fetch, and the build failed minutes later with "failed to fetch remote ...:
// not found". It is read as the GitHub repository it spells, in the caller's view,
// exactly as `owner/name` is.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// servesOwnGit is this deployment's own git apex as production's Mount sets it
// (brand.Apex of api.hanzo.ai), so its own addresses are a build source.
func servesOwnGit(t *testing.T) {
	t.Helper()
	was := selfGitHost
	selfGitHost = "hanzo.ai"
	t.Cleanup(func() { selfGitHost = was })
}

func TestTheBuildersAddressForAGitHubRowBuildsFromGitHub(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servesOwnGit(t)
		h := newDeployHarness(t)
		for _, admin := range []bool{true, false} {
			out := h.deploy(t, admin, map[string]any{
				"repo": "https://api.hanzo.ai/v1/git/acme-gh/web.git", "ref": "trunk",
				"name": "web", "partOf": "web", "mode": "branch",
			})
			if out.Build == nil || out.Build.Repo != "https://github.com/acme-gh/web.git" || out.Build.Ref != "trunk" {
				t.Fatalf("admin=%v: the build must clone GitHub's repository: %+v", admin, out.Build)
			}
			if out.App.Name != "web" {
				t.Fatalf("admin=%v: app %q", admin, out.App.Name)
			}
		}
		// Asked as the caller, as owner/name is: their org, their person.
		for _, who := range h.peer.callers() {
			if who.Org != "acme" || who.User != "u-acme" {
				t.Fatalf("the lookup must carry the caller: %+v", who)
			}
		}
		// The same spelling without .git, and in another case, is the same repository.
		out := h.deploy(t, true, map[string]any{"repo": "https://API.hanzo.ai/v1/git/acme-gh/web", "mode": "branch"})
		if out.Build == nil || out.Build.Repo != "https://github.com/acme-gh/web.git" {
			t.Fatalf("want GitHub's repository: %+v", out.Build)
		}
	})
}

// A repository outside the caller's GitHub view is 404 BEFORE anything is built,
// where it used to launch a build that could only fail.
func TestTheBuildersAddressOutsideTheCallersViewIsNotFound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servesOwnGit(t)
		h := newDeployHarness(t)
		for _, tc := range []struct {
			admin bool
			repo  string
		}{
			{true, "https://api.hanzo.ai/v1/git/globex/secret.git"},
			{false, "https://api.hanzo.ai/v1/git/acme-gh/ops.git"},
		} {
			code, body := h.post(t, tc.admin, map[string]any{"repo": tc.repo, "name": "x", "mode": "branch"})
			if code != http.StatusNotFound {
				t.Errorf("admin=%v %s: want 404, got %d %s", tc.admin, tc.repo, code, body)
			}
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a repository nobody may see launched %d build(s)", n)
		}
	})
}

// The org's OWN address on this host, and every other URL, are what they were:
// an org admin's to build as given, and asked of no GitHub lookup.
func TestTheOrgsOwnGitAddressIsUnchanged(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servesOwnGit(t)
		h := newDeployHarness(t)
		for _, repo := range []string{
			"https://api.hanzo.ai/v1/git/acme/web.git",
			"https://api.hanzo.ai/v1/git/acme-gh/proj/web.git",
			"https://gitlab.com/acme-gh/web.git",
		} {
			out := h.deploy(t, true, map[string]any{"repo": repo, "name": "web", "mode": "branch"})
			if out.Build == nil || out.Build.Repo != repo {
				t.Fatalf("%s: built %+v", repo, out.Build)
			}
			if code, _ := h.post(t, false, map[string]any{"repo": repo, "name": "web", "mode": "branch"}); code != http.StatusForbidden {
				t.Fatalf("%s: a member building it want 403, got %d", repo, code)
			}
		}
		if got := h.peer.callers(); len(got) != 0 {
			t.Fatalf("no GitHub lookup for these: %+v", got)
		}
	})
}

// A PRIVATE GITHUB REPOSITORY THE CALLER MAY NOT READ IS REFUSED BEFORE ANYTHING
// IS BUILT (#766). Its build is handed a token minted as the caller
// (checkout.go), and the mint is asked before a name is claimed or a review
// reserved, so a caller GitHub mints nothing for (no push of their own, no admin
// of the installation) gets that refusal naming the repository, by either
// spelling. No Job is launched and no name is claimed for it.
func TestAPrivateGitHubRepoTheCallerMayNotReadIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servesOwnGit(t)
		h := newDeployHarness(t)
		ns := serveNamespace(t, map[string]string{})
		for _, admin := range []bool{true, false} {
			for _, repo := range []string{"acme-gh/secret-site", "https://api.hanzo.ai/v1/git/acme-gh/secret-site.git"} {
				code, body := h.post(t, admin, map[string]any{"repo": repo, "mode": "branch"})
				if code != http.StatusForbidden || !strings.Contains(string(body), "acme-gh/secret-site") {
					t.Errorf("admin=%v %s: want 403 naming the repository, got %d %s", admin, repo, code, body)
				}
			}
		}
		if got := h.peer.minted(); len(got) != 4 {
			t.Fatalf("want one mint asked per deploy, got %+v", got)
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a private repository launched %d build(s)", n)
		}
		ns.mu.Lock()
		defer ns.mu.Unlock()
		if len(ns.asked) != 0 {
			t.Fatalf("a refused deploy claimed names: %v", ns.asked)
		}
	})
}
