// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hanzoai/cloud"
	iamstore "github.com/hanzoai/iam/pkg/store"
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
// Sending only what the question needs is the difference between an RPC and a
// database connection.
const rosterMethod = "iam.mailable"

// Recipient is one mailable person: enough to address them, and enough to match
// them against a warehouse cohort (which may name them by opaque id, by
// "owner/name", by bare username, or by email).
type Recipient struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// exposeRoster publishes the roster read. Mount calls it.
func exposeRoster() {
	cloud.Expose(rosterMethod, func(ctx context.Context, who cloud.Ident, _ []byte) ([]byte, error) {
		// The ORG comes from the capability and nothing else. It is the tenancy key
		// for the whole identity store, so a caller that could name it in a payload
		// could enumerate another tenant's people.
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("roster: no org on the capability")
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
		out := make([]Recipient, 0, len(users))
		for _, u := range users {
			if u == nil {
				continue
			}
			out = append(out, Recipient{ID: u.Id, Owner: u.Owner, Name: u.Name, Email: u.Email})
		}
		return json.Marshal(out)
	})
}
