package webhook

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"
)

type Events map[string]bool

func init() { orm.Register[Webhook]("webhook") }

type Webhook struct {
	mixin.Model[Webhook]

	// Name
	Name string `json:"name"`

	// Endpoint webhook should deliver events to.
	Url string `json:"url"`

	// Whether to use Live or Test data.
	Live bool `json:"live"`

	// Whether to send all events or selectively using Events.
	All bool `json:"all"`

	// Random token to check against
	AccessToken string `json:"accessToken"`

	// Events to selectively send.
	Events  Events `json:"events" datastore:"-" orm:"default:{}"`
	Events_ string `json:"-" datastore:",noindex"`

	// Whether this webhook is enabled or not.
	Enabled bool `json:"enabled"`
}

func (s *Webhook) Load(ps []datastore.Property) (err error) {
	// Ensure we're initialized
	if s.Events == nil {
		s.Events = make(Events)
	}

	// Load supported properties
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(s.Events_) > 0 {
		err = json.DecodeBytes([]byte(s.Events_), &s.Events)
	}

	return err
}

func (s *Webhook) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	s.Events_ = string(json.EncodeBytes(&s.Events))

	// Save properties
	return datastore.SaveStruct(s)
}

func New(db *datastore.Datastore) *Webhook {
	w := new(Webhook)
	w.Init(db)
	return w
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("webhook")
}
