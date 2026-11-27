package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// These operations live beside token custody so MCP calls work when the agent
// and provider run in separate processes. The org always comes from the caller.
type slackChannelsIn struct {
	// Types selects public_channel, private_channel, im or mpim, comma-separated.
	// Defaults to public_channel; each selected type requires its own read scope.
	Types string `json:"types"`
	// Cursor continues the previous page's next_cursor.
	Cursor string `json:"cursor"`
}

type slackConversation struct {
	// ID is the channel ID to pass to message reads and sends.
	ID string `json:"id"`
	// Name is the channel name, without #. DMs may have no name.
	Name string `json:"name"`
	// IsMember reports whether Hanzo has joined this channel.
	IsMember bool `json:"is_member"`
	// IsPrivate identifies a private channel or conversation.
	IsPrivate bool `json:"is_private"`
	// IsIM identifies a direct message.
	IsIM bool `json:"is_im"`
	// IsMPIM identifies a group direct message.
	IsMPIM bool `json:"is_mpim"`
}

type slackChannelsOut struct {
	// Channels contains one page of conversations visible to the bot.
	Channels []slackConversation `json:"channels"`
	// NextCursor is empty when there are no more pages.
	NextCursor string `json:"next_cursor"`
}

type slackMessagesIn struct {
	// Channel is a Slack channel ID, name (#hanzo-gtm), or Slack channel mention.
	Channel string `json:"channel"`
	// Limit bounds the page to 1-100 messages; zero defaults to 15.
	Limit int `json:"limit"`
	// Cursor continues the previous page's next_cursor.
	Cursor string `json:"cursor"`
	// ThreadTS reads a thread via conversations.replies. Empty reads recent
	// channel messages via conversations.history, which excludes thread replies.
	ThreadTS string `json:"thread_ts"`
	// Oldest filters to messages after this Slack timestamp (exclusive).
	Oldest string `json:"oldest"`
	// Latest filters to messages before this Slack timestamp (exclusive).
	Latest string `json:"latest"`
}

type slackMessage struct {
	// TS is Slack's exact message timestamp and ID; retain it as a string.
	TS string `json:"ts"`
	// User is the author's Slack user ID.
	User string `json:"user"`
	// BotID identifies a bot author when present.
	BotID string `json:"bot_id,omitempty"`
	// Text is the message in Slack's formatting.
	Text string `json:"text"`
	// Subtype distinguishes messages from channel events.
	Subtype string `json:"subtype,omitempty"`
	// ThreadTS is the parent timestamp for a threaded message.
	ThreadTS string `json:"thread_ts,omitempty"`
	// ReplyCount indicates whether a parent has replies to fetch separately.
	ReplyCount int `json:"reply_count,omitempty"`
	// Files are the files attached to this message. Read one's bytes with
	// GET /v1/provider/slack/file?id=<id>; nothing here is the content.
	Files []slackFile `json:"files,omitempty"`
}

// slackFile is one file attached to a message.
//
// The BYTES ARE NOT HERE, and the download URL is not either. Slack addresses a
// file's content with url_private_download, which is fetched with the
// workspace's bot token and is useless without it — publishing it would put a
// credentialed address in every message read, for a reader that cannot use it
// and a log that should not hold it. Read the content with
// GET /v1/provider/slack/file?id=<id>, which spends the org's own token
// server-side and streams what comes back.
type slackFile struct {
	// ID is the file's Slack ID and the handle the file read takes.
	ID string `json:"id"`
	// Name is the filename as uploaded.
	Name string `json:"name,omitempty"`
	// Title is the title Slack displays, which may differ from the filename.
	Title string `json:"title,omitempty"`
	// Mimetype is the type Slack recorded at upload. It is the uploader's word,
	// so it describes the file rather than deciding how the bytes are served.
	Mimetype string `json:"mimetype,omitempty"`
	// Size is the file's length in bytes.
	Size int64 `json:"size,omitempty"`
	// Permalink opens the file in Slack, for a person. It needs a Slack session
	// and is never the bytes.
	Permalink string `json:"permalink,omitempty"`
}

