package team

// This file is the bots-as-members READ surface (inspectability) + the in-process
// seam that sources bots from the canonical agents store. It replaces team-go's
// IAM-SA HTTP enumeration (pkg/bots), which was broken (HANZO_API_KEY rejected →
// bot_members=0). The ONE source of bots is now agents.ListForOrg, called
// in-process.

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/principal"
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
// and these are two different things — a workspace roster entry and a box.
type botRoster struct {
	// Bots is every agent of the caller's org, projected as a workspace member.
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
	// Active is whether the agent projects as a LIVE workspace member, derived
	// from its registry status: empty, "active" and "ready" are live, anything
	// else (archived/retired) is not. An inactive bot drops out of the Team list
	// while its past authorship survives.
	Active bool `json:"active"`
}

// ListBots returns the caller org's bot members — the org's agents projected as
// the workspace Employees they become, each with the member account uuid and
// Person reference the roster addresses it by. An agents subsystem that is not
// mounted answers an empty list, never an error.
func (b *botsBridge) listBots(ctx context.Context, _ *none) (*botRoster, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	bots, err := agentsBotLister(ctx, org)
	if err != nil {
		// A missing/disabled agents subsystem is an honest empty list, not a 500.
		return &botRoster{Bots: []botMember{}}, nil
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

// SyncBots re-projects the caller org's agents as workspace members into EVERY
// workspace of the org, and removes the ones whose agent is gone. It is
// idempotent, and admin only: mutating a workspace's roster requires the
// gateway-minted admin flag, which a client can never forge. It answers how many
// roster entries the reconcile touched.
func (b *botsBridge) syncBots(ctx context.Context, _ *none) (*botSync, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !admin(ctx) {
		return nil, zip.ErrForbidden("workspace admin required")
	}
	projected, err := b.syncOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "bot sync: %v", err)
	}
	return &botSync{Synced: true, Projected: projected}, nil
}

// syncOrg re-runs the FULL roster reconcile (humans + bots add AND stale-bot
// removal) for each of the org's workspaces, via the SAME session.reconcile path a
// live connect uses — one reconcile, one way. It broadcasts the applied txes so
// connected clients refresh live. Returns the number of roster entries touched.
func (b *botsBridge) syncOrg(ctx context.Context, org string) (int, error) {
	if b.accounts == nil || b.trans == nil {
		return 0, nil
	}
	wss, err := b.accounts.WorkspacesForOrg(ctx, org)
	if err != nil {
		return 0, err
	}
	touched := 0
	for _, ws := range wss {
		sess := &session{server: b.trans, store: b.trans.store, hier: b.trans.hier, org: org, workspace: ws.UUID, account: acctSystem}
		sess.seedWorkspace()
		touched += sess.reconcile(true) // broadcast: an admin re-sync updates live clients
	}
	return touched, nil
}

// agentsBotLister is the ONE in-process seam to the canonical agent registry: it
// reads the org's agents via agents.ListForOrg (org-scoped, no HTTP hop) and maps
// each to the minimal Bot shape the roster reconcile projects. A retired/archived
// agent (Status not active/ready) projects as an inactive Employee (drops out of
// the Team list while its authorship survives).
func agentsBotLister(ctx context.Context, org string) ([]Bot, error) {
	ags, err := agents.ListForOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]Bot, 0, len(ags))
	for _, a := range ags {
		out = append(out, Bot{ID: a.ID, Name: a.Name, Active: botActive(a.Status)})
	}
	return out, nil
}

// botActive maps an agent status to Employee.active. Empty / "active" / "ready"
// are live; anything else (archived/retired) is inactive.
func botActive(status string) bool {
	return status == "" || status == "active" || status == "ready"
}

// agentReplyRunner is the ONE in-process seam the Chunter responder (chat.go) uses
// to make a bot answer: it runs the agent through agents.RunOnBehalf — the SAME
// billed/metered/recorded run path the HTTP POST /v1/agents/:id/run handler uses —
// on behalf of the human who addressed it, and returns the model's text. A run that
// executed but whose model errored (nil error, error-status Run) surfaces as an
// error so the responder posts nothing rather than an empty bubble.
func agentReplyRunner(ctx context.Context, org, userSub, agentID, input string) (string, error) {
	run, err := agents.RunOnBehalf(ctx, org, userSub, agentID, input)
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
