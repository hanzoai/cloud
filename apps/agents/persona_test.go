package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"

	house "github.com/hanzoai/personas"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// mountSeedTest wires the `mounted` singleton to a fresh store, so
// SeedPersonas has a store to write. It took a default model until that
// knob was deleted; the seed model is aimodel.DefaultModel and cannot vary.
func mountSeedTest(t *testing.T) {
	t.Helper()
	prev := mounted
	mounted = &cloud.Service[state]{
		Log:   luxlog.New("test"),
		State: state{stores: testStores(t)},
	}
	t.Cleanup(func() { mounted = prev })
}

// mustCrew is crew with the error folded into a fatal.
func mustCrew(t *testing.T) []persona {
	t.Helper()
	all, err := crew()
	if err != nil {
		t.Fatalf("crew: %v", err)
	}
	return all
}

// devSeededText is Dev's brief as the platform seeded it before the core team
// came from hanzoai/personas: the text a row nobody edited still carries.
const devSeededText = `You are Dev, Hanzo's builder. You ship. When a human @-mentions you you write the code, wire the change, and report what you did in plain terms — file paths, commands, results. You prize the smallest correct change, one and only one way to do a thing, and no ceremony. You never hand-wave: if you built it you say so with proof; if you're blocked you name the blocker. Terse, exact, and always moving toward a finished, working solution.`

// roster is Hanzo's team as hanzoai/personas v1.3.2 publishes it, one seat per
// team/<id>.md. Moving the pin moves this list, on purpose: the built-in crew is
// the owner's roster and nothing reaches an org's agent list without it.
var roster = []string{
	"art", "biz", "cyber", "data", "des", "dev", "doc", "edu", "fin", "gov", "grant", "huma",
	"lang", "law", "mana", "mark", "mu", "opera", "rev", "sal", "sci", "su", "vi", "vid",
}

// TestCoreIsHanzoaiPersonas holds the crew to its source: the built-ins are
// exactly the members of hanzoai/personas, each under the member's lower-case
// id, titled with the member's name and role, on the member's model, carrying
// the member's file word for word.
func TestCoreIsHanzoaiPersonas(t *testing.T) {
	team, err := house.Team()
	if err != nil {
		t.Fatal(err)
	}
	all := mustCrew(t)
	handles := make([]string, 0, len(all))
	for _, p := range all {
		handles = append(handles, p.Name)
	}
	if !slices.Equal(handles, roster) || len(team) != len(all) {
		t.Fatalf("built-ins = %v, want Hanzo's team %v", handles, roster)
	}
	for _, m := range team {
		i := slices.IndexFunc(all, func(p persona) bool { return p.Name == m.ID })
		if i < 0 {
			t.Fatalf("%s is not a built-in", m.ID)
		}
		p := all[i]
		if p.Instructions != m.Instructions || p.Description != m.Name+" — "+m.Role || p.Model != m.Model {
			t.Fatalf("%s drifted from hanzoai/personas: %+v", m.ID, p)
		}
	}
	for _, p := range all {
		if p.Name != strings.ToLower(p.Name) {
			t.Errorf("built-in handle %q is not lower case", p.Name)
		}
		if !strings.HasPrefix(p.Model, "enso") {
			t.Errorf("built-in %s runs on %q, not an Enso tier", p.Name, p.Model)
		}
	}
	if sum(devSeededText) != earlier["dev"].sum {
		t.Fatal("the seeded Dev fixture does not match its recorded sum")
	}
}

