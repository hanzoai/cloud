// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package integrations

// slack_home.go is the App Home tab: what a person sees when they click Hanzo in
// Slack, and the two settings they can change there.
//
// Slack fills an UNPUBLISHED Home with its own "this is still a work in
// progress" placeholder, so the choice was never Home vs no Home — it was our
// page vs Slack's apology. Any app that enables home_tab_enabled and stops there
// looks half-built to everyone who opens it.
//
// The view is published PER USER on app_home_opened rather than once at install,
// which is how Slack models it: the page then reflects what is true now — this
// person's link, their model, their routing — instead of what was true when the
// workspace connected.
//
// The two controls write to the SAME userLink the chat path reads. There is no
// second settings store and no third source of truth for which model answers: a
// turn reads the link, the Home writes the link.

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	zip "github.com/zap-proto/zip"
)

// The action id the Home's one control carries. Slack echoes it back in the
// block_actions payload, so it IS the key for a setting change and is worth
// naming once.
//
// There used to be a second: a Mode select offering "Chat only" / "Chat + Code".
// It stored a value nothing ever read. What decided whether a message started a
// run was the `code:` prefix, not this preference, so "Chat only" never stopped
// anything and "Chat + Code" never enabled anything. A control that decides
// nothing is worse than a missing one: it tells a person they are safe.
const homeActionModel = "hanzo_model"

// homeModels is the model menu: three tiers, each labelled with what it actually
// is, default first.
//
// `enso` was previously offered as "Enso — auto / Picks the right model for each
// message". It does not: enso is one fixed route entry (deepseek-v4-pro, medium
// reasoning) in the enso catalog, with no per-query selection anywhere in it. The
// menu now names the tier instead of selling a property it does not have.
//
// Enso Flash is labelled "short answers" rather than "fastest", which is what it
// measures as: on a tool-driving turn it emits at 11.7 tok/s against enso's 19.1,
// so it is quicker only when the answer is short enough for its terseness to beat
// its rate (see cloud.ChatModel). Calling it "fastest, for quick questions" sent
// people to the slower tier for exactly the questions it is slower on.
//
// Named here rather than fetched from the catalog because this is a MENU, not an
// inventory: /v1/models lists 107 entries and a dropdown of 107 is not a choice,
// it is a search problem. A deployment that wants different options changes this
// list, and the value is forwarded verbatim either way.
var homeModels = []struct{ Value, Label, Note string }{
	{"enso", "Enso", "Default. Reasons before answering, and drives tools best"},
	{"enso-flash", "Enso Flash", "Terser and cheaper; best for short answers"},
	{"enso-ultra", "Enso Ultra", "Strongest, for hard problems"},
}

// slackPublishHome renders and publishes the Home tab for one user.
//
// It answers the two questions someone opening Home actually has — what can this
// do, and what do I type — and then gets out of the way. A Home that reads like a
// landing page teaches nothing.
//
// A user who has not linked sees the connect prompt INSTEAD of the settings,
// because a model preference for an identity we cannot resolve is a control that
// does nothing.
func slackPublishHome(s *cloud.Service[state], ctx context.Context, botToken, org, user string) error {
	link, linked, err := getUserLink(s, org, "slack", user)
	if err != nil {
		// A Home that cannot read the link still renders — unlinked is the honest
		// fallback, and the page is the place a person fixes exactly that.
		s.Log.Warn("slack home: link lookup", "org", org, "err", err)
		linked = false
	}
	return slackChatPost(ctx, botToken, "/views.publish", map[string]any{
		"user_id": user,
		"view":    homeView(s, org, user, link, linked),
	})
}

func section(text string) map[string]any {
	return map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}}
}

func divider() map[string]any { return map[string]any{"type": "divider"} }

// homeView is the whole page, as data. Split out from the publish so a test can
// assert the page WITHOUT a Slack round trip — the shape of this view is the
// contract, and a screenshot is not a test.
func homeView(s *cloud.Service[state], org, user string, link userLink, linked bool) map[string]any {
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": "Hanzo AI", "emoji": true}},
		section("The Open AI Cloud, in Slack. Ask a question, write and ship code, or query your own infrastructure."),
		divider(),
	}

	if !linked {
		// Unlinked: ONE instruction, and it is the one that unblocks everything.
		// Settings are withheld deliberately — a model chosen by an identity we
		// cannot resolve is a control that quietly does nothing.
		blocks = append(blocks,
			section("*Connect your Hanzo account*\nSend me a direct message and I will hand you a personal connect link. Once connected, every answer runs as _you_, against your org's own agents and balance."),
			divider(),
			section("*Then try*\n• `@Hanzo what changed on main today?`\n• `@Hanzo why is my service returning 500s?`\n• `@Hanzo code: my-repo add a health check`"),
		)
		return map[string]any{"type": "home", "blocks": blocks}
	}

	model := strings.TrimSpace(link.Model)
	if model == "" {
		// No pin: show what the turn will ACTUALLY use, read from the same constant
		// the turn reads. Showing the menu's first row instead made the menu a second
		// place that encoded the default, free to drift from the one that decides —
		// and a Home that names a different model than the one answering is worse
		// than no Home at all.
		model = cloud.ChatModel
	}
	blocks = append(blocks,
		// The selected option must be stated as initial_option or Slack renders the
		// menu blank and the person's own saved choice looks lost.
		map[string]any{
			"type":      "section",
			"text":      map[string]any{"type": "mrkdwn", "text": "*Model*\nWhich model answers your messages."},
			"accessory": modelSelect(model),
		},
		divider(),
		section("*Connected*\n"+linkedAs(link, org)),
		divider(),
		section("*Getting started*\n"+
			"• Send me a direct message — no mention needed\n"+
			"• `@Hanzo` in any channel you have invited me to\n"+
			"• `/hanzo <question>` anywhere, without inviting me\n"+
			"• Ask me to change code — \"fix the failing auth test in cloud\" — and I work the repo in a sandbox, push a branch, open a PR, and report back in the thread"),
		section("_I only post in channels I am a member of — invite me with_ `/invite @Hanzo`_. That is deliberate: I hold no permission to post anywhere uninvited._"),
		map[string]any{"type": "context", "elements": []map[string]any{
			{"type": "mrkdwn", "text": "<https://hanzo.ai|hanzo.ai>  ·  <https://docs.hanzo.ai|Docs>  ·  <https://cloud.hanzo.ai|Console>"},
		}},
	)
	return map[string]any{"type": "home", "blocks": blocks}
}

