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
func exposeRoster() {
	zip.Post[struct{}, plane.Roster](cloud.Plane(), "/iam/mailable",
		func(ctx context.Context, _ *struct{}) (*plane.Roster, error) {
			// The ORG comes from the caller and nothing else. It is the tenancy key
			// for the whole identity store, so a caller that could name it in an
			// argument could enumerate another tenant's people.
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("roster: no org on the call")
			}
			db := DB()
			if db == nil {
				// This process owns the store. Nil here is a boot-order fault, and an
				// empty roster would read as "this org has nobody" — an announcement that
				// silently reaches no one is worse than one that refuses.
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
		},
		zip.WithOperationID(plane.IAMMailable),
		zip.WithSummary("Who this org may mail"))
}
