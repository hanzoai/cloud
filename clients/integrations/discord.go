package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Discord is provider #3: a full OAuth2 GUILD-INSTALL integration. ONE Hanzo
// Discord app (DISCORD_CLIENT_ID/SECRET + a bot) is installed into an org's guild;
// the install returns the guild, which we map guild.id → org. Inbound is Discord's
// Interactions webhook (Ed25519-verified, discord_events.go) — an HTTP-native path
// that needs no gateway/websocket and no message-content intent; @hanzo is the
// `/hanzo` slash command.
//
// The per-guild custody is EMPTY: the bot is the app's single bot (no per-guild
// token), and interaction replies authenticate with the interaction token, so
// isolation is purely the guild_id → org gate. The card lights up on
// DISCORD_CLIENT_ID + DISCORD_CLIENT_SECRET; the interactions webhook additionally
// requires DISCORD_PUBLIC_KEY (Ed25519 verify key) and fails closed without it.
func init() {
	register(&Provider{
		ID:           "discord",
		Name:         "Discord",
		Description:  "Install the Hanzo bot in your server and run /hanzo.",
		Category:     "Communication",
		Scopes:       []string{"bot", "applications.commands"},
		RedirectPath: callbackPath("discord"),
		Secrets:      nil, // no per-guild secret; isolation is guild_id→org, replies use the interaction token
		Configured:   discordConfigured,
		Creds:        discordCreds,
		Authorize:    discordAuthorize,
		Exchange:     discordExchange,
		Revoke:       nil, // a guild removes the bot from Discord; no token to revoke
	})
}

const (
	discordClientIDEnv     = "DISCORD_CLIENT_ID"
	discordClientSecretEnv = "DISCORD_CLIENT_SECRET"
	discordPublicKeyEnv    = "DISCORD_PUBLIC_KEY"
	discordBotTokenEnv     = "DISCORD_BOT_TOKEN"
	// discordInstallPerms = VIEW_CHANNEL(1<<10) | SEND_MESSAGES(1<<11) |
	// USE_APPLICATION_COMMANDS(1<<31). Interaction replies don't require these, but
	// they let the bot also post normally.
	discordInstallPerms = "2147486720"
)

// discordOAuthAuthorizeURL / discordAPIBase are vars so tests can repoint them.
var (
	discordOAuthAuthorizeURL = "https://discord.com/oauth2/authorize"
	discordAPIBase           = "https://discord.com/api/v10"
)

func discordClientID() string     { return strings.TrimSpace(os.Getenv(discordClientIDEnv)) }
func discordClientSecret() string { return strings.TrimSpace(os.Getenv(discordClientSecretEnv)) }
func discordPublicKey() string    { return strings.TrimSpace(os.Getenv(discordPublicKeyEnv)) }
func discordBotToken() string     { return strings.TrimSpace(os.Getenv(discordBotTokenEnv)) }

func discordCreds() OAuthConfig {
	return OAuthConfig{ClientID: discordClientID(), ClientSecret: discordClientSecret()}
}

// discordConfigured gates the connect (guild-install OAuth) on the client id +
// secret. The interactions webhook separately requires DISCORD_PUBLIC_KEY.
func discordConfigured() bool { return discordClientID() != "" && discordClientSecret() != "" }

// discordAuthorize builds the guild-install consent URL (scope bot +
// applications.commands, with an install permission bitfield). Discord carries the
// state through and returns it (+ code + guild) on the callback.
func discordAuthorize(creds OAuthConfig, redirectURI, state string) (string, error) {
	q := url.Values{
		"client_id":     {creds.ClientID},
		"scope":         {"bot applications.commands"},
		"permissions":   {discordInstallPerms},
		"response_type": {"code"},
		"redirect_uri":  {redirectURI},
		"state":         {state},
	}
	return discordOAuthAuthorizeURL + "?" + q.Encode(), nil
}

// discordTokenResponse is the oauth2/token response for a guild install: `guild`
// carries the server the bot was added to.
type discordTokenResponse struct {
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	Guild       struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"guild"`
}

// discordExchange trades the install code for the guild identity. It custodies NO
// token (the bot is the app's single bot; replies use the interaction token), so it
// returns only the non-secret guild id/name for the connection row.
func discordExchange(ctx context.Context, creds OAuthConfig, redirectURI, code string) (*ExchangeResult, error) {
	form := url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordAPIBase+"/oauth2/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("discord oauth2/token http %d", resp.StatusCode)
	}
	var r discordTokenResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("discord token decode: %w", err)
	}
	if r.Error != "" {
		return nil, fmt.Errorf("discord oauth2/token: %s", r.Error)
	}
	if r.Guild.ID == "" {
		return nil, fmt.Errorf("discord oauth2/token: no guild in install response")
	}
	return &ExchangeResult{
		Tokens:       map[string]string{},
		ExternalID:   r.Guild.ID,
		AccountLabel: r.Guild.Name,
		Scopes:       splitSpace(r.Scope),
	}, nil
}

// splitSpace splits a space-separated scope string (Discord uses spaces, unlike
// Slack's commas).
func splitSpace(s string) []string {
	parts := strings.Fields(strings.TrimSpace(s))
	if len(parts) == 0 {
		return nil
	}
	return parts
}