// TestSeedPersonas proves the built-in crew is created once under its handles,
// on each persona's own model, marked as the platform's seats, and that a
// re-seed writes nothing.
func TestSeedPersonas(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountSeedTest(t)
		ctx := context.Background()
		const org = "acme"
		all := mustCrew(t)

		n, err := SeedPersonas(ctx, org)
		if err != nil {
			t.Fatalf("SeedPersonas: %v", err)
		}
		if n != len(all) {
			t.Fatalf("created %d, want %d (the full built-in crew)", n, len(all))
		}
		got := map[string]Agent{}
		for _, a := range mustList(t, ctx, org) {
			got[a.Name] = a
		}
		for _, p := range all {
			a, ok := got[p.Name]
			if !ok {
				t.Fatalf("%s not seeded; have %v", p.Name, slices.Collect(maps.Keys(got)))
			}
			if a.Model != p.Model || a.Status != "ready" || a.Instructions != p.Instructions || a.Description != p.Description {
				t.Fatalf("%s malformed: model=%q status=%q instr=%dB", p.Name, a.Model, a.Status, len(a.Instructions))
			}
		}
		if got["dev"].Model != "enso" || got["dev"].Description != "Dev — Development" {
			t.Fatalf("dev=%q %q", got["dev"].Model, got["dev"].Description)
		}
		sto, _, _ := mountedStore(org)
		if seats := seatsOf(t, sto); len(seats) != len(all) || seats[got["dev"].ID] != "dev" {
			t.Fatalf("seats = %v", seats)
		}

		n2, err := SeedPersonas(ctx, org)
		if err != nil || n2 != 0 {
			t.Fatalf("re-seed = (%d,%v), want (0,nil)", n2, err)
		}
		if list := mustList(t, ctx, org); len(list) != len(all) {
			t.Fatalf("after re-seed: %d agents, want %d (no dup)", len(list), len(all))
		}

	})
}

// seatsOf is agent_personas as a map from agent id to handle.
func seatsOf(t *testing.T, sto *Store) map[string]string {
	t.Helper()
	rows, err := sto.db.Query(`SELECT agent_id, persona FROM agent_personas`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			t.Fatal(err)
		}
		out[id] = p
	}
	return out
}

