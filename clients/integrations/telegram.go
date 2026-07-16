package integrations

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Telegram is the token-in-env chat provider: ONE Hanzo bot (created with
// @BotFather, TELEGRAM_BOT_TOKEN in cloud env) that every org binds their own
// chat/group to via a one-click t.me deep link — no OAuth, no per-org bot. The bot
// token is the OUTBOUND auth (sendMessage); a shared webhook secret
// (TELEGRAM_WEBHOOK_SECRET) is the INBOUND auth. Orgs are resolved from the
// inbound chat id.
//
// Telegram does NOT fit the OAuth authorize→callback round trip, so it SHADOWS the
// generic connect with its own literal POST /v1/integrations/telegram/connect
// (telegram_events.go) that mints a short, single-use deep-link code → org. The
// registry entry here exists for the CARD (list/get/available/connected/disconnect)
// and gates `available` on the bot token; its Authorize/Exchange are never reached
// (the literal connect + webhook own the flow) and fail loud if they ever are.
func init() {
	register(&Provider{
		ID:           "telegram",
		Name:         "Telegram",
		Description:  "Add the Hanzo bot to a Telegram chat and mention @hanzo.",
		Category:     "Communication",
		Scopes:       nil,
		RedirectPath: callbackPath("telegram"), // required by Mount's assertion; the generic /callback is unused for Telegram
		Secrets:      nil,                      // no per-connection secret — ONE global bot token in env; isolation is chat_id→org
		Configured:   telegramConfigured,
		Creds:        telegramCreds,
		Authorize:    telegramAuthorizeUnused,
		Exchange:     telegramExchangeUnused,
		Revoke:       nil, // nothing to revoke at Telegram; disconnect forgets the chat→org row
	})
}

const (
	telegramBotTokenEnv      = "TELEGRAM_BOT_TOKEN"
	telegramWebhookSecretEnv = "TELEGRAM_WEBHOOK_SECRET"
	telegramBotUsernameEnv   = "TELEGRAM_BOT_USERNAME"
)

func telegramBotToken() string      { return strings.TrimSpace(os.Getenv(telegramBotTokenEnv)) }
func telegramWebhookSecret() string { return strings.TrimSpace(os.Getenv(telegramWebhookSecretEnv)) }
func telegramBotUsername() string   { return strings.TrimSpace(os.Getenv(telegramBotUsernameEnv)) }

// telegramConfigured lights the card up as soon as the bot token lands. The webhook
// additionally requires TELEGRAM_WEBHOOK_SECRET (fails closed 503 without it) and
// the deep-link connect additionally requires TELEGRAM_BOT_USERNAME.
func telegramConfigured() bool { return telegramBotToken() != "" }

func telegramCreds() OAuthConfig { return OAuthConfig{} } // unused; Telegram has no OAuth app creds

// telegramAuthorizeUnused / telegramExchangeUnused are never reached: the literal
// telegram connect route + the webhook own the flow (see telegram_events.go). They
// fail loud rather than fabricate an OAuth round trip Telegram does not have.
func telegramAuthorizeUnused(OAuthConfig, string, string) (string, error) {
	return "", fmt.Errorf("telegram: use the deep-link connect, not OAuth authorize")
}

func telegramExchangeUnused(context.Context, OAuthConfig, string, string) (*ExchangeResult, error) {
	return nil, fmt.Errorf("telegram: use the deep-link connect, not OAuth exchange")
}
