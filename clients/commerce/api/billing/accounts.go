package billing

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/models/billingaccount"
	"github.com/hanzoai/cloud/clients/commerce/models/projectbinding"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// A BillingAccount (models/billingaccount) is a GCP-style funding entity SEPARATE
// from the org: it holds the spend limit + level + freeze that fund 1..N of the
// org's projects (dedicated or shared), and a project resolves to its account via
// a ProjectBinding (models/projectbinding). CRUD here is org-owner self-service —
// a limit is a self-imposed budget, not a money mint — so it lives in the user
// group beside spend-alerts, org-namespaced and IDOR-guarded by OrgId. Freeze
// (Enabled=false) is a platform anti-fraud control and is NOT exposed here.

// accountView is the wire shape of one account plus its DERIVED calendar-month
// spend (never stored, always summed from the ledger by AccountId).
func accountView(db *datastore.Datastore, test bool, a *billingaccount.BillingAccount) gin.H {
	view := gin.H{
		"id":         a.Id(),
		"orgId":      a.OrgId,
		"name":       a.Name,
		"shared":     a.Shared,
		"default":    a.Default,
		"level":      a.Level,
		"limitCents": a.LimitCents,
		"currency":   a.Currency,
		"enabled":    a.Enabled,
		"createdAt":  a.CreatedAt,
		"updatedAt":  a.UpdatedAt,
	}
	if spent, err := accountSpentCents(db, test, a.Id()); err == nil {
		view["periodSpentCents"] = spent
		if a.LimitCents > 0 {
			view["over"] = spent >= a.LimitCents
		}
	}
	return view
}

// loadOwnedAccount loads a billing account by id and confirms it belongs to the
// caller's org. Cross-org is blocked twice: the datastore is org-namespaced (a
// foreign id is unreachable) AND OrgId is re-checked. Returns (nil,false) as a
// clean not-found so a guessed id neither loads nor leaks existence.
func loadOwnedAccount(c *gin.Context, db *datastore.Datastore, id string) (*billingaccount.BillingAccount, bool) {
	if strings.TrimSpace(id) == "" {
		return nil, false
	}
	a := billingaccount.New(db)
	if err := a.GetById(id); err != nil {
		return nil, false
	}
	if a.OrgId != "" && a.OrgId != strings.TrimSpace(middleware.GetOrganization(c).Name) {
		return nil, false
	}
	return a, true
}

// ListBillingAccounts returns the org's billing accounts (with derived period
// spend). An org that has provisioned none still presents ONE synthetic account —
// itself, the org-wide default pool — so existing callers that expect >=1 account
// keep working and it mirrors the pre-account "org == billing account" behavior.
//
//	GET /v1/billing/accounts
func ListBillingAccounts(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	test := org.TestMode()

	rootKey := db.NewKey("synckey", "", 1, nil)
	accts := make([]*billingaccount.BillingAccount, 0)
	if _, err := billingaccount.Query(db).Ancestor(rootKey).Limit(maxAccountsPerOrg).GetAll(&accts); err != nil {
		log.Error("Failed to list billing accounts: %v", err, c)
		http.Fail(c, 500, "failed to list billing accounts", err)
		return
	}

	items := make([]gin.H, 0, len(accts))
	for _, a := range accts {
		items = append(items, accountView(db, test, a))
	}

	if len(items) == 0 {
		// The org-wide default pool, presented as the implicit account. Its ledger
		// rows carry AccountId "" (resolveAccountId falls back to the pool for an org
		// with no explicit account), so its spend is the org-wide sum, not shown here.
		items = append(items, gin.H{
			"id":        org.Id(),
			"orgId":     org.Id(),
			"name":      org.FullName,
			"default":   true,
			"currency":  currency.Type("usd"),
			"enabled":   true,
			"createdAt": org.CreatedAt,
			"synthetic": true,
		})
	}

	c.JSON(200, items)
}

type createBillingAccountRequest struct {
	Name       string `json:"name"`
	Shared     bool   `json:"shared"`
	Level      string `json:"level"`
	LimitCents int64  `json:"limitCents"`
	Currency   string `json:"currency"`
	Default    bool   `json:"default"`
}

