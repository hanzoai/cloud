package sendgrid

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/sendgrid-go"
	"github.com/sendgrid/rest"

	"github.com/hanzoai/cloud/clients/commerce/types/integration"
)

type API struct {
	Context context.Context
	Client  *rest.Client
	Key     string
}

func New(c context.Context, settings integration.SendGrid) *API {
	// Set deadline
	var cancel context.CancelFunc
	c, cancel = context.WithTimeout(c, time.Second*55)
	defer cancel()

	// Create standard HTTP client with timeout
	httpClient := &http.Client{
		Timeout: time.Second * 55,
	}

	client := &rest.Client{HTTPClient: httpClient}

	return &API{
		Client:  client,
		Context: c,
		Key:     settings.APIKey,
	}
}

// Make a single API call to Sendgrid
func (api API) Request(method string, url string, params map[string]string, body []byte) (*rest.Response, error) {
	req := sendgrid.GetRequest(api.Key, url, "")

	req.Method = rest.Method(method)
	if params != nil {
		req.QueryParams = params
	}

	if body != nil {
		req.Body = body
	}

	return api.Client.API(req)
}