type slackMessagesOut struct {
	// Channel is the resolved channel ID, reusable for further reads and replies.
	Channel string `json:"channel"`
	// Messages is one page, newest first for history and oldest first for threads.
	Messages []slackMessage `json:"messages"`
	// HasMore indicates that Slack has additional messages beyond this page.
	HasMore bool `json:"has_more"`
	// NextCursor continues this query; never assume one page is the whole history.
	NextCursor string `json:"next_cursor"`
}

type slackSendMessageIn struct {
	// Channel is a Slack channel ID, name (#hanzo-gtm), or Slack channel mention.
	Channel string `json:"channel"`
	// Text is the message to post as Hanzo, up to 40000 characters.
	Text string `json:"text"`
	// ThreadTS optionally replies under this parent message's exact ts.
	ThreadTS string `json:"thread_ts"`
}

type slackSendMessageOut struct {
	// Channel is the resolved channel ID where the message was posted.
	Channel string `json:"channel"`
	// TS is the posted message's exact Slack timestamp.
	TS string `json:"ts"`
}

func slackCallerToken(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return "", zip.ErrBadRequest("slack is not connected for this org")
	}
	return string(tok), nil
}

// slackChannels lists Slack conversations for the caller's connected workspace.
// Read next_cursor until empty to discover channels, including private channels
// or group DMs when types requests them and the bot has their read scopes.
func (o ops) slackChannels(ctx context.Context, in *slackChannelsIn) (*slackChannelsOut, error) {
	tok, err := slackCallerToken(ctx)
	if err != nil {
		return nil, err
	}
	types := strings.TrimSpace(in.Types)
	if types == "" {
		types = "public_channel"
	}
	for kind := range strings.SplitSeq(types, ",") {
		switch kind {
		case "public_channel", "private_channel", "im", "mpim":
		default:
			return nil, zip.ErrBadRequest("types must select public_channel, private_channel, im or mpim")
		}
	}
	return slackListConversations(ctx, tok, types, in.Cursor)
}

