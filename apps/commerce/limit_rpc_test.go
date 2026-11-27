// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/principal"
)

// sandboxes is the sandbox.running value of a sheet, and where it came from.
func sandboxes(t *testing.T, s *client.LimitSheet) (int64, string) {
	t.Helper()
	for _, v := range s.Values {
		if v.Key == client.LimitSandboxRunning {
			return v.Value, v.Source
		}
	}
	t.Fatalf("the sheet carries no %s: %+v", client.LimitSandboxRunning, s)
	return 0, ""
}

// staff is a SuperAdmin asking over the plane, as the admin ops delegate.
func staff() context.Context { return caller(authz.AdminOrg, true) }

// A limit resolves org, then plan, then default, on the REAL embed with a real plan
// recorded: the org's row beats its plan's, the plan's beats the default, and
// removing a row falls back one level at a time.
func TestALimitResolvesOrgThenPlanThenDefault(t *testing.T) {
	bootRecord(t)
	const org = "limit-acme"
	if _, err := planeSubscriptionRecord(caller(org, true), paidFor(t, org)); err != nil {
		t.Fatalf("record go: %v", err)
	}
	read := func() *client.LimitSheet {
		t.Helper()
		s, err := planeLimits(caller(org, false), &client.LimitsIn{})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return s
	}

	s := read()
	if n, src := sandboxes(t, s); n != 100 || src != client.LimitDefault || s.Plan != "go" || s.Org != org {
		t.Fatalf("untouched: %d from %s on plan %q org %q; want the default 100 on go", n, src, s.Plan, s.Org)
	}

	set := func(scope string, value int64) {
		t.Helper()
		row, err := planeLimitSet(staff(), &client.LimitSet{Key: client.LimitSandboxRunning, Scope: scope, Value: value, Reason: "agency grew"})
		if err != nil || row.Value != value || row.Scope != scope || row.UpdatedAt == 0 {
			t.Fatalf("set %s %d: %+v, %v", scope, value, row, err)
		}
	}
	set("plan:go", 300)
	if n, src := sandboxes(t, read()); n != 300 || src != client.LimitPlan {
		t.Fatalf("plan row: %d from %s; want 300 from the plan", n, src)
	}
	set("org:"+org, 500)
	s = read()
	if n, src := sandboxes(t, s); n != 500 || src != client.LimitOrg {
		t.Fatalf("org row: %d from %s; want 500 from the org", n, src)
	}
	if v := s.Values[0]; v.Org == nil || v.Plan == nil || v.Plan.Value != 300 || v.Org.Reason != "agency grew" {
		t.Fatalf("the levels are not both carried: %+v", v)
	}
	set("org:"+org, 700)
	if n, _ := sandboxes(t, read()); n != 700 {
		t.Fatalf("a restated org row reads %d; want 700", n)
	}

	if row, err := planeLimitDrop(staff(), &client.LimitDrop{Key: client.LimitSandboxRunning, Scope: "org:" + org}); err != nil || row.Value != 700 {
		t.Fatalf("drop org: %+v, %v", row, err)
	}
	if n, src := sandboxes(t, read()); n != 300 || src != client.LimitPlan {
		t.Fatalf("after the org's row went: %d from %s; want the plan's 300", n, src)
	}
	if _, err := planeLimitDrop(staff(), &client.LimitDrop{Key: client.LimitSandboxRunning, Scope: "plan:go"}); err != nil {
		t.Fatalf("drop plan: %v", err)
	}
	if n, src := sandboxes(t, read()); n != 100 || src != client.LimitDefault {
		t.Fatalf("after both went: %d from %s; want the default", n, src)
	}
	if _, err := planeLimitDrop(staff(), &client.LimitDrop{Key: client.LimitSandboxRunning, Scope: "plan:go"}); statusOf(err) != http.StatusNotFound {
		t.Fatalf("dropping a row that is not there: %v, want 404", err)
	}

	// Another org on the same plan reads the plan's row and never this org's.
	set("org:"+org, 900)
	set("plan:go", 250)
	const other = "limit-globex"
	if _, err := planeSubscriptionRecord(caller(other, true), paidFor(t, other)); err != nil {
		t.Fatalf("record go: %v", err)
	}
	o, err := planeLimits(caller(other, false), &client.LimitsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if n, src := sandboxes(t, o); n != 250 || src != client.LimitPlan {
		t.Fatalf("another org on dev: %d from %s; want the plan's 250", n, src)
	}
}

// A person in the signup org is held to their own plan: no org row reaches them,
// and the signup org is no scope to set one on.
func TestTheSignupOrgIsHeldPerPerson(t *testing.T) {
	bootRecord(t)
	person := account.Person(account.SignupOrg, "limit-sam").Subject()
	s, err := planeLimits(caller(account.SignupOrg, false), &client.LimitsIn{Subject: person})
	if err != nil {
		t.Fatal(err)
	}
	if n, src := sandboxes(t, s); n != 100 || src != client.LimitDefault || s.Plan != "" {
		t.Fatalf("a person on no plan: %d from %s plan %q; want the default", n, src, s.Plan)
	}
	if _, err := planeLimitSet(staff(), &client.LimitSet{Key: client.LimitSandboxRunning, Scope: "org:" + account.SignupOrg,
		Value: 5, Reason: "every stranger at once"}); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("a row on the signup org: %v, want 400", err)
	}
}

