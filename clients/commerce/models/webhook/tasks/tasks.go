package tasks

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/webhook"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

type Payload struct {
	Data        interface{} `json:"data"`
	AccessToken string      `json:"accessToken"`
}

type Client struct {
	ctx    context.Context
	client *http.Client
}

func (c *Client) Post(url string, data interface{}) error {
	req, err := http.NewRequest("POST", url, json.EncodeBuffer(data))
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Hanzo/1.0")
	req.Header.Set("Content-Type", "application/json")

	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// Read response
	body, _ := ioutil.ReadAll(res.Body)
	log.Debug("Webhook endpoint '%s' responded with %v", url, body)

	return nil
}

func createClient(ctx context.Context) *Client {
	// Set timeout
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Second*20)
	defer cancel()

	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	return &Client{ctx: ctx, client: client}
}

// Fire webhooks
var Emit = delay.Func("webhook-emit", func(ctx context.Context, org string, event string, data interface{}) {
	log.JSON(fmt.Sprintf("Emit webhook '%s' for '%s'", event, org), data, ctx)

	db := datastore.New(ctx)
	db.SetNamespace(org)

	// Fetch any webhooks for this organization
	hooks := make([]*webhook.Webhook, 0)
	_, err := webhook.Query(db).GetAll(&hooks)
	if err != nil {
		log.Warn("Failed to retrieve webhooks for organization '%s': %v", org, err, ctx)
	}

	// No hooks! Bye!
	if len(hooks) == 0 {
		log.Debug("No webhooks defined for organization '%s'", org, ctx)
		return
	}

	// Create client to send event data
	client := createClient(ctx)

	for _, hook := range hooks {
		// Has all events enabled
		if hook.All {
			client.Post(hook.Url, Payload{
				Data:        data,
				AccessToken: hook.AccessToken,
			})
			continue
		}

		// Check if current event is enabled
		if enabled, ok := hook.Events[event]; ok && enabled {
			client.Post(hook.Url, Payload{
				Data:        data,
				AccessToken: hook.AccessToken,
			})
		}
	}
})
