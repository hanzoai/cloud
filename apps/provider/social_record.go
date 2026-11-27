package provider

import (
	"context"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// rememberSocial asks the social plane to list this account where the org
// manages its publish targets. The token stays here. A social process that is
// not running does not fail the consent: the connection is already sealed.
func rememberSocial(ctx context.Context, conn Connection) {
	if conn.Org == "" || conn.Provider == "" || conn.ExternalID == "" {
		return
	}
	if _, ok := socialNetworks[conn.Provider]; !ok {
		return
	}
	handle := conn.AccountLabel
	if handle == "" {
		handle = conn.ExternalID
	}
	_, err := cloud.Ask[client.SocialTargetIn, client.SocialTargetOut](
		cloud.For(ctx, conn.Org), "social", client.SocialRecordTarget,
		&client.SocialTargetIn{Provider: conn.Provider, ExternalID: conn.ExternalID, Handle: handle})
	if err != nil && mounted != nil {
		mounted.Log.Warn("social account was not recorded", "provider", conn.Provider, "org", conn.Org, "err", err)
	}
}

// forgetSocial asks the social plane to drop an account this connection was
// listed as. A social process that is not running does not fail the disconnect:
// the connection is already gone.
func forgetSocial(ctx context.Context, conn Connection) {
	if conn.Org == "" || conn.ExternalID == "" || !socialNetworks[conn.Provider] {
		return
	}
	_, err := cloud.Ask[client.SocialTargetIn, client.SocialTargetOut](
		cloud.For(ctx, conn.Org), "social", client.SocialForgetTarget,
		&client.SocialTargetIn{Provider: conn.Provider, ExternalID: conn.ExternalID})
	if err != nil && mounted != nil {
		mounted.Log.Warn("social account was not forgotten", "provider", conn.Provider, "org", conn.Org, "err", err)
	}
}

// socialNetworks are the networks an org posts through /v1/channel and /v1/post
// whose credentials and posters live in this connector registry.
var socialNetworks = map[string]bool{
	"x": true, "facebook": true, "instagram": true, "linkedin": true,
	"tiktok": true, "youtube": true, "threads": true, "pinterest": true,
	"reddit": true, "bluesky": true, "mastodon": true,
	"telegram-bot": true, "discord-webhook": true,
	"devto": true, "medium": true, "hashnode": true, "hanzo-cms": true,
}