func slackListConversations(ctx context.Context, tok, types, cursor string) (*slackChannelsOut, error) {
	var r struct {
		OK       bool                `json:"ok"`
		Error    string              `json:"error"`
		Needed   string              `json:"needed"`
		Channels []slackConversation `json:"channels"`
		Meta     struct {
			Cursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	err := slackPostForm(ctx, tok, slackWebAPIBase+"/conversations.list", url.Values{
		"types": {types}, "exclude_archived": {"true"},
		"limit": {strconv.Itoa(slackJoinPage)}, "cursor": {cursor},
	}, &r)
	if err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, slackReadError("conversations.list", r.Error, r.Needed)
	}
	if r.Channels == nil {
		r.Channels = []slackConversation{}
	}
	return &slackChannelsOut{Channels: r.Channels, NextCursor: strings.TrimSpace(r.Meta.Cursor)}, nil
}

var slackConversationID = regexp.MustCompile(`^[CGD][A-Z0-9]+$`)
var slackTimestamp = regexp.MustCompile(`^[0-9]+\.[0-9]{1,6}$`)

func slackResolveConversation(ctx context.Context, tok, raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if strings.HasPrefix(name, "<#") && strings.HasSuffix(name, ">") {
		name, _, _ = strings.Cut(strings.TrimSuffix(strings.TrimPrefix(name, "<#"), ">"), "|")
	}
	if slackConversationID.MatchString(name) {
		return name, nil
	}
	name = strings.TrimPrefix(name, "#")
	if name == "" {
		return "", zip.ErrBadRequest("channel is required")
	}
	// Public lookup first: reading a public channel must not require groups:read.
	for _, kind := range []string{"public_channel", "private_channel"} {
		cursor := ""
		for page := range slackJoinPages {
			out, err := slackListConversations(ctx, tok, kind, cursor)
			if err != nil {
				return "", err
			}
			for _, ch := range out.Channels {
				if ch.Name == name {
					return ch.ID, nil
				}
			}
			if out.NextCursor == "" {
				break
			}
			if out.NextCursor == cursor || page == slackJoinPages-1 {
				return "", zip.ErrBadRequest("channel lookup page limit reached; use the channel ID")
			}
			cursor = out.NextCursor
		}
	}
	return "", zip.ErrNotFound("Slack channel not found; invite Hanzo to the channel and verify its name or ID")
}

func slackReadError(method, code, needed string) error {
	if code == "" {
		code = "unknown"
	}
	hint := ""
	switch code {
	case "missing_scope":
		hint = "; add the required Bot Token Scopes and reconnect Slack in Hanzo"
		if needed != "" {
			hint += " (needed: " + needed + ")"
		}
	case "not_in_channel", "channel_not_found":
		hint = "; invite Hanzo to this conversation and verify the channel ID"
	}
	return zip.Errorf(http.StatusBadGateway, "slack %s: %s%s", method, code, hint)
}

// slackMessages reads recent messages from a named Slack channel such as
// #hanzo-gtm. The bot must be a member and have the relevant history scope.
// Set thread_ts to fetch a parent's replies, and cursor to continue a page.
// Channel history is newest first; thread replies are oldest first.
func (o ops) slackMessages(ctx context.Context, in *slackMessagesIn) (*slackMessagesOut, error) {
	tok, err := slackCallerToken(ctx)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 15
	}
	if limit < 1 || limit > 100 {
		return nil, zip.ErrBadRequest("limit must be between 1 and 100")
	}
	for _, ts := range []string{in.ThreadTS, in.Oldest, in.Latest} {
		if ts != "" && !slackTimestamp.MatchString(ts) {
			return nil, zip.ErrBadRequest("timestamps must be Slack ts strings, for example 1710000000.000001")
		}
	}
	channel, err := slackResolveConversation(ctx, tok, in.Channel)
	if err != nil {
		return nil, err
	}
	method := "conversations.history"
	form := url.Values{"channel": {channel}, "limit": {strconv.Itoa(limit)}}
	for key, value := range map[string]string{"cursor": in.Cursor, "oldest": in.Oldest, "latest": in.Latest} {
		if value != "" {
			form.Set(key, value)
		}
	}
	if in.ThreadTS != "" {
		method = "conversations.replies"
		form.Set("ts", in.ThreadTS)
	}
	var r struct {
		OK       bool           `json:"ok"`
		Error    string         `json:"error"`
		Needed   string         `json:"needed"`
		Messages []slackMessage `json:"messages"`
		HasMore  bool           `json:"has_more"`
		Meta     struct {
			Cursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := slackPostForm(ctx, tok, slackWebAPIBase+"/"+method, form, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, slackReadError(method, r.Error, r.Needed)
	}
	if r.Messages == nil {
		r.Messages = []slackMessage{}
	}
	return &slackMessagesOut{Channel: channel, Messages: r.Messages, HasMore: r.HasMore, NextCursor: strings.TrimSpace(r.Meta.Cursor)}, nil
}

// slackSendMessage posts as Hanzo to the caller's connected Slack workspace.
// Use channel from a message read and thread_ts to reply to a specific message.
// Requires chat:write; this operation does not join channels automatically.
func (o ops) slackSendMessage(ctx context.Context, in *slackSendMessageIn) (*slackSendMessageOut, error) {
	tok, err := slackCallerToken(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Text) == "" || len([]rune(in.Text)) > 40000 {
		return nil, zip.ErrBadRequest("text must contain 1-40000 characters")
	}
	if in.ThreadTS != "" && !slackTimestamp.MatchString(in.ThreadTS) {
		return nil, zip.ErrBadRequest("thread_ts must be a Slack timestamp string")
	}
	channel, err := slackResolveConversation(ctx, tok, in.Channel)
	if err != nil {
		return nil, err
	}
	ts, err := slackPostThreadTS(ctx, tok, channel, in.ThreadTS, in.Text)
	if err != nil {
		return nil, fmt.Errorf("slack send message: %w", err)
	}
	return &slackSendMessageOut{Channel: channel, TS: ts}, nil
}
