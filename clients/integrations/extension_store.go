package integrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ExtConfig is the per-extension CONFIG row: the enablement bit + an opaque
// config blob, keyed by the extension identity (scope,org,user,provider,label).
// It is the config half of the unified /v1/connectors plane — orthogonal to
// custody (KMS holds the credential; this holds "is it on" + "how is it tuned").
// A MISSING row means "connected-and-enabled, no tuning": enablement defaults to
// ON so every already-connected connector keeps working without a config row.
//
// TENANCY. org scope → user="" label="" (one row per org,provider). user scope →
// user=<id> label=<label> (one row per connected instance). Every read/write is
// bound by the full key, so another tenant's row is simply "no row".
type ExtConfig struct {
	Scope, Org, User, Provider, Label string
	Enabled                           bool
	Config                            string // opaque JSON object; "" == "{}"
	UpdatedAt                         int64
}

const extConfigCols = `scope,org,user,provider,label,enabled,config,updated_at`

func scanExtConfig(sc interface{ Scan(...any) error }) (ExtConfig, error) {
	var c ExtConfig
	var enabled int64
	err := sc.Scan(&c.Scope, &c.Org, &c.User, &c.Provider, &c.Label, &enabled, &c.Config, &c.UpdatedAt)
	c.Enabled = enabled != 0
	return c, err
}

// GetExtConfig returns the config row for an extension identity. found=false (nil
// error) when there is no row — the caller reads that as enabled-by-default.
func (s *Store) GetExtConfig(ctx context.Context, scope, org, user, provider, label string) (ExtConfig, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+extConfigCols+` FROM extension_config
		 WHERE scope=? AND org=? AND user=? AND provider=? AND label=?`,
		scope, org, user, provider, label)
	c, err := scanExtConfig(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ExtConfig{}, false, nil
	}
	if err != nil {
		return ExtConfig{}, false, fmt.Errorf("get ext config: %w", err)
	}
	return c, true, nil
}

// SetExtEnabled upserts ONLY the enablement bit, PRESERVING any existing config
// blob (a new row seals config='{}'). The enablement axis and the config axis are
// written independently so toggling one never clobbers the other.
func (s *Store) SetExtEnabled(ctx context.Context, scope, org, user, provider, label string, enabled bool) error {
	now := time.Now().Unix()
	e := int64(0)
	if enabled {
		e = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO extension_config (`+extConfigCols+`) VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(scope,org,user,provider,label) DO UPDATE SET
		   enabled=excluded.enabled,
		   updated_at=excluded.updated_at`,
		scope, org, user, provider, label, e, "{}", now)
	if err != nil {
		return fmt.Errorf("set ext enabled: %w", err)
	}
	return nil
}

// SetExtConfig upserts ONLY the config blob, PRESERVING the enablement bit (a new
// row seals enabled=1, the default). config MUST be a compact JSON object string.
func (s *Store) SetExtConfig(ctx context.Context, scope, org, user, provider, label, config string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO extension_config (`+extConfigCols+`) VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(scope,org,user,provider,label) DO UPDATE SET
		   config=excluded.config,
		   updated_at=excluded.updated_at`,
		scope, org, user, provider, label, 1, config, now)
	if err != nil {
		return fmt.Errorf("set ext config: %w", err)
	}
	return nil
}

// ListExtConfig returns every config row VISIBLE to caller (org,user): the org's
// own org-scoped rows PLUS this user's user-scoped rows — never another user's.
// The (scope='org' OR user=?) predicate is the tenant boundary for the unified
// list; org is always ANDed first.
func (s *Store) ListExtConfig(ctx context.Context, org, user string) ([]ExtConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+extConfigCols+` FROM extension_config
		 WHERE org=? AND (scope=? OR user=?)`,
		org, orgScope, user)
	if err != nil {
		return nil, fmt.Errorf("list ext config: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ExtConfig
	for rows.Next() {
		c, err := scanExtConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ext config: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteExtConfig forgets an extension's config row (called on forget, so a
// re-connect starts enabled-by-default again). Idempotent.
func (s *Store) DeleteExtConfig(ctx context.Context, scope, org, user, provider, label string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM extension_config WHERE scope=? AND org=? AND user=? AND provider=? AND label=?`,
		scope, org, user, provider, label)
	if err != nil {
		return false, fmt.Errorf("delete ext config: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
