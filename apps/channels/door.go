package channels

// door.go — the ONE outbound path, and why it is a plane call.
//
// Each transport used to hold integrations' Send* helper as a function value
// (slackDoor = integrations.SendSlack). Every one of those ends at TokenFor,
// which is gated on integrations' `mounted` global — and this is a different
// PROCESS, so all four answered "integrations: not mounted" and no reply was
// ever posted. A turn ran, produced an answer, and spoke into nothing.
//
// The send itself stays in integrations because the per-org bot token IS the
// tenancy gate: an org that never connected a workspace cannot post, and that
// property only holds where the token lives. Channels never sees a token; it
// says what to post and to whom.

import (
	"context"

	"github.com/hanzoai/cloud/plane"
)

// post carries one outbound message to the transport that owns the provider.
func post(ctx context.Context, in plane.ChatSendIn) (string, error) {
	out, err := plane.Ask[plane.ChatSendIn, plane.ChatSendOut](ctx, "integrations", plane.ChatSend, &in)
	if err != nil {
		return "", err
	}
	if out == nil {
		return "", nil
	}
	return out.MessageID, nil
}
