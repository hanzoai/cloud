package transport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// policy.go is the gate engine — the OpenClaw sender-gates port onto SQLite.
// The policy key is (org, transport) only: integrations' connections PK is
// (org, provider), so exactly one platform account exists per pair and the
// account dimension collapses away. Gate decisions surface closed reason
// codes; sender ids never appear in logs.

// DMPolicy governs direct messages on a transport.
type DMPolicy string

const (
	DMPairing   DMPolicy = "pairing"
	DMAllowlist DMPolicy = "allowlist"
	DMOpen      DMPolicy = "open"
	// DMMembers admits anyone who can direct-message the app on a workspace
	// platform: a member of the workspace the org connected.
	DMMembers DMPolicy = "members"
)

// workspace are the platforms whose direct messages reach the app only from
// members of the workspace the org connected; their DMs default to members.
// Every other platform reaches strangers, so it pairs until the org says
// otherwise.
var workspace = map[string]bool{"slack": true}

// GroupPolicy governs group (and thread) surfaces on a transport.
type GroupPolicy string

const (
	GroupOpen      GroupPolicy = "open"
	GroupAllowlist GroupPolicy = "allowlist"
	GroupDisabled  GroupPolicy = "disabled"
)

// policyRow is one transport's access policy; the zero row is never stored —
// absent means the defaults: {members, open} on a workspace platform, else
// {pairing, open}.
type policyRow struct {
	DM    DMPolicy    `json:"dmPolicy"`
	Group GroupPolicy `json:"groupPolicy"`
}

