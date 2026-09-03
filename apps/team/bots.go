package team

// This file is the bots-as-members READ surface (inspectability) + the in-process
// client that sources bots from the canonical agents store. It replaces team-go's
// IAM-SA HTTP enumeration (pkg/bots), which was broken (HANZO_API_KEY rejected →
// bot_members=0). The ONE source of bots is now agents.ListForOrg, called
// in-process.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	agentspeer "github.com/hanzoai/cloud/plane/agents"
)

// botsBridge holds the transactor + account stores the bots routes read/write.
// degraded is the fail-closed posture Mount resolved (no HS256 secret): a typed
// op cannot be wrapped by Mount's guard, so it asks for itself — see typed.go.
type botsBridge struct {
	trans    *transServer
	accounts *accountStore
	degraded bool
}

// register declares the bots surface as TYPED ops, so one registry entry yields
// the route, the OpenAPI operation, the MCP tool, the CLI command and the SDK
// method. It takes no guard: a typed op is not a zip.Handler, so it carries the
// degraded refusal itself (b.degraded, typed.go).
func (b *botsBridge) register(app cloud.Router) {
	// The group is built HERE, from the one prefix constant, because a typed op's
	// path is its group's prefix composed with its leaf and cmd/zipdoc resolves
	// that prefix from the assignment in the SAME file — a group arriving as a
	// parameter is one it cannot see, and it refuses rather than filing the prose
	// under a path that does not exist. Same shape apps/agents/targets.go uses.
	g := app.Group(teamPrefix)
	zip.Get(g, "/bots", b.listBots)
	zip.Post(g, "/bots/sync", b.syncBots)
}

// botRoster is the org's bot members. It is NOT visor's botList (the compute
// fleet's bot MACHINES): one name may mean one thing across the fleet document,
// and these are two different things — a space roster entry and a box.
type botRoster struct {
	// Bots is every agent of the caller's org, projected as a space member.
	Bots []botMember `json:"bots"`
}

// botSync acknowledges a roster re-projection.
type botSync struct {
	// Synced is true when the reconcile ran.
	Synced bool `json:"synced"`
	// Projected is how many roster entries the reconcile touched.
	Projected int `json:"projected"`
}

// botMember is the published shape of one bot member.
type botMember struct {
	ID        string `json:"id"`        // the agent id
	Name      string `json:"name"`      // display name
	UserID    string `json:"userId"`    // derived member account uuid (personUuid)
	PersonRef string `json:"personRef"` // the projected Person _id
	// Active is whether the agent projects as a LIVE space member, derived
	// from its registry status: empty, "active" and "ready" are live, anything
	// else (archived/retired) is not. An inactive bot drops out of the Team list
	// while its past authorship survives.
	Active bool `json:"active"`
}

// ListBots returns the caller org's bot members — the org's agents projected as
// the space Employees they become, each with the member account uuid and
// Person reference the roster addresses it by. An agents subsystem that is not
// mounted answers an empty list, never an error.
func (b *botsBridge) listBots(ctx context.Context, _ *cloud.Unit) (*botRoster, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	// Through the transactor's OWN lister, which is the same client the mention
	// responder reads. It used to call agentsBotLister directly — a second path to
	// one fact, so the read surface and the responder could disagree about an
	// org's agents, and only one of them was reachable from a test.
	bots, err := b.trans.bots(ctx, org)
	if errors.Is(err, cloud.ErrNoPeer) {
		// A deployment that runs no agents subsystem HAS no agents, and an empty
		// list is the honest answer to "who are this org's bots".
		return &botRoster{Bots: []botMember{}}, nil
	}
	if err != nil {
		// Anything else is a peer that ANSWERED BADLY, and the two must not look
		// alike. Rendering this as an empty list is what let a roster that failed
		// on every call report "this org has no agents" for months.
		return nil, zip.Errorf(http.StatusBadGateway, "team: agent roster unavailable: %v", err)
	}
	out := make([]botMember, 0, len(bots))
	for _, bt := range bots {
		uid := botUserID(bt.ID)
		out = append(out, botMember{
			ID: bt.ID, Name: bt.Name, UserID: uid, PersonRef: PersonRef(uid), Active: bt.Active,
		})
	}
	return &botRoster{Bots: out}, nil
}

// SyncBots re-projects the caller org's agents as space members into EVERY
// space of the org, and removes the ones whose agent is gone. It is
// idempotent, and admin only: mutating a space's roster requires the
// gateway-minted admin flag, which a client can never forge. It answers how many
// roster entries the reconcile touched.
func (b *botsBridge) syncBots(ctx context.Context, _ *cloud.Unit) (*botSync, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !admin(ctx) {
		return nil, zip.ErrForbidden("space admin required")
	}
	projected, err := b.syncOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "bot sync: %v", err)
	}
	return &botSync{Synced: true, Projected: projected}, nil
}

