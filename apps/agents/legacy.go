package agents

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/hanzoai/cloud/basedb"
	"github.com/hanzoai/namespace"
)

// legacy.go carries the pre-split registry forward. Until this change every
// org's agents, runs, sessions, events, targets and claim keys lived in ONE
// database — the SYSTEM namespace's "agents" — isolated by an org column. They
// now live one per org, the SAME subsystem under each org's own namespace, so a
// deployment that already has the single file has to be fanned out before it
// serves — otherwise a live plane (hanzo link registers sessions into it right
// now) reads as empty on the first boot after the upgrade, which is
// indistinguishable from data loss to everyone looking at it.
//
// The fan-out runs ONCE, on mount, BEFORE any route is registered. It is
// FAIL-SECURE: an error fails the mount rather than serving an empty registry
// over real rows, because a crashloop is loud and recoverable and a silently
// empty dashboard is neither.
//
// It is also IDEMPOTENT independent of its marker: every row is copied under the
// primary key it already has with INSERT OR IGNORE, so a re-run cannot duplicate
// and cannot overwrite anything the new file has since written. The marker only
// saves the work.
//
// The legacy file is READ and then left exactly where it is. It is not renamed
// and not deleted: keeping the bytes is what makes the upgrade reversible on the
// day someone needs it to be.

// legacySubsystem names the single pre-split database, held in the SYSTEM
// namespace. The per-org files are this same subsystem under each org's own
// namespace, and the platform partition is a name no org slug can render, so the
// two can never collide.
const legacySubsystem = "agents"

