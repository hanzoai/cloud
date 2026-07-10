package campaignbudget

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[CampaignBudget]("campaignbudget") }

type CampaignBudget struct {
	mixin.Model[CampaignBudget]

	CampaignId   string `json:"campaignId"`
	Type         string `json:"type"`
	CurrencyCode string `json:"currencyCode"`
	Limit        int    `json:"limit"`
	Used         int    `json:"used"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (c *CampaignBudget) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(c, ps); err != nil {
		return err
	}

	if len(c.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(c.Metadata_), &c.Metadata)
	}

	return err
}

func (c *CampaignBudget) Save() ([]datastore.Property, error) {
	c.Metadata_ = string(json.EncodeBytes(&c.Metadata))

	return datastore.SaveStruct(c)
}

func New(db *datastore.Datastore) *CampaignBudget {
	c := new(CampaignBudget)
	c.Init(db)
	return c
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("campaignbudget")
}
