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

// OpenSession registers a LIVE root session for org attributed to actor (an
// "org/sub" identity or a bare label) with the given agent label + title, and
// returns its id. The session is born running (not terminal like openRunSession's
// completed one-shot) so a long job streams status/log/tool-call events into it
// until CloseSession moves it to a terminal state. Best-effort live fan-out
// (publish) rides the bus; the store row is the truth.
func OpenSession(ctx context.Context, org, actor, agent, title string) (string, error) {
	if mounted == nil {
		return "", fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return "", fmt.Errorf("agents: org required")
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return "", fmt.Errorf("agents: agent required")
	}
	if len(agent) > maxAgentLabel {
		return "", fmt.Errorf("agents: agent label too long")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = billingActor(org, "")
	}
	if len(actor) > maxActor {
		return "", fmt.Errorf("agents: actor too long")
	}
	title = strings.TrimSpace(title)
	if len(title) > maxTitle {
		title = title[:maxTitle]
	}
	id, err := genID("sess")
	if err != nil {
		return "", fmt.Errorf("agents: rng: %w", err)
	}
	now := time.Now().Unix()
	x := Session{
		ID: id, Org: org, Agent: agent, Actor: actor, Status: StatusRunning,
		RootID: id, Title: title,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := mounted.State.store.CreateSession(ctx, x); err != nil {
		return "", fmt.Errorf("agents: create session: %w", err)
	}
	publishSession(mounted, x, 0, 0)
	return id, nil
}

// OpenSessionOn is OpenSession with the run's dispatch TARGET recorded, so
// mission-control shows a routed run on the machine it was sent to (session.target
// == the target id) exactly as a locally-linked run shows its host. The target is
// re-resolved org-scoped and MUST belong to this org — a session can never claim
// to run on another tenant's machine (the same fail-closed rule sessionContext
// enforces on the HTTP register path). An empty target falls back to OpenSession.
func OpenSessionOn(ctx context.Context, org, actor, agent, title, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return OpenSession(ctx, org, actor, agent, title)
	}
	if mounted == nil {
		return "", fmt.Errorf("agents: not mounted")
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return "", fmt.Errorf("agents: org required")
	}
	if _, err := mounted.State.store.GetTarget(ctx, org, target); err != nil {
		if err == errTargetNotFound {
			return "", fmt.Errorf("agents: target not found in this org")
		}
		return "", fmt.Errorf("agents: resolve target: %w", err)
	}
	id, err := OpenSession(ctx, org, actor, agent, title)
	if err != nil {
		return "", err
	}
	// Stamp the target onto the freshly-opened row (org-scoped update); a failure
	// here is non-fatal — the session is live, it simply lacks its machine tag.
	if x, gerr := mounted.State.store.GetSession(ctx, org, id); gerr == nil {
		x.Target = target
		x.UpdatedAt = time.Now().Unix()
		_ = mounted.State.store.UpdateSession(ctx, x)
	}
	return id, nil
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
