package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	channelsplane "github.com/hanzoai/cloud/plane/channels"
	"github.com/zap-proto/zip"
)

// ingress.go is the chat-ingress client between the platform adapters and the
// channels inbox, plus the transport send helpers. Token custody never leaves
// this package; the event crosses on the PLANE.
//
// It used to cross on a package global — channels.Use installed a consumer
// function pointer here — and a package global is per-PROCESS. In production
// integrations, channels and agents run as three separate processes, so that
// pointer was nil on this side and every event was dropped where a nil check
// returns. The inbox took nothing and the pairing and allowlist gates never saw
// real traffic, silently, for as long as the client existed. This is the same
// mistake plane.AgentsRunOnBehalf was written to undo, one client over.

// emitIngress takes one authenticated event and delivers it to the channels
// inbox: a slot, the durable dedupe, then a detached dispatch over the plane.
// It answers whether it took the event — false means NOTHING was recorded and
// nothing was sent, so the caller answers the platform something that invites a
// redelivery. A duplicate is taken (the original already ran), so the caller has
// nothing left to do either way it answers true.
//
// The three live together because the ORDER is the contract. The slot is taken
// before the dedupe write, so a shed cannot burn an event id and the redelivery
// arrives clean; the dedupe is written before the dispatch, so a redelivery of an
// event that already ran does not run twice.
//
// Each adapter used to spell that order out for itself and then hand its slot to
// the goroutine below, which released it on no path at all. So every dispatched
// event leaked one, the pool is global with a small per-org share, and a few
// tenants of ordinary traffic wedged every tenant on every transport until the
// process restarted. The slot is acquired and released in this one function now,
// which is what makes that unwritable rather than remembered.
//
// The bound is what this process still spends per event: a goroutine and a plane
// call. The agent turn it was originally sized for runs in channels, behind
// channels' own pool; slack's in-process turns are the other holder here, and
// they take their slot beside channelSpawn, which releases it.
func emitIngress(ctx context.Context, s *cloud.Service[state], org string, in Inbound, replyRoot string) bool {
	channelReady()
	if !channelLim.acquire(org) {
		s.Log.Warn("integrations: at capacity, event not taken", "provider", in.Provider, "org", org)
		return false
	}
	// Fail CLOSED on an unreadable ledger, and say so on the wire: nothing was
	// recorded, so a redelivery costs nothing and may well succeed, where a 200
	// would drop the message with no second chance.
	fresh, err := s.State.store.MarkEvent(ctx, in.Provider, in.DedupeKey)
	if err != nil {
		channelLim.release(org)
		s.Log.Warn("integrations: dedupe unreadable, event not taken", "provider", in.Provider, "err", err)
		return false
	}
	if !fresh {
		channelLim.release(org)
		return true
	}
	if _, gerr := s.State.store.GCEvents(ctx, staleEventCutoff()); gerr != nil {
		s.Log.Warn("integrations: dedupe gc", "provider", in.Provider, "err", gerr)
	}
	// Detached, with its own bounded context, so the billed webhook path is never
	// delayed. No request-scoped context crosses the hop — everything the consumer
	// needs rides the event.
	go func() {
		defer channelLim.release(org)
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := channelsplane.ChannelsIngest(ctx,
			ingestIn(org, in, replyRoot))
		// SAID, not swallowed. The whole point of this client is that a dropped
		// event used to be invisible; an unreachable inbox must not become the
		// same silence one layer down.
		if err != nil {
			s.Log.Warn("integrations: channels ingest", "provider", in.Provider, "org", org, "err", err)
			return
		}
		if out != nil && !out.Taken {
			s.Log.Debug("integrations: channels declined event", "provider", in.Provider, "org", org)
		}
	}()
	return true
}

// SendSlackAt is the ONE Slack write, and it either POSTS or EDITS depending on
// whether it was given a message to edit. It returns the message's timestamp —
// Slack's id for it — which is what a later edit addresses.
//
// The two are one function because they are one act: put this text at this
// address. A long-running coding run needs the edit form because Slack has no
// server-sent stream to push progress down; the platform's own answer is to post
// one message and rewrite it (chat.update), and a run that posted a fresh
// message per phase would bury the channel it is reporting into.
//
// The org's bot token is fetched here and nowhere else, so a caller in another
// process reports progress into its own workspace without ever holding the
// token that does it.
func SendSlackAt(ctx context.Context, org, channel, threadTS, updateTS, text string) (string, error) {
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return "", err
	}
	// Progress text comes from a run in another process and is Markdown for the
	// same reason the reply is. Same translation, same edge.
	text = mrkdwn(text)
	if strings.TrimSpace(updateTS) != "" {
		return slackChatUpdate(ctx, string(tok), channel, updateTS, text)
	}
	return slackPostThreadTS(ctx, string(tok), channel, threadTS, text)
}

