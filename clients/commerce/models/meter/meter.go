package meter

import (
	"github.com/hanzoai/orm"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var kind = "meter"

// AggregationType controls how meter events are aggregated.
type AggregationType string

const (
	AggSum   AggregationType = "sum"
	AggCount AggregationType = "count"
	AggLast  AggregationType = "last"
)

// Meter defines a named usage metric with a specific aggregation strategy.
// Each meter has a unique EventName per org (e.g. "input_tokens", "api_calls").

func init() { orm.Register[Meter]("meter") }

type Meter struct {
	mixin.Model[Meter]

	Name            string          `json:"name"`
	EventName       string          `json:"eventName"`
	AggregationType AggregationType `json:"aggregationType"`
	Currency        currency.Type   `json:"currency"`

	// JSON-encoded list of dimension names, e.g. ["model","region"]
	Dimensions  []string `json:"dimensions,omitempty" datastore:"-"`
	Dimensions_ string   `json:"-" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (m *Meter) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(m, ps); err != nil {
		return err
	}

	if len(m.Dimensions_) > 0 {
		err = json.DecodeBytes([]byte(m.Dimensions_), &m.Dimensions)
		if err != nil {
			return err
		}
	}

	if len(m.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(m.Metadata_), &m.Metadata)
	}

	return err
}

func (m *Meter) Save() (ps []datastore.Property, err error) {
	m.Dimensions_ = string(json.EncodeBytes(&m.Dimensions))
	m.Metadata_ = string(json.EncodeBytes(&m.Metadata))
	return datastore.SaveStruct(m)
}

func (m *Meter) Validator() *val.Validator {
	return nil
}

func (m *Meter) Defaults() {
	m.Parent = m.Datastore().NewKey("synckey", "", 1, nil)
}

func New(db *datastore.Datastore) *Meter {
	m := new(Meter)
	m.Init(db)
	m.Defaults()
	return m
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
