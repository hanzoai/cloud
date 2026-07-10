package aggregate

import (
	"strconv"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[Aggregate]("aggregate") }

type Frequency string

const (
	Hourly Frequency = "Hourly"
	Daily            = "Daily"
)

func Init(a *Aggregate, name string, t time.Time, freq Frequency) {
	switch freq {
	case Hourly:
		a.BinTimestamp = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	case Daily:
		a.BinTimestamp = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	default:
		a.BinTimestamp = t
	}
	a.Name = name
	a.Instance = name + "_" + string(freq) + "_" + strconv.Itoa(int(a.BinTimestamp.Unix()))
}

type Aggregate struct {
	mixin.Model[Aggregate]

	Instance     string    `json:"instance"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	BinTimestamp time.Time `json:"binTimestamp"`
	Value        int64     `json:"value"`
	VectorValue  []int64   `json:"vectorValue,omitempty" orm:"default:[]"`
}

func (a *Aggregate) Load(p []datastore.Property) (err error) {
	// Ensure we're initialized
	if a.VectorValue == nil {
		a.VectorValue = make([]int64, 0)
	}

	// Load supported properties
	return datastore.LoadStruct(a, p)
}

func (a *Aggregate) Save() (p []datastore.Property, err error) {
	// Save properties
	return datastore.SaveStruct(a)
}

func New(db *datastore.Datastore) *Aggregate {
	a := new(Aggregate)
	a.Init(db)
	return a
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("aggregate")
}
