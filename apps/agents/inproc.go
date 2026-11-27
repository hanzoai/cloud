package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/remote"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/zap-proto/zip"
)

// inproc.go is the IN-PROCESS twin of the /v1/agent/sessions control plane
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
	return OpenSessionOn(ctx, org, actor, agent, title, "", "")
}

// OpenSessionOn is OpenSession with the run's dispatch TARGET recorded, so
// mission-control shows a routed run on the machine it was sent to (session.target
// == the target id) exactly as a locally-linked run shows its host. The target is
// re-resolved org-scoped and MUST belong to this org — a session can never claim
// to run on another tenant's machine (the same fail-closed rule sessionContext
// enforces on the HTTP register path) — and be the caller's to reach, so in a
// personal org a stranger's machine is answered as no machine and no session is
// tagged with it. An empty target is the ordinary sandbox session.
//
// conversation is the app conversation the run is part of (talk.go), or "". The
// session is BORN in its tree — under it, on its channel, its person's alone from
// the first write (held) — and never moved there later, so nobody else reads or
// writes the run in between. It must be actor's own conversation; anything else is
// not found and no session is opened.
func OpenSessionOn(ctx context.Context, org, actor, agent, title, target, conversation string) (string, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return "", err
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
	if target = strings.TrimSpace(target); target != "" {
		if _, err := sto.reachable(ctx, org, target); err == errTargetNotFound {
			return "", fmt.Errorf("agents: target not found in this org")
		} else if err != nil {
			return "", fmt.Errorf("agents: resolve target: %w", err)
		}
	}
	id := mint.ID("sess")
	now := time.Now().Unix()
	x := Session{
		ID: id, Org: org, Agent: agent, Actor: actor, Status: StatusRunning,
		RootID: id, Title: title, Target: target,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
		// This is the session that STAYS OPEN — a coding run holds it for as long
		// as the agent is working — so it is the one the sweep bills hour after
		// hour, and the one whose payer has to be right.
		Payer: payerOf(ctx, org).Subject(), MeteredAt: now,
	}
	if conversation = strings.TrimSpace(conversation); conversation != "" {
		c, err := sto.GetSession(ctx, org, conversation)
		if err != nil && err != errSessionNotFound {
			return "", fmt.Errorf("agents: read the conversation: %w", err)
		}
		if err != nil || !talks(c) || c.Actor != actor {
			return "", zip.ErrNotFound("agents: no such conversation of this person's")
		}
		x.ParentID, x.RootID, x.Channel = c.ID, c.RootID, talkChannel
	}
	if err := sto.CreateSession(ctx, x); err != nil {
		return "", fmt.Errorf("agents: create session: %w", err)
	}
	publishSession(mounted, x, 0, 0)
	return id, nil
}

// TagSession records what run an org's session is the view of — its kind,
// repository, base, branch, environment, pull request and sandbox — each
// non-empty field overwriting its column and an empty one left as it was, then
// republishes the session so a live board shows it. The (org, id) pair is
// re-resolved, so a caller can only tag a session THIS org owns.
func TagSession(ctx context.Context, org, sessionID string, t Session) error {
	sto, org, err := mountedStore(org)
	if err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("agents: session id required")
	}
	t = Session{
		Kind: clip(t.Kind, maxAgentLabel), Repo: remote.Clean(clip(t.Repo, maxRepo)),
		Base: clip(t.Base, maxBranch), Branch: clip(t.Branch, maxBranch),
		Environment: clip(t.Environment, maxSessionID), PR: clip(t.PR, maxTerminal),
		Mode: clip(t.Mode, maxAgentLabel), Box: clip(t.Box, maxSessionID),
		Project: clip(t.Project, maxProject),
	}
	if err := sto.TagSession(ctx, org, sessionID, t, time.Now().Unix()); errors.Is(err, errBoxHeld) {
		return zip.ErrConflict(err.Error())
	} else if err != nil {
		return err
	}
	x, err := sto.GetSession(ctx, org, sessionID)
	if err != nil {
		return err
	}
	ev, _ := sto.CountEvents(ctx, org, sessionID)
	publishSession(mounted, x, ev, fanOut(ctx, sto, x))
	return nil
}

