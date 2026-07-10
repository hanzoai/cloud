package notification

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Notification]("notification") }

type Channel string

const (
	Email Channel = "email"
	SMS   Channel = "sms"
	Push  Channel = "push"
)

type NotificationStatus string

const (
	Pending NotificationStatus = "pending"
	Sent    NotificationStatus = "sent"
	Failed  NotificationStatus = "failed"
)

type Notification struct {
	mixin.Model[Notification]

	To         string             `json:"to"`
	Channel    Channel            `json:"channel"`
	TemplateId string             `json:"templateId"`
	Status     NotificationStatus `json:"status" orm:"default:pending"`
	ProviderId string             `json:"providerId"`
	ExternalId string             `json:"externalId"`

	// Data stored as JSON in datastore
	Data  Map    `json:"data,omitempty" datastore:"-" orm:"default:{}"`
	Data_ string `json:"-" datastore:",noindex"`

	// Arbitrary metadata
	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (n *Notification) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(n, ps); err != nil {
		return err
	}

	if len(n.Data_) > 0 {
		if err = json.DecodeBytes([]byte(n.Data_), &n.Data); err != nil {
			return err
		}
	}

	if len(n.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(n.Metadata_), &n.Metadata)
	}

	return err
}

func (n *Notification) Save() ([]datastore.Property, error) {
	n.Data_ = string(json.EncodeBytes(&n.Data))
	n.Metadata_ = string(json.EncodeBytes(&n.Metadata))

	return datastore.SaveStruct(n)
}

func New(db *datastore.Datastore) *Notification {
	n := new(Notification)
	n.Init(db)
	return n
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("notification")
}
