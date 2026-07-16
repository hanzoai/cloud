package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// inproc.go is the IN-PROCESS twin of the /v1/agents/sessions control plane
// (sessions.go): the same store + live ZAP bus, entered directly by another
// in-process cloud subsystem that has ALREADY resolved its tenant server-side —
// exactly as RunOnBehalf is the in-process twin of the /run handler. The coding
// orchestrator (clients/coding) drives a session through these three calls so a
// coding run streams into the SAME registry the console + @hanzo/dev outer agent
// consume, with NO HTTP hop to self and NO second write path.
//
// ISOLATION: org is the ONLY tenant key on every call, threaded straight to the
// org-scoped store methods (CreateSession / GetSession / AppendEvent /
// UpdateSession), so a caller for org A can never open, append to, or close org
// B's session. A nil singleton (never mounted) fails closed with an error; a
// mismatched (org, id) resolves to errSessionNotFound.

// SessionOpen are the attributes of a session to register — the input to
// OpenSession, in the Session{Filter,Match} family. Org and Agent are required;
// Actor falls back to the org, and Title/Surface are optional. Identity (id,
// root, status) and timestamps are stamped by OpenSession, never by the caller.
type SessionOpen struct {
	Org     string
	Actor   string
	Agent   string
	Title   string
	Surface string
}

