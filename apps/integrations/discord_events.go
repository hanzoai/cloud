package integrations

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// discord_events.go is ADAPTER #2 of the ChatBridge core: the Discord Interactions
// webhook. Its three edges —
//
//	inbound-auth : Ed25519 signature verify (X-Signature-Ed25519 over timestamp+body,
//	               against the app public key) — NOT HMAC
//	parse        : parseDiscordInteraction (pure)
//	reply        : discordFollowup (PATCH the deferred response via the interaction webhook)
//
// — delegating dedupe/org-resolve/pool/brain/link to the core. ISOLATION ROOT: the
// org comes ONLY from OrgForExternalID("discord", guild_id), and guild_id is
// trustworthy only because the payload is Ed25519-verified first.
//
// HTTP-native: Discord Interactions need no gateway/websocket and no message-content
// intent. @hanzo is the `/hanzo` slash command. We ack within Discord's 3s budget
// with a DEFERRED (ephemeral) response, then edit it with the answer async.
//
// MOUNT HANDOFF (integrations owner adds these before the /:provider wildcards):
//
//	app.Post("/v1/integrations/discord/interactions", s.discordInteractions)
//	app.Get("/v1/integrations/discord/link",          s.discordLink)
//	app.Get("/v1/integrations/discord/link/discord",  s.discordLinkDiscord)
//	app.Get("/v1/integrations/discord/link/callback", s.discordLinkCallback)

// Discord interaction + response type numbers.
const (
	discordTypePing        = 1 // incoming PING
	discordTypeCommand     = 2 // APPLICATION_COMMAND (a slash command)
	discordRespPong        = 1 // PONG
	discordRespMessage     = 4 // CHANNEL_MESSAGE_WITH_SOURCE
	discordRespDeferred    = 5 // DEFERRED_CHANNEL_MESSAGE_WITH_SOURCE
	discordFlagEphemeral   = 64
	discordMaxContent      = 2000 // Discord message content hard cap
	discordSigHeader       = "X-Signature-Ed25519"
	discordTimestampHeader = "X-Signature-Timestamp"
)

// discordInteractions is the Interactions endpoint (the app's Interactions Endpoint
// URL). It Ed25519-verifies the request, answers PING, and routes /hanzo to an
// on-behalf-of agent run acked with a deferred ephemeral response and edited async.
func discordInteractions(s *cloud.Service[state], c *zip.Ctx) error {
	channelReady()
	pub := discordPublicKey()
	if pub == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "discord interactions not configured")
	}
	raw := slackReadBody(c) // shared bounded raw-body read
	if !verifyDiscordSignature(pub, c.Header(discordSigHeader), c.Header(discordTimestampHeader), raw) {
		return zip.ErrUnauthorized("bad discord signature")
	}
	it, ok := parseDiscordInteraction(raw)
	if !ok {
		return zip.ErrBadRequest("malformed interaction")
	}
	if it.Type == discordTypePing {
		return c.JSON(http.StatusOK, map[string]any{"type": discordRespPong})
	}
	if it.Type != discordTypeCommand || it.CommandName != "hanzo" {
		// Nothing we handle — ack with an empty ephemeral message so Discord is happy.
		return discordEphemeral(c, "Unsupported interaction.")
	}
	if it.GuildID == "" {
		return discordEphemeral(c, "Use /hanzo in a server where Hanzo is installed.")
	}
	if it.User == "" {
		return discordEphemeral(c, "Could not identify the invoking user.")
	}
	// ISOLATION ROOT: org comes ONLY from OrgForExternalID for the Ed25519-verified
	// guild_id — never a payload field.
	org, ok := OrgForExternalID("discord", it.GuildID)
	if !ok {
		return discordEphemeral(c, "This server isn't connected to Hanzo yet.")
	}
	in := Inbound{
		Provider: "discord", ExternalID: it.GuildID, User: it.User,
		Channel: it.ChannelID, Text: it.Prompt, DedupeKey: it.ID,
	}
	// Discord does NOT auto-retry a non-2xx, so an event we did not take is a
	// user-visible ask-to-run-it-again rather than a retriable failure. Nothing
	// was recorded either way, so the next /hanzo runs cleanly.
	if !emitIngress(c.Context(), s, org, in, "") {
		return discordEphemeral(c, "Hanzo could not take that just now — please run /hanzo again in a moment.")
	}
	// Ack SYNC with a deferred EPHEMERAL response (flags 64) — the async edit stays
	// ephemeral, so a link URL is never shown to the whole channel. A duplicate
	// delivery lands here too: the original already answered, and this ack is all
	// Discord is waiting for.
	return discordDeferredEphemeral(c)
}

