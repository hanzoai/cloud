// Copyright © 2026 Hanzo AI. MIT License.

package marketing

import (
	iamclient "github.com/hanzoai/cloud/clients/iam"
	model "github.com/hanzoai/iam/pkg/model"
	iamstore "github.com/hanzoai/iam/pkg/store"
)

// roster.go is the IDENTITY seam: the ONE place marketing learns who an org's
// customers are. A customer is not a CRM row — it is a user in Hanzo IAM, owned
// by its org — so the roster is READ from IAM and never copied here. There is no
// marketing-local contact table to drift, and marketing can never mutate a user.
//
// The read is IN-PROCESS. IAM is embedded in this same cloud binary
// (clients/iam mounts it and owns its orm.DB), so this reaches the canonical
// store directly through github.com/hanzoai/iam/pkg/store — no HTTP hop to
// /v1/iam from inside the process, and IAM's own model.User verbatim, never a
// local clone. Exactly how clients/platform reflects the IAM-owned Project.
//
// TENANCY. org is always principal.Org — the value SanitizeIdentity minted from
// the VALIDATED bearer owner claim, never a client header — and it is IAM's
// Owner, the tenancy key. GetMailableUsers refuses an empty org, so a missing
// principal can never widen into "every user of every organization".
//
// FAIL CLOSED. A deployment that enables "marketing" is meant to co-mount "iam".
// Until it does, iamclient.DB() is nil and every resolution reports
// errIAMUnavailable (→ 503) rather than resolving to an empty audience, because
// an announcement that silently reaches nobody is a worse failure than one that
// refuses to run.

// rosterFn reads an org's mailable IAM users. Production is the in-process IAM
// store; tests override it. It is the ONLY reader — mirroring sendFn, the only
// door out of the send gate — so a new audience kind cannot introduce a second
// path to the identity store.
var rosterFn = iamRoster

// iamRoster is the production roster read, guarded on the embedded IAM store's
// documented lifecycle: DB() is nil until IAM mounts cleanly, so callers must
// nil-guard rather than dereference.
func iamRoster(org string) ([]*model.User, error) {
	db := iamclient.DB()
	if db == nil {
		return nil, errIAMUnavailable
	}
	return iamstore.GetMailableUsers(db, org)
}

// addresses reduces a roster to the deliverable, normalized addresses to send to.
// It de-duplicates: two users sharing one mailbox receive ONE announcement, not
// two. Normalization matches the suppression gate's (normAddr), so an address
// resolved here is the same string the opt-out list is keyed by — an opted-out
// customer can never slip through on a case or whitespace difference.
func addresses(users []*model.User) []string {
	seen := make(map[string]bool, len(users))
	out := make([]string, 0, len(users))
	for _, u := range users {
		addr := normAddr(u.Email)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return out
}

// matchCohort keeps only the customers a cohort actually names. A cohort
// identifier is a warehouse distinct_id — whatever the product called identify()
// with — so it is matched against every identifier that names the same person:
// the IAM subject (the stable opaque Id), the "owner/name" subject, the bare
// username, or the email. An identifier that matches nobody is COUNTED, never
// invented into an address: a cohort of 500 that mails 3 says so.
//
// The index is org-local by construction (the roster is one org's users), so a
// collision between two of the org's own identifiers can only mis-attribute
// within that org — it can never reach another tenant.
func matchCohort(roster []*model.User, ids []string) reach {
	idx := make(map[string]*model.User, len(roster)*3)
	for _, u := range roster {
		for _, k := range []string{u.Id, u.Owner + "/" + u.Name, u.Name, u.Email} {
			if k = normAddr(k); k != "" && k != "/" {
				idx[k] = u
			}
		}
	}
	out := reach{addresses: make([]string, 0, len(ids)), cohort: ids}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		u, ok := idx[normAddr(id)]
		if !ok {
			out.unmatched++
			continue
		}
		addr := normAddr(u.Email)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out.addresses = append(out.addresses, addr)
	}
	return out
}
