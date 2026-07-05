package team

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// rosterServer builds a transactor server whose two roster sources are wired: a
// real accountStore (seeded with one workspace + its human owner) and an injected
// BotLister standing in for agents.ListForOrg. It returns the server, a query
// session bound to the seeded workspace, and the workspace uuid.
func rosterServer(t *testing.T, org, human, humanName string, bots []Bot) (*transServer, *session, string) {
	t.Helper()
	dir := t.TempDir()
	accounts, err := openAccountStore(filepath.Join(dir, "account.db"))
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accounts.Close() })

	ws, err := accounts.EnsureWorkspace(context.Background(), org, human, humanName)
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}

	srv := &transServer{
		hub:      newHub(),
		store:    newStore(filepath.Join(dir, "workspaces")),
		hier:     buildHierarchy(modelJSON),
		accounts: accounts,
		bots: func(_ context.Context, gotOrg string) ([]Bot, error) {
			if gotOrg != org { // the reconcile MUST query bots org-scoped
				t.Fatalf("bot lister called with org %q, want %q (tenant leak)", gotOrg, org)
			}
			return bots, nil
		},
	}
	live = srv
	t.Cleanup(func() { live = nil })
	sess := &session{server: srv, store: srv.store, hier: srv.hier, org: org, workspace: ws.UUID, account: acctSystem}
	return srv, sess, ws.UUID
}

// TestReconcileRosterHumansAndBots is the cloud-native port of team-go's
// backfill_roster_test: on connect the workspace docs store gets the human owner
// as a Person AND each of the org's agents as an Employee — sourced IN-PROCESS
// from the agents registry (here the injected BotLister), no Base collection and no
// IAM-SA HTTP. The reconcile runs unconditionally (no sentinel), so it is the ONE
// place bots become team members.
func TestReconcileRosterHumansAndBots(t *testing.T) {
	const org = "maxpower"
	const human = "113d4dd4-2486-40de-be2b-88d6e3e0b718"
	bots := []Bot{
		{ID: "agent_assistant", Name: "maxpower-assistant", Active: true},
		{ID: "agent_daveui", Name: "dave-ui-agent", Active: true},
	}
	_, sess, _ := rosterServer(t, org, human, "Dave Lorenzini", bots)

	// A fresh connect reconciles the FULL roster: human + both bots as Employees.
	sess.seedWorkspace()
	sess.reconcileRoster()

	emps := sess.queryDocs(mixinEmployee, nil)
	if len(emps) != 3 {
		t.Fatalf("employees after roster reconcile = %d, want 3 (human + 2 bots)", len(emps))
	}

	// The human renders "last,first" from the seeded display_name.
	hp := sess.queryDocs(clPerson, map[string]any{"personUuid": human})
	if len(hp) != 1 || hp[0]["name"] != "Lorenzini,Dave" {
		t.Fatalf("human person = %v, want name Lorenzini,Dave", hp)
	}

	// Each bot is a Person keyed by its DETERMINISTIC derived uuid, rendered from
	// its single-token agent name (Huly ",name"), and carries an active Employee.
	for _, b := range bots {
		uid := botUserID(b.ID)
		ps := sess.queryDocs(clPerson, map[string]any{"personUuid": uid})
		if len(ps) != 1 {
			t.Fatalf("bot %s not projected as Person", b.Name)
		}
		if ps[0]["name"] != ","+b.Name {
			t.Fatalf("bot %s name = %v, want ,%s", b.Name, ps[0]["name"], b.Name)
		}
		if ps[0]["avatarType"] != "color" {
			t.Fatalf("bot %s missing avatarType=color: %v", b.Name, ps[0])
		}
		emp, ok := ps[0][mixinEmployee].(map[string]any)
		if !ok || emp["active"] != true || emp["position"] != "Agent" {
			t.Fatalf("bot %s Employee mixin wrong: %v", b.Name, ps[0][mixinEmployee])
		}
	}
}

// TestReconcileRosterIdempotent proves a second connect's reconcile does not
// duplicate members and preserves history — the "reconcile on every connect"
// invariant that the deterministic PersonRef upsert guarantees.
func TestReconcileRosterIdempotent(t *testing.T) {
	const org = "acme"
	const human = "22222222-2222-4222-8222-222222222222"
	bots := []Bot{{ID: "agent_x", Name: "triage-bot", Active: true}}
	_, sess, _ := rosterServer(t, org, human, "Ada Lovelace", bots)

	sess.seedWorkspace()
	sess.reconcileRoster()
	sess.reconcileRoster() // reconnect

	if n := len(sess.queryDocs(clPerson, nil)); n != 2 {
		t.Fatalf("persons after two reconciles = %d, want 2 (no dupes)", n)
	}
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 2 {
		t.Fatalf("active employees = %d, want 2", n)
	}
}

