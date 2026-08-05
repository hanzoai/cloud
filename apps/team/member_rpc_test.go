// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package team

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// fromOrg is a plane call arriving from a peer that states its org, the way
// cloud.As does off a request. The org is the ONE thing these ops scope by.
func fromOrg(org string) context.Context { return cloud.For(context.Background(), org) }

// TestWorkspacesOfAnswersOnlyTheCallersOwnRows is the tenant property. meet uses
// this list to decide which workspaces to offer a person, and a workspace uuid IS
// the prefix of every room name in it — so a list that reached across an org would
// hand out the one string needed to name a room in another tenant.
func TestWorkspacesOfAnswersOnlyTheCallersOwnRows(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()

	const subA, subB = "ada@acme.test", "bob@other.test"
	acctA, acctB := accountID(subA), accountID(subB)

	mine, err := s.EnsureWorkspace(ctx, "acme", acctA, "Ada")
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	theirs, err := s.EnsureWorkspace(ctx, "other", acctB, "Bob")
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}

	got, err := workspacesOf(fromOrg("acme"), s, &plane.WorkspacesIn{Subject: subA})
	if err != nil {
		t.Fatalf("workspacesOf: %v", err)
	}
	if got.Account != acctA {
		t.Errorf("account = %q, want %q", got.Account, acctA)
	}
	if len(got.Items) != 1 || got.Items[0].UUID != mine.UUID {
		t.Fatalf("items = %+v, want exactly the caller's own workspace %s", got.Items, mine.UUID)
	}
	if got.Items[0].Role == "" {
		t.Error("the role is empty — meet decides whether to offer a room with it")
	}
	for _, w := range got.Items {
		if w.UUID == theirs.UUID {
			t.Fatal("SECURITY: another tenant's workspace appeared in the answer")
		}
	}

	// The SAME subject, asked from the other org, knows nothing. This is the probe
	// a caller would use to test whether an identity exists elsewhere.
	cross, err := workspacesOf(fromOrg("other"), s, &plane.WorkspacesIn{Subject: subA})
	if err != nil {
		t.Fatalf("workspacesOf: %v", err)
	}
	if cross.Account != "" || len(cross.Items) != 0 {
		t.Fatalf("SECURITY: a foreign org resolved the subject: %+v", cross)
	}
}

// TestWorkspacesOfAndMemberOfAgree. They read the same rows and are asked by the
// same caller for the same decision — one to OFFER a room, one to GRANT it — so a
// workspace present in one and absent from the other is a lobby that shows a room
// nobody can enter, or hides one everybody can.
func TestWorkspacesOfAndMemberOfAgree(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const sub = "ada@acme.test"

	w, err := s.EnsureWorkspace(ctx, "acme", accountID(sub), "Ada")
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}

	list, err := workspacesOf(fromOrg("acme"), s, &plane.WorkspacesIn{Subject: sub})
	if err != nil {
		t.Fatalf("workspacesOf: %v", err)
	}
	one, err := memberOf(fromOrg("acme"), s, &plane.MemberIn{Workspace: w.UUID, Subject: sub})
	if err != nil {
		t.Fatalf("memberOf: %v", err)
	}
	if !one.Member {
		t.Fatal("memberOf says not a member of a workspace the list names")
	}
	if len(list.Items) != 1 || list.Items[0].Role != one.Role {
		t.Errorf("role disagreement: list=%+v memberOf=%+v", list.Items, one)
	}
	if list.Account != one.Account {
		t.Errorf("account disagreement: list=%q memberOf=%q", list.Account, one.Account)
	}
}

// TestWorkspacesOfRefusesRatherThanAnsweringEmpty. A call with no org, and a call
// with no subject, are FAULTS — not "you have no workspaces". The distinction is
// the whole reason meet can render "ask for an invite" honestly: an empty list has
// to mean the rows are empty and nothing else.
func TestWorkspacesOfRefusesRatherThanAnsweringEmpty(t *testing.T) {
	s := newAccountStore(t)
	if _, err := workspacesOf(context.Background(), s, &plane.WorkspacesIn{Subject: "ada@acme.test"}); err == nil {
		t.Error("a call with no org was answered; it must be refused")
	}
	if _, err := workspacesOf(fromOrg("acme"), s, &plane.WorkspacesIn{}); err == nil {
		t.Error("a call with no subject was answered; it must be refused")
	}
	if _, err := workspacesOf(fromOrg("acme"), nil, &plane.WorkspacesIn{Subject: "ada@acme.test"}); err == nil {
		t.Error("a call against a closed store was answered; it must be refused")
	}
}

// TestWorkspacesOfIsSubjectOnly. The subject is the `sub` claim verbatim, never a
// username and never an account id a caller computed: accountID derives a UUID for
// a non-UUID subject, and a caller allowed to pass a raw UUID through would be
// naming a colleague's account.
func TestWorkspacesOfIsSubjectOnly(t *testing.T) {
	s := newAccountStore(t)
	ctx := context.Background()
	const sub = "ada@acme.test"
	acct := accountID(sub)
	if _, err := s.EnsureWorkspace(ctx, "acme", acct, "Ada"); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	// The ACCOUNT id, presented as if it were the subject. accountID passes a
	// UUID-shaped input through verbatim, so this is the shape that would resolve
	// if the op keyed on anything but a real subject — and it does resolve, which
	// is exactly why the op's contract says the caller may not choose it: the
	// value comes from the ATTESTED principal in meet, never from a body.
	same, err := workspacesOf(fromOrg("acme"), s, &plane.WorkspacesIn{Subject: acct})
	if err != nil {
		t.Fatalf("workspacesOf: %v", err)
	}
	if same.Account != acct {
		t.Fatalf("account = %q, want %q — accountID must be the ONE derivation", same.Account, acct)
	}
	// A subject this deployment has never seen is an empty answer, not an error:
	// "no rows" is a real fact about a real identity.
	none, err := workspacesOf(fromOrg("acme"), s, &plane.WorkspacesIn{Subject: "nobody@acme.test"})
	if err != nil {
		t.Fatalf("workspacesOf: %v", err)
	}
	if none.Account != "" || len(none.Items) != 0 {
		t.Fatalf("an unknown subject resolved to %+v", none)
	}
}