// CreateBillingAccount provisions a real billing account for the org. The org's
// FIRST account becomes its Default (unbound projects + legacy untagged spend draw
// there) unless one already exists; a request may also explicitly claim default,
// which demotes the prior one — exactly one account is ever Default.
//
//	POST /v1/billing/accounts
func CreateBillingAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	var req createBillingAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Fail(c, 400, "name is required", nil)
		return
	}
	if req.LimitCents < 0 {
		http.Fail(c, 400, "limitCents must be >= 0", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	if n, cerr := billingaccount.Query(db).Ancestor(rootKey).Limit(maxAccountsPerOrg + 1).Count(); cerr == nil && n >= maxAccountsPerOrg {
		http.Fail(c, 400, "billing-account limit reached for this organization", nil)
		return
	}

	prevDefault := make([]*billingaccount.BillingAccount, 0, 1)
	_, _ = billingaccount.Query(db).Ancestor(rootKey).Filter("Default=", true).Limit(1).GetAll(&prevDefault)
	hasDefault := len(prevDefault) > 0

	cur := currency.Type(strings.ToLower(strings.TrimSpace(req.Currency)))
	if cur == "" {
		cur = "usd"
	}

	a := billingaccount.New(db)
	a.OrgId = strings.TrimSpace(org.Name)
	a.Name = strings.TrimSpace(req.Name)
	a.Shared = req.Shared
	a.Level = strings.TrimSpace(req.Level)
	a.LimitCents = req.LimitCents
	a.Currency = cur
	a.Enabled = true
	a.Default = req.Default || !hasDefault

	if a.Default && hasDefault {
		for _, e := range prevDefault {
			e.Default = false
			_ = e.Update()
		}
	}

	if err := a.Create(); err != nil {
		log.Error("Failed to create billing account: %v", err, c)
		http.Fail(c, 500, "failed to create billing account", err)
		return
	}

	c.JSON(201, accountView(db, org.TestMode(), a))
}

// GetBillingAccount returns one account the caller owns.
//
//	GET /v1/billing/accounts/:id
func GetBillingAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	a, ok := loadOwnedAccount(c, db, c.Param("id"))
	if !ok {
		http.Fail(c, 404, "billing account not found", nil)
		return
	}
	c.JSON(200, accountView(db, org.TestMode(), a))
}

type updateBillingAccountRequest struct {
	Name       *string `json:"name"`
	Shared     *bool   `json:"shared"`
	Level      *string `json:"level"`
	LimitCents *int64  `json:"limitCents"`
	Default    *bool   `json:"default"`
}

// UpdateBillingAccount patches an account (partial; absent fields preserved). It
// can PROMOTE this account to the org default (demoting the prior one) but cannot
// clear default — the org always has exactly one. Enabled/freeze is not settable
// here (platform control).
//
//	PATCH /v1/billing/accounts/:id
func UpdateBillingAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	a, ok := loadOwnedAccount(c, db, c.Param("id"))
	if !ok {
		http.Fail(c, 404, "billing account not found", nil)
		return
	}

	var req updateBillingAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		http.Fail(c, 400, "invalid request body", err)
		return
	}

	if req.Name != nil {
		a.Name = strings.TrimSpace(*req.Name)
	}
	if req.Shared != nil {
		a.Shared = *req.Shared
	}
	if req.Level != nil {
		a.Level = strings.TrimSpace(*req.Level)
	}
	if req.LimitCents != nil {
		if *req.LimitCents < 0 {
			http.Fail(c, 400, "limitCents must be >= 0", nil)
			return
		}
		a.LimitCents = *req.LimitCents
	}
	if req.Default != nil && *req.Default && !a.Default {
		rootKey := db.NewKey("synckey", "", 1, nil)
		prev := make([]*billingaccount.BillingAccount, 0, 1)
		if _, err := billingaccount.Query(db).Ancestor(rootKey).Filter("Default=", true).Limit(1).GetAll(&prev); err == nil {
			for _, e := range prev {
				if e.Id() != a.Id() {
					e.Default = false
					_ = e.Update()
				}
			}
		}
		a.Default = true
	}

	if err := a.Update(); err != nil {
		log.Error("Failed to update billing account: %v", err, c)
		http.Fail(c, 500, "failed to update billing account", err)
		return
	}

	c.JSON(200, accountView(db, org.TestMode(), a))
}

