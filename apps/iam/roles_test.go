// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/client"
	iamschema "github.com/hanzoai/iam/pkg/schema"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

// The roles op is asked with the token's subject, a UUID, and answers from grants
// that name people by "<owner>/<name>". These tests seed the store the way IAM
// writes it and ask the op as a caller would, so a grant keyed one way and a
// caller named the other is a failure here rather than a 403 in production.

const (
	joshSub   = "8e4e1ba8-0000-4000-8000-000000000001"
	zSub      = "2d4d67ab-0000-4000-8000-000000000002"
	straySub  = "3c3c3c3c-0000-4000-8000-000000000003"
	barredSub = "4d4d4d4d-0000-4000-8000-000000000004"
	crewSub   = "5e5e5e5e-0000-4000-8000-000000000005"
)

// rolesStore opens an identity store, publishes it as this process's, and seeds
// one agency owner whose home org is his own and who administers a client's org
// by membership.
func rolesStore(t *testing.T) orm.DB {
	t.Helper()
	db, err := iamstore.Open("sqlite", filepath.Join(t.TempDir(), "iam.db"), "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	prev := embeddedDB
	embeddedDB = db
	t.Cleanup(func() { embeddedDB = prev; db.Close() })

	seatUser(t, db, joshSub, "agency", "josh", false, false)
	seatUser(t, db, zSub, "hanzo", "z", true, false)
	seatUser(t, db, straySub, "elsewhere", "stray", true, false)
	seatUser(t, db, barredSub, "agency", "barred", false, true)
	seatUser(t, db, crewSub, "agency", "crew", false, false)

	seatGrant(t, db, "agency/josh", "client", "", iamstore.RoleAdmin)
	seatGrant(t, db, "agency/barred", "client", "", iamstore.RoleAdmin)
	seatGrant(t, db, "agency/crew", "client", "", iamstore.RoleMember)
	// A grant scoped to one workspace, keyed by the subject the way a workspace
	// write records it. It admits the workspace, not the org.
	seatGrant(t, db, straySub, "client", "w1", iamstore.RoleOwner)

	seatRole(t, db, "client", "officer", "agency/crew")
	seatRole(t, db, "client", "owner", "elsewhere/stray")
	seatRole(t, db, "hanzo", "owner", "hanzo/z")
	return db
}

func seatUser(t *testing.T, db orm.DB, sub, owner, name string, admin, forbidden bool) {
	t.Helper()
	u := orm.New[iamschema.User](db)
	u.Id, u.Owner, u.Name = sub, owner, name
	u.IsAdmin, u.IsForbidden = admin, forbidden
	u.SetId(owner + "/" + name)
	if err := u.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed user %s/%s: %v", owner, name, err)
	}
}

func seatGrant(t *testing.T, db orm.DB, who, org, workspace, role string) {
	t.Helper()
	if _, err := iamstore.EnsureMembershipIn(context.Background(), db, who, org, workspace, "", role); err != nil {
		t.Fatalf("grant %s in %s: %v", who, org, err)
	}
}

func seatRole(t *testing.T, db orm.DB, org, name string, users ...string) {
	t.Helper()
	r := orm.New[iamschema.Role](db)
	r.Owner, r.Name, r.Users, r.IsEnabled = org, name, users, true
	r.SetId(org + "/" + name)
	if err := r.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed role %s/%s: %v", org, name, err)
	}
}

// askRoles calls the op as the plane delivers it: the caller's subject and the org
// they are acting in.
func askRoles(t *testing.T, sub, org string) []string {
	t.Helper()
	return askAs(t, zip.Caller{User: sub, Org: org})
}

func askAs(t *testing.T, who zip.Caller) []string {
	t.Helper()
	return askFull(t, who).Roles
}

func askFull(t *testing.T, who zip.Caller) *client.Roles {
	t.Helper()
	got, err := roles(zip.WithCaller(context.Background(), who), nil)
	if err != nil {
		t.Fatalf("roles(%s in %s): %v", who.User, who.Org, err)
	}
	return got
}

// ADMIN IS THE GRANT, NOT THE NAME. An owner or admin membership, or an admin at
// home, administers the org; a role a tenant named "System Manager" puts the name
// in the list and does not.
func TestRolesStateTheAdminGrantApartFromTheNames(t *testing.T) {
	rolesStore(t)
	for _, c := range []struct {
		sub, org string
		admin    bool
	}{
		{joshSub, "client", true}, // admin membership
		{zSub, "hanzo", true},     // admin at home
		{crewSub, "client", false},
		{straySub, "client", false},
		{barredSub, "client", false},
	} {
		if got := askFull(t, zip.Caller{User: c.sub, Org: c.org}); got.Admin != c.admin {
			t.Errorf("%s in %s: admin=%v, want %v (roles %v)", c.sub, c.org, got.Admin, c.admin, got.Roles)
		}
	}
	seatRole(t, DB(), "client", "System Manager", "agency/crew")
	got := askFull(t, zip.Caller{User: crewSub, Org: "client"})
	if !slices.Contains(got.Roles, "System Manager") || got.Admin {
		t.Fatalf("a member on a role named System Manager: roles %v admin=%v, want the name and not the grant", got.Roles, got.Admin)
	}
}

func TestRolesAdmitAMemberFromAnotherHomeOrg(t *testing.T) {
	rolesStore(t)
	if got := askRoles(t, joshSub, "client"); !slices.Contains(got, "System Manager") {
		t.Fatalf("an admin member of client whose home is agency holds %v, want System Manager", got)
	}
}

func TestRolesReadRostersByTheResolvedUser(t *testing.T) {
	rolesStore(t)
	got := askRoles(t, crewSub, "client")
	if !slices.Equal(got, []string{"officer"}) {
		t.Fatalf("a member named on the officer roster holds %v, want [officer]", got)
	}
}

