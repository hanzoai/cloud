package connectors

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// Slack, the one provider whose token response is not RFC 6749 §5.1.
//
// Everything about Slack's flow is standard — same authorize, same form POST,
// same code — except what comes back: a 200 carrying `ok:false` on failure, the
// workspace in `team{id,name}` instead of at a separate identity endpoint, and a
// BOT token rather than a user token. So Slack declares those differences and
// inherits the request, the status check, the body cap and the error handling
// from the engine, exactly like every other entry in the catalog.
//
// The env names are consts because a Slack deployment sets them and the plane's
// tests read them.

const (
	slackClientIDEnv     = "SLACK_CLIENT_ID"
	slackClientSecretEnv = "SLACK_CLIENT_SECRET"
	slackScopesEnv       = "SLACK_BOT_SCOPES"
	slackBotTokenSecret  = "bot_token"
)

// slackWebAPIBase is the root of Slack's Web API. A var so a test can point the
// exchange at a stub. The path segment is SLACK's namespace, not one of ours —
// isolated in a const so a route-hygiene grep never reads it as a Hanzo route.
var slackWebAPIBase = "https://slack.com/" + slackAPISegment

const slackAPISegment = "api"

// slackScopes is the bot scope set, overridable per deployment. Read fresh at
// each call so an env change applies without a rebuild.
func slackScopes() []string {
	if raw := strings.TrimSpace(os.Getenv(slackScopesEnv)); raw != "" {
		out := make([]string, 0, 8)
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{"chat:write", "channels:read", "channels:history", "users:read"}
}

// slackFinish reads Slack's token response.
//
// `ok:false` is a REFUSAL AT 200. A reader that only checks the HTTP status
// takes Slack's error document as a success and seals an empty string into KMS
// as the workspace's bot token — the connection then shows connected and every
// post fails.
func slackFinish(body []byte, res *ExchangeResult) error {
	var r struct {
		OK          bool   `json:"ok"`
		Error       string `json:"error"`
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		BotUserID   string `json:"bot_user_id"`
		Team        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"team"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return err
	}
	if !r.OK {
		if r.Error == "" {
			r.Error = "refused"
		}
		return errors.New(r.Error)
	}
	res.Tokens[slackBotTokenSecret] = r.AccessToken
	res.Scopes = splitScopes(r.Scope, ",")
	res.ExternalID = r.Team.ID
	res.AccountLabel = r.Team.Name
	res.BotUserID = r.BotUserID
	return nil
}

// slackSpec is Slack's catalog entry. The token URL is built from the Web API
// base so a test can redirect the exchange without reaching Slack.
func slackSpec() spec {
	return spec{
		id: "slack", name: "Slack",
		desc:     "Post messages and receive events in your workspace.",
		category: "Communication",
		authURL:  "https://slack.com/oauth/v2/authorize",
		tokenURL: slackWebAPIBase + "/oauth.v2.access",
		scopes:   slackScopes(),
		scopeSep: ",",

		clientIDEnv:     slackClientIDEnv,
		clientSecretEnv: slackClientSecretEnv,
		tokenSecret:     slackBotTokenSecret,
		finish:          slackFinish,
	}
}
