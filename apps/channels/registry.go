package channels

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// registry.go is the closed transport registry — the connected chat transports
// and nothing else, enumerated in fixed alphabetical order so GET /v1/channels
// is deterministic.

// capabilities advertises what a transport renders natively, and every field is
// a FACT about that transport rather than a preference: routes.go publishes them
// and the send path never consults them again, so a flag a caller believes and
// the transport cannot honour is a failure at the platform.
// capability_test.go holds each one against the behaviour behind it.
//
// Every transport ships media:false / actions:false — renderText (envelope.go)
// is the ONE downgrade path; native rendering is a named follow-up.
type capabilities struct {
	// DM is whether the transport carries a DIRECT message at all. True for slack,
	// teams, telegram and whatsapp — whatsapp is nothing else, since the Cloud API
	// addresses a person's number and there is no room a third party joins. False
	// for discord, honestly: that ingress is guild-scoped slash commands — an
	// interaction without a guild id is refused at the endpoint — so nothing ever
	// arrives classified as a DM, no reply route is ever learned for one, and a
	// send addressed at a Discord DM is refused 409.
	DM bool `json:"dm"`
	// Group is whether the transport carries multi-person rooms — a Discord guild
	// channel, a Slack channel, a Teams channel or group chat, a Telegram group or
	// supergroup. False on whatsapp alone, which has no such room to carry.
	Group bool `json:"group"`
	// Thread is whether a reply can be threaded UNDER a specific message. True for
	// slack alone: it is the only transport whose ingress reports a thread
	// (thread_ts, published as the envelope's replyTo) and whose send posts back
	// into it. Discord's replyTo makes an inline reply rather than a thread,
	// Telegram's and WhatsApp's each quote one message, and Teams carries no reply
	// target at all — a replyTo sent to it is ignored.
	Thread bool `json:"thread"`
	// Media is whether the transport renders an ATTACHMENT natively. False
	// everywhere, and a send is not refused for it: renderText flattens each
	// attachment to one `kind: url (mime)` line after the text rather than dropping
	// it. A transport whose egress hands its door the raw text would drop the
	// attachment instead, and an attachment-only send would reach the platform
	// with nothing to say — which is why the flag and the flattening are pinned
	// together.
	Media bool `json:"media"`
	// Actions is whether the transport renders an INTERACTIVE control natively, and
	// it is the flag to read before composing one. The vocabulary is a closed
	// kind-tagged union (envelope.go), exactly four kinds, each carrying only its
	// own field plus an optional label:
	//
	//	command  — a bot command to run (`command`), rendered as a button that
	//	           invokes it.
	//	url      — an external link (`url`), rendered as a link button.
	//	select   — a menu (`options`, each a label and the value choosing it
	//	           returns), rendered as a picker.
	//	approval — a reference to an approval request (`approval.id`), rendered as
	//	           approve/deny controls bound to that id.
	//
	// False on every transport, and nothing refuses a send for it:
	// actions are accepted, validated per kind, and flattened by renderText to one
	// line each after the text — `[label] command`, `[label] url`,
	// `[label] opt | opt`, `[label] approval requested: <id>`. So a caller that
	// needs a real control must read this flag and degrade itself; a caller that
	// only needs the choice communicated can send actions and take the text form.
	Actions bool `json:"actions"`
}

// transport is one chat transport. normalize turns an authenticated ingress
// event into the portable envelope (ok=false drops unclassifiable events).
// send delivers an outbound Message and owns the transport's org-verified
// target-binding check (chat bind / route row / per-org token), so no
// transport can be driven cross-tenant.
type transport struct {
	id        string
	caps      capabilities
	normalize func(ev plane.ChannelsIngestIn) (Message, bool)
	send      func(ctx context.Context, s *cloud.Service[state], org string, m Message) (Delivery, error)
}

// transports is the closed set; elements are package vars in their transport
// files. Fixed alphabetical order — the deterministic GET /v1/channels listing.
// Adding one means adding its probes in capability_test.go, which is what makes
// its published capabilities checkable rather than asserted.
var transports = []transport{discordTransport, slackTransport, teamsTransport, telegramTransport, whatsappTransport}

func transportFor(id string) (transport, bool) {
	for _, t := range transports {
		if t.id == id {
			return t, true
		}
	}
	return transport{}, false
}
