package team

// This file is the bots-as-members READ surface (inspectability) + the in-process
// seam that sources bots from the canonical agents store. It replaces team-go's
// IAM-SA HTTP enumeration (pkg/bots), which was broken (HANZO_API_KEY rejected →
// bot_members=0). The ONE source of bots is now agents.ListForOrg, called
// in-process.

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/principal"
)

// botsBridge holds the transactor + account stores the bots routes read/write.
type botsBridge struct {
	trans    *transServer
	accounts *accountStore
}

func (b *botsBridge) register(app *zip.App) {
	app.Get("/v1/team/bots", b.list)
	app.Post("/v1/team/bots/sync", b.sync)
}

// botView is the published shape of one bot member.
type botView struct {
	ID        string `json:"id"`        // the agent id
	Name      string `json:"name"`      // display name
	UserID    string `json:"userId"`    // derived member account uuid (personUuid)
	PersonRef string `json:"personRef"` // the projected Person _id
	Active    bool   `json:"active"`
}

// list returns the org's bot members — the org's agents projected as the workspace
// Employees they become. Org-scoped via principal.Tenant (the VALIDATED IAM owner
// claim), NEVER a client header.
func (b *botsBridge) list(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("validated org required")
	}
	bots, err := agentsBotLister(c.Context(), org)
	if err != nil {
		// A missing/disabled agents subsystem is an honest empty list, not a 500.
		return c.JSON(http.StatusOK, map[string]any{"bots": []botView{}})
	}
	out := make([]botView, 0, len(bots))
	for _, bt := range bots {
		uid := botUserID(bt.ID)
		out = append(out, botView{
			ID: bt.ID, Name: bt.Name, UserID: uid, PersonRef: PersonRef(uid), Active: bt.Active,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"bots": out})
}

// sync re-projects the org's agents as Employees into EVERY workspace of the org
// (idempotent). Admin only: mutating a workspace's roster requires the
// gateway-minted admin flag (never client-forgeable). Org-scoped via
// principal.Tenant.
func (b *botsBridge) sync(c *zip.Ctx) error {
	org, ok := principal.Tenant(c)
	if !ok {
		return zip.ErrForbidden("validated org required")
	}
	if !c.IsAdmin() {
		return zip.ErrForbidden("workspace admin required")
	}
	projected, err := b.syncOrg(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bot sync: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"synced": true, "projected": projected})
}

// syncOrg projects the org's agents into each of the org's workspaces via the
// in-process ingest path (same applyTx + hub broadcast as a live connect). Returns
// the number of (workspace × bot) projections applied.
func (b *botsBridge) syncOrg(ctx context.Context, org string) (int, error) {
	if b.accounts == nil || b.trans == nil {
		return 0, nil
	}
	wss, err := b.accounts.WorkspacesForOrg(ctx, org)
	if err != nil {
		return 0, err
	}
	bots, err := agentsBotLister(ctx, org)
	if err != nil {
		return 0, nil // no agents subsystem → nothing to project (not an error)
	}
	projected := 0
	for _, ws := range wss {
		for _, bt := range bots {
			uid := botUserID(bt.ID)
			if uid == "" {
				continue
			}
			exists := hasDoc(org, ws.UUID, PersonRef(uid))
			Apply(org, ws.UUID, acctSystem, MemberTxes(Member{
				UserID: uid, Name: pick(bt.Name, uid), Role: "member", IsBot: true, Active: bt.Active,
			}, exists)...)
			projected++
		}
	}
	return projected, nil
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
