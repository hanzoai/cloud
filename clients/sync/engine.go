package sync

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// engine.go is the ONE place a sync happens — a reconcile loop, not a noun. For
// every trigger (a GitHub/Hanzo Git webhook via cloud.Sync, a manual /run, or a chained
// propagation) it resolves the matching Syncs, then for each: guards the loop (an
// event by the Sync's own actor is our echo), dedupes by cursor (an event that
// leaves the cursor unchanged already happened), calls the kind's provider to
// Reconcile, records the new cursor, and PROPAGATES to Syncs whose source is this
// Sync's target — bounded by a hop limit so a cyclic set can never runaway.

// Sync directions. off pauses a sync without deleting it.
const (
	dirBoth = "both"
	dirPull = "pull"
	dirPush = "push"
	dirOff  = "off"
)

func validDirection(d string) bool {
	switch d {
	case dirBoth, dirPull, dirPush, dirOff:
		return true
	}
	return false
}

// dirPulls reports whether a direction drives the INBOUND advance (source → target).
func dirPulls(d string) bool { return d == dirBoth || d == dirPull }

// dirPushes reports whether a direction drives the OUTBOUND mirror (target → source).
func dirPushes(d string) bool { return d == dirBoth || d == dirPush }

// Triggers.
const (
	trigWebhook = "webhook"
	trigPoll    = "poll"
	trigManual  = "manual"
)

func validTrigger(t string) bool {
	switch t {
	case trigWebhook, trigPoll, trigManual:
		return true
	}
	return false
}

// maxHops bounds chained propagation depth: a Sync's target may be another Sync's
// source, so a reconcile fires the next hop — but never more than this many, so a
// cyclic graph terminates even if a provider keeps reporting change. Cursor
// idempotency already collapses the common echo; this is the hard ceiling.
const maxHops = 8

// reconcileEvent is the registered cloud.SyncFunc — the entry every trigger calls.
// It resolves the Syncs an event fires and reconciles each (best-effort: one Sync's
// failure is logged and never blocks the others).
func reconcileEvent(ctx context.Context, se cloud.SyncEvent) (cloud.SyncResult, error) {
	s := mounted.Load()
	if s == nil {
		return cloud.SyncResult{}, cloud.ErrSyncUnavailable
	}
	st, err := storeFor(s, se.Org)
	if err != nil {
		return cloud.SyncResult{}, err
	}
	ev := eventFromSeam(se)
	syncs, err := st.ResolveBySource(ctx, se.Org, se.Kind, ev.Provider)
	if err != nil {
		return cloud.SyncResult{}, err
	}
	var res cloud.SyncResult
	for _, sy := range syncs {
		if runOne(ctx, st, sy, ev) {
			res.Ran++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

// runOne reconciles one Sync for one event: hop guard → direction/off → loop guard
// → cursor idempotency → provider.Reconcile → advance cursor → propagate the chain.
// Returns whether the target changed.
func runOne(ctx context.Context, st *store, sy Sync, ev Event) bool {
	s := mounted.Load()
	if s == nil {
		return false
	}
	if ev.Hop > maxHops {
		s.Log.Warn("sync: hop limit reached", "sync", sy.ID, "hop", ev.Hop)
		return false
	}
	if sy.Direction == dirOff {
		return false
	}
	// Loop guard: an event whose actor is this Sync's own write identity is the echo
	// of our last reconcile. A manual run bypasses it (an explicit human re-sync).
	if !ev.Manual && ev.Actor != "" && strings.EqualFold(ev.Actor, sy.Actor) {
		return false
	}
	prov := providerFor(sy.Kind)
	if prov == nil {
		s.Log.Warn("sync: no provider for kind", "kind", sy.Kind, "sync", sy.ID)
		return false
	}
	// Cursor idempotency: an event at a position the cursor already holds is the
	// identical-fingerprint echo (generalized). Manual runs reconcile regardless.
	cur := parseCursor(sy.Cursor)
	if !ev.Manual && ev.Ref != "" && ev.After != "" && cur[ev.Ref] == ev.After {
		return false
	}
	changed, err := prov.Reconcile(ctx, sy, ev)
	if err != nil {
		s.Log.Warn("sync: reconcile failed", "sync", sy.ID, "kind", sy.Kind, "err", err)
		return false
	}
	if !changed {
		return false
	}
	now := time.Now().Unix()
	if ev.Ref != "" && ev.After != "" {
		cur[ev.Ref] = ev.After
	}
	if err := st.SetCursor(ctx, sy.Org, sy.ID, serializeCursor(cur), now); err != nil {
		s.Log.Warn("sync: set cursor", "sync", sy.ID, "err", err)
	}
	s.Log.Info("sync reconciled", "sync", sy.ID, "kind", sy.Kind, "ref", ev.Ref)
	propagate(ctx, st, sy, ev)
	return true
}

// propagate fires the next hop: Syncs whose SOURCE is this Sync's TARGET (a chain).
// The chained event carries the current Sync's actor, so a Sync that would push
// straight back is caught by the loop guard / cursor at that hop. Bounded by
// maxHops (checked at each runOne entry) so a cyclic graph terminates.
func propagate(ctx context.Context, st *store, sy Sync, ev Event) {
	if sy.Target.Locator == "" || ev.Hop+1 > maxHops {
		return
	}
	next, err := st.ListBySourceLocator(ctx, sy.Org, sy.Target.Locator)
	if err != nil {
		if s := mounted.Load(); s != nil {
			s.Log.Warn("sync: chain lookup", "sync", sy.ID, "err", err)
		}
		return
	}
	for _, downstream := range next {
		if downstream.ID == sy.ID {
			continue // never re-fire the same Sync on its own output
		}
		runOne(ctx, st, downstream, Event{
			Provider: sy.Target.Provider, Org: sy.Org, Locator: sy.Target.Locator,
			Repo: ev.Repo, Ref: ev.Ref, After: ev.After, Actor: sy.Actor, Hop: ev.Hop + 1,
		})
	}
}

// eventFromSeam converts the cross-package cloud.SyncEvent into the in-package Event.
func eventFromSeam(se cloud.SyncEvent) Event {
	return Event{
		Provider: se.Provider, Org: se.Org, Locator: se.Locator, Repo: se.Repo,
		Ref: se.Ref, Before: se.Before, After: se.After, Actor: se.Actor,
		Token: se.Token, Manual: se.Manual, Hop: se.Hop,
	}
}

// parseCursor decodes a Sync's cursor JSON (position→fingerprint). A blank or
// corrupt cursor is an empty map — the engine then treats every position as
// un-applied (safe: the provider's idempotent reconcile is the real guard).
func parseCursor(s string) map[string]string {
	m := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func serializeCursor(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}
