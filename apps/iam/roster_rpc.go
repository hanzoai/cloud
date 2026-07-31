// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/zap-proto/zip"
)

// The mailable roster, published on the internal plane.
//
// The identity store has one writer and it is this process. An app that wants to
// know who it may mail used to read the store directly through DB(), which worked
// while every subsystem shared a binary and returns nil the moment one does not —
// so marketing resolved every audience to "IAM unavailable" and could mail nobody.
//
// The projection is deliberately narrow. A caller needs four fields to name a
// person and reach them; handing over model.User would put the whole identity
// record — including the credential columns — on the wire for an audience count.
// Sending only what the question needs is the difference between an op and a
// database connection.

// exposeRoster publishes the roster read. Mount calls it.
//
// The handler is a NAMED function rather than the closure it used to be, because
// zipdoc lifts an op's prose off its handler's doc comment and can lift nothing
// from an anonymous one — the op published a summary, an operationID and no
// description at all, which is a plane call no generated client can explain.
func exposeRoster() {
	zip.Post[struct{}, plane.Roster](cloud.Plane(), "/iam/mailable",
		mailable,
		zip.WithOperationID(plane.IAMMailable),
		zip.WithSummary("Who this org may mail"))
}

// mailable answers with the people in the CALLER'S OWN org who may be sent mail —
// id, owner, name and email, and nothing else.
//
// It is the internal-plane replacement for reading the identity store through DB().
// That read worked only while every subsystem shared one binary and returned nil
// the moment one did not, so marketing resolved every audience to "IAM unavailable"
// and could mail nobody. This op runs in the process that owns the store, so it
// answers wherever the caller happens to live.
//
// The org is taken from the authenticated call and can never be named in an
// argument: it is the tenancy key for the whole identity store, so a caller able to
// pass it could enumerate another tenant's people. A call carrying no org is
// refused, not answered with an empty roster.
//
// The projection is deliberately narrow. Four fields are what it takes to name a
// person and reach them; returning the identity record itself would put every
// credential column on the wire for what is only an audience count.
//
// It fails closed on a store that is not open: this process owns the store, so a
// nil handle is a boot-order fault, and an empty roster would read as "this org has
// nobody" — an announcement that silently reaches no one is worse than one that
// refuses out loud.
func mailable(ctx context.Context, _ *struct{}) (*plane.Roster, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("roster: no org on the call")
	}
	db := DB()
	if db == nil {
		return nil, fmt.Errorf("roster: identity store not open in the process that owns it")
	}
	users, err := iamstore.GetMailableUsers(db, org)
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	out := make([]plane.Recipient, 0, len(users))
	for _, u := range users {
		if u == nil {
			continue
		}
		out = append(out, plane.Recipient{ID: u.Id, Owner: u.Owner, Name: u.Name, Email: u.Email})
	}
	return &plane.Roster{Recipients: out}, nil
}
