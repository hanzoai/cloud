// persona.go seeds and keeps an org's BUILT-IN agents — the named personas a
// human @-mentions in Hanzo Team and presses in the hanzo.ai chat. They are
// ordinary rows in the ONE agent registry (this package's store): nothing about
// them is special-cased downstream — they list, project into Team as bot members
// (bots.go), and answer through the SAME agents.RunOnBehalf path every other
// agent uses.
//
// THE CORE TEAM IS WRITTEN IN ONE PLACE: hanzoai/personas, team/<id>.md, embedded
// in this binary by the Go module at the version go.mod pins. A member's row is
// filed under the member's id ("dev"), titled "Dev — Development", and carries the
// member's file as its instructions, word for word. The hanzo.ai chat reads the
// same files. That team is the whole built-in crew: no built-in is defined here.
//
// THE HANDLE NEVER CHANGES. Tools ("agent_dev"), tool activations and channel
// bindings name an agent by its handle, so the handle stays the lower-case id;
// the name a person reads ("Dev") is the member's, and the surfaces draw it.
//
// A SEAT FOLLOWS ITS SOURCE UNTIL THE ORG EDITS IT. A row the platform seated is
// marked in agent_personas, and every store open rewrites a marked row's title
// and instructions to the current source (migratePersonas). An org that edits
// either takes the agent over: the update handler drops the mark, so an edit is
// never reverted.
//
// A SEAT CARRIES ITS ROLE'S SKILLS. A member names the role skills from the
// skill catalogue it needs (Dev: dev and code-review), and its seat's tools hold
// them as skill_<name>, which is how any agent attaches a skill (skills.go). A
// new seat is created with them; a marked seat gains a skill the source adds and
// loses one it drops, and agent_personas.skills records what was last carried,
// so a skill the org took out of its tools stays out. The org's own tools are
// never touched.
//
// A SEAT LEAVES WITH ITS MEMBER. A marked one-shot row whose member is no longer
// in the source is retired at store open: it leaves the agent list and the tool
// plane, and Team projects it as an inactive member, while its row, runs and past
// messages stay. It comes back if the member does, or once the org takes it over.

package agents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	house "github.com/hanzoai/personas"

	"github.com/hanzoai/cloud/sdk/mint"
)

// persona is one built-in agent definition. Name is the handle a row is filed
// under; Description is the display title; Instructions is the system prompt;
// Model is what a new row runs on; Skills are its role skills as the tool
// names a seat carries them by (skill_<name>).
type persona struct {
	Name         string
	Description  string
	Instructions string
	Model        string
	Skills       []string
}

// crew is every built-in agent: the core team as hanzoai/personas publishes it,
// and nothing else. The team is embedded, so it is read once per process.
var crew = sync.OnceValues(func() ([]persona, error) {
	team, err := house.Team()
	if err != nil {
		return nil, err
	}
	out := make([]persona, 0, len(team))
	for _, m := range team {
		skills := make([]string, 0, len(m.Skills))
		for _, s := range m.Skills {
			skills = append(skills, skillPrefix+s)
		}
		out = append(out, persona{
			Name:         m.ID,
			Description:  m.Name + " — " + m.Role,
			Instructions: m.Instructions,
			Model:        m.Model,
			Skills:       skills,
		})
	}
	return out, nil
})

// earlier is what a member's row was seeded with before the core team came from
// hanzoai/personas: its title and the sha256 of its instructions. A row carrying
// these, or the current source, is a seat nobody has edited. It names current
// members only, since seated consults it for a member of the crew.
var earlier = map[string]struct{ title, sum string }{
	"dev": {"Dev — the builder", "5c6e8fdfec38d89a4dca90b16a831dc4bce1f39a2275cee4c6ab4d556d76b7b5"},
	"des": {"Des — the designer", "a385fb258243a8c9299fe1350d6759eb5cfa08859914d29582b15f5d9db623e5"},
	"vi":  {"Vi — the visionary", "e55a8e6228015bc6e6bf379624d574398bf54267c467beabf204cf8bcf316751"},
}

// retired is the status of a seat whose member has left the source.
const retired = "retired"

