package bot

// The bot ROSTER — this org's bots as members of its team spaces.
//
// It answers here because a bot is one noun and /v1/bot is its address. The rows
// are team's: a space membership is a team fact, written by team's transactor and
// stored beside its spaces, so this asks team for them over the internal plane
// rather than reaching into another subsystem's store. The address follows the
// NOUN, the data stays with its owner, and the plane is how the two meet.
//
// It replaced /v1/team/bots, which put a second bot address under a subsystem a
// caller had to know about to ask a question about bots.

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	peer "github.com/hanzoai/cloud/client"
	teampeer "github.com/hanzoai/cloud/client/team"
)

type memberOps struct{}

// list returns the caller org's bots as space members — each with the member
// account uuid and the Person reference the roster addresses it by.
//
// A deployment that runs no team subsystem has no spaces and therefore no
// roster, which is an empty list rather than an error: ErrNoPeer is the ONE
// error that means "this deployment does not run that app", and every other
// failure is an outage and says so.
//
// Response: {"bots":[{"id":"a1","name":"Concierge","userId":"…","personRef":"…","active":true}]}
func (memberOps) list(ctx context.Context, _ *cloud.Unit) (*peer.BotRoster, error) {
	out, err := teampeer.TeamBots(ctx, &peer.BotsIn{})
	if err == cloud.ErrNoPeer {
		return &peer.BotRoster{Bots: []peer.BotMember{}}, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// sync re-projects the caller org's bots as members into every space of the org
// and removes the ones whose agent is gone. Idempotent, and admin only — the
// admin bit rides the caller to team, which is what decides it.
//
// Response: {"synced":true,"projected":12}
func (memberOps) sync(ctx context.Context, _ *cloud.Unit) (*peer.BotSync, error) {
	out, err := teampeer.TeamBotsSync(ctx, &peer.BotsIn{})
	if err == cloud.ErrNoPeer {
		// Nothing to project into. Saying so is honest; a fabricated success is not.
		return nil, zip.ErrNotFound("no team subsystem in this deployment")
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}
