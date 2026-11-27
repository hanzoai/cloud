package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
)

// slack_turns.go is the Slack adapter's READ edge: the conversation a message
// arrived in, taken from Slack itself.
//
// It exists because the assistant had no memory. Every @mention was answered in
// isolation — asked "weather in Benicia" and then "try again", it replied "what
// would you like me to help you with", because from where it sat that second
// message was the first thing anyone had ever said to it. Even worse, in a channel
// or existing thread where teammates discussed a bug, pasted a stack trace, or an
// alert bot posted before @hanzo was mentioned, none of those messages ever
// entered transport's inbox because they were not addressed to the bot.
//
// SLACK IS ASKED, rather than only replaying transport's inbox, because Slack
// holds the full ground-truth transcript: human discussion prior to @hanzo,
// alert/bot attachments and blocks, uploaded file metadata, and Hanzo's own
// replies in exact chronological order.
//
// It introduces no second authentication: the org's own custodied bot token, the
// same one the reply is posted with (TokenFor), on the same HTTP path every other
// Slack call takes.

const (
	// slackThreadKeep bounds how many messages from a thread are kept (preserving
	// the thread root plus the most recent replies).
	slackThreadKeep = 35
	// slackChannelKeep bounds how many recent messages from a top-level channel
	// or DM are kept.
	slackChannelKeep = 20
	// slackSurroundingKeep bounds how many channel messages immediately before a
	// thread root are included as lead-in context when @hanzo is invoked in a
	// non-DM thread.
	slackSurroundingKeep = 5
	// slackSurroundingMaxAge skips channel messages older than 2 hours before
	// the thread root so unrelated historical chatter does not pollute a thread.
	slackSurroundingMaxAge = 2 * 3600
	// slackTurnPage is the page asked of Slack. conversations.replies answers
	// OLDEST first, so a thread longer than one page is bounded here while
	// staying comfortably inside slackMaxBody.
	slackTurnPage = 100
	// slackUserCacheTTL caches resolved Slack display names per workspace/user.
	slackUserCacheTTL = time.Hour
	// slackUserLookupCap bounds uncached users.info calls per transcript fetch
	// so a busy channel cannot slow down a reply.
	slackUserLookupCap = 10
	// slackUserLookupTimeout bounds a single best-effort users.info call.
	slackUserLookupTimeout = 2 * time.Second
	// slackFilePreviewCap bounds an inline file preview snippet.
	slackFilePreviewCap = 400
)

type slackMessageAttachment struct {
	Pretext    string `json:"pretext"`
	AuthorName string `json:"author_name"`
	Title      string `json:"title"`
	TitleLink  string `json:"title_link"`
	Text       string `json:"text"`
	Fallback   string `json:"fallback"`
	Fields     []struct {
		Title string `json:"title"`
		Value string `json:"value"`
	} `json:"fields"`
}

type slackMessageBlock struct {
	Type    string `json:"type"`
	AltText string `json:"alt_text"`
	Text    *struct {
		Text string `json:"text"`
	} `json:"text"`
	Title *struct {
		Text string `json:"text"`
	} `json:"title"`
	Fields []struct {
		Text string `json:"text"`
	} `json:"fields"`
	Elements []json.RawMessage `json:"elements"`
}

type slackHistoryFile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Mimetype  string `json:"mimetype"`
	Permalink string `json:"permalink"`
	Preview   string `json:"preview"`
}

type slackHistoryMessage struct {
	User       string `json:"user"`
	BotID      string `json:"bot_id"`
	Username   string `json:"username"`
	BotProfile struct {
		Name string `json:"name"`
	} `json:"bot_profile"`
	Text        string                   `json:"text"`
	TS          string                   `json:"ts"`
	ThreadTS    string                   `json:"thread_ts"`
	Subtype     string                   `json:"subtype"`
	Attachments []slackMessageAttachment `json:"attachments"`
	Blocks      []slackMessageBlock      `json:"blocks"`
	Files       []slackHistoryFile       `json:"files"`
}

type slackHistoryResponse struct {
	OK       bool                  `json:"ok"`
	Error    string                `json:"error"`
	Needed   string                `json:"needed"`
	Messages []slackHistoryMessage `json:"messages"`
}

type slackUserSeen struct {
	name string
	at   time.Time
}

var slackUsers sync.Map

// slackTurns is the conversation `in` arrived in, oldest first and WITHOUT the
// message being answered.
//
// A thread reads as a thread (conversations.replies under its root, plus a small
// best-effort window of preceding channel messages for non-DM threads) and
// anything else reads as the room's recent messages (conversations.history) —
// the right answer for a top-level channel @mention, a DM, and a slash command.
func slackTurns(ctx context.Context, org string, in Inbound) ([]client.Turn, error) {
	if strings.TrimSpace(in.Channel) == "" {
		return nil, nil // nowhere to read from; a first message is a real answer
	}
	tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if err != nil {
		return nil, err
	}
	return slackReadTurns(ctx, string(tok), org, in)
}

