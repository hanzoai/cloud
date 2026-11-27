package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/mint"
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
	Session  string // one session, still only among the actor's own
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
	var b binder
	// ended_at=0: a session whose run is over is not live, paused or not — the app's
	// conversation resting between turns (Store.Rest) has nothing to stop or count. A
	// PARKED run has its end written and is still live: it goes on by itself, as its
	// person, so a revoke that tears down that person's sessions reaches it too.
	where := "org=" + b.bind(org) + " AND actor=" + b.bind(strings.TrimSpace(m.Actor)) +
		" AND status IN (" + b.bind(StatusRunning) + "," + b.bind(StatusPaused) + ") AND (ended_at=0 OR park<>'')"
	if id := strings.TrimSpace(m.Session); id != "" {
		where += " AND id=" + b.bind(id)
	}
	if h := strings.TrimSpace(m.Host); h != "" {
		where += " AND host=" + b.bind(h)
	}
	if p := strings.TrimSpace(m.Provider); p != "" {
		where += " AND provider=" + b.bind(p)
	}
	if a := strings.TrimSpace(m.Account); a != "" {
		where += " AND account=" + b.bind(a)
	}
	return where, b.args
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
// row. A match with no actor stops nothing (fail-closed).
//
// Absence is an ERROR (ErrNoPeer), never (0, nil). It was the latter, and agents
// ships as its own binary — so in the fleet the link process that calls this has
// never had the session store in it, and every credential revoke answered 200
// with {"sessionsStopped":0} having torn down nothing, while the sessions kept
// running under the revoked account. A zero that means "I could not ask" is
// indistinguishable from "there were none", and this is the client where that
// distinction is the security property. Callers take the plane leg (apps/links).
func StopSessions(ctx context.Context, org string, m SessionMatch) (int, error) {
	if m.empty() {
		return 0, nil // fail-closed: a match with no actor stops nothing, and that is an answer
	}
	sto, org, serr := mountedStore(org)
	if serr != nil {
		return 0, serr
	}
	live, err := sto.listActiveMatch(ctx, org, m)
	if err != nil {
		return 0, err
	}
	stopped := 0
	for _, x := range live {
		if _, _, err := stop(ctx, sto, x, why(m)); err != nil {
			// Best-effort per session: a failure on one does not abort the rest, so a
			// revoke tears down as many as it can and reports the true count.
			mounted.Log.Warn("agents: stop session", "org", org, "session", x.ID, "err", err)
			continue
		}
		stopped++
	}
	return stopped, nil
}

// StopSession stops one session of org for a reason the run's stream and its card tell:
// the stop of its owner's choosing, but made by the platform. It is what ends a parked
// coding run whose person can no longer act in the org (apps/coding park.go). It answers
// false when the session was not live: ended already, or not there.
func StopSession(ctx context.Context, org, sessionID, reason string) (bool, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return false, err
	}
	x, err := sto.GetSession(ctx, org, strings.TrimSpace(sessionID))
	if err != nil {
		if errors.Is(err, errSessionNotFound) {
			return false, nil
		}
		return false, err
	}
	if isTerminalStatus(x.Status) {
		return false, nil
	}
	if _, _, err := stop(ctx, sto, x, reason); err != nil {
		return false, err
	}
	return true, nil
}

// why is the sentence a stop carries: a named session was stopped by its
// owner; a sweep over everything an account ran is a logout.
func why(m SessionMatch) string {
	if strings.TrimSpace(m.Session) != "" {
		return "stopped by its owner"
	}
	return "account logged out via login manager"
}

// ErrStopped and ErrPaused are the causes a stopped or paused run's context
// carries, so the run can tell being stopped from running out of time — and its
// box can tell a run that is coming back from one that is over (box.go), and a
// coding run which work to keep (Hold).
var (
	ErrStopped = errors.New("stopped")
	ErrPaused  = errors.New("paused")
)

// running is the runs executing in this process.
var running = newRegistry()

// Hold registers a run of session id that executes in this process but not in
// this package — a coding run (apps/coding) — so every stop and pause of the
// session reaches it exactly as it reaches a turn: its context is cancelled with
// ErrStopped or ErrPaused as the cause, whoever asked, from wherever. done says
// it has ended. A session something already holds answers false.
func Hold(id string, cancel context.CancelCauseFunc) (done func(), ok bool) {
	if !hold(id, cancel) {
		return func() {}, false
	}
	return func() { release(id) }, true
}

// hold registers the run executing session id, and refuses when one already is:
// a session runs one run at a time here, so the resume of a turn whose last run
// is still unwinding is refused rather than racing it for the one slot — where
// the loser's release would take the winner's cancel with it.
func hold(id string, cancel context.CancelCauseFunc) bool { return running.hold(id, cancel) }

func release(id string) { running.release(id) }

// stop ends a live session, whoever asked: its owner from a chat, the steering
// API, a logout, or the reaper. It records the stop, cancels the run if it
// executes here, forwards to the workflow if one executes it elsewhere, and
// moves the session to stopped. A session already terminal is left alone. It
// returns the recorded stop and whether a workflow was told.
func stop(ctx context.Context, sto *Store, x Session, reason string) (Event, bool, error) {
	if isTerminalStatus(x.Status) {
		return Event{}, false, nil
	}
	now := time.Now().Unix()
	payload, _ := json.Marshal(controlPayload{Command: CmdStop, Message: reason})
	e, aerr := sto.AppendEvent(ctx, Event{
		ID: mint.ID("evt"), SessionID: x.ID, Org: x.Org, Kind: KindControl,
		Actor: billingActor(x.Org, ""), Payload: string(payload), CreatedAt: now,
	})
	if aerr == nil {
		publishEvent(mounted, x, e)
	}
	running.interrupt(ctx, x.ID, fmt.Errorf("%w: %s", ErrStopped, reason))
	forwarded := false
	if x.TaskWorkflowID != "" && mounted != nil && mounted.State.tasks != nil && mounted.State.tasks.Enabled() {
		if err := mounted.State.tasks.Cancel(ctx, x.TaskWorkflowID, x.TaskRunID, reason); err != nil {
			mounted.Log.Warn("agents: stop: workflow cancel", "session", x.ID, "err", err)
		} else {
			forwarded = true
		}
	}
	ended, err := sto.End(ctx, x.Org, x.ID, StatusStopped, now)
	if err != nil || !ended {
		return e, forwarded, err
	}
	if Stopped != nil {
		Stopped(x.Org, x.ID)
	}
	// A paused or parked session's end was written when it paused, and End keeps it.
	if x.EndedAt == 0 {
		x.EndedAt = now
	}
	x.Status, x.UpdatedAt = StatusStopped, now
	ev, _ := sto.CountEvents(ctx, x.Org, x.ID)
	publishSession(mounted, x, ev, fanOut(ctx, sto, x))
	return e, forwarded, nil
}

// CountActiveSessions returns how many of org's sessions matching m are live
// (running|paused) — the device view's "active sessions". Org-scoped; 0 when not
// mounted or the match is empty.
func CountActiveSessions(ctx context.Context, org string, m SessionMatch) (int, error) {
	if m.empty() {
		return 0, nil // the same fail-closed answer as StopSessions
	}
	sto, org, serr := mountedStore(org)
	if serr != nil {
		return 0, serr
	}
	return sto.countActiveMatch(ctx, org, m)
}
