package integrations

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/client"
)

// slack_turns.go is the Slack adapter's READ edge: the conversation a message
// arrived in, taken from Slack itself.
//
// It exists because the assistant had no memory. Every @mention was answered in
// isolation — asked "weather in Benicia" and then "try again", it replied "what
// would you like me to help you with", because from where it sat that second
// message was the first thing anyone had ever said to it.
//
// SLACK IS ASKED, rather than a stored copy replayed, for the reason that decides
// it: the transcript has to contain the assistant's OWN replies, and our inbox
// structurally cannot hold them — ingest records what ARRIVES, and an answer
// leaves. An agent shown only the questions reads its own words as the user's.
// Slack already holds both halves, in order, per thread, and it is what the
// person is looking at, so it is the conversation by definition.
//
// It introduces no second authentication: the org's own custodied bot token, the
// same one the reply is posted with (TokenFor), on the same HTTP path every other
// Slack call takes.

const (
	// slackTurnKeep is how many turns the model is shown. A history is a prompt,
	// and a prompt costs money every turn, so it is bounded here — enough to
	// follow an exchange, small enough that a channel with ten thousand messages
	// in it cannot make one answer expensive. It matches the inbox's own limit.
	slackTurnKeep = 20
	// slackTurnPage is the page asked of Slack. conversations.replies answers
	// OLDEST first, so a thread longer than one page has to be read whole and
	// tailed here; the page is what bounds that. Comfortably inside slackMaxBody.
	slackTurnPage = 100
)

// slackTurns is the conversation `in` arrived in, oldest first and WITHOUT the
// message being answered.
//
// A thread reads as a thread (conversations.replies under its root) and anything
// else reads as the room's recent messages (conversations.history) — the right
// answer for a DM, where Slack's assistant surface deliberately does not thread,
// and for a slash command, which has no thread to sit in.
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
	method, form := "/conversations.history", url.Values{
		"channel": {in.Channel},
		"limit":   {strconv.Itoa(slackTurnPage)},
	}
	oldestFirst := false
	if ts := strings.TrimSpace(in.ThreadID); ts != "" {
		method, oldestFirst = "/conversations.replies", true
		form.Set("ts", ts)
	}
	var r struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Messages []struct {
			User    string `json:"user"`
			BotID   string `json:"bot_id"`
			Text    string `json:"text"`
			TS      string `json:"ts"`
			Subtype string `json:"subtype"`
		} `json:"messages"`
	}
	if err := slackPostForm(ctx, botToken, slackWebAPIBase+method, form, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		// Named, because the one that matters is actionable: an install that
		// predates the history scopes answers `missing_scope`, and the fix is a
		// re-install rather than a code change — Slack does not grant new scopes to
		// a token it has already issued.
		return nil, fmt.Errorf("slack %s: %s", strings.TrimPrefix(method, "/"), cmp.Or(r.Error, "unknown"))
	}
	// Who we are in this workspace. Without it a bot message is only "some bot",
	// and the assistant would read another app's posts as its own words.
	self := ""
	if conn, ok := ConnectionFor(org, "slack", ""); ok {
		self = conn.BotUserID
	}
	turns := make([]client.Turn, 0, len(r.Messages))
	for _, m := range r.Messages {
		// A join, a leave, a pinned file: events with words in them that nobody
		// said. A bot_message is a real turn — usually ours.
		if m.Subtype != "" && m.Subtype != "bot_message" {
			continue
		}
		text := stripLeadingMention(m.Text)
		if text == "" {
			continue
		}
		at, _ := strconv.ParseFloat(m.TS, 64)
		turns = append(turns, client.Turn{
			Sender: m.User,
			Self:   (self != "" && m.User == self) || (self == "" && m.BotID != ""),
			Text:   text,
			At:     int64(at),
		})
	}
	if !oldestFirst { // conversations.history answers newest first
		for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
			turns[i], turns[j] = turns[j], turns[i]
		}
	}
	// The newest turn IS this message: it was posted to Slack before the event
	// reached us, so it is already in the transcript, and sending it again would
	// have the agent answer a question it appears to have been asked twice. It is
	// MATCHED rather than assumed, because a slash command never appears in the
	// room's history and dropping a turn blindly would eat somebody else's
	// sentence.
	if n := len(turns); n > 0 && !turns[n-1].Self && turns[n-1].Text == strings.TrimSpace(in.Text) {
		turns = turns[:n-1]
	}
	if len(turns) > slackTurnKeep {
		turns = turns[len(turns)-slackTurnKeep:]
	}
	return turns, nil
}
