package tools

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
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
	// Source is the repository the skill was read from, "<project>/<name>" or
	// "<name>"; empty for a skill written through the API. A push replaces every
	// skill of its source at once, so a skill leaves when its file does.
	Source string `json:"source,omitempty"`
	// Admitted is whether an admin of the org, or a SuperAdmin, wrote this
	// content. Only admitted content is carried into the org's agent runs. A
	// push writes content nobody administering the org attested, and a skill
	// written before writing one took an admin says no until an admin writes it
	// again.
	Admitted bool `json:"admitted"`
}

// SkillStore is the per-org registry of authored skills (one SQLite file, org
// column, the shared cloud store discipline). Isolation is a mandatory
// `WHERE org=?` on every statement; org is the validated principal value.
type SkillStore struct {
	db *sql.DB
}

// OpenSkillStore opens (and migrates) the authored-skill store under dir.
func OpenSkillStore(dir string) (*SkillStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "tools-skills", dir, func(db *sql.DB) (*SkillStore, error) {
		s := &SkillStore{db: db}
		ctx := context.Background()
		d := dialect.Of(db)
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS skills (
  id          TEXT NOT NULL,
  org         TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  content     TEXT NOT NULL,
  created_at  BIGINT NOT NULL,
  PRIMARY KEY (org, id)
);`); err != nil {
				return err
			}
			// A store made before a column existed gains it here, once.
			for _, c := range []struct{ name, ddl string }{
				{"source", "TEXT NOT NULL DEFAULT ''"},
				{"admitted", "BIGINT NOT NULL DEFAULT 0"},
			} {
				has, err := d.HasColumn(ctx, tx, "skills", c.name)
				if err != nil {
					return err
				}
				if has {
					continue
				}
				if _, err := tx.ExecContext(ctx, `ALTER TABLE skills ADD COLUMN `+c.name+` `+c.ddl); err != nil {
					return fmt.Errorf("%s: %w", c.name, err)
				}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("tools: skill migrate: %w", err)
		}
		return s, nil
	})
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
	if err := dialect.Text(sk.Org, sk.Name, sk.Description, sk.Content, sk.Source); err != nil {
		return Skill{}, fmt.Errorf("tools: put skill: %w", err)
	}
	sk.ID = sk.Name
	sk.CreatedAt = time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, putSkillSQL,
		sk.ID, sk.Org, sk.Name, sk.Description, sk.Content, sk.CreatedAt, sk.Source, boolInt(sk.Admitted)); err != nil {
		return Skill{}, fmt.Errorf("tools: put skill: %w", err)
	}
	return sk, nil
}

const putSkillSQL = `INSERT INTO skills (id, org, name, description, content, created_at, source, admitted) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT(org, id) DO UPDATE SET
   name=excluded.name, description=excluded.description,
   content=excluded.content, created_at=excluded.created_at, source=excluded.source,
   admitted=excluded.admitted`

// pushSkillSQL is putSkillSQL for a push: it takes over a name only from a row
// no admin wrote. An admin's skill keeps its name and its content whatever a
// repository later holds under that name.
const pushSkillSQL = putSkillSQL + `
 WHERE skills.admitted = 0`

// Replace makes skills the whole of what source contributes to org, in one
// transaction: what the source held before and no longer sends is removed, and
// each skill sent is written under the source. A skill of the same name from
// another source is taken over, the way the tool registry lets a later writer
// win a name — unless an admin wrote it (pushSkillSQL). Every skill's Org and
// Source are set here, never trusted, and none is admitted: a push is not an
// admin's act.
func (s *SkillStore) Replace(ctx context.Context, org, source string, skills []Skill) error {
	if org == "" || source == "" {
		return fmt.Errorf("tools: skills: org and source are required")
	}
	if err := dialect.Text(org, source); err != nil {
		return fmt.Errorf("tools: skills: %w", err)
	}
	for _, sk := range skills {
		if err := dialect.Text(sk.Name, sk.Description, sk.Content); err != nil {
			return fmt.Errorf("tools: skills: %q: %w", sk.Name, err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tools: skills: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM skills WHERE org=$1 AND source=$2 AND admitted=0`, org, source); err != nil {
		return fmt.Errorf("tools: skills: clear source: %w", err)
	}
	now := time.Now().Unix()
	for _, sk := range skills {
		if sk.Name == "" {
			return fmt.Errorf("tools: skills: empty name")
		}
		if _, err := tx.ExecContext(ctx, pushSkillSQL, sk.Name, org, sk.Name, sk.Description, sk.Content, now, source, 0); err != nil {
			return fmt.Errorf("tools: skills: put %q: %w", sk.Name, err)
		}
	}
	return tx.Commit()
}

// List returns the org's skills, name-sorted.
func (s *SkillStore) List(ctx context.Context, org string) ([]Skill, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org, name, description, content, created_at, source, admitted FROM skills
		 WHERE org=$1 ORDER BY name`, org)
	if err != nil {
		return nil, fmt.Errorf("tools: list skills: %w", err)
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		var sk Skill
		var admitted int
		if err := rows.Scan(&sk.ID, &sk.Org, &sk.Name, &sk.Description, &sk.Content, &sk.CreatedAt, &sk.Source, &admitted); err != nil {
			return nil, fmt.Errorf("tools: scan skill: %w", err)
		}
		sk.Admitted = admitted != 0
		// A row read from a repository before block descriptions were read whole
		// holds only the block's header (`>`); its file still has the words.
		if blockScalar(strings.TrimSpace(sk.Description)) {
			sk.Description = frontmatterDescription(sk.Content)
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// Delete removes one of the org's skills. Deleting what is not there is not an
// error — the caller's intent is "gone", and it is.
func (s *SkillStore) Delete(ctx context.Context, org, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE org=$1 AND id=$2`, org, id); err != nil {
		return fmt.Errorf("tools: delete skill: %w", err)
	}
	return nil
}

// orgSkillProvider surfaces an org's authored skills into the unified tool plane
// as SourceSkill entries, beside the deployment brand's embedded ones.
//
// Two providers share SourceSkill on purpose. The registry dedups by NAME with
// equal-rank ties going to whoever registered first, and Use registers the
// brand's skills (skills_peer.go) before this — so a brand skill always wins a
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
