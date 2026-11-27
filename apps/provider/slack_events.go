package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	transportpeer "github.com/hanzoai/cloud/client/transport"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/plane"
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
// The addresses provider.go's routes() registers these at, on the group at
// /v1/provider:
//
//	POST /slack/events
//	POST /slack/commands
//	GET  /slack/link
//	GET  /slack/link/slack
//	GET  /slack/link/callback

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
		slackUsedStates = newSeenSet("provider:slack-link", time.Duration(linkStateTTLSec)*time.Second)
	})
}

// ── Events webhook ──────────────────────────────────────────────────────────

// slackEvents is the Slack Events API webhook (the app's request_url:
// https://{domain}/v1/provider/slack/events). It HMAC-verifies the raw body,
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
		// the bot's OWN message before a pool slot or a dedupe row is spent. A bot
		// echo never triggers a run either way.
		bot := slackBot(org, d)
		if bot != "" && d.User == bot {
			return c.NoContent(http.StatusOK)
		}
		// In a room the agent answers only what is said TO it (slackRoute.addressed).
		// A message nobody addressed to it is acknowledged and nothing is recorded.
		heard, err := d.addressed(bot, func() (bool, error) {
			return s.State.store.InThread(c.Context(), d.TeamID, d.Channel, d.ThreadTS)
		})
		if err != nil {
			s.Log.Warn("slack: thread state unreadable, event not taken", "org", org, "err", err)
			return zip.Errorf(http.StatusServiceUnavailable, "slack event not taken; please redeliver")
		}
		if !heard {
			return c.NoContent(http.StatusOK)
		}
		// A summons makes its thread the agent's: later messages in it are answered
		// without another @mention. The thread is the one the summons was said in,
		// or the one its own ts roots when it was said in the room. Recorded before
		// the turn is taken, so a record that failed is redelivered, not lost.
		if d.summons(bot) {
			if err := s.State.store.JoinThread(c.Context(), d.TeamID, d.Channel, cmp.Or(d.ThreadTS, d.TS)); err != nil {
				s.Log.Warn("slack: thread not recorded, event not taken", "org", org, "err", err)
				return zip.Errorf(http.StatusServiceUnavailable, "slack event not taken; please redeliver")
			}
		}
		// An app_mention does not say whether its room is public, and the message
		// event Slack sends for the same words does; either may be the one taken,
		// so the mention asks Slack rather than answering "not public" by default.
		public := d.Public
		if d.Summons {
			public = slackRoomPublic(c.Context(), org, d.TeamID, d.Channel)
		}
		in := Inbound{
			Provider: "slack", ExternalID: d.TeamID, User: d.User,
			Channel: d.Channel, ThreadID: d.ThreadTS, MessageID: d.TS,
			Text: d.Text, DedupeKey: d.messageKey(), Public: public,
		}
		// An event we did not take is a retriable NON-2xx: nothing was recorded, so
		// no key is burned and Slack re-delivers it (no lost @mention, no
		// double-run).
		if !emitIngress(c.Context(), s, org, in, "") {
			return zip.Errorf(http.StatusTooManyRequests, "slack event not taken; please redeliver")
		}
		// Nothing is said HERE. The turn says "Working…" the moment it starts, in
		// the one message it then edits into its answer (apps/transport/live.go).
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
	case slackRouteGone:
		// Uninstalled in Slack is uninstalled here. The team came signed, and
		// release finds its org the way every event does — by the account — so
		// the workspace stops resolving, its dead token leaves custody, and it is
		// free to install into any org again. A repeat finds nothing and acks.
		orgs, err := release(c.Context(), s, s.State.providers["slack"], d.TeamID)
		if err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "slack uninstall not recorded; please redeliver")
		}
		s.Log.Info("slack: workspace uninstalled", "team", d.TeamID, "orgs", orgs)
		return c.NoContent(http.StatusOK)
	default: // slackRouteAck / slackRouteIgnore — valid but nothing to act on
		return c.NoContent(http.StatusOK)
	}
}

// ── Slash command ───────────────────────────────────────────────────────────

// slackCommands handles a Slack slash command (application/x-www-form-urlencoded)
// at https://{domain}/v1/provider/slack/commands. Same HMAC gate; deduped on
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
	// `/hanzo stop` ends the turns this person has running in this channel. It is
	// the same stop a "stop" said in the conversation makes — channels holds the
	// conversation a turn is rendering in, and agents checks the turn is theirs.
	if isStopWord(in.Text) {
		slackSlashReply(s, cloud.For(ctx, org), in, responseURL, stopTurns(cloud.For(ctx, org), org, in), true)
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
	slackRouteAgent                           // a message to the agent, or one said where it may be addressed
	slackRouteHome                            // app_home_opened: publish the Home tab
	slackRouteJoined                          // member_joined_channel: the bot now mirrors a room
	slackRouteGone                            // app_uninstalled / bot tokens_revoked: the workspace left
)

