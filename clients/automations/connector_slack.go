package automations

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/integrations"
)

// Slack connector. Name == the clients/integrations provider id "slack", so
// RunContext.Token resolves the SAME per-org bot token the OAuth plane sealed into
// KMS. Fails closed if the org has not connected Slack (Token returns an error).
// The actual chat.postMessage call is the SHARED integrations poster
// (integrations.PostSlackBlocks) — ONE Slack-post path across the whole binary.

// slackBotTokenSecret is the KMS secret name Slack's bot token is custodied under.
// It MUST equal the unexported const of the same name in
// clients/integrations/slack.go ("bot_token"); duplicated here because Go cannot
// import an unexported identifier. The integrations OAuth exchange seals the
// xoxb-… token under exactly this name.
const slackBotTokenSecret = "bot_token"

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
	// The ONE chat.postMessage path (integrations poster): the connector keeps its
	// per-org token custody (rc.Token), the HTTP call is shared.
	if err := integrations.PostSlackBlocks(ctx, strings.TrimSpace(string(tok)), channel, text, nil); err != nil {
		return nil, fmt.Errorf("slack send_message: %w", err)
	}
	return map[string]any{"ok": true, "channel": channel}, nil
}
