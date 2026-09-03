package integrations

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// slack_events.go is the SLACK ADAPTER of the ChatBridge core (channel.go): the Slack
// Events webhook + slash command. Like every adapter (discord_events.go /
// telegram_events.go / teams_events.go) its three edges are —
//
//	inbound-auth : verifySlackSignature (HMAC-SHA256 over the raw body + replay window)
//	parse        : routeSlackEvent / parseSlashCommand (pure)
//	reply        : slackReplier (bot-token chat.post{Message,Ephemeral}) / response_url
//
// — and it delegates the shared middle to the core: the bounded per-org pool
// (channelLim), the durable dedupe (store.MarkEvent), org resolution
// (OrgForExternalID — the ISOLATION ROOT), the ONE agent brain (channelReply, run
// ON-BEHALF-OF the linked user), and the per-user link. The @hanzo CHAT turn is now
// ONE code path across all four platforms.
//
// THERE IS NO SLACK-SPECIFIC BRANCH LEFT. A `code:` prefix used to route past the
// brain into the coding engine, and it was deleted rather than kept beside the
// tool path: a coding run is a TOOL the brain calls (create_agent_coding, the
// fleet's own MCP server), so a prefix a person had to type made the model's choice
// irrelevant here and left every surface that did not know the word —
// hanzo.app, hanzo.chat, MCP — unable to run code at all. Keeping it as a
// shortcut would have kept the model path unexercised, which is the same thing
// as not having one.
//
// ISOLATION BAR: a workspace's events reach ONLY the org that connected that Slack
// team. The org comes ONLY from OrgForExternalID("slack", team_id) — never a payload
// field — and team_id is trustworthy only because the whole payload is HMAC-verified
// against the signing secret first. The reply uses THAT org's bot token (TokenFor);
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
// single-use link-state seen-set — plus the shared channel state (channelReady: the
// bounded chat pool + link seen-set). The durable dedupe table is created in the
// store's migrate() at Mount. Cheap + idempotent; every Slack handler calls it first.
//
// It no longer sizes a coding pool. Admission for a coding run is the ENGINE's,
// stated once where the run actually consumes a sandbox; a second pool here would
// have been a second policy that the app endpoint did not share and that could
// disagree with the real one about what "full" means.
func slackBridgeReady(s *cloud.Service[state]) {
	channelReady()
	slackBridgeOnce.Do(func() {
		slackUsedStates = newSeenSet(time.Duration(linkStateTTLSec) * time.Second)
	})
}

// ── Events webhook ──────────────────────────────────────────────────────────

