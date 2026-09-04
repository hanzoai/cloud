// Copyright © 2026 Hanzo AI. MIT License.

package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// Delivery, published on the internal plane.
//
// The HTTP send surface derives the sending tenant from the VALIDATED PRINCIPAL
// and never from a header, which is exactly right for a customer calling it: you
// may send as yourself and nobody else. It is the wrong shape for a SIBLING
// SUBSYSTEM. IAM answers for every white-label identity host, so a verification
// code it mints may belong to any tenant; authenticating as a principal would let
// it send as one org only, and the workaround — a long-lived service credential
// mounted for the life of a pod — is a secret to mint, hold and rotate for a call
// that never leaves the cluster.
//
// On the plane the org is an ARGUMENT. The caller is a peer in the same trust
// domain reached over ZAP on a unix socket, not a customer over the edge, so it
// is trusted to name the tenant it is acting for — the same thing every other
// internal-plane op does. That is what lets one process send for every tenant
// with no credential at all.
//
// The provider stays notify's decision, resolved from the ORG's own KMS
// credentials (Twilio for SMS today). A caller that could name a provider could
// route another tenant's message through an account it does not own.

// exposeSend publishes the delivery op. Mount calls it.
//
// The handler is a NAMED function, not a closure: zipdoc lifts an op's prose off
// its handler's doc comment and can lift nothing from an anonymous one.
func exposeSend(s *service) {
	zip.Post[client.Send, client.Sent](cloud.Plane(), "/notify/send",
		func(ctx context.Context, in *client.Send) (*client.Sent, error) { return send(ctx, s, in) },
		zip.WithOperationID(client.NotifySend),
		zip.WithSummary("Deliver one message on the org's configured provider"))
}

// send delivers one message and answers with the provider that carried it.
//
// It funnels into the SAME sendReal path the HTTP handler and every in-process
// caller use, so there is never a second sender and never a divergence in how a
// provider is chosen or built. No suppression check runs here: this op carries
// transactional mail — a verification code the person is waiting on — and the
// opt-out decision belongs to the sender that owns the audience.
//
// Every field is required except the subject, and a missing one is refused rather
// than defaulted. An empty org is the important one: notify resolves the provider
// credential by org, so defaulting it would send this tenant's message through
// somebody else's account.
func send(ctx context.Context, s *service, in *client.Send) (*client.Sent, error) {
	if in == nil {
		return nil, zip.Errorf(400, "notify: no request")
	}
	org := strings.TrimSpace(in.Org)
	if org == "" {
		return nil, zip.Errorf(400, "notify: org is required to resolve a provider credential")
	}
	channel := strings.TrimSpace(in.Channel)
	switch channel {
	case "sms", "email":
	default:
		return nil, zip.Errorf(400, "notify: channel must be sms or email, got %q", channel)
	}
	to := strings.TrimSpace(in.To)
	if to == "" {
		return nil, zip.Errorf(400, "notify: a destination is required")
	}
	if strings.TrimSpace(in.Body) == "" {
		return nil, zip.Errorf(400, "notify: a body is required")
	}

	// provider "" lets notify pick the one whose credentials this org actually has
	// configured in KMS — the whole reason the caller does not name one.
	used, err := s.sendReal(ctx, org, channel, "", []string{to}, in.Subject, in.Body)
	if err != nil {
		// A failed delivery is an error, never a Sent with an empty provider: the
		// caller asked for a send, and reporting success for one that did not happen
		// leaves a person waiting on a message that will never arrive.
		return nil, fmt.Errorf("notify: send on %s for %s: %w", channel, org, err)
	}
	return &client.Sent{Provider: used}, nil
}