// clip trims s and bounds it at n bytes, on a rune boundary.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// LogSessionEvent appends one ordered event (message|tool-call|spawn|log|status|
// control|progress|event) to an org's session and fans it out live. The (org, id) pair is
// re-resolved so a caller can only write to a session THIS org owns; kind is
// validated against the closed vocabulary and payload is size-bounded + must be
// well-formed JSON (nil payload is allowed for a bare marker). actor falls back
// to the org.
func LogSessionEvent(ctx context.Context, org, sessionID, kind, actor string, payload []byte) error {
	sto, org, err := mountedStore(org)
	if err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("agents: session id required")
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
	x, err := sto.GetSession(ctx, org, sessionID)
	if err != nil {
		return err // errSessionNotFound (cross-org / unknown) or a real DB error
	}
	// A progress turn is read BEFORE it is stored, as the route that appends one
	// does: a malformed report is refused whole, not kept as a turn that moved
	// nothing.
	var report Progress
	if kind == KindProgress {
		if report, err = parseReport(payload); err != nil {
			return err
		}
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = billingActor(org, "")
	}
	if len(actor) > maxActor {
		actor = actor[:maxActor]
	}
	evID := mint.ID("evt")
	e, err := sto.AppendEvent(ctx, Event{
		ID: evID, SessionID: sessionID, Org: org, Kind: kind, Actor: actor,
		Payload: string(payload), CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		return fmt.Errorf("agents: append event: %w", err)
	}
	publishEvent(mounted, x, e)
	if kind == KindProgress {
		if err := reported(ctx, mounted, sto, org, x, report, e); err != nil {
			return fmt.Errorf("agents: progress: %w", err)
		}
	}
	return nil
}

// CloseSession moves an org's session to a terminal state (done|error), stamping
// ended_at, and publishes the update. The store's monotonic-terminal rule already
// forbids reopening a finished run; here we only ever set a terminal status, so a
// double-close is a harmless no-op on an already-terminal row.
func CloseSession(ctx context.Context, org, sessionID, status string, errStr ...string) error {
	sto, org, err := mountedStore(org)
	if err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("agents: session id required")
	}
	if !isTerminalStatus(status) {
		return fmt.Errorf("agents: close status must be done or error")
	}
	x, err := sto.GetSession(ctx, org, sessionID)
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
	if len(errStr) > 0 && errStr[0] != "" {
		x.Error = errStr[0]
	}
	if err := sto.UpdateSession(ctx, x); err != nil {
		return fmt.Errorf("agents: close session: %w", err)
	}
	ev, _ := sto.CountEvents(ctx, org, sessionID)
	publishSession(mounted, x, ev, fanOut(ctx, sto, x))
	return nil
}

// ParkSession leaves a running coding run's session waiting to go on by itself
// (Store.Park): paused with its end written, holding no sandbox, saying why (reason:
// ParkLimit, ParkPayment, ParkFault or ParkRestart) and when it is tried again (at, unix seconds, 0 for no
// time). It answers false when the session is not running, or parked already: it ended,
// or somebody stopped it first, and there is nothing to wait for. The update is
// published, so a live board shows it parked.
func ParkSession(ctx context.Context, org, sessionID, reason string, at int64) (bool, error) {
	if reason != ParkLimit && reason != ParkPayment && reason != ParkFault && reason != ParkRestart {
		return false, fmt.Errorf("agents: park reason must be %s, %s, %s or %s", ParkLimit, ParkPayment, ParkFault, ParkRestart)
	}
	sto, org, err := mountedStore(org)
	if err != nil {
		return false, err
	}
	now := time.Now().Unix()
	ok, err := sto.Park(ctx, org, strings.TrimSpace(sessionID), reason, at, now)
	if err != nil || !ok {
		return ok, err
	}
	return true, republish(ctx, sto, org, sessionID)
}

// UnparkSession puts a parked coding run's session back to running (Store.Unpark), and
// answers whether this call did it: of two tries, one. A session somebody stopped, or
// one that is not parked, answers false. The session is re-stamped with the wallet the
// caller is paid from today (ctx states the person), since its runtime is billed to the
// payer it names and the one it named when it began may not be the person's now.
func UnparkSession(ctx context.Context, org, sessionID string) (bool, error) {
	sto, org, err := mountedStore(org)
	if err != nil {
		return false, err
	}
	ok, err := sto.Unpark(ctx, org, strings.TrimSpace(sessionID), payerOf(ctx, org).Subject(), time.Now().Unix())
	if err != nil || !ok {
		return ok, err
	}
	return true, republish(ctx, sto, org, sessionID)
}

// republish tells a live board a session changed.
func republish(ctx context.Context, sto *Store, org, sessionID string) error {
	x, err := sto.GetSession(ctx, org, strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	ev, _ := sto.CountEvents(ctx, org, x.ID)
	publishSession(mounted, x, ev, fanOut(ctx, sto, x))
	return nil
}

// Stopped, when set by the composition root, is told of every session a stop (a
// person's, the reaper's, a logout's) ended, so a parked coding run's wake is
// cancelled with it (apps/coding Released).
var Stopped func(org, session string)

// ParkSaid is what a parked run says about itself, in the one wording the session's
// answer, its stream and its thread's card all use: why it waits, when it goes on by
// itself if anything names a time, that credits added go on at once, and that after
// MaxLife of waiting it goes on only on a person's Continue. A fault is the model API's own
// and credits do not clear it: it is tried again at the time it names. It fits a progress line
// (120 bytes) for any lifetime that reads in whole days or hours.
func ParkSaid(reason string, at int64) string {
	if reason == ParkRestart {
		return "parked: the platform restarted, goes on by itself"
	}
	if reason == ParkFault {
		if at > 0 {
			return "parked: the model API failed, tried again at " + time.Unix(at, 0).UTC().Format("2006-01-02 15:04 MST") +
				"; after " + lasting(MaxLife()) + ", on Continue"
		}
		return "parked: the model API failed, goes on on Continue"
	}
	why := "usage limit"
	if reason == ParkPayment {
		why = "payment refused"
	}
	say := "parked: " + why + ", goes on when credits are added"
	if at > 0 {
		say = "parked: " + why + ", goes on at " + time.Unix(at, 0).UTC().Format("2006-01-02 15:04 MST") + " or when credits are added"
	}
	return say + "; after " + lasting(MaxLife()) + ", on Continue"
}

// lasting is a duration in the words a person reads: whole days or hours, else minutes.
func lasting(d time.Duration) string {
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	case d >= time.Hour && d%time.Hour == 0:
		if d == time.Hour {
			return "1 hour"
		}
		return strconv.Itoa(int(d/time.Hour)) + " hours"
	}
	return strconv.Itoa(max(int(d/time.Minute), 1)) + " minutes"
}
