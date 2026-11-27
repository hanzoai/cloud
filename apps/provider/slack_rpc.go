// Copyright © 2026 Hanzo AI. MIT License.

package provider

// slack_rpc.go — the Slack egress as a plane op.
//
// Each subsystem is a separate process (a plugin is a process; see apps/plugin),
// and this subsystem alone holds the org's Slack bot token in memory — TokenFor
// reads THIS process's `mounted` connection store. A peer plugin that wants to
// post to Slack (o11y paging an alert) therefore cannot send in-process and cannot
// read a package global across the process boundary: it asks THIS process over the
// ZAP unix socket, exactly as x402 asks commerce to move money. The op runs here,
// so the send sees the real token store.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// exposeSlack publishes the Slack egress on the internal plane. Mount calls it.
func exposeSlack() {
	cloud.Plane().Post("/integrations/slack/send", planeSlackSend,
		zip.WithOperationID(client.IntegrationsSlackSend),
		zip.WithSummary("Post to an org's Slack channel via the org's KMS-custodied bot token"))
}

// planeSlackSend posts one message through the org's own bot token. The ORG is
// the CALLER's (cloud.Who(ctx).Org, set on the peer context by the caller), never
// an argument — a caller able to name it could post as another tenant. A named
// handler, not a closure, so zipdoc lifts this prose into the registry.
func planeSlackSend(ctx context.Context, in *client.SlackSendIn) (*client.SlackSent, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("slack send: no org on the call")
	}
	post := func() (string, error) {
		return SendSlackAt(ctx, org, in.Channel, in.Thread, in.Update, in.Text, in.Controls, client.Question{})
	}
	var ts string
	var err error
	if in.Join {
		token := func() (string, error) {
			tok, err := TokenFor(ctx, org, "slack", slackBotTokenSecret)
			return string(tok), err
		}
		ts, err = postJoining(ctx, in.Channel, token, post)
	} else {
		ts, err = post()
	}
	if err != nil {
		return nil, err
	}
	return &client.SlackSent{TS: ts}, nil
}

// postJoining posts, and when Slack refuses because the app is not in the
// channel, joins it and posts once more. A join Slack refuses is named in the
// error with its own code (missing_scope: the workspace installed without
// channels:join; a private channel cannot be joined at all), so the person
// reading it knows to invite the app instead.
func postJoining(ctx context.Context, channel string, token func() (string, error), post func() (string, error)) (string, error) {
	ts, err := post()
	if err == nil || !notInChannel(err) {
		return ts, err
	}
	tok, terr := token()
	if terr != nil {
		return "", err
	}
	if jerr := slackEnter(ctx, tok, channel); jerr != nil {
		return "", fmt.Errorf("%w; joining %s failed (%v): invite the app there with /invite", err, channel, jerr)
	}
	return post()
}

// notInChannel reports Slack's not_in_channel refusal, as slackChatPostTS words it.
func notInChannel(err error) bool {
	return strings.HasSuffix(err.Error(), ": not_in_channel")
}

// slackEnter joins the app to one channel, by id or by name.
func slackEnter(ctx context.Context, botToken, channel string) error {
	id, err := slackResolveConversation(ctx, botToken, channel)
	if err != nil {
		return err
	}
	return slackJoinOne(ctx, botToken, id)
}
