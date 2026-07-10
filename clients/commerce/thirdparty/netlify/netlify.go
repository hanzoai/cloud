package netlify

import (
	"context"

	"github.com/netlify/netlify-go"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

type Client struct {
	ctx    context.Context
	client *netlify.Client
}

func New(ctx context.Context, accessToken string) *Client {
	log.Debug("Creating Netlify client using AccessToken: '%s'", accessToken, ctx)

	client := newOauthClient(ctx, accessToken)

	return &Client{
		ctx: ctx,
		client: netlify.NewClient(&netlify.Config{
			AccessToken: accessToken,
			HttpClient:  client,
			UserAgent:   "Hanzo/1.0",
		}),
	}
}
