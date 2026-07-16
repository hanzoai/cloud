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
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// slack_events.go is the SLACK ADAPTER of the ChatBridge core (bridge.go): the Slack
// Events webhook + slash command. Like every adapter (discord_events.go /
// telegram_events.go / teams_events.go) its three edges are —
//
//	inbound-auth : verifySlackSignature (HMAC-SHA256 over the raw body + replay window)
//	parse        : routeSlackEvent / parseSlashCommand (pure)
//	reply        : slackReplier (bot-token chat.post{Message,Ephemeral}) / response_url
//
// — and it delegates the shared middle to the core: the bounded per-org pool
// (bridgeLim), the durable dedupe (store.MarkEvent), org resolution
// (OrgForExternalID — the ISOLATION ROOT), the ONE agent brain (bridgeReply, run
// ON-BEHALF-OF the linked user), and the per-user link. The @hanzo CHAT turn is now
// ONE code path across all four platforms.
//
// ONE Slack-SPECIFIC branch stays, deliberately NOT folded into the chat bridge: a
// `code:` prefix routes to the durable CODING agent (slack_coding.go) — its own pool
// (codingLim) and detached run. The coding agent is a distinct flow from a chat turn.
//
// ISOLATION BAR: a workspace's events reach ONLY the org that connected that Slack
// team. The org comes ONLY from OrgForExternalID("slack", team_id) — never a payload
// field — and team_id is trustworthy only because the whole payload is HMAC-verified
// with SLACK_SIGNING_SECRET first. The reply uses THAT org's bot token (TokenFor);
// the run is THAT org's agent on behalf of THAT org's linked user.
//
// MOUNT HANDOFF (registered in integrations.go's routes(); this file deliberately
// does NOT edit Mount/routes — clean separation):
//
//	app.Post("/v1/integrations/slack/events",        cloud.Handle(s, slackEvents))
//	app.Post("/v1/integrations/slack/commands",      cloud.Handle(s, slackCommands))
//	app.Get("/v1/integrations/slack/link",           cloud.Handle(s, slackLink))
//	app.Get("/v1/integrations/slack/link/slack",     cloud.Handle(s, slackLinkSlack))
//	app.Get("/v1/integrations/slack/link/callback",  cloud.Handle(s, slackLinkCallback))

// slackMaxBody bounds the webhook body we read + sign over, AND every Slack Web API
// response we read. Slack payloads are small; a hostile/oversized body can neither
// exhaust memory nor slip past the HMAC (we sign exactly what we read).
const slackMaxBody = 1 << 20 // 1 MiB

var (
	slackBridgeOnce sync.Once
	slackUsedStates *seenSet // single-use link-state nonces (process-lifetime)
)

// slackBridgeReady lazily initializes the Slack adapter's OWN process state — the
// coding pool (codingLim; the coding agent is a DISTINCT flow) and the single-use
// link-state seen-set — plus the shared bridge state (bridgeReady: the bounded chat
// pool + link seen-set). The durable dedupe table is created in the store's migrate()
// at Mount. Cheap + idempotent; every Slack handler calls it first.
func slackBridgeReady(s *cloud.Service[state]) {
	bridgeReady()
	slackBridgeOnce.Do(func() {
		codingLim = newOrgLimiter(codingConcurrency(), codingOrgConcurrency())
		slackUsedStates = newSeenSet(time.Duration(slackLinkTTLSec) * time.Second)
	})
}

// ── Events webhook ──────────────────────────────────────────────────────────

