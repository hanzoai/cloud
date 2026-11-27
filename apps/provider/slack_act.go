package provider

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/zap-proto/zip"
)

// slack_act.go is what an agent can DO to a workspace once it can read one:
// search it, change a message it posted, take one back, and react.
//
// Reading (conversations.history/replies) and posting (chat.postMessage) live in
// slack_messages.go. These are typed for the same reason those are: an untyped
// route publishes a path and no schema, so the MCP tool list names an operation
// with nowhere to put its arguments and an agent is never told the workspace can
// be searched at all.
//
// EVERY ONE IS THE CALLER'S OWN WORKSPACE. slackCallerToken resolves the bot
// token custodied for the VALIDATED org, so none of these can reach a workspace
// the caller has not connected. A scope the install never granted comes back from
// Slack as missing_scope, and slackReadError names it rather than reporting a
// server fault — the honest answer to "reinstall to grant search".

// slackSearchIn asks a workspace a question.
type slackSearchIn struct {
	// Query is Slack's own search syntax — bare words, or in:#channel,
	// from:@someone, before:2026-09-01, has:link.
	Query string `json:"query"`
	// Count bounds the page, 1-100. Zero means 20.
	Count int `json:"count"`
	// Page is the 1-based page of results. Zero means the first.
	Page int `json:"page"`
}

// slackSearchHit is one matching message.
type slackSearchHit struct {
	// Channel is the conversation ID the match sits in.
	Channel string `json:"channel"`
	// ChannelName is that conversation's name, when Slack reports one.
	ChannelName string `json:"channel_name,omitempty"`
	// User is the author's Slack user ID.
	User string `json:"user,omitempty"`
	// Username is the author's display name, when Slack reports one.
	Username string `json:"username,omitempty"`
	// Text is the matching message's text.
	Text string `json:"text"`
	// TS is the message's exact Slack timestamp, the handle for replying to or
	// reacting to it.
	TS string `json:"ts"`
	// Permalink addresses the message in a browser.
	Permalink string `json:"permalink,omitempty"`
}

// slackSearchOut is a page of matches and how many there are in total.
type slackSearchOut struct {
	// Matches are this page's hits, most relevant first.
	Matches []slackSearchHit `json:"matches"`
	// Total is how many messages matched, across every page.
	Total int `json:"total"`
	// Page is the page these matches came from.
	Page int `json:"page"`
	// Pages is how many pages the query has in total.
	Pages int `json:"pages"`
}

// slackUpdateMessageIn rewrites a message this app posted.
type slackUpdateMessageIn struct {
	// Channel is a Slack channel ID, name (#hanzo-gtm), or channel mention.
	Channel string `json:"channel"`
	// TS is the exact timestamp of the message to rewrite.
	TS string `json:"ts"`
	// Text is the replacement, up to 40000 characters.
	Text string `json:"text"`
}

// slackUpdateMessageOut confirms which message changed.
type slackUpdateMessageOut struct {
	// Channel is the resolved channel ID the message sits in.
	Channel string `json:"channel"`
	// TS is the rewritten message's exact Slack timestamp, unchanged by an edit.
	TS string `json:"ts"`
}

// slackDeleteMessageIn takes back a message this app posted.
//
// ON THE QUERY STRING, because a DELETE carries no body here — zip does not read
// one for GET, HEAD or DELETE. These fields once had `json:` tags, which read as
// a body and bound anyway through the json-name fallback: a caller who sent the
// obvious body got "ts must be a Slack timestamp string" about a timestamp that
// had been discarded unread. The tags say what the wire is.
type slackDeleteMessageIn struct {
	// Channel is a Slack channel ID, name (#hanzo-gtm), or channel mention.
	Channel string `json:"-" url:"channel"`
	// TS is the exact timestamp of the message to take back.
	TS string `json:"-" url:"ts"`
}

// slackDeleteMessageOut confirms which message is gone.
type slackDeleteMessageOut struct {
	// Channel is the resolved channel ID the message was in.
	Channel string `json:"channel"`
	// TS is the deleted message's timestamp, which now addresses nothing.
	TS string `json:"ts"`
}

// slackReactIn adds an emoji reaction to a message.
type slackReactIn struct {
	// Channel is a Slack channel ID, name (#hanzo-gtm), or channel mention.
	Channel string `json:"channel"`
	// TS is the exact timestamp of the message to react to.
	TS string `json:"ts"`
	// Name is the emoji's name WITHOUT colons — "eyes", not ":eyes:".
	Name string `json:"name"`
}

// slackReactOut confirms the reaction landed.
type slackReactOut struct {
	// Channel is the resolved channel ID the message sits in.
	Channel string `json:"channel"`
	// TS is the reacted-to message's exact Slack timestamp.
	TS string `json:"ts"`
	// Name is the emoji that was added, without colons.
	Name string `json:"name"`
}

