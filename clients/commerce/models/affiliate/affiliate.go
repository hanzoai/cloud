package affiliate

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/commission"
	"github.com/hanzoai/cloud/clients/commerce/models/types/schedule"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Affiliate]("affiliate") }

type Affiliate struct {
	mixin.Model[Affiliate]

	Enabled   bool `json:"enabled"`
	Connected bool `json:"connected"`

	UserId   string `json:"userId,omitempty"`
	Name     string `json:"name,omitempty"`
	Company  string `json:"company,omitempty"`
	Country  string `json:"country,omitempty"`
	TaxId    string `json:"taxId,omitempty"`
	Timezone string `json:"timezone,omitempty"`

	Commission commission.Commission `json:"commission,omitempty"`
	Schedule   schedule.Schedule     `json:"schedule,omitempty"`
	CouponId   string                `json:"couponId,omitempty"`

	Stripe struct {
		AccessToken    string
		PublishableKey string
		RefreshToken   string
		UserId         string

		// Save entire live and test tokens (legacy)
		Live integration.StripeConnectToken
		Test integration.StripeConnectToken
	} `json:"-"`
}

func New(db *datastore.Datastore) *Affiliate {
	a := new(Affiliate)
	a.Init(db)
	a.Schedule.Period = 30
	a.Schedule.Type = schedule.DailyRolling
	return a
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("affiliate")
}
