package saleschannel

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[SalesChannel]("saleschannel") }

type SalesChannel struct {
	mixin.Model[SalesChannel]

	// Name of sales channel
	Name string `json:"name"`

	// Description of sales channel
	Description string `json:"description" datastore:",noindex"`

	// Whether this sales channel is disabled
	IsDisabled bool `json:"isDisabled"`

	// Arbitrary metadata
	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s *SalesChannel) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}

	return err
}

func (s *SalesChannel) Save() ([]datastore.Property, error) {
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))

	return datastore.SaveStruct(s)
}

func New(db *datastore.Datastore) *SalesChannel {
	s := new(SalesChannel)
	s.Init(db)
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("saleschannel")
}
