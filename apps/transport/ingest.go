package transport

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// ingest is the registered integrations ingress consumer (transports.Use):
// normalize -> identity -> gate -> route -> inbox | pairing. It runs on a
// detached per-event goroutine with a bounded context
// (provider.emitIngress), so nothing here can delay a webhook.

// gcEverySec bounds opportunistic retention GC to once per 10 min across all
// ingest goroutines.
const gcEverySec = 600

var lastGC atomic.Int64

func ingest(ctx context.Context, ev client.TransportIngestIn) {
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
	// Identity is NOT resolved here. A Go call into integrations is gated on that
	// package's `mounted` global, and integrations is a different process, so it
	// would answer "not mounted" every time and leave UserID empty while looking
	// best-effort. The turn below resolves the asker over the plane, where the
	// answer can actually cross.
	now := time.Now().Unix()
	// Gate BEFORE any write (C1-F4): a channel_route row is a send capability,
	// so a blocked sender must not mint one.
	var v verdict
	var err error
	if m.Channel.Kind == ChannelDM {
		v, err = dmGate(ctx, st, ev.Org, m.Transport, m.Sender.ExternalID, ev.Installer, true)
	} else {
		// A thread is a group surface: ChannelThread deliberately gates under the
		// group policy.
		v, err = groupGate(ctx, st, ev.Org, m.Transport, m.Sender.ExternalID)
	}
	if err != nil {
		// Fail closed: an unreadable policy runs nothing. It is SAID, because a
		// store that cannot be read is our failure and not the sender's answer —
		// and a question that gets no reply reads as a bot that is not there. No
		// sender ids in logs — reason codes only.
		s.Log.Warn("transport: gate error, inbound dropped", "transport", m.Transport, "err", err)
		tell(ctx, s, tr, ev.Org, m, policyUnreadable)
		return
	}
	if v.Allow || v.Pair {
		// Route capture on allow AND pair — the pairing reply below must be able
		// to ride the teams transport — but the two grants are not the same grant,
		// so they do not last the same time. An ALLOWED sender's route lasts; a
		// PAIRING sender is one the org has explicitly not approved yet, so their
		// route lapses exactly when their pairing request does. Approve them and
		// their next message arrives on the allow branch, which makes it lasting.
		expires := int64(0)
		if !v.Allow {
			expires = now + int64(pairTTL/time.Second)
		}
		if rerr := st.upsertRoute(ctx, ev.Org, m.Transport, m.Channel.ID, ev.ReplyRoot, now, expires); rerr != nil {
			s.Log.Warn("transport: route upsert", "transport", m.Transport, "err", rerr)
		}
	}
	switch {
	case v.Allow:
		// ACCEPTED TRADEOFF (C1-F3): under groupPolicy=open any group member
		// inserts inbox rows; event-key dedupe, 8 KiB truncation, 30-day GC, and
		// single-conn SQLite serialization bound the damage. A per-org ingest
		// limiter is the named follow-up.
		if ierr := st.insertInbox(ctx, inboxRow{
			Org:         ev.Org,
			Transport:   m.Transport,
			Account:     m.Account,
			ChannelID:   m.Channel.ID,
			ChannelKind: m.Channel.Kind,
			Sender:      m.Sender.ExternalID,
			// SenderUser is the row's OWNER: the inbox lists a row to this subject
			// and to no one else (store.listInbox), and a row without one only to a
			// SuperAdmin. It may only ever be the sender's OWN linked account. The
			// subject a turn runs as is not that: for a member who never linked,
			// integrations answers with the installer's account (chatIdentity,
			// apps/provider/channel.go), and writing that here would list every
			// unlinked member's messages to whoever installed the workspace. No
			// normalizer sets UserID today, so every row is stored with no owner.
			SenderUser: m.Sender.UserID,
			Text:       m.Text,
			ReplyTo:    m.ReplyTo,
			EventKey:   m.Idempotency,
			CreatedAt:  now,
		}); ierr != nil {
			s.Log.Warn("transport: inbox insert", "transport", m.Transport, "err", ierr)
		}
		// AND ANSWER. This is the half that used to live beside the inbox in
		// integrations: every adapter emitted the event here and separately ran a
		// turn of its own, so one message drove two mechanisms. The inbox row above
		// records that it happened; this replies to it.
		//
		// A full pool sheds the turn, and says so: shedding used to drop the
		// question without a word.
		if !spawn(s, ev.Org, func() { turn(s, tr, ev.Org, m) }) {
			tell(ctx, s, tr, ev.Org, m, busyText)
		}
	case v.Pair:
		code, created, perr := upsertPairing(ctx, st, ev.Org, m.Transport, m.Sender.ExternalID, now)
		if perr != nil {
			s.Log.Warn("transport: pairing", "transport", m.Transport, "err", perr)
		} else if created {
			// Reply only when a request was minted (at most one per TTL per
			// sender; a full pending cap mints nothing). Ordering invariant: the
			// route upserted above is what lets this send pass the discord/teams
			// binding checks, and a slack/telegram chat is org-bound by the very
			// event that arrived — no binding special case needed. The pairing
			// message is never stored in the inbox and the code is never logged.
			if _, serr := tr.send(ctx, s, ev.Org, Message{
				Transport: m.Transport,
				Account:   m.Account,
				Channel:   m.Channel,
				ReplyTo:   m.ReplyTo,
				Text:      pairingText(code),
			}); serr != nil {
				s.Log.Warn("transport: pairing reply", "transport", m.Transport, "err", serr)
			}
		}
	default:
		// Blocked: closed reason code only — never sender ids.
		s.Log.Debug("transport: inbound blocked", "transport", m.Transport, "reason", string(v.Reason))
	}
	// Opportunistic retention GC, at most once per gcEverySec across goroutines.
	if last := lastGC.Load(); now-last > gcEverySec && lastGC.CompareAndSwap(last, now) {
		if gerr := st.gc(ctx, now); gerr != nil {
			s.Log.Warn("transport: gc", "err", gerr)
		}
	}
}