// discordEphemeral responds SYNC with an immediate ephemeral message (no async
// work) — used for the not-connected / unsupported / at-capacity cases.
func discordEphemeral(c *zip.Ctx, text string) error {
	return c.JSON(http.StatusOK, map[string]any{
		"type": discordRespMessage,
		"data": map[string]any{"content": text, "flags": discordFlagEphemeral},
	})
}

// discordDeferredEphemeral acks SYNC with a deferred EPHEMERAL response (flags 64);
// the async edit to @original stays ephemeral, so a link URL never reaches the
// whole channel.
func discordDeferredEphemeral(c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{
		"type": discordRespDeferred,
		"data": map[string]any{"flags": discordFlagEphemeral},
	})
}

// ── inbound auth (Ed25519 — Discord's trust boundary) ───────────────────────

// verifyDiscordSignature verifies the Ed25519 signature Discord puts on every
// interaction: sig over (timestamp || rawBody) against the app public key. Constant
// factors are stdlib crypto/ed25519 (Verify is constant-time on the signature).
// Any malformed input (bad hex, wrong length) returns false — never panics.
func verifyDiscordSignature(publicKeyHex, sigHex, timestamp string, rawBody []byte) bool {
	if publicKeyHex == "" || sigHex == "" || timestamp == "" {
		return false
	}
	pub, err := hex.DecodeString(publicKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := make([]byte, 0, len(timestamp)+len(rawBody))
	msg = append(msg, timestamp...)
	msg = append(msg, rawBody...)
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

// ── parse (pure — Discord's parse edge) ─────────────────────────────────────

// discordInteraction is the normalized subset of an interaction we act on.
type discordInteraction struct {
	Type        int
	ID          string
	AppID       string
	Token       string
	GuildID     string
	ChannelID   string
	User        string // invoking user id (member.user.id in a guild, user.id in a DM)
	CommandName string
	Prompt      string // the /hanzo "prompt" option value
}

// discordRawInteraction is the wire shape (fields we read).
type discordRawInteraction struct {
	Type          int    `json:"type"`
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	Token         string `json:"token"`
	GuildID       string `json:"guild_id"`
	ChannelID     string `json:"channel_id"`
	Member        *struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"member"`
	User *struct {
		ID string `json:"id"`
	} `json:"user"`
	Data *struct {
		Name    string `json:"name"`
		Options []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"options"`
	} `json:"data"`
}

// parseDiscordInteraction extracts a discordInteraction. ok=false only for a
// structurally invalid payload (empty type/id). The prompt is the string value of
// the "prompt" option (falling back to the first string option).
func parseDiscordInteraction(raw []byte) (discordInteraction, bool) {
	var r discordRawInteraction
	if err := json.Unmarshal(raw, &r); err != nil || r.Type == 0 || r.ID == "" {
		return discordInteraction{}, false
	}
	it := discordInteraction{
		Type: r.Type, ID: r.ID, AppID: r.ApplicationID, Token: r.Token,
		GuildID: r.GuildID, ChannelID: r.ChannelID,
	}
	if r.Member != nil && r.Member.User.ID != "" {
		it.User = r.Member.User.ID
	} else if r.User != nil {
		it.User = r.User.ID
	}
	if r.Data != nil {
		it.CommandName = r.Data.Name
		for _, o := range r.Data.Options {
			var v string
			if json.Unmarshal(o.Value, &v) == nil && v != "" {
				if o.Name == "prompt" {
					it.Prompt = v
					break
				}
				if it.Prompt == "" {
					it.Prompt = v
				}
			}
		}
	}
	return it, true
}
