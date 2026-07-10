package pricingrule

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[PricingRule]("billing-pricing-rule") }

// PricingModel defines how usage is converted to cost.
type PricingModel string

const (
	PerUnit PricingModel = "per_unit"
	Tiered  PricingModel = "tiered"
	Volume  PricingModel = "volume"
)

// Tier represents a single pricing tier for tiered/volume pricing.
type Tier struct {
	UpTo  int64 `json:"upTo"`  // 0 = infinity (final tier)
	Price int64 `json:"price"` // cents per unit in this tier
	Flat  int64 `json:"flat"`  // flat fee for entering this tier
}

// PricingRule maps a meter to a cost model. For per-unit pricing,
// UnitPrice is used directly. For tiered/volume pricing, the Tiers
// array defines the pricing bands.
type PricingRule struct {
	mixin.Model[PricingRule]

	MeterId     string        `json:"meterId"`
	PlanId      string        `json:"planId,omitempty"`
	PricingType PricingModel  `json:"model"`
	Currency    currency.Type `json:"currency"`
	UnitPrice   int64         `json:"unitPrice"` // cents, for per_unit model

	Tiers  []Tier `json:"tiers,omitempty" datastore:"-"`
	Tiers_ string `json:"-" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (p *PricingRule) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	if len(p.Tiers_) > 0 {
		err = json.DecodeBytes([]byte(p.Tiers_), &p.Tiers)
		if err != nil {
			return err
		}
	}

	if len(p.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(p.Metadata_), &p.Metadata)
	}

	return err
}

func (p *PricingRule) Save() (ps []datastore.Property, err error) {
	p.Tiers_ = string(json.EncodeBytes(&p.Tiers))
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))
	return datastore.SaveStruct(p)
}

func (p *PricingRule) Validator() *val.Validator {
	return nil
}

// CalculateCost computes the total cost in cents for a given usage quantity.
func (p *PricingRule) CalculateCost(quantity int64) int64 {
	switch p.PricingType {
	case PerUnit:
		return quantity * p.UnitPrice

	case Tiered:
		return p.calculateTiered(quantity)

	case Volume:
		return p.calculateVolume(quantity)

	default:
		return quantity * p.UnitPrice
	}
}

// calculateTiered: each tier applies to the portion of usage within that band.
func (p *PricingRule) calculateTiered(quantity int64) int64 {
	var total int64
	var remaining = quantity

	for _, tier := range p.Tiers {
		if remaining <= 0 {
			break
		}

		var units int64
		if tier.UpTo == 0 || remaining <= tier.UpTo {
			// Final tier or remaining fits within this tier
			units = remaining
		} else {
			units = tier.UpTo
		}

		total += tier.Flat + (units * tier.Price)
		remaining -= units
	}

	return total
}

// calculateVolume: the total quantity determines which single tier applies
// to ALL units.
func (p *PricingRule) calculateVolume(quantity int64) int64 {
	for _, tier := range p.Tiers {
		if tier.UpTo == 0 || quantity <= tier.UpTo {
			return tier.Flat + (quantity * tier.Price)
		}
	}

	// Fallback: use last tier
	if len(p.Tiers) > 0 {
		last := p.Tiers[len(p.Tiers)-1]
		return last.Flat + (quantity * last.Price)
	}

	return 0
}

func New(db *datastore.Datastore) *PricingRule {
	p := new(PricingRule)
	p.Init(db)
	p.Parent = db.NewKey("synckey", "", 1, nil)
	if p.Currency == "" {
		p.Currency = "usd"
	}
	if p.PricingType == "" {
		p.PricingType = PerUnit
	}
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("billing-pricing-rule")
}