// SendTelegram posts text to chatID via the Bot API sendMessage, threaded under
// replyTo when non-zero (telegramSend, telegram_events.go).
func SendTelegram(ctx context.Context, chatID, replyTo int64, text string) error {
	return telegramSend(ctx, chatID, replyTo, text)
}

// SendTeams posts a message activity to conversationID at the Bot Connection
// serviceURL (teamsSendActivity, teams_events.go).
func SendTeams(ctx context.Context, serviceURL, conversationID, text string) error {
	return teamsSendActivity(ctx, serviceURL, conversationID, text)
}

// SendDiscord posts text to a Discord channel via POST /channels/{id}/messages,
// referencing replyTo when non-empty. Content is capped at discordMaxContent;
// the bot token rides only the Authorization header, which is never logged.
// Returns the created message id.
func SendDiscord(ctx context.Context, channelID, replyTo, text string) (string, error) {
	tok := discordBotToken()
	if tok == "" {
		return "", fmt.Errorf("discord: bot token not configured")
	}
	if len(text) > discordMaxContent {
		text = text[:discordMaxContent]
	}
	fields := map[string]any{"content": text}
	if replyTo != "" {
		fields["message_reference"] = map[string]string{"message_id": replyTo}
	}
	payload, _ := json.Marshal(fields)
	endpoint := discordAPIBase + "/channels/" + channelID + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := channelHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, channelMaxBody))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("discord create message http %d", resp.StatusCode)
	}
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &m)
	return m.ID, nil
}

// ingestIn is the adapter-normalized event as it crosses to channels. Its own
// function because the mapping is the part that can silently be wrong, and the
// hop it feeds cannot be observed from this process.
func ingestIn(org string, in Inbound, replyRoot string) *plane.ChannelsIngestIn {
	out := &plane.ChannelsIngestIn{
		Org: org, Provider: in.Provider, ExternalID: in.ExternalID, User: in.User,
		Channel: in.Channel, ThreadID: in.ThreadID, Text: in.Text,
		DedupeKey: in.DedupeKey, ReplyRoot: replyRoot,
	}
	// Who installed this workspace, from the connection the org was resolved
	// through. Best-effort: an install that predates this field carries "", and
	// the gate then behaves exactly as it did before.
	for _, c := range Connections(org, in.Provider) {
		if c.ExternalID == in.ExternalID {
			out.Installer = c.Installer
			break
		}
	}
	return out
}

// serveIdentity publishes the linked-account lookup on the plane.
//
// An in-process lookup would be gated on this package's `mounted` global, and a
// package global is per-process. The turn runs in channels, a different process,
// so every such call answers "integrations: not mounted" and the turn runs as
// nobody — silently, because the caller treats identity as best-effort. Custody
// is the reason this stays here: the link lives in KMS under this subsystem, and
// only the answer crosses.
func serveIdentity() {
	zip.Post[plane.ChatIdentityIn, plane.ChatIdentityOut](cloud.Plane(), "/integrations/chat-identity", planeChatIdentity,
		zip.WithOperationID(plane.ChatIdentity),
		zip.WithSummary("Resolve the Hanzo account a chat user has linked"))
}

// planeChatIdentity answers WHO a chat turn runs as, and never with what.
//
// The org rides the request rather than the caller's plane identity, for the
// same reason AgentsRunOnBehalf does: the tenant is the one that connected the
// workspace, which the adapter resolved from a signed id, and the calling
// plugin's own identity is not it. No token is returned under any branch.
func planeChatIdentity(ctx context.Context, in *plane.ChatIdentityIn) (*plane.ChatIdentityOut, error) {
	s := mounted
	if s == nil || in == nil {
		return &plane.ChatIdentityOut{Say: "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly."}, nil
	}
	link, say, ephemeral := channelIdentity(s, in.Org, in.Provider, in.ExternalID, in.User)
	return &plane.ChatIdentityOut{Subject: link.Subject, Model: link.Model, Say: say, Ephemeral: ephemeral}, nil
}

// serveSend publishes the outbound send on the plane.
//
// The Send* helpers above are Go calls, and every one of them ends at TokenFor,
// which is gated on this package's `mounted` global. channels runs in a DIFFERENT
// PROCESS, so holding one as a function value there answers "integrations: not
// mounted" and posts no reply — a turn that runs perfectly and then speaks into
// nothing.
//
// The send stays here because the per-org bot token IS the tenancy gate: an org
// that never connected Slack cannot post, and that property only holds where the
// token is. Only the intent crosses.
func serveSend() {
	zip.Post[plane.ChatSendIn, plane.ChatSendOut](cloud.Plane(), "/integrations/chat-send", planeChatSend,
		zip.WithOperationID(plane.ChatSend),
		zip.WithSummary("Post one message back to a chat platform as the org"))
}

