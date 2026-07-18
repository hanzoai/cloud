package channels

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/clients/integrations"
)

// ingest is the registered integrations ingress consumer (channels.Mount):
// normalize -> identity -> gate -> route -> inbox | pairing. It runs on a
// detached per-event goroutine with a bounded context
// (integrations.emitIngress), so nothing here can delay a webhook.

// gcEverySec bounds opportunistic retention GC to once per 10 min across all
// ingest goroutines.
const gcEverySec = 600

var lastGC atomic.Int64

func ingest(ctx context.Context, ev integrations.IngressEvent) {
	s := mounted.Load()
	if s == nil {
		return
	}
	st := s.State.store
	tr, ok := transportFor(ev.In.Provider)
	if !ok {
		return
	}
	m, ok := tr.normalize(ev)
	if !ok {
		return
	}
	// Identity is best-effort: an unlinked user or KMS-down leaves UserID empty
	// and never blocks ingest.
	if subj, found, err := integrations.LinkedSubject(ev.Org, ev.In.Provider, ev.In.User); err == nil && found {
		m.Sender.UserID = subj
	}
	now := time.Now().Unix()
	// Gate BEFORE any write (C1-F4): a channel_route row is a send capability,
	// so a blocked sender must not mint one.
	var v verdict
	var err error
	if m.Room.Kind == RoomDM {
		v, err = dmGate(ctx, st, ev.Org, m.Channel, m.Sender.ExternalID, true)
	} else {
		// A thread is a group surface: RoomThread deliberately gates under the
		// group policy.
		v, err = groupGate(ctx, st, ev.Org, m.Channel, m.Sender.ExternalID)
	}
	if err != nil {
		// Fail closed: an unreadable policy drops the event. No sender ids in
		// logs — reason codes only.
		s.Log.Warn("channels: gate error, inbound dropped", "channel", m.Channel, "err", err)
		return
	}
	if v.Allow || v.Pair {
		// Route capture on allow AND pair — the pairing reply below must be able
		// to ride the teams door. Upserted for all four transports; only discord
		// (row presence = egress capability, ReplyRoot "") and teams (the
		// JWT-verified serviceURL) read it — slack/telegram bind egress via
		// per-org token / OrgForExternalID instead.
		if rerr := st.upsertRoute(ctx, ev.Org, m.Channel, m.Room.ID, ev.ReplyRoot, now); rerr != nil {
			s.Log.Warn("channels: route upsert", "channel", m.Channel, "err", rerr)
		}
	}
	switch {
	case v.Allow:
		// ACCEPTED TRADEOFF (C1-F3): under groupPolicy=open any group member
		// inserts inbox rows; event-key dedupe, 8 KiB truncation, 30-day GC, and
		// single-conn SQLite serialization bound the damage. A per-org ingest
		// limiter is the named follow-up alongside agent delivery.
		// Agent delivery is NOT built this pass: this insert is the seam a
		// future channels.RegisterDelivery consumer will observe.
		if ierr := st.insertInbox(ctx, inboxRow{
			Org:        ev.Org,
			Channel:    m.Channel,
			Account:    m.Account,
			RoomID:     m.Room.ID,
			RoomKind:   m.Room.Kind,
			Sender:     m.Sender.ExternalID,
			SenderUser: m.Sender.UserID,
			Text:       m.Text,
			ReplyTo:    m.ReplyTo,
			EventKey:   m.Idempotency,
			CreatedAt:  now,
		}); ierr != nil {
			s.Log.Warn("channels: inbox insert", "channel", m.Channel, "err", ierr)
		}
	case v.Pair:
		code, created, perr := upsertPairing(ctx, st, ev.Org, m.Channel, m.Sender.ExternalID, now)
		if perr != nil {
			s.Log.Warn("channels: pairing", "channel", m.Channel, "err", perr)
		} else if created {
			// Reply only when a request was minted (at most one per TTL per
			// sender; a full pending cap mints nothing). Ordering invariant: the
			// route upserted above is what lets this send pass the discord/teams
			// binding checks, and a slack/telegram chat is org-bound by the very
			// event that arrived — no binding special case needed. The pairing
			// message is never stored in the inbox and the code is never logged.
			if _, serr := tr.send(ctx, s, ev.Org, Message{
				Channel: m.Channel,
				Account: m.Account,
				Room:    m.Room,
				ReplyTo: m.ReplyTo,
				Text:    pairingText(code),
			}); serr != nil {
				s.Log.Warn("channels: pairing reply", "channel", m.Channel, "err", serr)
			}
		}
	default:
		// Blocked: closed reason code only — never sender ids.
		s.Log.Debug("channels: inbound blocked", "channel", m.Channel, "reason", string(v.Reason))
	}
	// Opportunistic retention GC, at most once per gcEverySec across goroutines.
	if last := lastGC.Load(); now-last > gcEverySec && lastGC.CompareAndSwap(last, now) {
		if gerr := st.gc(ctx, now); gerr != nil {
			s.Log.Warn("channels: gc", "err", gerr)
		}
	}
}

func pairingText(code string) string {
	return "Pairing code: " + code + " — an org admin can approve it in the Hanzo console (expires in 1 hour)."
}
