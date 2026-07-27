package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// routing.go is the machine-identity + liveness plane for routed runs (#48 half
// B). A run dispatched to a target is executed by an EXTERNAL machine (`hanzo
// code --serve`) that claims it over HTTP. Two properties make that safe:
//
//   - MACHINE IDENTITY. A target carries a claim key — a high-entropy capability
//     minted server-side, returned to the daemon ONCE, and stored only as a
//     SHA-256 hash (never plaintext, like every other secret). A claim/report
//     must present the key; cloud verifies it in constant time, scoped to
//     (org, target). Possession of the key IS being that machine, so one machine
//     can never claim another's runs even within the same org.
//
//   - LIVENESS. Dispatch routes ONLY to a target with a live runner. A serve
//     daemon proves liveness by polling Claim, which stamps serving_at; the gate
//     rejects a target whose last poll is older than servingTTL. A run to a dead
//     or absent runner fails closed at dispatch (never silently runs elsewhere),
//     and one that dies mid-flight is re-queued/timed-out by the durable owner.
//
// The claim key lives in its own table so the live (A) target CRUD is untouched.

const (
	// servingTTL bounds how stale a target's last claim poll may be and still be
	// considered "a runner is listening". The serve daemon re-polls right after a
	// 25s long-poll returns empty, so a healthy runner stamps well inside this.
	servingTTL = 90 * time.Second

	claimKeyPrefix = "tgtk_"
	claimKeyBytes  = 32 // 256-bit capability
	maxClaimKey    = 128
)

var (
	errNoClaimKey     = errors.New("agents: target has no claim key")
	errClaimKeyBad    = errors.New("agents: claim key mismatch")
	errTargetNotLive  = errors.New("agents: target has no live runner")
	errTargetNotReady = errors.New("agents: target is not online")
)

// migrateClaimKeys creates the per-target claim-key + serving-liveness table in
// the SAME agents.db (one store, one tenancy column). Idempotent.
func (s *Store) migrateClaimKeys() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS agent_target_claim_keys (
  org        TEXT NOT NULL,
  target_id  TEXT NOT NULL,
  key_hash   TEXT NOT NULL,
  serving_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org, target_id)
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate claim keys: %w", err)
	}
	return nil
}

// UpsertClaimKeyHash stores (or rotates) a target's claim-key hash. serving_at is
// reset to 0 on a fresh mint — the daemon proves liveness by its first poll.
func (s *Store) UpsertClaimKeyHash(ctx context.Context, org, targetID, hash string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_target_claim_keys (org,target_id,key_hash,serving_at,updated_at)
		 VALUES (?,?,?,0,?)
		 ON CONFLICT(org,target_id) DO UPDATE SET key_hash=excluded.key_hash, serving_at=0, updated_at=excluded.updated_at`,
		org, targetID, hash, now)
	if err != nil {
		return fmt.Errorf("upsert claim key: %w", err)
	}
	return nil
}

// ClaimKeyHash returns a target's stored hash + last serving stamp, or
// errNoClaimKey when none was ever minted.
func (s *Store) ClaimKeyHash(ctx context.Context, org, targetID string) (hash string, servingAt int64, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT key_hash, serving_at FROM agent_target_claim_keys WHERE org=? AND target_id=?`, org, targetID)
	err = row.Scan(&hash, &servingAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, errNoClaimKey
	}
	if err != nil {
		return "", 0, fmt.Errorf("get claim key: %w", err)
	}
	return hash, servingAt, nil
}

// StampServing records that a target's runner polled at now (its liveness
// heartbeat). Best-effort by the caller; a missing row is a no-op.
func (s *Store) StampServing(ctx context.Context, org, targetID string, now int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_target_claim_keys SET serving_at=? WHERE org=? AND target_id=?`, now, org, targetID)
	return err
}

// hashClaimKey is the at-rest form: SHA-256 hex of a high-entropy token. A random
// 256-bit key needs no password KDF; SHA-256 gives a fixed-size, constant-time-
// comparable digest and the plaintext is never stored.
func hashClaimKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// newClaimKey mints a fresh capability token.
func newClaimKey() (string, error) {
	b := make([]byte, claimKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return claimKeyPrefix + hex.EncodeToString(b), nil
}

// verifyClaimKey checks a presented key against the target's stored hash in
// constant time. Fail-closed: no key on file, or an empty presented key, is a
// mismatch — never an accidental pass.
func (s *Store) verifyClaimKey(ctx context.Context, org, targetID, presented string) error {
	presented = strings.TrimSpace(presented)
	if presented == "" || len(presented) > maxClaimKey {
		return errClaimKeyBad
	}
	stored, _, err := s.ClaimKeyHash(ctx, org, targetID)
	if err != nil {
		return err // errNoClaimKey or a real DB error
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(hashClaimKey(presented))) != 1 {
		return errClaimKeyBad
	}
	return nil
}

// TargetDispatchable is the DRY liveness gate, used at dispatch (fail closed
// before enqueue) AND re-checked at claim. A run is dispatchable only to a target
// that (a) exists in this org, (b) is online, and (c) has a live runner — a claim
// poll within servingTTL. Any failure is an explicit error the dispatcher renders
// honestly; it NEVER falls back to running elsewhere.
func (s *Store) TargetDispatchable(ctx context.Context, org, targetID string) error {
	t, err := s.GetTarget(ctx, org, targetID)
	if err != nil {
		return err // errTargetNotFound or a real DB error
	}
	if t.EffectiveStatus(time.Now()) != TargetOnline {
		return errTargetNotReady
	}
	_, servingAt, err := s.ClaimKeyHash(ctx, org, targetID)
	if err != nil {
		return errTargetNotLive // no claim key => no runner ever attached
	}
	if servingAt <= 0 || time.Now().Unix()-servingAt > int64(servingTTL/time.Second) {
		return errTargetNotLive
	}
	return nil
}

// TargetDispatchable is the exported gate the coding dispatcher injects (it never
// imports the store directly). Returns nil when a run may be routed to (org,
// targetID), else a descriptive error.
func TargetDispatchable(ctx context.Context, org, targetID string) error {
	if mounted == nil {
		return fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	targetID = strings.TrimSpace(targetID)
	if org == "" || targetID == "" {
		return fmt.Errorf("agents: org and target required")
	}
	return mounted.State.store.TargetDispatchable(ctx, org, targetID)
}
