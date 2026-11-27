package provider

// slack_controls.go — a run's buttons, drawn in Block Kit, and a press of one.
//
// A run is shown as ONE message edited in place — a chat turn by transport, a
// coding run by apps/coding — and while it works that message carries the run's
// controls (client.Controls): Stop and Pause, then Continue and Stop once it is
// paused, and nothing once it is over. This file is where those become Slack's
// own buttons, on the same chat.postMessage and chat.update every other post
// takes, with the plain text beside them for notifications and screen readers.
//
// A press comes back on the interactivity URL, signed like every event. What it
// MEANS is not decided here: transport resolves who pressed from the Slack user,
// exactly as it does for "stop" said in the thread, and agents acts only on that
// person's own session. This file carries the press there and, when it is
// refused, tells the presser privately whose run it is. It never posts a second
// message into the conversation: the run's own message shows the result.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	transportpeer "github.com/hanzoai/cloud/client/transport"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/text"
	zip "github.com/zap-proto/zip"
)

// The action ids a run's buttons carry, and the command each one is.
const (
	actStop     = "hanzo_stop"
	actPause    = "hanzo_pause"
	actContinue = "hanzo_continue"
)

var runCommands = map[string]string{
	actStop:     client.SessionStop,
	actPause:    client.SessionPause,
	actContinue: client.SessionResume,
}

// slackSectionMax is the most text one section block holds. The plain `text`
// beside the blocks keeps the whole of it.
const slackSectionMax = 3000

// controlBlocks is a run's message as Block Kit: its text, and its buttons under
// it. Stop is the danger button and asks first, because it ends the run.
func controlBlocks(msg string, c client.Controls) []any {
	value := controlValue(c)
	stop := button(actStop, "Stop", "danger", value)
	kept := "Work so far is kept."
	if c.Kind == client.ControlsCoding {
		kept = "Work so far is kept on its branch."
	}
	stop["confirm"] = map[string]any{
		"title":   plainText("Stop this run?"),
		"text":    plainText(kept),
		"confirm": plainText("Stop"),
		"deny":    plainText("Keep running"),
		"style":   "danger",
	}
	buttons := []any{stop, button(actPause, "Pause", "", value)}
	if c.Paused {
		buttons = []any{button(actContinue, "Continue", "primary", value), stop}
	}
	if len(msg) > slackSectionMax {
		msg = text.Cut(msg, slackSectionMax-1) + "…"
	}
	return []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": msg}},
		map[string]any{"type": "actions", "block_id": "hanzo_run", "elements": buttons},
	}
}

func plainText(s string) map[string]any { return map[string]any{"type": "plain_text", "text": s} }

func button(id, label, style, value string) map[string]any {
	b := map[string]any{"type": "button", "action_id": id, "text": plainText(label), "value": value}
	if style != "" {
		b["style"] = style
	}
	return b
}

// controlValue is what a button carries back: which run, of what kind, whose.
// Slack returns it verbatim in the signed payload of a press.
func controlValue(c client.Controls) string {
	b, _ := json.Marshal(struct {
		S string `json:"s"`
		K string `json:"k"`
		O string `json:"o,omitempty"`
	}{c.Session, c.Kind, c.Owner})
	return string(b)
}

func parseControlValue(v string) (client.Controls, bool) {
	var p struct {
		S string `json:"s"`
		K string `json:"k"`
		O string `json:"o"`
	}
	if json.Unmarshal([]byte(v), &p) != nil || p.S == "" {
		return client.Controls{}, false
	}
	return client.Controls{Session: p.S, Kind: p.K, Owner: p.O}, true
}

// actOn carries a press to transport; a value so a test can stand in for it.
var actOn = transportpeer.TransportAct

// slackPressBudget bounds carrying one press: a Continue starts a run, which is
// a session write and nothing more before it answers.
const slackPressBudget = 30 * time.Second

// slackPress carries one press to transport — act is what was pressed — as the
// Slack user who pressed it, from the message and thread it was pressed in, in the
// org the Slack-verified team resolves to. It runs after Slack has had its 200,
// so a slow run never makes Slack show the press failed.
func slackPress(s *cloud.Service[state], org string, in slackInteraction, act client.TransportActIn) {
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), slackPressBudget)
	defer cancel()
	room, message, thread := in.Container.ChannelID, in.Container.MessageTS, in.Message.ThreadTS
	if room == "" {
		room = in.Channel.ID
	}
	act.Provider, act.Account, act.Channel, act.Thread = "slack", strings.ToLower(in.Team.ID), room, thread
	act.Message, act.User = message, in.User.ID
	_, err := actOn(ctx, &act)
	if err == nil {
		return
	}
	s.Log.Warn("slack: a press was not applied", "org", org, "session", act.Controls.Session,
		"question", act.Question, "command", act.Command, "err", err)
	tok, terr := TokenFor(ctx, org, "slack", slackBotTokenSecret)
	if terr != nil {
		return
	}
	_ = slackPostEphemeral(ctx, string(tok), room, in.User.ID, thread, pressRefusal(err, act.Command, act.Controls.Owner))
}

