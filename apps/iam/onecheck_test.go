// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"slices"
	"testing"

	"github.com/zap-proto/zip"
)

// HIP-0527 §2, the one check, as the roles op answers it: a role in the org, or a
// SuperAdmin whose token IAM re-minted into the org by POST /v1/iam/assume. The
// assume writes no membership row, so a store-only answer refused every support
// session.

const opSub = "6f6f6f6f-0000-4000-8000-000000000006"

func TestRolesAdmitASuperAdminAssumedIntoTheOrg(t *testing.T) {
	db := rolesStore(t)
	seatUser(t, db, opSub, "admin", "op", false, false)
	// The boundary renders an assumed token as Admin (the SuperAdmin rule) and
	// OrgAdmin (the role its signed `orgs` names for the acting org).
	got := askFull(t, zip.Caller{User: opSub, Org: "client", Admin: true, OrgAdmin: true})
	if !slices.Equal(got.Roles, []string{"System Manager"}) || !got.Admin {
		t.Fatalf("a SuperAdmin assumed into client holds %v admin=%v, want [System Manager] and the grant", got.Roles, got.Admin)
	}
}

func TestRolesRefuseASuperAdminWhoDidNotAssume(t *testing.T) {
	db := rolesStore(t)
	seatUser(t, db, opSub, "admin", "op", false, false)
	// Naming the org in a header moves the acting org and leaves the token's
	// `orgs` without it, so OrgAdmin is false.
	if got := askFull(t, zip.Caller{User: opSub, Org: "client", Admin: true}); len(got.Roles) != 0 || got.Admin {
		t.Fatalf("a SuperAdmin with no assume holds %v admin=%v in client, want nothing", got.Roles, got.Admin)
	}
}

func TestRolesGiveNoSupportToARepointedCall(t *testing.T) {
	db := rolesStore(t)
	seatUser(t, db, opSub, "admin", "op", false, false)
	who := zip.Caller{User: opSub, Org: "client", Admin: true, OrgAdmin: true, ActedBy: "someone"}
	if got := askFull(t, who); len(got.Roles) != 0 || got.Admin {
		t.Fatalf("a call re-pointed into client holds %v admin=%v, want nothing", got.Roles, got.Admin)
	}
}

func TestRolesKeepAnOrgAdminInTheirOwnOrg(t *testing.T) {
	rolesStore(t)
	// josh administers client; acting in hanzo, where he holds nothing, the bit the
	// boundary renders for hanzo is false and so is his standing.
	if got := askFull(t, zip.Caller{User: joshSub, Org: "hanzo"}); len(got.Roles) != 0 || got.Admin {
		t.Fatalf("client's admin acting in hanzo holds %v admin=%v, want nothing", got.Roles, got.Admin)
	}
}

// A SuperAdmin whose account is disabled holds nothing, whatever the token they
// still carry says.
func TestRolesRefuseAForbiddenSuperAdminCarryingAnAssume(t *testing.T) {
	db := rolesStore(t)
	seatUser(t, db, opSub, "admin", "op", false, true)
	if got := askFull(t, zip.Caller{User: opSub, Org: "client", Admin: true, OrgAdmin: true}); len(got.Roles) != 0 || got.Admin {
		t.Fatalf("a forbidden SuperAdmin's assumed token holds %v admin=%v in client, want nothing", got.Roles, got.Admin)
	}
}

// The bits are the SuperAdmin rule and the token's role in the org; the account
// row must be a SuperAdmin's too.
func TestRolesRefuseSupportBitsOnAnAccountThatIsNoSuperAdmin(t *testing.T) {
	rolesStore(t)
	if got := askFull(t, zip.Caller{User: crewSub, Org: "hanzo", Admin: true, OrgAdmin: true}); len(got.Roles) != 0 || got.Admin {
		t.Fatalf("support bits on an account homed in agency hold %v admin=%v in hanzo, want nothing", got.Roles, got.Admin)
	}
}