// slackEvents is the Slack Events API webhook (the app's request_url:
// https://{domain}/v1/integrations/slack/events). It HMAC-verifies the raw body,
// answers the url_verification challenge, and routes @mentions / DMs — acking FAST
// (empty 200) and doing the billed work async on the bridge under the bounded pool,
// deduped durably on event_id. A `code:` prompt branches to the coding flow.
func slackEvents(s *cloud.Service[state], c *zip.Ctx) error {
	slackBridgeReady(s)
	secret := slackSigningSecret()
	if secret == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "slack events not configured")
	}
	raw := slackReadBody(c)
	if !verifySlackSignature(secret,
		c.Header("X-Slack-Signature"), c.Header("X-Slack-Request-Timestamp"), string(raw), 0) {
		return zip.ErrUnauthorized("bad slack signature")
	}
	d := routeSlackEvent(raw)
	switch d.Kind {
	case slackRouteChallenge:
		return c.String(http.StatusOK, d.Challenge)
	case slackRouteAgent:
		// ISOLATION ROOT, resolved SYNC (so the per-org limiter keys on the real
		// tenant and a shed happens BEFORE anything is recorded): org comes ONLY from
		// the install→org map for the Slack-verified team_id. An event for a team no
		// org connected is dropped (nothing to do — not a shed).
		org, ok := OrgForExternalID("slack", d.TeamID)
		if !ok {
			s.Log.Warn("slack: event for unconnected team", "team", d.TeamID)
			return c.NoContent(http.StatusOK)
		}
		// Echo-loop guard (belt-and-suspenders with the route-level bot_id drop): drop
		// the bot's OWN message using THIS org's recorded bot user id, before a pool
		// slot or a dedupe row is spent. A bot echo never triggers a run either way.
		if conn, ok := ConnectionFor(org, "slack"); ok && conn.BotUserID != "" && d.User == conn.BotUserID {
			return c.NoContent(http.StatusOK)
		}
		// SHED BEFORE the dedupe write (Red M-1). Try to acquire a pool slot first; if
		// the pool is full, record NOTHING and return a retriable NON-2xx — the turn
		// never ran, so no event_id is burned and Slack re-delivers when a slot frees
		// (no lost @mention, no double-run).
		if !bridgeLim.acquire(org) {
			s.Log.Warn("slack: at capacity, shedding for retry", "org", org)
			return zip.Errorf(http.StatusTooManyRequests, "slack agent pool at capacity")
		}
		// Slot held. DURABLE dedupe (BILLED path): a Slack retry of an event that
		// already ran must never double-run. Release the slot on every path that does
		// NOT dispatch. Fail CLOSED on a dedupe error (skip) rather than risk a double
		// charge.
		key := slackEventKey(raw)
		fresh, err := s.State.store.MarkEvent(c.Context(), "slack", key)
		if err != nil {
			bridgeLim.release(org)
			s.Log.Warn("slack: event dedupe error, skipping", "err", err)
			return c.NoContent(http.StatusOK)
		}
		if !fresh {
			bridgeLim.release(org)
			return c.NoContent(http.StatusOK)
		}
		// Opportunistic GC so the dedupe table cannot grow without bound.
		if _, gerr := s.State.store.GCEvents(c.Context(), staleEventCutoff()); gerr != nil {
			s.Log.Warn("slack: dedupe gc", "err", gerr)
		}
		route := d
		// CODING is a DISTINCT flow (its own pool + detached run); everything else is a
		// chat turn on the shared bridge. Either way the pool slot is held only for the
		// short synchronous hand-off (the coding run detaches under codingLim).
		if codingText, isCoding := codingIntent(route.Text); isCoding {
			bridgeSpawn(s, org, func() { slackCodingEvent(s, org, route, codingText) })
			return c.NoContent(http.StatusOK)
		}
		in := Inbound{
			Provider: "slack", ExternalID: route.TeamID, User: route.User,
			Channel: route.Channel, ThreadID: route.ThreadTS, Text: route.Text, DedupeKey: key,
		}
		reply := slackReplier(s, org, route.Channel, route.ThreadTS, route.User)
		bridgeSpawn(s, org, func() { runBridgeTurn(s, org, in, reply) })
		return c.NoContent(http.StatusOK)
	default: // slackRouteAck / slackRouteIgnore — valid but nothing to act on
		return c.NoContent(http.StatusOK)
	}
}

