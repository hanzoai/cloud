package review

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[Review]("review") }

type Review struct {
	mixin.Model[Review]

	UserId string `json:"userId"`

	ProductId string `json:"productId"`
	VariantId string `json:"variantId"`

	Name    string `json:"name"`
	Device  string `json:"device"`
	Comment string `json:"comment" datastore:",noindex"`
	Rating  int    `json:"rating"`

	Enabled bool `json:"-"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (r *Review) Load(p []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(r, p); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(r.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(r.Metadata_), &r.Metadata)
	}

	return
}

func (r *Review) Save() (p []datastore.Property, err error) {
	// Serialize unsupported properties
	r.Metadata_ = string(json.EncodeBytes(&r.Metadata))

	// Save properties
	return datastore.SaveStruct(r)
}

// New creates a new Review wired to the given datastore.
func New(db *datastore.Datastore) *Review {
	r := new(Review)
	r.Init(db)
	return r
}

// Query returns a datastore query for reviews.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("review")
}
