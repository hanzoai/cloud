package plaid

import (
	"context"
	"net/http"
	"time"

	"github.com/plaid/plaid-go/v15/plaid"
)

func New(ctx context.Context, client_id, secret, pub_key string, env Environment) *Client {
	// Set deadline
	ctx, cancel := context.WithTimeout(ctx, time.Second*55)
	defer cancel()

	// Set HTTP Client
	httpClient := &http.Client{Timeout: 55 * time.Second}

	configuration := plaid.NewConfiguration()
	configuration.HTTPClient = httpClient
	configuration.AddDefaultHeader("PLAID-CLIENT-ID", client_id)
	configuration.AddDefaultHeader("PLAID-SECRET", secret)
	configuration.UseEnvironment(plaid.Sandbox) // Available environments are Sandbox, Development, and Production
	pc := plaid.NewAPIClient(configuration)

	return &Client{pc, ctx}
}