// fanOutLegacy copies every org's rows out of the pre-split platform-wide agents
// database into that org's own database, once. A deployment with no legacy file
// (a fresh install, or one already fanned out) does nothing and returns nil.
func fanOutLegacy(ctx context.Context, dataDir string, st *state) error {
	path, err := namespace.Path(dataDir, namespace.System(), legacySubsystem)
	if err != nil {
		return fmt.Errorf("legacy store path: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		return nil // fresh install: nothing was ever written to the shared file
	}
	// The legacy file was always opened under the platform key, never an org's.
	raw, err := basedb.Open(namespace.System(), legacySubsystem, dataDir)
	if err != nil {
		return fmt.Errorf("open legacy store: %w", err)
	}
	defer func() { _ = raw.Close() }()
	raw.SetMaxOpenConns(1)

	done, err := legacyFannedOut(ctx, raw)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	// Migrate the legacy file to the CURRENT schema first. Source and destination
	// then have identical columns by construction, which is what lets the copy
	// below name them from the same shared column constants instead of
	// re-deriving a second, drifting list.
	src, err := openStore(raw)
	if err != nil {
		return fmt.Errorf("migrate legacy store: %w", err)
	}
	orgs, err := src.legacyOrgs(ctx)
	if err != nil {
		return err
	}
	for _, org := range orgs {
		dst, err := st.storeFor(org)
		if err != nil {
			// namespace.Sanitize refused this org, or its file would not open. Either way
			// the rows exist and we cannot place them: halt and name the org rather
			// than drop a tenant's history on the floor.
			return fmt.Errorf("legacy fan-out: org %q: %w", org, err)
		}
		if err := src.copyOrgTo(ctx, org, dst); err != nil {
			return fmt.Errorf("legacy fan-out: org %q: %w", org, err)
		}
	}
	return markLegacyFannedOut(ctx, raw)
}

// legacyOrgs is every distinct org that owns a row anywhere in the legacy file.
// It unions the four org-bearing tables rather than reading one of them, because
// an org can perfectly well have sessions and no agents (an external @hanzo/dev
// surface registers sessions against a label, never a cloud Agent row) — reading
// only `agents` would silently strip exactly those tenants.
func (s *Store) legacyOrgs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT org FROM agents
		UNION SELECT org FROM agent_runs
		UNION SELECT org FROM agent_sessions
		UNION SELECT org FROM agent_targets
		ORDER BY org`)
	if err != nil {
		return nil, fmt.Errorf("legacy orgs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var org string
		if err := rows.Scan(&org); err != nil {
			return nil, fmt.Errorf("scan legacy org: %w", err)
		}
		if org != "" {
			out = append(out, org)
		}
	}
	return out, rows.Err()
}

// copyOrgTo copies one org's rows from the legacy store into that org's own
// database, in a single destination transaction so a partially-copied org can
// never be observed. Every statement is INSERT OR IGNORE keyed on the row's own
// primary key, so re-running is a no-op and a row the new file has already
// written wins over the legacy copy of itself.
//
// model_snapshot carries no org column — it is keyed by agent id and exists only
// to make the model rewrite reversible — so its rows are selected through the
// agents they describe.
func (s *Store) copyOrgTo(ctx context.Context, org string, dst *Store) error {
	tx, err := dst.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	copies := []struct {
		what string
		sel  string
		ins  string
	}{
		{"agents",
			`SELECT ` + agentCols + ` FROM agents WHERE org=?`,
			`INSERT OR IGNORE INTO agents (` + agentCols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{"runs",
			`SELECT ` + runCols + ` FROM agent_runs WHERE org=?`,
			`INSERT OR IGNORE INTO agent_runs (` + runCols + `) VALUES (?,?,?,?,?,?,?,?,?,?)`},
		{"sessions",
			`SELECT ` + sessionCols + ` FROM agent_sessions WHERE org=?`,
			`INSERT OR IGNORE INTO agent_sessions (` + sessionCols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{"events",
			`SELECT ` + eventCols + ` FROM agent_session_events WHERE org=? ORDER BY session_id, seq`,
			`INSERT OR IGNORE INTO agent_session_events (` + eventCols + `) VALUES (?,?,?,?,?,?,?,?)`},
		{"targets",
			`SELECT ` + targetCols + ` FROM agent_targets WHERE org=?`,
			`INSERT OR IGNORE INTO agent_targets (` + targetCols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`},
		{"claim keys",
			`SELECT org,target_id,key_hash,serving_at,updated_at FROM agent_target_claim_keys WHERE org=?`,
			`INSERT OR IGNORE INTO agent_target_claim_keys (org,target_id,key_hash,serving_at,updated_at) VALUES (?,?,?,?,?)`},
		{"model snapshots",
			`SELECT m.agent_id, m.model, m.at FROM model_snapshot m
			   JOIN agents a ON a.id = m.agent_id WHERE a.org=?`,
			`INSERT OR IGNORE INTO model_snapshot (agent_id, model, at) VALUES (?,?,?)`},
	}
	for _, c := range copies {
		if err := copyRows(ctx, s.db, tx, org, c.sel, c.ins); err != nil {
			return fmt.Errorf("copy %s: %w", c.what, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// copyRows streams one org's rows from sel on src into ins on tx. It reads the
// column count off the result set rather than being told it, so the shared
// column constants stay the single source of truth for both halves of a copy and
// a column added to one of them can never silently desynchronise from the other.
//
// The read is fully drained and closed BEFORE the insert loop, because the
// legacy handle serves everything on one connection and holding a cursor open
// across writes to it would be a self-deadlock waiting to happen.
func copyRows(ctx context.Context, src *sql.DB, tx *sql.Tx, org, sel, ins string) error {
	rows, err := src.QueryContext(ctx, sel, org)
	if err != nil {
		return err
	}
	cols, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return err
	}
	var batch [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			_ = rows.Close()
			return err
		}
		batch = append(batch, vals)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, vals := range batch {
		if _, err := tx.ExecContext(ctx, ins, vals...); err != nil {
			return err
		}
	}
	return nil
}

// legacyFannedOut reports whether the fan-out already completed. The marker
// lives in the LEGACY file because that is the one place the fact is global: the
// per-org files are many, and "has this deployment been split yet" is one
// question with one answer.
func legacyFannedOut(ctx context.Context, raw *sql.DB) (bool, error) {
	if _, err := raw.ExecContext(ctx, legacyMarkerDDL); err != nil {
		return false, fmt.Errorf("legacy marker table: %w", err)
	}
	var n int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_fanout`).Scan(&n); err != nil {
		return false, fmt.Errorf("read legacy marker: %w", err)
	}
	return n > 0, nil
}

func markLegacyFannedOut(ctx context.Context, raw *sql.DB) error {
	if _, err := raw.ExecContext(ctx,
		`INSERT OR IGNORE INTO agent_fanout (id, at) VALUES (1, strftime('%s','now'))`); err != nil {
		return fmt.Errorf("mark legacy fan-out: %w", err)
	}
	return nil
}

const legacyMarkerDDL = `
CREATE TABLE IF NOT EXISTS agent_fanout (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  at INTEGER NOT NULL
);`
