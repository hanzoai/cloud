package sendgrid

import (
	"github.com/hanzoai/sendgrid-go/helpers/mail"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
)

// Send email
func newContent(contentType string, value string) *mail.Content {
	return &mail.Content{
		Type:  contentType,
		Value: value,
	}
}

func newEmail(email email.Email) *mail.Email {
	return &mail.Email{
		Name:    email.Name,
		Address: email.Address,
	}
}

func addSubsitutions(p *mail.Personalization, message *email.Message, address string) {
	if v, ok := message.Personalizations[address]; ok {
		for k, v := range v.Substitutions {
			p.SetSubstitution("-"+k+"-", v)
		}
	}
}

// Convert from our message type to sendgrid type
func newMessage(message *email.Message) []byte {
	m := new(mail.SGMailV3)

	// Set From
	from := newEmail(message.From)
	m.SetFrom(from)

	// Set subject
	m.Subject = message.Subject

	templateData := map[string]interface{}{}
	for k, v := range message.TemplateData {
		templateData[k] = v
	}

	// Add recipients + personalizations
	for _, to := range message.To {
		p := mail.NewPersonalization()
		p.AddTos(newEmail(to))
		p.DynamicTemplateData = templateData
		addSubsitutions(p, message, to.Address)
		m.AddPersonalizations(p)
	}

	for _, cc := range message.CC {
		p := mail.NewPersonalization()
		p.AddCCs(newEmail(cc))
		p.DynamicTemplateData = templateData
		addSubsitutions(p, message, cc.Address)
		m.AddPersonalizations(p)
	}

	for _, bcc := range message.BCC {
		p := mail.NewPersonalization()
		p.AddBCCs(newEmail(bcc))
		p.DynamicTemplateData = templateData
		addSubsitutions(p, message, bcc.Address)
		m.AddPersonalizations(p)
	}

	// Add section substitutions
	for k, v := range message.Substitutions {
		m.AddSection("-"+k+"-", v)
	}

	// Set tracking
	ts := mail.NewTrackingSettings()

	ct := mail.NewClickTrackingSetting()
	ct.SetEnable(message.Tracking.Clicks)
	ts.SetClickTracking(ct)

	ot := mail.NewOpenTrackingSetting()
	ot.SetEnable(message.Tracking.Opens)
	ts.SetOpenTracking(ot)

	m.SetTrackingSettings(ts)

	// Add content
	if message.Text != "" {
		m.AddContent(newContent("text/plain", message.Text))

	}

	if message.HTML != "" {
		m.AddContent(newContent("text/html", message.HTML))
	}

	// Use template if set
	if message.TemplateID != "" {
		m.SetTemplateID(message.TemplateID)
	}

	return mail.GetRequestBody(m)
}

// Send email
func (api API) Send(message *email.Message) error {
	body := newMessage(message)
	c := api.Context

	log.Info("Request Body: %v", string(body), c)

	res, err := api.Request("POST", "/v3/mail/send", nil, body)
	if err != nil {
		return log.Error("Failed to send email: %v", err, c)
	}
	log.Info("StatusCode: %v", res.StatusCode, c)
	log.Info("Body: %v", res.Body, c)
	log.Info("Headers: %v", res.Headers, c)
	return nil
}
