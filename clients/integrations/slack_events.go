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

	"github.com/hanzoai/cloud"
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
// MOUNT HANDOFF (registered in integrations.go's routes(); this file deliberately
// does NOT edit Mount/routes — clean separation):
//
//	app.Post("/v1/integrations/slack/events",        cloud.Handle(s, slackEvents))
//	app.Post("/v1/integrations/slack/commands",      cloud.Handle(s, slackCommands))
//	app.Get("/v1/integrations/slack/link",           cloud.Handle(s, slackLink))
//	app.Get("/v1/integrations/slack/link/slack",     cloud.Handle(s, slackLinkSlack))
//	app.Get("/v1/integrations/slack/link/callback",  cloud.Handle(s, slackLinkCallback))

// ── bridge process state (bounded pool + single-use link nonces) ────────────

const (
	// slackAgentTimeout bounds one async agent turn end to end (org resolve +
	// run + Slack post). Generous: a run executes a real model completion.
	slackAgentTimeout = 110 * time.Second
	// slackDefaultConcurrency caps simultaneous agent turns ACROSS ALL orgs so a
	// workspace insider cannot exhaust goroutines/FDs by bursting @hanzo.
	// Overridable via SLACK_AGENT_CONCURRENCY.
	slackDefaultConcurrency = 32
	// slackDefaultOrgConcurrency caps simultaneous agent turns for a SINGLE org (a
	// fraction of the global pool) so one tenant cannot starve the others.
	// Overridable via SLACK_AGENT_ORG_CONCURRENCY.
	slackDefaultOrgConcurrency = 8
	// slackMaxBody bounds the webhook body we read + sign over. Slack payloads are
	// small; a hostile/oversized body can neither exhaust memory nor slip past the
	// HMAC (we sign exactly what we read).
	slackMaxBody = 1 << 20 // 1 MiB
)

var (
	slackBridgeOnce sync.Once
	slackLim        *orgLimiter // bounded agent-turn pool: total cap + PER-ORG sub-limit
	slackUsedStates *seenSet    // single-use link-state nonces (process-lifetime)
)

// slackBridgeReady lazily initializes the bridge's process state: the bounded
// agent-turn pool (global cap + per-org sub-limit) and the single-use link
// seen-set. The durable dedupe table is created in the store's migrate() at Mount,
// so nothing store-scoped happens here. Cheap + idempotent; every Slack handler
// calls it first.
func slackBridgeReady(s *cloud.Service[state]) {
	slackBridgeOnce.Do(func() {
		slackLim = newOrgLimiter(slackAgentConcurrency(), slackOrgConcurrency())
		codingLim = newOrgLimiter(codingConcurrency(), codingOrgConcurrency())
		slackUsedStates = newSeenSet(time.Duration(slackLinkTTLSec) * time.Second)
	})
}

// slackSpawn runs an ALREADY-SLOTTED agent turn in a recovered goroutine. The
// webhook handler acquires the pool slot SYNCHRONOUSLY (slackLim.acquire) BEFORE
// recording the dedupe key, so a capacity shed can return a retriable non-2xx
// without burning the event_id (Red M-1); the slotted turn is then handed here.
// Two guarantees: (1) the slot is released on every exit, and (2) a panic anywhere
// in the turn (handleSlack* → slackAgentReply → agents.RunOnBehalf — a large
// surface over UNTRUSTED Slack input) is CONTAINED. On the SHARED multi-tenant
// cloud binary this is non-negotiable: middleware.Recover() wraps only the sync
// request goroutine, so an unrecovered panic here would crash EVERY tenant and
// subsystem (Red M-2). The recover defer is registered LAST so it runs FIRST
// (LIFO), and release still runs after it — a panicking turn frees its slot.
func slackSpawn(s *cloud.Service[state], org string, run func()) {
	go func() {
		defer slackLim.release(org)
		defer slackRecover(s, org)
		run()
	}()
}

// slackRecover contains a panic in an async agent turn so it can never crash the
// shared cloud process. Called ONLY as a deferred func (recover must be a direct
// call in the deferred function).
func slackRecover(s *cloud.Service[state], org string) {
	if r := recover(); r != nil {
		s.Log.Error("slack: agent turn panic (recovered)", "org", org, "err", r)
	}
}

// orgLimiter bounds concurrent agent turns two ways: a GLOBAL cap (total in-flight
// across all orgs) AND a PER-ORG cap (max in-flight for any single org). Data /
// token / billing isolation already holds via the resolved org; this adds the
// AVAILABILITY isolation that stops one tenant exhausting the shared worker pool.
type orgLimiter struct {
	mu       sync.Mutex
	inflight map[string]int
	perOrg   int
	global   chan struct{}
}

func newOrgLimiter(global, perOrg int) *orgLimiter {
	if global < 1 {
		global = 1
	}
	if perOrg < 1 {
		perOrg = 1
	}
	if perOrg > global {
		perOrg = global
	}
	return &orgLimiter{inflight: make(map[string]int), perOrg: perOrg, global: make(chan struct{}, global)}
}

