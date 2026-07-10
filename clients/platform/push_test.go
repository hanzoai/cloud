package platform

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
)

// seedGitApp writes a project + one git-source app tracking repoURL@branch.
func seedGitApp(t *testing.T, s *svc, org, slug, repoURL, branch string) Application {
	t.Helper()
	ctx := context.Background()
	proj := Project{ID: "proj_" + org + "_" + slug, Org: org, Slug: slug, Name: slug, CreatedAt: 1, UpdatedAt: 1}
	if err := s.store.CreateProject(ctx, proj); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	a := Application{
		ID: "app_" + org + "_" + slug, Org: org, ProjectID: proj.ID, Slug: slug, Name: slug,
		Environment: "production", Source: "git", RepoURL: repoURL, RepoBranch: branch, RepoProvider: "hanzo",
		BuildType: "pack", Port: 3000, Replicas: 1, EnvJSON: "[]", DomainsJSON: "[]",
		Status: "created", Namespace: tenantNamespace(org), CreatedAt: 1, UpdatedAt: 1,
	}
	if err := s.store.CreateApplication(ctx, a); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	return a
}

// pushSvc mounts an svc over a ready fake cluster (no HTTP routes needed —
// buildFromPush is called directly) and trusts the embedded-git apex as a build
// source, exactly as platform.Mount does from deps.Domain.
func pushSvc(t *testing.T) *svc {
	t.Helper()
	_, s := mountSvcK8s(t, fakeK8s())
	prev := selfGitHost
	selfGitHost = "git.hanzo.ai"
	t.Cleanup(func() { selfGitHost = prev })
	return s
}

// A push whose repo+branch matches a git app launches a build: the app flips to
// "building" and a "building" deployment for the pushed commit is recorded.
func TestBuildFromPush_LaunchesMatchingApp(t *testing.T) {
	ctx := context.Background()
	s := pushSvc(t)
	const clone = "https://git.hanzo.ai/v1/git/acme/site.git"
	a := seedGitApp(t, s, "acme", "site", "https://git.hanzo.ai/v1/git/acme/site", "main")

	// CloneURL carries the ".git" suffix; the app RepoURL does not — sameRepo must
	// still match after normalization.
	err := s.buildFromPush(ctx, mkPushEvent("acme", "site", "main", "deadbeefcafe", clone))
	if err != nil {
		t.Fatalf("buildFromPush: %v", err)
	}
	got, err := s.store.GetApplicationByID(ctx, "acme", a.ID)
	if err != nil {
		t.Fatalf("reload app: %v", err)
	}
	if got.Status != "building" {
		t.Fatalf("app status: want building, got %q", got.Status)
	}
	deps, err := s.store.ListDeployments(ctx, "acme", a.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 1 || deps[0].Status != "building" || deps[0].Source != "git" || deps[0].Commit != "deadbeefcafe" {
		t.Fatalf("unexpected deployments: %+v", deps)
	}
}

// A push to a branch no app tracks is a no-op: no deployment, no error.
func TestBuildFromPush_NoMatchIsNoop(t *testing.T) {
	ctx := context.Background()
	s := pushSvc(t)
	a := seedGitApp(t, s, "acme", "site", "https://git.hanzo.ai/v1/git/acme/site", "main")

	// Right repo, wrong branch.
	if err := s.buildFromPush(ctx, mkPushEvent("acme", "site", "feature", "abc123", "https://git.hanzo.ai/v1/git/acme/site.git")); err != nil {
		t.Fatalf("buildFromPush (wrong branch): %v", err)
	}
	// Right branch, different repo.
	if err := s.buildFromPush(ctx, mkPushEvent("acme", "other", "main", "abc123", "https://git.hanzo.ai/v1/git/acme/other.git")); err != nil {
		t.Fatalf("buildFromPush (other repo): %v", err)
	}
	deps, err := s.store.ListDeployments(ctx, "acme", a.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("expected no deployments, got %+v", deps)
	}
}

// An image-source app matching the repo URL is never built by a push (git only).
func TestBuildFromPush_IgnoresImageApp(t *testing.T) {
	ctx := context.Background()
	s := pushSvc(t)
	proj := Project{ID: "proj_acme_api", Org: "acme", Slug: "api", Name: "api", CreatedAt: 1, UpdatedAt: 1}
	if err := s.store.CreateProject(ctx, proj); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	img := Application{
		ID: "app_acme_api", Org: "acme", ProjectID: proj.ID, Slug: "api", Name: "api",
		Source: "image", RepoURL: "https://git.hanzo.ai/v1/git/acme/api", ImageRepo: "ghcr.io/hanzoai/api", ImageTag: "1",
		EnvJSON: "[]", DomainsJSON: "[]", Status: "live", CreatedAt: 1, UpdatedAt: 1,
	}
	if err := s.store.CreateApplication(ctx, img); err != nil {
		t.Fatalf("seed image app: %v", err)
	}
	if err := s.buildFromPush(ctx, mkPushEvent("acme", "api", "main", "abc123", "https://git.hanzo.ai/v1/git/acme/api.git")); err != nil {
		t.Fatalf("buildFromPush: %v", err)
	}
	deps, err := s.store.ListDeployments(ctx, "acme", img.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("image app must not build on push, got %+v", deps)
	}
}

func mkPushEvent(org, repo, branch, commit, cloneURL string) cloud.GitPushEvent {
	return cloud.GitPushEvent{Org: org, Project: "default", Repo: repo, Branch: branch, Commit: commit, CloneURL: cloneURL}
}
