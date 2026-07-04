package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/zap-proto/zip"
)

// slack_events.go is the @hanzo agent FRONT-DOOR inside the integrations plane:
// the Slack Events webhook + slash command, routed to an on-behalf-of agent run
// and answered in-thread. It CONSUMES the existing Slack OAuth provider
// (slack.go) — the per-org bot token it sealed — and the framework seams
// (OrgForExternalID / TokenFor / ConnectionFor); it never re-implements custody
// or org resolution.
//
// ISOLATION BAR: a workspace's events reach ONLY the org that connected that
// Slack team. The org comes ONLY from OrgForExternalID(team_id) — never the
// payload — the reply uses THAT org's bot token (TokenFor), and the run is THAT
// org's agent on behalf of THAT org's linked user. team_id itself is trustworthy
// only because the whole payload is HMAC-verified with SLACK_SIGNING_SECRET first.
//
// MOUNT HANDOFF (clients/integrations owner adds these lines to Mount; this file
// deliberately does NOT edit Mount — clean separation):
//
//	app.Post("/v1/integrations/slack/events",        s.slackEvents)
//	app.Post("/v1/integrations/slack/commands",      s.slackCommands)
//	app.Get("/v1/integrations/slack/link",           s.slackLink)
//	app.Get("/v1/integrations/slack/link/slack",     s.slackLinkSlack)
//	app.Get("/v1/integrations/slack/link/callback",  s.slackLinkCallback)

// ── bridge process state (bounded pool + single-use link nonces) ────────────

const (
	// slackAgentTimeout bounds one async agent turn end to end (org resolve +
	// run + Slack post). Generous: a run executes a real model completion.
	slackAgentTimeout = 110 * time.Second
	// slackDefaultConcurrency caps simultaneous agent turns so a workspace
	// insider cannot exhaust goroutines/FDs by bursting @hanzo. Overridable via
	// SLACK_AGENT_CONCURRENCY.
	slackDefaultConcurrency = 32
	// slackMaxBody bounds the webhook body we read + sign over. Slack payloads are
	// small; a hostile/oversized body can neither exhaust memory nor slip past the
	// HMAC (we sign exactly what we read).
	slackMaxBody = 1 << 20 // 1 MiB
)

var (
	slackBridgeOnce sync.Once
	slackSem        chan struct{} // agent-turn concurrency cap (process-lifetime)
	slackUsedStates *seenSet      // single-use link-state nonces (process-lifetime)
	// slackEnsured tracks which mounted store has had its dedupe table created, so
	// ensureSlackEvents runs once per store (correct across test remounts) without
	// touching the hot path after the first call.
	slackEnsured sync.Map // *Store -> struct{}
)

// slackBridgeReady lazily initializes the bridge's process state (bounded worker
// pool + single-use link seen-set) and ensures the durable dedupe table exists
// for THIS mount's store. Cheap + idempotent on the hot path; every Slack handler
// calls it first.
func (s *svc) slackBridgeReady() {
	slackBridgeOnce.Do(func() {
		slackSem = make(chan struct{}, slackAgentConcurrency())
		slackUsedStates = newSeenSet(time.Duration(slackLinkTTLSec) * time.Second)
	})
	if _, done := slackEnsured.LoadOrStore(s.store, struct{}{}); !done {
		if err := s.store.ensureSlackEvents(); err != nil {
			s.log.Warn("slack: ensure dedupe table", "err", err)
		}
	}
}

// slackDispatch runs an agent turn under the concurrency cap. It acquires a slot
// non-blockingly; if the pool is full the turn is dropped + logged (a DoS insider
// cannot pile up unbounded goroutines/FDs).
func (s *svc) slackDispatch(run func()) {
	select {
	case slackSem <- struct{}{}:
		go func() {
			defer func() { <-slackSem }()
			run()
		}()
	default:
		s.log.Warn("slack: agent pool at capacity, turn dropped")
	}
}

// ── Events webhook ──────────────────────────────────────────────────────────

