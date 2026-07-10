package watchlist

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/movie"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[Watchlist]("watchlist") }

type Status string

type Watchlist struct {
	mixin.Model[Watchlist]

	// Associated user .
	UserId string `json:"userId,omitempty"`

	// Email of the user or someone else if no user id exists
	Email string `json:"email,omitempty"`

	// Individual line items
	Movies  []movie.Movie `json:"movies" datastore:"-"`
	Movies_ string        `json:"-" datastore:",noindex"`

	// Arbitrary key/value pairs associated with this order
	Metadata  Map    `json:"metadata" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (c *Watchlist) Validator() *val.Validator {
	return val.New()
}

func (c *Watchlist) Load(ps []datastore.Property) (err error) {
	// Prevent duplicate deserialization
	if c.Loaded() {
		return nil
	}

	// Ensure we're initialized
	c.Defaults()

	// Load supported properties
	if err = datastore.LoadStruct(c, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(c.Movies_) > 0 {
		err = json.DecodeBytes([]byte(c.Movies_), &c.Movies)
	}

	if len(c.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(c.Metadata_), &c.Metadata)
	}

	return err
}

func (w *Watchlist) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	w.Metadata_ = string(json.EncodeBytes(&w.Metadata))
	w.Movies_ = string(json.EncodeBytes(w.Movies))

	// Save properties
	return datastore.SaveStruct(w)
}

func (w *Watchlist) SetItem(db *datastore.Datastore, id string) (err error) {
	// Check if already exists
	for _, mv := range w.Movies {
		if mv.Id() == id {
			return nil
		}
	}

	// New movie
	m := &movie.Movie{}
	e := m.GetById(id)

	if e != nil {
		return e
	}

	w.Movies = append(w.Movies, *m)
	return nil

}

func (w *Watchlist) RemoveItem(id string) (err error) {
	mvs := make([]movie.Movie, 0)
	for _, mv := range w.Movies {
		if !(mv.Id() == id) {
			mvs = append(mvs, mv)
		}
	}
	w.Movies = mvs
	return nil
}

func (w Watchlist) MoviesJSON() string {
	return json.Encode(w.Movies)
}

func (c Watchlist) IntId() int {
	return int(c.Key().IntID())
}

func (c Watchlist) DisplayId() string {
	return strconv.Itoa(c.IntId())
}

func (c Watchlist) DisplayCreatedAt() string {
	duration := time.Since(c.CreatedAt)

	if duration.Hours() > 24 {
		year, month, day := c.CreatedAt.Date()
		return fmt.Sprintf("%s %s, %s", month.String(), strconv.Itoa(day), strconv.Itoa(year))
	}

	return humanize.Time(c.CreatedAt)
}

func (c Watchlist) Description() string {
	if c.Movies == nil {
		return ""
	}

	buffer := bytes.NewBufferString("")

	for i, item := range c.Movies {
		if i > 0 {
			buffer.WriteString(", ")
		}
		buffer.WriteString(item.Name)
	}
	return buffer.String()
}

func (w *Watchlist) Defaults() {
	w.Movies = make([]movie.Movie, 0)
	w.Metadata = make(Map)
}

func New(db *datastore.Datastore) *Watchlist {
	w := new(Watchlist)
	w.Init(db)
	w.Defaults()
	return w
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("watchlist")
}
