package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	transportpeer "github.com/hanzoai/cloud/client/transport"
	"github.com/hanzoai/cloud/sdk"
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
// mistake client.AgentsRunOnBehalf was written to undo, one client over.

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
// call. The agent turn it was originally sized for runs in transport, behind
// transport's own pool; slack's in-process turns are the other holder here, and
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
		// A PANIC HERE IS A DROPPED MESSAGE, and it used to be a SILENT one.
		//
		// The recover is right: this goroutine is detached, so a panic escaping it
		// takes the process down and every other tenant's events with it. Throwing
		// the value away is what was wrong. The two branches below go to some
		// trouble to say that an event was not taken — and the one failure that
		// leaves no trace at all went to neither, so a room where nobody ever
		// answered looked exactly like a room where nobody had asked.
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("integrations: ingest panicked; the event is lost",
					"provider", in.Provider, "org", org, "panic", v,
					"stack", string(debug.Stack()))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := transportpeer.TransportIngest(ctx,
			ingestIn(org, in, replyRoot))
		// SAID, not swallowed — in the log AND to the person. The whole point of
		// this client is that a dropped event used to be invisible; an unreachable
		// inbox must not become the same silence one layer down, and the dedupe row
		// above is already written, so no redelivery will come to answer it.
		if err != nil {
			s.Log.Warn("integrations: channels ingest", "provider", in.Provider, "org", org, "err", err)
			untaken(s, ctx, org, in)
			return
		}
		if out != nil && !out.Taken {
			s.Log.Warn("integrations: channels declined event", "provider", in.Provider, "org", org)
			untaken(s, ctx, org, in)
		}
	}()
	return true
}

// ingestLost is what a person is told when their message never reached the agent.
const ingestLost = "Your message did not reach the agent — the service that runs it did not take it. This is on our side; please ask again in a minute."