// linkedAs says WHO the turns run as, in the org they bill. It is the line that
// answers "is this thing acting as me, and against which account".
func linkedAs(link userLink, org string) string {
	who := strings.TrimSpace(link.Subject)
	if who == "" {
		who = "your Hanzo account"
	}
	return "Running as `" + who + "` in *" + org + "*."
}

func modelSelect(selected string) map[string]any {
	opts := make([]map[string]any, 0, len(homeModels))
	var initial map[string]any
	for _, m := range homeModels {
		o := map[string]any{
			"text":        map[string]any{"type": "plain_text", "text": m.Label, "emoji": true},
			"description": map[string]any{"type": "plain_text", "text": m.Note},
			"value":       m.Value,
		}
		opts = append(opts, o)
		if m.Value == selected {
			initial = o
		}
	}
	sel := map[string]any{
		"type":      "static_select",
		"action_id": homeActionModel,
		"options":   opts,
	}
	if initial != nil {
		sel["initial_option"] = initial
	}
	return sel
}

// ── interactivity ────────────────────────────────────────────────────────────

// slackInteractionBody reports whether a verified request body is an
// INTERACTIVITY payload rather than an Events API envelope.
//
// Slack posts both to the same request URL, and they are different encodings:
// events arrive as a JSON body, interactions as form-encoded `payload=<json>`.
// Sniffing the encoding is what Slack's own contract gives us — there is no
// header that distinguishes them.
func slackInteractionBody(raw []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "payload=")
}

type slackInteraction struct {
	Type string `json:"type"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Actions []struct {
		ActionID       string `json:"action_id"`
		SelectedOption struct {
			Value string `json:"value"`
		} `json:"selected_option"`
	} `json:"actions"`
}

// parseSlackInteraction lifts the interaction out of the form body. It reads the
// team and user from the PAYLOAD Slack signed, never from anywhere else.
func parseSlackInteraction(raw []byte) (slackInteraction, bool) {
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return slackInteraction{}, false
	}
	blob := form.Get("payload")
	if blob == "" {
		return slackInteraction{}, false
	}
	var in slackInteraction
	if err := json.Unmarshal([]byte(blob), &in); err != nil {
		return slackInteraction{}, false
	}
	return in, in.Type == "block_actions" && in.User.ID != "" && in.Team.ID != ""
}

// slackHandleInteraction applies a Home setting change and republishes the page.
//
// ISOLATION is the same bar as every other Slack path: the org comes ONLY from
// the install→org map for the Slack-verified team_id, never from the payload.
// The payload names WHICH SETTING and WHICH USER — it never names the tenant.
//
// A value not in our own menu is DROPPED rather than stored. The options came
// from us, so anything else is a forged payload or a stale client, and storing
// it would let a caller choose the model that bills their org.
func slackHandleInteraction(s *cloud.Service[state], c *zip.Ctx, raw []byte) error {
	in, ok := parseSlackInteraction(raw)
	if !ok {
		return c.NoContent(200)
	}
	org, ok := OrgForExternalID("slack", in.Team.ID)
	if !ok {
		return c.NoContent(200)
	}
	link, linked, err := getUserLink(s, org, "slack", in.User.ID)
	if err != nil || !linked {
		// Nothing to store a preference on. Republish so the page shows the connect
		// prompt rather than controls that would silently do nothing.
		if tok, terr := TokenFor(c.Context(), org, "slack", slackBotTokenSecret); terr == nil {
			_ = slackPublishHome(s, c.Context(), string(tok), org, in.User.ID)
		}
		return c.NoContent(200)
	}

	changed := false
	for _, a := range in.Actions {
		v := strings.TrimSpace(a.SelectedOption.Value)
		switch a.ActionID {
		case homeActionModel:
			if validHomeModel(v) {
				link.Model, changed = v, true
			}
		}
	}
	if changed {
		if err := putUserLink(s, org, "slack", in.User.ID, link); err != nil {
			s.Log.Warn("slack home: save preference", "org", org, "err", err)
		}
	}
	// Republish either way: the page a person is looking at must show what is
	// actually stored, including when a write failed.
	if tok, terr := TokenFor(c.Context(), org, "slack", slackBotTokenSecret); terr == nil {
		_ = slackPublishHome(s, c.Context(), string(tok), org, in.User.ID)
	}
	return c.NoContent(200)
}

// validHomeModel accepts only a value this deployment actually offered.
func validHomeModel(v string) bool {
	for _, m := range homeModels {
		if m.Value == v {
			return true
		}
	}
	return false
}
