package coupon

import (
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/timeutil"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Coupon]("coupon") }

type Type string

const (
	Flat         Type = "flat"
	Percent           = "percent"
	FreeShipping      = "free-shipping"
	FreeItem          = "free-item"
)

var Types = []Type{Flat, Percent, FreeShipping}

type Coupon struct {
	mixin.Model[Coupon]

	Name string `json:"name"`

	// Possible values: flat, percent, free_shipping.
	Type Type `json:"type"`

	// Coupon code (must be unique).
	Code_   string `json:"code" datastore:"Code"`
	RawCode string `json:"-" datastore:"-"`

	// Indicates whether or not the Code is dynamically checked (for something like user-generated coupons)
	Dynamic bool `json:"dynamic"`

	CampaignId string `json:"campaignId,omitempty"`
	ReferrerId string `json:"referrerId,omitempty"`

	// Range in which coupon is valid
	StartDate time.Time `json:"startDate"`
	EndDate   time.Time `json:"endDate"`

	// Possible values: order, product.
	Filter string `json:"filter"`

	// Indicates whether this coupon may be applied once or more than once at checkout.
	Once bool `json:"once"`

	// The number of times this coupon can be used before it is used up and useless.  0 = unlimited
	Limit int `json:"limit"`

	// Product id for product-specific coupons.
	ProductId string `json:"productId,omitempty"`

	// Whether coupon is valid.
	Enabled bool `json:"enabled" orm:"default:true"`

	// Coupon amount. $5 should be 500 (prices in basic currency unit, like cents). 10% should be 10.
	// TODO: This needs to be currency.Cents in Hanzo.
	Amount int `json:"amount"`

	// Number of times coupon was redeemed.
	Used int `json:"used"`

	// Free product with coupon
	FreeProductId string `json:"freeProductId"`
	FreeVariantId string `json:"freeVariantId"`
	FreeQuantity  int    `json:"freeQuantity"`

	// List of buyer email addresses who have redeemed coupon.
	// Buyers []string `json:"buyers"`
}

func (co *Coupon) Load(ps []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(co, ps); err != nil {
		return err
	}

	return err
}

func (co *Coupon) Save() (ps []datastore.Property, err error) {

	co.Code_ = strings.ToUpper(co.Code_)

	// Save properties
	return datastore.SaveStruct(co)
}

func (c Coupon) Code() string {
	if c.RawCode != "" && c.RawCode != c.Code_ {
		return c.RawCode
	} else {
		return c.Code_
	}
}

func (c Coupon) DynamicCode() string {
	if c.RawCode == c.Code_ {
		return ""
	}

	return c.RawCode
}

func (c *Coupon) CodeFromId(uniqueid string) string {
	cid := c.Key()
	uid, err := hashid.DecodeKey(c.Context(), uniqueid)
	if err != nil {
		return ""
	}

	// Normal kind id for coupon is 3, this is 3333 to prevent accidental
	// decoding as normal hashid
	return hashid.Encode(3333, int(cid.IntID()), int(uid.IntID()))
}

func (c Coupon) ValidFor(t time.Time) bool {
	if !c.Enabled {
		log.Warn("Coupon Not Enabled", c.Context())
		return false // currently active, no need to check?
	}

	if !timeutil.IsZero(c.StartDate) && c.StartDate.After(t) {
		log.Warn("Coupon not yet Usable: %v > %v", c.StartDate.Unix(), t, c.Context())
		return false
	}

	if !timeutil.IsZero(c.EndDate) && c.EndDate.Before(t) {
		log.Warn("Coupon is Expired: %v < %v", c.EndDate.Unix(), t, c.Context())
		return false
	}

	return true
}

func (c Coupon) ItemId() string {
	if c.ProductId != "" {
		return c.ProductId
	}
	if c.FreeProductId != "" {
		return c.FreeProductId
	}
	if c.FreeVariantId != "" {
		return c.FreeProductId
	}
	return ""
}

func New(db *datastore.Datastore) *Coupon {
	c := new(Coupon)
	c.Init(db)
	return c
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("coupon")
}
