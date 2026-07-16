package integrations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// telegram_events.go is ADAPTER #4 of the ChatBridge core: the Telegram Bot API
// webhook + the deep-link connect. Its three edges —
//
//	inbound-auth : constant-time compare of X-Telegram-Bot-Api-Secret-Token
//	parse        : parseTelegramUpdate (pure) → the core's brain inputs
//	reply        : telegramSendMessage (sendMessage over the ONE env bot token)
//
// — and delegates EVERYTHING provider-blind to the core (bridgeSpawn dedupe org
// resolve, the brain, the per-user link). ISOLATION ROOT: the org comes ONLY from
// OrgForExternalID("telegram", chat_id), and chat_id is trustworthy only because
// the whole webhook is secret-token-verified first.
//
// MOUNT HANDOFF (integrations owner adds these — literals BEFORE the /:provider
// wildcards so they win, same discipline as the Slack bridge):
//
//	app.Post("/v1/integrations/telegram/connect", s.telegramConnect)   // shadows generic connect
//	app.Post("/v1/integrations/telegram/webhook", s.telegramWebhook)
//	app.Get("/v1/integrations/telegram/link",          s.telegramLink)
//	app.Get("/v1/integrations/telegram/link/auth",     s.telegramLinkAuth)
//	app.Get("/v1/integrations/telegram/link/callback", s.telegramLinkCallback)

const telegramSecretHeader = "X-Telegram-Bot-Api-Secret-Token"

// telegramAPIBase is the Telegram Bot API root; the bot token is a PATH segment
// (…/bot<token>/<method>). A var so tests can repoint it at an httptest stub.
var telegramAPIBase = "https://api.telegram.org"

// ── deep-link connect (shadows the generic OAuth connect) ───────────────────

// telegramConnect mints a short, single-use deep-link code bound to the caller's
// org and returns the t.me link the console navigates to. Org-authed: a caller with
// no validated principal is 403 (same gate as the framework connect). The code is
// stored as an oauth_nonce (org,telegram); the webhook's /start handler claims it to
// bind chat→org. It is short (128-bit hex) so it fits Telegram's 64-char `start`
// payload limit.
func telegramConnect(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required to connect an integration")
	}
	if !validOrg(org) {
		return zip.ErrBadRequest("org must be a DNS-1123 label")
	}
	if !telegramConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "telegram integration is not configured on this deployment")
	}
	username := telegramBotUsername()
	if username == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "telegram integration is missing TELEGRAM_BOT_USERNAME")
	}
	if _, err := s.State.store.GCNonces(c.Context(), staleNonceCutoff()); err != nil {
		s.Log.Warn("telegram: nonce gc", "err", err)
	}
	code, err := genToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.State.store.PutNonce(c.Context(), code, org, "telegram"); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "code: %v", err)
	}
	deepLink := "https://t.me/" + url.PathEscape(username) + "?start=" + url.QueryEscape(code)
	return c.JSON(http.StatusOK, map[string]any{"authorizeUrl": deepLink})
}

// ── webhook ─────────────────────────────────────────────────────────────────