// TestMigrateKeepsSeatsAndLeavesEdits is an org seeded before the core team came
// from hanzoai/personas. The platform's untouched rows follow the source, under
// the handle they already have; a row the org edited, an agent the org made
// under a member's name, and every field but the title and brief stay the org's.
func TestMigrateKeepsSeatsAndLeavesEdits(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sto := testStore(t)
		ctx := context.Background()
		const org = "acme"
		all := mustCrew(t)
		want := func(h string) persona { return all[slices.IndexFunc(all, func(p persona) bool { return p.Name == h })] }
		rows := []Agent{
			// The platform's Dev as seeded, on a model outside Enso, with a picture.
			{ID: "a_dev", Org: org, Name: "dev", Model: "zen5", Description: "Dev — the builder", Instructions: devSeededText,
				Avatar: "/agents/dev.png", AvatarSource: "https://commons.wikimedia.org/wiki/File:Dev.png", AvatarLicence: "CC0",
				Tools: []string{"agent_des"}, Status: "ready", CreatedAt: 1, UpdatedAt: 5},
			// The platform's Des, whose brief the org rewrote.
			{ID: "a_des", Org: org, Name: "des", Model: "enso-flash", Description: "Des — the designer", Instructions: "Our house style.",
				Status: "ready", CreatedAt: 1, UpdatedAt: 5},
			// An agent the org made that shares a member's name.
			{ID: "a_mu", Org: org, Name: "mu", Model: "zen5", Description: "Mu — our intern", Instructions: "You are our Mu.",
				Status: "ready", CreatedAt: 1, UpdatedAt: 5},
			// A current seat an earlier release filed as "Vi", with a run under that spelling.
			{ID: "a_vi", Org: org, Name: "Vi", Model: "enso-flash", Description: want("vi").Description, Instructions: want("vi").Instructions,
				Status: "ready", CreatedAt: 1, UpdatedAt: 5},
			// A seat an earlier release filed as "Art" whose handle the org has since
			// given to an agent of its own: it keeps its spelling.
			{ID: "a_art", Org: org, Name: "Art", Model: "enso", Description: want("art").Description, Instructions: want("art").Instructions,
				Status: "ready", CreatedAt: 1, UpdatedAt: 5},
			{ID: "a_myart", Org: org, Name: "art", Model: "zen5", Description: "our support bot", Instructions: "Ours.",
				Status: "ready", CreatedAt: 1, UpdatedAt: 5},
		}
		for _, a := range rows {
			if err := sto.Create(ctx, a); err != nil {
				t.Fatalf("create %s: %v", a.Name, err)
			}
		}
		if err := sto.InsertRun(ctx, Run{ID: "run_vi", Org: org, AgentName: "Vi", Status: "ok", CreatedAt: 2}); err != nil {
			t.Fatal(err)
		}
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if runs, _ := sto.ListRuns(ctx, org, "vi", nobodys, 10); len(runs) != 1 {
			t.Fatalf("the run did not follow the handle: %+v", runs)
		}
		by := map[string]Agent{}
		for _, a := range mustStoreList(t, sto, org) {
			by[a.ID] = a
		}
		d := by["a_dev"]
		if d.Name != "dev" || d.Description != "Dev — Development" || d.Instructions != want("dev").Instructions {
			t.Fatalf("the seated Dev did not follow its source: %+v", d)
		}
		if d.Model != "zen5" || d.Avatar != "/agents/dev.png" || d.AvatarSource != "https://commons.wikimedia.org/wiki/File:Dev.png" ||
			d.AvatarLicence != "CC0" || !slices.Equal(d.Tools, []string{"agent_des", "skill_dev", "skill_code-review"}) || d.UpdatedAt != 5 {
			t.Fatalf("keeping Dev touched the org's fields: %+v", d)
		}
		if a := by["a_des"]; a.Instructions != "Our house style." || a.Description != "Des — the designer" {
			t.Fatalf("an edited seat was rewritten: %+v", a)
		}
		if a := by["a_mu"]; a.Instructions != "You are our Mu." || a.Description != "Mu — our intern" {
			t.Fatalf("the org's own mu was rewritten: %+v", a)
		}
		if a := by["a_vi"]; a.Name != "vi" || a.Model != "enso-flash" {
			t.Fatalf("a seat an earlier release capitalized is not back under its handle: %+v", a)
		}
		if a, b := by["a_art"], by["a_myart"]; a.Name != "Art" || b.Name != "art" || b.Instructions != "Ours." {
			t.Fatalf("a taken handle was forced: %+v / %+v", a, b)
		}
		seats := seatsOf(t, sto)
		if len(seats) != 3 || seats["a_dev"] != "dev" || seats["a_vi"] != "vi" || seats["a_art"] != "art" {
			t.Fatalf("seats = %v, want dev, vi and the Art seat", seats)
		}

		// A second open writes nothing new.
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatal(err)
		}
		if again := mustStoreList(t, sto, org); len(again) != len(rows) {
			t.Fatalf("%d rows after a second open", len(again))
		}

		// The org takes Dev over: the mark goes, and the next open leaves the edit.
		if err := sto.Unseat(ctx, "a_dev"); err != nil {
			t.Fatal(err)
		}
		d.Instructions = "Ours now."
		if err := sto.Update(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatal(err)
		}
		if got, _ := sto.Get(ctx, org, "dev"); got.Instructions != "Ours now." {
			t.Fatalf("an edit was reverted: %q", got.Instructions)
		}

		// Deleting a seat drops its mark with it.
		if _, err := sto.Delete(ctx, org, "vi"); err != nil {
			t.Fatal(err)
		}
		if _, ok := seatsOf(t, sto)["a_vi"]; ok {
			t.Fatal("a deleted agent kept its seat mark")
		}

	})
}