// planeChatSend dispatches to the transport that owns the provider.
//
// The org rides the request rather than the caller's plane identity, as with the
// other chat ops: the tenant is the one that connected the workspace, resolved
// by the adapter from a signed id. It is safe because the send can only spend
// THAT org's own token — TokenFor fails closed for an org that never connected —
// and reads nothing across tenants.
//
// So the org is REQUIRED here, at the boundary, rather than left to each
// transport to notice. Custody is what the org buys: TokenFor's validOrg refuses
// an empty one and telegram's chat bind can never match it, so a caller that
// omitted it reached a per-transport error message on two transports and spent a
// shared app credential unchecked on two others. One refusal, named once, and a
// dropped org is loud where it was silent.
func planeChatSend(ctx context.Context, in *plane.ChatSendIn) (*plane.ChatSendOut, error) {
	if mounted == nil {
		return nil, fmt.Errorf("integrations: not mounted")
	}
	if in == nil || strings.TrimSpace(in.Text) == "" {
		return nil, fmt.Errorf("integrations: chat send needs text")
	}
	if strings.TrimSpace(in.Org) == "" {
		return nil, fmt.Errorf("integrations: chat send needs the org it sends as")
	}
	switch in.Provider {
	case "slack":
		if in.Private {
			if in.User == "" {
				return nil, fmt.Errorf("integrations: a private reply needs someone to send it to")
			}
			tok, terr := TokenFor(ctx, in.Org, "slack", "bot_token")
			if terr != nil {
				return nil, terr
			}
			return &plane.ChatSendOut{}, slackPostEphemeral(ctx, string(tok), in.Room, in.User, in.Text)
		}
		id, err := SendSlackAt(ctx, in.Org, in.Room, in.ReplyTo, "", in.Text)
		return &plane.ChatSendOut{MessageID: id}, err
	case "whatsapp":
		// Every WhatsApp message IS private — one person, one number, no room a
		// third party can see — so the flag has nothing to switch on and asking
		// for a public one would be asking for something the API cannot do.
		id, err := SendWhatsApp(ctx, in.Org, in.Room, in.ReplyTo, in.Text)
		return &plane.ChatSendOut{MessageID: id}, err
	case "discord":
		if in.Private {
			return nil, fmt.Errorf("integrations: discord has no private reply here")
		}
		id, err := SendDiscord(ctx, in.Room, in.ReplyTo, in.Text)
		return &plane.ChatSendOut{MessageID: id}, err
	case "teams":
		return &plane.ChatSendOut{}, SendTeams(ctx, in.Root, in.Room, in.Text)
	case "github":
		// The room is "owner/repo#N"; the reply is an issue comment posted with
		// the installation's own token, so it appears as the App.
		id, err := githubIssueComment(ctx, in.Org, in.Room, in.Text)
		return &plane.ChatSendOut{MessageID: id}, err
	case "linear":
		// The room is the issue id; the reply is a comment posted with the key of
		// the person who bound the organization (linearClaim), so it carries a name.
		id, err := linearIssueComment(ctx, mounted, in.Org, in.Room, in.Text)
		return &plane.ChatSendOut{MessageID: id}, err
	case "telegram":
		// THE ISOLATION ROOT for telegram, and it has to be asked here. There is
		// ONE global bot token, so the chat→org bind is the only thing standing
		// between an org and a chat it never onboarded. channels used to ask
		// OrgForExternalID itself — in-process, from another process, so it always
		// answered "not bound" and every telegram send failed closed. Failing
		// closed hid it; the check still has to be real.
		if boundOrg, ok := OrgForExternalID("telegram", in.Room); !ok || boundOrg != in.Org {
			return nil, fmt.Errorf("integrations: telegram chat %q is not bound to %q", in.Room, in.Org)
		}
		chat, err := strconv.ParseInt(in.Room, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integrations: telegram chat id %q: %w", in.Room, err)
		}
		var replyTo int64
		if in.ReplyTo != "" {
			replyTo, _ = strconv.ParseInt(in.ReplyTo, 10, 64)
		}
		return &plane.ChatSendOut{}, SendTelegram(ctx, chat, replyTo, in.Text)
	}
	return nil, fmt.Errorf("integrations: no transport for %q", in.Provider)
}
