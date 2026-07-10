package networktoken

import (
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var kind = "network-token"

// Status represents the lifecycle state of a network token.
type Status string

const (
	Active    Status = "active"
	Suspended Status = "suspended"
	Deleted   Status = "deleted"
)

// Network represents the card network.
type Network string

const (
	Visa       Network = "visa"
	Mastercard Network = "mastercard"
	Amex       Network = "amex"
)

// NetworkToken represents an EMVCo network token (DPAN) provisioned
// through a Token Service Provider (TSP).

func init() { orm.Register[NetworkToken]("network-token") }

type NetworkToken struct {
	mixin.Model[NetworkToken]

	// Reference to the vault card token (tok_...)
	CardTokenId string `json:"cardTokenId"`

	// EMV payment token (DPAN) — NOT the real PAN
	NetworkToken string `json:"networkToken,omitempty"`

	// Token expiry (may differ from card expiry)
	TokenExpiry string `json:"tokenExpiry,omitempty"`

	// TSP reference ID for lifecycle management
	TokenReference string `json:"tokenReference"`

	// Payment Account Reference — links tokens across networks
	PAR string `json:"par,omitempty"`

	// Card network: visa, mastercard, amex
	Network Network `json:"network"`

	// Current status
	Status Status `json:"status"`

	// Token Requestor ID (identifies the merchant/platform)
	TokenRequestorId string `json:"tokenRequestorId,omitempty"`

	// Last successful cryptogram refresh
	LastRefreshed time.Time `json:"lastRefreshed,omitempty"`

	// Metadata for wallet integrations (Apple Pay, Google Pay)
	WalletType string `json:"walletType,omitempty"` // apple_pay, google_pay, none

	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Suspend marks the token as suspended (e.g., lost card).
func (nt *NetworkToken) Suspend() error {
	if nt.Status != Active {
		return fmt.Errorf("cannot suspend network token in status %s", nt.Status)
	}
	nt.Status = Suspended
	return nil
}

// Resume reactivates a suspended token.
func (nt *NetworkToken) Resume() error {
	if nt.Status != Suspended {
		return fmt.Errorf("cannot resume network token in status %s", nt.Status)
	}
	nt.Status = Active
	return nil
}

// Delete marks the token for deletion.
func (nt *NetworkToken) MarkDeleted() error {
	if nt.Status == Deleted {
		return fmt.Errorf("network token already deleted")
	}
	nt.Status = Deleted
	return nil
}

// IsUsable returns true if the token can be used for transactions.
func (nt *NetworkToken) IsUsable() bool {
	return nt.Status == Active
}

func (nt *NetworkToken) Defaults() {
	nt.Parent = nt.Datastore().NewKey("synckey", "", 1, nil)
	if nt.Status == "" {
		nt.Status = Active
	}
	if nt.Metadata == nil {
		nt.Metadata = make(Map)
	}
}

func New(db *datastore.Datastore) *NetworkToken {
	nt := new(NetworkToken)
	nt.Init(db)
	nt.Defaults()
	return nt
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
