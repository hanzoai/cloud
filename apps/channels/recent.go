// Copyright © 2026 Hanzo AI. MIT License.

package channels

import (
	"context"
	"sort"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The last turns of one room, read back out of the inbox.
//
// The inbox has been recording every inbound turn since ingest existed, and
// nothing ever read them. A bridge that sends the agent only the newest message
// makes an assistant that cannot follow a two-line exchange: asked "weather in
// Benicia", then "try again", it asks WHICH CITY — and by the third turn it is
// looking "Benicia" up as an org, because a bare noun with no conversation around
// it looks like a lookup rather than a place. Nothing was missing but the read.

// serveRecent publishes the read. Mount calls it beside serveIngest.
func serveRecent() {
	zip.Post[plane.RecentIn, plane.Recent](cloud.Plane(), "/channels/recent", planeRecent,
		zip.WithOperationID(plane.ChannelsRecent),
		zip.WithSummary("The last turns of one room, oldest first"))
}

// recentLimit is how many turns a caller gets when it does not say, and the most
// it can have when it asks for more.
//
// A history is a prompt, and a prompt costs money per turn, so this is bounded
// where the store is rather than where the caller is. Twenty is enough to follow a
// conversation and small enough that a room with ten thousand messages in it
// cannot make one answer expensive.
const recentLimit, recentMax = 20, 100

// planeRecent answers with the conversation, oldest first.
//
// The ORG is the CALLER'S, read from the plane context and never an argument. It
// can be, because a caller that reaches this op already knows the tenant — it
// resolved it from a signed team/guild/chat id before it could answer at all, and
// it states it on the run context it already builds. An org a caller could PASS is
// an org whose conversations any caller could read, and no amount of the plane
// being unreachable from the edge makes that a good shape.
//
// Order is fixed HERE and not left to the caller. The store returns rows by id and
// a reader needs them in the order they were said; a bridge that had to sort them
// itself is a bridge that will one day forget to, and a transcript in the wrong
// order is worse than none — it invents an exchange that never happened.
func planeRecent(ctx context.Context, in *plane.RecentIn) (*plane.Recent, error) {
	if in == nil || in.Channel == "" || in.Room == "" {
		return nil, zip.ErrBadRequest("recent: channel and room are required")
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("recent: no org on the call")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(503, "recent: channels is not serving")
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = recentLimit
	case limit > recentMax:
		limit = recentMax
	}

	rows, err := s.State.store.recentRoom(ctx, org, in.Channel, in.Room, limit)
	if err != nil {
		return nil, err
	}
	// Newest-first out of the store so the LIMIT keeps the RECENT ones; reversed
	// here so the caller reads a conversation forwards.
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	turns := make([]plane.Turn, 0, len(rows))
	for _, r := range rows {
		if r.Text == "" {
			continue // an event with no words is not a turn
		}
		turns = append(turns, plane.Turn{Sender: r.SenderUser, Text: r.Text, At: r.CreatedAt})
	}
	return &plane.Recent{Turns: turns}, nil
}