// sum is the hex sha256 of an instruction text.
func sum(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// seated reports whether a row is p's seat exactly as the platform left it: filed
// under p's handle (in any case, so a row an earlier release capitalized is
// still found), carrying p's title and instructions now or as first seeded.
func seated(name, title, instructions string, p persona) bool {
	if !strings.EqualFold(name, p.Name) {
		return false
	}
	if title == p.Description && instructions == p.Instructions {
		return true
	}
	was, ok := earlier[p.Name]
	return ok && title == was.title && sum(instructions) == was.sum
}

// migratePersonas marks every seat nobody has edited, rewrites each marked
// row's title and instructions to the current source, and carries its role
// skills. It runs on every store open, which is how it reaches every org without
// a request writing anything. It touches only marked rows, and of them only the
// title, the brief and the skills the platform carries: a row's handle, model,
// likeness, budget and its own tools are the org's.
func (s *Store) migratePersonas(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS agent_personas (
  agent_id TEXT PRIMARY KEY,
  persona  TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("migrate: agent_personas: %w", err)
	}
	if err := s.addColumns(ctx, tx, "agent_personas", map[string]string{"skills": "TEXT NOT NULL DEFAULT '[]'"}); err != nil {
		return err
	}
	all, err := crew()
	if err != nil {
		return fmt.Errorf("migrate: crew: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name, description, instructions FROM agents
		WHERE id NOT IN (SELECT agent_id FROM agent_personas)`)
	if err != nil {
		return fmt.Errorf("migrate: scan seats: %w", err)
	}
	marks := map[string]string{}
	for rows.Next() {
		var id, name, title, instructions string
		if err := rows.Scan(&id, &name, &title, &instructions); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate: scan seat: %w", err)
		}
		for _, p := range all {
			if seated(name, title, instructions, p) {
				marks[id] = p.Name
				break
			}
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate: seats: %w", err)
	}
	for id, handle := range marks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_personas(agent_id, persona) VALUES ($1,$2) ON CONFLICT (agent_id) DO NOTHING`, id, handle); err != nil {
			return fmt.Errorf("migrate: mark %s: %w", handle, err)
		}
	}
	if err := s.rehandle(ctx, tx); err != nil {
		return err
	}
	for _, p := range all {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET description=$1, instructions=$2
			WHERE id IN (SELECT agent_id FROM agent_personas WHERE persona=$3) AND (description<>$4 OR instructions<>$5)`,
			p.Description, p.Instructions, p.Name, p.Description, p.Instructions); err != nil {
			return fmt.Errorf("migrate: keep %s: %w", p.Name, err)
		}
	}
	if err := s.equip(ctx, tx, all); err != nil {
		return err
	}
	return s.retire(ctx, tx, all)
}

// equip keeps each marked seat's role skills in its tools against what it last
// carried (agent_personas.skills): a skill the source added goes in, one it
// dropped comes out, one the org took out stays out, and every other tool is
// left as it is. A seat whose member left keeps its tools as they are.
func (s *Store) equip(ctx context.Context, tx *sql.Tx, all []persona) error {
	want := map[string][]string{}
	for _, p := range all {
		want[p.Name] = p.Skills
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.tools, p.persona, p.skills FROM agents a
		JOIN agent_personas p ON p.agent_id = a.id`)
	if err != nil {
		return fmt.Errorf("migrate: scan skills: %w", err)
	}
	type fix struct{ id, tools, skills string }
	var fixes []fix
	for rows.Next() {
		var id, tools, handle, carried string
		if err := rows.Scan(&id, &tools, &handle, &carried); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate: scan seat skills: %w", err)
		}
		skills, ok := want[handle]
		if !ok {
			continue
		}
		had, now := decodeList(carried), decodeList(tools)
		next := slices.DeleteFunc(slices.Clone(now), func(t string) bool { return slices.Contains(had, t) && !slices.Contains(skills, t) })
		for _, sk := range skills {
			if !slices.Contains(had, sk) && !slices.Contains(next, sk) {
				next = append(next, sk)
			}
		}
		if !slices.Equal(next, now) || !slices.Equal(had, skills) {
			fixes = append(fixes, fix{id, encodeList(next), encodeList(skills)})
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate: seat skills: %w", err)
	}
	for _, f := range fixes {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET tools=$1 WHERE id=$2`, f.tools, f.id); err != nil {
			return fmt.Errorf("migrate: carry skills %s: %w", f.id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_personas SET skills=$1 WHERE agent_id=$2`, f.skills, f.id); err != nil {
			return fmt.Errorf("migrate: record skills %s: %w", f.id, err)
		}
	}
	return nil
}

// retire applies the rule for a seat whose member has left: a row marked as a
// seat, whose member all does not hold, that runs only when asked (not
// long-running) is retired, and a retired row that no longer meets that is ready
// again, since its member came back or the org took it over. A long-running seat
// stays live: it acts on its own, and a retired row is hidden from the list the
// org would stop it from. It writes status only; the row, its runs, spend and
// sessions stay.
func (s *Store) retire(ctx context.Context, tx *sql.Tx, all []persona) error {
	// gone is the seats the rule retires, its arguments added to b.
	gone := func(b *binder) string {
		mode := b.bind(ModeLongRunning)
		names := make([]string, len(all))
		for i, p := range all {
			names[i] = b.bind(p.Name)
		}
		return `execution_mode<>` + mode +
			` AND id IN (SELECT agent_id FROM agent_personas WHERE persona NOT IN (` + strings.Join(names, ",") + `))`
	}
	var retiring binder
	retiring.args = []any{retired}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status=$1 WHERE status<>$1 AND `+gone(&retiring), retiring.args...); err != nil {
		return fmt.Errorf("migrate: retire: %w", err)
	}
	var restoring binder
	restoring.args = []any{retired}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status='ready' WHERE status=$1 AND NOT (`+gone(&restoring)+`)`, restoring.args...); err != nil {
		return fmt.Errorf("migrate: restore: %w", err)
	}
	return nil
}