// acquire takes one global + one per-org slot for org, non-blocking. It returns
// false (nothing acquired, no slot leaked) when the org is at its per-org cap OR
// the global pool is full — the per-org check precedes the global take, and the
// global take only bumps the per-org count on a successful send.
func (l *orgLimiter) acquire(org string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[org] >= l.perOrg {
		return false
	}
	select {
	case l.global <- struct{}{}:
		l.inflight[org]++
		return true
	default:
		return false
	}
}

// release returns the org's slot and the global slot. Called exactly once per
// successful acquire.
func (l *orgLimiter) release(org string) {
	l.mu.Lock()
	if n := l.inflight[org]; n > 1 {
		l.inflight[org] = n - 1
	} else {
		delete(l.inflight, org)
	}
	l.mu.Unlock()
	<-l.global
}

// ── Events webhook ──────────────────────────────────────────────────────────

// slackEvents is the Slack Events API webhook (the app's request_url:
// https://{domain}/v1/integrations/slack/events). It HMAC-verifies the raw body,
// answers the url_verification challenge, and routes @mentions / DMs to an
// on-behalf-of agent run — acking FAST (empty 200) and doing the billed work
// async under the bounded pool, deduped durably on event_id.
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
		// tenant and a shed happens BEFORE anything is recorded): org comes ONLY
		// from the install→org map for the Slack-verified team_id. An event for a
		// team no org connected is dropped (nothing to do — not a shed).
		org, ok := OrgForExternalID("slack", d.TeamID)
		if !ok {
			s.Log.Warn("slack: event for unconnected team", "team", d.TeamID)
			return c.NoContent(http.StatusOK)
		}
		// SHED BEFORE the dedupe write (Red M-1). Try to acquire a pool slot first;
		// if the pool is full, record NOTHING and return a retriable NON-2xx — the
		// turn never ran, so no event_id is burned and Slack re-delivers when a slot
		// frees (no lost @mention, no double-run).
		if !slackLim.acquire(org) {
			s.Log.Warn("slack: at capacity, shedding for retry", "org", org)
			return zip.Errorf(http.StatusTooManyRequests, "slack agent pool at capacity")
		}
		// Slot held. DURABLE dedupe (BILLED path): a Slack retry of an event that
		// already ran must never double-run. Release the slot on every path that
		// does NOT dispatch. Fail CLOSED on a dedupe error (skip) rather than risk a
		// double charge.
		fresh, err := s.State.store.MarkSlackEvent(c.Context(), slackEventKey(raw))
		if err != nil {
			slackLim.release(org)
			s.Log.Warn("slack: event dedupe error, skipping", "err", err)
			return c.NoContent(http.StatusOK)
		}
		if !fresh {
			slackLim.release(org)
			return c.NoContent(http.StatusOK)
		}
		// Opportunistic GC so the dedupe table cannot grow without bound.
		if _, gerr := s.State.store.GCSlackEvents(c.Context(), staleSlackEventCutoff()); gerr != nil {
			s.Log.Warn("slack: dedupe gc", "err", gerr)
		}
		route := d
		slackSpawn(s, org, func() { handleSlackAgent(s, org, route) })
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
	// removed; bounded under the same limiter, handled in handleSlackSlash), then
	// SHED before the dedupe write (Red M-1), same order as the events path.
	org, _ := OrgForExternalID("slack", team)
	if !slackLim.acquire(org) {
		s.Log.Warn("slack: at capacity, shedding slash", "org", org)
		return zip.Errorf(http.StatusTooManyRequests, "slack agent pool at capacity")
	}
	fresh, err := s.State.store.MarkSlackEvent(c.Context(), triggerID)
	if err != nil {
		slackLim.release(org)
		s.Log.Warn("slack: slash dedupe error, skipping", "err", err)
		return c.NoContent(http.StatusOK)
	}
	if !fresh {
		slackLim.release(org)
		return c.NoContent(http.StatusOK)
	}
	route := slackRoute{Kind: slackRouteAgent, TeamID: team, Channel: channel, User: user, Text: text}
	slackSpawn(s, org, func() { handleSlackSlash(s, org, route, responseURL) })
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