// OpenSession registers a LIVE root session for x.Org attributed to x.Actor (an
// "org/sub" identity or a bare label) with the given agent label + title, and
// returns its id. The session is born running (not terminal like openRunSession's
// completed one-shot) so a long job streams status/log/tool-call events into it
// until CloseSession moves it to a terminal state. Best-effort live fan-out
// (publish) rides the bus; the store row is the truth.
func OpenSession(ctx context.Context, x SessionOpen) (string, error) {
	if mounted == nil {
		return "", fmt.Errorf("agents: not mounted")
	}
	org := strings.TrimSpace(x.Org)
	if org == "" {
		return "", fmt.Errorf("agents: org required")
	}
	agent := strings.TrimSpace(x.Agent)
	if agent == "" {
		return "", fmt.Errorf("agents: agent required")
	}
	if len(agent) > maxAgentLabel {
		return "", fmt.Errorf("agents: agent label too long")
	}
	actor := strings.TrimSpace(x.Actor)
	if actor == "" {
		actor = billingActor(org, "")
	}
	if len(actor) > maxActor {
		return "", fmt.Errorf("agents: actor too long")
	}
	title := strings.TrimSpace(x.Title)
	if len(title) > maxTitle {
		title = title[:maxTitle]
	}
	surface := strings.TrimSpace(x.Surface)
	if len(surface) > maxSurface {
		return "", fmt.Errorf("agents: surface too long")
	}
	id, err := genID("sess")
	if err != nil {
		return "", fmt.Errorf("agents: rng: %w", err)
	}
	now := time.Now().Unix()
	s := Session{
		ID: id, Org: org, Agent: agent, Actor: actor, Status: StatusRunning,
		RootID: id, Title: title, Surface: surface,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := mounted.State.store.CreateSession(ctx, s); err != nil {
		return "", fmt.Errorf("agents: create session: %w", err)
	}
	publishSession(mounted, s, 0, 0)
	return id, nil
}

// ListSessions returns org's root sessions per filter — the in-process twin of
// GET /v1/agents/sessions. Org is the ONLY tenant key and is passed verbatim to
// the org-scoped store query, so a caller for org A can never enumerate org B's
// sessions. The caller MUST pass an org it already validated. Fails closed (nil,
// error) when the subsystem is not mounted or the org is empty.
func ListSessions(ctx context.Context, org string, f SessionFilter) ([]Session, error) {
	if mounted == nil {
		return nil, fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return nil, fmt.Errorf("agents: org required")
	}
	return mounted.State.store.ListSessions(ctx, org, f)
}

// GetSession returns one of org's sessions and whether it exists — the
// in-process twin of GET /v1/agents/sessions/:id. The (org, id) pair is resolved
// together, so another tenant's id is simply not found: the ownership decision is
// made HERE, on our own row, never delegated. found=false covers both unknown and
// cross-org; a real store failure is the error.
func GetSession(ctx context.Context, org, id string) (Session, bool, error) {
	if mounted == nil {
		return Session{}, false, fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	id = strings.TrimSpace(id)
	if org == "" || id == "" {
		return Session{}, false, nil
	}
	x, err := mounted.State.store.GetSession(ctx, org, id)
	if err == errSessionNotFound {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return x, true, nil
}

// StopSession force-stops ONE of org's live sessions — recording a control "stop"
// event carrying reason and transitioning the session to a terminal state — and
// reports whether a live session was found. It is the single-session twin of
// StopSessions (the login-revoke sweep) and shares its ONE write path (stopOne),
// so every forced teardown records the same event and obeys the same monotonic
// terminal rule. A session of another tenant, or an unknown id, is found=false —
// never another tenant's teardown. An already-terminal session is a no-op
// (found=true, nothing to stop). status is done: a requested stop ended the run,
// it did not fail it.
func StopSession(ctx context.Context, org, id, reason string) (bool, error) {
	x, found, err := GetSession(ctx, org, id)
	if err != nil || !found {
		return false, err
	}
	if isTerminalStatus(x.Status) {
		return true, nil
	}
	if err := stopOne(ctx, x, StatusDone, reason); err != nil {
		return false, err
	}
	return true, nil
}

// LogSessionEvent appends one ordered event (message|tool-call|spawn|log|status|
// control) to an org's session and fans it out live. The (org, id) pair is
// re-resolved so a caller can only write to a session THIS org owns; kind is
// validated against the closed vocabulary and payload is size-bounded + must be
// well-formed JSON (nil payload is allowed for a bare marker). actor falls back
// to the org.
func LogSessionEvent(ctx context.Context, org, sessionID, kind, actor string, payload []byte) error {
	if mounted == nil {
		return fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	sessionID = strings.TrimSpace(sessionID)
	if org == "" || sessionID == "" {
		return fmt.Errorf("agents: org and session id required")
	}
	kind = strings.TrimSpace(kind)
	if !validKind(kind) {
		return fmt.Errorf("agents: invalid event kind %q", kind)
	}
	if len(payload) > maxEventPayload {
		return fmt.Errorf("agents: event payload too large")
	}
	if len(payload) > 0 && !json.Valid(payload) {
		return fmt.Errorf("agents: event payload must be valid JSON")
	}
	x, err := mounted.State.store.GetSession(ctx, org, sessionID)
	if err != nil {
		return err // errSessionNotFound (cross-org / unknown) or a real DB error
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = billingActor(org, "")
	}
	if len(actor) > maxActor {
		actor = actor[:maxActor]
	}
	evID, err := genID("evt")
	if err != nil {
		return fmt.Errorf("agents: rng: %w", err)
	}
	e, err := mounted.State.store.AppendEvent(ctx, Event{
		ID: evID, SessionID: sessionID, Org: org, Kind: kind, Actor: actor,
		Payload: string(payload), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return fmt.Errorf("agents: append event: %w", err)
	}
	publishEvent(mounted, org, x.RootID, e)
	return nil
}

// CloseSession moves an org's session to a terminal state (done|error), stamping
// ended_at, and publishes the update. The store's monotonic-terminal rule already
// forbids reopening a finished run; here we only ever set a terminal status, so a
// double-close is a harmless no-op on an already-terminal row.
func CloseSession(ctx context.Context, org, sessionID, status string) error {
	if mounted == nil {
		return fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	sessionID = strings.TrimSpace(sessionID)
	if org == "" || sessionID == "" {
		return fmt.Errorf("agents: org and session id required")
	}
	if !isTerminalStatus(status) {
		return fmt.Errorf("agents: close status must be done or error")
	}
	x, err := mounted.State.store.GetSession(ctx, org, sessionID)
	if err != nil {
		return err
	}
	if isTerminalStatus(x.Status) {
		return nil // already finished — monotonic, nothing to do
	}
	now := time.Now().Unix()
	x.Status = status
	x.EndedAt = now
	x.UpdatedAt = now
	if err := mounted.State.store.UpdateSession(ctx, x); err != nil {
		return fmt.Errorf("agents: close session: %w", err)
	}
	ev, _ := mounted.State.store.CountEvents(ctx, org, sessionID)
	ch, _ := mounted.State.store.CountChildren(ctx, org, sessionID)
	publishSession(mounted, x, ev, ch)
	return nil
}
