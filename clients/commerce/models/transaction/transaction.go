package transaction

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// IAMUserKind is the destination/source kind of the gateway-spendable wallet:
// the balance the cloud gateway's prepaid gate reads and its usage debits. A
// credit INTO this wallet mints spendable inference/GPU value cross-charged to
// Hanzo's real upstream spend, so it is the money-GA asset the mint invariant
// protects. Legacy per-org "user"/"account" order wallets are org-scoped store
// credit (checkout capture, account balance) and are NOT gateway-spendable.
const IAMUserKind = "iam-user"

// MintRequiresAuthorization implements mintauth.Guarded: it reports whether
// persisting THIS transaction mints spendable balance into the gateway-honored
// IAM-user wallet. The datastore write sink (mintauth.Enforce) consults it so
// that enforcement lives at the ledger layer, not in per-route gates.
//
//   - Deposit → mints: a deposit has NO funded source, so any deposit crediting
//     the IAM-user wallet creates spendable balance from nothing.
//   - Transfer → mints when its destination is the IAM-user wallet: value moves
//     INTO the gateway-spendable balance. A legitimate internal transfer between
//     the org's own funded IAM-user accounts is itself a debit on the source
//     (which required a prior authorized mint), and moving value into the
//     gateway wallet is exactly what must be authorized.
//   - Withdraw / Hold / HoldRemoved → never mint (debits / reservations).
//
// Non-IAM-user destinations (legacy order wallets) are not gateway-spendable and
// are deliberately out of scope — gating them would break checkout capture with
// no money-GA benefit.
func (t *Transaction) MintRequiresAuthorization() bool {
	if t.DestinationKind != IAMUserKind {
		return false
	}
	switch t.Type {
	case Deposit, Transfer:
		return true
	default:
		return false
	}
}

// Compile-time guarantee the ledger sink can gate this entity.
var _ mintauth.Guarded = (*Transaction)(nil)

func init() {
	orm.Register[Transaction]("transaction", orm.WithParent[Transaction](func(db orm.DB) orm.Key {
		return db.NewKey("synckey", "", 1, nil)
	}))
}

type Type string

const (
	Hold        Type = "hold"
	HoldRemoved Type = "hold-removed"
	Transfer    Type = "transfer"
	Deposit     Type = "deposit"
	Withdraw    Type = "withdraw"
)

type Transaction struct {
	mixin.Model[Transaction]

	DestinationId   string `json:"destinationId"`
	DestinationKind string `json:"destinationKind"`

	Currency currency.Type  `json:"currency"`
	Amount   currency.Cents `json:"amount"`
	Type     Type           `json:"type"`

	Test bool `json:"test"`

	// Short text human readable description
	Notes string `json:"notes,omitempty"`

	// For searching
	Tags string `json:"tags,omitempty"`

	Event string `json:"event,omitempty"`

	// Source Data
	// We store Kind even though it is encoded in id for easier reference
	SourceId   string `json:"sourceId,omitempty"`
	SourceKind string `json:"sourceKind,omitempty"`

	// Scope attribution for per-scope spend caps (issue #70). Both are INDEXED so
	// spend can be summed per (project,service) scope for the calendar-month
	// period. Empty = the org-wide default scope (a legacy row that predates
	// scoping has NO Project/Service property and is therefore only ever counted
	// in an unfiltered org-wide sum, never in a project- or service-scoped one).
	Project string `json:"project,omitempty"`
	Service string `json:"service,omitempty"`

	// ExpiresAt marks when a deposit credit expires. Zero value means no expiry.
	// Expired deposits are excluded from balance calculations.
	ExpiresAt time.Time `json:"expiresAt,omitempty"`

	// Deprecated
	UserId string `json:"-"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (t *Transaction) Load(ps []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(t, ps); err != nil {
		return err
	}

	if t.UserId != "" {
		t.DestinationId = t.UserId
		t.DestinationKind = "user"
		t.UserId = ""
	}

	if len(t.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(t.Metadata_), &t.Metadata)
	}

	return err
}

func (t *Transaction) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	t.Metadata_ = string(json.EncodeBytes(&t.Metadata))

	// Save properties
	return datastore.SaveStruct(t)
}

func (t *Transaction) Validator() *val.Validator {
	return nil
}

func New(db *datastore.Datastore) *Transaction {
	t := new(Transaction)
	t.Init(db)
	t.Parent = db.NewKey("synckey", "", 1, nil)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("transaction")
}
