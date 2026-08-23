package channels

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/plane"
)

// ingest is the registered integrations ingress consumer (channels.Mount):
// normalize -> identity -> gate -> route -> inbox | pairing. It runs on a
// detached per-event goroutine with a bounded context
// (integrations.emitIngress), so nothing here can delay a webhook.

// gcEverySec bounds opportunistic retention GC to once per 10 min across all
// ingest goroutines.
const gcEverySec = 600

var lastGC atomic.Int64

func ingest(ctx context.Context, ev plane.ChannelsIngestIn) {
	s := mounted.Load()
	if s == nil {
		return
	}
	st := s.State.store
	tr, ok := transportFor(ev.Provider)
	if !ok {
		return
	}
	m, ok := tr.normalize(ev)
	if !ok {
		return
	}
	// Identity is NOT resolved here. integrations.LinkedSubject is a Go call
	// gated on that package's `mounted` global, and integrations is a different
	// process — it answered "not mounted" every time and left UserID empty while
	// looking best-effort. The turn below resolves the asker properly, over the
	// plane, where the answer can actually cross.
	now := time.Now().Unix()
	// Gate BEFORE any write (C1-F4): a channel_route row is a send capability,
	// so a blocked sender must not mint one.
	var v verdict
	var err error
	if m.Room.Kind == RoomDM {
		v, err = dmGate(ctx, st, ev.Org, m.Channel, m.Sender.ExternalID, ev.Installer, true)
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
		// limiter is the named follow-up.
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
		// AND ANSWER. This is the half that used to live beside the inbox in
		// integrations: every adapter emitted the event here and separately ran a
		// turn of its own, so one message drove two mechanisms. The inbox row above
		// records that it happened; this replies to it.
		spawn(s, ev.Org, func() { turn(s, tr, ev.Org, m) })
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

// serveIngest publishes the inbound door on the plane.
//
// The adapters live in the integrations PROCESS and this inbox lives in this
// one, so the door has to be an address rather than a function pointer. It was a
// pointer — integrations.RegisterIngress, installed at Mount — and a package
// global is per-process: on the emitting side it was nil, and every event
// returned at the nil check. Nothing logged, because dropping is what a nil
// consumer is for. The inbox held nothing and the gates below never ran on real
// traffic for as long as the client existed.
func serveIngest() {
	zip.Post[plane.ChannelsIngestIn, plane.ChannelsIngestOut](cloud.Plane(), "/channels/ingest", planeIngest,
		zip.WithOperationID(plane.ChannelsIngest),
		zip.WithSummary("Take one authenticated inbound chat event from a platform adapter"))
}

// planeIngest answers an adapter's event.
//
// The org travels IN the request rather than coming from the caller's plane
// identity, for the same reason AgentsRunOnBehalf does: the tenant is the one
// that connected the workspace, which the adapter resolved from the signed
// team/guild/chat id, and the adapter plugin's own identity is not it. Taken
// reports whether this inbox carries the transport — a fact worth returning,
// since the silent version of that answer is the bug this door replaces.
func planeIngest(ctx context.Context, in *plane.ChannelsIngestIn) (*plane.ChannelsIngestOut, error) {
	if in == nil || mounted.Load() == nil {
		return &plane.ChannelsIngestOut{}, nil
	}
	if _, ok := transportFor(in.Provider); !ok {
		return &plane.ChannelsIngestOut{}, nil
	}
	ingest(ctx, *in)
	return &plane.ChannelsIngestOut{Taken: true}, nil
}
