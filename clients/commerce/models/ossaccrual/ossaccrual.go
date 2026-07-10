// Package ossaccrual is the payout-accrual ledger for the SBOM-driven OSS
// developer payout system. Each OSSAccrual is one append-only ledger line: a
// slice of one org's spend attributed to one OSS package, ready to be paid to
// that package's upstream maintainers.
//
// It is the persistent counterpart to the pure ossattr.Attribute math: when an
// org's usage is recorded, the accrual engine computes the capped, weighted,
// pro-rata split and writes one OSSAccrual per package. The ledger is the
// provable thing — "Hanzo owes pkg:golang/.../gin $X.XX, accrued across these
// transactions" — independent of whether the money has been disbursed yet.
//
// Invariants:
//  1. Append-only: accruals are never mutated to change an amount; the running
//     total is the SUM of accrual lines for a package. Disbursement is a
//     separate Payout referencing the lines it settles (Status transitions
//     pending -> settled record the payout, they do not rewrite the amount).
//  2. Idempotent: IdempotencyKey = ossattr.AccrualID(org, txn, purl) makes the
//     same (org, spend-transaction, package) accrue exactly once, even on
//     retries — mirroring commerce's tag-deduped usage recording.
//  3. Tenant-scoped to the SPENDING org's namespace? No — accruals are a
//     PLATFORM liability (Hanzo -> OSS), so they live in the platform/system
//     namespace, aggregated across all paying orgs. SpendOrg records who the
//     spend came from for audit, but the payable is global.
package ossaccrual

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var kind = "oss-accrual"

func init() { orm.Register[OSSAccrual]("oss-accrual") }

// Status is the lifecycle of an accrual line.
type Status string

const (
	// Pending: accrued, not yet included in a payout.
	Pending Status = "pending"
	// Held: accrued but the maintainer/funding target is unresolved; the
	// amount waits in the held pool until resolution.
	Held Status = "held"
	// Queued: included in a payout batch that is being disbursed.
	Queued Status = "queued"
	// Settled: the referenced payout has paid out.
	Settled Status = "settled"
)

// Scope mirrors ossattr.Scope (direct | transitive) for the line's audit trail.
type Scope string

const (
	Direct     Scope = "direct"
	Transitive Scope = "transitive"
)

// OSSAccrual is one accrual ledger line.
type OSSAccrual struct {
	mixin.Model[OSSAccrual]

	// Package identity (PURL is the canonical key joining to SBOM + resolver).
	PURL      string `json:"purl"`
	Name      string `json:"name,omitempty"`
	Ecosystem string `json:"ecosystem,omitempty"`
	Version   string `json:"version,omitempty"`
	Scope     Scope  `json:"scope,omitempty"`

	// Attribution provenance — everything needed to recompute this line.
	SpendOrg     string         `json:"spendOrg"` // org whose spend produced this
	SpendUser    string         `json:"spendUser,omitempty"`
	SourceTxnID  string         `json:"sourceTxnId"`  // the usage transaction
	SpendCents   currency.Cents `json:"spendCents"`   // the org's charge this line derives from
	PoolFraction float64        `json:"poolFraction"` // effective (<=0.25) pool fraction
	Weight       float64        `json:"weight"`       // package weight used
	ShareRatio   float64        `json:"shareRatio"`   // weight / totalWeight

	// The accrued amount owed to this package.
	Currency currency.Type  `json:"currency"`
	Amount   currency.Cents `json:"amount"`

	Status Status `json:"status" orm:"default:pending"`

	// PayoutId links the line to the disbursement that settles it (set when
	// Status -> queued/settled). Empty while pending/held.
	PayoutId string `json:"payoutId,omitempty"`

	// Resolved maintainer target snapshot (denormalized from the resolver at
	// accrual time so the ledger is self-contained for audit). Empty -> held.
	FundingTarget string `json:"fundingTarget,omitempty"` // e.g. "github_sponsors:gin-gonic"
	FundingKind   string `json:"fundingKind,omitempty"`   // github_sponsors|opencollective|...

	// IdempotencyKey = ossattr.AccrualID(spendOrg, sourceTxnId, purl).
	IdempotencyKey string `json:"idempotencyKey"`

	Test bool `json:"test"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (a *OSSAccrual) Defaults() {
	a.Parent = a.Datastore().NewKey("synckey", "", 1, nil)
	if a.Status == "" {
		a.Status = Pending
	}
	if a.Currency == "" {
		a.Currency = "usd"
	}
}

func (a *OSSAccrual) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(a, ps); err != nil {
		return err
	}
	if len(a.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(a.Metadata_), &a.Metadata)
	}
	return err
}

func (a *OSSAccrual) Save() (ps []datastore.Property, err error) {
	a.Metadata_ = string(json.EncodeBytes(&a.Metadata))
	return datastore.SaveStruct(a)
}

func (a *OSSAccrual) Validator() *val.Validator { return nil }

func New(db *datastore.Datastore) *OSSAccrual {
	a := new(OSSAccrual)
	a.Init(db)
	a.Defaults()
	return a
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
