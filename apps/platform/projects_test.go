package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	model "github.com/hanzoai/iam/pkg/model"
)

// fakeProjects is an in-memory, org-scoped ProjectStore standing in for the
// embedded IAM store in tests. It proves the project lifecycle is DELEGATED
// (platform owns no project rows) and records create/delete calls so a test can
// assert the delegation actually happened. It returns IAM's canonical
// *model.Project — the SAME type the production adapter returns — so no
// platform-local project model is introduced anywhere.
type fakeProjects struct {
	mu      sync.Mutex
	byKey   map[string]*model.Project // "<org>/<name>"
	creates []string
	deletes []string
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{byKey: map[string]*model.Project{}}
}

func (f *fakeProjects) key(org, name string) string { return org + "/" + name }

func (f *fakeProjects) List(_ context.Context, org string) ([]*model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*model.Project{}
	for _, p := range f.byKey {
		if p.Owner == org {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeProjects) Get(_ context.Context, org, name string) (*model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byKey[f.key(org, name)], nil
}

func (f *fakeProjects) Create(_ context.Context, org, name, display, description string) (*model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byKey[f.key(org, name)]; ok {
		return nil, errConflict
	}
	p := &model.Project{
		Owner: org, Name: name, Organization: org, DisplayName: display,
		Description: description, CreatedTime: "2026-01-01T00:00:00Z",
	}
	f.byKey[f.key(org, name)] = p
	f.creates = append(f.creates, f.key(org, name))
	return p, nil
}

func (f *fakeProjects) Delete(_ context.Context, org, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(org, name)
	if _, ok := f.byKey[k]; !ok {
		return false, nil
	}
	delete(f.byKey, k)
	f.deletes = append(f.deletes, k)
	return true, nil
}

func (f *fakeProjects) Exists(_ context.Context, org, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.byKey[f.key(org, name)]
	return ok, nil
}

// TestPlatformOwnsAppsNotProjects proves the boundary: the platform serves no
// project lifecycle at all — a project is created and deleted at /v1/iam/projects
// — while apps under a project stay platform's, and an app under a project IAM
// does not have is refused rather than invented.
func TestPlatformOwnsAppsNotProjects(t *testing.T) {
	app, s := mountSvcK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
	fp := s.State.projects.(*fakeProjects)

	// No project lifecycle on this surface. Both verbs are simply absent.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/platform/projects"},
		{http.MethodDelete, "/v1/platform/projects/web"},
	} {
		code, _ := do(t, app, tc.method, tc.path, "maxpower", map[string]any{"name": "Web", "slug": "web"})
		if code == http.StatusCreated || code == http.StatusNoContent || code == http.StatusOK {
			t.Fatalf("%s %s succeeded (%d): platform must not manage projects", tc.method, tc.path, code)
		}
	}
	if len(fp.creates) != 0 || len(fp.deletes) != 0 {
		t.Fatalf("platform touched the project lifecycle: creates=%v deletes=%v", fp.creates, fp.deletes)
	}

	// Given a project that exists in IAM, platform creates the app under it.
	seedProject(t, app, "maxpower", "web")
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
	}); code != http.StatusCreated {
		t.Fatalf("create app under an existing project want 201")
	}

	// An app under a project IAM does not have is refused, never invented.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/ghost/apps", "maxpower", map[string]any{
		"name": "x", "source": "image", "image": map[string]any{"repository": "ghcr.io/x/y", "tag": "1"},
	}); code != http.StatusNotFound {
		t.Fatalf("create app under missing project want 404, got %d", code)
	}

	// The read that survives is a PROJECTION IAM cannot serve: project + app count.
	code, body := do(t, app, http.MethodGet, "/v1/platform/projects", "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list want 200, got %d", code)
	}
	var views []projectView
	if err := json.Unmarshal(body, &views); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(views) != 1 || views[0].Applications != 1 {
		t.Fatalf("want one project carrying its app count, got %+v", views)
	}
}
