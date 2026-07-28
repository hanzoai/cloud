package channels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// policy.go is the gate engine — the OpenClaw sender-gates port onto SQLite.
// The policy key is (org, channel) only: integrations' connections PK is
// (org, provider), so exactly one platform account exists per pair and the
// account dimension collapses away. Gate decisions surface closed reason
// codes; sender ids never appear in logs.

// DMPolicy governs direct messages on a channel.
type DMPolicy string

const (
	DMPairing   DMPolicy = "pairing"
	DMAllowlist DMPolicy = "allowlist"
	DMOpen      DMPolicy = "open"
)

// GroupPolicy governs group (and thread) surfaces on a channel.
type GroupPolicy string

const (
	GroupOpen      GroupPolicy = "open"
	GroupAllowlist GroupPolicy = "allowlist"
	GroupDisabled  GroupPolicy = "disabled"
)

// policyRow is one channel's access policy; the zero row is never stored —
// absent means the defaults {pairing, open}.
type policyRow struct {
	DM    DMPolicy    `json:"dmPolicy"`
	Group GroupPolicy `json:"groupPolicy"`
}

func (p policyRow) validate() error {
	switch p.DM {
	case DMPairing, DMAllowlist, DMOpen:
	default:
		return fmt.Errorf("policy: unknown dm policy %q", p.DM)
	}
	switch p.Group {
	case GroupOpen, GroupAllowlist, GroupDisabled:
	default:
		return fmt.Errorf("policy: unknown group policy %q", p.Group)
	}
	return nil
}

// gateReason is the closed set of gate outcomes — the only decision detail
// that may be logged.
type gateReason string

const (
	dmOpenWildcard      gateReason = "dmOpenWildcard"
	dmAllowlisted       gateReason = "dmAllowlisted"
	dmPaired            gateReason = "dmPaired"
	dmNotAllowlisted    gateReason = "dmNotAllowlisted"
	dmPairingRequired   gateReason = "dmPairingRequired"
	groupOpen           gateReason = "groupOpen"
	groupAllowlisted    gateReason = "groupAllowlisted"
	groupDisabled       gateReason = "groupDisabled"
	groupEmptyAllowlist gateReason = "groupEmptyAllowlist"
	groupNotAllowlisted gateReason = "groupNotAllowlisted"
)

// verdict is a gate decision. Pair=true only on pairing-required: mint a code
// and drop — the message never reaches the inbox.
type verdict struct {
	Allow  bool
	Pair   bool
	Reason gateReason
}

// policyFor returns the channel's policy; an absent row means the defaults
// (dm=pairing — strictest; group=open).
func policyFor(ctx context.Context, st *store, org, channel string) (policyRow, error) {
	var p policyRow
	err := st.db.QueryRowContext(ctx, `SELECT dm_policy, group_policy FROM channel_policy
  WHERE org = ? AND channel = ?`, org, channel).Scan(&p.DM, &p.Group)
	if errors.Is(err, sql.ErrNoRows) {
		return policyRow{DM: DMPairing, Group: GroupOpen}, nil
	}
	return p, err
}

// setPolicy upserts the channel's policy.
func setPolicy(ctx context.Context, st *store, org, channel string, p policyRow, now int64) error {
	if err := p.validate(); err != nil {
		return err
	}
	_, err := st.db.ExecContext(ctx, `INSERT INTO channel_policy (org, channel, dm_policy, group_policy, updated_at)
  VALUES (?,?,?,?,?)
  ON CONFLICT (org, channel) DO UPDATE SET dm_policy = excluded.dm_policy,
    group_policy = excluded.group_policy, updated_at = excluded.updated_at`,
		org, channel, string(p.DM), string(p.Group), now)
	return err
}

// allowEntries returns the channel's allow entries for scope, split by source
// class: config (admin PUT) and paired (approved pairings).
func allowEntries(ctx context.Context, st *store, org, channel, scope string) (config, paired []string, err error) {
	rows, err := st.db.QueryContext(ctx, `SELECT entry, source FROM channel_allow
  WHERE org = ? AND channel = ? AND scope = ? ORDER BY entry`, org, channel, scope)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var entry, source string
		if err := rows.Scan(&entry, &source); err != nil {
			return nil, nil, err
		}
		if source == "pairing" {
			paired = append(paired, entry)
		} else {
			config = append(config, entry)
		}
	}
	return config, paired, rows.Err()
}

