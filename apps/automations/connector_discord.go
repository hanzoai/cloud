package automations

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discord connector. Name="discord" == the clients/integrations provider id whose
// bot token is custodied under discordTokenSecret. Complete + correct; fails
// closed "discord not connected" until a bot token is sealed.
//
// verify_member answers "is userId a member of guildId?" — the basis for
// social:discord:join points. It emits {verified, platform, source, dedupKey}; a
// flow threads source+dedupKey into waitlist.award_points.

const discordTokenSecret = "bot_token"

var discordAPIBase = "https://discord.com/api/v10"

var discordClient = &http.Client{Timeout: 15 * time.Second}

func init() {
	register(&Connector{
		Name:        "discord",
		DisplayName: "Discord",
		AuthType:    "bot_token",
		AuthReq:     true,
		Actions: map[string]*Action{
			"verify_member": {
				Name:        "verify_member",
				DisplayName: "Verify Member",
				Description: "Verify a user is a member of a Discord guild; the basis for social:discord:join points.",
				Props: []PropSpec{
					{Name: "guildId", Type: "string", Required: true, Description: "Discord guild (server) id."},
					{Name: "userId", Type: "string", Required: true, Description: "Discord user id to check."},
				},
				Run: runDiscordVerifyMember,
			},
		},
	})
}

func runDiscordVerifyMember(ctx context.Context, rc RunContext) (any, error) {
	tokb, err := rc.Token(discordTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("discord not connected: %w", err)
	}
	token := strings.TrimSpace(string(tokb))
	guildId := strInput(rc.Input, "guildId")
	userId := strInput(rc.Input, "userId")
	if guildId == "" || userId == "" {
		return nil, fmt.Errorf("discord verify_member: guildId and userId are required")
	}

	// GET /guilds/{guild}/members/{user}: 200 => member, 404 => not a member.
	endpoint := fmt.Sprintf("%s/guilds/%s/members/%s",
		strings.TrimRight(discordAPIBase, "/"), url.PathEscape(guildId), url.PathEscape(userId))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("discord verify_member: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+token)
	resp, err := discordClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discord verify_member: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
		return discordVerdict(true), nil
	case http.StatusNotFound:
		return discordVerdict(false), nil
	default:
		return nil, fmt.Errorf("discord verify_member: http %d", resp.StatusCode)
	}
}

func discordVerdict(verified bool) map[string]any {
	return map[string]any{
		"verified": verified, "platform": "discord",
		"source": "social:discord:join", "dedupKey": "social:discord:join",
	}
}
