package automations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// trigger.go is the IFTTT inbound plane: it turns an external event (a provider
// webhook, an inbound channel message, a scheduler tick, an internal producer) into
// durable runs of the flows that subscribe to it. It is the inbound analog of the
// engine's outbound step execution — the "IF this happens" half of "IF this THEN
// that". It composes the SAME store, the SAME durable engine (via runStarter →
// executeFlow), and the SAME exactly-once run bookkeeping (recordRunStart) the manual
// /run and cron paths use; it invents no second engine.

// TriggerEvent is the ONE inbound shape every trigger source hands to Deliver.
// Source/Name select which flows fire (they equal a flow trigger's pieceName /
// triggerName); Payload is threaded into each run as {{trigger.*}}; DedupeKey makes a
// re-delivered event idempotent (a flow fires at most once per DedupeKey).
type TriggerEvent struct {
	Source    string         // provider slug == the flow trigger's pieceName ("github","stripe","slack",…)
	Name      string         // event name == the flow trigger's triggerName ("push","charge","message",…)
	DedupeKey string         // idempotency key; "" ⇒ every delivery is its own run
	Payload   map[string]any // the event body, readable by actions as {{trigger.field}}
}

// ErrNoOrg is returned when Deliver is called without a server-verified org. The org
// is the sole tenant key, so a missing/invalid one must start NOTHING (fail-closed).
var ErrNoOrg = errors.New("automations: Deliver requires a server-verified org")

// Deliver fires every ENABLED flow in org whose WEBHOOK/APP_WEBHOOK trigger matches
// (ev.Source, ev.Name), starting each as a durable run with ev.Payload threaded in,
// and returns how many runs it started.
//
// TENANT ISOLATION — org is the ONLY tenant key and MUST be a server-verified tenant
// (resolved from a signed principal, or a signature-verified webhook via
// integrations.OrgForExternalID) — NEVER a client-supplied field. MatchTriggers leads
// its index with org, so an event delivered for org A can only ever start org A's flows.
//
// IDEMPOTENCY — with a DedupeKey the run id is deterministic in
// (org, flowID, Source, Name, DedupeKey), and CreateRunIfAbsent is the atomic gate: a
// re-delivered event finds the row already present and skips it, so a flow fires at
// most once per event. Without a DedupeKey every delivery is its own run.
//
// FAIL-CLOSED — an empty/invalid org, or the engine not being ready, returns an error
// and starts nothing. A flow that was disabled or deleted since it subscribed is
// skipped (the index is a fast lookup; the flow's live status is authoritative).
func Deliver(ctx context.Context, org string, ev TriggerEvent) (started int, err error) {
	if org == "" || !validOrg(org) {
		return 0, ErrNoOrg
	}
	s := mounted
	if s == nil {
		return 0, ErrEngineNotReady
	}
	subs, err := s.State.store.MatchTriggers(ctx, org, ev.Source, ev.Name)
	if err != nil {
		return 0, err
	}
	for _, sub := range subs {
		f, ferr := s.State.store.GetFlow(ctx, org, sub.FlowID)
		if ferr != nil || f.Status != FlowEnabled {
			continue // disabled/deleted since it subscribed — status is authoritative
		}
		v, verr := s.State.store.GetVersion(ctx, org, sub.VersionID)
		if verr != nil {
			continue
		}
		runID := deliverRunID(org, sub.FlowID, ev)
		now := time.Now().UnixMilli()
		created, cerr := s.State.store.CreateRunIfAbsent(ctx, FlowRun{
			ID: runID, Org: org, FlowID: f.ID, FlowVersionID: v.ID, WorkflowID: runID,
			Status: RunRunning, StartTime: now, Created: now, Updated: now,
		})
		if cerr != nil {
			return started, cerr
		}
		if !created {
			continue // idempotent: this event already delivered this flow
		}
		in := FlowRunInput{
			Owner:         org, // VALIDATED org — the cred scope + isolation boundary; NEVER from the event
			FlowID:        f.ID,
			FlowVersionID: v.ID,
			RunID:         runID,
			Steps:         flattenSteps(&v),
			Trigger:       ev.Payload,
		}
		if _, serr := runStarter(ctx, in); serr != nil {
			// The row exists (visible in listRuns) but the durable start failed. Mark it
			// FAILED and stop: the row stays, so a re-delivery with the same DedupeKey is a
			// no-op (at-most-once — a financial trigger must never double-fire on retry).
			_ = s.State.store.UpdateRunStatus(ctx, org, runID, RunFailed, now, now)
			return started, serr
		}
		started++
	}
	return started, nil
}

// deliverRunID is the run/workflow id for a delivered event. With a DedupeKey it is
// deterministic in (org, flowID, source, name, dedupeKey) so a re-delivery maps to the
// SAME id and CreateRunIfAbsent makes the second delivery a no-op. Without a DedupeKey
// it is a fresh random id, so every delivery is its own run.
func deliverRunID(org, flowID string, ev TriggerEvent) string {
	if ev.DedupeKey == "" {
		if id, err := genID("run"); err == nil {
			return id
		}
		// genID only fails if the process RNG is broken; fall through to a content id so
		// Deliver still makes progress and stays idempotent on a retry.
	}
	sum := sha256.Sum256([]byte(org + "\x00" + flowID + "\x00" + ev.Source + "\x00" + ev.Name + "\x00" + ev.DedupeKey))
	return "evt_" + hex.EncodeToString(sum[:16])
}