// slackEvents is the Slack Events API webhook (the app's request_url:
// https://{domain}/v1/integrations/slack/events). It HMAC-verifies the raw body,
// answers the url_verification challenge, and routes @mentions / DMs to an
// on-behalf-of agent run — acking FAST (empty 200) and doing the billed work
// async under the bounded pool, deduped durably on event_id.
func (s *svc) slackEvents(c *zip.Ctx) error {
	s.slackBridgeReady()
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
		// DURABLE dedupe (billed path): a Slack retry must never double-run. Fail
		// CLOSED on a dedupe error (skip) rather than risk a double charge.
		fresh, err := s.store.MarkSlackEvent(c.Context(), slackEventKey(raw))
		if err != nil {
			s.log.Warn("slack: event dedupe error, skipping", "err", err)
			return c.NoContent(http.StatusOK)
		}
		if !fresh {
			return c.NoContent(http.StatusOK)
		}
		// Opportunistic GC so the dedupe table cannot grow without bound.
		if _, gerr := s.store.GCSlackEvents(c.Context(), staleSlackEventCutoff()); gerr != nil {
			s.log.Warn("slack: dedupe gc", "err", gerr)
		}
		route := d
		s.slackDispatch(func() { s.handleSlackAgent(route) })
		return c.NoContent(http.StatusOK)
	default: // slackRouteAck / slackRouteIgnore — valid but nothing to act on
		return c.NoContent(http.StatusOK)
	}
}

// ── Slash command ───────────────────────────────────────────────────────────

// slackCommands handles a Slack slash command (application/x-www-form-urlencoded)
// at https://{domain}/v1/integrations/slack/commands. Same HMAC gate; deduped on
// trigger_id; acks within Slack's 3s budget (empty 200) and posts the answer
// asynchronously via the command's response_url.
func (s *svc) slackCommands(c *zip.Ctx) error {
	s.slackBridgeReady()
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
	fresh, err := s.store.MarkSlackEvent(c.Context(), triggerID)
	if err != nil {
		s.log.Warn("slack: slash dedupe error, skipping", "err", err)
		return c.NoContent(http.StatusOK)
	}
	if !fresh {
		return c.NoContent(http.StatusOK)
	}
	route := slackRoute{Kind: slackRouteAgent, TeamID: team, Channel: channel, User: user, Text: text}
	s.slackDispatch(func() { s.handleSlackSlash(route, responseURL) })
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

// ── the bridge (per-org isolated) ───────────────────────────────────────────

// handleSlackAgent answers an @mention / DM. It resolves the org from the
// Slack-verified team_id (ISOLATION ROOT), skips the bot's own echo, fetches THAT
// org's bot token (the reply sink), and posts the agent's answer — or, when the
// user is unlinked, the account-link prompt EPHEMERALLY (so a link URL never
// reaches a whole channel) — into the SAME thread.
func (s *svc) handleSlackAgent(d slackRoute) {
	ctx, cancel := context.WithTimeout(context.Background(), slackAgentTimeout)
	defer cancel()

	// ISOLATION ROOT: org is derived ONLY from the install→org map keyed by the
	// Slack-verified team_id — NEVER from the payload/client. A team resolves to
	// exactly the org that connected it (earliest-connected wins in the store), so
	// team A's event can never act as org B.
	org, ok := OrgForExternalID("slack", d.TeamID)
	if !ok {
		s.log.Warn("slack: event for unconnected team", "team", d.TeamID)
		return
	}
	// Echo-loop guard: drop the bot's OWN messages using THIS org's recorded bot
	// user id (the CTO seam), belt-and-suspenders with the route-level bot_id drop.
	if conn, ok := ConnectionFor(org, "slack"); ok && conn.BotUserID != "" && d.User == conn.BotUserID {
		return
	}
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		s.log.Warn("slack: bot token fetch", "team", d.TeamID, "err", err)
		return
	}
	reply, linkPrompt := s.slackAgentReply(ctx, org, d.TeamID, d.User, d.Text)
	if reply == "" {
		return
	}
	if linkPrompt {
		if err := slackPostEphemeral(ctx, string(tok), d.Channel, d.User, reply); err != nil {
			s.log.Warn("slack: ephemeral post", "team", d.TeamID, "err", err)
		}
		return
	}
	if err := slackPostThread(ctx, string(tok), d.Channel, d.ThreadTS, reply); err != nil {
		s.log.Warn("slack: thread post", "team", d.TeamID, "err", err)
	}
}

