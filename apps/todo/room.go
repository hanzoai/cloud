package todo

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// settled reports that a status ENDS the work rather than positioning it.
//
// It is one function because the estate had the pair spelled inline in two
// places — the forge PATCH closes the upstream issue on it (source.go) and the
// GitHub mirror maps a closed issue onto it (github_sink.go) — and a third
// reader deciding for itself is how a board's "open" count comes to disagree with
// which issues the forge has closed.
func settled(status string) bool { return status == "done" || status == "canceled" }

// roomRef addresses one collaboration room's work.
type roomRef struct {
	// Room is the room, spelled "<workspace>_<room>" — the same value
	// GET /v1/meet/call answers with, so a channel's call and its work name the
	// room identically. From the path.
	Room string `json:"room"`
	// Project is the IAM project whose index to read; empty reads the org's
	// default, which is where every mirrored source lands. It is the same
	// defaulting GET /v1/todo/issues does, deliberately: this rollup must count
	// exactly the rows that listing returns, and two different defaults would let
	// a channel's header disagree with its own list.
	Project string `json:"project" url:"-"`
}

// roomWork is what a channel's work looks like at a glance: how much is open, how
// it is spread across the board's columns, and when any of it last moved.
//
// It is COUNTS and not rows. The rows are GET /v1/todo/issues?room=<room>, which
// is the same filter over the same store — so this is a second PROJECTION of one
// query rather than a second answer to it, and a surface that renders both cannot
// show a header disagreeing with the list beneath it.
type roomWork struct {
	// Room is the room these counts are for, echoed back as it was resolved.
	Room string `json:"room"`
	// Open is how many items are still work: everything whose status does not
	// end it. It is the number a channel header shows.
	Open int `json:"open"`
	// Total is every item bound to this room, settled ones included, so Total
	// minus Open is what the room has finished.
	Total int `json:"total"`
	// Status is the count per board column, carrying EVERY column this surface
	// knows — an empty column reads 0 rather than being absent, so a caller can
	// render the board without inventing the vocabulary. The keys are the same
	// closed set every other operation here validates against.
	Status map[string]int `json:"status"`
	// Updated is when anything in this room's work last moved, in unix seconds.
	// ABSENT when the room has no work at all: zero would read as the epoch, and
	// a room nobody has filed anything in has no last activity rather than an
	// infinitely old one. Total is 0 in exactly that case.
	Updated int64 `json:"updated,omitempty"`
}

// roomWorkOf summarises one room's work.
//
// The room is opaque here and is deliberately not resolved: this package cannot
// say whether a room exists — apps/team owns that document — so an unknown room
// answers an EMPTY board rather than a 404. That is the honest answer and the
// useful one: a channel that has never had an item filed in it and a channel id
// that was mistyped both have no work, and inventing a distinction would require
// this surface to hold a second copy of the room list (HIP-0523 §2 forbids it,
// and it would drift the first time a room was renamed).
//
// Tenancy is the validated principal's org and nothing else, so a caller cannot
// read another tenant's channel by naming its room.
func (o ops) roomWorkOf(ctx context.Context, in *roomRef) (*roomWork, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	room := strings.TrimSpace(in.Room)
	if room == "" {
		return nil, zip.ErrBadRequest("which room?")
	}
	proj := strings.TrimSpace(in.Project)
	if proj == "" {
		proj = principal.DefaultProject
	}
	store, err := storeFor(o.s, org, proj)
	if err != nil {
		return nil, err
	}
	// projectID "" spans every board in the store, which is the whole point: the
	// work a channel is about is not confined to one repository's board.
	rows, err := store.ListIssues(ctx, org, "", IssueFilter{Room: room})
	if err != nil {
		return nil, err
	}

	out := &roomWork{Room: room, Total: len(rows), Status: make(map[string]int, len(statuses))}
	// Seed every column at zero from the app's OWN closed set rather than from a
	// list spelled here — the same reason the tool schema's enum is rendered from
	// it. A column added there appears here with no edit.
	for s := range statuses {
		out.Status[s] = 0
	}
	for _, r := range rows {
		out.Status[r.Status]++
		if !settled(r.Status) {
			out.Open++
		}
		if r.UpdatedAt > out.Updated {
			out.Updated = r.UpdatedAt
		}
	}
	return out, nil
}
