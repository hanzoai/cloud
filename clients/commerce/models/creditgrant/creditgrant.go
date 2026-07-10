package creditgrant

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[CreditGrant]("credit-grant") }

// CreditGrant represents a discrete credit allocation for a user.
// Grants can have expiry dates, priority ordering, and meter eligibility
// restrictions. The burn-down algorithm consumes grants in priority order
// (lower priority number = consumed first), then by earliest expiry.
type CreditGrant struct {
	mixin.Model[CreditGrant]

	UserId         string        `json:"userId"`
	Name           string        `json:"name"`
	AmountCents    int64         `json:"amountCents"`
	RemainingCents int64         `json:"remainingCents"`
	Currency       currency.Type `json:"currency" orm:"default:usd"`

	EffectiveAt time.Time `json:"effectiveAt"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`

	// Lower priority = burn first
	Priority int `json:"priority"`

	// JSON: list of meter IDs this grant applies to (empty = all meters)
	Eligibility  []string `json:"eligibility,omitempty" datastore:"-"`
	Eligibility_ string   `json:"-" datastore:",noindex"`

	// Searchable tags: "promo", "purchased", "earned"
	Tags string `json:"tags,omitempty"`

	Voided bool `json:"voided"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (g *CreditGrant) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(g, ps); err != nil {
		return err
	}

	if len(g.Eligibility_) > 0 {
		err = json.DecodeBytes([]byte(g.Eligibility_), &g.Eligibility)
		if err != nil {
			return err
		}
	}

	if len(g.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(g.Metadata_), &g.Metadata)
	}

	return err
}

func (g *CreditGrant) Save() (ps []datastore.Property, err error) {
	g.Eligibility_ = string(json.EncodeBytes(&g.Eligibility))
	g.Metadata_ = string(json.EncodeBytes(&g.Metadata))
	return datastore.SaveStruct(g)
}

func (g *CreditGrant) Validator() *val.Validator {
	return nil
}

// IsActive returns true if the grant is usable right now.
func (g *CreditGrant) IsActive() bool {
	now := time.Now()
	if g.Voided {
		return false
	}
	if g.RemainingCents <= 0 {
		return false
	}
	if now.Before(g.EffectiveAt) {
		return false
	}
	if !g.ExpiresAt.IsZero() && now.After(g.ExpiresAt) {
		return false
	}
	return true
}

// IsEligibleForMeter checks if this grant can be applied to a given meter.
// Empty eligibility means eligible for all meters.
func (g *CreditGrant) IsEligibleForMeter(meterId string) bool {
	if len(g.Eligibility) == 0 {
		return true
	}
	for _, id := range g.Eligibility {
		if id == meterId {
			return true
		}
	}
	return false
}

// Defaults sets runtime-computed defaults that cannot be expressed as struct tags.
func (g *CreditGrant) Defaults() {
	if g.EffectiveAt.IsZero() {
		g.EffectiveAt = time.Now()
	}
}

func New(db *datastore.Datastore) *CreditGrant {
	g := new(CreditGrant)
	g.Init(db)
	g.Parent = db.NewKey("synckey", "", 1, nil)
	g.Defaults()
	return g
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("credit-grant")
}
