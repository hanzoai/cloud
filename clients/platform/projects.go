// projects.go — the project LIFECYCLE port. Projects are owned by Hanzo IAM
// (hanzo.id), the ONE source of truth for the org-scoped (owner,name) project
// resource. Platform REFERENCES that store; it never persists a project row of
// its own. Applications still live under a project (the platform_apps.project_id
// column is the project NAME), but create/list/get/delete/exists of the bare
// project delegate here.
//
// IAM is embedded in the SAME cloud binary (clients/iam mounts the whole Beego
// handler; iamserver.InitEmbed wires the shared object store), so the reference
// is an IN-PROCESS call into github.com/hanzoai/iam-v1/object — no HTTP hop to
// /v1/iam, and IAM's canonical *object.Project is used verbatim, never cloned
// into a platform-local struct. This couples platform to the embedded IAM
// runtime: a cloud deployment that enables "platform" MUST also enable "iam"
// (both are single-binary co-residents by design), else the object store's
// engine is nil and a project call fails.
package platform

import (
	"context"
	"net/http"
	"time"

	iamobj "github.com/hanzoai/iam-v1/object"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients/principal"
)

// iamStore runs an embedded-IAM object-store call, converting a nil-store panic
// into a clean 503. The store's engine (iamobj.ormer) is a package global that
// is nil until the co-resident IAM subsystem initializes it; a project call
// against a nil engine would otherwise nil-deref and surface as a 500 "runtime
// error: invalid memory address". A deployment that enables "platform" is meant
// to co-mount "iam" (see the package doc) — until it does, this reports the
// honest "IAM not available" instead of panicking. Never masks a real error.
func iamStore[T any](fn func() (T, error)) (out T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = zip.Errorf(http.StatusServiceUnavailable,
				"platform requires the co-resident IAM store, which is not initialized")
		}
	}()
	return fn()
}

// ProjectStore is platform's org-scoped view of the IAM-owned project lifecycle.
// Every method is scoped to org (the validated owner) and keyed by the project
// name — there is no platform-minted project id; the IAM identity is (org,name),
// and that name is the app-scope key AND the operator CR `part-of` label. The
// value type is IAM's canonical *object.Project, so there is exactly ONE project
// model across the binary.
type ProjectStore interface {
	List(ctx context.Context, org string) ([]*iamobj.Project, error)
	// Get returns nil (no error) when the project does not exist — IAM's convention.
	Get(ctx context.Context, org, name string) (*iamobj.Project, error)
	Create(ctx context.Context, org, name, display, description string) (*iamobj.Project, error)
	Delete(ctx context.Context, org, name string) (bool, error)
	Exists(ctx context.Context, org, name string) (bool, error)
}

// iamProjects backs ProjectStore with the in-process IAM object store — the SAME
// embedded IAM that serves /v1/iam. It maps platform's (org,name) to IAM's
// (Owner,Name) and delegates; no auth or project logic is reimplemented.
type iamProjects struct{}

func (iamProjects) List(_ context.Context, org string) ([]*iamobj.Project, error) {
	return iamStore(func() ([]*iamobj.Project, error) { return iamobj.GetProjects(org) })
}

func (iamProjects) Get(_ context.Context, org, name string) (*iamobj.Project, error) {
	return iamStore(func() (*iamobj.Project, error) { return iamobj.GetProject(org + "/" + name) })
}

func (p iamProjects) Create(ctx context.Context, org, name, display, description string) (*iamobj.Project, error) {
	// Pre-check existence so a duplicate name is a clean 409 (errConflict) rather
	// than a driver-specific unique-constraint error surfacing as a 500.
	existing, err := p.Get(ctx, org, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errConflict
	}
	proj := &iamobj.Project{
		Owner: org, Name: name, Organization: org,
		DisplayName: display, Description: description,
		IsDefault: principal.IsDefaultProject(name),
	}
	ok, err := iamStore(func() (bool, error) { return iamobj.AddProject(proj) })
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errConflict
	}
	return proj, nil
}

func (iamProjects) Delete(_ context.Context, org, name string) (bool, error) {
	return iamStore(func() (bool, error) {
		return iamobj.DeleteProject(&iamobj.Project{Owner: org, Name: name})
	})
}

func (p iamProjects) Exists(ctx context.Context, org, name string) (bool, error) {
	proj, err := p.Get(ctx, org, name)
	return proj != nil, err
}

// projectView is the published HTTP shape for an IAM-owned project. Slug is the
// (org,name) key that scopes apps and matches the :project route param; Name is
// the human display; CreatedAt is derived from IAM's RFC3339 CreatedTime.
type projectView struct {
	Org          string `json:"org"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Applications int    `json:"applications"`
	CreatedAt    int64  `json:"createdAt"`
}

func toProjectView(p *iamobj.Project, apps int) projectView {
	return projectView{
		Org: p.Owner, Slug: p.Name, Name: firstNonEmpty(p.DisplayName, p.Name),
		Description: p.Description, Applications: apps, CreatedAt: projectCreatedAt(p),
	}
}

// projectCreatedAt converts IAM's RFC3339 CreatedTime to a unix timestamp; an
// absent/unparseable value yields 0 (never a fabricated time).
func projectCreatedAt(p *iamobj.Project) int64 {
	if t, err := time.Parse(time.RFC3339, p.CreatedTime); err == nil {
		return t.Unix()
	}
	return 0
}
