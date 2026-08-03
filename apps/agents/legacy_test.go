package agents

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// seedLegacy writes one org's worth of every kind of row into the pre-split
// platform-wide agents database under dir, exactly as the shipped single-file
// store wrote them.
func seedLegacy(t *testing.T, dir string, orgs ...string) {
	t.Helper()
	st, err := openStoreAt(dir)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	ctx := context.Background()
	for _, org := range orgs {
		if err := st.Create(ctx, Agent{ID: "agent_" + org, Org: org, Name: "bot", Model: "enso-pro", CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatalf("%s agent: %v", org, err)
		}
		if err := st.InsertRun(ctx, Run{ID: "run_" + org, Org: org, AgentName: "bot", Status: "ok", Model: "enso-pro", Input: "hi", Output: "yo", CreatedAt: 2}); err != nil {
			t.Fatalf("%s run: %v", org, err)
		}
		if err := st.CreateSession(ctx, Session{
			ID: "sess_" + org, Org: org, Agent: "hanzo", Actor: org + "/u1", Status: StatusRunning,
			RootID: "sess_" + org, Title: "t", Terminal: "https://" + org + ".share.hanzo.ai",
			Project: "site", Published: true, StartedAt: 3, CreatedAt: 3, UpdatedAt: 3,
		}); err != nil {
			t.Fatalf("%s session: %v", org, err)
		}
		for i := 0; i < 3; i++ {
			if _, err := st.AppendEvent(ctx, Event{
				ID: "evt_" + org + string(rune('a'+i)), SessionID: "sess_" + org, Org: org,
				Kind: KindLog, Actor: org + "/u1", Payload: `{"n":1}`, CreatedAt: int64(4 + i),
			}); err != nil {
				t.Fatalf("%s event %d: %v", org, i, err)
			}
		}
		if err := st.CreateTarget(ctx, Target{
			ID: "tgt_" + org, Org: org, Owner: org + "/u1", Label: "box", Kind: TargetMachine,
			Status: TargetOnline, Host: "box", CreatedAt: 5, UpdatedAt: 5,
		}); err != nil {
			t.Fatalf("%s target: %v", org, err)
		}
		if err := st.UpsertClaimKeyHash(ctx, org, "tgt_"+org, hashClaimKey("k-"+org), 6); err != nil {
			t.Fatalf("%s claim key: %v", org, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}
}

// TestFanOutLegacyCarriesEveryOrgForward is the upgrade guard. The agents plane
// is live — hanzo link registers sessions into the single agents.db right now —
// so the first boot after the split has exactly one acceptable outcome: every
// org reads back exactly what it had. Anything less is data loss wearing a green
// build.
func TestFanOutLegacyCarriesEveryOrgForward(t *testing.T) {
	dir := t.TempDir()
	seedLegacy(t, dir, "acme", "globex")
	ctx := context.Background()

	st := &state{stores: cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)}
	t.Cleanup(func() { _ = st.stores.CloseAll() })
	if err := fanOutLegacy(ctx, dir, st); err != nil {
		t.Fatalf("fan-out: %v", err)
	}

	for _, org := range []string{"acme", "globex"} {
		sto := storeOf(t, st, org)
		a, err := sto.Get(ctx, org, "bot")
		if err != nil || a.ID != "agent_"+org {
			t.Fatalf("%s agent: %+v %v", org, a, err)
		}
		runs, err := sto.ListRuns(ctx, org, "bot", 10)
		if err != nil || len(runs) != 1 || runs[0].Output != "yo" {
			t.Fatalf("%s runs: %+v %v", org, runs, err)
		}
		x, err := sto.GetSession(ctx, org, "sess_"+org)
		if err != nil {
			t.Fatalf("%s session: %v", org, err)
		}
		// The terminal is the field a recent fix taught UpdateSession to persist;
		// a copy that drops it would silently un-fix it.
		if x.Terminal != "https://"+org+".share.hanzo.ai" || x.Project != "site" || !x.Published {
			t.Fatalf("%s session lost fields: %+v", org, x)
		}
		evs, err := sto.ListEvents(ctx, org, "sess_"+org, 0, 100)
		if err != nil || len(evs) != 3 {
			t.Fatalf("%s events: %d %v", org, len(evs), err)
		}
		// Seq is a subscriber's resume cursor. A copy that renumbered it would
		// silently rewind or skip every live watcher.
		for i, e := range evs {
			if e.Seq != int64(i+1) {
				t.Fatalf("%s event %d seq = %d, want %d", org, i, e.Seq, i+1)
			}
		}
		tg, err := sto.GetTarget(ctx, org, "tgt_"+org)
		if err != nil || tg.Owner != org+"/u1" {
			t.Fatalf("%s target: %+v %v", org, tg, err)
		}
		// The claim key is a live capability: a machine that already holds it must
		// still authenticate after the split, or every serve daemon in the fleet
		// silently stops claiming work.
		if err := sto.verifyClaimKey(ctx, org, "tgt_"+org, "k-"+org); err != nil {
			t.Fatalf("%s claim key no longer verifies: %v", org, err)
		}
	}

	// No org may see another's rows — now because they are not in the file.
	if _, err := storeOf(t, st, "acme").GetSession(ctx, "globex", "sess_globex"); err != errSessionNotFound {
		t.Fatalf("globex's session visible from acme's file: %v", err)
	}
}

// TestFanOutLegacyIsIdempotent proves a re-run is safe two ways: the marker
// short-circuits it, and even with the marker removed the copy itself cannot
// duplicate or clobber, because every row goes in under the primary key it
// already has.
func TestFanOutLegacyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	seedLegacy(t, dir, "acme")
	ctx := context.Background()

	st := &state{stores: cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)}
	t.Cleanup(func() { _ = st.stores.CloseAll() })
	if err := fanOutLegacy(ctx, dir, st); err != nil {
		t.Fatalf("fan-out 1: %v", err)
	}
	// A write made AFTER the split must survive a second fan-out: the legacy file
	// still holds the older copy of this session, and an overwrite would undo it.
	sto := storeOf(t, st, "acme")
	x, err := sto.GetSession(ctx, "acme", "sess_acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	x.Title = "written after the split"
	x.UpdatedAt = 99
	if err := sto.UpdateSession(ctx, x); err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := fanOutLegacy(ctx, dir, st); err != nil {
		t.Fatalf("fan-out 2: %v", err)
	}
	// Drop the marker so the copy itself is exercised again, not just skipped.
	raw := rawAt(t, dir)
	if _, err := raw.Exec(`DELETE FROM agent_fanout`); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	_ = raw.Close()
	if err := fanOutLegacy(ctx, dir, st); err != nil {
		t.Fatalf("fan-out 3: %v", err)
	}

	got, err := sto.GetSession(ctx, "acme", "sess_acme")
	if err != nil {
		t.Fatalf("get after re-run: %v", err)
	}
	if got.Title != "written after the split" {
		t.Fatalf("a re-run clobbered a post-split write: title = %q", got.Title)
	}
	evs, err := sto.ListEvents(ctx, "acme", "sess_acme", 0, 100)
	if err != nil || len(evs) != 3 {
		t.Fatalf("a re-run duplicated events: %d %v", len(evs), err)
	}
	runs, err := sto.ListRuns(ctx, "acme", "bot", 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("a re-run duplicated runs: %d %v", len(runs), err)
	}
}

// TestFanOutLegacyNoopsWithoutALegacyFile keeps a fresh install cheap and quiet:
// no legacy file means nothing to carry, and nothing is created looking for it.
func TestFanOutLegacyNoopsWithoutALegacyFile(t *testing.T) {
	dir := t.TempDir()
	st := &state{stores: cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)}
	t.Cleanup(func() { _ = st.stores.CloseAll() })
	if err := fanOutLegacy(context.Background(), dir, st); err != nil {
		t.Fatalf("fresh install fan-out: %v", err)
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) != 0 {
		t.Fatalf("fresh install wrote %d entries into an empty data dir", len(ents))
	}
}

// TestMountFansOutBeforeServing is the end-to-end form: a deployment that has
// the single file boots, and the org's records are readable over the SAME HTTP
// surface with no shape change.
func TestMountFansOutBeforeServing(t *testing.T) {
	dir := t.TempDir()
	seedLegacy(t, dir, "acme")

	app := mountAppDir(t, dir)
	code, body := do(t, app, "GET", "/v1/agents", "acme", nil)
	if code != 200 {
		t.Fatalf("list after upgrade = %d: %s", code, body)
	}
	if !strings.Contains(string(body), `"name":"bot"`) {
		t.Fatalf("the org's agent did not survive the upgrade: %s", body)
	}
	code, body = do(t, app, "GET", "/v1/agents/sessions", "acme", nil)
	if code != 200 || !strings.Contains(string(body), `"id":"sess_acme"`) ||
		!strings.Contains(string(body), `"https://acme.share.hanzo.ai"`) {
		t.Fatalf("the org's session did not survive the upgrade: %d %s", code, body)
	}
}