// telegramWebhook is the Bot API webhook (setWebhook url:
// https://{domain}/v1/integrations/telegram/webhook). It constant-time-verifies the
// secret token, parses the update, and either binds a chat (/start <code>) or routes
// an @hanzo trigger to an on-behalf-of agent run — always acking 200 fast and doing
// billed work async, deduped durably on update_id.
func telegramWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	secret := telegramWebhookSecret()
	if secret == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "telegram webhook not configured")
	}
	// INBOUND AUTH: a shared secret Telegram echoes on every update. Constant-time
	// compare; a mismatch is a forged request → 401 (Telegram never sends a wrong one).
	if subtle.ConstantTimeCompare([]byte(c.Header(telegramSecretHeader)), []byte(secret)) != 1 {
		return zip.ErrUnauthorized("bad telegram secret token")
	}
	m, ok := parseTelegramUpdate(slackReadBody(c)) // slackReadBody = the shared bounded raw-body read
	if !ok {
		return c.NoContent(http.StatusOK) // non-message update (edited/callback/etc.) — nothing to do
	}

	// /start <code> or /connect <code> binds this chat to an org (idempotent).
	if code, isConnect := telegramConnectCode(m.Text); isConnect {
		telegramBind(s, c, m, code)
		return c.NoContent(http.StatusOK)
	}

	// Agent trigger? A private-chat message is always a trigger; a group message
	// must @mention the bot or use the /hanzo command. Non-triggers are ignored.
	prompt, trigger := telegramTrigger(m)
	if !trigger {
		return c.NoContent(http.StatusOK)
	}

	// ISOLATION ROOT, resolved SYNC (so the per-org limiter keys on the real tenant
	// and a shed happens BEFORE anything is recorded): org comes ONLY from the
	// chat→org bind for the secret-verified chat id. An unbound chat is dropped.
	chatID := strconv.FormatInt(m.ChatID, 10)
	org, ok := OrgForExternalID("telegram", chatID)
	if !ok {
		s.Log.Warn("telegram: message in unbound chat", "chat", chatID)
		return c.NoContent(http.StatusOK)
	}
	// SHED BEFORE the dedupe write (Red M-1): acquire a pool slot first; if the pool
	// is full, record NOTHING and return a retriable NON-2xx so Telegram re-delivers
	// when a slot frees (no lost message, no double-run).
	if !bridgeLim.acquire(org) {
		s.Log.Warn("telegram: at capacity, shedding for retry", "org", org)
		return zip.Errorf(http.StatusTooManyRequests, "telegram agent pool at capacity")
	}
	// Slot held. DURABLE dedupe (billed path) on update_id: release on every path
	// that does NOT dispatch. Fail CLOSED on a dedupe error.
	fresh, err := s.State.store.MarkEvent(c.Context(), "telegram", strconv.FormatInt(m.UpdateID, 10))
	if err != nil {
		bridgeLim.release(org)
		s.Log.Warn("telegram: dedupe error, skipping", "err", err)
		return c.NoContent(http.StatusOK)
	}
	if !fresh {
		bridgeLim.release(org)
		return c.NoContent(http.StatusOK)
	}
	if _, gerr := s.State.store.GCEvents(c.Context(), staleEventCutoff()); gerr != nil {
		s.Log.Warn("telegram: dedupe gc", "err", gerr)
	}
	in := Inbound{
		Provider: "telegram", ExternalID: chatID, User: strconv.FormatInt(m.UserID, 10),
		Channel: chatID, ThreadID: strconv.FormatInt(m.MessageID, 10), Text: prompt,
		DedupeKey: strconv.FormatInt(m.UpdateID, 10),
	}
	reply := telegramReplier(in)
	bridgeSpawn(s, org, func() { runBridgeTurn(s, org, in, reply) })
	return c.NoContent(http.StatusOK)
}

// telegramBind claims a deep-link connect code and binds this chat to its org. The
// bind reply goes to the chat so the connector sees success. Wrong/expired code →
// a terse instruction (never a silent no-op).
func telegramBind(s *cloud.Service[state], c *zip.Ctx, m telegramMessage, code string) {
	org, ok, err := s.State.store.ClaimNonce(c.Context(), code, "telegram")
	if err != nil {
		s.Log.Warn("telegram: claim connect code", "err", err)
		return
	}
	chatID := strconv.FormatInt(m.ChatID, 10)
	if !ok {
		_ = telegramSend(c.Context(), m.ChatID, m.MessageID, "That connect link is invalid or already used. Start again from the Hanzo console.")
		return
	}
	conn := Connection{Org: org, Provider: "telegram", ExternalID: chatID, AccountLabel: sanitizeMeta(m.ChatTitle)}
	if err := s.State.store.Upsert(c.Context(), conn); err != nil {
		s.Log.Warn("telegram: bind upsert", "chat", chatID, "err", err)
		return
	}
	s.Log.Info("telegram: chat bound", "chat", chatID, "org", org)
	_ = telegramSend(c.Context(), m.ChatID, m.MessageID, "Connected to Hanzo. Mention @hanzo (or /hanzo) to chat.")
}

// telegramReplier builds the reply closure for one inbound message: sendMessage to
// the chat, threaded under the triggering message. Telegram has no ephemeral in a
// group; the link prompt is safe to post in-chat because redemption re-verifies the
// Telegram user (Login Widget) — a bystander clicking it links only THEIR own account.
func telegramReplier(in Inbound) replyFunc {
	return func(ctx context.Context, text string, _ephemeral bool) error {
		chatID, _ := strconv.ParseInt(in.Channel, 10, 64)
		msgID, _ := strconv.ParseInt(in.ThreadID, 10, 64)
		return telegramSend(ctx, chatID, msgID, text)
	}
}