// Only a SuperAdmin writes, and only what the registry names, in range, with a
// reason; an org's own caller — its admin included — is refused before anything is
// read, and a plan's limits are not a tenant's to read.
func TestALimitWriteIsRefusedUnlessItIsAStaffDecision(t *testing.T) {
	bootRecord(t)
	ok := client.LimitSet{Key: client.LimitSandboxRunning, Scope: "org:limit-initech", Value: 10, Reason: "pilot"}
	orgAdmin := zip.WithCaller(context.Background(), zip.Caller{Org: "limit-initech", User: "ann", OrgAdmin: true})
	for name, ctx := range map[string]context.Context{"member": caller("limit-initech", false), "org admin": orgAdmin} {
		if _, err := planeLimitSet(ctx, &ok); statusOf(err) != http.StatusForbidden {
			t.Fatalf("%s set: %v, want 403", name, err)
		}
		if _, err := planeLimitDrop(ctx, &client.LimitDrop{Key: ok.Key, Scope: ok.Scope}); statusOf(err) != http.StatusForbidden {
			t.Fatalf("%s drop: %v, want 403", name, err)
		}
		if _, err := planeLimits(ctx, &client.LimitsIn{Plan: "go"}); statusOf(err) != http.StatusForbidden {
			t.Fatalf("%s read a plan's limits: %v, want 403", name, err)
		}
	}
	for name, in := range map[string]client.LimitSet{
		"unknown key":   {Key: "sandbox.disk", Scope: ok.Scope, Value: 1, Reason: "x"},
		"over the max":  {Key: ok.Key, Scope: ok.Scope, Value: 1_000_001, Reason: "x"},
		"below -1":      {Key: ok.Key, Scope: ok.Scope, Value: -2, Reason: "x"},
		"no reason":     {Key: ok.Key, Scope: ok.Scope, Value: 10, Reason: "  "},
		"no scope kind": {Key: ok.Key, Scope: "limit-initech", Value: 10, Reason: "x"},
		"user scope":    {Key: ok.Key, Scope: "user:ann", Value: 10, Reason: "x"},
		"empty name":    {Key: ok.Key, Scope: "plan:", Value: 10, Reason: "x"},
	} {
		if _, err := planeLimitSet(staff(), &in); statusOf(err) != http.StatusBadRequest {
			t.Fatalf("%s: %v, want 400", name, err)
		}
	}
	if _, err := planeLimitSet(staff(), &client.LimitSet{Key: ok.Key, Scope: ok.Scope, Value: -1, Reason: "unbounded by contract"}); err != nil {
		t.Fatalf("-1 on a limit that allows no bound: %v", err)
	}
	plan, err := planeLimits(caller(principal.Reserved, false), &client.LimitsIn{Plan: "GO"})
	if err != nil || plan.Plan != "go" || plan.Org != "" {
		t.Fatalf("the platform reads a plan's: %+v, %v", plan, err)
	}
}