// syncOrg re-runs the FULL roster reconcile (humans + bots add AND stale-bot
// removal) for each of the org's spaces, via the SAME session.reconcile path a
// live connect uses — one reconcile, one way. It broadcasts the applied txes so
// connected clients refresh live. Returns the number of roster entries touched.
func (b *botsBridge) syncOrg(ctx context.Context, org string) (int, error) {
	if b.accounts == nil || b.trans == nil {
		return 0, nil
	}
	wss, err := b.accounts.SpacesForOrg(ctx, org)
	if err != nil {
		return 0, err
	}
	touched := 0
	for _, ws := range wss {
		sess := &session{server: b.trans, store: b.trans.store, hier: b.trans.hier, org: org, space: ws.UUID, account: acctSystem}
		sess.seedSpace()
		touched += sess.reconcile(true) // broadcast: an admin re-sync updates live clients
	}
	return touched, nil
}

// agentsBotLister is the ONE client to the canonical agent registry, and it asks
// the agents PROCESS rather than calling into it. A retired/archived agent
// (Status not active/ready) projects as an inactive Employee (drops out of the
// Team list while its authorship survives).
//
// It used to be `agents.ListForOrg`, an in-process call, and the comment above it
// called that "org-scoped, no HTTP hop" — which was true and beside the point.
// That function gates on agents' `mounted` package global, and a package global
// is per-PROCESS: team and agents are separate manifest rows and therefore
// separate plugin binaries, so it answered ErrNoPeer in every real deployment.
// The cost was not an outage but something quieter — listBots renders an error as
// an EMPTY ROSTER, so `GET /v1/team/bots` answered `[]` for an org holding
// agents, no bot was ever projected as a space member, and the mention
// responder found nobody to address and stayed silent while the boot log said it
// was ENABLED. One global, three symptoms.
//
// The org rides the CALLER, not the argument, and it must be stated on a DETACHED
// context: zip reads a stated caller only where there is NO request behind the
// context (caller.go), so stating it on a live request's context is silently
// discarded and the peer answers "org required". This is the same shape
// apps/integrations uses at its own plane.Ask, and the reason is written out at
// apps/agents/onbehalf_rpc.go — the one place it was learned the expensive way.
func agentsBotLister(_ context.Context, org string) ([]Bot, error) {
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), rosterTimeout)
	defer cancel()
	roster, err := agentspeer.AgentsRoster(ctx, &plane.RosterIn{})
	if err != nil {
		return nil, err
	}
	out := make([]Bot, 0, len(roster.Agents))
	for _, a := range roster.Agents {
		out = append(out, Bot{ID: a.ID, Name: a.Name, Active: botActive(a.Status)})
	}
	return out, nil
}

// rosterTimeout bounds one roster read. It is short because every caller is on a
// latency path a person is waiting on — a member list render, or the mention
// gate that runs before an agent can answer — and a roster that has not arrived
// in this long is not going to change the answer.
const rosterTimeout = 5 * time.Second

// botActive maps an agent status to Employee.active. Empty / "active" / "ready"
// are live; anything else (archived/retired) is inactive.
func botActive(status string) bool {
	return status == "" || status == "active" || status == "ready"
}

// agentReplyRunner is the ONE client the Chunter responder (chat.go) uses to make
// a bot answer: it runs the agent through the SAME billed/metered/recorded run
// path the HTTP POST /v1/agent/:id/run handler uses, on behalf of the human who
// addressed it, and returns the model's text. A run that executed but whose model
// errored (nil error, error-status Run) surfaces as an error so the responder
// posts nothing rather than an empty bubble.
//
// It asks the agents PROCESS, for the reason agentsBotLister does and with the
// same consequence if it did not: agents.RunOnBehalf gates on that package's
// `mounted` global, which is nil here. apps/channels and apps/integrations took
// this leg long ago; team is the third bridge and was the one still calling in.
//
// The ORG is on the wire AND stated on the context, and both are load-bearing for
// different halves: the stated one authorizes the run's balance gate (which reads
// the caller's identity, never an argument), and the payload one is what the run
// RECORD is filed under.
func agentReplyRunner(_ context.Context, org, userSub, agentID, input string) (string, error) {
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), agentReplyTimeout)
	defer cancel()
	run, err := agentspeer.AgentsRunOnBehalf(ctx, &plane.RunOnBehalfIn{
		Org: org, Subject: userSub, Ref: agentID, Input: input,
	})
	if err != nil {
		return "", err
	}
	if run.Error != "" {
		return "", errors.New(run.Error)
	}
	return run.Output, nil
}

// botUserID derives a STABLE member account uuid from an agent id — a UUIDv5 over
// namespace "agent:<id>", so re-syncs converge (never duplicate a bot member) and
// the value is a valid UUID (the Person.personUuid + token invariant). Empty in →
// empty out.
func botUserID(agentID string) string {
	if agentID == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("agent:"+agentID)).String()
}
