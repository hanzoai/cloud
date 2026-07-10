package billingaccount

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[BillingAccount]("billing-account") }

// BillingAccount is a GCP-style funding entity, SEPARATE from the org: it holds
// the spend limit + level + freeze state that fund 1..N of the org's projects
// (dedicated to one project, or shared across many). It is OWNED by exactly one
// org — its datastore namespace — and never crosses org boundaries: a project in
// org A can only ever bind to an account in org A.
//
// Like every money model here it carries NO stored balance. An account's balance
// and period spend are DERIVED on demand from the append-only ledger, filtered by
// Transaction.AccountId == this account's Id(), so they can never drift from the
// debited spend. A ledger row with AccountId "" is the org-wide default pool
// (every row that predates billing accounts), so an org with no explicit account
// behaves byte-for-byte as before.
type BillingAccount struct {
	mixin.Model[BillingAccount]

	// OrgId is the owner org (its namespace) — exactly one, never cross-org.
	OrgId string `json:"orgId"`
	Name  string `json:"name"`

	// Shared funds many projects; a dedicated account funds one. Descriptive only
	// (the binding count is authoritative) — it lets the console warn before
	// attaching a second project to a dedicated account.
	Shared bool `json:"shared"`

	// Default marks the org's fallback account: a project with no explicit
	// ProjectBinding draws here, and legacy untagged ledger rows ("") are counted
	// as this account's balance. Exactly one account per org is Default.
	Default bool `json:"default"`

	// Level is the tier/level id (plan.Slug vocabulary) this account funds at.
	Level string `json:"level,omitempty"`

	// LimitCents is the account-wide spend cap for the calendar-month period; 0 =
	// uncapped. Enforced most-restrictive-wins alongside the per-scope caps (#70).
	LimitCents int64 `json:"limitCents"`

	Currency currency.Type `json:"currency" orm:"default:usd"`

	// Enabled=false FREEZES the account: the debit-time gate refuses any billable
	// request drawing on it (the sub-second account kill-switch), independent of
	// balance or caps.
	Enabled bool `json:"enabled"`
}

func (b *BillingAccount) Validator() *val.Validator {
	return val.New()
}

func New(db *datastore.Datastore) *BillingAccount {
	b := new(BillingAccount)
	b.Init(db)
	b.Parent = db.NewKey("synckey", "", 1, nil)
	return b
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("billing-account")
}