// telegramSend posts a message via the Bot API sendMessage, threaded under
// replyTo. The bot token is a URL path segment and is never logged.
func telegramSend(ctx context.Context, chatID, replyTo int64, text string) error {
	tok := telegramBotToken()
	if tok == "" {
		return fmt.Errorf("telegram: bot token not configured")
	}
	form := url.Values{
		"chat_id": {strconv.FormatInt(chatID, 10)},
		"text":    {text},
	}
	if replyTo != 0 {
		form.Set("reply_to_message_id", strconv.FormatInt(replyTo, 10))
	}
	endpoint := telegramAPIBase + "/bot" + tok + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &r)
	if !r.OK {
		return fmt.Errorf("telegram sendMessage failed: %s", nonEmpty(r.Description, "unknown"))
	}
	return nil
}

// ── parse (pure — Telegram's parse edge) ────────────────────────────────────

// telegramMessage is the normalized subset of a Telegram Update.message we act on.
type telegramMessage struct {
	UpdateID  int64
	MessageID int64
	UserID    int64
	IsBot     bool
	ChatID    int64
	ChatType  string
	ChatTitle string
	Text      string
}

// telegramUpdate is the raw Update envelope (message subset).
type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		From      struct {
			ID    int64 `json:"id"`
			IsBot bool  `json:"is_bot"`
		} `json:"from"`
		Chat struct {
			ID       int64  `json:"id"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			Username string `json:"username"`
		} `json:"chat"`
	} `json:"message"`
}

// parseTelegramUpdate extracts a telegramMessage from a raw update. ok=false for a
// non-message update (edited_message, callback_query, channel_post, …), a bot's own
// message, or an empty-text message — the caller acks those with nothing to do.
func parseTelegramUpdate(raw []byte) (telegramMessage, bool) {
	var u telegramUpdate
	if err := json.Unmarshal(raw, &u); err != nil || u.Message == nil {
		return telegramMessage{}, false
	}
	msg := u.Message
	if msg.From.IsBot || strings.TrimSpace(msg.Text) == "" {
		return telegramMessage{}, false
	}
	title := msg.Chat.Title
	if title == "" {
		title = msg.Chat.Username
	}
	return telegramMessage{
		UpdateID: u.UpdateID, MessageID: msg.MessageID, UserID: msg.From.ID, IsBot: msg.From.IsBot,
		ChatID: msg.Chat.ID, ChatType: msg.Chat.Type, ChatTitle: title, Text: msg.Text,
	}, true
}

// telegramConnectCode returns the connect code from a "/start <code>" or
// "/connect <code>" command (tolerating a "@botusername" suffix on the command), and
// whether the text WAS such a command. A bare "/start" with no code is NOT a connect.
func telegramConnectCode(text string) (string, bool) {
	f := strings.Fields(strings.TrimSpace(text))
	if len(f) < 2 {
		return "", false
	}
	cmd := strings.ToLower(f[0])
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}
	if cmd == "/start" || cmd == "/connect" {
		return f[1], true
	}
	return "", false
}

// telegramTrigger decides whether a (non-connect) message is an @hanzo agent
// trigger and returns the stripped prompt. A private-chat message is always a
// trigger. A group message triggers only on an @<botusername> mention or the
// /hanzo command; the mention/command token is stripped so the agent gets the
// actual prompt. A non-triggering group message returns ("", false).
func telegramTrigger(m telegramMessage) (string, bool) {
	text := strings.TrimSpace(m.Text)
	// /hanzo command (any chat), tolerating a @botusername suffix.
	if f := strings.Fields(text); len(f) > 0 {
		cmd := strings.ToLower(f[0])
		if at := strings.IndexByte(cmd, '@'); at >= 0 {
			cmd = cmd[:at]
		}
		if cmd == "/hanzo" {
			return strings.TrimSpace(strings.TrimPrefix(text, f[0])), true
		}
	}
	if m.ChatType == "private" {
		return text, true // DM: everything is a prompt
	}
	// Group: require an @<botusername> mention; strip it.
	if u := telegramBotUsername(); u != "" {
		at := "@" + u
		if idx := indexFold(text, at); idx >= 0 {
			stripped := strings.TrimSpace(text[:idx] + text[idx+len(at):])
			return stripped, true
		}
	}
	return "", false
}

// indexFold is a case-insensitive strings.Index (Telegram usernames are compared
// case-insensitively).
func indexFold(s, substr string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}
