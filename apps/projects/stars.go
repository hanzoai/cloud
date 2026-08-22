package projects

import (
	"context"
	"net/http"
	"time"

	"github.com/zap-proto/zip"
)

// Starring — the one per-PERSON fact in a store that is otherwise per-org.
//
// Everything else about a project belongs to the org: who can see it, who is
// billed for it, who may deploy it. Authorship deliberately is too (see the note
// on Visibility in store.go — a second field restating the org boundary could
// only ever disagree with it, and once did).
//
// A star restates nothing. It is not access, not ownership and not authorship:
// it is one person saying "show me this one first", and two people in the same
// org can disagree about it without either being wrong. That is exactly why it
// needs its own table keyed by user, and why it is the ONLY one of the three
// sidebar filters that turned out to be a real feature — "created by me" and
// "shared with me" are the org boundary seen at the wrong grain.
//
// ORG IS IN THE KEY, not just the user. A star is a row about a project, and
// every row about a project in this store is org-isolated; leaving org out would
// make the stars table the one place where a user id crosses tenants.

// starsDDL is applied by migrate(). Additive and idempotent, like every other
// table here.
const starsDDL = `
CREATE TABLE IF NOT EXISTS project_stars (
  org        TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  project_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (org, user_id, project_id)
);
CREATE INDEX IF NOT EXISTS ix_stars_org_user ON project_stars(org, user_id);
`

// Star marks a project for one person. Idempotent: starring twice is starring
// once, so a double-click or a retried request is not an error.
func (s *Store) Star(ctx context.Context, org, user, projectID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO project_stars (org, user_id, project_id, created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(org, user_id, project_id) DO NOTHING`,
		org, user, projectID, time.Now().Unix())
	return err
}

// Unstar removes it. Also idempotent — unstarring what was never starred is the
// state the caller asked for, not a failure.
func (s *Store) Unstar(ctx context.Context, org, user, projectID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_stars WHERE org = ? AND user_id = ? AND project_id = ?`,
		org, user, projectID)
	return err
}

// StarredBy is the set of project ids this person has starred in this org.
//
// A SET, fetched once, rather than a per-project lookup: the list endpoint
// renders every project the org has, and asking the database one question per
// row is how a list of forty projects becomes forty-one queries.
//
// An empty user (no validated principal) yields an empty set rather than an
// error — the caller is about to render an unstarred list, which is the honest
// answer for "nobody in particular is asking".
func (s *Store) StarredBy(ctx context.Context, org, user string) (map[string]bool, error) {
	out := map[string]bool{}
	if org == "" || user == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id FROM project_stars WHERE org = ? AND user_id = ?`, org, user)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return out, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// star bookmarks a project for the person calling, and answers whether it is
// starred afterwards.
//
// The star is YOURS: it is keyed by you as well as by the project, so two people
// see two answers for the same one and starring it says nothing about anybody
// else's list. Starring a project you have already starred leaves it starred.
func (o ops) star(ctx context.Context, in *projectsRef) (*projectsStar, error) {
	return o.setStar(ctx, in, true)
}

// unstar removes the caller's own bookmark from a project, and answers whether
// it is starred afterwards.
//
// It removes only YOUR star — the same one star wrote — so a project other
// people have starred stays on their lists. Unstarring one you had not starred
// is not an error; it leaves it unstarred.
func (o ops) unstar(ctx context.Context, in *projectsRef) (*projectsStar, error) {
	return o.setStar(ctx, in, false)
}

// setStar is the one body behind both ops, so they cannot drift on who may write
// a star or on what a written one means.
//
// It resolves the project through `siteOf` — the SAME org-scoped lookup every
// other route on this surface uses — so a star can only be written against a
// project the caller can already see. Without that, the stars table would be a
// way to ask whether a slug exists in another tenant.
func (o ops) setStar(ctx context.Context, in *projectsRef, on bool) (*projectsStar, error) {
	c, org, p, err := o.siteOf(ctx, in.Slug)
	if err != nil {
		return nil, err
	}
	// A star belongs to a PERSON. Org alone is not enough to write one, and an
	// org token with no user behind it has nobody to star on behalf of.
	user := c.User()
	if user == "" {
		return nil, zip.ErrForbidden("a validated user is required to star a project")
	}

	if on {
		err = o.s.State.store.Star(ctx, org, user, p.ID)
	} else {
		err = o.s.State.store.Unstar(ctx, org, user, p.ID)
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "star: %v", err)
	}
	return &projectsStar{Starred: on}, nil
}
