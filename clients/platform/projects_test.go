package platform

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	iamobj "github.com/hanzoai/iam/object"
)

// fakeProjects is an in-memory, org-scoped ProjectStore standing in for the
// embedded IAM object store in tests. It proves the project lifecycle is
// DELEGATED (platform owns no project rows) and records create/delete calls so a
// test can assert the delegation actually happened. It returns IAM's canonical
// *object.Project — the SAME type the production adapter returns — so no
// platform-local project model is introduced anywhere.
type fakeProjects struct {
	mu      sync.Mutex
	byKey   map[string]*iamobj.Project // "<org>/<name>"
	creates []string
	deletes []string
}

func newFakeProjects() *fakeProjects {
	return &fakeProjects{byKey: map[string]*iamobj.Project{}}
}

func (f *fakeProjects) key(org, name string) string { return org + "/" + name }

func (f *fakeProjects) List(_ context.Context, org string) ([]*iamobj.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*iamobj.Project{}
	for _, p := range f.byKey {
		if p.Owner == org {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeProjects) Get(_ context.Context, org, name string) (*iamobj.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byKey[f.key(org, name)], nil
}

func (f *fakeProjects) Create(_ context.Context, org, name, display, description string) (*iamobj.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byKey[f.key(org, name)]; ok {
		return nil, errConflict
	}
	p := &iamobj.Project{
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

// TestProjectLifecycleDelegatesToIAM proves platform OWNS no project: create,
// list, delete of the bare project route through the ProjectStore (IAM), and a
// project delete cascades ONLY platform's own app tree — apps-in-project stays
// platform's job.
func TestProjectLifecycleDelegatesToIAM(t *testing.T) {
	app, s := mountSvcK8s(t, &k8sClient{initErr: "no cluster (test)", limits: testLimits()})
	fp := s.State.projects.(*fakeProjects)

	// Create delegates to the ProjectStore, not platform's store.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects", "maxpower", map[string]any{"name": "Web", "slug": "web"}); code != http.StatusCreated {
		t.Fatalf("create want 201")
	}
	if len(fp.creates) != 1 || fp.creates[0] != "maxpower/web" {
		t.Fatalf("create must delegate to the IAM-backed ProjectStore, got %v", fp.creates)
	}

	// An app can be created under the delegated project (existence checked via the store).
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
	}); code != http.StatusCreated {
		t.Fatalf("create app under delegated project want 201")
	}
	// An app under a project that does NOT exist in IAM is refused 404.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/ghost/apps", "maxpower", map[string]any{
		"name": "x", "source": "image", "image": map[string]any{"repository": "ghcr.io/x/y", "tag": "1"},
	}); code != http.StatusNotFound {
		t.Fatalf("create app under missing project want 404, got %d", code)
	}
	// A duplicate project name is the delegated 409.
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects", "maxpower", map[string]any{"name": "Web", "slug": "web"}); code != http.StatusConflict {
		t.Fatalf("duplicate create want 409")
	}

	// Delete delegates to the ProjectStore AND cascades platform's app tree.
	if code, _ := do(t, app, http.MethodDelete, "/v1/platform/projects/web", "maxpower", nil); code != http.StatusNoContent {
		t.Fatalf("delete want 204")
	}
	if len(fp.deletes) != 1 || fp.deletes[0] != "maxpower/web" {
		t.Fatalf("delete must delegate to the IAM-backed ProjectStore, got %v", fp.deletes)
	}
	if _, err := s.State.store.GetApplication(context.Background(), "maxpower", "web", "api"); !errors.Is(err, errNotFound) {
		t.Fatalf("project delete must cascade platform apps, got %v", err)
	}
	// Deleting a project that is not in IAM → 404 (no fabricated success).
	if code, _ := do(t, app, http.MethodDelete, "/v1/platform/projects/web", "maxpower", nil); code != http.StatusNotFound {
		t.Fatalf("delete missing project want 404, got %d", code)
	}
}
