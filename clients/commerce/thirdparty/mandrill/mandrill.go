package mandrill

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/keighl/mandrill"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
)

type Client struct {
	client  *mandrill.Client
	context context.Context
}

// Convert our message to mandrill message type
func newMessage(message *email.Message) *mandrill.Message {
	m := new(mandrill.Message)

	m.FromEmail = message.From.Address
	m.FromName = message.From.Name
	m.Subject = message.Subject

	// Add recipients
	for _, to := range message.To {
		m.AddRecipient(to.Address, to.Name, "to")
	}
	for _, cc := range message.CC {
		m.AddRecipient(cc.Address, cc.Name, "cc")
	}
	for _, bcc := range message.BCC {
		m.AddRecipient(bcc.Address, bcc.Name, "bcc")
	}

	// Add global mail merge variables
	m.GlobalMergeVars = mandrill.MapToVars(message.Substitutions)

	// Add recpient unique merge variables
	mergeVars := make([]*mandrill.RcptMergeVars, 0)

	for k, v := range message.Personalizations {
		vars := mandrill.MapToRecipientVars(k, v.Substitutions)
		mergeVars = append(mergeVars, vars)
	}

	m.MergeVars = mergeVars

	gMV := map[string]interface{}{}
	for k, v := range message.TemplateData {
		for k2, v2 := range v {
			gMV[strings.ToUpper(k+k2)] = v2
		}
	}

	m.GlobalMergeVars = append(m.GlobalMergeVars, mandrill.MapToVars(gMV)...)

	// Add content
	if message.HTML != "" {
		m.HTML = message.HTML
	}

	if message.Text != "" {
		m.Text = message.Text
	}

	return m
}

// Send email
func (c *Client) Send(message *email.Message) error {
	var (
		res []*mandrill.Response
		err error
		msg = newMessage(message)
	)

	log.Info("Send Email %v, %v", message, msg, c.context)

	if message.TemplateID != "" {
		res, err = c.client.MessagesSendTemplate(msg, message.TemplateID, msg.GlobalMergeVars)
	} else {
		res, err = c.client.MessagesSend(msg)
	}

	if err != nil {
		log.Error(err)
		return err
	}

	log.Info("%v", res)

	return nil
}

func New(c context.Context, in integration.Mandrill) *Client {
	// Set deadline
	var cancel context.CancelFunc
	c, cancel = context.WithTimeout(c, time.Second*55)
	defer cancel()

	// Create standard HTTP client with timeout
	httpClient := &http.Client{
		Timeout: time.Second * 55,
	}

	client := mandrill.ClientWithKey(in.APIKey)
	client.HTTPClient = httpClient
	return &Client{
		client:  client,
		context: c,
	}
}