// ── Slash command ───────────────────────────────────────────────────────────

// slackCommands handles a Slack slash command (application/x-www-form-urlencoded)
// at https://{domain}/v1/integrations/slack/commands. Same HMAC gate; deduped on
// trigger_id; acks within Slack's 3s budget (empty 200) and posts the answer
// asynchronously via the command's response_url on the bridge.
func slackCommands(s *cloud.Service[state], c *zip.Ctx) error {
	slackBridgeReady(s)
	secret := slackSigningSecret()
	if secret == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "slack events not configured")
	}
	raw := slackReadBody(c)
	if !verifySlackSignature(secret,
		c.Header("X-Slack-Signature"), c.Header("X-Slack-Request-Timestamp"), string(raw), 0) {
		return zip.ErrUnauthorized("bad slack signature")
	}
	team, channel, user, text, responseURL, triggerID, ok := parseSlashCommand(raw)
	if !ok {
		return zip.ErrBadRequest("missing team_id or user_id")
	}
	// Resolve the org SYNC (may be "" — a workspace whose Hanzo connection was
	// removed; handled in slackSlashTurn), then SHED before the dedupe write (Red
	// M-1), same order as the events path.
	org, _ := OrgForExternalID("slack", team)
	if !bridgeLim.acquire(org) {
		s.Log.Warn("slack: at capacity, shedding slash", "org", org)
		return zip.Errorf(http.StatusTooManyRequests, "slack agent pool at capacity")
	}
	fresh, err := s.State.store.MarkEvent(c.Context(), "slack", triggerID)
	if err != nil {
		bridgeLim.release(org)
		s.Log.Warn("slack: slash dedupe error, skipping", "err", err)
		return c.NoContent(http.StatusOK)
	}
	if !fresh {
		bridgeLim.release(org)
		return c.NoContent(http.StatusOK)
	}
	in := Inbound{Provider: "slack", ExternalID: team, User: user, Channel: channel, Text: text}
	bridgeSpawn(s, org, func() { slackSlashTurn(s, org, in, responseURL) })
	return c.NoContent(http.StatusOK)
}

// parseSlashCommand extracts the fields a slash command carries. Pure. ok is false
// when an identifying field (team_id, user_id) is absent.
func parseSlashCommand(raw []byte) (team, channel, user, text, responseURL, triggerID string, ok bool) {
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return "", "", "", "", "", "", false
	}
	team = form.Get("team_id")
	channel = form.Get("channel_id")
	user = form.Get("user_id")
	text = strings.TrimSpace(form.Get("text"))
	responseURL = form.Get("response_url")
	triggerID = form.Get("trigger_id")
	ok = team != "" && user != ""
	return
}

// ── Slack dispatch: chat via the bridge, coding via its own flow ────────────

// slackReplier is the Slack Events reply seam handed to runBridgeTurn: it fetches
// THIS org's bot token (the isolation-scoped reply sink) and posts the agent's
// answer in-thread, or — for the (sensitive) account-link prompt — EPHEMERALLY so a
// link URL never reaches a whole channel. Bound to the resolved org (never a payload
// field), so the reply can only ever use the connecting org's token.
func slackReplier(s *cloud.Service[state], org, channel, threadTS, user string) replyFunc {
	return func(ctx context.Context, text string, ephemeral bool) error {
		tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
		if err != nil {
			return err
		}
		if ephemeral {
			return slackPostEphemeral(ctx, string(tok), channel, user, text)
		}
		return slackPostThread(ctx, string(tok), channel, threadTS, text)
	}
}