type slackRoute struct {
	Kind      slackRouteKind
	Challenge string
	TeamID    string
	Channel   string
	User      string
	Text      string
	// ThreadTS is the thread the message is ALREADY IN, and it is empty at the
	// top level. TS is the message's own ts — the root a NEW thread would be
	// opened at. Two fields because they are two facts: one field holding
	// "thread_ts, or ts when there is none" cannot say whether the person spoke
	// in a thread or in the room, and that is the whole of the decision the
	// answering side has to make.
	ThreadTS string
	TS       string
	// Public is a message event's channel_type "channel": everyone in the
	// workspace can read the room. An app_mention does not say, so it is false.
	Public bool
	// Room is a message said in a channel, a private channel or a group DM —
	// anywhere but a DM with the agent. Summons is an app_mention: Slack's own
	// report that the message names the app.
	Room    bool
	Summons bool
	// Mentions are the user ids the message names with <@…>, and Bot the app's
	// bot user as the event's own authorization states it.
	Mentions []string
	Bot      string
}

// addressed reports whether the agent answers this message. A DM is always
// addressed, and so is an app_mention. In a room a message is addressed when it
// @mentions the bot, or when it is in a thread the agent answered in or was
// summoned into (joined reads the thread record). A plain message in a room —
// including one that only spells the app's name — is not, and neither is a reply
// under a notice the bot posted for something other than a turn.
func (d slackRoute) addressed(bot string, joined func() (bool, error)) (bool, error) {
	if !d.Room || d.summons(bot) {
		return true, nil
	}
	if d.ThreadTS == "" {
		return false, nil
	}
	return joined()
}

// summons reports whether the message names the agent: an app_mention, or a
// room message carrying the bot's <@…>. Slack delivers both events for one such
// message; messageKey makes them one turn.
func (d slackRoute) summons(bot string) bool {
	return d.Summons || (bot != "" && slices.Contains(d.Mentions, bot))
}

// messageKey is the dedupe key of the message an event carries. It names the
// message rather than the event, because Slack sends an app_mention AND a
// message event for one @mention, and a retry repeats both: one message, one
// turn.
func (d slackRoute) messageKey() string {
	if d.TeamID == "" || d.Channel == "" || d.TS == "" {
		return ""
	}
	return d.TeamID + ":" + d.Channel + ":" + d.TS
}

// slackBot is the bot user id of org's Slack app: the one its install recorded,
// else the one the event's authorization names.
func slackBot(org string, d slackRoute) string {
	if conn, ok := ConnectionFor(org, "slack", ""); ok && conn.BotUserID != "" {
		return conn.BotUserID
	}
	return d.Bot
}

// slackMentions returns the user ids a Slack message text names, in order:
// every <@U…> and <@U…|label> token.
func slackMentions(text string) []string {
	var ids []string
	for {
		i := strings.Index(text, "<@")
		if i < 0 {
			return ids
		}
		text = text[i+2:]
		j := strings.IndexByte(text, '>')
		if j < 0 {
			return ids
		}
		id, _, _ := strings.Cut(text[:j], "|")
		if id != "" {
			ids = append(ids, id)
		}
		text = text[j+1:]
	}
}

type slackEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	TeamID    string          `json:"team_id"`
	EventID   string          `json:"event_id"`
	Event     json.RawMessage `json:"event"`
	// Authorizations names the installation the event was delivered for; for a
	// bot install its user_id is the app's bot user.
	Authorizations []struct {
		UserID string `json:"user_id"`
		IsBot  bool   `json:"is_bot"`
	} `json:"authorizations"`
}

// bot is the app's bot user id as the envelope's authorization states it.
func (e slackEnvelope) bot() string {
	for _, a := range e.Authorizations {
		if a.IsBot {
			return a.UserID
		}
	}
	return ""
}

type slackMessageEvent struct {
	Type        string                   `json:"type"`
	Channel     string                   `json:"channel"`
	ChannelType string                   `json:"channel_type"`
	User        string                   `json:"user"`
	Text        string                   `json:"text"`
	TS          string                   `json:"ts"`
	Subtype     string                   `json:"subtype"`
	BotID       string                   `json:"bot_id"`
	ThreadTS    string                   `json:"thread_ts"`
	Attachments []slackMessageAttachment `json:"attachments"`
	Blocks      []slackMessageBlock      `json:"blocks"`
	Files       []slackHistoryFile       `json:"files"`
	// Tokens is what a tokens_revoked event revoked: people's tokens under oauth,
	// the app's under bot.
	Tokens struct {
		Bot []string `json:"bot"`
	} `json:"tokens"`
}

