package channels

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
)

// registry.go is the closed transport registry — exactly the four connected
// chat transports, enumerated in fixed alphabetical order so GET /v1/channels
// is deterministic.

// capabilities advertises what a transport renders natively. All four ship
// media:false / actions:false this pass — renderText (envelope.go) is the ONE
// downgrade path; native rendering is a named follow-up.
type capabilities struct {
	DM      bool `json:"dm"`
	Group   bool `json:"group"`
	Thread  bool `json:"thread"`
	Media   bool `json:"media"`
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
