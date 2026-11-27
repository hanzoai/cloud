package provider

// discord_webhook.go — a Discord channel an org posts to, by webhook.
//
// This is not the `discord` connector, which installs the Hanzo bot into a
// server so people can talk to it. A webhook is the channel's own address for
// being posted to: a person makes one in the channel's settings and pastes it,
// and nothing needs an app, a bot, or a review. The webhook IS the credential,
// since the secret is in its address, so it is sealed and never shown again.
// The webhook's id is the key, so one org can post to several channels, and one
// webhook belongs to one org.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const discordWebhookProvider = "discord-webhook"

// linkSecret is where a message can be found: <server>/<channel>.
const linkSecret = "link"

// discordHosts are the addresses Discord serves webhooks from. A webhook on any
// other host is not Discord's, and is refused before anything is sent to it.
var discordHosts = map[string]bool{
	"discord.com": true, "canary.discord.com": true, "ptb.discord.com": true, "discordapp.com": true,
}

var discordHook = regexp.MustCompile(`^/api(?:/v\d+)?/webhooks/(\d{5,25})/([A-Za-z0-9_-]{20,128})$`)

// discordBase is the scheme and host a webhook is called at. A var so a test
// stands in for Discord; production never reassigns it.
var discordBase = ""

func init() {
	register(&Provider{
		ID:           discordWebhookProvider,
		Name:         "Discord",
		Description:  "Post to a Discord channel with its webhook.",
		Category:     "Social",
		Kind:         apiKeyKind,
		AdminOnly:    true,
		Secrets:      []string{accessSecret, linkSecret},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       discordVerify,
		Fields: []Field{
			{Name: "webhook", Label: "Webhook address", Secret: true, Required: true,
				Help: "In Discord open the channel's settings, Integrations, Webhooks, New Webhook, and copy the webhook URL."},
		},
	})
}

// discordHookOf reads a webhook address and answers its path and id. The path
// holds the secret, and is all that is kept of the address: the host is
// Discord's, and is chosen when the webhook is called.
func discordHookOf(raw string) (path, id string, err error) {
	u, perr := url.Parse(strings.TrimSpace(raw))
	if perr != nil || u.Scheme != "https" || u.User != nil || !discordHosts[strings.ToLower(u.Host)] {
		return "", "", errors.New("That is not a Discord webhook address.")
	}
	m := discordHook.FindStringSubmatch(u.Path)
	if m == nil {
		return "", "", errors.New("That is not a Discord webhook address.")
	}
	return "/api/webhooks/" + m[1] + "/" + m[2], m[1], nil
}

// discordAddress is where a webhook is called.
func discordAddress(path string) string {
	if discordBase != "" {
		return discordBase + path
	}
	return "https://discord.com" + path
}

func discordVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	path, id, err := discordHookOf(in.Fields["webhook"])
	if err != nil {
		return nil, verifyRefused(err.Error())
	}
	req, err := newRequest(ctx, http.MethodGet, discordAddress(path), "")
	if err != nil {
		return nil, verifyRefused("That is not a Discord webhook address.")
	}
	var hook struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		ChannelID string `json:"channel_id"`
		GuildID   string `json:"guild_id"`
	}
	if _, err := call(ctx, req, &hook); err != nil {
		var refused postErr
		if errors.As(err, &refused) && refused.status >= 400 && refused.status < 500 {
			return nil, verifyRefused("Discord does not know that webhook. It may have been deleted.")
		}
		return nil, verifyRefused("Discord could not be reached.")
	}
	if hook.ID != id {
		return nil, verifyRefused("Discord answered for a different webhook.")
	}
	label := hook.Name
	if label == "" {
		label = "Discord channel"
	}
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: "https://discord.com" + path, linkSecret: hook.GuildID + "/" + hook.ChannelID},
		ExternalID:   id,
		Key:          id,
		AccountLabel: label,
	}, nil
}

// postDiscord posts the words to the channel. Photos ride as embeds, which
// Discord draws under the words; a video is its address on a line of its own,
// which Discord unfurls into a player.
func postDiscord(ctx context.Context, to postTo) (string, error) {
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	if len(images) > 10 {
		return "", errors.New("Discord takes ten photos at most in a post")
	}
	path, _, err := discordHookOf(to.Token)
	if err != nil {
		return "", errors.New("the stored webhook is not a Discord webhook; reconnect the channel")
	}
	content := to.Text
	for _, v := range videos {
		content += "\n" + v
	}
	body := map[string]any{
		"content": content,
		// A post is words. Whether it pings a role or the whole server is not
		// something the words can ask for.
		"allowed_mentions": map[string]any{"parse": []string{}},
	}
	if len(images) > 0 {
		embeds := make([]map[string]any, len(images))
		for i, img := range images {
			embeds[i] = map[string]any{"image": map[string]string{"url": img}}
		}
		body["embeds"] = embeds
	}
	req, err := newJSONRequest(ctx, discordAddress(path)+"?wait=true", body)
	if err != nil {
		return "", err
	}
	var sent struct {
		ID        string `json:"id"`
		ChannelID string `json:"channel_id"`
	}
	if _, err := call(ctx, req, &sent); err != nil {
		return "", err
	}
	if sent.ID == "" {
		return "", errors.New("Discord answered without the message's id")
	}
	if guild, channel, ok := strings.Cut(to.Sealed[linkSecret], "/"); ok && guild != "" {
		if sent.ChannelID != "" {
			channel = sent.ChannelID
		}
		return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guild, channel, sent.ID), nil
	}
	return "https://discord.com/channels/@me", nil
}