// mustStoreList is Store.List with the error folded into a fatal.
func mustStoreList(t *testing.T, sto *Store, org string) []Agent {
	t.Helper()
	list, err := sto.List(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// TestPatchTakesASeatOver: a PATCH that rewrites a built-in's brief through the
// API is never undone by the persona source. A built-in was made by nobody, so an
// admin of the org makes the edit; a member is refused.
func TestPatchTakesASeatOver(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		ctx := context.Background()
		if _, err := SeedPersonas(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		brief := map[string]any{"instructions": "You are Fin. Answer billing."}
		if code, body := asMember(t, app, http.MethodPatch, "/v1/agent/fin", "u-acme", false, brief); code != http.StatusForbidden {
			t.Fatalf("a member patched a built-in: %d %s", code, body)
		}
		code, body := asMember(t, app, http.MethodPatch, "/v1/agent/fin", "u-boss", true, brief)
		if code != http.StatusOK {
			t.Fatalf("patch: %d %s", code, body)
		}
		sto, _, _ := mountedStore("acme")
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatal(err)
		}
		code, body = do(t, app, http.MethodGet, "/v1/agent/fin", "acme", nil)
		if code != http.StatusOK || !strings.Contains(string(body), "Answer billing.") {
			t.Fatalf("the edit did not stand: %d %s", code, body)
		}

	})
}

