package connectors

import (
	"context"
	"strconv"
)

// The catalog: every connector this deployment knows how to speak, as data.
//
// One entry per provider, each a `spec` the engine in oauth2.go turns into a
// working authorization-code flow. There is no per-provider route, no
// per-provider handler and no per-provider copy of the token exchange — adding
// a connector is adding a struct literal to this file.
//
// A provider appears in the catalog whether or not this deployment has its
// credentials. `available:false` on a card is the honest answer to "we know
// this connector, nobody has configured it here" — hiding it would leave an
// operator with no way to discover what env vars to set, and connect fails
// closed with a 503 rather than a dead end.
//
// SCOPES ARE THE LEAST THAT WORKS. Each set below is what the stated purpose
// needs — post, read the account's own profile — and nothing more. Several of
// these providers gate write scopes behind app review; that is their business
// process, not a code path, and until it is granted the connect will fail at
// the provider's own consent screen with the provider's own message.

func init() {
	for _, sp := range catalog {
		register(oauthProvider(sp))
	}
}

// pkceChallenge is the PKCE code_challenge/verifier for providers that require
// PKCE on a confidential client (X is the notable one). It is `plain`, so the
// challenge and verifier are the same string and no per-request state has to be
// carried between the authorize and the callback — which matters here because
// the callback is state-authed and stateless by design.
//
// `plain` is weaker than S256 and the reason it is acceptable is specific: PKCE
// defends a PUBLIC client whose secret is on the user's device. This is a
// confidential server-side client that also sends its client_secret, so the
// exchange is already bound to something an attacker does not have.
const pkceChallenge = "hanzo-connectors-pkce-v1-plain-challenge"

