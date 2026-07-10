package email

import (
	"encoding/gob"
	"time"
)

func init() {
	gob.Register(map[string]interface{}{})
}

// Email name and address for sender, recpient, etc
type Email struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Custom header
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Attachment holds attachement information
type Attachment struct {
	Content     string `json:"content,omitempty"`
	Type        string `json:"type,omitempty"`
	Name        string `json:"name,omitempty"`
	Filename    string `json:"filename,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	ContentID   string `json:"contentId,omitempty"`
}

// Map of substiutitons to apply to email on provider side
type Substitutions map[string]string

// Tags to replace in an email for a specific recipient
type Personalization struct {
	// Override default message
	Substitutions Substitutions `json:"substitution"`
	Subject       string        `json:"subject,omitempty"`
	Headers       []Header      `json:"headers,omitempty"`
	SendAt        time.Time     `json:"sendAt,omitempty"`
}

// Map of address -> personalizations
type Personalizations map[string]*Personalization

// Tracking settings for a given message
type Tracking struct {
	Opens  bool `json:"opens"`
	Clicks bool `json:"clicks"`
}

// Represents a single email message
type Message struct {
	Subject          string                            `json:"subject,omitempty"`
	From             Email                             `json:"from"`
	ReplyTo          Email                             `json:"replyTo,omitempty"`
	To               []Email                           `json:"to"`
	CC               []Email                           `json:"cc,omitempty"`
	BCC              []Email                           `json:"bcc,omitempty"`
	HTML             string                            `json:"html,omitempty"`
	Text             string                            `json:"text,omitempty"`
	TemplateData     map[string]map[string]interface{} `json:"templateData,omitempty"`
	TemplateID       string                            `json:"templateId,omitempty"`
	Attachments      []Attachment                      `json:"attachments,omitempty"`
	Substitutions    Substitutions                     `json:"substitutions,omitempty"`
	Personalizations Personalizations                  `json:"personalizations,omitempty"`
	Headers          []Header                          `json:"headers,omitempty"`
	SendAt           time.Time                         `json:"sendAt,omitempty"`
	Tags             []string                          `json:"tags,omitempty"`
	Tracking         Tracking                          `json:"tracking,omitempty"`
}

func (m *Message) AddAttachments(ats ...Attachment) {
	m.Attachments = append(m.Attachments, ats...)
}

func (m *Message) AddTos(tos ...Email) {
	m.To = append(m.To, tos...)
}

func (m *Message) AddCCs(ccs ...Email) {
	m.CC = append(m.CC, ccs...)
}

func (m *Message) AddBCCs(bccs ...Email) {
	m.BCC = append(m.BCC, bccs...)
}

func (m *Message) AddSubsitutions(subs Substitutions) {
	for k, v := range subs {
		m.Substitutions[k] = v
	}
}

func (m *Message) AddPersonalizations(personalizations Personalizations) {
	for k, v := range personalizations {
		m.Personalizations[k] = v
	}
}

func NewMessage() *Message {
	m := new(Message)
	m.To = make([]Email, 0)
	m.CC = make([]Email, 0)
	m.BCC = make([]Email, 0)
	m.Headers = make([]Header, 0)
	m.Tags = make([]string, 0)
	m.Substitutions = make(Substitutions)
	m.Personalizations = make(Personalizations)
	m.TemplateData = make(map[string]map[string]interface{})
	return m
}

func NewPersonalization() *Personalization {
	p := new(Personalization)
	p.Headers = make([]Header, 0)
	p.Substitutions = make(Substitutions)
	return p
}