// slackCodingEvent runs the @mention/DM CODING path for a PRE-RESOLVED org: it
// fetches THIS org's bot token (the reply sink) and hands off to slack_coding.go,
// which owns the parse, the link check, its own bounded pool (codingLim), and the
// detached run. Coding is deliberately NOT folded into the chat bridge.
func slackCodingEvent(s *cloud.Service[state], org string, d slackRoute, codingText string) {
	ctx, cancel := context.WithTimeout(context.Background(), bridgeAgentTimeout)
	defer cancel()
	// Echo-loop guard already applied in the sync path; the bot token is the reply
	// sink both the ack and the result card post through.
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		s.Log.Warn("slack: bot token fetch", "team", d.TeamID, "err", err)
		return
	}
	handleSlackCoding(s, ctx, org, string(tok), d.TeamID, d.Channel, d.ThreadTS, d.User, codingText)
}

// slackSlashTurn is the async slash body dispatched on the bridge. An empty org means
// the workspace's Hanzo connection was removed. A `code:` prompt branches to the
// coding flow (slack_coding.go). Otherwise it runs the ONE agent brain (bridgeReply)
// and delivers via the (host-pinned) response_url: an answer goes in_channel; the
// account-link prompt goes ephemeral (only the invoker sees it).
func slackSlashTurn(s *cloud.Service[state], org string, in Inbound, responseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), bridgeAgentTimeout)
	defer cancel()
	if org == "" {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "This Slack workspace isn't connected to Hanzo yet.")
		return
	}
	if codingText, isCoding := codingIntent(in.Text); isCoding {
		handleSlackSlashCoding(s, ctx, org, in.ExternalID, in.Channel, in.User, codingText, responseURL)
		return
	}
	text, ephemeral := bridgeReply(s, ctx, org, in.Provider, in.ExternalID, in.User, in.Text)
	if text == "" {
		return
	}
	responseType := "in_channel"
	if ephemeral {
		responseType = "ephemeral"
	}
	if err := slackPostResponseURL(ctx, responseURL, responseType, text); err != nil {
		s.Log.Warn("slack: slash reply", "team", in.ExternalID, "err", err)
	}
}

// ── event routing (pure) ────────────────────────────────────────────────────

type slackRouteKind int

const (
	slackRouteIgnore    slackRouteKind = iota // malformed / unsupported
	slackRouteChallenge                       // url_verification handshake
	slackRouteAck                             // valid but nothing to act on (echo/subtype/non-message)
	slackRouteAgent                           // @mention / DM: run an agent on-behalf-of the user
)

type slackRoute struct {
	Kind      slackRouteKind
	Challenge string
	TeamID    string
	Channel   string
	User      string
	Text      string
	ThreadTS  string
}

type slackEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	TeamID    string          `json:"team_id"`
	EventID   string          `json:"event_id"`
	Event     json.RawMessage `json:"event"`
}

type slackMessageEvent struct {
	Type        string `json:"type"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	User        string `json:"user"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
	Subtype     string `json:"subtype"`
	BotID       string `json:"bot_id"`
	ThreadTS    string `json:"thread_ts"`
}

// routeSlackEvent decides what to do with a signature-verified Slack payload. Pure
// — no I/O. app_mention (@hanzo in a channel) and message with channel_type=="im"
// (a DM) are AGENT triggers; the leading <@BOTID> mention token is stripped so the
// agent receives the user's actual prompt. The bot's own messages (bot_id/subtype)
// are always dropped so a reply never loops back.
func routeSlackEvent(raw []byte) slackRoute {
	var env slackEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Type == "" {
		return slackRoute{Kind: slackRouteIgnore}
	}
	if env.Type == "url_verification" {
		if env.Challenge != "" {
			return slackRoute{Kind: slackRouteChallenge, Challenge: env.Challenge}
		}
		return slackRoute{Kind: slackRouteIgnore}
	}
	if env.Type != "event_callback" || len(env.Event) == 0 {
		return slackRoute{Kind: slackRouteAck}
	}
	var ev slackMessageEvent
	if err := json.Unmarshal(env.Event, &ev); err != nil {
		return slackRoute{Kind: slackRouteAck}
	}
	switch ev.Type {
	case "app_mention":
		if ev.BotID != "" || ev.User == "" || ev.Text == "" {
			return slackRoute{Kind: slackRouteAck}
		}
		return slackRoute{
			Kind: slackRouteAgent, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User,
			Text: stripLeadingMention(ev.Text), ThreadTS: threadOr(ev.ThreadTS, ev.TS),
		}
	case "message":
		// Drop the bot's own messages + non-plain subtypes (edit/delete/join), and
		// act ONLY on DMs (channel_type=="im"); a plain channel message with no
		// mention is not an agent trigger.
		if ev.BotID != "" || ev.Subtype != "" || ev.User == "" || ev.Text == "" || ev.ChannelType != "im" {
			return slackRoute{Kind: slackRouteAck}
		}
		return slackRoute{
			Kind: slackRouteAgent, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User,
			Text: stripLeadingMention(ev.Text), ThreadTS: threadOr(ev.ThreadTS, ev.TS),
		}
	default:
		return slackRoute{Kind: slackRouteAck}
	}
}

