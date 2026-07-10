package promotionrule

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[PromotionRule]("promotionrule") }

type PromotionRule struct {
	mixin.Model[PromotionRule]

	PromotionId string `json:"promotionId"`
	Attribute   string `json:"attribute"`
	Operator    string `json:"operator"`

	// Values stored as JSON-encoded string in datastore
	Values  []string `json:"values" datastore:"-"`
	Values_ string   `json:"-" datastore:",noindex"`
}

func (p *PromotionRule) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	if len(p.Values_) > 0 {
		err = json.DecodeBytes([]byte(p.Values_), &p.Values)
	}

	return err
}

func (p *PromotionRule) Save() ([]datastore.Property, error) {
	p.Values_ = string(json.EncodeBytes(&p.Values))

	return datastore.SaveStruct(p)
}

func New(db *datastore.Datastore) *PromotionRule {
	p := new(PromotionRule)
	p.Init(db)
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("promotionrule")
}
