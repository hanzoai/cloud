package reward

import (
	"context"
	"testing"

	"github.com/hanzoai/reward/rule"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// The level table the galaxy asks for is the program's own, with a band left empty settled
// to the level's name as Program.LevelOf settles it, and only the platform asks.
func TestLevelsAreTheProgramsTable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		out, err := r.s.levels(platformCtx(), &cloud.Unit{})
		if err != nil {
			t.Fatalf("levels: %v", err)
		}
		p, err := r.s.eng.Program(platformCtx(), program)
		if err != nil {
			t.Fatalf("program: %v", err)
		}
		if len(out.Levels) != len(p.Levels) || len(out.Levels) == 0 {
			t.Fatalf("%d levels, the program has %d", len(out.Levels), len(p.Levels))
		}
		for i, l := range p.Levels {
			at, _ := p.LevelOf(l.MinLifetimePoints)
			if got := out.Levels[i]; got != (client.RewardLevel{Level: l.Level, Band: at.Band, From: l.MinLifetimePoints}) || got.Band == "" {
				t.Errorf("level %d: %+v, the program has %+v", i, got, l)
			}
		}
		// The seed's: 100 levels, level n from 10·n² points, bands of ten.
		if len(out.Levels) == 100 {
			if out.Levels[10] != (client.RewardLevel{Level: 10, Band: "Wave", From: 1000}) || out.Levels[99] != (client.RewardLevel{Level: 99, Band: "Singularity", From: 98010}) {
				t.Errorf("the seeded table is not read: %+v %+v", out.Levels[10], out.Levels[99])
			}
		}

		// Staff edit the table and the read follows it; a level with no band is its name's.
		p.Levels = []rule.Level{{Level: 1, Name: "Newcomer", MinLifetimePoints: 0}, {Level: 2, Name: "Regular", Band: "Locals", MinLifetimePoints: 50}}
		if _, err := r.s.eng.SaveProgram(platformCtx(), program, *p, "staff"); err != nil {
			t.Fatalf("save: %v", err)
		}
		out, err = r.s.levels(platformCtx(), &cloud.Unit{})
		if err != nil || len(out.Levels) != 2 || out.Levels[0].Band != "Newcomer" || out.Levels[1].Band != "Locals" || out.Levels[1].From != 50 {
			t.Fatalf("an edited table is not read: %+v %v", out, err)
		}

	})
}

func TestOnlyThePlatformReadsTheLevels(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		for _, org := range []string{"acme", ""} {
			if _, err := r.s.levels(cloud.For(context.Background(), org), &cloud.Unit{}); err == nil {
				t.Errorf("%q read the level table", org)
			}
		}
		if _, err := r.s.levels(platformCtx(), &cloud.Unit{}); err != nil {
			t.Errorf("the platform was refused: %v", err)
		}

	})
}
