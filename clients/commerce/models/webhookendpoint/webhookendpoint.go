package webhookendpoint

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[WebhookEndpoint]("webhook-endpoint") }

// WebhookEndpoint configures an HTTP endpoint that receives billing events.
type WebhookEndpoint struct {
	mixin.Model[WebhookEndpoint]

	// URL to POST events to
	Url string `json:"url"`

	// HMAC signing secret (auto-generated)
	Secret string `json:"secret,omitempty"`

	// Enabled/disabled status
	Status string `json:"status"`

	// Event types to receive, e.g. ["invoice.paid", "payment_intent.succeeded"]
	// Empty list means all events.
	Events  []string `json:"events,omitempty" datastore:"-"`
	Events_ string   `json:"-" datastore:",noindex"`

	Description string `json:"description,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (w *WebhookEndpoint) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(w, ps); err != nil {
		return err
	}

	if len(w.Events_) > 0 {
		if err = json.DecodeBytes([]byte(w.Events_), &w.Events); err != nil {
			return err
		}
	}

	if len(w.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(w.Metadata_), &w.Metadata)
	}

	return err
}

func (w *WebhookEndpoint) Save() (ps []datastore.Property, err error) {
	w.Events_ = string(json.EncodeBytes(&w.Events))
	w.Metadata_ = string(json.EncodeBytes(&w.Metadata))
	return datastore.SaveStruct(w)
}

func (w *WebhookEndpoint) Validator() *val.Validator {
	return nil
}

// MatchesEvent returns true if this endpoint should receive the given event type.
func (w *WebhookEndpoint) MatchesEvent(eventType string) bool {
	if len(w.Events) == 0 {
		return true // empty = receive all events
	}
	for _, t := range w.Events {
		if t == eventType || t == "*" {
			return true
		}
	}
	return false
}

func (w *WebhookEndpoint) Defaults() {
	w.Parent = w.Datastore().NewKey("synckey", "", 1, nil)
	if w.Status == "" {
		w.Status = "enabled"
	}
}

func New(db *datastore.Datastore) *WebhookEndpoint {
	w := new(WebhookEndpoint)
	w.Init(db)
	w.Defaults()
	return w
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("webhook-endpoint")
}