// routeSlackEvent decides what to do with a signature-verified Slack payload. Pure
// — no I/O. app_mention and every plain message on a conversation surface route
// to the agent arm, carrying the facts slackRoute.addressed decides on; the
// leading <@BOTID> mention token is stripped so the agent receives the user's
// actual prompt. The bot's own messages (bot_id/subtype) are always dropped so a
// reply never loops back.
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
		text := slackFormatMessageText(stripLeadingMention(ev.Text), ev.Attachments, ev.Blocks, ev.Files)
		if ev.BotID != "" || ev.User == "" || (ev.Text == "" && text == "") {
			return slackRoute{Kind: slackRouteAck}
		}
		// A summons is reported exactly as it arrived: the thread it was in, or
		// none. It used to be reported as threaded ALWAYS — its own ts standing in
		// for the thread it had no thread — so an @mention typed in a channel came
		// out the far side indistinguishable from one typed in a thread, and every
		// answer went behind a "1 reply" nobody clicks. Where to answer is the
		// answering side's decision to make, and it cannot make it from a fact this
		// side has already collapsed.
		return slackRoute{
			Kind: slackRouteAgent, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User,
			Text: text, ThreadTS: ev.ThreadTS, TS: ev.TS,
			Room: true, Summons: true, Mentions: slackMentions(ev.Text), Bot: env.bot(),
		}
	// The bot was added to a channel. There is nothing to record: Slack delivers
	// message.channels only for channels the app has JOINED, so its membership IS
	// the subscription and the arm below starts mirroring the room on its own. The
	// event is carried anyway because it is the only moment the bot's reach grows,
	// and an operator who cannot see that has no way to know where it can speak.
	case "member_joined_channel":
		return slackRoute{Kind: slackRouteJoined, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User}
	// The workspace removed the app. Slack sends app_uninstalled and revokes the
	// bot's token (tokens_revoked), in either order; either one means the
	// workspace is gone. A revoke of people's tokens alone leaves the bot standing.
	case "app_uninstalled":
		return slackRoute{Kind: slackRouteGone, TeamID: env.TeamID}
	case "tokens_revoked":
		if len(ev.Tokens.Bot) == 0 {
			return slackRoute{Kind: slackRouteAck}
		}
		return slackRoute{Kind: slackRouteGone, TeamID: env.TeamID}
	case "message":
		// Every surface a member can be spoken to on: a public channel, a private
		// channel, a group DM, a DM. A DM is a conversation with the agent; on the
		// other three the message is a Room one, answered only when addressed.
		//
		// Dropped: the bot's own posts (bot_id, and the bot_message subtype) and
		// every subtype that is not a person speaking — an edit, a delete, a join
		// notice — so a reply never loops back on itself and a join notice is not
		// read as a question. A message with a file, and a thread reply also sent
		// to the channel, are a person speaking.
		switch ev.Subtype {
		case "", "file_share", "thread_broadcast":
		default:
			return slackRoute{Kind: slackRouteAck}
		}
		text := slackFormatMessageText(stripLeadingMention(ev.Text), ev.Attachments, ev.Blocks, ev.Files)
		if ev.BotID != "" || ev.User == "" || (ev.Text == "" && text == "") {
			return slackRoute{Kind: slackRouteAck}
		}
		switch ev.ChannelType {
		case "channel", "group", "im", "mpim":
		default:
			return slackRoute{Kind: slackRouteAck}
		}
		// Both facts, unconflated: the thread this was said in (empty in the room),
		// and the message's own ts. A person who deliberately threaded is asking for
		// a side conversation and gets one; everyone else gets an answer where they
		// spoke.
		return slackRoute{
			Kind: slackRouteAgent, TeamID: env.TeamID, Channel: ev.Channel, User: ev.User,
			Text: text, ThreadTS: ev.ThreadTS, TS: ev.TS,
			Public: ev.ChannelType == "channel", Room: ev.ChannelType != "im",
			Mentions: slackMentions(ev.Text), Bot: env.bot(),
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

// slackPostEphemeral posts a message visible ONLY to `user` in `channel` — used
// for the account-link prompt so a link URL is NEVER shown to a whole channel,
// and for telling a presser a run is not theirs — in threadTS when it is set.
func slackPostEphemeral(ctx context.Context, botToken, channel, user, threadTS, text string) error {
	fields := map[string]any{"channel": channel, "user": user, "text": text}
	if threadTS != "" {
		fields["thread_ts"] = threadTS
	}
	return slackChatPost(ctx, botToken, "/chat.postEphemeral", fields)
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
	return env.Or(slackSigningEnv, "")
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

// isStopWord reports whether a slash body asks to stop what is running.
func isStopWord(text string) bool {
	return strings.EqualFold(strings.Trim(text, " \t\n.!"), "stop")
}

// stopTurns asks channels to stop this person's turns in this channel, and says
// what happened. The org rides the caller, as every stop does.
func stopTurns(ctx context.Context, org string, in Inbound) string {
	out, err := plane.Call[client.TransportStopIn, client.SessionCount](ctx, transportpeer.App, client.TransportStop,
		&client.TransportStopIn{Provider: in.Provider, Account: in.ExternalID, Channel: in.Channel, User: in.User})
	if err != nil {
		return "I could not reach the agent to stop it. This is on our side — please try again."
	}
	if out == nil || out.Count == 0 {
		return "Nothing of yours is running here."
	}
	return "Stopped."
}