// slackSearch answers a workspace question: POST /v1/provider/slack/search.
//
// Slack's search.messages wants a USER token for a person's own view of a
// workspace; a bot token searches what the app can see. Whichever the install
// granted, the token custodied for this org is the one sent.
func (o ops) slackSearch(ctx context.Context, in *slackSearchIn) (*slackSearchOut, error) {
	tok, err := slackCallerToken(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Query)
	if q == "" || len([]rune(q)) > 1000 {
		return nil, zip.ErrBadRequest("query must contain 1-1000 characters")
	}
	count := in.Count
	if count == 0 {
		count = 20
	}
	if count < 1 || count > 100 {
		return nil, zip.ErrBadRequest("count must be between 1 and 100")
	}
	page := in.Page
	if page == 0 {
		page = 1
	}
	if page < 1 || page > 100 {
		return nil, zip.ErrBadRequest("page must be between 1 and 100")
	}
	var r struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Needed   string `json:"needed"`
		Messages struct {
			Total      int `json:"total"`
			Pagination struct {
				Page      int `json:"page"`
				PageCount int `json:"page_count"`
			} `json:"pagination"`
			Matches []struct {
				Text      string `json:"text"`
				TS        string `json:"ts"`
				User      string `json:"user"`
				Username  string `json:"username"`
				Permalink string `json:"permalink"`
				Channel   struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"channel"`
			} `json:"matches"`
		} `json:"messages"`
	}
	form := url.Values{
		"query": {q},
		"count": {strconv.Itoa(count)},
		"page":  {strconv.Itoa(page)},
	}
	if err := slackPostForm(ctx, tok, slackWebAPIBase+"/search.messages", form, &r); err != nil {
		return nil, fmt.Errorf("slack search: %w", err)
	}
	if !r.OK {
		return nil, slackReadError("search.messages", r.Error, r.Needed)
	}
	out := &slackSearchOut{
		Matches: make([]slackSearchHit, 0, len(r.Messages.Matches)),
		Total:   r.Messages.Total,
		Page:    r.Messages.Pagination.Page,
		Pages:   r.Messages.Pagination.PageCount,
	}
	for _, m := range r.Messages.Matches {
		out.Matches = append(out.Matches, slackSearchHit{
			Channel:     m.Channel.ID,
			ChannelName: m.Channel.Name,
			User:        m.User,
			Username:    m.Username,
			Text:        m.Text,
			TS:          m.TS,
			Permalink:   m.Permalink,
		})
	}
	return out, nil
}

// slackUpdateMessage rewrites one of this app's messages:
// PUT /v1/provider/slack/messages. Slack refuses a message the token did not
// post, and that refusal is reported as it arrives.
func (o ops) slackUpdateMessage(ctx context.Context, in *slackUpdateMessageIn) (*slackUpdateMessageOut, error) {
	tok, channel, err := slackTarget(ctx, in.Channel, in.TS)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Text) == "" || len([]rune(in.Text)) > 40000 {
		return nil, zip.ErrBadRequest("text must contain 1-40000 characters")
	}
	var r struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Needed  string `json:"needed"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	form := url.Values{"channel": {channel}, "ts": {in.TS}, "text": {in.Text}}
	if err := slackPostForm(ctx, tok, slackWebAPIBase+"/chat.update", form, &r); err != nil {
		return nil, fmt.Errorf("slack update message: %w", err)
	}
	if !r.OK {
		return nil, slackReadError("chat.update", r.Error, r.Needed)
	}
	return &slackUpdateMessageOut{Channel: r.Channel, TS: r.TS}, nil
}

// slackDeleteMessage takes back one of this app's messages:
// DELETE /v1/provider/slack/messages.
func (o ops) slackDeleteMessage(ctx context.Context, in *slackDeleteMessageIn) (*slackDeleteMessageOut, error) {
	tok, channel, err := slackTarget(ctx, in.Channel, in.TS)
	if err != nil {
		return nil, err
	}
	var r struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Needed  string `json:"needed"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	form := url.Values{"channel": {channel}, "ts": {in.TS}}
	if err := slackPostForm(ctx, tok, slackWebAPIBase+"/chat.delete", form, &r); err != nil {
		return nil, fmt.Errorf("slack delete message: %w", err)
	}
	if !r.OK {
		return nil, slackReadError("chat.delete", r.Error, r.Needed)
	}
	return &slackDeleteMessageOut{Channel: r.Channel, TS: r.TS}, nil
}

// slackReact adds an emoji reaction: POST /v1/provider/slack/reactions.
func (o ops) slackReact(ctx context.Context, in *slackReactIn) (*slackReactOut, error) {
	tok, channel, err := slackTarget(ctx, in.Channel, in.TS)
	if err != nil {
		return nil, err
	}
	// Slack names an emoji without colons, and accepts the skin-tone suffix.
	name := strings.Trim(strings.TrimSpace(in.Name), ":")
	if name == "" || len(name) > 100 {
		return nil, zip.ErrBadRequest("name must be an emoji name without colons")
	}
	var r struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Needed string `json:"needed"`
	}
	form := url.Values{"channel": {channel}, "timestamp": {in.TS}, "name": {name}}
	if err := slackPostForm(ctx, tok, slackWebAPIBase+"/reactions.add", form, &r); err != nil {
		return nil, fmt.Errorf("slack react: %w", err)
	}
	if !r.OK {
		return nil, slackReadError("reactions.add", r.Error, r.Needed)
	}
	return &slackReactOut{Channel: channel, TS: in.TS, Name: name}, nil
}

// slackTarget is the three things every act-on-a-message op needs: the org's
// token, the resolved conversation, and a timestamp that is actually one. One
// function, so update, delete and react cannot disagree about what a valid
// target is.
func slackTarget(ctx context.Context, rawChannel, ts string) (token, channel string, err error) {
	token, err = slackCallerToken(ctx)
	if err != nil {
		return "", "", err
	}
	if !slackTimestamp.MatchString(ts) {
		return "", "", zip.ErrBadRequest("ts must be a Slack timestamp string")
	}
	channel, err = slackResolveConversation(ctx, token, rawChannel)
	if err != nil {
		return "", "", err
	}
	return token, channel, nil
}