// handleSlackAgent answers an @mention / DM for a PRE-RESOLVED org (the ISOLATION
// ROOT, resolved in the sync webhook path via OrgForExternalID on the
// Slack-verified team_id — never a client value). It skips the bot's own echo,
// fetches THAT org's bot token (the reply sink), and posts the agent's answer — or,
// when the user is unlinked, the account-link prompt EPHEMERALLY (so a link URL
// never reaches a whole channel) — into the SAME thread.
func handleSlackAgent(s *cloud.Service[state], org string, d slackRoute) {
	ctx, cancel := context.WithTimeout(context.Background(), slackAgentTimeout)
	defer cancel()

	// Echo-loop guard: drop the bot's OWN messages using THIS org's recorded bot
	// user id (the CTO seam), belt-and-suspenders with the route-level bot_id drop.
	if conn, ok := ConnectionFor(org, "slack"); ok && conn.BotUserID != "" && d.User == conn.BotUserID {
		return
	}
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		s.Log.Warn("slack: bot token fetch", "team", d.TeamID, "err", err)
		return
	}
	// A `code:` prefix branches off the chat-only reply into a durable coding run
	// (fresh sandbox agent → native git branch → PR, reported back in-thread).
	// Everything else stays on the existing chat path below, unchanged.
	if codingText, isCoding := codingIntent(d.Text); isCoding {
		handleSlackCoding(s, ctx, org, string(tok), d.TeamID, d.Channel, d.ThreadTS, d.User, codingText)
		return
	}
	reply, linkPrompt := slackAgentReply(s, ctx, org, d.TeamID, d.User, d.Text)
	if reply == "" {
		return
	}
	if linkPrompt {
		if err := slackPostEphemeral(ctx, string(tok), d.Channel, d.User, reply); err != nil {
			s.Log.Warn("slack: ephemeral post", "team", d.TeamID, "err", err)
		}
		return
	}
	if err := slackPostThread(ctx, string(tok), d.Channel, d.ThreadTS, reply); err != nil {
		s.Log.Warn("slack: thread post", "team", d.TeamID, "err", err)
	}
}

// handleSlackSlash runs the agent for a slash command (org PRE-RESOLVED in the
// sync path) and delivers the reply via the (host-pinned) response_url. An answer
// goes in_channel; a link prompt goes ephemeral (only the invoker sees it). An
// empty org means the workspace's Hanzo connection was removed.
func handleSlackSlash(s *cloud.Service[state], org string, d slackRoute, responseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), slackAgentTimeout)
	defer cancel()
	if org == "" {
		_ = slackPostResponseURL(ctx, responseURL, "ephemeral", "This Slack workspace isn't connected to Hanzo yet.")
		return
	}
	// A `code:` prefix branches into a durable coding run (see slack_coding.go).
	if codingText, isCoding := codingIntent(d.Text); isCoding {
		handleSlackSlashCoding(s, ctx, org, d.TeamID, d.Channel, d.User, codingText, responseURL)
		return
	}
	reply, linkPrompt := slackAgentReply(s, ctx, org, d.TeamID, d.User, d.Text)
	if reply == "" {
		return
	}
	responseType := "in_channel"
	if linkPrompt {
		responseType = "ephemeral"
	}
	if err := slackPostResponseURL(ctx, responseURL, responseType, reply); err != nil {
		s.Log.Warn("slack: slash reply", "team", d.TeamID, "err", err)
	}
}

// slackAgentReply is the ONE agent brain, shared by the @mention/DM and slash
// paths. It resolves the caller's linked Hanzo identity and either runs the agent
// ON BEHALF OF them IN-PROCESS (RunOnBehalf — no gateway hop) returning the
// model's answer, or, when unlinked, returns a short prompt carrying the link URL.
// linkPrompt reports whether the reply is the (sensitive) link prompt — the caller
// MUST deliver those ephemerally. Every returned string is safe to post; internal
// errors are logged (never a token) and surfaced as a terse message.
func slackAgentReply(s *cloud.Service[state], ctx context.Context, org, teamID, slackUser, text string) (reply string, linkPrompt bool) {
	link, linked, err := getSlackUserLink(s, org, slackUser)
	if err != nil {
		s.Log.Warn("slack: user link lookup", "team", teamID, "err", err)
		return "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly.", false
	}
	if !linked {
		u, serr := slackLinkURL(s, teamID, slackUser)
		if serr != nil {
			s.Log.Error("slack: link url", "team", teamID, "err", serr)
			return "Connect your Hanzo account to use @hanzo.", true
		}
		return "Connect your Hanzo account to use @hanzo: " + u, true
	}
	// IN-PROCESS on-behalf-of run: org (isolation gate) + the linked user's Hanzo
	// subject drive billing/attribution; org is the tenant + balance. No bearer, no
	// gateway hop. The agent ref is env-set (SLACK_AGENT_REF, default "hanzo").
	run, rerr := agents.RunOnBehalf(ctx, org, link.Subject, slackAgentRef(), text)
	if rerr != nil {
		s.Log.Warn("slack: agent run", "team", teamID, "err", rerr) // never logs a token
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

// slackOrgConcurrency caps how many agent turns a SINGLE org may run concurrently
// (Red M1 availability isolation) — a fraction of the global pool, so one tenant
// can't monopolize it. Overridable via SLACK_AGENT_ORG_CONCURRENCY; clamped to the
// global cap in newOrgLimiter.
func slackOrgConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SLACK_AGENT_ORG_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return slackDefaultOrgConcurrency
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
