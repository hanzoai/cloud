package coupon

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

type Redemption struct {
	// Time coupon was used
	CreatedAt time.Time `json:"createdAt"`

	// Coupon code or dynamic code
	Code string `json:"code"`
}

func (c *Coupon) SaveRedemption() error {
	db := datastore.New(c.Context())
	_, err := db.Put("redemption", &Redemption{time.Now(), c.Code()})
	return err
}

func (c *Coupon) Redemptions() int {
	db := datastore.New(c.Context())
	count, _ := db.Query("redemption").Filter("Code=", c.Code()).Count()
	return count
}

func (c *Coupon) Redeemable() bool {
	if !c.Enabled {
		log.Debug("Coupon Not Enabled")
		return false
	}

	// Unlimited coupon usage if limit is set less than 1
	if c.Limit < 1 {
		log.Debug("Limit is Infinite")
		return true
	}

	r := c.Redemptions()
	if r >= c.Limit {
		log.Debug("c.Redemptions %v >= c.Limits %v", r, c.Limit)
		return false
	}

	return true
}