// slackEvents is the Slack Events API webhook (the app's request_url:
// https://{domain}/v1/integrations/slack/events). It HMAC-verifies the raw body,
// answers the url_verification challenge, and routes @mentions / DMs — acking FAST
// (empty 200) and doing the billed work async on the channel under the bounded pool,
// deduped durably on event_id. There is one flow: the turn's own tools reach the
// sandbox when the model picks that tool.
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
	// Slack posts INTERACTIVITY to the same request URL as events, form-encoded
	// rather than JSON. It is past the same signature check, so it is equally
	// trusted; it just is not an Events envelope and routeSlackEvent would ignore
	// it. Handled here, before routing, because that is where the two encodings
	// actually diverge.
	if slackInteractionBody(raw) {
		return slackHandleInteraction(s, c, raw)
	}
	d := routeSlackEvent(raw)
	switch d.Kind {
	case slackRouteChallenge:
		return c.String(http.StatusOK, d.Challenge)
	case slackRouteHome:
		// Same ISOLATION ROOT as the agent arm: the org comes ONLY from the
		// install→org map for the Slack-verified team_id, never from the payload.
		org, ok := OrgForExternalID("slack", d.TeamID)
		if !ok {
			return c.NoContent(http.StatusOK)
		}
		tok, err := TokenFor(c.Context(), org, "slack", slackBotTokenSecret)
		if err != nil {
			s.Log.Warn("slack home: no bot token", "org", org, "err", err)
			return c.NoContent(http.StatusOK)
		}
		if err := slackPublishHome(s, c.Context(), string(tok), org, d.User); err != nil {
			// A Home that fails to render is cosmetic — never fail the event, or
			// Slack retries a view publish it will render identically next open.
			s.Log.Warn("slack home: publish failed", "org", org, "err", err)
		}
		return c.NoContent(http.StatusOK)
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
		if conn, ok := ConnectionFor(org, "slack", ""); ok && conn.BotUserID != "" && d.User == conn.BotUserID {
			return c.NoContent(http.StatusOK)
		}
		route := d
		in := Inbound{
			Provider: "slack", ExternalID: route.TeamID, User: route.User,
			Channel: route.Channel, ThreadID: route.ThreadTS, Text: route.Text, DedupeKey: slackEventKey(raw),
		}
		// An event we did not take is a retriable NON-2xx: nothing was recorded, so
		// no event_id is burned and Slack re-delivers it (no lost @mention, no
		// double-run).
		if !emitIngress(c.Context(), s, org, in, "") {
			return zip.Errorf(http.StatusTooManyRequests, "slack event not taken; please redeliver")
		}
		// SAY SOMETHING IMMEDIATELY. A turn is a real model completion and measured
		// 9,955 / 35,893 / 52,985 ms in production — the plumbing is ~25ms of it.
		// Until this, the person saw an empty thread for the whole of that, which is
		// indistinguishable from the bot being broken; the day's actual bug reports
		// were "it does nothing" for a system that was working and slow.
		//
		// setStatus is Slack's own affordance for exactly this and it is the only
		// one available: there is NO SSE to a Slack client, so "streaming" here
		// means a status and then a message, never a token stream.
		//
		// It stays in the ADAPTER while the turn moved to channels, because it is
		// Slack's own gesture and nothing portable answers to it. Best-effort by
		// construction: a failed status must never cost the answer, so the error is
		// dropped and a full pool skips the gesture rather than the reply. Slack
		// clears it when the reply lands.
		if channelLim.acquire(org) {
			channelSpawn(s, org, func() { slackThinking(s, org, route.Channel, route.ThreadTS) })
		}
		return c.NoContent(http.StatusOK)
	case slackRouteJoined:
		// Only the BOT's own join changes anything: a person joining a room it is
		// already in does not widen where it can speak. Recorded, not acted on —
		// the message arm mirrors the room from the next message, with no list to
		// keep and nothing to fall out of step with Slack's own membership.
		org, ok := OrgForExternalID("slack", d.TeamID)
		if !ok {
			return c.NoContent(http.StatusOK)
		}
		if conn, ok := ConnectionFor(org, "slack", ""); ok && conn.BotUserID != "" && d.User == conn.BotUserID {
			s.Log.Info("slack: now mirroring a channel", "org", org, "channel", d.Channel)
		}
		return c.NoContent(http.StatusOK)
	default: // slackRouteAck / slackRouteIgnore — valid but nothing to act on
		return c.NoContent(http.StatusOK)
	}
}

// ── Slash command ───────────────────────────────────────────────────────────

// slackCommands handles a Slack slash command (application/x-www-form-urlencoded)
// at https://{domain}/v1/integrations/slack/commands. Same HMAC gate; deduped on
// trigger_id; acks within Slack's 3s budget (empty 200) and posts the answer
// asynchronously via the command's response_url on the channel.
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
	if !channelLim.acquire(org) {
		s.Log.Warn("slack: at capacity, shedding slash", "org", org)
		return zip.Errorf(http.StatusTooManyRequests, "slack agent pool at capacity")
	}
	fresh, err := s.State.store.MarkEvent(c.Context(), "slack", triggerID)
	if err != nil {
		channelLim.release(org)
		s.Log.Warn("slack: slash dedupe error, skipping", "err", err)
		return c.NoContent(http.StatusOK)
	}
	if !fresh {
		channelLim.release(org)
		return c.NoContent(http.StatusOK)
	}
	in := Inbound{Provider: "slack", ExternalID: team, User: user, Channel: channel, Text: text}
	channelSpawn(s, org, func() { slackSlashTurn(s, org, in, responseURL) })
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

// ── Slack dispatch: every turn goes to the one brain ────────────────────────

// slackSlashTurn is the async slash body dispatched on the channel. An empty org means
// the workspace's Hanzo connection was removed. A body that NAMES a registry command
// runs it as the linked user (slack_command.go); anything else runs the ONE agent
// brain (channelReply) — including a request to change code, which the brain answers
// by calling the coding tool. Delivery is via the (host-pinned) response_url: an agent
// answer goes in_channel; a command's result and the account-link prompt go ephemeral
// (only the invoker sees them).
func slackSlashTurn(s *cloud.Service[state], org string, in Inbound, responseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), channelAgentTimeout)
	defer cancel()
	if org == "" {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "This Slack workspace isn't connected to Hanzo yet.")
		return
	}
	if cmds := commands(s); len(cmds) > 0 {
		if argv, named := resolve(cmds, in.Text); named {
			// EPHEMERAL, and decided here because it is one fact about the whole
			// branch: a command's result is org data the caller asked for and the
			// link prompt carries a URL, so both belong to the person who typed it.
			slackSlashReply(s, ctx, in, responseURL, slackCommandTurn(s, ctx, org, in, cmds, argv), true)
			return
		}
	}
	// A slash command answers synchronously on the request, which already has a
	// span; the run id is recorded on it for the same reason the async turn records
	// one — so this invocation can be joined to the run it caused.
	text, ephemeral, runID := channelReply(s, org, in)
	if runID != "" {
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("hanzo.agent.run_id", runID))
	}
	slackSlashReply(s, ctx, in, responseURL, text, ephemeral)
}