// stripLeadingMention removes a single leading Slack mention token ("<@U…>" or
// "<@U…|label>") plus following whitespace, so the agent receives the actual
// prompt ("@hanzo what's up" -> "what's up"). Unmentioned text is returned trimmed.
func stripLeadingMention(text string) string {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "<@") {
		if end := strings.IndexByte(t, '>'); end >= 0 {
			return strings.TrimSpace(t[end+1:])
		}
	}
	return t
}

// threadOr returns threadTS when set, else ts — so a reply always threads under
// the triggering message (a top-level trigger has no thread_ts; its own ts is the
// thread root).
func threadOr(threadTS, ts string) string {
	if threadTS != "" {
		return threadTS
	}
	return ts
}

// slackEventKey extracts the dedupe key (Slack event_id) from a payload; empty if
// absent, which callers treat as non-dedupable (never blocks).
func slackEventKey(raw []byte) string {
	var env slackEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ""
	}
	if env.Type == "event_callback" {
		return env.EventID
	}
	return ""
}

// ── Slack Web API posting (bot-token chat.* + slash response_url) ────────────

// slackChatPost is the shared chat.* poster: JSON body, bot bearer, ok-envelope
// check. The bot token is never logged on error. It targets slackWebAPIBase (a
// package var the OAuth provider already exposes, repointable by tests). fields is
// map[string]any so a caller can pass a Block Kit `blocks` array alongside the
// string channel/text — the ONE chat.* HTTP path for every Slack post in cloud.
func slackChatPost(ctx context.Context, botToken, method string, fields map[string]any) error {
	payload, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackWebAPIBase+method, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := slackHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, slackMaxBody))
	var data struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &data)
	if !data.OK {
		return fmt.Errorf("slack %s failed: %s", strings.TrimPrefix(method, "/"), nonEmpty(data.Error, "unknown"))
	}
	return nil
}

// slackPostThread posts to a channel, threaded under threadTS when non-empty.
func slackPostThread(ctx context.Context, botToken, channel, threadTS, text string) error {
	fields := map[string]any{"channel": channel, "text": text}
	if threadTS != "" {
		fields["thread_ts"] = threadTS
	}
	return slackChatPost(ctx, botToken, "/chat.postMessage", fields)
}

// slackPostEphemeral posts a message visible ONLY to `user` in `channel` — used
// for the account-link prompt so a link URL is NEVER shown to a whole channel.
func slackPostEphemeral(ctx context.Context, botToken, channel, user, text string) error {
	return slackChatPost(ctx, botToken, "/chat.postEphemeral", map[string]any{
		"channel": channel, "user": user, "text": text,
	})
}

// ── exported Slack posting (the ONE path other subsystems reuse) ─────────────

