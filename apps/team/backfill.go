// Copyright © 2026 Hanzo AI. MIT License.

package team

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/orm/query"
)

// Moving the space roster into IAM.
//
// Team held members(workspace_id, user_id, role, …) beside IAM's own membership,
// so who may act in a space had two answers. IAM is the one now, and these
// rows are what the estate already believes — they have to arrive there before
// the table goes, or every live space loses its roster on the deploy that
// stops reading it.

// backfill copies every member row into IAM and drops the table once they all
// land.
//
// It runs at mount and is idempotent twice over: the grant write never downgrades
// an existing role, and a dropped table makes the whole thing a no-op. A row that
// IAM refuses leaves the table in place and the next boot tries again — the one
// outcome that must never happen is dropping rows IAM did not take.
func backfill(ctx context.Context, s *accountStore) error {
	// Asked, not inferred from a failed read: "the table is gone" and "the read
	// broke" are the same error string and opposite situations — one is finished,
	// the other must not drop anything.
	if !s.hasMembers(ctx) {
		return nil
	}
	rows, err := s.pendingGrants(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return s.dropMembers(ctx)
	}
	for _, r := range rows {
		if r.Org == "" || r.SpaceUUID == "" || r.Account == "" {
			continue // a row naming no scope or nobody grants nothing
		}
		role := r.Role
		if role == "" {
			role = "member"
		}
		if _, err := iam.IAMGrant(cloud.For(ctx, r.Org), &client.GrantIn{
			User: r.Account, Space: r.SpaceUUID, Role: role,
		}); err != nil {
			return fmt.Errorf("team: backfill %s/%s: %w", r.Org, r.Account, err)
		}
	}
	return s.dropMembers(ctx)
}

// grant is one member row resolved to the scope IAM files it against.
type grant struct {
	Org       string `db:"owner_org"`
	SpaceUUID string `db:"uuid"`
	Account   string `db:"user_id"`
	Role      string `db:"role"`
}

// pendingGrants reads every member row with its space's org and uuid. It
// returns an error when the table is absent, which is how a second boot knows
// there is nothing left to move.
func (s *accountStore) pendingGrants(ctx context.Context) ([]grant, error) {
	var out []grant
	err := s.db.Select("w.owner_org", "w.uuid", "m.user_id", "m.role").
		From("members m").
		// `members` is the LEGACY table and workspace_id is the column it was
		// written with. It is read once and dropped, never created again, so the
		// name stays what is actually on disk — renaming it here reads nothing on
		// every deployment that has rows to move, which is the only kind that
		// matters.
		InnerJoin("spaces w", query.NewExp("w.id = m.workspace_id")).
		Where(query.NewExp("m.active = 1")).
		WithContext(ctx).All(&out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// dropMembers removes the table. Called only after every row reached IAM.
func (s *accountStore) dropMembers(ctx context.Context) error {
	_, err := s.db.NewQuery("DROP TABLE IF EXISTS members").WithContext(ctx).Execute()
	return err
}

// hasMembers reports whether the legacy roster table is still present.
func (s *accountStore) hasMembers(ctx context.Context) bool {
	var n int
	err := s.db.NewQuery(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='members'`).
		WithContext(ctx).Row(&n)
	return err == nil && n > 0
}
