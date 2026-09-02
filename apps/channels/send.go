package channels

// send.go — the ONE outbound path, and why it is a plane call.
//
// A transport must not hold an integrations Send* helper as a function value.
// Every one of those ends at TokenFor, which is gated on integrations' `mounted`
// global — and this is a different PROCESS, so each would answer "integrations:
// not mounted" and post no reply at all: a turn that runs, produces an answer,
// and speaks into nothing. The plane call crosses the process boundary that a
// function value cannot.
//
// The send itself stays in integrations because the per-org bot token IS the
// tenancy gate: an org that never connected a workspace cannot post, and that
// property only holds where the token lives. Channels never sees a token; it
// says what to post and to whom.

import (
	"context"

	"github.com/hanzoai/cloud/plane"
)

// ask is the plane call the send rides; a test swaps it to read what actually
// reached the wire, which is where the org went missing.
var ask = plane.Ask[plane.ChatSendIn, plane.ChatSendOut]

// post carries one outbound message to the transport that owns the provider, AS
// an org.
//
// The tenant is a parameter rather than a field the caller fills, because it is
// what resolves the credential the send spends: integrations refuses a send that
// names no org, and four of the five transports named none — so telegram and whatsapp
// could not deliver at all, and discord and teams spent a shared app credential
// with nothing to check it against. A parameter cannot be forgotten the way a
// field can.
func post(ctx context.Context, org string, in plane.ChatSendIn) (string, error) {
	in.Org = org
	out, err := ask(ctx, "integrations", plane.ChatSend, &in)
	if err != nil {
		return "", err
	}
	if out == nil {
		return "", nil
	}
	return out.MessageID, nil
}