// tell answers m with one sentence of ours, where its turn would have answered,
// ONCE: it is said on the ingest path, which a retrying send would hold open.
func tell(ctx context.Context, s *cloud.Service[state], tr transport, org string, m Message, text string) {
	reply := Message{Transport: m.Transport, Account: m.Account, Channel: m.Channel, ReplyTo: answerIn(m), Text: text}
	if _, err := tr.send(ctx, s, org, reply); err != nil {
		s.Log.Warn("transport: could not say why a message went unanswered", "transport", m.Transport, "err", err)
	}
}

func pairingText(code string) string {
	return "Pairing code: " + code + " — an org admin can approve it in the Hanzo console (expires in 1 hour)."
}

// serveIngest publishes the inbound endpoint on the plane.
//
// The adapters live in the integrations PROCESS and this inbox lives in this
// one, so the endpoint has to be an address rather than a function pointer. It was a
// pointer — provider.RegisterIngress, installed at Mount — and a package
// global is per-process: on the emitting side it was nil, and every event
// returned at the nil check. Nothing logged, because dropping is what a nil
// consumer is for. The inbox held nothing and the gates below never ran on real
// traffic for as long as the client existed.
func serveIngest() {
	cloud.Plane().Post("/transport/ingest", planeIngest,
		zip.WithOperationID(client.TransportIngest),
		zip.WithSummary("Take one authenticated inbound chat event from a platform adapter"))
}

// planeIngest answers an adapter's event.
//
// The org travels IN the request rather than coming from the caller's plane
// identity, for the same reason AgentsRunOnBehalf does: the tenant is the one
// that connected the workspace, which the adapter resolved from the signed
// team/guild/chat id, and the adapter plugin's own identity is not it. Taken
// reports whether this inbox carries the transport — a fact worth returning,
// since the silent version of that answer is the bug this endpoint replaces.
func planeIngest(ctx context.Context, in *client.TransportIngestIn) (*client.TransportIngestOut, error) {
	if in == nil || mounted.Load() == nil {
		return &client.TransportIngestOut{}, nil
	}
	if _, ok := transportFor(in.Provider); !ok {
		return &client.TransportIngestOut{}, nil
	}
	ingest(ctx, *in)
	return &client.TransportIngestOut{Taken: true}, nil
}
