package dispute

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Dispute]("dispute") }

// Status represents the state of a dispute.
type Status string

const (
	WarningNeedsResponse Status = "warning_needs_response"
	NeedsResponse        Status = "needs_response"
	UnderReview          Status = "under_review"
	Won                  Status = "won"
	Lost                 Status = "lost"
	WarningUnderReview   Status = "warning_under_review"
	WarningClosed        Status = "warning_closed"
)

// DisputeEvidence holds the evidence submitted for a dispute.
type DisputeEvidence struct {
	CustomerName         string `json:"customerName,omitempty"`
	CustomerEmailAddress string `json:"customerEmailAddress,omitempty"`
	ProductDescription   string `json:"productDescription,omitempty"`
	ServiceDate          string `json:"serviceDate,omitempty"`
	UncategorizedText    string `json:"uncategorizedText,omitempty"`
}

// Dispute represents a customer challenge to a charge.
type Dispute struct {
	mixin.Model[Dispute]

	Amount          int64                  `json:"amount"`
	Currency        currency.Type          `json:"currency"`
	Status          Status                 `json:"status"`
	ProviderRef     string                 `json:"providerRef"`
	Reason          string                 `json:"reason,omitempty"`
	EvidenceDueBy   time.Time              `json:"evidenceDueBy,omitempty"`
	PaymentIntentId string                 `json:"paymentIntentId,omitempty"`
	Evidence        *DisputeEvidence       `json:"evidence,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Created         time.Time              `json:"created,omitempty"`
}

func (d *Dispute) Defaults() {
	d.Parent = d.Datastore().NewKey("synckey", "", 1, nil)
	if d.Status == "" {
		d.Status = NeedsResponse
	}
}

func New(db *datastore.Datastore) *Dispute {
	d := new(Dispute)
	d.Init(db)
	d.Defaults()
	return d
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("dispute")
}