// handleSlackSlash runs the agent for a slash command and delivers the reply via
// the (host-pinned) response_url. An answer goes in_channel; a link prompt goes
// ephemeral (only the invoker sees it).
func (s *svc) handleSlackSlash(d slackRoute, responseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), slackAgentTimeout)
	defer cancel()
	org, ok := OrgForExternalID("slack", d.TeamID)
	if !ok {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "This Slack workspace isn't connected to Hanzo yet.")
		return
	}
	reply, linkPrompt := s.slackAgentReply(ctx, org, d.TeamID, d.User, d.Text)
	if reply == "" {
		return
	}
	responseType := "in_channel"
	if linkPrompt {
		responseType = "ephemeral"
	}
	if err := slackPostResponseURL(ctx, responseURL, responseType, reply); err != nil {
		s.log.Warn("slack: slash reply", "team", d.TeamID, "err", err)
	}
}

// slackAgentReply is the ONE agent brain, shared by the @mention/DM and slash
// paths. It resolves the caller's linked Hanzo identity and either runs the agent
// ON BEHALF OF them IN-PROCESS (RunOnBehalf — no gateway hop) returning the
// model's answer, or, when unlinked, returns a short prompt carrying the link URL.
// linkPrompt reports whether the reply is the (sensitive) link prompt — the caller
// MUST deliver those ephemerally. Every returned string is safe to post; internal
// errors are logged (never a token) and surfaced as a terse message.
func (s *svc) slackAgentReply(ctx context.Context, org, teamID, slackUser, text string) (reply string, linkPrompt bool) {
	link, linked, err := s.getSlackUserLink(org, slackUser)
	if err != nil {
		s.log.Warn("slack: user link lookup", "team", teamID, "err", err)
		return "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly.", false
	}
	if !linked {
		u, serr := s.slackLinkURL(teamID, slackUser)
		if serr != nil {
			s.log.Error("slack: link url", "team", teamID, "err", serr)
			return "Connect your Hanzo account to use @hanzo.", true
		}
		return "Connect your Hanzo account to use @hanzo: " + u, true
	}
	// IN-PROCESS on-behalf-of run: org (isolation gate) + the linked user's Hanzo
	// subject drive billing/attribution; org is the tenant + balance. No bearer, no
	// gateway hop. The agent ref is env-set (SLACK_AGENT_REF, default "hanzo").
	run, rerr := agents.RunOnBehalf(ctx, org, link.Subject, slackAgentRef(), text)
	if rerr != nil {
		s.log.Warn("slack: agent run", "team", teamID, "err", rerr) // never logs a token
		return "Sorry — the agent hit an error handling that. Please try again.", false
	}
	if run.Status != "ok" {
		return "Sorry — the agent hit an error handling that. Please try again.", false
	}
	if strings.TrimSpace(run.Output) == "" {
		return "(the agent returned an empty response)", false
	}
	return run.Output, false
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
// package var the OAuth provider already exposes, repointable by tests).
func slackChatPost(ctx context.Context, botToken, method string, fields map[string]string) error {
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
	fields := map[string]string{"channel": channel, "text": text}
	if threadTS != "" {
		fields["thread_ts"] = threadTS
	}
	return slackChatPost(ctx, botToken, "/chat.postMessage", fields)
}

// slackPostEphemeral posts a message visible ONLY to `user` in `channel` — used
// for the account-link prompt so a link URL is NEVER shown to a whole channel.
func slackPostEphemeral(ctx context.Context, botToken, channel, user, text string) error {
	return slackChatPost(ctx, botToken, "/chat.postEphemeral", map[string]string{
		"channel": channel, "user": user, "text": text,
	})
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

func slackAgentRef() string {
	if v := strings.TrimSpace(os.Getenv("SLACK_AGENT_REF")); v != "" {
		return v
	}
	return "hanzo"
}

func slackAgentConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SLACK_AGENT_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return slackDefaultConcurrency
}

// slackReadBody returns the exact raw request body the HMAC must be computed over,
// bounded to slackMaxBody. fiber's transport already bounds the body; this is the
// local belt-and-suspenders at the crypto boundary.
func slackReadBody(c *zip.Ctx) []byte {
	b := c.Body()
	if len(b) > slackMaxBody {
		return b[:slackMaxBody]
	}
	return b
}