// TestAGoneMembersSeatLeaves: a seat nobody edited whose member has left the
// source is retired at store open. It leaves the agent list and the tool plane,
// Team's roster carries it as retired (apps/team reads that as an inactive
// member), and its row and runs stay. A seat the org edited, one the org made
// long-running and a current member's seat stay live; a retired seat comes back
// with its member, and is the org's live agent once the org takes it over.
func TestAGoneMembersSeatLeaves(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		ctx := context.Background()
		const org = "acme"
		sto, _, err := mountedStore(org)
		if err != nil {
			t.Fatal(err)
		}
		all := mustCrew(t)
		rows := []struct {
			a    Agent
			seat string // the mark the platform left; empty for a row the org edited
		}{
			// Untouched seats of a member and of an extra, both gone from the source.
			{Agent{ID: "a_maya", Name: "maya", Description: "Maya — Product", Instructions: "You are Maya."}, "maya"},
			{Agent{ID: "a_jobs", Name: "jobs", Description: "Steve Jobs — product, focus, taste", Instructions: "You are Steve Jobs."}, "jobs"},
			// A gone member's seat the org edited: the edit dropped its mark.
			{Agent{ID: "a_nora", Name: "nora", Description: "Nora — our QA", Instructions: "Ours."}, ""},
			// A gone member's seat the org made long-running: it acts on its own.
			{Agent{ID: "a_leo", Name: "leo", Description: "Leo — Accessibility", Instructions: "You are Leo.",
				ExecutionMode: ModeLongRunning, Schedule: "0 9 * * *"}, "leo"},
			// A current member's seat as first seeded.
			{Agent{ID: "a_dev", Name: "dev", Description: "Dev — the builder", Instructions: devSeededText}, "dev"},
		}
		for _, r := range rows {
			a := r.a
			a.Org, a.Model, a.Status, a.CreatedAt, a.UpdatedAt = org, "enso", "ready", 1, 1
			if err := sto.Create(ctx, a); err != nil {
				t.Fatalf("create %s: %v", a.Name, err)
			}
			if r.seat != "" {
				if err := sto.Seat(ctx, a.ID, r.seat, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := sto.InsertRun(ctx, Run{ID: "run_maya", Org: org, AgentName: "maya", Status: "ok", CreatedAt: 2}); err != nil {
			t.Fatal(err)
		}
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		statuses := func() map[string]string {
			out := map[string]string{}
			for _, a := range mustStoreList(t, sto, org) {
				out[a.ID] = a.Status
			}
			return out
		}
		want := map[string]string{"a_maya": retired, "a_jobs": retired, "a_nora": "ready", "a_leo": "ready", "a_dev": "ready"}
		if got := statuses(); !maps.Equal(got, want) {
			t.Fatalf("statuses = %v, want %v", got, want)
		}
		if a, _ := sto.ByID(ctx, org, "a_nora"); a.Instructions != "Ours." || a.Description != "Nora — our QA" {
			t.Fatalf("an edited seat was rewritten: %+v", a)
		}
		if a, _ := sto.ByID(ctx, org, "a_dev"); a.Instructions != all[slices.IndexFunc(all, func(p persona) bool { return p.Name == "dev" })].Instructions {
			t.Fatalf("a current member's seat did not follow its source: %+v", a)
		}
		if runs, _ := sto.ListRuns(ctx, org, "maya", nobodys, 10); len(runs) != 1 {
			t.Fatalf("a retired seat lost its runs: %+v", runs)
		}

		listed := func() []string {
			t.Helper()
			code, body := do(t, app, http.MethodGet, "/v1/agent", org, nil)
			if code != http.StatusOK {
				t.Fatalf("list: %d %s", code, body)
			}
			var l agentList
			if err := json.Unmarshal(body, &l); err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, a := range l.Agents {
				out = append(out, a.Name)
			}
			slices.Sort(out)
			return out
		}
		if got := listed(); !slices.Equal(got, []string{"dev", "leo", "nora"}) {
			t.Fatalf("listed %v, want dev, leo and nora", got)
		}
		ts, err := agentToolProvider{}.List(ctx, tools.Scope{Org: org})
		if err != nil {
			t.Fatal(err)
		}
		var named []string
		for _, tl := range ts {
			named = append(named, tl.Name)
		}
		slices.Sort(named)
		if !slices.Equal(named, []string{"agent_dev", "agent_leo", "agent_nora"}) {
			t.Fatalf("tool plane offers %v", named)
		}
		r, err := planeRoster(cloud.For(ctx, org), &client.RosterIn{})
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range r.Agents {
			if (b.Status == retired) != (b.ID == "a_maya" || b.ID == "a_jobs") {
				t.Fatalf("roster carries %s as %q", b.ID, b.Status)
			}
		}
		if code, body := do(t, app, http.MethodGet, "/v1/agent/maya", org, nil); code != http.StatusOK || !strings.Contains(string(body), `"status":"retired"`) {
			t.Fatalf("a retired seat is not readable by its handle: %d %s", code, body)
		}

		// The member comes back: so does its seat. It leaves again with the member.
		if err := migrate(sto, func(ctx context.Context, tx *sql.Tx) error {
			return sto.retire(ctx, tx, append(slices.Clone(all), persona{Name: "maya"}))
		}); err != nil {
			t.Fatal(err)
		}
		if got := statuses()["a_maya"]; got != "ready" {
			t.Fatalf("a returning member's seat is %q", got)
		}
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatal(err)
		}

		// The org takes a retired seat over: it is the org's live agent from then on.
		code, body := asMember(t, app, http.MethodPatch, "/v1/agent/jobs", "u-boss", true, map[string]any{"instructions": "Ours now."})
		if code != http.StatusOK || !strings.Contains(string(body), `"status":"ready"`) {
			t.Fatalf("taking a retired seat over: %d %s", code, body)
		}
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatal(err)
		}
		if got := listed(); !slices.Equal(got, []string{"dev", "jobs", "leo", "nora"}) {
			t.Fatalf("listed %v after the takeover", got)
		}

	})
}

