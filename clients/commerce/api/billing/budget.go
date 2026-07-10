package billing

// GCP-style billing accounts — the funding topology that resolves a request's
// project to the BillingAccount that pays for it, and the per-account cap/freeze
// the metering verdict enforces. It reuses the SAME append-only usage ledger the
// balance gate and per-scope caps (issue #70) read — an account's balance and
// period spend are summed on demand from Transaction.AccountId, never a stored
// total, so they can never drift from the debited spend.
//
// Resolution is ALWAYS server-side from the caller's org namespace: the account
// is derived from the org's ProjectBinding / default account, never trusted from a
// client header. AccountId "" is the org-wide default pool, so an org that has
// provisioned no billing account behaves byte-for-byte as before.

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/billingaccount"
	"github.com/hanzoai/cloud/clients/commerce/models/projectbinding"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
)

// maxAccountsPerOrg / maxBindingsPerOrg bound the budget scan exactly as
// maxScopeRowsPerOrg bounds the spend-alert scan — a tenant cannot inflate the row
// set to slow the money path.
const (
	maxAccountsPerOrg = 256
	maxBindingsPerOrg = 1024
)

// budget is an org's funding topology, loaded once per authorization: its billing
// accounts (indexed by id) and the project→account bindings. It answers "which
// account funds this project" (accountFor) and carries the per-account cap/freeze
// state the verdict enforces. Loaded from the caller's org namespace only.
type budget struct {
	accounts  map[string]*billingaccount.BillingAccount
	byProject map[string]string // normalized project -> account id
	defaultID string
}

// loadBudget reads the org's accounts + bindings (bounded) and indexes them.
func loadBudget(db *datastore.Datastore) (*budget, error) {
	rootKey := db.NewKey("synckey", "", 1, nil)

	accts := make([]*billingaccount.BillingAccount, 0)
	if _, err := billingaccount.Query(db).Ancestor(rootKey).Limit(maxAccountsPerOrg).GetAll(&accts); err != nil {
		return nil, err
	}
	binds := make([]*projectbinding.ProjectBinding, 0)
	if _, err := projectbinding.Query(db).Ancestor(rootKey).Limit(maxBindingsPerOrg).GetAll(&binds); err != nil {
		return nil, err
	}

	b := &budget{
		accounts:  make(map[string]*billingaccount.BillingAccount, len(accts)),
		byProject: make(map[string]string, len(binds)),
	}
	for _, a := range accts {
		b.accounts[a.Id()] = a
		if a.Default {
			b.defaultID = a.Id()
		}
	}
	for _, bd := range binds {
		if bd.BillingAccountId != "" {
			b.byProject[projectbinding.NormalizeProject(bd.Project)] = bd.BillingAccountId
		}
	}
	return b, nil
}

// accountFor resolves the funding account id for a NORMALIZED project: the
// project's binding, else the org's default account, else "" (the org-wide pool,
// for an org that has provisioned no billing account).
func (b *budget) accountFor(project string) string {
	if id, ok := b.byProject[project]; ok {
		return id
	}
	return b.defaultID
}

// resolveAccountId loads the org budget and returns the account funding a
// (possibly unnormalized) project. Best-effort: on a load error it returns "" (the
// org-wide pool) so an attribution read never fails a debit — the debit still
// records, just against the default pool. The AUTHORIZATION path (loadBudget in
// AuthorizeSpendCap) fails CLOSED instead; attribution and enforcement have
// deliberately different postures.
func resolveAccountId(db *datastore.Datastore, project string) string {
	b, err := loadBudget(db)
	if err != nil {
		return ""
	}
	return b.accountFor(projectbinding.NormalizeProject(project))
}

// accountSpentCents sums this calendar-month's api-usage debits attributed to a
// billing account (Transaction.AccountId == accountID), reusing the EXACT ledger
// the balance gate and per-scope caps read (Withdraw / SourceKind "iam-user" /
// Tags "api-usage") so the account cap counts precisely the debited spend and can
// never drift. The window is the same single CreatedAt>= inequality.
func accountSpentCents(db *datastore.Datastore, test bool, accountID string) (int64, error) {
	rootKey := db.NewKey("synckey", "", 1, nil)
	q := transaction.Query(db).Ancestor(rootKey).
		Filter("Test=", test).
		Filter("SourceKind=", "iam-user").
		Filter("Tags=", "api-usage").
		Filter("AccountId=", accountID).
		Filter("CreatedAt>=", periodStartUTC())

	transs := make([]*transaction.Transaction, 0)
	if _, err := q.GetAll(&transs); err != nil {
		return 0, err
	}
	var sum int64
	for _, t := range transs {
		sum += int64(t.Amount)
	}
	return sum, nil
}
