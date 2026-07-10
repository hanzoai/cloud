package smtprelay

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

type Request struct {
	Username string        `json:"username"`
	Password string        `json:"password"`
	Host     string        `json:"host"`
	Port     string        `json:"port"`
	From     email.Email   `json:"mailFrom"`
	To       []email.Email `json:"mailTo"`
	Msg      string        `json:"msg"`
}

type Client struct {
	endpoint string
	username string
	password string
	settings integration.SMTPRelay
	client   *http.Client
}

func New(ctx context.Context, settings integration.SMTPRelay) *Client {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Second*55)
	defer cancel()

	// Create standard HTTP client with timeout
	client := &http.Client{
		Timeout: time.Second * 55,
	}

	return &Client{
		client:   client,
		endpoint: config.SMTPRelay.Endpoint,
		username: config.SMTPRelay.Username,
		password: config.SMTPRelay.Password,
		settings: settings,
	}
}

func (c *Client) Request(r *Request) error {
	var payload *bytes.Reader

	if r != nil {
		payload = bytes.NewReader(json.EncodeBytes(r))
	}

	req, err := http.NewRequest("POST", c.endpoint, payload)
	if err != nil {
		return err
	}

	req.SetBasicAuth(c.username, c.password)
	req.Header.Add("Content-Type", "application/json")

	res, err := c.client.Do(req)

	log.Debug(res)

	return err
}

func (c *Client) Send(message *email.Message) error {
	req := new(Request)
	req.To = message.To
	req.Msg = message.HTML
	return c.Request(req)
}