// DeleteBillingAccount removes an account and the bindings that point at it (their
// projects fall back to the org default). The default account cannot be deleted —
// promote another to default first. Past debits keep their AccountId: the ledger
// is immutable history, so an account's spend stays auditable after deletion.
//
//	DELETE /v1/billing/accounts/:id
func DeleteBillingAccount(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	a, ok := loadOwnedAccount(c, db, c.Param("id"))
	if !ok {
		http.Fail(c, 404, "billing account not found", nil)
		return
	}
	if a.Default {
		http.Fail(c, 400, "cannot delete the default billing account; promote another account to default first", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	binds := make([]*projectbinding.ProjectBinding, 0)
	if _, err := projectbinding.Query(db).Ancestor(rootKey).Filter("BillingAccountId=", a.Id()).Limit(maxBindingsPerOrg).GetAll(&binds); err == nil {
		for _, bd := range binds {
			_ = bd.Delete()
		}
	}

	if err := a.Delete(); err != nil {
		log.Error("Failed to delete billing account: %v", err, c)
		http.Fail(c, 500, "failed to delete billing account", err)
		return
	}
	c.JSON(204, nil)
}

// ListAccountProjects lists the projects funded by an account (its bindings).
//
//	GET /v1/billing/accounts/:id/projects
func ListAccountProjects(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	a, ok := loadOwnedAccount(c, db, c.Param("id"))
	if !ok {
		http.Fail(c, 404, "billing account not found", nil)
		return
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	binds := make([]*projectbinding.ProjectBinding, 0)
	if _, err := projectbinding.Query(db).Ancestor(rootKey).Filter("BillingAccountId=", a.Id()).Limit(maxBindingsPerOrg).GetAll(&binds); err != nil {
		http.Fail(c, 500, "failed to list project bindings", err)
		return
	}
	items := make([]gin.H, 0, len(binds))
	for _, bd := range binds {
		items = append(items, gin.H{"project": bd.Project, "billingAccountId": bd.BillingAccountId})
	}
	c.JSON(200, items)
}

// BindAccountProject binds a project to this account (upsert — a project funds
// from exactly one account, so this moves it off any prior account).
//
//	PUT /v1/billing/accounts/:id/projects/:project
func BindAccountProject(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	a, ok := loadOwnedAccount(c, db, c.Param("id"))
	if !ok {
		http.Fail(c, 404, "billing account not found", nil)
		return
	}
	project := projectbinding.NormalizeProject(c.Param("project"))

	rootKey := db.NewKey("synckey", "", 1, nil)
	existing := make([]*projectbinding.ProjectBinding, 0, 1)
	if _, err := projectbinding.Query(db).Ancestor(rootKey).Filter("Project=", project).Limit(1).GetAll(&existing); err == nil && len(existing) > 0 {
		existing[0].BillingAccountId = a.Id()
		if err := existing[0].Update(); err != nil {
			http.Fail(c, 500, "failed to update project binding", err)
			return
		}
		c.JSON(200, gin.H{"project": existing[0].Project, "billingAccountId": existing[0].BillingAccountId})
		return
	}

	if n, cerr := projectbinding.Query(db).Ancestor(rootKey).Limit(maxBindingsPerOrg + 1).Count(); cerr == nil && n >= maxBindingsPerOrg {
		http.Fail(c, 400, "project-binding limit reached for this organization", nil)
		return
	}
	bd := projectbinding.New(db)
	bd.Project = project
	bd.BillingAccountId = a.Id()
	if err := bd.Create(); err != nil {
		http.Fail(c, 500, "failed to create project binding", err)
		return
	}
	c.JSON(201, gin.H{"project": bd.Project, "billingAccountId": bd.BillingAccountId})
}

// UnbindAccountProject removes a project's binding to this account (the project
// falls back to the org default account).
//
//	DELETE /v1/billing/accounts/:id/projects/:project
func UnbindAccountProject(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	a, ok := loadOwnedAccount(c, db, c.Param("id"))
	if !ok {
		http.Fail(c, 404, "billing account not found", nil)
		return
	}
	project := projectbinding.NormalizeProject(c.Param("project"))

	rootKey := db.NewKey("synckey", "", 1, nil)
	existing := make([]*projectbinding.ProjectBinding, 0, 1)
	if _, err := projectbinding.Query(db).Ancestor(rootKey).Filter("Project=", project).Limit(1).GetAll(&existing); err == nil && len(existing) > 0 {
		if existing[0].BillingAccountId == a.Id() {
			if err := existing[0].Delete(); err != nil {
				http.Fail(c, 500, "failed to delete project binding", err)
				return
			}
		}
	}
	c.JSON(204, nil)
}

// billingAccountMember is a simplified member record. Org members live in IAM, not
// Commerce, so the member endpoints below delegate there.
type billingAccountMember struct {
	ID      string    `json:"id"`
	UserID  string    `json:"userId"`
	Email   string    `json:"email"`
	Name    string    `json:"name"`
	Role    string    `json:"role"`
	AddedAt time.Time `json:"addedAt"`
}

// ListAccountMembers returns the members of a billing account (org). Currently the
// requesting IAM user is the sole member surfaced, since the full roster lives in
// IAM.
//
//	GET /v1/billing/accounts/:id/members
func ListAccountMembers(c *gin.Context) {
	org := middleware.GetOrganization(c)

	if id := c.Param("id"); id != org.Id() {
		if _, ok := loadOwnedAccount(c, datastore.New(org.Namespaced(c)), id); !ok {
			http.Fail(c, 403, "access denied to billing account", nil)
			return
		}
	}

	members := make([]gin.H, 0)
	if claims := iammiddleware.GetIAMClaims(c); claims.Subject != "" {
		role := "member"
		for _, r := range claims.Roles {
			if r == "admin" || r == "owner" {
				role = r
				break
			}
		}
		members = append(members, gin.H{
			"id":      claims.Subject,
			"userId":  claims.Subject,
			"email":   claims.Email,
			"role":    role,
			"addedAt": org.CreatedAt,
		})
	}

	c.JSON(200, members)
}

// AddAccountMember is a stub. Member management is done via IAM.
//
//	POST /v1/billing/accounts/:id/members
func AddAccountMember(c *gin.Context) {
	http.Fail(c, 501, "member management must be done via the Hanzo console", nil)
}

// UpdateMemberRole is a stub. Role updates are done via IAM.
//
//	PATCH /v1/billing/accounts/:id/members/:memberId
func UpdateMemberRole(c *gin.Context) {
	http.Fail(c, 501, "role updates must be done via the Hanzo console", nil)
}

// RemoveAccountMember is a stub. Member removal is done via IAM.
//
//	DELETE /v1/billing/accounts/:id/members/:memberId
func RemoveAccountMember(c *gin.Context) {
	http.Fail(c, 501, "member removal must be done via the Hanzo console", nil)
}