// slackReadTurns is the read itself, given the token. Custody is above and the
// conversation is here for the same reason the send path splits that way
// (SendSlackAt / slackPostThreadTS): what a transcript IS can then be stated
// against a Slack stub, without a key store standing behind it.
func slackReadTurns(ctx context.Context, botToken, org string, in Inbound) ([]client.Turn, error) {
	self := ""
	if conn, ok := ConnectionFor(org, "slack", ""); ok {
		self = conn.BotUserID
	}
	lookups := 0
	threadTS := strings.TrimSpace(in.ThreadID)
	msgID := strings.TrimSpace(in.MessageID)
	msgTS, hasMsgTS := parseSlackTS(msgID)

	if threadTS != "" {
		form := url.Values{
			"channel": {in.Channel},
			"ts":      {threadTS},
			"limit":   {strconv.Itoa(slackTurnPage)},
		}
		var r slackHistoryResponse
		if err := slackPostForm(ctx, botToken, slackWebAPIBase+"/conversations.replies", form, &r); err != nil {
			return nil, err
		}
		if !r.OK {
			return nil, slackHistoryError("conversations.replies", r.Error, r.Needed)
		}
		seenTS := make(map[string]bool, len(r.Messages))
		turns := make([]client.Turn, 0, len(r.Messages))
		for _, m := range r.Messages {
			if m.TS != "" {
				seenTS[m.TS] = true
			}
			if !slackKeepSubtype(m.Subtype) {
				continue
			}
			if msgID != "" && m.TS == msgID {
				continue
			}
			text := slackFormatMessageText(stripLeadingMention(m.Text), m.Attachments, m.Blocks, m.Files)
			if text == "" {
				continue
			}
			sender, isSelf := slackSender(ctx, botToken, org, m, self, &lookups)
			at, _ := parseSlackTS(m.TS)
			turns = append(turns, client.Turn{
				Sender: sender,
				Self:   isSelf,
				Text:   text,
				At:     int64(at),
			})
		}
		if msgID == "" {
			turns = dropTrailingDuplicate(turns, in.Text)
		}
		// Keep the thread root (original bug report / alert / question) plus the
		// newest replies when a thread exceeds slackThreadKeep.
		if len(turns) > slackThreadKeep {
			kept := make([]client.Turn, 0, slackThreadKeep)
			kept = append(kept, turns[0])
			kept = append(kept, turns[len(turns)-(slackThreadKeep-1):]...)
			turns = kept
		}
		// Best-effort: include a few immediately preceding channel messages before
		// the thread root so @hanzo sees channel context that led into the thread.
		if !strings.HasPrefix(in.Channel, "D") {
			if pre := slackSurroundingTurns(ctx, botToken, org, in.Channel, threadTS, self, seenTS, &lookups); len(pre) > 0 {
				turns = append(pre, turns...)
			}
		}
		return turns, nil
	}

	form := url.Values{
		"channel": {in.Channel},
		"limit":   {strconv.Itoa(slackTurnPage)},
	}
	var r slackHistoryResponse
	if err := slackPostForm(ctx, botToken, slackWebAPIBase+"/conversations.history", form, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, slackHistoryError("conversations.history", r.Error, r.Needed)
	}
	turns := make([]client.Turn, 0, len(r.Messages))
	for _, m := range r.Messages {
		if !slackKeepSubtype(m.Subtype) {
			continue
		}
		if msgID != "" {
			if m.TS == msgID {
				continue
			}
			if hasMsgTS {
				if ts, ok := parseSlackTS(m.TS); ok && ts > msgTS {
					continue
				}
			}
		}
		text := slackFormatMessageText(stripLeadingMention(m.Text), m.Attachments, m.Blocks, m.Files)
		if text == "" {
			continue
		}
		sender, isSelf := slackSender(ctx, botToken, org, m, self, &lookups)
		at, _ := parseSlackTS(m.TS)
		turns = append(turns, client.Turn{
			Sender: sender,
			Self:   isSelf,
			Text:   text,
			At:     int64(at),
		})
	}
	// conversations.history answers newest first; reverse to chronological order.
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	if msgID == "" {
		turns = dropTrailingDuplicate(turns, in.Text)
	}
	if len(turns) > slackChannelKeep {
		turns = turns[len(turns)-slackChannelKeep:]
	}
	return turns, nil
}

