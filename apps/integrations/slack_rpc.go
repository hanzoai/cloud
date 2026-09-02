// Copyright © 2026 Hanzo AI. MIT License.

package integrations

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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// exposeSlack publishes the Slack egress on the internal plane. Mount calls it.
func exposeSlack() {
	zip.Post[plane.SlackSendIn, plane.SlackSent](cloud.Plane(), "/integrations/slack/send", planeSlackSend,
		zip.WithOperationID(plane.IntegrationsSlackSend),
		zip.WithSummary("Post to an org's Slack channel via the org's KMS-custodied bot token"))
}

// planeSlackSend posts one message through the org's own bot token. The ORG is
// the CALLER's (cloud.Who(ctx).Org, set on the peer context by the caller), never
// an argument — a caller able to name it could post as another tenant. A named
// handler, not a closure, so zipdoc lifts this prose into the registry.
func planeSlackSend(ctx context.Context, in *plane.SlackSendIn) (*plane.SlackSent, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("slack send: no org on the call")
	}
	ts, err := SendSlackAt(ctx, org, in.Channel, in.Thread, in.Update, in.Text)
	if err != nil {
		return nil, err
	}
	return &plane.SlackSent{TS: ts}, nil
}
