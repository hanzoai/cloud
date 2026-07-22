// projects.go — the project LIFECYCLE port. Projects are owned by Hanzo IAM
// (hanzo.id), the ONE source of truth for the org-scoped (owner,name) project
// resource. Platform REFERENCES that store; it never persists a project row of
// its own. Applications still live under a project (the platform_apps.project_id
// column is the project NAME), but create/list/get/delete/exists of the bare
// project delegate here.
//
// IAM is embedded in the SAME cloud binary (clients/iam mounts the clean iam-v2
// zip-natively and opens its orm.DB), so the reference is an IN-PROCESS call into
// the clean iam's project store (github.com/hanzoai/iam/pkg/store over
// clients/iam.DB()) — no HTTP hop to /v1/iam, and IAM's canonical model.Project is
// used verbatim, never cloned into a platform-local struct. This couples platform to
// the embedded IAM runtime: a cloud deployment that enables "platform" MUST also
// enable "iam" (both are single-binary co-residents by design), else clients/iam.DB()
// is nil and a project call fails closed (503). The retired Casdoor iam-v1 object
// store is GONE.
package platform

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"

	iamclient "github.com/hanzoai/cloud/clients/iam"
	"github.com/hanzoai/cloud/clients/principal"
	model "github.com/hanzoai/iam/pkg/model"
	iamstore "github.com/hanzoai/iam/pkg/store"
)

// iamStore runs an in-process IAM project-store call against db (the embedded IAM's
// orm.DB, sourced from clients/iam.DB()): it short-circuits an absent store (IAM not
// mounted → db nil) into a clean 503, and recovers any unexpected nil-deref as the
// same 503 rather than a 500. The embedded IAM's DB is nil until the co-resident IAM
// subsystem mounts it; a deployment that enables "platform" is meant to co-mount "iam"
// (see the package doc) — until it does, this reports the honest "IAM not available".
// Mirrors the old iam-v1 nil-ormer guard, now guarding a nil DB. Never masks a real
// error.
func iamStore[T any](db orm.DB, fn func(db orm.DB) (T, error)) (out T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = zip.Errorf(http.StatusServiceUnavailable,
				"platform requires the co-resident IAM store, which is not initialized")
		}
	}()
	if db == nil {
		return out, zip.Errorf(http.StatusServiceUnavailable,
			"platform requires the co-resident IAM store, which is not initialized")
	}
	return fn(db)
}

// ProjectStore is platform's org-scoped view of the IAM-owned project lifecycle.
// Every method is scoped to org (the validated owner) and keyed by the project
// name — there is no platform-minted project id; the IAM identity is (org,name),
// and that name is the app-scope key AND the operator CR `part-of` label. The
// value type is IAM's canonical *model.Project, so there is exactly ONE project
// model across the binary.
type ProjectStore interface {
	List(ctx context.Context, org string) ([]*model.Project, error)
	// Get returns nil (no error) when the project does not exist — IAM's convention.
	Get(ctx context.Context, org, name string) (*model.Project, error)
	Create(ctx context.Context, org, name, display, description string) (*model.Project, error)
	Delete(ctx context.Context, org, name string) (bool, error)
	Exists(ctx context.Context, org, name string) (bool, error)
}

// iamProjects backs ProjectStore with the in-process clean-iam project store — the
// SAME embedded IAM that serves /v1/iam. It maps platform's (org,name) to IAM's
// (Owner,Name) and delegates; no auth or project logic is reimplemented.
type iamProjects struct{}

func (iamProjects) List(_ context.Context, org string) ([]*model.Project, error) {
	return iamStore(iamclient.DB(), func(db orm.DB) ([]*model.Project, error) { return iamstore.GetProjects(db, org) })
}

func (iamProjects) Get(_ context.Context, org, name string) (*model.Project, error) {
	return iamStore(iamclient.DB(), func(db orm.DB) (*model.Project, error) { return iamstore.GetProject(db, org+"/"+name) })
}

func (p iamProjects) Create(ctx context.Context, org, name, display, description string) (*model.Project, error) {
	// Pre-check existence so a duplicate name is a clean 409 (errConflict) rather
	// than a driver-specific unique-constraint error surfacing as a 500.
	existing, err := p.Get(ctx, org, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, errConflict
	}
	proj := &model.Project{
		Owner: org, Name: name, Organization: org,
		DisplayName: display, Description: description,
		IsDefault: principal.IsDefaultProject(name),
	}
	ok, err := iamStore(iamclient.DB(), func(db orm.DB) (bool, error) { return iamstore.AddProject(db, proj) })
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errConflict
	}
	return proj, nil
}

func (iamProjects) Delete(_ context.Context, org, name string) (bool, error) {
	return iamStore(iamclient.DB(), func(db orm.DB) (bool, error) {
		return iamstore.DeleteProject(db, &model.Project{Owner: org, Name: name})
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

func toProjectView(p *model.Project, apps int) projectView {
	return projectView{
		Org: p.Owner, Slug: p.Name, Name: firstNonEmpty(p.DisplayName, p.Name),
		Description: p.Description, Applications: apps, CreatedAt: projectCreatedAt(p),
	}
}

// projectCreatedAt converts IAM's RFC3339 CreatedTime to a unix timestamp; an
// absent/unparseable value yields 0 (never a fabricated time).
func projectCreatedAt(p *model.Project) int64 {
	if t, err := time.Parse(time.RFC3339, p.CreatedTime); err == nil {
		return t.Unix()
	}
	return 0
}
