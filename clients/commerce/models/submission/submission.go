package submission

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Submission]("submission") }

type Submission struct {
	mixin.Model[Submission]

	Email  string `json:"email"`
	UserId string `json:"userId,omitempty"`

	Client client.Client `json:"client"`

	Metadata  Map    `json:"metadata" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s *Submission) Load(ps []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}

	return err
}

func (s *Submission) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))

	// Save properties
	return datastore.SaveStruct(s)
}

func (s *Submission) Validator() *val.Validator {
	return val.New()
}

func FromJSON(db *datastore.Datastore, data []byte) *Submission {
	s := New(db)
	json.DecodeBytes(data, s)
	return s
}

// New creates a new Submission wired to the given datastore.
func New(db *datastore.Datastore) *Submission {
	s := new(Submission)
	s.Init(db)
	return s
}

// Query returns a datastore query for submissions.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("submission")
}
