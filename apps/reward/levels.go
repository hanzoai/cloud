package reward

// levels.go — the level table, asked for by the public galaxy (apps/leaderboard), which
// draws a star in the band its points reach in the table staff keep rather than in a
// copy of the seed's. The program is read as the levels are read everywhere else:
// through Program.LevelOf, which settles a band that was left empty.

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeLevels publishes the ask on the internal plane. routes calls it.
func (s *state) exposeLevels() {
	cloud.Plane().Post("/reward/levels", s.levels,
		zip.WithOperationID(client.RewardLevels),
		zip.WithSummary("Answer the level table of the rewards program"))
}

// levels answers the program's level table: each level's number, its band and the
// lifetime points it begins at, lowest first. It names no person and no balance. Only
// the platform asks.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (s *state) levels(ctx context.Context, _ *cloud.Unit) (*client.RewardLevelTable, error) {
	if !platform(ctx) {
		return nil, zip.ErrForbidden("reward: only the platform reads the level table")
	}
	p, err := s.eng.Program(ctx, program)
	if err != nil {
		return nil, refusal(err)
	}
	out := &client.RewardLevelTable{Levels: make([]client.RewardLevel, 0, len(p.Levels))}
	for _, l := range p.Levels {
		at, _ := p.LevelOf(l.MinLifetimePoints)
		out.Levels = append(out.Levels, client.RewardLevel{Level: l.Level, Band: at.Band, From: l.MinLifetimePoints})
	}
	return out, nil
}
