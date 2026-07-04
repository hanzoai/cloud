package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Slack connector. Name == the clients/integrations provider id "slack", so
// RunContext.Token resolves the SAME per-org bot token the OAuth plane sealed into
// KMS. Fails closed if the org has not connected Slack (Token returns an error).

// slackBotTokenSecret is the KMS secret name Slack's bot token is custodied under.
// It MUST equal the unexported const of the same name in
// clients/integrations/slack.go ("bot_token"); duplicated here because Go cannot
// import an unexported identifier. The integrations OAuth exchange seals the
// xoxb-… token under exactly this name.
const slackBotTokenSecret = "bot_token"

// slackAPIBase is Slack's Web API root. A var so a happy-path test can point it at
// an httptest stub; production hits the fixed public endpoint.
var slackAPIBase = "https://slack.com/api"

// slackClient bounds every Slack call with a real timeout (fail-secure).
var slackClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Connector{
		Name:        "slack",
		DisplayName: "Slack",
		AuthType:    "bot_token",
		AuthReq:     true,
		Actions: map[string]*Action{
			"send_message": {
				Name:        "send_message",
				DisplayName: "Send Message",
				Description: "Post a message to a Slack channel via chat.postMessage.",
				Props: []PropSpec{
					{Name: "channel", Type: "string", Required: true, Description: "Channel id or name."},
					{Name: "text", Type: "string", Required: true, Description: "Message text."},
				},
				Run: runSlackSendMessage,
			},
		},
	})
}

func runSlackSendMessage(ctx context.Context, rc RunContext) (any, error) {
	// Credential first: fail closed if Slack is not connected for THIS org. Token is
	// bound to (in.Owner, "slack"), so it can only ever return this tenant's token.
	tok, err := rc.Token(slackBotTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("slack not connected: %w", err)
	}
	channel := strInput(rc.Input, "channel")
	text := strInput(rc.Input, "text")
	if channel == "" || text == "" {
		return nil, fmt.Errorf("slack send_message: channel and text are required")
	}

	payload, err := jsonBytes(map[string]any{"channel": channel, "text": text})
	if err != nil {
		return nil, fmt.Errorf("slack send_message: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackAPIBase+"/chat.postMessage", payload)
	if err != nil {
		return nil, fmt.Errorf("slack send_message: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	resp, err := slackClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack send_message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("slack send_message: read: %w", err)
	}
	var r struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("slack send_message: decode: %w", err)
	}
	if !r.OK {
		return nil, fmt.Errorf("slack send_message error: %s", nonEmpty(r.Error, "unknown_error"))
	}
	return map[string]any{"ok": true, "channel": r.Channel, "ts": r.TS}, nil
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