// slackSlashReply delivers a slash answer through the (host-pinned) response_url.
// One function for both branches, so what is ephemeral is decided by whoever
// produced the answer and stated once here — a link prompt or a command result
// reaches only the person who asked; an agent answer goes to the channel.
func slackSlashReply(s *cloud.Service[state], ctx context.Context, in Inbound, responseURL, text string, ephemeral bool) {
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
	slackRouteHome                            // app_home_opened: publish the Home tab
	slackRouteJoined                          // member_joined_channel: the bot now mirrors a room
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
	// The Agents & AI Apps surface. Slack sends assistant_thread_started when a
	// user opens the agent's thread and assistant_thread_context_changed when they
	// navigate; SUBSCRIBING to them is one of the three things that make the app
	// eligible for the "Add Agents" picker (with the assistant:write scope in
	// slack.go and the toggle in the app config).
	//
	// Named explicitly rather than left to `default` even though both merely ack.
	// The default arm acks every unknown event, so the wire behaviour is identical —
	// but a reader looking for "does this app support agents" needs to find the
	// answer here, and a silent default cannot say yes. It also gives the greeting /
	// setSuggestedPrompts call an obvious home when we want one.
	//
	// No reply is needed to open the thread: the user's first message arrives as a
	// normal `message` event with channel_type=="im", which the arm below already
	// routes to the agent. So the conversation works the moment the app is listed.
	case "assistant_thread_started", "assistant_thread_context_changed":
		return slackRoute{Kind: slackRouteAck}
	// The Home tab. Slack shows its OWN "this is still a work in progress"
	// placeholder for any app whose manifest enables home_tab_enabled and which
	// never publishes a view — so an app that does nothing here looks unfinished
	// to every user who clicks it. Publishing on open (rather than once at
	// install) is what Slack's API expects: the view is per-user and is rendered
	// from whatever we publish the moment they look.
	case "app_home_opened":
		if ev.User == "" {
			return slackRoute{Kind: slackRouteAck}
		}
		return slackRoute{Kind: slackRouteHome, TeamID: env.TeamID, User: ev.User}
	case "app_mention":
		if ev.BotID != "" || ev.User == "" || ev.Text == "" {
			return slackRoute{Kind: slackRouteAck}
		}
		return slackRoute{
			Kind: slackRouteAgent, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User,
			Text: stripLeadingMention(ev.Text), ThreadTS: threadOr(ev.ThreadTS, ev.TS),
		}
	// The bot was added to a channel. There is nothing to record: Slack delivers
	// message.channels only for channels the app has JOINED, so its membership IS
	// the subscription and the arm below starts mirroring the room on its own. The
	// event is carried anyway because it is the only moment the bot's reach grows,
	// and an operator who cannot see that has no way to know where it can speak.
	case "member_joined_channel":
		return slackRoute{Kind: slackRouteJoined, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User}
	case "message":
		// Every surface a member can be spoken to on: a public channel, a private
		// channel, a group DM, a DM. It answered ONLY DMs before, so @hanzo sat
		// silent in every room it had been invited to unless someone spelled out its
		// name — a member that hears one of four rooms is not a member.
		//
		// Dropped: the bot's own posts (bot_id, and the bot_message subtype) and
		// every non-plain subtype — an edit, a delete, a join notice — so a reply
		// never loops back on itself and a join notice is not read as a question.
		if ev.BotID != "" || ev.Subtype != "" || ev.User == "" || ev.Text == "" {
			return slackRoute{Kind: slackRouteAck}
		}
		switch ev.ChannelType {
		case "channel", "group", "im", "mpim":
		default:
			return slackRoute{Kind: slackRouteAck}
		}
		// Answer where the conversation already is: under the thread when the
		// message is in one, in the room otherwise. Threading an unthreaded message
		// buries a one-sentence answer behind a "1 reply" nobody clicks, which reads
		// as a dead bot; Slack's own assistants answer inline. A person who
		// deliberately threaded is asking for a side conversation, and gets one.
		//
		// An @mention is the exception, above: it is a summons rather than a turn in
		// the room's conversation, so it threads under itself and leaves the channel
		// as it found it.
		return slackRoute{
			Kind: slackRouteAgent, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User,
			Text: stripLeadingMention(ev.Text), ThreadTS: ev.ThreadTS,
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
		if _, after, ok := strings.Cut(t, ">"); ok {
			return strings.TrimSpace(after)
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
	_, err := slackChatPostTS(ctx, botToken, method, fields)
	return err
}

// slackChatPostTS is that same one path, returning the message timestamp Slack
// answers with. Every chat.* call already gets a `ts` back; it was simply being
// dropped, which is what made a posted message unaddressable and an edit
// impossible. Callers that do not need it use slackChatPost above.
func slackChatPostTS(ctx context.Context, botToken, method string, fields map[string]any) (string, error) {
	payload, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackWebAPIBase+method, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := slackHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, slackMaxBody))
	var data struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		TS    string `json:"ts"`
	}
	_ = json.Unmarshal(body, &data)
	if !data.OK {
		return "", fmt.Errorf("slack %s failed: %s", strings.TrimPrefix(method, "/"), cmp.Or(data.Error, "unknown"))
	}
	return data.TS, nil
}

