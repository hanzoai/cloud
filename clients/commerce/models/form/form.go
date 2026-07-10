package form

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/types/form"
	"github.com/hanzoai/cloud/clients/commerce/models/types/thankyou"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/util/fs"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Form]("form") }

var jsTemplate = ""
var Submit = form.Submit
var Subscribe = form.Subscribe

// Settings used for injection into form.js
type Settings struct {
	// Name of list
	Name string `json:"name"`

	// Type of form
	Type form.Type `json:"type"`

	// Thank you settings
	ThankYou ThankYou `json:"thankyou"`
}

// Thank you configuration
type ThankYou struct {
	Type thankyou.Type `json:"type"`
	Url  string        `json:"url,omitempty"`
	HTML string        `json:"html,omitempty"`
}

type Form struct {
	mixin.Model[Form]

	// Name of list
	Name string `json:"name"`

	// Type of form
	Type form.Type `json:"type"`

	// Whether to send email confirmation
	SendWelcome bool `json:"sendWelcome"`

	// Overwrites default Template Id
	WelcomeTemplateId string `json:"welcomeTemplateId"`

	// Email list settings for this list
	EmailList email.List `json:"emailList,omitempty"`

	// Email forwarding
	Forward struct {
		Email   string `json:"email"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	} `json:"forward"`

	// Thank you settings
	ThankYou ThankYou `json:"thankyou"`

	// Conversion tracking info
	Facebook struct {
		Id       string `json:"id"`
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"facebook"`

	Google struct {
		Category string `json:"category"`
		Name     string `json:"name"`
	} `json:"google"`

	Mailchimp struct {
		ListId string `json:"listId"`
		APIKey string `json:"apiKey"`
	} `json:"mailchimp,omitempty"`
}

func (f *Form) Defaults() {
	f.Facebook.Value = "0.00"
	f.Facebook.Currency = "USD"
	f.ThankYou.Type = thankyou.Disabled
	f.EmailList.Enabled = true
	f.Type = "subscribe"
}

func (f *Form) Validator() *val.Validator {
	return val.New()
}

func (f *Form) AddSubscriber(s *subscriber.Subscriber) error {
	fkey := f.Key()
	s.FormId = f.Id()
	s.Parent = fkey
	s.Normalize()

	return f.Datastore().RunInTransaction(func(db *datastore.Datastore) error {
		keys, err := subscriber.Query(db).Ancestor(fkey).Filter("Email=", s.Email).GetKeys()

		if len(keys) != 0 {
			return SubscriberAlreadyExists
		}

		if err != nil {
			return err
		}

		return s.Create()
	}, nil)
}

func (f *Form) Js() string {
	if jsTemplate == "" {
		var cwd, _ = os.Getwd()
		jsTemplate = string(fs.ReadFile(cwd + "/resources/form.js"))

	}

	// Endpoint for subscription
	endpoint := config.UrlFor("api", "/form/", f.Id(), "/subscribe")
	// Check for development environment via environment variable
	if env := os.Getenv("ENV"); env == "development" || env == "dev" {
		endpoint = "http://localhost:8080" + endpoint
	} else {
		endpoint = "https:" + endpoint
	}

	return fmt.Sprintf(jsTemplate, endpoint, json.Encode(Settings{f.Name, f.Type, f.ThankYou}))
}

func New(db *datastore.Datastore) *Form {
	f := new(Form)
	f.Init(db)
	f.Defaults()
	return f
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("form")
}

func FromJSON(db *datastore.Datastore, data []byte) *Form {
	f := New(db)
	json.DecodeBytes(data, f)
	return f
}
