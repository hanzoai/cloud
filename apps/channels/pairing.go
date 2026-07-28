package channels

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// pairing.go is the OpenClaw pairing-store port. A sender hitting a
// pairing-policy DM gets an 8-char code minted here; an org admin approves the
// code, which grants the sender a pairing-source DM allow entry. Codes are
// bearer capabilities: stored uppercase, shown only on the admin surface,
// never logged. The pending cap is per (org, channel) — the channel-account
// key, since one platform account exists per pair.

const (
	pairCodeLen = 8
	// pairAlphabet is uppercase A-Z0-9 minus the confusables 0/O/1/I — exactly
	// 32 symbols, so one random byte mod 32 carries no modulo bias.
	pairAlphabet     = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	pairTTL          = time.Hour
	pairMaxPending   = 3
	pairCodeAttempts = 500
)

// pairingRow is one pending pairing request. Times are Unix seconds.
type pairingRow struct {
	Org       string `json:"org"`
	Channel   string `json:"channel"`
	Sender    string `json:"sender"`
	Code      string `json:"code"`
	CreatedAt int64  `json:"createdAt"`
	LastSeen  int64  `json:"lastSeen"`
}

// pairCutoff is the oldest unexpired created_at: rows strictly older are
// expired, so a request expires strictly AFTER pairTTL (the pairing-store.ts
// `>` boundary). prunePairing and listPairing use the same cutoff.
func pairCutoff(now int64) int64 { return now - int64(pairTTL/time.Second) }

func prunePairing(ctx context.Context, tx *sql.Tx, org, channel string, now int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM channel_pairing
  WHERE org = ? AND channel = ? AND created_at < ?`, org, channel, pairCutoff(now))
	return err
}

// mintCode returns pairCodeLen chars drawn from pairAlphabet via crypto/rand.
func mintCode() (string, error) {
	buf := make([]byte, pairCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		buf[i] = pairAlphabet[int(b)%len(pairAlphabet)]
	}
	return string(buf), nil
}

// upsertPairing records that sender needs pairing on (org, channel).
// created=true means a new code was minted and the caller should send the
// pairing reply; created=false means an existing pending request was refreshed
// (same code, last_seen advanced) or the pending cap is full (code="") — both
// throttle the chat reply to at most one per TTL per sender.
func upsertPairing(ctx context.Context, st *store, org, channel, sender string, now int64) (code string, created bool, err error) {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := prunePairing(ctx, tx, org, channel, now); err != nil {
		return "", false, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT code FROM channel_pairing
  WHERE org = ? AND channel = ? AND sender = ?`, org, channel, sender).Scan(&existing)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `UPDATE channel_pairing SET last_seen = ?
  WHERE org = ? AND channel = ? AND sender = ?`, now, org, channel, sender); err != nil {
			return "", false, err
		}
		return existing, false, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return "", false, err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_pairing
  WHERE org = ? AND channel = ?`, org, channel).Scan(&pending); err != nil {
		return "", false, err
	}
	if pending >= pairMaxPending {
		// Exact OpenClaw port: no eviction at the cap. ACCEPTED TRADEOFF — three
		// junk requests lock pairing for this (org, channel) for up to 1 h; an
		// admin approval or TTL expiry clears the slots.
		return "", false, tx.Commit()
	}
	for range pairCodeAttempts {
		c, err := mintCode()
		if err != nil {
			return "", false, err
		}
		// Codes are stored and minted uppercase, so exact match IS the
		// case-insensitive uniqueness check against pending codes.
		var one int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM channel_pairing
  WHERE org = ? AND channel = ? AND code = ?`, org, channel, c).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO channel_pairing (org, channel, sender, code, created_at, last_seen)
  VALUES (?,?,?,?,?,?)`, org, channel, sender, c, now, now); err != nil {
			return "", false, err
		}
		return c, true, tx.Commit()
	}
	return "", false, fmt.Errorf("pairing: no unique code after %d attempts", pairCodeAttempts)
}

// approvePairing consumes a pending code: the request row is deleted and the
// sender gains a pairing-source DM allow entry. ok=false means no pending
// request matches the code (unknown, expired, or already approved).
func approvePairing(ctx context.Context, st *store, org, channel, code string, now int64) (sender string, ownerBootstrapped bool, ok bool, err error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return "", false, false, nil
	}
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := prunePairing(ctx, tx, org, channel, now); err != nil {
		return "", false, false, err
	}
	err = tx.QueryRowContext(ctx, `SELECT sender FROM channel_pairing
  WHERE org = ? AND channel = ? AND code = ?`, org, channel, code).Scan(&sender)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, tx.Commit()
	}
	if err != nil {
		return "", false, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_pairing
  WHERE org = ? AND channel = ? AND sender = ?`, org, channel, sender); err != nil {
		return "", false, false, err
	}
	// `*` and "" are policy syntax, never identities — refuse to mint a grant.
	if sender == "" || sender == "*" {
		return "", false, false, tx.Commit()
	}
	// OWNERSHIP (mirror of policy.go putAllow): this is the ONLY writer of
	// pairing-source channel_allow rows, and approval grants DM access only —
	// NEVER group. OR IGNORE keeps an existing config-source grant, which is
	// already broader, authoritative.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO channel_allow (org, channel, scope, entry, source, created_at)
  VALUES (?,?,'dm',?,'pairing',?)`, org, channel, sender, now); err != nil {
		return "", false, false, err
	}
	// Owner bootstrap: the FIRST approved pairing in the org records the owner
	// as '<channel>:<sender>'; once any owner exists, later approvals grant DM
	// access only.
	var ownerEntry string
	err = tx.QueryRowContext(ctx, `SELECT entry FROM channel_owner WHERE org = ?`, org).Scan(&ownerEntry)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO channel_owner (org, entry, created_at)
  VALUES (?,?,?)`, org, channel+":"+sender, now); err != nil {
			return "", false, false, err
		}
		ownerBootstrapped = true
	case err != nil:
		return "", false, false, err
	}
	return sender, ownerBootstrapped, true, tx.Commit()
}

// listPairing returns the org's pending (unexpired) requests, ordered for
// deterministic JSON. Codes appear here for the admin approval surface.
func listPairing(ctx context.Context, st *store, org string, now int64) ([]pairingRow, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT org, channel, sender, code, created_at, last_seen
  FROM channel_pairing WHERE org = ? AND created_at >= ?
  ORDER BY channel, created_at, sender`, org, pairCutoff(now))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []pairingRow{}
	for rows.Next() {
		var r pairingRow
		if err := rows.Scan(&r.Org, &r.Channel, &r.Sender, &r.Code, &r.CreatedAt, &r.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