var catalog = []spec{
	// ── Social ────────────────────────────────────────────────────────────────
	{
		id: "x", name: "X", desc: "Post to X and read your account's timeline.",
		category:  "Social",
		authURL:   "https://twitter.com/i/oauth2/authorize",
		tokenURL:  "https://api.x.com/2/oauth2/token",
		revokeURL: "https://api.x.com/2/oauth2/revoke",
		scopes:    []string{"tweet.read", "tweet.write", "users.read", "offline.access"},
		// X requires PKCE even for a confidential client, and rejects the
		// exchange without a matching verifier.
		authExtra:       map[string]string{"code_challenge": pkceChallenge, "code_challenge_method": "plain"},
		basicAuth:       true,
		clientIDEnv:     "X_CLIENT_ID",
		clientSecretEnv: "X_CLIENT_SECRET",
		identify:        identifyX,
	},
	{
		id: "linkedin", name: "LinkedIn", desc: "Share posts to your LinkedIn profile or page.",
		category:        "Social",
		authURL:         "https://www.linkedin.com/oauth/v2/authorization",
		tokenURL:        "https://www.linkedin.com/oauth/v2/accessToken",
		scopes:          []string{"openid", "profile", "w_member_social"},
		clientIDEnv:     "LINKEDIN_CLIENT_ID",
		clientSecretEnv: "LINKEDIN_CLIENT_SECRET",
		identify:        identifyOIDC("https://api.linkedin.com/v2/userinfo"),
	},
	{
		id: "facebook", name: "Facebook", desc: "Publish to the Facebook Pages you manage.",
		category:        "Social",
		authURL:         "https://www.facebook.com/v21.0/dialog/oauth",
		tokenURL:        "https://graph.facebook.com/v21.0/oauth/access_token",
		scopes:          []string{"pages_manage_posts", "pages_read_engagement", "pages_show_list"},
		scopeSep:        ",",
		clientIDEnv:     "FACEBOOK_CLIENT_ID",
		clientSecretEnv: "FACEBOOK_CLIENT_SECRET",
		identify:        identifyGraph,
	},
	{
		id: "instagram", name: "Instagram", desc: "Publish photos and reels to a professional account.",
		category: "Social",
		// Instagram's professional-account API is Facebook Login, not the retired
		// Basic Display: same dialog, same graph, different scopes.
		authURL:         "https://www.facebook.com/v21.0/dialog/oauth",
		tokenURL:        "https://graph.facebook.com/v21.0/oauth/access_token",
		scopes:          []string{"instagram_basic", "instagram_content_publish", "pages_show_list"},
		scopeSep:        ",",
		clientIDEnv:     "INSTAGRAM_CLIENT_ID",
		clientSecretEnv: "INSTAGRAM_CLIENT_SECRET",
		identify:        identifyGraph,
	},
	{
		id: "threads", name: "Threads", desc: "Publish posts to Threads.",
		category:        "Social",
		authURL:         "https://threads.net/oauth/authorize",
		tokenURL:        "https://graph.threads.net/oauth/access_token",
		scopes:          []string{"threads_basic", "threads_content_publish"},
		scopeSep:        ",",
		clientIDEnv:     "THREADS_CLIENT_ID",
		clientSecretEnv: "THREADS_CLIENT_SECRET",
	},
	{
		id: "pinterest", name: "Pinterest", desc: "Create pins on the boards you own.",
		category:        "Social",
		authURL:         "https://www.pinterest.com/oauth/",
		tokenURL:        "https://api.pinterest.com/v5/oauth/token",
		scopes:          []string{"boards:read", "pins:read", "pins:write"},
		scopeSep:        ",",
		basicAuth:       true,
		clientIDEnv:     "PINTEREST_CLIENT_ID",
		clientSecretEnv: "PINTEREST_CLIENT_SECRET",
		identify:        identifyPinterest,
	},
	{
		id: "reddit", name: "Reddit", desc: "Submit posts and read your Reddit account.",
		category: "Social",
		authURL:  "https://www.reddit.com/api/v1/authorize",
		tokenURL: "https://www.reddit.com/api/v1/access_token",
		scopes:   []string{"identity", "submit", "read"},
		// Reddit issues a refresh token only when asked at the consent screen.
		authExtra:       map[string]string{"duration": "permanent"},
		basicAuth:       true,
		clientIDEnv:     "REDDIT_CLIENT_ID",
		clientSecretEnv: "REDDIT_CLIENT_SECRET",
		identify:        identifyReddit,
	},
	{
		id: "tiktok", name: "TikTok", desc: "Publish videos to your TikTok account.",
		category:        "Social",
		authURL:         "https://www.tiktok.com/v2/auth/authorize/",
		tokenURL:        "https://open.tiktokapis.com/v2/oauth/token/",
		revokeURL:       "https://open.tiktokapis.com/v2/oauth/revoke/",
		scopes:          []string{"user.info.basic", "video.publish"},
		scopeSep:        ",",
		clientIDEnv:     "TIKTOK_CLIENT_ID",
		clientSecretEnv: "TIKTOK_CLIENT_SECRET",
	},

	// ── Video ─────────────────────────────────────────────────────────────────
	{
		id: "youtube", name: "YouTube", desc: "Upload videos and manage your channel.",
		category:  "Video",
		authURL:   "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:  "https://oauth2.googleapis.com/token",
		revokeURL: "https://oauth2.googleapis.com/revoke",
		scopes:    []string{"https://www.googleapis.com/auth/youtube.upload", "https://www.googleapis.com/auth/youtube.readonly"},
		// Google returns a refresh token only on the first consent unless it is
		// asked to prompt every time.
		authExtra:       map[string]string{"access_type": "offline", "prompt": "consent"},
		clientIDEnv:     "YOUTUBE_CLIENT_ID",
		clientSecretEnv: "YOUTUBE_CLIENT_SECRET",
		identify:        identifyOIDC("https://openidconnect.googleapis.com/v1/userinfo"),
	},
	{
		id: "twitch", name: "Twitch", desc: "Read your channel and post announcements.",
		category:        "Video",
		authURL:         "https://id.twitch.tv/oauth2/authorize",
		tokenURL:        "https://id.twitch.tv/oauth2/token",
		revokeURL:       "https://id.twitch.tv/oauth2/revoke",
		scopes:          []string{"user:read:email", "channel:manage:broadcast"},
		clientIDEnv:     "TWITCH_CLIENT_ID",
		clientSecretEnv: "TWITCH_CLIENT_SECRET",
		identify:        identifyTwitch,
	},

	// ── Communication ─────────────────────────────────────────────────────────
	// Slack's token response is not RFC 6749 §5.1, so it declares a reader for
	// that ONE leg and inherits the rest — see slack.go.
	slackSpec(),
	{
		id: "discord", name: "Discord", desc: "Post to channels in the servers you manage.",
		category:        "Communication",
		authURL:         "https://discord.com/oauth2/authorize",
		tokenURL:        "https://discord.com/api/oauth2/token",
		revokeURL:       "https://discord.com/api/oauth2/token/revoke",
		scopes:          []string{"identify", "guilds", "webhook.incoming"},
		basicAuth:       true,
		clientIDEnv:     "DISCORD_CLIENT_ID",
		clientSecretEnv: "DISCORD_CLIENT_SECRET",
		identify:        identifyDiscord,
	},

	// ── Developer ─────────────────────────────────────────────────────────────
	{
		id: "github", name: "GitHub", desc: "Read repositories and open pull requests.",
		category:        "Developer",
		authURL:         "https://github.com/login/oauth/authorize",
		tokenURL:        "https://github.com/login/oauth/access_token",
		scopes:          []string{"repo", "read:org"},
		clientIDEnv:     "GITHUB_CLIENT_ID",
		clientSecretEnv: "GITHUB_CLIENT_SECRET",
		identify:        identifyGitHub,
	},

	// ── Productivity ──────────────────────────────────────────────────────────
	{
		id: "google", name: "Google", desc: "Read and send Gmail, and use Drive and Calendar.",
		category:  "Productivity",
		authURL:   "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:  "https://oauth2.googleapis.com/token",
		revokeURL: "https://oauth2.googleapis.com/revoke",
		scopes: []string{
			"openid", "email", "profile",
			"https://www.googleapis.com/auth/gmail.send",
			"https://www.googleapis.com/auth/calendar.events",
			"https://www.googleapis.com/auth/drive.file",
		},
		authExtra:       map[string]string{"access_type": "offline", "prompt": "consent"},
		clientIDEnv:     "GOOGLE_CLIENT_ID",
		clientSecretEnv: "GOOGLE_CLIENT_SECRET",
		identify:        identifyOIDC("https://openidconnect.googleapis.com/v1/userinfo"),
	},
	{
		id: "microsoft", name: "Microsoft", desc: "Send Outlook mail and read your calendar.",
		category:        "Productivity",
		authURL:         "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		tokenURL:        "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		scopes:          []string{"openid", "profile", "offline_access", "Mail.Send", "Calendars.ReadWrite"},
		clientIDEnv:     "MICROSOFT_CLIENT_ID",
		clientSecretEnv: "MICROSOFT_CLIENT_SECRET",
		identify:        identifyMicrosoft,
	},
	{
		id: "notion", name: "Notion", desc: "Read and write pages in your workspace.",
		category:        "Productivity",
		authURL:         "https://api.notion.com/v1/oauth/authorize",
		tokenURL:        "https://api.notion.com/v1/oauth/token",
		authExtra:       map[string]string{"owner": "user"},
		basicAuth:       true,
		clientIDEnv:     "NOTION_CLIENT_ID",
		clientSecretEnv: "NOTION_CLIENT_SECRET",
	},
}