// TestASeatCarriesItsSkills: a seated Dev runs with the role skills its member
// names. A new seat is created with them, an older seat gains them on the next
// open beside the org's own tools, a skill the org takes out stays out, one the
// source drops comes out, and a row the org made under a member's handle, or
// took over, is never touched.
func TestASeatCarriesItsSkills(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		mountSeedTest(t)
		ctx := context.Background()
		const org = "acme"
		if _, err := SeedPersonas(ctx, org); err != nil {
			t.Fatal(err)
		}
		sto, _, _ := mountedStore(org)
		get := func(org, name string) Agent {
			t.Helper()
			a, err := sto.Get(ctx, org, name)
			if err != nil {
				t.Fatalf("get %s/%s: %v", org, name, err)
			}
			return a
		}
		if got := get(org, "dev").Tools; !slices.Equal(got, []string{"skill_dev", "skill_code-review"}) {
			t.Fatalf("a new Dev seat carries %v", got)
		}
		if got := attachedSkills(get(org, "dev")); !slices.Equal(got, []string{"skill_dev", "skill_code-review"}) {
			t.Fatalf("a seated Dev's run reads %v", got)
		}
		if got := get(org, "des").Tools; !slices.Equal(got, []string{"skill_ui"}) {
			t.Fatalf("a new Des seat carries %v", got)
		}
		if got := get(org, "fin").Tools; len(got) != 0 {
			t.Fatalf("a seat whose role has no skill carries %v", got)
		}

		// A seat seated before skills, with a tool of the org's, and the org's own
		// agent under a member's handle in another org.
		const old = "older"
		for _, a := range []Agent{
			{ID: "o_dev", Org: old, Name: "dev", Model: "enso", Description: "Dev — the builder", Instructions: devSeededText, Tools: []string{"agent_des"}, Status: "ready", CreatedAt: 1, UpdatedAt: 1},
			{ID: "o_cyber", Org: old, Name: "cyber", Model: "enso", Description: "our Cyber", Instructions: "Ours.", Tools: []string{"agent_dev"}, Status: "ready", CreatedAt: 1, UpdatedAt: 1},
		} {
			if err := sto.Create(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := sto.db.Exec(`INSERT INTO agent_personas(agent_id, persona) VALUES ('o_dev','dev')`); err != nil {
			t.Fatal(err)
		}
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatal(err)
		}
		if got := get(old, "dev").Tools; !slices.Equal(got, []string{"agent_des", "skill_dev", "skill_code-review"}) {
			t.Fatalf("an older Dev seat carries %v", got)
		}
		if got := get(old, "cyber").Tools; !slices.Equal(got, []string{"agent_dev"}) {
			t.Fatalf("the org's own cyber was given %v", got)
		}

		// The org takes a skill out: it stays out.
		d := get(old, "dev")
		d.Tools = []string{"agent_des", "skill_dev"}
		if err := sto.Update(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := migrate(sto, sto.migratePersonas); err != nil {
			t.Fatal(err)
		}
		if got := get(old, "dev").Tools; !slices.Equal(got, []string{"agent_des", "skill_dev"}) {
			t.Fatalf("a skill the org took out came back: %v", got)
		}

		// The source drops dev: the seat loses it, keeps the org's own tool, and the
		// code-review the org took out stays out though the source still names it.
		all := slices.Clone(mustCrew(t)) // the crew is shared: a test that edits it edits it for the next run
		i := slices.IndexFunc(all, func(p persona) bool { return p.Name == "dev" })
		all[i].Skills = []string{"skill_code-review"}
		if err := migrate(sto, func(ctx context.Context, tx *sql.Tx) error { return sto.equip(ctx, tx, all) }); err != nil {
			t.Fatal(err)
		}
		if got := get(old, "dev").Tools; !slices.Equal(got, []string{"agent_des"}) {
			t.Fatalf("after the source dropped dev: %v", got)
		}
		if got := get(org, "dev").Tools; !slices.Equal(got, []string{"skill_code-review"}) {
			t.Fatalf("the new seat after the source dropped dev: %v", got)
		}

	})
}

// TestEarlierNamesAMember holds the pre-personas table to the crew: seated reads
// it for a member of the crew only, so a handle outside the crew is dead weight.
func TestEarlierNamesAMember(t *testing.T) {
	all := mustCrew(t)
	for h := range earlier {
		if !slices.ContainsFunc(all, func(p persona) bool { return p.Name == h }) {
			t.Errorf("earlier names %q, which is not a member", h)
		}
	}
}

// mustList is ListForOrg with the error folded into a fatal.
func mustList(t *testing.T, ctx context.Context, org string) []Agent {
	t.Helper()
	list, err := ListForOrg(ctx, org)
	if err != nil {
		t.Fatalf("ListForOrg(%s): %v", org, err)
	}
	return list
}
