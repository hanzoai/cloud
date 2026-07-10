package partner

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/commission"
	"github.com/hanzoai/cloud/clients/commerce/models/types/schedule"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Partner]("partner") }

type Partner struct {
	mixin.Model[Partner]

	Enabled   bool `json:"enabled"`
	Connected bool `json:"connected"`

	Name     string  `json:"name"`
	Email    string  `json:"email,omitempty"`
	Phone    string  `json:"phone,omitempty"`
	Address  Address `json:"address,omitempty"`
	Website  string  `json:"website,omitempty"`
	Country  string  `json:"country"`
	TaxId    string  `json:"taxId"`
	Timezone string  `json:"timezone"`

	Commission commission.Commission `json:"commission"`
	Schedule   schedule.Schedule     `json:"schedule"`

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

// New creates a new Partner wired to the given datastore.
func New(db *datastore.Datastore) *Partner {
	p := new(Partner)
	p.Init(db)
	return p
}

// Query returns a datastore query for partners.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("partner")
}