func (p policyRow) validate() error {
	switch p.DM {
	case DMPairing, DMAllowlist, DMOpen, DMMembers:
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
	dmOpenWildcard gateReason = "dmOpenWildcard"
	dmAllowlisted  gateReason = "dmAllowlisted"
	dmPaired       gateReason = "dmPaired"
	// dmMember: a member of a connected workspace, under the members policy.
	dmMember gateReason = "dmMember"
	// dmInstaller: admitted as the person who installed this workspace, before
	// the org has an owner. See dmGate.
	dmInstaller         gateReason = "dmInstaller"
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

// policyFor returns the transport's policy; an absent row means the defaults
// (dm=members on a workspace platform, else pairing; group=open).
func policyFor(ctx context.Context, st *store, org, transport string) (policyRow, error) {
	var p policyRow
	err := st.db.QueryRowContext(ctx, `SELECT dm_policy, group_policy FROM channel_policy
  WHERE org = $1 AND channel = $2`, org, transport).Scan(&p.DM, &p.Group)
	if errors.Is(err, sql.ErrNoRows) {
		if workspace[transport] {
			return policyRow{DM: DMMembers, Group: GroupOpen}, nil
		}
		return policyRow{DM: DMPairing, Group: GroupOpen}, nil
	}
	return p, err
}

// setPolicy upserts the transport's policy.
func setPolicy(ctx context.Context, st *store, org, transport string, p policyRow, now int64) error {
	if err := p.validate(); err != nil {
		return err
	}
	_, err := st.db.ExecContext(ctx, `INSERT INTO channel_policy (org, channel, dm_policy, group_policy, updated_at)
  VALUES ($1,$2,$3,$4,$5)
  ON CONFLICT (org, channel) DO UPDATE SET dm_policy = excluded.dm_policy,
    group_policy = excluded.group_policy, updated_at = excluded.updated_at`,
		org, transport, string(p.DM), string(p.Group), now)
	return err
}

// allowEntries returns the transport's allow entries for scope, split by source
// class: config (admin PUT) and paired (approved pairings).
func allowEntries(ctx context.Context, st *store, org, transport, scope string) (config, paired []string, err error) {
	rows, err := st.db.QueryContext(ctx, `SELECT entry, source FROM channel_allow
  WHERE org = $1 AND channel = $2 AND scope = $3 ORDER BY entry`, org, transport, scope)
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
// this transport ('*' rows are shared across transports). No wildcard handling
// here — `*` is gate-level syntax, not an identity.
func matches(ctx context.Context, st *store, org, transport, sender string, entries []string) (bool, error) {
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
  WHERE org = $1 AND name = $2 AND channel IN ($3, '*') AND entry = $4`, org, name, transport, sender).Scan(&one)
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
func dmGate(ctx context.Context, st *store, org, transport, sender, installer string, mayPair bool) (verdict, error) {
	p, err := policyFor(ctx, st, org, transport)
	if err != nil {
		return verdict{}, err
	}
	if p.DM == DMMembers && workspace[transport] {
		return verdict{Allow: true, Reason: dmMember}, nil
	}
	config, paired, err := allowEntries(ctx, st, org, transport, "dm")
	if err != nil {
		return verdict{}, err
	}
	if p.DM == DMOpen {
		// Open is NOT unconditional: it requires `*` or an explicit config
		// match, and pairing-source rows never widen an open transport.
		if slices.Contains(config, "*") {
			return verdict{Allow: true, Reason: dmOpenWildcard}, nil
		}
		m, err := matches(ctx, st, org, transport, sender, config)
		if err != nil {
			return verdict{}, err
		}
		if m {
			return verdict{Allow: true, Reason: dmAllowlisted}, nil
		}
		return verdict{Reason: dmNotAllowlisted}, nil
	}
	m, err := matches(ctx, st, org, transport, sender, config)
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
		// THE INSTALLER, before anyone has been approved. The first approved
		// pairing becomes the org's transport owner — but nobody can be approved
		// until an admin approves, and approving needs an admin surface. For an
		// org that installed the bot itself that is a circle: the person who
		// completed the OAuth was ALREADY an admin of this org, and asking them to
		// approve themselves proves nothing that Slack has not already proved.
		//
		// Narrow on purpose. It applies ONLY while the org has no owner, and ONLY
		// to the one person the provider named as having done the install. Once an
		// owner exists this does nothing, so it cannot widen an org that has
		// already decided who may speak.
		if installer != "" && sender == installer {
			owned, err := hasOwner(ctx, st, org)
			if err != nil {
				return verdict{}, err
			}
			if !owned {
				return verdict{Allow: true, Reason: dmInstaller}, nil
			}
		}
		if mayPair {
			return verdict{Pair: true, Reason: dmPairingRequired}, nil
		}
	}
	return verdict{Reason: dmNotAllowlisted}, nil
}

// groupGate decides a group (or thread) message.
func groupGate(ctx context.Context, st *store, org, transport, sender string) (verdict, error) {
	p, err := policyFor(ctx, st, org, transport)
	if err != nil {
		return verdict{}, err
	}
	switch p.Group {
	case GroupDisabled:
		return verdict{Reason: groupDisabled}, nil
	case GroupOpen:
		return verdict{Allow: true, Reason: groupOpen}, nil
	}
	config, _, err := allowEntries(ctx, st, org, transport, "group")
	if err != nil {
		return verdict{}, err
	}
	if len(config) == 0 {
		return verdict{Reason: groupEmptyAllowlist}, nil
	}
	if slices.Contains(config, "*") {
		return verdict{Allow: true, Reason: groupAllowlisted}, nil
	}
	m, err := matches(ctx, st, org, transport, sender, config)
	if err != nil {
		return verdict{}, err
	}
	if m {
		return verdict{Allow: true, Reason: groupAllowlisted}, nil
	}
	return verdict{Reason: groupNotAllowlisted}, nil
}

// putAllow replaces the transport's config-source allow entries for scope.
// OWNERSHIP: config-source rows are owned by this func (the admin PUT);
// pairing-source rows are owned exclusively by approvePairing (pairing.go) —
// the source column is the write boundary, so policy edits never revoke an
// approved pairing and vice versa. An entry the admin lists explicitly takes
// config ownership (upgrade on conflict): a config grant is valid under every
// policy, and from then on the admin PUT owns its lifecycle.
func putAllow(ctx context.Context, st *store, org, transport, scope string, entries []string, now int64) error {
	if scope != "dm" && scope != "group" {
		return fmt.Errorf("allow: unknown scope %q", scope)
	}
	tx, err := st.d.BeginWrite(ctx, st.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_allow
  WHERE org = $1 AND channel = $2 AND scope = $3 AND source = 'config'`, org, transport, scope); err != nil {
		return err
	}
	for _, e := range entries {
		if e == "" {
			return fmt.Errorf("allow: empty entry")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO channel_allow (org, channel, scope, entry, source, created_at)
  VALUES ($1,$2,$3,$4,'config',$5)
  ON CONFLICT (org, channel, scope, entry) DO UPDATE SET source = 'config', created_at = excluded.created_at`,
			org, transport, scope, e, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// putAccessGroups replaces the org's access groups wholesale. groups maps
// name -> transport ('*' = shared across transports) -> member entries.
func putAccessGroups(ctx context.Context, st *store, org string, groups map[string]map[string][]string) error {
	tx, err := st.d.BeginWrite(ctx, st.db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_access_group WHERE org = $1`, org); err != nil {
		return err
	}
	for name, byTransport := range groups {
		for transport, entries := range byTransport {
			for _, e := range entries {
				if name == "" || transport == "" || e == "" {
					return fmt.Errorf("access group: name, transport, and entry required")
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO channel_access_group (org, name, channel, entry)
  VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, org, name, transport, e); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}
