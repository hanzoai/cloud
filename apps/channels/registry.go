package channels

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
)

// registry.go is the closed transport registry — exactly the four connected
// chat transports, enumerated in fixed alphabetical order so GET /v1/channels
// is deterministic.

// capabilities advertises what a transport renders natively. All four ship
// media:false / actions:false this pass — renderText (envelope.go) is the ONE
// downgrade path; native rendering is a named follow-up.
type capabilities struct {
	// DM is whether the transport carries a DIRECT message at all. True for slack,
	// teams and telegram. False for discord, honestly: that ingress is guild-scoped
	// slash commands — an interaction without a guild id is refused at the door —
	// so nothing ever arrives classified as a DM, no reply route is ever learned
	// for one, and a send addressed at a Discord DM is refused 409.
	DM bool `json:"dm"`
	// Group is whether the transport carries multi-person rooms — a Discord guild
	// channel, a Slack channel, a Teams channel or group chat, a Telegram group or
	// supergroup. True on all four.
	Group bool `json:"group"`
	// Thread is whether a reply can be threaded UNDER a specific message. True for
	// slack alone: it is the only transport whose ingress reports a thread
	// (thread_ts, published as the envelope's replyTo) and whose door posts back
	// into it. Discord's replyTo makes an inline reply rather than a thread,
	// Telegram's answers one message id, and Teams carries no reply target at all —
	// a replyTo sent to it is ignored.
	Thread bool `json:"thread"`
	// Media is whether the transport renders an ATTACHMENT natively. False on all
	// four this pass, and a send is not refused for it: renderText flattens each
	// attachment to one `kind: url (mime)` line after the text rather than dropping
	// it.
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
	// False on all four transports this pass, and nothing refuses a send for it:
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
	normalize func(ev integrations.IngressEvent) (Message, bool)
	send      func(ctx context.Context, s *cloud.Service[state], org string, m Message) (Delivery, error)
}

// transports is the closed set; elements are package vars in their transport
// files. Fixed alphabetical order — the deterministic GET /v1/channels listing.
var transports = []transport{discordTransport, slackTransport, teamsTransport, telegramTransport}

func transportFor(id string) (transport, bool) {
	for _, t := range transports {
		if t.id == id {
			return t, true
		}
	}
	return transport{}, false
}