// ── Identity calls ──────────────────────────────────────────────────────────
//
// Each names the connection in the UI. They are best-effort by design (see the
// exchange in oauth2.go): a token that works must not be thrown away because a
// display name could not be read.

// identifyOIDC reads a standard OpenID Connect userinfo document. Google,
// LinkedIn and anything else that implements the spec share this one.
func identifyOIDC(endpoint string) func(context.Context, string) (account, error) {
	return func(ctx context.Context, token string) (account, error) {
		var u struct {
			Sub   string `json:"sub"`
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := getJSON(ctx, endpoint, token, &u); err != nil {
			return account{}, err
		}
		return account{ExternalID: u.Sub, Label: firstNonEmpty(u.Email, u.Name)}, nil
	}
}

func identifyX(ctx context.Context, token string) (account, error) {
	var r struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := getJSON(ctx, "https://api.x.com/2/users/me", token, &r); err != nil {
		return account{}, err
	}
	return account{ExternalID: r.Data.ID, Label: "@" + r.Data.Username}, nil
}

func identifyGitHub(ctx context.Context, token string) (account, error) {
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := getJSON(ctx, "https://api.github.com/user", token, &u); err != nil {
		return account{}, err
	}
	return account{ExternalID: strconv.FormatInt(u.ID, 10), Label: u.Login}, nil
}

func identifyDiscord(ctx context.Context, token string) (account, error) {
	var u struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := getJSON(ctx, "https://discord.com/api/users/@me", token, &u); err != nil {
		return account{}, err
	}
	return account{ExternalID: u.ID, Label: u.Username}, nil
}

func identifyGraph(ctx context.Context, token string) (account, error) {
	var u struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := getJSON(ctx, "https://graph.facebook.com/v21.0/me", token, &u); err != nil {
		return account{}, err
	}
	return account{ExternalID: u.ID, Label: u.Name}, nil
}

func identifyTwitch(ctx context.Context, token string) (account, error) {
	var r struct {
		Data []struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"data"`
	}
	if err := getJSON(ctx, "https://api.twitch.tv/helix/users", token, &r); err != nil {
		return account{}, err
	}
	if len(r.Data) == 0 {
		return account{}, errNoIdentity
	}
	return account{ExternalID: r.Data[0].ID, Label: r.Data[0].Login}, nil
}

func identifyReddit(ctx context.Context, token string) (account, error) {
	var u struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := getJSON(ctx, "https://oauth.reddit.com/api/v1/me", token, &u); err != nil {
		return account{}, err
	}
	return account{ExternalID: u.ID, Label: "u/" + u.Name}, nil
}

func identifyPinterest(ctx context.Context, token string) (account, error) {
	var u struct {
		Username string `json:"username"`
	}
	if err := getJSON(ctx, "https://api.pinterest.com/v5/user_account", token, &u); err != nil {
		return account{}, err
	}
	return account{ExternalID: u.Username, Label: u.Username}, nil
}

func identifyMicrosoft(ctx context.Context, token string) (account, error) {
	var u struct {
		ID   string `json:"id"`
		Mail string `json:"mail"`
		UPN  string `json:"userPrincipalName"`
	}
	if err := getJSON(ctx, "https://graph.microsoft.com/v1.0/me", token, &u); err != nil {
		return account{}, err
	}
	return account{ExternalID: u.ID, Label: firstNonEmpty(u.Mail, u.UPN)}, nil
}

func firstNonEmpty(xs ...string) string {
	for _, s := range xs {
		if s != "" {
			return s
		}
	}
	return ""
}