func TestRolesGiveTheHomeAdminSystemManager(t *testing.T) {
	rolesStore(t)
	got := askRoles(t, zSub, "hanzo")
	if !slices.Equal(got, []string{"System Manager", "owner"}) {
		t.Fatalf("hanzo's own admin holds %v, want [System Manager owner]", got)
	}
}

func TestRolesRefuseANonMember(t *testing.T) {
	rolesStore(t)
	// stray is an admin at home, holds a workspace grant in client, and is named
	// on client's owner roster, but no org-wide membership admits him to client.
	if got := askRoles(t, straySub, "client"); len(got) != 0 {
		t.Fatalf("a caller with no membership in client holds %v, want nothing", got)
	}
	if got := askRoles(t, joshSub, "hanzo"); len(got) != 0 {
		t.Fatalf("josh acting in an org he does not belong to holds %v, want nothing", got)
	}
}

func TestRolesRefuseAForbiddenUser(t *testing.T) {
	rolesStore(t)
	if got := askRoles(t, barredSub, "client"); len(got) != 0 {
		t.Fatalf("a forbidden user holds %v, want nothing", got)
	}
}

func TestRolesRefuseAnUnknownSubject(t *testing.T) {
	rolesStore(t)
	if got := askRoles(t, "9f9f9f9f-0000-4000-8000-000000000009", "client"); len(got) != 0 {
		t.Fatalf("a subject naming no user holds %v, want nothing", got)
	}
}

// A key names its holder differently from a token. At home it carries the bare
// username, which is their row there; a member's key carries their own
// "<home>/<name>" as its subject and acts in the org it was minted in.
func TestRolesResolveKeyHolders(t *testing.T) {
	rolesStore(t)
	if got := askAs(t, zip.Caller{User: "z", Org: "hanzo", Owner: "hanzo"}); !slices.Contains(got, "System Manager") {
		t.Fatalf("hanzo's admin presenting a home key holds %v, want System Manager", got)
	}
	if got := askAs(t, zip.Caller{User: "agency/josh", Org: "client", Owner: "client"}); !slices.Contains(got, "System Manager") {
		t.Fatalf("a member's key in client holds %v, want System Manager", got)
	}
	// A bare username outside the holder's home names nobody: the same name in
	// another org is a different person, here one holding a role there.
	seatUser(t, DB(), "6f6f6f6f-0000-4000-8000-000000000006", "client", "z", false, false)
	seatRole(t, DB(), "client", "manager", "client/z")
	if got := askAs(t, zip.Caller{User: "z", Org: "client", Owner: "hanzo"}); len(got) != 0 {
		t.Fatalf("a bare username acting away from home holds %v, want nothing", got)
	}
}

// MEMBER IS THE MEMBERSHIP SET. A person may act in their home org and in every org
// a membership row names them in — the `orgs` claim — and in none once IAM forbids
// them or the row is gone.
func TestRolesSayWhoMayActInTheOrg(t *testing.T) {
	db := rolesStore(t)
	for _, c := range []struct {
		sub, org string
		member   bool
	}{
		{joshSub, "agency", true},    // home, no membership row of its own
		{zSub, "hanzo", true},        // an admin at home
		{joshSub, "client", true},    // an org-wide grant
		{crewSub, "client", true},    // a member grant
		{straySub, "client", false},  // a workspace grant keyed by subject names no user key
		{joshSub, "hanzo", false},    // nothing there
		{barredSub, "agency", false}, // forbidden, at home
		{barredSub, "client", false}, // forbidden, with a grant
		{"9f9f9f9f-0000-4000-8000-000000000009", "client", false},
	} {
		if got := askFull(t, zip.Caller{User: c.sub, Org: c.org}); got.Member != c.member {
			t.Errorf("%s in %s: member=%v, want %v", c.sub, c.org, got.Member, c.member)
		}
	}
	// Removed from client: the row goes, and so does the standing.
	if _, err := iamstore.DeleteMembership(context.Background(), db, "agency/crew", "client"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := askFull(t, zip.Caller{User: crewSub, Org: "client"}); got.Member {
		t.Fatal("a member removed from client may still act in it")
	}
}

// flaky fails its first read of memberships, as a busy store does once.
type flaky struct {
	orm.DB
	failed *atomic.Bool
}

func (f flaky) Query(kind string) orm.Query {
	q := f.DB.Query(kind)
	if kind == orm.Kind[iamschema.Membership]() && f.failed.CompareAndSwap(false, true) {
		return flakyQuery{q}
	}
	return q
}

type flakyQuery struct{ orm.Query }

func (q flakyQuery) Filter(s string, v any) orm.Query { return flakyQuery{q.Query.Filter(s, v)} }
func (q flakyQuery) Order(s string) orm.Query         { return flakyQuery{q.Query.Order(s)} }
func (q flakyQuery) GetAll(context.Context, any) ([]orm.Key, error) {
	return nil, errors.New("database is locked")
}

// A read of the membership set that fails is an error, never an answer: an
// invited member must not read as a non-member because the store was busy, which
// is what turned their automations off.
func TestRolesSayNothingWhenTheSetCannotBeRead(t *testing.T) {
	db := rolesStore(t)
	embeddedDB = flaky{DB: db, failed: new(atomic.Bool)}
	if got, err := roles(zip.WithCaller(context.Background(), zip.Caller{User: joshSub, Org: "client"}), nil); err == nil {
		t.Fatalf("a failed read answered %+v, want an error", got)
	}
	// Once the store answers, so does the op.
	if got := askFull(t, zip.Caller{User: joshSub, Org: "client"}); !got.Member {
		t.Fatalf("an invited member after the store recovered: %+v", got)
	}
}
