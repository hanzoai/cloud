package role

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Role]("role") }

type Role struct {
	mixin.Model[Role]

	Name string `json:"name"`

	// Permissions stored as JSON in datastore
	Permissions  []string `json:"permissions" datastore:"-" orm:"default:[]"`
	Permissions_ string   `json:"-" datastore:",noindex"`

	// Arbitrary metadata
	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (r *Role) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(r, ps); err != nil {
		return err
	}

	if len(r.Permissions_) > 0 {
		if err = json.DecodeBytes([]byte(r.Permissions_), &r.Permissions); err != nil {
			return err
		}
	}

	if len(r.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(r.Metadata_), &r.Metadata)
	}

	return err
}

func (r *Role) Save() ([]datastore.Property, error) {
	r.Permissions_ = string(json.EncodeBytes(&r.Permissions))
	r.Metadata_ = string(json.EncodeBytes(&r.Metadata))

	return datastore.SaveStruct(r)
}

func New(db *datastore.Datastore) *Role {
	r := new(Role)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("role")
}