// matches reports whether sender is granted by entries: an exact id match, or
// membership in an `accessGroup:<name>` resolved against the org's groups for
// this channel ('*' rows are shared across channels). No wildcard handling
// here — `*` is gate-level syntax, not an identity.
func matches(ctx context.Context, st *store, org, channel, sender string, entries []string) (bool, error) {
	for _, e := range entries {
		if e == sender {
			return true, nil
		}
		name, isGroup := strings.CutPrefix(e, "accessGroup:")
		if !isGroup {
			continue
		}
		var one int
		err := st.db.QueryRowContext(ctx, `SELECT 1 FROM channel_access_group
  WHERE org = ? AND name = ? AND channel IN (?, '*') AND entry = ?`, org, name, channel, sender).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// dmGate decides a direct message, in the ported OpenClaw order. mayPair=true
// lets a pairing-policy miss mint a pairing request instead of a plain block.
func dmGate(ctx context.Context, st *store, org, channel, sender string, mayPair bool) (verdict, error) {
	p, err := policyFor(ctx, st, org, channel)
	if err != nil {
		return verdict{}, err
	}
	config, paired, err := allowEntries(ctx, st, org, channel, "dm")
	if err != nil {
		return verdict{}, err
	}
	if p.DM == DMOpen {
		// Open is NOT unconditional: it requires `*` or an explicit config
		// match, and pairing-source rows never widen an open channel.
		if slices.Contains(config, "*") {
			return verdict{Allow: true, Reason: dmOpenWildcard}, nil
		}
		m, err := matches(ctx, st, org, channel, sender, config)
		if err != nil {
			return verdict{}, err
		}
		if m {
			return verdict{Allow: true, Reason: dmAllowlisted}, nil
		}
		return verdict{Reason: dmNotAllowlisted}, nil
	}
	m, err := matches(ctx, st, org, channel, sender, config)
	if err != nil {
		return verdict{}, err
	}
	if m {
		return verdict{Allow: true, Reason: dmAllowlisted}, nil
	}
	if p.DM == DMPairing {
		// Pairing-source rows are valid ONLY under pairing policy — the source
		// column encodes the grant class, so switching to allowlist suspends
		// paired senders without deleting their grants.
		if slices.Contains(paired, sender) {
			return verdict{Allow: true, Reason: dmPaired}, nil
		}
		if mayPair {
			return verdict{Pair: true, Reason: dmPairingRequired}, nil
		}
	}
	return verdict{Reason: dmNotAllowlisted}, nil
}

// groupGate decides a group (or thread) message.
func groupGate(ctx context.Context, st *store, org, channel, sender string) (verdict, error) {
	p, err := policyFor(ctx, st, org, channel)
	if err != nil {
		return verdict{}, err
	}
	switch p.Group {
	case GroupDisabled:
		return verdict{Reason: groupDisabled}, nil
	case GroupOpen:
		return verdict{Allow: true, Reason: groupOpen}, nil
	}
	config, _, err := allowEntries(ctx, st, org, channel, "group")
	if err != nil {
		return verdict{}, err
	}
	if len(config) == 0 {
		return verdict{Reason: groupEmptyAllowlist}, nil
	}
	if slices.Contains(config, "*") {
		return verdict{Allow: true, Reason: groupAllowlisted}, nil
	}
	m, err := matches(ctx, st, org, channel, sender, config)
	if err != nil {
		return verdict{}, err
	}
	if m {
		return verdict{Allow: true, Reason: groupAllowlisted}, nil
	}
	return verdict{Reason: groupNotAllowlisted}, nil
}

// putAllow replaces the channel's config-source allow entries for scope.
// OWNERSHIP: config-source rows are owned by this func (the admin PUT);
// pairing-source rows are owned exclusively by approvePairing (pairing.go) —
// the source column is the write boundary, so policy edits never revoke an
// approved pairing and vice versa. An entry the admin lists explicitly takes
// config ownership (upgrade on conflict): a config grant is valid under every
// policy, and from then on the admin PUT owns its lifecycle.
func putAllow(ctx context.Context, st *store, org, channel, scope string, entries []string, now int64) error {
	if scope != "dm" && scope != "group" {
		return fmt.Errorf("allow: unknown scope %q", scope)
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_allow
  WHERE org = ? AND channel = ? AND scope = ? AND source = 'config'`, org, channel, scope); err != nil {
		return err
	}
	for _, e := range entries {
		if e == "" {
			return fmt.Errorf("allow: empty entry")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO channel_allow (org, channel, scope, entry, source, created_at)
  VALUES (?,?,?,?,'config',?)
  ON CONFLICT (org, channel, scope, entry) DO UPDATE SET source = 'config', created_at = excluded.created_at`,
			org, channel, scope, e, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// putAccessGroups replaces the org's access groups wholesale. groups maps
// name -> channel ('*' = shared across channels) -> member entries.
func putAccessGroups(ctx context.Context, st *store, org string, groups map[string]map[string][]string) error {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_access_group WHERE org = ?`, org); err != nil {
		return err
	}
	for name, byChannel := range groups {
		for channel, entries := range byChannel {
			for _, e := range entries {
				if name == "" || channel == "" || e == "" {
					return fmt.Errorf("access group: name, channel, and entry required")
				}
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO channel_access_group (org, name, channel, entry)
  VALUES (?,?,?,?)`, org, name, channel, e); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}
