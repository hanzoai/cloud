package promotion

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Promotion]("promotion") }

type Promotion struct {
	mixin.Model[Promotion]

	Code           string     `json:"code"`
	Type           string     `json:"type"`
	Status         string     `json:"status" orm:"default:draft"`
	IsAutomatic    bool       `json:"isAutomatic"`
	IsTaxInclusive bool       `json:"isTaxInclusive"`
	CampaignId     string     `json:"campaignId"`
	StartsAt       *time.Time `json:"startsAt,omitempty"`
	EndsAt         *time.Time `json:"endsAt,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (p *Promotion) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	if len(p.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(p.Metadata_), &p.Metadata)
	}

	return err
}

func (p *Promotion) Save() ([]datastore.Property, error) {
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))

	return datastore.SaveStruct(p)
}

func New(db *datastore.Datastore) *Promotion {
	p := new(Promotion)
	p.Init(db)
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("promotion")
}
