package projectbinding

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[ProjectBinding]("project-binding") }

// defaultProject is the reserved id of every org's default project. It MUST match
// the cloud wire contract (clients/principal.DefaultProject / iamauth.DefaultProject)
// and spendalert.NormalizeProject: an absent project and the literal "default"
// denote the SAME scope, so a binding for Project "" governs the default scope.
const defaultProject = "default"

// ProjectBinding links one of the org's projects to the BillingAccount that funds
// it (GCP's Project.billingAccountId). It is the SINGLE source of truth for "which
// account does this project's usage debit": the token's billing_account claim is a
// signed cache of this row, and the debit-time resolver reads THIS row, never a
// client header. A project with no binding draws the org's Default account (or the
// org-wide pool if the org has none). Project "" is the org's default scope.
type ProjectBinding struct {
	mixin.Model[ProjectBinding]

	// Project is the project slug within the org; "" = the default scope.
	Project string `json:"project"`

	// BillingAccountId is the funding BillingAccount.Id().
	BillingAccountId string `json:"billingAccountId"`
}

// NormalizeProject folds the default project onto the canonical "" scope, matching
// spendalert.NormalizeProject and the cloud principal resolver, so a request's
// project resolves to its binding exactly as the org-wide default row is matched.
func NormalizeProject(project string) string {
	p := strings.TrimSpace(project)
	if p == defaultProject {
		return ""
	}
	return p
}

func (p *ProjectBinding) Validator() *val.Validator {
	return val.New()
}

func New(db *datastore.Datastore) *ProjectBinding {
	p := new(ProjectBinding)
	p.Init(db)
	p.Parent = db.NewKey("synckey", "", 1, nil)
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("project-binding")
}
