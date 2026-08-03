package tools

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/namespace"
)

// Org-authored skills.
//
// The brand's skills are GENERATED from the OpenAPI source of truth and embedded
// (apps/skills), which means changing them is a rebuild and a redeploy.
// That is right for the catalogue a deployment ships and wrong for the one an org
// writes, so an org's own skills live here instead — added at runtime, visible
// immediately, and never able to reach the public discovery surface because they
// are in a different container entirely.
//
// This is the same split clients/templates already keeps: a PUBLIC embedded
// catalogue with no write route, and a PRIVATE per-org store whose every read
// binds org. A private row has no path into the public gallery by CONSTRUCTION,
// not by a filter someone has to remember to write.

// Skill is one org-authored skill: discovery + activation metadata plus the
// SKILL.md body. Like the brand's skills it is NOT dispatchable — a skill is
// attached to an agent, not called.
type Skill struct {
	// ID is the skill's id within the org. It is DERIVED from Name, so writing
	// the same name again revises that skill rather than adding another.
	ID string `json:"id"`
	// Org is the org that authored the skill — the validated caller's, never a
	// value the body supplied.
	Org string `json:"org"`
	// Name is the skill's name: one lowercase path segment (a-z0-9, _ or -).
	Name string `json:"name"`
	// Description is the one-line summary discovery shows for the skill.
	Description string `json:"description"`
	// Content is the SKILL.md body, markdown.
	Content string `json:"content"`
	// CreatedAt is when the skill was last written, Unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// SkillStore is the per-org registry of authored skills (one SQLite file, org
// column, the shared cloud store discipline). Isolation is a mandatory
// `WHERE org=?` on every statement; org is the validated principal value.
type SkillStore struct {
	db *sql.DB
}

// OpenSkillStore opens (and migrates) the authored-skill store under dir.
func OpenSkillStore(dir string) (*SkillStore, error) {
	db, err := cek.Open(namespace.System(), "tools-skills", dir)
	if err != nil {
		return nil, fmt.Errorf("tools: open skill store: %w", err)
	}
	sqlpool.Single(db)
	s := &SkillStore{db: db}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS skills (
  id          TEXT NOT NULL,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  content     TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (org, id)
);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tools: skill migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database.
func (s *SkillStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Put inserts or replaces a skill. The id is derived from the name so writing
// the same name twice REVISES that skill rather than accumulating duplicates
// that would then collide in the tool registry.
func (s *SkillStore) Put(ctx context.Context, sk Skill) (Skill, error) {
	if sk.Org == "" {
		return Skill{}, fmt.Errorf("tools: skill: empty org")
	}
	if sk.Name == "" {
		return Skill{}, fmt.Errorf("tools: skill: empty name")
	}
	sk.ID = sk.Name
	sk.CreatedAt = time.Now().Unix()
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO skills (id, org, name, description, content, created_at) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(org, id) DO UPDATE SET
		   name=excluded.name, description=excluded.description,
		   content=excluded.content, created_at=excluded.created_at`,
		sk.ID, sk.Org, sk.Name, sk.Description, sk.Content, sk.CreatedAt); err != nil {
		return Skill{}, fmt.Errorf("tools: put skill: %w", err)
	}
	return sk, nil
}

// List returns the org's skills, name-sorted.
func (s *SkillStore) List(ctx context.Context, org string) ([]Skill, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, name, description, content, created_at FROM skills
		 WHERE org=? ORDER BY name`, org)
	if err != nil {
		return nil, fmt.Errorf("tools: list skills: %w", err)
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		var sk Skill
		if err := rows.Scan(&sk.ID, &sk.Org, &sk.Name, &sk.Description, &sk.Content, &sk.CreatedAt); err != nil {
			return nil, fmt.Errorf("tools: scan skill: %w", err)
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// Delete removes one of the org's skills. Deleting what is not there is not an
// error — the caller's intent is "gone", and it is.
func (s *SkillStore) Delete(ctx context.Context, org, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE org=? AND id=?`, org, id); err != nil {
		return fmt.Errorf("tools: delete skill: %w", err)
	}
	return nil
}

// orgSkillProvider surfaces an org's authored skills into the unified tool plane
// as SourceSkill entries, beside the deployment brand's embedded ones.
//
// Two providers share SourceSkill on purpose. The registry dedups by NAME with
// equal-rank ties going to whoever registered first, and apps/skills
// mounts at order 8 while this mounts at 123 — so a brand skill always wins a
// name collision against an org's. The deployment's own catalogue is the one
// that cannot be shadowed.
type orgSkillProvider struct {
	store *SkillStore
}

func (orgSkillProvider) Source() Source { return SourceSkill }

func (p orgSkillProvider) List(ctx context.Context, scope Scope) ([]Tool, error) {
	if p.store == nil || scope.Org == "" {
		return nil, nil
	}
	skills, err := p.store.List(ctx, scope.Org)
	if err != nil {
		return nil, nil // an unreadable store is an empty skill set, not a failed listing
	}
	out := make([]Tool, 0, len(skills))
	for _, sk := range skills {
		out = append(out, Tool{
			Name:         "skill_" + sk.Name,
			Source:       SourceSkill,
			Description:  sk.Description,
			Dispatchable: false,
		})
	}
	return out, nil
}

func (orgSkillProvider) Dispatch(_ context.Context, _ Principal, _ string, _ map[string]any) (any, error) {
	return nil, ErrNotDispatchable
}
