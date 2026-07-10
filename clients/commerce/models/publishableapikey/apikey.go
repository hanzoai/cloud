package publishableapikey

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[PublishableApiKey]("publishableapikey") }

type KeyType string

const (
	Publishable KeyType = "publishable"
	Secret      KeyType = "secret"
)

type PublishableApiKey struct {
	mixin.Model[PublishableApiKey]

	Title      string     `json:"title"`
	Type       KeyType    `json:"type"`
	Token      string     `json:"-"`
	Salt       string     `json:"-"`
	Redacted   string     `json:"redacted"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	RevokedBy  string     `json:"revokedBy"`
}

func New(db *datastore.Datastore) *PublishableApiKey {
	k := new(PublishableApiKey)
	k.Init(db)
	return k
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("publishableapikey")
}
