package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ingress.go is the chat-ingress seam between the platform adapters and the
// /v1/channels transport plane (clients/channels), plus the transport send
// doors. The seam is the cloud.RegisterSync idiom (sync_seam.go): channels
// registers its consumer at Mount, adapters emit with NO import of channels —
// the dependency points one way and token custody never leaves this package.

// IngressEvent is one authenticated inbound chat event crossing the seam. Org
// is resolved via OrgForExternalID on a signature-verified payload; In is the
// adapter-normalized Inbound (bridge.go); ReplyRoot is a transport-verified
// reply root (Teams: the JWT-verified serviceURL; "" elsewhere).
type IngressEvent struct {
	Org       string
	In        Inbound
	ReplyRoot string
}

// ingressFn is the single registered consumer — nil until channels mounts.
// Written once at Mount before serving, read from webhook goroutines (the
// RegisterSync pattern, sync_seam.go).
var ingressFn func(context.Context, IngressEvent)

// RegisterIngress installs the ingress consumer. Called once at channels.Mount.
func RegisterIngress(fn func(context.Context, IngressEvent)) { ingressFn = fn }

// emitIngress hands one event to the registered consumer on a detached
// goroutine with its own bounded context, so the billed webhook path is never
// delayed and a panicking consumer cannot crash the shared process. No
// request-scoped context crosses the hop — everything the consumer needs rides
// the event. The adapter's bridgeLim slot ownership is untouched.
func emitIngress(org string, in Inbound, replyRoot string) {
	fn := ingressFn
	if fn == nil {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fn(ctx, IngressEvent{Org: org, In: in, ReplyRoot: replyRoot})
	}()
}

// LinkedSubject returns the Hanzo account subject bound to (org, provider,
// extUser) by the account-link flow (bridge_link.go / *_link.go). Returns
// ("", false, nil) when the user has not linked; an error (fail closed) on an
// unmounted subsystem, invalid org, or KMS-down.
func LinkedSubject(org, provider, extUser string) (string, bool, error) {
	if mounted == nil {
		return "", false, fmt.Errorf("integrations: not mounted")
	}
	link, ok, err := getUserLink(mounted, org, provider, extUser)
	if err != nil || !ok {
		return "", false, err
	}
	return link.Subject, true, nil
}

// ── transport send doors (each delegates to the ONE existing HTTP path) ──────

// SendSlack posts text to channel, threaded under threadTS when non-empty
// (slackPostThread, slack_events.go — posts top-level chat.postMessage when
// threadTS == ""). The token is the org's OWN custodied bot token: TokenFor
// fails closed for unmounted/unknown/not-connected/KMS-down, so an org that
// never connected Slack cannot post — the per-org token IS the tenancy gate.
func SendSlack(ctx context.Context, org, channel, threadTS, text string) error {
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return err
	}
	return slackPostThread(ctx, string(tok), channel, threadTS, text)
}

// SendTelegram posts text to chatID via the Bot API sendMessage, threaded under
// replyTo when non-zero (telegramSend, telegram_events.go).
func SendTelegram(ctx context.Context, chatID, replyTo int64, text string) error {
	return telegramSend(ctx, chatID, replyTo, text)
}

// SendTeams posts a message activity to conversationID at the Bot Connector
// serviceURL (teamsSendActivity, teams_events.go).
func SendTeams(ctx context.Context, serviceURL, conversationID, text string) error {
	return teamsSendActivity(ctx, serviceURL, conversationID, text)
}

// SendDiscord posts text to a Discord channel via POST /channels/{id}/messages,
// referencing replyTo when non-empty. Content is capped at discordMaxContent;
// the bot token rides only the Authorization header, which is never logged.
// Returns the created message id.
func SendDiscord(ctx context.Context, channelID, replyTo, text string) (string, error) {
	tok := discordBotToken()
	if tok == "" {
		return "", fmt.Errorf("discord: bot token not configured")
	}
	if len(text) > discordMaxContent {
		text = text[:discordMaxContent]
	}
	fields := map[string]any{"content": text}
	if replyTo != "" {
		fields["message_reference"] = map[string]string{"message_id": replyTo}
	}
	payload, _ := json.Marshal(fields)
	endpoint := discordAPIBase + "/channels/" + channelID + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("discord create message http %d", resp.StatusCode)
	}
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &m)
	return m.ID, nil
}