// PostSlackBlocks posts a Block Kit message (with a text fallback shown in
// notifications) to channel using botToken, via the shared chat.postMessage path.
// blocks is Block Kit JSON (a []any of section/context/… maps); nil posts text
// only. Exported so a subsystem holding a resolved bot token (an automations
// connector) posts through the SAME code path as the OAuth bridge — no third
// chat.postMessage implementation.
func PostSlackBlocks(ctx context.Context, botToken, channel, text string, blocks []any) error {
	fields := map[string]any{"channel": channel, "text": text}
	if len(blocks) > 0 {
		fields["blocks"] = blocks
	}
	return slackChatPost(ctx, botToken, "/chat.postMessage", fields)
}

// PostSlackBlocksThread posts a Block Kit message threaded under threadTS (when
// non-empty) via the shared chat.postMessage path — the same door PostSlackBlocks
// uses, plus in-thread delivery so a coding result lands under the triggering
// @hanzo message.
func PostSlackBlocksThread(ctx context.Context, botToken, channel, threadTS, text string, blocks []any) error {
	fields := map[string]any{"channel": channel, "text": text}
	if len(blocks) > 0 {
		fields["blocks"] = blocks
	}
	if threadTS != "" {
		fields["thread_ts"] = threadTS
	}
	return slackChatPost(ctx, botToken, "/chat.postMessage", fields)
}

// NotifySlack posts a Block Kit message to channel on behalf of org's connected
// Slack workspace. It resolves the org's KMS-sealed bot token (TokenFor —
// fail-closed: unmounted / org not connected / KMS-down all error, never post) and
// delivers through PostSlackBlocks. The caller never handles the raw token. This is
// the door the git-lifecycle notifier (clients/git) uses so token custody stays
// entirely inside the integrations plane.
func NotifySlack(ctx context.Context, org, channel, text string, blocks []any) error {
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return err
	}
	return PostSlackBlocks(ctx, strings.TrimSpace(string(tok)), channel, text, blocks)
}

// slackResponseHost is the ONLY host a slash-command response_url may target. A
// var (not const) so a test can repoint delivery at an httptest stub; production
// never mutates it. Pinning stops a forged command (should the signature ever be
// bypassed) from turning the bridge into an SSRF/exfil client to an attacker host.
var slackResponseHost = "hooks.slack.com"

// slackPostResponseURL delivers a slash reply to Slack's response_url (a
// short-lived capability URL — no bot token needed). The host is pinned.
func slackPostResponseURL(ctx context.Context, responseURL, responseType, text string) error {
	u, err := url.Parse(responseURL)
	if err != nil {
		return fmt.Errorf("slack: bad response_url")
	}
	if u.Scheme != "https" || u.Hostname() != slackResponseHost {
		return fmt.Errorf("slack: response_url host not allowed")
	}
	payload, _ := json.Marshal(map[string]string{"response_type": responseType, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := slackHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: response_url status %d", resp.StatusCode)
	}
	return nil
}

// ── config (env, read at call time — operator-injected from KMS) ────────────

func slackSigningSecret() string { return strings.TrimSpace(os.Getenv("SLACK_SIGNING_SECRET")) }

// slackAgentRef resolves the agent the Slack CODING flow runs (slack_coding.go). The
// CHAT path resolves its agent through the bridge (bridgeAgentRef("slack")); this
// stays for the coding path, unchanged: SLACK_AGENT_REF, default "hanzo".
func slackAgentRef() string {
	if v := strings.TrimSpace(os.Getenv("SLACK_AGENT_REF")); v != "" {
		return v
	}
	return "hanzo"
}

// slackReadBody returns the exact raw request body the HMAC must be computed over,
// bounded to slackMaxBody. fiber's transport already bounds the body; this is the
// local belt-and-suspenders at the crypto boundary. Shared: every adapter's webhook
// reads its raw body through here.
func slackReadBody(c *zip.Ctx) []byte {
	b := c.Body()
	if len(b) > slackMaxBody {
		return b[:slackMaxBody]
	}
	return b
}
