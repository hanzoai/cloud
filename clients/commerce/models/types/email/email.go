package email

import (
	"encoding/gob"
)

// Types of system-defined emails
type Type string

const (
	OrderConfirmation     Type = "order.confirmation"
	UserWelcome           Type = "user.welcome"
	UserEmailConfirmation Type = "user.emailConfirmation"
	UserEmailConfirmed    Type = "user.emailConfirmed"
	UserPasswordReset     Type = "user.passwordReset"
	SubscriberWelcome     Type = "subscriber.welcome"
)

// Specific email configuration
type Email struct {
	Enabled       bool   `json:"enabled"`
	IntegrationId string `json:"integrationId"`

	FromEmail string `json:"fromEmail"`
	FromName  string `json:"fromName"`

	Cc  []string `json:"cc,omitempty"`
	Bcc []string `json:"bcc,omitempty"`

	Subject string `json:"subject"`

	// HTML template to render email from (on our end)
	Template string `json:"template" datastore:",noindex"`

	// ID of remote HTML template (i.e., Mandrill, Sendgrid managed templates)
	TemplateId string `json:"templateId"`

	// HTML / Text body to use (will override any templating directives)
	Html string `json:"html" datastore:",noindex"`
	Text string `json:"text" datastore:",noindex"`
}

// System-wide email settings
type Settings struct {

	// Default email configuration
	Defaults struct {
		Enabled       bool     `json:"enabled"`
		IntegrationId string   `json:"integrationId"`
		FromName      string   `json:"fromName"`
		FromEmail     string   `json:"fromEmail"`
		Cc            []string `json:"cc,omitempty"`
		Bcc           []string `json:"bcc,omitempty"`
	} `json:"defaults"`

	// Per-email configuration
	Order struct {
		Confirmation Email `json:"confirmation"`
	} `json:"order"`

	User struct {
		Welcome           Email `json:"welcome"`
		EmailConfirmation Email `json:"emailConfirmation"`
		EmailConfirmed    Email `json:"emailConfirmed"`
		PasswordReset     Email `json:"passwordReset"`
	} `json:"user"`

	Subscriber struct {
		Welcome Email `json:"welcome"`
	} `json:"subscriber"`
}

// Return email settings updated from defaults
func (s Settings) Config(typ Type) Email {
	conf := Email{}

	switch typ {
	case OrderConfirmation:
		conf = s.Order.Confirmation
	case UserWelcome:
		conf = s.User.Welcome
	case UserEmailConfirmation:
		conf = s.User.EmailConfirmation
	case UserEmailConfirmed:
		conf = s.User.EmailConfirmed
	case UserPasswordReset:
		conf = s.User.PasswordReset
	case SubscriberWelcome:
		conf = s.Subscriber.Welcome
	}

	// Use organization defaults
	if !s.Defaults.Enabled {
		conf.Enabled = false
	}

	if conf.FromEmail == "" {
		conf.FromEmail = s.Defaults.FromEmail
	}

	if conf.FromName == "" {
		conf.FromName = s.Defaults.FromName
	}

	if len(conf.Cc) == 0 {
		conf.Cc = s.Defaults.Cc
	}

	if len(conf.Bcc) == 0 {
		conf.Bcc = s.Defaults.Bcc
	}

	if conf.IntegrationId == "" {
		conf.IntegrationId = s.Defaults.IntegrationId
	}

	return conf
}

func init() {
	gob.Register(Email{})
	gob.Register(Settings{})
}