// rehandle files every marked seat back under its lower-case handle when its
// name differs from it only in case, moving the runs, spend and sessions filed
// under the other spelling in the same transaction. A seat whose handle the org
// has since given to another agent keeps its spelling: that row is not the
// seat's to take.
func (s *Store) rehandle(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.org, a.name, p.persona FROM agents a
		JOIN agent_personas p ON p.agent_id = a.id WHERE a.name <> p.persona`)
	if err != nil {
		return fmt.Errorf("migrate: scan handles: %w", err)
	}
	type move struct{ id, org, from, to string }
	var moves []move
	for rows.Next() {
		var m move
		if err := rows.Scan(&m.id, &m.org, &m.from, &m.to); err != nil {
			_ = rows.Close()
			return fmt.Errorf("migrate: scan handle: %w", err)
		}
		if strings.EqualFold(m.from, m.to) {
			moves = append(moves, m)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate: handles: %w", err)
	}
	for _, m := range moves {
		// The handle is taken when another agent of the org already holds it: that row
		// is not the seat's to take, so the seat keeps its spelling. Asked in the
		// statement and not by catching the unique violation, which on PostgreSQL
		// would abort the migration's whole transaction.
		res, err := tx.ExecContext(ctx, `UPDATE agents SET name=$1 WHERE id=$2 AND name=$3
			AND NOT EXISTS (SELECT 1 FROM agents WHERE org=$4 AND name=$1)`, m.to, m.id, m.from, m.org)
		if err != nil {
			return fmt.Errorf("migrate: rehandle %s: %w", m.to, err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				return fmt.Errorf("migrate: rehandle %s: %w", m.to, err)
			}
			continue
		}
		for _, q := range []string{
			`UPDATE agent_runs SET agent_name=$1 WHERE org=$2 AND agent_name=$3`,
			`UPDATE agent_spend SET agent_name=$1 WHERE org=$2 AND agent_name=$3`,
			`UPDATE agent_sessions SET agent=$1 WHERE org=$2 AND agent=$3`,
		} {
			if _, err := tx.ExecContext(ctx, q, m.to, m.org, m.from); err != nil {
				return fmt.Errorf("migrate: rehandle %s history: %w", m.to, err)
			}
		}
	}
	return nil
}

// Seat marks an agent as the platform's seat for the built-in handle, so the
// persona source keeps it current, recording the skills it was seated with.
func (s *Store) Seat(ctx context.Context, id, handle string, skills []string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO agent_personas(agent_id, persona, skills) VALUES ($1,$2,$3) ON CONFLICT (agent_id) DO NOTHING`, id, handle, encodeList(skills)); err != nil {
		return fmt.Errorf("seat agent: %w", err)
	}
	return nil
}

// Unseat drops an agent's seat mark: the org has taken it over.
func (s *Store) Unseat(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM agent_personas WHERE agent_id=$1`, id); err != nil {
		return fmt.Errorf("unseat agent: %w", err)
	}
	return nil
}

// taken reports whether rows already hold p's handle in any case, so the seed
// creates no second agent beside it.
func taken(rows []Agent, p persona) bool {
	return slices.ContainsFunc(rows, func(a Agent) bool { return strings.EqualFold(a.Name, p.Name) })
}

// SeedPersonas ensures the built-in crew exists for org. Idempotent: a handle the
// org already holds, in any case, is left to the org, so it is safe to call on
// every org first-touch (a new Team space, say). A new seat is marked, so the
// persona source keeps it current. Returns the number newly created.
//
// A subsystem that is not mounted no-ops rather than erroring, so a caller on the
// login path can call it best-effort without ever blocking a human.
func SeedPersonas(ctx context.Context, org string) (int, error) {
	sto, _, serr := mountedStore(org)
	if serr != nil {
		return 0, nil // never mounted, or an org this deployment cannot place: no-op
	}
	all, err := crew()
	if err != nil {
		return 0, err
	}

	rows, err := sto.List(ctx, org)
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	created := 0
	for _, p := range all {
		if taken(rows, p) {
			continue
		}
		a := Agent{
			ID:           mint.ID("agent"),
			Org:          org,
			Name:         p.Name,
			Model:        p.Model,
			Instructions: p.Instructions,
			Description:  p.Description,
			Tools:        slices.Clone(p.Skills),
			Status:       "ready",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		err := sto.Create(ctx, a)
		switch {
		case err == nil:
			created++
			if err := sto.Seat(ctx, a.ID, p.Name, p.Skills); err != nil {
				return created, err
			}
		case errors.Is(err, errConflict):
			// Created between the list and here — the one-way idempotent no-op.
		default:
			return created, err
		}
	}
	return created, nil
}