// TestReconcileRemovesDeletedBot is the Phase-2B removal-reconcile: an agent that
// is DELETED from the registry (no longer in ListForOrg) must not linger as a stale
// active Employee — the next reconcile deactivates it (drops it from the Team list)
// while its Person survives for authorship history. Idempotent.
func TestReconcileRemovesDeletedBot(t *testing.T) {
	const org = "acme"
	const human = "44444444-4444-4444-8444-444444444444"
	bots := []Bot{
		{ID: "agent_a", Name: "alpha", Active: true},
		{ID: "agent_b", Name: "bravo", Active: true},
	}
	_, sess, _ := rosterServer(t, org, human, "Grace Hopper", bots)
	sess.seedWorkspace()
	sess.reconcileRoster()
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 3 {
		t.Fatalf("active employees (2 bots + human) = %d, want 3", n)
	}

	// agent_b is DELETED from the registry — the lister now returns only agent_a.
	sess.server.bots = func(context.Context, string) ([]Bot, error) {
		return []Bot{{ID: "agent_a", Name: "alpha", Active: true}}, nil
	}
	sess.reconcileRoster()

	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 2 {
		t.Fatalf("active employees after agent_b delete = %d, want 2 (human + agent_a)", n)
	}
	uidB := botUserID("agent_b")
	pb := sess.queryDocs(clPerson, map[string]any{"personUuid": uidB})
	if len(pb) != 1 {
		t.Fatalf("deleted bot's Person = %d, want 1 (history preserved)", len(pb))
	}
	if emp, _ := pb[0][mixinEmployee].(map[string]any); emp == nil || emp["active"] != false {
		t.Fatalf("deleted bot Employee not deactivated: %v", pb[0][mixinEmployee])
	}
	// agent_a is untouched (still active).
	if n := len(sess.queryDocs(clPerson, map[string]any{"personUuid": botUserID("agent_a")})); n != 1 {
		t.Fatalf("surviving bot missing")
	}
	// Idempotent: a second reconcile leaves the counts unchanged.
	sess.reconcileRoster()
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 2 {
		t.Fatalf("second reconcile active = %d, want 2 (idempotent)", n)
	}
}

// TestReconcileTransientBotErrorKeepsBots proves the removal half is guarded: a
// transient lister ERROR must NOT be read as "no agents" and deactivate every bot.
// The existing bots stay active until an authoritative list says otherwise.
func TestReconcileTransientBotErrorKeepsBots(t *testing.T) {
	const org = "acme"
	const human = "55555555-5555-4555-8555-555555555555"
	_, sess, _ := rosterServer(t, org, human, "Ada", []Bot{{ID: "agent_x", Name: "x", Active: true}})
	sess.seedWorkspace()
	sess.reconcileRoster()
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 2 {
		t.Fatalf("active employees = %d, want 2", n)
	}

	// The lister now ERRORS (agents subsystem hiccup) — must NOT nuke the bot.
	sess.server.bots = func(context.Context, string) ([]Bot, error) {
		return nil, fmt.Errorf("agents: transient")
	}
	sess.reconcileRoster()
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 2 {
		t.Fatalf("active employees after transient error = %d, want 2 (bot NOT nuked)", n)
	}
}

// TestReconcileRosterBotDeactivation proves an agent that goes inactive (archived/
// retired) drops out of the active Team list on the next reconcile while its Person
// survives — the bots-as-members lifecycle sourced from the agents registry.
func TestReconcileRosterBotDeactivation(t *testing.T) {
	const org = "acme"
	const human = "33333333-3333-4333-8333-333333333333"

	// First connect: the bot is active.
	_, sess, _ := rosterServer(t, org, human, "Grace Hopper", []Bot{{ID: "agent_z", Name: "zen", Active: true}})
	sess.seedWorkspace()
	sess.reconcileRoster()
	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 2 {
		t.Fatalf("active employees (bot live) = %d, want 2", n)
	}

	// Re-point the bot source to report the agent inactive, then reconnect.
	sess.server.bots = func(context.Context, string) ([]Bot, error) {
		return []Bot{{ID: "agent_z", Name: "zen", Active: false}}, nil
	}
	sess.reconcileRoster()

	if n := len(sess.queryDocs(mixinEmployee, map[string]any{"active": true})); n != 1 {
		t.Fatalf("active employees after bot deactivate = %d, want 1 (human only)", n)
	}
	if n := len(sess.queryDocs(clPerson, nil)); n != 2 {
		t.Fatalf("persons after deactivate = %d, want 2 (bot Person history preserved)", n)
	}
}
