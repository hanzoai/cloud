package platform

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
)

// seedGitApp writes one git-source app tracking repoURL@branch under the default
// project (projects live in IAM; app.ProjectID is the project name).
func seedGitApp(t *testing.T, s *cloud.Service[state], org, slug, repoURL, branch string) Application {
	t.Helper()
	ctx := context.Background()
	a := Application{
		ID: "app_" + org + "_" + slug, Org: org, ProjectID: "default", Slug: slug, Name: slug,
		Environment: "production", Source: "git", RepoURL: repoURL, RepoBranch: branch, RepoProvider: "hanzo",
		BuildType: "pack", Port: 3000, Replicas: 1, EnvJSON: "[]", DomainsJSON: "[]",
		Status: "created", Namespace: tenantNamespace(org), CreatedAt: 1, UpdatedAt: 1,
	}
	if err := s.State.store.CreateApplication(ctx, a); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	return a
}

// pushService mounts a Service over a ready fake cluster (no HTTP routes needed —
// buildFromPush is called directly) and trusts the embedded-git apex as a build
// source, exactly as platform.Mount does from deps.Domain.
func pushService(t *testing.T) *cloud.Service[state] {
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
	s := pushService(t)
	const clone = "https://git.hanzo.ai/v1/git/acme/site.git"
	a := seedGitApp(t, s, "acme", "site", "https://git.hanzo.ai/v1/git/acme/site", "main")

	// CloneURL carries the ".git" suffix; the app RepoURL does not — sameRepo must
	// still match after normalization.
	err := buildFromPush(s, ctx, mkPushEvent("acme", "site", "main", "deadbeefcafe", clone))
	if err != nil {
		t.Fatalf("buildFromPush: %v", err)
	}
	got, err := s.State.store.GetApplicationByID(ctx, "acme", a.ID)
	if err != nil {
		t.Fatalf("reload app: %v", err)
	}
	if got.Status != "building" {
		t.Fatalf("app status: want building, got %q", got.Status)
	}
	deps, err := s.State.store.ListDeployments(ctx, "acme", a.ID)
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
	s := pushService(t)
	a := seedGitApp(t, s, "acme", "site", "https://git.hanzo.ai/v1/git/acme/site", "main")

	// Right repo, wrong branch.
	if err := buildFromPush(s, ctx, mkPushEvent("acme", "site", "feature", "abc123", "https://git.hanzo.ai/v1/git/acme/site.git")); err != nil {
		t.Fatalf("buildFromPush (wrong branch): %v", err)
	}
	// Right branch, different repo.
	if err := buildFromPush(s, ctx, mkPushEvent("acme", "other", "main", "abc123", "https://git.hanzo.ai/v1/git/acme/other.git")); err != nil {
		t.Fatalf("buildFromPush (other repo): %v", err)
	}
	deps, err := s.State.store.ListDeployments(ctx, "acme", a.ID)
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
	s := pushService(t)
	img := Application{
		ID: "app_acme_api", Org: "acme", ProjectID: "default", Slug: "api", Name: "api",
		Source: "image", RepoURL: "https://git.hanzo.ai/v1/git/acme/api", ImageRepo: "ghcr.io/hanzoai/api", ImageTag: "1",
		EnvJSON: "[]", DomainsJSON: "[]", Status: "live", CreatedAt: 1, UpdatedAt: 1,
	}
	if err := s.State.store.CreateApplication(ctx, img); err != nil {
		t.Fatalf("seed image app: %v", err)
	}
	if err := buildFromPush(s, ctx, mkPushEvent("acme", "api", "main", "abc123", "https://git.hanzo.ai/v1/git/acme/api.git")); err != nil {
		t.Fatalf("buildFromPush: %v", err)
	}
	deps, err := s.State.store.ListDeployments(ctx, "acme", img.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("image app must not build on push, got %+v", deps)
	}
}

// mkPushEvent takes a short branch and qualifies it, because the event carries a
// full ref — the same shape a tag push arrives in.
// A tag push reaches this consumer like any other ref, and must not rebuild an
// app that tracks a branch — not even when the tag shares the branch's name.
func TestBuildFromPush_TagDoesNotRebuildBranchApp(t *testing.T) {
	ctx := context.Background()
	s := pushService(t)
	a := seedGitApp(t, s, "acme", "site", "https://git.hanzo.ai/v1/git/acme/site", "main")

	ev := mkPushEvent("acme", "site", "main", "abc123", "https://git.hanzo.ai/v1/git/acme/site.git")
	ev.Ref = "refs/tags/main"
	if err := buildFromPush(s, ctx, ev); err != nil {
		t.Fatalf("buildFromPush (tag): %v", err)
	}
	deps, err := s.State.store.ListDeployments(ctx, "acme", a.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("a tag push must not rebuild a branch-tracking app, got %+v", deps)
	}
}

func mkPushEvent(org, repo, branch, commit, cloneURL string) cloud.GitPushEvent {
	return cloud.GitPushEvent{Org: org, Project: "default", Repo: repo, Ref: "refs/heads/" + branch, Commit: commit, CloneURL: cloneURL}
}
