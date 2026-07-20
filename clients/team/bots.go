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

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/principal"
)

// botsBridge holds the transactor + account stores the bots routes read/write.
type botsBridge struct {
	trans    *transServer
	accounts *accountStore
}

func (b *botsBridge) register(r zip.Router, guard guardFn) {
	r.Get("/bots", guard(b.list))
	r.Post("/bots/sync", guard(b.sync))
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
// Employees they become. Org-scoped via principal.Org (the VALIDATED IAM owner
// claim), NEVER a client header.
func (b *botsBridge) list(c *zip.Ctx) error {
	org, ok := principal.Org(c)
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
// principal.Org.
func (b *botsBridge) sync(c *zip.Ctx) error {
	org, ok := principal.Org(c)
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
