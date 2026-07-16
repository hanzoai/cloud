package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sessions_stop.go is the login-manager tie-in: the in-process action a link
// revoke takes to tear down the live sessions that ran under a revoked account or
// device, plus the active-session count the device view shows. Both are org-scoped
// (org is the ONLY tenant key) and nil-safe (no agents mounted → 0), and neither
// can fan out to another tenant or to an org's every session by accident.

// SessionMatch selects live (running|paused) sessions to stop or count. Actor (the
// owning subject, org/user) is MANDATORY and always ANDed, so a match can only ever
// affect the caller's OWN sessions — never a co-tenant's. Host/Provider/Account are
// optional narrowing WITHIN the actor's sessions (empty = any of the actor's). A
// match with no actor selects NOTHING (fail-closed), so a login-out can never sweep
// another user's — or an org's every — session, even when Host/Provider/Account are
// attacker-set at link upsert.
type SessionMatch struct {
	Actor    string
	Host     string
	Provider string
	Account  string
}

// empty reports whether the match lacks its mandatory actor scope. Without an actor
// the match selects nothing — the fail-closed direction (an under-stop, never a
// cross-user over-stop).
func (m SessionMatch) empty() bool {
	return strings.TrimSpace(m.Actor) == ""
}

// where builds the ANDed predicate + args for a live-session match under org. Actor
// is always in the base predicate (the guard rejects an empty actor before this runs),
// so a stop/count is bounded to the caller's own sessions before any optional narrowing.
func (m SessionMatch) where(org string) (string, []any) {
	where := "org=? AND actor=? AND status IN (?,?)"
	args := []any{org, strings.TrimSpace(m.Actor), StatusRunning, StatusPaused}
	if h := strings.TrimSpace(m.Host); h != "" {
		where += " AND host=?"
		args = append(args, h)
	}
	if p := strings.TrimSpace(m.Provider); p != "" {
		where += " AND provider=?"
		args = append(args, p)
	}
	if a := strings.TrimSpace(m.Account); a != "" {
		where += " AND account=?"
		args = append(args, a)
	}
	return where, args
}

// listActiveMatch returns org's live sessions matching m, oldest first.
func (s *Store) listActiveMatch(ctx context.Context, org string, m SessionMatch) ([]Session, error) {
	where, args := m.where(org)
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionCols+` FROM agent_sessions WHERE `+where+` ORDER BY created_at ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list active match: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// countActiveMatch counts org's live sessions matching m.
func (s *Store) countActiveMatch(ctx context.Context, org string, m SessionMatch) (int, error) {
	where, args := m.where(org)
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_sessions WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active match: %w", err)
	}
	return n, nil
}

// StopSessions closes every RUNNING|PAUSED session of org matching m — recording a
// control "stop" event on each and transitioning it to a terminal state — and
// returns how many it stopped. It is the action a login-out (link revoke) takes so
// the sessions that ran under a revoked account/device are torn down. Org AND Actor
// scope it: the caller passes their own actor (org/user), so a revoke can only ever
// stop the caller's OWN sessions — never a co-tenant's, never an org's every session
// — even though m's Host/Provider/Account come from an attacker-controllable link
// row. A match with no actor stops nothing (fail-closed). Not-mounted → (0, nil), so
// a revoke tolerates a deployment with no session plane.
func StopSessions(ctx context.Context, org string, m SessionMatch) (int, error) {
	if mounted == nil {
		return 0, nil
	}
	org = strings.TrimSpace(org)
	if org == "" || m.empty() {
		return 0, nil
	}
	live, err := mounted.State.store.listActiveMatch(ctx, org, m)
	if err != nil {
		return 0, err
	}
	stopped := 0
	for _, x := range live {
		if err := stopOne(ctx, x, StatusError, "account logged out via login manager"); err != nil {
			// Best-effort per session: a failure on one does not abort the rest, so a
			// revoke tears down as many as it can and reports the true count.
			mounted.Log.Warn("agents: stop session", "org", org, "session", x.ID, "err", err)
			continue
		}
		stopped++
	}
	return stopped, nil
}

// stopOne records a stop control event carrying reason on a live session and
// moves it to the given terminal state — the forced-teardown transition, and the
// ONE write path every forced stop takes (the login-revoke sweep below, and the
// single-session StopSession). A session already terminal is skipped (monotonic
// terminal rule). status must be terminal; reason rides the control event so the
// stream records WHY the run ended.
func stopOne(ctx context.Context, x Session, status, reason string) error {
	if isTerminalStatus(x.Status) {
		return nil
	}
	now := time.Now().Unix()
	if evID, err := genID("evt"); err == nil {
		payload, _ := json.Marshal(controlPayload{Command: CmdStop, Message: reason})
		e, aerr := mounted.State.store.AppendEvent(ctx, Event{
			ID: evID, SessionID: x.ID, Org: x.Org, Kind: KindControl,
			Actor: billingActor(x.Org, ""), Payload: string(payload), CreatedAt: now,
		})
		if aerr == nil {
			publishEvent(mounted, x.Org, x.RootID, e)
		}
	}
	x.Status = status
	x.EndedAt = now
	x.UpdatedAt = now
	if err := mounted.State.store.UpdateSession(ctx, x); err != nil {
		return err
	}
	ev, _ := mounted.State.store.CountEvents(ctx, x.Org, x.ID)
	ch, _ := mounted.State.store.CountChildren(ctx, x.Org, x.ID)
	publishSession(mounted, x, ev, ch)
	return nil
}

// CountActiveSessions returns how many of org's sessions matching m are live
// (running|paused) — the device view's "active sessions". Org-scoped; 0 when not
// mounted or the match is empty.
func CountActiveSessions(ctx context.Context, org string, m SessionMatch) (int, error) {
	if mounted == nil {
		return 0, nil
	}
	org = strings.TrimSpace(org)
	if org == "" || m.empty() {
		return 0, nil
	}
	return mounted.State.store.countActiveMatch(ctx, org, m)
}
