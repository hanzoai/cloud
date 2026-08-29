package team

// The space membership read, published on the internal plane.
//
// The spaces/members tables have one writer and it is this process. A peer
// that must decide something about a space — meet, deciding whether a caller
// may join a room — used to read that decision off a signed space claim,
// which is the second bearer authority the estate is retiring. Once the caller
// arrives with an IAM identity and no space claim, the rows are the only
// place the answer exists, and they live here.
//
// The projection is deliberately narrow: whether there is a row, and the role on
// it. A peer deciding a join needs exactly that; handing over the member record
// would put a space's roster on the wire for one boolean.

import (
	"context"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
)

// exposeMember publishes the membership read. Mount calls it.
func exposeMember(accounts *accountStore) {
	zip.Post[plane.MemberIn, plane.Member](cloud.Plane(), "/team/member",
		func(ctx context.Context, in *plane.MemberIn) (*plane.Member, error) {
			return memberOf(ctx, accounts, in)
		},
		zip.WithOperationID(plane.TeamMember),
		zip.WithSummary("This person's role in that space"))

	zip.Post[plane.SpacesIn, plane.Spaces](cloud.Plane(), "/team/spaces",
		func(ctx context.Context, in *plane.SpacesIn) (*plane.Spaces, error) {
			return spacesOf(ctx, accounts, in)
		},
		zip.WithOperationID(plane.TeamSpaces),
		zip.WithSummary("The spaces this person is in"))
}

// spacesOf answers which spaces the caller holds a member row in, within
// the CALLER'S OWN org, and with what role on each.
//
// It is memberOf asked the other way round, over exactly the same rows and the
// same three refusals — org off the call, store open, subject present — so the
// two can never disagree about who is a member of what. What it adds is the role
// per row, which the peer needs to offer only the rooms it would also admit.
//
// The subject → account resolution is the STORE's (AccountForSubject), the same
// one the request lane and memberOf use: one derivation of one address. An
// identity this deployment has never seen resolves to no account and the answer
// is an empty list — the honest "no rows", not an error a lobby would have to
// render as a fault.
//
// SpacesOf is already owner_org-scoped on its join, so a subject known in
// another tenant returns nothing here rather than that tenant's spaces.
func spacesOf(ctx context.Context, accounts *accountStore, in *plane.SpacesIn) (*plane.Spaces, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("team: no org on the call")
	}
	if accounts == nil {
		return nil, zip.Errorf(503, "team: account store not open in the process that owns it")
	}
	if in.Subject == "" {
		return nil, zip.ErrBadRequest("team: subject is required")
	}
	account, ok := accounts.AccountForSubject(ctx, org, in.Subject)
	if !ok {
		return &plane.Spaces{}, nil
	}
	spaces, err := accounts.SpacesOf(ctx, org, account)
	if err != nil {
		return nil, zip.Errorf(500, "team: spaces of: %v", err)
	}
	out := &plane.Spaces{Account: account, Items: make([]plane.Space, 0, len(spaces))}
	for _, w := range spaces {
		// The role is READ per row rather than assumed from the join, because the
		// join proves a row exists and the peer decides on what the row SAYS. A row
		// that vanishes between the two reads is simply not offered.
		role, ok := accounts.Membership(ctx, org, w.UUID, account)
		if !ok {
			continue
		}
		out.Items = append(out.Items, plane.Space{UUID: w.UUID, Name: w.Name, Role: role})
		if out.Name == "" {
			out.Name = accounts.MemberName(ctx, org, w.UUID, account)
		}
	}
	return out, nil
}

// memberOf answers whether the named account holds a member row in the named
// space of the CALLER'S OWN org, and with what role.
//
// The org is taken from the CALL rather than from the argument. That is not a
// guarantee about the peer — a peer states its own caller (cloud.For / cloud.As),
// so a compromised or buggy one can name any org, and this op is only ever as
// tenant-safe as the process asking. It is the internal plane: peers are trusted,
// the socket is a UDS on the same node, and there is no authority here a peer could
// not also get by asking for the org it wanted. What taking it off the call DOES
// buy is that the org travels with the identity the asking process authenticated,
// so a peer cannot answer one caller's question with another caller's tenant by
// mistake — the failure mode that a space-plus-org argument invites.
//
// A call carrying no org is refused, not answered with "not a member": a refusal is
// a fault the operator can see, while a false negative is a join that silently
// stops working.
//
// It fails closed on a store that is not open: this process owns the store, so a
// nil handle is a boot-order fault, and "not a member" would read as a real
// answer about a space nobody could check.
func memberOf(ctx context.Context, accounts *accountStore, in *plane.MemberIn) (*plane.Member, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrUnauthorized("team: no org on the call")
	}
	if accounts == nil {
		return nil, zip.Errorf(503, "team: account store not open in the process that owns it")
	}
	if in.Space == "" || in.Subject == "" {
		return nil, zip.ErrBadRequest("team: space and subject are required")
	}
	// The subject → account resolution is THIS package's, and it is the STORE's:
	// AccountForSubject is the same function the request lane uses, so a peer and a
	// browser resolve one identity to one account or the peer is told there is none.
	// A peer that derived its own would be a second derivation of one address — and
	// it would have to reproduce the subject-only rule that keeps a token with no
	// `sub` from resolving to whoever its username names.
	account, ok := accounts.AccountForSubject(ctx, org, in.Subject)
	if !ok {
		return &plane.Member{}, nil
	}
	w, err := accounts.SpaceByUUID(ctx, org, in.Space)
	if err != nil {
		// Not this tenant's space, or none at all — the same answer either way,
		// so a probe learns nothing about what exists in another org.
		return &plane.Member{}, nil
	}
	role, ok := accounts.Membership(ctx, org, w.UUID, account)
	if !ok {
		return &plane.Member{}, nil
	}
	return &plane.Member{Member: true, Role: role, Account: account}, nil
}
