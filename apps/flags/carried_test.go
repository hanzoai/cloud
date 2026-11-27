// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package flags

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// asked is one switch as a reader asks it: its value and whether it is saved.
func asked(c *Client, key string) (string, bool) {
	c.invalidate()
	v, source := c.resolve(Def{Key: key, Type: TypeString})
	return v, source == "flags"
}

// A PLAN THAT WAS SERVED ON ITS SHIPPED DEFAULTS KEEPS ITS FIGURES ACROSS THE
// UPGRADE. A store that holds nothing for a carried plan is the store of every
// deployment before this release; the migration saves each of its nine figures,
// and the model policy with each costly family's share, so the plan is named by
// what is saved and reads exactly what it read.
func TestACarriedPlanKeepsItsFiguresAcrossTheUpgrade(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		for plan := range CarriedPlans {
			for _, f := range PlanFields {
				if _, set := asked(c, PlanKey(f, plan)); set {
					t.Fatalf("%s of %s is saved before the migration ran", f, plan)
				}
			}
		}
		wrote, err := c.carry(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if want := len(CarriedPlans)*len(PlanFields) + 1; len(wrote) != want {
			t.Fatalf("the migration wrote %d switches, want %d: nine a plan and the model policy", len(wrote), want)
		}
		for plan, seed := range CarriedPlans {
			for field, want := range map[string]string{
				"session": strconv.Itoa(seed.Session), "day": strconv.Itoa(seed.Day),
				"premium": strconv.Itoa(seed.Premium), "ours": strconv.Itoa(seed.Ours),
				"premium_window": "0", "premium_window_hours": "0", "ours_window": "0", "ours_window_hours": "0",
				"overage": "true",
			} {
				got, set := asked(c, PlanKey(field, plan))
				if !set || got != want {
					t.Errorf("%s of %s reads %q (saved %v), want %q saved", field, plan, got, set, want)
				}
			}
		}
		policy, set := asked(c, ModelPolicy)
		if !set {
			t.Fatal("the model policy is not saved after the migration")
		}
		var rules map[string]struct {
			Caps map[string]struct {
				PeriodCents int `json:"periodCents"`
			} `json:"caps"`
		}
		if err := json.Unmarshal([]byte(policy), &rules); err != nil {
			t.Fatal(err)
		}
		if got := rules["anthropic/claude-opus*"].Caps["max-20x"].PeriodCents; got != 3000 {
			t.Errorf("Max 20x's share of a costly family is %d¢ after the migration, want the 3000¢ it was", got)
		}

		// It is one migration: a second run finds every switch held and writes nothing.
		again, err := c.carry(context.Background())
		if err != nil || len(again) != 0 {
			t.Fatalf("a second run wrote %d switches (err %v), want none", len(again), err)
		}
		st, err := c.platform()
		if err != nil {
			t.Fatal(err)
		}
		row, found, err := st.Get(PlanKey("session", "dev"))
		if err != nil || !found {
			t.Fatalf("dev's session window: found %v err %v", found, err)
		}
		if row.Version != 1 || row.UpdatedBy != carrier {
			t.Errorf("dev's session window is version %d by %q, want written once by %q", row.Version, row.UpdatedBy, carrier)
		}
	})
}

// AN ADMIN'S EDIT WINS, whenever it was made. A figure saved before the upgrade
// is left as saved while the plan's other figures are carried beside it; a figure
// an admin saves after the migration is never put back by a later boot.
func TestAnAdminsEditWinsOverTheMigration(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		save := func(key, value string) {
			t.Helper()
			def, err := Valued(value, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := SetPlatformSwitch(key, def, "z@hanzo.ai"); err != nil {
				t.Fatal(err)
			}
		}
		// Before the upgrade: an admin had set Max 20x's premium allowance and the
		// model policy; every other figure of the plan stood at its default.
		save(PlanKey("premium", "max-20x"), "12345")
		save(ModelPolicy, `{"kai":{"class":"ours","grantsPay":true}}`)

		wrote, err := c.carry(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := wrote[PlanKey("premium", "max-20x")]; ok {
			t.Fatal("the migration wrote over a figure an admin had saved")
		}
		if _, ok := wrote[ModelPolicy]; ok {
			t.Fatal("the migration wrote over the model policy an admin had saved")
		}
		if got, _ := asked(c, PlanKey("premium", "max-20x")); got != "12345" {
			t.Fatalf("Max 20x's premium allowance reads %q, want the admin's 12345", got)
		}
		if got, set := asked(c, PlanKey("ours", "max-20x")); !set || got != strconv.Itoa(CarriedPlans["max-20x"].Ours) {
			t.Fatalf("Max 20x's allowance of Hanzo's models reads %q (saved %v), want its default carried", got, set)
		}
		if got, _ := asked(c, ModelPolicy); !strings.Contains(got, `"kai"`) || strings.Contains(got, "claude-opus") {
			t.Fatalf("the model policy reads %q, want the admin's", got)
		}

		// After the migration: an admin edits a carried figure, and the next boot
		// leaves it.
		save(PlanKey("session", "dev"), "7")
		if again, err := c.carry(context.Background()); err != nil || len(again) != 0 {
			t.Fatalf("the next boot wrote %d switches (err %v), want none", len(again), err)
		}
		if got, _ := asked(c, PlanKey("session", "dev")); got != "7" {
			t.Fatalf("dev's session window reads %q after the next boot, want the admin's 7", got)
		}
	})
}

// A plan that stopped part-way is finished by the next run: a migration cut short
// leaves a plan part-saved, and the plan is whole once it has run again.
func TestAMigrationCutShortIsFinishedByTheNext(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := newTestClient(t)
		st, err := c.platform()
		if err != nil {
			t.Fatal(err)
		}
		def, _ := Valued("50", false)
		if ok, err := st.keep(context.Background(), PlanKey("day", "team"), def, carrier); err != nil || !ok {
			t.Fatalf("keep: wrote %v err %v", ok, err)
		}
		if ok, err := st.keep(context.Background(), PlanKey("day", "team"), def, carrier); err != nil || ok {
			t.Fatalf("keep over a held switch: wrote %v err %v, want it left", ok, err)
		}
		if _, err := c.carry(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, f := range PlanFields {
			if _, set := asked(c, PlanKey(f, "team")); !set {
				t.Errorf("%s of team is not saved after the rerun", f)
			}
		}
	})
}

// No figure of a plan decides a request from this package's table: outside the
// migration and tests, nothing in the tree names CarriedPlans or CarriedValues.
func TestOnlyTheMigrationReadsTheCarriedFigures(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "node_modules" || n == "webui" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Base(path) == "carried.go" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "Carried") {
			return nil
		}
		code := string(src)
		for _, name := range []string{"CarriedPlans", "CarriedValues", "flags.Carried"} {
			if strings.Contains(code, name) {
				t.Errorf("%s names %s: a plan's usage is what is saved for it, read from the flag store", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