// pressRefusal is what the presser is told, privately, when a press did nothing.
func pressRefusal(err error, command, owner string) string {
	verb, what := map[string]string{client.SessionStop: "stop", client.SessionPause: "pause",
		client.SessionResume: "continue", client.SessionAnswer: "answer"}[command], "run"
	if command == client.SessionAnswer {
		what = "question"
	}
	var he *zip.HTTPError
	switch {
	case errors.As(err, &he) && he.Status == http.StatusForbidden && owner != "":
		return "Only <@" + owner + "> can " + verb + " this " + what + "."
	case errors.As(err, &he) && he.Status == http.StatusForbidden:
		return "Only the person this " + what + " is for can " + verb + " it."
	case errors.As(err, &he) && (he.Status == http.StatusConflict || he.Status == http.StatusNotFound) && what == "question":
		return "That question is no longer open."
	case errors.As(err, &he) && (he.Status == http.StatusConflict || he.Status == http.StatusNotFound):
		return "That run has already finished."
	}
	return "I could not " + verb + " that " + what + " just now. This is on our side — please try again."
}

// ── a question ───────────────────────────────────────────────────────────────

// actAnswer is the action id of a question's options: the select's own, and each
// button's with its position after it (Slack wants an action id once per block).
const actAnswer = "hanzo_answer"

// buttonsUpTo is how many options are drawn as buttons; more are one select,
// which is what a row of buttons stops being readable at.
const buttonsUpTo = 5

// questionBlocks is a question a run asked (apps/agents/ask.go) as Block Kit: the
// question, an answer per option — buttons, or a select past buttonsUpTo — and a
// line saying a reply in words answers it too.
//
// The question and its options are the MODEL's text, so they are escaped before
// they meet mrkdwn: a question must not be able to page a workspace.
func questionBlocks(q client.Question, thread bool) []any {
	value := func(i int) string {
		b, _ := json.Marshal(struct {
			Q string `json:"q"`
			O string `json:"o,omitempty"`
			I int    `json:"i"`
		}{q.ID, q.Owner, i})
		return string(b)
	}
	msg := slackEscape(q.Text)
	var answers map[string]any
	if len(q.Options) <= buttonsUpTo {
		buttons := make([]any, 0, len(q.Options))
		for i, o := range q.Options {
			if o.Description != "" {
				msg += "\n• *" + slackEscape(o.Label) + "* — " + slackEscape(o.Description)
			}
			buttons = append(buttons, button(actAnswer+"_"+strconv.Itoa(i), o.Label, "", value(i)))
		}
		answers = map[string]any{"type": "actions", "block_id": "hanzo_question", "elements": buttons}
	} else {
		opts := make([]any, 0, len(q.Options))
		for i, o := range q.Options {
			opt := map[string]any{"text": plainText(o.Label), "value": value(i)}
			if o.Description != "" {
				opt["description"] = plainText(text.Cut(o.Description, 75))
			}
			opts = append(opts, opt)
		}
		answers = map[string]any{"type": "actions", "block_id": "hanzo_question", "elements": []any{map[string]any{
			"type": "static_select", "action_id": actAnswer, "placeholder": plainText("Choose an answer"), "options": opts,
		}}}
	}
	reply := "Or reply here in your own words."
	if thread {
		reply = "Or reply in this thread in your own words."
	}
	if len(msg) > slackSectionMax {
		msg = text.Cut(msg, slackSectionMax-1) + "…"
	}
	return []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": msg}},
		answers,
		map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": reply}}},
	}
}

// parseQuestionValue reads a pressed option back: which question, and whose.
func parseQuestionValue(v string) (question, owner string, ok bool) {
	var p struct {
		Q string `json:"q"`
		O string `json:"o"`
	}
	if json.Unmarshal([]byte(v), &p) != nil || p.Q == "" {
		return "", "", false
	}
	return p.Q, p.O, true
}
