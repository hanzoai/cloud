package meter

import (
	"github.com/hanzoai/orm"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var eventKind = "meter-event"

// MeterEvent records a single usage data point for a meter.

func init() { orm.Register[MeterEvent]("meter-event") }

type MeterEvent struct {
	mixin.Model[MeterEvent]

	MeterId   string    `json:"meterId"`
	UserId    string    `json:"userId"`
	Value     int64     `json:"value"`
	Timestamp time.Time `json:"timestamp"`

	// Idempotency key for deduplication
	Idempotency string `json:"idempotency,omitempty"`

	// JSON-encoded dimension values, e.g. {"model":"gpt-4","region":"us"}
	Dimensions  Map    `json:"dimensions,omitempty" datastore:"-"`
	Dimensions_ string `json:"-" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}



func (e *MeterEvent) Defaults() {
	e.Parent = e.Datastore().NewKey("synckey", "", 1, nil)
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
}

func (e *MeterEvent) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(e, ps); err != nil {
		return err
	}

	if len(e.Dimensions_) > 0 {
		err = json.DecodeBytes([]byte(e.Dimensions_), &e.Dimensions)
		if err != nil {
			return err
		}
	}

	if len(e.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(e.Metadata_), &e.Metadata)
	}

	return err
}

func (e *MeterEvent) Save() (ps []datastore.Property, err error) {
	e.Dimensions_ = string(json.EncodeBytes(&e.Dimensions))
	e.Metadata_ = string(json.EncodeBytes(&e.Metadata))
	return datastore.SaveStruct(e)
}

func (e *MeterEvent) Validator() *val.Validator {
	return nil
}

func NewEvent(db *datastore.Datastore) *MeterEvent {
	e := new(MeterEvent)
	e.Init(db)
	e.Defaults()
	return e
}

func QueryEvents(db *datastore.Datastore) datastore.Query {
	return db.Query(eventKind)
}