// slackSurroundingTurns fetches up to slackSurroundingKeep channel messages
// strictly preceding threadTS within slackSurroundingMaxAge. Best-effort: any
// API or permission error returns nil without failing the thread read.
func slackSurroundingTurns(ctx context.Context, botToken, org, channel, threadTS, self string, seenTS map[string]bool, lookups *int) []client.Turn {
	rootTS, ok := parseSlackTS(threadTS)
	if !ok || rootTS <= 0 {
		return nil
	}
	form := url.Values{
		"channel":   {channel},
		"latest":    {threadTS},
		"inclusive": {"false"},
		"limit":     {strconv.Itoa(slackSurroundingKeep)},
	}
	var r slackHistoryResponse
	if err := slackPostForm(ctx, botToken, slackWebAPIBase+"/conversations.history", form, &r); err != nil || !r.OK {
		return nil
	}
	out := make([]client.Turn, 0, len(r.Messages))
	for _, m := range r.Messages {
		if m.TS == threadTS || seenTS[m.TS] || !slackKeepSubtype(m.Subtype) {
			continue
		}
		ts, ok := parseSlackTS(m.TS)
		if !ok || ts >= rootTS || rootTS-ts > slackSurroundingMaxAge {
			continue
		}
		text := slackFormatMessageText(stripLeadingMention(m.Text), m.Attachments, m.Blocks, m.Files)
		if text == "" {
			continue
		}
		sender, isSelf := slackSender(ctx, botToken, org, m, self, lookups)
		out = append(out, client.Turn{
			Sender: sender,
			Self:   isSelf,
			Text:   "[channel context before thread] " + text,
			At:     int64(ts),
		})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func slackHistoryError(method, code, needed string) error {
	code = cmp.Or(strings.TrimSpace(code), "unknown")
	if code == "missing_scope" && strings.TrimSpace(needed) != "" {
		return fmt.Errorf("slack %s: %s (needed: %s; update Slack app scopes and reconnect)", method, code, strings.TrimSpace(needed))
	}
	return fmt.Errorf("slack %s: %s", method, code)
}

func slackKeepSubtype(subtype string) bool {
	switch subtype {
	case "", "bot_message", "file_share", "thread_broadcast", "me_message":
		return true
	default:
		return false
	}
}

func parseSlackTS(ts string) (float64, bool) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func dropTrailingDuplicate(turns []client.Turn, currentText string) []client.Turn {
	trimmed := strings.TrimSpace(currentText)
	if trimmed == "" {
		return turns
	}
	if n := len(turns); n > 0 && !turns[n-1].Self && strings.TrimSpace(turns[n-1].Text) == trimmed {
		return turns[:n-1]
	}
	return turns
}

func slackSender(ctx context.Context, botToken, org string, m slackHistoryMessage, self string, lookups *int) (string, bool) {
	botName := cmp.Or(strings.TrimSpace(m.BotProfile.Name), strings.TrimSpace(m.Username))
	isSelf := false
	if self != "" {
		isSelf = m.User == self
	} else if m.BotID != "" {
		isSelf = botName == "" || strings.EqualFold(botName, "hanzo")
	}
	if isSelf {
		return cmp.Or(m.User, "hanzo"), true
	}
	if m.User != "" {
		return slackResolveUserName(ctx, botToken, org, m.User, lookups), false
	}
	if botName != "" {
		return botName, false
	}
	return strings.TrimSpace(m.BotID), false
}

func slackResolveUserName(ctx context.Context, botToken, org, userID string, lookups *int) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	key := slackWebAPIBase + ":" + org + ":" + userID
	if v, ok := slackUsers.Load(key); ok {
		if seen := v.(slackUserSeen); time.Since(seen.at) < slackUserCacheTTL {
			return cmp.Or(seen.name, userID)
		}
	}
	if lookups != nil && *lookups >= slackUserLookupCap {
		return userID
	}
	if lookups != nil {
		*lookups++
	}
	uctx, cancel := context.WithTimeout(ctx, slackUserLookupTimeout)
	defer cancel()
	var r struct {
		OK   bool `json:"ok"`
		User struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	name := ""
	if err := slackPostForm(uctx, botToken, slackWebAPIBase+"/users.info", url.Values{"user": {userID}}, &r); err == nil && r.OK && r.User.ID != "" {
		name = cmp.Or(
			strings.TrimSpace(r.User.Profile.DisplayName),
			strings.TrimSpace(r.User.Profile.RealName),
			strings.TrimSpace(r.User.RealName),
			strings.TrimSpace(r.User.Name),
		)
	}
	slackUsers.Store(key, slackUserSeen{name: name, at: time.Now()})
	return cmp.Or(name, userID)
}

// slackFormatMessageText combines a Slack message's primary text with any
// non-duplicate Block Kit text, attachments (alerts, unfurls, forwarded posts),
// and attached file metadata so the agent sees full context.
func slackFormatMessageText(baseText string, attachments []slackMessageAttachment, blocks []slackMessageBlock, files []slackHistoryFile) string {
	var parts []string
	base := strings.TrimSpace(baseText)
	if base != "" {
		parts = append(parts, base)
	}
	if bt := slackExtractBlocksText(blocks); bt != "" && !strings.Contains(base, bt) {
		parts = append(parts, bt)
	}
	for _, a := range attachments {
		if at := slackFormatAttachment(a); at != "" && !strings.Contains(base, at) {
			parts = append(parts, at)
		}
	}
	for _, f := range files {
		if ft := slackFormatFile(f); ft != "" {
			parts = append(parts, ft)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func slackFormatAttachment(a slackMessageAttachment) string {
	var lines []string
	if p := strings.TrimSpace(a.Pretext); p != "" {
		lines = append(lines, p)
	}
	if author := strings.TrimSpace(a.AuthorName); author != "" {
		lines = append(lines, "Author: "+author)
	}
	if title := strings.TrimSpace(a.Title); title != "" {
		if link := strings.TrimSpace(a.TitleLink); link != "" {
			lines = append(lines, title+" ("+link+")")
		} else {
			lines = append(lines, title)
		}
	}
	if body := strings.TrimSpace(a.Text); body != "" {
		lines = append(lines, body)
	}
	for _, f := range a.Fields {
		k, v := strings.TrimSpace(f.Title), strings.TrimSpace(f.Value)
		switch {
		case k != "" && v != "":
			lines = append(lines, k+": "+v)
		case v != "":
			lines = append(lines, v)
		case k != "":
			lines = append(lines, k)
		}
	}
	if len(lines) == 0 {
		if fb := strings.TrimSpace(a.Fallback); fb != "" {
			lines = append(lines, fb)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "[Attachment] " + strings.Join(lines, "\n")
}

func slackExtractBlocksText(blocks []slackMessageBlock) string {
	var lines []string
	for _, b := range blocks {
		if b.Title != nil {
			if t := strings.TrimSpace(b.Title.Text); t != "" {
				lines = append(lines, t)
			}
		}
		if b.Text != nil {
			if t := strings.TrimSpace(b.Text.Text); t != "" {
				lines = append(lines, t)
			}
		}
		for _, f := range b.Fields {
			if t := strings.TrimSpace(f.Text); t != "" {
				lines = append(lines, t)
			}
		}
		for _, raw := range b.Elements {
			if t := slackExtractElementText(raw); t != "" {
				lines = append(lines, t)
			}
		}
		if b.Type == "image" {
			if alt := strings.TrimSpace(b.AltText); alt != "" {
				lines = append(lines, "[Image: "+alt+"]")
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func slackExtractElementText(raw json.RawMessage) string {
	var el struct {
		Type      string            `json:"type"`
		Text      string            `json:"text"`
		URL       string            `json:"url"`
		UserID    string            `json:"user_id"`
		ChannelID string            `json:"channel_id"`
		AltText   string            `json:"alt_text"`
		Elements  []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(raw, &el); err != nil {
		return ""
	}
	if len(el.Elements) > 0 {
		var nested []string
		for _, child := range el.Elements {
			if t := slackExtractElementText(child); t != "" {
				nested = append(nested, t)
			}
		}
		if el.Type == "rich_text_section" || el.Type == "rich_text_preformatted" || el.Type == "rich_text_quote" {
			return strings.TrimSpace(strings.Join(nested, ""))
		}
		return strings.TrimSpace(strings.Join(nested, "\n"))
	}
	switch el.Type {
	case "link":
		if el.Text != "" && el.URL != "" && el.Text != el.URL {
			return el.Text + " (" + el.URL + ")"
		}
		return cmp.Or(el.Text, el.URL)
	case "user":
		if el.UserID != "" {
			return "<@" + el.UserID + ">"
		}
	case "channel":
		if el.ChannelID != "" {
			return "<#" + el.ChannelID + ">"
		}
	case "image":
		if el.AltText != "" {
			return "[Image: " + el.AltText + "]"
		}
	}
	return el.Text
}

func slackFormatFile(f slackHistoryFile) string {
	label := cmp.Or(strings.TrimSpace(f.Title), strings.TrimSpace(f.Name), strings.TrimSpace(f.ID))
	if label == "" {
		return ""
	}
	var meta []string
	if mt := strings.TrimSpace(f.Mimetype); mt != "" {
		meta = append(meta, mt)
	}
	if id := strings.TrimSpace(f.ID); id != "" && id != label {
		meta = append(meta, "id="+id)
	}
	line := "[File: " + label
	if len(meta) > 0 {
		line += " (" + strings.Join(meta, ", ") + ")"
	}
	line += "]"
	if link := strings.TrimSpace(f.Permalink); link != "" {
		line += " " + link
	}
	if prev := strings.TrimSpace(f.Preview); prev != "" {
		if len(prev) > slackFilePreviewCap {
			prev = prev[:slackFilePreviewCap] + "…"
		}
		line += "\n" + prev
	}
	return line
}
