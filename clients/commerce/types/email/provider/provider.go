package provider

// Available email providers
type Type string

const (
	Mailchimp Type = "mailchimp"
	Mandrill  Type = "mandrill"
	SendGrid  Type = "sendgrid"
	SmtpRelay Type = "smtprelay"
)
