package subscriber

import (
	"crypto/md5"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
	. "github.com/hanzoai/cloud/clients/commerce/util/strings"
)

var mailchimpReserved = map[string]bool{
	"INTERESTS":  true,
	"REWARDS":    true,
	"ARCHIVE":    true,
	"USER_URL":   true,
	"DATE":       true,
	"EMAIL":      true,
	"EMAIL_TYPE": true,
	"TO":         true,
	"MC":         true,
	"LIST":       true,
}

var invalidFieldNameRe = regexp.MustCompile("[ -]")
var invalidNameChars = regexp.MustCompile(`[-_ ]`)
var invalidNameMorpheme = regexp.MustCompile(`^f|^l|^full|name$`)

func normalizeName(s string) string {
	empty := []byte("")
	b := invalidNameChars.ReplaceAll([]byte(s), empty)
	b = invalidNameMorpheme.ReplaceAll(b, empty)
	return string(b)
}

func init() { orm.Register[Subscriber]("subscriber") }

type Subscriber struct {
	mixin.Model[Subscriber]

	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`

	Email  string `json:"email"`
	FormId string `json:"formId"`
	UserId string `json:"userId,omitempty"`

	Unsubscribed    bool      `json:"unsubscribed"`
	UnsubscribeDate time.Time `json:"unsubscribeDate,omitempty"`

	Client client.Client `json:"client"`

	Tags []string `json:"tags" orm:"default:[]"`

	Metadata  Map    `json:"metadata" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (s Subscriber) Md5() string {
	h := md5.New()
	io.WriteString(h, s.Email)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (s Subscriber) Name() string {
	firstName := ""
	lastName := ""
	fullName := ""

	// Check metadata for name keys
	for k := range s.Metadata {
		k = normalizeName(k)
		if v, ok := s.Metadata[k].(string); ok {
			if k == "first" || k == "FNAME" {
				firstName = v
			}
			if k == "last" || k == "LNAME" {
				lastName = v
			}
			if k == "full" {
				fullName = v
			}

		}
	}

	// Use full name if found
	if fullName != "" {
		return fullName
	}

	// Combine first/last into full name
	parts := make([]string, 0)
	if firstName != "" {
		parts = append(parts, firstName)
	}

	if lastName != "" {
		parts = append(parts, lastName)
	}

	return strings.Join(parts, " ")
}

func (s Subscriber) MergeFields() map[string]interface{} {
	fields := make(map[string]interface{})

	for k, v := range s.Metadata {
		fields[k] = fmt.Sprintf("%v", v)
	}

	// Update metadata with some extra client data
	fields["useragent"] = s.Client.UserAgent
	fields["referer"] = s.Client.Referer
	fields["language"] = s.Client.Language
	fields["country"] = s.Client.Country
	fields["region"] = s.Client.Region
	fields["city"] = s.Client.City

	// Normalize merge fields
	for k, v := range fields {
		// Remove any empty merge fields
		if v == "" {
			delete(fields, k)
		}

		// Rename invalid fieldnames
		if invalidFieldNameRe.Match([]byte(k)) {
			k2 := invalidFieldNameRe.ReplaceAll([]byte(k), []byte("_"))
			fields[string(k2)] = v
			delete(fields, k)
		}

		// Reserved field names should be renamed
		if _, ok := mailchimpReserved[strings.ToUpper(k)]; ok {
			fields["_"+k] = v
			delete(fields, k)
		}
	}

	return fields
}

func (s *Subscriber) Load(ps []datastore.Property) (err error) {
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

func (s *Subscriber) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))

	// Save properties
	return datastore.SaveStruct(s)
}

func (s *Subscriber) Normalize() {
	s.Email = StripWhitespace(s.Email)
	s.Email = strings.ToLower(s.Email)
}

func FromJSON(db *datastore.Datastore, data []byte) *Subscriber {
	s := New(db)
	json.DecodeBytes(data, s)
	return s
}

// New creates a new Subscriber wired to the given datastore.
func New(db *datastore.Datastore) *Subscriber {
	s := new(Subscriber)
	s.Init(db)
	return s
}

// Query returns a datastore query for subscribers.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("subscriber")
}