// untaken tells the person whose message channels did not take, in the room they
// asked in. Slack is answered from here because this process holds the
// workspace's bot token; a transport whose replies go only through channels —
// the thing that just failed — has nobody else to say it.
func untaken(s *cloud.Service[state], ctx context.Context, org string, in Inbound) {
	if in.Provider != "slack" {
		return
	}
	if _, err := SendSlackAt(ctx, org, in.Channel, in.ThreadID, "", ingestLost, client.Controls{}, client.Question{}); err != nil {
		s.Log.Warn("integrations: could not say a message went untaken", "org", org, "err", err)
	}
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
//
// A run still going carries its controls (slack_controls.go) as Block Kit under
// the text, and the text stays beside them for notifications and screen readers.
// A question a run asked is drawn the same way, with an answer per option
// (questionBlocks). An edit with neither takes the buttons away — an empty
// `blocks` is how chat.update is told to — so a run that is over shows no Stop and
// an answered question no options.
func SendSlackAt(ctx context.Context, org, channel, threadTS, updateTS, text string, ctl client.Controls, q client.Question) (string, error) {
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return "", err
	}
	// Progress text comes from a run in another process and is Markdown for the
	// same reason the reply is. Same translation, same edge.
	text = mrkdwn(text)
	fields := map[string]any{"channel": channel, "text": text}
	switch {
	case q.ID != "":
		fields["blocks"] = questionBlocks(q, threadTS != "")
	case ctl.Session != "":
		fields["blocks"] = controlBlocks(text, ctl)
	}
	if strings.TrimSpace(updateTS) != "" {
		fields["ts"] = updateTS
		if fields["blocks"] == nil {
			fields["blocks"] = []any{}
		}
		return slackChatPostTS(ctx, string(tok), "/chat.update", fields)
	}
	if threadTS != "" {
		fields["thread_ts"] = threadTS
	}
	return slackChatPostTS(ctx, string(tok), "/chat.postMessage", fields)
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

// ingestIn is the adapter-normalized event as it crosses to transport. Its own
// function because the mapping is the part that can silently be wrong, and the
// hop it feeds cannot be observed from this process.
func ingestIn(org string, in Inbound, replyRoot string) *client.TransportIngestIn {
	out := &client.TransportIngestIn{
		Org: org, Provider: in.Provider, ExternalID: in.ExternalID, User: in.User,
		Channel: in.Channel, ThreadID: in.ThreadID, MessageID: in.MessageID, Text: in.Text,
		DedupeKey: in.DedupeKey, ReplyRoot: replyRoot, Public: in.Public,
		Group: in.Group, Attachments: in.Attachments,
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
// package global is per-process. The turn runs in transport, a different process,
// so every such call answers "integrations: not mounted" and the turn runs as
// nobody — silently, because the caller treats identity as best-effort. Custody
// is the reason this stays here: the link lives in KMS under this subsystem, and
// only the answer crosses.
func serveIdentity() {
	cloud.Plane().Post("/integrations/chat-identity", planeChatIdentity,
		zip.WithOperationID(client.ChatIdentity),
		zip.WithSummary("Resolve the Hanzo account a chat user has linked"))
}

// planeChatIdentity answers WHO a chat turn runs as, and never with what.
//
// The org rides the request rather than the caller's plane identity, for the
// same reason AgentsRunOnBehalf does: the tenant is the one that connected the
// workspace, which the adapter resolved from a signed id, and the calling
// plugin's own identity is not it. No token is returned under any branch.
func planeChatIdentity(ctx context.Context, in *client.ChatIdentityIn) (*client.ChatIdentityOut, error) {
	s := mounted
	if s == nil || in == nil {
		return &client.ChatIdentityOut{Say: linkUnreadable}, nil
	}
	link, say, ephemeral := chatIdentity(s, in.Org, in.Provider, in.ExternalID, in.User)
	return &client.ChatIdentityOut{Subject: link.Subject, Model: link.Model, Say: say, Ephemeral: ephemeral}, nil
}

// serveSend publishes the outbound send on the plane.
//
// The Send* helpers above are Go calls, and every one of them ends at TokenFor,
// which is gated on this package's `mounted` global. transport runs in a DIFFERENT
// PROCESS, so holding one as a function value there answers "integrations: not
// mounted" and posts no reply — a turn that runs perfectly and then speaks into
// nothing.
//
// The send stays here because the per-org bot token IS the tenancy gate: an org
// that never connected Slack cannot post, and that property only holds where the
// token is. Only the intent crosses.
func serveSend() {
	cloud.Plane().Post("/integrations/chat-send", planeChatSend,
		zip.WithOperationID(client.ChatSend),
		zip.WithSummary("Post one message back to a chat platform as the org"))
}

// serveHistory publishes the platform transcript read on the plane.
//
// Like serveSend, token custody stays in this process: transport runs in its
// own process and its local inbox only contains messages previously addressed
// to @hanzo. Exposing the Slack history/thread read over the plane lets every
// @hanzo mention and thread reply see the full preceding channel or thread
// conversation (including human discussion, bot alerts, attachments, and files)
// without leaking any bot token out of provider.
func serveHistory() {
	cloud.Plane().Post("/integrations/chat-history", planeChatHistory,
		zip.WithOperationID(client.ChatHistory),
		zip.WithSummary("Read recent channel or thread turns from a chat platform as the org"))
}

// planeChatHistory reads recent channel or thread messages from the platform
// using the org's custodied bot token.
func planeChatHistory(ctx context.Context, in *client.ChatHistoryIn) (*client.ChatHistoryOut, error) {
	s := mounted
	if s == nil {
		return nil, fmt.Errorf("integrations: not mounted")
	}
	if in == nil || strings.TrimSpace(in.Org) == "" || strings.TrimSpace(in.Channel) == "" {
		return &client.ChatHistoryOut{}, nil
	}
	switch in.Provider {
	case "slack":
		turns, err := slackTurns(ctx, in.Org, Inbound{
			Provider:   "slack",
			ExternalID: in.ExternalID,
			Channel:    in.Channel,
			ThreadID:   in.ThreadID,
			MessageID:  in.MessageID,
			Text:       in.Text,
		})
		if err != nil {
			s.Log.Warn("integrations: chat history", "provider", in.Provider, "org", in.Org, "channel", in.Channel, "thread", in.ThreadID, "err", err)
			return nil, err
		}
		return &client.ChatHistoryOut{Turns: turns}, nil
	default:
		return &client.ChatHistoryOut{}, nil
	}
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
// transport to notice. Custody is what the org buys: TokenFor refuses an org that
// names no store and telegram's chat bind can never match it, so a caller that
// omitted it reached a per-transport error message on two transports and spent a
// shared app credential unchecked on two others. One refusal, named once, and a
// dropped org is loud where it was silent.
func planeChatSend(ctx context.Context, in *client.ChatSendIn) (*client.ChatSendOut, error) {
	if mounted == nil {
		return nil, fmt.Errorf("integrations: not mounted")
	}
	if in == nil || strings.TrimSpace(in.Text) == "" {
		return nil, fmt.Errorf("integrations: chat send needs text")
	}
	if strings.TrimSpace(in.Org) == "" {
		return nil, fmt.Errorf("integrations: chat send needs the org it sends as")
	}
	if in.Edit != "" && in.Provider != "slack" {
		return nil, fmt.Errorf("integrations: %s messages cannot be edited here", in.Provider)
	}
	if in.Edit != "" && in.Private {
		return nil, fmt.Errorf("integrations: a private reply cannot be edited")
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
			return &client.ChatSendOut{}, slackPostEphemeral(ctx, string(tok), in.Channel, in.User, in.ReplyTo, in.Text)
		}
		id, err := SendSlackAt(ctx, in.Org, in.Channel, in.ReplyTo, in.Edit, in.Text, in.Controls, in.Question)
		if err == nil && in.Edit == "" {
			// A thread the agent answers in is one it follows: a reply there reaches
			// it without another @mention (slackRoute.addressed). The thread is the
			// one it answered in, or the one its own message roots.
			slackFollow(ctx, in.Org, in.Channel, cmp.Or(in.ReplyTo, id))
		}
		return &client.ChatSendOut{MessageID: id}, err
	case "whatsapp":
		// A direct message is one person. A group is recipient_type group, and
		// Channel is the group id. The flag is the only thing that tells those
		// apart: both ids are opaque strings.
		id, err := SendWhatsApp(ctx, in.Org, in.Channel, in.ReplyTo, in.Text, in.Group)
		return &client.ChatSendOut{MessageID: id}, err
	case "discord":
		if in.Private {
			return nil, fmt.Errorf("integrations: discord has no private reply here")
		}
		id, err := SendDiscord(ctx, in.Channel, in.ReplyTo, in.Text)
		return &client.ChatSendOut{MessageID: id}, err
	case "teams":
		return &client.ChatSendOut{}, SendTeams(ctx, in.Root, in.Channel, in.Text)
	case "github":
		// The room is "owner/repo#N"; the reply is an issue comment posted with
		// the installation's own token, so it appears as the App.
		id, err := githubIssueComment(ctx, in.Org, in.Channel, in.Text)
		return &client.ChatSendOut{MessageID: id}, err
	case "linear":
		// The room is the issue id; the reply is a comment posted with the key of
		// the person who bound the organization (linearClaim), so it carries a name.
		id, err := linearIssueComment(ctx, mounted, in.Org, in.Channel, in.Text)
		return &client.ChatSendOut{MessageID: id}, err
	case "telegram":
		// THE ISOLATION ROOT for telegram, and it has to be asked here. There is
		// ONE global bot token, so the chat→org bind is the only thing standing
		// between an org and a chat it never onboarded. channels used to ask
		// OrgForExternalID itself — in-process, from another process, so it always
		// answered "not bound" and every telegram send failed closed. Failing
		// closed hid it; the check still has to be real.
		if boundOrg, ok := OrgForExternalID("telegram", in.Channel); !ok || boundOrg != in.Org {
			return nil, fmt.Errorf("integrations: telegram chat %q is not bound to %q", in.Channel, in.Org)
		}
		chat, err := strconv.ParseInt(in.Channel, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integrations: telegram chat id %q: %w", in.Channel, err)
		}
		var replyTo int64
		if in.ReplyTo != "" {
			replyTo, _ = strconv.ParseInt(in.ReplyTo, 10, 64)
		}
		return &client.ChatSendOut{}, SendTelegram(ctx, chat, replyTo, in.Text)
	}
	return nil, fmt.Errorf("integrations: no transport for %q", in.Provider)
}
