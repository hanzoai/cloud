package platform

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkProject(org, slug, name string) Project {
	return Project{ID: "proj_" + org + "_" + slug, Org: org, Slug: slug, Name: name, CreatedAt: 100, UpdatedAt: 100}
}

func mkApp(org, projectID, slug string) Application {
	return Application{
		ID: "app_" + org + "_" + slug, Org: org, ProjectID: projectID, Slug: slug, Name: slug,
		Source: "image", ImageRepo: "ghcr.io/hanzoai/nginx", ImageTag: "1", BuildType: "image",
		Port: 8080, Replicas: 1, EnvJSON: "[]", DomainsJSON: "[]", Status: "draft",
		Namespace: tenantNamespace(org), CreatedAt: 100, UpdatedAt: 100,
	}
}

func TestProjectCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateProject(ctx, mkProject("maxpower", "web", "Web")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetProject(ctx, "maxpower", "web")
	if err != nil || got.Name != "Web" {
		t.Fatalf("get: %v %+v", err, got)
	}
	// Cross-tenant isolation: another org cannot read it.
	if _, err := s.GetProject(ctx, "acme", "web"); !errors.Is(err, errNotFound) {
		t.Fatalf("expected notFound for other org, got %v", err)
	}
	// Duplicate (org,slug) conflicts; same slug under a different org is fine.
	if err := s.CreateProject(ctx, mkProject("maxpower", "web", "Web")); !errors.Is(err, errConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := s.CreateProject(ctx, mkProject("acme", "web", "Acme Web")); err != nil {
		t.Fatalf("other-org same-slug: %v", err)
	}
}

func TestApplicationCRUDAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("maxpower", "web", "Web")); err != nil {
		t.Fatalf("project: %v", err)
	}
	proj, _ := s.GetProject(ctx, "maxpower", "web")

	a := mkApp("maxpower", proj.ID, "api")
	if err := s.CreateApplication(ctx, a); err != nil {
		t.Fatalf("create app: %v", err)
	}
	// Same (org,project,slug) conflicts.
	if err := s.CreateApplication(ctx, a); !errors.Is(err, errConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	// Get scoped to org+project.
	got, err := s.GetApplication(ctx, "maxpower", proj.ID, "api")
	if err != nil || got.Slug != "api" {
		t.Fatalf("get app: %v %+v", err, got)
	}
	// Cross-tenant: acme cannot read maxpower's app even with the right IDs.
	if _, err := s.GetApplication(ctx, "acme", proj.ID, "api"); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-tenant get must be notFound, got %v", err)
	}
	if _, err := s.GetApplicationByID(ctx, "acme", a.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-tenant getByID must be notFound, got %v", err)
	}
	// List is org+project scoped.
	list, _ := s.ListApplications(ctx, "maxpower", proj.ID)
	if len(list) != 1 {
		t.Fatalf("want 1 app, got %d", len(list))
	}
	if other, _ := s.ListApplications(ctx, "acme", proj.ID); len(other) != 0 {
		t.Fatalf("acme must see 0 apps in maxpower's project, got %d", len(other))
	}
	// Update + delete.
	got.Replicas = 3
	got.UpdatedAt = 200
	if err := s.UpdateApplication(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if re, _ := s.GetApplication(ctx, "maxpower", proj.ID, "api"); re.Replicas != 3 {
		t.Fatalf("update not persisted: %+v", re)
	}
	_, deleted, err := s.DeleteApplication(ctx, "maxpower", proj.ID, "api")
	if err != nil || !deleted {
		t.Fatalf("delete: %v deleted=%v", err, deleted)
	}
}

func TestDeploymentVersioningAndIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_ = s.CreateProject(ctx, mkProject("maxpower", "web", "Web"))
	proj, _ := s.GetProject(ctx, "maxpower", "web")
	a := mkApp("maxpower", proj.ID, "api")
	_ = s.CreateApplication(ctx, a)

	for want := 1; want <= 3; want++ {
		v, err := s.NextVersion(ctx, a.ID)
		if err != nil || v != want {
			t.Fatalf("NextVersion=%d want %d err=%v", v, want, err)
		}
		d := Deployment{ID: "dep_" + itoa(v), Org: "maxpower", ApplicationID: a.ID, Version: v, Status: "deploying", Source: "image", CreatedAt: 100, UpdatedAt: 100}
		if err := s.InsertDeployment(ctx, d); err != nil {
			t.Fatalf("insert deployment v%d: %v", v, err)
		}
	}
	// Duplicate (app,version) conflicts.
	if err := s.InsertDeployment(ctx, Deployment{ID: "dep_dup", Org: "maxpower", ApplicationID: a.ID, Version: 1, Status: "x", Source: "image", CreatedAt: 1, UpdatedAt: 1}); !errors.Is(err, errConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	list, _ := s.ListDeployments(ctx, "maxpower", a.ID)
	if len(list) != 3 || list[0].Version != 3 {
		t.Fatalf("want 3 deployments newest-first, got %d first=%v", len(list), list)
	}
	// Cross-tenant deployment isolation.
	if other, _ := s.ListDeployments(ctx, "acme", a.ID); len(other) != 0 {
		t.Fatalf("acme must see 0 of maxpower's deployments, got %d", len(other))
	}
	if _, err := s.GetDeployment(ctx, "acme", a.ID, "dep_1"); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-tenant getDeployment must be notFound, got %v", err)
	}
}

func TestCascadeDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_ = s.CreateProject(ctx, mkProject("maxpower", "web", "Web"))
	proj, _ := s.GetProject(ctx, "maxpower", "web")
	a := mkApp("maxpower", proj.ID, "api")
	_ = s.CreateApplication(ctx, a)
	_ = s.InsertDeployment(ctx, Deployment{ID: "dep_1", Org: "maxpower", ApplicationID: a.ID, Version: 1, Status: "live", Source: "image", CreatedAt: 1, UpdatedAt: 1})
	_ = s.InsertBuild(ctx, Build{ID: "bld_1", Org: "maxpower", ApplicationID: a.ID, DeploymentID: "dep_1", Status: "succeeded", CreatedAt: 1, UpdatedAt: 1})

	_, apps, deleted, err := s.DeleteProject(ctx, "maxpower", "web")
	if err != nil || !deleted {
		t.Fatalf("delete project: %v deleted=%v", err, deleted)
	}
	if len(apps) != 1 {
		t.Fatalf("delete should report 1 app for teardown, got %d", len(apps))
	}
	// Everything under the project is gone.
	if _, err := s.GetApplicationByID(ctx, "maxpower", a.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("app should be gone, got %v", err)
	}
	if l, _ := s.ListDeployments(ctx, "maxpower", a.ID); len(l) != 0 {
		t.Fatalf("deployments should be gone, got %d", len(l))
	}
	if _, err := s.GetBuild(ctx, "maxpower", "bld_1"); !errors.Is(err, errNotFound) {
		t.Fatalf("build should be gone, got %v", err)
	}
}

func TestBuildCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	b := Build{ID: "bld_1", Org: "maxpower", ApplicationID: "app_x", Status: "queued", Image: "ghcr.io/hanzoai/tenant-maxpower-api:main", CreatedAt: 1, UpdatedAt: 1}
	if err := s.InsertBuild(ctx, b); err != nil {
		t.Fatalf("insert build: %v", err)
	}
	b.Status, b.JobName, b.UpdatedAt = "building", "pf-build-maxpower-api-abc", 2
	if err := s.UpdateBuild(ctx, b); err != nil {
		t.Fatalf("update build: %v", err)
	}
	got, err := s.GetBuild(ctx, "maxpower", "bld_1")
	if err != nil || got.Status != "building" || got.JobName == "" {
		t.Fatalf("get build: %v %+v", err, got)
	}
	// Cross-tenant build isolation.
	if _, err := s.GetBuild(ctx, "acme", "bld_1"); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-tenant getBuild must be notFound, got %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