// slackPostThreadTS posts and reports the new message's id, so a long run can
// come back and rewrite it.
func slackPostThreadTS(ctx context.Context, botToken, channel, threadTS, text string) (string, error) {
	fields := map[string]any{"channel": channel, "text": text}
	if threadTS != "" {
		fields["thread_ts"] = threadTS
	}
	return slackChatPostTS(ctx, botToken, "/chat.postMessage", fields)
}

// slackChatUpdate rewrites an already-posted message in place — Slack's own
// answer to "there is no stream": one message in the thread, edited as the run
// moves, instead of a dozen.
func slackChatUpdate(ctx context.Context, botToken, channel, ts, text string) (string, error) {
	return slackChatPostTS(ctx, botToken, "/chat.update", map[string]any{
		"channel": channel, "ts": ts, "text": text,
	})
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

// NotifySlack posts a Block Kit message to channel on behalf of org's connected
// Slack workspace. It resolves the org's KMS-sealed bot token (TokenFor —
// fail-closed: unmounted / org not connected / KMS-down all error, never post) and
// delivers through PostSlackBlocks. The caller never handles the raw token. This is
// the entry point the git-lifecycle notifier (clients/git) uses so token custody stays
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
// bypassed) from turning the channel into an SSRF/exfil client to an attacker host.
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

// ── config ─────────────────────────────────────────────────────────────────

// slackSigningSecret reads SLACK_SIGNING_SECRET, the plain env its siblings
// SLACK_CLIENT_ID / SLACK_CLIENT_SECRET arrive on, synced from KMS by the one
// cloud-slack-kms-sync KMSSecret — one family of secrets, one delivery.
//
// Empty yields "", which both callers answer with a 503: Slack verification
// fails closed rather than accepting an unverified body.
func slackSigningSecret() string {
	return environ.Or(slackSigningEnv, "")
}

// Which agent answers is not a variable here at all: a slash command asks channels
// for the room's binding (agentRefFor), the same row a mention is answered from.

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

// slackThinking shows Slack's native "is thinking…" indicator on a thread while
// a turn runs.
//
// It exists because the wait is REAL and long: a turn is a model completion,
// measured 9,955–52,985 ms in production against ~25 ms of plumbing. Perceived
// latency is the only kind we can fix without changing the model, and an empty
// thread for fifty seconds reads as a dead bot.
//
// assistant.threads.setStatus is the ONE mechanism Slack offers here. There is no
// SSE to a Slack client, so the honest vocabulary is: a status, then a message.
// Anything else would be a second message to edit, which is worse — it occupies
// the thread with a placeholder the reader has to skip past.
//
// It requires the assistant:write scope and a thread; a channel mention without a
// thread has nowhere to hang a status, so that case is skipped rather than faked.
// Every failure is swallowed: a status that did not render must never cost the
// answer that follows it.
func slackThinking(s *cloud.Service[state], org, channel, threadTS string) {
	if strings.TrimSpace(threadTS) == "" || strings.TrimSpace(channel) == "" {
		return // no thread to decorate; the reply itself is the only signal
	}
	ctx, cancel := context.WithTimeout(context.Background(), slackStatusBudget)
	defer cancel()
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return
	}
	_ = slackChatPost(ctx, string(tok), "/assistant.threads.setStatus", map[string]any{
		"channel_id": channel,
		"thread_ts":  threadTS,
		"status":     "is thinking…",
	})
}

// slackStatusBudget bounds the status call. It is deliberately tiny: the status
// is a courtesy that runs BEFORE the work, so a slow Slack must not delay the
// answer it is announcing.
const slackStatusBudget = 3 * time.Second
